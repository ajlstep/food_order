// Package config reads every setting of the gateway: real environment
// variables first, then the .env file, then the defaults. See .env.example.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is every setting of the gateway.
type Config struct {
	HTTPAddr string // where the gateway listens, for example ":8080"

	NATSURL      string
	NATSUser     string
	NATSPassword string

	// BotKey is the key the bot sends in Authorization. Whoever has it can
	// speak for any Telegram user, so it is long and never committed.
	BotKey string

	// RPCTimeout is how long the gateway waits for a service to answer.
	RPCTimeout time.Duration

	AdditionalLogs bool // also send debug records to the logs service
	LogConsole     bool // also print every record to the console
}

// minKeyLength: a shorter key could be guessed by trying.
const minKeyLength = 32

// Load reads envFile (optional) and the environment.
func Load(envFile string) (Config, error) {
	file, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	var errs []error
	r := reader{file: file, errs: &errs}

	c := Config{
		HTTPAddr: r.str("GATEWAY_HTTP_ADDR", ":8080"),

		NATSURL:      r.str("GATEWAY_NATS_URL", "nats://nats:4222"),
		NATSUser:     r.str("GATEWAY_NATS_USER", "gateway"),
		NATSPassword: r.str("GATEWAY_NATS_PASSWORD", ""),

		BotKey:     r.str("GATEWAY_BOT_KEY", ""),
		RPCTimeout: r.dur("GATEWAY_RPC_TIMEOUT", "5s"),

		AdditionalLogs: r.boolean("GATEWAY_ADDITIONAL_LOGS", false),
		LogConsole:     r.boolean("GATEWAY_LOG_CONSOLE", false),
	}
	errs = append(errs, c.validate()...)
	return c, errors.Join(errs...)
}

func (c Config) validate() []error {
	var errs []error
	if len(c.BotKey) < minKeyLength {
		errs = append(errs, fmt.Errorf("GATEWAY_BOT_KEY: required, at least %d characters", minKeyLength))
	}
	if c.RPCTimeout <= 0 {
		errs = append(errs, errors.New("GATEWAY_RPC_TIMEOUT: must be more than 0"))
	}
	if c.HTTPAddr == "" {
		errs = append(errs, errors.New("GATEWAY_HTTP_ADDR: required"))
	}
	return errs
}

// reader gets values by key: environment, then file, then the default.
// Wrong values are collected, so the person sees all of them at once.
type reader struct {
	file map[string]string
	errs *[]error
}

func (r reader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	if v, ok := r.file[key]; ok {
		return v
	}
	return def
}

func (r reader) dur(key, def string) time.Duration {
	raw := r.str(key, def)
	d, err := time.ParseDuration(raw)
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s: not a valid duration: %q", key, raw))
	}
	return d
}

func (r reader) boolean(key string, def bool) bool {
	raw := r.str(key, strconv.FormatBool(def))
	b, err := strconv.ParseBool(raw)
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s: not true or false: %q", key, raw))
	}
	return b
}

func readEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, sc.Err()
}
