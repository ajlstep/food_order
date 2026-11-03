// Package notify describes how a message reaches a person when the person
// did not ask for it: which events become messages, and the shape of a
// message in the queue of a channel (stream NOTIFY).
//
//	food.* event -> gateway (internal/notify) -> notify.<channel> -> bot
//
// A message carries data, not finished text: the words and the buttons
// belong to the channel. The full story is in the vault: etape/E6-Notificari.md.
package notify

import (
	"encoding/json"

	"food-order/shared/contracts/food"
)

// Events is the list of events that end up in front of a person: the gateway
// listens to them.
var Events = []string{
	food.SubjectRecommendationReady,
	food.SubjectRatingRequested,
	food.SubjectOrgInvite,
	food.SubjectOrgNotice,
	food.SubjectAlert,
}

// Channels. A channel is also the last word of its queue subject.
const ChannelTelegram = "telegram"

// Kinds of messages: they tell the channel how to show Data.
const (
	KindRecommendation = "recommendation" // Data: the data of the event
	KindRating         = "rating"
	KindOrgInvite      = "org_invite"
	KindOrgNotice      = "org_notice"
	KindAlert          = "alert" // for the admin chat, so it has no ChatRef
)

// Class says on how many channels a message goes.
type Class int

const (
	ClassNormal    Class = iota // the channel used last
	ClassImportant              // every active channel
)

// Subject is the queue of a channel.
func Subject(channel string) string { return "notify." + channel }

// Message is one item in the queue of a channel.
type Message struct {
	ID      string          `json:"id"` // unique for this event and channel
	Kind    string          `json:"kind"`
	UserID  string          `json:"user_id,omitempty"`
	ChatRef string          `json:"chat_ref,omitempty"` // where to send it (Telegram: chat id)
	Data    json.RawMessage `json:"data"`               // the data of the event, unchanged
}
