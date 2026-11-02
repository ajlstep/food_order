// Command users is the service that knows who the people are: accounts,
// channels (Telegram, ...), email confirmation. It talks only over NATS.
//
// main only starts things: it reads the settings, builds the pieces, joins
// them and waits for the stop signal. The rules are in internal/auth.
//
// Settings: internal/config, see .env.example.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // time zones inside the binary: settings are checked against them, and the image may have none

	"food-order/shared/bus"
	"food-order/shared/rpc"
	"food-order/users/internal/api"
	"food-order/users/internal/applog"
	"food-order/users/internal/auth"
	"food-order/users/internal/config"
	"food-order/users/internal/db"
	"food-order/users/internal/mailer"
	"food-order/users/internal/profile"
)

func main() {
	envFile := flag.String("env", ".env", "path of the .env file (optional)")
	flag.Parse()

	if err := run(*envFile); err != nil {
		fmt.Fprintln(os.Stderr, "users stopped:", err)
		os.Exit(1)
	}
}

func run(envFile string) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}

	log := applog.New(applog.Options{
		Service: "users", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword,
		Additional: cfg.AdditionalLogs, Console: cfg.LogConsole,
	})
	defer log.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		log.Fatal("cannot open the database", "err", err)
		return err
	}
	defer database.Close()

	store := auth.NewSQLStore(database)
	resolver := auth.NewResolver(store, log.Logger, time.Now)
	email := auth.NewEmailService(store, newMailer(cfg), auth.EmailRules{
		CodeTTL: cfg.CodeTTL, Resend: cfg.CodeResend, MaxAttempts: cfg.CodeMaxAttempts, PerHour: cfg.CodesPerHour,
	}, cfg.CodeSecret, log.Logger, time.Now)

	profileSvc := profile.NewService(profile.NewSQLStore(database), log.Logger, time.Now)

	nc, err := bus.Connect(bus.Options{Name: "users", URL: cfg.NATSURL, User: cfg.NATSUser, Password: cfg.NATSPassword}, log.Logger)
	if err != nil {
		log.Fatal("cannot connect to NATS", "err", err)
		return err
	}

	srv := rpc.NewServer(nc, log.Logger, "users")
	srv.ContextHook = func(ctx context.Context, req rpc.Request) context.Context {
		return applog.With(ctx, "request_id", req.RequestID)
	}
	if err := api.Register(srv, log.Logger, auth.NewHandler(resolver, email), profile.NewHandler(profileSvc)); err != nil {
		log.Fatal("cannot register the handlers", "err", err)
		return err
	}
	log.Info("users started", "mail", cfg.MailDriver)

	<-ctx.Done()
	log.Info("users stopping")
	_ = nc.Drain() // answer the requests already received, then close
	email.Wait()   // let the letters that are being sent leave
	return nil
}

// newMailer chooses how letters leave, from the settings.
func newMailer(cfg config.Config) mailer.Mailer {
	if cfg.MailDriver == "smtp" {
		return &mailer.SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Password: cfg.SMTPPassword, From: cfg.MailFrom}
	}
	return &mailer.Console{Out: os.Stdout}
}
