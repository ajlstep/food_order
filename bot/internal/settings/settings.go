// Package settings is the bot's side of the user settings (order time, order
// days, time zone, ...). The values live in the users service, in a fixed
// form; this file turns what people type ("luni-vineri", "16.30") into that
// form, shows it back, and reads it (next order day, ...). users checks the
// values again: this package only makes them friendly.
//
// To add a setting: one row in the table of users, and here one Def in Defs
// (key, default, parse, format) and, if code needs it, an accessor on Settings.
package settings

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"food-order/shared/contracts/users"
)

// Keys of the known settings.
const (
	KeyOrderTime      = users.SettingOrderTime
	KeyOrderDays      = users.SettingOrderDays
	KeyTimezone       = users.SettingTimezone
	KeyRecFor         = users.SettingRecFor
	KeyNotifyPersonal = users.SettingNotifyPersonal
	KeyNotifyGroup    = users.SettingNotifyGroup
	KeyOrderTo        = users.SettingOrderTo
)

// Def describes one setting.
type Def struct {
	Key     string
	Aliases []string // words accepted in "/setari <word> <value>"
	Label   string   // shown to the user
	Help    string   // example of a value
	Default string
	// Parse checks a value typed by the user and returns it in canonical form.
	Parse func(string) (string, error)
	// Format shows a canonical value to the user.
	Format func(string) string
}

// Defs is the list of settings, in the order they are shown.
var Defs = []Def{
	{
		Key: KeyOrderTime, Aliases: []string{"ora", "timp", "time"},
		Label: "Ora de comandă", Help: "/setari ora 16:30",
		Default: "16:00", Parse: parseTime, Format: func(v string) string { return v },
	},
	{
		Key: KeyOrderDays, Aliases: []string{"zile", "zilele", "days"},
		Label: "Zilele de comandă", Help: "/setari zile luni-vineri  sau  /setari zile lu,mi,vi",
		Default: "1,2,3,4,5", Parse: parseDays, Format: FormatDays,
	},
	{
		Key: KeyTimezone, Aliases: []string{"fus", "tz", "timezone"},
		Label: "Fusul orar", Help: "/setari fus Europe/Chisinau",
		Default: "Europe/Chisinau", Parse: parseTimezone, Format: func(v string) string { return v },
	},
	{
		Key: KeyRecFor, Aliases: []string{"recomandare", "pentru"},
		Label: "Recomandarea e pentru", Help: "/setari recomandare azi  sau  /setari recomandare maine",
		Default: recTomorrow, Parse: parseRecFor, Format: formatRecFor,
	},
	{
		Key: KeyNotifyPersonal, Aliases: []string{"personale"},
		Label: "Notificări personale", Help: "/setari personale da",
		Default: "true", Parse: parseOnOff, Format: formatOnOff,
	},
	{
		Key: KeyNotifyGroup, Aliases: []string{"grup"},
		Label: "Notificări de grup", Help: "/setari grup nu",
		Default: "true", Parse: parseOnOff, Format: formatOnOff,
	},
	{
		Key: KeyOrderTo, Aliases: []string{"comanda"},
		Label: "Comenzile merg la", Help: "/setari comanda personal  sau  /setari comanda grup",
		Default: users.OrderToGroup, Parse: parseOrderTo, Format: formatOrderTo,
	},
}

// Values of rec_for.
const (
	recToday    = "today"
	recTomorrow = "tomorrow"
)

// ErrUnknown is returned by Find for a key or alias that does not exist.
var ErrUnknown = errors.New("unknown setting")

// Find returns the setting for a key or an alias (case-insensitive).
func Find(word string) (Def, error) {
	w := strings.ToLower(strings.TrimSpace(word))
	for _, d := range Defs {
		if d.Key == w || slices.Contains(d.Aliases, w) {
			return d, nil
		}
	}
	return Def{}, fmt.Errorf("%w: %q", ErrUnknown, word)
}

// Settings are the effective values of one user: what is saved, plus the
// defaults for what is not. Unknown keys found in the database are kept.
type Settings map[string]string

// WithDefaults fills the missing keys.
func WithDefaults(saved map[string]string) Settings {
	s := Settings{}
	for _, d := range Defs {
		s[d.Key] = d.Default
	}
	for k, v := range saved {
		s[k] = v
	}
	return s
}

// OrderTime returns the hour and minute of the order time.
func (s Settings) OrderTime() (hour, minute int) {
	v, err := parseTime(s[KeyOrderTime])
	if err != nil {
		v = defaultOf(KeyOrderTime)
	}
	h, _ := strconv.Atoi(v[:2])
	m, _ := strconv.Atoi(v[3:])
	return h, m
}

// OrderDays says, for every time.Weekday (Sunday = 0), if it is an order day.
func (s Settings) OrderDays() [7]bool {
	var days [7]bool
	for _, p := range strings.Split(s[KeyOrderDays], ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 1 && n <= 7 {
			days[n%7] = true // ISO 7 (Sunday) -> time.Sunday (0)
		}
	}
	return days
}

// Location returns the time zone (Europe/Chisinau if the saved one is bad).
func (s Settings) Location() *time.Location {
	if loc, err := time.LoadLocation(s[KeyTimezone]); err == nil {
		return loc
	}
	loc, _ := time.LoadLocation(defaultOf(KeyTimezone))
	return loc
}

// Personal says whether the orders of the person stay out of their group.
func (s Settings) Personal() bool { return s[KeyOrderTo] == users.OrderToPersonal }

// RecommendationDay is the day a recommendation asked now is for: today, or
// the next order day, as the person chose.
func (s Settings) RecommendationDay(now time.Time) time.Time {
	if s[KeyRecFor] == recToday {
		t := now.In(s.Location())
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	}
	return s.NextOrderDay(now)
}

// NextOrderDay is the first order day after today (in the user's time zone).
// With no order days set, it is tomorrow.
func (s Settings) NextOrderDay(now time.Time) time.Time {
	loc := s.Location()
	t := now.In(loc)
	today := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	days := s.OrderDays()
	for i := 1; i <= 7; i++ {
		d := today.AddDate(0, 0, i)
		if days[d.Weekday()] {
			return d
		}
	}
	return today.AddDate(0, 0, 1)
}

func defaultOf(key string) string {
	for _, d := range Defs {
		if d.Key == key {
			return d.Default
		}
	}
	return ""
}

// --- parsing ---

// parseTime accepts 16:30, 16.30, 16-30, 1630, 16 -> "16:30" / "16:00".
func parseTime(v string) (string, error) {
	v = strings.TrimSpace(v)
	bad := fmt.Errorf("ora %q nu e de forma 16:30", v)
	var hs, ms string
	if i := strings.IndexAny(v, ":.-h"); i >= 0 {
		hs, ms = v[:i], v[i+1:]
	} else if len(v) == 4 {
		hs, ms = v[:2], v[2:]
	} else {
		hs, ms = v, "0"
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return "", bad
	}
	return fmt.Sprintf("%02d:%02d", h, m), nil
}

// ShiftTime adds minutes to a canonical time, wrapping around midnight.
func ShiftTime(v string, minutes int) string {
	t, err := parseTime(v)
	if err != nil {
		t = defaultOf(KeyOrderTime)
	}
	h, _ := strconv.Atoi(t[:2])
	m, _ := strconv.Atoi(t[3:])
	total := ((h*60+m+minutes)%(24*60) + 24*60) % (24 * 60)
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

var dayWords = map[string]int{
	"1": 1, "lu": 1, "luni": 1, "mon": 1, "пн": 1,
	"2": 2, "ma": 2, "marti": 2, "tue": 2, "вт": 2,
	"3": 3, "mi": 3, "miercuri": 3, "wed": 3, "ср": 3,
	"4": 4, "jo": 4, "joi": 4, "thu": 4, "чт": 4,
	"5": 5, "vi": 5, "vineri": 5, "fri": 5, "пт": 5,
	"6": 6, "sa": 6, "sambata": 6, "sat": 6, "сб": 6,
	"7": 7, "du": 7, "duminica": 7, "sun": 7, "вс": 7,
}

var noDiacritics = strings.NewReplacer("ă", "a", "â", "a", "î", "i", "ș", "s", "ş", "s", "ț", "t", "ţ", "t")

// parseDays accepts "luni-vineri", "1-5", "lu,mi,vi", "lu mi vi" -> "1,3,5".
func parseDays(v string) (string, error) {
	text := noDiacritics.Replace(strings.ToLower(strings.TrimSpace(v)))
	text = strings.ReplaceAll(text, "–", "-")
	if text == "" {
		return "", errors.New("scrie zilele, de exemplu luni-vineri")
	}
	var set [8]bool
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		from, to, isRange := strings.Cut(part, "-")
		a, ok := dayWords[from]
		if !ok {
			return "", fmt.Errorf("nu știu ziua %q", from)
		}
		b := a
		if isRange {
			if b, ok = dayWords[to]; !ok {
				return "", fmt.Errorf("nu știu ziua %q", to)
			}
		}
		for d := a; ; d = d%7 + 1 { // also wraps: vineri-luni = 5,6,7,1
			set[d] = true
			if d == b {
				break
			}
		}
	}
	var out []string
	for d := 1; d <= 7; d++ {
		if set[d] {
			out = append(out, strconv.Itoa(d))
		}
	}
	return strings.Join(out, ","), nil
}

func parseTimezone(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "Local" {
		return "", errors.New("scrie un fus orar, de exemplu Europe/Chisinau")
	}
	if _, err := time.LoadLocation(v); err != nil {
		return "", fmt.Errorf("nu cunosc fusul orar %q", v)
	}
	return v, nil
}

func parseRecFor(v string) (string, error) {
	switch noDiacritics.Replace(strings.ToLower(strings.TrimSpace(v))) {
	case "azi", "today":
		return recToday, nil
	case "maine", "tomorrow":
		return recTomorrow, nil
	}
	return "", errors.New("scrie azi sau mâine")
}

func formatRecFor(v string) string {
	if v == recToday {
		return "azi"
	}
	return "mâine"
}

func parseOrderTo(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "grup", users.OrderToGroup:
		return users.OrderToGroup, nil
	case users.OrderToPersonal:
		return users.OrderToPersonal, nil
	}
	return "", errors.New("scrie grup sau personal")
}

func formatOrderTo(v string) string {
	if v == users.OrderToPersonal {
		return "adresa mea (personal)"
	}
	return "adresa grupului"
}

func parseOnOff(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "da", "yes", "on", "true":
		return "true", nil
	case "nu", "no", "off", "false":
		return "false", nil
	}
	return "", errors.New("scrie da sau nu")
}

func formatOnOff(v string) string {
	if v == "false" {
		return "nu"
	}
	return "da"
}

// Toggle gives the other value of a setting with two values.
func Toggle(key, v string) (string, error) {
	switch key {
	case KeyRecFor:
		if v == recToday {
			return recTomorrow, nil
		}
		return recToday, nil
	case KeyNotifyPersonal, KeyNotifyGroup:
		if v == "false" {
			return "true", nil
		}
		return "false", nil
	case KeyOrderTo:
		if v == users.OrderToPersonal {
			return users.OrderToGroup, nil
		}
		return users.OrderToPersonal, nil
	}
	return "", fmt.Errorf("%w: %q has more than two values", ErrUnknown, key)
}

// DayShort are the short names, index = ISO weekday (1 = Monday).
var DayShort = [8]string{"", "Lu", "Ma", "Mi", "Jo", "Vi", "Sâ", "Du"}

var dayLong = [8]string{"", "luni", "marți", "miercuri", "joi", "vineri", "sâmbătă", "duminică"}

// FormatDays shows "1,2,3,4,5" as "luni–vineri" and "1,3,5" as "Lu, Mi, Vi".
func FormatDays(v string) string {
	var days []int
	for _, p := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 1 && n <= 7 {
			days = append(days, n)
		}
	}
	switch {
	case len(days) == 0:
		return "nicio zi"
	case len(days) == 7:
		return "în fiecare zi"
	case len(days) >= 3 && days[len(days)-1]-days[0] == len(days)-1:
		return dayLong[days[0]] + "–" + dayLong[days[len(days)-1]]
	}
	names := make([]string, len(days))
	for i, d := range days {
		names[i] = DayShort[d]
	}
	return strings.Join(names, ", ")
}

// ToggleDay adds or removes one ISO weekday from a canonical list.
func ToggleDay(v string, day int) string {
	var set [8]bool
	for _, p := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 1 && n <= 7 {
			set[n] = true
		}
	}
	if day >= 1 && day <= 7 {
		set[day] = !set[day]
	}
	var out []string
	for d := 1; d <= 7; d++ {
		if set[d] {
			out = append(out, strconv.Itoa(d))
		}
	}
	return strings.Join(out, ",")
}
