package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRequestSendsOneLetterWithTheCode(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "ana")

	sent, err := w.email.Request(context.Background(), p.UserID, "  Ana@Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	w.email.Wait()
	if sent.ValidFor != 30*time.Minute {
		t.Errorf("ValidFor = %v", sent.ValidFor)
	}
	letters := w.mail.Sent()
	if len(letters) != 1 || letters[0].To != "ana@example.com" || !strings.Contains(letters[0].Body, "K7P2") {
		t.Fatalf("unexpected letters: %+v", letters)
	}
}

func TestRequestRejectsBadAddresses(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	for _, bad := range []string{"", "ana", "ana@", "@example.com", "ana@example", "Ana <ana@example.com>", "a b@example.com", "ana@example.com,x@y.com"} {
		_, err := w.email.Request(context.Background(), p.UserID, bad)
		wantCode(t, err, "invalid_email")
	}
	if len(w.mail.Sent()) != 0 {
		t.Fatal("a letter was sent for a bad address")
	}
}

func TestRequestForUnknownOrConfirmedAccount(t *testing.T) {
	w := newWorld(t)
	_, err := w.email.Request(context.Background(), "no-such-user", "a@example.com")
	wantCode(t, err, "account_not_found")

	id := w.confirmed("1001", "ana@example.com")
	_, err = w.email.Request(context.Background(), id, "other@example.com")
	wantCode(t, err, "already_confirmed")
}

func TestPauseBetweenCodes(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	ctx := context.Background()
	if _, err := w.email.Request(ctx, p.UserID, "ana@example.com"); err != nil {
		t.Fatal(err)
	}

	w.clock.Advance(10 * time.Second)
	_, err := w.email.Request(ctx, p.UserID, "ana@example.com")
	e := wantCode(t, err, "cooldown")
	if e.Details["retry_after"] != 20 {
		t.Errorf("retry_after = %v, want 20", e.Details["retry_after"])
	}

	w.clock.Advance(21 * time.Second)
	if _, err := w.email.Request(ctx, p.UserID, "ana@example.com"); err != nil {
		t.Fatalf("after the pause: %v", err)
	}
	w.email.Wait()
	if n := len(w.mail.Sent()); n != 2 {
		t.Fatalf("letters sent: %d, want 2 (the refused request sends nothing)", n)
	}
}

func TestANewCodeCancelsTheOldOne(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	ctx := context.Background()
	w.email.newCode = func() (string, error) { return "AAAA", nil }
	w.email.Request(ctx, p.UserID, "ana@example.com")
	w.clock.Advance(time.Minute)
	w.email.newCode = func() (string, error) { return "BBBB", nil }
	w.email.Request(ctx, p.UserID, "ana@example.com")

	_, err := w.email.Confirm(ctx, p.UserID, "AAAA")
	wantCode(t, err, "wrong_code")
	if _, err := w.email.Confirm(ctx, p.UserID, "BBBB"); err != nil {
		t.Fatalf("the new code: %v", err)
	}
}

func TestCodesPerHour(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := w.email.Request(ctx, p.UserID, "ana@example.com"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		w.clock.Advance(31 * time.Second)
	}
	_, err := w.email.Request(ctx, p.UserID, "ana@example.com")
	e := wantCode(t, err, "too_many_codes")
	// the oldest code was sent 5*31 s ago; it leaves the hour in 3600-155 s
	if e.Details["retry_after"] != 3445 {
		t.Errorf("retry_after = %v, want 3445", e.Details["retry_after"])
	}

	// the limit is for the address, not only for the account: somebody else cannot use us to flood it
	q := w.person("1002", "")
	_, err = w.email.Request(ctx, q.UserID, "ana@example.com")
	wantCode(t, err, "too_many_codes")

	w.clock.Advance(time.Hour)
	if _, err := w.email.Request(ctx, p.UserID, "ana@example.com"); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
}

func TestConfirmActivatesTheAccount(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "ana")
	ctx := context.Background()
	w.email.Request(ctx, p.UserID, "Ana@Example.com")

	res, err := w.email.Confirm(ctx, p.UserID, "k7p2") // case does not matter
	if err != nil {
		t.Fatal(err)
	}
	if res.UserID != p.UserID || res.Attached {
		t.Fatalf("unexpected result %+v", res)
	}
	u, _ := w.store.UserByID(ctx, p.UserID)
	if u.Status != StatusActive || u.Email != "ana@example.com" {
		t.Fatalf("user after confirmation: %+v", u)
	}

	_, err = w.email.Confirm(ctx, p.UserID, "K7P2")
	wantCode(t, err, "already_confirmed")
}

func TestACodeWorksOnce(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	ctx := context.Background()
	w.email.Request(ctx, p.UserID, "ana@example.com")
	w.email.Confirm(ctx, p.UserID, "K7P2")

	if _, err := w.store.CodeByUser(ctx, p.UserID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the code is still stored: %v", err)
	}
}

func TestWrongAttemptsCancelTheCode(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	ctx := context.Background()
	w.email.Request(ctx, p.UserID, "ana@example.com")

	for left := 4; left >= 1; left-- {
		_, err := w.email.Confirm(ctx, p.UserID, "ZZZZ")
		e := wantCode(t, err, "wrong_code")
		if e.Details["attempts_left"] != left {
			t.Fatalf("attempts_left = %v, want %d", e.Details["attempts_left"], left)
		}
	}
	_, err := w.email.Confirm(ctx, p.UserID, "ZZZZ")
	wantCode(t, err, "too_many_attempts")

	// the right code does not help any more: a new one must be asked for
	_, err = w.email.Confirm(ctx, p.UserID, "K7P2")
	wantCode(t, err, "no_code")
}

func TestExpiredCode(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	ctx := context.Background()
	w.email.Request(ctx, p.UserID, "ana@example.com")

	w.clock.Advance(29 * time.Minute)
	if _, err := w.email.Confirm(ctx, p.UserID, "ZZZZ"); err == nil {
		t.Fatal("wrong code accepted")
	}
	w.clock.Advance(2 * time.Minute) // 31 minutes in total
	_, err := w.email.Confirm(ctx, p.UserID, "K7P2")
	wantCode(t, err, "expired")
}

func TestConfirmWithoutARequest(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	_, err := w.email.Confirm(context.Background(), p.UserID, "K7P2")
	wantCode(t, err, "no_code")
}

func TestConfirmAcceptsHowPeopleType(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	w.email.Request(context.Background(), p.UserID, "ana@example.com")
	if _, err := w.email.Confirm(context.Background(), p.UserID, " k7-p 2 "); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedCodeIsNotAnAttempt(t *testing.T) {
	w := newWorld(t)
	p := w.person("1001", "")
	ctx := context.Background()
	w.email.Request(ctx, p.UserID, "ana@example.com")
	for _, bad := range []string{"", "K7P", "K7P2X", "K70O"} { // 0 and O are not in the alphabet
		_, err := w.email.Confirm(ctx, p.UserID, bad)
		wantCode(t, err, "invalid_input")
	}
	rec, _ := w.store.CodeByUser(ctx, p.UserID)
	if rec.Attempts != 0 {
		t.Fatalf("attempts = %d", rec.Attempts)
	}
}

// The person writes from a second messenger with an email that already has an
// account: the new channel goes to that account and the empty one disappears.
func TestEmailOfAnExistingAccountAttachesTheChannel(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	original := w.confirmed("1001", "ana@example.com")

	second := w.person("2002", "ana_phone") // a new pending account
	if second.UserID == original {
		t.Fatal("test setup: expected a new account")
	}
	// the answer to the request must not reveal that the address is taken
	if _, err := w.email.Request(ctx, second.UserID, "ana@example.com"); err != nil {
		t.Fatalf("request for an address that has an account: %v", err)
	}
	res, err := w.email.Confirm(ctx, second.UserID, "K7P2")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Attached || res.UserID != original {
		t.Fatalf("expected attach to %s, got %+v", original, res)
	}

	ident, err := w.store.IdentityByKey(ctx, "telegram", "2002")
	if err != nil || ident.UserID != original {
		t.Fatalf("the channel did not move: %+v, %v", ident, err)
	}
	if _, err := w.store.UserByID(ctx, second.UserID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the pending account still exists: %v", err)
	}
	// and the next message from that channel is the original person
	if again := w.person("2002", "ana_phone"); again.UserID != original || again.Status != StatusActive {
		t.Fatalf("resolve after attach: %+v", again)
	}
}

func TestEmailOfABlockedAccountIsRefused(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	owner := w.confirmed("1001", "ana@example.com")
	if _, err := w.store.q.ExecContext(ctx, `UPDATE users SET status = 'blocked' WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	second := w.person("2002", "")
	w.email.Request(ctx, second.UserID, "ana@example.com")
	_, err := w.email.Confirm(ctx, second.UserID, "K7P2")
	wantCode(t, err, "account_blocked")
	// nothing moved
	if ident, _ := w.store.IdentityByKey(ctx, "telegram", "2002"); ident.UserID != second.UserID {
		t.Fatal("the channel moved to a blocked account")
	}
}

func TestMailFailureDoesNotLeakTheAddress(t *testing.T) {
	w := newWorld(t)
	w.mail.Err = errors.New("550 mailbox ana@example.com unavailable")
	p := w.person("1001", "")
	if _, err := w.email.Request(context.Background(), p.UserID, "ana@example.com"); err != nil {
		t.Fatalf("a mail failure must not fail the request: %v", err)
	}
	w.email.Wait()
	logs := w.logs.String()
	if !strings.Contains(logs, "could not send the confirmation email") {
		t.Fatalf("the failure was not logged:\n%s", logs)
	}
	if strings.Contains(logs, "ana@example.com") || strings.Contains(logs, "K7P2") {
		t.Fatalf("personal data in the logs:\n%s", logs)
	}
}

func TestRandomCodeUsesOnlyTheAlphabet(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := randomCode()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := normalizeCode(c); !ok {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 1500 { // 2000 draws from ~1M codes: nearly all different
		t.Fatalf("codes repeat too much: %d different of 2000", len(seen))
	}
}
