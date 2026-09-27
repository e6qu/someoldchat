package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Starring a channel — stars.add given only a channel — has no message to
// authorize through, so the channel's own visibility is its only check. A
// workspace member outside a private channel must be answered exactly as if the
// channel did not exist, while a member of it stars and unstars it.
func TestStarringAPrivateChannelRequiresBeingAbleToSeeIt(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{{ID: "U1", WorkspaceID: "T1"}, {ID: "U2", WorkspaceID: "T1"}} {
		if err := s.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedConversation(domain.Conversation{ID: "CP", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("CP", "U1"); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}

	if err := messages.AddStar(ctx, "T1", "U2", "CP", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a non-member starring a private channel: err=%v, want not found", err)
	}
	if err := messages.RemoveStar(ctx, "T1", "U2", "CP", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a non-member unstarring a private channel: err=%v, want not found rather than not starred", err)
	}
	if page, err := messages.Stars(ctx, "T1", "U2", domain.PageRequest{Limit: 10}); err != nil || len(page.Stars) != 0 {
		t.Fatalf("a refused star was recorded: page=%+v err=%v", page, err)
	}

	// The positive control: the member of the channel stars and unstars it.
	if err := messages.AddStar(ctx, "T1", "U1", "CP", ""); err != nil {
		t.Fatalf("a member starring a private channel: %v", err)
	}
	if err := messages.RemoveStar(ctx, "T1", "U1", "CP", ""); err != nil {
		t.Fatalf("a member unstarring a private channel: %v", err)
	}
	if err := messages.RemoveStar(ctx, "T1", "U1", "CP", ""); !errors.Is(err, ErrNotStarred) {
		t.Fatalf("a member unstarring a channel twice: err=%v, want %v", err, ErrNotStarred)
	}
}
