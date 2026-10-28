package htmlsrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"food-order/parser/internal/menu"
)

var testSel = Selectors{
	Item: ".dish", Name: ".title", Price: ".price", Weight: ".weight",
	Image: "img", CategoryHeading: "h2.cat",
}

func TestParse(t *testing.T) {
	body, err := os.ReadFile("testdata/menu.html")
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://site.example.md/menu?day=2026-10-28")
	got, err := Parse(body, base, testSel)
	if err != nil {
		t.Fatal(err)
	}
	want := []menu.Item{
		{Name: "Ciorbă de pui", Category: "Supe", Price: 32.5, Weight: "300 g", ImageURL: "https://site.example.md/img/ciorba.jpg"},
		{Name: "Şnitel cu piure", Category: "Felul doi", Price: 58, Weight: "250 g", ImageURL: "https://cdn.example.md/snitel.jpg"},
		{Name: "Pește la cuptor", Category: "Felul doi", Price: 70},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("item %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestParseNoMatchesIsEmptyNotError(t *testing.T) {
	got, err := Parse([]byte("<html><body><p>Meniul va fi publicat</p></body></html>"), nil, testSel)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestParseNeedsSelectors(t *testing.T) {
	if _, err := Parse([]byte("<html></html>"), nil, Selectors{}); err == nil {
		t.Fatal("expected an error for empty selectors")
	}
}

func TestFetchSendsDateUserAgentAndSavesRaw(t *testing.T) {
	page, _ := os.ReadFile("testdata/menu.html")
	var gotDay, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDay, gotUA = r.URL.Query().Get("day"), r.UserAgent()
		w.Write(page)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := &Fetcher{URLTemplate: srv.URL + "/menu?day={date}", Sel: testSel, UserAgent: "test-agent/1.0", Raw: menu.RawStore{Dir: dir}}
	items, err := f.Fetch(context.Background(), time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	if gotDay != "2026-10-28" || gotUA != "test-agent/1.0" {
		t.Errorf("day=%q ua=%q", gotDay, gotUA)
	}
	if _, err := os.Stat(filepath.Join(dir, "last_ok.html")); err != nil {
		t.Errorf("raw page not saved: %v", err)
	}
}

func TestFetchBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 503) }))
	defer srv.Close()
	f := &Fetcher{URLTemplate: srv.URL, Sel: testSel}
	if _, err := f.Fetch(context.Background(), time.Now()); err == nil {
		t.Fatal("expected an error on 503")
	}
}
