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
// (menu not published yet, or the site layout changed).
var ErrEmpty = errors.New("menu is empty")

// ThrottledError is returned when the site was already asked very recently.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("site was asked recently, retry in %s", e.RetryAfter.Round(time.Second))
}
