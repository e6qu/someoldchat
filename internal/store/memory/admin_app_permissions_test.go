package memory

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func TestAppPermissionsContract(t *testing.T) {
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	storetest.CheckAppPermissions(t, s)
}
