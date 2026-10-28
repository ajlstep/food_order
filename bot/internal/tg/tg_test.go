package tg

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/contracts"
)

func f(v float64) *float64 { return &v }

func TestCallbackRoundTrip(t *testing.T) {
	cases := []struct {
		data string
		want Callback
	}{
		{encodeOrder("2026-10-29", 12, true, f(45.5)), Callback{Kind: kindOrder, Date: "2026-10-29", DishID: 12, Forced: true, Price: f(45.5)}},
		{encodeOrder("2026-10-29", 7, false, nil), Callback{Kind: kindOrder, Date: "2026-10-29", DishID: 7}},
		{encodeForced("2026-10-29", true), Callback{Kind: kindForced, Date: "2026-10-29", Forced: true}},
		{encodeRate(99, 4), Callback{Kind: kindRate, OrderID: 99, Rating: 4}},
		{encodeNoNote(99), Callback{Kind: kindNoNote, OrderID: 99}},
		{encodeAllergen("2026-10-29", 12, true), Callback{Kind: kindAllergen, Date: "2026-10-29", DishID: 12, Yes: true}},
	}
	for _, c := range cases {
		got, err := decodeCallback(c.data)
		if err != nil {
			t.Fatalf("%s: %v", c.data, err)
		}
		if got.Kind != c.want.Kind || got.Date != c.want.Date || got.DishID != c.want.DishID ||
			got.Forced != c.want.Forced || got.OrderID != c.want.OrderID || got.Rating != c.want.Rating ||
			got.Yes != c.want.Yes || (got.Price == nil) != (c.want.Price == nil) ||
			(got.Price != nil && *got.Price != *c.want.Price) {
			t.Errorf("%s: got %+v, want %+v", c.data, got, c.want)
		}
		if len(c.data) > callbackMax {
			t.Errorf("%s is longer than %d bytes", c.data, callbackMax)
		}
	}
}

func TestCallbackBad(t *testing.T) {
	for _, d := range []string{"", "x:1", "o:2026:1:0:-", "r:1:9", "r:abc:3", "o:20261029:1:0:abc", "a:20261029:1"} {
		if _, err := decodeCallback(d); err == nil {
			t.Errorf("%q: expected an error", d)
		}
	}
}

func TestLongestCallbackFits(t *testing.T) {
	d := encodeOrder("2026-12-31", 9_999_999_999, true, f(12345.67))
	if len(d) > callbackMax {
		t.Fatalf("%d bytes", len(d))
	}
}

var picks = []contracts.Pick{
	{DishID: 12, Name: "Ciorbă de pui", Price: f(45), Reason: "Ai dat 5 la ea", KcalMin: f(280), KcalMax: f(380), AllergensPossible: []string{"gluten", "ouă"}},
	{DishID: 15, Name: "Pârjoale <de casă>", Price: f(52.5)},
}

func TestRecommendationText(t *testing.T) {
	s := recommendationText("2026-10-29", picks)
	for _, want := range []string{"joi, 29 oct", "Ciorbă de pui", "45 lei", "~280–380 kcal", "posibil: gluten, ouă", "52,50 lei", "&lt;de casă&gt;"} {
		if !strings.Contains(s, want) {
			t.Errorf("text has no %q:\n%s", want, s)
		}
	}
}

func TestToggleForced(t *testing.T) {
	kb := recommendationKeyboard("2026-10-29", picks, false)
	// 2 dishes + switch + 1 allergen row
	if len(kb.InlineKeyboard) != 4 {
		t.Fatalf("rows = %d", len(kb.InlineKeyboard))
	}

	on := toggleForced(kb, "2026-10-29", true)
	first := on.InlineKeyboard[0][0]
	cb, _ := decodeCallback(first.CallbackData)
	if !cb.Forced || !strings.HasPrefix(first.Text, forcedPrefix) || cb.DishID != 12 || *cb.Price != 45 {
		t.Fatalf("forced button = %+v (%+v)", first, cb)
	}
	sw, _ := decodeCallback(on.InlineKeyboard[2][0].CallbackData)
	if sw.Kind != kindForced || sw.Forced {
		t.Fatalf("switch must now turn forced off: %+v", sw)
	}
	if on.InlineKeyboard[3][0].CallbackData != kb.InlineKeyboard[3][0].CallbackData {
		t.Fatal("allergen row must not change")
	}

	off := toggleForced(on, "2026-10-29", false)
	if off.InlineKeyboard[0][0].Text != kb.InlineKeyboard[0][0].Text ||
		off.InlineKeyboard[0][0].CallbackData != kb.InlineKeyboard[0][0].CallbackData {
		t.Fatalf("toggling back must give the original button: %+v", off.InlineKeyboard[0][0])
	}
}

func TestApplyPrefsCommand(t *testing.T) {
	cur := contracts.Prefs{Items: []contracts.Pref{{ID: 1, Kind: "allergen", Value: "lapte", Hard: true}}}

	next, changed, err := applyPrefsCommand(cur, "add nu! Ciuperci Pleurotus")
	if err != nil || !changed || len(next.Items) != 2 {
		t.Fatalf("add: %v %v %+v", changed, err, next)
	}
	if p := next.Items[1]; p.Kind != "dislike" || !p.Hard || p.Value != "ciuperci pleurotus" {
		t.Fatalf("added = %+v", p)
	}
	if len(cur.Items) != 1 {
		t.Fatal("the current list must not be changed in place")
	}

	if _, changed, _ := applyPrefsCommand(cur, "add alergen LAPTE"); changed {
		t.Fatal("a duplicate must not be added")
	}

	next, changed, err = applyPrefsCommand(next, "del 1")
	if err != nil || !changed || len(next.Items) != 1 || next.Items[0].Kind != "dislike" {
		t.Fatalf("del: %v %v %+v", changed, err, next)
	}

	for _, bad := range []string{"add", "add ceva x", "del 5", "del x", "sterge 1"} {
		if _, _, err := applyPrefsCommand(cur, bad); err != errPrefsUsage {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if _, changed, err := applyPrefsCommand(cur, ""); changed || err != nil {
		t.Fatal("no args = just show the list")
	}
}

func TestTextsDoNotPanic(t *testing.T) {
	r := 4
	_ = historyText([]contracts.Order{{ID: 1, Date: "2026-10-28", DishName: "Supă", Rating: &r, Forced: true, Comment: "prea sărat"}}, 15)
	_ = statsText(contracts.Stats{From: "2026-10-01", To: "2026-10-28", Orders: 3, Spent: 140, Kcal: &contracts.Range{Min: 900, Max: 1200}})
	_ = prefsText(contracts.Prefs{Items: []contracts.Pref{{Kind: "like", Value: "supă"}}})
	_ = alertText(contracts.Alert{Service: "core", Level: "error", Text: "<nu e meniu>"})
	_ = ratingKeyboard(1)
	_ = models.InlineKeyboardMarkup{}
}
