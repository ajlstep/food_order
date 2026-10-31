package tg

import (
	"context"
	"errors"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/settings"
	"food-order/bot/internal/store"
)

// Connecting, profile and settings: this is the bot's own data (bot.db).

// onStart registers a new user, or brings back one that sent /stop.
func (b *Bot) onStart(ctx context.Context, _ *bot.Bot, u *models.Update) {
	chat, from, _ := updateChat(u)
	usr, created, err := b.users.Register(ctx, profileOf(chat, from))
	if err != nil {
		b.log.ErrorContext(ctx, "register", "err", err)
		_ = b.sendTo(ctx, chat, "❌ Nu am putut să te conectez. Încearcă din nou puțin mai târziu.", nil)
		return
	}
	st, err := b.users.Settings(ctx, usr.ID)
	if err != nil {
		st = settings.WithDefaults(nil)
	}
	ctx = withSession(ctx, session{user: usr, settings: st})

	if created {
		b.log.InfoContext(ctx, "new user", "user_id", usr.ID, "username", usr.Username, "first_name", usr.FirstName,
			"last_name", usr.LastName, "chat_id", usr.ChatID)
		var markup models.ReplyMarkup
		if usr.Phone == "" {
			markup = phoneKeyboard()
		}
		b.replyWith(ctx, welcomeText(usr, st), markup)
		return
	}
	b.log.InfoContext(ctx, "user came back", "user_id", usr.ID, "username", usr.Username, "status", usr.Status)
	b.reply(ctx, welcomeBackText(usr))
}

func (b *Bot) onStop(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	s := sessionFrom(ctx)
	if err := b.users.SetStatus(ctx, s.user.ID, store.StatusStopped); err != nil {
		b.fail(ctx, "stop", err)
		return
	}
	b.log.InfoContext(ctx, "user stopped the automatic recommendations", "user_id", s.user.ID)
	b.reply(ctx, stoppedText)
}

func (b *Bot) onProfile(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	s := sessionFrom(ctx)
	var markup models.ReplyMarkup
	if s.user.Phone == "" {
		markup = phoneKeyboard()
	}
	b.replyWith(ctx, profileText(s.user), markup)
}

// onContact saves the phone number shared with the "Trimite numărul" button.
func (b *Bot) onContact(ctx context.Context, u *models.Update) {
	s := sessionFrom(ctx)
	c := u.Message.Contact
	if u.Message.From == nil || c.UserID != u.Message.From.ID {
		b.log.InfoContext(ctx, "someone else's contact refused", "contact_user_id", c.UserID)
		b.replyWith(ctx, "Trimite numărul tău cu butonul de jos, nu contactul altcuiva.", phoneKeyboard())
		return
	}
	phone := strings.TrimSpace(c.PhoneNumber)
	if phone != "" && !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	if err := b.users.SetPhone(ctx, s.user.ID, phone); err != nil {
		b.fail(ctx, "set phone", err)
		return
	}
	b.log.InfoContext(ctx, "phone saved", "user_id", s.user.ID, "phone", phone)
	b.replyWith(ctx, "✅ Am salvat numărul "+esc(phone)+".", removeKeyboard())
}

// onSettings: "/setari" shows the settings with buttons;
// "/setari <key> <value>" changes one.
func (b *Bot) onSettings(ctx context.Context, _ *bot.Bot, u *models.Update) {
	s := sessionFrom(ctx)
	args := commandArgs(u)
	if args == "" {
		b.replyWith(ctx, settingsText(s.settings), settingsKeyboard(s.settings))
		return
	}
	word, value, _ := strings.Cut(args, " ")
	def, err := settings.Find(word)
	if errors.Is(err, settings.ErrUnknown) {
		b.log.InfoContext(ctx, "unknown setting", "key", word)
		b.replyWith(ctx, "Nu știu setarea „"+esc(word)+"”.\n\n"+settingsText(s.settings), settingsKeyboard(s.settings))
		return
	}
	canon, err := def.Parse(value)
	if err != nil {
		b.log.InfoContext(ctx, "bad setting value", "key", def.Key, "value", value, "err", err)
		b.reply(ctx, "❌ "+esc(err.Error())+"\nExemplu: <code>"+esc(def.Help)+"</code>")
		return
	}
	if err := b.users.SetSetting(ctx, s.user.ID, def.Key, canon); err != nil {
		b.fail(ctx, "set setting", err)
		return
	}
	b.log.InfoContext(ctx, "setting changed", "user_id", s.user.ID, "key", def.Key, "old", s.settings[def.Key], "new", canon)
	s.settings[def.Key] = canon
	b.replyWith(ctx, "✅ "+esc(def.Label)+": <b>"+esc(def.Format(canon))+"</b>\n\n"+settingsText(s.settings), settingsKeyboard(s.settings))
}

// settingsButton handles the day toggles and the ±30 min buttons, then
// redraws the same message.
func (b *Bot) settingsButton(ctx context.Context, api *bot.Bot, q *models.CallbackQuery, cb Callback) {
	s := sessionFrom(ctx)
	var key, value, note string
	switch cb.Kind {
	case kindDay:
		key = settings.KeyOrderDays
		value = settings.ToggleDay(s.settings[key], cb.Day)
		note = settings.FormatDays(value)
	case kindTime:
		key = settings.KeyOrderTime
		value = settings.ShiftTime(s.settings[key], cb.Minutes)
		note = value
	}
	if err := b.users.SetSetting(ctx, s.user.ID, key, value); err != nil {
		_, _ = api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Nu a mers"})
		b.log.ErrorContext(ctx, "set setting", "err", err)
		return
	}
	b.log.InfoContext(ctx, "setting changed", "user_id", s.user.ID, "key", key, "old", s.settings[key], "new", value)
	s.settings[key] = value
	_, _ = api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: note})

	if msg := q.Message.Message; msg != nil {
		_, err := api.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: msg.Chat.ID, MessageID: msg.ID,
			Text: settingsText(s.settings), ParseMode: models.ParseModeHTML,
			ReplyMarkup: settingsKeyboard(s.settings),
		})
		if err != nil {
			b.log.WarnContext(ctx, "edit settings message", "err", err)
		}
	}
}
