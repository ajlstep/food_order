package tg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"food-order/bot/internal/contracts"
	"food-order/bot/internal/events"
	"food-order/bot/internal/services"
	"food-order/bot/internal/store"
)

// fakeTelegram answers the few Bot API methods the bot uses.
type fakeTelegram struct {
	mu      sync.Mutex
	updates []string // raw JSON updates, given out once
	sent    []map[string]string
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	fields := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = r.ParseMultipartForm(1 << 20)
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
	} else {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		for k, v := range m {
			s, _ := json.Marshal(v)
			fields[k] = strings.Trim(string(s), `"`)
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "getMe":
		io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test"}}`)
	case "getUpdates":
		ups := "[" + strings.Join(f.updates, ",") + "]"
		f.updates = nil
		io.WriteString(w, `{"ok":true,"result":`+ups+`}`)
	case "sendMessage":
		f.sent = append(f.sent, fields)
		io.WriteString(w, `{"ok":true,"result":{"message_id":2,"date":0,"chat":{"id":42,"type":"private"}}}`)
	default:
		io.WriteString(w, `{"ok":true,"result":true}`)
	}
}

func (f *fakeTelegram) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, m["text"]+" | "+m["reply_markup"])
	}
	return out
}

func (f *fakeTelegram) push(updates ...string) {
	f.mu.Lock()
	f.updates = append(f.updates, updates...)
	f.mu.Unlock()
}

func from(chat int64) map[string]any {
	nick := fmt.Sprintf("user%d", chat)
	if chat == 42 {
		nick = "ion_md"
	}
	return map[string]any{"id": chat, "is_bot": false, "first_name": "Ion", "username": nick, "language_code": "ro"}
}

func update(id int, chat int64, text string) string {
	msg := map[string]any{
		"message_id": id, "date": 0, "text": text,
		"chat": map[string]any{"id": chat, "type": "private"},
		"from": from(chat),
	}
	if strings.HasPrefix(text, "/") {
		cmd, _, _ := strings.Cut(text, " ")
		msg["entities"] = []map[string]any{{"type": "bot_command", "offset": 0, "length": len(cmd)}}
	}
	b, _ := json.Marshal(map[string]any{"update_id": id, "message": msg})
	return string(b)
}

func contactUpdate(id int, chat, contactUser int64, phone string) string {
	b, _ := json.Marshal(map[string]any{"update_id": id, "message": map[string]any{
		"message_id": id, "date": 0,
		"chat":    map[string]any{"id": chat, "type": "private"},
		"from":    from(chat),
		"contact": map[string]any{"phone_number": phone, "first_name": "Ion", "user_id": contactUser},
	}})
	return string(b)
}

func callbackUpdate(id int, chat int64, data string) string {
	b, _ := json.Marshal(map[string]any{"update_id": id, "callback_query": map[string]any{
		"id": "cb1", "from": from(chat), "chat_instance": "x", "data": data,
		"message": map[string]any{"message_id": 5, "date": 1, "chat": map[string]any{"id": chat, "type": "private"}},
	}})
	return string(b)
}

type coreRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (c *coreRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.calls = append(c.calls, r.Header.Get("X-User-ID")+" "+r.Method+" "+r.URL.Path+" "+string(b))
	c.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (c *coreRecorder) get() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// newTestBot makes a bot with a real bot.db in a temp dir.
func newTestBot(t *testing.T, tgURL, coreURL string, allowed map[int64]bool) (*Bot, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	hc := &http.Client{Timeout: 5 * time.Second}
	b, err := New(Options{
		Token: "123:abc", AdminChatID: 1000, AllowedChats: allowed,
		Users:     db,
		Core:      services.NewCore(coreURL, hc),
		Catalog:   services.NewCatalog(coreURL, hc),
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ServerURL: tgURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Chisinau")
	b.now = func() time.Time { return time.Date(2026, 10, 28, 13, 0, 0, 0, loc) } // Wednesday
	return b, db
}

func TestSmokeStartMenuSettingsAndEvent(t *testing.T) {
	tgFake := &fakeTelegram{}
	tgSrv := httptest.NewServer(tgFake)
	defer tgSrv.Close()
	core := &coreRecorder{}
	coreSrv := httptest.NewServer(core)
	defer coreSrv.Close()

	b, db := newTestBot(t, tgSrv.URL, coreSrv.URL, map[int64]bool{42: true, 43: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.RunPolling(ctx)
	sent := func() int { return len(tgFake.sentTexts()) }

	// a chat outside the allowed list is ignored completely
	tgFake.push(update(1, 999, "/start"))
	// an allowed but unknown chat is asked to /start
	tgFake.push(update(2, 43, "/meniu"))
	waitFor(t, func() bool { return sent() >= 1 })
	if got := tgFake.sentTexts()[0]; !strings.Contains(got, "Trimite /start") {
		t.Fatalf("unknown user reply = %q", got)
	}
	if _, err := db.UserByChat(ctx, 999); err == nil {
		t.Fatal("a chat outside the allowed list must not be registered")
	}

	// /start registers and asks for the phone
	tgFake.push(update(3, 42, "/start"))
	waitFor(t, func() bool { return sent() >= 2 })
	welcome := tgFake.sentTexts()[1]
	if !strings.Contains(welcome, "Te-am conectat") || !strings.Contains(welcome, "request_contact") || !strings.Contains(welcome, "luni–vineri") {
		t.Fatalf("welcome = %q", welcome)
	}
	u, err := db.UserByChat(ctx, 42)
	if err != nil || u.Username != "ion_md" || u.FirstName != "Ion" {
		t.Fatalf("registered user = %+v %v", u, err)
	}

	// the phone: someone else's contact is refused, own is saved
	tgFake.push(contactUpdate(4, 42, 777, "37369111111"))
	waitFor(t, func() bool { return sent() >= 3 })
	tgFake.push(contactUpdate(5, 42, 42, "37369000000"))
	waitFor(t, func() bool { return sent() >= 4 })
	if u, _ = db.UserByID(ctx, u.ID); u.Phone != "+37369000000" {
		t.Fatalf("phone = %q", u.Phone)
	}

	// settings by command
	tgFake.push(update(6, 42, "/setari ora 11.30"))
	waitFor(t, func() bool { return sent() >= 5 })
	st, _ := db.Settings(ctx, u.ID)
	if st["order_time"] != "11:30" {
		t.Fatalf("order_time = %q", st["order_time"])
	}
	// and by button: Wednesday off
	tgFake.push(callbackUpdate(7, 42, encodeDay(3)))
	waitFor(t, func() bool { st, _ = db.Settings(ctx, u.ID); return st["order_days"] == "1,2,4,5" })

	// /meniu: Wednesday 13:00 -> next order day is Thursday 29, for this user id
	tgFake.push(update(8, 42, "/meniu"))
	waitFor(t, func() bool { return len(core.get()) >= 1 })
	want := fmt.Sprintf(`%d POST /v1/recommendations {"date":"2026-10-29"}`, u.ID)
	if calls := core.get(); len(calls) != 1 || calls[0] != want {
		t.Fatalf("core calls = %q, want %q", calls, want)
	}

	// the event from core goes to this user's chat
	n := sent()
	err = b.RecommendationReady(ctx, contracts.RecommendationReady{UserID: u.ID, Date: "2026-10-29", Picks: []contracts.Pick{
		{DishID: 12, Name: "Ciorbă de pui", Price: f(45)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return sent() > n })
	tgFake.mu.Lock()
	lastMsg := tgFake.sent[len(tgFake.sent)-1]
	tgFake.mu.Unlock()
	if lastMsg["chat_id"] != "42" || !strings.Contains(lastMsg["reply_markup"], "o:20261029:12:0:45") {
		t.Fatalf("recommendation message = %v", lastMsg)
	}

	// an event for a user that does not exist is dropped for good
	err = b.RecommendationReady(ctx, contracts.RecommendationReady{UserID: 9999, Date: "2026-10-29"})
	if !errors.Is(err, events.ErrPermanent) {
		t.Fatalf("unknown user: err = %v", err)
	}

	// /stop: no more commands until /start
	tgFake.push(update(9, 42, "/stop"))
	waitFor(t, func() bool { u, _ = db.UserByID(ctx, u.ID); return u.Status == store.StatusStopped })
	if err := b.RecommendationReady(ctx, contracts.RecommendationReady{UserID: u.ID, Date: "2026-10-29"}); err != nil {
		t.Fatalf("stopped user: %v", err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}
