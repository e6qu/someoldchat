package scheduler

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Slackbot answers a member's message that says a custom response's phrase,
// where it was said and in its thread; answers a direct message to it with a
// custom response or help; never answers itself, an app or what was said
// before it started reading; and answers each message once.
func TestSlackbotAnswersCustomResponsesAndItsDirectMessages(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
	messages := service.Messages{Store: repository}
	worker, err := NewSlackbotResponseWorker(messages, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "lunch before Slackbot read anything", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AddSlackbotResponse(ctx, "T1", "U1", []string{"lunch, wifi password"}, []string{"Tacos at noon!"}); err != nil {
		t.Fatal(err)
	}
	// The first run starts the cursor at the head of the journal, so the
	// message posted before Slackbot was reading goes unanswered.
	if answered, err := worker.RunOnce(ctx, "T1"); err != nil || answered != 0 {
		t.Fatalf("first run answered=%d err=%v", answered, err)
	}
	asked, err := messages.Post(ctx, "T1", "U1", "C1", "Lunch?", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "lunchtime is over", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "what's the wifi password", domain.NewMessageTimestamp(asked.CreatedAt), ""); err != nil {
		t.Fatal(err)
	}
	opened, err := messages.OpenConversation(ctx, "T1", "U1", []domain.UserID{domain.SlackbotUserID})
	if err != nil {
		t.Fatal(err)
	}
	direct := opened.Conversation
	if _, err := messages.Post(ctx, "T1", "U1", direct.ID, "hello?", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", direct.ID, "is it lunch", "", ""); err != nil {
		t.Fatal(err)
	}
	if answered, err := worker.RunOnce(ctx, "T1"); err != nil || answered != 4 {
		t.Fatalf("answered=%d err=%v, want the channel, the thread and two direct messages", answered, err)
	}
	// Slackbot's own answers are journalled too; they are not answered.
	if answered, err := worker.RunOnce(ctx, "T1"); err != nil || answered != 0 {
		t.Fatalf("a second run answered=%d err=%v", answered, err)
	}
	// Slackbot's top-level messages in a conversation.
	slackbotSaid := func(conversation domain.ConversationID) []domain.Message {
		t.Helper()
		page, err := repository.ListMessages(ctx, conversation, domain.HistoryRequest{Page: domain.PageRequest{Limit: 50}})
		if err != nil {
			t.Fatal(err)
		}
		var said []domain.Message
		for _, message := range page.Messages {
			if message.AuthorID == domain.SlackbotUserID && message.ThreadTimestamp == "" {
				said = append(said, message)
			}
		}
		return said
	}
	channel := slackbotSaid("C1")
	if len(channel) != 1 || channel[0].Text != "Tacos at noon!" || channel[0].ThreadTimestamp != "" {
		t.Fatalf("Slackbot in the channel said %+v", channel)
	}
	replies, err := messages.Replies(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(asked.CreatedAt), domain.ThreadRequest{Page: domain.PageRequest{Limit: 50}})
	if err != nil {
		t.Fatal(err)
	}
	threaded := 0
	for _, reply := range replies.Messages {
		if reply.AuthorID == domain.SlackbotUserID && reply.Text == "Tacos at noon!" {
			threaded++
		}
	}
	if threaded != 1 {
		t.Fatalf("Slackbot's thread replies=%+v", replies.Messages)
	}
	dm := slackbotSaid(direct.ID)
	if len(dm) != 2 {
		t.Fatalf("Slackbot in its DM said %+v", dm)
	}
	texts := map[string]bool{dm[0].Text: true, dm[1].Text: true}
	if !texts[domain.SlackbotHelpText] || !texts["Tacos at noon!"] {
		t.Fatalf("Slackbot's DM answers=%v", texts)
	}
}
