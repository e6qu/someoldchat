package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func assistantWorld(t *testing.T) (context.Context, *memory.Store, Messages, domain.MessageTimestamp) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "assistant"}); err != nil {
		t.Fatal(err)
	}
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
	messages := Messages{Store: repository}
	root, err := messages.Post(ctx, "T1", "U1", "C1", "how do I deploy?", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repository, messages, domain.NewMessageTimestamp(root.CreatedAt)
}

// The three writes each set one field, and setting one must not disturb the
// others. A whole-record write would clear the title whenever an app updated
// only its status, which is the commonest thing an assistant does.
func TestAssistantWritesTouchOneFieldEach(t *testing.T) {
	ctx, _, messages, thread := assistantWorld(t)
	if err := messages.SetAssistantThreadTitle(ctx, "T1", "U1", "C1", thread, "Deploy help"); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetAssistantThreadSuggestedPrompts(ctx, "T1", "U1", "C1", thread, "Try one", []domain.AssistantPrompt{
		{Title: "Roll back", Message: "How do I roll back?"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is thinking..."); err != nil {
		t.Fatal(err)
	}
	value, err := messages.AssistantThread(ctx, "T1", "U1", "C1", thread)
	if err != nil {
		t.Fatal(err)
	}
	if value.Title != "Deploy help" || value.Status != "is thinking..." || value.PromptsTitle != "Try one" || len(value.Prompts) != 1 {
		t.Fatalf("state = %+v, want all three fields kept", value)
	}

	// Clearing the status is how an assistant says it has stopped working, so
	// the empty string is accepted here and must leave the rest alone.
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, ""); err != nil {
		t.Fatal(err)
	}
	after, err := messages.AssistantThread(ctx, "T1", "U1", "C1", thread)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "" || after.Title != "Deploy help" || len(after.Prompts) != 1 {
		t.Fatalf("state after clearing the status = %+v, want only the status gone", after)
	}
}

// Assistant state is not message content: it must not become a message, or it
// would appear in history, in search and in unread counts, and outlive the
// moment it describes.
func TestAssistantStateIsNotAMessage(t *testing.T) {
	ctx, repository, messages, thread := assistantWorld(t)
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is thinking..."); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("messages = %d, want the assistant status to have created none", len(page.Messages))
	}
}

// A thread nothing has touched has no state, and the caller is told that rather
// than handed an empty record it cannot distinguish from a cleared one.
func TestAssistantThreadWithoutStateIsNotFound(t *testing.T) {
	ctx, _, messages, thread := assistantWorld(t)
	if _, err := messages.AssistantThread(ctx, "T1", "U1", "C1", thread); err == nil {
		t.Fatal("an untouched thread reported state")
	}
}

func TestAssistantWritesAreValidated(t *testing.T) {
	ctx, _, messages, thread := assistantWorld(t)
	if err := messages.SetAssistantThreadTitle(ctx, "T1", "U1", "C1", thread, "   "); !errors.Is(err, ErrInvalidAssistantThread) {
		t.Errorf("an empty title = %v, want it refused", err)
	}
	if err := messages.SetAssistantThreadSuggestedPrompts(ctx, "T1", "U1", "C1", thread, "", nil); !errors.Is(err, ErrInvalidAssistantThread) {
		t.Errorf("no prompts = %v, want it refused", err)
	}
	tooMany := make([]domain.AssistantPrompt, domain.AssistantPromptLimit+1)
	for index := range tooMany {
		tooMany[index] = domain.AssistantPrompt{Title: "t", Message: "m"}
	}
	if err := messages.SetAssistantThreadSuggestedPrompts(ctx, "T1", "U1", "C1", thread, "", tooMany); !errors.Is(err, ErrInvalidAssistantThread) {
		t.Errorf("too many prompts = %v, want it refused", err)
	}
	if err := messages.SetAssistantThreadTitle(ctx, "T1", "U1", "C1", "not-a-timestamp", "x"); !errors.Is(err, ErrInvalidTimestamp) {
		t.Errorf("a malformed thread = %v, want it refused", err)
	}
}

// Writing assistant state is a posting-shaped act: it is shown to everyone who
// can read the thread, so a non-member cannot do it.
func TestAssistantWriteRequiresConversationMembership(t *testing.T) {
	ctx, repository, messages, thread := assistantWorld(t)
	if err := repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "outsider"}); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U2", "C1", thread, "meddling"); err == nil {
		t.Fatal("a non-member set assistant state")
	}
}

var _ = events.Event{}
