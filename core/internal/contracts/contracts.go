// Package contracts holds the JSON shapes core exchanges with the other
// services: HTTP bodies (bot -> core, core -> catalog) and NATS events.
// They follow the Obsidian notes projects/food-order/Comunicare and Core.
//
// The bot has a copy of the bot-facing types in bot/internal/contracts; keep
// the field names in sync. Types marked "assumed" are not fixed in the notes
// yet (catalog does not exist): core reads them tolerantly.
package contracts

import (
	"encoding/json"
	"time"
)

// HeaderUserID carries users.id from bot.db on every call from the bot.
const HeaderUserID = "X-User-ID"

// NATS subjects.
const (
	// core publishes
	SubjectMenuRequested           = "food.menu.requested"
	SubjectRecommendationRequested = "food.recommendation.requested"
	SubjectRecommendationReady     = "food.recommendation.ready"
	SubjectRatingRequested         = "food.rating.requested"
	SubjectFeedbackParseRequested  = "food.feedback.parse.requested"
	SubjectAlert                   = "food.alert"

	// core listens
	SubjectMenuReady               = "food.menu.ready"
	SubjectMenuError               = "food.menu.error"
	SubjectRecommendationGenerated = "food.recommendation.generated"
	SubjectFeedbackParsed          = "food.feedback.parsed"
)

// ListenSubjects is the filter of core's durable consumer.
var ListenSubjects = []string{
	SubjectMenuReady,
	SubjectMenuError,
	SubjectRecommendationGenerated,
	SubjectFeedbackParsed,
}

// Menu error codes sent by the menu source (parser now, api later).
const (
	MenuErrNoMenu            = "no_menu"
	MenuErrSourceUnavailable = "source_unavailable"
	MenuErrParseFailed       = "parse_failed"
	MenuErrBadRequest        = "bad_request"
)

// Envelope is the common wrapper of every event.
type Envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Version int             `json:"version"`
	TS      time.Time       `json:"ts"`
	Data    json.RawMessage `json:"data"`
}

// Range is a value known as an interval (exact => Min == Max).
type Range struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// ---------------------------------------------------------------- events

// MenuRequested: core -> active menu source.
type MenuRequested struct {
	RequestID string `json:"request_id"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
}

// MenuReady: catalog -> core, the whole menu of the request is in catalog.
type MenuReady struct {
	RequestID string  `json:"request_id"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	DishIDs   []int64 `json:"dish_ids"`
}

// MenuError: menu source -> core.
type MenuError struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// Candidate is one dish sent to llm for the recommendation. No user data.
type Candidate struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Price       *float64 `json:"price,omitempty"`
	PortionG    *float64 `json:"portion_g,omitempty"`
	KcalMin     *float64 `json:"kcal_min,omitempty"`
	KcalMax     *float64 `json:"kcal_max,omitempty"`
	ProteinGMin *float64 `json:"protein_g_min,omitempty"`
	ProteinGMax *float64 `json:"protein_g_max,omitempty"`
	FatGMin     *float64 `json:"fat_g_min,omitempty"`
	FatGMax     *float64 `json:"fat_g_max,omitempty"`
	CarbsGMin   *float64 `json:"carbs_g_min,omitempty"`
	CarbsGMax   *float64 `json:"carbs_g_max,omitempty"`
	Tags        []string `json:"tags"`
	AvgRating   *float64 `json:"avg_rating,omitempty"`
}

// ContextOrder is one recent meal in the short context for llm.
type ContextOrder struct {
	Date   string `json:"date"`
	Name   string `json:"name"`
	Rating *int   `json:"rating,omitempty"`
	Forced bool   `json:"forced,omitempty"`
}

// RecommendationContext is the short context for llm (assumed shape):
// food facts only, never names, phones or chat ids.
type RecommendationContext struct {
	MaxPicks   int            `json:"max_picks"`
	Recent     []ContextOrder `json:"recent"`      // last 5-7 days
	Liked      []string       `json:"liked"`       // top dishes by rating
	Disliked   []string       `json:"disliked"`    // dishes rated low
	Likes      []string       `json:"likes"`       // soft "like" preferences
	Avoid      []string       `json:"avoid"`       // soft "dislike" preferences
	MonthSpent float64        `json:"month_spent"` // money spent this month
}

// RecommendationRequested: core -> llm. core links request_id to user_id.
type RecommendationRequested struct {
	RequestID  string                `json:"request_id"`
	Date       string                `json:"date"`
	Candidates []Candidate           `json:"candidates"`
	Context    RecommendationContext `json:"context"`
}

// GeneratedPick is one pick returned by llm (only ids from the list).
type GeneratedPick struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// RecommendationGenerated: llm -> core.
type RecommendationGenerated struct {
	RequestID string          `json:"request_id"`
	Picks     []GeneratedPick `json:"picks"`
}

// Pick is one recommended dish as the bot shows it.
type Pick struct {
	DishID            int64    `json:"dish_id"`
	Name              string   `json:"name"`
	Price             *float64 `json:"price,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	KcalMin           *float64 `json:"kcal_min,omitempty"`
	KcalMax           *float64 `json:"kcal_max,omitempty"`
	AllergensPossible []string `json:"allergens_possible,omitempty"`
}

// RecommendationReady: core -> bot.
type RecommendationReady struct {
	UserID int64  `json:"user_id"`
	Date   string `json:"date"`
	Picks  []Pick `json:"picks"`
}

// RatingRequested: core -> bot, the day after the meal.
type RatingRequested struct {
	UserID  int64 `json:"user_id"`
	OrderID int64 `json:"order_id"`
}

// FeedbackParseRequested: core -> llm.
type FeedbackParseRequested struct {
	OrderID int64  `json:"order_id"`
	Rating  int    `json:"rating"`
	Comment string `json:"comment"`
}

// FeedbackParsed: llm -> core. Labels is kept as raw JSON (e.g.
// {"sărat": "prea mult"}).
type FeedbackParsed struct {
	OrderID         int64           `json:"order_id"`
	Labels          json.RawMessage `json:"labels"`
	KitchenFeedback string          `json:"kitchen_feedback"`
}

// Alert: any service -> bot -> admin chat.
type Alert struct {
	Service string `json:"service"`
	Level   string `json:"level"` // info | warn | error
	Text    string `json:"text"`
	UserID  int64  `json:"user_id,omitempty"`
}

// ------------------------------------------------------------ HTTP: core

// CreateRecommendationRequest is the body of POST /v1/recommendations.
type CreateRecommendationRequest struct {
	Date string `json:"date"`
}

// Recommendation is the answer of GET (and POST) /v1/recommendations.
type Recommendation struct {
	Date   string `json:"date"`
	Status string `json:"status"` // waiting_menu | generating | ready
	Picks  []Pick `json:"picks"`
}

// CreateOrderRequest is the body of POST /v1/orders.
type CreateOrderRequest struct {
	Date   string   `json:"date"`
	DishID int64    `json:"dish_id"`
	Price  *float64 `json:"price,omitempty"`
	Forced bool     `json:"forced"`
}

// Order is one saved choice with its snapshot.
type Order struct {
	ID              int64           `json:"id"`
	Date            string          `json:"date"`
	DishID          int64           `json:"dish_id"`
	DishName        string          `json:"dish_name"`
	Price           *float64        `json:"price,omitempty"`
	Rating          *int            `json:"rating,omitempty"`
	Comment         string          `json:"comment,omitempty"`
	Labels          json.RawMessage `json:"labels,omitempty"`
	Forced          bool            `json:"forced"`
	PortionG        *float64        `json:"portion_g,omitempty"`
	KcalMin         *float64        `json:"kcal_min,omitempty"`
	KcalMax         *float64        `json:"kcal_max,omitempty"`
	ProteinGMin     *float64        `json:"protein_g_min,omitempty"`
	ProteinGMax     *float64        `json:"protein_g_max,omitempty"`
	FatGMin         *float64        `json:"fat_g_min,omitempty"`
	FatGMax         *float64        `json:"fat_g_max,omitempty"`
	CarbsGMin       *float64        `json:"carbs_g_min,omitempty"`
	CarbsGMax       *float64        `json:"carbs_g_max,omitempty"`
	NutritionSource string          `json:"nutrition_source,omitempty"` // llm | kitchen | missing
}

// RatingRequest is the body of POST /v1/orders/{id}/rating.
type RatingRequest struct {
	Rating  int    `json:"rating"`
	Comment string `json:"comment,omitempty"`
}

// Pref is one food preference.
type Pref struct {
	ID    int64  `json:"id,omitempty"`
	Kind  string `json:"kind"` // allergen | dislike | like | diet | target
	Value string `json:"value"`
	Hard  bool   `json:"hard"`
}

// Prefs is the body of GET and PUT /v1/prefs.
type Prefs struct {
	Items []Pref `json:"items"`
}

// DishCount is one line of the "favourite dishes" list.
type DishCount struct {
	DishID    int64    `json:"dish_id"`
	Name      string   `json:"name"`
	Count     int      `json:"count"`
	AvgRating *float64 `json:"avg_rating,omitempty"`
}

// DayStats is one day in GET /v1/stats.
type DayStats struct {
	Date             string   `json:"date"`
	Orders           int      `json:"orders"`
	Spent            float64  `json:"spent"`
	Kcal             *Range   `json:"kcal,omitempty"`
	ProteinG         *Range   `json:"protein_g,omitempty"`
	FatG             *Range   `json:"fat_g,omitempty"`
	CarbsG           *Range   `json:"carbs_g,omitempty"`
	WithoutNutrition int      `json:"without_nutrition"`
	Rating           *float64 `json:"rating,omitempty"`
}

// MonthStats is one month in GET /v1/stats.
type MonthStats struct {
	Month  string  `json:"month"` // 2026-10
	Orders int     `json:"orders"`
	Spent  float64 `json:"spent"`
}

// Stats is the answer of GET /v1/stats. Nutrition totals are intervals:
// min and max are summed separately.
type Stats struct {
	From             string       `json:"from"`
	To               string       `json:"to"`
	Orders           int          `json:"orders"`
	Spent            float64      `json:"spent"`
	Forced           int          `json:"forced"`
	ForcedPercent    float64      `json:"forced_percent"`
	Rated            int          `json:"rated"`
	AvgRating        *float64     `json:"avg_rating,omitempty"`
	Kcal             *Range       `json:"kcal,omitempty"`
	ProteinG         *Range       `json:"protein_g,omitempty"`
	FatG             *Range       `json:"fat_g,omitempty"`
	CarbsG           *Range       `json:"carbs_g,omitempty"`
	WithoutNutrition int          `json:"without_nutrition"`
	TopDishes        []DishCount  `json:"top_dishes"`
	Days             []DayStats   `json:"days"`
	Months           []MonthStats `json:"months"`
}

// ErrorBody is the common error answer: {"error": {"code", "message"}}.
// The bot shows Message to the user, so it is written in Romanian.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inside of ErrorBody.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// --------------------------------------------------------- HTTP: catalog
// (assumed shapes: catalog is not written yet)

// CatalogNutrition is the nutrition of one portion. Only the values that
// go into the snapshot and the score are read; the rest stays in catalog.
type CatalogNutrition struct {
	Kcal     *Range `json:"kcal,omitempty"`
	ProteinG *Range `json:"protein_g,omitempty"`
	FatG     *Range `json:"fat_g,omitempty"`
	CarbsG   *Range `json:"carbs_g,omitempty"`
}

// CatalogComponent is one part of the dish of that day.
type CatalogComponent struct {
	Name   string   `json:"name"`
	Grams  *float64 `json:"grams,omitempty"`
	Method string   `json:"method,omitempty"` // raw|boiled|steamed|baked|grilled|fried|deep_fried|stewed|smoked|pickled|other
}

// CatalogAllergens are the allergens of a dish by source.
// Confirmed == nil: nobody confirmed; empty list: confirmed "none".
type CatalogAllergens struct {
	Given     []string  `json:"given,omitempty"`
	Confirmed *[]string `json:"confirmed,omitempty"`
	Possible  []string  `json:"possible,omitempty"`
}

// CatalogDish is one dish of the menu of a day.
type CatalogDish struct {
	ID              int64              `json:"id"` // dishes.id = dish_id
	Date            string             `json:"date,omitempty"`
	Name            string             `json:"name"`
	Category        string             `json:"category,omitempty"`
	Price           *float64           `json:"price,omitempty"`
	WeightG         *float64           `json:"weight_g,omitempty"`
	WeightText      string             `json:"weight_text,omitempty"`
	WeightSource    string             `json:"weight_source,omitempty"`
	Nutrition       *CatalogNutrition  `json:"nutrition,omitempty"`
	NutritionSource string             `json:"nutrition_source,omitempty"` // llm | kitchen | missing
	Components      []CatalogComponent `json:"components,omitempty"`
	Allergens       CatalogAllergens   `json:"allergens"`
	Tags            []string           `json:"tags,omitempty"`
}

// SimilarRequest is the body of POST /v1/similar on catalog.
type SimilarRequest struct {
	LikedIDs     []int64 `json:"liked_ids"`
	CandidateIDs []int64 `json:"candidate_ids"`
}

// SimilarScore is one line of the answer of POST /v1/similar (0..1).
type SimilarScore struct {
	ID    int64   `json:"id"`
	Score float64 `json:"score"`
}
