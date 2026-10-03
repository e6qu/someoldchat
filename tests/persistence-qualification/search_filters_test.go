package qualification

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// searchFiltersKeepToMembersAndPeople holds the web client's two search
// filters: "Only my channels" keeps results to conversations the searcher
// belongs to, where an unjoined public channel is otherwise searchable, and
// "Exclude automations" drops what an app, a bot user or a bot_message posted.
func searchFiltersKeepToMembersAndPeople(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	at := time.Unix(1700000000, 0).UTC()
	other := domain.ConversationID("C-unjoined-" + f.suffix)
	if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: other, WorkspaceID: f.workspaceID, Name: "unjoined-" + f.suffix}); err != nil {
		t.Fatal(err)
	}
	botUser := domain.UserID("U-search-bot-" + f.suffix)
	if err := f.repository.SeedUser(ctx, domain.User{ID: botUser, WorkspaceID: f.workspaceID, Name: "robot"}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.CreateBot(ctx, domain.Bot{ID: domain.BotID("B-search-" + f.suffix), WorkspaceID: f.workspaceID, UserID: botUser, Name: "robot", UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	post := func(name string, conversation domain.ConversationID, author domain.UserID, change func(*domain.Message)) {
		t.Helper()
		message := domain.Message{ID: domain.MessageID(name + "-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: conversation, AuthorID: author,
			Text: "deploy " + name, Attachments: "[]", CreatedAt: domain.MessageInstant(at)}
		if change != nil {
			change(&message)
		}
		at = at.Add(time.Second)
		if err := f.repository.CreateMessage(ctx, message, f.event("event-"+name, "message.created", string(message.ID)), ""); err != nil {
			t.Fatalf("post %s: %v", name, err)
		}
	}
	post("person", f.channelID, f.userID, nil)
	post("unjoined", other, f.userID, nil)
	post("app", f.channelID, f.userID, func(m *domain.Message) { m.AppID = "A-search" })
	post("botuser", f.channelID, botUser, nil)
	post("botmessage", f.channelID, f.userID, func(m *domain.Message) { m.Subtype = "bot_message" })

	search := func(change func(*domain.MessageSearch)) []string {
		t.Helper()
		plan := domain.MessageSearch{Terms: []string{"deploy"}, Page: domain.PageRequest{Limit: 20}}
		change(&plan)
		page, err := f.repository.SearchMessages(ctx, f.workspaceID, f.userID, plan)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != len(page.Messages) {
			t.Fatalf("total=%d for %d results: the count and the page must apply the same filters", page.Total, len(page.Messages))
		}
		names := make([]string, 0, len(page.Messages))
		for _, message := range page.Messages {
			names = append(names, string(message.ID))
		}
		sort.Strings(names)
		return names
	}
	want := func(name string, got []string, ids ...string) {
		t.Helper()
		expected := make([]string, 0, len(ids))
		for _, id := range ids {
			expected = append(expected, id+"-"+f.suffix)
		}
		sort.Strings(expected)
		if len(got) != len(expected) {
			t.Fatalf("%s: %v, want %v", name, got, expected)
		}
		for index := range got {
			if got[index] != expected[index] {
				t.Fatalf("%s: %v, want %v", name, got, expected)
			}
		}
	}
	want("everything", search(func(*domain.MessageSearch) {}), "person", "unjoined", "app", "botuser", "botmessage")
	want("only my channels", search(func(plan *domain.MessageSearch) { plan.MemberOf = f.userID }), "person", "app", "botuser", "botmessage")
	want("exclude automations", search(func(plan *domain.MessageSearch) { plan.ExcludeAutomations = true }), "person", "unjoined")
	want("both", search(func(plan *domain.MessageSearch) { plan.MemberOf = f.userID; plan.ExcludeAutomations = true }), "person")
}
