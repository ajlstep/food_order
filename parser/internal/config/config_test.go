package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mapLookup(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := fromLookup(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != "bistro" || cfg.SiteURL != "" || cfg.Addr != ":8083" ||
		cfg.HTTPTimeout != 30*time.Second || cfg.CacheTTL != 3*time.Hour ||
		cfg.MinInterval != 10*time.Minute || cfg.RawDir != "data/raw" ||
		cfg.UserAgent != DefaultUserAgent || cfg.Location.String() != "Europe/Chisinau" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestValues(t *testing.T) {
	cfg, err := fromLookup(mapLookup(map[string]string{
		EnvSource:      "bistro",
		EnvSiteURL:     "https://bistro.md/ru/katalog?date={date}",
		EnvUserAgent:   "my-agent",
		EnvHTTPTimeout: "5s",
		EnvAddr:        "127.0.0.1:9000",
		EnvTZ:          "UTC",
		EnvCacheTTL:    "90m",
		EnvMinInterval: "1m",
		EnvRawDir:      "/tmp/raw",
		EnvNATSEnabled: "false",
		EnvNATSURL:     "nats://localhost:4222",
		EnvNATSToken:   "secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Source: "bistro", SiteURL: "https://bistro.md/ru/katalog?date={date}", UserAgent: "my-agent",
		HTTPTimeout: 5 * time.Second, Addr: "127.0.0.1:9000", Location: time.UTC,
		CacheTTL: 90 * time.Minute, MinInterval: time.Minute, RawDir: "/tmp/raw",
		NATSEnabled: false, NATSURL: "nats://localhost:4222", NATSToken: "secret",
		NATSStream: DefaultNATSStream, NATSDurable: DefaultNATSDurable,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v\nwant %+v", cfg, want)
	}
}

func TestAllProblemsReportedTogether(t *testing.T) {
	_, err := fromLookup(mapLookup(map[string]string{
		EnvHTTPTimeout: "soon",
		EnvCacheTTL:    "-5m",
		EnvTZ:          "Mars/Base",
		EnvSiteURL:     "bistro.md/catalog",
		EnvNATSEnabled: "maybe",
		EnvNATSToken:   "t",
		EnvNATSUser:    "u",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, key := range []string{EnvHTTPTimeout, EnvCacheTTL, EnvTZ, EnvSiteURL, EnvNATSEnabled, EnvNATSToken} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".env")
	os.WriteFile(file, []byte("PARSER_ADDR=:1111\nPARSER_CACHE_TTL=2h\nPARSER_RAW_DIR=from-file\n"), 0o600)
	t.Setenv(EnvAddr, ":2222")   // real environment beats the file
	t.Setenv(EnvRawDir, "")      // empty = not set -> file value is used
	t.Setenv(EnvMinInterval, "") // not in the file either -> default
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":2222" || cfg.CacheTTL != 2*time.Hour || cfg.RawDir != "from-file" || cfg.MinInterval != DefaultMinInterval {
		t.Errorf("unexpected: %+v", cfg)
	}
}

func TestLoadMissingFileIsFine(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(""); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBrokenFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(file, []byte("PARSER_ADDR=:1\nthis is not valid\n"), 0o600)
	_, err := Load(file)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("want an error naming line 2, got %v", err)
	}
}

func TestParseEnv(t *testing.T) {
	text := "\ufeff# comment\n\n" +
		"A=plain\n" +
		"export B = spaced value  \n" +
		"C=\"with # hash and \\\"quotes\\\" and \\\\ and \\n newline\"\n" +
		"D='single # literal \\n'\n" +
		"E=value # trailing comment\n" +
		"F=\n" +
		"G=http://x/y?a=1&b=2#frag\n" +
		"H=\"quoted\" # comment\r\n"
	got, err := parseEnv(text)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"A": "plain",
		"B": "spaced value",
		"C": "with # hash and \"quotes\" and \\ and \n newline",
		"D": `single # literal \n`,
		"E": "value",
		"F": "",
		"G": "http://x/y?a=1&b=2#frag",
		"H": "quoted",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestParseEnvErrors(t *testing.T) {
	for _, bad := range []string{"NOEQUALS", "=value", "BAD KEY=1", `A="open`, `A='open`, `A="x" y`} {
		if _, err := parseEnv(bad); err == nil {
			t.Errorf("parseEnv(%q) should fail", bad)
		}
	}
}

// .env.example documents the defaults, so it must really equal them.
func TestEnvExampleMatchesDefaults(t *testing.T) {
	cfg, err := Load("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	want, err := fromLookup(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf(".env.example differs from the defaults:\n got %+v\nwant %+v", cfg, want)
	}
}
