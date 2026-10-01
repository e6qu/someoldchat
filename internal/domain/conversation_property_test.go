package domain

import (
	"errors"
	"testing"
)

func TestParseConversationPropertyUsesTheReferenceCodes(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"exclude_from_slack_ai": true }`: true,
		`{"exclude_from_slack_ai":false}`:  false,
	} {
		got, err := ParseConversationProperty(raw)
		if err != nil || got.ExcludeFromSlackAI != want {
			t.Fatalf("ParseConversationProperty(%s) = %+v, %v", raw, got, err)
		}
	}
	for raw, want := range map[string]error{
		`not json`:                        ErrInvalidConversationProperty,
		`[]`:                              ErrInvalidConversationProperty,
		`null`:                            ErrInvalidConversationProperty,
		`{}`:                              ErrInvalidConversationProperty,
		`{"a":1}{"b":2}`:                  ErrInvalidConversationProperty,
		`{"exclude_from_slack_ai":"yes"}`: ErrInvalidConversationProperty,
		`{"exclude_from_slack_ai":true,"is_private":true}`: ErrTooManyConversationProperties,
		`{"is_private":true}`:                              ErrConversationPropertyNotAllowed,
	} {
		if _, err := ParseConversationProperty(raw); !errors.Is(err, want) {
			t.Fatalf("ParseConversationProperty(%s) err=%v, want %v", raw, err, want)
		}
	}
}
