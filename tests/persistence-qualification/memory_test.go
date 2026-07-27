//go:build !dqlite && !postgres

package qualification

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestMemoryQualification runs the whole backend-neutral contract against the
// in-memory repository. It used to be excluded from this suite, which is why the
// in-memory profile — a selectable storage profile, not a test double — could
// disagree with the SQL profiles on validation, sentinel errors, ordering and
// normalization without anything failing.
func TestMemoryQualification(t *testing.T) {
	runQualification(t, func(t *testing.T, _ context.Context) (qualificationStore, func()) {
		return memoryQualificationStore{Store: memory.New()}, func() {}
	})
}

// memoryQualificationStore adapts the in-memory seed helpers to the suite's
// context-and-error signature. The in-memory helpers cannot take a context: they
// are called from roughly two hundred test call sites in packages this change
// does not own, and adding a leading parameter to them is not a source-compatible
// edit. Returning an error is, which is why the helpers now report invalid input
// instead of panicking or silently doing nothing; only the context parameter is
// bridged here. See the follow-up note in README.md.
type memoryQualificationStore struct{ *memory.Store }

func (s memoryQualificationStore) SeedWorkspace(_ context.Context, value domain.Workspace) error {
	return s.Store.SeedWorkspace(value)
}

func (s memoryQualificationStore) SeedUser(_ context.Context, value domain.User) error {
	return s.Store.SeedUser(value)
}

func (s memoryQualificationStore) SeedConversation(_ context.Context, value domain.Conversation) error {
	return s.Store.SeedConversation(value)
}

func (s memoryQualificationStore) SeedConversationMember(_ context.Context, conversation domain.ConversationID, user domain.UserID) error {
	return s.Store.SeedConversationMember(conversation, user)
}
