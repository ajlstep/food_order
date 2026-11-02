package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"food-order/gateway/internal/access"
	"food-order/gateway/internal/forward"
	"food-order/shared/contracts/gateway"
	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
)

// The whole gateway over a real NATS server, with a fake users behind it
// that tells back what it received.

const key = "test-key-test-key-test-key-12345"

// echo is what the fake users answers: what reached it.
type echo struct {
	UserID    string          `json:"user_id"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
}

func start(t *testing.T) *httptest.Server {
	t.Helper()
	ns, err := server.NewServer(&server.Options{Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats did not start")
	}
	t.Cleanup(ns.Shutdown)
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)

	log := slog.New(slog.DiscardHandler)
	fakeUsers(t, nc, log)

	client := rpc.NewClient(nc)
	const timeout = 300 * time.Millisecond
	h := Handler(access.New(key, access.NewRPCResolver(client, timeout), log), forward.New(client, timeout, log), nc.IsConnected)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// fakeUsers: external id "7" is active, "8" waits for its email. Channels
// get is not answered at all, and settings set is too slow.
func fakeUsers(t *testing.T, nc *nats.Conn, log *slog.Logger) {
	t.Helper()
	srv := rpc.NewServer(nc, log, "users")
	echoes := func(_ context.Context, req rpc.Request) (any, error) {
		return echo{UserID: req.UserID, RequestID: req.RequestID, Data: req.Data}, nil
	}
	handlers := map[string]rpc.Handler{
		users.SubjectAuthResolve: func(_ context.Context, req rpc.Request) (any, error) {
			var in users.ResolveRequest
			if err := rpc.Decode(req, &in); err != nil {
				return nil, err
			}
			if in.ExternalID == "8" {
				return users.ResolveResponse{UserID: "u-pending", Status: users.StatusPending}, nil
			}
			return users.ResolveResponse{UserID: "u-active", Status: users.StatusActive, DisplayName: in.DisplayName}, nil
		},
		users.SubjectAuthEmailRequest: func(ctx context.Context, req rpc.Request) (any, error) {
			if strings.Contains(string(req.Data), "again@") {
				return nil, &rpc.Error{Code: users.ErrCooldown, Details: map[string]any{"retry_after": 25}}
			}
			return echoes(ctx, req)
		},
		users.SubjectAuthIdentityStatus:  echoes,
		users.SubjectProfileGet:          echoes,
		users.SubjectProfileLocationsDel: echoes,
		users.SubjectProfileSettingsSet: func(context.Context, rpc.Request) (any, error) {
			time.Sleep(time.Second)
			return nil, nil
		},
	}
	for subject, h := range handlers {
		if err := srv.Handle(subject, h); err != nil {
			t.Fatal(err)
		}
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
}

// call sends one request as the bot, for the Telegram user externalID.
func call(t *testing.T, srv *httptest.Server, method, path, externalID, body string) (*http.Response, gateway.Response) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(gateway.HeaderAuthorization, "Bearer "+key)
	req.Header.Set(gateway.HeaderChannel, "telegram")
	req.Header.Set(gateway.HeaderExternalID, externalID)
	req.Header.Set(gateway.HeaderRequestID, "bot-req-0001")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out gateway.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: the answer is not JSON: %v", method, path, err)
	}
	return resp, out
}

func dataOf[T any](t *testing.T, r gateway.Response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.Data, &v); err != nil {
		t.Fatalf("data %s: %v", r.Data, err)
	}
	return v
}

func TestIdentifyTakesTheIdentityFromTheHeaders(t *testing.T) {
	srv := start(t)
	// the body tries to speak for somebody else; the headers win
	resp, out := call(t, srv, "POST", "/v1/auth/identify", "7", `{"external_id":"999","display_name":"Ana"}`)
	if resp.StatusCode != 200 || !out.OK {
		t.Fatalf("status %d, %+v", resp.StatusCode, out)
	}
	got := dataOf[users.ResolveResponse](t, out)
	if got.UserID != "u-active" || got.DisplayName != "Ana" {
		t.Errorf("identify = %+v", got)
	}
	if out.RequestID != "bot-req-0001" || resp.Header.Get(gateway.HeaderRequestID) != "bot-req-0001" {
		t.Errorf("the request id of the bot was not kept: %q", out.RequestID)
	}
}

func TestRegistrationWritesThePersonIntoTheData(t *testing.T) {
	srv := start(t)
	// a pending account may ask for a code; a user_id in the body is ignored
	_, out := call(t, srv, "POST", "/v1/auth/email", "8", `{"email":"a@b.md","user_id":"someone-else"}`)
	got := dataOf[echo](t, out)
	var data users.EmailRequestRequest
	_ = json.Unmarshal(got.Data, &data)
	if data.UserID != "u-pending" || data.Email != "a@b.md" {
		t.Errorf("users got %+v", data)
	}
	if got.RequestID != "bot-req-0001" {
		t.Errorf("request id in the envelope = %q", got.RequestID)
	}
}

func TestChannelStatusTakesTheChannelFromTheHeaders(t *testing.T) {
	srv := start(t)
	_, out := call(t, srv, "POST", "/v1/auth/channel-status", "7", `{"status":"stopped","channel":"other"}`)
	var data users.IdentityStatusRequest
	_ = json.Unmarshal(dataOf[echo](t, out).Data, &data)
	if data != (users.IdentityStatusRequest{Channel: "telegram", ExternalID: "7", Status: "stopped"}) {
		t.Errorf("users got %+v", data)
	}
}

func TestProfilePutsThePersonInTheEnvelope(t *testing.T) {
	srv := start(t)
	_, out := call(t, srv, "GET", "/v1/me", "7", "")
	if got := dataOf[echo](t, out); got.UserID != "u-active" {
		t.Errorf("envelope user_id = %q", got.UserID)
	}

	_, out = call(t, srv, "DELETE", "/v1/me/locations/42", "7", "")
	var data users.LocationIDRequest
	_ = json.Unmarshal(dataOf[echo](t, out).Data, &data)
	if data.ID != 42 {
		t.Errorf("location id = %d", data.ID)
	}
}

func TestErrors(t *testing.T) {
	srv := start(t)
	tests := []struct {
		name, method, path, externalID, body string
		wantStatus                           int
		wantCode                             string
	}{
		{"pending cannot see the profile", "GET", "/v1/me", "8", "", 403, users.ErrEmailNotConfirmed},
		{"a refusal of users with its wait", "POST", "/v1/auth/email", "7", `{"email":"again@b.md"}`, 429, users.ErrCooldown},
		{"body that is not JSON", "PATCH", "/v1/me", "7", `{name:`, 400, rpc.CodeBadRequest},
		{"registration without body", "POST", "/v1/auth/email", "7", "", 400, rpc.CodeBadRequest},
		{"location id not a number", "DELETE", "/v1/me/locations/abc", "7", "", 400, rpc.CodeBadRequest},
		{"nobody listens", "GET", "/v1/me/channels", "7", "", 503, rpc.CodeUnavailable},
		{"the service is late", "PATCH", "/v1/me/settings", "7", `{"values":{}}`, 504, rpc.CodeTimeout},
		{"unknown path", "GET", "/v1/nothing", "7", "", 404, gateway.ErrNotFoundRoute},
		{"body too large", "PUT", "/v1/me/prefs", "7", `{"x":"` + strings.Repeat("a", 70<<10) + `"}`, 413, gateway.ErrBodyTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, out := call(t, srv, tt.method, tt.path, tt.externalID, tt.body)
			if resp.StatusCode != tt.wantStatus || out.Error == nil || out.Error.Code != tt.wantCode {
				t.Fatalf("got %d %+v, want %d %s", resp.StatusCode, out.Error, tt.wantStatus, tt.wantCode)
			}
			if tt.wantCode == users.ErrCooldown && resp.Header.Get("Retry-After") != "25" {
				t.Errorf("Retry-After = %q", resp.Header.Get("Retry-After"))
			}
		})
	}
}

func TestHealthNeedsNoKey(t *testing.T) {
	srv := start(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
}
