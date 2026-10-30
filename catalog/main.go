// Command catalog is the food service of food-order: dishes, the menu of
// each day, weight, components, nutrition per portion (min/max, with its
// source), allergens and embeddings. It knows nothing about users.
//
// It saves the menu the source sends (food.menu.fetched), asks llm only for
// what is missing (food.dish.analyze.requested, food.dish.embed.requested),
// and tells core when the requested menu is complete (food.menu.ready).
// HTTP (:8082) for core and bot. All settings live in internal/config
// (see .env.example).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"food-order/catalog/internal/api"
	"food-order/catalog/internal/bus"
	"food-order/catalog/internal/config"
	"food-order/catalog/internal/contracts"
	"food-order/catalog/internal/events"
	"food-order/catalog/internal/service"
	"food-order/catalog/internal/store"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	debug := flag.Bool("debug", false, "log at debug level")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(log, *envFile); err != nil {
		log.Error("catalog stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, envFile string) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}

	// the port first: if it is taken, stop before touching the database or the broker
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("%s=%s is already in use (another catalog, or the parser on the same port?): %w", config.EnvAddr, cfg.Addr, err)
	}
	defer ln.Close()

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
			Stream: cfg.NATSStream, Durable: cfg.NATSDurable,
		}, log)
		if err != nil {
			return err
		}
		defer b.Close()
		pub = b
	} else {
		// nobody could answer: llm is reached only through the broker
		log.Warn("CATALOG_NATS_URL is empty: no broker; llm is never asked, menus come only through POST /v1/import")
		cfg.AnalysisEnabled, cfg.EmbedEnabled = false, false
	}

	svc := service.New(db, pub, cfg, log)
	errc := make(chan error, 2)

	// every goroutine that uses the database is in wg: it is waited for
	// before the database closes (the defers above)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer stop() // runs before wg.Wait
	startTick := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick(ctx, svc, cfg.Tick)
		}()
	}

	if b != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := events.New(svc)
			err := b.Consume(ctx, contracts.ListenSubjects, h.Handle,
				startTick, // the stream exists: publishing works now
				func(subject string, err error) {
					_ = pub.Publish(context.Background(), contracts.SubjectAlert, contracts.Alert{
						Service: "catalog", Level: "error", Text: "Un eveniment " + subject + " a eșuat de prea multe ori: " + err.Error(),
					})
				})
			if err != nil && !errors.Is(err, context.Canceled) {
				errc <- err
			}
		}()
	} else {
		startTick()
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(svc, db, cfg.ImportEnabled, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	log.Info("catalog started", "addr", cfg.Addr, "db", cfg.DBPath, "nats", cfg.NATSURL,
		"analysis", cfg.AnalysisEnabled, "embeddings", cfg.EmbedEnabled, "analysis_timeout", cfg.AnalysisTimeout.String(),
		"import", cfg.ImportEnabled)

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

// tick runs the periodic work (llm timeouts, failed publishes) until ctx ends.
func tick(ctx context.Context, svc *service.Service, every time.Duration) {
	svc.Tick(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			svc.Tick(ctx)
		}
	}
}
