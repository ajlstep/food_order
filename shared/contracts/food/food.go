// Package food names the food.* events and holds the ones that are not
// owned by a single service. The payload of the others is in contracts/core
// and contracts/catalog. Everybody imports this file, so nobody can disagree
// about a subject.
//
// The full description, in words, is in the vault: Comunicare.md.
package food

// Subjects of the stream FOOD.
const (
	SubjectMenuRequested = "food.menu.requested" // core -> the active menu source
	SubjectMenuFetched   = "food.menu.fetched"   // source -> catalog
	SubjectMenuError     = "food.menu.error"     // source -> core
	SubjectMenuReady     = "food.menu.ready"     // catalog -> core

	SubjectDishAnalyzeRequested = "food.dish.analyze.requested" // catalog -> llm
	SubjectDishAnalyzed         = "food.dish.analyzed"          // llm -> catalog
	SubjectDishEmbedRequested   = "food.dish.embed.requested"   // catalog -> llm
	SubjectDishEmbedded         = "food.dish.embedded"          // llm -> catalog

	SubjectRecommendationRequested = "food.recommendation.requested" // core -> llm
	SubjectRecommendationGenerated = "food.recommendation.generated" // llm -> core
	SubjectRecommendationReady     = "food.recommendation.ready"     // core -> people
	SubjectRatingRequested         = "food.rating.requested"         // core -> people
	SubjectFeedbackParseRequested  = "food.feedback.parse.requested" // core -> llm
	SubjectFeedbackParsed          = "food.feedback.parsed"          // llm -> core

	SubjectOrgInvite = "food.org.invite" // users -> people: somebody was invited
	SubjectOrgNotice = "food.org.notice" // users -> people: something changed in a company or group

	SubjectAlert = "food.alert" // any service -> the admin chat
)

// Error codes of food.menu.error (what core does with each is in Core.md).
const (
	MenuErrNoMenu            = "no_menu"            // nothing for that period yet: core asks again later
	MenuErrSourceUnavailable = "source_unavailable" // the site does not answer: core retries with a growing pause
	MenuErrParseFailed       = "parse_failed"       // the site changed: core alerts at once
	MenuErrBadRequest        = "bad_request"        // invalid period: a bug, core alerts
)

// MenuRequested: core -> source. To is optional (one day).
type MenuRequested struct {
	RequestID string `json:"request_id"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
}

// MenuError: source -> core.
type MenuError struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// MenuReady: catalog -> core, every dish of the request has what it needs
// (or llm did not answer in time and the dish goes with what there is).
type MenuReady struct {
	RequestID string  `json:"request_id"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	DishIDs   []int64 `json:"dish_ids"`
}

// Alert: any service -> the admin chat. Who it is about stays in the logs:
// alerts are read by a person, in a chat.
type Alert struct {
	Service string `json:"service"`
	Level   string `json:"level"` // info | warn | error
	Text    string `json:"text"`
}

// Kinds of an organisation: what an invitation or a notice is about.
const (
	OrgCompany = "company"
	OrgGroup   = "group"
)

// OrgInvite: users -> the person invited. There are no people's names in it:
// events live in a stream for days, and the invitation is about the company.
type OrgInvite struct {
	UserID   string `json:"user_id"`
	InviteID string `json:"invite_id"`
	Kind     string `json:"kind"` // company | group
	Name     string `json:"name"` // of the company or group
}

// What an OrgNotice says.
const (
	NoticeAccepted = "accepted" // an invitation of yours was accepted
	NoticeDeclined = "declined" // ... or declined
	NoticeRemoved  = "removed"  // you were taken out
	NoticeLeft     = "left"     // a member left
	NoticeOwner    = "owner"    // you are the owner now
)

// OrgNotice: users -> one person.
type OrgNotice struct {
	UserID string `json:"user_id"`
	Notice string `json:"notice"`
	Kind   string `json:"kind"` // company | group
	Name   string `json:"name"` // of the company or group
}
