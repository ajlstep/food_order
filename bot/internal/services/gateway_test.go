package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"food-order/shared/contracts/gateway"
	"food-order/shared/contracts/users"
)

func testGateway(t *testing.T, h http.HandlerFunc) *Gateway {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewGateway(srv.URL+"/", "cheia", srv.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestEveryCallSaysWhoItIsFor(t *testing.T) {
	g := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
		checks := map[string]string{
			gateway.HeaderAuthorization: "Bearer cheia",
			gateway.HeaderChannel:       "telegram",
			gateway.HeaderExternalID:    "42",
		}
		for header, want := range checks {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/email" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var in users.EmailRequestRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Email != "a@b.md" {
			t.Errorf("body = %+v, %v", in, err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"sent":true,"valid_for_seconds":1800},"request_id":"x"}`))
	})
	out, err := g.RequestEmailCode(context.Background(), "42", "a@b.md")
	if err != nil || !out.Sent || out.ValidForSecond != 1800 {
		t.Fatalf("%+v, %v", out, err)
	}
}

func TestRefusalKeepsCodeAndDetails(t *testing.T) {
	g := testGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"wrong_code","message":"nu","details":{"attempts_left":3}},"request_id":"x"}`))
	})
	_, err := g.ConfirmEmail(context.Background(), "42", "ABCD")
	var e *GatewayError
	if !errors.As(err, &e) || e.Status != 422 || e.Code != users.ErrWrongCode || e.Number("attempts_left") != 3 {
		t.Fatalf("err = %#v", err)
	}
}

func TestEmptyListGoesAsEmptyList(t *testing.T) {
	g := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if list, ok := raw["allergens"].([]any); !ok || len(list) != 0 {
			t.Errorf("allergens = %#v, want []", raw["allergens"])
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"allergens":[]},"request_id":"x"}`))
	})
	if _, err := g.SetAllergens(context.Background(), "42", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSomethingThatIsNotTheGateway(t *testing.T) {
	g := testGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>proxy</html>"))
	})
	_, err := g.Profile(context.Background(), "42")
	var e *GatewayError
	if err == nil || errors.As(err, &e) {
		t.Fatalf("err = %#v, want a plain failure", err)
	}
}
