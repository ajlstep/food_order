package tg

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/shared/contracts/users"
)

// foodProfile is what /preferinte shows: two lists in users, one list on
// screen. The numbers in the list go through the allergens first, then the
// preferences; "/preferinte del N" uses the same numbers.
type foodProfile struct {
	Allergens []string
	Prefs     []users.Pref
}

// allergenWord is the word after "/preferinte add" for the allergen list.
const allergenWord = "alergen"

// Words accepted after "/preferinte add" for the list of preferences.
var prefKinds = map[string]users.Pref{
	"nu":    {Kind: users.PrefDislike},
	"nu!":   {Kind: users.PrefDislike, Hard: true},
	"place": {Kind: users.PrefLike},
	"dieta": {Kind: users.PrefDiet},
	"dietă": {Kind: users.PrefDiet},
}

var errPrefsUsage = errors.New("usage")

// applyPrefsCommand changes the lists according to the words after
// /preferinte. No words = nothing to change (changed == false).
func applyPrefsCommand(cur foodProfile, args string) (next foodProfile, changed bool, err error) {
	f := strings.Fields(args)
	if len(f) == 0 {
		return cur, false, nil
	}
	switch strings.ToLower(f[0]) {
	case "add", "+":
		if len(f) < 3 {
			return cur, false, errPrefsUsage
		}
		return addFood(cur, strings.ToLower(f[1]), strings.ToLower(strings.Join(f[2:], " ")))
	case "del", "-":
		if len(f) != 2 {
			return cur, false, errPrefsUsage
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			return cur, false, errPrefsUsage
		}
		return deleteFood(cur, n)
	}
	return cur, false, errPrefsUsage
}

func addFood(cur foodProfile, word, value string) (foodProfile, bool, error) {
	if word == allergenWord {
		if slices.Contains(cur.Allergens, value) {
			return cur, false, nil
		}
		cur.Allergens = append(slices.Clone(cur.Allergens), value)
		return cur, true, nil
	}
	pref, ok := prefKinds[word]
	if !ok {
		return cur, false, errPrefsUsage
	}
	for _, it := range cur.Prefs {
		if it.Kind == pref.Kind && strings.EqualFold(it.Value, value) {
			return cur, false, nil // already there
		}
	}
	pref.Value = value
	cur.Prefs = append(slices.Clone(cur.Prefs), pref)
	return cur, true, nil
}

// deleteFood removes line n of the list on screen.
func deleteFood(cur foodProfile, n int) (foodProfile, bool, error) {
	i := n - 1
	if i < 0 || i >= len(cur.Allergens)+len(cur.Prefs) {
		return cur, false, errPrefsUsage
	}
	if i < len(cur.Allergens) {
		cur.Allergens = slices.Delete(slices.Clone(cur.Allergens), i, i+1)
		return cur, true, nil
	}
	i -= len(cur.Allergens)
	cur.Prefs = slices.Delete(slices.Clone(cur.Prefs), i, i+1)
	return cur, true, nil
}

func (b *Bot) loadFood(ctx context.Context, p person) (foodProfile, error) {
	allergens, err := b.users.Allergens(ctx, p.ExternalID)
	if err != nil {
		return foodProfile{}, err
	}
	prefs, err := b.users.Prefs(ctx, p.ExternalID)
	if err != nil {
		return foodProfile{}, err
	}
	return foodProfile{Allergens: allergens, Prefs: prefs}, nil
}

// saveFood sends only the list that changed and returns what users saved,
// so the numbers on screen are the ones users has now.
func (b *Bot) saveFood(ctx context.Context, p person, cur, next foodProfile) (foodProfile, error) {
	var err error
	if !slices.Equal(cur.Allergens, next.Allergens) {
		if next.Allergens, err = b.users.SetAllergens(ctx, p.ExternalID, next.Allergens); err != nil {
			return next, err
		}
	}
	if !slices.Equal(cur.Prefs, next.Prefs) {
		if next.Prefs, err = b.users.SetPrefs(ctx, p.ExternalID, next.Prefs); err != nil {
			return next, err
		}
	}
	return next, nil
}

func (b *Bot) onPrefs(ctx context.Context, _ *bot.Bot, u *models.Update) {
	p := personFrom(ctx)
	cur, err := b.loadFood(ctx, p)
	if err != nil {
		b.fail(ctx, "prefs", err)
		return
	}
	next, changed, err := applyPrefsCommand(cur, commandArgs(u))
	if errors.Is(err, errPrefsUsage) {
		b.log.InfoContext(ctx, "prefs command not understood")
		b.reply(ctx, "Nu am înțeles.\n\n"+prefsHelp)
		return
	}
	if changed {
		if next, err = b.saveFood(ctx, p, cur, next); err != nil {
			b.fail(ctx, "set prefs", err)
			return
		}
		b.log.InfoContext(ctx, "prefs changed", "allergens", len(next.Allergens), "prefs", len(next.Prefs))
	}
	b.reply(ctx, prefsText(next))
}
