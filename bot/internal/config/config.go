// Package config reads the bot settings from the environment and, optionally,
// from a .env file. A variable set in the environment wins over the file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Names of the environment variables.
const (
	EnvTelegramToken = "BOT_TELEGRAM_TOKEN"
	EnvAdminChatID   = "BOT_ADMIN_CHAT_ID"
	EnvAllowedChats  = "BOT_ALLOWED_CHAT_IDS"
	EnvDBPath        = "BOT_DB_PATH"
	EnvCoreURL       = "BOT_CORE_URL"
	EnvCatalogURL    = "BOT_CATALOG_URL"
	EnvNATSURL       = "BOT_NATS_URL"
	EnvNATSToken     = "BOT_NATS_TOKEN"
	EnvNATSUser      = "BOT_NATS_USER"
	EnvNATSPassword  = "BOT_NATS_PASSWORD"
	EnvNATSStream    = "BOT_NATS_STREAM"
	EnvNATSDurable   = "BOT_NATS_DURABLE"
	EnvAddr          = "BOT_ADDR"
	EnvHTTPTimeout   = "BOT_HTTP_TIMEOUT"
	EnvMode          = "BOT_MODE"
	EnvWebhookURL    = "BOT_WEBHOOK_URL"
	EnvWebhookSecret = "BOT_WEBHOOK_SECRET"
)

// How updates come from Telegram.
const (
	ModeWebhook = "webhook" // Telegram POSTs every update to BOT_WEBHOOK_URL
	ModePolling = "polling" // the bot asks Telegram (getUpdates); for local runs
)

// Telegram allows 1-256 chars of A-Z, a-z, 0-9, _ and -; we ask for at least 16.
var secretRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,256}$`)

// Config is everything the bot needs to start.
type Config struct {
	TelegramToken string
	AdminChatID   int64          // gets food.alert; 0 = alerts only in the log
	AllowedChats  map[int64]bool // empty = anyone can /start
	DBPath        string         // bot.db: users and their settings

	CoreURL     string
	CatalogURL  string
	HTTPTimeout time.Duration

	NATSURL      string // empty = run without events (only commands)
	NATSToken    string
	NATSUser     string
	NATSPassword string
	NATSStream   string
	NATSDurable  string

	Addr string // HTTP server: /healthz and, in webhook mode, the webhook path

	Mode          string // webhook | polling
	WebhookURL    string // public https URL that Telegram calls
	WebhookPath   string // path part of WebhookURL, served on Addr
	WebhookSecret string // Telegram sends it in X-Telegram-Bot-Api-Secret-Token
}

// Load reads the .env file (if it exists) and the environment.
func Load(envFile string) (Config, error) {
	file, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok {
			return strings.TrimSpace(v)
		}
		if v, ok := file[key]; ok {
			return strings.TrimSpace(v)
		}
		return def
	}
	return parse(get)
}

func parse(get func(key, def string) string) (Config, error) {
	var errs []error
	cfg := Config{
		TelegramToken: get(EnvTelegramToken, ""),
		CoreURL:       strings.TrimRight(get(EnvCoreURL, "http://core:9081"), "/"),
		CatalogURL:    strings.TrimRight(get(EnvCatalogURL, "http://catalog:9082"), "/"),
		NATSURL:       get(EnvNATSURL, "nats://nats:4222"),
		NATSToken:     get(EnvNATSToken, ""),
		NATSUser:      get(EnvNATSUser, ""),
		NATSPassword:  get(EnvNATSPassword, ""),
		NATSStream:    get(EnvNATSStream, "FOOD"),
		NATSDurable:   get(EnvNATSDurable, "bot"),
		Addr:          get(EnvAddr, ":9001"),
		DBPath:        get(EnvDBPath, "data/bot.db"),
	}

	if cfg.TelegramToken == "" {
		errs = append(errs, fmt.Errorf("%s is required", EnvTelegramToken))
	}

	if v := get(EnvAdminChatID, ""); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id == 0 {
			errs = append(errs, fmt.Errorf("%s=%q is not a chat id", EnvAdminChatID, v))
		}
		cfg.AdminChatID = id
	}

	cfg.AllowedChats = map[int64]bool{}
	for _, part := range strings.FieldsFunc(get(EnvAllowedChats, ""), func(r rune) bool { return r == ',' || r == ' ' }) {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id == 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not a chat id", EnvAllowedChats, part))
			continue
		}
		cfg.AllowedChats[id] = true
	}
	if cfg.DBPath == "" {
		errs = append(errs, fmt.Errorf("%s is required", EnvDBPath))
	}

	d, err := time.ParseDuration(get(EnvHTTPTimeout, "10s"))
	if err != nil || d <= 0 {
		errs = append(errs, fmt.Errorf("%s: expected a duration like 10s", EnvHTTPTimeout))
	}
	cfg.HTTPTimeout = d

	cfg.Mode = strings.ToLower(get(EnvMode, ModeWebhook))
	switch cfg.Mode {
	case ModePolling:
	case ModeWebhook:
		cfg.WebhookURL = get(EnvWebhookURL, "")
		cfg.WebhookSecret = get(EnvWebhookSecret, "")
		u, err := url.Parse(cfg.WebhookURL)
		switch {
		case cfg.WebhookURL == "":
			errs = append(errs, fmt.Errorf("%s is required when %s=webhook (e.g. https://ajl.duckdns.org/tg/webhook)", EnvWebhookURL, EnvMode))
		case err != nil || u.Scheme != "https" || u.Host == "":
			errs = append(errs, fmt.Errorf("%s=%q: Telegram needs a public https URL", EnvWebhookURL, cfg.WebhookURL))
		default:
			cfg.WebhookPath = u.EscapedPath()
			if cfg.WebhookPath == "" || cfg.WebhookPath == "/" || cfg.WebhookPath == "/healthz" {
				errs = append(errs, fmt.Errorf("%s needs its own path, e.g. /tg/webhook", EnvWebhookURL))
			}
		}
		if !secretRe.MatchString(cfg.WebhookSecret) {
			errs = append(errs, fmt.Errorf("%s: 16-256 chars of A-Z a-z 0-9 _ - (e.g. openssl rand -hex 32)", EnvWebhookSecret))
		}
	default:
		errs = append(errs, fmt.Errorf("%s=%q: expected webhook or polling", EnvMode, cfg.Mode))
	}

	if cfg.NATSToken != "" && cfg.NATSUser != "" {
		errs = append(errs, fmt.Errorf("set either %s or %s/%s, not both", EnvNATSToken, EnvNATSUser, EnvNATSPassword))
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// readEnvFile reads simple KEY=value lines. A missing file gives an empty map.
// Supports comments (#), "export KEY=value" and values in ” or "".
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
