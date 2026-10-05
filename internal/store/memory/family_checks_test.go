package memory

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func newFamilyCheckStore(t *testing.T) *Store {
	t.Helper()
	s := New()
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		if err := s.SeedWorkspace(domain.Workspace{ID: workspace, Name: string(workspace)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	for _, conversation := range []domain.Conversation{{ID: "C1", WorkspaceID: "T1", Name: "one"}, {ID: "C2", WorkspaceID: "T1", Name: "two"}, {ID: "CX", WorkspaceID: "T2", Name: "elsewhere"}} {
		if err := s.SeedConversation(conversation); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestGuestStatusEventsCommitWithTheChange(t *testing.T) {
	storetest.CheckGuestStatusEventsCommitWithTheChange(t, newFamilyCheckStore(t))
}

func TestShortTokenRotation(t *testing.T) {
	storetest.CheckShortTokenRotation(t, newFamilyCheckStore(t))
}

func TestExistingConversationsExcludedFromAI(t *testing.T) {
	storetest.CheckExistingConversationsExcludedFromAI(t, newFamilyCheckStore(t))
}

func TestMemoryCanvasPresence(t *testing.T) {
	storetest.CheckCanvasPresence(t, newFamilyCheckStore(t))
}
