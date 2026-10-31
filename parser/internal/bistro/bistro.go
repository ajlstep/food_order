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
//
// When the page of the date cannot be parsed or has no dishes, the same
// page with the ISO week is tried once: ?date=2026-10-30&week=2026-W44.
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

	"food-order/parser/internal/applog"
	"food-order/parser/internal/menu"
)

// DefaultURL is the catalog page; {date} is replaced with YYYY-MM-DD.
const DefaultURL = "https://bistro.md/ro/catalog?date={date}"

// WeekURL makes the second-chance page from the first one: the same URL
// with &week={week} (ISO week of the date, e.g. 2026-W44), as in
// https://bistro.md/ro/catalog?date=2026-10-30&week=2026-W44.
// A template that already has {week} is kept as it is.
func WeekURL(tmpl string) string {
	if strings.Contains(tmpl, "{week}") {
		return tmpl
	}
	sep := "&"
	if !strings.Contains(tmpl, "?") {
		sep = "?"
	}
	return tmpl + sep + "week={week}"
}

// ISOWeek formats the ISO week of a date as the site wants it: 2026-W44.
func ISOWeek(date time.Time) string {
	y, w := date.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// fill puts the date and its week into a URL template.
func fill(tmpl string, date time.Time) string {
	return strings.NewReplacer("{date}", date.Format("2006-01-02"), "{week}", ISOWeek(date)).Replace(tmpl)
}

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
	// WeekURLTemplate is the second chance, asked only when the first page
	// cannot be parsed or has no dishes. Default: WeekURL(URLTemplate).
	WeekURLTemplate string
	UserAgent       string
	Client          *http.Client
	Raw             menu.RawStore // keeps the page of the last successful run
	Log             *slog.Logger
}

// Fetch reads the menu of a date. If the page of the date cannot be parsed
// or has no dishes, the same page with the week (&week=2026-W44) is tried
// once; if that one gives no dishes either, the answer of the first page
// stands. A site that does not answer is not asked twice.
func (f *Fetcher) Fetch(ctx context.Context, date time.Time) ([]menu.Item, error) {
	tmpl := f.URLTemplate
	if tmpl == "" {
		tmpl = DefaultURL
	}
	pageURL := fill(tmpl, date)
	items, err := f.fetchPage(ctx, pageURL)
	if err == nil && len(items) > 0 {
		return items, nil
	}
	if err != nil && !errors.Is(err, menu.ErrParseFailed) {
		return nil, err // the site is down: asking again right away would not help
	}

	weekTmpl := f.WeekURLTemplate
	if weekTmpl == "" {
		weekTmpl = WeekURL(tmpl)
	}
	weekURL := fill(weekTmpl, date)
	if weekURL == pageURL {
		return items, err
	}
	why := "no dishes"
	if err != nil {
		why = err.Error()
	}
	f.log().InfoContext(ctx, "date page gave nothing, trying the week page", "date", date.Format("2006-01-02"), "why", why, "url", weekURL)
	weekItems, weekErr := f.fetchPage(ctx, weekURL)
	if weekErr == nil && len(weekItems) > 0 {
		return weekItems, nil
	}
	if weekErr != nil {
		f.log().WarnContext(ctx, "week page failed too", "url", weekURL, "err", weekErr)
	}
	return items, err
}

// fetchPage downloads one page and parses it; the raw page is kept only
// when it gave dishes.
func (f *Fetcher) fetchPage(ctx context.Context, pageURL string) ([]menu.Item, error) {
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
	start := time.Now()
	f.log().DebugContext(ctx, "site request", "url", pageURL, "user_agent", f.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		f.log().WarnContext(ctx, "site did not answer", "url", pageURL, "ms", applog.Since(start), "err", err)
		return nil, fmt.Errorf("%w: request %s: %w", menu.ErrSourceUnavailable, pageURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		f.log().WarnContext(ctx, "site answered with an error", "url", pageURL, "status", resp.StatusCode, "ms", applog.Since(start))
		return nil, fmt.Errorf("%w: site answered %s for %s", menu.ErrSourceUnavailable, resp.Status, pageURL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		f.log().WarnContext(ctx, "site page could not be read", "url", pageURL, "ms", applog.Since(start), "err", err)
		return nil, fmt.Errorf("%w: read %s: %w", menu.ErrSourceUnavailable, pageURL, err)
	}
	f.log().DebugContext(ctx, "site answered", "url", pageURL, "status", resp.StatusCode, "bytes", len(body),
		"content_type", resp.Header.Get("Content-Type"), "ms", applog.Since(start))

	items, err := Parse(body)
	if err != nil {
		f.log().ErrorContext(ctx, "site page not understood", "url", pageURL, "bytes", len(body), "err", err)
		return nil, err
	}
	f.log().DebugContext(ctx, "site page parsed", "url", pageURL, "dishes", len(items))
	if len(items) > 0 { // keep the raw page only for runs that worked
		if err := f.Raw.Save("bistro_last_ok.html", body); err != nil {
			f.log().WarnContext(ctx, "could not save raw page", "err", err)
		}
	}
	return items, nil
}

func (f *Fetcher) log() *slog.Logger {
	if f.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return f.Log
}

// Parse extracts the dishes from the catalog page.
//
// A page without any card gives (nil, nil): the menu is not published (the
// caller reports it as empty). Cards that give no dish at all mean the layout
// changed, which is an error.
func Parse(body []byte) ([]menu.Item, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: bistro: parse html: %w", menu.ErrParseFailed, err)
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
		return nil, fmt.Errorf("%w: bistro: found menu cards but no dishes in them; the site layout has probably changed", menu.ErrParseFailed)
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
