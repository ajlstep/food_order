package tg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"food-order/bot/internal/store"
)

// statsCore answers GET /v1/stats with a different amount per user.
type statsCore struct {
	mu    sync.Mutex
	calls []string
}

func (c *statsCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.calls = append(c.calls, r.Header.Get("X-User-ID")+" "+r.URL.Path+"?"+r.URL.RawQuery)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.Header.Get("X-User-ID") {
	case "1":
		_, _ = w.Write([]byte(`{"orders": 10, "spent": 450}`))
	default:
		_, _ = w.Write([]byte(`{"orders": 3, "spent": 150.5}`))
	}
}

// lastTo returns the last message sent to a chat.
func (f *fakeTelegram) lastTo(chat string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		if f.sent[i]["chat_id"] == chat {
			return f.sent[i]
		}
	}
	return nil
}

func (f *fakeTelegram) countTo(chat string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.sent {
		if m["chat_id"] == chat {
			n++
		}
	}
	return n
}

func TestGroupFlow(t *testing.T) {
	tgFake := &fakeTelegram{}
	tgSrv := httptest.NewServer(tgFake)
	defer tgSrv.Close()
	core := &statsCore{}
	coreSrv := httptest.NewServer(core)
	defer coreSrv.Close()

	b, db := newTestBot(t, tgSrv.URL, coreSrv.URL, nil)
	b.now = time.Now // joined_at is written with the real clock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.RunPolling(ctx)

	id := 0
	say := func(chat int64, text string) string { // sends and waits for the reply
		key := fmtChat(chat)
		n := tgFake.countTo(key)
		id++
		tgFake.push(update(id, chat, text))
		waitFor(t, func() bool { return tgFake.countTo(key) > n })
		return tgFake.lastTo(key)["text"]
	}
	press := func(chat int64, data string, waitChat int64) string {
		key := fmtChat(waitChat)
		n := tgFake.countTo(key)
		id++
		tgFake.push(callbackUpdate(id, chat, data))
		waitFor(t, func() bool { return tgFake.countTo(key) > n })
		return tgFake.lastTo(key)["text"]
	}

	// ana (chat 101, user id 1) and ion (chat 42, user id 2)
	say(101, "/start")
	say(42, "/start")

	if got := say(42, "/grup invita @user101"); !strings.Contains(got, "Nu ești în niciun grup") {
		t.Fatalf("invite without group: %q", got)
	}
	if got := say(101, "/grup nou   Endava  Chișinău "); !strings.Contains(got, "Am creat grupul «Endava Chișinău»") {
		t.Fatalf("create: %q", got)
	}
	// only the owner: the list must still be sent, without a keyboard
	n0 := tgFake.countTo("101")
	say(101, "/grup membri")
	if m := tgFake.lastTo("101"); tgFake.countTo("101") != n0+1 || !strings.Contains(m["text"], "1 membri") || strings.Contains(m["reply_markup"], "null") {
		t.Fatalf("members of a one-person group = %v", m)
	}
	if kb := membersKeyboard(store.Group{OwnerID: 1}, nil); kb.InlineKeyboard == nil {
		t.Fatal("keyboard rows must never be nil")
	}
	if got := say(101, "/grup invita @nobody"); !strings.Contains(got, "Nu există așa utilizator") {
		t.Fatalf("unknown user: %q", got)
	}

	// invite ion by nickname: ion gets the invitation with buttons
	n := tgFake.countTo("42")
	if got := say(101, "/grup invita @ION_MD"); !strings.Contains(got, "Am trimis invitația") {
		t.Fatalf("invite: %q", got)
	}
	waitFor(t, func() bool { return tgFake.countTo("42") > n })
	inv := tgFake.lastTo("42")
	if !strings.Contains(inv["text"], "te invită în grupul") {
		t.Fatalf("invitation = %v", inv)
	}
	data := regexp.MustCompile(`gi:\d+:y`).FindString(inv["reply_markup"])
	if data == "" {
		t.Fatalf("no accept button: %s", inv["reply_markup"])
	}
	if got := say(42, "/grup stat"); !strings.Contains(got, "Nu ești în niciun grup") {
		t.Fatalf("not yet a member: %q", got)
	}

	// ion accepts -> ana is told
	press(42, data, 42)
	waitFor(t, func() bool { return strings.Contains(tgFake.lastTo("101")["text"], "a acceptat invitația") })
	ion, _ := db.UserByChat(ctx, 42)
	if g, err := db.GroupOf(ctx, ion.ID); err != nil || g.Name != "Endava Chișinău" {
		t.Fatalf("ion's group: %+v %v", g, err)
	}

	// only the owner sees statistics
	if got := say(42, "/grup stat"); !strings.Contains(got, "Doar owner-ul") {
		t.Fatalf("member stats: %q", got)
	}
	got := say(101, "/grup stat")
	if !strings.Contains(got, "600,50 lei") || !strings.Contains(got, "13 mese") || !strings.Contains(got, "2 membri") {
		t.Fatalf("group stats: %q", got)
	}
	if got := say(101, "/grup stat @ion_md 7"); !strings.Contains(got, "150,50 lei") {
		t.Fatalf("member stats: %q", got)
	}
	core.mu.Lock()
	for _, c := range core.calls {
		if !strings.HasPrefix(c, "1 /v1/stats") && !strings.HasPrefix(c, "2 /v1/stats") {
			t.Errorf("unexpected core call %q", c)
		}
	}
	core.mu.Unlock()

	// members list with a remove button; ana removes ion, ion is told
	say(101, "/grup membri")
	list := tgFake.lastTo("101")
	if !strings.Contains(list["text"], "2 membri") || !strings.Contains(list["reply_markup"], "gk:2") {
		t.Fatalf("members = %v", list)
	}
	press(101, "gk:2", 42)
	if !strings.Contains(tgFake.lastTo("42")["text"], "Ai fost scos") {
		t.Fatalf("removed user not told: %v", tgFake.lastTo("42"))
	}
	if _, err := db.GroupOf(ctx, ion.ID); err == nil {
		t.Fatal("ion must be out")
	}

	// ana leaves: she was the last one -> the group is deleted
	if got := say(101, "/grup iesi"); !strings.Contains(got, "Sigur ieși") {
		t.Fatalf("leave: %q", got)
	}
	ana, _ := db.UserByChat(ctx, 101)
	g, _ := db.GroupOf(ctx, ana.ID)
	if got := press(101, encodeLeave(g.ID), 101); !strings.Contains(got, "l-am șters") {
		t.Fatalf("last leaves: %q", got)
	}
	if _, err := db.GroupByID(ctx, g.ID); err != store.ErrNoGroup {
		t.Fatalf("group must be deleted: %v", err)
	}
}

func fmtChat(chat int64) string { return strconv.FormatInt(chat, 10) }
