package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// ConversationPropertyRepository is the part of store.Store the bulk channel
// property check drives.
type ConversationPropertyRepository interface {
	SetExistingConversationsExcludedFromAI(context.Context, domain.WorkspaceID, []domain.ConversationID, bool, events.Event) (int, error)
	ConversationsExcludedFromAI(context.Context, domain.WorkspaceID, []domain.ConversationID) ([]domain.ConversationID, error)
	ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error)
}

// CheckExistingConversationsExcludedFromAI requires a bulk property write to
// set every named channel of the workspace, skip the rest, and journal one
// event; a request naming none of the workspace's channels writes nothing,
// journals nothing, and answers ErrNotFound. The repository must hold
// workspace T1 with channels C1 and C2, and channel CX of another workspace.
func CheckExistingConversationsExcludedFromAI(t *testing.T, repository ConversationPropertyRepository) {
	t.Helper()
	ctx := context.Background()
	at := time.Unix(1_758_000_000, 0).UTC()
	event := func(id domain.EventID) events.Event {
		value, err := events.New(id, "T1", "U1", events.NewPayload("channel.ai_exclusion_set", events.Int("channels", 3)), at)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	journalled := func() int {
		records, err := repository.ListEventsAfter(ctx, "T1", 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, record := range records {
			if record.Event.Topic == "channel.ai_exclusion_set" {
				count++
			}
		}
		return count
	}
	applied, err := repository.SetExistingConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C1", "C-nobody", "CX"}, true, event("Ev-props-1"))
	if err != nil || applied != 1 {
		t.Fatalf("applied=%d err=%v, want the one channel of the workspace", applied, err)
	}
	excluded, err := repository.ConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C1", "C2"})
	if err != nil || !reflect.DeepEqual(excluded, []domain.ConversationID{"C1"}) {
		t.Fatalf("excluded=%v err=%v", excluded, err)
	}
	if got := journalled(); got != 1 {
		t.Fatalf("journalled %d events, want 1", got)
	}
	if _, err := repository.SetExistingConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C-nobody", "CX"}, false, event("Ev-props-2")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no valid channel err=%v, want ErrNotFound", err)
	}
	if got := journalled(); got != 1 {
		t.Fatalf("a refused write journalled: %d events", got)
	}
	applied, err = repository.SetExistingConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C1", "C2"}, false, event("Ev-props-3"))
	if err != nil || applied != 2 {
		t.Fatalf("applied=%d err=%v", applied, err)
	}
	if excluded, err := repository.ConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C1", "C2"}); err != nil || len(excluded) != 0 {
		t.Fatalf("excluded after inclusion=%v err=%v", excluded, err)
	}
}
