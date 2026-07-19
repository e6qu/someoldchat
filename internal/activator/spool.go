package activator

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
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
	List(context.Context, int) ([]SpooledRequest, error)
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
	spool := &SQLiteSpool{db: db, cipher: sealed, limits: limits}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL; CREATE TABLE IF NOT EXISTS activator_request_spool (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, payload BLOB NOT NULL, body_bytes INTEGER NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_until TEXT NOT NULL DEFAULT '')`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize SQLite request spool: %w", err)
	}
	if err := spool.migrateLeases(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate SQLite request spool leases: %w", err)
	}
	return spool, nil
}

func (s *SQLiteSpool) migrateLeases(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(activator_request_spool)`)
	if err != nil {
		return err
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []string{"body_bytes", "lease_owner", "lease_until"} {
		if !columns[column] {
			definition := `TEXT NOT NULL DEFAULT ''`
			if column == "body_bytes" {
				definition = `INTEGER NOT NULL DEFAULT 0`
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE activator_request_spool ADD COLUMN `+column+` `+definition); err != nil {
				return err
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, payload FROM activator_request_spool WHERE body_bytes = 0`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uint64
		var sealed []byte
		if err := rows.Scan(&id, &sealed); err != nil {
			_ = rows.Close()
			return err
		}
		payload, err := s.open(sealed)
		if err != nil {
			_ = rows.Close()
			return err
		}
		var request SpooledRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			_ = rows.Close()
			return err
		}
		if int64(len(request.Body)) > s.limits.MaxBodyBytes {
			_ = rows.Close()
			return errors.New("existing spooled request exceeds body limit")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE activator_request_spool SET body_bytes = ? WHERE id = ?`, len(request.Body), id); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteSpool) Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]SpooledRequest, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return nil, errors.New("request spool is not initialized")
	}
	if strings.TrimSpace(owner) == "" || limit <= 0 || lease <= 0 {
		return nil, errors.New("spool claim requires an owner, positive limit, and positive lease")
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(lease)
	nowValue := now.Format(time.RFC3339Nano)
	leaseValue := leaseUntil.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE activator_request_spool SET lease_owner = ?, lease_until = ? WHERE id IN (SELECT id FROM activator_request_spool WHERE lease_until = '' OR lease_until <= ? ORDER BY id LIMIT ?)`, owner, leaseValue, nowValue, limit); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, payload FROM activator_request_spool WHERE lease_owner = ? AND lease_until = ? ORDER BY id`, owner, leaseValue)
	if err != nil {
		return nil, err
	}
	requests, err := s.decodeRows(rows)
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
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
	now := time.Now().UTC()
	args := make([]any, 0, len(ids)+3)
	args = append(args, now.Add(lease).Format(time.RFC3339Nano), owner, now.Format(time.RFC3339Nano))
	for index, id := range ids {
		if id == 0 {
			return errors.New("spool renewal requires positive request IDs")
		}
		placeholders[index] = "?"
		args = append(args, id)
	}
	query := `UPDATE activator_request_spool SET lease_until = ? WHERE lease_owner = ? AND lease_until > ? AND id IN (` + strings.Join(placeholders, ",") + `)`
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
	payload, err := json.Marshal(SpooledRequest{Method: request.Method, URL: request.URL.String(), Host: request.Host, Header: request.Header.Clone(), Body: body, CreatedAt: time.Now().UTC()})
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
	result, err := tx.ExecContext(ctx, `INSERT INTO activator_request_spool(created_at, payload, body_bytes) VALUES (?, ?, ?)`, time.Now().UTC().Format(time.RFC3339Nano), sealed, len(body))
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

func (s *SQLiteSpool) List(ctx context.Context, limit int) ([]SpooledRequest, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return nil, errors.New("request spool is not initialized")
	}
	if limit <= 0 {
		return nil, errors.New("spool list limit must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, payload FROM activator_request_spool ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result, err := s.decodeRows(rows)
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	return result, errors.Join(err, rows.Err())
}

func (s *SQLiteSpool) Delete(ctx context.Context, owner string, id uint64) error {
	if s == nil || s.db == nil {
		return errors.New("request spool is not initialized")
	}
	if strings.TrimSpace(owner) == "" {
		return errors.New("request spool delete requires an owner")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM activator_request_spool WHERE id = ? AND lease_owner = ? AND lease_until > ?`, id, owner, time.Now().UTC().Format(time.RFC3339Nano))
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

func (s *SQLiteSpool) decodeRows(rows *sql.Rows) ([]SpooledRequest, error) {
	result := make([]SpooledRequest, 0)
	for rows.Next() {
		var id uint64
		var sealed []byte
		if err := rows.Scan(&id, &sealed); err != nil {
			return nil, err
		}
		payload, err := s.open(sealed)
		if err != nil {
			return nil, err
		}
		var request SpooledRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, err
		}
		request.ID = id
		if request.Method == "" || request.URL == "" || request.Header == nil || int64(len(request.Body)) > s.limits.MaxBodyBytes {
			return nil, errors.New("spooled request metadata is invalid")
		}
		result = append(result, request)
	}
	return result, rows.Err()
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
