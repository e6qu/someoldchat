package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestHuddleParticipationIsTheMembersHuddleState drives the huddle operations
// a member uses — start, join, leave, end — and reads the result back the way
// apps do: users.info carries the huddle the member is in, and each change
// journals exactly one user.huddle_changed, which reaches an app holding
// users:read and no app without it. The calls API, which cannot see a huddle,
// cannot end one or move its members either.
func TestHuddleParticipationIsTheMembersHuddleState(t *testing.T) {
	ctx, messages := huddleInviteWorld(t)
	repository := messages.Store.(*memory.Store)
	huddle, err := messages.ActiveHuddle(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	stateOf := func(user domain.UserID) domain.CallID {
		t.Helper()
		value, err := messages.UserInfo(ctx, "T1", "U3", user)
		if err != nil {
			t.Fatal(err)
		}
		if (value.HuddleCallID != "") != (value.HuddleState() == domain.HuddleStateInAHuddle) {
			t.Fatalf("%s: call %q with state %q", user, value.HuddleCallID, value.HuddleState())
		}
		return value.HuddleCallID
	}
	changes := func() []events.Record {
		t.Helper()
		records, err := repository.ListEventsAfter(ctx, "T1", 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		var found []events.Record
		for _, record := range records {
			if record.Event.Topic == events.UserHuddleChangedTopic {
				found = append(found, record)
			}
		}
		return found
	}

	if stateOf("U1") != huddle.ID || stateOf("U2") != huddle.ID || stateOf("U3") != "" {
		t.Fatalf("states after start and join: U1=%q U2=%q U3=%q", stateOf("U1"), stateOf("U2"), stateOf("U3"))
	}
	if found := changes(); len(found) != 2 || found[0].Event.ActorID != "U1" || found[1].Event.ActorID != "U2" {
		t.Fatalf("changes after start and join = %+v", found)
	}
	// Joining again, by either path, changes nothing.
	if _, err := messages.JoinHuddle(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.StartHuddle(ctx, "T1", "U2", "C1", ""); err != nil {
		t.Fatal(err)
	}
	if found := changes(); len(found) != 2 {
		t.Fatalf("a repeated join journalled %d changes", len(found)-2)
	}

	if _, err := messages.LeaveHuddle(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatal(err)
	}
	if stateOf("U2") != "" || stateOf("U1") != huddle.ID {
		t.Fatalf("after U2 left: U1=%q U2=%q", stateOf("U1"), stateOf("U2"))
	}
	if found := changes(); len(found) != 3 || !strings.Contains(found[2].Event.Payload, `"huddle_state":"default_unset"`) {
		t.Fatalf("changes after leaving = %+v", found)
	}

	// calls.end, calls.update and calls.participants.* do not reach a huddle.
	if err := messages.EndCall(ctx, "T1", "U2", huddle.ID, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("calls.end on a huddle: %v", err)
	}
	if _, err := messages.UpdateCall(ctx, "T1", "U2", huddle.ID, "renamed", "", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("calls.update on a huddle: %v", err)
	}
	if err := messages.AddCallParticipants(ctx, "T1", "U2", huddle.ID, []domain.CallParticipant{{SlackID: "U3"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("calls.participants.add on a huddle: %v", err)
	}
	if err := messages.RemoveCallParticipants(ctx, "T1", "U2", huddle.ID, []domain.CallParticipant{{SlackID: "U1"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("calls.participants.remove on a huddle: %v", err)
	}
	if running, err := messages.ActiveHuddle(ctx, "T1", "U1", "C1"); err != nil || len(running.Participants) != 1 || stateOf("U3") != "" {
		t.Fatalf("the calls API moved the huddle: %+v err=%v", running, err)
	}

	if _, err := messages.EndHuddle(ctx, "T1", "U1", "C1"); err != nil {
		t.Fatal(err)
	}
	found := changes()
	if stateOf("U1") != "" || len(found) != 4 || found[3].Event.ActorID != "U1" {
		t.Fatalf("after ending: U1=%q changes=%+v", stateOf("U1"), found)
	}

	// Delivery: users:read admits it, any other grant does not.
	at := time.Unix(1_758_000_000, 0).UTC()
	requireSeedErr(t, repository.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1", Name: "bot"}))
	requireSeedErr(t, repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "bot", UpdatedAt: at}))
	requireSeedErr(t, repository.SeedToken(ctx, "xoxb-A1", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: domain.TokenBot, Scopes: []string{"users:read"}}))
	requireSeedErr(t, repository.SeedToken(ctx, "xoxb-A2", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A2", BotID: "B2", TokenType: domain.TokenBot, Scopes: []string{"channels:read", "calls:read"}}))
	prepared, visible, err := PrepareAppEvent(ctx, repository, appEventTestKey, "https://chat.example.test", "A1", found[0])
	if err != nil || !visible {
		t.Fatalf("users:read app visible=%v err=%v", visible, err)
	}
	// The image URLs of the journalled user object are resolved for delivery,
	// as they are for user_change.
	if !strings.Contains(prepared.Event.Payload, `"image_24":"https://chat.example.test/`) || !strings.Contains(prepared.Event.Payload, `"huddle_state":"in_a_huddle"`) {
		t.Fatalf("prepared payload = %s", prepared.Event.Payload)
	}
	if _, visible, err := PrepareAppEvent(ctx, repository, appEventTestKey, "", "A2", found[0]); err != nil || visible {
		t.Fatalf("an app without users:read received user_huddle_changed: visible=%v err=%v", visible, err)
	}
}
