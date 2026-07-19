package load

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/outbox"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestOutboxReplicasPartitionDurableClaims(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T-competition"})
	repository.SeedUser(domain.User{ID: "U-competition", WorkspaceID: "T-competition"})
	repository.SeedConversation(domain.Conversation{ID: "C-competition", WorkspaceID: "T-competition"})
	createdAt := time.Now().UTC()
	for index := 0; index < 32; index++ {
		id := domain.MessageID(fmt.Sprintf("M-competition-%02d", index))
		if err := repository.CreateMessage(ctx, domain.Message{ID: id, WorkspaceID: "T-competition", Conversation: "C-competition", AuthorID: "U-competition", Text: string(id), CreatedAt: createdAt.Add(time.Duration(index) * time.Nanosecond)}, events.Event{ID: domain.EventID("E-" + string(id)), WorkspaceID: "T-competition", Topic: "message.created", Payload: string(id), CreatedAt: createdAt}, ""); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	delivered := make([]string, 0, 32)
	delivery := func(_ context.Context, record events.Record) error {
		mu.Lock()
		defer mu.Unlock()
		delivered = append(delivered, string(record.Event.ID))
		return nil
	}
	workers := make([]outbox.Worker, 0, 4)
	for index := 0; index < 4; index++ {
		worker, err := outbox.NewWorker(repository, fmt.Sprintf("worker-%d", index), 3, time.Second, delivery)
		if err != nil {
			t.Fatal(err)
		}
		workers = append(workers, worker)
	}

	var group sync.WaitGroup
	for _, worker := range workers {
		worker := worker
		group.Add(1)
		go func() {
			defer group.Done()
			for attempts := 0; attempts < 32; attempts++ {
				count, err := worker.RunOnce(ctx, "T-competition")
				if err != nil {
					t.Errorf("worker run failed: %v", err)
					return
				}
				if count == 0 {
					return
				}
			}
			t.Errorf("worker did not drain within bounded attempts")
		}()
	}
	group.Wait()

	if len(delivered) != 32 {
		t.Fatalf("delivered %d records, want 32: %v", len(delivered), delivered)
	}
	sort.Strings(delivered)
	for index := 0; index < 32; index++ {
		want := fmt.Sprintf("E-M-competition-%02d", index)
		if delivered[index] != want {
			t.Fatalf("delivered[%d]=%q, want %q; all=%v", index, delivered[index], want, delivered)
		}
	}
}
