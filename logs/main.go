// Command logs collects the logs that services publish on NATS
// (logs.info, logs.warn, logs.error, ...) into SQLite and shows them in a
// web page.
//
//	logs collect   listens to logs.> on NATS and writes into the database
//	logs web       web UI (:9084) that reads the same database
//
// Settings: internal/config, see .env.example.
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

	"food-order/logs/internal/collector"
	"food-order/logs/internal/config"
	"food-order/logs/internal/store"
	"food-order/logs/internal/web"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	debug := flag.Bool("debug", false, "log at debug level")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: logs [-env .env] [-debug] collect|web")
		flag.PrintDefaults()
	}
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(log, *envFile, flag.Arg(0)); err != nil {
		log.Error("logs stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, envFile, mode string) error {
	if mode != "collect" && mode != "web" {
		flag.Usage()
		return errors.New("mode must be collect or web")
	}
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if mode == "collect" {
		return collector.Run(ctx, cfg, st, log)
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: web.Handler(st, log), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("logs web listening", "addr", cfg.Addr, "db", cfg.DBPath)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
