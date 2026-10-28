package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// readEnvFile reads a .env file. A file that does not exist gives an empty map.
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
	vars, err := parseEnv(string(b))
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return vars, nil
}

// parseEnv understands the usual .env format:
//
//	# comment
//	KEY=value
//	export KEY=value
//	KEY="value with spaces and \"quotes\""   (\n \" \\ work inside double quotes)
//	KEY='taken literally'
//	KEY=value # comment after a space
func parseEnv(text string) (map[string]string, error) {
	vars := map[string]string{}
	text = strings.TrimPrefix(text, "\ufeff") // BOM from Windows editors
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))

		key, rest, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !validKey(key) {
			return nil, fmt.Errorf("line %d: expected KEY=value", i+1)
		}
		val, err := parseValue(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("line %d (%s): %w", i+1, key, err)
		}
		vars[key] = val
	}
	return vars, nil
}

func validKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func parseValue(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	switch s[0] {
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", errors.New("quote is not closed")
		}
		return s[1 : 1+end], afterQuote(s[end+2:])
	case '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch c := s[i]; c {
			case '\\':
				if i+1 >= len(s) {
					return "", errors.New("quote is not closed")
				}
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case '"', '\\':
					b.WriteByte(s[i])
				default:
					b.WriteByte('\\')
					b.WriteByte(s[i])
				}
			case '"':
				return b.String(), afterQuote(s[i+1:])
			default:
				b.WriteByte(c)
			}
		}
		return "", errors.New("quote is not closed")
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s), nil
}

// afterQuote allows only spaces or a comment after a closing quote.
func afterQuote(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.HasPrefix(rest, "#") {
		return nil
	}
	return fmt.Errorf("unexpected text after the closing quote: %q", rest)
}
