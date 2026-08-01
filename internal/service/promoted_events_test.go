package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestPromotedProducerPayloadsTranslateEndToEnd drives real service mutations
// and asserts the journal records they mint translate into the promised Slack
// events. This is the producer↔builder seam: the builder tests in
// internal/events pin the payload contract, and this test proves the
// producers actually honor it — a field dropped in a producer fails here, not
// in a customer's webhook.
func TestPromotedProducerPayloadsTranslateEndToEnd(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", RealName: "Alice"}),
		repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob"}),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	if err := repository.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleOwner); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository}

	// One helper reads the whole journal fresh each time: sequence numbers
	// keep growing across the mutations below.
	lastEventOf := func(topic string) events.Event {
		t.Helper()
		records, err := repository.ListEventsAfter(ctx, "T1", 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		for index := len(records) - 1; index >= 0; index-- {
			if records[index].Event.Topic == topic {
				return records[index].Event
			}
		}
		t.Fatalf("no %s event in the journal", topic)
		return events.Event{}
	}
	translated := func(topic string, surface events.Surface) []events.Inner {
		t.Helper()
		event := lastEventOf(topic)
		delivered, err := events.Broadcastable(event)
		if err != nil {
			t.Fatalf("%s: %v", topic, err)
		}
		inners, err := events.SlackInner(topic, delivered, surface)
		if err != nil {
			t.Fatalf("%s: %v", topic, err)
		}
		if len(inners) == 0 {
			t.Fatalf("%s produced a record its own builder withholds", topic)
		}
		return inners
	}
	firstEncoded := func(topic string, surface events.Surface, wantType string, fragments ...string) {
		t.Helper()
		inners := translated(topic, surface)
		if inners[0].Type() != wantType {
			t.Fatalf("%s translated to %s, want %s", topic, inners[0].Type(), wantType)
		}
		encoded, err := inners[0].Encode()
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(encoded, fragment) {
				t.Fatalf("%s inner %s lacks %s", topic, encoded, fragment)
			}
		}
	}

	// Conversation lifecycle: public create/rename/archive, and the private
	// rename that must travel under the group_* vocabulary.
	public, err := messages.CreateConversation(ctx, "T1", "U1", "announcements", false)
	if err != nil {
		t.Fatal(err)
	}
	firstEncoded("conversation.created", events.SurfaceEventsAPI, "channel_created",
		`"name":"announcements"`, `"creator":"U1"`, `"is_private":false`)
	if _, err := messages.RenameConversation(ctx, "T1", "U1", public.ID, "bulletins"); err != nil {
		t.Fatal(err)
	}
	firstEncoded("conversation.renamed", events.SurfaceEventsAPI, "channel_rename", `"name":"bulletins"`)
	if _, err := messages.SetConversationArchived(ctx, "T1", "U1", public.ID, true); err != nil {
		t.Fatal(err)
	}
	firstEncoded("conversation.archived", events.SurfaceEventsAPI, "channel_archive",
		`"channel":"`+string(public.ID)+`"`, `"user":"U1"`)

	private, err := messages.CreateConversation(ctx, "T1", "U1", "secrets", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RenameConversation(ctx, "T1", "U1", private.ID, "classified"); err != nil {
		t.Fatal(err)
	}
	firstEncoded("conversation.renamed", events.SurfaceEventsAPI, "group_rename", `"name":"classified"`)

	// Emoji: the add carries the image URL, the rename both names.
	if err := messages.AdminAddEmoji(ctx, "T1", "U1", "party", "https://emoji.test/party.png"); err != nil {
		t.Fatal(err)
	}
	firstEncoded("emoji.added", events.SurfaceSocketMode, "emoji_changed",
		`"subtype":"add"`, `"name":"party"`, `"value":"https://emoji.test/party.png"`)
	if err := messages.AdminRenameEmoji(ctx, "T1", "U1", "party", "celebrate"); err != nil {
		t.Fatal(err)
	}
	firstEncoded("emoji.renamed", events.SurfaceSocketMode, "emoji_changed",
		`"subtype":"rename"`, `"old_name":"party"`, `"new_name":"celebrate"`)

	// User groups: creation snapshots the subteam object, membership changes
	// speak in deltas.
	group, err := messages.CreateUserGroup(ctx, "T1", "U1", "oncall", "", "Handles incidents")
	if err != nil {
		t.Fatal(err)
	}
	firstEncoded("usergroup.created", events.SurfaceEventsAPI, "subteam_created",
		`"handle":"oncall"`, `"team_id":"T1"`)
	if _, err := messages.SetUserGroupUsers(ctx, "T1", "U1", group.ID, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	firstEncoded("usergroup.users_changed", events.SurfaceEventsAPI, "subteam_members_changed",
		`"added_users":["U2"]`, `"removed_users":[]`, `"subteam_id":"`+string(group.ID)+`"`)

	// Profile change: the record fans out to the current catalog names, and a
	// status change adds the third.
	profile := domain.UserProfile{DisplayName: "Bobby", StatusText: "away fishing", StatusEmoji: ":fish:"}
	if _, err := messages.SetUserProfile(ctx, "T1", "U2", profile); err != nil {
		t.Fatal(err)
	}
	inners := translated("user.profile_changed", events.SurfaceEventsAPI)
	types := make([]string, 0, len(inners))
	for _, inner := range inners {
		types = append(types, inner.Type())
	}
	if len(types) != 3 || types[0] != "user_change" || types[1] != "user_profile_changed" || types[2] != "user_status_changed" {
		t.Fatalf("profile change fan-out=%v", types)
	}
	encoded, err := inners[0].Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, `"status_text":"away fishing"`) || strings.Contains(encoded, `"email"`) {
		t.Fatalf("user_change user object wrong or leaking: %s", encoded)
	}

	// DND: snoozing carries the dnd_status object.
	if _, err := messages.SetSnooze(ctx, "T1", "U2", 30); err != nil {
		t.Fatal(err)
	}
	firstEncoded("user.dnd_snoozed", events.SurfaceEventsAPI, "dnd_updated",
		`"user":"U2"`, `"snooze_enabled":true`, `"snooze_endtime":`)

	// Workspace rename.
	if _, err := messages.AdminSetWorkspaceName(ctx, "T1", "U1", "Renamed Workspace"); err != nil {
		t.Fatal(err)
	}
	firstEncoded("workspace.name_changed", events.SurfaceEventsAPI, "team_rename", `"name":"Renamed Workspace"`)
}

// TestPromotedEventsHonorScopeAndLifecycleVisibility pins the app-delivery
// decisions the promotions depend on: dnd_updated needs dnd:read (the old
// prefix switch matched nothing and let users:read through), team_rename
// needs team:read (it used to require nothing), a PUBLIC channel_created
// reaches an app whose bot is in no channel at all — a new channel has no
// members, and Slack addresses the event to the workspace — while a private
// lifecycle record stays invisible to a bot outside the room.
func TestPromotedEventsHonorScopeAndLifecycleVisibility(t *testing.T) {
	ctx := context.Background()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	state.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1"})
	if err := state.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := state.SeedToken(ctx, "xoxb-A1", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: []string{"channels:read", "dnd:read", "team:read"}}); err != nil {
		t.Fatal(err)
	}
	if err := state.SeedToken(ctx, "xoxb-A2", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A2", BotID: "B2", TokenType: "bot", Scopes: []string{"users:read", "channels:read", "groups:read"}}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0).UTC()

	dndEvent, err := newEvent("T1", "U1", dndEventPayload("user.dnd_snoozed", "U1", domain.DoNotDisturb{WorkspaceID: "T1", UserID: "U1", SnoozeUntil: at.Add(time.Hour)}, at), at)
	if err != nil {
		t.Fatal(err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, appEventTestKey, "A1", events.Record{Sequence: 1, Event: dndEvent}); err != nil || !visible {
		t.Fatalf("dnd:read app visible=%v err=%v", visible, err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, appEventTestKey, "A2", events.Record{Sequence: 1, Event: dndEvent}); err != nil || visible {
		t.Fatalf("users:read must not admit dnd_updated: visible=%v err=%v", visible, err)
	}

	renameEvent, err := newEvent("T1", "U1", events.NewPayload("workspace.name_changed", events.String("name", "Renamed")), at)
	if err != nil {
		t.Fatal(err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, appEventTestKey, "A1", events.Record{Sequence: 2, Event: renameEvent}); err != nil || !visible {
		t.Fatalf("team:read app visible=%v err=%v", visible, err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, appEventTestKey, "A2", events.Record{Sequence: 2, Event: renameEvent}); err != nil || visible {
		t.Fatalf("team_rename without team:read: visible=%v err=%v", visible, err)
	}

	// Public lifecycle: the bot is a member of nothing, and must still see it.
	state.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	createdEvent, err := conversationLifecycleEvent("T1", "conversation.created", domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}, "U1")
	if err != nil {
		t.Fatal(err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, appEventTestKey, "A1", events.Record{Sequence: 3, Event: createdEvent}); err != nil || !visible {
		t.Fatalf("public channel_created hidden from channels:read app: visible=%v err=%v", visible, err)
	}

	// Private lifecycle: existence and name stay inside the room.
	state.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "secrets", IsPrivate: true})
	privateRename, err := conversationLifecycleEvent("T1", "conversation.renamed", domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "classified", IsPrivate: true}, "U1")
	if err != nil {
		t.Fatal(err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, appEventTestKey, "A2", events.Record{Sequence: 4, Event: privateRename}); err != nil || visible {
		t.Fatalf("private rename leaked to a bot outside the room: visible=%v err=%v", visible, err)
	}
}
