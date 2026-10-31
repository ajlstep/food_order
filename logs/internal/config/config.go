// Package config reads every setting of logs: real environment variables
// first, then the .env file, then the defaults. See .env.example.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// Config is every setting of the logs service.
type Config struct {
	NATSURL      string
	NATSToken    string
	NATSUser     string
	NATSPassword string
	Stream       string
	Subject      string
	Durable      string
	MaxAge       time.Duration

	DBPath string
	Keep   time.Duration
	Addr   string
}

// Load reads envFile (optional) and the environment.
func Load(envFile string) (Config, error) {
	file, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		if v, ok := file[key]; ok {
			return v
		}
		return def
	}
	var errs []error
	dur := func(key, def string) time.Duration {
		d, err := time.ParseDuration(get(key, def))
		if err != nil || d < 0 {
			errs = append(errs, fmt.Errorf("%s: not a valid duration: %q", key, get(key, def)))
		}
		return d
	}
	c := Config{
		NATSURL:      get("LOGS_NATS_URL", "nats://nats:4222"),
		NATSToken:    get("LOGS_NATS_TOKEN", ""),
		NATSUser:     get("LOGS_NATS_USER", ""),
		NATSPassword: get("LOGS_NATS_PASSWORD", ""),
		Stream:       get("LOGS_NATS_STREAM", "LOGS"),
		Subject:      get("LOGS_NATS_SUBJECT", "logs.>"),
		Durable:      get("LOGS_NATS_DURABLE", "logs-collector"),
		MaxAge:       dur("LOGS_NATS_MAX_AGE", "168h"),
		DBPath:       get("LOGS_DB_PATH", "data/logs.db"),
		Keep:         dur("LOGS_KEEP", "720h"),
		Addr:         get("LOGS_ADDR", ":9084"),
	}
	return c, errors.Join(errs...)
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
