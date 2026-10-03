package qualification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// clientConnectionsDecidePresence holds the leases behind presence: a member
// is connected until their latest lease lapses, closing one draws that back to
// the latest that remains, a renewal never shortens a lease, and the presence
// events are journalled only when the member goes online or offline.
func clientConnectionsDecidePresence(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	now := time.Now().UTC()
	head, err := f.repository.LatestEventSequence(ctx, f.workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	first := domain.ClientConnection{ID: domain.ClientConnectionID("cc-first-" + f.suffix), WorkspaceID: f.workspaceID, UserID: f.userID, ExpiresAt: now.Add(time.Minute)}
	second := domain.ClientConnection{ID: domain.ClientConnectionID("cc-second-" + f.suffix), WorkspaceID: f.workspaceID, UserID: f.userID, ExpiresAt: now.Add(2 * time.Minute)}
	if err := f.repository.OpenClientConnection(ctx, first, now, f.event("online-first", "user.presence_changed", string(f.userID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.OpenClientConnection(ctx, second, now, f.event("online-second", "user.presence_changed", string(f.userID))); err != nil {
		t.Fatal(err)
	}
	user, err := f.repository.GetUser(ctx, f.userID)
	if err != nil || !user.ConnectedUntil.Equal(second.ExpiresAt) {
		t.Fatalf("connected until %v err=%v, want the later lease %v", user.ConnectedUntil, err, second.ExpiresAt)
	}
	if count, err := f.repository.CountClientConnections(ctx, f.workspaceID, f.userID, now); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := f.repository.RenewClientConnection(ctx, f.workspaceID, f.userID, first.ID, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.RenewClientConnection(ctx, f.workspaceID, f.userID, "cc-missing-"+domain.ClientConnectionID(f.suffix), now.Add(time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("renewing a connection that is not there: %v", err)
	}
	if err := f.repository.CloseClientConnection(ctx, f.workspaceID, f.userID, second.ID, now, f.event("offline-second", "user.presence_changed", string(f.userID))); err != nil {
		t.Fatal(err)
	}
	user, err = f.repository.GetUser(ctx, f.userID)
	if err != nil || !user.ConnectedUntil.Equal(first.ExpiresAt) {
		t.Fatalf("connected until %v err=%v, want the remaining lease %v: a renewal never shortens one", user.ConnectedUntil, err, first.ExpiresAt)
	}
	if err := f.repository.CloseClientConnection(ctx, f.workspaceID, f.userID, first.ID, now, f.event("offline-first", "user.presence_changed", string(f.userID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.CloseClientConnection(ctx, f.workspaceID, f.userID, first.ID, now, f.event("offline-again", "user.presence_changed", string(f.userID))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("closing a closed connection: %v", err)
	}
	user, err = f.repository.GetUser(ctx, f.userID)
	if err != nil || !user.ConnectedUntil.IsZero() || user.ConnectedAt(now) {
		t.Fatalf("connected until %v err=%v, want no connection", user.ConnectedUntil, err)
	}

	// A lease its server never closed lapses, and the member's next
	// connection removes it.
	lapsed := domain.ClientConnection{ID: domain.ClientConnectionID("cc-lapsed-" + f.suffix), WorkspaceID: f.workspaceID, UserID: f.userID, ExpiresAt: now.Add(-time.Second)}
	if err := f.repository.OpenClientConnection(ctx, lapsed, now.Add(-time.Minute), f.event("online-lapsed", "user.presence_changed", string(f.userID))); err != nil {
		t.Fatal(err)
	}
	if count, err := f.repository.CountClientConnections(ctx, f.workspaceID, f.userID, now); err != nil || count != 0 {
		t.Fatalf("a lapsed lease counted: count=%d err=%v", count, err)
	}
	fresh := domain.ClientConnection{ID: domain.ClientConnectionID("cc-fresh-" + f.suffix), WorkspaceID: f.workspaceID, UserID: f.userID, ExpiresAt: now.Add(time.Minute)}
	if err := f.repository.OpenClientConnection(ctx, fresh, now, f.event("online-fresh", "user.presence_changed", string(f.userID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.RenewClientConnection(ctx, f.workspaceID, f.userID, lapsed.ID, now.Add(time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the lapsed lease survived the next connection: %v", err)
	}

	records, err := f.repository.ListEventsAfter(ctx, f.workspaceID, head, 100)
	if err != nil {
		t.Fatal(err)
	}
	var journalled []string
	for _, record := range records {
		journalled = append(journalled, string(record.Event.ID))
	}
	want := []string{"online-first-" + f.suffix, "offline-first-" + f.suffix, "online-lapsed-" + f.suffix, "online-fresh-" + f.suffix}
	if len(journalled) != len(want) {
		t.Fatalf("journalled %v, want %v: only going online or offline is announced", journalled, want)
	}
	for index := range want {
		if journalled[index] != want[index] {
			t.Fatalf("journalled %v, want %v", journalled, want)
		}
	}
}
