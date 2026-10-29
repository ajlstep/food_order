package tg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/contracts"
)

// onButton handles every inline button. What a button means is in its
// callback_data (see callback.go); the user comes from the session.
func (b *Bot) onButton(ctx context.Context, api *bot.Bot, u *models.Update) {
	q := u.CallbackQuery
	s := sessionFrom(ctx)
	answer := func(text string) {
		_, _ = api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text})
	}
	cb, err := decodeCallback(q.Data)
	if err != nil {
		b.log.Warn("button", "err", err)
		answer("Buton necunoscut")
		return
	}
	msg := q.Message.Message // nil if the message is too old for Telegram
	clear := func() {
		if msg != nil {
			_, _ = api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
				ChatID: msg.Chat.ID, MessageID: msg.ID, ReplyMarkup: emptyKeyboard(),
			})
		}
	}

	switch cb.Kind {
	case kindDay, kindTime:
		b.settingsButton(ctx, api, q, cb)

	case kindInvite, kindKick, kindLeave:
		b.groupButton(ctx, q, cb, answer, clear)

	case kindOrder:
		if cb.Date < b.day(s.settings, 0) {
			answer("Recomandarea a expirat")
			clear()
			b.reply(ctx, "Recomandarea asta e pentru o zi trecută. Trimite /alege pentru cea de acum.")
			return
		}
		o, err := b.core.CreateOrder(ctx, s.user.ID, contracts.CreateOrderRequest{
			Date: cb.Date, DishID: cb.DishID, Price: cb.Price, Forced: cb.Forced,
		})
		if err != nil {
			answer("Nu a mers")
			b.fail(ctx, "create order", err)
			return
		}
		answer("Salvat")
		clear()
		name := o.DishName
		if name == "" {
			name = "felul #" + strconv.FormatInt(cb.DishID, 10)
		}
		forced := ""
		if cb.Forced {
			forced = " (din lipsă)"
		}
		b.reply(ctx, fmt.Sprintf("✅ Ai ales pentru %s: <b>%s</b>%s.", esc(humanDate(cb.Date)), esc(name), forced))

	case kindForced:
		answer("")
		if msg == nil || msg.ReplyMarkup == nil {
			return
		}
		kb := toggleForced(*msg.ReplyMarkup, cb.Date, cb.Forced)
		_, err := api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID: msg.Chat.ID, MessageID: msg.ID, ReplyMarkup: kb,
		})
		if err != nil {
			b.log.Warn("edit keyboard", "err", err)
		}

	case kindRate:
		b.mu.Lock()
		b.pending[s.user.ID] = &pendingRating{orderID: cb.OrderID, rating: cb.Rating, until: b.now().Add(commentWait)}
		b.mu.Unlock()
		answer(fmt.Sprintf("%d⭐", cb.Rating))
		clear()
		b.replyWith(ctx, fmt.Sprintf("Ai dat %d⭐. Scrie un comentariu (ce a fost bun sau rău) sau apasă butonul:", cb.Rating),
			noCommentKeyboard(cb.OrderID))

	case kindNoNote:
		p := b.takePending(s.user.ID)
		if p == nil || p.orderID != cb.OrderID {
			answer("Nota a expirat, trimite /nota")
			clear()
			return
		}
		answer("")
		clear()
		b.submitRating(ctx, p.orderID, p.rating, "")

	case kindAllergen:
		pick, ok := b.findPick(ctx, s.user.ID, cb.Date, cb.DishID)
		if !ok {
			answer("Nu mai găsesc felul, trimite /alege")
			return
		}
		var allergens []string // "no" = confirmed: none of the possible ones
		if cb.Yes {
			allergens = pick.AllergensPossible
		}
		if err := b.catalog.ConfirmAllergens(ctx, s.user.ID, cb.DishID, allergens); err != nil {
			answer("Nu a mers")
			b.fail(ctx, "confirm allergens", err)
			return
		}
		if cb.Yes {
			answer("Salvat: conține " + strings.Join(allergens, ", "))
		} else {
			answer("Salvat: nu conține")
		}
	}
}

// findPick looks in memory, and asks core again after a restart.
func (b *Bot) findPick(ctx context.Context, userID int64, date string, dishID int64) (contracts.Pick, bool) {
	b.mu.Lock()
	pick, ok := b.picks[userID][date][dishID]
	b.mu.Unlock()
	if ok {
		return pick, true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := b.core.Recommendation(cctx, userID, date)
	if err != nil {
		return pick, false
	}
	b.rememberPicks(userID, date, r.Picks)
	for _, p := range r.Picks {
		if p.DishID == dishID {
			return p, true
		}
	}
	return pick, false
}
