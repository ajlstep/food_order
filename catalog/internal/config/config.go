// Package config holds every setting of catalog in one struct and is the
// only place that reads the environment. Order: real environment variables,
// then the .env file, then the defaults below. Every variable is described
// in .env.example. All problems are reported together.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // Europe/Chisinau also works in a minimal Docker image
)

// Variable names.
const (
	EnvDBPath       = "CATALOG_DB_PATH"
	EnvTZ           = "CATALOG_TZ"
	EnvPartnersFile = "CATALOG_PARTNERS_FILE"

	EnvNATSURL      = "CATALOG_NATS_URL"
	EnvNATSUser     = "CATALOG_NATS_USER"
	EnvNATSPassword = "CATALOG_NATS_PASSWORD"

	EnvAnalysisEnabled = "CATALOG_ANALYSIS_ENABLED"
	EnvEmbedEnabled    = "CATALOG_EMBED_ENABLED"
	EnvAnalysisTimeout = "CATALOG_ANALYSIS_TIMEOUT"
	EnvProcessedKeep   = "CATALOG_PROCESSED_KEEP"
	EnvTick            = "CATALOG_TICK"

	EnvAdditionalLogs = "CATALOG_ADDITIONAL_LOGS"
	EnvLogConsole     = "CATALOG_LOG_CONSOLE"
)

// Config is the complete configuration of catalog.
type Config struct {
	DBPath       string
	Location     *time.Location // what "tomorrow" and the order limits of the partners mean
	PartnersFile string         // the partners and their schedule, written by hand

	NATSURL      string
	NATSUser     string
	NATSPassword string

	AnalysisEnabled bool          // false: llm is never asked about dishes (analysis.enabled)
	EmbedEnabled    bool          // false: no embeddings, similarity answers nothing
	AnalysisTimeout time.Duration // llm did not answer: the menu goes ready with what there is
	ProcessedKeep   time.Duration // how long processed event ids are kept
	Tick            time.Duration // how often catalog looks around (timeouts, failed publishes)

	// Logs go to NATS (logs.<level>), to the logs service.
	AdditionalLogs bool // also send the detailed debug logs (every call, every dish, every event)
	LogConsole     bool // also print the logs to the console
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
// the key is set at all. An empty value counts as "not set".
func FromLookup(get func(string) (string, bool)) (Config, error) {
	l := loader{get: get}
	cfg := Config{
		DBPath:       l.str(EnvDBPath, "data/catalog.db"),
		Location:     l.location(EnvTZ, "Europe/Chisinau"),
		PartnersFile: l.str(EnvPartnersFile, "partners.json"),

		NATSURL:      l.str(EnvNATSURL, "nats://nats:4222"),
		NATSUser:     l.str(EnvNATSUser, "catalog"),
		NATSPassword: l.str(EnvNATSPassword, ""),

		AnalysisEnabled: l.boolean(EnvAnalysisEnabled, false),
		EmbedEnabled:    l.boolean(EnvEmbedEnabled, false),
		AnalysisTimeout: l.duration(EnvAnalysisTimeout, 30*time.Minute),
		ProcessedKeep:   l.duration(EnvProcessedKeep, 30*24*time.Hour),
		Tick:            l.duration(EnvTick, 30*time.Second),

		AdditionalLogs: l.boolean(EnvAdditionalLogs, false),
		LogConsole:     l.boolean(EnvLogConsole, false),
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

func (l *loader) location(key, def string) *time.Location {
	name := l.str(key, def)
	loc, err := time.LoadLocation(name)
	if err != nil {
		l.fail(key, "unknown time zone %q (example: Europe/Chisinau)", name)
		return time.UTC
	}
	return loc
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
