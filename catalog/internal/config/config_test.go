package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func lookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// .env.example must document the defaults, exactly.
func TestEnvExampleIsTheDefaults(t *testing.T) {
	b, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	vars, err := parseEnv(string(b))
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromLookup(lookup(vars))
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	if got.Location.String() != want.Location.String() {
		t.Errorf("location %v != %v", got.Location, want.Location)
	}
	got.Location, want.Location = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf(".env.example differs from the defaults:\n%+v\n%+v", got, want)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok && !strings.HasPrefix(line, "#") && !strings.HasPrefix(k, "CATALOG_") {
			t.Errorf("unexpected variable %q", k)
		}
	}
}

func TestValues(t *testing.T) {
	cfg, err := FromLookup(lookup(map[string]string{
		EnvAnalysisEnabled: "nu",
		EnvAnalysisTimeout: "", // empty = default
		EnvNATSURL:         "", // set to "" = no broker
		EnvTick:            "5s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnalysisEnabled || cfg.NATSURL != "" || cfg.Tick != 5*time.Second {
		t.Errorf("analysis %v nats %q tick %v", cfg.AnalysisEnabled, cfg.NATSURL, cfg.Tick)
	}
	if cfg.AnalysisTimeout != 30*time.Minute {
		t.Errorf("empty value must keep the default: %v", cfg.AnalysisTimeout)
	}
}

func TestErrorsTogether(t *testing.T) {
	_, err := FromLookup(lookup(map[string]string{
		EnvAnalysisTimeout: "soon",
		EnvEmbedEnabled:    "maybe",
		EnvTZ:              "Mars/Base",
		EnvNATSToken:       "t",
		EnvNATSUser:        "u",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, k := range []string{EnvAnalysisTimeout, EnvEmbedEnabled, EnvTZ, EnvNATSToken} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error does not name %s: %v", k, err)
		}
	}
}

func TestParseEnv(t *testing.T) {
	vars, err := parseEnv("\ufeff# c\nexport A=1\nB=\"x y\"\nC=z # comment\n")
	if err != nil {
		t.Fatal(err)
	}
	if vars["A"] != "1" || vars["B"] != "x y" || vars["C"] != "z" {
		t.Errorf("%v", vars)
	}
	if _, err := parseEnv("nope"); err == nil {
		t.Error("expected an error")
	}
}
