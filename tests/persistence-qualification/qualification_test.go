package qualification

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type qualificationStore interface {
	store.Store
	SeedWorkspace(context.Context, domain.Workspace) error
	SeedUser(context.Context, domain.User) error
	SeedConversation(context.Context, domain.Conversation) error
	SeedConversationMember(context.Context, domain.ConversationID, domain.UserID) error
}

func TestCoreRepositoryContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := openStore(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspace := domain.Workspace{ID: domain.WorkspaceID("T-qualification-" + suffix), Name: "Qualification"}
	user := domain.User{ID: domain.UserID("U-qualification-" + suffix), WorkspaceID: workspace.ID, Email: "Alice@example.com", Name: "alice"}
	conversation := domain.Conversation{ID: domain.ConversationID("C-qualification-" + suffix), WorkspaceID: workspace.ID, Name: "general"}
	if err := repository.SeedWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversationMember(ctx, conversation.ID, user.ID); err != nil {
		t.Fatal(err)
	}

	loadedUser, err := repository.FindUserByEmail(ctx, workspace.ID, " ALICE@EXAMPLE.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if loadedUser.ID != user.ID || loadedUser.Email != "alice@example.com" {
		t.Fatalf("user=%+v, want normalized email and user identity", loadedUser)
	}

	createdAt := time.Unix(1700000000, 0).UTC()
	message := domain.Message{ID: domain.MessageID("M-qualification-" + suffix), WorkspaceID: workspace.ID, Conversation: conversation.ID, AuthorID: user.ID, Text: "committed", CreatedAt: createdAt}
	event := events.Event{ID: domain.EventID("E-qualification-" + suffix), WorkspaceID: workspace.ID, Topic: "message.created", Payload: string(message.ID), CreatedAt: createdAt}
	idempotencyKey := "idempotency-qualification-" + suffix
	if err := repository.CreateMessage(ctx, message, event, idempotencyKey); err != nil {
		t.Fatal(err)
	}
	duplicate := message
	duplicate.ID = domain.MessageID("M-qualification-duplicate-" + suffix)
	duplicate.Text = "different"
	duplicateEvent := event
	duplicateEvent.ID = domain.EventID("E-qualification-duplicate-" + suffix)
	duplicateEvent.Payload = string(duplicate.ID)
	if err := repository.CreateMessage(ctx, duplicate, duplicateEvent, idempotencyKey); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("duplicate idempotency error=%v, want ErrIdempotencyConflict", err)
	}

	loadedMessage, err := repository.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedMessage.Text != message.Text || loadedMessage.AuthorID != message.AuthorID {
		t.Fatalf("message=%+v, want committed message", loadedMessage)
	}
	page, err := repository.ListMessages(ctx, conversation.ID, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != message.ID || page.HasMore {
		t.Fatalf("message page=%+v, want one bounded item", page)
	}
	if _, err := repository.GetIdempotentMessage(ctx, workspace.ID, user.ID, idempotencyKey); err != nil {
		t.Fatal(err)
	}
}
