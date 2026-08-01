package events

import (
	"testing"
)

// TestWithheldTopicsAreExactlyTheMessageProjectionRows is the shrink-only
// gate over the topic table. A row with an event name and no builder is a
// mapping this repository knows about and deliberately does not render —
// today that is exactly the message family, whose Slack bodies are written by
// the per-app projection in internal/service/app_events.go after conversation
// visibility is proved, never by a builder that cannot look content up.
//
// Promoting a topic removes it from this list; adding a new withheld mapping
// adds it here, with the decision that keeps it withheld recorded in the
// table's note. What this test forbids is the silent state in between, where
// a topic gains an event name nobody can produce and nobody decided to defer.
func TestWithheldTopicsAreExactlyTheMessageProjectionRows(t *testing.T) {
	withheld := map[string]bool{
		// message.created carries a builder now — it derives the app_mention
		// companion — so only its siblings remain purely projection-shaped.
		"message.changed":     true,
		"message.deleted":     true,
		"message.unfurled":    true,
		EphemeralMessageTopic: true,
		// The file rows are projection-written for the same reason as the
		// message rows: the file object is hydrated per app only after access
		// is proved (service.prepareAppFileEvent).
		"file.created":  true,
		"file.shared":   true,
		"file.unshared": true,
	}
	for _, rule := range topicRules {
		mapped := rule.slack.eventType != "" && rule.slack.build == nil
		if mapped && !withheld[rule.topic] {
			t.Errorf("topic %s maps to %s with no builder and no recorded deferral", rule.topic, rule.slack.eventType)
		}
		if !mapped && withheld[rule.topic] {
			t.Errorf("topic %s is no longer withheld; remove it from this gate", rule.topic)
		}
	}
}

// Every alternate an event may travel under must itself be a name, and a rule
// must not declare its primary name twice.
func TestAlternateEventNamesAreWellFormed(t *testing.T) {
	for _, rule := range topicRules {
		for _, alternate := range rule.slack.alternates {
			if alternate == "" {
				t.Errorf("topic %s declares an empty alternate event name", rule.topic)
			}
			if alternate == rule.slack.eventType {
				t.Errorf("topic %s declares its primary event %s as an alternate", rule.topic, alternate)
			}
			if rule.slack.build == nil {
				t.Errorf("topic %s declares alternates without a builder to choose among them", rule.topic)
			}
		}
	}
}
