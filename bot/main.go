// Command bot is the Telegram service of food-order: the interface between
// people and the other services. It keeps no data: everything it asks goes
// through the gateway. From NATS it reads only its own queue of messages for
// people (notify.telegram).
//
// main only starts things: it reads the settings, builds the pieces, joins
// them and waits for the stop signal.
//
// All settings live in internal/config (see .env.example).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // Europe/Chisinau also works in a minimal Docker image

	"food-order/bot/internal/applog"
	"food-order/bot/internal/config"
	"food-order/bot/internal/notify"
	"food-order/bot/internal/services"
	"food-order/bot/internal/tg"
	"food-order/shared/bus"
	"food-order/shared/streams"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	debug := flag.Bool("debug", false, "send the detailed logs too (same as BOT_ADDITIONAL_LOGS=true)")
	flag.Parse()

	cfg, err := config.Load(*envFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bot:", err) // no logger yet: the config says where logs go
		os.Exit(1)
	}
	al := applog.New(applog.Options{
		Service: "bot", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword,
		Additional: cfg.AdditionalLogs || *debug, Console: cfg.LogConsole,
	})
	slog.SetDefault(al.Logger)
	if err := run(al.Logger, cfg); err != nil {
		al.Fatal("bot stopped", "err", err)
		os.Exit(1)
	}
	al.Info("bot stopped")
	al.Close()
}

func run(log *slog.Logger, cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hc := &http.Client{Timeout: cfg.HTTPTimeout}
	b, err := tg.New(tg.Options{
		Token:        cfg.TelegramToken,
		ServerURL:    cfg.TelegramAPIURL,
		AdminChatID:  cfg.AdminChatID,
		AllowedChats: cfg.AllowedChats,
		Users:        services.NewGateway(cfg.GatewayURL, cfg.GatewayKey, hc, log),
		Log:          log,
	})
	if err != nil {
		return err
	}

	failed := make(chan error, 2)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           b.HTTPHandler(cfg.WebhookPath, cfg.WebhookSecret),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	if cfg.NATSURL == "" {
		log.Warn("BOT_NATS_URL is empty: no notifications, only commands work")
	} else {
		nc, err := bus.Connect(bus.Options{Name: "bot", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword}, log)
		if err != nil {
			return err
		}
		defer nc.Close()
		queue := notify.NewHandler(b, log)
		go func() {
			if err := streams.Consume(ctx, nc, streams.Notify, streams.ConsumerBotTelegram, queue.Handle, log); err != nil {
				failed <- err
			}
		}()
	}

	if cfg.Mode == config.ModeWebhook {
		go b.RunWebhook(ctx, cfg.WebhookURL, cfg.WebhookSecret)
	} else {
		go b.RunPolling(ctx)
	}
	log.Info("bot started", "mode", cfg.Mode, "addr", cfg.Addr, "gateway", cfg.GatewayURL,
		"additional_logs", cfg.AdditionalLogs, "admin_chat", cfg.AdminChatID != 0, "allowed_chats", len(cfg.AllowedChats))

	select {
	case err = <-failed:
		stop()
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return err
}
