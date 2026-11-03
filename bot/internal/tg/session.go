package tg

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/applog"
	"food-order/bot/internal/services"
	"food-order/shared/contracts/users"
)

// Who is talking. Every update starts with the question "who is this?" to
// the gateway; the bot remembers nothing about the person between updates.

// person is who the bot talks to now: enough to reach them and to speak for
// them to the other services.
type person struct {
	UserID     string // the user_id of the whole project
	ExternalID string // the Telegram user id
	ChatID     int64
	Name       string
}

type personKey struct{}

func withPerson(ctx context.Context, p person) context.Context {
	return context.WithValue(ctx, personKey{}, p)
}

func personFrom(ctx context.Context) person {
	p, _ := ctx.Value(personKey{}).(person)
	return p
}

func updateChat(u *models.Update) (chatID int64, from *models.User, private bool) {
	switch {
	case u.Message != nil:
		return u.Message.Chat.ID, u.Message.From, u.Message.Chat.Type == models.ChatTypePrivate
	case u.CallbackQuery != nil && u.CallbackQuery.Message.Message != nil:
		m := u.CallbackQuery.Message.Message
		return m.Chat.ID, &u.CallbackQuery.From, m.Chat.Type == models.ChatTypePrivate
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID, &u.CallbackQuery.From, true
	}
	return 0, nil, false
}

func isCommand(text, name string) bool {
	cmd, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /start@my_bot
	return cmd == "/"+name
}

// resolveRequest is what the channel says about the person.
func resolveRequest(chat int64, from *models.User) users.ResolveRequest {
	return users.ResolveRequest{
		ChatRef:     strconv.FormatInt(chat, 10),
		Nickname:    from.Username,
		DisplayName: strings.TrimSpace(from.FirstName + " " + from.LastName),
		Language:    from.LanguageCode,
	}
}

// auth runs before every handler: only private chats, only allowed chats.
// Then it asks who the person is and sends them where their account allows:
// the commands for an active account, the sign-up for one still waiting for
// its email. The gateway applies the same rule; this is only how the bot
// shows it.
func (b *Bot) auth(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, api *bot.Bot, u *models.Update) {
		chat, from, private := updateChat(u)
		if chat == 0 || from == nil {
			b.log.DebugContext(ctx, "telegram update without a chat, ignored", "update_id", u.ID)
			return
		}
		start := time.Now()
		action, details := describeUpdate(u)
		ctx = applog.With(ctx, "update_id", u.ID, "req_id", "tg-"+strconv.FormatInt(u.ID, 10))
		defer func() {
			b.log.DebugContext(ctx, "telegram update handled", "action", action, "ms", applog.Since(start))
		}()
		if !private {
			b.log.InfoContext(ctx, "ignored update from a group")
			return
		}
		if len(b.allowed) > 0 && !b.allowed[chat] {
			b.log.InfoContext(ctx, "ignored update from a chat not in the allowed list")
			return
		}

		externalID := services.ExternalID(from.ID)
		who, err := b.users.Identify(ctx, externalID, resolveRequest(chat, from))
		if err != nil {
			b.log.ErrorContext(ctx, "identify", "err", err)
			b.answerCallback(ctx, u, "Nu a mers")
			_ = b.sendTo(ctx, chat, serviceDownText, nil)
			return
		}
		ctx = applog.With(ctx, "user_id", who.UserID, "account", who.Status)
		ctx = withPerson(ctx, person{UserID: who.UserID, ExternalID: externalID, ChatID: chat, Name: who.DisplayName})
		b.log.InfoContext(ctx, "user action", append([]any{"action", action}, details...)...)

		switch who.Status {
		case users.StatusActive:
			next(ctx, api, u)
		case users.StatusPending:
			b.onSignup(ctx, u)
		default: // blocked or deleted
			b.answerCallback(ctx, u, "")
			_ = b.sendTo(ctx, chat, accountClosedText, nil)
		}
	}
}

func (b *Bot) answerCallback(ctx context.Context, u *models.Update, text string) {
	if u.CallbackQuery != nil {
		_, _ = b.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: u.CallbackQuery.ID, Text: text})
	}
}
