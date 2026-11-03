package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"food-order/shared/contracts/catalog"
	"food-order/shared/contracts/core"
	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
)

// The rows with something to take from the path or the query: what reaches
// the service must be the same as if the bot had sent it as JSON.
func TestRoutesOfFoodAndOrg(t *testing.T) {
	cases := []struct {
		method, path, body string
		subject            string
		wantData           string
	}{
		{"POST", "/v1/recommendations", `{"date":"2026-11-04"}`, core.SubjectRecommendationCreate, `{"date":"2026-11-04"}`},
		{"GET", "/v1/recommendations?date=2026-11-04", "", core.SubjectRecommendationGet, `{"date":"2026-11-04"}`},
		{"POST", "/v1/orders", `{"date":"2026-11-04","dish_id":7}`, core.SubjectOrderCreate, `{"date":"2026-11-04","dish_id":7}`},
		{"POST", "/v1/orders/9/rating", `{"rating":5}`, core.SubjectOrderRate, `{"order_id":9,"rating":5}`},
		{"GET", "/v1/orders?from=2026-11-01&to=2026-11-04", "", core.SubjectOrdersList, `{"from":"2026-11-01","to":"2026-11-04"}`},
		{"GET", "/v1/stats?from=2026-11-01&to=2026-11-04", "", core.SubjectStats, `{"from":"2026-11-01","to":"2026-11-04"}`},
		{"POST", "/v1/dishes/12/allergens/confirm", `{"allergens":["gluten"]}`, catalog.SubjectAllergensConfirm, `{"dish_id":12,"allergens":["gluten"]}`},
		{"GET", "/v1/partners?date=2026-11-04", "", catalog.SubjectPartnersList, `{"date":"2026-11-04"}`},
		{"DELETE", "/v1/me/company/members/u-9", "", users.SubjectOrgCompanyRemove, `{"user_id":"u-9"}`},
		{"GET", "/v1/me/company/report?from=2026-11-01&to=2026-11-04", "", core.SubjectCompanyReport, `{"from":"2026-11-01","to":"2026-11-04"}`},
		{"DELETE", "/v1/me/group/members/u-9", "", users.SubjectOrgGroupRemove, `{"user_id":"u-9"}`},
		{"POST", "/v1/me/invites/i-1/accept", "", users.SubjectOrgInviteAccept, `{"id":"i-1"}`},
		{"POST", "/v1/me/invites/i-1/decline", "", users.SubjectOrgInviteDecline, `{"id":"i-1"}`},
	}

	srv, nc := startWithConn(t)
	got := make(chan string, 1)
	seen := func(subject string) rpc.Handler {
		return func(_ context.Context, req rpc.Request) (any, error) {
			got <- subject + " " + req.UserID + " " + string(req.Data)
			return map[string]bool{"ok": true}, nil
		}
	}
	server := rpc.NewServer(nc, slog.New(slog.DiscardHandler), "fake")
	for _, c := range cases {
		if err := server.Handle(c.subject, seen(c.subject)); err != nil && !strings.Contains(err.Error(), "already") {
			t.Fatal(err)
		}
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	for _, c := range cases {
		resp, out := call(t, srv, c.method, c.path, "7", c.body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s: status %d %+v", c.method, c.path, resp.StatusCode, out)
			continue
		}
		parts := strings.SplitN(<-got, " ", 3)
		if parts[0] != c.subject || parts[1] != "u-active" {
			t.Errorf("%s %s reached %q as %q", c.method, c.path, parts[0], parts[1])
		}
		var a, b any
		_ = json.Unmarshal([]byte(parts[2]), &a)
		_ = json.Unmarshal([]byte(c.wantData), &b)
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if string(x) != string(y) {
			t.Errorf("%s %s: data %s, want %s", c.method, c.path, x, y)
		}
	}
}

func TestFoodNeedsAConfirmedEmail(t *testing.T) {
	srv := start(t)
	for _, path := range []string{"/v1/recommendations", "/v1/orders", "/v1/me/company", "/v1/me/group"} {
		if resp, _ := call(t, srv, "POST", path, "8", `{}`); resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s: status %d, want 403", path, resp.StatusCode)
		}
	}
}
