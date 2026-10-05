package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/crdt"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type canvasPresenceStore interface {
	RecordCanvasPresence(context.Context, domain.CanvasPresence, time.Time) error
	ClearCanvasPresence(context.Context, domain.WorkspaceID, domain.CanvasID, domain.UserID, string) error
	ListCanvasPresence(context.Context, domain.WorkspaceID, domain.CanvasID, time.Time) ([]domain.CanvasPresence, error)
}

// CheckCanvasPresence holds every profile to one presence contract: a page's
// row is replaced rather than appended, lapses at its expiry, is removed when
// the page leaves, and is read only on its own canvas and workspace.
func CheckCanvasPresence(t *testing.T, s canvasPresenceStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	page := func(user domain.UserID, session string, caret crdt.ID, expires time.Time) domain.CanvasPresence {
		return domain.CanvasPresence{WorkspaceID: "T1", CanvasID: "F1", UserID: user, Session: session, Caret: caret, ExpiresAt: expires}
	}
	for _, value := range []domain.CanvasPresence{
		page("U1", "first-tab", crdt.ID{Replica: "U1.first", Clock: 3}, now.Add(time.Minute)),
		{WorkspaceID: "T1", CanvasID: "F1", UserID: "U1", Session: "first-tab", Caret: crdt.ID{Replica: "U1.first", Clock: 7}, Anchor: crdt.ID{Replica: "seed", Clock: 2}, ExpiresAt: now.Add(time.Minute)},
		page("U1", "second-tab", crdt.ID{}, now.Add(time.Minute)),
		page("U2", "lapsing-tab", crdt.ID{}, now.Add(time.Second)),
		{WorkspaceID: "T1", CanvasID: "F2", UserID: "U2", Session: "other-canvas", ExpiresAt: now.Add(time.Minute)},
		{WorkspaceID: "T2", CanvasID: "F1", UserID: "U9", Session: "other-workspace", ExpiresAt: now.Add(time.Minute)},
	} {
		if err := s.RecordCanvasPresence(ctx, value, now); err != nil {
			t.Fatalf("record %+v: %v", value, err)
		}
	}
	present, err := s.ListCanvasPresence(ctx, "T1", "F1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 3 || present[0].Session != "first-tab" || present[0].Caret.Clock != 7 || present[0].Anchor != (crdt.ID{Replica: "seed", Clock: 2}) || present[1].Session != "second-tab" || !present[1].Caret.IsZero() || present[2].UserID != "U2" {
		t.Fatalf("present = %+v", present)
	}
	if present, err = s.ListCanvasPresence(ctx, "T1", "F1", now.Add(2*time.Second)); err != nil || len(present) != 2 {
		t.Fatalf("after a row lapses present = %+v err=%v", present, err)
	}
	if err := s.ClearCanvasPresence(ctx, "T1", "F1", "U1", "second-tab"); err != nil {
		t.Fatal(err)
	}
	if present, err = s.ListCanvasPresence(ctx, "T1", "F1", now); err != nil || len(present) != 2 || present[0].Session != "first-tab" {
		t.Fatalf("after a page leaves present = %+v err=%v", present, err)
	}
	for _, invalid := range []domain.CanvasPresence{
		page("U1", "short", crdt.ID{}, now.Add(time.Minute)),
		{WorkspaceID: "T1", CanvasID: "F1", UserID: "U1", Session: "anchor-alone", Anchor: crdt.ID{Replica: "seed", Clock: 2}, ExpiresAt: now.Add(time.Minute)},
	} {
		if err := s.RecordCanvasPresence(ctx, invalid, now); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("%+v recorded err=%v", invalid, err)
		}
	}
}
