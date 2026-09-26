package service

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Every scope an event requires is one an app can be granted. link_shared
// required links:read while auth defined no such scope, so no installation
// could ever hold it and the event could never be delivered.
func TestEveryScopeAnEventRequiresIsGrantable(t *testing.T) {
	grantable := make(map[string]bool)
	for _, scope := range auth.AllScopes() {
		grantable[scope] = true
	}
	state := memory.New()
	for _, topic := range []string{"file.created", "reaction.added", "pin.added", "star.added", "emoji.changed", "usergroup.created", "user.dnd_snoozed", "user.profile_changed", "workspace.name_changed", "link.shared"} {
		required, err := appEventRequiredScopes(context.Background(), state, events.Event{Topic: topic})
		if err != nil {
			t.Fatal(err)
		}
		if len(required) == 0 {
			t.Errorf("%s requires no scope", topic)
		}
		for _, scope := range required {
			if !grantable[scope] {
				t.Errorf("%s requires %q, which no app can be granted", topic, scope)
			}
		}
	}
}
