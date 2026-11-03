package tg

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"food-order/bot/internal/settings"
	contracts "food-order/shared/contracts/core"
)

// settingsOf reads the settings of a person. Food commands only need them
// for the dates, so a failure falls back to the defaults.
func (b *Bot) settingsOf(ctx context.Context, p person) settings.Settings {
	saved, err := b.users.Settings(ctx, p.ExternalID)
	if err != nil {
		b.log.WarnContext(ctx, "settings, using the defaults", "err", err)
	}
	return settings.WithDefaults(saved)
}

// --- dates, in the user's time zone ---

func (b *Bot) today(s settings.Settings) time.Time {
	loc := s.Location()
	t := b.now().In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func (b *Bot) day(s settings.Settings, offset int) string {
	return b.today(s).AddDate(0, 0, offset).Format(dateLayout)
}

func (b *Bot) recommendationDay(s settings.Settings) string {
	return s.RecommendationDay(b.now()).Format(dateLayout)
}

// --- recommendations ---

func (b *Bot) rememberPicks(userID, date string, picks []contracts.Pick) {
	b.mu.Lock()
	defer b.mu.Unlock()
	byDate := b.picks[userID]
	if byDate == nil {
		byDate = map[string]map[int64]contracts.Pick{}
		b.picks[userID] = byDate
	}
	m := make(map[int64]contracts.Pick, len(picks))
	for _, p := range picks {
		m[p.DishID] = p
	}
	byDate[date] = m
	if len(byDate) > 7 { // keep only the last few days
		var dates []string
		for d := range byDate {
			dates = append(dates, d)
		}
		sort.Strings(dates)
		for _, d := range dates[:len(dates)-7] {
			delete(byDate, d)
		}
	}
}

func (b *Bot) showRecommendation(ctx context.Context, p person, date string, picks []contracts.Pick) error {
	names := make([]string, 0, len(picks))
	for _, pick := range picks {
		names = append(names, strconv.FormatInt(pick.DishID, 10)+" "+pick.Name)
	}
	b.log.InfoContext(ctx, "recommendation shown", "user_id", p.UserID, "date", date, "picks", len(picks), "dishes", names)
	if len(picks) == 0 {
		return b.sendPerson(ctx, p, fmt.Sprintf("Pentru %s nu am găsit nimic potrivit în meniu. 😕", esc(humanDate(date))), nil)
	}
	b.rememberPicks(p.UserID, date, picks)
	loc := b.settingsOf(ctx, p).Location()
	return b.sendPerson(ctx, p, recommendationText(date, picks, loc), recommendationKeyboard(date, picks, false))
}

func (b *Bot) findOrder(ctx context.Context, externalID string, st settings.Settings, id int64) *contracts.Order {
	orders, err := b.users.Orders(ctx, externalID, b.day(st, -14), b.day(st, 7))
	if err != nil {
		b.log.WarnContext(ctx, "orders for rating question", "err", err)
		return nil
	}
	for i := range orders {
		if orders[i].ID == id {
			return &orders[i]
		}
	}
	return nil
}
