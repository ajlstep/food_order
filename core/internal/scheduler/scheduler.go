// Package scheduler is the menu scheduler module of core: it wakes up every
// CORE_TICK and lets the service do what is due (ask for the menu, retry,
// alert, ask for ratings, recover what a broker outage left behind).
//
// When each user gets the recommendation is decided by the bot (the user's
// order time and days); core only asks for the menu, one for everybody.
package scheduler

import (
	"context"
	"time"
)

// Ticker is what runs on every tick (implemented by *service.Service).
type Ticker interface{ Tick(ctx context.Context) }

// Run calls t.Tick at once and then every interval until ctx ends.
func Run(ctx context.Context, t Ticker, interval time.Duration) {
	t.Tick(ctx)
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			t.Tick(ctx)
		}
	}
}
