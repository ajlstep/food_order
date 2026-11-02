package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAndFile(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	os.WriteFile(env, []byte("USERS_CODE_SECRET=0123456789abcdef\nUSERS_CODE_TTL=10m\n# comment\n"), 0o600)
	t.Setenv("USERS_CODES_PER_HOUR", "7") // the environment wins over the file

	c, err := Load(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.CodeTTL != 10*time.Minute || c.CodesPerHour != 7 || c.CodeResend != 30*time.Second {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLoadReportsAllProblemsAtOnce(t *testing.T) {
	t.Setenv("USERS_CODE_TTL", "soon")
	t.Setenv("USERS_MAIL_DRIVER", "pigeon")
	_, err := Load(filepath.Join(t.TempDir(), "missing.env"))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"USERS_CODE_SECRET", "USERS_CODE_TTL", "USERS_MAIL_DRIVER"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}
