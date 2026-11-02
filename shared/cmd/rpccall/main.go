// Command rpccall sends one request to a service and prints the answer. It
// is for trying a service by hand, the way the gateway will call it.
//
//	go run ./cmd/rpccall -subject rpc.users.ping
//	go run ./cmd/rpccall -subject rpc.users.auth.resolve -data '{"channel":"telegram","external_id":"1"}'
//	go run ./cmd/rpccall -for USER_ID -subject rpc.users.profile.get
//
// -for puts a user_id in the envelope: the services that work on a person's
// data (the profile zone of users) take the person from there.
//
// It connects as the gateway user by default; the password is in nuts/.env
// (NATS_PASSWORD_GATEWAY), pass it with -password or NATS_PASSWORD.
// From outside Docker the server is at nats://127.0.0.1:4222.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"food-order/shared/bus"
	"food-order/shared/rpc"
)

func main() {
	url := flag.String("url", "nats://127.0.0.1:4222", "NATS server")
	user := flag.String("user", "gateway", "NATS user")
	password := flag.String("password", os.Getenv("NATS_PASSWORD"), "NATS password (or NATS_PASSWORD)")
	subject := flag.String("subject", "", "rpc subject, for example rpc.users.ping")
	data := flag.String("data", "", "JSON data of the request")
	forUser := flag.String("for", "", "user_id of the person the request is for (goes in the envelope)")
	flag.Parse()

	if *subject == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*url, *user, *password, *subject, *data, *forUser); err != nil {
		fmt.Fprintln(os.Stderr, "rpccall:", err)
		os.Exit(1)
	}
}

func run(url, user, password, subject, data, forUser string) error {
	nc, err := bus.Connect(bus.Options{Name: "rpccall", URL: url, User: user, Password: password}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		return err
	}
	defer nc.Close()

	req := rpc.Request{V: rpc.Version, RequestID: fmt.Sprintf("rpccall-%d", time.Now().UnixNano()), UserID: forUser}
	if data != "" {
		req.Data = json.RawMessage(data)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("the -data is not valid JSON: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := nc.RequestWithContext(ctx, subject, raw)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, msg.Data, "", "  "); err != nil {
		pretty.Write(msg.Data)
	}
	fmt.Println(pretty.String())
	return nil
}
