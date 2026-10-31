package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"food-order/logs/internal/store"
)

var (
	tsKeys      = []string{"ts", "time", "timestamp", "t"}
	messageKeys = []string{"message", "msg", "text"}
	serviceKeys = []string{"service", "source", "app", "component"}
)

// Parse turns a NATS message into a log entry. Nothing is rejected: a
// message that is not JSON is saved as it is. The level is the part of the
// subject after "logs." (logs.warn -> warn, logs.payment.error -> payment.error).
//
// A JSON object gives: ts (ts/time/timestamp), service (service/source/app),
// message (message/msg/text); every other field goes to Entry.Fields.
// The common envelope {id,type,version,ts,data} is unwrapped.
func Parse(subject string, data []byte, received time.Time) store.Entry {
	e := store.Entry{
		Subject: subject,
		Level:   strings.TrimPrefix(subject, "logs."),
		TS:      received.UTC().Format(store.TimeFormat),
		Message: string(data),
	}
	if e.Level == "" {
		e.Level = "other"
	}

	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return e // plain text
	}
	// common envelope: the log itself is in "data"
	if inner, ok := obj["data"].(map[string]any); ok && obj["type"] != nil {
		if _, has := obj["ts"]; has {
			if _, dup := inner["ts"]; !dup {
				inner["ts"] = obj["ts"]
			}
		}
		obj = inner
	}

	if v, k := take(obj, tsKeys); k != "" {
		if t, ok := parseTime(v); ok {
			e.TS = t.UTC().Format(store.TimeFormat)
		} else {
			obj[k] = v // not a time: keep it as a field
		}
	}
	if v, _ := take(obj, serviceKeys); v != nil {
		e.Service = fmt.Sprint(v)
	}
	if v, k := take(obj, messageKeys); k != "" {
		e.Message = fmt.Sprint(v)
	} else {
		e.Message = string(data) // no message field: show the whole JSON
	}
	if len(obj) > 0 {
		raw, _ := json.Marshal(obj) // encoding/json sorts map keys
		e.Fields = string(raw)
	}
	return e
}

// take removes and returns the first of keys that exists in obj.
func take(obj map[string]any, keys []string) (any, string) {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			delete(obj, k)
			return v, k
		}
	}
	return nil, ""
}

// parseTime accepts RFC 3339 text and unix seconds or milliseconds.
func parseTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t, true
		}
		if n, err := strconv.ParseFloat(x, 64); err == nil {
			return unix(n), true
		}
	case json.Number:
		if n, err := x.Float64(); err == nil {
			return unix(n), true
		}
	}
	return time.Time{}, false
}

func unix(n float64) time.Time {
	if n > 1e11 { // milliseconds
		return time.UnixMilli(int64(n))
	}
	return time.Unix(int64(n), int64((n-float64(int64(n)))*1e9))
}
