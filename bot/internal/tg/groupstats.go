package tg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"food-order/bot/internal/contracts"
	"food-order/bot/internal/settings"
	"food-order/bot/internal/store"
)

// Group statistics (owner only). The orders are in core, per user; the bot
// asks core for each member's statistics (GET /v1/stats with X-User-ID)
// and adds them up. A member counts only from the day they joined.

type memberStat struct {
	member store.Member
	from   string // the period actually counted for this member
	stats  contracts.Stats
	err    error
	none   bool // joined after the period
}

// groupStats: "/grup stat [@nick|telefon] [zile | YYYY-MM-DD YYYY-MM-DD]".
func (b *Bot) groupStats(ctx context.Context, g store.Group, args string) {
	s := sessionFrom(ctx)
	fields := strings.Fields(args)

	var target *store.User
	if len(fields) > 0 && (strings.HasPrefix(fields[0], "@") || store.NormalizePhone(fields[0]) != "") {
		u, err := b.users.FindUser(ctx, fields[0])
		if errors.Is(err, store.ErrNotFound) {
			b.reply(ctx, "❌ "+notFoundText(fields[0]))
			return
		}
		if err != nil {
			b.fail(ctx, "find user", err)
			return
		}
		target = &u
		fields = fields[1:]
	}
	from, to, err := parsePeriod(fields, b.today(s.settings))
	if err != nil {
		b.reply(ctx, "❌ "+esc(err.Error())+"\n\n"+groupStatsHelp)
		return
	}

	members, err := b.users.Members(ctx, g.ID)
	if err != nil {
		b.fail(ctx, "members", err)
		return
	}
	if target != nil {
		var found []store.Member
		for _, m := range members {
			if m.User.ID == target.ID {
				found = append(found, m)
			}
		}
		if len(found) == 0 {
			b.reply(ctx, fmt.Sprintf("%s nu e în grupul tău.", esc(displayName(*target))))
			return
		}
		members = found
	}

	rows := b.collectStats(ctx, members, from, to, s.settings)
	if target != nil {
		b.reply(ctx, memberStatsText(rows[0], to))
		return
	}
	b.reply(ctx, groupStatsText(g, from, to, rows))
}

func (b *Bot) collectStats(ctx context.Context, members []store.Member, from, to string, st settings.Settings) []memberStat {
	rows := make([]memberStat, len(members))
	sem := make(chan struct{}, 4) // at most 4 calls to core at once
	var wg sync.WaitGroup
	for i, m := range members {
		rows[i] = memberStat{member: m, from: from}
		joined := m.JoinedAt.In(st.Location()).Format(dateLayout)
		if joined > rows[i].from {
			rows[i].from = joined
		}
		if rows[i].from > to {
			rows[i].none = true
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			rows[i].stats, rows[i].err = b.core.Stats(cctx, rows[i].member.User.ID, rows[i].from, to)
			if rows[i].err != nil {
				b.log.Warn("member stats", "user_id", rows[i].member.User.ID, "err", rows[i].err)
			}
		}(i)
	}
	wg.Wait()
	return rows
}

// parsePeriod reads the period of a statistic:
//
//	(nothing)              this month, until today
//	7                      the last 7 days
//	2026-10-01 2026-10-31  or 01.10.2026 31.10.2026: from - to
func parsePeriod(f []string, today time.Time) (from, to string, err error) {
	switch len(f) {
	case 0:
		return today.AddDate(0, 0, -today.Day()+1).Format(dateLayout), today.Format(dateLayout), nil
	case 1:
		n, err := strconv.Atoi(f[0])
		if err != nil || n < 1 || n > 366 {
			return "", "", errors.New("perioada nu e bună")
		}
		return today.AddDate(0, 0, -(n - 1)).Format(dateLayout), today.Format(dateLayout), nil
	case 2:
		a, err1 := parseDay(f[0], today.Location())
		z, err2 := parseDay(f[1], today.Location())
		if err1 != nil || err2 != nil {
			return "", "", errors.New("datele trebuie să fie ca 2026-10-01 sau 01.10.2026")
		}
		if z.Before(a) {
			a, z = z, a
		}
		if z.Sub(a) > 366*24*time.Hour {
			return "", "", errors.New("perioada poate fi de cel mult un an")
		}
		return a.Format(dateLayout), z.Format(dateLayout), nil
	}
	return "", "", errors.New("perioada nu e bună")
}

func parseDay(s string, loc *time.Location) (time.Time, error) {
	for _, layout := range []string{dateLayout, "02.01.2006", "2.1.2006"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("bad date")
}

func groupStatsText(g store.Group, from, to string, rows []memberStat) string {
	var orders, forced, failed int
	var spent float64
	for _, r := range rows {
		if r.err != nil {
			failed++
			continue
		}
		orders += r.stats.Orders
		forced += r.stats.Forced
		spent += r.stats.Spent
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].stats.Spent > rows[j].stats.Spent })

	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>%s</b>\n%s — %s\n\n", esc(g.Name), esc(humanDate(from)), esc(humanDate(to)))
	fmt.Fprintf(&b, "Total: <b>%s</b> · %d mese", money(spent), orders)
	if forced > 0 {
		fmt.Fprintf(&b, " (din lipsă: %d)", forced)
	}
	fmt.Fprintf(&b, " · %d membri\n\n", len(rows))
	for i, r := range rows {
		fmt.Fprintf(&b, "%d. %s — ", i+1, esc(displayName(r.member.User)))
		switch {
		case r.none:
			b.WriteString("a intrat după perioadă")
		case r.err != nil:
			b.WriteString("nu am putut lua datele")
		default:
			fmt.Fprintf(&b, "%s · %d mese", money(r.stats.Spent), r.stats.Orders)
			if r.from > from {
				fmt.Fprintf(&b, " <i>(din %s)</i>", esc(humanDate(r.from)))
			}
		}
		b.WriteString("\n")
	}
	if failed > 0 {
		fmt.Fprintf(&b, "\n<i>%d membri lipsesc din total: serviciul nu a răspuns.</i>\n", failed)
	}
	b.WriteString("\nPe o persoană: <code>/grup stat @nickname</code>")
	return b.String()
}

func memberStatsText(r memberStat, to string) string {
	head := "👤 <b>" + esc(displayName(r.member.User)) + "</b>\n"
	switch {
	case r.none:
		return head + "A intrat în grup după perioada aceasta."
	case r.err != nil:
		return head + "❌ " + userError(r.err)
	}
	st := r.stats
	st.From, st.To = r.from, to // only the time in the group
	return head + statsText(st)
}
