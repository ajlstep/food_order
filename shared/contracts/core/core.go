// Package core describes the messages of the core service: the requests it
// answers (the gateway brings them from the bot) and the events about one
// person or one recommendation. The person is always in the envelope of a
// request (rpc.Request.UserID), never in the data.
//
// The full description, in words, is in the vault: services/Core.md.
package core

import (
	"encoding/json"

	"food-order/shared/contracts/catalog"
)

// Subjects of the requests core answers.
const (
	SubjectRecommendationCreate = "rpc.core.recommendations.create"
	SubjectRecommendationGet    = "rpc.core.recommendations.get"
	SubjectOrderCreate          = "rpc.core.orders.create"
	SubjectOrderRate            = "rpc.core.orders.rate"
	SubjectOrdersList           = "rpc.core.orders.list"
	SubjectStats                = "rpc.core.stats"
	SubjectCompanyReport        = "rpc.core.reports.company"

	// SubjectAll is what the service listens to.
	SubjectAll = "rpc.core.>"
)

// Range is a value known as an interval (exact => Min == Max).
type Range = catalog.Range

// ---------------------------------------------------------------- events

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
	Components        []string `json:"components,omitempty"` // what is in it: "LUNCH 1" alone says nothing
	Price             *float64 `json:"price,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	KcalMin           *float64 `json:"kcal_min,omitempty"`
	KcalMax           *float64 `json:"kcal_max,omitempty"`
	AllergensPossible []string `json:"allergens_possible,omitempty"`
	PartnerName       string   `json:"partner_name,omitempty"`
	OrderUntilMs      int64    `json:"order_until_ms,omitempty"`
}

// RecommendationReady: core -> bot.
type RecommendationReady struct {
	UserID string `json:"user_id"`
	Date   string `json:"date"`
	Picks  []Pick `json:"picks"`
}

// RatingRequested: core -> bot, the day after the meal.
type RatingRequested struct {
	UserID  string `json:"user_id"`
	OrderID int64  `json:"order_id"`
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

// ---------------------------------------------------------------- requests

// CreateRecommendationRequest asks for the recommendation of a day.
type CreateRecommendationRequest struct {
	Date string `json:"date"`
}

// GetRecommendationRequest asks for the last recommendation of a day.
type GetRecommendationRequest struct {
	Date string `json:"date"`
}

// Recommendation is one recommendation as the bot shows it.
type Recommendation struct {
	Date   string `json:"date"`
	Status string `json:"status"` // waiting_menu | generating | ready
	Picks  []Pick `json:"picks"`
}

// CreateOrderRequest is the choice of a person. Personal means "for myself,
// not at the address of my group": such an order is nobody else's business
// and stays out of the company reports.
type CreateOrderRequest struct {
	Date     string   `json:"date"`
	DishID   int64    `json:"dish_id"`
	Price    *float64 `json:"price,omitempty"`
	Forced   bool     `json:"forced"`
	Personal bool     `json:"personal,omitempty"`
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

	// Where it goes, as it was when the person chose. All empty = personal.
	GroupName   string `json:"group_name,omitempty"`
	CompanyName string `json:"company_name,omitempty"`
	Address     string `json:"address,omitempty"`
}

// RateRequest is a rating with its comment.
type RateRequest struct {
	OrderID int64  `json:"order_id"`
	Rating  int    `json:"rating"`
	Comment string `json:"comment,omitempty"`
}

// PeriodRequest names a period; both ends are included. Empty = the last 30 days.
type PeriodRequest struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// OrdersResponse lists orders, oldest first.
type OrdersResponse struct {
	Items []Order `json:"items"`
}

// ReportLine is one order in the report of a company. The names of the people
// are not here: core does not know them, the bot adds them from the members.
type ReportLine struct {
	UserID    string   `json:"user_id"`
	Date      string   `json:"date"`
	DishName  string   `json:"dish_name"`
	Price     *float64 `json:"price,omitempty"`
	GroupName string   `json:"group_name,omitempty"`
}

// CompanyReport is who ordered what, only at the addresses of the company.
type CompanyReport struct {
	From  string       `json:"from"`
	To    string       `json:"to"`
	Spent float64      `json:"spent"`
	Items []ReportLine `json:"items"`
}

// DishCount is one line of the "favourite dishes" list.
type DishCount struct {
	DishID    int64    `json:"dish_id"`
	Name      string   `json:"name"`
	Count     int      `json:"count"`
	AvgRating *float64 `json:"avg_rating,omitempty"`
}

// DayStats is one day of the statistics.
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

// MonthStats is one month of the statistics.
type MonthStats struct {
	Month  string  `json:"month"` // 2026-10
	Orders int     `json:"orders"`
	Spent  float64 `json:"spent"`
}

// Stats are the numbers of a person. Nutrition totals are intervals:
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
