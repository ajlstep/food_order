// Package web serves the logs UI: one page and a small JSON API.
package web

import (
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"food-order/logs/internal/store"
)

//go:embed index.html
var indexHTML []byte

// Handler returns the HTTP handler of the UI.
func Handler(st *store.Store, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.Ping(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	// GET /api/logs?level=warn,error&service=core&q=text&from=...&to=...&before=ID&after=ID&limit=100
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := store.Filter{Service: q.Get("service"), Text: strings.TrimSpace(q.Get("q"))}
		if l := q.Get("level"); l != "" {
			f.Levels = strings.Split(l, ",")
		}
		var ok bool
		if f.From, ok = timeParam(q.Get("from")); !ok {
			http.Error(w, "bad from", http.StatusBadRequest)
			return
		}
		if f.To, ok = timeParam(q.Get("to")); !ok {
			http.Error(w, "bad to", http.StatusBadRequest)
			return
		}
		f.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
		f.AfterID, _ = strconv.ParseInt(q.Get("after"), 10, 64)
		f.Limit, _ = strconv.Atoi(q.Get("limit"))
		items, err := st.List(r.Context(), f)
		if err != nil {
			log.Error("list logs", "err", err)
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"items": items})
	})
	mux.HandleFunc("GET /api/meta", func(w http.ResponseWriter, r *http.Request) {
		m, err := st.Meta(r.Context())
		if err != nil {
			log.Error("meta", "err", err)
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, m)
	})
	return mux
}

// timeParam accepts "" or RFC 3339 and returns the stored time format.
func timeParam(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", false
	}
	return t.UTC().Format(store.TimeFormat), true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
