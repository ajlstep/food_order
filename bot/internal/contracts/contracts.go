// Package contracts holds the JSON shapes that bot exchanges with the other
// services (HTTP bodies and NATS events). They follow the notes in
// Obsidian: projects/food-order/Comunicare.
//
// When the project gets a shared contracts module, these types move there.
// Fields marked "assumed" are not fixed in the notes yet: the bot only reads
// them if they are present, so core can add them later without breaking it.
package contracts

import (
	"encoding/json"
	"time"
)

// NATS subjects the bot listens to.
const (
	SubjectRecommendationReady = "food.recommendation.ready"
	SubjectRatingRequested     = "food.rating.requested"
	SubjectAlert               = "food.alert"
)

// Envelope is the common wrapper of every event.
type Envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Version int             `json:"version"`
	TS      time.Time       `json:"ts"`
	Data    json.RawMessage `json:"data"`
}

// Pick is one recommended dish.
type Pick struct {
	DishID int64    `json:"dish_id"`
	Name   string   `json:"name"`
	Price  *float64 `json:"price,omitempty"`
	Reason string   `json:"reason,omitempty"`

	// assumed: optional extras that core may add to the pick.
	KcalMin           *float64 `json:"kcal_min,omitempty"`
	KcalMax           *float64 `json:"kcal_max,omitempty"`
	AllergensPossible []string `json:"allergens_possible,omitempty"`
}

// RecommendationReady is the data of food.recommendation.ready.
type RecommendationReady struct {
	UserID int64  `json:"user_id"` // users.id in bot.db
	Date   string `json:"date"`    // 2026-10-29
	Picks  []Pick `json:"picks"`
}

// RatingRequested is the data of food.rating.requested.
type RatingRequested struct {
	UserID  int64 `json:"user_id"`
	OrderID int64 `json:"order_id"`
}

// Alert is the data of food.alert. It goes to the admin chat.
type Alert struct {
	Service string `json:"service"`
	Level   string `json:"level"` // info | warn | error
	Text    string `json:"text"`
	UserID  int64  `json:"user_id,omitempty"` // if the alert is about one user
}

// --- HTTP: core ---
//
// Every call from the bot to core carries the user in the header
// X-User-ID: <users.id>. Core keeps orders, prefs and recommendations per
// user id and never sees Telegram data (chat id, name, phone).

// HeaderUserID is the header with the user id on calls to core.
const HeaderUserID = "X-User-ID"

// CreateRecommendationRequest is the body of POST /v1/recommendations.
type CreateRecommendationRequest struct {
	Date string `json:"date"`
}

// Recommendation is the answer of GET /v1/recommendations?date=.
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

// Order is one saved choice (POST /v1/orders answer and GET /v1/orders items).
type Order struct {
	ID              int64    `json:"id"`
	Date            string   `json:"date"`
	DishID          int64    `json:"dish_id"`
	DishName        string   `json:"dish_name"`
	Price           *float64 `json:"price,omitempty"`
	Rating          *int     `json:"rating,omitempty"`
	Comment         string   `json:"comment,omitempty"`
	Forced          bool     `json:"forced"`
	KcalMin         *float64 `json:"kcal_min,omitempty"`
	KcalMax         *float64 `json:"kcal_max,omitempty"`
	NutritionSource string   `json:"nutrition_source,omitempty"` // llm | kitchen | missing
}

// RatingRequest is the body of POST /v1/orders/{id}/rating.
type RatingRequest struct {
	Rating  int    `json:"rating"`
	Comment string `json:"comment,omitempty"`
}

// Pref is one preference (table prefs in core).
type Pref struct {
	ID    int64  `json:"id,omitempty"`
	Kind  string `json:"kind"` // allergen | dislike | like | diet
	Value string `json:"value"`
	Hard  bool   `json:"hard"`
}

// Prefs is the body of GET and PUT /v1/prefs (assumed: the whole list).
type Prefs struct {
	Items []Pref `json:"items"`
}

// Range is a value known as an interval (exact => Min == Max).
type Range struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// DishCount is one line of the "favourite dishes" list.
type DishCount struct {
	DishID    int64    `json:"dish_id"`
	Name      string   `json:"name"`
	Count     int      `json:"count"`
	AvgRating *float64 `json:"avg_rating,omitempty"`
}

// Stats is the answer of GET /v1/stats (assumed shape).
type Stats struct {
	From             string      `json:"from"`
	To               string      `json:"to"`
	Orders           int         `json:"orders"`
	Spent            float64     `json:"spent"`
	Forced           int         `json:"forced"`
	AvgRating        *float64    `json:"avg_rating,omitempty"`
	Kcal             *Range      `json:"kcal,omitempty"`
	ProteinG         *Range      `json:"protein_g,omitempty"`
	FatG             *Range      `json:"fat_g,omitempty"`
	CarbsG           *Range      `json:"carbs_g,omitempty"`
	WithoutNutrition int         `json:"without_nutrition"`
	TopDishes        []DishCount `json:"top_dishes,omitempty"`
}

// --- HTTP: catalog ---

// ConfirmAllergensRequest is the body of POST /v1/dishes/{id}/allergens/confirm.
type ConfirmAllergensRequest struct {
	Allergens []string `json:"allergens"`
}

// --- errors ---

// ErrorBody is the common error answer: {"error": {"code", "message"}}.
type ErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
