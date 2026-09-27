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

// MessageCompanionRepository is the part of store.Store the companion-event
// check drives.
type MessageCompanionRepository interface {
	CreateMessage(context.Context, domain.Message, events.Event, string, ...events.Event) error
	UpdateMessage(context.Context, domain.Message, events.Event, ...events.Event) error
	ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error)
}

// CheckMessageCompanionEventsCommitWithTheMessage requires the companion
// events of a message write - link.shared for the links a message shares - to
// be journalled after the announcement, in order, in the same transaction: a
// write that fails journals none of them. The repository must hold workspace
// T1, member U1 and public conversation C1 that U1 belongs to.
func CheckMessageCompanionEventsCommitWithTheMessage(t *testing.T, repository MessageCompanionRepository) {
	t.Helper()
	ctx := context.Background()
	created := time.Unix(1_700_000_000, 0).UTC()
	record := func(id domain.EventID, topic string) events.Event {
		event, err := events.New(id, "T1", "U1", events.NewPayload(topic,
			events.String("channel_id", "C1"), events.String("message_id", "M1")), created)
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "https://example.com", CreatedAt: created}
	if err := repository.CreateMessage(ctx, message, record("E-created", "message.created"), "", record("E-shared", "link.shared")); err != nil {
		t.Fatal(err)
	}
	// The same instant is taken, so the write fails and journals nothing.
	contested := message
	contested.ID = "M2"
	if err := repository.CreateMessage(ctx, contested, record("E-contested", "message.created"), "", record("E-contested-shared", "link.shared")); !errors.Is(err, store.ErrMessageTimestampTaken) {
		t.Fatalf("contested create err=%v", err)
	}
	message.Text = "https://example.com https://example.org"
	if err := repository.UpdateMessage(ctx, message, record("E-changed", "message.changed"), record("E-edit-shared", "link.shared")); err != nil {
		t.Fatal(err)
	}
	records, err := repository.ListEventsAfter(ctx, "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var journalled []domain.EventID
	for _, value := range records {
		journalled = append(journalled, value.Event.ID)
	}
	want := []domain.EventID{"E-created", "E-shared", "E-changed", "E-edit-shared"}
	if len(journalled) != len(want) {
		t.Fatalf("journalled %v, want %v", journalled, want)
	}
	for index := range want {
		if journalled[index] != want[index] {
			t.Fatalf("journalled %v, want %v", journalled, want)
		}
	}
}
