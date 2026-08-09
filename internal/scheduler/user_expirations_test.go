package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/scheduler"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Access is refused the moment an account lapses: both credential lookups
// exclude it. What did not happen is the account catching up — it stayed
// undeleted and its membership stayed active, so every other member went on
// seeing a full participant nobody could sign into.
func TestALapsedAccountIsDeactivatedOnce(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedUser(domain.User{ID: "UG", WorkspaceID: "T1", Name: "guest"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "UG")
	if err := store.SeedToken(ctx, "xoxp-guest", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "UG", TokenType: domain.TokenUser, Scopes: []string{"chat:write"},
	}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: store}

	now := time.Now().UTC().Truncate(time.Second)
	expiration := now.Add(-time.Hour)
	if err := messages.SetUserExpiration(ctx, "T1", "U1", "UG", expiration); err != nil {
		t.Fatal(err)
	}
	// The credential is already refused, which is the enforcement that exists.
	if _, err := store.LookupToken(ctx, "xoxp-guest"); err == nil {
		t.Fatal("a lapsed account could still authenticate")
	}
	// The account has not caught up: this is the gap the worker closes.
	before, err := store.GetUser(ctx, "UG")
	if err != nil || before.Deleted {
		t.Fatalf("guest=%+v err=%v, want it still listed as active before the sweep", before, err)
	}

	worker, err := scheduler.NewUserExpirationWorker(store, 10)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := worker.RunOnceAt(ctx, "T1", now)
	if err != nil || expired != 1 {
		t.Fatalf("expired=%d err=%v, want one account", expired, err)
	}

	guest, err := store.GetUser(ctx, "UG")
	if err != nil || !guest.Deleted {
		t.Fatalf("guest=%+v err=%v, want the account deactivated", guest, err)
	}
	// The membership is deactivated with the account, so the directory and the
	// seat count agree with the refusal the credential lookups already made.
	membership, err := store.GetWorkspaceMembership(ctx, "T1", "UG")
	if err != nil {
		t.Fatalf("read the guest's membership: %v", err)
	}
	if membership.Active {
		t.Fatal("a deactivated account kept an active membership")
	}

	// A second sweep finds nothing: the account is already deactivated, so it
	// is not due again and no second event is appended.
	second, err := worker.RunOnceAt(ctx, "T1", now)
	if err != nil || second != 0 {
		t.Fatalf("second sweep expired=%d err=%v, want none", second, err)
	}
}

// An account whose expiration has not arrived is left alone, and one with no
// expiration at all is never due.
func TestAnAccountIsNotDeactivatedBeforeItsExpiration(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedUser(domain.User{ID: "UG", WorkspaceID: "T1", Name: "guest"})
	store.SeedUser(domain.User{ID: "UP", WorkspaceID: "T1", Name: "permanent"})
	messages := service.Messages{Store: store}

	now := time.Now().UTC().Truncate(time.Second)
	if err := messages.SetUserExpiration(ctx, "T1", "U1", "UG", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	worker, err := scheduler.NewUserExpirationWorker(store, 10)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := worker.RunOnceAt(ctx, "T1", now)
	if err != nil || expired != 0 {
		t.Fatalf("expired=%d err=%v, want none", expired, err)
	}
	for _, id := range []domain.UserID{"UG", "UP"} {
		user, err := store.GetUser(ctx, id)
		if err != nil || user.Deleted {
			t.Fatalf("user %s=%+v err=%v, want it left alone", id, user, err)
		}
	}
}

// Two workers over one store must deactivate an account once between them: the
// claim is the deactivation, so the loser writes nothing.
func TestTwoWorkersDeactivateALapsedAccountOnce(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedUser(domain.User{ID: "UG", WorkspaceID: "T1", Name: "guest"})
	messages := service.Messages{Store: store}

	now := time.Now().UTC().Truncate(time.Second)
	if err := messages.SetUserExpiration(ctx, "T1", "U1", "UG", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	first, err := scheduler.NewUserExpirationWorker(store, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.NewUserExpirationWorker(store, 10)
	if err != nil {
		t.Fatal(err)
	}
	firstCount, firstErr := first.RunOnceAt(ctx, "T1", now)
	secondCount, secondErr := second.RunOnceAt(ctx, "T1", now)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("first err=%v second err=%v", firstErr, secondErr)
	}
	if firstCount+secondCount != 1 {
		t.Fatalf("first=%d second=%d, want exactly one deactivation between them", firstCount, secondCount)
	}
}
