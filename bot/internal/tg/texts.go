package tg

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/contracts"
)

// All the texts the user sees are in this file (Romanian for now; Russian later).

const helpText = `Sunt botul tău de prânz 🍽

/meniu — cere recomandarea pentru următoarea zi de comandă
/alege — arată ultima recomandare (/alege azi pentru azi)
/nota — notează ultima masă
/istoric — ce am ales (/istoric 60 = ultimele 60 de zile)
/preferinte — alergeni, ce nu mănânc, ce-mi place
/stat — statistica lunii (/stat 7 = ultimele 7 zile)
/setari — ora și zilele de comandă
/profil — numele, nickname-ul, telefonul
/stop — opresc recomandările automate`

const prefsHelp = `Exemple:
<code>/preferinte add alergen lapte</code>
<code>/preferinte add nu! ciuperci</code>  (! = niciodată, filtru dur)
<code>/preferinte add nu prăjit</code>
<code>/preferinte add place supă</code>
<code>/preferinte add dieta fără zahăr</code>
<code>/preferinte del 3</code>  (numărul din listă)`

const forcedPrefix = "🤷 "

var weekdays = [...]string{"duminică", "luni", "marți", "miercuri", "joi", "vineri", "sâmbătă"}
var months = [...]string{"ian", "feb", "mar", "apr", "mai", "iun", "iul", "aug", "sep", "oct", "noi", "dec"}

// humanDate: 2026-10-29 -> "joi, 29 oct".
func humanDate(date string) string {
	t, err := time.Parse(dateLayout, date)
	if err != nil {
		return date
	}
	return fmt.Sprintf("%s, %d %s", weekdays[t.Weekday()], t.Day(), months[t.Month()-1])
}

func money(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimSuffix(s, ".00")
	return strings.Replace(s, ".", ",", 1) + " lei"
}

// kcalRange shows an interval, or one number when min == max.
func kcalRange(min, max *float64) string {
	switch {
	case min == nil && max == nil:
		return ""
	case min == nil:
		return fmt.Sprintf("≤%.0f kcal", *max)
	case max == nil || *min == *max:
		return fmt.Sprintf("%.0f kcal", *min)
	default:
		return fmt.Sprintf("~%.0f–%.0f kcal", *min, *max)
	}
}

func rangeText(r *contracts.Range, unit string) string {
	if r == nil {
		return "—"
	}
	if r.Min == r.Max {
		return fmt.Sprintf("%.0f %s", r.Min, unit)
	}
	return fmt.Sprintf("%.0f–%.0f %s", r.Min, r.Max, unit)
}

func esc(s string) string { return html.EscapeString(s) }

// recommendationText is the message with the picks (HTML).
func recommendationText(date string, picks []contracts.Pick) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🍽 <b>Recomandări pentru %s</b>\n", esc(humanDate(date)))
	for i, p := range picks {
		fmt.Fprintf(&b, "\n%d. <b>%s</b>", i+1, esc(p.Name))
		var facts []string
		if p.Price != nil {
			facts = append(facts, money(*p.Price))
		}
		if k := kcalRange(p.KcalMin, p.KcalMax); k != "" {
			facts = append(facts, k)
		}
		if len(facts) > 0 {
			b.WriteString(" — " + strings.Join(facts, " · "))
		}
		if p.Reason != "" {
			fmt.Fprintf(&b, "\n   <i>%s</i>", esc(p.Reason))
		}
		if len(p.AllergensPossible) > 0 {
			fmt.Fprintf(&b, "\n   ⚠️ posibil: %s", esc(strings.Join(p.AllergensPossible, ", ")))
		}
	}
	b.WriteString("\n\nAlege un fel:")
	return b.String()
}

// recommendationKeyboard: one button per dish, the "necessity" switch,
// and allergen buttons for the dishes that may contain allergens.
func recommendationKeyboard(date string, picks []contracts.Pick, forced bool) models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	for i, p := range picks {
		label := fmt.Sprintf("%d. %s", i+1, shorten(p.Name, 40))
		if p.Price != nil {
			label += " · " + money(*p.Price)
		}
		if forced {
			label = forcedPrefix + label
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: label, CallbackData: encodeOrder(date, p.DishID, forced, p.Price)}})
	}
	if forced {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "↩️ Înapoi (aleg cu plăcere)", CallbackData: encodeForced(date, false)}})
	} else {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "🤷 Nimic nu mi-a plăcut, aleg din lipsă", CallbackData: encodeForced(date, true)}})
	}
	for i, p := range picks {
		if len(p.AllergensPossible) == 0 {
			continue
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: fmt.Sprintf("⚠️ %d: da, conține", i+1), CallbackData: encodeAllergen(date, p.DishID, true)},
			{Text: fmt.Sprintf("✅ %d: nu conține", i+1), CallbackData: encodeAllergen(date, p.DishID, false)},
		})
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func ratingKeyboard(orderID int64) models.InlineKeyboardMarkup {
	row := make([]models.InlineKeyboardButton, 0, 5)
	for i := 1; i <= 5; i++ {
		row = append(row, models.InlineKeyboardButton{Text: strconv.Itoa(i) + "⭐", CallbackData: encodeRate(orderID, i)})
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
}

func noCommentKeyboard(orderID int64) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Fără comentariu", CallbackData: encodeNoNote(orderID)}},
	}}
}

func emptyKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
}

func ratingQuestion(o *contracts.Order) string {
	if o == nil || o.DishName == "" {
		return "Cum a fost masa? Dă o notă:"
	}
	return fmt.Sprintf("Cum a fost <b>%s</b> (%s)? Dă o notă:", esc(o.DishName), esc(humanDate(o.Date)))
}

func historyText(orders []contracts.Order, limit int) string {
	if len(orders) == 0 {
		return "Nu ai alegeri în perioada asta."
	}
	var b strings.Builder
	b.WriteString("<b>Ultimele alegeri</b>\n")
	for i, o := range orders {
		if i == limit {
			fmt.Fprintf(&b, "\n… și încă %d", len(orders)-limit)
			break
		}
		fmt.Fprintf(&b, "\n%s — %s", esc(humanDate(o.Date)), esc(o.DishName))
		if o.Price != nil {
			b.WriteString(" · " + money(*o.Price))
		}
		if o.Rating != nil {
			fmt.Fprintf(&b, " · %d⭐", *o.Rating)
		} else {
			b.WriteString(" · fără notă")
		}
		if o.Forced {
			b.WriteString(" · 🤷")
		}
		if o.Comment != "" {
			fmt.Fprintf(&b, "\n   <i>%s</i>", esc(shorten(o.Comment, 80)))
		}
	}
	return b.String()
}

var kindNames = map[string]string{
	"allergen": "Alergeni",
	"dislike":  "Nu mănânc",
	"like":     "Îmi place",
	"diet":     "Dietă",
}

func prefsText(p contracts.Prefs) string {
	if len(p.Items) == 0 {
		return "Nu ai preferințe salvate.\n\n" + prefsHelp
	}
	var b strings.Builder
	b.WriteString("<b>Preferințele mele</b>\n")
	for _, kind := range []string{"allergen", "dislike", "like", "diet"} {
		first := true
		for i, it := range p.Items {
			if it.Kind != kind {
				continue
			}
			if first {
				fmt.Fprintf(&b, "\n<b>%s</b>\n", kindNames[kind])
				first = false
			}
			hard := ""
			if it.Hard && kind != "allergen" {
				hard = " (niciodată)"
			}
			fmt.Fprintf(&b, "%d. %s%s\n", i+1, esc(it.Value), hard)
		}
	}
	b.WriteString("\n" + prefsHelp)
	return b.String()
}

func statsText(s contracts.Stats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>%s — %s</b>\n\n", esc(humanDate(s.From)), esc(humanDate(s.To)))
	fmt.Fprintf(&b, "Mese: %d", s.Orders)
	if s.Forced > 0 {
		fmt.Fprintf(&b, " (din lipsă: %d)", s.Forced)
	}
	fmt.Fprintf(&b, "\nCheltuit: %s\n", money(s.Spent))
	if s.AvgRating != nil {
		fmt.Fprintf(&b, "Nota medie: %.1f⭐\n", *s.AvgRating)
	}
	fmt.Fprintf(&b, "\nCalorii: %s\nProteine: %s · Grăsimi: %s · Glucide: %s\n",
		rangeText(s.Kcal, "kcal"), rangeText(s.ProteinG, "g"), rangeText(s.FatG, "g"), rangeText(s.CarbsG, "g"))
	if s.WithoutNutrition > 0 {
		fmt.Fprintf(&b, "<i>%d mese fără valori nutriționale</i>\n", s.WithoutNutrition)
	}
	if len(s.TopDishes) > 0 {
		b.WriteString("\n<b>Ce aleg cel mai des</b>\n")
		for i, d := range s.TopDishes {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "%d. %s × %d", i+1, esc(d.Name), d.Count)
			if d.AvgRating != nil {
				fmt.Fprintf(&b, " · %.1f⭐", *d.AvgRating)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func alertText(a contracts.Alert) string {
	icon := "ℹ️"
	switch a.Level {
	case "warn", "warning":
		icon = "🟠"
	case "error":
		icon = "🔴"
	}
	return fmt.Sprintf("%s <b>%s</b>: %s", icon, esc(a.Service), esc(a.Text))
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
