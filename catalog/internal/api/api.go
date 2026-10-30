// Package api is catalog's HTTP API (port 8082), called by core and bot on
// the internal network. Errors: {"error": {"code", "message"}}, the message
// in Romanian. Only the allergen confirmation needs X-User-ID.
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

	"food-order/catalog/internal/contracts"
	"food-order/catalog/internal/service"
)

// Pinger checks the database for /healthz.
type Pinger interface {
	Ping(ctx context.Context) error
}

// New returns the handler with every route. importOn enables POST /v1/import.
func New(svc *service.Service, db Pinger, importOn bool, log *slog.Logger) http.Handler {
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
	mux.HandleFunc("GET /v1/menu", a.menu)
	mux.HandleFunc("GET /v1/dishes", a.dishes)
	mux.HandleFunc("GET /v1/dishes/{id}", a.dish)
	mux.HandleFunc("POST /v1/similar", a.similar)
	mux.HandleFunc("POST /v1/dishes/{id}/allergens/confirm", a.confirmAllergens)
	if importOn {
		mux.HandleFunc("POST /v1/import", a.importMenu)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Nu există așa ceva.")
	})
	return a.logRequests(mux)
}

type api struct {
	svc *service.Service
	log *slog.Logger
}

func (a *api) menu(w http.ResponseWriter, r *http.Request) {
	out, err := a.svc.Menu(r.Context(), r.URL.Query().Get("date"))
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) dishes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := a.svc.Dishes(r.Context(), q.Get("ids"), q.Get("date"))
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) dish(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := a.svc.Dish(r.Context(), id, r.URL.Query().Get("date"))
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) similar(w http.ResponseWriter, r *http.Request) {
	var in contracts.SimilarRequest
	if !readJSON(w, r, &in) {
		return
	}
	out, err := a.svc.Similar(r.Context(), in)
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) confirmAllergens(w http.ResponseWriter, r *http.Request) {
	v := r.Header.Get(contracts.HeaderUserID)
	userID, err := strconv.ParseInt(v, 10, 64)
	if v == "" || err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "missing_user", "Lipsește antetul X-User-ID (id-ul utilizatorului din bot).")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in contracts.ConfirmAllergensRequest
	if !readJSON(w, r, &in) {
		return
	}
	out, err := a.svc.ConfirmAllergens(r.Context(), userID, id, in.Allergens)
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) importMenu(w http.ResponseWriter, r *http.Request) {
	var in contracts.MenuFetched
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "Corpul cererii nu e JSON valid (aceeași formă ca data din food.menu.fetched).")
		return
	}
	out, err := a.svc.Import(r.Context(), in)
	if a.fail(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id", "Id-ul felului nu e un număr.")
		return 0, false
	}
	return id, true
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
	writeError(w, http.StatusInternalServerError, "internal", "Eroare internă în catalog.")
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
		a.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}
