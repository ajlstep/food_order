package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"food-order/catalog/internal/bus"
	"food-order/catalog/internal/config"
	"food-order/catalog/internal/contracts"
	"food-order/catalog/internal/service"
	"food-order/catalog/internal/store"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Defaults()
	cfg.AnalysisEnabled, cfg.EmbedEnabled = false, false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(service.New(st, bus.Off{}, cfg, log), st, true, log))
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, user string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if user != "" {
		req.Header.Set(contracts.HeaderUserID, user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	return resp.StatusCode
}

func TestAPI(t *testing.T) {
	srv := newServer(t)
	if code := call(t, srv, "GET", "/healthz", "", nil, nil); code != 200 {
		t.Errorf("healthz %d", code)
	}

	price := 63.0
	menu := contracts.MenuFetched{RequestID: "r1", Source: "parser", Days: []contracts.MenuDay{{Date: "2026-11-02", Items: []contracts.SourceItem{
		{Name: "LUNCH 1 Stand", Price: &price, Components: []contracts.Component{{Name: "Orez cu legume"}}},
	}}}}
	var imp contracts.ImportResponse
	if code := call(t, srv, "POST", "/v1/import", "", menu, &imp); code != 200 || imp.Days[0].Items != 1 {
		t.Fatalf("import %d %+v", code, imp)
	}

	var m contracts.MenuResponse
	if code := call(t, srv, "GET", "/v1/menu?date=2026-11-02", "", nil, &m); code != 200 || len(m.Items) != 1 {
		t.Fatalf("menu %d %+v", code, m)
	}
	id := m.Items[0].ID
	ids := strconv.FormatInt(id, 10)

	var ds contracts.DishesResponse
	if code := call(t, srv, "GET", "/v1/dishes?ids="+ids+",999&date=2026-11-02", "", nil, &ds); code != 200 || len(ds.Items) != 1 {
		t.Errorf("dishes %d %+v", code, ds)
	}
	var d contracts.Dish
	if code := call(t, srv, "GET", "/v1/dishes/"+ids, "", nil, &d); code != 200 || d.Name != "LUNCH 1 Stand" {
		t.Errorf("dish %d %+v", code, d)
	}
	var eb contracts.ErrorBody
	if code := call(t, srv, "GET", "/v1/dishes/999", "", nil, &eb); code != 404 || eb.Error.Code != "not_found" {
		t.Errorf("unknown dish %d %+v", code, eb)
	}
	if code := call(t, srv, "GET", "/v1/menu?date=ieri", "", nil, &eb); code != 400 || eb.Error.Code != "bad_date" {
		t.Errorf("bad date %d %+v", code, eb)
	}
	if code := call(t, srv, "GET", "/v1/dishes?ids=a", "", nil, &eb); code != 400 || eb.Error.Code != "bad_ids" {
		t.Errorf("bad ids %d %+v", code, eb)
	}

	confirm := contracts.ConfirmAllergensRequest{Allergens: []string{"Gluten"}}
	if code := call(t, srv, "POST", "/v1/dishes/"+ids+"/allergens/confirm", "", confirm, &eb); code != 400 || eb.Error.Code != "missing_user" {
		t.Errorf("confirm without user %d %+v", code, eb)
	}
	if code := call(t, srv, "POST", "/v1/dishes/"+ids+"/allergens/confirm", "3", confirm, &d); code != 200 ||
		d.Allergens.Confirmed == nil || (*d.Allergens.Confirmed)[0] != "gluten" {
		t.Errorf("confirm %d %+v", code, d.Allergens)
	}
	if code := call(t, srv, "POST", "/v1/dishes/"+ids+"/allergens/confirm", "3", map[string]any{}, &eb); code != 400 || eb.Error.Code != "bad_allergens" {
		t.Errorf("confirm without list %d %+v", code, eb)
	}

	var sim contracts.SimilarResponse
	if code := call(t, srv, "POST", "/v1/similar", "", contracts.SimilarRequest{LikedIDs: []int64{id}, CandidateIDs: []int64{id}}, &sim); code != 200 || sim.Scores == nil {
		t.Errorf("similar %d %+v", code, sim)
	}
	if code := call(t, srv, "POST", "/v1/import", "", map[string]any{"days": []any{}}, &eb); code != 400 || eb.Error.Code != "bad_menu" {
		t.Errorf("empty import %d %+v", code, eb)
	}
	if code := call(t, srv, "GET", "/v1/nope", "", nil, &eb); code != 404 {
		t.Errorf("unknown route %d", code)
	}
}
