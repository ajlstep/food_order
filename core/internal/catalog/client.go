// Package catalog is the HTTP client core uses to ask catalog about dishes.
// Core never opens catalog.db; it asks here and keeps its own snapshot.
//
// The answers are read tolerantly (catalog is not written yet): a list may
// come as {"items": [...]} or as a bare array.
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"food-order/core/internal/applog"
	"food-order/core/internal/contracts"
)

// ErrNotFound is returned for a 404 answer.
var ErrNotFound = errors.New("catalog: not found")

// Client calls catalog.
type Client struct {
	base string
	http *http.Client
	log  *slog.Logger
}

// New makes a client. baseURL is like http://catalog:9082.
func New(baseURL string, hc *http.Client) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), http: hc, log: slog.New(slog.DiscardHandler)}
}

// WithLog makes the client log every call (debug) and every failure (warn).
func (c *Client) WithLog(l *slog.Logger) *Client {
	c.log = l
	return c
}

// Menu returns the dishes of a day (GET /v1/menu?date=).
func (c *Client) Menu(ctx context.Context, date string) ([]contracts.CatalogDish, error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/v1/menu?date="+url.QueryEscape(date), nil, &raw); err != nil {
		return nil, err
	}
	return decodeDishes(raw)
}

// Dishes returns dishes by id, with the values of the menu of date
// (GET /v1/dishes?ids=&date=).
func (c *Client) Dishes(ctx context.Context, ids []int64, date string) ([]contracts.CatalogDish, error) {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	q := url.Values{"ids": {strings.Join(parts, ",")}}
	if date != "" {
		q.Set("date", date)
	}
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/v1/dishes?"+q.Encode(), nil, &raw); err != nil {
		return nil, err
	}
	return decodeDishes(raw)
}

// Similar returns how much each candidate looks like the liked dishes (0..1).
func (c *Client) Similar(ctx context.Context, liked, candidates []int64) (map[int64]float64, error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodPost, "/v1/similar", contracts.SimilarRequest{LikedIDs: liked, CandidateIDs: candidates}, &raw); err != nil {
		return nil, err
	}
	var obj struct {
		Scores json.RawMessage `json:"scores"`
	}
	list := raw
	if json.Unmarshal(raw, &obj) == nil && len(obj.Scores) > 0 {
		list = obj.Scores
	}
	out := map[int64]float64{}
	var scores []contracts.SimilarScore
	if err := json.Unmarshal(list, &scores); err == nil {
		for _, s := range scores {
			out[s.ID] = clamp01(s.Score)
		}
		return out, nil
	}
	var byID map[string]float64 // also accept {"12": 0.8}
	if err := json.Unmarshal(list, &byID); err != nil {
		return nil, fmt.Errorf("catalog: /v1/similar: unexpected answer")
	}
	for k, v := range byID {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil {
			out[id] = clamp01(v)
		}
	}
	return out, nil
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

func decodeDishes(raw json.RawMessage) ([]contracts.CatalogDish, error) {
	var obj struct {
		Items  *[]contracts.CatalogDish `json:"items"`
		Dishes *[]contracts.CatalogDish `json:"dishes"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		switch {
		case obj.Items != nil:
			return *obj.Items, nil
		case obj.Dishes != nil:
			return *obj.Dishes, nil
		}
	}
	var list []contracts.CatalogDish
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("catalog: unexpected list: %w", err)
	}
	return list, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (err error) {
	start := time.Now()
	status := 0
	size := 0
	defer func() {
		args := []any{"method", method, "url", c.base + path, "status", status, "bytes", size, "ms", applog.Since(start)}
		if err != nil && !errors.Is(err, ErrNotFound) {
			c.log.WarnContext(ctx, "catalog call failed", append(args, "err", err)...)
			return
		}
		c.log.DebugContext(ctx, "catalog call", append(args, "request", in)...)
	}()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if id := requestID(ctx); id != "" {
		req.Header.Set("X-Request-ID", id) // catalog logs the same id
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	size = len(raw)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var eb contracts.ErrorBody
		if json.Unmarshal(raw, &eb) == nil && eb.Error.Code != "" {
			return fmt.Errorf("catalog: %s %s: http %d: %s: %s", method, path, resp.StatusCode, eb.Error.Code, eb.Error.Message)
		}
		return fmt.Errorf("catalog: %s %s: http %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("catalog: %s %s: bad json: %w", method, path, err)
	}
	return nil
}

// requestID is the req_id of the current request, if any.
func requestID(ctx context.Context) string {
	for _, a := range applog.Fields(ctx) {
		if a.Key == "req_id" {
			return a.Value.String()
		}
	}
	return ""
}
