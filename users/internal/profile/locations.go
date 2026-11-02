package profile

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Locations lists the saved addresses, oldest first.
func (s *Service) Locations(ctx context.Context, userID string) ([]Location, error) {
	if _, err := s.active(ctx, s.store, userID); err != nil {
		return nil, err
	}
	list, err := s.store.Locations(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load locations: %w", err)
	}
	return list, nil
}

// SaveLocation adds an address (ID 0) or changes one. The first address is
// the default. IsDefault set to true moves the default here; set to false it
// changes nothing (the default moves only by choosing another one).
func (s *Service) SaveLocation(ctx context.Context, userID string, in Location) (Location, error) {
	l, err := cleanLocation(in)
	if err != nil {
		return Location{}, err
	}

	var saved Location
	err = s.store.InTx(ctx, func(tx Tx) error {
		if _, err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		existing, err := tx.Locations(ctx, userID)
		if err != nil {
			return err
		}
		if l.ID == 0 && len(existing) >= maxLocations {
			return fail("too_many", "limit", maxLocations)
		}
		if saved, err = tx.SaveLocation(ctx, userID, l, s.now()); err != nil {
			if errors.Is(err, ErrNotFound) {
				return fail("not_found")
			}
			return err
		}
		if l.IsDefault || len(existing) == 0 {
			if err := tx.SetDefaultLocation(ctx, userID, saved.ID); err != nil {
				return err
			}
			saved.IsDefault = true
		}
		return nil
	})
	if err != nil {
		return Location{}, fmt.Errorf("save location: %w", err)
	}
	return saved, nil
}

func cleanLocation(in Location) (Location, error) {
	l := in
	l.Label = strings.Join(strings.Fields(in.Label), " ")
	l.Address = strings.Join(strings.Fields(in.Address), " ")
	l.Notes = strings.Join(strings.Fields(in.Notes), " ")
	switch {
	case l.Label == "" || tooLong(l.Label, maxLabelLen):
		return Location{}, fail("invalid_input", "field", "label")
	case l.Address == "" || tooLong(l.Address, maxAddrLen):
		return Location{}, fail("invalid_input", "field", "address")
	case tooLong(l.Notes, maxNotesLen):
		return Location{}, fail("invalid_input", "field", "notes")
	case (l.Lat == nil) != (l.Lon == nil):
		return Location{}, fail("invalid_input", "field", "lat", "reason", "lat and lon go together")
	case l.Lat != nil && !validCoordinates(*l.Lat, *l.Lon):
		return Location{}, fail("invalid_input", "field", "lat", "reason", "coordinates out of range")
	}
	return l, nil
}

func validCoordinates(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// DeleteLocation removes an address. If it was the default, the oldest of
// the rest becomes the default, so a person with addresses always has one.
func (s *Service) DeleteLocation(ctx context.Context, userID string, id int64) error {
	err := s.store.InTx(ctx, func(tx Tx) error {
		if _, err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		wasDefault, err := tx.DeleteLocation(ctx, userID, id)
		if errors.Is(err, ErrNotFound) {
			return fail("not_found")
		}
		if err != nil || !wasDefault {
			return err
		}
		rest, err := tx.Locations(ctx, userID)
		if err != nil || len(rest) == 0 {
			return err
		}
		return tx.SetDefaultLocation(ctx, userID, rest[0].ID)
	})
	if err != nil {
		return fmt.Errorf("delete location: %w", err)
	}
	return nil
}

// SetDefaultLocation chooses the default address.
func (s *Service) SetDefaultLocation(ctx context.Context, userID string, id int64) error {
	err := s.store.InTx(ctx, func(tx Tx) error {
		if _, err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		if err := tx.SetDefaultLocation(ctx, userID, id); errors.Is(err, ErrNotFound) {
			return fail("not_found")
		} else {
			return err
		}
	})
	if err != nil {
		return fmt.Errorf("set default location: %w", err)
	}
	return nil
}
