package profile

import (
	"strings"
	"testing"
)

func loc(label string) Location { return Location{Label: label, Address: "str. " + label + " 1"} }

func defaults(list []Location) (n int, id int64) {
	for _, l := range list {
		if l.IsDefault {
			n++
			id = l.ID
		}
	}
	return
}

func TestFirstLocationIsTheDefault(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")

	first, err := w.svc.SaveLocation(ctx, id, loc("acasă"))
	if err != nil || !first.IsDefault || first.ID == 0 {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, _ := w.svc.SaveLocation(ctx, id, loc("serviciu"))
	if second.IsDefault {
		t.Fatal("the second address took the default")
	}
	list, _ := w.svc.Locations(ctx, id)
	if n, def := defaults(list); n != 1 || def != first.ID || len(list) != 2 || list[0].ID != first.ID {
		t.Fatalf("list: %+v", list)
	}
}

func TestMovingTheDefault(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	a, _ := w.svc.SaveLocation(ctx, id, loc("a"))
	b, _ := w.svc.SaveLocation(ctx, id, loc("b"))

	// by saving with IsDefault
	b.IsDefault = true
	if _, err := w.svc.SaveLocation(ctx, id, b); err != nil {
		t.Fatal(err)
	}
	list, _ := w.svc.Locations(ctx, id)
	if n, def := defaults(list); n != 1 || def != b.ID {
		t.Fatalf("after save: %+v", list)
	}
	// IsDefault=false in a save does not take the default away
	b.IsDefault = false
	w.svc.SaveLocation(ctx, id, b)
	list, _ = w.svc.Locations(ctx, id)
	if n, def := defaults(list); n != 1 || def != b.ID {
		t.Fatalf("a save with false changed the default: %+v", list)
	}
	// by choosing
	if err := w.svc.SetDefaultLocation(ctx, id, a.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = w.svc.Locations(ctx, id)
	if n, def := defaults(list); n != 1 || def != a.ID {
		t.Fatalf("after choosing: %+v", list)
	}
}

func TestUpdateKeepsTheID(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	a, _ := w.svc.SaveLocation(ctx, id, loc("a"))

	a.Address, a.Notes, a.Lat, a.Lon = "  str.  Nouă   5 ", "etaj 3, interfon 12", ptr(47.0105), ptr(28.8638)
	got, err := w.svc.SaveLocation(ctx, id, a)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.Address != "str. Nouă 5" || got.Notes != "etaj 3, interfon 12" || got.Lat == nil || *got.Lat != 47.0105 {
		t.Fatalf("got %+v", got)
	}
	if list, _ := w.svc.Locations(ctx, id); len(list) != 1 {
		t.Fatalf("an update added a row: %+v", list)
	}
}

func TestDeletingTheDefaultPromotesTheOldest(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	a, _ := w.svc.SaveLocation(ctx, id, loc("a"))
	b, _ := w.svc.SaveLocation(ctx, id, loc("b"))
	c, _ := w.svc.SaveLocation(ctx, id, loc("c"))
	w.svc.SetDefaultLocation(ctx, id, c.ID)

	if err := w.svc.DeleteLocation(ctx, id, c.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := w.svc.Locations(ctx, id)
	if n, def := defaults(list); n != 1 || def != a.ID || len(list) != 2 {
		t.Fatalf("after deleting the default: %+v", list)
	}
	// deleting one that is not the default changes nothing else
	if err := w.svc.DeleteLocation(ctx, id, b.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = w.svc.Locations(ctx, id)
	if n, def := defaults(list); n != 1 || def != a.ID || len(list) != 1 {
		t.Fatalf("after deleting another: %+v", list)
	}
	// the last one
	w.svc.DeleteLocation(ctx, id, a.ID)
	if list, _ := w.svc.Locations(ctx, id); len(list) != 0 {
		t.Fatalf("not empty: %+v", list)
	}
}

func TestNobodyTouchesAnotherPersonsLocations(t *testing.T) {
	w := newWorld(t)
	mine := w.active("u1")
	theirs := w.active("u2")
	a, _ := w.svc.SaveLocation(ctx, theirs, loc("a"))

	wantCode(t, w.svc.DeleteLocation(ctx, mine, a.ID), "not_found")
	wantCode(t, w.svc.SetDefaultLocation(ctx, mine, a.ID), "not_found")
	a.Address = "hijacked"
	wantCode(t, second(w.svc.SaveLocation(ctx, mine, a)), "not_found")

	if list, _ := w.svc.Locations(ctx, theirs); len(list) != 1 || list[0].Address == "hijacked" {
		t.Fatalf("their address changed: %+v", list)
	}
	if list, _ := w.svc.Locations(ctx, mine); len(list) != 0 {
		t.Fatalf("my list: %+v", list)
	}
}

func TestLocationRefusals(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	bad := map[string]Location{
		"no label":    {Address: "x"},
		"long label":  {Label: strings.Repeat("l", 41), Address: "x"},
		"no address":  {Label: "a"},
		"long notes":  {Label: "a", Address: "x", Notes: strings.Repeat("n", 201)},
		"lat only":    {Label: "a", Address: "x", Lat: ptr(47.0)},
		"lat range":   {Label: "a", Address: "x", Lat: ptr(91.0), Lon: ptr(10.0)},
		"lon range":   {Label: "a", Address: "x", Lat: ptr(10.0), Lon: ptr(181.0)},
		"default id?": {ID: 999, Label: "a", Address: "x"}, // an id that is not theirs: not_found, below
	}
	for name, l := range bad {
		err := second(w.svc.SaveLocation(ctx, id, l))
		if name == "default id?" {
			wantCode(t, err, "not_found")
			continue
		}
		wantCode(t, err, "invalid_input")
	}
}

func TestAtMostTwentyLocations(t *testing.T) {
	w := newWorld(t)
	id := w.active("u1")
	for i := 0; i < 20; i++ {
		if _, err := w.svc.SaveLocation(ctx, id, loc(strings.Repeat("x", i+1))); err != nil {
			t.Fatalf("location %d: %v", i+1, err)
		}
	}
	e := wantCode(t, second(w.svc.SaveLocation(ctx, id, loc("one more"))), "too_many")
	if e.Details["limit"] != 20 {
		t.Fatalf("details %v", e.Details)
	}
	// changing an existing one is still possible
	list, _ := w.svc.Locations(ctx, id)
	list[0].Notes = "changed"
	if _, err := w.svc.SaveLocation(ctx, id, list[0]); err != nil {
		t.Fatalf("update at the limit: %v", err)
	}
}
