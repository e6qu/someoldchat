package qualification

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// personalListingsStopAtALeftPrivateConversation pins who may still read a
// message through a personal listing. The SQL repositories listed reactions in
// a private conversation only to a current member while memory listed them to
// anyone who had ever reacted, and neither profile applied the rule to stars —
// so a member who left a private channel kept reading the text of every message
// they had starred there. A public conversation is unaffected by leaving, and a
// starred message reads back as it is now, not as it was when starred.
func personalListingsStopAtALeftPrivateConversation(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	private := domain.ConversationID("C-private-" + f.suffix)
	if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: private, WorkspaceID: f.workspaceID, Name: "private-" + f.suffix, Kind: domain.ConversationTypePrivate}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SeedConversationMember(ctx, private, f.userID); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	secret := domain.Message{ID: domain.MessageID("secret-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: private, AuthorID: f.userID, Text: "the secret", CreatedAt: at}
	if err := f.repository.CreateMessage(ctx, secret, f.event("event-secret", "message.created", string(secret.ID)), ""); err != nil {
		t.Fatal(err)
	}
	public := f.message(t, ctx, "public", at.Add(time.Second))
	for index, message := range []domain.Message{secret, public} {
		if err := f.repository.AddReaction(ctx, domain.Reaction{Message: message.ID, Name: "eyes", UserID: f.userID, CreatedAt: at}, f.event(fmt.Sprintf("reaction-%d", index), "reaction.added", string(message.ID))); err != nil {
			t.Fatal(err)
		}
		if err := f.repository.AddStar(ctx, domain.Star{Conversation: message.Conversation, UserID: f.userID, Message: message, CreatedAt: at.Add(time.Duration(index) * time.Second)}, f.event(fmt.Sprintf("star-%d", index), "star.added", string(message.ID))); err != nil {
			t.Fatal(err)
		}
	}
	edited := public
	edited.Text = "edited after it was starred"
	if err := f.repository.UpdateMessage(ctx, edited, f.event("edit", "message.changed", string(edited.ID))); err != nil {
		t.Fatal(err)
	}

	listed := func() (reactions, stars map[domain.MessageID]string) {
		t.Helper()
		reactions, stars = map[domain.MessageID]string{}, map[domain.MessageID]string{}
		reactionPage, err := f.repository.ListUserReactions(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range reactionPage.Items {
			reactions[item.Message.ID] = item.Message.Text
		}
		starPage, err := f.repository.ListStars(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if starPage.Total != len(starPage.Stars) {
			t.Fatalf("star total %d counts stars the member cannot list (%d listed)", starPage.Total, len(starPage.Stars))
		}
		for _, star := range starPage.Stars {
			stars[star.Message.ID] = star.Message.Text
		}
		return reactions, stars
	}
	reactions, stars := listed()
	if len(reactions) != 2 || len(stars) != 2 {
		t.Fatalf("a member lists reactions=%v stars=%v, want both messages in each", reactions, stars)
	}
	if stars[public.ID] != edited.Text {
		t.Fatalf("a starred message reads back as %q, want its current text %q", stars[public.ID], edited.Text)
	}

	if err := f.repository.RemoveConversationMember(ctx, private, f.userID, f.event("left", "member.left", string(private))); err != nil {
		t.Fatal(err)
	}
	reactions, stars = listed()
	if _, leaked := reactions[secret.ID]; leaked {
		t.Fatalf("after leaving the private conversation its message is still listed in reactions: %v", reactions)
	}
	if _, leaked := stars[secret.ID]; leaked {
		t.Fatalf("after leaving the private conversation its message is still listed in stars: %v", stars)
	}
	if _, kept := reactions[public.ID]; !kept {
		t.Fatalf("leaving a private conversation hid a public reaction: %v", reactions)
	}
	if _, kept := stars[public.ID]; !kept {
		t.Fatalf("leaving a private conversation hid a public star: %v", stars)
	}
}
