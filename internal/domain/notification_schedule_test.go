package domain

import (
	"testing"
	"time"
)

func schedule(days []time.Weekday, start, end int) NotificationSchedule {
	return NotificationSchedule{Enabled: true, Days: days, StartMinute: start, EndMinute: end, TimeZone: "Europe/Berlin"}
}

func at(t *testing.T, value string) time.Time {
	t.Helper()
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("zone data unavailable: %v", err)
	}
	instant, err := time.ParseInLocation("2006-01-02 15:04", value, location)
	if err != nil {
		t.Fatal(err)
	}
	return instant.UTC()
}

// The ordinary window, and the boundaries. The start is inclusive and the end
// exclusive, so a window ending at 18:00 does not notify at 18:00 — the same
// convention every other range in this product uses.
func TestAScheduleAllowsItsOwnWindow(t *testing.T) {
	weekdays := schedule([]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}, 9*60, 18*60)
	for name, moment := range map[string]struct {
		when  string
		allow bool
	}{
		"inside the window":  {"2026-08-05 12:00", true},
		"at the start":       {"2026-08-05 09:00", true},
		"at the end":         {"2026-08-05 18:00", false},
		"before the window":  {"2026-08-05 08:59", false},
		"after the window":   {"2026-08-05 18:01", false},
		"on an excluded day": {"2026-08-08 12:00", false},
		"midnight on Sunday": {"2026-08-09 00:00", false},
	} {
		if got := weekdays.AllowsAt(at(t, moment.when)); got != moment.allow {
			t.Fatalf("%s: AllowsAt(%s) = %v, want %v", name, moment.when, got, moment.allow)
		}
	}
}

// A window whose end is before its start runs overnight. Reading it as empty
// would silence a member who asked to be reachable at night, and the day it
// belongs to is the day it began on — a Friday-night window keeps notifying
// into Saturday morning without Saturday being selected.
func TestAnOvernightScheduleBelongsToTheDayItStarted(t *testing.T) {
	nights := schedule([]time.Weekday{time.Friday}, 22*60, 7*60)
	for name, moment := range map[string]struct {
		when  string
		allow bool
	}{
		"Friday night":               {"2026-08-07 23:30", true},
		"Saturday morning after it":  {"2026-08-08 06:30", true},
		"Saturday at the end":        {"2026-08-08 07:00", false},
		"Saturday evening":           {"2026-08-08 23:30", false},
		"Sunday morning":             {"2026-08-09 06:30", false},
		"Friday afternoon before it": {"2026-08-07 21:00", false},
	} {
		if got := nights.AllowsAt(at(t, moment.when)); got != moment.allow {
			t.Fatalf("%s: AllowsAt(%s) = %v, want %v", name, moment.when, got, moment.allow)
		}
	}
}

// The schedule is a statement about the member's day, so it is read in their
// zone rather than the server's. The same instant is inside the window for one
// member and outside it for another.
func TestAScheduleIsReadInItsOwnTimeZone(t *testing.T) {
	// 17:30 in Berlin is 00:30 the next day in Tokyo, so the same instant is
	// inside one member's window and outside another's — by hour and by day.
	instant := at(t, "2026-08-05 17:30")
	berlin := schedule([]time.Weekday{time.Wednesday}, 9*60, 18*60)
	if !berlin.AllowsAt(instant) {
		t.Fatal("the member's own zone did not allow their own window")
	}
	tokyo := berlin
	tokyo.TimeZone = "Asia/Tokyo"
	if tokyo.AllowsAt(instant) {
		t.Fatal("the same instant was allowed in a zone where it is 00:30 on Thursday")
	}
}

// Off allows everything, which is what off means, and an unresolvable zone
// fails open: the cost of that is a notification that should have waited, and
// the cost of failing closed is one that never arrives at all.
func TestAScheduleFailsOpen(t *testing.T) {
	off := NotificationSchedule{}
	if !off.AllowsAt(time.Now()) {
		t.Fatal("a schedule that is off suppressed a notification")
	}
	broken := schedule([]time.Weekday{time.Monday}, 9*60, 18*60)
	broken.TimeZone = "Not/AZone"
	if !broken.AllowsAt(time.Now()) {
		t.Fatal("an unresolvable zone silenced a member")
	}
}

func TestAScheduleRefusesWhatItCannotMean(t *testing.T) {
	for name, value := range map[string]NotificationSchedule{
		"no days at all":      {Enabled: true, StartMinute: 9 * 60, EndMinute: 18 * 60, TimeZone: "UTC"},
		"an empty window":     {Enabled: true, Days: []time.Weekday{time.Monday}, StartMinute: 9 * 60, EndMinute: 9 * 60, TimeZone: "UTC"},
		"a repeated day":      {Enabled: true, Days: []time.Weekday{time.Monday, time.Monday}, StartMinute: 9 * 60, EndMinute: 18 * 60, TimeZone: "UTC"},
		"a minute past a day": {Enabled: true, Days: []time.Weekday{time.Monday}, StartMinute: 0, EndMinute: NotificationScheduleDayMinutes, TimeZone: "UTC"},
		"an unknown zone":     {Enabled: true, Days: []time.Weekday{time.Monday}, StartMinute: 9 * 60, EndMinute: 18 * 60, TimeZone: "Not/AZone"},
	} {
		if value.Valid() {
			t.Fatalf("%s was accepted", name)
		}
	}
	if !(NotificationSchedule{}).Valid() {
		t.Fatal("a schedule that is off was refused")
	}
}
