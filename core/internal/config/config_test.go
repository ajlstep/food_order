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
		if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok && !strings.HasPrefix(line, "#") && !strings.HasPrefix(k, "CORE_") {
			t.Errorf("unexpected variable %q", k)
		}
	}
}

func TestValues(t *testing.T) {
	cfg, err := FromLookup(lookup(map[string]string{
		EnvMenuRequestTime: "14:30",
		EnvMenuTimeout:     "", // empty = default
		EnvMenuDays:        "1,3,5",
		EnvScoreWeights:    "rating=2, repeat=0",
		EnvLLMEnabled:      "false",
		EnvNATSURL:         "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MenuRequestTime != (Clock{14, 30}) || !cfg.MenuDays[3] || cfg.MenuDays[2] {
		t.Errorf("menu: %+v %v", cfg.MenuRequestTime, cfg.MenuDays)
	}
	if cfg.MenuTimeout != 10*time.Minute {
		t.Errorf("empty value must keep the default: %v", cfg.MenuTimeout)
	}
	if cfg.Score.Weights.Rating != 2 || cfg.Score.Weights.Repeat != 0 || cfg.Score.Weights.Similarity != DefaultWeights.Similarity {
		t.Errorf("weights: %+v", cfg.Score.Weights)
	}
	if cfg.LLMEnabled || cfg.NATSURL != "" { // set to "" = no broker
		t.Errorf("llm %v nats %q", cfg.LLMEnabled, cfg.NATSURL)
	}
}

func TestErrorsTogether(t *testing.T) {
	_, err := FromLookup(lookup(map[string]string{
		EnvMenuRequestTime: "25:00",
		EnvMenuTimeout:     "soon",
		EnvScoreWeights:    "speed=1",
		EnvRecPicks:        "9",
		EnvTZ:              "Mars/Base",
	}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, key := range []string{EnvMenuRequestTime, EnvMenuTimeout, EnvScoreWeights, EnvRecPicks, EnvTZ} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}

func TestClockOn(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Chisinau")
	at := Clock{15, 0}.On(time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC), loc)
	if at.Hour() != 15 || at.Day() != 30 || at.Location() != loc {
		t.Errorf("On = %v", at)
	}
}

func TestParseEnv(t *testing.T) {
	vars, err := parseEnv("\ufeff# c\nexport A=1\nB=\"x y\"\nC=z # note\n")
	if err != nil || vars["A"] != "1" || vars["B"] != "x y" || vars["C"] != "z" {
		t.Fatalf("%v %v", vars, err)
	}
	if _, err := parseEnv("nope"); err == nil {
		t.Fatal("expected error")
	}
}
