package profile

import (
	"context"
	"testing"
	"time"

	"food-order/shared/contracts/users"
)

func TestDueDay(t *testing.T) {
	chisinau, _ := time.LoadLocation("Europe/Chisinau")
	wednesday16 := time.Date(2026, 11, 4, 16, 10, 0, 0, chisinau)
	friday16 := time.Date(2026, 11, 6, 16, 10, 0, 0, chisinau)
	window := 2 * time.Hour

	tests := []struct {
		name    string
		change  map[string]string
		at      time.Time
		wantDay string // "" = not due
	}{
		{"defaults: tomorrow", nil, wednesday16, "2026-11-05"},
		{"friday: monday, not saturday", nil, friday16, "2026-11-09"},
		{"for today", map[string]string{users.SettingRecFor: "today"}, wednesday16, "2026-11-04"},
		{"before the time", map[string]string{users.SettingOrderTime: "16:30"}, wednesday16, ""},
		{"after the window", map[string]string{users.SettingOrderTime: "14:00"}, wednesday16, ""},
		{"not an order day", map[string]string{users.SettingOrderDays: "1,2"}, wednesday16, ""},
		{"no days", map[string]string{users.SettingOrderDays: ""}, wednesday16, ""},
		{"notifications off", map[string]string{users.SettingNotifyPersonal: "false"}, wednesday16, ""},
		{"own time zone", map[string]string{users.SettingTimezone: "Asia/Tokyo", users.SettingOrderTime: "23:00"}, wednesday16, "2026-11-05"},
		{"one day a week", map[string]string{users.SettingOrderDays: "3"}, wednesday16, "2026-11-11"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := defaultSettings()
			for k, v := range tt.change {
				values[k] = v
			}
			day, ok := dueDay(values, tt.at, window)
			if ok != (tt.wantDay != "") || day != tt.wantDay {
				t.Fatalf("day = %q, ok = %v, want %q", day, ok, tt.wantDay)
			}
		})
	}
}

func TestDueListsOnlyWhoCanGetAMessage(t *testing.T) {
	w := newWorld(t)
	w.channel(w.active("ok"), "telegram", "1", "active", 1)
	w.channel(w.active("stopped"), "telegram", "2", "stopped", 1)
	w.channel(w.user("pending", StatusPending), "telegram", "3", "active", 1)
	w.active("nochannel")
	w.channel(w.active("other-time"), "telegram", "4", "active", 1)
	if _, err := w.svc.SetSettings(context.Background(), "other-time", map[string]string{users.SettingOrderTime: "09:00"}); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 11, 4, 14, 5, 0, 0, time.UTC) // 16:05 in Chisinau, a Wednesday
	items, err := w.svc.Due(context.Background(), at, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].UserID != "ok" || items[0].ForDate != "2026-11-05" {
		t.Fatalf("items = %+v", items)
	}
}
