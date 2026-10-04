package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// rateLimitSchema holds each rate-limit key's theoretical arrival time, in
// Unix microseconds (see domain.RateAllowance). A row whose time has passed
// admits exactly what an absent row admits, so rows are collected once their
// time passes and the table holds only the keys that are currently limiting.
const rateLimitSchema = `CREATE TABLE IF NOT EXISTS rate_limits (
 key TEXT PRIMARY KEY, theoretical_arrival INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS rate_limits_arrival ON rate_limits(theoretical_arrival);
`

// rateSweepInterval bounds how often this process collects passed keys.
const rateSweepInterval = time.Minute

// TakeRateToken admits or refuses one call against the key's allowance.
//
// The admission is a conditional write rather than a read and a write, so
// replicas sharing the database share the budget exactly: the UPDATE advances
// the arrival time only WHERE the call is admitted, and PostgreSQL re-checks
// that predicate against the row a concurrent writer committed, as SQLite and
// dqlite serialise writers outright. A key seen for the first time is
// inserted already advanced; a concurrent first insert loses to the other and
// falls through to the conditional update.
func (s *Store) TakeRateToken(ctx context.Context, key string, allowance domain.RateAllowance, now time.Time) (time.Duration, bool, error) {
	if !domain.ValidRateLimitKey(key) {
		return 0, false, store.InvalidArgument("a rate limit needs a key of bounded text")
	}
	if !allowance.Valid() {
		return 0, false, store.InvalidArgument("a rate limit needs a positive interval and burst")
	}
	s.sweepRateLimits(ctx, now)
	for attempt := 0; ; attempt++ {
		wait, admitted, collected, err := s.takeRateTokenOnce(ctx, key, allowance, now)
		// A row collected between the statements is a row whose time had
		// passed, so the next attempt admits the call as a first one. It is
		// bounded only because a sweep cannot recur within one interval.
		if !collected || attempt == 2 {
			return wait, admitted, err
		}
	}
}

// takeRateTokenOnce is one transactional attempt. collected reports that the
// key's row vanished between the conditional update and the read of its
// arrival time.
func (s *Store) takeRateTokenOnce(ctx context.Context, key string, allowance domain.RateAllowance, now time.Time) (time.Duration, bool, bool, error) {
	at := now.UnixMicro()
	interval := allowance.Interval.Microseconds()
	if interval < 1 {
		interval = 1
	}
	tolerance := int64(allowance.Burst-1) * interval
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return 0, false, false, err
	}
	defer tx.Rollback()
	inserted, err := tx.ExecContext(ctx, `INSERT INTO rate_limits(key, theoretical_arrival) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, key, at+interval)
	if err != nil {
		return 0, false, false, classify(err)
	}
	if count, err := inserted.RowsAffected(); err != nil {
		return 0, false, false, err
	} else if count == 1 {
		return 0, true, false, tx.Commit()
	}
	advanced, err := tx.ExecContext(ctx, `UPDATE rate_limits
		SET theoretical_arrival = (CASE WHEN theoretical_arrival > ? THEN theoretical_arrival ELSE ? END) + ?
		WHERE key = ? AND (CASE WHEN theoretical_arrival > ? THEN theoretical_arrival ELSE ? END) <= ?`,
		at, at, interval, key, at, at, at+tolerance)
	if err != nil {
		return 0, false, false, classify(err)
	}
	if count, err := advanced.RowsAffected(); err != nil {
		return 0, false, false, err
	} else if count == 1 {
		return 0, true, false, tx.Commit()
	}
	var arrival int64
	if err := tx.QueryRowContext(ctx, `SELECT theoretical_arrival FROM rate_limits WHERE key = ?`, key).Scan(&arrival); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, true, nil
		}
		return 0, false, false, err
	}
	wait := time.Duration(arrival-at-tolerance) * time.Microsecond
	if wait < time.Microsecond {
		wait = time.Microsecond
	}
	return wait, false, false, tx.Commit()
}

// sweepRateLimits collects keys whose arrival time has passed, at most once
// per rateSweepInterval in this process. A failed sweep leaves rows that
// still admit correctly, so it is retried at the next interval rather than
// failing the call that happened to trigger it.
func (s *Store) sweepRateLimits(ctx context.Context, now time.Time) {
	s.rateSweep.Lock()
	due := now.Sub(s.rateSweep.last) >= rateSweepInterval
	if due {
		s.rateSweep.last = now
	}
	s.rateSweep.Unlock()
	if !due {
		return
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rate_limits WHERE theoretical_arrival < ?`, now.UnixMicro()); err != nil {
		return
	}
	_ = tx.Commit()
}
