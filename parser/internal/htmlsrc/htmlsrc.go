// Package htmlsrc is step v1 of the parser: plain HTTP + CSS selectors
// (goquery). It implements menu.Fetcher. If the site turns out to load the menu
// with JS, replace it with a JSON-endpoint fetcher (v2) or chromedp (v3);
// nothing outside this package has to change.
package htmlsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"food-order/parser/internal/menu"
)

const maxBody = 5 << 20 // 5 MiB is plenty for a menu page

// Selectors tells the parser where things are on the page.
// Name, Price, Category, Weight and Image are searched inside each Item element.
// Category can also come from headings that precede the items (CategoryHeading).
type Selectors struct {
	Item            string `json:"item"`
	Name            string `json:"name"`
	Price           string `json:"price,omitempty"`
	Category        string `json:"category,omitempty"`
	CategoryHeading string `json:"category_heading,omitempty"`
	Weight          string `json:"weight,omitempty"`
	Image           string `json:"image,omitempty"`
	ImageAttr       string `json:"image_attr,omitempty"` // default "src"; use "data-src" for lazy images
}

func (s Selectors) validate() error {
	if s.Item == "" || s.Name == "" {
		return errors.New(`selectors: "item" and "name" are required`)
	}
	return nil
}

// LoadSelectors reads selectors from a JSON file.
func LoadSelectors(path string) (Selectors, error) {
	var s Selectors
	b, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("selectors: %w (copy selectors.example.json to %s and adjust it to the site)", err, path)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("selectors %s: %w", path, err)
	}
	return s, s.validate()
}

// Fetcher downloads the page for a date and parses it.
type Fetcher struct {
	URLTemplate string // e.g. https://site.md/menu?day={date}
	DateLayout  string // Go layout for {date}; default 2006-01-02
	Sel         Selectors
	Client      *http.Client
	UserAgent   string
	Raw         menu.RawStore
	Log         *slog.Logger
}

func (f *Fetcher) Fetch(ctx context.Context, date time.Time) ([]menu.Item, error) {
	layout := f.DateLayout
	if layout == "" {
		layout = "2006-01-02"
	}
	pageURL := strings.ReplaceAll(f.URLTemplate, "{date}", date.Format(layout))
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("bad site url %q: %w", pageURL, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "ro,ru;q=0.8,en;q=0.5")

	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", pageURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("site answered %s for %s", resp.Status, pageURL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pageURL, err)
	}

	items, err := Parse(body, base, f.Sel)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 { // keep the raw page only for runs that worked
		if err := f.Raw.Save("last_ok.html", body); err != nil && f.Log != nil {
			f.Log.Warn("could not save raw page", "err", err)
		}
	}
	return items, nil
}

// Parse extracts the dishes from an HTML page. base is used to make image
// links absolute.
func Parse(body []byte, base *url.URL, sel Selectors) ([]menu.Item, error) {
	if err := sel.validate(); err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	imgAttr := sel.ImageAttr
	if imgAttr == "" {
		imgAttr = "src"
	}

	scope := sel.Item
	if sel.CategoryHeading != "" { // one pass, in document order: headings and items mixed
		scope = sel.CategoryHeading + ", " + sel.Item
	}

	var items []menu.Item
	category := ""
	doc.Find(scope).Each(func(_ int, s *goquery.Selection) {
		if sel.CategoryHeading != "" && s.Is(sel.CategoryHeading) {
			category = menu.CleanText(s.Text())
			return
		}
		name := menu.CleanText(s.Find(sel.Name).First().Text())
		if name == "" {
			return
		}
		it := menu.Item{Name: name, Category: category}
		if sel.Category != "" {
			if c := menu.CleanText(s.Find(sel.Category).First().Text()); c != "" {
				it.Category = c
			}
		}
		if sel.Price != "" {
			if p, ok := menu.ParsePrice(s.Find(sel.Price).First().Text()); ok {
				it.Price = p
			}
		}
		if sel.Weight != "" {
			it.Weight = menu.CleanText(s.Find(sel.Weight).First().Text())
		}
		if sel.Image != "" {
			if v, ok := s.Find(sel.Image).First().Attr(imgAttr); ok && strings.TrimSpace(v) != "" {
				it.ImageURL = resolve(base, strings.TrimSpace(v))
			}
		}
		items = append(items, it)
	})
	return items, nil
}

func resolve(base *url.URL, ref string) string {
	if base == nil {
		return ref
	}
	u, err := base.Parse(ref)
	if err != nil {
		return ref
	}
	return u.String()
}
