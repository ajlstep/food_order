package collector

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func TestParsePlainText(t *testing.T) {
	e := Parse("logs.info", []byte("hello world"), now)
	if e.Level != "info" || e.Message != "hello world" || e.TS != "2026-10-31T12:00:00.000Z" || e.Fields != "" {
		t.Fatalf("%+v", e)
	}
}

func TestParseJSON(t *testing.T) {
	e := Parse("logs.error", []byte(`{"service":"core","msg":"boom","time":"2026-10-31T10:00:00+02:00","user":7}`), now)
	if e.Service != "core" || e.Message != "boom" || e.TS != "2026-10-31T08:00:00.000Z" || e.Fields != `{"user":7}` {
		t.Fatalf("%+v", e)
	}
}

func TestParseEnvelope(t *testing.T) {
	e := Parse("logs.warn", []byte(`{"id":"X","type":"logs.warn","version":1,"ts":"2026-10-31T09:00:00Z","data":{"app":"bot","message":"slow"}}`), now)
	if e.Service != "bot" || e.Message != "slow" || e.TS != "2026-10-31T09:00:00.000Z" || e.Fields != "" {
		t.Fatalf("%+v", e)
	}
}

func TestParseOtherLevelAndUnixMillis(t *testing.T) {
	e := Parse("logs.payment.debug", []byte(`{"message":"x","ts":1788000000000}`), now)
	if e.Level != "payment.debug" || e.TS != "2026-08-29T10:40:00.000Z" {
		t.Fatalf("%+v", e)
	}
}

func TestParseBadTimeStaysAField(t *testing.T) {
	e := Parse("logs.info", []byte(`{"message":"x","ts":"yesterday"}`), now)
	if e.TS != "2026-10-31T12:00:00.000Z" || e.Fields != `{"ts":"yesterday"}` {
		t.Fatalf("%+v", e)
	}
}
