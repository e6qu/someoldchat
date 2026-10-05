package domain

import (
	"slices"
	"time"
)

// AdvanceReminder returns the occurrence of a recurring reminder that follows
// value, positioned by the anchor's calendar fields: its time of day, its
// day-of-month for a monthly recurrence, and its month and day for a yearly
// one. A day the target month does not have is clamped to that month's last
// day rather than overflowing into the next month, so a reminder anchored on
// the 31st falls on February 28th (29th in a leap year) and April 30th and is
// back on the 31st in March and May; one anchored on February 29th falls on
// the 28th in common years. time.AddDate would instead turn January 31st plus
// a month into March 3rd and keep the series on the 3rd from then on.
//
// weekdays are the days a weekly recurrence falls on; none means every seven
// days. Both the phrase parser (the first occurrence) and the delivery worker
// (every later one) step through this one function, so they cannot disagree.
// The zero time means the recurrence cannot be advanced.
func AdvanceReminder(recurrence ReminderRecurrence, weekdays []time.Weekday, anchor, value time.Time, location *time.Location) time.Time {
	anchor, value = anchor.In(location), value.In(location)
	hour, minute := anchor.Hour(), anchor.Minute()
	switch recurrence {
	case ReminderDaily:
		return value.AddDate(0, 0, 1)
	case ReminderWeekly:
		if len(weekdays) == 0 {
			return value.AddDate(0, 0, 7)
		}
		// The next of the named days, at the anchor's time of day.
		for days := 1; days <= 7; days++ {
			candidate := value.AddDate(0, 0, days)
			if slices.Contains(weekdays, candidate.Weekday()) {
				return time.Date(candidate.Year(), candidate.Month(), candidate.Day(), hour, minute, 0, 0, location)
			}
		}
		return time.Time{}
	case ReminderMonthly:
		year, month := value.Year(), value.Month()
		if month == time.December {
			year, month = year+1, time.January
		} else {
			month++
		}
		return time.Date(year, month, clampReminderDay(anchor.Day(), year, month), hour, minute, 0, 0, location)
	case ReminderYearly:
		year, month := value.Year()+1, anchor.Month()
		return time.Date(year, month, clampReminderDay(anchor.Day(), year, month), hour, minute, 0, 0, location)
	default:
		return time.Time{}
	}
}

// AddCalendarMonths moves value months calendar months on at the same
// wall-clock time, clamping a day the target month does not have to its last
// day: January 31st plus one month is February 28th (29th in a leap year), not
// time.AddDate's March 3rd.
func AddCalendarMonths(value time.Time, months int) time.Time {
	total := int(value.Month()) - 1 + months
	year := value.Year() + total/12
	if total%12 < 0 {
		year--
	}
	month := time.Month((total%12+12)%12 + 1)
	return time.Date(year, month, clampReminderDay(value.Day(), year, month), value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
}

// clampReminderDay bounds a day-of-month to the number of days the given
// month has, so the 31st becomes the 28th, 29th or 30th where the month is
// shorter.
func clampReminderDay(day int, year int, month time.Month) int {
	if last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day(); day > last {
		return last
	}
	return day
}

// ReminderSeriesAnchor checks the anchor a new or edited reminder carries
// against its first due instant and returns the anchor to store. A zero
// anchor, or a one-time reminder, is anchored on its due instant. Otherwise
// the due instant must be the anchor itself or the series' very next
// occurrence after it, which is the only way a phrase produces them apart
// ("every month" on the 31st once today's time has passed); anything else is
// a series that does not contain its own first occurrence.
func ReminderSeriesAnchor(recurrence ReminderRecurrence, weekdays []time.Weekday, anchor, due time.Time, location *time.Location) (time.Time, bool) {
	if anchor.IsZero() || recurrence == ReminderOnce || anchor.Equal(due) {
		return due, true
	}
	if !anchor.Before(due) || !AdvanceReminder(recurrence, weekdays, anchor, anchor, location).Equal(due) {
		return time.Time{}, false
	}
	return anchor.UTC(), true
}
