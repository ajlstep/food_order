package profile

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Allergens returns the person's allergens.
func (s *Service) Allergens(ctx context.Context, userID string) ([]string, error) {
	if _, err := s.active(ctx, s.store, userID); err != nil {
		return nil, err
	}
	labels, err := s.store.Allergens(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load allergens: %w", err)
	}
	return labels, nil
}

// SetAllergens replaces the list. An allergen is always a hard filter: core
// never recommends a dish that may contain one.
func (s *Service) SetAllergens(ctx context.Context, userID string, in []string) ([]string, error) {
	clean := make([]string, 0, len(in))
	for _, raw := range in {
		l := normLabel(raw)
		if l == "" || tooLong(l, maxLabelLen) {
			return nil, fail("invalid_input", "field", "allergens")
		}
		if !slices.Contains(clean, l) {
			clean = append(clean, l)
		}
	}
	if len(clean) > maxAllergens {
		return nil, fail("too_many", "limit", maxAllergens)
	}
	err := s.store.InTx(ctx, func(tx Tx) error {
		if _, err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		return tx.ReplaceAllergens(ctx, userID, clean)
	})
	if err != nil {
		return nil, fmt.Errorf("set allergens: %w", err)
	}
	return clean, nil
}

// Prefs returns the person's preferences, in the order they made them.
func (s *Service) Prefs(ctx context.Context, userID string) ([]Pref, error) {
	if _, err := s.active(ctx, s.store, userID); err != nil {
		return nil, err
	}
	items, err := s.store.Prefs(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load preferences: %w", err)
	}
	return items, nil
}

// SetPrefs replaces the list. Allergens are not preferences: they have their
// own list, so that "never" cannot be mixed up with "rather not".
func (s *Service) SetPrefs(ctx context.Context, userID string, in []Pref) ([]Pref, error) {
	clean, err := cleanPrefs(in)
	if err != nil {
		return nil, err
	}
	err = s.store.InTx(ctx, func(tx Tx) error {
		if _, err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		return tx.ReplacePrefs(ctx, userID, clean)
	})
	if err != nil {
		return nil, fmt.Errorf("set preferences: %w", err)
	}
	return clean, nil
}

func cleanPrefs(in []Pref) ([]Pref, error) {
	seen := map[string]bool{}
	out := make([]Pref, 0, len(in))
	for _, p := range in {
		kind := strings.ToLower(strings.TrimSpace(p.Kind))
		switch kind {
		case "like", "dislike", "diet", "target":
		case "allergen":
			return nil, fail("invalid_input", "field", "kind", "reason", "allergens have their own list")
		default:
			return nil, fail("invalid_input", "field", "kind", "reason", "unknown kind")
		}

		value := normLabel(p.Value)
		if kind == "target" {
			var err error
			if value, err = normTarget(p.Value); err != nil {
				return nil, fail("invalid_input", "field", "value", "reason", err.Error())
			}
		}
		if value == "" || tooLong(value, maxValueLen) {
			return nil, fail("invalid_input", "field", "value")
		}

		key := kind + "\x00" + value
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Pref{Kind: kind, Value: value, Hard: p.Hard})
	}
	if len(out) > maxPrefs {
		return nil, fail("too_many", "limit", maxPrefs)
	}
	return out, nil
}

// Food returns allergens and preferences together: what core needs to filter
// and score dishes for the person.
func (s *Service) Food(ctx context.Context, userID string) (allergens []string, prefs []Pref, err error) {
	if _, err := s.active(ctx, s.store, userID); err != nil {
		return nil, nil, err
	}
	if allergens, err = s.store.Allergens(ctx, userID); err != nil {
		return nil, nil, fmt.Errorf("load allergens: %w", err)
	}
	if prefs, err = s.store.Prefs(ctx, userID); err != nil {
		return nil, nil, fmt.Errorf("load preferences: %w", err)
	}
	return allergens, prefs, nil
}
