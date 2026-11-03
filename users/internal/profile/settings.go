package profile

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"food-order/shared/contracts/users"
)

// A setting is a key with a text value in a fixed form. The bot turns what the
// person types ("luni-vineri", "16.30") into this form; here we only accept
// the fixed form, so every service reads the same thing.
//
// To add a setting: one more entry in settingDefs. No migration is needed.
type settingDef struct {
	key   string
	def   string
	canon func(string) (string, error) // checks a value and returns it in canonical form
}

var settingDefs = []settingDef{
	{users.SettingOrderTime, "16:00", canonTime},
	{users.SettingOrderDays, "1,2,3,4,5", canonDays},
	{users.SettingTimezone, "Europe/Chisinau", canonTimezone},
	{users.SettingRecFor, "tomorrow", oneOf("today", "tomorrow")},
	{users.SettingNotifyPersonal, "true", oneOf("true", "false")},
	{users.SettingNotifyGroup, "true", oneOf("true", "false")},
	{users.SettingOrderTo, users.OrderToGroup, oneOf(users.OrderToGroup, users.OrderToPersonal)},
}

// defaultSettings returns every setting with its default.
func defaultSettings() map[string]string {
	m := make(map[string]string, len(settingDefs))
	for _, d := range settingDefs {
		m[d.key] = d.def
	}
	return m
}

// canonSetting checks one value. known is false for a key that does not exist.
func canonSetting(key, value string) (canon string, known bool, err error) {
	for _, d := range settingDefs {
		if d.key == key {
			canon, err = d.canon(strings.TrimSpace(value))
			return canon, true, err
		}
	}
	return "", false, nil
}

var timeForm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func canonTime(v string) (string, error) {
	if !timeForm.MatchString(v) {
		return "", fmt.Errorf("%q is not a time like 16:30", v)
	}
	return v, nil
}

// canonDays accepts ISO weekdays "1,3,5" and returns them sorted, without
// repeats. An empty list is allowed: the person orders on no day.
func canonDays(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	var days []int
	for _, part := range strings.Split(v, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > 7 {
			return "", fmt.Errorf("%q is not a weekday from 1 (Monday) to 7 (Sunday)", part)
		}
		if !slices.Contains(days, n) {
			days = append(days, n)
		}
	}
	slices.Sort(days)
	out := make([]string, len(days))
	for i, d := range days {
		out[i] = strconv.Itoa(d)
	}
	return strings.Join(out, ","), nil
}

func canonTimezone(v string) (string, error) {
	// "Local" would mean the zone of the server, not of the person
	if v == "" || v == "Local" {
		return "", fmt.Errorf("a time zone like Europe/Chisinau is needed")
	}
	if _, err := time.LoadLocation(v); err != nil {
		return "", fmt.Errorf("unknown time zone %q", v)
	}
	return v, nil
}

func oneOf(allowed ...string) func(string) (string, error) {
	return func(v string) (string, error) {
		if !slices.Contains(allowed, v) {
			return "", fmt.Errorf("%q is not one of %s", v, strings.Join(allowed, ", "))
		}
		return v, nil
	}
}
