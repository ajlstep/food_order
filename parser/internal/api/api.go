// Package api exposes the parser over HTTP (see Comunicare.md):
//
//	GET /healthz
//	GET /v1/fetch?date=2026-10-28   (default: tomorrow)
//
// It never writes to any database; it only returns the list of dishes.
package api

import (
	"sync/atomic"
	"io"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"food-order/parser/internal/applog"
	"food-order/parser/internal/menu"
)

// Getter is what the handler needs from menu.Service.
type Getter interface {
	Get(ctx context.Context, date time.Time) (menu.Result, error)
}

type itemDTO struct {
	menu.Item
	NameNorm string `json:"name_norm"`
}

type fetchResponse struct {
	Date      string    `json:"date"`
	FetchedAt time.Time `json:"fetched_at"`
	Cached    bool      `json:"cached"`
	Items     []itemDTO `json:"items"`
}

// New builds the HTTP handler. loc decides what "tomorrow" means.
func New(g Getter, loc *time.Location, now func() time.Time, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	a := &api{log: log}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/fetch", func(w http.ResponseWriter, r *http.Request) {
		date := now().In(loc).AddDate(0, 0, 1)
		if q := r.URL.Query().Get("date"); q != "" {
			d, err := time.ParseInLocation("2006-01-02", q, loc)
			if err != nil {
				log.WarnContext(r.Context(), "bad date", "date", q)
				writeErr(w, http.StatusBadRequest, "bad_date", "date must look like 2026-10-28")
				return
			}
			date = d
		}

		ctx := r.Context()
		log.InfoContext(ctx, "menu fetch asked over HTTP", "date", date.Format("2006-01-02"))
		res, err := g.Get(ctx, date)
		var th *menu.ThrottledError
		switch {
		case err == nil:
			log.InfoContext(ctx, "menu fetched", "date", res.Date, "dishes", len(res.Items), "cached", res.Cached, "fetched_at", res.FetchedAt)
			out := fetchResponse{Date: res.Date, FetchedAt: res.FetchedAt, Cached: res.Cached, Items: make([]itemDTO, 0, len(res.Items))}
			for _, it := range res.Items {
				out.Items = append(out.Items, itemDTO{Item: it, NameNorm: menu.NormName(it.Name)})
			}
			writeJSON(w, http.StatusOK, out)
		case errors.As(err, &th): // first: a throttled answer also wraps the last outcome
			log.WarnContext(ctx, "fetch throttled", "retry_after", th.RetryAfter, "last_err", th.Last)
			w.Header().Set("Retry-After", strconv.Itoa(int(th.RetryAfter.Seconds())+1))
			writeErr(w, http.StatusTooManyRequests, "throttled", th.Error())
		case errors.Is(err, menu.ErrEmpty):
			log.WarnContext(ctx, "menu is empty", "date", res.Date, "asked", date.Format("2006-01-02"))
			writeErr(w, http.StatusNotFound, "menu_empty", "no dishes found for this date (not published yet or site changed)")
		case errors.Is(err, context.DeadlineExceeded):
			log.ErrorContext(ctx, "the site did not answer in time", "err", err)
			writeErr(w, http.StatusGatewayTimeout, "timeout", "the site did not answer in time")
		default:
			log.ErrorContext(ctx, "fetch failed", "err", err)
			writeErr(w, http.StatusBadGateway, "fetch_failed", err.Error())
		}
	})
	return a.logRequests(mux)
}

type api struct{ log *slog.Logger }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	ctx    context.Context // the request context with its log fields
	log    *slog.Logger
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// logRequests gives every request an id and its fields in the context
// (req_id, method, path, user_id), so every log of the request carries
// them, and logs the request itself: info for every call, warn for a 4xx, error for
// a 5xx, debug for /healthz. The body is in the debug log.
func (a *api) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = newReqID(start)
		}
		ctx := applog.With(r.Context(), "req_id", reqID, "http_method", r.Method, "http_path", r.URL.Path)
		if u := r.Header.Get("X-User-ID"); u != "" {
			ctx = applog.With(ctx, "user_id", u)
		}
		var body []byte
		if r.Body != nil && r.ContentLength != 0 && a.log.Enabled(ctx, slog.LevelDebug) {
			body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", reqID)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK, ctx: ctx, log: a.log}
		a.log.DebugContext(ctx, "http request", "query", r.URL.RawQuery, "remote", r.RemoteAddr,
			"user_agent", r.UserAgent(), "body", bodyForLog(body))
		next.ServeHTTP(sw, r)
		args := []any{"status", sw.status, "ms", applog.Since(start), "bytes", sw.bytes, "query", r.URL.RawQuery}
		switch {
		case r.URL.Path == "/healthz":
			a.log.DebugContext(ctx, "http", args...)
		case sw.status >= 500:
			a.log.ErrorContext(ctx, "http", args...)
		case sw.status >= 400:
			a.log.WarnContext(ctx, "http", args...)
		default:
			a.log.InfoContext(ctx, "http", args...)
		}
	})
}

// logCtx is the request context with its log fields (from logRequests).
func logCtx(w http.ResponseWriter) context.Context {
	if sw, ok := w.(*statusWriter); ok {
		return sw.ctx
	}
	return context.Background()
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

var reqSeq atomic.Uint64

func newReqID(t time.Time) string {
	return "parser-" + strconv.FormatInt(t.UnixMilli(), 36) + "-" + strconv.FormatUint(reqSeq.Add(1), 36)
}
