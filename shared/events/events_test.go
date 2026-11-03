package events

import (
	"testing"
	"time"
)

func TestNewIDSortsByTime(t *testing.T) {
	a := NewID(time.UnixMilli(1000))
	b := NewID(time.UnixMilli(2000))
	if len(a) != 26 || len(b) != 26 {
		t.Fatalf("lengths %d, %d", len(a), len(b))
	}
	if a >= b {
		t.Fatalf("%s is not before %s", a, b)
	}
}
