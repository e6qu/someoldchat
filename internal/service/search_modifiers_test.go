package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func modifierWorld(t *testing.T) (context.Context, Messages) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "ada"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
	return ctx, Messages{Store: repository}
}

// `has::eyes:` asks for that reaction. Answering it with "any reaction" returns
// messages a member can see are wrong, which is worse than returning nothing
// because it looks like an answer.
func TestNamedEmojiSearchMatchesThatReactionOnly(t *testing.T) {
	ctx, messages := modifierWorld(t)
	watched, err := messages.Post(ctx, "T1", "U1", "C1", "watched thing", "", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := messages.Post(ctx, "T1", "U1", "C1", "other thing", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.AddReaction(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(watched.CreatedAt), "eyes"); err != nil {
		t.Fatal(err)
	}
	if err := messages.AddReaction(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(other.CreatedAt), "tada"); err != nil {
		t.Fatal(err)
	}

	named, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{Query: "thing has::eyes:", Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(named.Messages) != 1 || named.Messages[0].ID != watched.ID {
		t.Fatalf("has::eyes: = %+v, want only the watched message", named.Messages)
	}
	// The unnamed form still means "any reaction", which is the other question
	// members ask and the one that used to answer both.
	any, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{Query: "thing has:reaction", Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(any.Messages) != 2 {
		t.Fatalf("has:reaction = %+v err = %v, want both", any.Messages, err)
	}
	absent, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{Query: "thing has::rocket:", Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(absent.Messages) != 0 {
		t.Fatalf("an unused emoji = %+v err = %v, want nothing", absent.Messages, err)
	}
}

func TestLinkSearchFindsMessagesCarryingAURL(t *testing.T) {
	ctx, messages := modifierWorld(t)
	linked, err := messages.Post(ctx, "T1", "U1", "C1", "report at https://example.test/report", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "report is finished", "", ""); err != nil {
		t.Fatal(err)
	}
	found, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{Query: "report has:link", Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Messages) != 1 || found.Messages[0].ID != linked.ID {
		t.Fatalf("has:link = %+v, want only the message carrying a URL", found.Messages)
	}
}

// Slack's help shows during: with a month name and with a year, not only the
// numeric form. A bare month means this year, because that is what a member
// typing "during:July" means — reading it as year zero would return nothing and
// look like "no results" rather than a misunderstanding.
func TestDuringAcceptsTheFormsSlackDocuments(t *testing.T) {
	year := time.Now().UTC().Year()
	for _, value := range []string{"2024-07", "July 2024", "Jul 2024"} {
		after, before, ok := parseSearchPeriod(value)
		if !ok || after.Year() != 2024 || after.Month() != time.July || !before.Equal(after.AddDate(0, 1, 0)) {
			t.Fatalf("during:%s = %s..%s ok=%v, want July 2024", value, after, before, ok)
		}
	}
	after, before, ok := parseSearchPeriod("2024")
	if !ok || after.Year() != 2024 || !before.Equal(after.AddDate(1, 0, 0)) {
		t.Fatalf("during:2024 = %s..%s ok=%v, want the whole year", after, before, ok)
	}
	after, before, ok = parseSearchPeriod("July")
	if !ok || after.Year() != year || after.Month() != time.July || !before.Equal(after.AddDate(0, 1, 0)) {
		t.Fatalf("during:July = %s..%s ok=%v, want July of %d", after, before, ok, year)
	}
	if _, _, ok := parseSearchPeriod("Julyish"); ok {
		t.Fatal("a period that is not one was accepted")
	}
	if _, _, ok := parseSearchPeriod(""); ok {
		t.Fatal("an empty period was accepted")
	}
}

// A search that is only modifiers still has to be answerable, and the store
// refuses a plan with nothing to match on. has:link is a thing to match on.
func TestAModifierOnlySearchIsAnswerable(t *testing.T) {
	ctx, messages := modifierWorld(t)
	if _, err := messages.Post(ctx, "T1", "U1", "C1", fmt.Sprintf("see https://example.test/%d", time.Now().UnixNano()), "", ""); err != nil {
		t.Fatal(err)
	}
	found, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{Query: "has:link", Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Messages) != 1 {
		t.Fatalf("has:link alone = %+v, want the linked message", found.Messages)
	}
}
