package tg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"food-order/bot/internal/store"
)

// recordingTelegram wraps fakeTelegram and remembers the setWebhook body.
type recordingTelegram struct {
	*fakeTelegram
	mu         sync.Mutex
	setWebhook string
}

func (r *recordingTelegram) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if strings.HasSuffix(req.URL.Path, "/setWebhook") {
		_ = req.ParseMultipartForm(1 << 20)
		var parts []string
		if req.MultipartForm != nil {
			for k, v := range req.MultipartForm.Value {
				parts = append(parts, k+"="+v[0])
			}
		}
		r.mu.Lock()
		r.setWebhook = strings.Join(parts, "&")
		r.mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":true}`)
		return
	}
	r.fakeTelegram.ServeHTTP(w, req)
}

func (r *recordingTelegram) webhookBody() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.setWebhook
}

func TestWebhookEndToEnd(t *testing.T) {
	const secret = "s3cret_s3cret_s3cret"
	tgFake := &recordingTelegram{fakeTelegram: &fakeTelegram{}}
	tgSrv := httptest.NewServer(tgFake)
	defer tgSrv.Close()

	core := &coreRecorder{}
	coreSrv := httptest.NewServer(core)
	defer coreSrv.Close()
	b, db := newTestBot(t, tgSrv.URL, coreSrv.URL, nil)
	u, _, err := db.Register(context.Background(), store.Profile{ChatID: 42, TGUserID: 42})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.RunWebhook(ctx, "https://ajl.duckdns.org/tg/webhook", secret)

	// the bot registers itself at Telegram with the URL and the secret
	waitFor(t, func() bool { return tgFake.webhookBody() != "" })
	wh := tgFake.webhookBody()
	for _, want := range []string{"url=https://ajl.duckdns.org/tg/webhook", "secret_token=" + secret, "callback_query"} {
		if !strings.Contains(wh, want) {
			t.Errorf("setWebhook has no %q: %s", want, wh)
		}
	}

	// Telegram -> our webhook
	hook := httptest.NewServer(b.WebhookHandler(secret))
	defer hook.Close()
	post := func(sec, body string) int {
		req, _ := http.NewRequest(http.MethodPost, hook.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if sec != "" {
			req.Header.Set("X-Telegram-Bot-Api-Secret-Token", sec)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("", update(1, 42, "/meniu")); code != http.StatusUnauthorized {
		t.Fatalf("no secret: status %d, want 401", code)
	}
	if code := post("wrong_wrong_wrong_wrong", update(2, 42, "/meniu")); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: status %d, want 401", code)
	}
	if resp, _ := http.Get(hook.URL); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", resp.StatusCode)
	}
	if code := post(secret, update(3, 42, "/meniu")); code != http.StatusOK {
		t.Fatalf("good secret: status %d", code)
	}

	waitFor(t, func() bool { return len(tgFake.sentTexts()) >= 1 })
	calls := core.get()
	if len(calls) != 1 || calls[0] != fmt.Sprintf(`%d POST /v1/recommendations {"date":"2026-10-29"}`, u.ID) {
		t.Fatalf("core calls = %q (the updates with a bad secret must be ignored)", calls)
	}
	if got := tgFake.sentTexts()[0]; !strings.Contains(got, "Am cerut recomandarea") {
		t.Fatalf("reply = %q", got)
	}
}
