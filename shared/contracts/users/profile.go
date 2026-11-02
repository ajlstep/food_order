package users

// The profile zone: what an active person has set about themselves. The
// calls are made for the person in Request.UserID of the envelope (the
// gateway puts it there), never for a user_id taken from the data: a caller
// cannot ask for somebody else's profile by writing another id.

// Subjects of the profile zone.
const (
	SubjectProfileGet           = "rpc.users.profile.get"
	SubjectProfileUpdate        = "rpc.users.profile.update"
	SubjectProfileSettingsGet   = "rpc.users.profile.settings.get"
	SubjectProfileSettingsSet   = "rpc.users.profile.settings.set"
	SubjectProfileAllergensGet  = "rpc.users.profile.allergens.get"
	SubjectProfileAllergensSet  = "rpc.users.profile.allergens.set"
	SubjectProfilePrefsGet      = "rpc.users.profile.prefs.get"
	SubjectProfilePrefsSet      = "rpc.users.profile.prefs.set"
	SubjectProfileFoodGet       = "rpc.users.profile.food.get" // allergens and prefs together, for core
	SubjectProfileLocationsList = "rpc.users.profile.locations.list"
	SubjectProfileLocationsSave = "rpc.users.profile.locations.save"
	SubjectProfileLocationsDel  = "rpc.users.profile.locations.delete"
	SubjectProfileLocationsMain = "rpc.users.profile.locations.default"
	SubjectProfileChannelsGet   = "rpc.users.profile.channels.get"
)

// Error codes of the profile zone.
const (
	ErrEmailNotConfirmed = "email_not_confirmed" // the account is still pending
	ErrTooMany           = "too_many"            // a list is longer than allowed (limit)
)

// Keys of the settings. The values are text in a fixed form (see the vault, Users.md).
const (
	SettingOrderTime      = "order_time"      // "16:00"
	SettingOrderDays      = "order_days"      // "1,2,3,4,5": ISO weekdays, 1 = Monday
	SettingTimezone       = "timezone"        // "Europe/Chisinau"
	SettingRecFor         = "rec_for"         // "today" | "tomorrow": the day a recommendation is for
	SettingNotifyPersonal = "notify_personal" // "true" | "false"
	SettingNotifyGroup    = "notify_group"    // "true" | "false"
)

// Kinds of preferences. Allergens are separate (see AllergensRequest).
const (
	PrefLike    = "like"
	PrefDislike = "dislike"
	PrefDiet    = "diet"
	PrefTarget  = "target" // value "kcal=700" (kcal, protein_g, fat_g, carbs_g)
)

// ProfileResponse is the account as the person sees it.
type ProfileResponse struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone,omitempty"`
	Language    string `json:"language"`
	Status      string `json:"status"`
}

// ProfileUpdateRequest changes the fields that are present.
type ProfileUpdateRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Language    *string `json:"language,omitempty"` // ro | ru | en
}

// SettingsResponse has every known setting: the saved value, or the default.
type SettingsResponse struct {
	Settings map[string]string `json:"settings"`
}

// SettingsSetRequest changes the settings that are present; all or none.
type SettingsSetRequest struct {
	Values map[string]string `json:"values"`
}

// AllergensResponse and AllergensSetRequest: the whole list, replaced at each set.
// Labels are lower case words as the catalog writes them ("gluten", "ouă", "lapte").
type AllergensResponse struct {
	Allergens []string `json:"allergens"`
}

// AllergensSetRequest replaces the list.
type AllergensSetRequest struct {
	Allergens []string `json:"allergens"`
}

// Pref is one food preference. Hard means "never", not just "rather not".
type Pref struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Hard  bool   `json:"hard"`
}

// PrefsResponse is the list in the order the person made it.
type PrefsResponse struct {
	Items []Pref `json:"items"`
}

// PrefsSetRequest replaces the list.
type PrefsSetRequest struct {
	Items []Pref `json:"items"`
}

// FoodResponse is what core needs to filter and score dishes.
type FoodResponse struct {
	Allergens []string `json:"allergens"`
	Prefs     []Pref   `json:"prefs"`
}

// Location is a saved address. Lat and Lon are both present or both absent.
type Location struct {
	ID        int64    `json:"id,omitempty"` // 0 when saving a new one
	Label     string   `json:"label"`        // "acasă", "serviciu"
	Address   string   `json:"address"`
	Lat       *float64 `json:"lat,omitempty"`
	Lon       *float64 `json:"lon,omitempty"`
	Notes     string   `json:"notes,omitempty"` // floor, entrance, intercom
	IsDefault bool     `json:"is_default"`
}

// LocationsResponse lists the saved addresses.
type LocationsResponse struct {
	Items []Location `json:"items"`
}

// LocationIDRequest names one location (delete, set as default).
type LocationIDRequest struct {
	ID int64 `json:"id"`
}

// Channel is one channel account of the person.
type Channel struct {
	Channel    string `json:"channel"`
	ChatRef    string `json:"chat_ref"`
	Status     string `json:"status"`
	LastSeenMs int64  `json:"last_seen_ms"`
}

// ChannelsResponse lists the channels and says which one to use for a
// notification: the active one used most recently.
type ChannelsResponse struct {
	Channels []Channel `json:"channels"`
	Latest   string    `json:"latest"` // channel name, "" if none is active
}
