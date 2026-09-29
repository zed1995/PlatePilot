package curate

import (
	"strconv"
	"strings"

	"github.com/zed/platepilot/shared/domain/restaurant"
)

// weekdayByName maps the day tokens that appear in Google Local hours.
var weekdayByName = map[string]int{
	"sunday": 0, "sun": 0,
	"monday": 1, "mon": 1,
	"tuesday": 2, "tue": 2, "tues": 2,
	"wednesday": 3, "wed": 3,
	"thursday": 4, "thu": 4, "thur": 4, "thurs": 4,
	"friday": 5, "fri": 5,
	"saturday": 6, "sat": 6,
}

// ParseHours converts the raw hours matrix into normalised intervals. Rows that
// cannot be parsed are skipped rather than failing the whole restaurant.
func ParseHours(raw [][]string) []restaurant.HoursEntry {
	out := make([]restaurant.HoursEntry, 0, len(raw))
	for _, row := range raw {
		if len(row) < 2 {
			continue
		}
		weekday, ok := weekdayByName[strings.ToLower(strings.TrimSpace(row[0]))]
		if !ok {
			continue
		}
		for _, spec := range row[1:] {
			if entry, ok := parseInterval(weekday, spec); ok {
				out = append(out, entry)
			}
		}
	}
	return out
}

func parseInterval(weekday int, spec string) (restaurant.HoursEntry, bool) {
	value := strings.TrimSpace(spec)
	lower := strings.ToLower(value)
	switch {
	case value == "":
		return restaurant.HoursEntry{}, false
	case strings.Contains(lower, "closed"):
		return restaurant.HoursEntry{Weekday: weekday, IsClosed: true}, true
	case strings.Contains(lower, "24 hours"), strings.Contains(lower, "open 24"):
		return restaurant.HoursEntry{Weekday: weekday, OpenMinute: 0, CloseMinute: 1440}, true
	}
	openSpec, closeSpec, ok := splitRange(value)
	if !ok {
		return restaurant.HoursEntry{}, false
	}
	openMinute, ok := parseClock(openSpec)
	if !ok {
		return restaurant.HoursEntry{}, false
	}
	closeMinute, ok := parseClock(closeSpec)
	if !ok {
		return restaurant.HoursEntry{}, false
	}
	if closeMinute <= openMinute {
		closeMinute += 24 * 60 // interval runs past midnight
	}
	return restaurant.HoursEntry{Weekday: weekday, OpenMinute: openMinute, CloseMinute: closeMinute}, true
}

// splitRange splits "11AM–10PM" style ranges on en/em dash, " to ", or hyphen.
func splitRange(s string) (string, string, bool) {
	for _, sep := range []string{"\u2013", "\u2014", " to "} {
		if idx := strings.Index(s, sep); idx > 0 {
			return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+len(sep):]), true
		}
	}
	if idx := strings.Index(s, "-"); idx > 0 {
		return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:]), true
	}
	return "", "", false
}

// parseClock converts "11AM", "10PM", "11:30AM", or "22:00" to minutes past
// midnight.
func parseClock(s string) (int, bool) {
	value := strings.ToUpper(strings.TrimSpace(s))
	if value == "" {
		return 0, false
	}
	if value == "NOON" {
		return 12 * 60, true
	}
	if value == "MIDNIGHT" {
		return 0, true
	}

	meridiem := ""
	switch {
	case strings.HasSuffix(value, "AM"):
		meridiem = "AM"
		value = strings.TrimSpace(strings.TrimSuffix(value, "AM"))
	case strings.HasSuffix(value, "PM"):
		meridiem = "PM"
		value = strings.TrimSpace(strings.TrimSuffix(value, "PM"))
	}

	hourPart, minutePart := value, "0"
	if idx := strings.Index(value, ":"); idx >= 0 {
		hourPart, minutePart = value[:idx], value[idx+1:]
	}
	hour, err := strconv.Atoi(strings.TrimSpace(hourPart))
	if err != nil {
		return 0, false
	}
	minute, err := strconv.Atoi(strings.TrimSpace(minutePart))
	if err != nil {
		return 0, false
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}

	switch meridiem {
	case "AM":
		if hour == 12 {
			hour = 0
		}
	case "PM":
		if hour != 12 {
			hour += 12
		}
	}
	return hour*60 + minute, true
}
