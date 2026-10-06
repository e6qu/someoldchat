package domain

import (
	"errors"
	"testing"
	"time"
)

// REMIND-02: a reminder set for a day lands at the member's default reminder
// time; one naming its own time, or "in" a duration, keeps that time.
func TestReminderSetForADayLandsAtTheMemberDefaultTime(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC) // a Wednesday
	early := ReminderClock{Hour: 7, Minute: 30}
	for _, testCase := range []struct {
		expression string
		want       time.Time
	}{
		{"stretch tomorrow", time.Date(2026, 7, 30, 7, 30, 0, 0, time.UTC)},
		{"stretch on Friday", time.Date(2026, 7, 31, 7, 30, 0, 0, time.UTC)},
		{"stretch on August 3", time.Date(2026, 8, 3, 7, 30, 0, 0, time.UTC)},
		{"stretch every Thursday", time.Date(2026, 7, 30, 7, 30, 0, 0, time.UTC)},
		{"stretch tomorrow at 6pm", time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)},
		{"stretch in 2 hours", now.Add(2 * time.Hour)},
	} {
		_, occurrence, err := ParseReminderExpressionAt(testCase.expression, now, time.UTC, early)
		if err != nil || !occurrence.Due.Equal(testCase.want) {
			t.Errorf("%q due=%s err=%v, want %s", testCase.expression, occurrence.Due, err, testCase.want)
		}
	}
	// Without a preference the default is Slack's 9 a.m.
	if _, occurrence, err := ParseReminderExpression("stretch tomorrow", now, time.UTC); err != nil || occurrence.Due.Hour() != 9 {
		t.Fatalf("default due=%s err=%v", occurrence.Due, err)
	}
	if occurrence, err := ParseReminderTimeAt("tomorrow", now, time.UTC, early); err != nil || !occurrence.Due.Equal(time.Date(2026, 7, 30, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("reminders.add phrase due=%s err=%v", occurrence.Due, err)
	}
}

func TestReminderDefaultClockReadsOnlyAValidPreference(t *testing.T) {
	for value, want := range map[string]ReminderClock{
		"":      DefaultReminderClock,
		"07:30": {Hour: 7, Minute: 30},
		"23:59": {Hour: 23, Minute: 59},
		"00:00": {},
		"7:30":  DefaultReminderClock,
		"24:00": DefaultReminderClock,
		"09:60": DefaultReminderClock,
		"9am":   DefaultReminderClock,
	} {
		if got := ReminderDefaultClock(map[string]string{ReminderDefaultTimePreference: value}); got != want {
			t.Errorf("%q read as %s, want %s", value, got, want)
		}
	}
	if _, _, err := NormalizeMemberPreference(ReminderDefaultTimePreference, "9am"); !errors.Is(err, ErrInvalidMemberPreference) {
		t.Fatalf("an unreadable default reminder time was kept: %v", err)
	}
	if _, value, err := NormalizeMemberPreference(ReminderDefaultTimePreference, "07:30"); err != nil || value != "07:30" {
		t.Fatalf("07:30 normalized to %q err=%v", value, err)
	}
	// Clearing it returns the member to 9 a.m.
	if _, _, err := NormalizeMemberPreference(ReminderDefaultTimePreference, ""); err != nil {
		t.Fatalf("clearing the default reminder time: %v", err)
	}
}
