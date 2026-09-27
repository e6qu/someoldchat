package memory

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func TestFunctionExecutionTokensAndFileAccessGrants(t *testing.T) {
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "UB"} {
		if err := s.SeedUser(domain.User{ID: user, WorkspaceID: "T1"}); err != nil {
			t.Fatal(err)
		}
	}
	storetest.CheckFunctionExecutionTokens(t, s)
	storetest.CheckFileAccessGrants(t, s)
}
