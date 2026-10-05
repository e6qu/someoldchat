package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// A member's huddle_state is the running huddle they are in, and it changes in
// the transaction that moves them: starting or joining puts them in, leaving
// takes them out — back into another huddle they are still in, if any — and a
// huddle ending releases everyone still in it. Each change journals exactly one
// user.huddle_changed whose user object is the member as the change leaves
// them; a join that finds the member already in changes nothing and journals
// nothing. A huddle's participants cannot be replaced through the calls API's
// SetCallParticipants, which would move members without their state. Every
// profile must agree, because users.info and the event are read from whichever
// one a deployment chose.
func huddleStateFollowsTheMember(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	second := f.secondMember(t, ctx)
	elsewhere := domain.ConversationID("C-huddle-elsewhere-" + f.suffix)
	if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: elsewhere, WorkspaceID: f.workspaceID, Name: "elsewhere-" + f.suffix}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SeedConversationMember(ctx, elsewhere, f.userID); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_900, 0).UTC()
	start := func(name string, conversation domain.ConversationID, actor domain.UserID, started time.Time) domain.Call {
		t.Helper()
		call := domain.Call{
			ID: domain.CallID(name + "-" + f.suffix), WorkspaceID: f.workspaceID, Kind: domain.CallKindHuddle,
			ConversationID: conversation, CreatedBy: actor, StartedAt: started,
		}
		value, _, err := f.repository.StartHuddle(ctx, call,
			f.event(name+"-started", "huddle.started", string(call.ID)),
			f.event(name+"-joined", "huddle.joined", string(call.ID)),
			domain.Message{
				ID: domain.MessageID("M-" + name + "-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: conversation,
				AuthorID: actor, Subtype: domain.MessageSubtypeHuddleThread, CreatedAt: started, Attachments: "[]",
			})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return value
	}
	stateOf := func(user domain.UserID) domain.CallID {
		t.Helper()
		value, err := f.repository.GetUser(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		return value.HuddleCallID
	}
	type change struct {
		user  domain.UserID
		state string
		call  domain.CallID
	}
	changes := func() []change {
		t.Helper()
		records, err := f.repository.ListEventsAfter(ctx, f.workspaceID, 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		var found []change
		for _, record := range records {
			if record.Event.Topic != events.UserHuddleChangedTopic {
				continue
			}
			var payload struct {
				UserID string `json:"user_id"`
				User   struct {
					Profile struct {
						HuddleState string `json:"huddle_state"`
						CallID      string `json:"huddle_state_call_id"`
					} `json:"profile"`
				} `json:"user"`
			}
			if err := json.Unmarshal([]byte(record.Event.Payload), &payload); err != nil {
				t.Fatalf("payload %q: %v", record.Event.Payload, err)
			}
			found = append(found, change{domain.UserID(payload.UserID), payload.User.Profile.HuddleState, domain.CallID(payload.User.Profile.CallID)})
		}
		return found
	}
	expect := func(step string, want ...change) {
		t.Helper()
		got := changes()
		if len(got) != len(want) {
			t.Fatalf("%s: changes=%+v, want %+v", step, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("%s: changes=%+v, want %+v", step, got, want)
			}
		}
	}
	in := func(user domain.UserID, call domain.CallID) change {
		return change{user, domain.HuddleStateInAHuddle, call}
	}
	out := func(user domain.UserID) change { return change{user, domain.HuddleStateDefaultUnset, ""} }

	if state := stateOf(f.userID); state != "" {
		t.Fatalf("a new member starts in huddle %q", state)
	}
	first := start("state-a", f.channelID, f.userID, at)
	if stateOf(f.userID) != first.ID {
		t.Fatalf("the starter is not in the huddle they started")
	}
	start("state-a-again", f.channelID, second, at)
	if stateOf(second) != first.ID {
		t.Fatalf("a member who joined by starting is not in the running huddle")
	}
	// Joining a huddle one is already in changes nothing.
	if _, err := f.repository.JoinCall(ctx, f.workspaceID, first.ID, second, f.event("state-rejoin", "huddle.joined", string(first.ID))); err != nil {
		t.Fatal(err)
	}
	expect("after both joined", in(f.userID, first.ID), in(second, first.ID))

	// A second huddle elsewhere becomes the member's state; leaving it — which
	// ends it, as they were alone — puts them back in the first.
	other := start("state-b", elsewhere, f.userID, at.Add(time.Minute))
	if stateOf(f.userID) != other.ID {
		t.Fatalf("state=%q, want the huddle just joined %q", stateOf(f.userID), other.ID)
	}
	if _, err := f.repository.LeaveCall(ctx, f.workspaceID, other.ID, f.userID,
		f.event("state-b-left", "huddle.left", string(other.ID)), f.event("state-b-ended", "huddle.ended", string(other.ID))); err != nil {
		t.Fatal(err)
	}
	if stateOf(f.userID) != first.ID {
		t.Fatalf("state=%q, want the huddle still running %q", stateOf(f.userID), first.ID)
	}

	if _, err := f.repository.LeaveCall(ctx, f.workspaceID, first.ID, second,
		f.event("state-a-left", "huddle.left", string(first.ID)), f.event("state-a-ended", "huddle.ended", string(first.ID))); err != nil {
		t.Fatal(err)
	}
	if stateOf(second) != "" || stateOf(f.userID) != first.ID {
		t.Fatalf("after one left: left=%q stayed=%q", stateOf(second), stateOf(f.userID))
	}

	// The calls API cannot replace a huddle's participants.
	if err := f.repository.SetCallParticipants(ctx, f.workspaceID, first.ID, []domain.UserID{second}, nil,
		f.event("state-a-replaced", "call.participants_changed", string(first.ID))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replacing a huddle's participants: %v, want ErrNotFound", err)
	}

	ending := f.event("state-a-end", "huddle.ended", string(first.ID))
	ending.ActorID = second
	if err := f.repository.EndCall(ctx, f.workspaceID, first.ID, 60, ending); err != nil {
		t.Fatal(err)
	}
	if stateOf(f.userID) != "" {
		t.Fatalf("an ended huddle kept %q in it", f.userID)
	}
	expect("after it ended",
		in(f.userID, first.ID), in(second, first.ID),
		in(f.userID, other.ID), in(f.userID, first.ID),
		out(second), out(f.userID))
}
