package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	users "food-order/shared/contracts/users"
	"food-order/shared/rpc"
	"food-order/users/internal/auth"
	"food-order/users/internal/db"
	"food-order/users/internal/mailer"
	"food-order/users/internal/org"
	"food-order/users/internal/profile"
)

// The whole service over a real NATS server: what the gateway will do.
type env struct {
	client *rpc.Client
	mail   *mailer.Fake
	email  *auth.EmailService
}

// noEvents is a publisher that drops everything.
type noEvents struct{}

func (noEvents) Publish(context.Context, string, any) error { return nil }

func start(t *testing.T) *env {
	t.Helper()
	srv, err := server.NewServer(&server.Options{Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats did not start")
	}
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)

	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := auth.NewSQLStore(database)
	mail := &mailer.Fake{}
	email := auth.NewEmailService(store, mail, auth.EmailRules{CodeTTL: 30 * time.Minute, Resend: 30 * time.Second, MaxAttempts: 5, PerHour: 5},
		"test-secret-0123456789", log, time.Now)
	if err := Register(rpc.NewServer(nc, log, "users"), log, auth.NewHandler(auth.NewResolver(store, log, time.Now), email), profile.NewHandler(profile.NewService(profile.NewSQLStore(database), log, time.Now)), org.NewHandler(org.NewService(org.NewSQLStore(database), noEvents{}, log, time.Now))); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	return &env{client: rpc.NewClient(nc), mail: mail, email: email}
}

func (e *env) call(subject string, in, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.client.Call(ctx, subject, "req-test", in, out)
}

func wantRPC(t *testing.T, err error, code string) *rpc.Error {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("expected error %q, got %v", code, err)
	}
	return re
}

func TestPing(t *testing.T) {
	e := start(t)
	var out map[string]string
	if err := e.call(users.SubjectPing, nil, &out); err != nil || out["service"] != "users" {
		t.Fatalf("ping: %v %v", out, err)
	}
}

// The registration as the bot will live it: /start, email, code.
func TestRegistrationFromStartToActive(t *testing.T) {
	e := start(t)

	var who users.ResolveResponse
	err := e.call(users.SubjectAuthResolve, users.ResolveRequest{Channel: "telegram", ExternalID: "42", Nickname: "ana", Language: "ru"}, &who)
	if err != nil || !who.IsNew || who.Status != users.StatusPending || who.Language != "ru" {
		t.Fatalf("resolve: %+v %v", who, err)
	}

	var sent users.EmailRequestResponse
	if err := e.call(users.SubjectAuthEmailRequest, users.EmailRequestRequest{UserID: who.UserID, Email: "ana@example.com"}, &sent); err != nil {
		t.Fatal(err)
	}
	if !sent.Sent || sent.ValidForSecond != 1800 {
		t.Fatalf("request: %+v", sent)
	}
	e.email.Wait()
	letters := e.mail.Sent()
	if len(letters) != 1 {
		t.Fatalf("letters: %d", len(letters))
	}
	code := between(letters[0].Body, "confirmare: ", "\n")

	// a wrong code first: the answer carries attempts_left
	err = e.call(users.SubjectAuthEmailConfirm, users.EmailConfirmRequest{UserID: who.UserID, Code: "ZZZZ"}, nil)
	re := wantRPC(t, err, users.ErrWrongCode)
	if re.Details["attempts_left"] != float64(4) { // numbers come back from JSON as float64
		t.Fatalf("details: %v", re.Details)
	}

	var done users.EmailConfirmResponse
	if err := e.call(users.SubjectAuthEmailConfirm, users.EmailConfirmRequest{UserID: who.UserID, Code: code}, &done); err != nil {
		t.Fatal(err)
	}
	if done.Result != users.ConfirmConfirmed || done.UserID != who.UserID {
		t.Fatalf("confirm: %+v", done)
	}

	// the next message of the same person: active now
	err = e.call(users.SubjectAuthResolve, users.ResolveRequest{Channel: "telegram", ExternalID: "42"}, &who)
	if err != nil || who.IsNew || who.Status != users.StatusActive {
		t.Fatalf("resolve after confirmation: %+v %v", who, err)
	}
}

func TestRefusalsKeepTheirCodesAndDetails(t *testing.T) {
	e := start(t)
	var who users.ResolveResponse
	e.call(users.SubjectAuthResolve, users.ResolveRequest{Channel: "telegram", ExternalID: "42"}, &who)

	wantRPC(t, e.call(users.SubjectAuthEmailRequest, users.EmailRequestRequest{UserID: who.UserID, Email: "nope"}, nil), users.ErrInvalidEmail)
	wantRPC(t, e.call(users.SubjectAuthEmailRequest, users.EmailRequestRequest{UserID: "ghost", Email: "a@example.com"}, nil), rpc.CodeNotFound)

	e.call(users.SubjectAuthEmailRequest, users.EmailRequestRequest{UserID: who.UserID, Email: "a@example.com"}, nil)
	re := wantRPC(t, e.call(users.SubjectAuthEmailRequest, users.EmailRequestRequest{UserID: who.UserID, Email: "a@example.com"}, nil), users.ErrCooldown)
	if re.Details["retry_after"] != float64(30) {
		t.Fatalf("retry_after: %v", re.Details)
	}
}

func TestBadMessages(t *testing.T) {
	e := start(t)
	// no data at all
	wantRPC(t, e.call(users.SubjectAuthResolve, nil, nil), rpc.CodeBadRequest)
	// wrong type of a field
	wantRPC(t, e.call(users.SubjectAuthResolve, map[string]any{"channel": 5}, nil), rpc.CodeBadRequest)
}

func TestIdentityStatus(t *testing.T) {
	e := start(t)
	wantRPC(t, e.call(users.SubjectAuthIdentityStatus, users.IdentityStatusRequest{Channel: "telegram", ExternalID: "42", Status: users.IdentityBlocked}, nil), rpc.CodeNotFound)
	e.call(users.SubjectAuthResolve, users.ResolveRequest{Channel: "telegram", ExternalID: "42"}, nil)
	if err := e.call(users.SubjectAuthIdentityStatus, users.IdentityStatusRequest{Channel: "telegram", ExternalID: "42", Status: users.IdentityBlocked}, nil); err != nil {
		t.Fatal(err)
	}
}

func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	v, _, _ := strings.Cut(rest, to)
	return v
}

// ---------------------------------------------------------------- profile zone

// registered goes through /start, email and code, and returns an active account.
func (e *env) registered(t *testing.T, externalID, email string) string {
	t.Helper()
	var who users.ResolveResponse
	if err := e.call(users.SubjectAuthResolve, users.ResolveRequest{Channel: "telegram", ExternalID: externalID}, &who); err != nil {
		t.Fatal(err)
	}
	if err := e.call(users.SubjectAuthEmailRequest, users.EmailRequestRequest{UserID: who.UserID, Email: email}, nil); err != nil {
		t.Fatal(err)
	}
	e.email.Wait()
	letters := e.mail.Sent()
	code := between(letters[len(letters)-1].Body, "confirmare: ", "\n")
	if err := e.call(users.SubjectAuthEmailConfirm, users.EmailConfirmRequest{UserID: who.UserID, Code: code}, nil); err != nil {
		t.Fatal(err)
	}
	return who.UserID
}

// callFor is a call about one person, as the gateway makes it.
func (e *env) callFor(userID, subject string, in, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.client.CallFor(ctx, subject, "req-test", userID, in, out)
}

func TestProfileAfterRegistration(t *testing.T) {
	e := start(t)
	id := e.registered(t, "42", "ana@example.com")

	var p users.ProfileResponse
	if err := e.callFor(id, users.SubjectProfileGet, nil, &p); err != nil {
		t.Fatal(err)
	}
	if p.UserID != id || p.Email != "ana@example.com" || p.Status != users.StatusActive || p.Language != "ro" {
		t.Fatalf("profile: %+v", p)
	}

	var s users.SettingsResponse
	err := e.callFor(id, users.SubjectProfileSettingsSet, users.SettingsSetRequest{Values: map[string]string{"order_time": "09:30"}}, &s)
	if err != nil || s.Settings["order_time"] != "09:30" || s.Settings["rec_for"] != "tomorrow" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	re := wantRPC(t, e.callFor(id, users.SubjectProfileSettingsSet, users.SettingsSetRequest{Values: map[string]string{"order_time": "late"}}, nil), users.ErrInvalidInput)
	if re.Details["field"] != "order_time" {
		t.Fatalf("details: %v", re.Details)
	}

	var al users.AllergensResponse
	if err := e.callFor(id, users.SubjectProfileAllergensSet, users.AllergensSetRequest{Allergens: []string{"Gluten", "Ouă"}}, &al); err != nil {
		t.Fatal(err)
	}
	var food users.FoodResponse
	e.callFor(id, users.SubjectProfilePrefsSet, users.PrefsSetRequest{Items: []users.Pref{{Kind: "dislike", Value: "ciuperci", Hard: true}}}, nil)
	if err := e.callFor(id, users.SubjectProfileFoodGet, nil, &food); err != nil {
		t.Fatal(err)
	}
	if len(food.Allergens) != 2 || food.Allergens[0] != "gluten" || len(food.Prefs) != 1 || !food.Prefs[0].Hard {
		t.Fatalf("food: %+v", food)
	}

	var loc users.Location
	lat, lon := 47.0105, 28.8638
	if err := e.callFor(id, users.SubjectProfileLocationsSave, users.Location{Label: "acasă", Address: "str. Mare 1", Lat: &lat, Lon: &lon}, &loc); err != nil {
		t.Fatal(err)
	}
	if loc.ID == 0 || !loc.IsDefault || loc.Lat == nil {
		t.Fatalf("location: %+v", loc)
	}
	var list users.LocationsResponse
	e.callFor(id, users.SubjectProfileLocationsList, nil, &list)
	if len(list.Items) != 1 {
		t.Fatalf("list: %+v", list)
	}

	var ch users.ChannelsResponse
	if err := e.callFor(id, users.SubjectProfileChannelsGet, nil, &ch); err != nil || ch.Latest != "telegram" || len(ch.Channels) != 1 {
		t.Fatalf("channels: %+v %v", ch, err)
	}
}

// Empty lists must reach the caller as [] and not as null.
func TestEmptyListsAreArrays(t *testing.T) {
	e := start(t)
	id := e.registered(t, "42", "ana@example.com")
	var raw map[string]any
	for _, subject := range []string{users.SubjectProfileAllergensGet, users.SubjectProfilePrefsGet, users.SubjectProfileLocationsList} {
		if err := e.callFor(id, subject, nil, &raw); err != nil {
			t.Fatal(err)
		}
		for k, v := range raw {
			if v == nil {
				t.Errorf("%s: %q is null", subject, k)
			}
		}
	}
}

func TestProfileOfAPendingAccountIsRefused(t *testing.T) {
	e := start(t)
	var who users.ResolveResponse
	e.call(users.SubjectAuthResolve, users.ResolveRequest{Channel: "telegram", ExternalID: "42"}, &who)
	wantRPC(t, e.callFor(who.UserID, users.SubjectProfileGet, nil, nil), users.ErrEmailNotConfirmed)
	wantRPC(t, e.callFor(who.UserID, users.SubjectProfileFoodGet, nil, nil), users.ErrEmailNotConfirmed)
}

// The person is the one in the envelope: a user_id inside the data is not read.
func TestProfileNeedsTheUserInTheEnvelope(t *testing.T) {
	e := start(t)
	id := e.registered(t, "42", "ana@example.com")
	wantRPC(t, e.call(users.SubjectProfileGet, map[string]string{"user_id": id}, nil), rpc.CodeBadRequest)
}

func TestPhoneSetFromTheBot(t *testing.T) {
	e := start(t)
	var who users.ResolveResponse
	e.call(users.SubjectAuthResolve, users.ResolveRequest{Channel: "telegram", ExternalID: "42"}, &who)
	if err := e.call(users.SubjectAuthPhoneSet, users.PhoneSetRequest{UserID: who.UserID, Phone: "+373 69 123 456"}, nil); err != nil {
		t.Fatal(err)
	}
	var other users.ResolveResponse
	e.call(users.SubjectAuthResolve, users.ResolveRequest{Channel: "telegram", ExternalID: "43"}, &other)
	wantRPC(t, e.call(users.SubjectAuthPhoneSet, users.PhoneSetRequest{UserID: other.UserID, Phone: "37369123456"}, nil), users.ErrPhoneTaken)
	wantRPC(t, e.call(users.SubjectAuthPhoneSet, users.PhoneSetRequest{UserID: other.UserID, Phone: "abc"}, nil), users.ErrInvalidPhone)
}
