package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"food-order/bot/internal/settings"
	"food-order/bot/internal/store"
)

type fakeCore struct {
	calls []string
	fail  bool
}

func (f *fakeCore) RequestRecommendation(_ context.Context, userID int64, date string) error {
	f.calls = append(f.calls, date)
	if f.fail {
		return errors.New("core down")
	}
	return nil
}

func TestTick(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u, _, _ := st.Register(ctx, store.Profile{ChatID: 1})
	_ = st.SetSetting(ctx, u.ID, settings.KeyOrderTime, "16:00")
	stopped, _, _ := st.Register(ctx, store.Profile{ChatID: 2})
	_ = st.SetStatus(ctx, stopped.ID, store.StatusStopped)

	loc, _ := time.LoadLocation("Europe/Chisinau")
	now := time.Date(2026, 10, 30, 15, 59, 0, 0, loc) // Friday
	core := &fakeCore{}
	var notified []string
	s := &Scheduler{
		Store: st, Core: core, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
		Notify: func(_ context.Context, _ store.User, text string) { notified = append(notified, text) },
	}

	s.Tick(ctx)
	if len(core.calls) != 0 {
		t.Fatal("15:59: too early")
	}
	now = now.Add(time.Minute) // 16:00
	s.Tick(ctx)
	s.Tick(ctx) // same minute again (or after a restart): no second call
	if len(core.calls) != 1 || core.calls[0] != "2026-11-02" {
		t.Fatalf("calls = %v, want one call for Monday", core.calls)
	}

	// next Monday: core is down -> retried every minute, then the user is told
	now = time.Date(2026, 11, 2, 16, 0, 0, 0, loc)
	core.fail = true
	core.calls = nil
	for i := 0; i < 8; i++ {
		s.Tick(ctx)
		now = now.Add(time.Minute)
	}
	if len(core.calls) != store.MaxReminderAttempts || core.calls[0] != "2026-11-03" {
		t.Fatalf("calls = %v", core.calls)
	}
	if len(notified) != 1 {
		t.Fatalf("notified = %v", notified)
	}
}
