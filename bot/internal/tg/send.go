package tg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/applog"
	"food-order/bot/internal/services"
	"food-order/shared/contracts/users"
)

// sendTo sends an HTML message. markup may be nil, an inline keyboard,
// a reply keyboard or a keyboard removal.
func (b *Bot) sendTo(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) error {
	p := &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	}
	if markup != nil {
		p.ReplyMarkup = markup
	}
	start := time.Now()
	m, err := b.api.SendMessage(ctx, p)
	if err != nil {
		b.log.WarnContext(ctx, "telegram message not sent", "ms", applog.Since(start), "err", err)
		return err
	}
	b.log.DebugContext(ctx, "telegram message sent", "message_id", m.ID, "chars", len([]rune(text)),
		"markup", markup != nil, "ms", applog.Since(start))
	return nil
}

// sendPerson sends to a person. Somebody who blocked the bot is marked as
// such in users, so nobody tries again.
func (b *Bot) sendPerson(ctx context.Context, p person, text string, markup models.ReplyMarkup) error {
	err := b.sendTo(ctx, p.ChatID, text, markup)
	if errors.Is(err, bot.ErrorForbidden) {
		b.log.InfoContext(ctx, "the person blocked the bot", "user_id", p.UserID)
		if serr := b.users.SetChannelStatus(ctx, p.ExternalID, users.IdentityBlocked); serr != nil {
			return fmt.Errorf("marking the channel as blocked: %w", serr)
		}
		return nil // nothing more to do for this person
	}
	return err
}

// reply answers the person of the current update.
func (b *Bot) reply(ctx context.Context, text string) { b.replyWith(ctx, text, nil) }

func (b *Bot) replyWith(ctx context.Context, text string, markup models.ReplyMarkup) {
	if err := b.sendPerson(ctx, personFrom(ctx), text, markup); err != nil {
		b.log.ErrorContext(ctx, "send", "err", err)
	}
}

// fail logs the error and tells the person in one line what went wrong.
// A refusal (4xx) is a warning, anything else an error.
func (b *Bot) fail(ctx context.Context, what string, err error) {
	if refused(err) {
		b.log.WarnContext(ctx, what+" refused", "err", err)
	} else {
		b.log.ErrorContext(ctx, what+" failed", "err", err)
	}
	b.reply(ctx, "❌ "+userError(err))
}

func refused(err error) bool {
	var gwErr *services.GatewayError
	return errors.As(err, &gwErr) && gwErr.Status < 500
}

func userError(err error) string {
	var gwErr *services.GatewayError
	switch {
	case errors.As(err, &gwErr):
		return refusalText(gwErr)
	case errors.Is(err, context.DeadlineExceeded):
		return "Serviciul nu a răspuns la timp. Încearcă din nou puțin mai târziu."
	default:
		return "Nu a mers: serviciul nu răspunde."
	}
}

func commandArgs(u *models.Update) string {
	if u.Message == nil {
		return ""
	}
	_, rest, _ := strings.Cut(strings.TrimSpace(u.Message.Text), " ")
	return strings.TrimSpace(rest)
}
