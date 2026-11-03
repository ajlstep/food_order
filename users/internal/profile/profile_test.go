package profile

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

var ctx = context.Background()

func ptr[T any](v T) *T { return &v }

// Nothing works before the email is confirmed, whoever calls.
func TestEveryCallNeedsAnActiveAccount(t *testing.T) {
	w := newWorld(t)
	w.user("pending", StatusPending)
	w.user("blocked", "blocked")

	calls := map[string]func(id string) error{
		"Profile":       func(id string) error { _, err := w.svc.Profile(ctx, id); return err },
		"UpdateProfile": func(id string) error { _, err := w.svc.UpdateProfile(ctx, id, ProfileChange{}); return err },
		"Channels":      func(id string) error { _, _, err := w.svc.Channels(ctx, id); return err },
		"Settings":      func(id string) error { _, err := w.svc.Settings(ctx, id); return err },
		"SetSettings":   func(id string) error { _, err := w.svc.SetSettings(ctx, id, nil); return err },
		"Allergens":     func(id string) error { _, err := w.svc.Allergens(ctx, id); return err },
		"SetAllergens":  func(id string) error { _, err := w.svc.SetAllergens(ctx, id, nil); return err },
		"Prefs":         func(id string) error { _, err := w.svc.Prefs(ctx, id); return err },
		"SetPrefs":      func(id string) error { _, err := w.svc.SetPrefs(ctx, id, nil); return err },
		"Food":          func(id string) error { _, _, err := w.svc.Food(ctx, id); return err },
		"Locations":     func(id string) error { _, err := w.svc.Locations(ctx, id); return err },
		"SaveLocation": func(id string) error {
			_, err := w.svc.SaveLocation(ctx, id, Location{Label: "a", Address: "b"})
			return err
		},
		"DeleteLocation": func(id string) error { return w.svc.DeleteLocation(ctx, id, 1) },
		"SetDefault":     func(id string) error { return w.svc.SetDefaultLocation(ctx, id, 1) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			wantCode(t, call("pending"), "email_not_confirmed")
			wantCode(t, call("blocked"), "account_blocked")
			wantCode(t, call("ghost"), "account_not_found")
		})
	}
}

func TestUpdateProfile(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")

	a, err := w.svc.UpdateProfile(ctx, id, ProfileChange{DisplayName: ptr("  Ana   Maria "), Language: ptr("RU")})
	if err != nil {
		t.Fatal(err)
	}
	if a.DisplayName != "Ana Maria" || a.Language != "ru" {
		t.Fatalf("got %+v", a)
	}
	again, _ := w.svc.Profile(ctx, id)
	if again.DisplayName != "Ana Maria" || again.Language != "ru" {
		t.Fatalf("not saved: %+v", again)
	}

	wantCode(t, second(w.svc.UpdateProfile(ctx, id, ProfileChange{Language: ptr("de")})), "invalid_input")
	wantCode(t, second(w.svc.UpdateProfile(ctx, id, ProfileChange{DisplayName: ptr("   ")})), "invalid_input")
	wantCode(t, second(w.svc.UpdateProfile(ctx, id, ProfileChange{DisplayName: ptr(strings.Repeat("x", 65))})), "invalid_input")
}

// second drops the first result, to check only the error.
func second[T any](_ T, err error) error { return err }

func TestPhone(t *testing.T) {
	w := newWorld(t)
	w.user("pending", StatusPending) // a person still registering can give the phone
	w.active("u2")

	if err := w.svc.SetPhone(ctx, "pending", "+373 (69) 123-456"); err != nil {
		t.Fatal(err)
	}
	a, _ := w.store.Account(ctx, "pending")
	if a.Phone != "+37369123456" {
		t.Fatalf("phone = %q", a.Phone)
	}
	wantCode(t, w.svc.SetPhone(ctx, "u2", "37369123456"), "phone_taken") // same phone, other spelling
	wantCode(t, w.svc.SetPhone(ctx, "u2", "12ab"), "invalid_phone")
	wantCode(t, w.svc.SetPhone(ctx, "u2", "12345"), "invalid_phone")
	wantCode(t, w.svc.SetPhone(ctx, "ghost", "37369000000"), "account_not_found")
	// the same person may set their own phone again
	if err := w.svc.SetPhone(ctx, "pending", "37369123456"); err != nil {
		t.Fatalf("own phone again: %v", err)
	}
}

func TestChannelsLatestActive(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	w.channel(id, "telegram", "1", "active", 100)
	w.channel(id, "viber", "2", "blocked", 300) // newest, but the bot is blocked there
	w.channel(id, "web", "3", "active", 200)

	list, latest, err := w.svc.Channels(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "web" || len(list) != 3 || list[0].Channel != "viber" {
		t.Fatalf("latest=%q list=%+v", latest, list)
	}

	other := w.active("u2")
	w.channel(other, "telegram", "9", "stopped", 1)
	if _, latest, _ := w.svc.Channels(ctx, other); latest != "" {
		t.Fatalf("no active channel, but latest = %q", latest)
	}
}

func TestSettings(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")

	got, err := w.svc.Settings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"order_time": "16:00", "order_days": "1,2,3,4,5", "timezone": "Europe/Chisinau",
		"rec_for": "tomorrow", "notify_personal": "true", "notify_group": "true", "order_to": "group",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults: %v", got)
	}

	got, err = w.svc.SetSettings(ctx, id, map[string]string{"order_time": " 09:30 ", "order_days": "5, 1,3,3", "rec_for": "today"})
	if err != nil {
		t.Fatal(err)
	}
	if got["order_time"] != "09:30" || got["order_days"] != "1,3,5" || got["rec_for"] != "today" || got["timezone"] != "Europe/Chisinau" {
		t.Fatalf("after set: %v", got)
	}
	// it is saved
	if again, _ := w.svc.Settings(ctx, id); !reflect.DeepEqual(again, got) {
		t.Fatalf("not saved: %v", again)
	}
	// and it is only theirs
	other := w.active("u2")
	if o, _ := w.svc.Settings(ctx, other); o["order_time"] != "16:00" {
		t.Fatalf("settings leaked to another person: %v", o)
	}
}

func TestSettingsRefusals(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	for key, bad := range map[string]string{
		"order_time":      "25:00",
		"order_days":      "1,8",
		"timezone":        "Mars/Olympus",
		"rec_for":         "yesterday",
		"notify_personal": "maybe",
		"flavour":         "x", // unknown key
	} {
		e := wantCode(t, second(w.svc.SetSettings(ctx, id, map[string]string{key: bad})), "invalid_input")
		if e.Details["field"] != key {
			t.Errorf("%s: refusal names field %v", key, e.Details["field"])
		}
	}
	// "Local" would be the zone of the server
	wantCode(t, second(w.svc.SetSettings(ctx, id, map[string]string{"timezone": "Local"})), "invalid_input")
}

func TestSettingsAreAllOrNothing(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	_, err := w.svc.SetSettings(ctx, id, map[string]string{"order_time": "10:00", "order_days": "x"})
	wantCode(t, err, "invalid_input")
	if got, _ := w.svc.Settings(ctx, id); got["order_time"] != "16:00" {
		t.Fatalf("a valid value was saved although another one was refused: %v", got)
	}
}

func TestNoOrderDaysIsAllowed(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	got, err := w.svc.SetSettings(ctx, id, map[string]string{"order_days": ""})
	if err != nil || got["order_days"] != "" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestAllergens(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")

	got, err := w.svc.SetAllergens(ctx, id, []string{"  Ouă ", "GLUTEN", "ouă", "Lapte  de   vacă", "alune"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ouă", "gluten", "lapte de vacă", "alune"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// cedilla ş and ţ are the same letters as ș and ț
	got, _ = w.svc.SetAllergens(ctx, id, []string{"Ştiucă", "Ţelină"})
	if want := []string{"știucă", "țelină"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// the list is replaced, not added to
	if saved, _ := w.svc.Allergens(ctx, id); !reflect.DeepEqual(saved, got) {
		t.Fatalf("saved %v", saved)
	}
	// an empty list clears; the answer is [] and not null
	cleared, err := w.svc.SetAllergens(ctx, id, []string{})
	if err != nil || cleared == nil || len(cleared) != 0 {
		t.Fatalf("clear: %v %v", cleared, err)
	}
	if saved, _ := w.svc.Allergens(ctx, id); saved == nil || len(saved) != 0 {
		t.Fatalf("saved after clear: %#v", saved)
	}
}

func TestAllergenRefusals(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	wantCode(t, second(w.svc.SetAllergens(ctx, id, []string{"ok", "  "})), "invalid_input")
	wantCode(t, second(w.svc.SetAllergens(ctx, id, []string{strings.Repeat("a", 41)})), "invalid_input")

	many := make([]string, 31)
	for i := range many {
		many[i] = "a" + strings.Repeat("b", i)
	}
	e := wantCode(t, second(w.svc.SetAllergens(ctx, id, many)), "too_many")
	if e.Details["limit"] != 30 {
		t.Fatalf("details %v", e.Details)
	}
}

func TestPrefs(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")

	got, err := w.svc.SetPrefs(ctx, id, []Pref{
		{Kind: "dislike", Value: "  Ciuperci ", Hard: true},
		{Kind: "LIKE", Value: "Pește"},
		{Kind: "dislike", Value: "ciuperci"}, // repeated
		{Kind: "target", Value: "kcal 700"},
		{Kind: "target", Value: "Protein_g:40,5"},
		{Kind: "diet", Value: "fără carne"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Pref{
		{"dislike", "ciuperci", true}, {"like", "pește", false},
		{"target", "kcal=700", false}, {"target", "protein_g=40.5", false}, {"diet", "fără carne", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if saved, _ := w.svc.Prefs(ctx, id); !reflect.DeepEqual(saved, want) { // the order of the person is kept
		t.Fatalf("saved %v", saved)
	}
}

func TestPrefRefusals(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	for name, p := range map[string]Pref{
		"allergen kind":  {Kind: "allergen", Value: "gluten"},
		"unknown kind":   {Kind: "love", Value: "x"},
		"empty value":    {Kind: "like", Value: "  "},
		"long value":     {Kind: "like", Value: strings.Repeat("x", 101)},
		"target no name": {Kind: "target", Value: "700"},
		"target unknown": {Kind: "target", Value: "salt=3"},
		"target text":    {Kind: "target", Value: "kcal=lots"},
		"target zero":    {Kind: "target", Value: "kcal=0"},
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, second(w.svc.SetPrefs(ctx, id, []Pref{p})), "invalid_input")
		})
	}
}

func TestFoodGivesBothLists(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	w.svc.SetAllergens(ctx, id, []string{"gluten"})
	w.svc.SetPrefs(ctx, id, []Pref{{Kind: "like", Value: "supă"}})

	allergens, prefs, err := w.svc.Food(ctx, id)
	if err != nil || !reflect.DeepEqual(allergens, []string{"gluten"}) || len(prefs) != 1 {
		t.Fatalf("food: %v %v %v", allergens, prefs, err)
	}
	// and nobody sees another's
	other := w.active("u2")
	a2, p2, _ := w.svc.Food(ctx, other)
	if len(a2) != 0 || len(p2) != 0 {
		t.Fatalf("leak: %v %v", a2, p2)
	}
}

func TestTextNormalizers(t *testing.T) {
	for in, want := range map[string]string{"+37369123456": "+37369123456", "069 123 456": "+069123456", "(373) 69.123.456": "+37369123456"} {
		if got, ok := NormPhone(in); !ok || got != want {
			t.Errorf("NormPhone(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "1234567", "1234567890123456", "37369abc456", "tel:37369123456"} {
		if _, ok := NormPhone(bad); ok {
			t.Errorf("NormPhone(%q) was accepted", bad)
		}
	}
}
