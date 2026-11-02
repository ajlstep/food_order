// Package config reads every setting of users: real environment variables
// first, then the .env file, then the defaults. See .env.example.
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

// Config is every setting of the users service.
type Config struct {
	NATSURL      string
	NATSUser     string
	NATSPassword string

	DBPath string

	// CodeSecret signs the confirmation codes (HMAC). Without it a stolen
	// database would let someone work out all the 4-character codes.
	CodeSecret      string
	CodeTTL         time.Duration // how long a code works
	CodeResend      time.Duration // pause before a new code may be asked
	CodeMaxAttempts int           // wrong attempts before the code is cancelled
	CodesPerHour    int           // codes for one email address in an hour

	MailDriver   string // console | smtp
	MailFrom     string
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string

	AdditionalLogs bool // also send debug records to the logs service
	LogConsole     bool // also print every record to the console
}

// Load reads envFile (optional) and the environment.
func Load(envFile string) (Config, error) {
	file, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	var errs []error
	r := reader{file: file, errs: &errs}

	c := Config{
		NATSURL:      r.str("USERS_NATS_URL", "nats://nats:4222"),
		NATSUser:     r.str("USERS_NATS_USER", ""),
		NATSPassword: r.str("USERS_NATS_PASSWORD", ""),

		DBPath: r.str("USERS_DB_PATH", "data/users.db"),

		CodeSecret:      r.str("USERS_CODE_SECRET", ""),
		CodeTTL:         r.dur("USERS_CODE_TTL", "30m"),
		CodeResend:      r.dur("USERS_CODE_RESEND", "30s"),
		CodeMaxAttempts: r.integer("USERS_CODE_MAX_ATTEMPTS", 5),
		CodesPerHour:    r.integer("USERS_CODES_PER_HOUR", 5),

		MailDriver:   r.str("USERS_MAIL_DRIVER", "console"),
		MailFrom:     r.str("USERS_MAIL_FROM", ""),
		SMTPHost:     r.str("USERS_SMTP_HOST", ""),
		SMTPPort:     r.integer("USERS_SMTP_PORT", 587),
		SMTPUser:     r.str("USERS_SMTP_USER", ""),
		SMTPPassword: r.str("USERS_SMTP_PASSWORD", ""),

		AdditionalLogs: r.boolean("USERS_ADDITIONAL_LOGS", false),
		LogConsole:     r.boolean("USERS_LOG_CONSOLE", false),
	}
	errs = append(errs, c.validate()...)
	return c, errors.Join(errs...)
}

func (c Config) validate() []error {
	var errs []error
	if len(c.CodeSecret) < 16 {
		errs = append(errs, errors.New("USERS_CODE_SECRET: required, at least 16 characters"))
	}
	if c.CodeMaxAttempts < 1 {
		errs = append(errs, errors.New("USERS_CODE_MAX_ATTEMPTS: must be at least 1"))
	}
	if c.CodesPerHour < 1 {
		errs = append(errs, errors.New("USERS_CODES_PER_HOUR: must be at least 1"))
	}
	switch c.MailDriver {
	case "console":
	case "smtp":
		if c.SMTPHost == "" || c.MailFrom == "" {
			errs = append(errs, errors.New("USERS_MAIL_DRIVER=smtp needs USERS_SMTP_HOST and USERS_MAIL_FROM"))
		}
	default:
		errs = append(errs, fmt.Errorf("USERS_MAIL_DRIVER: %q is not console or smtp", c.MailDriver))
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
	if err != nil || d < 0 {
		*r.errs = append(*r.errs, fmt.Errorf("%s: not a valid duration: %q", key, raw))
	}
	return d
}

func (r reader) integer(key string, def int) int {
	raw := r.str(key, strconv.Itoa(def))
	n, err := strconv.Atoi(raw)
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s: not a number: %q", key, raw))
	}
	return n
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
