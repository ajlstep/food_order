package tg

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/go-telegram/bot"

	"food-order/bot/internal/notify"
	contracts "food-order/shared/contracts/core"
	"food-order/shared/contracts/food"
	"food-order/shared/streams"
)

// What the bot does with a message from its queue (notify.Sender). The
// gateway already chose the person and the chat; here it only gets its words
// and buttons. The answers follow streams.Handler: an error means "try
// again", streams.ErrDrop means "this will never work".

// recipient makes a person from a queue message. Only private chats exist,
// so on Telegram the chat id and the user id are the same number.
func recipient(to notify.Recipient) (person, error) {
	chat, err := strconv.ParseInt(to.ChatRef, 10, 64)
	if err != nil {
		return person{}, fmt.Errorf("%w: chat_ref is not a chat id", streams.ErrDrop)
	}
	return person{UserID: to.UserID, ExternalID: to.ChatRef, ChatID: chat}, nil
}

// settle turns a refused message into a drop: Telegram will refuse the same
// message each time, while a network failure is worth another try.
func settle(err error) error {
	if errors.Is(err, bot.ErrorBadRequest) {
		return fmt.Errorf("%w: %v", streams.ErrDrop, err)
	}
	return err
}

// RecommendationReady sends the picks with their buttons.
func (b *Bot) RecommendationReady(ctx context.Context, to notify.Recipient, r contracts.RecommendationReady) error {
	p, err := recipient(to)
	if err != nil {
		return err
	}
	return settle(b.showRecommendation(ctx, p, r.Date, r.Picks))
}

// RatingRequested asks the person to rate an order.
func (b *Bot) RatingRequested(ctx context.Context, to notify.Recipient, r contracts.RatingRequested) error {
	p, err := recipient(to)
	if err != nil {
		return err
	}
	o := b.findOrder(ctx, p.ExternalID, b.settingsOf(ctx, p), r.OrderID) // only for the dish name; nil is fine
	b.log.InfoContext(ctx, "rating question sent", "user_id", p.UserID, "order", r.OrderID)
	return settle(b.sendPerson(ctx, p, ratingQuestion(o), ratingKeyboard(r.OrderID)))
}

// OrgInvite shows an invitation with its two buttons.
func (b *Bot) OrgInvite(ctx context.Context, to notify.Recipient, d food.OrgInvite) error {
	p, err := recipient(to)
	if err != nil {
		return err
	}
	return settle(b.sendPerson(ctx, p, inviteText(d.Kind, d.Name), inviteKeyboard(d.InviteID)))
}

// OrgNotice tells a person what changed in their company or group.
func (b *Bot) OrgNotice(ctx context.Context, to notify.Recipient, d food.OrgNotice) error {
	p, err := recipient(to)
	if err != nil {
		return err
	}
	return settle(b.sendPerson(ctx, p, noticeText(d), nil))
}

// Alert forwards an alert from any service to the admin chat.
func (b *Bot) Alert(ctx context.Context, a food.Alert) error {
	if b.admin == 0 {
		b.log.WarnContext(ctx, "alert (no admin chat set)", "service", a.Service, "level", a.Level, "text", a.Text)
		return nil
	}
	b.log.InfoContext(ctx, "alert forwarded to the admin chat", "from_service", a.Service, "alert_level", a.Level, "text", a.Text)
	return settle(b.sendTo(ctx, b.admin, alertText(a), nil))
}
