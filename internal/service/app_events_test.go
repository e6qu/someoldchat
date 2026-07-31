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

func TestPrepareAppEventHydratesOnlyConversationVisibleMessagesAndFiles(t *testing.T) {
	ctx := context.Background()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	state.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1"})
	state.SeedUser(domain.User{ID: "UO", WorkspaceID: "T1"})
	state.SeedUser(domain.User{ID: "US", WorkspaceID: "T1"})
	state.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", IsPrivate: true})
	state.SeedConversationMember("C1", "U1")
	state.SeedConversationMember("C1", "UB")
	state.SeedConversationMember("C1", "US")
	if err := state.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "member bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := state.SeedToken(ctx, "xoxb-A1", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: []string{"groups:history", "reactions:read", "files:read"}}); err != nil {
		t.Fatal(err)
	}
	if err := state.CreateBot(ctx, domain.Bot{ID: "B2", WorkspaceID: "T1", AppID: "A2", UserID: "UO", Name: "outsider bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := state.SeedToken(ctx, "xoxb-A2", domain.TokenRecord{WorkspaceID: "T1", UserID: "UO", AppID: "A2", BotID: "B2", TokenType: "bot", Scopes: []string{"groups:history", "reactions:read", "files:read"}}); err != nil {
		t.Fatal(err)
	}
	if err := state.SeedToken(ctx, "xoxb-A3", domain.TokenRecord{WorkspaceID: "T1", UserID: "US", AppID: "A3", BotID: "B3", TokenType: "bot", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1_700_000_000, 123_000_000).UTC()
	file := domain.File{ID: "F1", WorkspaceID: "T1", Uploader: "U1", Name: "report.txt", Title: "Report", MIMEType: "text/plain", BlobKey: "private/blob/key", Size: 12, CreatedAt: created}
	fileEvent, err := newEvent("T1", "U1", events.NewPayload("file.created", events.String("file_id", "F1"), events.Strings("channel_ids", []string{"C1"})), created)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CreateFile(ctx, file, fileEvent); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "private report", CreatedAt: created.Add(time.Microsecond)}
	messageEvent, err := newEvent("T1", "U1", messagePayload("message.created", message), message.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CreateFileShareMessage(ctx, []domain.FileID{"F1"}, message, []events.Event{messageEvent}); err != nil {
		t.Fatal(err)
	}

	prepared, visible, err := PrepareAppEvent(ctx, state, "A1", events.Record{Sequence: 2, Event: messageEvent})
	if err != nil || !visible {
		t.Fatalf("prepared=%+v visible=%v err=%v", prepared, visible, err)
	}
	bodies, err := events.SlackEventBodies(prepared, "A1")
	if err != nil || len(bodies) != 1 {
		t.Fatalf("bodies=%q err=%v", bodies, err)
	}
	body := string(bodies[0])
	for _, expected := range []string{`"type":"message"`, `"text":"private report"`, `"subtype":"file_share"`, `"files":[`, `"id":"F1"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("callback is missing %s: %s", expected, body)
		}
	}
	if strings.Contains(body, file.BlobKey) {
		t.Fatalf("storage blob key crossed the app boundary: %s", body)
	}

	if _, visible, err := PrepareAppEvent(ctx, state, "A2", events.Record{Sequence: 2, Event: messageEvent}); err != nil || visible {
		t.Fatalf("outsider visible=%v err=%v", visible, err)
	}
	reactionEvent, err := newEvent("T1", "U1", events.NewPayload("reaction.added",
		events.String("channel_id", "C1"), events.String("user_id", "U1"),
		events.String("ts", string(domain.NewMessageTimestamp(message.CreatedAt))), events.String("reaction", "eyes")), created)
	if err != nil {
		t.Fatal(err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, "A1", events.Record{Sequence: 3, Event: reactionEvent}); err != nil || !visible {
		t.Fatalf("member bot reaction visible=%v err=%v", visible, err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, "A2", events.Record{Sequence: 3, Event: reactionEvent}); err != nil || visible {
		t.Fatalf("outsider bot reaction visible=%v err=%v", visible, err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, "A3", events.Record{Sequence: 3, Event: reactionEvent}); err != nil || visible {
		t.Fatalf("member bot without reactions:read visible=%v err=%v", visible, err)
	}
	starEvent, err := newEvent("T1", "U1", events.NewPayload("star.added",
		events.String("channel_id", "C1"), events.String("user_id", "U1"),
		events.String("ts", string(domain.NewMessageTimestamp(message.CreatedAt)))), created)
	if err != nil {
		t.Fatal(err)
	}
	if _, visible, err := PrepareAppEvent(ctx, state, "A1", events.Record{Sequence: 4, Event: starEvent}); err != nil || visible {
		t.Fatalf("user-scoped star exposed without a user authorization: visible=%v err=%v", visible, err)
	}
	if err := state.SeedToken(ctx, "xoxp-A1-U1", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user", Scopes: []string{"stars:read"}}); err != nil {
		t.Fatal(err)
	}
	preparedStar, visible, err := PrepareAppEvent(ctx, state, "A1", events.Record{Sequence: 4, Event: starEvent})
	if err != nil || !visible {
		t.Fatalf("user-authorized star visible=%v err=%v", visible, err)
	}
	starBodies, err := events.SlackEventBodies(preparedStar, "A1")
	if err != nil || len(starBodies) != 1 || !strings.Contains(string(starBodies[0]), `"type":"star_added"`) ||
		!strings.Contains(string(starBodies[0]), `"user_id":"U1"`) || !strings.Contains(string(starBodies[0]), `"is_bot":false`) {
		t.Fatalf("user-authorized star bodies=%q err=%v", starBodies, err)
	}
	preparedFile, visible, err := PrepareAppEvent(ctx, state, "A1", events.Record{Sequence: 1, Event: fileEvent})
	if err != nil || !visible {
		t.Fatalf("prepared file=%+v visible=%v err=%v", preparedFile, visible, err)
	}
	fileBodies, err := events.SlackEventBodies(preparedFile, "A1")
	if err != nil || len(fileBodies) != 1 || !strings.Contains(string(fileBodies[0]), `"type":"file_created"`) {
		t.Fatalf("file bodies=%q err=%v", fileBodies, err)
	}
	sharedFile := file
	sharedFile.SharedChannels = []domain.ConversationID{"C1"}
	sharedEvent, err := fileEventAt("T1", "U1", "file.shared", sharedFile, "C1", created.Add(2*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	preparedShared, visible, err := PrepareAppEvent(ctx, state, "A1", events.Record{Sequence: 5, Event: sharedEvent})
	if err != nil || !visible {
		t.Fatalf("prepared shared=%+v visible=%v err=%v", preparedShared, visible, err)
	}
	sharedBodies, err := events.SlackEventBodies(preparedShared, "A1")
	if err != nil || len(sharedBodies) != 1 {
		t.Fatalf("shared bodies=%q err=%v", sharedBodies, err)
	}
	for _, fragment := range []string{`"type":"file_shared"`, `"channel_id":"C1"`, `"file_id":"F1"`, `"user_id":"U1"`} {
		if !strings.Contains(string(sharedBodies[0]), fragment) {
			t.Fatalf("file_shared missing %s: %s", fragment, sharedBodies[0])
		}
	}
}

func TestPrepareUserEventHydratesOnlyJoinedConversationMessages(t *testing.T) {
	ctx := context.Background()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	state.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	state.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", IsPrivate: true})
	state.SeedConversationMember("C1", "U1")
	created := time.Unix(1_700_000_100, 456_000_000).UTC()
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "real RTM message", CreatedAt: created}
	event, err := newEvent("T1", "U1", messagePayload("message.created", message), created)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CreateMessage(ctx, message, event, ""); err != nil {
		t.Fatal(err)
	}

	prepared, visible, err := PrepareUserEvent(ctx, state, "T1", "U1", events.Record{Sequence: 1, Event: event})
	if err != nil || !visible {
		t.Fatalf("prepared=%+v visible=%v err=%v", prepared, visible, err)
	}
	inners, err := events.RTMEvents(prepared.Event.Topic, mustDeliverable(t, prepared.Event))
	if err != nil || len(inners) != 1 {
		t.Fatalf("inners=%+v err=%v", inners, err)
	}
	body, err := inners[0].Encode()
	if err != nil || !strings.Contains(body, `"text":"real RTM message"`) || !strings.Contains(body, `"channel":"C1"`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
	if _, visible, err := PrepareUserEvent(ctx, state, "T1", "U2", events.Record{Sequence: 1, Event: event}); err != nil || visible {
		t.Fatalf("outsider visible=%v err=%v", visible, err)
	}
	reactionEvent, err := newEvent("T1", "U1", events.NewPayload("reaction.added",
		events.String("channel_id", "C1"), events.String("user_id", "U1"),
		events.String("ts", string(domain.NewMessageTimestamp(message.CreatedAt))), events.String("reaction", "eyes")), created)
	if err != nil {
		t.Fatal(err)
	}
	if _, visible, err := PrepareUserEvent(ctx, state, "T1", "U1", events.Record{Sequence: 2, Event: reactionEvent}); err != nil || !visible {
		t.Fatalf("member reaction visible=%v err=%v", visible, err)
	}
	if _, visible, err := PrepareUserEvent(ctx, state, "T1", "U2", events.Record{Sequence: 2, Event: reactionEvent}); err != nil || visible {
		t.Fatalf("outsider reaction visible=%v err=%v", visible, err)
	}
}

func TestMessageEventSnapshotsPreserveEveryMutationVersion(t *testing.T) {
	ctx := context.Background()
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	state.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1"})
	state.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", IsPrivate: true})
	state.SeedConversationMember("C1", "U1")
	state.SeedConversationMember("C1", "UB")
	if err := state.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := state.SeedToken(ctx, "xoxb-A1", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: []string{"groups:history"}}); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Unix(1_700_001_000, 123_456_000).UTC()
	original := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "version one", CreatedAt: createdAt}
	createdEvent, err := messageEventAt("T1", "message.created", original, nil, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CreateMessage(ctx, original, createdEvent, ""); err != nil {
		t.Fatal(err)
	}

	changed := original
	changed.Text = "version two"
	changeEvent, err := messageEventAt("T1", "message.changed", changed, &original, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateMessage(ctx, changed, changeEvent); err != nil {
		t.Fatal(err)
	}

	deleted := changed
	deleted.Deleted = true
	deleteEvent, err := messageEventAt("T1", "message.deleted", deleted, &changed, createdAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateMessage(ctx, deleted, deleteEvent); err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		event     events.Event
		fragments []string
	}{
		"created": {createdEvent, []string{`"text":"version one"`}},
		"changed": {changeEvent, []string{`"subtype":"message_changed"`, `"text":"version two"`, `"text":"version one"`, `"previous_message"`}},
		"deleted": {deleteEvent, []string{`"subtype":"message_deleted"`, `"text":"version two"`, `"deleted_ts":"1700001000.123456"`}},
	} {
		t.Run(name, func(t *testing.T) {
			prepared, visible, err := PrepareAppEvent(ctx, state, "A1", events.Record{Sequence: 1, Event: test.event})
			if err != nil || !visible {
				t.Fatalf("visible=%v err=%v", visible, err)
			}
			bodies, err := events.SlackEventBodies(prepared, "A1")
			if err != nil || len(bodies) != 1 {
				t.Fatalf("bodies=%q err=%v", bodies, err)
			}
			body := string(bodies[0])
			for _, fragment := range test.fragments {
				if !strings.Contains(body, fragment) {
					t.Fatalf("callback missing %s: %s", fragment, body)
				}
			}
		})
	}
}

func mustDeliverable(t *testing.T, event events.Event) events.Delivered {
	t.Helper()
	delivered, err := events.Deliverable(event)
	if err != nil {
		t.Fatal(err)
	}
	return delivered
}
