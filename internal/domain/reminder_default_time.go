package domain

import (
	"fmt"
	"strings"
	"time"
)

// ReminderClock is a time of day a reminder set for a day is delivered at.
type ReminderClock struct {
	Hour   int
	Minute int
}

// DefaultReminderClock is Slack's default: "By default, reminders you set for
// a specific day (like “tomorrow” or “every Tuesday”) will be delivered at 9
// a.m. in your time zone" ("Set a reminder").
var DefaultReminderClock = ReminderClock{Hour: 9}

// ReminderDefaultTimePreference is the member preference behind Preferences'
// "Set a default time for reminder notifications". Its value is a 24-hour
// HH:MM; absent, the default is DefaultReminderClock.
const ReminderDefaultTimePreference = "reminder-default-time"

// String is the clock as the preference stores it.
func (c ReminderClock) String() string {
	return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute)
}

// ParseReminderClock reads a stored HH:MM, refusing anything else.
func ParseReminderClock(value string) (ReminderClock, bool) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil || len(strings.TrimSpace(value)) != len("15:04") {
		return ReminderClock{}, false
	}
	return ReminderClock{Hour: parsed.Hour(), Minute: parsed.Minute()}, true
}

// ReminderDefaultClock reads the member's default reminder time from their
// preferences, falling back to Slack's 9 a.m.
func ReminderDefaultClock(preferences map[string]string) ReminderClock {
	if clock, ok := ParseReminderClock(preferences[ReminderDefaultTimePreference]); ok {
		return clock
	}
	return DefaultReminderClock
}
