package sqlstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// Message creation is the hottest write in the product and the one every wave
// in the phase6 stack touched. Each call writes the message and enqueues a
// durable outbox event in one transaction, so these benchmarks measure that
// pair rather than either half alone. Blocks and attachments are varied because
// they change the row size, not the statement count.

func benchStore(b *testing.B, name string) *Store {
	b.Helper()
	ctx := context.Background()
	store, err := Open(ctx, fmt.Sprintf("file:bench-%s-%d?mode=memory&cache=shared", name, time.Now().UnixNano()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	if err := store.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Bench"}); err != nil {
		b.Fatal(err)
	}
	if err := store.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "bench"}); err != nil {
		b.Fatal(err)
	}
	if err := store.CreateConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "bench"}, "U1", events.Event{
		ID: "evt_seed", WorkspaceID: "T1", Topic: "conversation.created", Payload: "{}", CreatedAt: time.Now().UTC(),
	}); err != nil {
		b.Fatal(err)
	}
	return store
}

func benchBlocks(count int) string {
	if count == 0 {
		return ""
	}
	items := make([]string, count)
	for index := range items {
		items[index] = `{"type":"section","text":{"type":"mrkdwn","text":"benchmark line"}}`
	}
	return "[" + strings.Join(items, ",") + "]"
}

func BenchmarkCreateMessage(b *testing.B) {
	for _, payload := range []struct {
		name   string
		blocks int
	}{{"text_only", 0}, {"with_10_blocks", 10}, {"with_100_blocks", 100}} {
		b.Run(payload.name, func(b *testing.B) {
			ctx := context.Background()
			store := benchStore(b, payload.name)
			blocks := benchBlocks(payload.blocks)
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				message := domain.Message{
					ID:           domain.MessageID(fmt.Sprintf("msg_%d", index)),
					WorkspaceID:  "T1",
					Conversation: "C1",
					AuthorID:     "U1",
					Text:         "benchmark message",
					Blocks:       blocks,
					CreatedAt:    time.Now().UTC(),
				}
				event := events.Event{
					ID:          domain.EventID(fmt.Sprintf("evt_%d", index)),
					WorkspaceID: "T1",
					Topic:       "message.created",
					Payload:     "{}",
					CreatedAt:   time.Now().UTC(),
				}
				if err := store.CreateMessage(ctx, message, event, fmt.Sprintf("idem_%d", index)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkListMessages(b *testing.B) {
	ctx := context.Background()
	store := benchStore(b, "list")
	blocks := benchBlocks(5)
	const seeded = 500
	for index := 0; index < seeded; index++ {
		message := domain.Message{
			ID:           domain.MessageID(fmt.Sprintf("msg_%d", index)),
			WorkspaceID:  "T1",
			Conversation: "C1",
			AuthorID:     "U1",
			Text:         "benchmark message",
			Blocks:       blocks,
			CreatedAt:    time.Now().UTC().Add(time.Duration(index) * time.Millisecond),
		}
		event := events.Event{
			ID:          domain.EventID(fmt.Sprintf("evt_%d", index)),
			WorkspaceID: "T1",
			Topic:       "message.created",
			Payload:     "{}",
			CreatedAt:   time.Now().UTC(),
		}
		if err := store.CreateMessage(ctx, message, event, fmt.Sprintf("idem_%d", index)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := store.ListMessages(ctx, "C1", domain.PageRequest{Limit: 50}); err != nil {
			b.Fatal(err)
		}
	}
}
