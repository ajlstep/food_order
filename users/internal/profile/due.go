package profile

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"food-order/shared/contracts/users"
)

// DueCandidate is an active person who can get a message, with what they saved.
type DueCandidate struct {
	UserID string
	Saved  map[string]string
}

// DueItem is a person who has the order time now.
type DueItem struct {
	UserID  string
	ForDate string
}

// Due says who has the order time between at-window and at, and for which
// day their recommendation is. core asks this every few minutes; the window
// is there so that a stopped core catches up. Asking twice gives the same
// people: core checks whether the recommendation already exists.
func (s *Service) Due(ctx context.Context, at time.Time, window time.Duration) ([]DueItem, error) {
	people, err := s.store.DueCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("load candidates: %w", err)
	}
	var out []DueItem
	for _, p := range people {
		values := defaultSettings()
		for k := range values {
			if v, ok := p.Saved[k]; ok {
				values[k] = v
			}
		}
		if day, ok := dueDay(values, at, window); ok {
			out = append(out, DueItem{UserID: p.UserID, ForDate: day})
		}
	}
	return out, nil
}

// dueDay is the rule for one person. ok is false when it is not their time.
func dueDay(values map[string]string, at time.Time, window time.Duration) (day string, ok bool) {
	if values[users.SettingNotifyPersonal] == "false" {
		return "", false
	}
	loc, err := time.LoadLocation(values[users.SettingTimezone])
	if err != nil {
		return "", false
	}
	days := orderDays(values[users.SettingOrderDays])
	now := at.In(loc)
	if !slices.Contains(days, isoWeekday(now)) {
		return "", false
	}
	hour, minute, ok := splitTime(values[users.SettingOrderTime])
	if !ok {
		return "", false
	}
	order := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
	if order.After(now) || now.Sub(order) >= window {
		return "", false
	}
	if values[users.SettingRecFor] == "today" {
		return now.Format(time.DateOnly), true
	}
	// Today is an order day, so the loop finds one at the latest in 7 days.
	for i := 1; ; i++ {
		next := now.AddDate(0, 0, i)
		if slices.Contains(days, isoWeekday(next)) {
			return next.Format(time.DateOnly), true
		}
	}
}

func isoWeekday(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

func orderDays(v string) []int {
	var out []int
	for _, part := range strings.Split(v, ",") {
		if d, err := strconv.Atoi(part); err == nil {
			out = append(out, d)
		}
	}
	return out
}

func splitTime(v string) (hour, minute int, ok bool) {
	h, m, found := strings.Cut(v, ":")
	hour, err1 := strconv.Atoi(h)
	minute, err2 := strconv.Atoi(m)
	return hour, minute, found && err1 == nil && err2 == nil
}
