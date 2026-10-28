// Package api exposes the parser over HTTP (see Comunicare.md):
//
//	GET /healthz
//	GET /v1/fetch?date=2026-10-28   (default: tomorrow)
//
// It never writes to any database; it only returns the list of dishes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

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
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/fetch", func(w http.ResponseWriter, r *http.Request) {
		date := now().In(loc).AddDate(0, 0, 1)
		if q := r.URL.Query().Get("date"); q != "" {
			d, err := time.ParseInLocation("2006-01-02", q, loc)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "bad_date", "date must look like 2026-10-28")
				return
			}
			date = d
		}

		res, err := g.Get(r.Context(), date)
		var th *menu.ThrottledError
		switch {
		case err == nil:
			out := fetchResponse{Date: res.Date, FetchedAt: res.FetchedAt, Cached: res.Cached, Items: make([]itemDTO, 0, len(res.Items))}
			for _, it := range res.Items {
				out.Items = append(out.Items, itemDTO{Item: it, NameNorm: menu.NormName(it.Name)})
			}
			writeJSON(w, http.StatusOK, out)
		case errors.Is(err, menu.ErrEmpty):
			log.Warn("menu is empty", "date", res.Date, "asked", date.Format("2006-01-02"))
			writeErr(w, http.StatusNotFound, "menu_empty", "no dishes found for this date (not published yet or site changed)")
		case errors.As(err, &th):
			w.Header().Set("Retry-After", strconv.Itoa(int(th.RetryAfter.Seconds())+1))
			writeErr(w, http.StatusTooManyRequests, "throttled", th.Error())
		case errors.Is(err, context.DeadlineExceeded):
			writeErr(w, http.StatusGatewayTimeout, "timeout", "the site did not answer in time")
		default:
			log.Error("fetch failed", "err", err)
			writeErr(w, http.StatusBadGateway, "fetch_failed", err.Error())
		}
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
