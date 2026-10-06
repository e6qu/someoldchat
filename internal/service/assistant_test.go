package service

import (
	"context"
	"errors"
	"strings"
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
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is thinking...", []string{"Reading the runbook", " ", "Checking the deploy"}, domain.AgentIdentity{}); err != nil {
		t.Fatal(err)
	}
	value, err := messages.AssistantThread(ctx, "T1", "U1", "C1", thread)
	if err != nil {
		t.Fatal(err)
	}
	if value.Title != "Deploy help" || value.Status != "is thinking..." || value.PromptsTitle != "Try one" || len(value.Prompts) != 1 {
		t.Fatalf("state = %+v, want all three fields kept", value)
	}
	// The loading messages travel with the status, blank lines dropped.
	if strings.Join(value.LoadingMessages, "|") != "Reading the runbook|Checking the deploy" {
		t.Fatalf("loading messages = %q", value.LoadingMessages)
	}
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is thinking...", make([]string, domain.AssistantLoadingMessageLimit+1), domain.AgentIdentity{}); !errors.Is(err, domain.ErrInvalidAssistantThread) {
		t.Fatalf("eleven loading messages error = %v, want %v", err, domain.ErrInvalidAssistantThread)
	}

	// Clearing the status is how an assistant says it has stopped working, so
	// the empty string is accepted here and must leave the rest alone.
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "", nil, domain.AgentIdentity{}); err != nil {
		t.Fatal(err)
	}
	after, err := messages.AssistantThread(ctx, "T1", "U1", "C1", thread)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "" || len(after.LoadingMessages) != 0 || after.Title != "Deploy help" || len(after.Prompts) != 1 {
		t.Fatalf("state after clearing the status = %+v, want only the status gone", after)
	}
}

// The icon_emoji, icon_url and username an assistant sets its status with
// belong to that status: stored trimmed with who set it, replaced by the next
// call — one without them shows the setter's own name again — and cleared with
// the status. A username past the bound or an icon_url that is not an absolute
// http(s) address is refused before anything is written.
func TestAssistantStatusIdentityTravelsWithTheStatus(t *testing.T) {
	ctx, _, messages, thread := assistantWorld(t)
	read := func() domain.AssistantThread {
		t.Helper()
		value, err := messages.AssistantThread(ctx, "T1", "U1", "C1", thread)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	identity := domain.AgentIdentity{Username: " Deploy bot ", IconEmoji: ":robot_face:", IconURL: "https://example.test/bot.png"}
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is thinking...", nil, identity); err != nil {
		t.Fatal(err)
	}
	if value := read(); value.StatusUserID != "U1" || value.StatusIdentity != (domain.AgentIdentity{Username: "Deploy bot", IconEmoji: ":robot_face:", IconURL: "https://example.test/bot.png"}) {
		t.Fatalf("status = %+v, want the trimmed identity and its setter", value)
	}
	for name, invalid := range map[string]domain.AgentIdentity{
		"long username":   {Username: strings.Repeat("u", domain.AgentIdentityUsernameLimit+1)},
		"relative icon":   {IconURL: "/bot.png"},
		"script icon":     {IconURL: "javascript:alert(1)"},
		"schemeless icon": {IconURL: "example.test/bot.png"},
	} {
		if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is still thinking...", nil, invalid); !errors.Is(err, domain.ErrInvalidAssistantThread) {
			t.Errorf("%s = %v, want %v", name, err, domain.ErrInvalidAssistantThread)
		}
	}
	if value := read(); value.Status != "is thinking..." || value.StatusIdentity.Username != "Deploy bot" {
		t.Fatalf("a refused identity changed the status: %+v", value)
	}
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is still thinking...", nil, domain.AgentIdentity{}); err != nil {
		t.Fatal(err)
	}
	if value := read(); value.Status != "is still thinking..." || !value.StatusIdentity.Empty() || value.StatusUserID != "U1" {
		t.Fatalf("status = %+v, want the identity replaced by the call that set none", value)
	}
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is thinking...", nil, identity); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, " ", nil, identity); err != nil {
		t.Fatal(err)
	}
	if value := read(); value.Status != "" || !value.StatusIdentity.Empty() || value.StatusUserID != "" {
		t.Fatalf("cleared status = %+v, want its identity and setter gone with it", value)
	}
}

// Assistant state is not message content: it must not become a message, or it
// would appear in history, in search and in unread counts, and outlive the
// moment it describes.
func TestAssistantStateIsNotAMessage(t *testing.T) {
	ctx, repository, messages, thread := assistantWorld(t)
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U1", "C1", thread, "is thinking...", nil, domain.AgentIdentity{}); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListMessages(ctx, "C1", domain.HistoryRequest{Page: domain.PageRequest{Limit: 20}})
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
	if err := messages.SetAssistantThreadTitle(ctx, "T1", "U1", "C1", thread, "   "); !errors.Is(err, domain.ErrInvalidAssistantThread) {
		t.Errorf("an empty title = %v, want it refused", err)
	}
	if err := messages.SetAssistantThreadSuggestedPrompts(ctx, "T1", "U1", "C1", thread, "", nil); !errors.Is(err, domain.ErrInvalidAssistantThread) {
		t.Errorf("no prompts = %v, want it refused", err)
	}
	tooMany := make([]domain.AssistantPrompt, domain.AssistantPromptLimit+1)
	for index := range tooMany {
		tooMany[index] = domain.AssistantPrompt{Title: "t", Message: "m"}
	}
	if err := messages.SetAssistantThreadSuggestedPrompts(ctx, "T1", "U1", "C1", thread, "", tooMany); !errors.Is(err, domain.ErrInvalidAssistantThread) {
		t.Errorf("too many prompts = %v, want it refused", err)
	}
	if err := messages.SetAssistantThreadTitle(ctx, "T1", "U1", "C1", "not-a-timestamp", "x"); !errors.Is(err, domain.ErrInvalidTimestamp) {
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
	if err := messages.SetAssistantThreadStatus(ctx, "T1", "U2", "C1", thread, "meddling", nil, domain.AgentIdentity{}); err == nil {
		t.Fatal("a non-member set assistant state")
	}
}

var _ = events.Event{}
