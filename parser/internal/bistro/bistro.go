// Package bistro reads the menu of https://bistro.md (page /ro/catalog?date=YYYY-MM-DD).
//
// The page is plain server-rendered HTML, so one GET + CSS selectors is enough.
// Every other site gets its own package next to this one and implements the
// same menu.Fetcher.
//
// How the page looks (checked on the page saved on 2026-10-27):
//
//	#_catalog-complex                       the grid view (the only one we read)
//	  .main-catalog__title                  section title, e.g. "Lunch Box"
//	  .main-catalog__product                one card: LUNCH 1, LUNCH 2, DIETA, POST
//	    p.product__name                     card name
//	    .product__item_infos                one per size (Stand., Maxi)
//	      .buy-product__item                data-name="Stand." data-price="63"
//	      .product_info_text                Stand.: "dish; dish; dish; bread"
//	                                        Maxi:   "LUNCH 1 Stand. + Soup name"
//	#_catalog-complex_list                  the same cards again (list view): ignored,
//	                                        otherwise every dish would be returned twice
//
// Each size becomes its own menu.Item ("LUNCH 1 Stand", "LUNCH 1 Maxi"): in the
// data they are two different dishes, even though on the plate Maxi is Stand + soup.
// The components of Maxi are the soup followed by the components of Stand.
// Pictures and stock are not read.
package bistro

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"food-order/parser/internal/menu"
)

// DefaultURL is the catalog page; {date} is replaced with YYYY-MM-DD.
const DefaultURL = "https://bistro.md/ro/catalog?date={date}"

const (
	maxBody = 5 << 20 // 5 MiB is plenty for one page

	selSection  = "#_catalog-complex"
	selTitle    = ".main-catalog__title"
	selCard     = ".main-catalog__product"
	selName     = ".product__name"
	selVariant  = ".product__item_infos"
	selBuy      = ".buy-product__item"
	selBuyPrice = ".buy-product__name span" // fallback if data-price is missing
	selInfoText = ".product_info_text"
)

// Fetcher downloads the catalog page for a date and parses it.
type Fetcher struct {
	URLTemplate string // default DefaultURL
	UserAgent   string
	Client      *http.Client
	Raw         menu.RawStore // keeps the page of the last successful run
	Log         *slog.Logger
}

func (f *Fetcher) Fetch(ctx context.Context, date time.Time) ([]menu.Item, error) {
	tmpl := f.URLTemplate
	if tmpl == "" {
		tmpl = DefaultURL
	}
	pageURL := strings.ReplaceAll(tmpl, "{date}", date.Format("2006-01-02"))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bad site url %q: %w", pageURL, err)
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

	items, err := Parse(body)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 { // keep the raw page only for runs that worked
		if err := f.Raw.Save("bistro_last_ok.html", body); err != nil && f.Log != nil {
			f.Log.Warn("could not save raw page", "err", err)
		}
	}
	return items, nil
}

// Parse extracts the dishes from the catalog page.
//
// A page without any card gives (nil, nil): the menu is not published (the
// caller reports it as empty). Cards that give no dish at all mean the layout
// changed, which is an error.
func Parse(body []byte) ([]menu.Item, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bistro: parse html: %w", err)
	}

	var items []menu.Item
	cards := 0
	doc.Find(selSection).Each(func(_ int, section *goquery.Selection) {
		category := menu.CleanText(section.Find(selTitle).First().Text())
		section.Find(selCard).Each(func(_ int, card *goquery.Selection) {
			cards++
			items = append(items, parseCard(card, category)...)
		})
	})

	if cards > 0 && len(items) == 0 {
		return nil, errors.New("bistro: found menu cards but no dishes in them; the site layout has probably changed")
	}
	return items, nil
}

type variant struct {
	name  string // "Stand", "Maxi"
	price float64
	text  string // the .product_info_text of this size
}

func parseCard(card *goquery.Selection, category string) []menu.Item {
	slot := menu.CleanText(card.Find(selName).First().Text())
	if slot == "" {
		return nil
	}

	var variants []variant
	card.Find(selVariant).Each(func(_ int, v *goquery.Selection) {
		buy := v.Find(selBuy).First()
		name, _ := buy.Attr("data-name")
		name = strings.TrimRight(menu.CleanText(name), ".")
		if name == "" {
			return
		}
		price, ok := menu.ParsePrice(attr(buy, "data-price"))
		if !ok {
			price, _ = menu.ParsePrice(buy.Find(selBuyPrice).First().Text())
		}
		variants = append(variants, variant{
			name:  name,
			price: price,
			text:  menu.CleanText(v.Find(selInfoText).First().Text()),
		})
	})

	// The base set is the first size whose text is a plain list (no "+ soup").
	var base []string
	for _, v := range variants {
		if _, extended := splitExtended(v.text, slot); !extended {
			base = splitList(v.text)
			break
		}
	}

	items := make([]menu.Item, 0, len(variants))
	for _, v := range variants {
		var components []string
		if soup, extended := splitExtended(v.text, slot); extended {
			if soup != "" {
				components = append(components, soup)
			}
			components = append(components, base...)
		} else {
			components = splitList(v.text)
		}
		items = append(items, menu.Item{
			Name:       slot + " " + v.name,
			Category:   category,
			Price:      v.price,
			Components: components,
		})
	}
	return items
}

// splitExtended recognises "LUNCH 1 Stand. + Supă de linte": the text starts
// with the card name and has a "+" after it. It returns the part after the "+".
func splitExtended(text, slot string) (soup string, extended bool) {
	if !strings.HasPrefix(menu.NormName(text), menu.NormName(slot)) {
		return "", false
	}
	_, after, found := strings.Cut(text, "+")
	if !found {
		return "", false
	}
	return menu.CleanText(after), true
}

// splitList splits "a; b; c" into its parts.
func splitList(text string) []string {
	var out []string
	for _, p := range strings.Split(text, ";") {
		if p = menu.CleanText(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func attr(s *goquery.Selection, name string) string {
	v, _ := s.Attr(name)
	return v
}
