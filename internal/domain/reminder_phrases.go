package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The reminder phrases Slack's /remind and reminders.add understand: a
// relative span ("in 15 minutes"), tomorrow, a date, a weekday, a recurrence
// ("every Thursday", "every day at 9am"), a month and day, or a time today.
// Each pattern's first group is the reminder text before the phrase.
var remindInPattern = regexp.MustCompile(`(?i)^(.*?)\s+in\s+(a|an|[1-9][0-9]*)\s+(minute|minutes|hour|hours|day|days|week|weeks|month|months|year|years)$`)
var remindTomorrowPattern = regexp.MustCompile(`(?i)^(.*?)\s+tomorrow(?:\s+at\s+(.+))?$`)
var remindDatePattern = regexp.MustCompile(`(?i)^(.*?)\s+on\s+([0-9]{4}-[0-9]{2}-[0-9]{2})(?:\s+at\s+(.+))?$`)
var remindWeekdayPattern = regexp.MustCompile(`(?i)^(.*?)\s+every\s+(sunday|monday|tuesday|wednesday|thursday|friday|saturday)(?:\s+at\s+(.+))?$`)
var remindRecurringPattern = regexp.MustCompile(`(?i)^(.*?)\s+every\s+(day|week|month|year)(?:\s+at\s+(.+))?$`)
var remindOnWeekdayPattern = regexp.MustCompile(`(?i)^(.*?)\s+on\s+(sunday|monday|tuesday|wednesday|thursday|friday|saturday)(?:\s+at\s+(.+))?$`)
var remindOnMonthDayPattern = regexp.MustCompile(`(?i)^(.*?)\s+on\s+(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept?|oct|nov|dec)\s+([0-9]{1,2})(?:\s+at\s+(.+))?$`)
var remindTodayPattern = regexp.MustCompile(`(?i)^(.*?)\s+at\s+(.+)$`)

// ReminderOccurrence is when a reminder phrase first comes due and how it
// recurs. Anchor positions a recurring series: the delivery worker steps from
// it (AdvanceReminder), so it is stored apart from Due. The two differ when
// the first occurrence was clamped: "every month" set on the 31st after its
// time has passed first falls on the next month's last day, while the series
// keeps the 31st. For a one-time reminder Anchor is Due.
type ReminderOccurrence struct {
	Due        time.Time
	Recurrence ReminderRecurrence
	Anchor     time.Time
}

func once(due time.Time) ReminderOccurrence {
	return ReminderOccurrence{Due: due, Recurrence: ReminderOnce, Anchor: due}
}

// ParseReminderExpression reads "<text> <when>" - the text of a reminder
// followed by one of the phrases above - in the member's zone. It is shared by
// the first-party /remind command and the Web API's reminders.add, which used
// to accept only a number and answered cannot_parse to every phrase Slack
// documents.
func ParseReminderExpression(expression string, now time.Time, location *time.Location) (string, ReminderOccurrence, error) {
	localNow := now.In(location)
	if match := remindInPattern.FindStringSubmatch(expression); match != nil {
		// "a" and "an" are the spoken form of one: "in an hour" means "in 1 hour".
		count := 1
		if quantity := strings.ToLower(match[2]); quantity != "a" && quantity != "an" {
			count, _ = strconv.Atoi(quantity)
		}
		unit := strings.ToLower(match[3])
		// A month and a year are calendar steps, not fixed spans: "in 2 months"
		// keeps the wall-clock time of day and lands on the same day-of-month two
		// months on, the way AddDate resolves a short month (Jan 31 + 1 month is
		// early March). This is a one-time reminder, so there is no series to
		// keep on its day; "every month" clamps instead (AdvanceReminder). The
		// fixed units stay an absolute duration, as they were.
		switch {
		case strings.HasPrefix(unit, "month"):
			return strings.TrimSpace(match[1]), once(localNow.AddDate(0, count, 0)), nil
		case strings.HasPrefix(unit, "year"):
			return strings.TrimSpace(match[1]), once(localNow.AddDate(count, 0, 0)), nil
		}
		duration := time.Duration(count) * time.Minute
		switch {
		case strings.HasPrefix(unit, "hour"):
			duration = time.Duration(count) * time.Hour
		case strings.HasPrefix(unit, "day"):
			duration = time.Duration(count) * 24 * time.Hour
		case strings.HasPrefix(unit, "week"):
			duration = time.Duration(count) * 7 * 24 * time.Hour
		}
		return strings.TrimSpace(match[1]), once(now.Add(duration)), nil
	}
	if match := remindTomorrowPattern.FindStringSubmatch(expression); match != nil {
		hour, minute, err := parseReminderClock(match[2], 9, 0)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		tomorrow := localNow.AddDate(0, 0, 1)
		due, err := reminderLocalTime(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), hour, minute, location)
		return strings.TrimSpace(match[1]), once(due), err
	}
	if match := remindDatePattern.FindStringSubmatch(expression); match != nil {
		date, err := time.ParseInLocation("2006-01-02", match[2], location)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		hour, minute, err := parseReminderClock(match[3], 9, 0)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		due, err := reminderLocalTime(date.Year(), date.Month(), date.Day(), hour, minute, location)
		return strings.TrimSpace(match[1]), once(due), err
	}
	if match := remindWeekdayPattern.FindStringSubmatch(expression); match != nil {
		hour, minute, err := parseReminderClock(match[3], 9, 0)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		due, err := comingWeekday(match[2], hour, minute, localNow, now, location)
		if err != nil {
			return "", ReminderOccurrence{}, err
		}
		return strings.TrimSpace(match[1]), ReminderOccurrence{Due: due, Recurrence: ReminderWeekly, Anchor: due}, nil
	}
	if match := remindRecurringPattern.FindStringSubmatch(expression); match != nil {
		hour, minute, err := parseReminderClock(match[3], 9, 0)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		recurrence := map[string]ReminderRecurrence{
			"day": ReminderDaily, "week": ReminderWeekly,
			"month": ReminderMonthly, "year": ReminderYearly,
		}[strings.ToLower(match[2])]
		// Today at the named time positions the series: its day-of-month (and
		// for yearly its month) is the day the reminder recurs on. When that
		// time has passed, the first occurrence is the series' next one, which
		// in a shorter month is clamped to the month's last day. The anchor
		// stays today, so "every month" set on October 31st falls on November
		// 30th and then December 31st rather than settling on the 1st or 30th.
		anchor, err := reminderLocalTime(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, location)
		if err != nil {
			return "", ReminderOccurrence{}, err
		}
		due := anchor
		for !due.After(now) {
			due = AdvanceReminder(recurrence, nil, anchor, due, location)
		}
		return strings.TrimSpace(match[1]), ReminderOccurrence{Due: due, Recurrence: recurrence, Anchor: anchor}, nil
	}
	if match := remindOnWeekdayPattern.FindStringSubmatch(expression); match != nil {
		// "on friday" is a single occurrence on the coming Friday, distinct from
		// "every friday". It shares the coming-weekday resolution with the
		// recurring form but records no recurrence.
		hour, minute, err := parseReminderClock(match[3], 9, 0)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		due, err := comingWeekday(match[2], hour, minute, localNow, now, location)
		if err != nil {
			return "", ReminderOccurrence{}, err
		}
		return strings.TrimSpace(match[1]), once(due), nil
	}
	if match := remindOnMonthDayPattern.FindStringSubmatch(expression); match != nil {
		// "on July 4" is a single occurrence on the next such date: this year if it
		// is still ahead, otherwise the next. An impossible day for the month —
		// "on February 30" — is rejected rather than rolled into March.
		month := monthByName(match[2])
		day, _ := strconv.Atoi(match[3])
		hour, minute, err := parseReminderClock(match[4], 9, 0)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		due, err := reminderLocalTime(localNow.Year(), month, day, hour, minute, location)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		if !due.After(now) {
			due, err = reminderLocalTime(localNow.Year()+1, month, day, hour, minute, location)
			if err != nil {
				return "", ReminderOccurrence{}, ErrInvalidLaterReminder
			}
		}
		return strings.TrimSpace(match[1]), once(due), nil
	}
	if match := remindTodayPattern.FindStringSubmatch(expression); match != nil {
		hour, minute, err := parseReminderClock(match[2], 0, 0)
		if err != nil {
			return "", ReminderOccurrence{}, ErrInvalidLaterReminder
		}
		due, err := reminderLocalTime(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, location)
		if err != nil || !due.After(now) {
			return "", ReminderOccurrence{}, ErrReminderTimeInPast
		}
		return strings.TrimSpace(match[1]), once(due), nil
	}
	return "", ReminderOccurrence{}, ErrInvalidLaterReminder
}

// comingWeekday resolves the next occurrence of a named weekday at the given
// clock time, rolling to the following week when this week's time has already
// passed. Both "every <weekday>" and "on <weekday>" position their first
// delivery this way.
func comingWeekday(name string, hour, minute int, localNow, now time.Time, location *time.Location) (time.Time, error) {
	weekday := map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
		"wednesday": time.Wednesday, "thursday": time.Thursday,
		"friday": time.Friday, "saturday": time.Saturday,
	}[strings.ToLower(name)]
	days := (int(weekday) - int(localNow.Weekday()) + 7) % 7
	date := localNow.AddDate(0, 0, days)
	due, err := reminderLocalTime(date.Year(), date.Month(), date.Day(), hour, minute, location)
	if err != nil {
		return time.Time{}, err
	}
	if !due.After(now) {
		date = date.AddDate(0, 0, 7)
		due, err = reminderLocalTime(date.Year(), date.Month(), date.Day(), hour, minute, location)
	}
	return due, err
}

// monthByName maps a full or abbreviated month name to its time.Month. The
// pattern only hands it a name it already matched, so the zero return is
// unreachable and exists to keep the switch total.
func monthByName(name string) time.Month {
	switch strings.ToLower(name) {
	case "january", "jan":
		return time.January
	case "february", "feb":
		return time.February
	case "march", "mar":
		return time.March
	case "april", "apr":
		return time.April
	case "may":
		return time.May
	case "june", "jun":
		return time.June
	case "july", "jul":
		return time.July
	case "august", "aug":
		return time.August
	case "september", "sep", "sept":
		return time.September
	case "october", "oct":
		return time.October
	case "november", "nov":
		return time.November
	case "december", "dec":
		return time.December
	}
	return time.Month(0)
}

func parseReminderClock(value string, defaultHour, defaultMinute int) (int, int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultHour, defaultMinute, nil
	}
	for _, layout := range []string{"15:04", "3pm", "3:04pm"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.Hour(), parsed.Minute(), nil
		}
	}
	return 0, 0, ErrInvalidLaterReminder
}

func reminderLocalTime(year int, month time.Month, day, hour, minute int, location *time.Location) (time.Time, error) {
	value := time.Date(year, month, day, hour, minute, 0, 0, location)
	local := value.In(location)
	if local.Year() != year || local.Month() != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute {
		return time.Time{}, ErrInvalidLaterReminder
	}
	return value, nil
}

// ParseReminderTime reads a reminders.add `time` phrase on its own ("in 15
// minutes", "tomorrow at 9am", "every Thursday"). The phrase patterns expect
// reminder text before them, so a placeholder stands in for it; a phrase that
// leaves anything of its own in the text position is not one the grammar
// reads.
func ParseReminderTime(phrase string, now time.Time, location *time.Location) (ReminderOccurrence, error) {
	const placeholder = "reminder"
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return ReminderOccurrence{}, ErrInvalidLaterReminder
	}
	text, occurrence, err := ParseReminderExpression(placeholder+" "+phrase, now, location)
	if err != nil {
		return ReminderOccurrence{}, err
	}
	if text != placeholder {
		return ReminderOccurrence{}, ErrInvalidLaterReminder
	}
	return occurrence, nil
}
