// Command catalog is the food service of food-order: dishes, the menu of
// each day, weight, components, nutrition per portion (min/max, with its
// source), allergens, embeddings and the partners who cook. It knows nothing
// about users.
//
// It saves the menu the source sends (food.menu.fetched), asks llm only for
// what is missing (food.dish.analyze.requested, food.dish.embed.requested),
// and tells core when the requested menu is complete (food.menu.ready).
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
	"time"

	"food-order/catalog/internal/api"
	"food-order/catalog/internal/applog"
	"food-order/catalog/internal/config"
	"food-order/catalog/internal/events"
	"food-order/catalog/internal/service"
	"food-order/catalog/internal/store"
	"food-order/shared/bus"
	"food-order/shared/contracts/food"
	sharedevents "food-order/shared/events"
	"food-order/shared/rpc"
	"food-order/shared/streams"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	debug := flag.Bool("debug", false, "send the detailed logs too (same as CATALOG_ADDITIONAL_LOGS=true)")
	flag.Parse()

	cfg, err := config.Load(*envFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err) // no logger yet: the config says where logs go
		os.Exit(1)
	}
	al := applog.New(applog.Options{
		Service: "catalog", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword,
		Additional: cfg.AdditionalLogs || *debug, Console: cfg.LogConsole,
	})
	slog.SetDefault(al.Logger)
	if err := run(al.Logger, cfg); err != nil {
		al.Fatal("catalog stopped", "err", err)
		os.Exit(1)
	}
	al.Info("catalog stopped")
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

	nc, err := bus.Connect(bus.Options{Name: "catalog", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword}, log)
	if err != nil {
		return err
	}
	defer nc.Close()

	pub, err := sharedevents.NewPublisher(nc, log)
	if err != nil {
		return err
	}
	svc := service.New(db, pub, cfg, log)
	if err := svc.LoadPartners(ctx, cfg.PartnersFile); err != nil {
		return err
	}

	srv := rpc.NewServer(nc, log, "catalog")
	srv.ContextHook = func(ctx context.Context, req rpc.Request) context.Context {
		return applog.With(ctx, "req_id", req.RequestID)
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
		tick(ctx, svc, cfg.Tick)
	}()
	go func() {
		defer wg.Done()
		err := streams.Consume(ctx, nc, streams.Food, streams.ConsumerCatalog, events.New(svc).Handle, log,
			streams.OnStuck(func(subject string, err error) {
				_ = pub.Publish(context.Background(), food.SubjectAlert, food.Alert{
					Service: "catalog", Level: "error", Text: "Un eveniment " + subject + " a eșuat de prea multe ori: " + err.Error(),
				})
			}))
		if err != nil {
			failed <- err
		}
	}()
	log.Info("catalog started", "db", cfg.DBPath, "analysis", cfg.AnalysisEnabled, "embeddings", cfg.EmbedEnabled,
		"analysis_timeout", cfg.AnalysisTimeout.String(), "additional_logs", cfg.AdditionalLogs, "tick", cfg.Tick)

	select {
	case err = <-failed:
	case <-ctx.Done():
	}
	stop()
	_ = nc.Drain() // answer the requests already received
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
