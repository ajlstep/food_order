package settings

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	ok := map[string]string{"16:30": "16:30", "9.05": "09:05", "1630": "16:30", "7": "07:00", "23-59": "23:59", "11h15": "11:15"}
	for in, want := range ok {
		if got, err := parseTime(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, in := range []string{"", "24:00", "12:60", "ab", "1:2:3"} {
		if _, err := parseTime(in); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}

func TestParseDays(t *testing.T) {
	ok := map[string]string{
		"luni-vineri":      "1,2,3,4,5",
		"1-5":              "1,2,3,4,5",
		"Lu, Mi, Vi":       "1,3,5",
		"marți joi":        "2,4",
		"vineri-luni":      "1,5,6,7",
		"sâmbătă,duminică": "6,7",
		"пн-пт":            "1,2,3,4,5",
	}
	for in, want := range ok {
		if got, err := parseDays(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, in := range []string{"", "luni-xyz", "8"} {
		if _, err := parseDays(in); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}

func TestFormatDays(t *testing.T) {
	cases := map[string]string{
		"1,2,3,4,5": "luni–vineri", "1,3,5": "Lu, Mi, Vi", "": "nicio zi",
		"1,2,3,4,5,6,7": "în fiecare zi", "6,7": "Sâ, Du",
	}
	for in, want := range cases {
		if got := FormatDays(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if got := ToggleDay("1,2,3", 2); got != "1,3" {
		t.Errorf("toggle off: %q", got)
	}
	if got := ToggleDay("1,3", 7); got != "1,3,7" {
		t.Errorf("toggle on: %q", got)
	}
	if got := ShiftTime("23:45", 30); got != "00:15" {
		t.Errorf("shift: %q", got)
	}
	if got := ShiftTime("00:10", -30); got != "23:40" {
		t.Errorf("shift back: %q", got)
	}
}

func TestFind(t *testing.T) {
	d, err := Find("ORA")
	if err != nil || d.Key != KeyOrderTime {
		t.Fatalf("Find(ora) = %v, %v", d.Key, err)
	}
	if _, err := Find("culoare"); err == nil {
		t.Fatal("expected ErrUnknown")
	}
}

func TestDueAndNextOrderDay(t *testing.T) {
	s := WithDefaults(map[string]string{KeyOrderTime: "16:00"}) // Monday–Friday
	loc := s.Location()
	at := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, loc) }
	// 2026-10-30 is a Friday, 2026-11-02 a Monday

	if _, ok := s.Due(at(30, 15, 59), time.Hour); ok {
		t.Error("before the order time it must not be due")
	}
	d, ok := s.Due(at(30, 16, 0), time.Hour)
	if !ok || d.Format("2006-01-02") != "2026-11-02" {
		t.Errorf("Friday 16:00 -> %v %v, want Monday 2026-11-02", d, ok)
	}
	if _, ok := s.Due(at(30, 17, 0), time.Hour); ok {
		t.Error("after the window it must not be due")
	}
	if _, ok := s.Due(at(31, 16, 10), time.Hour); ok {
		t.Error("Saturday is not an order day")
	}
	// the time zone of the user counts, not the one of the server
	if _, ok := s.Due(time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC), time.Hour); !ok {
		t.Error("14:05 UTC is 16:05 in Chisinau: must be due")
	}

	if got := s.NextOrderDay(at(28, 10, 0)).Format("2006-01-02"); got != "2026-10-29" {
		t.Errorf("next after Wednesday = %s", got)
	}
	none := WithDefaults(map[string]string{KeyOrderDays: ""})
	if got := none.NextOrderDay(at(28, 10, 0)).Format("2006-01-02"); got != "2026-10-29" {
		t.Errorf("no days -> tomorrow, got %s", got)
	}
}

func TestBadSavedValuesFallBack(t *testing.T) {
	s := WithDefaults(map[string]string{KeyOrderTime: "garbage", KeyTimezone: "Mars/Base", "future_key": "x"})
	if h, m := s.OrderTime(); h != 16 || m != 0 {
		t.Errorf("time = %d:%d", h, m)
	}
	if s.Location().String() != "Europe/Chisinau" {
		t.Errorf("loc = %s", s.Location())
	}
	if s["future_key"] != "x" {
		t.Error("unknown keys must be kept")
	}
}
