package rules

import (
	"testing"

	"food-order/core/internal/config"
	"food-order/core/internal/contracts"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

func cfg() config.Score { return config.Defaults().Score }

func ids(r Result) []int64 {
	var out []int64
	for _, s := range r.Ranked {
		out = append(out, s.Dish.ID)
	}
	return out
}

func TestNorm(t *testing.T) {
	for in, want := range map[string]string{
		"Ciorbă de PUI!":       "ciorba de pui",
		"  Mămăligă, brânză":   "mamaliga branza",
		"Ţelină / ţuică":       "telina tuica",
		"Сырники  со сметаной": "сырники со сметаной",
	} {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAllergenFilter(t *testing.T) {
	confirmedNone := []string{}
	dishes := []contracts.CatalogDish{
		{ID: 1, Name: "Supă de legume"},
		{ID: 2, Name: "Paste carbonara", Components: []contracts.CatalogComponent{{Name: "smântână"}}},
		{ID: 3, Name: "Pui la grătar", Allergens: contracts.CatalogAllergens{Given: []string{"lactate"}}},
		{ID: 4, Name: "Salată", Allergens: contracts.CatalogAllergens{Possible: []string{"lapte"}}},
		{ID: 5, Name: "Orez", Allergens: contracts.CatalogAllergens{Possible: []string{"lapte"}, Confirmed: &confirmedNone}},
		{ID: 6, Name: "Сырники со сметаной"},
	}
	res := Rank(Input{Date: "2026-10-30", Dishes: dishes, Prefs: []contracts.Pref{{Kind: KindAllergen, Value: "Lapte", Hard: true}}}, cfg())
	got := map[int64]bool{}
	for _, id := range ids(res) {
		got[id] = true
	}
	if !got[1] || !got[5] {
		t.Errorf("dishes 1 and 5 must stay, got %v", ids(res))
	}
	for _, id := range []int64{2, 3, 4, 6} {
		if got[id] {
			t.Errorf("dish %d must be excluded (milk), got %v", id, ids(res))
		}
	}
	if len(res.Excluded) != 4 {
		t.Errorf("excluded = %+v", res.Excluded)
	}
}

func TestEffectiveAllergens(t *testing.T) {
	none := []string{}
	nuts := []string{"nuci"}
	cases := []struct {
		a    contracts.CatalogAllergens
		want int
	}{
		{contracts.CatalogAllergens{Possible: []string{"a", "b"}}, 2},
		{contracts.CatalogAllergens{Given: []string{"a"}, Possible: []string{"b", "c"}}, 1},
		{contracts.CatalogAllergens{Possible: []string{"a"}, Confirmed: &none}, 0},
		{contracts.CatalogAllergens{Given: []string{"a"}, Confirmed: &nuts}, 2},
	}
	for n, c := range cases {
		if got := EffectiveAllergens(c.a); len(got) != c.want {
			t.Errorf("case %d: %v", n, got)
		}
	}
}

func TestHardDislikeAndDiet(t *testing.T) {
	dishes := []contracts.CatalogDish{
		{ID: 1, Name: "Cartofi prăjiți"},
		{ID: 2, Name: "Pește", Components: []contracts.CatalogComponent{{Name: "pește", Method: "fried"}}},
		{ID: 3, Name: "Ciuperci la cuptor", Tags: []string{"vegetarian"}},
		{ID: 4, Name: "Legume la abur", Tags: []string{"vegetarian"}},
	}
	prefs := []contracts.Pref{{Kind: KindDislike, Value: "prăjit", Hard: true}, {Kind: KindDislike, Value: "ciuperci", Hard: true}}
	if got := ids(Rank(Input{Date: "2026-10-30", Dishes: dishes, Prefs: prefs}, cfg())); len(got) != 1 || got[0] != 4 {
		t.Errorf("only dish 4 must stay, got %v", got)
	}
	diet := []contracts.Pref{{Kind: KindDiet, Value: "vegetarian", Hard: true}}
	if got := ids(Rank(Input{Date: "2026-10-30", Dishes: dishes, Prefs: diet}, cfg())); len(got) != 2 {
		t.Errorf("hard diet keeps only the vegetarian ones, got %v", got)
	}
}

func TestScoreOrder(t *testing.T) {
	dishes := []contracts.CatalogDish{{ID: 1, Name: "Zeamă"}, {ID: 2, Name: "Pilaf"}, {ID: 3, Name: "Sarmale"}}
	hist := []Past{
		{DishID: 3, Date: "2026-10-01", Rating: i(5)},
		{DishID: 3, Date: "2026-10-08", Rating: i(5)},
		{DishID: 2, Date: "2026-10-02", Rating: i(1), Labels: `{"sărat":"prea mult"}`},
	}
	res := Rank(Input{Date: "2026-10-30", Dishes: dishes, History: hist}, cfg())
	if got := ids(res); got[0] != 3 || got[2] != 2 {
		t.Errorf("order = %v, want 3 first and 2 last", got)
	}
	if res.Ranked[0].AvgRating == nil || *res.Ranked[0].AvgRating != 5 {
		t.Errorf("avg rating of 3: %v", res.Ranked[0].AvgRating)
	}

	// the same dish eaten yesterday loses its place
	hist = append(hist, Past{DishID: 3, Date: "2026-10-29"})
	res = Rank(Input{Date: "2026-10-30", Dishes: dishes, History: hist}, cfg())
	if ids(res)[0] == 3 {
		t.Errorf("eaten yesterday, must not stay first: %v", ids(res))
	}
	for _, s := range res.Ranked {
		if s.Dish.ID == 3 && s.Parts.Repeat != 1 {
			t.Errorf("repeat penalty = %v", s.Parts.Repeat)
		}
	}
}

func TestSoftPrefs(t *testing.T) {
	dishes := []contracts.CatalogDish{{ID: 1, Name: "Supă de pui"}, {ID: 2, Name: "Supă de vită"}}
	res := Rank(Input{Date: "2026-10-30", Dishes: dishes, Prefs: []contracts.Pref{{Kind: KindLike, Value: "vită"}}}, cfg())
	if ids(res)[0] != 2 {
		t.Errorf("like: %v", ids(res))
	}
	res = Rank(Input{Date: "2026-10-30", Dishes: dishes, Prefs: []contracts.Pref{{Kind: KindDislike, Value: "pui"}}}, cfg())
	if ids(res)[0] != 2 || len(res.Ranked) != 2 {
		t.Errorf("soft dislike only lowers: %v", ids(res))
	}
}

func TestTarget(t *testing.T) {
	near := contracts.CatalogDish{ID: 1, Name: "a", NutritionSource: "llm", Nutrition: &contracts.CatalogNutrition{Kcal: &contracts.Range{Min: 650, Max: 750}}}
	far := contracts.CatalogDish{ID: 2, Name: "b", NutritionSource: "llm", Nutrition: &contracts.CatalogNutrition{Kcal: &contracts.Range{Min: 1200, Max: 1400}}}
	none := contracts.CatalogDish{ID: 3, Name: "c"}
	tg := map[string]float64{"kcal": 700}
	if v := targetFit(near, tg, "mid"); v != 1 {
		t.Errorf("near mid = %v", v)
	}
	if v := targetFit(near, tg, "worst"); v >= 1 || v < 0.9 {
		t.Errorf("near worst = %v", v)
	}
	if v := targetFit(far, tg, "mid"); v > 0.2 {
		t.Errorf("far = %v", v)
	}
	if v := targetFit(none, tg, "mid"); v != 0.5 {
		t.Errorf("no values = %v", v)
	}
	if n, v, err := ParseTarget("kcal = 700"); err != nil || n != "kcal" || v != 700 {
		t.Errorf("ParseTarget: %v %v %v", n, v, err)
	}
	if _, _, err := ParseTarget("zahar=3"); err == nil {
		t.Error("unknown nutrient must fail")
	}
}

func TestTextMatching(t *testing.T) {
	tx := newText("Omletă cu ou de prepeliță și cașcaval")
	for _, kw := range []string{"ou", "omleta", "cascav"} {
		if !tx.has(norm(kw)) {
			t.Errorf("must find %q", kw)
		}
	}
	if newText("Rouă").has("ou") {
		t.Error(`"ou" is a whole word only`)
	}
	if !newText("cartofi la cuptor").has(norm("la cuptor")) {
		t.Error("phrase")
	}
}
