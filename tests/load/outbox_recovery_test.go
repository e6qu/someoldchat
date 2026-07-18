package load

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/outbox"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestOutboxReplacementWorkerRecoversAfterCrash(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T-outbox"})
	repository.SeedUser(domain.User{ID: "U-outbox", WorkspaceID: "T-outbox"})
	repository.SeedConversation(domain.Conversation{ID: "C-outbox", WorkspaceID: "T-outbox"})
	createdAt := time.Now().UTC()
	if err := repository.CreateMessage(ctx, domain.Message{ID: "M-outbox", WorkspaceID: "T-outbox", Conversation: "C-outbox", AuthorID: "U-outbox", Text: "recover", CreatedAt: createdAt}, events.Event{ID: "E-outbox", WorkspaceID: "T-outbox", Topic: "message.created", Payload: "M-outbox", CreatedAt: createdAt}, ""); err != nil {
		t.Fatal(err)
	}

	crashed, err := repository.ClaimEvents(ctx, "T-outbox", "crashed-worker", 1, 20*time.Millisecond)
	if err != nil || len(crashed) != 1 {
		t.Fatalf("crashed worker claim=%v err=%v", crashed, err)
	}

	delivered := make(chan events.Record, 1)
	replacement, err := outbox.NewWorker(repository, "replacement-worker", 1, time.Minute, func(_ context.Context, record events.Record) error {
		delivered <- record
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		count, runErr := replacement.RunOnce(ctx, "T-outbox")
		if runErr != nil {
			t.Fatal(runErr)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement worker did not reclaim the expired lease")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case record := <-delivered:
		if record.Sequence != crashed[0].Sequence || record.Event.ID != "E-outbox" {
			t.Fatalf("recovered record=%+v, want sequence %d and event E-outbox", record, crashed[0].Sequence)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement worker did not deliver the recovered event")
	}

	if err := repository.AckEvents(ctx, "crashed-worker", []uint64{crashed[0].Sequence}); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("crashed owner ack error=%v, want lease conflict", err)
	}
	if next, err := repository.ClaimEvents(ctx, "T-outbox", "third-worker", 1, time.Minute); err != nil || len(next) != 0 {
		t.Fatalf("acknowledged event was reclaimed: records=%v err=%v", next, err)
	}
}
