// Package settings describes the user settings kept by the bot (order time,
// order days, time zone, ...). Every setting is a key with a text value in
// the user_settings table; this file says what each key means, its default
// and how to read it.
//
// To add a setting: append one Def to Defs (key, default, parse, format) and,
// if code needs it, add an accessor on Settings. No migration needed.
package settings

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Keys of the known settings.
const (
	KeyOrderTime = "order_time" // "16:00": when the bot asks for the recommendation
	KeyOrderDays = "order_days" // "1,2,3,4,5": ISO weekdays (1 = Monday .. 7 = Sunday)
	KeyTimezone  = "timezone"   // "Europe/Chisinau"
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
}

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

// Due says whether the automatic recommendation must be asked now:
// today is an order day and now is between the order time and order time +
// window. forDate is the day the recommendation is for (the next order day).
//
// Example with Monday–Friday at 16:00: on Friday at 16:00 the bot asks for
// Monday, on Monday at 16:00 for Tuesday, nothing on Saturday and Sunday.
func (s Settings) Due(now time.Time, window time.Duration) (forDate time.Time, ok bool) {
	loc := s.Location()
	t := now.In(loc)
	if !s.OrderDays()[t.Weekday()] {
		return time.Time{}, false
	}
	h, m := s.OrderTime()
	at := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, loc)
	if t.Before(at) || !t.Before(at.Add(window)) {
		return time.Time{}, false
	}
	return s.NextOrderDay(now), true
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
