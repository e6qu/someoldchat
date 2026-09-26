//go:build postgres

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/sqlstore"
)

// openIsolatedStore opens the repository in a schema of its own and returns it
// with a plain connection pool on the same schema, for driving transactions the
// repository API would not interleave on demand.
func openIsolatedStore(t *testing.T, ctx context.Context) (*sqlstore.Store, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("SAMEOLDCHAT_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SAMEOLDCHAT_POSTGRES_DSN is required for PostgreSQL qualification")
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	schemaName := fmt.Sprintf("sameoldchat_outbox_order_%d", time.Now().UnixNano())
	schemaIdentifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schemaIdentifier+" CASCADE"); err != nil {
			t.Errorf("drop outbox order test schema: %v", err)
		}
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	repository, err := Open(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return repository, raw
}

const rawOutboxInsert = `INSERT INTO outbox (id, workspace_id, actor_id, topic, payload, private_payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES ($1, 'T1', 'U1', 'message.created', $2, '', '2026-01-01T00:00:00.000000000Z', 0, '', '', '')`

func rawEventPayload(id string) string {
	return fmt.Sprintf(`{"type":"message.created","message_id":%q}`, id)
}

// A journal reader resumes with "sequence > cursor". An identity allocated at
// insert time let a transaction that inserted first commit last, below a cursor
// a reader had already advanced past, and that record was never delivered.
func TestOutboxRecordCommittedLateIsStillAfterTheReadersCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, raw := openIsolatedStore(t, ctx)

	early, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer early.Rollback()
	if _, err := early.ExecContext(ctx, rawOutboxInsert, "evA", rawEventPayload("A")); err != nil {
		t.Fatal(err)
	}
	late, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Rollback()
	if _, err := late.ExecContext(ctx, rawOutboxInsert, "evB", rawEventPayload("B")); err != nil {
		t.Fatal(err)
	}
	if err := late.Commit(); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ListEventsAfter(ctx, "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Event.ID != "evB" {
		t.Fatalf("first read returned %+v, want only the committed evB", first)
	}
	cursor := first[0].Sequence
	if err := early.Commit(); err != nil {
		t.Fatal(err)
	}
	second, err := repository.ListEventsAfter(ctx, "T1", cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Event.ID != "evA" || second[0].Sequence <= cursor {
		t.Fatalf("after evA committed, a reader at %d got %+v: the late commit landed behind the cursor", cursor, second)
	}
}

// The order is fixed at commit, so the advisory lock is taken only after a
// transaction's other locks are held. Taking it at insert time would deadlock
// two writers that each hold a row the other needs.
func TestOutboxCommitOrderDoesNotDeadlockWritersHoldingRowLocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, raw := openIsolatedStore(t, ctx)

	first, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback()
	if _, err := first.ExecContext(ctx, rawOutboxInsert, "ev1", rawEventPayload("1")); err != nil {
		t.Fatal(err)
	}
	second, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Rollback()
	if _, err := second.ExecContext(ctx, `UPDATE workspaces SET name = 'second' WHERE id = 'T1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ExecContext(ctx, rawOutboxInsert, "ev2", rawEventPayload("2")); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		// Waits on the row the second transaction holds.
		_, execErr := first.ExecContext(ctx, `UPDATE workspaces SET name = 'first' WHERE id = 'T1'`)
		if execErr == nil {
			execErr = first.Commit()
		}
		blocked <- execErr
	}()
	time.Sleep(100 * time.Millisecond)
	if err := second.Commit(); err != nil {
		t.Fatalf("the writer holding the row could not commit: %v", err)
	}
	if err := <-blocked; err != nil {
		t.Fatalf("the waiting writer failed: %v", err)
	}
}

// Many writers committing in a jittered order, and a reader following the
// journal by cursor while they do, must end with the reader having seen every
// record exactly once.
func TestConcurrentOutboxWritersNeverSkipACursorReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, raw := openIsolatedStore(t, ctx)
	raw.SetMaxOpenConns(32)

	const writers, perWriter = 16, 25
	var writing sync.WaitGroup
	failures := make(chan error, writers)
	for writer := range writers {
		writing.Add(1)
		go func() {
			defer writing.Done()
			for index := range perWriter {
				id := fmt.Sprintf("ev-%d-%d", writer, index)
				tx, err := raw.BeginTx(ctx, nil)
				if err != nil {
					failures <- err
					return
				}
				if _, err := tx.ExecContext(ctx, rawOutboxInsert, id, rawEventPayload(id)); err != nil {
					_ = tx.Rollback()
					failures <- err
					return
				}
				time.Sleep(time.Duration(rand.IntN(3000)) * time.Microsecond)
				if err := tx.Commit(); err != nil {
					failures <- err
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { writing.Wait(); close(done) }()

	seen := map[string]int{}
	cursor := uint64(0)
	read := func() {
		for {
			page, err := repository.ListEventsAfter(ctx, "T1", cursor, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range page {
				if record.Sequence <= cursor {
					t.Fatalf("sequence %d returned after cursor %d", record.Sequence, cursor)
				}
				cursor = record.Sequence
				seen[string(record.Event.ID)]++
			}
			if len(page) < 100 {
				return
			}
		}
	}
	for following := true; following; {
		select {
		case <-done:
			following = false
		default:
			read()
		}
	}
	read()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if len(seen) != writers*perWriter {
		t.Fatalf("a reader following the journal saw %d of %d records", len(seen), writers*perWriter)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s was delivered %d times", id, count)
		}
	}
}
