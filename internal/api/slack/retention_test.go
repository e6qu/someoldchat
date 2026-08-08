package slack

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// The workspace default governs a channel with no override, an override wins
// while it exists, and removing it hands the channel back to the default.
// getCustomRetention reports the duration that actually applies, so a caller
// never has to resolve the two and cannot resolve them differently from the
// sweep.
func TestCustomRetentionOverridesAndRevertsToTheWorkspaceDefault(t *testing.T) {
	store, mux := connectWorkspace(t)
	ctx := context.Background()

	empty := connectCall(t, mux, "/api/admin.conversations.getCustomRetention", url.Values{"channel_id": {"C1"}})
	if empty["is_policy_enabled"] != false || empty["duration_days"].(float64) != 0 {
		t.Fatalf("an unconfigured workspace reports %v, want no policy", empty)
	}

	if _, err := (service.Messages{Store: store}).SetWorkspaceRetention(ctx, "T1", "U1", domain.RetentionPolicy{MessageDays: 365, FileDays: 90}); err != nil {
		t.Fatal(err)
	}
	inherited := connectCall(t, mux, "/api/admin.conversations.getCustomRetention", url.Values{"channel_id": {"C1"}})
	if inherited["is_policy_enabled"] != true || inherited["duration_days"].(float64) != 365 || inherited["is_custom"] != false {
		t.Fatalf("a channel following the default reports %v", inherited)
	}

	if set := connectCall(t, mux, "/api/admin.conversations.setCustomRetention", url.Values{"channel_id": {"C1"}, "duration_days": {"30"}}); set["ok"] != true {
		t.Fatalf("setCustomRetention=%v", set)
	}
	custom := connectCall(t, mux, "/api/admin.conversations.getCustomRetention", url.Values{"channel_id": {"C1"}})
	if custom["duration_days"].(float64) != 30 || custom["is_custom"] != true {
		t.Fatalf("an overridden channel reports %v", custom)
	}

	if removed := connectCall(t, mux, "/api/admin.conversations.removeCustomRetention", url.Values{"channel_id": {"C1"}}); removed["ok"] != true {
		t.Fatalf("removeCustomRetention=%v", removed)
	}
	reverted := connectCall(t, mux, "/api/admin.conversations.getCustomRetention", url.Values{"channel_id": {"C1"}})
	if reverted["duration_days"].(float64) != 365 || reverted["is_custom"] != false {
		t.Fatalf("a channel whose override was removed reports %v, want the workspace default again", reverted)
	}
	// Removing an override a channel never had is not an error: the caller
	// asked for the default to govern, and it does.
	if again := connectCall(t, mux, "/api/admin.conversations.removeCustomRetention", url.Values{"channel_id": {"C1"}}); again["ok"] != true {
		t.Fatalf("removing an absent override=%v", again)
	}
}

// Slack's documented bound is an integer greater than 0 and less than 36500.
// Both ends are refused with Slack's own code, and so is a value that is not a
// number at all — the caller made one mistake and gets one answer.
func TestCustomRetentionRefusesADurationOutsideSlacksRange(t *testing.T) {
	_, mux := connectWorkspace(t)
	for name, days := range map[string]string{
		"zero":         "0",
		"negative":     "-1",
		"at maximum":   "36500",
		"past maximum": "40000",
		"not a number": "ninety",
	} {
		result := connectCall(t, mux, "/api/admin.conversations.setCustomRetention", url.Values{"channel_id": {"C1"}, "duration_days": {days}})
		if result["ok"] == true || result["error"] != "invalid_duration" {
			t.Fatalf("%s duration %q=%v, want invalid_duration", name, days, result)
		}
	}
}

// Slack refuses a custom policy on conversation types that have no
// administrator to govern them, and on the channel everyone is required to be
// in. Both are refusals about the conversation, not about the request.
func TestCustomRetentionRefusesUnsupportedConversationTypes(t *testing.T) {
	store, mux := connectWorkspace(t)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "CMPIM", WorkspaceID: "T1", Name: "group-dm", Kind: domain.ConversationTypeMPIM})
	if _, err := store.SetWorkspaceDefaultChannels(ctx, "T1", []domain.ConversationID{"C1"}, events.Event{
		ID: "E-defaults", WorkspaceID: "T1", Topic: "workspace.default_channels_changed",
		Payload: `{"type":"workspace.default_channels_changed"}`, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"CMPIM", "C1"} {
		result := connectCall(t, mux, "/api/admin.conversations.setCustomRetention", url.Values{"channel_id": {channel}, "duration_days": {"30"}})
		if result["ok"] == true || result["error"] != "channel_type_not_supported" {
			t.Fatalf("%s=%v, want channel_type_not_supported", channel, result)
		}
	}
}
