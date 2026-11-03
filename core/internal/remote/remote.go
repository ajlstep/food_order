// Package remote is how core asks the other services, over NATS. It holds no
// rules: one method, one request, and a time limit so a service that does
// not answer cannot hold core up.
package remote

import (
	"context"
	"time"

	"food-order/shared/contracts/catalog"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
)

// Caller sends a request to a service (rpc.Client does it).
type Caller interface {
	CallFor(ctx context.Context, subject, requestID, userID string, in, out any) error
}

// Client asks catalog and users.
type Client struct {
	caller  Caller
	timeout time.Duration
}

// New makes a client; timeout is how long a service may take to answer.
func New(caller Caller, timeout time.Duration) *Client {
	return &Client{caller: caller, timeout: timeout}
}

func (c *Client) call(ctx context.Context, subject, userID string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.caller.CallFor(ctx, subject, events.NewID(time.Now()), userID, in, out)
}

// --- catalog ---

// Menu returns the dishes of a day.
func (c *Client) Menu(ctx context.Context, date string) ([]catalog.Dish, error) {
	var out catalog.MenuResponse
	err := c.call(ctx, catalog.SubjectMenuGet, "", catalog.MenuGetRequest{Date: date}, &out)
	return out.Items, err
}

// Dishes returns dishes by id, with the values of the menu of date.
func (c *Client) Dishes(ctx context.Context, ids []int64, date string) ([]catalog.Dish, error) {
	var out catalog.DishesResponse
	err := c.call(ctx, catalog.SubjectDishesGet, "", catalog.DishesGetRequest{IDs: ids, Date: date}, &out)
	return out.Items, err
}

// Similar returns how much each candidate looks like the liked dishes (0..1).
func (c *Client) Similar(ctx context.Context, liked, candidates []int64) (map[int64]float64, error) {
	var out catalog.SimilarResponse
	err := c.call(ctx, catalog.SubjectSimilar, "", catalog.SimilarRequest{LikedIDs: liked, CandidateIDs: candidates}, &out)
	scores := make(map[int64]float64, len(out.Scores))
	for _, s := range out.Scores {
		scores[s.ID] = s.Score
	}
	return scores, err
}

// --- users ---

// Food returns the allergens and preferences of a person.
func (c *Client) Food(ctx context.Context, userID string) (users.FoodResponse, error) {
	var out users.FoodResponse
	err := c.call(ctx, users.SubjectProfileFoodGet, userID, nil, &out)
	return out, err
}

// Due returns who has the order time now, and for which day.
func (c *Client) Due(ctx context.Context, at time.Time, window time.Duration) ([]users.DueItem, error) {
	var out users.DueResponse
	in := users.DueRequest{AtMs: at.UnixMilli(), WindowMinutes: int(window.Minutes())}
	err := c.call(ctx, users.SubjectProfileDue, "", in, &out)
	return out.Items, err
}

// Snapshot returns where an order of this person goes now.
func (c *Client) Snapshot(ctx context.Context, userID string) (users.SnapshotResponse, error) {
	var out users.SnapshotResponse
	err := c.call(ctx, users.SubjectOrgSnapshot, userID, nil, &out)
	return out, err
}

// Company returns the company of a person.
func (c *Client) Company(ctx context.Context, userID string) (users.Company, error) {
	var out users.Company
	err := c.call(ctx, users.SubjectOrgCompanyGet, userID, nil, &out)
	return out, err
}
