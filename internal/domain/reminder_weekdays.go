package domain

import (
	"slices"
	"strings"
	"time"
)

// reminderWeekdayNames are the day names reminders.add's recurrence.weekdays
// takes, which are also how a reminder's weekdays are stored.
var reminderWeekdayNames = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

// ParseReminderWeekday reads one of reminders.add's weekday names.
func ParseReminderWeekday(name string) (time.Weekday, bool) {
	day, ok := reminderWeekdayNames[strings.ToLower(strings.TrimSpace(name))]
	return day, ok
}

// NormalizeReminderWeekdays orders the days a weekly reminder recurs on from
// Sunday and drops repeats, so one set has one representation.
func NormalizeReminderWeekdays(days []time.Weekday) []time.Weekday {
	if len(days) == 0 {
		return nil
	}
	normalized := slices.Clone(days)
	slices.Sort(normalized)
	return slices.Compact(normalized)
}

// EncodeReminderWeekdays is the stored form of a weekday set: the lower-case
// day names joined by commas, empty for a reminder that names none.
func EncodeReminderWeekdays(days []time.Weekday) string {
	names := make([]string, 0, len(days))
	for _, day := range NormalizeReminderWeekdays(days) {
		names = append(names, strings.ToLower(day.String()))
	}
	return strings.Join(names, ",")
}

// DecodeReminderWeekdays reads EncodeReminderWeekdays's form back. A name it
// does not know is skipped rather than failing the read of the reminder.
func DecodeReminderWeekdays(encoded string) []time.Weekday {
	var days []time.Weekday
	for _, name := range strings.Split(encoded, ",") {
		if day, ok := ParseReminderWeekday(name); ok {
			days = append(days, day)
		}
	}
	return NormalizeReminderWeekdays(days)
}
