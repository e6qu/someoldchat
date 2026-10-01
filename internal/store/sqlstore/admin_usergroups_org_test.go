package sqlstore

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func TestSQLiteOrganizationUserGroups(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		if err := s.SeedWorkspace(ctx, domain.Workspace{ID: workspace}); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []domain.User{{ID: "U1", WorkspaceID: "T1"}, {ID: "UB", WorkspaceID: "T1"}, {ID: "UX", WorkspaceID: "T2"}} {
		if err := s.SeedUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	storetest.CheckOrganizationUserGroups(t, s)
}
