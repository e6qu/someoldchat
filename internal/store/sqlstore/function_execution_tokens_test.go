package sqlstore

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/storetest"
)

func TestSQLiteFunctionExecutionTokens(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.UserID{"U1", "UB"} {
		if err := s.SeedUser(ctx, domain.User{ID: user, WorkspaceID: "T1"}); err != nil {
			t.Fatal(err)
		}
	}
	storetest.CheckFunctionExecutionTokens(t, s)
}
