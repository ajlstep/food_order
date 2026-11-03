package tg

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/services"
	contracts "food-order/shared/contracts/core"
)

// Food commands: everything goes to the gateway, for the person of the update.

func (b *Bot) onHelp(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	b.reply(ctx, helpText)
}

func (b *Bot) onMenu(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	p := personFrom(ctx)
	date := b.recommendationDay(b.settingsOf(ctx, p))
	if err := b.users.RequestRecommendation(ctx, p.ExternalID, date); err != nil {
		b.fail(ctx, "request recommendation", err)
		return
	}
	b.log.InfoContext(ctx, "recommendation requested by the user", "date", date)
	b.reply(ctx, fmt.Sprintf("⏳ Am cerut recomandarea pentru %s. Îți scriu când e gata.", esc(humanDate(date))))
}

func (b *Bot) onChoose(ctx context.Context, _ *bot.Bot, u *models.Update) {
	p := personFrom(ctx)
	st := b.settingsOf(ctx, p)
	date := b.recommendationDay(st)
	if a := strings.ToLower(commandArgs(u)); a == "azi" || a == "today" {
		date = b.day(st, 0)
	}
	r, err := b.users.Recommendation(ctx, p.ExternalID, date)
	switch {
	case services.IsNotFound(err):
		b.reply(ctx, fmt.Sprintf("Nu am recomandare pentru %s. Trimite /meniu.", esc(humanDate(date))))
		return
	case err != nil:
		b.fail(ctx, "get recommendation", err)
		return
	}
	switch r.Status {
	case "", "ready":
		if err := b.showRecommendation(ctx, p, date, r.Picks); err != nil {
			b.log.ErrorContext(ctx, "send recommendation", "err", err)
		}
	case "waiting_menu":
		b.reply(ctx, "⏳ Încă aștept meniul de la bucătărie. Îți scriu când e gata.")
	case "generating":
		b.reply(ctx, "⏳ Recomandarea se pregătește. Îți scriu când e gata.")
	default:
		b.reply(ctx, "Recomandarea are starea „"+esc(r.Status)+"”.")
	}
}

func (b *Bot) onRate(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	p := personFrom(ctx)
	st := b.settingsOf(ctx, p)
	today := b.day(st, 0)
	orders, err := b.users.Orders(ctx, p.ExternalID, b.day(st, -14), today)
	if err != nil {
		b.fail(ctx, "orders", err)
		return
	}
	// the latest order without a rating, not in the future
	var last *contracts.Order
	for i := range orders {
		o := &orders[i]
		if o.Rating != nil || o.Date > today {
			continue
		}
		if last == nil || o.Date > last.Date || o.Date == last.Date && o.ID > last.ID {
			last = o
		}
	}
	if last == nil {
		b.reply(ctx, "Nu ai mese nenotate în ultimele 2 săptămâni. 👍")
		return
	}
	b.replyWith(ctx, ratingQuestion(last), ratingKeyboard(last.ID))
}

func (b *Bot) onHistory(ctx context.Context, _ *bot.Bot, u *models.Update) {
	p := personFrom(ctx)
	st := b.settingsOf(ctx, p)
	days := 30
	if n, err := strconv.Atoi(commandArgs(u)); err == nil && n > 0 && n <= 366 {
		days = n
	}
	orders, err := b.users.Orders(ctx, p.ExternalID, b.day(st, -days), b.day(st, 7))
	if err != nil {
		b.fail(ctx, "orders", err)
		return
	}
	sort.SliceStable(orders, func(i, j int) bool {
		if orders[i].Date != orders[j].Date {
			return orders[i].Date > orders[j].Date
		}
		return orders[i].ID > orders[j].ID
	})
	b.reply(ctx, historyText(orders, 15))
}

func (b *Bot) onStats(ctx context.Context, _ *bot.Bot, u *models.Update) {
	p := personFrom(ctx)
	settings := b.settingsOf(ctx, p)
	today := b.today(settings)
	from := today.AddDate(0, 0, -today.Day()+1).Format(dateLayout) // 1st of the month
	if n, err := strconv.Atoi(commandArgs(u)); err == nil && n > 0 && n <= 366 {
		from = b.day(settings, -(n - 1))
	}
	to := today.Format(dateLayout)
	st, err := b.users.Stats(ctx, p.ExternalID, from, to)
	if err != nil {
		b.fail(ctx, "stats", err)
		return
	}
	if st.From == "" {
		st.From = from
	}
	if st.To == "" {
		st.To = to
	}
	b.reply(ctx, statsText(st))
}

// onText gets every message that is not a known command: a shared phone
// number, the comment after a rating, or something else (-> help).
func (b *Bot) onText(ctx context.Context, _ *bot.Bot, u *models.Update) {
	if u.Message == nil {
		return
	}
	if u.Message.Contact != nil {
		b.onContact(ctx, u)
		return
	}
	text := strings.TrimSpace(u.Message.Text)
	if text == "" {
		return
	}
	if text == btnNotNow {
		b.replyWith(ctx, "Bine. Îl poți trimite oricând din /profil.", removeKeyboard())
		return
	}
	if !strings.HasPrefix(text, "/") {
		if p := b.takePending(personFrom(ctx).UserID); p != nil {
			b.submitRating(ctx, p.orderID, p.rating, text)
			return
		}
	}
	b.log.DebugContext(ctx, "text not understood, help sent")
	b.reply(ctx, helpText)
}

func (b *Bot) takePending(userID string) *pendingRating {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pending[userID]
	delete(b.pending, userID)
	if p == nil || b.now().After(p.until) {
		return nil
	}
	return p
}

func (b *Bot) submitRating(ctx context.Context, orderID int64, rating int, comment string) {
	if err := b.users.Rate(ctx, personFrom(ctx).ExternalID, orderID, rating, comment); err != nil {
		b.fail(ctx, "rate", err)
		return
	}
	b.log.InfoContext(ctx, "rating saved", "order", orderID, "rating", rating)
	b.reply(ctx, fmt.Sprintf("✅ Am salvat nota %d⭐. Mulțumesc!", rating))
}
