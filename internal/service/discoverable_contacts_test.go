package service

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// In a discoverable workspace, a member found by email is one who has not
// turned that off in Privacy & visibility; turning it back on finds them again.
func TestAMemberCanOptOutOfBeingFoundByEmail(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test", Discoverability: domain.WorkspaceDiscoverabilityOpen}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "asker@example.com"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "open@example.com"})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Email: "private@example.com"})
	messages := Messages{Store: s}
	found := func() []domain.UserID {
		t.Helper()
		users, err := messages.DiscoverableContacts(ctx, "T1", "U1", []string{"open@example.com", "private@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]domain.UserID, 0, len(users))
		for _, user := range users {
			ids = append(ids, user.ID)
		}
		return ids
	}
	if got := found(); len(got) != 2 {
		t.Fatalf("before opting out: %v", got)
	}
	if err := messages.SetMemberPreference(ctx, "T1", "U3", DiscoverableByEmailPreference, "false"); err != nil {
		t.Fatal(err)
	}
	if got := found(); len(got) != 1 || got[0] != "U2" {
		t.Fatalf("after U3 opted out: %v", got)
	}
	if err := messages.SetMemberPreference(ctx, "T1", "U3", DiscoverableByEmailPreference, "true"); err != nil {
		t.Fatal(err)
	}
	if got := found(); len(got) != 2 {
		t.Fatalf("after U3 opted back in: %v", got)
	}
}
