package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("GATEWAY_BOT_KEY", strings.Repeat("k", 32))
	c, err := Load("does-not-exist.env")
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.RPCTimeout != 5*time.Second || c.NATSUser != "gateway" {
		t.Errorf("defaults = %+v", c)
	}
}

func TestLoadRefusesWeakSettings(t *testing.T) {
	t.Setenv("GATEWAY_BOT_KEY", "short")
	t.Setenv("GATEWAY_RPC_TIMEOUT", "0s")
	_, err := Load("does-not-exist.env")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"GATEWAY_BOT_KEY", "GATEWAY_RPC_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
