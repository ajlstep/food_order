// Command core is the business-logic service of food-order: per user_id (from
// the users service) it keeps orders with a snapshot of the dish and of where
// the order goes, ratings, comments and labels; it asks for the shared menu,
// scores the dishes, orchestrates the recommendation (catalog -> score -> llm
// -> person) and computes the statistics.
//
// It talks only over NATS: requests (internal/api), events (internal/events).
//
// main only starts things: it reads the settings, builds the pieces, joins
// them and waits for the stop signal. All settings live in internal/config
// (see .env.example).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"food-order/core/internal/api"
	"food-order/core/internal/applog"
	"food-order/core/internal/config"
	"food-order/core/internal/events"
	"food-order/core/internal/remote"
	"food-order/core/internal/scheduler"
	"food-order/core/internal/service"
	"food-order/core/internal/store"
	"food-order/shared/bus"
	"food-order/shared/contracts/food"
	sharedevents "food-order/shared/events"
	"food-order/shared/rpc"
	"food-order/shared/streams"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	debug := flag.Bool("debug", false, "send the detailed logs too (same as CORE_ADDITIONAL_LOGS=true)")
	flag.Parse()

	cfg, err := config.Load(*envFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "core:", err) // no logger yet: the config says where logs go
		os.Exit(1)
	}
	al := applog.New(applog.Options{
		Service: "core", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword,
		Additional: cfg.AdditionalLogs || *debug, Console: cfg.LogConsole,
	})
	slog.SetDefault(al.Logger)
	if err := run(al.Logger, cfg); err != nil {
		al.Fatal("core stopped", "err", err)
		os.Exit(1)
	}
	al.Info("core stopped")
	al.Close()
}

func run(log *slog.Logger, cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	nc, err := bus.Connect(bus.Options{Name: "core", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword}, log)
	if err != nil {
		return err
	}
	defer nc.Close()

	pub, err := sharedevents.NewPublisher(nc, log)
	if err != nil {
		return err
	}
	others := remote.New(rpc.NewClient(nc), cfg.RPCTimeout)
	svc := service.New(db, others, others, pub, cfg, log)

	srv := rpc.NewServer(nc, log, "core")
	srv.ContextHook = func(ctx context.Context, req rpc.Request) context.Context {
		return applog.With(ctx, "req_id", req.RequestID, "user_id", req.UserID)
	}
	if err := api.New(svc).Register(srv, log); err != nil {
		return err
	}

	// the database closes after these are done
	var wg sync.WaitGroup
	defer wg.Wait()
	defer stop() // runs before wg.Wait

	failed := make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		scheduler.Run(ctx, svc, cfg.Tick)
	}()
	go func() {
		defer wg.Done()
		err := streams.Consume(ctx, nc, streams.Food, streams.ConsumerCore, events.New(svc).Handle, log,
			streams.OnStuck(func(subject string, err error) {
				_ = pub.Publish(context.Background(), food.SubjectAlert, food.Alert{
					Service: "core", Level: "error", Text: "Un eveniment " + subject + " a eșuat de prea multe ori: " + err.Error(),
				})
			}))
		if err != nil {
			failed <- err
		}
	}()
	log.Info("core started", "db", cfg.DBPath, "menu_request_time", cfg.MenuRequestTime.String(), "llm", cfg.LLMEnabled,
		"tz", cfg.Location.String(), "additional_logs", cfg.AdditionalLogs, "tick", cfg.Tick)

	select {
	case err = <-failed:
	case <-ctx.Done():
	}
	stop()
	_ = nc.Drain() // answer the requests already received
	return err
}
