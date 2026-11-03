package tg

import (
	"strings"

	"github.com/go-telegram/bot/models"
)

// describeUpdate says what the person did: "/meniu", "button order", "text",
// "contact", for the logs. It never gives what they wrote: while signing up
// that is an email or a code, and those do not go in logs.
func describeUpdate(u *models.Update) (string, []any) {
	switch {
	case u.Message != nil && u.Message.Contact != nil:
		return "contact", nil
	case u.Message != nil && strings.HasPrefix(strings.TrimSpace(u.Message.Text), "/"):
		cmd, _, _ := strings.Cut(strings.TrimSpace(u.Message.Text), " ")
		cmd, _, _ = strings.Cut(cmd, "@")
		return cmd, []any{"message_id", u.Message.ID}
	case u.Message != nil:
		return "text", []any{"message_id", u.Message.ID}
	case u.CallbackQuery != nil:
		if cb, err := decodeCallback(u.CallbackQuery.Data); err == nil {
			return "button " + cb.Kind, nil
		}
		return "button", nil
	}
	return "other", nil
}
