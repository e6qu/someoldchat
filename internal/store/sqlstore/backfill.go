package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Data migrations that rewrite whole columns do not belong inside the schema
// transaction, and they do not belong on the path a process must finish before
// it can serve.
//
// Migration step 78 rewrote every stored timestamp with one UPDATE per distinct
// value, every rewrite buffered in a Go slice first, all of it inside the single
// BEGIN IMMEDIATE that holds the schema_migration_lock fence. Measured on SQLite
// that ran at 790 rows/s, so a workspace with five million messages spent about
// an hour and three quarters on ONE of twenty-one columns with the fence held,
// no progress visible, hundreds of megabytes of rewrites resident, and no resume
// point: a crash at 80 % rolled the whole thing back.
//
// The replacement that shipped moved the work out of the fence and then put it
// back on the startup path, and made it QUADRATIC: every chunk re-ran
// `SELECT DISTINCT col … WHERE col > ? ORDER BY col LIMIT 500` against a column
// with no index, so every chunk was a full table scan plus a temp B-tree.
// Measured at 5k/10k/20k/40k rows it ran in 77 ms / 196 ms / 554 ms / 2.0 s —
// four times the time for twice the rows — which extrapolates to about 8.7 hours
// for messages.created_at on the five-million-row example, five times slower
// than the shape it replaced, repeated on every replica, inside Open.
//
// What this file does now:
//
//   - Every pass is LINEAR. The twenty columns that only need re-encoding get a
//     transient index on the column for the duration of the pass, so each chunk
//     is an index range seek over its own 500 values instead of a scan of the
//     table; the index is dropped when the pass finishes. messages.created_at is
//     walked through the covering index messages(conversation, created_at, id)
//     that already exists, one conversation at a time. Folded-search copies are
//     walked through their table's primary key, which needs no
//     transient index at all — an index on free text would be as large as the
//     table it is meant to make cheap. Measured rates are in
//     TestSQLiteBackfillRateIsLinear and TestSQLiteFoldBackfillRateIsLinear.
//
// The machinery is no longer timestamp-shaped. columnBackfill states which
// column a pass chunks by, which it reads, which it writes and how it derives
// one from the other, so a new column-wide rewrite is a registry entry rather
// than a second copy of the driver loop.
//   - Each chunk commits with its own durable cursor, so a crash resumes where
//     it stopped instead of starting over, and two replicas cooperate on one
//     scan rather than duplicating it.
//   - None of it runs under the migration fence AND none of it runs on the path
//     Open must complete. Migrate starts the drain on a goroutine and returns;
//     AwaitBackfills is how a caller that needs the rewrite finished — a test, a
//     readiness probe, an operator command — waits for it. Close cancels it.
//   - A value the drain cannot decode is counted in schema_backfills.rejected
//     and reported by BackfillStatus, so "done" no longer means "done, and also
//     silently skipped four rows for ever". ResetBackfill re-runs a pass.
//
// A row that has not been rewritten yet still DECODES correctly —
// domain.StoredTime reads the legacy encoding too — but it does not COMPARE
// correctly against a canonical value, and every read predicate in the
// repository is such a comparison. While a column is pending, a keyset page
// boundary, a lease fence or an unread count that straddles a legacy row can be
// wrong in either direction; the earlier claim in this file that the state was
// "exactly the state the database was already in before the upgrade" was false,
// because before the upgrade both sides of those comparisons were legacy and
// agreed. That window is the price of not holding the fence, and the way to
// keep it short is to keep every pass linear.

// backfillChunkSize is the number of rows (for messages.created_at) or distinct
// column values (for every other column) rewritten per transaction. It bounds
// resident memory, bounds how much work a crash discards, and bounds the
// parameter count of the CASE statement, which is two per value plus two.
const backfillChunkSize = 500

// backfillCursorSeparator joins the three parts of the messages.created_at
// cursor. It is below every byte that can appear in an identifier or in a
// timestamp encoding, so the lexical order of the joined string is the lexical
// order of the tuple.
const backfillCursorSeparator = "\n"

// columnBackfill is one column-wide rewrite.
//
// It used to be one column-wide RE-ENCODING — the struct was named
// timestampBackfill, its rewrite was hard-coded to domain.ParseStoredTime, and
// the column it read was the column it wrote. The folded-search columns need the
// same guarantees (chunked, off the startup path, resumable, linear, a value it
// cannot handle counted rather than fatal) and a different rewrite that reads one
// column and writes another, so the shape is now stated in fields:
//
//   - key is the column the pass orders and chunks by, and the value it stores in
//     schema_backfills.cursor. It must be unique per row or hold few enough
//     distinct values that a chunk of them is bounded work.
//   - source is the column the rewrite reads and target the column it writes. A
//     re-encoding sets all three to one column, which is the original behaviour.
//   - rewrite derives target's value from source's. An error counts the row in
//     schema_backfills.rejected and records a notice; only a fatalRewrite stops
//     the pass.
type columnBackfill struct {
	// name is the durable progress key. It is the table and the column written,
	// so a deployment that already ran one step and not another resumes correctly
	// and two steps that owe the same column collapse to one pass.
	name    string
	table   string
	key     string
	source  string
	target  string
	pending string
	rewrite func(string) (string, error)
	// index requests a transient index on key for the duration of the pass. A
	// pass that chunks by a column with no index of its own needs one or every
	// chunk is a full table scan; a pass that chunks by the primary key does not.
	index bool
}

// keyed reports whether the pass chunks by the same column it reads, which is
// the shape a re-encoding has and which lets the chunk query be a DISTINCT over
// one column.
func (b columnBackfill) keyed() bool { return b.key == b.source }

// fatalRewrite marks a rewrite failure that must stop the pass instead of being
// counted as one unrepairable row. It is for a broken invariant of this
// repository — a re-encoding that produced a non-order-preserving value — not for
// data a deployment might legitimately hold.
type fatalRewrite struct{ error }

// canonicalStoredTimeShape is a SQL predicate that is true for a value in the
// fixed-width, single-zone form domain.StoredTime writes.
//
// It is a SHAPE test, not a validity test, and the difference is deliberate.
// The previous predicate tested the length and the trailing 'Z' only and called
// itself "exact"; it was exact in neither direction. It selected the empty
// string, which is the "unset" marker every nullable timestamp column stores and
// which no rewrite can improve, and it did NOT select
// "2024-01-02 03:04:05.123456789Z" — thirty bytes, ends in 'Z', a space where
// the 'T' belongs — which domain.ParseStoredTime rejects and every read of that
// row therefore fails on. Pinning all seven separators leaves only digit-level
// corruption unselected, and a rewrite could not repair that either: it would
// have nothing to decode.
func canonicalStoredTimeShape(column string) string {
	return fmt.Sprintf("length(%[1]s) = %[2]d AND substr(%[1]s, 5, 1) = '-' AND substr(%[1]s, 8, 1) = '-' AND substr(%[1]s, 11, 1) = 'T' AND substr(%[1]s, 14, 1) = ':' AND substr(%[1]s, 17, 1) = ':' AND substr(%[1]s, 20, 1) = '.' AND substr(%[1]s, %[2]d, 1) = 'Z'",
		column, domain.StoredTimeWidth)
}

// normalizePending selects the values a re-encoding pass must rewrite: every
// non-empty value that is not already in the canonical shape.
func normalizePending(column string) string {
	return fmt.Sprintf("(%s <> '' AND NOT (%s))", column, canonicalStoredTimeShape(column))
}

// messagesCreatedAtBackfill is the re-encoding pass over messages.created_at.
// It is named because the identity pass depends on it: the identity pass reads
// rows in STORED order and can only assign identifiers if that order is
// chronological order, which is true exactly when every value is already in the
// order-preserving encoding.
const messagesCreatedAtBackfill = "messages.created_at"

// messagesIdentityBackfill is the pass that turns messages.created_at into a
// unique public identifier per conversation. See runMessageIdentityBackfill.
const messagesIdentityBackfill = "messages.created_at.identity"

// messagesConversationCreatedUniqueIndex is the invariant that makes a merged
// message identifier impossible rather than merely unlikely. See
// runMessageIdentityBackfill.
const messagesConversationCreatedUniqueIndex = "messages_conversation_created_unique"

// rewriteStoredTime is the re-encoding passes' rewrite: a stored instant in any
// encoding this repository has ever written, in the fixed-width order-preserving
// form.
func rewriteStoredTime(value string) (string, error) {
	parsed, err := domain.ParseStoredTime(value)
	if err != nil {
		// An undecodable value must not brick the process. Aborting here left the
		// schema at its old version and failed identically on every restart of
		// every binary, with no skip, no repair mode and no way to reach the
		// database through the product — and the authors already knew free-form
		// text lives in timestamp columns in the field, which is why
		// schema_migrations.applied_at is exempt from this rewrite.
		return "", err
	}
	normalized := domain.NewStoredTime(parsed)
	if !normalized.Ordered() {
		return "", fatalRewrite{fmt.Errorf("rewrite of %q produced the non-ordered value %q", value, string(normalized))}
	}
	return string(normalized), nil
}

// foldedColumns are the searchable values that carry a stored, Go-folded copy.
//
// Case-insensitive matching cannot be delegated to the engine: SQLite and dqlite
// fold ASCII only, PostgreSQL folds by the database's locale, and both search
// paths folded the query TERM in Go. The two disagreed for every non-ASCII
// character, so a message containing "ÄPFEL" was found by neither
// SearchMessages("äpfel") nor SearchMessages("ÄPFEL") on SQLite and dqlite while
// the in-memory and PostgreSQL profiles found it by both. See
// domain.FoldSearchText.
var foldedColumns = []struct{ table, source, folded string }{
	{"messages", "text", "text_folded"},
	{"conversations", "name", "name_folded"},
	{"conversations", "topic", "topic_folded"},
	{"conversations", "purpose", "purpose_folded"},
	{"files", "name", "name_folded"},
	{"files", "title", "title_folded"},
}

// foldPending selects the rows whose folded copy has not been computed yet.
//
// It is a SHAPE test, like canonicalStoredTimeShape, and for the same reason: a
// predicate that could compare the stored fold with the correct fold would need
// the engine to compute the correct fold, which is exactly the thing no engine
// agrees on. domain.FoldSearchText never maps a non-empty value to an empty one,
// so "source is not empty and the copy is" is true of precisely the rows written
// before the column existed and false of every row a writer of this release
// produced.
func foldPending(source, folded string) string {
	return fmt.Sprintf("(%s <> '' AND %s = '')", source, folded)
}

func foldBackfillNames() []string {
	names := make([]string, 0, len(foldedColumns))
	for _, target := range foldedColumns {
		names = append(names, target.table+"."+target.folded)
	}
	return names
}

// columnBackfills is the registry of every column-wide rewrite, keyed by name.
var columnBackfills = func() map[string]columnBackfill {
	registry := make(map[string]columnBackfill, len(storedTimestampColumns)+len(foldedColumns))
	for _, target := range storedTimestampColumns {
		name := target.table + "." + target.column
		registry[name] = columnBackfill{
			name: name, table: target.table,
			key: target.column, source: target.column, target: target.column,
			pending: normalizePending(target.column),
			rewrite: rewriteStoredTime,
			index:   true,
		}
	}
	for _, target := range foldedColumns {
		name := target.table + "." + target.folded
		registry[name] = columnBackfill{
			name: name, table: target.table,
			// Chunked by the primary key rather than by the value: the source is
			// free text, so an index on it would be as large as the table it is
			// meant to make cheap, and messages(id) and conversations(id) are
			// already indexed. Ordering by the primary key also makes each chunk
			// exactly the rows it rewrites, with no DISTINCT to collapse.
			key: "id", source: target.source, target: target.folded,
			pending: foldPending(target.source, target.folded),
			rewrite: func(value string) (string, error) { return domain.FoldSearchText(value), nil },
			index:   false,
		}
	}
	return registry
}()

// Migration notice kinds. A notice is a durable record of something the upgrade
// could not decide silently, addressed to an operator.
const (
	// MigrationNoticeUnparsedTimestamp records a stored value in a timestamp
	// column that no encoding this repository has ever written can decode.
	MigrationNoticeUnparsedTimestamp = "unparsed_timestamp"
	// MigrationNoticeEmailCleared records a workspace e-mail address removed
	// because two accounts collided once normalized.
	MigrationNoticeEmailCleared = "email_cleared"
	// MigrationNoticeMessageInstantMoved records a message whose public
	// timestamp the upgrade had to change because another message in the same
	// conversation already owned it.
	MigrationNoticeMessageInstantMoved = "message_instant_moved"
	// MigrationNoticeMessageInstantsNotUnique records that the uniqueness
	// invariant could not be installed because rows the upgrade could not decode
	// still share an identifier.
	MigrationNoticeMessageInstantsNotUnique = "message_instants_not_unique"
)

// migrationNoticeSubjectLimit caps how much of an offending value a notice
// records. schema_migration_notices is keyed by (kind, subject), so an
// unbounded subject is one unbounded row per distinct bad value, all written in
// one chunk transaction.
const migrationNoticeSubjectLimit = 96

// MigrationNotice is one recorded finding from a data migration.
type MigrationNotice struct {
	Kind       string
	Subject    string
	Detail     string
	ObservedAt time.Time
}

// MigrationNotices reports everything the data migrations recorded rather than
// decided. It exists so "the upgrade skipped something" is answerable without a
// SQL client: the alternative that shipped was an abort with no way through and
// no way to see what was wrong. runPendingBackfills logs the same set at WARN
// when a drain finishes, so an operator who never calls this still learns.
func (s *Store) MigrationNotices(ctx context.Context) ([]MigrationNotice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, subject, detail, observed_at FROM schema_migration_notices ORDER BY kind, subject`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notices := make([]MigrationNotice, 0)
	for rows.Next() {
		var notice MigrationNotice
		var observed string
		if err := rows.Scan(&notice.Kind, &notice.Subject, &notice.Detail, &observed); err != nil {
			return nil, err
		}
		// A notice's own timestamp must never be the reason a notice cannot be
		// read, so an undecodable one is reported as the zero instant.
		notice.ObservedAt, _ = domain.ParseStoredTime(observed)
		notices = append(notices, notice)
	}
	return notices, rows.Err()
}

func recordMigrationNotice(ctx context.Context, db queryExecutor, kind, subject, detail string, at time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO schema_migration_notices(kind, subject, detail, observed_at) VALUES (?, ?, ?, ?) ON CONFLICT(kind, subject) DO NOTHING`, kind, truncateNoticeSubject(subject), detail, domain.NewStoredTime(at))
	return err
}

func truncateNoticeSubject(subject string) string {
	if len(subject) <= migrationNoticeSubjectLimit {
		return subject
	}
	return subject[:migrationNoticeSubjectLimit] + "…"
}

// registerBackfills records the columns a migration step owes, inside
// the schema transaction, so the record and the version bump commit together.
//
// Registration RESETS the progress of a named backfill rather than skipping an
// existing row. It runs only from a `version < N` branch, and the migration
// transaction is fenced, so exactly one process ever executes a given step: a
// replica that starts after the winner committed sees the new version and never
// reaches this code, and a process that crashed mid-rewrite also sees the new
// version on restart and resumes from its stored cursor instead of restarting
// the scan.
func registerBackfills(ctx context.Context, db queryExecutor, names []string) error {
	for _, name := range names {
		if _, ok := columnBackfills[name]; !ok && name != messagesIdentityBackfill {
			return fmt.Errorf("unknown column backfill %q", name)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_backfills(name, cursor, done, rejected) VALUES (?, '', 0, 0) ON CONFLICT(name) DO UPDATE SET cursor = '', done = 0, rejected = 0`, name); err != nil {
			return fmt.Errorf("register backfill %q: %w", name, err)
		}
	}
	return nil
}

// reEncodingBackfillNames lists the pure re-encoding passes: one per stored
// timestamp column.
func reEncodingBackfillNames() []string {
	names := make([]string, 0, len(storedTimestampColumns))
	for _, target := range storedTimestampColumns {
		names = append(names, target.table+"."+target.column)
	}
	return names
}

// PendingBackfills reports the data migrations that still owe work. An empty
// result means every column has been walked to the end — see BackfillStatus for
// whether anything was skipped on the way.
func (s *Store) PendingBackfills(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schema_backfills WHERE done = 0 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// BackfillStatus is one data migration's durable progress.
type BackfillStatus struct {
	Name string
	Done bool
	// Rejected counts values the pass could not decode and therefore left in a
	// non-order-preserving encoding. A pass can be Done with Rejected > 0; that
	// is exactly the state the previous shape reported as "nothing pending".
	Rejected int
}

// BackfillStatuses reports every registered data migration and what it left
// behind. "Done with N unresolved" is a real outcome and has to be sayable.
func (s *Store) BackfillStatuses(ctx context.Context) ([]BackfillStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, done, rejected FROM schema_backfills ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := make([]BackfillStatus, 0)
	for rows.Next() {
		var status BackfillStatus
		var done int
		if err := rows.Scan(&status.Name, &done, &status.Rejected); err != nil {
			return nil, err
		}
		status.Done = done != 0
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

// ResetBackfill re-arms one data migration so the next drain walks it again.
// Without it, repairing the rows a pass rejected required hand-editing
// schema_backfills, which is the SQL client the notices exist to remove.
func (s *Store) ResetBackfill(ctx context.Context, name string) error {
	if _, ok := columnBackfills[name]; !ok && name != messagesIdentityBackfill {
		return fmt.Errorf("unknown column backfill %q", name)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO schema_backfills(name, cursor, done, rejected) VALUES (?, '', 0, 0) ON CONFLICT(name) DO UPDATE SET cursor = '', done = 0, rejected = 0`, name)
	return err
}

// runPendingBackfills drains every registered rewrite. It runs OUTSIDE the
// migration fence and off the startup path, and is safe to run concurrently with
// another replica doing the same: every chunk's UPDATE is idempotent and the
// durable cursor only ever moves forward, so two replicas cooperate on one scan
// instead of duplicating it.
func (s *Store) runPendingBackfills(ctx context.Context) error {
	pending, err := s.PendingBackfills(ctx)
	if err != nil {
		return fmt.Errorf("read pending backfills: %w", err)
	}
	for _, name := range pending {
		var runErr error
		switch task, known := columnBackfills[name]; {
		case name == messagesIdentityBackfill:
			runErr = s.runMessageIdentityBackfill(ctx)
		case known:
			runErr = s.runColumnBackfill(ctx, task)
		default:
			// A name written by a newer binary. Leaving it pending is correct:
			// this binary does not know how to satisfy it and must not mark it
			// done.
			continue
		}
		if runErr != nil {
			return fmt.Errorf("backfill %s: %w", name, runErr)
		}
	}
	if len(pending) > 0 {
		s.reportMigrationNotices(ctx)
	}
	return nil
}

// reportMigrationNotices is the operator surface the notices had none of. The
// table, MigrationNotices and BackfillStatuses were reachable only from the
// store's own tests, so an upgrade that cleared a sign-in address or skipped a
// timestamp told nobody.
func (s *Store) reportMigrationNotices(ctx context.Context) {
	statuses, err := s.BackfillStatuses(ctx)
	if err == nil {
		for _, status := range statuses {
			if status.Rejected > 0 {
				slog.Warn("data migration finished with unresolved values", "backfill", status.Name, "rejected", status.Rejected, "remedy", "repair the values and call ResetBackfill")
			}
		}
	}
	notices, err := s.MigrationNotices(ctx)
	if err != nil {
		return
	}
	for _, notice := range notices {
		slog.Warn("data migration notice", "kind", notice.Kind, "subject", notice.Subject, "detail", notice.Detail)
	}
}

// backfillChunkObserver is a test seam, in the same spirit as Store.now: the
// properties worth pinning about this code — that the migration fence is already
// released while it runs, that Open has already returned, that a crash mid-scan
// resumes from the durable cursor, and that the work is batched rather than one
// statement per row — are only observable from inside the loop. Returning an
// error aborts the pass, which is how a test simulates a process dying mid-scan
// without racing a real cancellation. It is nil in every non-test build.
// It is an atomic because the drain runs on its own goroutine now, so a test
// that installs and removes a seam races the drain unless the handoff is
// synchronised.
var backfillChunkObserver atomic.Pointer[func(name string, chunk int) error]

func setBackfillChunkObserver(observer func(name string, chunk int) error) {
	if observer == nil {
		backfillChunkObserver.Store(nil)
		return
	}
	backfillChunkObserver.Store(&observer)
}

func (s *Store) observeChunk(name string, chunk int) error {
	observer := backfillChunkObserver.Load()
	if observer == nil {
		return nil
	}
	return (*observer)(name, chunk)
}

// backfillIndexName is the transient index that makes a value-ordered pass
// linear.
func backfillIndexName(task columnBackfill) string {
	return "backfill_" + task.table + "_" + task.key
}

// createBackfillIndex is what removes the quadratic term. Without an index that
// leads with the column, `… WHERE col > ? AND pending ORDER BY col LIMIT 500` is
// a full table scan plus a temp B-tree PER CHUNK — EXPLAIN QUERY PLAN reports
// "SCAN messages USING COVERING INDEX messages_conversation_created" and "USE
// TEMP B-TREE FOR DISTINCT" — so the chunk's LIMIT bounds what Go receives and
// nothing else. With it, each chunk is a range seek that touches its own 500
// entries, the DISTINCT is satisfied by the index order, and the claim that the
// chunk bounds resident memory becomes true of the engine as well as of Go.
//
// It is created once per pass and dropped when the pass finishes. An interrupted
// pass deliberately leaves it: the resumed pass reuses it instead of paying for
// a second build.
func (s *Store) createBackfillIndex(ctx context.Context, task columnBackfill) error {
	if !task.index {
		return nil
	}
	statement := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s(%s)`, backfillIndexName(task), task.table, task.key)
	if err := underContention(ctx, func() error { _, err := s.db.ExecContext(ctx, statement); return err }); err != nil {
		return fmt.Errorf("create backfill index on %s: %w", task.name, err)
	}
	return nil
}

func (s *Store) dropBackfillIndex(ctx context.Context, task columnBackfill) error {
	if !task.index {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DROP INDEX IF EXISTS `+backfillIndexName(task)); err != nil {
		return fmt.Errorf("drop backfill index on %s: %w", task.name, err)
	}
	return nil
}

func (s *Store) runColumnBackfill(ctx context.Context, task columnBackfill) error {
	// Checked before the index is built, so a pass that is already finished — or
	// that was never registered, which is how a fresh database reaches this —
	// costs one row read and leaves nothing behind.
	if _, done, err := s.backfillProgress(ctx, task.name); err != nil || done {
		return err
	}
	if err := s.createBackfillIndex(ctx, task); err != nil {
		return err
	}
	// The chunk is SELECTed outside a transaction on purpose. A transaction that
	// reads and then writes takes its read snapshot first, which is both the
	// check-then-act shape and, on WAL, a source of unretryable snapshot
	// conflicts under concurrent replicas. A stale chunk here costs at most one
	// redundant idempotent UPDATE.
	for chunk := 1; ; chunk++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cursor, done, err := s.backfillProgress(ctx, task.name)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		values, err := s.selectBackfillChunk(ctx, task, cursor)
		if err != nil {
			return err
		}
		if len(values) == 0 {
			return s.finishColumnBackfill(ctx, task)
		}
		if err := s.observeChunk(task.name, chunk); err != nil {
			return err
		}
		// The drain is a background writer competing with live traffic now that
		// it no longer runs on the startup path, so it loses races and must wait
		// them out rather than failing the pass.
		if err := underContention(ctx, func() error { return s.applyBackfillChunk(ctx, task, cursor, values) }); err != nil {
			return err
		}
		if len(values) < backfillChunkSize {
			return s.finishColumnBackfill(ctx, task)
		}
	}
}

func (s *Store) backfillProgress(ctx context.Context, name string) (string, bool, error) {
	var cursor string
	var done int
	err := s.db.QueryRowContext(ctx, `SELECT cursor, done FROM schema_backfills WHERE name = ?`, name).Scan(&cursor, &done)
	if errors.Is(err, sql.ErrNoRows) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	return cursor, done != 0, nil
}

// backfillRow is one chunk entry: the value the pass chunks by and the value the
// rewrite reads. A pass that chunks by the column it reads has them equal, and
// its chunk query stays the DISTINCT it always was.
type backfillRow struct{ key, source string }

func (s *Store) selectBackfillChunk(ctx context.Context, task columnBackfill, cursor string) ([]backfillRow, error) {
	projection := `SELECT DISTINCT ` + task.key
	if !task.keyed() {
		projection = `SELECT ` + task.key + `, ` + task.source
	}
	query := projection + ` FROM ` + task.table +
		` WHERE ` + task.key + ` > ? AND ` + task.pending +
		` ORDER BY ` + task.key + ` LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, cursor, backfillChunkSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]backfillRow, 0, backfillChunkSize)
	for rows.Next() {
		var value backfillRow
		if task.keyed() {
			if err := rows.Scan(&value.key); err != nil {
				return nil, err
			}
			value.source = value.key
		} else if err := rows.Scan(&value.key, &value.source); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// applyBackfillChunk rewrites one chunk and advances the durable cursor in the
// same transaction, so progress and work are never out of step.
func (s *Store) applyBackfillChunk(ctx context.Context, task columnBackfill, cursor string, values []backfillRow) error {
	type rewrite struct{ key, to string }
	rewrites := make([]rewrite, 0, len(values))
	type reject struct {
		value  string
		reason string
	}
	rejects := make([]reject, 0)
	for _, value := range values {
		rewritten, err := task.rewrite(value.source)
		if err != nil {
			var fatal fatalRewrite
			if errors.As(err, &fatal) {
				return err
			}
			// An unrepairable value must not brick the process. Aborting left the
			// schema at its old version and failed identically on every restart of
			// every binary, with no skip, no repair mode and no way to reach the
			// database through the product. The value is left exactly as it is, it
			// is counted in schema_backfills.rejected so "done" cannot mean "done
			// and clean", and it is recorded so an operator can find it.
			rejects = append(rejects, reject{value: value.source, reason: err.Error()})
			continue
		}
		// A row that already holds the value the rewrite derives is not written.
		// For a re-encoding that is the common case; for a folded copy it is every
		// row a writer of this release produced.
		if task.keyed() && rewritten == value.source {
			continue
		}
		rewrites = append(rewrites, rewrite{key: value.key, to: rewritten})
	}

	last := values[len(values)-1].key
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(rewrites) > 0 {
		// One statement per chunk: "SET target = CASE key WHEN ? THEN ? … ELSE
		// target END WHERE key > ? AND key <= ?".
		//
		// The bound is the chunk's own key range, which is two parameters; the
		// previous shape repeated all 500 values a second time in an `IN (…)`
		// list, and under a non-deterministic ICU collation on PostgreSQL that
		// list could match a value no WHEN arm covered, whereupon the missing
		// ELSE wrote NULL into a NOT NULL column. `ELSE target` also makes the
		// statement idempotent if the engine revisits a row it has already
		// rewritten, which an UPDATE that moves rows within the index it is
		// scanning may do: a rewritten value matches no WHEN arm, so the second
		// visit is a no-op.
		var builder strings.Builder
		builder.WriteString(`UPDATE `)
		builder.WriteString(task.table)
		builder.WriteString(` SET `)
		builder.WriteString(task.target)
		builder.WriteString(` = CASE `)
		builder.WriteString(task.key)
		arguments := make([]any, 0, len(rewrites)*2+2)
		for _, value := range rewrites {
			builder.WriteString(` WHEN ? THEN ?`)
			arguments = append(arguments, value.key, value.to)
		}
		builder.WriteString(` ELSE `)
		builder.WriteString(task.target)
		builder.WriteString(` END WHERE `)
		builder.WriteString(task.key)
		builder.WriteString(` > ? AND `)
		builder.WriteString(task.key)
		builder.WriteString(` <= ?`)
		builder.WriteString(` AND `)
		builder.WriteString(task.pending)
		arguments = append(arguments, cursor, last)
		if _, err := tx.ExecContext(ctx, builder.String(), arguments...); err != nil {
			return err
		}
	}
	observed := s.now().UTC()
	for _, value := range rejects {
		subject := task.name + "=" + value.value
		if err := recordMigrationNotice(ctx, tx, MigrationNoticeUnparsedTimestamp, subject, value.reason, observed); err != nil {
			return err
		}
	}
	if err := advanceBackfillCursorOn(ctx, tx, task.name, last, len(rejects)); err != nil {
		return err
	}
	return tx.Commit()
}

// advanceBackfillCursorOn moves the durable cursor forward and counts what the
// chunk skipped. The cursor only moves forward, so a replica that is behind
// cannot rewind a replica that is ahead.
//
// Forward is decided by the column's collation rather than by byte order, and
// that is sufficient here: the chunk's ORDER BY uses the same collation, so the
// scan is monotone and terminating under any collation. It is NOT sufficient for
// the ordering guarantee the rewritten values themselves carry, which is a byte
// property — see the COLLATE "C" note on the PostgreSQL dialect.
func advanceBackfillCursorOn(ctx context.Context, db queryExecutor, name, cursor string, rejected int) error {
	_, err := db.ExecContext(ctx, `UPDATE schema_backfills SET cursor = ?, rejected = rejected + ? WHERE name = ? AND done = 0 AND cursor < ?`, cursor, rejected, name, cursor)
	return err
}

func (s *Store) finishColumnBackfill(ctx context.Context, task columnBackfill) error {
	if err := s.finishBackfill(ctx, task.name); err != nil {
		return err
	}
	return s.dropBackfillIndex(ctx, task)
}

func (s *Store) finishBackfill(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE schema_backfills SET done = 1 WHERE name = ?`, name)
	return err
}

// runMessageIdentityBackfill rewrites messages.created_at to the resolution of a
// message's own public identifier AND keeps every message individually
// addressable by it.
//
// A message's Slack-style timestamp is `<seconds>.<six digits>`: it is the
// message's public identifier and it carries microseconds. Storing an instant
// finer than that identifier makes the two disagree — a message created at
// .123456789 stores greater than a read cursor built from its own ts, which
// truncates to .123456, so the message can never be marked read. The shipped
// step 81 fixed that by truncating the STORED value, which is the wrong side:
// two messages 123 ns apart became byte-identical, so they shared one created_at
// and one ts, GetMessageByCreatedAt could only ever return the lower id, and the
// other message became permanently unaddressable. That is a merge of two
// identities, and the sub-microsecond difference that distinguished them is
// destroyed by the truncation itself.
//
// The scheme here is the one the real Slack timestamp uses: the identifier is
// unique BY CONSTRUCTION, not by luck of the clock. Within a conversation the
// pass walks messages in (created_at, id) order and assigns each one the first
// free microsecond at or after its own truncated instant, so
//
//   - a message whose truncated instant is not taken keeps it — the common case
//     rewrites the encoding and nothing else;
//   - a message that would collide moves forward by whole microseconds, in id
//     order, which preserves the relative order of the pair and gives the loser
//     an identifier of its own;
//   - the row that KEEPS the contested instant is the lowest id, which is the row
//     GetMessageByCreatedAt already resolved that instant to, so no reference
//     that resolves today starts resolving to a different message.
//
// What cannot be recovered, and is not claimed: for rows the shipped step 81
// already merged, the original sub-microsecond instants are gone, so the moved
// message's timestamp is a new identifier rather than its original one, and a
// thread_timestamp or read cursor recorded against the shared value stays
// attached to the row that kept it. Each move is recorded as a
// MigrationNoticeMessageInstantMoved so an operator can see exactly which
// messages changed identifier and by how much.
//
// The pass finishes by creating a UNIQUE index on (conversation, created_at).
// That is what makes the defect impossible rather than merely repaired: after
// it, no write can merge two identifiers, on any engine, whatever the clock did.
func (s *Store) runMessageIdentityBackfill(ctx context.Context) error {
	// The walk below reads rows in stored order and requires that order to be
	// chronological, which is only true once messages.created_at is in the
	// order-preserving encoding. Running the re-encoding first is not an
	// optimisation: under the legacy encoding "…:00Z" sorts after
	// "…:00.00001Z", so a walk over legacy values would hand out identifiers in
	// the wrong order and push hundreds of messages forward for nothing. The call
	// returns immediately when that pass is already finished or was never
	// registered.
	if err := s.runColumnBackfill(ctx, columnBackfills[messagesCreatedAtBackfill]); err != nil {
		return err
	}
	for chunk := 1; ; chunk++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cursor, done, err := s.backfillProgress(ctx, messagesIdentityBackfill)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		conversation, at, id := decodeMessageInstantCursor(cursor)
		if conversation == "" {
			next, found, err := s.nextConversationAfter(ctx, "")
			if err != nil {
				return err
			}
			if !found {
				return s.finishMessageIdentityBackfill(ctx)
			}
			if err := s.advanceMessageInstantCursor(ctx, next, "", ""); err != nil {
				return err
			}
			continue
		}
		rows, err := s.selectMessageInstantChunk(ctx, conversation, at, id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			next, found, err := s.nextConversationAfter(ctx, conversation)
			if err != nil {
				return err
			}
			if !found {
				return s.finishMessageIdentityBackfill(ctx)
			}
			if err := s.advanceMessageInstantCursor(ctx, next, "", ""); err != nil {
				return err
			}
			continue
		}
		if err := s.observeChunk(messagesIdentityBackfill, chunk); err != nil {
			return err
		}
		if err := underContention(ctx, func() error { return s.applyMessageInstantChunk(ctx, conversation, at, rows) }); err != nil {
			return err
		}
	}
}

// messageInstantConversationExhausted is the within-conversation cursor value
// written when a chunk reached the end of a conversation. It sorts above every
// value a timestamp column can hold, so the next read of that conversation
// returns nothing and the walk moves on. It exists because the last chunk of a
// conversation may END on a value the pass could not decode, and an undecodable
// value is not usable as a cursor: it can sort anywhere, so resuming from it can
// step over rows that sort below it.
const messageInstantConversationExhausted = "\U0010FFFF"

func encodeMessageInstantCursor(conversation, at, id string) string {
	return conversation + backfillCursorSeparator + at + backfillCursorSeparator + id
}

func decodeMessageInstantCursor(cursor string) (string, string, string) {
	parts := strings.SplitN(cursor, backfillCursorSeparator, 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

func (s *Store) advanceMessageInstantCursor(ctx context.Context, conversation, at, id string) error {
	return advanceBackfillCursorOn(ctx, s.db, messagesIdentityBackfill, encodeMessageInstantCursor(conversation, at, id), 0)
}

func (s *Store) nextConversationAfter(ctx context.Context, conversation string) (string, bool, error) {
	var next string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM conversations WHERE id > ? ORDER BY id LIMIT 1`, conversation).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return next, true, nil
}

type messageInstantRow struct {
	createdAt string
	id        string
}

// selectMessageInstantChunk reads the next rows of one conversation in
// (created_at, id) order. The predicate is written so
// messages(conversation, created_at, id) — which already exists and covers every
// column read here — serves it as one range seek.
func (s *Store) selectMessageInstantChunk(ctx context.Context, conversation, at, id string) ([]messageInstantRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT created_at, id FROM messages WHERE conversation = ? AND (created_at > ? OR (created_at = ? AND id > ?)) ORDER BY created_at, id LIMIT ?`,
		conversation, at, at, id, backfillChunkSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]messageInstantRow, 0, backfillChunkSize)
	for rows.Next() {
		var value messageInstantRow
		if err := rows.Scan(&value.createdAt, &value.id); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// applyMessageInstantChunk assigns identifiers to one chunk of one conversation
// and commits them with the cursor.
//
// A chunk that did not fill is the end of the conversation: every row in it is
// written and the cursor moves past the conversation, so an undecodable value at
// the tail cannot strand the walk.
//
// A chunk that filled is cut at the last row whose assigned instant is strictly
// below the next row's own truncated instant. Cutting there is what makes the
// cursor safe: every row the chunk wrote sits at or below the cursor, and every
// row it did not write sorts above it, so the next chunk can neither revisit a
// rewritten row nor step over an unwritten one.
func (s *Store) applyMessageInstantChunk(ctx context.Context, conversation, at string, rows []messageInstantRow) error {
	assigned := make([]string, len(rows))
	instants := make([]time.Time, len(rows))
	parsed := make([]bool, len(rows))

	previous := time.Time{}
	if at != "" {
		// The cursor's instant is the last identifier this pass handed out in
		// this conversation, so the chunk continues from it rather than from the
		// stored value of some row.
		if value, err := domain.ParseStoredTime(at); err == nil {
			previous = value
		}
	}
	for index, row := range rows {
		value, err := domain.ParseStoredTime(row.createdAt)
		if err != nil {
			continue
		}
		want := domain.MessageInstant(value)
		if !want.After(previous) {
			want = previous.Add(time.Microsecond)
		}
		instants[index] = want
		parsed[index] = true
		assigned[index] = string(domain.NewStoredTime(want))
		previous = want
	}

	cut := len(rows) - 1
	cursor := encodeMessageInstantCursor(conversation, messageInstantConversationExhausted, "")
	if len(rows) == backfillChunkSize {
		cut = -1
		for index := len(rows) - 2; index >= 0; index-- {
			next, err := domain.ParseStoredTime(rows[index+1].createdAt)
			if !parsed[index] || err != nil {
				continue
			}
			if instants[index].Before(domain.MessageInstant(next)) {
				cut = index
				break
			}
		}
		if cut < 0 {
			// Every row in the chunk is one unbroken cascade, or none of it
			// decodes, so there is no safe place to stop. Refusing is the honest
			// outcome: a silent cursor advance here would step over rows and
			// leave them colliding.
			return fmt.Errorf("conversation %q has %d consecutive messages this pass cannot separate, starting at %q; repair the data and call ResetBackfill", conversation, backfillChunkSize, rows[0].createdAt)
		}
		cursor = encodeMessageInstantCursor(conversation, assigned[cut], rows[cut].id)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	observed := s.now().UTC()
	rejected := 0
	for index := 0; index <= cut; index++ {
		if !parsed[index] {
			rejected++
			subject := messagesIdentityBackfill + "=" + rows[index].createdAt
			_, reason := domain.ParseStoredTime(rows[index].createdAt)
			if err := recordMigrationNotice(ctx, tx, MigrationNoticeUnparsedTimestamp, subject, reason.Error(), observed); err != nil {
				return err
			}
			continue
		}
		if assigned[index] == rows[index].createdAt {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET created_at = ? WHERE id = ?`, assigned[index], rows[index].id); err != nil {
			return err
		}
		moved, err := domain.ParseStoredTime(rows[index].createdAt)
		if err == nil && !domain.MessageInstant(moved).Equal(instants[index]) {
			// The row did not merely change encoding: its public identifier is a
			// different instant now, because the one it truncated to was taken.
			detail := fmt.Sprintf("message %s in conversation %s moved from %s to %s because another message already owned that microsecond",
				rows[index].id, conversation, domain.NewMessageTimestamp(domain.MessageInstant(moved)), domain.NewMessageTimestamp(instants[index]))
			if err := recordMigrationNotice(ctx, tx, MigrationNoticeMessageInstantMoved, rows[index].id, detail, observed); err != nil {
				return err
			}
		}
	}
	if err := advanceBackfillCursorOn(ctx, tx, messagesIdentityBackfill, cursor, rejected); err != nil {
		return err
	}
	return tx.Commit()
}

// finishMessageIdentityBackfill installs the uniqueness invariant. If rows the
// pass could not decode still share an identifier the index cannot be created;
// that is recorded and the pass still completes, because refusing to finish
// would leave the store permanently un-drained over data no rewrite can repair.
func (s *Store) finishMessageIdentityBackfill(ctx context.Context) error {
	statement := fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS %s ON messages(conversation, created_at)`, messagesConversationCreatedUniqueIndex)
	if _, err := s.db.ExecContext(ctx, statement); err != nil {
		if noticeErr := recordMigrationNotice(ctx, s.db, MigrationNoticeMessageInstantsNotUnique, messagesIdentityBackfill, err.Error(), s.now().UTC()); noticeErr != nil {
			return noticeErr
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE schema_backfills SET rejected = rejected + 1 WHERE name = ?`, messagesIdentityBackfill); err != nil {
			return err
		}
	}
	return s.finishBackfill(ctx, messagesIdentityBackfill)
}
