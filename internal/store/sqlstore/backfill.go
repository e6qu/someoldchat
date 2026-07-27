package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Data migrations that rewrite whole columns do not belong inside the schema
// transaction.
//
// Migration step 78 rewrote every stored timestamp with one UPDATE per distinct
// value, every rewrite buffered in a Go slice first, all of it inside the single
// BEGIN IMMEDIATE that holds the schema_migration_lock fence. Measured on SQLite
// that ran at 790 rows/s, so a workspace with five million messages spent about
// an hour and three quarters on ONE of twenty-one columns with the fence held,
// no progress visible, hundreds of megabytes of rewrites resident, and no resume
// point: a crash at 80 % rolled the whole thing back. On dqlite each of those
// UPDATEs is a separate round trip to the leader, and on PostgreSQL the single
// long transaction pins the oldest xmin and blocks autovacuum for the duration.
// An upgrade is not allowed to be an outage.
//
// The shape here is different in four ways:
//
//   - the work is SELECTED in bounded chunks by a SQL predicate that already
//     excludes correct values, so a re-run costs one query per column and the
//     working set is only the rows that are genuinely legacy;
//   - each chunk is one UPDATE with a CASE expression, so a chunk is one round
//     trip and one Raft commit rather than several hundred;
//   - each chunk commits with its own durable cursor, so a crash resumes where
//     it stopped instead of starting over;
//   - none of it runs under the migration fence. The schema transaction records
//     the schema version and the set of backfills owed, and commits; the
//     rewriting happens afterwards, so a replica that is waiting on the fence
//     starts serving as soon as the schema is current.
//
// A row that has not been rewritten yet reads correctly — domain.StoredTime
// decodes the legacy encoding too — it is only mis-ORDERED against its
// neighbours, which is exactly the state the database was already in before the
// upgrade. Trading a temporary continuation of a pre-existing defect for hours
// of total unavailability is the whole point.

// backfillChunkSize is the number of distinct column values rewritten per
// transaction. It bounds resident memory (at most this many strings), bounds the
// parameter count of the CASE statement (three per value, well inside both
// engines' limits), and bounds how much work a crash discards.
const backfillChunkSize = 500

// timestampBackfill is one column-wide rewrite.
type timestampBackfill struct {
	// name is the durable progress key. It is the table and column, so a
	// deployment that already ran one step and not another resumes correctly and
	// two steps that owe the same column collapse to one pass.
	name    string
	table   string
	column  string
	pending string
	rewrite func(time.Time) domain.StoredTime
}

// normalizePending selects values that are not in the fixed-width, single-zone
// form. A value of exactly StoredTimeWidth bytes ending in 'Z' that parses as
// RFC 3339 can only be the canonical form, so this predicate is exact: it never
// selects a row that would not change, which is what makes a re-run free.
func normalizePending(column string) string {
	return fmt.Sprintf("(length(%s) <> %d OR substr(%s, %d, 1) <> 'Z')", column, domain.StoredTimeWidth, column, domain.StoredTimeWidth)
}

// truncatePending additionally selects values carrying precision finer than the
// microsecond a message's own public timestamp can express.
func truncatePending(column string) string {
	return fmt.Sprintf("(length(%s) <> %d OR substr(%s, %d, 1) <> 'Z' OR substr(%s, %d, 3) <> '000')",
		column, domain.StoredTimeWidth, column, domain.StoredTimeWidth, column, domain.StoredTimeWidth-3)
}

func normalizeRewrite(value time.Time) domain.StoredTime { return domain.NewStoredTime(value) }

func truncateRewrite(value time.Time) domain.StoredTime {
	return domain.NewStoredTime(domain.MessageInstant(value))
}

// messagesCreatedAtBackfill is named separately because two migration steps owe
// it: step 78 (encoding) and step 81 (resolution). They are the same pass, so a
// database upgrading from 77 rewrites the messages table once rather than twice.
const messagesCreatedAtBackfill = "messages.created_at"

// timestampBackfills is the registry keyed by name.
var timestampBackfills = func() map[string]timestampBackfill {
	registry := make(map[string]timestampBackfill, len(storedTimestampColumns))
	for _, target := range storedTimestampColumns {
		name := target.table + "." + target.column
		task := timestampBackfill{name: name, table: target.table, column: target.column, pending: normalizePending(target.column), rewrite: normalizeRewrite}
		if name == messagesCreatedAtBackfill {
			// A message's timestamp IS its public identifier and carries
			// microseconds. A stored instant finer than that identifier can never
			// be matched by a read cursor, a thread-root lookup or a
			// created-at lookup built from it, so the message stays permanently
			// unread. Normalising the encoding alone carried that across the
			// upgrade intact.
			task.pending = truncatePending(target.column)
			task.rewrite = truncateRewrite
		}
		registry[name] = task
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
)

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
// no way to see what was wrong.
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
	_, err := db.ExecContext(ctx, `INSERT INTO schema_migration_notices(kind, subject, detail, observed_at) VALUES (?, ?, ?, ?) ON CONFLICT(kind, subject) DO NOTHING`, kind, subject, detail, domain.NewStoredTime(at))
	return err
}

// registerTimestampBackfills records the columns a migration step owes, inside
// the schema transaction, so the record and the version bump commit together.
//
// Registration RESETS the progress of a named backfill rather than skipping an
// existing row. It runs only from a `version < N` branch, and the migration
// transaction is fenced, so exactly one process ever executes a given step: a
// replica that starts after the winner committed sees the new version and never
// reaches this code, and a process that crashed mid-rewrite also sees the new
// version on restart and resumes from its stored cursor instead of restarting
// the scan. The reset is what makes a step that owes a column ALREADY drained by
// an earlier step — messages.created_at is owed by both 78 and 81 — run again
// for its own, stricter, transform.
func registerTimestampBackfills(ctx context.Context, db queryExecutor, names []string) error {
	for _, name := range names {
		if _, ok := timestampBackfills[name]; !ok {
			return fmt.Errorf("unknown timestamp backfill %q", name)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_backfills(name, cursor, done) VALUES (?, '', 0) ON CONFLICT(name) DO UPDATE SET cursor = '', done = 0`, name); err != nil {
			return fmt.Errorf("register backfill %q: %w", name, err)
		}
	}
	return nil
}

func allTimestampBackfillNames() []string {
	names := make([]string, 0, len(storedTimestampColumns))
	for _, target := range storedTimestampColumns {
		names = append(names, target.table+"."+target.column)
	}
	return names
}

// PendingBackfills reports the data migrations that still owe work. An empty
// result means every column has been rewritten to the end.
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

// runPendingBackfills drains every registered rewrite. It runs OUTSIDE the
// migration fence, on every start of every binary, and is safe to run
// concurrently with another replica doing the same: every chunk's UPDATE is
// keyed by the value it replaces and is therefore idempotent, and the durable
// cursor only ever moves forward, so two replicas cooperate on one scan instead
// of duplicating it.
func (s *Store) runPendingBackfills(ctx context.Context) error {
	pending, err := s.PendingBackfills(ctx)
	if err != nil {
		return fmt.Errorf("read pending backfills: %w", err)
	}
	for _, name := range pending {
		task, ok := timestampBackfills[name]
		if !ok {
			// A name written by a newer binary. Leaving it pending is correct:
			// this binary does not know how to satisfy it and must not mark it
			// done.
			continue
		}
		if err := s.runTimestampBackfill(ctx, task); err != nil {
			return fmt.Errorf("backfill %s: %w", name, err)
		}
	}
	return nil
}

// backfillChunkObserver is a test seam, in the same spirit as Store.now: the
// properties worth pinning about this code — that the migration fence is already
// released while it runs, that a crash mid-scan resumes from the durable cursor,
// and that the work is batched rather than one statement per row — are only
// observable from inside the loop. It is nil in every non-test build.
var backfillChunkObserver func(name string, chunk int)

func (s *Store) runTimestampBackfill(ctx context.Context, task timestampBackfill) error {
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
			return s.finishBackfill(ctx, task.name)
		}
		if backfillChunkObserver != nil {
			backfillChunkObserver(task.name, chunk)
		}
		if err := s.applyBackfillChunk(ctx, task, values); err != nil {
			return err
		}
		if len(values) < backfillChunkSize {
			return s.finishBackfill(ctx, task.name)
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

func (s *Store) selectBackfillChunk(ctx context.Context, task timestampBackfill, cursor string) ([]string, error) {
	query := `SELECT DISTINCT ` + task.column + ` FROM ` + task.table +
		` WHERE ` + task.column + ` > ? AND ` + task.pending +
		` ORDER BY ` + task.column + ` LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, cursor, backfillChunkSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, backfillChunkSize)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// applyBackfillChunk rewrites one chunk and advances the durable cursor in the
// same transaction, so progress and work are never out of step.
func (s *Store) applyBackfillChunk(ctx context.Context, task timestampBackfill, values []string) error {
	type rewrite struct {
		from string
		to   domain.StoredTime
	}
	rewrites := make([]rewrite, 0, len(values))
	type reject struct {
		value  string
		reason string
	}
	rejects := make([]reject, 0)
	for _, value := range values {
		parsed, err := domain.ParseStoredTime(value)
		if err != nil {
			// An undecodable value must not brick the process. Aborting here left
			// the schema at its old version and failed identically on every
			// restart of every binary, with no skip, no repair mode and no way to
			// reach the database through the product — and the authors already
			// knew free-form text lives in timestamp columns in the field, which
			// is why schema_migrations.applied_at is exempt from this rewrite.
			// The value is left exactly as it is, which is no worse than before
			// the upgrade, and it is recorded so an operator can find it.
			rejects = append(rejects, reject{value: value, reason: err.Error()})
			continue
		}
		normalized := task.rewrite(parsed)
		if !normalized.Ordered() {
			return fmt.Errorf("rewrite of %q produced the non-ordered value %q", value, string(normalized))
		}
		if string(normalized) != value {
			rewrites = append(rewrites, rewrite{from: value, to: normalized})
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(rewrites) > 0 {
		// One statement per chunk: "SET col = CASE col WHEN ? THEN ? … END WHERE
		// col IN (…)". The previous shape issued one statement per distinct value,
		// which on dqlite is one leader round trip per message.
		var builder strings.Builder
		builder.WriteString(`UPDATE `)
		builder.WriteString(task.table)
		builder.WriteString(` SET `)
		builder.WriteString(task.column)
		builder.WriteString(` = CASE `)
		builder.WriteString(task.column)
		arguments := make([]any, 0, len(rewrites)*3)
		for _, value := range rewrites {
			builder.WriteString(` WHEN ? THEN ?`)
			arguments = append(arguments, value.from, string(value.to))
		}
		builder.WriteString(` END WHERE `)
		builder.WriteString(task.column)
		builder.WriteString(` IN (`)
		for index, value := range rewrites {
			if index > 0 {
				builder.WriteString(`, `)
			}
			builder.WriteString(`?`)
			arguments = append(arguments, value.from)
		}
		builder.WriteString(`)`)
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
	// The cursor only moves forward, so a replica that is behind cannot rewind a
	// replica that is ahead.
	if _, err := tx.ExecContext(ctx, `UPDATE schema_backfills SET cursor = ? WHERE name = ? AND done = 0 AND cursor < ?`, values[len(values)-1], task.name, values[len(values)-1]); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) finishBackfill(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE schema_backfills SET done = 1 WHERE name = ?`, name)
	return err
}
