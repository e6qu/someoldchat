package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// legacyTimestampDatabase writes count messages carrying the variable-width
// encoding a pre-correction release wrote, then winds the recorded schema
// version back so reopening replays the data migration against real data.
func legacyTimestampDatabase(t *testing.T, ctx context.Context, path string, count int, extra ...string) {
	t.Helper()
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedConversationFixture(t, ctx, first)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	insert := `INSERT INTO messages (id, workspace_id, conversation, author_id, text, blocks, attachments, thread_timestamp, created_at, deleted, unfurls) VALUES (?, 'T1', 'C1', 'U1', 'legacy', '', '[]', '', ?, 0, '{}')`
	for index := 0; index < count; index++ {
		// A microsecond apart with a trailing zero in the fraction, so every value
		// is distinct AND every value is variable width under RFC3339Nano.
		instant := base.Add(time.Duration(index) * 10 * time.Microsecond)
		if _, err := first.db.ExecContext(ctx, insert, fmt.Sprintf("M%d", index), instant.UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	for index, value := range extra {
		if _, err := first.db.ExecContext(ctx, insert, fmt.Sprintf("X%d", index), value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 77 WHERE version = ?`, schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
}

func inspect(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countUnordered(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var unordered int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE length(created_at) <> ? OR substr(created_at, ?, 1) <> 'Z'`, domain.StoredTimeWidth, domain.StoredTimeWidth).Scan(&unordered); err != nil {
		t.Fatal(err)
	}
	return unordered
}

// TestSQLiteBackfillRunsWithTheMigrationFenceReleased is the property that turns
// the upgrade from an outage into a slow start: by the time the first row is
// rewritten, the schema version is already recorded and the migration fence is
// already free, so every other replica and every other binary can start.
//
// The fence is probed by actually taking it from a second connection with a
// short busy timeout. If the migration transaction were still open — which is
// where this rewrite used to live, for its entire duration — that write would
// fail with SQLITE_BUSY instead of committing.
func TestSQLiteBackfillRunsWithTheMigrationFenceReleased(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fence.db")
	legacyTimestampDatabase(t, ctx, path, 3)

	probe, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(250)")
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()

	var observed int
	var recordedVersion int
	var fenceErr error
	backfillChunkObserver = func(name string, chunk int) {
		if name != messagesCreatedAtBackfill {
			return
		}
		observed++
		if err := probe.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&recordedVersion); err != nil {
			fenceErr = fmt.Errorf("read version: %w", err)
			return
		}
		tx, err := probe.BeginTx(ctx, nil)
		if err != nil {
			fenceErr = fmt.Errorf("begin: %w", err)
			return
		}
		if _, err := tx.ExecContext(ctx, sqliteMigrationLockStatement); err != nil {
			_ = tx.Rollback()
			fenceErr = fmt.Errorf("take the migration fence: %w", err)
			return
		}
		if err := tx.Commit(); err != nil {
			fenceErr = fmt.Errorf("commit: %w", err)
		}
	}
	defer func() { backfillChunkObserver = nil }()

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if observed == 0 {
		t.Fatal("the rewrite never ran, so the property was not exercised")
	}
	if fenceErr != nil {
		t.Fatalf("the migration fence was still held while the rewrite ran: %v", fenceErr)
	}
	if recordedVersion != schemaVersion {
		t.Fatalf("schema version during the rewrite = %d, want %d already recorded", recordedVersion, schemaVersion)
	}
	if unordered := countUnordered(t, ctx, migrated.db); unordered != 0 {
		t.Fatalf("%d rows still carry a variable-width timestamp", unordered)
	}
}

// TestSQLiteBackfillResumesFromItsDurableCursor kills the process mid-rewrite and
// requires the next start to continue rather than begin again. The previous shape
// had no resume point at all: a crash at 80 % rolled the whole rewrite back.
func TestSQLiteBackfillResumesFromItsDurableCursor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resume.db")
	const rows = backfillChunkSize * 3
	legacyTimestampDatabase(t, ctx, path, rows)

	interrupted, cancel := context.WithCancel(ctx)
	backfillChunkObserver = func(name string, chunk int) {
		if name == messagesCreatedAtBackfill && chunk == 2 {
			cancel()
		}
	}
	if _, err := Open(interrupted, path); err == nil {
		t.Fatal("the interrupted start reported success")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted start err=%v, want a cancellation", err)
	}
	backfillChunkObserver = nil
	cancel()

	db := inspect(t, path)
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version after an interrupted rewrite = %d, want %d", version, schemaVersion)
	}
	var cursor string
	var done int
	if err := db.QueryRowContext(ctx, `SELECT cursor, done FROM schema_backfills WHERE name = ?`, messagesCreatedAtBackfill).Scan(&cursor, &done); err != nil {
		t.Fatal(err)
	}
	if done != 0 || cursor == "" {
		t.Fatalf("progress after an interrupted rewrite: cursor=%q done=%d, want an unfinished cursor", cursor, done)
	}
	remaining := countUnordered(t, ctx, db)
	if remaining == 0 || remaining == rows {
		t.Fatalf("%d of %d rows remain unrewritten; the interruption left either no progress or no work", remaining, rows)
	}

	// The next start resumes: it must not rescan from the beginning, and it must
	// finish.
	var firstChunkCursor string
	backfillChunkObserver = func(name string, chunk int) {
		if name == messagesCreatedAtBackfill && chunk == 1 {
			_ = db.QueryRowContext(ctx, `SELECT cursor FROM schema_backfills WHERE name = ?`, name).Scan(&firstChunkCursor)
		}
	}
	defer func() { backfillChunkObserver = nil }()
	resumed, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if firstChunkCursor != cursor {
		t.Fatalf("the resumed run started at cursor %q, want the stored %q", firstChunkCursor, cursor)
	}
	if unordered := countUnordered(t, ctx, resumed.db); unordered != 0 {
		t.Fatalf("%d rows still unrewritten after resuming", unordered)
	}
	pending, err := resumed.PendingBackfills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending backfills after completion: %v", pending)
	}
}

// TestSQLiteBackfillSurvivesAnUnparseableTimestamp is the "one bad byte bricks
// every binary" case. Aborting the migration left the schema at its old version
// and failed identically on every restart of every process, with no skip, no
// repair mode and no way to reach the database through the product.
func TestSQLiteBackfillSurvivesAnUnparseableTimestamp(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "unparseable.db")
	legacyTimestampDatabase(t, ctx, path, 4, "not-a-timestamp")

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("an unparseable value in one row must not stop the upgrade: %v", err)
	}
	defer migrated.Close()

	var version int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	// The decodable rows were rewritten; the undecodable one was left exactly as
	// it is, which is no worse than before the upgrade.
	var survivors int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE created_at = 'not-a-timestamp'`).Scan(&survivors); err != nil {
		t.Fatal(err)
	}
	if survivors != 1 {
		t.Fatalf("the unparseable value was rewritten or lost (%d rows)", survivors)
	}
	if unordered := countUnordered(t, ctx, migrated.db); unordered != 1 {
		t.Fatalf("%d rows are not in the fixed-width form, want only the unparseable one", unordered)
	}

	notices, err := migrated.MigrationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var recorded bool
	for _, notice := range notices {
		if notice.Kind == MigrationNoticeUnparsedTimestamp && strings.Contains(notice.Subject, "not-a-timestamp") {
			recorded = true
			if !strings.Contains(notice.Subject, messagesCreatedAtBackfill) {
				t.Fatalf("notice subject=%q, want the column named", notice.Subject)
			}
			if notice.Detail == "" {
				t.Fatal("notice carries no reason")
			}
		}
	}
	if !recorded {
		t.Fatalf("notices=%+v, want the skipped value recorded so an operator can find it", notices)
	}

	// And every subsequent start still works. This is the half that made the
	// original defect permanent rather than transient.
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening a database containing an unparseable timestamp failed: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteBackfillBatchesItsWritesAndIsIdempotent pins the shape that produced
// the speed-up and reports the rate. The bar is the measured 790 rows/s of the
// one-UPDATE-per-distinct-value form; the structural assertion is that a chunk is
// one statement rather than backfillChunkSize of them, which is what removes one
// Raft round trip per row on dqlite.
func TestSQLiteBackfillBatchesItsWritesAndIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("rewrites twenty thousand rows")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rate.db")
	const rows = 20000
	legacyTimestampDatabase(t, ctx, path, rows)

	chunks := 0
	backfillChunkObserver = func(name string, _ int) {
		if name == messagesCreatedAtBackfill {
			chunks++
		}
	}
	defer func() { backfillChunkObserver = nil }()

	started := time.Now()
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	elapsed := time.Since(started)

	want := (rows + backfillChunkSize - 1) / backfillChunkSize
	if chunks != want {
		t.Fatalf("the rewrite took %d chunks for %d rows, want %d — the writes are not batched", chunks, rows, want)
	}
	if unordered := countUnordered(t, ctx, migrated.db); unordered != 0 {
		t.Fatalf("%d rows still carry a variable-width timestamp", unordered)
	}
	t.Logf("rewrote %d rows in %s (%.0f rows/s) in %d chunks", rows, elapsed, float64(rows)/elapsed.Seconds(), chunks)

	// Idempotent, and free on a re-run: the pre-filter selects nothing, so the
	// second pass does no chunks at all.
	chunks = 0
	if err := migrated.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration failed: %v", err)
	}
	if chunks != 0 {
		t.Fatalf("a re-run did %d chunks of work, want none", chunks)
	}
}

// TestSQLiteBackfillIsSafeWhenReplicasStartTogether drives enough rows that the
// replicas genuinely overlap inside the rewrite loop rather than racing only for
// the fence.
func TestSQLiteBackfillIsSafeWhenReplicasStartTogether(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "replicas.db")
	const rows = backfillChunkSize * 4
	legacyTimestampDatabase(t, ctx, path, rows)

	const replicas = 4
	start := make(chan struct{})
	results := make(chan error, replicas)
	var group sync.WaitGroup
	for index := 0; index < replicas; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			replica, err := Open(context.Background(), path)
			if err == nil {
				err = replica.Close()
			}
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent replica startup failed: %v", err)
		}
	}

	final, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	if unordered := countUnordered(t, ctx, final.db); unordered != 0 {
		t.Fatalf("%d rows still carry a variable-width timestamp after concurrent startup", unordered)
	}
	var stored int
	if err := final.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != rows {
		t.Fatalf("stored %d messages, want %d — concurrent rewrites merged or dropped rows", stored, rows)
	}
	pending, err := final.PendingBackfills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending backfills after concurrent startup: %v", pending)
	}
}

// TestSQLiteBackfillIsANoOpOnAFreshDatabase covers the other half of the
// migration contract: a database created by this release owes the rewrite
// nothing, and says so.
func TestSQLiteBackfillIsANoOpOnAFreshDatabase(t *testing.T) {
	ctx := context.Background()
	chunks := 0
	backfillChunkObserver = func(string, int) { chunks++ }
	defer func() { backfillChunkObserver = nil }()

	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if chunks != 0 {
		t.Fatalf("a fresh database did %d chunks of rewriting", chunks)
	}
	pending, err := fresh.PendingBackfills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a fresh database owes %v", pending)
	}
	notices, err := fresh.MigrationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 {
		t.Fatalf("a fresh database recorded %+v", notices)
	}
	if err := fresh.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration on a fresh database failed: %v", err)
	}
}
