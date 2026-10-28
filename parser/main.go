// Command parser is the "parser" service of food-order: it brings the menu
// from the site and returns it as a list of dishes. No state, no database.
//
// All settings live in internal/config (see .env.example).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"food-order/parser/internal/api"
	"food-order/parser/internal/bistro"
	"food-order/parser/internal/config"
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
	svc := menu.NewService(fetcher, cfg.CacheTTL, cfg.MinInterval)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(svc, cfg.Location, time.Now, log),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("parser listening", "addr", cfg.Addr, "source", cfg.Source)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newFetcher picks the module for the site. Every site has its own package
// (internal/bistro, ...) that implements menu.Fetcher; add a case for a new one.
func newFetcher(cfg config.Config, log *slog.Logger) (menu.Fetcher, error) {
	switch cfg.Source {
	case "bistro":
		return &bistro.Fetcher{
			URLTemplate: cfg.SiteURL,
			UserAgent:   cfg.UserAgent,
			Client:      &http.Client{Timeout: cfg.HTTPTimeout},
			Raw:         menu.RawStore{Dir: cfg.RawDir},
			Log:         log,
		}, nil
	default:
		return nil, fmt.Errorf("%s=%q is unknown (available: bistro)", config.EnvSource, cfg.Source)
	}
}
