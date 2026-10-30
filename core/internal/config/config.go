// Package config holds every setting of core in one struct and is the only
// place that reads the environment. Order: real environment variables, then
// the .env file, then the defaults below. Every variable is described in
// .env.example. All problems are reported together.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Europe/Chisinau also works in a minimal Docker image
)

// Variable names.
const (
	EnvAddr        = "CORE_ADDR"
	EnvDBPath      = "CORE_DB_PATH"
	EnvTZ          = "CORE_TZ"
	EnvCatalogURL  = "CORE_CATALOG_URL"
	EnvHTTPTimeout = "CORE_HTTP_TIMEOUT"

	EnvNATSURL      = "CORE_NATS_URL"
	EnvNATSToken    = "CORE_NATS_TOKEN"
	EnvNATSUser     = "CORE_NATS_USER"
	EnvNATSPassword = "CORE_NATS_PASSWORD"
	EnvNATSStream   = "CORE_NATS_STREAM"
	EnvNATSDurable  = "CORE_NATS_DURABLE"
	EnvNATSMaxAge   = "CORE_NATS_MAX_AGE"

	EnvMenuRequestTime   = "CORE_MENU_REQUEST_TIME"
	EnvMenuDays          = "CORE_MENU_DAYS"
	EnvMenuRetryInterval = "CORE_MENU_RETRY_INTERVAL"
	EnvMenuDeadline      = "CORE_MENU_DEADLINE"
	EnvMenuTimeout       = "CORE_MENU_TIMEOUT"
	EnvMenuBackoff       = "CORE_MENU_BACKOFF"
	EnvMenuBackoffMax    = "CORE_MENU_BACKOFF_MAX"
	EnvMenuMaxAttempts   = "CORE_MENU_MAX_ATTEMPTS"

	EnvRatingTime     = "CORE_RATING_TIME"
	EnvRatingLookback = "CORE_RATING_LOOKBACK_DAYS"

	EnvLLMEnabled = "CORE_LLM_ENABLED"
	EnvLLMTimeout = "CORE_LLM_TIMEOUT"

	EnvRecCandidates   = "CORE_REC_CANDIDATES"
	EnvRecPicks        = "CORE_REC_PICKS"
	EnvRecHistoryDays  = "CORE_REC_HISTORY_DAYS"
	EnvRecRegenerate   = "CORE_REC_REGENERATE_ON_REVISION"
	EnvScoreWeights    = "CORE_SCORE_WEIGHTS"
	EnvScoreBayesC     = "CORE_SCORE_BAYES_C"
	EnvScoreRepeatDays = "CORE_SCORE_REPEAT_DAYS"
	EnvScoreTargetMode = "CORE_SCORE_TARGET_MODE"

	EnvTick = "CORE_TICK"
)

// Weights of the score parts (the same for everybody; the data is per user).
type Weights struct {
	Rating     float64 // Bayesian mean of the user's ratings of the dish
	Frequency  float64 // how often the user chose it (weak signal)
	Similarity float64 // similar to what the user liked (catalog /v1/similar)
	Like       float64 // matches the user's soft "like" / "diet" prefs
	Target     float64 // fits the user's calorie / macro target
	Novelty    float64 // never chosen before
	Penalty    float64 // soft dislikes and complaint labels on past orders
	Repeat     float64 // chosen in the last RepeatDays days
}

// DefaultWeights are the starting values; calibrate in CORE_SCORE_WEIGHTS.
var DefaultWeights = Weights{
	Rating: 1.0, Frequency: 0.3, Similarity: 0.6, Like: 0.5,
	Target: 0.4, Novelty: 0.15, Penalty: 0.8, Repeat: 0.5,
}

// Score is the configuration of the deterministic score.
type Score struct {
	Weights    Weights
	BayesC     float64 // prior strength of the Bayesian mean (in "ratings")
	RepeatDays int     // a dish chosen this many days ago or less is penalised
	TargetMode string  // mid | worst: which end of an interval is compared to the target
}

// Config is the complete configuration of core.
type Config struct {
	Addr        string
	DBPath      string
	Location    *time.Location
	CatalogURL  string
	HTTPTimeout time.Duration

	NATSURL      string // empty = run without the broker (HTTP only)
	NATSToken    string
	NATSUser     string
	NATSPassword string
	NATSStream   string
	NATSDurable  string
	NATSMaxAge   time.Duration

	// Menu scheduler.
	MenuRequestTime   Clock         // daily, ask the menu of the next serving day
	MenuDays          map[int]bool  // ISO weekdays the kitchen serves (1 = Monday)
	MenuRetryInterval time.Duration // pause after no_menu
	MenuDeadline      Clock         // on the day before the menu day: give up and alert
	MenuTimeout       time.Duration // no answer in this time => source_unavailable
	MenuBackoff       time.Duration // first pause after source_unavailable (doubles)
	MenuBackoffMax    time.Duration // longest pause after source_unavailable
	MenuMaxAttempts   int           // after this many sends without a menu: alert

	// Rating question, the day after the meal.
	RatingTime         Clock
	RatingLookbackDays int // ask only for meals of the last N days

	// Recommendation.
	LLMEnabled           bool          // false: picks come from the score only
	LLMTimeout           time.Duration // no answer from llm => picks from the score
	RecCandidates        int           // how many go to llm (top by score)
	RecPicks             int           // how many go to the user
	RecHistoryDays       int           // history used for the score
	RegenerateOnRevision bool          // new menu for a day => new recommendation (if not chosen yet)
	Score                Score

	Tick time.Duration // how often the scheduler looks around
}

// Clock is a time of day (HH:MM).
type Clock struct{ Hour, Min int }

// On returns the moment of this clock on the day of t, in loc.
func (c Clock) On(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), c.Hour, c.Min, 0, 0, loc)
}

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Min) }

// ParseClock reads "15:00".
func ParseClock(s string) (Clock, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return Clock{}, fmt.Errorf("%q is not a time like 15:00", s)
	}
	return Clock{t.Hour(), t.Minute()}, nil
}

// Load reads the .env file (if it exists) and the environment.
func Load(envFile string) (Config, error) {
	file, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	return FromLookup(func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok {
			return strings.TrimSpace(v), true
		}
		v, ok := file[key]
		return strings.TrimSpace(v), ok
	})
}

// Defaults returns the configuration with every default (used by tests).
func Defaults() Config {
	cfg, err := FromLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		panic(err)
	}
	return cfg
}

// FromLookup builds the Config from any key->value source; get says whether
// the key is set at all. An empty value counts as "not set", except for
// CORE_NATS_URL, where it means "run without a broker".
func FromLookup(get func(string) (string, bool)) (Config, error) {
	l := loader{get: get}
	cfg := Config{
		Addr:        l.str(EnvAddr, ":8081"),
		DBPath:      l.str(EnvDBPath, "data/core.db"),
		Location:    l.location(EnvTZ, "Europe/Chisinau"),
		CatalogURL:  strings.TrimRight(l.str(EnvCatalogURL, "http://catalog:8082"), "/"),
		HTTPTimeout: l.duration(EnvHTTPTimeout, 10*time.Second),

		NATSURL:      l.optional(EnvNATSURL, "nats://nats:4222"),
		NATSToken:    l.str(EnvNATSToken, ""),
		NATSUser:     l.str(EnvNATSUser, ""),
		NATSPassword: l.str(EnvNATSPassword, ""),
		NATSStream:   l.str(EnvNATSStream, "FOOD"),
		NATSDurable:  l.str(EnvNATSDurable, "core"),
		NATSMaxAge:   l.duration(EnvNATSMaxAge, 7*24*time.Hour),

		MenuRequestTime:   l.clock(EnvMenuRequestTime, "15:00"),
		MenuDays:          l.weekdays(EnvMenuDays, "1,2,3,4,5"),
		MenuRetryInterval: l.duration(EnvMenuRetryInterval, 30*time.Minute),
		MenuDeadline:      l.clock(EnvMenuDeadline, "20:00"),
		MenuTimeout:       l.duration(EnvMenuTimeout, 10*time.Minute),
		MenuBackoff:       l.duration(EnvMenuBackoff, time.Minute),
		MenuBackoffMax:    l.duration(EnvMenuBackoffMax, 30*time.Minute),
		MenuMaxAttempts:   l.integer(EnvMenuMaxAttempts, 5, 1, 1000),

		RatingTime:         l.clock(EnvRatingTime, "10:00"),
		RatingLookbackDays: l.integer(EnvRatingLookback, 3, 1, 60),

		LLMEnabled:           l.boolean(EnvLLMEnabled, true),
		LLMTimeout:           l.duration(EnvLLMTimeout, 3*time.Minute),
		RecCandidates:        l.integer(EnvRecCandidates, 8, 1, 50),
		RecPicks:             l.integer(EnvRecPicks, 5, 1, 20),
		RecHistoryDays:       l.integer(EnvRecHistoryDays, 90, 1, 3650),
		RegenerateOnRevision: l.boolean(EnvRecRegenerate, false),
		Score: Score{
			Weights:    l.weights(EnvScoreWeights),
			BayesC:     l.float(EnvScoreBayesC, 2, 0, 100),
			RepeatDays: l.integer(EnvScoreRepeatDays, 5, 0, 60),
			TargetMode: l.oneOf(EnvScoreTargetMode, "mid", "mid", "worst"),
		},

		Tick: l.duration(EnvTick, 30*time.Second),
	}
	if cfg.RecPicks > cfg.RecCandidates {
		l.fail(EnvRecPicks, "must not be larger than %s (%d)", EnvRecCandidates, cfg.RecCandidates)
	}
	if cfg.MenuBackoffMax < cfg.MenuBackoff {
		l.fail(EnvMenuBackoffMax, "must not be smaller than %s", EnvMenuBackoff)
	}
	if cfg.NATSToken != "" && cfg.NATSUser != "" {
		l.fail(EnvNATSToken, "set either %s or %s/%s, not both", EnvNATSToken, EnvNATSUser, EnvNATSPassword)
	}
	if u, err := url.Parse(cfg.CatalogURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail(EnvCatalogURL, "%q is not an http(s) URL", cfg.CatalogURL)
	}
	if err := errors.Join(l.errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

type loader struct {
	get  func(string) (string, bool)
	errs []error
}

func (l *loader) fail(key, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
}

func (l *loader) str(key, def string) string {
	if v, ok := l.get(key); ok && v != "" {
		return v
	}
	return def
}

// optional: a key set to "" is kept empty (it turns something off).
func (l *loader) optional(key, def string) string {
	if v, ok := l.get(key); ok {
		return v
	}
	return def
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v, ok := l.get(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.fail(key, "%q is not a positive duration (examples: 30s, 10m, 3h)", v)
		return def
	}
	return d
}

func (l *loader) integer(key string, def, min, max int) int {
	v, ok := l.get(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		l.fail(key, "%q: expected a whole number %d..%d", v, min, max)
		return def
	}
	return n
}

func (l *loader) float(key string, def, min, max float64) float64 {
	v, ok := l.get(key)
	if !ok || v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < min || f > max {
		l.fail(key, "%q: expected a number %g..%g", v, min, max)
		return def
	}
	return f
}

func (l *loader) boolean(key string, def bool) bool {
	v, ok := l.get(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "da", "on":
		return true
	case "0", "false", "no", "nu", "off":
		return false
	}
	l.fail(key, "%q: expected true or false", v)
	return def
}

func (l *loader) oneOf(key, def string, allowed ...string) string {
	v := strings.ToLower(l.str(key, def))
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.fail(key, "%q: expected one of %s", v, strings.Join(allowed, ", "))
	return def
}

func (l *loader) location(key, def string) *time.Location {
	name := l.str(key, def)
	loc, err := time.LoadLocation(name)
	if err != nil {
		l.fail(key, "unknown time zone %q (example: Europe/Chisinau)", name)
		return time.UTC
	}
	return loc
}

func (l *loader) clock(key, def string) Clock {
	c, err := ParseClock(l.str(key, def))
	if err != nil {
		l.fail(key, "%v", err)
		c, _ = ParseClock(def)
	}
	return c
}

func (l *loader) weekdays(key, def string) map[int]bool {
	v := l.str(key, def)
	days := map[int]bool{}
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 7 {
			l.fail(key, "%q: expected ISO weekdays 1..7 (1 = Monday), e.g. 1,2,3,4,5", v)
			return map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}
		}
		days[n] = true
	}
	if len(days) == 0 {
		l.fail(key, "at least one day is needed")
	}
	return days
}

// weights reads "rating=1,frequency=0.3,..."; missing names keep the default.
func (l *loader) weights(key string) Weights {
	w := DefaultWeights
	v, ok := l.get(key)
	if !ok || v == "" {
		return w
	}
	fields := map[string]*float64{
		"rating": &w.Rating, "frequency": &w.Frequency, "similarity": &w.Similarity,
		"like": &w.Like, "target": &w.Target, "novelty": &w.Novelty,
		"penalty": &w.Penalty, "repeat": &w.Repeat,
	}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, num, found := strings.Cut(part, "=")
		ptr, known := fields[strings.ToLower(strings.TrimSpace(name))]
		f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
		if !found || !known || err != nil || f < 0 {
			names := make([]string, 0, len(fields))
			for n := range fields {
				names = append(names, n)
			}
			sort.Strings(names)
			l.fail(key, "%q: expected name=number (>= 0); names: %s", part, strings.Join(names, ", "))
			continue
		}
		*ptr = f
	}
	return w
}

// readEnvFile reads simple KEY=value lines. A missing file gives an empty map.
func readEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return parseEnv(string(b))
}

// parseEnv supports comments (#), "export KEY=value" and quoted values.
func parseEnv(text string) (map[string]string, error) {
	vars := map[string]string{}
	text = strings.TrimPrefix(text, "\ufeff") // BOM from Windows editors
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("config: .env line %d: expected KEY=value", i+1)
		}
		val = strings.TrimSpace(val)
		if n := len(val); n >= 2 && (val[0] == '"' && val[n-1] == '"' || val[0] == '\'' && val[n-1] == '\'') {
			val = val[1 : n-1]
		} else if j := strings.Index(val, " #"); j >= 0 {
			val = strings.TrimSpace(val[:j])
		}
		vars[key] = val
	}
	return vars, nil
}
