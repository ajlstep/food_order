// Package services holds the HTTP clients for core and catalog.
// The bot does not keep business data: every action goes to core.
package services

import (
	"time"
	"log/slog"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"food-order/bot/internal/applog"
	"food-order/bot/internal/contracts"
)

// ErrNotFound is returned when the service answers 404.
var ErrNotFound = errors.New("not found")

// APIError is a non-2xx answer with the common error body.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("http %d: %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("http %d: %s", e.Status, e.Message)
}

// Is makes errors.Is(err, ErrNotFound) work for a 404 APIError.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

type client struct {
	base string
	http *http.Client
	name string // core / catalog, for the logs
	log  *slog.Logger
}

// do makes one JSON call. userID > 0 adds the X-User-ID header.
// Every call is logged at debug (url, status, time, body); a failure at warn.
func (c client) do(ctx context.Context, userID int64, method, path string, in, out any) (err error) {
	start := time.Now()
	status, size := 0, 0
	var respBody []byte
	defer func() {
		lg := c.log
		if lg == nil {
			lg = slog.Default()
		}
		args := []any{"service_called", c.name, "method", method, "url", c.base + path, "status", status, "bytes", size, "ms", applog.Since(start)}
		if err != nil {
			lg.WarnContext(ctx, c.name+" call failed", append(args, "err", err, "request", in)...)
			return
		}
		lg.DebugContext(ctx, c.name+" call", append(args, "request", in, "response", bodyForLog(respBody))...)
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
	for _, a := range applog.Fields(ctx) {
		if a.Key == "req_id" { // the other service logs the same id
			req.Header.Set("X-Request-ID", a.Value.String())
		}
	}
	if userID > 0 {
		req.Header.Set(contracts.HeaderUserID, strconv.FormatInt(userID, 10))
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	size, respBody = len(raw), raw
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Status: resp.StatusCode}
		var eb contracts.ErrorBody
		if json.Unmarshal(raw, &eb) == nil && eb.Error.Code != "" {
			apiErr.Code, apiErr.Message = eb.Error.Code, eb.Error.Message
		} else {
			apiErr.Message = strings.TrimSpace(string(raw))
		}
		return apiErr
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: bad json: %w", method, path, err)
	}
	return nil
}

// Core is the client of the core service. Every method acts for one user
// (users.id from the bot database), sent as the X-User-ID header.
type Core struct{ c client }

// NewCore makes a core client. baseURL is like http://core:9081.
func NewCore(baseURL string, hc *http.Client) *Core {
	return &Core{client{base: strings.TrimRight(baseURL, "/"), http: hc, name: "core"}}
}

// WithLog sets the logger of the calls.
func (c *Core) WithLog(l *slog.Logger) *Core {
	c.c.log = l
	return c
}

// RequestRecommendation asks for the recommendation of a day (POST, 202).
// The result arrives later as food.recommendation.ready.
func (c *Core) RequestRecommendation(ctx context.Context, userID int64, date string) error {
	return c.c.do(ctx, userID, http.MethodPost, "/v1/recommendations", contracts.CreateRecommendationRequest{Date: date}, nil)
}

// Recommendation returns the last saved recommendation of a day.
func (c *Core) Recommendation(ctx context.Context, userID int64, date string) (contracts.Recommendation, error) {
	var r contracts.Recommendation
	err := c.c.do(ctx, userID, http.MethodGet, "/v1/recommendations?date="+url.QueryEscape(date), nil, &r)
	return r, err
}

// CreateOrder saves the user's choice.
func (c *Core) CreateOrder(ctx context.Context, userID int64, o contracts.CreateOrderRequest) (contracts.Order, error) {
	var out contracts.Order
	err := c.c.do(ctx, userID, http.MethodPost, "/v1/orders", o, &out)
	return out, err
}

// Rate saves the rating (1..5) and the comment of an order.
func (c *Core) Rate(ctx context.Context, userID, orderID int64, r contracts.RatingRequest) error {
	return c.c.do(ctx, userID, http.MethodPost, "/v1/orders/"+strconv.FormatInt(orderID, 10)+"/rating", r, nil)
}

// Orders returns the orders between two dates (inclusive), as core sorts them.
func (c *Core) Orders(ctx context.Context, userID int64, from, to string) ([]contracts.Order, error) {
	var out []contracts.Order
	q := url.Values{"from": {from}, "to": {to}}
	err := c.c.do(ctx, userID, http.MethodGet, "/v1/orders?"+q.Encode(), nil, &out)
	return out, err
}

// Prefs returns the user's food preferences (allergens, dislikes, ...).
func (c *Core) Prefs(ctx context.Context, userID int64) (contracts.Prefs, error) {
	var out contracts.Prefs
	err := c.c.do(ctx, userID, http.MethodGet, "/v1/prefs", nil, &out)
	return out, err
}

// SetPrefs replaces the user's preferences with the given list.
func (c *Core) SetPrefs(ctx context.Context, userID int64, p contracts.Prefs) error {
	return c.c.do(ctx, userID, http.MethodPut, "/v1/prefs", p, nil)
}

// Stats returns the statistics between two dates (inclusive).
func (c *Core) Stats(ctx context.Context, userID int64, from, to string) (contracts.Stats, error) {
	var out contracts.Stats
	q := url.Values{"from": {from}, "to": {to}}
	err := c.c.do(ctx, userID, http.MethodGet, "/v1/stats?"+q.Encode(), nil, &out)
	return out, err
}

// Catalog is the client of the catalog service (only allergen confirmation).
type Catalog struct{ c client }

// NewCatalog makes a catalog client. baseURL is like http://catalog:9082.
func NewCatalog(baseURL string, hc *http.Client) *Catalog {
	return &Catalog{client{base: strings.TrimRight(baseURL, "/"), http: hc, name: "catalog"}}
}

// WithLog sets the logger of the calls.
func (c *Catalog) WithLog(l *slog.Logger) *Catalog {
	c.c.log = l
	return c
}

// ConfirmAllergens saves a user's confirmation of the dish allergens
// (an empty list means "I checked: none of them"). The dish facts are
// shared by everybody, so catalog only gets the user id for the record.
func (c *Catalog) ConfirmAllergens(ctx context.Context, userID, dishID int64, allergens []string) error {
	if allergens == nil {
		allergens = []string{}
	}
	return c.c.do(ctx, userID, http.MethodPost, "/v1/dishes/"+strconv.FormatInt(dishID, 10)+"/allergens/confirm",
		contracts.ConfirmAllergensRequest{Allergens: allergens}, nil)
}

func bodyForLog(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if len(b) > 8<<10 {
		return string(b[:8<<10]) + "…"
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}
