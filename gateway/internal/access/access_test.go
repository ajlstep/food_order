package access

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"food-order/gateway/internal/requestid"
	"food-order/shared/contracts/gateway"
	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
)

const testKey = "test-key-test-key-test-key-12345"

// fakeResolver answers with a fixed account, or a fixed error.
type fakeResolver struct {
	status string
	err    error
	calls  int
	got    users.ResolveRequest
}

func (f *fakeResolver) Resolve(_ context.Context, in users.ResolveRequest) (users.ResolveResponse, error) {
	f.calls++
	f.got = in
	if f.err != nil {
		return users.ResolveResponse{}, f.err
	}
	return users.ResolveResponse{UserID: "u-1", Status: f.status}, nil
}

func TestWrap(t *testing.T) {
	tests := []struct {
		name       string
		level      Level
		key        string
		channel    string
		externalID string
		status     string
		resolveErr error
		wantStatus int
		wantCode   string
		wantLookUp bool
	}{
		{name: "no key", level: LevelActive, key: "", channel: "telegram", externalID: "7", wantStatus: 401, wantCode: gateway.ErrUnauthorized},
		{name: "wrong key", level: LevelActive, key: testKey + "x", channel: "telegram", externalID: "7", wantStatus: 401, wantCode: gateway.ErrUnauthorized},
		{name: "no channel", level: LevelActive, key: testKey, externalID: "7", wantStatus: 400, wantCode: gateway.ErrNoIdentity},
		{name: "no external id", level: LevelActive, key: testKey, channel: "telegram", wantStatus: 400, wantCode: gateway.ErrNoIdentity},
		{name: "channel the bot may not speak for", level: LevelActive, key: testKey, channel: "whatsapp", externalID: "7", wantStatus: 403, wantCode: gateway.ErrChannelRefused},

		{name: "channel level does not ask users", level: LevelChannel, key: testKey, channel: "Telegram ", externalID: " 7", wantStatus: 200},

		{name: "pending passes the registration", level: LevelAccount, key: testKey, channel: "telegram", externalID: "7", status: users.StatusPending, wantStatus: 200, wantLookUp: true},
		{name: "pending stops at active routes", level: LevelActive, key: testKey, channel: "telegram", externalID: "7", status: users.StatusPending, wantStatus: 403, wantCode: users.ErrEmailNotConfirmed, wantLookUp: true},
		{name: "active passes", level: LevelActive, key: testKey, channel: "telegram", externalID: "7", status: users.StatusActive, wantStatus: 200, wantLookUp: true},
		{name: "blocked stops everywhere", level: LevelAccount, key: testKey, channel: "telegram", externalID: "7", status: users.StatusBlocked, wantStatus: 403, wantCode: users.ErrAccountBlocked, wantLookUp: true},
		{name: "deleted is not found", level: LevelActive, key: testKey, channel: "telegram", externalID: "7", status: users.StatusDeleted, wantStatus: 404, wantCode: rpc.CodeNotFound, wantLookUp: true},

		{name: "users does not listen", level: LevelActive, key: testKey, channel: "telegram", externalID: "7", resolveErr: rpc.NewError(rpc.CodeUnavailable, "x"), wantStatus: 503, wantCode: rpc.CodeUnavailable, wantLookUp: true},
		{name: "users is late", level: LevelActive, key: testKey, channel: "telegram", externalID: "7", resolveErr: rpc.NewError(rpc.CodeTimeout, "x"), wantStatus: 504, wantCode: rpc.CodeTimeout, wantLookUp: true},
		{name: "a technical failure is internal", level: LevelActive, key: testKey, channel: "telegram", externalID: "7", resolveErr: io.ErrUnexpectedEOF, wantStatus: 500, wantCode: rpc.CodeInternal, wantLookUp: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := &fakeResolver{status: tt.status, err: tt.resolveErr}
			acc := New(testKey, res, slog.New(slog.DiscardHandler))

			var seen Person
			var seenID Identity
			final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen, seenID = PersonFrom(r.Context()), IdentityFrom(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tt.key != "" {
				req.Header.Set(gateway.HeaderAuthorization, "Bearer "+tt.key)
			}
			req.Header.Set(gateway.HeaderChannel, tt.channel)
			req.Header.Set(gateway.HeaderExternalID, tt.externalID)
			rec := httptest.NewRecorder()
			requestid.Middleware(acc.Wrap(tt.level, final)).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := res.calls > 0; got != tt.wantLookUp {
				t.Errorf("asked users = %v, want %v", got, tt.wantLookUp)
			}
			if tt.wantCode != "" {
				var resp gateway.Response
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error == nil {
					t.Fatalf("body is not an error answer: %s", rec.Body)
				}
				if resp.Error.Code != tt.wantCode {
					t.Errorf("code = %q, want %q", resp.Error.Code, tt.wantCode)
				}
				if resp.RequestID == "" {
					t.Error("the answer has no request_id")
				}
				return
			}
			if seenID != (Identity{Channel: "telegram", ExternalID: "7"}) {
				t.Errorf("identity = %+v, want it trimmed and lower case", seenID)
			}
			if tt.wantLookUp && seen.UserID != "u-1" {
				t.Errorf("person = %+v, want u-1", seen)
			}
			if tt.wantLookUp && res.got != (users.ResolveRequest{Channel: "telegram", ExternalID: "7"}) {
				t.Errorf("users was asked %+v", res.got)
			}
		})
	}
}
