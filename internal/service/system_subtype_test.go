package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A system subtype is written only by the store call that makes the change it
// reports, so a posted message cannot claim one: a forged huddle, join or
// rename is refused.
func TestPostedMessagesCannotClaimASystemSubtype(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}
	for _, subtype := range []domain.MessageSubtype{domain.MessageSubtypeHuddleThread, domain.MessageSubtypeChannelJoin, domain.MessageSubtypeChannelName} {
		_, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{Conversation: "C1", Text: "forged", Subtype: subtype})
		if !errors.Is(err, domain.ErrInvalidMessage) {
			t.Errorf("%s: err=%v, want ErrInvalidMessage", subtype, err)
		}
	}
	if _, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{Conversation: "C1", Text: "waves", Subtype: domain.MessageSubtypeMeMessage}); err != nil {
		t.Fatalf("me_message: %v", err)
	}
}
