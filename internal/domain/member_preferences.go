package domain

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// A member's preferences follow them to every client, as Slack's do: the
// web client's appearance, sidebar, mark-as-read and composer choices are
// kept for the member rather than for one browser. A preference is an opaque
// name and value the client defines; the server bounds them and keeps them
// apart per member and workspace.

// The bounds of a member's preferences.
const (
	MemberPreferenceLimit      = 200
	memberPreferenceValueLimit = 1024
)

// ErrInvalidMemberPreference is a preference outside its bounds.
var ErrInvalidMemberPreference = errors.New("invalid member preference")

// memberPreferenceName is a lower-case name, optionally qualified by the one
// thing it is about, as "section-sort:starred" is the starred section's sort.
var memberPreferenceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}(:[A-Za-z0-9_.-]{1,64})?$`)

// NormalizeMemberPreference checks a preference's name and value. A value
// carries no control characters, which nothing a client sets needs.
func NormalizeMemberPreference(name, value string) (string, string, error) {
	name = strings.TrimSpace(name)
	if !memberPreferenceName.MatchString(name) || !utf8.ValidString(value) || utf8.RuneCountInString(value) > memberPreferenceValueLimit {
		return "", "", ErrInvalidMemberPreference
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", "", ErrInvalidMemberPreference
		}
	}
	// The reminder default time is read by the server when it parses a
	// reminder, so a value it could not read is refused rather than stored
	// and silently ignored.
	if name == ReminderDefaultTimePreference && value != "" {
		if _, ok := ParseReminderClock(value); !ok {
			return "", "", ErrInvalidMemberPreference
		}
	}
	return name, value, nil
}

// LanguagePreference is the member preference Language & region's picker
// writes: the locale the member reads the product in. It is the one
// preference the server reads about a member other than the caller, because
// Slack's user object reports it as the member's locale (users.info and
// users.list with include_locale).
const LanguagePreference = "language"

// hiddenPersonPreference prefixes the preference that records a person the
// member has hidden, as Slack's "Hide a person" does. It is the member's own
// preference: nobody else, administrators included, can read it.
const hiddenPersonPreference = "hidden-person:"

// HiddenPersonPreference names the preference that hides person.
func HiddenPersonPreference(person UserID) string {
	return hiddenPersonPreference + string(person)
}

// HiddenPeople reads the people a member has hidden from their preferences.
func HiddenPeople(preferences map[string]string) map[UserID]bool {
	hidden := make(map[UserID]bool)
	for name, value := range preferences {
		if person, ok := strings.CutPrefix(name, hiddenPersonPreference); ok && person != "" && value == "true" {
			hidden[UserID(person)] = true
		}
	}
	return hidden
}
