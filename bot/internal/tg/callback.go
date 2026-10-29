package tg

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// No message state is stored: what a button means is written in its
// callback_data (max 64 bytes). Formats:
//
//	o:<YYYYMMDD>:<dish_id>:<forced 0|1>:<price or ->   choose this dish
//	f:<YYYYMMDD>:<0|1>                                 switch "choose from necessity" on/off
//	r:<order_id>:<1..5>                                 rating
//	n:<order_id>                                        rating without comment
//	a:<YYYYMMDD>:<dish_id>:<y|n>                        allergens: yes, it may contain them / no
//	d:<1..7>                                            settings: switch an order day (ISO weekday)
//	t:<+30|-30>                                         settings: move the order time (minutes)
//	gi:<invite_id>:<y|n>                                group: accept / decline an invitation
//	gk:<user_id>                                        group: the owner removes a member
//	gl:<group_id>                                       group: confirm leaving
const (
	kindOrder    = "o"
	kindForced   = "f"
	kindRate     = "r"
	kindNoNote   = "n"
	kindAllergen = "a"
	kindDay      = "d"
	kindTime     = "t"
	kindInvite   = "gi"
	kindKick     = "gk"
	kindLeave    = "gl"
)

const callbackMax = 64

// Callback is a decoded callback_data.
type Callback struct {
	Kind    string
	Date    string // 2026-10-29
	DishID  int64
	Forced  bool
	Price   *float64
	OrderID int64
	Rating  int
	Yes     bool
	Day     int   // ISO weekday, 1 = Monday
	Minutes int   // time shift
	ID      int64 // invite id, user id or group id (group buttons)
}

var errBadCallback = errors.New("bad callback data")

func encodeOrder(date string, dishID int64, forced bool, price *float64) string {
	p := "-"
	if price != nil {
		p = strconv.FormatFloat(*price, 'f', -1, 64)
	}
	return join(kindOrder, compactDate(date), strconv.FormatInt(dishID, 10), bit(forced), p)
}

func encodeForced(date string, on bool) string { return join(kindForced, compactDate(date), bit(on)) }

func encodeRate(orderID int64, rating int) string {
	return join(kindRate, strconv.FormatInt(orderID, 10), strconv.Itoa(rating))
}

func encodeNoNote(orderID int64) string { return join(kindNoNote, strconv.FormatInt(orderID, 10)) }

func encodeAllergen(date string, dishID int64, yes bool) string {
	yn := "n"
	if yes {
		yn = "y"
	}
	return join(kindAllergen, compactDate(date), strconv.FormatInt(dishID, 10), yn)
}

func encodeDay(isoDay int) string { return join(kindDay, strconv.Itoa(isoDay)) }

func encodeTime(minutes int) string {
	return join(kindTime, fmt.Sprintf("%+d", minutes))
}

func encodeInvite(inviteID int64, accept bool) string {
	yn := "n"
	if accept {
		yn = "y"
	}
	return join(kindInvite, strconv.FormatInt(inviteID, 10), yn)
}

func encodeKick(userID int64) string { return join(kindKick, strconv.FormatInt(userID, 10)) }

func encodeLeave(groupID int64) string { return join(kindLeave, strconv.FormatInt(groupID, 10)) }

func join(parts ...string) string {
	s := strings.Join(parts, ":")
	if len(s) > callbackMax {
		panic(fmt.Sprintf("callback data too long (%d bytes): %s", len(s), s))
	}
	return s
}

func bit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// decodeCallback parses callback_data written by the encode* functions.
func decodeCallback(data string) (Callback, error) {
	p := strings.Split(data, ":")
	bad := fmt.Errorf("%w: %q", errBadCallback, data)
	cb := Callback{Kind: p[0]}
	var err error
	switch cb.Kind {
	case kindOrder:
		if len(p) != 5 {
			return cb, bad
		}
		if cb.Date, err = expandDate(p[1]); err != nil {
			return cb, bad
		}
		if cb.DishID, err = strconv.ParseInt(p[2], 10, 64); err != nil {
			return cb, bad
		}
		cb.Forced = p[3] == "1"
		if p[4] != "-" {
			v, err := strconv.ParseFloat(p[4], 64)
			if err != nil {
				return cb, bad
			}
			cb.Price = &v
		}
	case kindForced:
		if len(p) != 3 {
			return cb, bad
		}
		if cb.Date, err = expandDate(p[1]); err != nil {
			return cb, bad
		}
		cb.Forced = p[2] == "1"
	case kindRate:
		if len(p) != 3 {
			return cb, bad
		}
		if cb.OrderID, err = strconv.ParseInt(p[1], 10, 64); err != nil {
			return cb, bad
		}
		if cb.Rating, err = strconv.Atoi(p[2]); err != nil || cb.Rating < 1 || cb.Rating > 5 {
			return cb, bad
		}
	case kindNoNote:
		if len(p) != 2 {
			return cb, bad
		}
		if cb.OrderID, err = strconv.ParseInt(p[1], 10, 64); err != nil {
			return cb, bad
		}
	case kindAllergen:
		if len(p) != 4 {
			return cb, bad
		}
		if cb.Date, err = expandDate(p[1]); err != nil {
			return cb, bad
		}
		if cb.DishID, err = strconv.ParseInt(p[2], 10, 64); err != nil {
			return cb, bad
		}
		cb.Yes = p[3] == "y"
	case kindDay:
		if len(p) != 2 {
			return cb, bad
		}
		if cb.Day, err = strconv.Atoi(p[1]); err != nil || cb.Day < 1 || cb.Day > 7 {
			return cb, bad
		}
	case kindTime:
		if len(p) != 2 {
			return cb, bad
		}
		if cb.Minutes, err = strconv.Atoi(p[1]); err != nil || cb.Minutes == 0 || cb.Minutes < -720 || cb.Minutes > 720 {
			return cb, bad
		}
	case kindInvite:
		if len(p) != 3 || (p[2] != "y" && p[2] != "n") {
			return cb, bad
		}
		if cb.ID, err = strconv.ParseInt(p[1], 10, 64); err != nil || cb.ID <= 0 {
			return cb, bad
		}
		cb.Yes = p[2] == "y"
	case kindKick, kindLeave:
		if len(p) != 2 {
			return cb, bad
		}
		if cb.ID, err = strconv.ParseInt(p[1], 10, 64); err != nil || cb.ID <= 0 {
			return cb, bad
		}
	default:
		return cb, bad
	}
	return cb, nil
}

// compactDate turns 2026-10-29 into 20261029 (to save bytes in callback_data).
func compactDate(date string) string { return strings.ReplaceAll(date, "-", "") }

func expandDate(s string) (string, error) {
	t, err := time.Parse("20060102", s)
	if err != nil {
		return "", err
	}
	return t.Format(dateLayout), nil
}

const dateLayout = "2006-01-02"
