package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGroupLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	reg := func(chat int64, nick string) User {
		u, _, err := s.Register(ctx, Profile{ChatID: chat, TGUserID: chat, FirstName: nick, Username: nick})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	ana, ion, dan := reg(1, "ana"), reg(2, "Ion_MD"), reg(3, "dan")

	if _, err := s.CreateGroup(ctx, ana.ID, " x "); !errors.Is(err, ErrBadGroupName) {
		t.Fatalf("short name: %v", err)
	}
	g, err := s.CreateGroup(ctx, ana.ID, "  Endava   Chișinău ")
	if err != nil || g.Name != "Endava Chișinău" || g.OwnerID != ana.ID {
		t.Fatalf("create: %+v %v", g, err)
	}
	if _, err := s.CreateGroup(ctx, ana.ID, "Alta"); !errors.Is(err, ErrAlreadyInGroup) {
		t.Fatalf("second group: %v", err)
	}

	// find by nickname (any case, with or without @) and by phone
	if u, err := s.FindUser(ctx, "@ion_md"); err != nil || u.ID != ion.ID {
		t.Fatalf("find nick: %+v %v", u, err)
	}
	_ = s.SetPhone(ctx, dan.ID, "+37369123456")
	for _, q := range []string{"+373 69 123 456", "069123456", "0037369123456"} {
		if u, err := s.FindUser(ctx, q); err != nil || u.ID != dan.ID {
			t.Fatalf("find phone %q: %+v %v", q, u, err)
		}
	}
	if _, err := s.FindUser(ctx, "@nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	// invitations
	if _, err := s.CreateInvite(ctx, g.ID, ion.ID, dan.ID); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner invites: %v", err)
	}
	inv, err := s.CreateInvite(ctx, g.ID, ana.ID, ion.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInvite(ctx, g.ID, ana.ID, ion.ID); !errors.Is(err, ErrInvitePending) {
		t.Fatalf("double invite: %v", err)
	}
	if _, _, err := s.AnswerInvite(ctx, inv.ID, dan.ID, true); !errors.Is(err, ErrInviteGone) {
		t.Fatalf("someone else answers: %v", err)
	}
	if _, g2, err := s.AnswerInvite(ctx, inv.ID, ion.ID, true); err != nil || g2.ID != g.ID {
		t.Fatalf("accept: %v", err)
	}
	if _, _, err := s.AnswerInvite(ctx, inv.ID, ion.ID, true); !errors.Is(err, ErrInviteGone) {
		t.Fatalf("answer twice: %v", err)
	}
	if _, err := s.CreateInvite(ctx, g.ID, ana.ID, ion.ID); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("invite a member: %v", err)
	}

	// dan declines, then is invited again and accepts
	inv2, _ := s.CreateInvite(ctx, g.ID, ana.ID, dan.ID)
	if inv, _, err := s.AnswerInvite(ctx, inv2.ID, dan.ID, false); err != nil || inv.Status != "declined" {
		t.Fatalf("decline: %+v %v", inv, err)
	}
	inv3, err := s.CreateInvite(ctx, g.ID, ana.ID, dan.ID)
	if err != nil {
		t.Fatalf("invite after decline: %v", err)
	}
	time.Sleep(time.Second) // joined_at has second precision: keep the order ana, ion, dan
	if _, _, err := s.AnswerInvite(ctx, inv3.ID, dan.ID, true); err != nil {
		t.Fatal(err)
	}

	members, _ := s.Members(ctx, g.ID)
	if len(members) != 3 || members[0].User.ID != ana.ID {
		t.Fatalf("members = %+v", members)
	}

	// remove
	if _, err := s.RemoveMember(ctx, g.ID, ion.ID, dan.ID); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner removes: %v", err)
	}
	if _, err := s.RemoveMember(ctx, g.ID, ana.ID, ana.ID); !errors.Is(err, ErrCannotRemoveOwner) {
		t.Fatalf("owner removes self: %v", err)
	}
	if _, err := s.RemoveMember(ctx, g.ID, ana.ID, dan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GroupOf(ctx, dan.ID); !errors.Is(err, ErrNoGroup) {
		t.Fatalf("removed is still in: %v", err)
	}

	// owner leaves -> ion is the owner; ion leaves -> group deleted
	r, err := s.LeaveGroup(ctx, ana.ID)
	if err != nil || r.Deleted || r.NewOwner == nil || r.NewOwner.ID != ion.ID {
		t.Fatalf("owner leaves: %+v %v", r, err)
	}
	if g3, _ := s.GroupOf(ctx, ion.ID); g3.OwnerID != ion.ID {
		t.Fatalf("new owner = %d", g3.OwnerID)
	}
	pending, _ := s.CreateInvite(ctx, g.ID, ion.ID, dan.ID)
	r, err = s.LeaveGroup(ctx, ion.ID)
	if err != nil || !r.Deleted {
		t.Fatalf("last leaves: %+v %v", r, err)
	}
	if _, err := s.GroupByID(ctx, g.ID); !errors.Is(err, ErrNoGroup) {
		t.Fatal("an empty group must be deleted")
	}
	if _, _, err := s.AnswerInvite(ctx, pending.ID, dan.ID, true); !errors.Is(err, ErrInviteGone) {
		t.Fatalf("invitation of a deleted group: %v", err)
	}
	if _, err := s.LeaveGroup(ctx, ion.ID); !errors.Is(err, ErrNoGroup) {
		t.Fatalf("leave without group: %v", err)
	}
}

func TestAcceptWhileInAnotherGroup(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a, _, _ := s.Register(ctx, Profile{ChatID: 1})
	b, _, _ := s.Register(ctx, Profile{ChatID: 2})
	ga, _ := s.CreateGroup(ctx, a.ID, "Grupul A")
	gb, _ := s.CreateGroup(ctx, b.ID, "Grupul B")
	inv, _ := s.CreateInvite(ctx, ga.ID, a.ID, b.ID)
	_, cur, err := s.AnswerInvite(ctx, inv.ID, b.ID, true)
	if !errors.Is(err, ErrAlreadyInGroup) || cur.ID != gb.ID {
		t.Fatalf("accept while in B: %v %+v", err, cur)
	}
	_, _ = s.LeaveGroup(ctx, b.ID) // B is deleted
	if _, _, err := s.AnswerInvite(ctx, inv.ID, b.ID, true); err != nil {
		t.Fatalf("after leaving B the invitation still works: %v", err)
	}
}

func TestInviteExpires(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a, _, _ := s.Register(ctx, Profile{ChatID: 1})
	b, _, _ := s.Register(ctx, Profile{ChatID: 2})
	g, _ := s.CreateGroup(ctx, a.ID, "Grup")
	inv, _ := s.CreateInvite(ctx, g.ID, a.ID, b.ID)

	s.now = func() time.Time { return time.Now().Add(InviteTTL + time.Hour) }
	if _, _, err := s.AnswerInvite(ctx, inv.ID, b.ID, true); !errors.Is(err, ErrInviteGone) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := s.CreateInvite(ctx, g.ID, a.ID, b.ID); err != nil {
		t.Fatalf("a new invitation after expiry: %v", err)
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{"+373 69 123 456": "37369123456", "069123456": "37369123456", "ion_md": "", "123": "", "(373) 69-12-34-56": "37369123456"}
	for in, want := range cases {
		if got := NormalizePhone(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
