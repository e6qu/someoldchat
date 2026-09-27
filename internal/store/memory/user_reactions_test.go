package memory

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func TestUserReactionsPageByMessage(t *testing.T) {
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Kind: domain.ConversationTypePublic}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
	storetest.CheckUserReactionsPageByMessage(t, s)
}

func TestMessageCompanionEventsCommitWithTheMessage(t *testing.T) {
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Kind: domain.ConversationTypePublic}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
	storetest.CheckMessageCompanionEventsCommitWithTheMessage(t, s)
}

func TestOwnMessagesAreNeverUnread(t *testing.T) {
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "U2"} {
		if err := s.SeedUser(domain.User{ID: user, WorkspaceID: "T1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Kind: domain.ConversationTypePublic}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "U2"} {
		if err := s.SeedConversationMember("C1", user); err != nil {
			t.Fatal(err)
		}
	}
	storetest.CheckOwnMessagesAreNeverUnread(t, s)
}
