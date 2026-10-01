package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// ThreadKeywordRepository is the part of store.Store the thread keyword check
// drives.
type ThreadKeywordRepository interface {
	CreateMessage(context.Context, domain.Message, events.Event, string, ...events.Event) error
	SetWorkspaceNotificationPreferences(context.Context, domain.WorkspaceNotificationPreferences, events.Event) error
	SetConversationNotificationPreferences(context.Context, domain.ConversationNotificationPreferences, events.Event) error
	SetThreadFollowed(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, bool, events.Event) error
	ListActivity(context.Context, domain.WorkspaceID, domain.UserID, domain.ActivityQuery) (domain.ActivityPage, error)
}

// CheckKeywordsMatchOnlyFollowedThreads requires Slack's current keyword rule:
// "Keywords in messages sent in threads you're not following won't trigger a
// notification", so a reply in a thread the member follows does match, a reply
// in one they do not follow does not, and a muted channel matches neither. The
// repository must hold workspace T1, members U1 and U2, and public
// conversation C1 both belong to.
func CheckKeywordsMatchOnlyFollowedThreads(t *testing.T, repository ThreadKeywordRepository) {
	t.Helper()
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	event := func(id string, at time.Time) events.Event {
		return events.Event{ID: domain.EventID("E" + id), WorkspaceID: "T1", Topic: "message.created", Payload: id, CreatedAt: at}
	}
	post := func(id domain.MessageID, offset time.Duration, text string, thread domain.MessageTimestamp) domain.MessageTimestamp {
		t.Helper()
		created := base.Add(offset)
		message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: text, ThreadTimestamp: thread, CreatedAt: created}
		if err := repository.CreateMessage(ctx, message, event(string(id), created), ""); err != nil {
			t.Fatal(err)
		}
		return domain.NewMessageTimestamp(created)
	}
	keywordItems := func() int {
		t.Helper()
		page, err := repository.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{
			Kinds: []domain.ActivityKind{domain.ActivityKeyword}, Page: domain.PageRequest{Limit: 20},
		})
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}

	preferences := domain.DefaultWorkspaceNotificationPreferences("T1", "U2")
	preferences.Keywords = []string{"release"}
	if err := repository.SetWorkspaceNotificationPreferences(ctx, preferences, event("prefs", base)); err != nil {
		t.Fatal(err)
	}

	root := post("M1", 0, "thread root", "")
	post("M2", time.Second, "release soon", root)
	if got := keywordItems(); got != 0 {
		t.Fatalf("a keyword in a thread U2 does not follow raised %d keyword items, want 0", got)
	}

	if err := repository.SetThreadFollowed(ctx, "T1", "U2", "C1", root, true, event("follow", base.Add(2*time.Second))); err != nil {
		t.Fatal(err)
	}
	post("M3", 3*time.Second, "RELEASE now", root)
	if got := keywordItems(); got != 1 {
		t.Fatalf("a keyword in a thread U2 follows raised %d keyword items, want 1", got)
	}

	muted := domain.DefaultConversationNotificationPreferences("T1", "U2", "C1")
	muted.Level = domain.NotificationMute
	if err := repository.SetConversationNotificationPreferences(ctx, muted, event("mute", base.Add(4*time.Second))); err != nil {
		t.Fatal(err)
	}
	post("M4", 5*time.Second, "release later", root)
	if got := keywordItems(); got != 1 {
		t.Fatalf("a keyword in a muted channel's followed thread raised %d keyword items, want 1", got)
	}
}
