package org

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
	"food-order/users/internal/db"
	"food-order/users/internal/refusal"
)

// recorder keeps what would have been published.
type recorder struct {
	invites []food.OrgInvite
	notices []string // "user notice kind"
}

func (r *recorder) Publish(_ context.Context, _ string, data any) error {
	switch d := data.(type) {
	case food.OrgInvite:
		r.invites = append(r.invites, d)
	case food.OrgNotice:
		r.notices = append(r.notices, d.UserID+" "+d.Notice+" "+d.Kind)
	}
	return nil
}

type world struct {
	t     *testing.T
	store *SQLStore
	svc   *Service
	pub   *recorder
	now   time.Time
	ctx   context.Context
}

func newWorld(t *testing.T) *world {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	w := &world{t: t, store: NewSQLStore(database), pub: &recorder{}, ctx: context.Background(),
		now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	w.svc = NewService(w.store, w.pub, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return w.now })
	return w
}

// person adds an account with nickname = its id and phone +3736900000<n>.
func (w *world) person(id, status string) string {
	w.t.Helper()
	_, err := w.store.q.ExecContext(w.ctx,
		`INSERT INTO users (id, display_name, phone, status, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 1)`,
		id, "name-"+id, "+37369"+strings.Repeat("0", 6-len(id))+id, status)
	if err != nil {
		w.t.Fatal(err)
	}
	_, err = w.store.q.ExecContext(w.ctx,
		`INSERT INTO identities (user_id, channel, external_id, nickname, last_seen_at, created_at) VALUES (?, 'telegram', ?, ?, 1, 1)`,
		id, id, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) people(ids ...string) {
	for _, id := range ids {
		w.person(id, statusActive)
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *refusal.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected refusal %q, got %v", code, err)
	}
}

func (w *world) ok(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

// join invites and accepts, the only way in.
func (w *world) joinCompany(owner, who string) {
	w.t.Helper()
	w.ok(w.svc.InviteToCompany(w.ctx, owner, "@"+who))
	w.ok(w.svc.AcceptInvite(w.ctx, who, w.pub.invites[len(w.pub.invites)-1].InviteID))
}

func (w *world) joinGroup(owner, who string) {
	w.t.Helper()
	w.ok(w.svc.InviteToGroup(w.ctx, owner, "@"+who))
	w.ok(w.svc.AcceptInvite(w.ctx, who, w.pub.invites[len(w.pub.invites)-1].InviteID))
}

func (w *world) company(name, owner string) Company {
	w.t.Helper()
	c, err := w.svc.CreateCompany(w.ctx, owner, name)
	w.ok(err)
	return c
}

func (w *world) group(owner string, in GroupInput) Group {
	w.t.Helper()
	if in.Address == "" {
		in.Address = "Str. Mare 1"
	}
	if in.Name == "" {
		in.Name = "Etaj"
	}
	g, err := w.svc.CreateGroup(w.ctx, owner, in)
	w.ok(err)
	return g
}

func TestNothingWorksBeforeTheEmail(t *testing.T) {
	w := newWorld(t)
	w.person("1", "pending")
	_, err := w.svc.CreateCompany(w.ctx, "1", "Acme")
	wantCode(t, err, users.ErrEmailNotConfirmed)
	_, err = w.svc.Snapshot(w.ctx, "1")
	wantCode(t, err, users.ErrEmailNotConfirmed)
}

func TestOneCompanyOnePerson(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2")
	w.company("Acme", "1")
	_, err := w.svc.CreateCompany(w.ctx, "1", "Alta")
	wantCode(t, err, users.ErrInCompany)

	other := w.company("Beta", "2")
	w.ok(w.svc.InviteToCompany(w.ctx, "2", "@1"))
	err = w.svc.AcceptInvite(w.ctx, "1", w.pub.invites[0].InviteID)
	wantCode(t, err, users.ErrInCompany)
	_ = other
}

func TestInviteFindsPeople(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2", "3")
	w.person("4", "pending")
	w.company("Acme", "1")

	wantCode(t, w.svc.InviteToCompany(w.ctx, "1", "@nimeni"), users.ErrUserNotFound)
	wantCode(t, w.svc.InviteToCompany(w.ctx, "1", "@4"), users.ErrUserNotFound) // not confirmed
	wantCode(t, w.svc.InviteToCompany(w.ctx, "1", "nu e nimic"), users.ErrInvalidInput)
	wantCode(t, w.svc.InviteToCompany(w.ctx, "1", "@1"), users.ErrCannotInviteSelf)
	wantCode(t, w.svc.InviteToCompany(w.ctx, "2", "@3"), users.ErrNotInCompany)

	w.ok(w.svc.InviteToCompany(w.ctx, "1", "+373 69 000 002")) // by phone
	wantCode(t, w.svc.InviteToCompany(w.ctx, "1", "@2"), users.ErrInviteExists)
	if len(w.pub.invites) != 1 || w.pub.invites[0].UserID != "2" || w.pub.invites[0].Name != "Acme" {
		t.Fatalf("invites = %+v", w.pub.invites)
	}
}

func TestOnlyTheOwnerInvites(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2", "3")
	w.company("Acme", "1")
	w.joinCompany("1", "2")
	wantCode(t, w.svc.InviteToCompany(w.ctx, "2", "@3"), users.ErrNotOwner)
	wantCode(t, w.svc.RemoveFromCompany(w.ctx, "2", "1"), users.ErrNotOwner)
	v, err := w.svc.Company(w.ctx, "2")
	w.ok(err)
	if v.Members != nil || v.MemberCount != 2 {
		t.Fatalf("a member must not get the list: %+v", v)
	}
}

func TestInviteExpiresAfterAWeek(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2")
	w.company("Acme", "1")
	w.ok(w.svc.InviteToCompany(w.ctx, "1", "@2"))
	id := w.pub.invites[0].InviteID

	w.now = w.now.Add(inviteTTL)
	wantCode(t, w.svc.AcceptInvite(w.ctx, "2", id), users.ErrInviteExpired)
	list, err := w.svc.Invites(w.ctx, "2")
	w.ok(err)
	if len(list) != 0 {
		t.Fatalf("list = %+v", list)
	}
	w.ok(w.svc.InviteToCompany(w.ctx, "1", "@2")) // a new one is allowed
}

func TestInviteIsAnsweredOnceAndOnlyByThePerson(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2", "3")
	w.company("Acme", "1")
	w.ok(w.svc.InviteToCompany(w.ctx, "1", "@2"))
	id := w.pub.invites[0].InviteID

	wantCode(t, w.svc.AcceptInvite(w.ctx, "3", id), users.ErrInviteExpired)
	w.ok(w.svc.DeclineInvite(w.ctx, "2", id))
	wantCode(t, w.svc.AcceptInvite(w.ctx, "2", id), users.ErrInviteExpired)
	if len(w.pub.notices) != 1 || w.pub.notices[0] != "1 declined company" {
		t.Fatalf("notices = %q", w.pub.notices)
	}
}

func TestOwnerStaysWhileOthersAreIn(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2")
	w.company("Acme", "1")
	w.joinCompany("1", "2")

	wantCode(t, w.svc.LeaveCompany(w.ctx, "1"), users.ErrOwnerStays)
	w.ok(w.svc.LeaveCompany(w.ctx, "2"))
	w.ok(w.svc.LeaveCompany(w.ctx, "1"))

	_, err := w.svc.Company(w.ctx, "1")
	wantCode(t, err, users.ErrNotInCompany)
	var closed int
	w.ok(w.store.q.QueryRowContext(w.ctx, `SELECT COUNT(*) FROM companies WHERE deleted_at IS NOT NULL`).Scan(&closed))
	if closed != 1 {
		t.Fatal("the company must stay, closed: old orders point to it")
	}
}

func TestGroupBelongsToTheCompanyUnlessFree(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2", "3")
	c := w.company("Acme", "1")
	w.joinCompany("1", "2")

	g := w.group("1", GroupInput{})
	if g.CompanyID != c.ID {
		t.Fatalf("group = %+v", g)
	}
	free := w.group("2", GroupInput{Free: true})
	if free.CompanyID != "" {
		t.Fatalf("free group = %+v", free)
	}
	// a company group takes only people of the company
	wantCode(t, w.svc.InviteToGroup(w.ctx, "1", "@3"), users.ErrOtherCompany)
	// a free group takes anybody
	w.joinGroup("2", "3")
}

func TestLeavingACompanyLeavesItsGroup(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2", "3")
	w.company("Acme", "1")
	w.joinCompany("1", "2")
	w.joinCompany("1", "3")
	w.group("2", GroupInput{})
	w.joinGroup("2", "3")
	w.pub.notices = nil

	w.ok(w.svc.RemoveFromCompany(w.ctx, "1", "2"))

	if _, _, err := w.svc.Group(w.ctx, "2"); err == nil {
		t.Fatal("still in the group")
	}
	g, members, err := w.svc.Group(w.ctx, "3")
	w.ok(err)
	if g.OwnerID != "3" || len(members) != 1 {
		t.Fatalf("the oldest member takes over: %+v %+v", g, members)
	}
	want := "3 owner group|2 removed company"
	if got := strings.Join(w.pub.notices, "|"); got != want {
		t.Fatalf("notices = %q", got)
	}
}

func TestFreeGroupStaysWhenLeavingTheCompany(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2")
	w.company("Acme", "1")
	w.joinCompany("1", "2")
	w.group("2", GroupInput{Free: true})
	w.ok(w.svc.LeaveCompany(w.ctx, "2"))
	if _, _, err := w.svc.Group(w.ctx, "2"); err != nil {
		t.Fatal(err)
	}
}

func TestLastMemberClosesTheGroup(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2")
	g := w.group("1", GroupInput{})
	w.ok(w.svc.LeaveGroup(w.ctx, "1"))
	if _, err := w.store.GroupByID(w.ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	// an invitation to a closed place is not answerable
	g2 := w.group("2", GroupInput{})
	w.people("3")
	w.ok(w.svc.InviteToGroup(w.ctx, "2", "@3"))
	id := w.pub.invites[0].InviteID
	w.ok(w.svc.DeleteGroup(w.ctx, "2"))
	wantCode(t, w.svc.AcceptInvite(w.ctx, "3", id), users.ErrInviteExpired)
	if list, _ := w.svc.Invites(w.ctx, "3"); len(list) != 0 {
		t.Fatalf("list = %+v, group %s", list, g2.ID)
	}
}

func TestDeleteGroupTellsTheMembers(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2")
	w.group("1", GroupInput{})
	w.joinGroup("1", "2")
	wantCode(t, w.svc.DeleteGroup(w.ctx, "2"), users.ErrNotOwner)
	w.pub.notices = nil
	w.ok(w.svc.DeleteGroup(w.ctx, "1"))
	if len(w.pub.notices) != 1 || w.pub.notices[0] != "2 removed group" {
		t.Fatalf("notices = %q", w.pub.notices)
	}
}

func TestSnapshotIsTheAddressOfTheGroup(t *testing.T) {
	w := newWorld(t)
	w.people("1")
	s, err := w.svc.Snapshot(w.ctx, "1")
	w.ok(err)
	if s.ID != "" {
		t.Fatalf("no group: %+v", s)
	}
	w.company("Acme", "1")
	lat, lon := 47.0, 28.8
	w.group("1", GroupInput{Name: "Etaj 3", Address: "Str.  Mare 1", Lat: &lat, Lon: &lon})
	s, err = w.svc.Snapshot(w.ctx, "1")
	w.ok(err)
	if s.Name != "Etaj 3" || s.CompanyName != "Acme" || s.Address != "Str. Mare 1" || s.Lat == nil || *s.Lat != lat {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestDeliveryFee(t *testing.T) {
	w := newWorld(t)
	w.people("1")
	w.company("Acme", "1")
	zero, minus := int64(0), int64(-1)

	v, err := w.svc.UpdateCompany(w.ctx, "1", CompanyChange{MaxFee: &zero})
	w.ok(err)
	if v.MaxFee == nil || *v.MaxFee != 0 {
		t.Fatalf("0 means only free delivery: %+v", v.MaxFee)
	}
	_, err = w.svc.UpdateCompany(w.ctx, "1", CompanyChange{MaxFee: &minus})
	wantCode(t, err, users.ErrInvalidInput)
	v, err = w.svc.UpdateCompany(w.ctx, "1", CompanyChange{ClearFee: true})
	w.ok(err)
	if v.MaxFee != nil {
		t.Fatalf("no limit: %v", *v.MaxFee)
	}
}

func TestEventsCarryNoPeople(t *testing.T) {
	w := newWorld(t)
	w.people("1", "2")
	w.company("Acme", "1")
	w.joinCompany("1", "2")
	for _, n := range w.pub.notices {
		if strings.Contains(n, "name-") {
			t.Fatalf("a name in an event: %q", n)
		}
	}
}
