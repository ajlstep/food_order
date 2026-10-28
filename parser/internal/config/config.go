// Package config holds every setting of the parser service in one struct and
// is the only place that reads the environment.
//
// Values come from, in this order: real environment variables, then the .env
// file, then the defaults below. An empty value counts as "not set".
// Every variable is described in .env.example.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // time zones work even on machines without a tz database
)

// Config is the complete configuration of the service.
type Config struct {
	// Source is the site module to use ("bistro"). Each site has its own
	// package in internal/<site>.
	Source string
	// SiteURL is the page to download; {date} is replaced with YYYY-MM-DD.
	// Empty means the default URL of the chosen source.
	SiteURL string
	// UserAgent is sent with every request to the site.
	UserAgent string
	// HTTPTimeout is the timeout of one request to the site.
	HTTPTimeout time.Duration

	// Addr is the address the HTTP API listens on.
	Addr string
	// Location decides what "tomorrow" means for requests without a date.
	Location *time.Location

	// CacheTTL is how long a menu that was found is kept.
	CacheTTL time.Duration
	// MinInterval is the shortest pause between two requests to the site for the
	// same day, also after a failure or an empty menu.
	MinInterval time.Duration

	// RawDir is where the page of the last successful run is saved
	// ("" turns it off).
	RawDir string
}

// Variable names.
const (
	EnvSource      = "PARSER_SOURCE"
	EnvSiteURL     = "PARSER_SITE_URL"
	EnvUserAgent   = "PARSER_USER_AGENT"
	EnvHTTPTimeout = "PARSER_HTTP_TIMEOUT"
	EnvAddr        = "PARSER_ADDR"
	EnvTZ          = "PARSER_TZ"
	EnvCacheTTL    = "PARSER_CACHE_TTL"
	EnvMinInterval = "PARSER_MIN_INTERVAL"
	EnvRawDir      = "PARSER_RAW_DIR"
)

// Defaults.
const (
	DefaultSource      = "bistro"
	DefaultUserAgent   = "food-order-parser/1.0 (personal menu reader)"
	DefaultHTTPTimeout = 30 * time.Second
	DefaultAddr        = ":8082"
	DefaultTZ          = "Europe/Chisinau"
	DefaultCacheTTL    = 3 * time.Hour
	DefaultMinInterval = 10 * time.Minute
	DefaultRawDir      = "data/raw"
)

// Load reads the configuration. envFile is the path of a .env file; a missing
// file is fine, a broken one is an error. All problems are reported together.
func Load(envFile string) (Config, error) {
	fileVars, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	return fromLookup(func(key string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fileVars[key]
	})
}

// fromLookup builds the Config from any key->value source (used by the tests).
func fromLookup(get func(string) string) (Config, error) {
	l := loader{get: get}

	cfg := Config{
		Source:      l.str(EnvSource, DefaultSource),
		SiteURL:     l.str(EnvSiteURL, ""),
		UserAgent:   l.str(EnvUserAgent, DefaultUserAgent),
		HTTPTimeout: l.duration(EnvHTTPTimeout, DefaultHTTPTimeout),
		Addr:        l.str(EnvAddr, DefaultAddr),
		Location:    l.location(EnvTZ, DefaultTZ),
		CacheTTL:    l.duration(EnvCacheTTL, DefaultCacheTTL),
		MinInterval: l.duration(EnvMinInterval, DefaultMinInterval),
		RawDir:      l.str(EnvRawDir, DefaultRawDir),
	}
	if cfg.SiteURL != "" {
		l.checkURL(EnvSiteURL, cfg.SiteURL)
	}

	if err := errors.Join(l.errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// loader reads values and collects the errors instead of stopping at the first.
type loader struct {
	get  func(string) string
	errs []error
}

func (l *loader) fail(key, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
}

func (l *loader) str(key, def string) string {
	if v := strings.TrimSpace(l.get(key)); v != "" {
		return v
	}
	return def
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(l.get(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail(key, "%q is not a duration (examples: 30s, 10m, 3h)", v)
		return def
	}
	if d <= 0 {
		l.fail(key, "must be greater than zero, got %q", v)
	}
	return d
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

func (l *loader) checkURL(key, raw string) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail(key, "%q is not an http(s) URL", raw)
	}
}
