package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"food-order/bot/internal/contracts"
)

func TestCreateOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-ID") != "3" {
			t.Errorf("X-User-ID = %q", r.Header.Get("X-User-ID"))
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/orders" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var in contracts.CreateOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in.DishID != 12 || !in.Forced || in.Date != "2026-10-29" {
			t.Errorf("body = %+v", in)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 7, "dish_id": 12, "dish_name": "Ciorbă", "date": "2026-10-29", "forced": true}`))
	}))
	defer srv.Close()

	o, err := NewCore(srv.URL+"/", srv.Client()).CreateOrder(context.Background(), 3,
		contracts.CreateOrderRequest{Date: "2026-10-29", DishID: 12, Forced: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.ID != 7 || o.DishName != "Ciorbă" {
		t.Fatalf("order = %+v", o)
	}
}

func TestErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error": {"code": "expired", "message": "recomandarea a expirat"}}`))
	}))
	defer srv.Close()

	err := NewCore(srv.URL, srv.Client()).Rate(context.Background(), 1, 3, contracts.RatingRequest{Rating: 5})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "expired" {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("409 must not be ErrNotFound")
	}
}

func TestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	_, err := NewCore(srv.URL, srv.Client()).Recommendation(context.Background(), 1, "2026-10-29")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestConfirmAllergensSendsEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/dishes/12/allergens/confirm" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if list, ok := raw["allergens"].([]any); !ok || len(list) != 0 {
			t.Errorf("allergens = %#v, want []", raw["allergens"])
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := NewCatalog(srv.URL, srv.Client()).ConfirmAllergens(context.Background(), 1, 12, nil); err != nil {
		t.Fatal(err)
	}
}
