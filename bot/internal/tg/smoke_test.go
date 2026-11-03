package tg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"food-order/bot/internal/notify"
	"food-order/bot/internal/services"
	"food-order/shared/contracts/catalog"
	contracts "food-order/shared/contracts/core"
	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
	"food-order/shared/streams"
)

// blockedChat is a chat that blocked the bot.
const blockedChat = "66"

// fakeTelegram answers the few Bot API methods the bot uses.
type fakeTelegram struct {
	mu       sync.Mutex
	updates  []string // raw JSON updates, given out once
	sent     []map[string]string
	answered int // button presses the bot answered
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
		if fields["chat_id"] == blockedChat {
			io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
			return
		}
		f.sent = append(f.sent, fields)
		io.WriteString(w, `{"ok":true,"result":{"message_id":2,"date":0,"chat":{"id":42,"type":"private"}}}`)
	case "answerCallbackQuery":
		f.answered++
		io.WriteString(w, `{"ok":true,"result":true}`)
	default:
		io.WriteString(w, `{"ok":true,"result":true}`)
	}
}

func (f *fakeTelegram) answers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answered
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

// fakeUsers is the gateway with users behind it, in memory. The rules of the
// sign-up are tested in users; here only the words and the flow of the bot.
type fakeUsers struct {
	mu        sync.Mutex
	status    map[string]string // external id -> account status
	ids       map[string]string // external id -> user_id
	statuses  []string          // the SetChannelStatus calls
	phone     string
	settings  map[string]string
	allergens []string
	prefs     []users.Pref
	calls     []string
	down      bool
	org       orgFake
}

// orgFake answers the org calls; err is what the next one refuses with.
type orgFake struct {
	company users.Company
	group   users.Group
	invites []users.Invite
	err     error
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{status: map[string]string{}, ids: map[string]string{}, settings: map[string]string{}}
}

func (f *fakeUsers) activate(ext, userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[ext], f.ids[ext] = users.StatusActive, userID
}

func (f *fakeUsers) refuse(status int, code string, details map[string]any) error {
	return &services.GatewayError{Status: status, Code: code, Details: details}
}

func (f *fakeUsers) log(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeUsers) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

func (f *fakeUsers) Identify(_ context.Context, ext string, in users.ResolveRequest) (users.ResolveResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return users.ResolveResponse{}, f.refuse(503, "unavailable", nil)
	}
	if f.status[ext] == "" {
		f.status[ext], f.ids[ext] = users.StatusPending, "u-"+ext
	}
	f.log("identify " + ext + " " + in.ChatRef + " " + in.Nickname + " " + in.DisplayName)
	return users.ResolveResponse{UserID: f.ids[ext], Status: f.status[ext], DisplayName: in.DisplayName}, nil
}

func (f *fakeUsers) SetChannelStatus(_ context.Context, ext, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, ext+" "+status)
	return nil
}

func (f *fakeUsers) RequestEmailCode(_ context.Context, _, email string) (users.EmailRequestResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("email " + email)
	if email == "rau@" {
		return users.EmailRequestResponse{}, f.refuse(400, users.ErrInvalidEmail, nil)
	}
	if email == "repede@exemplu.md" {
		return users.EmailRequestResponse{}, f.refuse(429, users.ErrCooldown, map[string]any{"retry_after": 25.0})
	}
	return users.EmailRequestResponse{Sent: true, ValidForSecond: 1800}, nil
}

func (f *fakeUsers) ConfirmEmail(_ context.Context, ext, code string) (users.EmailConfirmResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if code != "AB2C" {
		return users.EmailConfirmResponse{}, f.refuse(422, users.ErrWrongCode, map[string]any{"attempts_left": 4.0})
	}
	f.status[ext] = users.StatusActive
	return users.EmailConfirmResponse{UserID: f.ids[ext], Result: users.ConfirmConfirmed}, nil
}

func (f *fakeUsers) SetPhone(_ context.Context, _, phone string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phone = phone
	return nil
}

func (f *fakeUsers) Profile(_ context.Context, ext string) (users.ProfileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return users.ProfileResponse{UserID: f.ids[ext], DisplayName: "Ion", Email: "ion@exemplu.md", Phone: f.phone}, nil
}

func (f *fakeUsers) Channels(context.Context, string) (users.ChannelsResponse, error) {
	return users.ChannelsResponse{Channels: []users.Channel{{Channel: "telegram", Status: users.IdentityActive}}, Latest: "telegram"}, nil
}

func (f *fakeUsers) Settings(context.Context, string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.settings), nil
}

func (f *fakeUsers) SetSettings(_ context.Context, _ string, values map[string]string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	maps.Copy(f.settings, values)
	return maps.Clone(f.settings), nil
}

func (f *fakeUsers) Allergens(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allergens, nil
}

func (f *fakeUsers) SetAllergens(_ context.Context, _ string, list []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allergens = list
	return list, nil
}

func (f *fakeUsers) Prefs(context.Context, string) ([]users.Pref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prefs, nil
}

func (f *fakeUsers) SetPrefs(_ context.Context, _ string, items []users.Pref) ([]users.Pref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefs = items
	return items, nil
}

func (f *fakeUsers) RequestRecommendation(_ context.Context, ext, date string) error {
	f.log("recommend " + ext + " " + date)
	return nil
}

func (f *fakeUsers) Recommendation(context.Context, string, string) (contracts.Recommendation, error) {
	return contracts.Recommendation{}, nil
}

func (f *fakeUsers) CreateOrder(_ context.Context, ext string, o contracts.CreateOrderRequest) (contracts.Order, error) {
	f.log(fmt.Sprintf("order %s personal=%v", ext, o.Personal))
	return contracts.Order{ID: 1, GroupName: "Etaj 3", Address: "Str. Mare 1"}, nil
}

func (f *fakeUsers) Rate(_ context.Context, ext string, id int64, rating int, _ string) error {
	f.log(fmt.Sprintf("rate %s %d %d", ext, id, rating))
	return nil
}

func (f *fakeUsers) Orders(context.Context, string, string, string) ([]contracts.Order, error) {
	return nil, nil
}

func (f *fakeUsers) Stats(context.Context, string, string, string) (contracts.Stats, error) {
	return contracts.Stats{}, nil
}

func (f *fakeUsers) ConfirmAllergens(context.Context, string, int64, []string) error { return nil }

func (f *fakeUsers) Partners(context.Context, string, string) ([]catalog.Partner, error) {
	return []catalog.Partner{{Name: "Bistro", OrderUntilMs: 1793000000000}}, nil
}

func (f *fakeUsers) Company(context.Context, string) (users.Company, error) {
	return f.org.company, f.org.err
}

func (f *fakeUsers) CreateCompany(_ context.Context, _, name string) (users.Company, error) {
	f.log("company " + name)
	return users.Company{ID: "c", Name: name, IsOwner: true}, f.org.err
}

func (f *fakeUsers) UpdateCompany(_ context.Context, _ string, in users.CompanyUpdateRequest) (users.Company, error) {
	if in.MaxDeliveryFee != nil {
		f.log(fmt.Sprintf("fee %d", *in.MaxDeliveryFee))
	}
	return f.org.company, f.org.err
}

func (f *fakeUsers) LeaveCompany(context.Context, string) error { return f.org.err }

func (f *fakeUsers) InviteToCompany(_ context.Context, _, who string) error {
	f.log("invite " + who)
	return f.org.err
}

func (f *fakeUsers) RemoveFromCompany(_ context.Context, _, id string) error {
	f.log("remove " + id)
	return f.org.err
}

func (f *fakeUsers) CompanyReport(context.Context, string, string, string) (contracts.CompanyReport, error) {
	return contracts.CompanyReport{}, f.org.err
}

func (f *fakeUsers) Group(context.Context, string) (users.Group, error) {
	return f.org.group, f.org.err
}

func (f *fakeUsers) CreateGroup(_ context.Context, _ string, in users.GroupCreateRequest) (users.Group, error) {
	f.log("group " + in.Name + " / " + in.Address)
	return users.Group{Name: in.Name, Address: in.Address}, f.org.err
}

func (f *fakeUsers) DeleteGroup(context.Context, string) error { return f.org.err }

func (f *fakeUsers) LeaveGroup(context.Context, string) error { return f.org.err }

func (f *fakeUsers) InviteToGroup(context.Context, string, string) error { return f.org.err }

func (f *fakeUsers) RemoveFromGroup(context.Context, string, string) error { return f.org.err }

func (f *fakeUsers) Invites(context.Context, string) ([]users.Invite, error) {
	return f.org.invites, f.org.err
}

func (f *fakeUsers) AnswerInvite(_ context.Context, _, id string, accept bool) error {
	f.log(fmt.Sprintf("answer %s %v", id, accept))
	return f.org.err
}

func (f *fakeUsers) setting(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings[key]
}

// logBuffer keeps what the bot logs, to check what does not get there.
type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newTestBot(t *testing.T, tgURL string, people *fakeUsers, allowed map[int64]bool) *Bot {
	t.Helper()
	return newTestBotLog(t, tgURL, people, allowed, io.Discard)
}

func newTestBotLog(t *testing.T, tgURL string, people *fakeUsers, allowed map[int64]bool, out io.Writer) *Bot {
	t.Helper()
	b, err := New(Options{
		Token: "123:abc", AdminChatID: 1000, AllowedChats: allowed,
		Users:     people,
		Log:       slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ServerURL: tgURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Chisinau")
	b.now = func() time.Time { return time.Date(2026, 10, 28, 13, 0, 0, 0, loc) } // Wednesday
	return b
}

// harness is a running bot with everything around it faked.
type harness struct {
	t      *testing.T
	tg     *fakeTelegram
	people *fakeUsers
	bot    *Bot
	nextID int
}

func startBot(t *testing.T, out io.Writer, allowed map[int64]bool) *harness {
	t.Helper()
	tgFake := &fakeTelegram{}
	tgSrv := httptest.NewServer(tgFake)
	t.Cleanup(tgSrv.Close)
	people := newFakeUsers()
	b := newTestBotLog(t, tgSrv.URL, people, allowed, out)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.RunPolling(ctx)
	return &harness{t: t, tg: tgFake, people: people, bot: b}
}

// say sends one update and waits for the bot to answer it.
func (h *harness) say(chat int64, text string) string {
	h.t.Helper()
	h.nextID++
	return h.push(update(h.nextID, chat, text))
}

func (h *harness) press(chat int64, data string) string {
	h.t.Helper()
	h.nextID++
	return h.push(callbackUpdate(h.nextID, chat, data))
}

// pressQuiet is for the buttons that change a message instead of sending one.
func (h *harness) pressQuiet(chat int64, data string) {
	h.t.Helper()
	h.nextID++
	before := h.tg.answers()
	h.tg.push(callbackUpdate(h.nextID, chat, data))
	waitFor(h.t, func() bool { return h.tg.answers() > before })
}

func (h *harness) push(u string) string {
	h.t.Helper()
	before := len(h.tg.sentTexts())
	h.tg.push(u)
	waitFor(h.t, func() bool { return len(h.tg.sentTexts()) > before })
	texts := h.tg.sentTexts()
	return texts[len(texts)-1]
}

func TestSignUpFlow(t *testing.T) {
	var logs logBuffer
	h := startBot(t, &logs, nil)

	// nothing works before the email is confirmed
	if got := h.say(42, "/start"); !strings.Contains(got, "scrie adresa ta de email") {
		t.Fatalf("/start = %q", got)
	}
	if got := h.say(42, "/meniu"); !strings.Contains(got, "scrie adresa ta de email") {
		t.Fatalf("/meniu while waiting = %q", got)
	}
	if h.people.called("recommend 42 2026-10-29") {
		t.Fatal("food must not be asked for an account in waiting")
	}

	if got := h.say(42, "ACUM"); !strings.Contains(got, "Mai ai 4 încercări") {
		t.Fatalf("wrong code = %q", got)
	}
	if got := h.say(42, "rau@"); !strings.Contains(got, "nu pare corectă") {
		t.Fatalf("bad email = %q", got)
	}
	if got := h.say(42, "repede@exemplu.md"); !strings.Contains(got, "peste 25 de secunde") {
		t.Fatalf("cooldown = %q", got)
	}

	got := h.say(42, "ana@exemplu.md")
	for _, want := range []string{"ana@exemplu.md", "Spam", "30 de minute", kindResend + ":0", kindOtherEmail + ":0"} {
		if !strings.Contains(got, want) {
			t.Errorf("code sent: no %q in %q", want, got)
		}
	}

	// "send again" uses the address it remembers
	h.press(42, encodeResend())
	if n := strings.Count(strings.Join(h.people.calls, "\n"), "email ana@exemplu.md"); n != 2 {
		t.Fatalf("emails asked = %d, want 2", n)
	}
	if got := h.press(42, encodeOtherEmail()); !strings.Contains(got, "scrie adresa ta de email") {
		t.Fatalf("other email = %q", got)
	}

	got = h.say(42, "AB2C")
	for _, want := range []string{"Emailul e confirmat", "luni–vineri", "request_contact"} {
		if !strings.Contains(got, want) {
			t.Errorf("confirmed: no %q in %q", want, got)
		}
	}

	// from now on it is an active account
	if got := h.say(42, "/meniu"); !strings.Contains(got, "Am cerut recomandarea") {
		t.Fatalf("/meniu when active = %q", got)
	}
	// an old sign-up button does nothing
	h.pressQuiet(42, encodeResend())

	for _, secret := range []string{"ana@exemplu.md", "AB2C", "ACUM", "ion_md"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("%q reached the logs", secret)
		}
	}
}

func TestIdentifySendsTheChannelProfile(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	h.say(42, "/start")
	if !h.people.called("identify 42 42 ion_md Ion") {
		t.Fatalf("calls = %q", h.people.calls)
	}
}

func TestOnlyAllowedPrivateChats(t *testing.T) {
	h := startBot(t, io.Discard, map[int64]bool{42: true})
	h.tg.push(update(1, 999, "/start"))
	h.nextID = 1
	h.say(42, "/start") // answered: the first update was only skipped
	if h.people.called("identify 999 999 user999 Ion") {
		t.Fatal("a chat outside the allowed list must not reach the gateway")
	}
}

func TestGatewayDown(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	h.people.down = true
	if got := h.say(42, "/start"); !strings.Contains(got, "nu răspunde") {
		t.Fatalf("reply = %q", got)
	}
}

func TestBlockedAccount(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	h.people.activate("42", "u-42")
	h.people.status["42"] = users.StatusBlocked
	if got := h.say(42, "/setari"); !strings.Contains(got, "nu e activ") {
		t.Fatalf("reply = %q", got)
	}
}

func TestActiveAccount(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	h.people.activate("42", "u-42")

	// the phone: someone else's contact is refused, own is saved
	h.nextID++
	h.push(contactUpdate(h.nextID, 42, 777, "37369111111"))
	if h.people.phone != "" {
		t.Fatal("someone else's phone was saved")
	}
	h.nextID++
	h.push(contactUpdate(h.nextID, 42, 42, "37369000000"))
	if h.people.phone != "+37369000000" {
		t.Fatalf("phone = %q", h.people.phone)
	}

	// settings: words become the fixed form
	h.say(42, "/setari ora 11.30")
	if got := h.people.setting("order_time"); got != "11:30" {
		t.Fatalf("order_time = %q", got)
	}
	h.say(42, "/setari zile luni-miercuri")
	if got := h.people.setting("order_days"); got != "1,2,3" {
		t.Fatalf("order_days = %q", got)
	}
	if got := h.say(42, "/setari fus Marte/Olimp"); !strings.Contains(got, "nu cunosc") {
		t.Fatalf("bad zone = %q", got)
	}
	// and buttons
	h.pressQuiet(42, encodeDay(3))
	waitFor(t, func() bool { return h.people.setting("order_days") == "1,2" })
	h.pressQuiet(42, encodeToggle("notify_personal"))
	waitFor(t, func() bool { return h.people.setting("notify_personal") == "false" })

	// the two lists of /preferinte
	h.say(42, "/preferinte add alergen Gluten")
	got := h.say(42, "/preferinte add nu! ceapă")
	if !strings.Contains(got, "1. gluten") || !strings.Contains(got, "2. ceapă (niciodată)") {
		t.Fatalf("prefs = %q", got)
	}
	h.say(42, "/preferinte del 1")
	if len(h.people.allergens) != 0 || len(h.people.prefs) != 1 {
		t.Fatalf("after del: %q %+v", h.people.allergens, h.people.prefs)
	}

	if got := h.say(42, "/profil"); !strings.Contains(got, "ion@exemplu.md") || !strings.Contains(got, "telegram (activ)") {
		t.Fatalf("profile = %q", got)
	}
	// /stop and /start change the channel
	h.say(42, "/stop")
	h.say(42, "/start")
	if !slices.Equal(h.people.statuses, []string{"42 stopped", "42 active"}) {
		t.Fatalf("statuses = %q", h.people.statuses)
	}

	// food goes through the gateway, for the person in the channel
	h.say(42, "/meniu")
	if !h.people.called("recommend 42 2026-11-02") { // the days are now Monday-Tuesday
		t.Fatalf("calls = %q", h.people.calls)
	}
}

func TestRecommendationDayFollowsTheSetting(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	h.people.activate("42", "u-42")
	h.people.settings["rec_for"] = "today"
	h.say(42, "/meniu")
	if !h.people.called("recommend 42 2026-10-28") {
		t.Fatalf("calls = %q", h.people.calls)
	}
}

func TestOrderShowsWhereItGoes(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	h.people.activate("42", "u-42")
	h.people.settings["order_to"] = users.OrderToPersonal
	got := h.press(42, encodeOrder("2026-10-29", 12, false, f(45)))
	if !h.people.called("order 42 personal=true") {
		t.Fatalf("calls = %q", h.people.calls)
	}
	if !strings.Contains(got, "Etaj 3, Str. Mare 1") {
		t.Fatalf("order = %q", got)
	}
}

func TestCompanyAndGroupCommands(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	h.people.activate("42", "u-42")

	// no company: the words for it, and the help
	h.people.org.err = &services.GatewayError{Code: users.ErrNotInCompany, Status: 422}
	if got := h.say(42, "/companie"); !strings.Contains(got, "Nu ești într-o companie") || !strings.Contains(got, "/companie nou") {
		t.Fatalf("/companie = %q", got)
	}
	h.people.org.err = nil

	if got := h.say(42, "/companie nou Acme SRL"); !strings.Contains(got, "Acme SRL") {
		t.Fatalf("new = %q", got)
	}
	h.say(42, "/companie invita @ion")
	h.say(42, "/companie taxa 20")
	if !h.people.called("company Acme SRL") || !h.people.called("invite @ion") || !h.people.called("fee 2000") {
		t.Fatalf("calls = %q", h.people.calls)
	}

	// the group needs the bar between name and address
	if got := h.say(42, "/grup nou Etaj 3"); !strings.Contains(got, "/grup nou Etajul 3") {
		t.Fatalf("no bar = %q", got)
	}
	h.say(42, "/grup nou Etaj 3 | Str. Mare 1, of. 5")
	if !h.people.called("group Etaj 3 / Str. Mare 1, of. 5") {
		t.Fatalf("calls = %q", h.people.calls)
	}

	// a refusal of users comes in words
	h.people.org.err = &services.GatewayError{Code: users.ErrOwnerStays, Status: 422}
	if got := h.say(42, "/companie iesi"); !strings.Contains(got, "Proprietarul nu poate ieși") {
		t.Fatalf("leave = %q", got)
	}
	h.people.org.err = nil

	// only the owner has the buttons to take somebody out
	h.people.org.company = users.Company{Name: "Acme", IsOwner: true, Members: []users.Member{
		{UserID: "u-42", DisplayName: "Ion", IsOwner: true}, {UserID: "u-7", DisplayName: "Ana", Nickname: "ana"},
	}}
	h.say(42, "/companie membri")
	h.tg.mu.Lock()
	markup := h.tg.sent[len(h.tg.sent)-1]["reply_markup"]
	h.tg.mu.Unlock()
	if !strings.Contains(markup, "kc:u-7") || strings.Contains(markup, "kc:u-42") {
		t.Fatalf("markup = %s", markup)
	}
	h.pressQuiet(42, encodeKick(kindKickCompany, "u-7"))
	waitFor(t, func() bool { return h.people.called("remove u-7") })
}

func TestInvitations(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	h.people.activate("42", "u-42")
	if got := h.say(42, "/invitatii"); !strings.Contains(got, "Nu ai invitații") {
		t.Fatalf("none = %q", got)
	}
	h.people.org.invites = []users.Invite{{ID: "i-1", Kind: food.OrgCompany, Name: "Acme"}}
	if got := h.say(42, "/invitatii"); !strings.Contains(got, "Acme") || !strings.Contains(got, "Proprietarul va vedea") {
		t.Fatalf("invite = %q", got)
	}
	h.pressQuiet(42, encodeInvite("i-1", true))
	waitFor(t, func() bool { return h.people.called("answer i-1 true") })
}

func TestNotifications(t *testing.T) {
	h := startBot(t, io.Discard, nil)
	ctx := context.Background()
	to := notify.Recipient{UserID: "u-42", ChatRef: "42"}

	err := h.bot.RecommendationReady(ctx, to, contracts.RecommendationReady{Date: "2026-10-29", Picks: []contracts.Pick{
		{DishID: 12, Name: "Ciorbă de pui", Price: f(45)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h.tg.mu.Lock()
	last := h.tg.sent[len(h.tg.sent)-1]
	h.tg.mu.Unlock()
	if last["chat_id"] != "42" || !strings.Contains(last["reply_markup"], "o:20261029:12:0:45") {
		t.Fatalf("recommendation message = %v", last)
	}

	if err := h.bot.RatingRequested(ctx, to, contracts.RatingRequested{OrderID: 5}); err != nil {
		t.Fatal(err)
	}
	if err := h.bot.Alert(ctx, food.Alert{Service: "core", Level: "error", Text: "nu e meniu"}); err != nil {
		t.Fatal(err)
	}
	texts := h.tg.sentTexts()
	if !strings.Contains(texts[1], "r:5:5") || !strings.Contains(texts[2], "nu e meniu") {
		t.Fatalf("texts = %q", texts)
	}
	h.tg.mu.Lock()
	admin := h.tg.sent[2]["chat_id"]
	h.tg.mu.Unlock()
	if admin != "1000" {
		t.Fatalf("the alert went to %q, want the admin chat", admin)
	}

	if err := h.bot.OrgInvite(ctx, to, food.OrgInvite{InviteID: "i-1", Kind: food.OrgGroup, Name: "Etaj 3"}); err != nil {
		t.Fatal(err)
	}
	h.tg.mu.Lock()
	inviteMarkup := h.tg.sent[3]["reply_markup"]
	h.tg.mu.Unlock()
	if !strings.Contains(inviteMarkup, "iy:i-1") || !strings.Contains(inviteMarkup, "in:i-1") {
		t.Fatalf("invite = %s", inviteMarkup)
	}
	if err := h.bot.OrgNotice(ctx, to, food.OrgNotice{Notice: food.NoticeRemoved, Kind: food.OrgCompany, Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if texts := h.tg.sentTexts(); !strings.Contains(texts[4], "Ai fost scos din compania <b>Acme</b>") {
		t.Fatalf("notice = %q", texts[4])
	}

	// somebody who blocked the bot: done, and users knows
	blocked := notify.Recipient{UserID: "u-66", ChatRef: blockedChat}
	if err := h.bot.RecommendationReady(ctx, blocked, contracts.RecommendationReady{Date: "2026-10-29", Picks: []contracts.Pick{{DishID: 1, Name: "Supă"}}}); err != nil {
		t.Fatalf("blocked: %v", err)
	}
	if !slices.Equal(h.people.statuses, []string{blockedChat + " blocked"}) {
		t.Fatalf("statuses = %q", h.people.statuses)
	}

	// a chat that is not a number can never work
	bad := notify.Recipient{UserID: "u-1", ChatRef: "abc"}
	if err := h.bot.RatingRequested(ctx, bad, contracts.RatingRequested{}); !errors.Is(err, streams.ErrDrop) {
		t.Fatalf("bad chat: err = %v", err)
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
