package bistro

import (
	"context"
	"errors"
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

func TestISOWeekAndWeekURL(t *testing.T) {
	for date, want := range map[string]string{
		"2026-10-30": "2026-W44", // the example from the site
		"2026-10-26": "2026-W44",
		"2026-11-02": "2026-W45",
		"2027-01-01": "2026-W53", // the ISO year of a date can be the year before
		"2026-01-05": "2026-W02",
	} {
		d, _ := time.Parse("2006-01-02", date)
		if got := ISOWeek(d); got != want {
			t.Errorf("ISOWeek(%s) = %s, want %s", date, got, want)
		}
	}
	d, _ := time.Parse("2006-01-02", "2026-10-30")
	if got := fill(WeekURL(DefaultURL), d); got != "https://bistro.md/ro/catalog?date=2026-10-30&week=2026-W44" {
		t.Errorf("week url = %s", got)
	}
	if got := WeekURL("http://x/menu"); got != "http://x/menu?week={week}" {
		t.Errorf("without a query: %s", got)
	}
	if got := WeekURL("http://x/?w={week}&d={date}"); got != "http://x/?w={week}&d={date}" {
		t.Errorf("a template with {week} is kept: %s", got)
	}
}

// pages: what the fake site answers with and without the week parameter.
func fakeSite(t *testing.T, plain, withWeek []byte) (*httptest.Server, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Query().Get("week") != "" {
			w.Write(withWeek)
			return
		}
		w.Write(plain)
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

var (
	emptyPage   = []byte(`<html><body><div id="_catalog-complex"></div></body></html>`)
	changedPage = []byte(`<div id="_catalog-complex"><div class="main-catalog__product"><p class=" product__name">LUNCH 1</p></div></div>`)
	day30       = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
)

func TestFetchWeekWhenDatePageIsEmpty(t *testing.T) {
	srv, asked := fakeSite(t, emptyPage, catalog(t))
	dir := t.TempDir()
	f := &Fetcher{URLTemplate: srv.URL + "/ro/catalog?date={date}", Raw: menu.RawStore{Dir: dir}}
	items, err := f.Fetch(context.Background(), day30)
	if err != nil || len(items) != 8 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	want := []string{"date=2026-10-30", "date=2026-10-30&week=2026-W44"}
	if !reflect.DeepEqual(*asked, want) {
		t.Errorf("asked %q, want %q", *asked, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "bistro_last_ok.html")); err != nil {
		t.Errorf("raw page of the week run not saved: %v", err)
	}
}

func TestFetchWeekWhenDatePageCannotBeParsed(t *testing.T) {
	srv, asked := fakeSite(t, changedPage, catalog(t))
	f := &Fetcher{URLTemplate: srv.URL + "/ro/catalog?date={date}"}
	items, err := f.Fetch(context.Background(), day30)
	if err != nil || len(items) != 8 || len(*asked) != 2 {
		t.Fatalf("items=%d err=%v asked=%q", len(items), err, *asked)
	}
}

func TestFetchNoWeekWhenDatePageWorks(t *testing.T) {
	srv, asked := fakeSite(t, catalog(t), emptyPage)
	f := &Fetcher{URLTemplate: srv.URL + "/ro/catalog?date={date}"}
	if items, err := f.Fetch(context.Background(), day30); err != nil || len(items) != 8 || len(*asked) != 1 {
		t.Fatalf("items=%d err=%v asked=%q", len(items), err, *asked)
	}
}

func TestFetchFirstAnswerStandsWhenWeekGivesNothing(t *testing.T) {
	// empty + empty: still "no menu"
	srv, asked := fakeSite(t, emptyPage, emptyPage)
	f := &Fetcher{URLTemplate: srv.URL + "/ro/catalog?date={date}"}
	if items, err := f.Fetch(context.Background(), day30); err != nil || len(items) != 0 || len(*asked) != 2 {
		t.Fatalf("items=%d err=%v asked=%q", len(items), err, *asked)
	}
	// changed + empty: the date page said "site changed", that stands
	srv, _ = fakeSite(t, changedPage, emptyPage)
	f = &Fetcher{URLTemplate: srv.URL + "/ro/catalog?date={date}"}
	if _, err := f.Fetch(context.Background(), day30); !errors.Is(err, menu.ErrParseFailed) {
		t.Fatalf("want parse failed, got %v", err)
	}
}

func TestFetchSiteDownIsNotAskedTwice(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	f := &Fetcher{URLTemplate: srv.URL + "/ro/catalog?date={date}"}
	if _, err := f.Fetch(context.Background(), day30); !errors.Is(err, menu.ErrSourceUnavailable) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestFetchCustomWeekURL(t *testing.T) {
	srv, asked := fakeSite(t, emptyPage, catalog(t))
	f := &Fetcher{URLTemplate: srv.URL + "/ro/catalog?date={date}", WeekURLTemplate: srv.URL + "/ro/catalog?week={week}&date={date}"}
	if items, err := f.Fetch(context.Background(), day30); err != nil || len(items) != 8 || (*asked)[1] != "week=2026-W44&date=2026-10-30" {
		t.Fatalf("items=%d err=%v asked=%q", len(items), err, *asked)
	}
}
