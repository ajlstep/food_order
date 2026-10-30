// Package rules is the deterministic part of the recommendation: the hard
// filters (allergens, what the user never eats) and the score. The model
// only orders the top of this list and writes the reason.
package rules

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"food-order/core/internal/config"
	"food-order/core/internal/contracts"
)

// Pref kinds.
const (
	KindAllergen = "allergen"
	KindDislike  = "dislike"
	KindLike     = "like"
	KindDiet     = "diet"
	KindTarget   = "target" // "kcal=700": daily target for one meal (provisional, see README)
)

// Kinds are the accepted preference kinds.
var Kinds = map[string]bool{KindAllergen: true, KindDislike: true, KindLike: true, KindDiet: true, KindTarget: true}

// Past is one past order of the user, as the score needs it.
type Past struct {
	DishID int64
	Name   string
	Date   string
	Rating *int
	Forced bool
	Labels string // raw JSON object from llm, "" = none
}

// Input is everything the score looks at for one user and one day.
type Input struct {
	Date       string // the day of the menu
	Dishes     []contracts.CatalogDish
	Prefs      []contracts.Pref
	History    []Past
	Similarity map[int64]float64 // from catalog, may be nil
}

// Parts is the breakdown of a score (for logs and tests).
type Parts struct {
	Rating, Frequency, Similarity, Like, Target, Novelty, Penalty, Repeat float64
}

// Scored is a dish that passed the hard filters, with its score.
type Scored struct {
	Dish      contracts.CatalogDish
	Score     float64
	Parts     Parts
	AvgRating *float64 // the user's plain mean rating of this dish, if rated
}

// Excluded is a dish removed by a hard filter.
type Excluded struct {
	DishID int64
	Name   string
	Reason string
}

// Result of Rank: the dishes by score (best first) and the excluded ones.
type Result struct {
	Ranked   []Scored
	Excluded []Excluded
}

// Rank applies the hard filters and orders the rest by score.
func Rank(in Input, cfg config.Score) Result {
	var res Result
	targets := Targets(in.Prefs)
	stats := historyStats(in.History, in.Date)

	for _, d := range in.Dishes {
		t := dishText(d)
		if reason := hardFilter(d, t, in.Prefs); reason != "" {
			res.Excluded = append(res.Excluded, Excluded{DishID: d.ID, Name: d.Name, Reason: reason})
			continue
		}
		res.Ranked = append(res.Ranked, score(d, t, in, stats, targets, cfg))
	}

	// frequency is relative to the alternatives of the day
	maxCount := 0
	for _, s := range res.Ranked {
		if c := stats.count[s.Dish.ID]; c > maxCount {
			maxCount = c
		}
	}
	for i := range res.Ranked {
		s := &res.Ranked[i]
		if maxCount > 0 {
			s.Parts.Frequency = float64(stats.count[s.Dish.ID]) / float64(maxCount)
		}
		w := cfg.Weights
		s.Score = w.Rating*s.Parts.Rating + w.Frequency*s.Parts.Frequency + w.Similarity*s.Parts.Similarity +
			w.Like*s.Parts.Like + w.Target*s.Parts.Target + w.Novelty*s.Parts.Novelty -
			w.Penalty*s.Parts.Penalty - w.Repeat*s.Parts.Repeat
		s.Score = math.Round(s.Score*10000) / 10000
	}
	sort.SliceStable(res.Ranked, func(i, j int) bool {
		if res.Ranked[i].Score != res.Ranked[j].Score {
			return res.Ranked[i].Score > res.Ranked[j].Score
		}
		return res.Ranked[i].Dish.ID < res.Ranked[j].Dish.ID
	})
	return res
}

// dishText is what preferences are matched against: name, category,
// components, cooking methods (with the words people use) and tags.
func dishText(d contracts.CatalogDish) text {
	parts := []string{d.Name, d.Category}
	for _, c := range d.Components {
		parts = append(parts, c.Name)
		parts = append(parts, methodWords[c.Method]...)
	}
	parts = append(parts, d.Tags...)
	return newText(parts...)
}

// componentText is only what the dish is made of (for allergen words).
func componentText(d contracts.CatalogDish) text {
	parts := []string{d.Name}
	for _, c := range d.Components {
		parts = append(parts, c.Name)
	}
	return newText(parts...)
}

// EffectiveAllergens: given by the source > confirmed by a user > guessed
// by the model. A confirmation (even "none") replaces the guess.
func EffectiveAllergens(a contracts.CatalogAllergens) []string {
	out := append([]string{}, a.Given...)
	if a.Confirmed != nil {
		return append(out, (*a.Confirmed)...)
	}
	if len(out) > 0 {
		return out
	}
	return append(out, a.Possible...)
}

// hardFilter returns why a dish must not be recommended ("" = it may).
func hardFilter(d contracts.CatalogDish, t text, prefs []contracts.Pref) string {
	allergens := newText(strings.Join(EffectiveAllergens(d.Allergens), " ; "))
	components := componentText(d)
	for _, p := range prefs {
		v := norm(p.Value)
		if v == "" {
			continue
		}
		switch {
		case p.Kind == KindAllergen:
			kws := allergenKeywords(p.Value)
			if allergens.anyOf(kws) {
				return "alergen: " + p.Value
			}
			if components.anyOf(kws) {
				return "alergen (în componente): " + p.Value
			}
		case p.Kind == KindDislike && p.Hard:
			if t.has(v) {
				return "nu mănâncă: " + p.Value
			}
		case p.Kind == KindDiet && p.Hard:
			if !t.has(v) {
				return "nu se potrivește cu dieta: " + p.Value
			}
		}
	}
	return ""
}

type history struct {
	count     map[int64]int     // non-forced choices of the dish
	ratingSum map[int64]float64 // ratings of the dish
	ratingN   map[int64]int     // how many ratings
	lastDays  map[int64]int     // days since the dish was last chosen (>= 1)
	labels    map[int64]int     // complaint labels on past orders of the dish
	ordered   map[int64]bool    // chosen at least once
	userMean  float64           // mean of all the user's ratings (3 if none)
}

func historyStats(h []Past, today string) history {
	st := history{
		count: map[int64]int{}, ratingSum: map[int64]float64{}, ratingN: map[int64]int{},
		lastDays: map[int64]int{}, labels: map[int64]int{}, ordered: map[int64]bool{}, userMean: 3,
	}
	day, _ := time.Parse("2006-01-02", today)
	sum, n := 0.0, 0
	for _, p := range h {
		st.ordered[p.DishID] = true
		if !p.Forced {
			st.count[p.DishID]++
		}
		if p.Rating != nil {
			st.ratingSum[p.DishID] += float64(*p.Rating)
			st.ratingN[p.DishID]++
			sum += float64(*p.Rating)
			n++
		}
		if pd, err := time.Parse("2006-01-02", p.Date); err == nil && !day.IsZero() {
			ago := int(day.Sub(pd).Hours() / 24)
			if ago >= 1 {
				if cur, ok := st.lastDays[p.DishID]; !ok || ago < cur {
					st.lastDays[p.DishID] = ago
				}
			}
		}
		st.labels[p.DishID] += countLabels(p.Labels)
	}
	if n > 0 {
		st.userMean = sum / float64(n)
	}
	return st
}

// countLabels counts the entries of a labels JSON object ({"sărat": "prea mult"}).
func countLabels(raw string) int {
	if raw == "" {
		return 0
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return 0
	}
	return len(m)
}

func score(d contracts.CatalogDish, t text, in Input, st history, targets map[string]float64, cfg config.Score) Scored {
	s := Scored{Dish: d}

	// rating: Bayesian mean pulled towards the user's own mean, mapped 1..5 -> 0..1
	n := float64(st.ratingN[d.ID])
	bayes := (cfg.BayesC*st.userMean + st.ratingSum[d.ID]) / (cfg.BayesC + n)
	if cfg.BayesC+n == 0 {
		bayes = st.userMean
	}
	s.Parts.Rating = clamp((bayes-1)/4, 0, 1)
	if n > 0 {
		avg := math.Round(st.ratingSum[d.ID]/n*100) / 100
		s.AvgRating = &avg
	}

	s.Parts.Similarity = clamp(in.Similarity[d.ID], 0, 1)

	likes, dislikes := 0, 0
	for _, p := range in.Prefs {
		v := norm(p.Value)
		if v == "" || !t.has(v) {
			continue
		}
		switch {
		case p.Kind == KindLike, p.Kind == KindDiet && !p.Hard:
			likes++
		case p.Kind == KindDislike && !p.Hard:
			dislikes++
		}
	}
	s.Parts.Like = clamp(0.5*float64(likes), 0, 1)
	s.Parts.Penalty = clamp(0.5*float64(dislikes)+0.25*float64(st.labels[d.ID]), 0, 1)

	s.Parts.Target = targetFit(d, targets, cfg.TargetMode)

	if !st.ordered[d.ID] {
		s.Parts.Novelty = 1
	}
	if ago, ok := st.lastDays[d.ID]; ok && cfg.RepeatDays > 0 && ago <= cfg.RepeatDays {
		s.Parts.Repeat = float64(cfg.RepeatDays-ago+1) / float64(cfg.RepeatDays)
	}
	return s
}

// targetFit: 1 = on target, 0 = far away. A dish without values gets 0.5
// (it does not take part), as does everybody when there is no target.
func targetFit(d contracts.CatalogDish, targets map[string]float64, mode string) float64 {
	if len(targets) == 0 || d.Nutrition == nil || d.NutritionSource == "missing" {
		return 0.5
	}
	values := map[string]*contracts.Range{
		"kcal": d.Nutrition.Kcal, "protein_g": d.Nutrition.ProteinG,
		"fat_g": d.Nutrition.FatG, "carbs_g": d.Nutrition.CarbsG,
	}
	sum, n := 0.0, 0
	for name, target := range targets {
		r := values[name]
		if r == nil || target <= 0 {
			continue
		}
		v := (r.Min + r.Max) / 2
		if mode == "worst" {
			v = r.Min
			if math.Abs(r.Max-target) > math.Abs(r.Min-target) {
				v = r.Max
			}
		}
		sum += clamp(1-math.Abs(v-target)/target, 0, 1)
		n++
	}
	if n == 0 {
		return 0.5
	}
	return sum / float64(n)
}

// Targets reads the "target" preferences ("kcal=700", "protein_g=40").
func Targets(prefs []contracts.Pref) map[string]float64 {
	out := map[string]float64{}
	for _, p := range prefs {
		if p.Kind != KindTarget {
			continue
		}
		if name, v, err := ParseTarget(p.Value); err == nil {
			out[name] = v
		}
	}
	return out
}

// TargetNames are the nutrients a target can be set for.
var TargetNames = map[string]bool{"kcal": true, "protein_g": true, "fat_g": true, "carbs_g": true}

// ParseTarget reads "kcal=700" (also "kcal 700" or "kcal:700").
func ParseTarget(s string) (string, float64, error) {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r == '=' || r == ':' || r == ' ' })
	if len(f) != 2 || !TargetNames[f[0]] {
		return "", 0, fmt.Errorf("țintă: scrie de exemplu kcal=700 (kcal, protein_g, fat_g, carbs_g)")
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(f[1], ",", "."), 64)
	if err != nil || v <= 0 || v > 10000 {
		return "", 0, fmt.Errorf("țintă: %q nu e un număr pozitiv", f[1])
	}
	return f[0], v, nil
}

func clamp(f, lo, hi float64) float64 {
	switch {
	case f < lo:
		return lo
	case f > hi:
		return hi
	}
	return f
}
