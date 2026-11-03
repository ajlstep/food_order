package tg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	contracts "food-order/shared/contracts/core"
)

// onButton handles every inline button. What a button means is in its
// callback_data (see callback.go); the person comes from the context.
func (b *Bot) onButton(ctx context.Context, api *bot.Bot, u *models.Update) {
	q := u.CallbackQuery
	p := personFrom(ctx)
	answer := func(text string) {
		_, _ = api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text})
	}
	cb, err := decodeCallback(q.Data)
	if err != nil {
		b.log.WarnContext(ctx, "unknown button", "data", q.Data, "err", err)
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
	case kindDay, kindTime, kindToggle:
		b.settingsButton(ctx, api, q, cb, answer)

	case kindResend, kindOtherEmail: // a button left from the sign-up
		answer("Emailul e deja confirmat")
		clear()

	case kindInviteYes, kindInviteNo, kindKickCompany, kindKickGroup:
		b.orgButton(ctx, cb, answer, clear)

	case kindOrder:
		st := b.settingsOf(ctx, p)
		if cb.Date < b.day(st, 0) {
			b.log.InfoContext(ctx, "order button of a past day", "date", cb.Date, "dish_id", cb.DishID)
			answer("Recomandarea a expirat")
			clear()
			b.reply(ctx, "Recomandarea asta e pentru o zi trecută. Trimite /alege pentru cea de acum.")
			return
		}
		o, err := b.users.CreateOrder(ctx, p.ExternalID, contracts.CreateOrderRequest{
			Date: cb.Date, DishID: cb.DishID, Price: cb.Price, Forced: cb.Forced, Personal: st.Personal(),
		})
		if err != nil {
			answer("Nu a mers")
			b.fail(ctx, "create order", err)
			return
		}
		b.log.InfoContext(ctx, "dish chosen", "order", o.ID, "date", cb.Date, "dish_id", cb.DishID, "dish", o.DishName,
			"price", cb.Price, "forced", cb.Forced)
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
		b.reply(ctx, fmt.Sprintf("✅ Ai ales pentru %s: <b>%s</b>%s.%s", esc(humanDate(cb.Date)), esc(name), forced, destinationText(o)))

	case kindForced:
		answer("")
		if msg == nil || msg.ReplyMarkup == nil {
			return
		}
		b.log.DebugContext(ctx, "forced toggle", "date", cb.Date, "forced", cb.Forced)
		kb := toggleForced(*msg.ReplyMarkup, cb.Date, cb.Forced)
		_, err := api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID: msg.Chat.ID, MessageID: msg.ID, ReplyMarkup: kb,
		})
		if err != nil {
			b.log.WarnContext(ctx, "edit keyboard", "err", err)
		}

	case kindRate:
		b.mu.Lock()
		b.pending[p.UserID] = &pendingRating{orderID: cb.OrderID, rating: cb.Rating, until: b.now().Add(commentWait)}
		b.mu.Unlock()
		b.log.InfoContext(ctx, "stars given, waiting for a comment", "order", cb.OrderID, "rating", cb.Rating, "wait", commentWait)
		answer(fmt.Sprintf("%d⭐", cb.Rating))
		clear()
		b.replyWith(ctx, fmt.Sprintf("Ai dat %d⭐. Scrie un comentariu (ce a fost bun sau rău) sau apasă butonul:", cb.Rating),
			noCommentKeyboard(cb.OrderID))

	case kindNoNote:
		rating := b.takePending(p.UserID)
		if rating == nil || rating.orderID != cb.OrderID {
			b.log.InfoContext(ctx, "no-comment button for an expired rating", "order", cb.OrderID)
			answer("Nota a expirat, trimite /nota")
			clear()
			return
		}
		answer("")
		clear()
		b.submitRating(ctx, rating.orderID, rating.rating, "")

	case kindAllergen:
		pick, ok := b.findPick(ctx, p, cb.Date, cb.DishID)
		if !ok {
			b.log.InfoContext(ctx, "allergen button for a dish no longer known", "date", cb.Date, "dish_id", cb.DishID)
			answer("Nu mai găsesc felul, trimite /alege")
			return
		}
		var allergens []string // "no" = confirmed: none of the possible ones
		if cb.Yes {
			allergens = pick.AllergensPossible
		}
		if err := b.users.ConfirmAllergens(ctx, p.ExternalID, cb.DishID, allergens); err != nil {
			answer("Nu a mers")
			b.fail(ctx, "confirm allergens", err)
			return
		}
		b.log.InfoContext(ctx, "allergens confirmed", "dish_id", cb.DishID, "dish", pick.Name, "contains", cb.Yes)
		if cb.Yes {
			answer("Salvat: conține " + strings.Join(allergens, ", "))
		} else {
			answer("Salvat: nu conține")
		}
	}
}

// findPick looks in memory, and asks core again after a restart.
func (b *Bot) findPick(ctx context.Context, p person, date string, dishID int64) (contracts.Pick, bool) {
	b.mu.Lock()
	pick, ok := b.picks[p.UserID][date][dishID]
	b.mu.Unlock()
	if ok {
		return pick, true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := b.users.Recommendation(cctx, p.ExternalID, date)
	if err != nil {
		return pick, false
	}
	b.rememberPicks(p.UserID, date, r.Picks)
	for _, candidate := range r.Picks {
		if candidate.DishID == dishID {
			return candidate, true
		}
	}
	return pick, false
}
