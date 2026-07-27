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
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// legacyTimestampDatabase writes count messages carrying the variable-width
// encoding a pre-correction release wrote, then winds the recorded schema
// version back so reopening replays the data migration against real data.
func legacyTimestampDatabase(t *testing.T, ctx context.Context, path string, count int, extra ...string) {
	t.Helper()
	legacyTimestampDatabaseAtVersion(t, ctx, path, 77, count, extra...)
}

func legacyTimestampDatabaseAtVersion(t *testing.T, ctx context.Context, path string, version, count int, extra ...string) {
	t.Helper()
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AwaitBackfills(ctx); err != nil {
		t.Fatal(err)
	}
	seedConversationFixture(t, ctx, first)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < count; index++ {
		// A microsecond apart with a trailing zero in the fraction, so every value
		// is distinct AND every value is variable width under RFC3339Nano.
		instant := base.Add(time.Duration(index) * 10 * time.Microsecond)
		insertRawMessage(t, ctx, first, fmt.Sprintf("M%d", index), instant.UTC().Format(time.RFC3339Nano))
	}
	for index, value := range extra {
		insertRawMessage(t, ctx, first, fmt.Sprintf("X%d", index), value)
	}
	rewindSchema(t, ctx, first, version)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
}

// insertRawMessage bypasses CreateMessage on purpose: these tests are about
// values no writer of this release can produce.
func insertRawMessage(t *testing.T, ctx context.Context, s *Store, id, createdAt string) {
	t.Helper()
	const insert = `INSERT INTO messages (id, workspace_id, conversation, author_id, text, blocks, attachments, thread_timestamp, created_at, deleted, unfurls) VALUES (?, 'T1', 'C1', 'U1', 'legacy', '', '[]', '', ?, 0, '{}')`
	if _, err := s.db.ExecContext(ctx, insert, id, createdAt); err != nil {
		t.Fatal(err)
	}
}

// rewindSchema puts the database back at an older release's recorded version and
// removes the artefacts this release's migration would otherwise skip, so
// reopening replays the upgrade against real data.
func rewindSchema(t *testing.T, ctx context.Context, s *Store, version int) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_migrations SET version = ? WHERE version = ?`, version, schemaVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP INDEX IF EXISTS `+messagesConversationCreatedUniqueIndex); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_backfills`); err != nil {
		t.Fatal(err)
	}
}

// openDrained is Open plus "and wait for the data migrations". Open deliberately
// does not wait; a test that inspects rewritten rows must say so.
func openDrained(t *testing.T, ctx context.Context, path string) *Store {
	t.Helper()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AwaitBackfills(ctx); err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	return s
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

// TestUpgradeKeepsTwoMessagesInOneMicrosecondDistinct is the data-loss case.
//
// The shipped step 81 truncated every stored instant to a microsecond in place,
// so two messages written 123 ns apart became byte-identical: one created_at,
// one public timestamp, and GetMessageByCreatedAt could only ever answer with
// the lower identifier. Before this change the test reported
//
//	two messages 123ns apart share one identifier after the upgrade:
//	"2024-01-01T00:00:00.123456000Z"
//
// and the lookup for the second message returned the first.
func TestUpgradeKeepsTwoMessagesInOneMicrosecondDistinct(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "distinct.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AwaitBackfills(ctx); err != nil {
		t.Fatal(err)
	}
	seedConversationFixture(t, ctx, first)
	insertRawMessage(t, ctx, first, "MA", "2024-01-01T00:00:00.123456111Z")
	insertRawMessage(t, ctx, first, "MB", "2024-01-01T00:00:00.123456999Z")
	insertRawMessage(t, ctx, first, "MC", "2024-01-01T00:00:00.2Z")
	rewindSchema(t, ctx, first, 77)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	migrated := openDrained(t, ctx, path)
	defer migrated.Close()

	var a, b, c string
	for _, row := range []struct {
		id    string
		value *string
	}{{"MA", &a}, {"MB", &b}, {"MC", &c}} {
		if err := migrated.db.QueryRowContext(ctx, `SELECT created_at FROM messages WHERE id = ?`, row.id).Scan(row.value); err != nil {
			t.Fatal(err)
		}
	}
	if a == b {
		t.Fatalf("two messages 123ns apart share one identifier after the upgrade: %q", a)
	}
	if a != "2024-01-01T00:00:00.123456000Z" {
		t.Fatalf("the lower identifier did not keep the contested microsecond: %q", a)
	}
	if b != "2024-01-01T00:00:00.123457000Z" {
		t.Fatalf("the moved message = %q, want the next free microsecond", b)
	}
	if c != "2024-01-01T00:00:00.200000000Z" {
		t.Fatalf("a message that never collided was moved: %q", c)
	}

	// Individually addressable: each message answers to its own public timestamp.
	for _, row := range []struct{ id, stored string }{{"MA", a}, {"MB", b}, {"MC", c}} {
		instant, err := domain.ParseStoredTime(row.stored)
		if err != nil {
			t.Fatal(err)
		}
		timestamp := domain.NewMessageTimestamp(instant)
		parsed, err := domain.ParseMessageTimestamp(timestamp)
		if err != nil {
			t.Fatal(err)
		}
		found, err := migrated.GetMessageByCreatedAt(ctx, "C1", parsed)
		if err != nil {
			t.Fatalf("message %s is not addressable by its own ts %s: %v", row.id, timestamp, err)
		}
		if string(found.ID) != row.id {
			t.Fatalf("ts %s resolves to %s, want %s", timestamp, found.ID, row.id)
		}
	}

	// And the move is on the record, because the instant it originally held is
	// not recoverable.
	notices, err := migrated.MigrationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var moved bool
	for _, notice := range notices {
		if notice.Kind == MigrationNoticeMessageInstantMoved && notice.Subject == "MB" {
			moved = true
		}
	}
	if !moved {
		t.Fatalf("notices=%+v, want the moved identifier recorded", notices)
	}
}

// TestCreateMessageRefusesATakenMicrosecond is the other half: after the
// upgrade, nothing may create the collision again. Before this change the second
// insert succeeded and the two rows shared one public timestamp.
func TestCreateMessageRefusesATakenMicrosecond(t *testing.T) {
	ctx := context.Background()
	s := openDrained(t, ctx, filepath.Join(t.TempDir(), "unique.db"))
	defer s.Close()
	seedConversationFixture(t, ctx, s)

	instant := time.Date(2024, 5, 1, 12, 0, 0, 123456000, time.UTC)
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "first", CreatedAt: instant}
	if err := s.CreateMessage(ctx, message, events.Event{ID: "evt_1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: instant}, ""); err != nil {
		t.Fatal(err)
	}
	// 123 nanoseconds later is the same microsecond, which is the same public
	// identifier.
	second := domain.Message{ID: "M2", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "second", CreatedAt: instant.Add(123 * time.Nanosecond)}
	if err := s.CreateMessage(ctx, second, events.Event{ID: "evt_2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: instant}, ""); !errors.Is(err, store.ErrMessageTimestampTaken) {
		t.Fatalf("second message on the same microsecond err=%v, want ErrMessageTimestampTaken", err)
	}
	second.CreatedAt = instant.Add(time.Microsecond)
	if err := s.CreateMessage(ctx, second, events.Event{ID: "evt_2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: instant}, ""); err != nil {
		t.Fatal(err)
	}
	// A different conversation may hold the same instant; the identifier is
	// per-conversation.
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	elsewhere := domain.Message{ID: "M3", WorkspaceID: "T1", Conversation: "C2", AuthorID: "U1", Text: "elsewhere", CreatedAt: instant}
	if err := s.CreateMessage(ctx, elsewhere, events.Event{ID: "evt_3", WorkspaceID: "T1", Topic: "message.created", Payload: "M3", CreatedAt: instant}, ""); err != nil {
		t.Fatalf("the same instant in another conversation was refused: %v", err)
	}
}

// TestSQLiteBackfillLeavesOpenFree is the property the design claimed and the
// shipped code contradicted: Migrate ran the whole drain itself, so every
// replica sat inside Open for the entire rewrite.
//
// Before this change the assertion below failed — Open had not returned while a
// chunk was in flight — because runPendingBackfills was Migrate's return value.
func TestSQLiteBackfillLeavesOpenFree(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "startup.db")
	legacyTimestampDatabase(t, ctx, path, 1200)

	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	setBackfillChunkObserver(func(name string, chunk int) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	})
	defer func() { setBackfillChunkObserver(nil) }()

	opened := make(chan *Store, 1)
	go func() {
		s, err := Open(context.Background(), path)
		if err != nil {
			close(opened)
			t.Error(err)
			return
		}
		opened <- s
	}()

	<-entered
	var migrated *Store
	select {
	case migrated = <-opened:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("Open did not return while a rewrite chunk was in flight: the upgrade is still an outage")
	}
	if migrated == nil {
		close(release)
		t.Fatal("Open failed")
	}
	// The store is usable while the rewrite runs.
	if _, err := migrated.PendingBackfills(ctx); err != nil {
		t.Fatalf("the store is unusable while the rewrite runs: %v", err)
	}
	close(release)
	setBackfillChunkObserver(nil)
	if err := migrated.AwaitBackfills(ctx); err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if unordered := countUnordered(t, ctx, migrated.db); unordered != 0 {
		t.Fatalf("%d rows still carry a variable-width timestamp", unordered)
	}
}

// TestSQLiteBackfillRunsWithTheMigrationFenceReleased probes the fence by
// actually taking it from a second connection with a short busy timeout.
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
	setBackfillChunkObserver(func(name string, chunk int) error {
		observed++
		if err := probe.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&recordedVersion); err != nil {
			fenceErr = fmt.Errorf("read version: %w", err)
			return nil
		}
		tx, err := probe.BeginTx(ctx, nil)
		if err != nil {
			fenceErr = fmt.Errorf("begin: %w", err)
			return nil
		}
		if _, err := tx.ExecContext(ctx, sqliteMigrationLockStatement); err != nil {
			_ = tx.Rollback()
			fenceErr = fmt.Errorf("take the migration fence: %w", err)
			return nil
		}
		if err := tx.Commit(); err != nil {
			fenceErr = fmt.Errorf("commit: %w", err)
		}
		return nil
	})
	defer func() { setBackfillChunkObserver(nil) }()

	migrated := openDrained(t, ctx, path)
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

// TestSQLiteBackfillResumesFromItsDurableCursor kills the pass mid-rewrite and
// requires the next start to continue rather than begin again. The previous
// shape had no resume point at all: a crash at 80 % rolled the whole rewrite
// back.
func TestSQLiteBackfillResumesFromItsDurableCursor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resume.db")
	const rows = backfillChunkSize * 3
	legacyTimestampDatabase(t, ctx, path, rows)

	interrupted := errors.New("interrupted")
	setBackfillChunkObserver(func(name string, chunk int) error {
		if name == messagesCreatedAtBackfill && chunk == 2 {
			return interrupted
		}
		return nil
	})
	partial, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := partial.AwaitBackfills(ctx); !errors.Is(err, interrupted) {
		t.Fatalf("the interrupted drain reported %v, want the interruption", err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	setBackfillChunkObserver(nil)

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
	setBackfillChunkObserver(func(name string, chunk int) error {
		if name == messagesCreatedAtBackfill && chunk == 1 {
			_ = db.QueryRowContext(ctx, `SELECT cursor FROM schema_backfills WHERE name = ?`, name).Scan(&firstChunkCursor)
		}
		return nil
	})
	defer func() { setBackfillChunkObserver(nil) }()
	resumed := openDrained(t, ctx, path)
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
// every binary" case, plus the half that was missing: a pass that skipped a
// value must not report itself clean, and there must be a supported way to run
// it again.
//
// Before this change PendingBackfills was empty and nothing anywhere recorded
// that a row in an ordering column had been stepped over — "rows left un-ordered
// after the backfill reported DONE: 1".
func TestSQLiteBackfillSurvivesAnUnparseableTimestamp(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "unparseable.db")
	legacyTimestampDatabase(t, ctx, path, 4, "not-a-timestamp")

	migrated := openDrained(t, ctx, path)
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

	// "Done" is not "done and clean".
	statuses, err := migrated.BackfillStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reported bool
	for _, status := range statuses {
		if status.Name != messagesCreatedAtBackfill {
			continue
		}
		reported = true
		if !status.Done {
			t.Fatal("the pass did not finish")
		}
		if status.Rejected != 1 {
			t.Fatalf("rejected=%d, want the skipped value counted", status.Rejected)
		}
	}
	if !reported {
		t.Fatalf("statuses=%+v, want the pass reported", statuses)
	}

	// And there is a supported re-run, so repairing the value is not a SQL-client
	// operation.
	if err := migrated.ResetBackfill(ctx, messagesCreatedAtBackfill); err != nil {
		t.Fatal(err)
	}
	pending, err := migrated.PendingBackfills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0] != messagesCreatedAtBackfill {
		t.Fatalf("pending after ResetBackfill = %v, want the pass re-armed", pending)
	}

	// And every subsequent start still works. This is the half that made the
	// original defect permanent rather than transient.
	reopened := openDrained(t, ctx, path)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteBackfillChunkQueryIsIndexed is the structural half of the
// performance claim. The shipped shape had no index leading with the column
// being rewritten, so EXPLAIN QUERY PLAN reported a full scan plus a temp B-tree
// for DISTINCT — per chunk — which is where the quadratic term came from and why
// the stated memory bound was false: LIMIT bounded what Go received, not what
// the engine materialised.
func TestSQLiteBackfillChunkQueryIsIndexed(t *testing.T) {
	ctx := context.Background()
	s := openDrained(t, ctx, filepath.Join(t.TempDir(), "plan.db"))
	defer s.Close()

	task := timestampBackfills["outbox.created_at"]
	if task.name == "" {
		t.Fatal("the re-encoding registry no longer holds outbox.created_at")
	}
	if err := s.createBackfillIndex(ctx, task); err != nil {
		t.Fatal(err)
	}
	query := `SELECT DISTINCT ` + task.column + ` FROM ` + task.table +
		` WHERE ` + task.column + ` > ? AND ` + task.pending +
		` ORDER BY ` + task.column + ` LIMIT ?`
	rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN `+query, "", backfillChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, " | ")
	if strings.Contains(strings.ToUpper(joined), "TEMP B-TREE") {
		t.Fatalf("the chunk query still materialises the whole distinct set: %s", joined)
	}
	if !strings.Contains(joined, backfillIndexName(task)) {
		t.Fatalf("the chunk query does not use the transient index: %s", joined)
	}
}

// TestSQLiteBackfillRateIsLinear is the measured half. The shipped shape was
// quadratic — 5k/10k/20k/40k rows in 77 ms / 196 ms / 554 ms / 2.0 s, four times
// the time for twice the rows — which extrapolates to about 8.7 hours for
// messages.created_at on the five-million-row example the design used, against
// the 1 h 45 m of the single-pass migration it replaced.
//
// The bar here is a shape bar, not a stopwatch bar: doubling the rows may not
// more than triple the time. A quadratic pass quadruples it.
func TestSQLiteBackfillRateIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("rewrites sixty thousand rows")
	}
	ctx := context.Background()
	measure := func(rows int) time.Duration {
		t.Helper()
		path := filepath.Join(t.TempDir(), fmt.Sprintf("rate-%d.db", rows))
		legacyTimestampDatabase(t, ctx, path, rows)
		started := time.Now()
		migrated := openDrained(t, ctx, path)
		elapsed := time.Since(started)
		defer migrated.Close()
		if unordered := countUnordered(t, ctx, migrated.db); unordered != 0 {
			t.Fatalf("%d rows still carry a variable-width timestamp", unordered)
		}
		t.Logf("%6d rows in %8s (%7.0f rows/s)", rows, elapsed.Round(time.Millisecond), float64(rows)/elapsed.Seconds())
		return elapsed
	}
	small := measure(10000)
	large := measure(20000)
	if large > 3*small {
		t.Fatalf("doubling the rows multiplied the time by %.1f (%s -> %s); the pass is not linear", float64(large)/float64(small), small, large)
	}
}

// TestSQLiteBackfillIsIdempotent pins that a completed rewrite costs nothing to
// repeat.
func TestSQLiteBackfillIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "idempotent.db")
	legacyTimestampDatabase(t, ctx, path, backfillChunkSize*2)
	migrated := openDrained(t, ctx, path)
	defer migrated.Close()
	if unordered := countUnordered(t, ctx, migrated.db); unordered != 0 {
		t.Fatalf("%d rows still carry a variable-width timestamp", unordered)
	}
	chunks := 0
	setBackfillChunkObserver(func(string, int) error { chunks++; return nil })
	defer func() { setBackfillChunkObserver(nil) }()
	if err := migrated.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration failed: %v", err)
	}
	if err := migrated.AwaitBackfills(ctx); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("a re-run did %d chunks of work, want none", chunks)
	}
	// The transient index the pass built for itself is not left behind.
	var indexes int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name LIKE 'backfill_%'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 {
		t.Fatalf("%d transient backfill indexes survived the drain", indexes)
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
				err = replica.AwaitBackfills(context.Background())
				if closeErr := replica.Close(); err == nil {
					err = closeErr
				}
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

	final := openDrained(t, ctx, path)
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
	var distinct int
	if err := final.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT created_at) FROM messages`).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != rows {
		t.Fatalf("%d distinct identifiers for %d messages — concurrent rewrites merged identities", distinct, rows)
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
// migration contract: a database created by this release owes the re-encodings
// nothing and says so. It still installs the uniqueness invariant, which is part
// of the schema this release promises.
func TestSQLiteBackfillIsANoOpOnAFreshDatabase(t *testing.T) {
	ctx := context.Background()
	chunks := 0
	setBackfillChunkObserver(func(string, int) error { chunks++; return nil })
	defer func() { setBackfillChunkObserver(nil) }()

	path := filepath.Join(t.TempDir(), "fresh.db")
	fresh := openDrained(t, ctx, path)
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
	statuses, err := fresh.BackfillStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Name != messagesIdentityBackfill {
		t.Fatalf("a fresh database registered %+v, want only the uniqueness pass", statuses)
	}
	notices, err := fresh.MigrationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 {
		t.Fatalf("a fresh database recorded %+v", notices)
	}
	var unique int
	if err := fresh.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, messagesConversationCreatedUniqueIndex).Scan(&unique); err != nil {
		t.Fatal(err)
	}
	if unique != 1 {
		t.Fatal("a fresh database did not get the message identifier uniqueness index")
	}
	if err := fresh.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration on a fresh database failed: %v", err)
	}
	if err := fresh.AwaitBackfills(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteBackfillRepairsAPopulatedDatabaseFromEveryShippedVersion is the
// "idempotent, resumable, safe against fresh and populated databases" contract
// stated as a test: every release that shipped one of these steps must arrive at
// the same state.
func TestSQLiteBackfillRepairsAPopulatedDatabaseFromEveryShippedVersion(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{77, 78, 81, 82} {
		t.Run(fmt.Sprintf("from-%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "upgrade.db")
			legacyTimestampDatabaseAtVersion(t, ctx, path, version, 40)
			migrated := openDrained(t, ctx, path)
			defer migrated.Close()
			if unordered := countUnordered(t, ctx, migrated.db); unordered != 0 {
				t.Fatalf("%d rows still carry a variable-width timestamp", unordered)
			}
			var rows, distinct int
			if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT created_at) FROM messages`).Scan(&rows, &distinct); err != nil {
				t.Fatal(err)
			}
			if rows != distinct {
				t.Fatalf("%d messages share %d identifiers", rows, distinct)
			}
			pending, err := migrated.PendingBackfills(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 0 {
				t.Fatalf("pending after the upgrade: %v", pending)
			}
		})
	}
}
