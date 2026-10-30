// Package menu holds the types shared by the parser: the dish, the Fetcher
// interface and the Service that adds caching and politeness on top of it.
package menu

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Item is one dish from the menu.
type Item struct {
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Price    float64 `json:"price"`
	Weight   string  `json:"weight,omitempty"`    // optional
	ImageURL string  `json:"image_url,omitempty"` // optional

	// Components are the parts of a set menu ("Orez cu legume", "Supă de linte"...).
	// Optional: sites that list single dishes leave it empty.
	Components []string `json:"components,omitempty"`
}

// Fetcher brings the menu for one day. Implementations can be swapped
// (plain HTML -> JSON endpoint -> headless browser) without touching the rest.
type Fetcher interface {
	Fetch(ctx context.Context, date time.Time) ([]Item, error)
}

// ErrEmpty means the site answered but no dishes were found
// (menu not published yet). It becomes the error code no_menu.
var ErrEmpty = errors.New("menu is empty")

// Errors a Fetcher wraps (fmt.Errorf("%w: ...")) so the caller can tell
// them apart; they become the error codes of food.menu.error.
var (
	// ErrSourceUnavailable: the site did not answer, answered with an error
	// status, or the answer could not be read (source_unavailable).
	ErrSourceUnavailable = errors.New("source unavailable")
	// ErrParseFailed: the page is there but does not look as expected any
	// more; the site probably changed (parse_failed).
	ErrParseFailed = errors.New("parse failed")
)

// ThrottledError is returned when the site was already asked very recently.
// Last is the outcome of that recent attempt (ErrEmpty or the fetch error),
// so a caller that cannot wait still knows what the site said.
type ThrottledError struct {
	RetryAfter time.Duration
	Last       error
}

func (e *ThrottledError) Error() string {
	msg := fmt.Sprintf("site was asked recently, retry in %s", e.RetryAfter.Round(time.Second))
	if e.Last != nil {
		msg += " (last time: " + e.Last.Error() + ")"
	}
	return msg
}

// Unwrap gives the outcome of the recent attempt (errors.Is(err, ErrEmpty) ...).
func (e *ThrottledError) Unwrap() error { return e.Last }
