package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Every write of the data-migration drain waits out a writer that holds the
// database longer than one busy_timeout, as the chunk writes always did. The
// bookkeeping writes used a bare ExecContext, so four replicas finishing a pass
// together could fail it with "database is locked" under load.
func TestBackfillBookkeepingWaitsOutALongWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a write lock past the busy timeout")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "contention.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	holder, err := other.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(requiredBusyTimeout*time.Millisecond + time.Second)
		_, _ = holder.ExecContext(ctx, `COMMIT`)
	}()
	if err := store.ResetBackfill(ctx, "conversations.topic_folded"); err != nil {
		t.Fatalf("a drain write gave up on a writer that released the database: %v", err)
	}
	<-released
	if err := store.finishBackfill(ctx, "conversations.topic_folded"); err != nil {
		t.Fatal(err)
	}
}
