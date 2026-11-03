// Command eventpub publishes one food.* event, the way core does. It is for
// trying the notifications by hand, without core:
//
//	go run ./cmd/eventpub -password PAROLA -kind recommendation -for USER_ID
//	go run ./cmd/eventpub -password PAROLA -kind rating -for USER_ID
//	go run ./cmd/eventpub -password PAROLA -kind alert
//
// It connects as admin; the password is in nuts/.env (NATS_PASSWORD_ADMIN),
// pass it with -password or NATS_PASSWORD. From outside Docker the server is
// at nats://127.0.0.1:4222.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"food-order/shared/bus"
	"food-order/shared/contracts/food"
	"food-order/shared/events"
)

func main() {
	url := flag.String("url", "nats://127.0.0.1:4222", "NATS server")
	password := flag.String("password", os.Getenv("NATS_PASSWORD"), "password of the admin user (or NATS_PASSWORD)")
	kind := flag.String("kind", "", "recommendation, rating or alert")
	forUser := flag.String("for", "", "user_id of the person")
	flag.Parse()

	subject, data, err := sample(*kind, *forUser)
	if err != nil {
		fmt.Fprintln(os.Stderr, "eventpub:", err)
		os.Exit(2)
	}
	if err := publish(*url, *password, subject, data); err != nil {
		fmt.Fprintln(os.Stderr, "eventpub:", err)
		os.Exit(1)
	}
	fmt.Println("published", subject)
}

// sample makes the data of an event of the given kind.
func sample(kind, userID string) (string, any, error) {
	if kind != "alert" && userID == "" {
		return "", nil, fmt.Errorf("-for USER_ID is required for %q", kind)
	}
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	switch kind {
	case "recommendation":
		return food.SubjectRecommendationReady, map[string]any{
			"user_id": userID, "date": tomorrow,
			"picks": []map[string]any{
				{"dish_id": 1, "name": "Ciorbă de pui", "price": 45, "reason": "probă, din eventpub"},
				{"dish_id": 2, "name": "Pârjoale cu piure", "price": 52.5},
			},
		}, nil
	case "rating":
		return food.SubjectRatingRequested, map[string]any{"user_id": userID, "order_id": 1}, nil
	case "alert":
		return food.SubjectAlert, map[string]any{"service": "eventpub", "level": "warn", "text": "alertă de probă"}, nil
	}
	return "", nil, fmt.Errorf("-kind must be recommendation, rating or alert, not %q", kind)
}

func publish(url, password, subject string, data any) error {
	nc, err := bus.Connect(bus.Options{Name: "eventpub", URL: url, User: "admin", Password: password}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		return err
	}
	defer nc.Close()
	pub, err := events.NewPublisher(nc, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return pub.Publish(ctx, subject, data)
}
