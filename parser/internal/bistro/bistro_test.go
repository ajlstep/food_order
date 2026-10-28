package bistro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"food-order/parser/internal/menu"
)

// testdata/Catalog.html is the real page https://bistro.md/ro/catalog?date=2026-10-27
// saved from the browser.
func catalog(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/Catalog.html")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseRealPage(t *testing.T) {
	got, err := Parse(catalog(t))
	if err != nil {
		t.Fatal(err)
	}

	// 4 cards x 2 sizes; the list view of the same cards must not add duplicates.
	type row struct {
		name  string
		price float64
	}
	wantRows := []row{
		{"LUNCH 1 Stand", 63}, {"LUNCH 1 Maxi", 75},
		{"LUNCH 2 Stand", 63}, {"LUNCH 2 Maxi", 75},
		{"DIETA Stand", 63}, {"DIETA Maxi", 75},
		{"POST Stand", 63}, {"POST Maxi", 75},
	}
	if len(got) != len(wantRows) {
		t.Fatalf("got %d items, want %d: %+v", len(got), len(wantRows), got)
	}
	for i, w := range wantRows {
		if got[i].Name != w.name || got[i].Price != w.price || got[i].Category != "Lunch Box" {
			t.Errorf("item %d = {%q %q %v}, want {%q Lunch Box %v}", i, got[i].Name, got[i].Category, got[i].Price, w.name, w.price)
		}
		if got[i].ImageURL != "" || got[i].Weight != "" {
			t.Errorf("item %d: pictures and weights are not read, got %+v", i, got[i])
		}
	}

	stand1 := []string{"File de pui cu ananas și cașc.", "Orez cu legume", `Salata rusească "Șuba"`, "Pâine complex"}
	if !reflect.DeepEqual(got[0].Components, stand1) {
		t.Errorf("LUNCH 1 Stand components = %q", got[0].Components)
	}
	// Maxi = soup + Stand
	if want := append([]string{"Supă de linte"}, stand1...); !reflect.DeepEqual(got[1].Components, want) {
		t.Errorf("LUNCH 1 Maxi components = %q, want %q", got[1].Components, want)
	}
	if got[5].Components[0] != "Supă cu arpacaș și roșii" || len(got[5].Components) != 5 {
		t.Errorf("DIETA Maxi components = %q", got[5].Components)
	}
	if got[7].Components[0] != "Supă-post de linte" || got[6].Components[1] != "Tocană cu ardei" {
		t.Errorf("POST components: stand=%q maxi=%q", got[6].Components, got[7].Components)
	}
}

func TestParseNoCardsIsEmptyNotError(t *testing.T) {
	got, err := Parse([]byte(`<html><body><div id="_catalog-complex"></div><p>Meniul va fi publicat</p></body></html>`))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	got, err = Parse([]byte(`<html><body></body></html>`))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestParseChangedLayoutIsError(t *testing.T) {
	page := `<div id="_catalog-complex"><div class="main-catalog__product"><p class=" product__name">LUNCH 1</p></div></div>`
	if _, err := Parse([]byte(page)); err == nil {
		t.Fatal("cards without dishes must be reported as an error")
	}
}

func TestParsePriceFallbackAndMissingSoup(t *testing.T) {
	page := `<div id="_catalog-complex"><h3 class="main-catalog__title">Lunch Box</h3>
<div class="main-catalog__product"><p class=" product__name">LUNCH 9</p>
 <div class="product__item_infos">
   <div class="buy-product__item" data-name="Stand."><span class="buy-product__name">Stand. <span>63,50</span> mdl</span></div>
   <div class="product_info_text">Ciorbă; Pâine</div></div>
 <div class="product__item_infos">
   <div class="buy-product__item" data-name="Maxi" data-price="75"></div>
   <div class="product_info_text">LUNCH 9 Stand. +</div></div>
</div></div>`
	got, err := Parse([]byte(page))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].Price != 63.5 {
		t.Errorf("price from the span fallback = %v", got[0].Price)
	}
	if !reflect.DeepEqual(got[1].Components, []string{"Ciorbă", "Pâine"}) || got[1].Price != 75 {
		t.Errorf("Maxi without a soup name = %+v", got[1])
	}
}

func TestFetch(t *testing.T) {
	page := catalog(t)
	var gotDate, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDate, gotUA = r.URL.Query().Get("date"), r.UserAgent()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := &Fetcher{URLTemplate: srv.URL + "/ro/catalog?date={date}", UserAgent: "test-agent/1.0", Raw: menu.RawStore{Dir: dir}}
	items, err := f.Fetch(context.Background(), time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	if err != nil || len(items) != 8 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	if gotDate != "2026-10-28" || gotUA != "test-agent/1.0" {
		t.Errorf("date=%q ua=%q", gotDate, gotUA)
	}
	if _, err := os.Stat(filepath.Join(dir, "bistro_last_ok.html")); err != nil {
		t.Errorf("raw page not saved: %v", err)
	}
}

func TestFetchBadStatusAndEmptyDoNotSaveRaw(t *testing.T) {
	status := 503
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != 200 {
			http.Error(w, "no", status)
			return
		}
		w.Write([]byte("<html><body>nimic</body></html>"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := &Fetcher{URLTemplate: srv.URL, Raw: menu.RawStore{Dir: dir}}
	if _, err := f.Fetch(context.Background(), time.Now()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected an error on 503, got %v", err)
	}
	status = 200
	if items, err := f.Fetch(context.Background(), time.Now()); err != nil || len(items) != 0 {
		t.Fatalf("empty page: %v %v", items, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("raw page must be kept only for successful runs, found %d files", len(entries))
	}
}
