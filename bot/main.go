// Command bot is the Telegram service of food-order: the interface between
// the users and the other services. It owns the users (Telegram chat, name,
// nickname, phone) and their settings (order time, order days, ...) in its
// own SQLite file, bot.db. Core only knows users.id. Food business logic
// stays in core: the bot shows recommendations, sends choices and ratings
// to core, asks for the recommendation at each user's order time, and
// forwards the events (recommendation ready, rating question) to the user
// and alerts to the admin chat.
//
// All settings live in internal/config (see .env.example).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // Europe/Chisinau also works in a minimal Docker image

	"food-order/bot/internal/config"
	"food-order/bot/internal/events"
	"food-order/bot/internal/scheduler"
	"food-order/bot/internal/services"
	"food-order/bot/internal/store"
	"food-order/bot/internal/tg"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, *envFile); err != nil {
		log.Error("bot stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, envFile string) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	hc := &http.Client{Timeout: cfg.HTTPTimeout}
	core := services.NewCore(cfg.CoreURL, hc)
	b, err := tg.New(tg.Options{
		Token:        cfg.TelegramToken,
		AdminChatID:  cfg.AdminChatID,
		AllowedChats: cfg.AllowedChats,
		Users:        db,
		Core:         core,
		Catalog:      services.NewCatalog(cfg.CatalogURL, hc),
		Log:          log,
	})
	if err != nil {
		return err
	}

	// at every user's order time, on its order days: ask core for the recommendation
	sched := &scheduler.Scheduler{Store: db, Core: core, Notify: b.NotifyUser, Log: log}
	go sched.Run(ctx)

	errc := make(chan error, 2)

	// One HTTP server: /healthz for Docker and, in webhook mode, the path
	// Telegram calls. Only the webhook path should be exposed by the proxy.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	if cfg.Mode == config.ModeWebhook {
		mux.Handle(cfg.WebhookPath, b.WebhookHandler(cfg.WebhookSecret))
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	// events from NATS (optional: without BOT_NATS_URL only the commands work)
	if cfg.NATSURL != "" {
		go func() {
			err := events.Run(ctx, events.ConsumerConfig{
				URL:      cfg.NATSURL,
				Token:    cfg.NATSToken,
				User:     cfg.NATSUser,
				Password: cfg.NATSPassword,
				Stream:   cfg.NATSStream,
				Durable:  cfg.NATSDurable,
			}, events.NewHandler(b), log)
			if err != nil && !errors.Is(err, context.Canceled) {
				errc <- err
			}
		}()
	} else {
		log.Warn("BOT_NATS_URL is empty: events are off, only commands work")
	}

	// both return when ctx is cancelled
	if cfg.Mode == config.ModeWebhook {
		go b.RunWebhook(ctx, cfg.WebhookURL, cfg.WebhookSecret)
		log.Info("bot started (webhook)", "db", cfg.DBPath, "addr", cfg.Addr, "path", cfg.WebhookPath, "public_url", cfg.WebhookURL,
			"core", cfg.CoreURL, "catalog", cfg.CatalogURL, "nats", cfg.NATSURL)
	} else {
		go b.RunPolling(ctx)
		log.Info("bot started (polling)", "db", cfg.DBPath, "addr", cfg.Addr, "core", cfg.CoreURL, "catalog", cfg.CatalogURL, "nats", cfg.NATSURL)
	}

	select {
	case err = <-errc:
		stop()
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return err
}
