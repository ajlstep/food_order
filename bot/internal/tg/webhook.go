package tg

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/go-telegram/bot"
)

// The only update types the bot handles; Telegram does not send the rest.
var allowedUpdates = []string{"message", "callback_query"}

// How the updates arrive:
//
//	webhook: Telegram makes POST https://<your domain>/tg/webhook for every
//	         message or button press. The bot needs a public https address
//	         (reverse proxy -> bot:8084). Telegram keeps the updates for up to
//	         24 h if the bot is down and sends them when it is back.
//	polling: the bot asks Telegram (getUpdates) in a loop. No public address
//	         needed; handy for local runs. Starting it removes the webhook.

// RunWebhook registers the webhook at Telegram and handles the updates that
// WebhookHandler receives, until ctx is cancelled. If Telegram cannot be
// reached at start, it keeps retrying in the background.
func (b *Bot) RunWebhook(ctx context.Context, publicURL, secret string) {
	b.setCommands(ctx)
	go b.registerWebhook(ctx, publicURL, secret)
	b.api.StartWebhook(ctx)
}

func (b *Bot) registerWebhook(ctx context.Context, publicURL, secret string) {
	wait := 5 * time.Second
	for {
		_, err := b.api.SetWebhook(ctx, &bot.SetWebhookParams{
			URL:            publicURL,
			SecretToken:    secret,
			AllowedUpdates: allowedUpdates,
			MaxConnections: 5,
		})
		if err == nil {
			b.log.Info("webhook registered", "url", publicURL)
			return
		}
		b.log.Error("set webhook failed, retrying", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < 5*time.Minute {
			wait *= 2
		}
	}
}

// RunPolling removes the webhook (Telegram refuses getUpdates while one is
// set) and asks for updates in a loop until ctx is cancelled.
func (b *Bot) RunPolling(ctx context.Context) {
	if _, err := b.api.DeleteWebhook(ctx, &bot.DeleteWebhookParams{}); err != nil {
		b.log.Warn("delete webhook", "err", err)
	}
	b.setCommands(ctx)
	b.api.Start(ctx)
}

// WebhookHandler is the HTTP handler for the webhook path. It accepts only
// POST with the right secret header, so nobody else can send fake updates.
func (b *Bot) WebhookHandler(secret string) http.Handler {
	next := b.api.WebhookHandler()
	want := []byte(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		got := []byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			b.log.Warn("webhook: wrong secret token", "remote", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r) // queues the update; the handlers run in workers
		w.WriteHeader(http.StatusOK)
	})
}
