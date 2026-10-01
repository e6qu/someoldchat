package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// AgentSessionRepository is the part of store.Store the agent session check
// drives.
type AgentSessionRepository interface {
	store.AgentSessionStore
	CreateMessage(context.Context, domain.Message, events.Event, string, ...events.Event) error
	GetMessageByCreatedAt(context.Context, domain.ConversationID, time.Time) (domain.Message, error)
	ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error)
	DeleteConversation(context.Context, domain.WorkspaceID, domain.ConversationID, events.Event) error
}

// CheckAgentSessions holds every profile to one agent session contract: the
// session row is created by the first status write with that write's title
// and initiator and keeps them afterwards; each app has its own agent row; an
// identity override is replaced only by a write that carries one; a rename
// and a stop apply only to the state they were computed from and otherwise
// answer store.ErrConflict, writing nothing; the streams a stop names are
// found by ListActiveMessageStreams and ended; every mutation's events are in
// the journal; and deleting the conversation removes its sessions. The
// repository must hold workspace T1, member U1 and conversation C1.
func CheckAgentSessions(t *testing.T, repository AgentSessionRepository) {
	t.Helper()
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	sequence := 0
	event := func(topic string) events.Event {
		sequence++
		id := domain.EventID("EAS" + string(rune('a'+sequence)))
		value, err := events.New(id, "T1", "U1", events.NewPayload(topic, events.String("channel_id", "C1")), base)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	journal := func() map[domain.EventID]bool {
		records, err := repository.ListEventsAfter(ctx, "T1", 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[domain.EventID]bool{}
		for _, record := range records {
			seen[record.Event.ID] = true
		}
		return seen
	}
	thread := domain.NewMessageTimestamp(base)
	if err := repository.CreateMessage(ctx, domain.Message{ID: "MAS-root", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "root", CreatedAt: base}, event("message.created"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetAgentSession(ctx, "T1", "C1", thread); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a session nobody wrote: %v", err)
	}
	write := func(app domain.AppID, status domain.AgentSessionStatus, title string, initiator domain.UserID, identity *domain.AgentIdentity, offset time.Duration) (domain.AgentSession, events.Event) {
		t.Helper()
		value := domain.AgentSessionStatusWrite{WorkspaceID: "T1", Conversation: "C1", ThreadTimestamp: thread, AppID: app, Status: status, Title: title, InitiatorUserID: initiator, At: base.Add(offset)}
		if identity != nil {
			value.ReplaceIdentity, value.Identity = true, *identity
		}
		produced := event("agent_session.status_set")
		session, err := repository.SetAgentSessionStatus(ctx, value, produced)
		if err != nil {
			t.Fatal(err)
		}
		return session, produced
	}
	created, createdEvent := write("A2", domain.AgentSessionProcessing, "First title", "U1", &domain.AgentIdentity{IconEmoji: ":robot_face:", Username: "Agent"}, time.Second)
	if created.Title != "First title" || created.InitiatorUserID != "U1" || len(created.Agents) != 1 || created.Agents[0].Status != domain.AgentSessionProcessing ||
		created.Agents[0].Identity != (domain.AgentIdentity{IconEmoji: ":robot_face:", Username: "Agent"}) || !created.CreatedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("created=%+v", created)
	}
	// The second app gets its own row; the session keeps the creating title
	// and initiator; the agents come back ordered by app.
	second, _ := write("A1", domain.AgentSessionActive, "Ignored", "U9", nil, 2*time.Second)
	if second.Title != "First title" || second.InitiatorUserID != "U1" || len(second.Agents) != 2 || second.Agents[0].AppID != "A1" || second.Agents[1].AppID != "A2" ||
		second.Status() != domain.AgentSessionProcessing || !second.CreatedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("second=%+v", second)
	}
	// A write with no identity leaves the override; one with an identity
	// replaces all three fields.
	kept, _ := write("A2", domain.AgentSessionSuspended, "", "", nil, 3*time.Second)
	if agent, _ := kept.Agent("A2"); agent.Identity != (domain.AgentIdentity{IconEmoji: ":robot_face:", Username: "Agent"}) || agent.Status != domain.AgentSessionSuspended {
		t.Fatalf("kept=%+v", agent)
	}
	replaced, _ := write("A2", domain.AgentSessionProcessing, "", "", &domain.AgentIdentity{IconURL: "https://example.test/a.png"}, 4*time.Second)
	if agent, _ := replaced.Agent("A2"); agent.Identity != (domain.AgentIdentity{IconURL: "https://example.test/a.png"}) {
		t.Fatalf("replaced=%+v", agent)
	}
	read, err := repository.GetAgentSession(ctx, "T1", "C1", thread)
	if err != nil || len(read.Agents) != 2 || read.Agents[1].Identity.IconURL != "https://example.test/a.png" || read.Title != "First title" {
		t.Fatalf("read=%+v err=%v", read, err)
	}

	// Rename: applies to the title it was computed from, and only then.
	renameEvent := event("agent_session.renamed")
	renamed, err := repository.RenameAgentSession(ctx, domain.AgentSessionRename{WorkspaceID: "T1", Conversation: "C1", ThreadTimestamp: thread, ExpectedTitle: "First title", Title: "Second title", At: base.Add(5 * time.Second)}, []events.Event{renameEvent})
	if err != nil || renamed.Title != "Second title" {
		t.Fatalf("renamed=%+v err=%v", renamed, err)
	}
	staleEvent := event("agent_session.renamed")
	if _, err := repository.RenameAgentSession(ctx, domain.AgentSessionRename{WorkspaceID: "T1", Conversation: "C1", ThreadTimestamp: thread, ExpectedTitle: "First title", Title: "Lost race", At: base.Add(6 * time.Second)}, []events.Event{staleEvent}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a stale rename: %v", err)
	}
	if _, err := repository.RenameAgentSession(ctx, domain.AgentSessionRename{WorkspaceID: "T1", Conversation: "C1", ThreadTimestamp: "1700000099.000000", ExpectedTitle: "", Title: "Nowhere", At: base}, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("renaming a missing session: %v", err)
	}

	// Streams: an app's stream in progress in the thread is found; another
	// app's, a finished one and one in another thread are not.
	stream := func(id domain.MessageID, app domain.AppID, state string, threadTS domain.MessageTimestamp, offset time.Duration) domain.Message {
		message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", AppID: app, Text: "streaming", StreamState: state, ThreadTimestamp: threadTS, CreatedAt: base.Add(offset)}
		if err := repository.CreateMessage(ctx, message, event("message.created"), ""); err != nil {
			t.Fatal(err)
		}
		return message
	}
	active := stream("MAS-active", "A2", `{"active":true,"task_display_mode":"timeline"}`, thread, 10*time.Second)
	stream("MAS-other-app", "A1", `{"active":true}`, thread, 11*time.Second)
	stream("MAS-done", "A2", `{"active":false}`, thread, 12*time.Second)
	stream("MAS-plain", "A2", "", thread, 13*time.Second)
	found, err := repository.ListActiveMessageStreams(ctx, "C1", thread, "A2")
	if err != nil || len(found) != 1 || found[0] != domain.NewMessageTimestamp(active.CreatedAt) {
		t.Fatalf("active streams=%v err=%v", found, err)
	}

	stopped := active
	stopped.StreamState = `{"active":false,"task_display_mode":"timeline"}`
	stop := domain.AgentSessionStop{WorkspaceID: "T1", Conversation: "C1", ThreadTimestamp: thread, Agents: []domain.AppID{"A2"},
		Streams: []domain.AgentSessionStoppedStream{{Message: stopped, PreviousStreamState: active.StreamState}}}
	// A stop computed for an agent that is no longer processing writes
	// nothing, not even the stream it names.
	notProcessing := stop
	notProcessing.Agents = []domain.AppID{"A1", "A2"}
	refusedEvent := event("agent_session.stopped")
	if err := repository.StopAgentSession(ctx, notProcessing, []events.Event{refusedEvent}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stopping an agent that is not processing: %v", err)
	}
	if still, _ := repository.GetMessageByCreatedAt(ctx, "C1", active.CreatedAt); still.StreamState != active.StreamState {
		t.Fatalf("a refused stop changed the stream: %s", still.StreamState)
	}
	staleStream := stop
	staleStream.Streams = []domain.AgentSessionStoppedStream{{Message: stopped, PreviousStreamState: `{"active":true}`}}
	if err := repository.StopAgentSession(ctx, staleStream, []events.Event{event("agent_session.stopped")}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stopping a stream that moved on: %v", err)
	}
	stopEvent := event("agent_session.stopped")
	if err := repository.StopAgentSession(ctx, stop, []events.Event{stopEvent}); err != nil {
		t.Fatal(err)
	}
	if ended, _ := repository.GetMessageByCreatedAt(ctx, "C1", active.CreatedAt); ended.StreamState != stopped.StreamState {
		t.Fatalf("the stop did not end the stream: %s", ended.StreamState)
	}
	if found, _ := repository.ListActiveMessageStreams(ctx, "C1", thread, "A2"); len(found) != 0 {
		t.Fatalf("a stopped stream is still active: %v", found)
	}
	if after, _ := repository.GetAgentSession(ctx, "T1", "C1", thread); after.Status() != domain.AgentSessionProcessing {
		t.Fatalf("a stop must not move the status: %s", after.Status())
	}

	seen := journal()
	for _, committed := range []events.Event{createdEvent, renameEvent, stopEvent} {
		if !seen[committed.ID] {
			t.Errorf("event %s (%s) was not journaled with its mutation", committed.ID, committed.Topic)
		}
	}
	for _, discarded := range []events.Event{staleEvent, refusedEvent} {
		if seen[discarded.ID] {
			t.Errorf("event %s (%s) of a refused mutation was journaled", discarded.ID, discarded.Topic)
		}
	}

	if err := repository.DeleteConversation(ctx, "T1", "C1", event("conversation.deleted")); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetAgentSession(ctx, "T1", "C1", thread); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a deleted conversation's session survived: %v", err)
	}
}
