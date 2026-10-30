package config

import (
	"strings"
	"testing"
)

func getter(m map[string]string) func(string, string) string {
	return func(k, def string) string {
		if v, ok := m[k]; ok {
			return v
		}
		return def
	}
}

const goodSecret = "0123456789abcdef0123"

func TestParseWebhook(t *testing.T) {
	cfg, err := parse(getter(map[string]string{
		EnvTelegramToken: "123:abc",
		EnvAdminChatID:   "42",
		EnvWebhookURL:    "https://ajl.duckdns.org/tg/webhook",
		EnvWebhookSecret: goodSecret,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeWebhook || cfg.WebhookPath != "/tg/webhook" {
		t.Fatalf("mode=%s path=%s", cfg.Mode, cfg.WebhookPath)
	}
}

func TestParseWebhookErrors(t *testing.T) {
	base := map[string]string{EnvTelegramToken: "1:a"}
	cases := map[string]map[string]string{
		"missing url":  {EnvWebhookSecret: goodSecret},
		"http url":     {EnvWebhookURL: "http://x.org/tg", EnvWebhookSecret: goodSecret},
		"no path":      {EnvWebhookURL: "https://x.org/", EnvWebhookSecret: goodSecret},
		"short secret": {EnvWebhookURL: "https://x.org/tg", EnvWebhookSecret: "abc"},
		"bad secret":   {EnvWebhookURL: "https://x.org/tg", EnvWebhookSecret: "has spaces in it!!!!"},
		"bad mode":     {EnvMode: "push"},
	}
	for name, extra := range cases {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		if _, err := parse(getter(m)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := parse(getter(map[string]string{
		EnvTelegramToken: "123:abc",
		EnvAdminChatID:   "42",
		EnvMode:          "polling",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminChatID != 42 || cfg.CoreURL != "http://core:9081" || cfg.NATSStream != "FOOD" || cfg.Addr != ":9001" ||
		cfg.DBPath != "data/bot.db" || len(cfg.AllowedChats) != 0 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestParseErrors(t *testing.T) {
	_, err := parse(getter(map[string]string{
		EnvAdminChatID:  "not-a-number",
		EnvAllowedChats: "1, x",
		EnvNATSToken:    "t",
		EnvNATSUser:     "u",
		EnvHTTPTimeout:  "abc",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{EnvTelegramToken, EnvAdminChatID, EnvAllowedChats, EnvHTTPTimeout, EnvNATSToken} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestParseEnv(t *testing.T) {
	vars, err := parseEnv("\ufeff# comment\nA=1\nexport B = two # note\nC=\"with # hash\"\nD='x'\n\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two", "C": "with # hash", "D": "x"}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
	if _, err := parseEnv("no equals sign"); err == nil {
		t.Error("expected an error for a bad line")
	}
}

func TestAllowedChats(t *testing.T) {
	cfg, err := parse(getter(map[string]string{
		EnvTelegramToken: "1:a", EnvMode: "polling", EnvAllowedChats: "42, 7,9",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedChats) != 3 || !cfg.AllowedChats[7] || cfg.AdminChatID != 0 {
		t.Fatalf("%+v", cfg)
	}
}
