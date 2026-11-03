package tg

import (
	"context"
	"errors"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/settings"
	"food-order/shared/contracts/users"
)

// The person's own data: all of it lives in users, reached through the gateway.

// onStart: an account that waits for its email never gets here (see auth).
// For an active one, /start brings the channel back after /stop.
func (b *Bot) onStart(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	p := personFrom(ctx)
	if err := b.users.SetChannelStatus(ctx, p.ExternalID, users.IdentityActive); err != nil {
		b.fail(ctx, "start", err)
		return
	}
	b.reply(ctx, welcomeBackText(p.Name))
}

func (b *Bot) onStop(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	p := personFrom(ctx)
	if err := b.users.SetChannelStatus(ctx, p.ExternalID, users.IdentityStopped); err != nil {
		b.fail(ctx, "stop", err)
		return
	}
	b.log.InfoContext(ctx, "the person stopped the notifications")
	b.reply(ctx, stoppedText)
}

func (b *Bot) onProfile(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	p := personFrom(ctx)
	profile, err := b.users.Profile(ctx, p.ExternalID)
	if err != nil {
		b.fail(ctx, "profile", err)
		return
	}
	channels, err := b.users.Channels(ctx, p.ExternalID)
	if err != nil {
		b.fail(ctx, "channels", err)
		return
	}
	var markup models.ReplyMarkup
	if profile.Phone == "" {
		markup = phoneKeyboard()
	}
	b.replyWith(ctx, profileText(profile, channels), markup)
}

// onContact saves the phone number shared with the "Trimite numărul" button.
func (b *Bot) onContact(ctx context.Context, u *models.Update) {
	p := personFrom(ctx)
	c := u.Message.Contact
	if u.Message.From == nil || c.UserID != u.Message.From.ID {
		b.log.InfoContext(ctx, "someone else's contact refused")
		b.replyWith(ctx, "Trimite numărul tău cu butonul de jos, nu contactul altcuiva.", phoneKeyboard())
		return
	}
	phone := strings.TrimSpace(c.PhoneNumber)
	if phone != "" && !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	if err := b.users.SetPhone(ctx, p.ExternalID, phone); err != nil {
		b.fail(ctx, "set phone", err)
		return
	}
	b.log.InfoContext(ctx, "phone saved")
	b.replyWith(ctx, "✅ Am salvat numărul "+esc(phone)+".", removeKeyboard())
}

// loadSettings is for the commands that show or change the settings: unlike
// settingsOf, it must not show the defaults as if they were the person's.
func (b *Bot) loadSettings(ctx context.Context, p person) (settings.Settings, error) {
	saved, err := b.users.Settings(ctx, p.ExternalID)
	if err != nil {
		return nil, err
	}
	return settings.WithDefaults(saved), nil
}

// saveSetting changes one setting and returns all of them as users has them.
func (b *Bot) saveSetting(ctx context.Context, p person, key, value string) (settings.Settings, error) {
	saved, err := b.users.SetSettings(ctx, p.ExternalID, map[string]string{key: value})
	if err != nil {
		return nil, err
	}
	b.log.InfoContext(ctx, "setting changed", "key", key)
	return settings.WithDefaults(saved), nil
}

// onSettings: "/setari" shows the settings with buttons;
// "/setari <key> <value>" changes one.
func (b *Bot) onSettings(ctx context.Context, _ *bot.Bot, u *models.Update) {
	p := personFrom(ctx)
	st, err := b.loadSettings(ctx, p)
	if err != nil {
		b.fail(ctx, "settings", err)
		return
	}
	args := commandArgs(u)
	if args == "" {
		b.replyWith(ctx, settingsText(st), settingsKeyboard(st))
		return
	}
	word, value, _ := strings.Cut(args, " ")
	def, err := settings.Find(word)
	if errors.Is(err, settings.ErrUnknown) {
		b.log.InfoContext(ctx, "unknown setting", "key", word)
		b.replyWith(ctx, "Nu știu setarea „"+esc(word)+"”.\n\n"+settingsText(st), settingsKeyboard(st))
		return
	}
	canon, err := def.Parse(value)
	if err != nil {
		b.log.InfoContext(ctx, "bad setting value", "key", def.Key)
		b.reply(ctx, "❌ "+esc(err.Error())+"\nExemplu: <code>"+esc(def.Help)+"</code>")
		return
	}
	st, err = b.saveSetting(ctx, p, def.Key, canon)
	if err != nil {
		b.fail(ctx, "set setting", err)
		return
	}
	b.replyWith(ctx, "✅ "+esc(def.Label)+": <b>"+esc(def.Format(canon))+"</b>\n\n"+settingsText(st), settingsKeyboard(st))
}

// changeFor says what a settings button changes: the key, the new value and
// the short note shown on the button press.
func changeFor(st settings.Settings, cb Callback) (key, value, note string, err error) {
	switch cb.Kind {
	case kindDay:
		value = settings.ToggleDay(st[settings.KeyOrderDays], cb.Day)
		return settings.KeyOrderDays, value, settings.FormatDays(value), nil
	case kindTime:
		value = settings.ShiftTime(st[settings.KeyOrderTime], cb.Minutes)
		return settings.KeyOrderTime, value, value, nil
	}
	value, err = settings.Toggle(cb.Key, st[cb.Key])
	if err != nil {
		return "", "", "", err
	}
	def, err := settings.Find(cb.Key)
	if err != nil {
		return "", "", "", err
	}
	return cb.Key, value, def.Label + ": " + def.Format(value), nil
}

// settingsButton handles every settings button, then redraws the same message.
func (b *Bot) settingsButton(ctx context.Context, api *bot.Bot, q *models.CallbackQuery, cb Callback, answer func(string)) {
	p := personFrom(ctx)
	st, err := b.loadSettings(ctx, p)
	if err != nil {
		answer("Nu a mers")
		b.fail(ctx, "settings", err)
		return
	}
	key, value, note, err := changeFor(st, cb)
	if err != nil {
		answer("Buton necunoscut")
		b.log.WarnContext(ctx, "settings button", "err", err)
		return
	}
	if st, err = b.saveSetting(ctx, p, key, value); err != nil {
		answer("Nu a mers")
		b.fail(ctx, "set setting", err)
		return
	}
	answer(note)

	if msg := q.Message.Message; msg != nil {
		_, err := api.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: msg.Chat.ID, MessageID: msg.ID,
			Text: settingsText(st), ParseMode: models.ParseModeHTML,
			ReplyMarkup: settingsKeyboard(st),
		})
		if err != nil {
			b.log.WarnContext(ctx, "edit settings message", "err", err)
		}
	}
}
