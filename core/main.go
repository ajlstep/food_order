// Command core is the business-logic service of food-order: per user_id
// (users.id from the bot's database) it keeps orders with a snapshot of the
// dish, ratings, comments, labels and food preferences; it asks for the
// shared menu, scores the dishes, orchestrates the recommendation
// (catalog -> score -> llm -> bot) and computes the statistics.
//
// HTTP (:9081) for the bot; NATS JetStream for the menu, llm and events.
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

	"food-order/core/internal/api"
	"food-order/core/internal/bus"
	"food-order/core/internal/catalog"
	"food-order/core/internal/config"
	"food-order/core/internal/contracts"
	"food-order/core/internal/events"
	"food-order/core/internal/scheduler"
	"food-order/core/internal/service"
	"food-order/core/internal/store"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	debug := flag.Bool("debug", false, "log at debug level (e.g. why dishes were excluded)")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(log, *envFile); err != nil {
		log.Error("core stopped", "err", err)
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

	var pub bus.Publisher = bus.Off{}
	var b *bus.Bus
	if cfg.NATSURL != "" {
		b, err = bus.Connect(bus.Config{
			URL: cfg.NATSURL, Token: cfg.NATSToken, User: cfg.NATSUser, Password: cfg.NATSPassword,
			Stream: cfg.NATSStream, Durable: cfg.NATSDurable, MaxAge: cfg.NATSMaxAge,
		}, log)
		if err != nil {
			return err
		}
		defer b.Close()
		pub = b
	} else {
		log.Warn("CORE_NATS_URL is empty: no broker; menu requests and llm are off, picks come from the score")
	}

	svc := service.New(db, catalog.New(cfg.CatalogURL, &http.Client{Timeout: cfg.HTTPTimeout}), pub, cfg, log)
	errc := make(chan error, 2)

	if b != nil {
		go func() {
			if err := b.EnsureStream(ctx); err != nil {
				return // ctx ended
			}
			// the scheduler starts once the stream exists, so its first
			// menu request is not published into nothing
			go scheduler.Run(ctx, svc, cfg.Tick)
			h := events.New(svc)
			err := b.Consume(ctx, contracts.ListenSubjects, h.Handle, func(subject string, err error) {
				_ = pub.Publish(context.Background(), contracts.SubjectAlert, contracts.Alert{
					Service: "core", Level: "error", Text: "Un eveniment " + subject + " a eșuat de prea multe ori: " + err.Error(),
				})
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				errc <- err
			}
		}()
	}

	if b == nil {
		go scheduler.Run(ctx, svc, cfg.Tick)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(svc, db, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	log.Info("core started", "addr", cfg.Addr, "db", cfg.DBPath, "catalog", cfg.CatalogURL, "nats", cfg.NATSURL,
		"menu_request_time", cfg.MenuRequestTime.String(), "llm", cfg.LLMEnabled, "tz", cfg.Location.String())

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
