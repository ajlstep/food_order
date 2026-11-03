package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"food-order/catalog/internal/config"
	"food-order/catalog/internal/service"
	"food-order/catalog/internal/store"
	"food-order/shared/contracts/catalog"
	"food-order/shared/rpc"
)

type nobody struct{}

func (nobody) Publish(context.Context, string, any) error { return nil }

// call answers a request the way the service does on NATS.
func call(t *testing.T, routes map[string]rpc.Handler, subject, user string, in, out any) string {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	data, err := routes[subject](context.Background(), rpc.Request{V: rpc.Version, UserID: user, Data: raw})
	var refused *rpc.Error
	if errors.As(err, &refused) {
		return refused.Code
	}
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		b, _ := json.Marshal(data)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	return ""
}

func TestRequests(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Defaults()
	cfg.AnalysisEnabled, cfg.EmbedEnabled = false, false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	routes := New(service.New(st, nobody{}, cfg, log)).Routes(log)

	price := 63.0
	menu := catalog.MenuFetched{RequestID: "r1", Source: "parser", Days: []catalog.MenuDay{{Date: "2026-11-02", Items: []catalog.SourceItem{
		{Name: "LUNCH 1 Stand", Price: &price, Components: []catalog.Component{{Name: "Orez cu legume"}}},
	}}}}
	var imp catalog.ImportResponse
	if code := call(t, routes, catalog.SubjectMenuImport, "", menu, &imp); code != "" || imp.Days[0].Items != 1 {
		t.Fatalf("import %q %+v", code, imp)
	}

	var m catalog.MenuResponse
	if code := call(t, routes, catalog.SubjectMenuGet, "", catalog.MenuGetRequest{Date: "2026-11-02"}, &m); code != "" || len(m.Items) != 1 {
		t.Fatalf("menu %q %+v", code, m)
	}
	id := m.Items[0].ID

	var ds catalog.DishesResponse
	in := catalog.DishesGetRequest{IDs: []int64{id, 999}, Date: "2026-11-02"}
	if code := call(t, routes, catalog.SubjectDishesGet, "", in, &ds); code != "" || len(ds.Items) != 1 {
		t.Errorf("dishes %q %+v", code, ds)
	}
	if code := call(t, routes, catalog.SubjectMenuGet, "", catalog.MenuGetRequest{Date: "ieri"}, nil); code != "bad_date" {
		t.Errorf("bad date %q", code)
	}
	if code := call(t, routes, catalog.SubjectDishesGet, "", catalog.DishesGetRequest{}, nil); code != "bad_ids" {
		t.Errorf("no ids %q", code)
	}

	confirm := catalog.ConfirmAllergensRequest{DishID: id, Allergens: []string{"Gluten"}}
	if code := call(t, routes, catalog.SubjectAllergensConfirm, "", confirm, nil); code != rpc.CodeBadRequest {
		t.Errorf("confirm without a person %q", code)
	}
	var d catalog.Dish
	if code := call(t, routes, catalog.SubjectAllergensConfirm, "u3", confirm, &d); code != "" ||
		d.Allergens.Confirmed == nil || (*d.Allergens.Confirmed)[0] != "gluten" {
		t.Errorf("confirm %q %+v", code, d.Allergens)
	}
	noList := catalog.ConfirmAllergensRequest{DishID: id}
	if code := call(t, routes, catalog.SubjectAllergensConfirm, "u3", noList, nil); code != "bad_allergens" {
		t.Errorf("confirm without a list %q", code)
	}

	var sim catalog.SimilarResponse
	if code := call(t, routes, catalog.SubjectSimilar, "", catalog.SimilarRequest{LikedIDs: []int64{id}, CandidateIDs: []int64{id}}, &sim); code != "" || sim.Scores == nil {
		t.Errorf("similar %q %+v", code, sim)
	}
	if code := call(t, routes, catalog.SubjectMenuImport, "", map[string]any{"days": []any{}}, nil); code != "bad_menu" {
		t.Errorf("empty import %q", code)
	}
}
