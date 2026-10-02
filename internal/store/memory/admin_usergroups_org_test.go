package memory

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func TestOrganizationUserGroups(t *testing.T) {
	s := New()
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		if err := s.SeedWorkspace(domain.Workspace{ID: workspace}); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []domain.User{{ID: "U1", WorkspaceID: "T1"}, {ID: "UB", WorkspaceID: "T1"}, {ID: "UX", WorkspaceID: "T2"}} {
		if err := s.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	storetest.CheckOrganizationUserGroups(t, s)
}
