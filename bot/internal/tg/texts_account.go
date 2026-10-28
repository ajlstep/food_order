package tg

import (
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/settings"
	"food-order/bot/internal/store"
)

// Texts for connecting, profile and settings.

const (
	btnSharePhone = "📱 Trimite numărul"
	btnNotNow     = "Nu acum"
)

func welcomeText(u store.User, s settings.Settings) string {
	name := u.FirstName
	if name == "" {
		name = "acolo"
	}
	return fmt.Sprintf("👋 Salut, %s! Te-am conectat.\n\n"+
		"La <b>ora de comandă</b>, în <b>zilele de comandă</b>, îți trimit singur recomandarea pentru următoarea zi de comandă. Acum:\n%s\n\n"+
		"Le schimbi cu /setari. Ce nu mănânci și alergenii — /preferinte.\n\n"+
		"Dacă vrei, trimite-mi și numărul de telefon (butonul de jos). E opțional.",
		esc(name), settingsSummary(s))
}

func welcomeBackText(u store.User) string {
	name := u.FirstName
	if name == "" {
		name = "din nou"
	}
	return fmt.Sprintf("👋 Bine ai revenit, %s! Primești din nou recomandările.\n\n%s", esc(name), helpText)
}

const stoppedText = "⏸ Am oprit recomandările automate. Datele tale rămân.\nTrimite /start ca să revii."

const needStartText = "👋 Nu te cunosc încă. Trimite /start ca să te conectez."

const stoppedNeedStartText = "⏸ Ai oprit botul. Trimite /start ca să revii."

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
	b.WriteString("\n\nApasă pe zile ca să le bifezi, ±30 min pentru oră, sau scrie:\n")
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
	}}
}

func profileText(u store.User) string {
	var b strings.Builder
	b.WriteString("👤 <b>Profilul meu</b>\n\n")
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = "—"
	}
	fmt.Fprintf(&b, "Nume: %s\n", esc(name))
	if u.Username != "" {
		fmt.Fprintf(&b, "Nickname: @%s\n", esc(u.Username))
	} else {
		b.WriteString("Nickname: —\n")
	}
	if u.Phone != "" {
		fmt.Fprintf(&b, "Telefon: %s\n", esc(u.Phone))
	} else {
		b.WriteString("Telefon: — (îl poți trimite cu butonul de jos)\n")
	}
	fmt.Fprintf(&b, "Id în sistem: <code>%d</code>\n", u.ID)
	if !u.CreatedAt.IsZero() {
		fmt.Fprintf(&b, "Conectat din: %s\n", u.CreatedAt.Format("02.01.2006"))
	}
	b.WriteString("\nNumele și nickname-ul le iau din Telegram și se actualizează singure.\n/stop — opresc recomandările automate.")
	return b.String()
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
