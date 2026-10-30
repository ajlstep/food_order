// Package contracts holds the NATS events of the menu source contract
// (Obsidian: projects/food-order/Parser and Comunicare). The parser now and
// the public api later answer the same way, so core and catalog do not
// know which one is active.
//
// core has the same names in core/internal/contracts and catalog in
// catalog/internal/contracts; keep the field names in sync.
package contracts

import (
	"encoding/json"
	"time"
)

// NATS subjects.
const (
	SubjectMenuRequested = "food.menu.requested" // core -> source (the parser listens)
	SubjectMenuFetched   = "food.menu.fetched"   // source -> catalog
	SubjectMenuError     = "food.menu.error"     // source -> core
	SubjectAlert         = "food.alert"          // any service -> bot -> admin chat
)

// ListenSubjects is the filter of the parser's durable consumer.
var ListenSubjects = []string{SubjectMenuRequested}

// Error codes of food.menu.error (what core does with each is in the notes).
const (
	ErrNoMenu            = "no_menu"            // nothing for that period yet: core asks again later
	ErrSourceUnavailable = "source_unavailable" // the site does not answer: core retries with a growing pause
	ErrParseFailed       = "parse_failed"       // the site changed: core alerts at once
	ErrBadRequest        = "bad_request"        // invalid period: a bug, core alerts
)

// SourceName is the "source" of food.menu.fetched.
const SourceName = "parser"

// Envelope is the common wrapper of every event.
type Envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Version int             `json:"version"`
	TS      time.Time       `json:"ts"`
	Data    json.RawMessage `json:"data"`
}

// MenuRequested: core -> source. To is optional (one day).
type MenuRequested struct {
	RequestID string `json:"request_id"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
}

// MenuFetched: source -> catalog.
type MenuFetched struct {
	RequestID string `json:"request_id"`
	Source    string `json:"source"`
	Days      []Day  `json:"days"`
}

// Day is the menu of one day.
type Day struct {
	Date  string `json:"date"` // 2026-11-02
	Items []Item `json:"items"`
}

// Item is one dish as the site shows it. The parser does not normalise
// names or decide which dish is "the same": catalog does.
type Item struct {
	Name       string   `json:"name"`
	Category   string   `json:"category,omitempty"`
	Price      *float64 `json:"price,omitempty"`       // nil when the site gives none
	WeightText string   `json:"weight_text,omitempty"` // as on the site, e.g. "250/50"
	ImageURL   string   `json:"image_url,omitempty"`
	Components []string `json:"components,omitempty"` // the parts of a set menu, as text
}

// MenuError: source -> core.
type MenuError struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// Alert: any service -> bot -> admin chat.
type Alert struct {
	Service string `json:"service"`
	Level   string `json:"level"` // info | warn | error
	Text    string `json:"text"`
}
