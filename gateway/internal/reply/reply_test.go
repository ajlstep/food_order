package reply

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"food-order/shared/contracts/gateway"
	"food-order/shared/rpc"
)

func TestFail(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
		wantRetry   string
	}{
		{name: "a refusal keeps its message", err: rpc.NewError("invalid_email", "x"), wantStatus: 400, wantCode: "invalid_email", wantMessage: "x"},
		{name: "cooldown tells when to come back", err: &rpc.Error{Code: "cooldown", Details: map[string]any{"retry_after": 12.0}}, wantStatus: 429, wantCode: "cooldown", wantRetry: "12"},
		{name: "a part of a second rounds up", err: &rpc.Error{Code: "too_many_codes", Details: map[string]any{"retry_after": 0.2}}, wantStatus: 429, wantCode: "too_many_codes", wantRetry: "1"},
		{name: "an unknown code of a service", err: rpc.NewError("something_new", ""), wantStatus: 422, wantCode: "something_new"},
		{name: "no service: subject stays inside", err: rpc.NewError(rpc.CodeUnavailable, "no service listens on rpc.users.x"), wantStatus: 503, wantCode: rpc.CodeUnavailable, wantMessage: "Service Unavailable"},
		{name: "a plain error is internal", err: errors.New("disk on fire"), wantStatus: 500, wantCode: rpc.CodeInternal, wantMessage: "Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Fail(rec, httptest.NewRequest(http.MethodGet, "/", nil), tt.err)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Retry-After"); got != tt.wantRetry {
				t.Errorf("Retry-After = %q, want %q", got, tt.wantRetry)
			}
			var resp gateway.Response
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.OK || resp.Error == nil || resp.Error.Code != tt.wantCode {
				t.Fatalf("answer = %s, want code %q", rec.Body, tt.wantCode)
			}
			if tt.wantMessage != "" && resp.Error.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", resp.Error.Message, tt.wantMessage)
			}
		})
	}
}
