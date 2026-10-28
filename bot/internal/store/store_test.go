package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"food-order/bot/internal/settings"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data", "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRegisterAndProfile(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p := Profile{ChatID: 42, TGUserID: 42, FirstName: "Ion", Username: "ion_md", LanguageCode: "ro"}

	u, created, err := s.Register(ctx, p)
	if err != nil || !created || u.ID == 0 || u.Status != StatusActive || u.Username != "ion_md" {
		t.Fatalf("first register: %+v %v %v", u, created, err)
	}
	if err := s.SetPhone(ctx, u.ID, "+37369000000"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, u.ID, StatusStopped); err != nil {
		t.Fatal(err)
	}

	p.Username = "ion_new"
	u2, created, err := s.Register(ctx, p)
	if err != nil || created || u2.ID != u.ID {
		t.Fatalf("second register: %+v %v %v", u2, created, err)
	}
	if u2.Status != StatusActive || u2.Username != "ion_new" || u2.Phone != "+37369000000" {
		t.Fatalf("/start again must reactivate, refresh the nickname and keep the phone: %+v", u2)
	}

	if _, err := s.UserByChat(ctx, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown chat: %v", err)
	}
	got, err := s.UserByID(ctx, u.ID)
	if err != nil || got.ChatID != 42 {
		t.Fatalf("by id: %+v %v", got, err)
	}

	p.FirstName = "Ioan"
	got, err = s.Touch(ctx, got, p)
	if err != nil || got.FirstName != "Ioan" {
		t.Fatalf("touch: %+v %v", got, err)
	}
	again, _ := s.UserByID(ctx, u.ID)
	if again.FirstName != "Ioan" {
		t.Fatal("touch must save")
	}
}

func TestSettings(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, _, _ := s.Register(ctx, Profile{ChatID: 1})

	st, err := s.Settings(ctx, u.ID)
	if err != nil || st[settings.KeyOrderTime] != "16:00" || st[settings.KeyOrderDays] != "1,2,3,4,5" {
		t.Fatalf("defaults: %v %v", st, err)
	}
	if err := s.SetSetting(ctx, u.ID, settings.KeyOrderTime, "11:30"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, u.ID, settings.KeyOrderTime, "12:00"); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Settings(ctx, u.ID)
	if st[settings.KeyOrderTime] != "12:00" {
		t.Fatalf("saved = %v", st)
	}

	other, _, _ := s.Register(ctx, Profile{ChatID: 2})
	_ = s.SetStatus(ctx, other.ID, StatusStopped)
	active, err := s.ActiveUsers(ctx)
	if err != nil || len(active) != 1 || active[0].User.ID != u.ID || active[0].Settings[settings.KeyOrderTime] != "12:00" {
		t.Fatalf("active = %+v %v", active, err)
	}
}

func TestReminderClaim(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, _, _ := s.Register(ctx, Profile{ChatID: 1})

	n, ok, err := s.ClaimReminder(ctx, u.ID, "2026-11-02")
	if err != nil || !ok || n != 1 {
		t.Fatalf("first claim: %d %v %v", n, ok, err)
	}
	if _, ok, _ := s.ClaimReminder(ctx, u.ID, "2026-11-02"); ok {
		t.Fatal("pending reminder must not be claimed twice")
	}
	_ = s.FinishReminder(ctx, u.ID, "2026-11-02", false)
	n, ok, _ = s.ClaimReminder(ctx, u.ID, "2026-11-02")
	if !ok || n != 2 {
		t.Fatalf("after a failure it can be retried: %d %v", n, ok)
	}
	_ = s.FinishReminder(ctx, u.ID, "2026-11-02", true)
	if _, ok, _ := s.ClaimReminder(ctx, u.ID, "2026-11-02"); ok {
		t.Fatal("a sent reminder is done")
	}

	for i := 0; i < 10; i++ {
		_, ok, _ = s.ClaimReminder(ctx, u.ID, "2026-11-03")
		if ok {
			_ = s.FinishReminder(ctx, u.ID, "2026-11-03", false)
		}
	}
	if _, ok, _ := s.ClaimReminder(ctx, u.ID, "2026-11-03"); ok {
		t.Fatal("after MaxReminderAttempts it must stop")
	}
}

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u, _, _ := s.Register(context.Background(), Profile{ChatID: 5, FirstName: "A"})
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.UserByChat(context.Background(), 5)
	if err != nil || got.ID != u.ID {
		t.Fatalf("after reopen: %+v %v", got, err)
	}
}
