// Command gateway is the way into food-order. It takes HTTP from the bot
// (later also from the web and the app), checks who calls and for whom, and
// turns every request into a NATS request to the right service. It holds no
// business data; the other services cannot be reached from outside.
//
// main only starts things: it reads the settings, builds the pieces, joins
// them and waits for the stop signal. The order of the checks is in
// internal/access, the routes in internal/api.
//
// Settings: internal/config, see .env.example.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"food-order/gateway/internal/access"
	"food-order/gateway/internal/api"
	"food-order/gateway/internal/applog"
	"food-order/gateway/internal/config"
	"food-order/gateway/internal/forward"
	"food-order/shared/bus"
	"food-order/shared/rpc"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	flag.Parse()

	if err := run(*envFile); err != nil {
		fmt.Fprintln(os.Stderr, "gateway stopped:", err)
		os.Exit(1)
	}
}

func run(envFile string) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}

	log := applog.New(applog.Options{
		Service: "gateway", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword,
		Additional: cfg.AdditionalLogs, Console: cfg.LogConsole,
	})
	defer log.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	nc, err := bus.Connect(bus.Options{Name: "gateway", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword}, log.Logger)
	if err != nil {
		log.Fatal("cannot connect to NATS", "err", err)
		return err
	}
	defer nc.Close()

	client := rpc.NewClient(nc)
	acc := access.New(cfg.BotKey, access.NewRPCResolver(client, cfg.RPCTimeout), log.Logger)
	fwd := forward.New(client, cfg.RPCTimeout, log.Logger)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(acc, fwd, nc.IsConnected),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.RPCTimeout + 10*time.Second, // longer than the longest wait for a service
		IdleTimeout:       60 * time.Second,
	}

	failed := make(chan error, 1)
	go func() { failed <- srv.ListenAndServe() }()
	log.Info("gateway started", "addr", cfg.HTTPAddr)

	select {
	case err := <-failed:
		log.Fatal("the HTTP server stopped", "err", err)
		return err
	case <-ctx.Done():
	}

	log.Info("gateway stopping")
	// finish the requests already started, at most 10 seconds
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("cannot stop the HTTP server cleanly", "err", err)
	}
	return nil
}
