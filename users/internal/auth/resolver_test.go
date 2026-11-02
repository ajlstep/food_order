package auth

import (
	"context"
	"testing"
)

func TestResolveCreatesAPendingAccountOnce(t *testing.T) {
	w := newWorld(t)

	first := w.person("1001", "ana")
	if !first.IsNew || first.Status != StatusPending || first.UserID == "" {
		t.Fatalf("unexpected first answer: %+v", first)
	}
	second := w.person("1001", "ana")
	if second.IsNew || second.UserID != first.UserID {
		t.Fatalf("the same person must get the same account: %+v vs %+v", first, second)
	}
	other := w.person("1002", "ion")
	if other.UserID == first.UserID {
		t.Fatal("two people share an account")
	}
}

func TestResolveRefreshesWhatTheChannelSays(t *testing.T) {
	w := newWorld(t)
	w.person("1001", "ana")
	_, err := w.resolver.Resolve(context.Background(), Who{Channel: "telegram", ExternalID: "1001", Nickname: "ana_new", ChatRef: "555", Language: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	ident, err := w.store.IdentityByKey(context.Background(), "telegram", "1001")
	if err != nil {
		t.Fatal(err)
	}
	if ident.Nickname != "ana_new" || ident.ChatRef != "555" || ident.Language != "ru" {
		t.Fatalf("identity not refreshed: %+v", ident)
	}
}

func TestResolveLanguage(t *testing.T) {
	w := newWorld(t)
	for _, c := range []struct{ code, want string }{{"ru", "ru"}, {"en-US", "en"}, {"de", "ro"}, {"", "ro"}} {
		r, err := w.resolver.Resolve(context.Background(), Who{Channel: "telegram", ExternalID: "lang-" + c.code + c.want, Language: c.code})
		if err != nil {
			t.Fatal(err)
		}
		if r.Language != c.want {
			t.Errorf("language %q: got %q, want %q", c.code, r.Language, c.want)
		}
	}
}

func TestResolveRejectsBadInput(t *testing.T) {
	w := newWorld(t)
	for _, bad := range []Who{
		{Channel: "", ExternalID: "1"},
		{Channel: "Tele gram", ExternalID: "1"},
		{Channel: "telegram", ExternalID: " "},
	} {
		_, err := w.resolver.Resolve(context.Background(), bad)
		wantCode(t, err, "invalid_input")
	}
}

func TestBlockedBotComesBackWhenThePersonWritesAgain(t *testing.T) {
	w := newWorld(t)
	w.person("1001", "ana")
	ctx := context.Background()

	if err := w.resolver.SetStatus(ctx, "telegram", "1001", IdentityBlocked); err != nil {
		t.Fatal(err)
	}
	w.person("1001", "ana")
	ident, _ := w.store.IdentityByKey(ctx, "telegram", "1001")
	if ident.Status != IdentityActive {
		t.Fatalf("status after writing again: %s", ident.Status)
	}

	// /stop is a choice of the person: writing does not undo it
	_ = w.resolver.SetStatus(ctx, "telegram", "1001", IdentityStopped)
	w.person("1001", "ana")
	ident, _ = w.store.IdentityByKey(ctx, "telegram", "1001")
	if ident.Status != IdentityStopped {
		t.Fatalf("status after /stop and a message: %s", ident.Status)
	}
}

func TestSetStatusErrors(t *testing.T) {
	w := newWorld(t)
	wantCode(t, w.resolver.SetStatus(context.Background(), "telegram", "nobody", IdentityStopped), "account_not_found")
	w.person("1001", "")
	wantCode(t, w.resolver.SetStatus(context.Background(), "telegram", "1001", "sleeping"), "invalid_input")
}
