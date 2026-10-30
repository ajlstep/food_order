// Package contracts holds the JSON shapes catalog exchanges with the other
// services: HTTP bodies (core and bot -> catalog) and NATS events (menu
// source, llm, core). They follow the Obsidian notes
// projects/food-order/Comunicare and Catalog.
//
// core keeps its own copy of the catalog shapes in core/internal/contracts
// (CatalogDish, CatalogNutrition, ...) and bot has ConfirmAllergensRequest;
// keep the field names in sync. Every nutrition value is an interval
// {min, max}; an exact value has min == max.
package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// HeaderUserID carries users.id from bot.db (only the allergen confirmation
// needs it; catalog knows nothing else about users).
const HeaderUserID = "X-User-ID"

// NATS subjects.
const (
	// catalog listens
	SubjectMenuFetched  = "food.menu.fetched"
	SubjectDishAnalyzed = "food.dish.analyzed"
	SubjectDishEmbedded = "food.dish.embedded"

	// catalog publishes
	SubjectDishAnalyzeRequested = "food.dish.analyze.requested"
	SubjectDishEmbedRequested   = "food.dish.embed.requested"
	SubjectMenuReady            = "food.menu.ready"
	SubjectAlert                = "food.alert"
)

// ListenSubjects is the filter of catalog's durable consumer.
var ListenSubjects = []string{SubjectMenuFetched, SubjectDishAnalyzed, SubjectDishEmbedded}

// What catalog may ask llm for (the "need" list), in this order.
const (
	NeedComponents = "components"
	NeedNutrition  = "nutrition"
	NeedAllergens  = "allergens"
	NeedPortion    = "portion"
	NeedTags       = "tags"
)

// Cooking methods of a component; anything else is stored as "other".
var Methods = []string{"raw", "boiled", "steamed", "baked", "grilled", "fried", "deep_fried", "stewed", "smoked", "pickled", "other"}

// Envelope is the common wrapper of every event.
type Envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Version int             `json:"version"`
	TS      time.Time       `json:"ts"`
	Data    json.RawMessage `json:"data"`
}

// Range is a value known as an interval (exact => Min == Max).
//
// It is read tolerantly: {"min": 1, "max": 2}, {"value": 3}, 3, [1, 2];
// with only min or only max, the other end is the same.
type Range struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// UnmarshalJSON implements the tolerant reading described on Range.
func (r *Range) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	switch b[0] {
	case '{':
		var o struct {
			Min, Max, Value *float64
		}
		if err := json.Unmarshal(b, &o); err != nil {
			return err
		}
		switch {
		case o.Min != nil && o.Max != nil:
			r.Min, r.Max = *o.Min, *o.Max
		case o.Min != nil:
			r.Min, r.Max = *o.Min, *o.Min
		case o.Max != nil:
			r.Min, r.Max = *o.Max, *o.Max
		case o.Value != nil:
			r.Min, r.Max = *o.Value, *o.Value
		default:
			return fmt.Errorf("range: expected min/max or value")
		}
	case '[':
		var a []float64
		if err := json.Unmarshal(b, &a); err != nil {
			return err
		}
		switch len(a) {
		case 1:
			r.Min, r.Max = a[0], a[0]
		case 2:
			r.Min, r.Max = a[0], a[1]
		default:
			return fmt.Errorf("range: expected [min, max]")
		}
	default:
		var f float64
		if err := json.Unmarshal(b, &f); err != nil {
			return fmt.Errorf("range: %w", err)
		}
		r.Min, r.Max = f, f
	}
	return nil
}

// Valid says whether the range can be stored (finite, not negative).
func (r Range) Valid() bool {
	ok := func(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 }
	return ok(r.Min) && ok(r.Max)
}

// Nutrition is the nutrition of ONE PORTION of the cooked dish.
type Nutrition struct {
	Kcal          *Range `json:"kcal,omitempty"`
	ProteinG      *Range `json:"protein_g,omitempty"`
	FatG          *Range `json:"fat_g,omitempty"`
	SaturatedFatG *Range `json:"saturated_fat_g,omitempty"`
	CarbsG        *Range `json:"carbs_g,omitempty"`
	SugarsG       *Range `json:"sugars_g,omitempty"`
	FiberG        *Range `json:"fiber_g,omitempty"`
	SaltG         *Range `json:"salt_g,omitempty"`
}

// Fields returns pointers to every value, in the column order of catalog.db.
func (n *Nutrition) Fields() []**Range {
	return []**Range{&n.Kcal, &n.ProteinG, &n.FatG, &n.SaturatedFatG, &n.CarbsG, &n.SugarsG, &n.FiberG, &n.SaltG}
}

// Clean drops invalid values, swaps a reversed min/max and returns nil
// when nothing is left.
func (n *Nutrition) Clean() *Nutrition {
	if n == nil {
		return nil
	}
	out := *n
	has := false
	for _, f := range out.Fields() {
		if *f == nil {
			continue
		}
		r := **f
		if !r.Valid() {
			*f = nil
			continue
		}
		if r.Min > r.Max {
			r.Min, r.Max = r.Max, r.Min
		}
		*f = &r
		has = true
	}
	if !has {
		return nil
	}
	return &out
}

// Scale multiplies every value (min and max) by k: "per 100 g" -> portion.
func (n *Nutrition) Scale(k float64) *Nutrition {
	if n == nil {
		return nil
	}
	out := *n
	for _, f := range out.Fields() {
		if *f != nil {
			r := Range{Min: (*f).Min * k, Max: (*f).Max * k}
			*f = &r
		}
	}
	return &out
}

// Component is one part of a dish: what, how many grams, how it is cooked.
type Component struct {
	Name   string   `json:"name"`
	Grams  *float64 `json:"grams,omitempty"`
	Method string   `json:"method,omitempty"`
}

// UnmarshalJSON also accepts a bare string ("Supă de linte"), as the parser
// sends the parts of a set menu.
func (c *Component) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &c.Name)
	}
	type plain Component
	var p struct {
		plain
		Method2 string `json:"cooking_method"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Component(p.plain)
	if c.Method == "" {
		c.Method = p.Method2
	}
	return nil
}

// NormMethod returns one of Methods ("" stays "").
func NormMethod(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if m == "" {
		return ""
	}
	m = strings.ReplaceAll(strings.ReplaceAll(m, "-", "_"), " ", "_")
	for _, ok := range Methods {
		if m == ok {
			return m
		}
	}
	return "other"
}

// ---------------------------------------------------------------- events

// MenuFetched: menu source (parser now, api later) -> catalog.
// Revision is optional (top level or per day): without it catalog counts
// the revisions itself.
type MenuFetched struct {
	RequestID string    `json:"request_id"`
	Source    string    `json:"source"` // parser | api
	Revision  int       `json:"revision,omitempty"`
	Days      []MenuDay `json:"days"`
}

// MenuDay is the menu of one day.
type MenuDay struct {
	Date     string       `json:"date"` // 2026-10-28
	Revision int          `json:"revision,omitempty"`
	Items    []SourceItem `json:"items"`
}

// SourceItem is one dish as the source gave it. Everything but the name is
// optional: the parser gives little (name, price, components as text), the
// future api will give more.
type SourceItem struct {
	ExternalID  string      `json:"external_id,omitempty"`
	Name        string      `json:"name"`
	Category    string      `json:"category,omitempty"`
	Price       *float64    `json:"price,omitempty"`
	WeightG     *float64    `json:"weight_g,omitempty"`
	WeightText  string      `json:"weight_text,omitempty"`
	Weight      string      `json:"weight,omitempty"` // the parser's name for weight_text
	Description string      `json:"description,omitempty"`
	ImageURL    string      `json:"image_url,omitempty"`
	Components  []Component `json:"components,omitempty"`
	// Nutrition of the cooked dish, per portion unless "per": "100g".
	// Values as Range (number or {min, max}).
	Nutrition json.RawMessage `json:"nutrition,omitempty"`
	// nil = the source said nothing; [] = the source says "no allergens".
	Allergens []string `json:"allergens,omitempty"`
}

// Known is what catalog already has about a dish (sent to llm as context).
type Known struct {
	Components []Component `json:"components,omitempty"`
	Nutrition  *Nutrition  `json:"nutrition,omitempty"`
	Allergens  []string    `json:"allergens,omitempty"`
}

// DishAnalyzeRequested: catalog -> llm. Need lists only what is missing.
type DishAnalyzeRequested struct {
	MenuItemID  int64    `json:"menu_item_id"`
	DishID      int64    `json:"dish_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	WeightG     *float64 `json:"weight_g,omitempty"`
	WeightText  string   `json:"weight_text,omitempty"`
	ImageURL    string   `json:"image_url,omitempty"`
	Known       Known    `json:"known"`
	Need        []string `json:"need"`
}

// DishAnalyzed: llm -> catalog. Only the fields asked for in need.
// A nil list = not answered; an empty list = "none".
type DishAnalyzed struct {
	MenuItemID        int64       `json:"menu_item_id"`
	Components        []Component `json:"components,omitempty"`
	TypicalPortionG   *float64    `json:"typical_portion_g,omitempty"`
	Nutrition         *Nutrition  `json:"nutrition,omitempty"`
	AllergensPossible []string    `json:"allergens_possible,omitempty"`
	Tags              []string    `json:"tags,omitempty"`
	Model             string      `json:"model"`
}

// DishEmbedRequested: catalog -> llm.
type DishEmbedRequested struct {
	DishID int64  `json:"dish_id"`
	Text   string `json:"text"`
}

// DishEmbedded: llm -> catalog.
type DishEmbedded struct {
	DishID int64     `json:"dish_id"`
	Model  string    `json:"model"`
	Vector []float64 `json:"vector"`
}

// MenuReady: catalog -> core, every dish of the request has what it needs
// (or llm did not answer in time and the dish goes with what there is).
type MenuReady struct {
	RequestID string  `json:"request_id"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	DishIDs   []int64 `json:"dish_ids"`
}

// Alert: any service -> bot -> admin chat.
type Alert struct {
	Service string `json:"service"`
	Level   string `json:"level"` // info | warn | error
	Text    string `json:"text"`
	UserID  int64  `json:"user_id,omitempty"`
}

// ------------------------------------------------------------------ HTTP

// DishComponent is a component as the API shows it, with its source.
type DishComponent struct {
	Name   string   `json:"name"`
	Grams  *float64 `json:"grams,omitempty"`
	Method string   `json:"method,omitempty"`
	Source string   `json:"source"` // site | kitchen | llm
}

// Allergens of a dish by source. Priority: given > confirmed > possible.
// Confirmed == nil: nobody confirmed; empty list: confirmed "none".
type Allergens struct {
	Given     []string  `json:"given,omitempty"`
	Confirmed *[]string `json:"confirmed,omitempty"`
	Possible  []string  `json:"possible,omitempty"`
}

// Dish is one dish with the values of the menu of one day (core reads it
// as CatalogDish).
type Dish struct {
	ID              int64           `json:"id"` // dishes.id = dish_id everywhere
	MenuItemID      int64           `json:"menu_item_id"`
	Date            string          `json:"date"`
	Name            string          `json:"name"`
	Category        string          `json:"category,omitempty"`
	Price           *float64        `json:"price,omitempty"`
	Description     string          `json:"description,omitempty"`
	ImageURL        string          `json:"image_url,omitempty"`
	WeightG         *float64        `json:"weight_g,omitempty"`
	WeightText      string          `json:"weight_text,omitempty"`
	WeightSource    string          `json:"weight_source,omitempty"` // site | kitchen | llm | me
	Nutrition       *Nutrition      `json:"nutrition,omitempty"`     // per portion
	NutritionSource string          `json:"nutrition_source"`        // llm | kitchen | missing
	Components      []DishComponent `json:"components,omitempty"`
	Allergens       Allergens       `json:"allergens"`
	Tags            []string        `json:"tags,omitempty"`
	Status          string          `json:"status"`   // pending | analyzed | confirmed
	Analysis        string          `json:"analysis"` // none | pending | requested | done | timeout | off
	Source          string          `json:"source"`   // parser | api
	Revision        int             `json:"revision"`
}

// MenuResponse is the answer of GET /v1/menu.
type MenuResponse struct {
	Date  string `json:"date"`
	Items []Dish `json:"items"`
}

// DishesResponse is the answer of GET /v1/dishes.
type DishesResponse struct {
	Items []Dish `json:"items"`
}

// SimilarRequest is the body of POST /v1/similar.
type SimilarRequest struct {
	LikedIDs     []int64 `json:"liked_ids"`
	CandidateIDs []int64 `json:"candidate_ids"`
}

// SimilarScore is one line of the answer of POST /v1/similar (0..1).
type SimilarScore struct {
	ID     int64   `json:"id"`
	Score  float64 `json:"score"`
	LikeID int64   `json:"like_id"` // the liked dish it resembles most
}

// SimilarResponse is the answer of POST /v1/similar. Candidates without an
// embedding are left out.
type SimilarResponse struct {
	Scores []SimilarScore `json:"scores"`
}

// ConfirmAllergensRequest is the body of POST /v1/dishes/{id}/allergens/confirm.
// An empty list means "confirmed: no allergens".
type ConfirmAllergensRequest struct {
	Allergens []string `json:"allergens"`
}

// ImportResponse is the answer of POST /v1/import (debugging).
type ImportResponse struct {
	RequestID string      `json:"request_id"`
	Days      []DayImport `json:"days"`
}

// DayImport says what happened to one day of an imported menu.
type DayImport struct {
	Date     string `json:"date"`
	Revision int    `json:"revision"`
	Items    int    `json:"items"`
	Skipped  bool   `json:"skipped,omitempty"` // an older revision than the saved one
	Changed  bool   `json:"changed"`
}

// ErrorBody is the common error answer: {"error": {"code", "message"}}.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inside of ErrorBody (message in Romanian: the bot may
// show it).
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
