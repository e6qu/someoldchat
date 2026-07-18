package load

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/scheduler"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestScheduledReplacementWorkerRecoversAfterCrash(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T-scheduled"})
	repository.SeedUser(domain.User{ID: "U-scheduled", WorkspaceID: "T-scheduled"})
	repository.SeedConversation(domain.Conversation{ID: "C-scheduled", WorkspaceID: "T-scheduled", Name: "scheduled"})
	repository.SeedConversationMember("C-scheduled", "U-scheduled")
	createdAt := time.Now().UTC().Add(-time.Minute)
	item := domain.ScheduledMessage{WorkspaceID: "T-scheduled", ID: "Q-crashed", Channel: "C-scheduled", Author: "U-scheduled", Text: "recover scheduled", PostAt: createdAt, CreatedAt: createdAt}
	if err := repository.CreateScheduledMessage(ctx, item, events.Event{ID: "E-scheduled", WorkspaceID: "T-scheduled", Topic: "message.scheduled", Payload: string(item.ID), CreatedAt: createdAt}); err != nil {
		t.Fatal(err)
	}

	claimed, err := repository.ClaimScheduledMessages(ctx, "T-scheduled", "crashed-worker", 1, 20*time.Millisecond)
	if err != nil || len(claimed) != 1 || claimed[0].ID != item.ID {
		t.Fatalf("crashed worker claim=%v err=%v", claimed, err)
	}

	replacement, err := scheduler.NewWorker(repository, service.Messages{Store: repository}, "replacement-worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		count, runErr := replacement.RunOnce(ctx, "T-scheduled")
		if runErr != nil {
			t.Fatal(runErr)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement scheduler did not reclaim the expired lease")
		}
		time.Sleep(time.Millisecond)
	}

	if err := repository.MarkScheduledMessageDelivered(ctx, "crashed-worker", item.ID); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("crashed owner mark error=%v, want lease conflict", err)
	}
	page, err := repository.ListMessages(ctx, item.Channel, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != item.Text {
		t.Fatalf("scheduled messages=%+v err=%v", page.Messages, err)
	}
}
