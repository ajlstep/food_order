package tg

import (
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/settings"
	"food-order/shared/contracts/users"
)

// Texts for connecting, profile and settings.

const (
	btnSharePhone = "📱 Trimite numărul"
	btnNotNow     = "Nu acum"
)

const (
	askEmailText = "👋 Salut! Ca să începem, scrie adresa ta de email. Îți trimit un cod de confirmare."

	serviceDownText = "Serviciul nu răspunde acum. Încearcă din nou puțin mai târziu."

	accountClosedText = "Contul tău nu e activ. Scrie-i administratorului."

	stoppedText = "⏸ Am oprit notificările. Datele tale rămân.\nTrimite /start ca să le primești din nou."
)

func codeSentText(email string, minutes int) string {
	return fmt.Sprintf("📧 Am trimis un cod pe <b>%s</b>.\n\n<b>Poate ajunge în Spam</b>, caută-l și acolo. "+
		"Codul e valabil %d de minute. Scrie-l aici.", esc(email), minutes)
}

func confirmedText(result string) string {
	if result == users.ConfirmAttached {
		return "✅ Emailul e confirmat. Aveai deja un cont, așa că am legat acest chat de el."
	}
	return "✅ Emailul e confirmat. Contul e gata. Setările tale:"
}

func waitText(what string, seconds int) string {
	if seconds <= 0 {
		return what + " Încearcă puțin mai târziu."
	}
	return fmt.Sprintf("%s Încearcă din nou peste %d de secunde.", what, seconds)
}

func wrongCodeText(attemptsLeft int) string {
	return fmt.Sprintf("Codul nu e bun. Mai ai %d încercări.", attemptsLeft)
}

func welcomeBackText(name string) string {
	if name == "" {
		name = "din nou"
	}
	return fmt.Sprintf("👋 Bine ai revenit, %s! Primești din nou notificările.\n\n%s", esc(name), helpText)
}

func signupKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "🔁 Trimite din nou", CallbackData: encodeResend()},
		{Text: "✏️ Alt email", CallbackData: encodeOtherEmail()},
	}}}
}

func settingsSummary(s settings.Settings) string {
	var b strings.Builder
	for _, d := range settings.Defs {
		fmt.Fprintf(&b, "• %s: <b>%s</b>\n", esc(d.Label), esc(d.Format(s[d.Key])))
	}
	return strings.TrimRight(b.String(), "\n")
}

func settingsText(s settings.Settings) string {
	var b strings.Builder
	b.WriteString("⚙️ <b>Setările mele</b>\n\n")
	b.WriteString(settingsSummary(s))
	b.WriteString("\n\nApasă pe butoane sau scrie:\n")
	for _, d := range settings.Defs {
		fmt.Fprintf(&b, "<code>%s</code>\n", esc(d.Help))
	}
	b.WriteString("\nLa ora de comandă îți trimit recomandarea pentru următoarea zi de comandă (vineri → luni).")
	return b.String()
}

func settingsKeyboard(s settings.Settings) models.InlineKeyboardMarkup {
	days := s.OrderDays()
	dayBtn := func(iso int) models.InlineKeyboardButton {
		mark := "▫️"
		if days[iso%7] {
			mark = "✅"
		}
		return models.InlineKeyboardButton{Text: mark + " " + settings.DayShort[iso], CallbackData: encodeDay(iso)}
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{dayBtn(1), dayBtn(2), dayBtn(3), dayBtn(4)},
		{dayBtn(5), dayBtn(6), dayBtn(7)},
		{
			{Text: "🕐 −30 min", CallbackData: encodeTime(-30)},
			{Text: "🕐 +30 min", CallbackData: encodeTime(30)},
		},
		{toggleButton(s, "🍽", settings.KeyRecFor)},
		{toggleButton(s, "🔔", settings.KeyNotifyPersonal)},
		{toggleButton(s, "👥", settings.KeyNotifyGroup)},
		{toggleButton(s, "📍", settings.KeyOrderTo)},
	}}
}

// toggleButton shows a setting with two values and its value now.
func toggleButton(s settings.Settings, icon, key string) models.InlineKeyboardButton {
	def, _ := settings.Find(key) // the keys above are all in settings.Defs
	return models.InlineKeyboardButton{
		Text:         icon + " " + def.Label + ": " + def.Format(s[key]),
		CallbackData: encodeToggle(key),
	}
}

func profileText(p users.ProfileResponse, c users.ChannelsResponse) string {
	var b strings.Builder
	b.WriteString("👤 <b>Profilul meu</b>\n\n")
	fmt.Fprintf(&b, "Nume: %s\n", esc(orDash(p.DisplayName)))
	fmt.Fprintf(&b, "Email: %s\n", esc(orDash(p.Email)))
	if p.Phone != "" {
		fmt.Fprintf(&b, "Telefon: %s\n", esc(p.Phone))
	} else {
		b.WriteString("Telefon: — (îl poți trimite cu butonul de jos)\n")
	}
	fmt.Fprintf(&b, "Id în sistem: <code>%s</code>\n", esc(p.UserID))
	for _, ch := range c.Channels {
		fmt.Fprintf(&b, "Canal: %s (%s)\n", esc(ch.Channel), channelStatusName(ch.Status))
	}
	b.WriteString("\n/stop — opresc notificările.")
	return b.String()
}

func channelStatusName(status string) string {
	switch status {
	case users.IdentityActive:
		return "activ"
	case users.IdentityStopped:
		return "oprit"
	}
	return "blocat"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func phoneKeyboard() models.ReplyKeyboardMarkup {
	return models.ReplyKeyboardMarkup{
		Keyboard: [][]models.KeyboardButton{
			{{Text: btnSharePhone, RequestContact: true}},
			{{Text: btnNotNow}},
		},
		ResizeKeyboard:  true,
		OneTimeKeyboard: true,
	}
}

func removeKeyboard() models.ReplyKeyboardRemove {
	return models.ReplyKeyboardRemove{RemoveKeyboard: true}
}
