// Command parser is the "parser" service of food-order: it brings the menu
// from the site and returns it as a list of dishes. No state, no database.
//
// Normal flow (NATS): core publishes food.menu.requested, the parser
// answers with food.menu.fetched (to catalog) or food.menu.error (to core).
// HTTP (GET /v1/fetch) stays for debugging.
//
// All settings live in internal/config (see .env.example).
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

	"food-order/parser/internal/api"
	"food-order/parser/internal/bistro"
	"food-order/parser/internal/bus"
	"food-order/parser/internal/config"
	"food-order/parser/internal/contracts"
	"food-order/parser/internal/events"
	"food-order/parser/internal/menu"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, *envFile); err != nil {
		log.Error("parser stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, envFile string) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}
	fetcher, err := newFetcher(cfg, log)
	if err != nil {
		return err
	}
	// one Service for HTTP and NATS: the same cache and the same pause between requests to the site
	svc := menu.NewService(fetcher, cfg.CacheTTL, cfg.MinInterval)

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("%s=%s is already in use (catalog is on 9082): %w", config.EnvAddr, cfg.Addr, err)
	}
	defer ln.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 2)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer stop() // runs before wg.Wait

	if cfg.NATSEnabled {
		b, err := bus.Connect(bus.Config{
			URL: cfg.NATSURL, Token: cfg.NATSToken, User: cfg.NATSUser, Password: cfg.NATSPassword,
			Stream: cfg.NATSStream, Durable: cfg.NATSDurable,
		}, log)
		if err != nil {
			return err
		}
		defer b.Close()
		h := events.New(svc, b, cfg.Location, log)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := b.Consume(ctx, contracts.ListenSubjects, h.Handle, nil, func(subject string, err error) {
				_ = b.Publish(context.Background(), contracts.SubjectAlert, contracts.Alert{
					Service: "parser", Level: "error", Text: "Răspunsul la " + subject + " nu a putut fi trimis: " + err.Error(),
				})
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				errc <- err
			}
		}()
	} else {
		log.Warn(config.EnvNATSEnabled + "=false: HTTP only, nobody will answer core's menu requests")
	}

	srv := &http.Server{
		Handler:           api.New(svc, cfg.Location, time.Now, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	log.Info("parser listening", "addr", cfg.Addr, "source", cfg.Source, "nats", cfg.NATSEnabled, "nats_url", cfg.NATSURL)

	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return err
}

// newFetcher picks the module for the site. Every site has its own package
// (internal/bistro, ...) that implements menu.Fetcher; add a case for a new one.
func newFetcher(cfg config.Config, log *slog.Logger) (menu.Fetcher, error) {
	switch cfg.Source {
	case "bistro":
		return &bistro.Fetcher{
			URLTemplate:     cfg.SiteURL,
			WeekURLTemplate: cfg.SiteWeekURL,
			UserAgent:       cfg.UserAgent,
			Client:          &http.Client{Timeout: cfg.HTTPTimeout},
			Raw:             menu.RawStore{Dir: cfg.RawDir},
			Log:             log,
		}, nil
	default:
		return nil, fmt.Errorf("%s=%q is unknown (available: bistro)", config.EnvSource, cfg.Source)
	}
}
