package load

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestMemoryStoreConcurrentMessageLoad(t *testing.T) {
	const (
		workers       = 24
		messagesEach  = 100
		pageSize      = 37
		expectedTotal = workers * messagesEach
	)

	ctx := context.Background()
	repository := memory.New()
	conversation := domain.ConversationID("C-load")
	if err := repository.CreateConversation(ctx, domain.Conversation{
		ID: conversation, WorkspaceID: "T-load", Name: "load", IsPrivate: false,
	}, domain.UserID("U-load"), events.Event{}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	var group sync.WaitGroup
	group.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer group.Done()
			for offset := 0; offset < messagesEach; offset++ {
				sequence := worker*messagesEach + offset
				createdAt := time.Unix(1, int64(sequence)).UTC()
				message := domain.Message{
					ID:           domain.MessageID(fmt.Sprintf("M-%06d", sequence)),
					WorkspaceID:  "T-load",
					Conversation: conversation,
					AuthorID:     "U-load",
					Text:         "load",
					CreatedAt:    createdAt,
				}
				event := events.Event{ID: domain.EventID(fmt.Sprintf("E-%06d", sequence)), WorkspaceID: "T-load", Topic: "message.created", CreatedAt: createdAt}
				if err := repository.CreateMessage(ctx, message, event, ""); err != nil {
					t.Errorf("create message %s: %v", message.ID, err)
					return
				}
			}
		}(worker)
	}
	group.Wait()

	seen := make(map[domain.MessageID]struct{}, expectedTotal)
	var cursor domain.Cursor
	for len(seen) < expectedTotal {
		page, err := repository.ListMessages(ctx, conversation, domain.PageRequest{Limit: pageSize, Cursor: cursor})
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(page.Messages) == 0 {
			t.Fatalf("pagination ended after %d messages, want %d", len(seen), expectedTotal)
		}
		for index, message := range page.Messages {
			if _, exists := seen[message.ID]; exists {
				t.Fatalf("message %s appeared twice", message.ID)
			}
			if index > 0 && !page.Messages[index-1].CreatedAt.Before(message.CreatedAt) {
				t.Fatalf("page is not ordered at %s", message.ID)
			}
			seen[message.ID] = struct{}{}
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("page claims more messages without a cursor")
		}
		cursor = page.NextCursor
	}
	if len(seen) != expectedTotal {
		t.Fatalf("got %d messages, want %d", len(seen), expectedTotal)
	}
}

func TestMemoryStoreIdempotencyIsAtomicUnderLoad(t *testing.T) {
	const callers = 128
	ctx := context.Background()
	repository := memory.New()
	conversation := domain.ConversationID("C-idempotency")
	if err := repository.CreateConversation(ctx, domain.Conversation{ID: conversation, WorkspaceID: "T-idempotency", Name: "idempotency"}, "U-idempotency", events.Event{}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	var accepted atomic.Int32
	var conflicts atomic.Int32
	var group sync.WaitGroup
	group.Add(callers)
	for caller := 0; caller < callers; caller++ {
		go func(caller int) {
			defer group.Done()
			createdAt := time.Unix(2, int64(caller)).UTC()
			err := repository.CreateMessage(ctx, domain.Message{
				ID: domain.MessageID(fmt.Sprintf("M-idempotent-%d", caller)), WorkspaceID: "T-idempotency", Conversation: conversation, AuthorID: "U-idempotency", Text: "once", CreatedAt: createdAt,
			}, events.Event{ID: domain.EventID(fmt.Sprintf("E-idempotent-%d", caller)), WorkspaceID: "T-idempotency", Topic: "message.created", CreatedAt: createdAt}, "request-1")
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, store.ErrIdempotencyConflict):
				conflicts.Add(1)
			default:
				t.Errorf("caller %d: unexpected error: %v", caller, err)
			}
		}(caller)
	}
	group.Wait()

	if accepted.Load() != 1 || conflicts.Load() != callers-1 {
		t.Fatalf("accepted %d writes and %d conflicts, want 1 and %d", accepted.Load(), conflicts.Load(), callers-1)
	}
	if _, err := repository.GetIdempotentMessage(ctx, "T-idempotency", "U-idempotency", "request-1"); err != nil {
		t.Fatalf("get idempotent message: %v", err)
	}
}

func BenchmarkMemoryStoreCreateMessage(b *testing.B) {
	repository := memory.New()
	conversation := domain.ConversationID("C-benchmark")
	if err := repository.CreateConversation(context.Background(), domain.Conversation{ID: conversation, WorkspaceID: "T-benchmark", Name: "benchmark"}, "U-benchmark", events.Event{}); err != nil {
		b.Fatal(err)
	}
	var sequence atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := sequence.Add(1)
			createdAt := time.Unix(3, int64(id)).UTC()
			err := repository.CreateMessage(context.Background(), domain.Message{
				ID: domain.MessageID(fmt.Sprintf("M-benchmark-%d", id)), WorkspaceID: "T-benchmark", Conversation: conversation, AuthorID: "U-benchmark", Text: "benchmark", CreatedAt: createdAt,
			}, events.Event{ID: domain.EventID(fmt.Sprintf("E-benchmark-%d", id)), WorkspaceID: "T-benchmark", Topic: "message.created", CreatedAt: createdAt}, "")
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
