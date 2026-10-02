package sqlstore

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func TestSQLiteUserReactionsPageByMessage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Kind: domain.ConversationTypePublic}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember(ctx, "C1", "U1"); err != nil {
		t.Fatal(err)
	}
	storetest.CheckUserReactionsPageByMessage(t, s)
}

func TestSQLiteMessageCompanionEventsCommitWithTheMessage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Kind: domain.ConversationTypePublic}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember(ctx, "C1", "U1"); err != nil {
		t.Fatal(err)
	}
	storetest.CheckMessageCompanionEventsCommitWithTheMessage(t, s)
}

func TestSQLiteOwnMessagesAreNeverUnread(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "U2"} {
		if err := s.SeedUser(ctx, domain.User{ID: user, WorkspaceID: "T1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Kind: domain.ConversationTypePublic}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "U2"} {
		if err := s.SeedConversationMember(ctx, "C1", user); err != nil {
			t.Fatal(err)
		}
	}
	storetest.CheckOwnMessagesAreNeverUnread(t, s)
}

func TestSQLiteKeywordsMatchOnlyFollowedThreads(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "U2"} {
		if err := s.SeedUser(ctx, domain.User{ID: user, WorkspaceID: "T1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Kind: domain.ConversationTypePublic}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "U2"} {
		if err := s.SeedConversationMember(ctx, "C1", user); err != nil {
			t.Fatal(err)
		}
	}
	storetest.CheckKeywordsMatchOnlyFollowedThreads(t, s)
}
