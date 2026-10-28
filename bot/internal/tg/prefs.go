package tg

import (
	"errors"
	"strconv"
	"strings"

	"food-order/bot/internal/contracts"
)

// Words accepted after "/preferinte add".
var prefKinds = map[string]contracts.Pref{
	"alergen": {Kind: "allergen", Hard: true}, // an allergen is always a hard filter
	"nu":      {Kind: "dislike"},
	"nu!":     {Kind: "dislike", Hard: true},
	"place":   {Kind: "like"},
	"dieta":   {Kind: "diet"},
	"dietă":   {Kind: "diet"},
}

var errPrefsUsage = errors.New("usage")

// applyPrefsCommand changes the list according to the words after /preferinte.
// No words = nothing to change (changed == false).
func applyPrefsCommand(cur contracts.Prefs, args string) (next contracts.Prefs, changed bool, err error) {
	f := strings.Fields(args)
	if len(f) == 0 {
		return cur, false, nil
	}
	items := append([]contracts.Pref(nil), cur.Items...)
	switch strings.ToLower(f[0]) {
	case "add", "+":
		if len(f) < 3 {
			return cur, false, errPrefsUsage
		}
		tmpl, ok := prefKinds[strings.ToLower(f[1])]
		if !ok {
			return cur, false, errPrefsUsage
		}
		value := strings.ToLower(strings.Join(f[2:], " "))
		for _, it := range items {
			if it.Kind == tmpl.Kind && strings.EqualFold(it.Value, value) {
				return cur, false, nil // already there
			}
		}
		tmpl.Value = value
		items = append(items, tmpl)
	case "del", "-":
		if len(f) != 2 {
			return cur, false, errPrefsUsage
		}
		n, err := strconv.Atoi(f[1])
		if err != nil || n < 1 || n > len(items) {
			return cur, false, errPrefsUsage
		}
		items = append(items[:n-1], items[n:]...)
	default:
		return cur, false, errPrefsUsage
	}
	return contracts.Prefs{Items: items}, true, nil
}
