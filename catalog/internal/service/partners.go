package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"food-order/catalog/internal/store"
	contracts "food-order/shared/contracts/catalog"
)

// The partners: who cooks and until when an order is taken. The schedule is a
// property of the partner, valid over many days; a menu is only the dishes of
// a date. Kept apart, "the partner is closed" stays different from "the menu
// was not loaded".
//
// While menus come from the parser nobody sends the schedule, so it is written
// by hand in partners.json; later it comes from the partners' api.

const clockFormat = "15:04"

// LoadPartners reads the file and saves what it says. A missing file is not
// a failure: without partners there are simply no order limits.
func (s *Service) LoadPartners(ctx context.Context, path string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		s.Log.WarnContext(ctx, "no partners file: no order limits", "path", path)
		return nil
	}
	if err != nil {
		return fmt.Errorf("partners: %w", err)
	}
	list, err := parsePartners(raw)
	if err != nil {
		return fmt.Errorf("partners: %s: %w", path, err)
	}
	if err := s.Store.SavePartners(ctx, list); err != nil {
		return fmt.Errorf("partners: %w", err)
	}
	s.Log.InfoContext(ctx, "partners loaded", "path", path, "partners", len(list))
	return nil
}

// partnersFile is the shape of partners.json.
type partnersFile struct {
	Partners []struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		Source   string `json:"source"`
		Schedule []struct {
			Days         []int  `json:"days"` // ISO weekdays of the meal, 1 = Monday
			DaysBefore   int    `json:"days_before"`
			OrderUntil   string `json:"order_until"`
			DeliveryFrom string `json:"delivery_from"`
			DeliveryTo   string `json:"delivery_to"`
		} `json:"schedule"`
		Closed []struct {
			Date   string `json:"date"`
			Reason string `json:"reason"`
		} `json:"closed"`
	} `json:"partners"`
}

// parsePartners reads the file and refuses a mistake: a wrong hour here
// would turn orders down, so it is better not to start.
func parsePartners(raw []byte) ([]store.Partner, error) {
	var f partnersFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	out := make([]store.Partner, 0, len(f.Partners))
	for _, p := range f.Partners {
		if p.Key == "" || p.Name == "" || p.Source == "" {
			return nil, fmt.Errorf("a partner needs key, name and source: %+v", p.Key)
		}
		sp := store.Partner{Key: p.Key, Name: p.Name, Source: p.Source}
		for _, sc := range p.Schedule {
			if _, err := time.Parse(clockFormat, sc.OrderUntil); err != nil {
				return nil, fmt.Errorf("%s: order_until %q is not like 10:30", p.Key, sc.OrderUntil)
			}
			if sc.DaysBefore < 0 {
				return nil, fmt.Errorf("%s: days_before is negative", p.Key)
			}
			for _, d := range sc.Days {
				if d < 1 || d > 7 {
					return nil, fmt.Errorf("%s: day %d is not 1-7", p.Key, d)
				}
				sp.Schedule = append(sp.Schedule, store.ScheduleDay{Weekday: d, DaysBefore: sc.DaysBefore,
					OrderUntil: sc.OrderUntil, DeliveryFrom: sc.DeliveryFrom, DeliveryTo: sc.DeliveryTo})
			}
		}
		for _, c := range p.Closed {
			if err := checkDate(c.Date); err != nil {
				return nil, fmt.Errorf("%s: closed date %q", p.Key, c.Date)
			}
			sp.Closed = append(sp.Closed, store.Closure{Date: c.Date, Reason: c.Reason})
		}
		out = append(out, sp)
	}
	return out, nil
}

// Partners lists the partners and how things stand on date.
func (s *Service) Partners(ctx context.Context, date string) (contracts.PartnersResponse, error) {
	if err := checkDate(date); err != nil {
		return contracts.PartnersResponse{}, err
	}
	list, err := s.Store.Partners(ctx)
	if err != nil {
		return contracts.PartnersResponse{}, err
	}
	out := contracts.PartnersResponse{Items: make([]contracts.Partner, 0, len(list))}
	for _, p := range list {
		out.Items = append(out.Items, availability(p, date, s.Cfg.Location))
	}
	return out, nil
}

// availability is the rule: is the partner open for a meal on date, and until
// when can one order it.
func availability(p store.Partner, date string, loc *time.Location) contracts.Partner {
	out := contracts.Partner{ID: p.ID, Name: p.Name, Date: date}
	for _, c := range p.Closed {
		if c.Date == date {
			out.Closed, out.Reason = true, c.Reason
			return out
		}
	}
	meal, err := time.ParseInLocation(store.DateFormat, date, loc)
	if err != nil {
		out.Closed = true
		return out
	}
	weekday := int(meal.Weekday())
	if weekday == 0 {
		weekday = 7 // ISO: Sunday is 7
	}
	for _, sc := range p.Schedule {
		if sc.Weekday != weekday {
			continue
		}
		at, err := time.Parse(clockFormat, sc.OrderUntil)
		if err != nil { // checked when it was loaded
			break
		}
		day := meal.AddDate(0, 0, -sc.DaysBefore)
		limit := time.Date(day.Year(), day.Month(), day.Day(), at.Hour(), at.Minute(), 0, 0, loc)
		out.OrderUntilMs = limit.UnixMilli()
		out.DeliveryFrom, out.DeliveryTo = sc.DeliveryFrom, sc.DeliveryTo
		return out
	}
	out.Closed, out.Reason = true, "nu lucrează în ziua asta"
	return out
}

// withPartners writes on every dish who cooks it and until when it can be
// ordered. The partner of a dish is the one its menu came from.
func (s *Service) withPartners(ctx context.Context, dishes []contracts.Dish) error {
	list, err := s.Store.Partners(ctx)
	if err != nil {
		return err
	}
	bySource := make(map[string]store.Partner, len(list))
	for _, p := range list {
		bySource[p.Source] = p
	}
	for i := range dishes {
		d := &dishes[i]
		p, ok := bySource[d.Source]
		if !ok {
			continue // nobody said who cooks it: no limit known
		}
		a := availability(p, d.Date, s.Cfg.Location)
		d.PartnerID, d.PartnerName, d.OrderUntilMs, d.Closed = a.ID, a.Name, a.OrderUntilMs, a.Closed
	}
	return nil
}
