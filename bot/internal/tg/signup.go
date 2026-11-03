package tg

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/services"
	"food-order/shared/contracts/users"
)

// Signing up: an account in waiting can do only this. The bot keeps no "step"
// of the flow: the state is in users, and what the person wrote says the rest
// (with an @ it is an email, otherwise a code). So a restart breaks nothing.

// onSignup handles every update of an account that waits for its email.
func (b *Bot) onSignup(ctx context.Context, u *models.Update) {
	if u.CallbackQuery != nil {
		b.signupButton(ctx, u)
		return
	}
	if u.Message == nil {
		return
	}
	text := strings.TrimSpace(u.Message.Text)
	switch {
	case text == "" || strings.HasPrefix(text, "/"):
		b.reply(ctx, askEmailText)
	case strings.Contains(text, "@"):
		b.requestCode(ctx, text)
	default:
		b.confirmCode(ctx, text)
	}
}

func (b *Bot) signupButton(ctx context.Context, u *models.Update) {
	b.answerCallback(ctx, u, "")
	cb, err := decodeCallback(u.CallbackQuery.Data)
	if err != nil {
		b.reply(ctx, askEmailText)
		return
	}
	p := personFrom(ctx)
	email := b.lastEmail(p.UserID)
	if cb.Kind == kindResend && email != "" {
		b.requestCode(ctx, email)
		return
	}
	b.reply(ctx, askEmailText) // "another email", or the address was lost in a restart
}

func (b *Bot) requestCode(ctx context.Context, email string) {
	p := personFrom(ctx)
	sent, err := b.users.RequestEmailCode(ctx, p.ExternalID, email)
	if err != nil {
		b.fail(ctx, "request code", err)
		return
	}
	b.rememberEmail(p.UserID, email)
	b.log.InfoContext(ctx, "confirmation code asked for")
	b.replyWith(ctx, codeSentText(email, sent.ValidForSecond/60), signupKeyboard())
}

func (b *Bot) confirmCode(ctx context.Context, code string) {
	p := personFrom(ctx)
	res, err := b.users.ConfirmEmail(ctx, p.ExternalID, code)
	if err != nil {
		b.fail(ctx, "confirm code", err)
		return
	}
	b.forgetEmail(p.UserID)
	b.log.InfoContext(ctx, "email confirmed", "result", res.Result, "account_user_id", res.UserID)
	b.replyWith(ctx, confirmedText(res.Result)+"\n\n"+settingsSummary(b.settingsOf(ctx, p))+"\n\n"+helpText, b.phoneOffer(ctx, p))
}

// phoneOffer shows the phone button to somebody who has no phone saved.
func (b *Bot) phoneOffer(ctx context.Context, p person) models.ReplyMarkup {
	profile, err := b.users.Profile(ctx, p.ExternalID)
	if err != nil {
		b.log.WarnContext(ctx, "profile for the phone button", "err", err)
		return nil
	}
	if profile.Phone == "" {
		return phoneKeyboard()
	}
	return nil
}

func (b *Bot) lastEmail(userID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.emails[userID]
}

func (b *Bot) rememberEmail(userID, email string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.emails[userID] = email
}

func (b *Bot) forgetEmail(userID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.emails, userID)
}

// refusalText says in words why users or the gateway said no. An unknown
// code gets a general sentence: a new code on the server must not show up
// as technical text to a person.
func refusalText(e *services.GatewayError) string {
	if t := e.Text(); t != "" {
		return esc(t) // core and catalog write their own sentence
	}
	if t, ok := orgRefusals[e.Code]; ok {
		return t
	}
	switch e.Code {
	case users.ErrInvalidEmail:
		return "Adresa nu pare corectă. Scrie-o din nou, de exemplu nume@exemplu.md."
	case users.ErrCooldown:
		return waitText("Am trimis deja un cod.", e.Number("retry_after"))
	case users.ErrTooManyCodes:
		return waitText("Prea multe coduri pentru adresa asta.", e.Number("retry_after"))
	case users.ErrWrongCode:
		return wrongCodeText(e.Number("attempts_left"))
	case users.ErrExpiredCode:
		return "Codul a expirat. Apasă „Trimite din nou” sau scrie emailul încă o dată."
	case users.ErrTooManyAttempts:
		return "Prea multe încercări, codul a fost anulat. Scrie emailul ca să primești altul."
	case users.ErrNoCode:
		return "Scrie mai întâi emailul, ca să-ți trimit un cod."
	case users.ErrInvalidInput:
		return "Nu am înțeles. Codul are 4 caractere, litere și cifre."
	case users.ErrAccountBlocked:
		return accountClosedText
	case users.ErrAlreadyConfirmed:
		return "Emailul e deja confirmat."
	case users.ErrPhoneTaken:
		return "Numărul ăsta e deja la alt cont."
	case users.ErrInvalidPhone:
		return "Numărul nu pare corect."
	case users.ErrEmailNotConfirmed:
		return askEmailText
	case users.ErrTooMany:
		return "Lista e prea lungă."
	}
	if e.Status >= 500 {
		return serviceDownText
	}
	return "Nu a mers. Încearcă din nou."
}
