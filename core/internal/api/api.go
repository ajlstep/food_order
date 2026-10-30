// Package api is core's HTTP API (port 8081), called by the bot. Every
// /v1 call carries the user in the header X-User-ID (users.id from bot.db);
// without it the answer is 400. Errors: {"error": {"code", "message"}}.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"food-order/core/internal/contracts"
	"food-order/core/internal/service"
)

// Pinger checks the database for /healthz.
type Pinger interface {
	Ping(ctx context.Context) error
}

// New returns the handler with every route.
func New(svc *service.Service, db Pinger, log *slog.Logger) http.Handler {
	a := &api{svc: svc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/recommendations", a.user(a.createRecommendation))
	mux.HandleFunc("GET /v1/recommendations", a.user(a.getRecommendation))
	mux.HandleFunc("POST /v1/orders", a.user(a.createOrder))
	mux.HandleFunc("POST /v1/orders/{id}/rating", a.user(a.rate))
	mux.HandleFunc("GET /v1/orders", a.user(a.orders))
	mux.HandleFunc("GET /v1/prefs", a.user(a.prefs))
	mux.HandleFunc("PUT /v1/prefs", a.user(a.setPrefs))
	mux.HandleFunc("GET /v1/stats", a.user(a.stats))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Nu există așa ceva.")
	})
	return a.logRequests(mux)
}

type api struct {
	svc *service.Service
	log *slog.Logger
}

type userHandler func(w http.ResponseWriter, r *http.Request, userID int64)

// user reads X-User-ID; core has no users table: a new id simply appears.
func (a *api) user(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get(contracts.HeaderUserID)
		id, err := strconv.ParseInt(v, 10, 64)
		if v == "" || err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "missing_user", "Lipsește antetul X-User-ID (id-ul utilizatorului din bot).")
			return
		}
		h(w, r, id)
	}
}

func (a *api) createRecommendation(w http.ResponseWriter, r *http.Request, userID int64) {
	var in contracts.CreateRecommendationRequest
	if !readJSON(w, r, &in) {
		return
	}
	rec, err := a.svc.RequestRecommendation(r.Context(), userID, in.Date)
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, rec)
}

func (a *api) getRecommendation(w http.ResponseWriter, r *http.Request, userID int64) {
	rec, err := a.svc.Recommendation(r.Context(), userID, r.URL.Query().Get("date"))
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (a *api) createOrder(w http.ResponseWriter, r *http.Request, userID int64) {
	var in contracts.CreateOrderRequest
	if !readJSON(w, r, &in) {
		return
	}
	o, err := a.svc.CreateOrder(r.Context(), userID, in)
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (a *api) rate(w http.ResponseWriter, r *http.Request, userID int64) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id", "Id-ul comenzii nu e un număr.")
		return
	}
	var in contracts.RatingRequest
	if !readJSON(w, r, &in) {
		return
	}
	o, err := a.svc.Rate(r.Context(), userID, id, in)
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (a *api) orders(w http.ResponseWriter, r *http.Request, userID int64) {
	q := r.URL.Query()
	out, err := a.svc.Orders(r.Context(), userID, q.Get("from"), q.Get("to"))
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) prefs(w http.ResponseWriter, r *http.Request, userID int64) {
	out, err := a.svc.Prefs(r.Context(), userID)
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) setPrefs(w http.ResponseWriter, r *http.Request, userID int64) {
	var in contracts.Prefs
	if !readJSON(w, r, &in) {
		return
	}
	out, err := a.svc.SetPrefs(r.Context(), userID, in)
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) stats(w http.ResponseWriter, r *http.Request, userID int64) {
	q := r.URL.Query()
	out, err := a.svc.Stats(r.Context(), userID, q.Get("from"), q.Get("to"))
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// fail writes the error answer; it returns true if there was an error.
func (a *api) fail(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var se *service.Error
	if errors.As(err, &se) {
		writeError(w, se.Status, se.Code, se.Message)
		return true
	}
	a.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "Eroare internă în core.")
	return true
}

func readJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "Corpul cererii nu e JSON valid.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, contracts.ErrorBody{Error: contracts.ErrorDetail{Code: code, Message: msg}})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (a *api) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" {
			return
		}
		a.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"user_id", r.Header.Get(contracts.HeaderUserID), "ms", time.Since(start).Milliseconds())
	})
}
