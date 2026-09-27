package qualification

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// historyPagesRootsWithinItsWindow is conversations.history's read: roots and
// broadcast replies only, bounded by oldest/latest/inclusive inside the keyset
// read, so Limit and HasMore describe the listed rows. The thread read keeps
// its root whatever window narrows the replies.
func historyPagesRootsWithinItsWindow(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Unix(1700000400, 0).UTC()
	first := f.message(t, ctx, "window-1", base.Add(1*time.Second))
	rootTS := domain.NewMessageTimestamp(first.CreatedAt)
	quiet := f.reply(t, ctx, "window-quiet", rootTS, base.Add(2*time.Second))
	loud := domain.Message{
		ID: domain.MessageID("window-loud-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
		AuthorID: f.userID, Text: "loud", ThreadTimestamp: rootTS, ReplyBroadcast: true, Attachments: "[]",
		CreatedAt: base.Add(3 * time.Second),
	}
	if err := f.repository.CreateMessage(ctx, loud, f.event("event-window-loud", "message.created", string(loud.ID)), ""); err != nil {
		t.Fatal(err)
	}
	second := f.message(t, ctx, "window-2", base.Add(4*time.Second))
	third := f.message(t, ctx, "window-3", base.Add(5*time.Second))

	ids := func(page domain.MessagePage) []domain.MessageID {
		result := make([]domain.MessageID, 0, len(page.Messages))
		for _, message := range page.Messages {
			result = append(result, message.ID)
		}
		return result
	}
	all, err := f.repository.ListMessages(ctx, f.channelID, domain.HistoryRequest{Page: domain.PageRequest{Limit: 10, Descending: true}, RootsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(all); !slices.Equal(got, []domain.MessageID{third.ID, second.ID, loud.ID, first.ID}) {
		t.Fatalf("roots-only history = %v, want no quiet reply %s", got, quiet.ID)
	}
	if !all.Messages[2].ReplyBroadcast {
		t.Fatalf("the broadcast flag did not survive the read: %+v", all.Messages[2])
	}
	exact, err := f.repository.ListMessages(ctx, f.channelID, domain.HistoryRequest{
		Page: domain.PageRequest{Limit: 1, Descending: true}, RootsOnly: true,
		Window: domain.MessageWindow{Latest: second.CreatedAt, Inclusive: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(exact); !slices.Equal(got, []domain.MessageID{second.ID}) || !exact.HasMore {
		t.Fatalf("latest inclusive limit 1 = %v has_more=%v", got, exact.HasMore)
	}
	between, err := f.repository.ListMessages(ctx, f.channelID, domain.HistoryRequest{
		Page: domain.PageRequest{Limit: 10}, RootsOnly: true,
		Window: domain.MessageWindow{Oldest: first.CreatedAt, Latest: third.CreatedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(between); !slices.Equal(got, []domain.MessageID{loud.ID, second.ID}) || between.HasMore {
		t.Fatalf("exclusive window = %v has_more=%v", got, between.HasMore)
	}
	thread, err := f.repository.ListThreadMessages(ctx, f.channelID, rootTS, domain.ThreadRequest{
		Page: domain.PageRequest{Limit: 10}, Window: domain.MessageWindow{Oldest: quiet.CreatedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(thread); !slices.Equal(got, []domain.MessageID{first.ID, loud.ID}) {
		t.Fatalf("windowed thread = %v, want the root and the reply after the window opens", got)
	}
}

// messageAnnotationsAgreeAcrossProfiles is the batched read behind every
// message's `reactions` and `pinned_to`, the followed-thread batch behind
// `subscribed`, and the message a pin carries.
func messageAnnotationsAgreeAcrossProfiles(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	other := domain.UserID("U-annotation-" + f.suffix)
	if err := f.repository.SeedUser(ctx, domain.User{ID: other, WorkspaceID: f.workspaceID, Email: "annotation-" + f.suffix + "@example.com", Name: "annotation"}); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1700000500, 0).UTC()
	reacted := f.message(t, ctx, "annotated", base)
	plain := f.message(t, ctx, "plain", base.Add(time.Second))
	for index, reaction := range []domain.Reaction{
		{Message: reacted.ID, Name: "tada", UserID: other},
		{Message: reacted.ID, Name: "eyes", UserID: f.userID},
		{Message: reacted.ID, Name: "tada", UserID: f.userID},
	} {
		reaction.CreatedAt = base.Add(time.Duration(index+2) * time.Second)
		if err := f.repository.AddReaction(ctx, reaction, f.event(fmt.Sprintf("reaction-%d", index), "reaction.added", string(reaction.Message))); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.repository.AddPin(ctx, domain.Pin{Message: reacted.ID, UserID: f.userID, CreatedAt: base.Add(10 * time.Second)}, f.event("pin", "pin.added", string(reacted.ID))); err != nil {
		t.Fatal(err)
	}
	annotations, err := f.repository.MessageAnnotations(ctx, f.channelID, []domain.MessageID{reacted.ID, plain.ID, "M-elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.MessageAnnotation{Pinned: true, Reactions: []domain.ReactionSummary{
		{Name: "tada", Users: []domain.UserID{other, f.userID}, Count: 2},
		{Name: "eyes", Users: []domain.UserID{f.userID}, Count: 1},
	}}
	if len(annotations) != 1 || fmt.Sprint(annotations[reacted.ID]) != fmt.Sprint(want) {
		t.Fatalf("annotations = %+v, want only %+v", annotations, want)
	}
	pins, _, _, err := f.repository.ListPins(ctx, f.channelID, domain.PageRequest{Limit: 10})
	if err != nil || len(pins) != 1 || pins[0].Item.ID != reacted.ID || pins[0].Item.Text != reacted.Text {
		t.Fatalf("pins = %+v err=%v", pins, err)
	}
	// An author follows their own message, so the unfollowed root is made
	// explicitly rather than assumed.
	root := domain.NewMessageTimestamp(reacted.CreatedAt)
	if err := f.repository.SetThreadFollowed(ctx, f.workspaceID, f.userID, f.channelID, root, true, f.event("follow", "thread.followed", string(root))); err != nil {
		t.Fatal(err)
	}
	unfollowed := domain.NewMessageTimestamp(plain.CreatedAt)
	if err := f.repository.SetThreadFollowed(ctx, f.workspaceID, f.userID, f.channelID, unfollowed, false, f.event("unfollow", "thread.unfollowed", string(unfollowed))); err != nil {
		t.Fatal(err)
	}
	followed, err := f.repository.FollowedThreadRoots(ctx, f.workspaceID, f.userID, f.channelID, []domain.MessageTimestamp{root, domain.NewMessageTimestamp(plain.CreatedAt)})
	if err != nil || len(followed) != 1 || !followed[root] {
		t.Fatalf("followed roots = %v err=%v", followed, err)
	}
}
