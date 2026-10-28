package tg

import (
	"strings"

	"github.com/go-telegram/bot/models"
)

// toggleForced rebuilds the keyboard of a recommendation message with the
// "choose from necessity" flag switched, using only what is already in the
// buttons (no state needed). Allergen rows stay as they are.
func toggleForced(kb models.InlineKeyboardMarkup, date string, on bool) models.InlineKeyboardMarkup {
	out := models.InlineKeyboardMarkup{InlineKeyboard: make([][]models.InlineKeyboardButton, 0, len(kb.InlineKeyboard))}
	for _, row := range kb.InlineKeyboard {
		var newRow []models.InlineKeyboardButton
		for _, btn := range row {
			cb, err := decodeCallback(btn.CallbackData)
			switch {
			case err == nil && cb.Kind == kindOrder:
				label := strings.TrimPrefix(btn.Text, forcedPrefix)
				if on {
					label = forcedPrefix + label
				}
				newRow = append(newRow, models.InlineKeyboardButton{
					Text:         label,
					CallbackData: encodeOrder(cb.Date, cb.DishID, on, cb.Price),
				})
			case err == nil && cb.Kind == kindForced:
				if on {
					newRow = append(newRow, models.InlineKeyboardButton{Text: "↩️ Înapoi (aleg cu plăcere)", CallbackData: encodeForced(date, false)})
				} else {
					newRow = append(newRow, models.InlineKeyboardButton{Text: "🤷 Nimic nu mi-a plăcut, aleg din lipsă", CallbackData: encodeForced(date, true)})
				}
			default:
				newRow = append(newRow, models.InlineKeyboardButton{Text: btn.Text, CallbackData: btn.CallbackData})
			}
		}
		out.InlineKeyboard = append(out.InlineKeyboard, newRow)
	}
	return out
}
