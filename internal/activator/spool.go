package activator

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Spool interface {
	Enqueue(context.Context, *http.Request, []byte) (uint64, error)
	// Pending reports how many deliverable requests are queued. The drain
	// worker uses it to decide whether waking the stack is warranted at all,
	// without decrypting any bodies.
	Pending(context.Context) (int, error)
	Claim(context.Context, string, int, time.Duration) ([]SpooledRequest, error)
	Renew(context.Context, string, []uint64, time.Duration) error
	Delete(context.Context, string, uint64) error
}

var ErrSpoolCapacity = errors.New("request spool capacity exceeded")
var ErrSpoolLeaseLost = errors.New("request spool lease lost")

type SpoolLimits struct {
	MaxBodyBytes      int64
	MaxQueuedBytes    int64
	MaxQueuedRequests int
}

type SpooledRequest struct {
	ID        uint64
	Method    string
	URL       string
	Host      string
	Header    http.Header
	Body      []byte
	CreatedAt time.Time
}

type SQLiteSpool struct {
	db     *sql.DB
	cipher cipher.AEAD
	limits SpoolLimits
	now    func() time.Time
}

func OpenSQLiteSpool(dsn string, key []byte, limits SpoolLimits) (*SQLiteSpool, error) {
	if strings.TrimSpace(dsn) == "" || len(key) != 32 || limits.MaxBodyBytes <= 0 || limits.MaxQueuedBytes <= 0 || limits.MaxQueuedRequests <= 0 {
		return nil, errors.New("SQLite request spool requires a DSN, 32-byte key, and positive queue limits")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	sealed, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite request spool: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	spool := &SQLiteSpool{db: db, cipher: sealed, limits: limits, now: time.Now}
	// lease_expires_unix_nano is an integer instant, not a formatted timestamp.
	// Comparing formatted RFC 3339 values through julianday() silently rounds to
	// roughly 40 microseconds, so lease decisions taken near the boundary could
	// go either way and refuse the delete of an already-delivered request.
	// lease_token gives each claim a distinct identity, so a claim cannot select
	// the rows of a previous claim that happens to share an expiry value.
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL; CREATE TABLE IF NOT EXISTS activator_request_spool (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, payload BLOB NOT NULL, body_bytes INTEGER NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_token TEXT NOT NULL DEFAULT '', lease_expires_unix_nano INTEGER NOT NULL DEFAULT 0, poison_reason TEXT NOT NULL DEFAULT '')`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize SQLite request spool: %w", err)
	}
	if err := spool.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate SQLite request spool: %w", err)
	}
	return spool, nil
}

func (s *SQLiteSpool) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	columns, err := spoolColumns(ctx, tx)
	if err != nil {
		return err
	}
	for column, definition := range map[string]string{
		"body_bytes":              `INTEGER NOT NULL DEFAULT 0`,
		"lease_owner":             `TEXT NOT NULL DEFAULT ''`,
		"lease_token":             `TEXT NOT NULL DEFAULT ''`,
		"lease_expires_unix_nano": `INTEGER NOT NULL DEFAULT 0`,
		"poison_reason":           `TEXT NOT NULL DEFAULT ''`,
	} {
		if !columns[column] {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE activator_request_spool ADD COLUMN `+column+` `+definition); err != nil {
				return err
			}
		}
	}
	// Leases written by an older binary as RFC 3339 text are dropped rather than
	// converted: an unexpired text lease cannot be compared against the integer
	// column, and releasing it only makes the entry claimable again.
	if columns["lease_until"] {
		if _, err := tx.ExecContext(ctx, `UPDATE activator_request_spool SET lease_owner = '', lease_token = '', lease_expires_unix_nano = 0 WHERE lease_until <> ''`); err != nil {
			return err
		}
	}
	if err := s.backfillBodyBytes(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func spoolColumns(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(activator_request_spool)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, rows.Close()
}

// backfillBodyBytes records the queued byte accounting for rows written before
// the column existed. A row that cannot be decoded is quarantined rather than
// aborting startup, so one bad row cannot keep the activator from booting.
func (s *SQLiteSpool) backfillBodyBytes(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, payload FROM activator_request_spool WHERE body_bytes = 0 AND poison_reason = ''`)
	if err != nil {
		return err
	}
	type pending struct {
		id     uint64
		bytes  int
		poison string
	}
	updates := make([]pending, 0)
	for rows.Next() {
		var id uint64
		var sealed []byte
		if err := rows.Scan(&id, &sealed); err != nil {
			_ = rows.Close()
			return err
		}
		request, decodeErr := s.decodePayload(sealed)
		if decodeErr != nil {
			updates = append(updates, pending{id: id, poison: decodeErr.Error()})
			continue
		}
		updates = append(updates, pending{id: id, bytes: len(request.Body)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		if update.poison != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE activator_request_spool SET poison_reason = ? WHERE id = ?`, update.poison, update.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE activator_request_spool SET body_bytes = ? WHERE id = ?`, update.bytes, update.id); err != nil {
			return err
		}
	}
	return nil
}

// Claim leases the oldest deliverable requests to one owner.
//
// A row that cannot be decrypted or decoded is quarantined and skipped instead
// of failing the batch. Aborting on the first undecodable row made every later
// claim fail forever — nothing ever removed the bad row — so a single row
// written under a rotated key took the whole durable queue down permanently.
func (s *SQLiteSpool) Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]SpooledRequest, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return nil, errors.New("request spool is not initialized")
	}
	if strings.TrimSpace(owner) == "" || limit <= 0 || lease <= 0 {
		return nil, errors.New("spool claim requires an owner, positive limit, and positive lease")
	}
	token, err := leaseToken()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().UnixNano()
	expires := s.now().UTC().Add(lease).UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE activator_request_spool SET lease_owner = ?, lease_token = ?, lease_expires_unix_nano = ? WHERE id IN (SELECT id FROM activator_request_spool WHERE poison_reason = '' AND lease_expires_unix_nano <= ? ORDER BY id LIMIT ?)`, owner, token, expires, now, limit); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, payload FROM activator_request_spool WHERE lease_token = ? ORDER BY id`, token)
	if err != nil {
		return nil, err
	}
	requests, poisoned, err := s.decodeRows(rows)
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	for id, reason := range poisoned {
		if _, err := tx.ExecContext(ctx, `UPDATE activator_request_spool SET poison_reason = ?, lease_owner = '', lease_token = '', lease_expires_unix_nano = 0 WHERE id = ?`, reason, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return requests, nil
}

func (s *SQLiteSpool) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteSpool) Renew(ctx context.Context, owner string, ids []uint64, lease time.Duration) error {
	if s == nil || s.db == nil {
		return errors.New("request spool is not initialized")
	}
	if strings.TrimSpace(owner) == "" || len(ids) == 0 || lease <= 0 {
		return errors.New("spool renewal requires an owner, request IDs, and a positive lease")
	}
	placeholders := make([]string, len(ids))
	now := s.now().UTC()
	args := make([]any, 0, len(ids)+3)
	args = append(args, now.Add(lease).UnixNano(), owner, now.UnixNano())
	for index, id := range ids {
		if id == 0 {
			return errors.New("spool renewal requires positive request IDs")
		}
		placeholders[index] = "?"
		args = append(args, id)
	}
	query := `UPDATE activator_request_spool SET lease_expires_unix_nano = ? WHERE lease_owner = ? AND lease_expires_unix_nano > ? AND id IN (` + strings.Join(placeholders, ",") + `)`
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return ErrSpoolLeaseLost
	}
	return nil
}

func (s *SQLiteSpool) Enqueue(ctx context.Context, request *http.Request, body []byte) (uint64, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return 0, errors.New("request spool is not initialized")
	}
	if request == nil || request.URL == nil {
		return 0, errors.New("request spool requires a request with a URL")
	}
	if int64(len(body)) > s.limits.MaxBodyBytes {
		return 0, errors.New("request body exceeds spool limit")
	}
	now := s.now().UTC()
	payload, err := json.Marshal(SpooledRequest{Method: request.Method, URL: request.URL.String(), Host: request.Host, Header: request.Header.Clone(), Body: body, CreatedAt: now})
	if err != nil {
		return 0, err
	}
	sealed, err := s.seal(payload)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var queuedRequests int
	var queuedBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(body_bytes), 0) FROM activator_request_spool`).Scan(&queuedRequests, &queuedBytes); err != nil {
		return 0, err
	}
	if queuedRequests >= s.limits.MaxQueuedRequests || queuedBytes+int64(len(body)) > s.limits.MaxQueuedBytes {
		return 0, ErrSpoolCapacity
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO activator_request_spool(created_at, payload, body_bytes) VALUES (?, ?, ?)`, now.Format(time.RFC3339Nano), sealed, len(body))
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if id <= 0 {
		return 0, errors.New("request spool did not allocate an ID")
	}
	return uint64(id), nil
}

// Pending counts deliverable requests. Quarantined rows are excluded: they are
// kept for operator inspection but can never be delivered, so counting them
// would make the drain worker wake the stack forever.
func (s *SQLiteSpool) Pending(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("request spool is not initialized")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activator_request_spool WHERE poison_reason = ''`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// Quarantined counts rows removed from delivery because they could not be
// decoded. It exists so a poisoned backlog is observable rather than silent.
func (s *SQLiteSpool) Quarantined(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("request spool is not initialized")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activator_request_spool WHERE poison_reason <> ''`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// List returns deliverable requests in queue order. It is an inspection helper
// for tests and tooling; the delivery path uses Claim.
func (s *SQLiteSpool) List(ctx context.Context, limit int) ([]SpooledRequest, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return nil, errors.New("request spool is not initialized")
	}
	if limit <= 0 {
		return nil, errors.New("spool list limit must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, payload FROM activator_request_spool WHERE poison_reason = '' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	result, poisoned, err := s.decodeRows(rows)
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if len(poisoned) != 0 {
		return result, fmt.Errorf("request spool holds %d undecodable rows", len(poisoned))
	}
	return result, nil
}

// Delete removes a delivered request. It is fenced on the owner but not on the
// lease expiry: a lease that expired while the mutation was being applied does
// not make the request undelivered, and refusing the delete there left the entry
// to be delivered a second time. Another owner taking the entry does change
// lease_owner, and that conflict is still reported.
func (s *SQLiteSpool) Delete(ctx context.Context, owner string, id uint64) error {
	if s == nil || s.db == nil {
		return errors.New("request spool is not initialized")
	}
	if strings.TrimSpace(owner) == "" {
		return errors.New("request spool delete requires an owner")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM activator_request_spool WHERE id = ? AND lease_owner = ?`, id, owner)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// decodeRows decodes every row it can and reports the rest by ID with the reason
// they cannot be delivered, so the caller can quarantine them and continue.
func (s *SQLiteSpool) decodeRows(rows *sql.Rows) ([]SpooledRequest, map[uint64]string, error) {
	result := make([]SpooledRequest, 0)
	poisoned := make(map[uint64]string)
	for rows.Next() {
		var id uint64
		var sealed []byte
		if err := rows.Scan(&id, &sealed); err != nil {
			return nil, nil, err
		}
		request, err := s.decodePayload(sealed)
		if err != nil {
			poisoned[id] = err.Error()
			continue
		}
		request.ID = id
		result = append(result, request)
	}
	return result, poisoned, rows.Err()
}

func (s *SQLiteSpool) decodePayload(sealed []byte) (SpooledRequest, error) {
	payload, err := s.open(sealed)
	if err != nil {
		return SpooledRequest{}, err
	}
	var request SpooledRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return SpooledRequest{}, err
	}
	if request.Method == "" || request.URL == "" || request.Header == nil || int64(len(request.Body)) > s.limits.MaxBodyBytes {
		return SpooledRequest{}, errors.New("spooled request metadata is invalid")
	}
	return request, nil
}

func (s *SQLiteSpool) seal(payload []byte) ([]byte, error) {
	nonce := make([]byte, s.cipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.cipher.Seal(nonce, nonce, payload, nil), nil
}

func (s *SQLiteSpool) open(sealed []byte) ([]byte, error) {
	nonceSize := s.cipher.NonceSize()
	if len(sealed) < nonceSize {
		return nil, errors.New("spooled request ciphertext is truncated")
	}
	return s.cipher.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
}

// leaseToken gives one claim a unique identity that does not depend on the
// clock, so two claims by the same owner can never select the same rows.
func leaseToken() (string, error) {
	var token [16]byte
	if _, err := io.ReadFull(rand.Reader, token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
