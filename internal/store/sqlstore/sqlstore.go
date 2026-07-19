package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/store"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, domain TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', discoverability TEXT NOT NULL DEFAULT 'open', icon_url TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS users (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 email TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, real_name TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
 status_text TEXT NOT NULL DEFAULT '', status_emoji TEXT NOT NULL DEFAULT '',
 image_24 TEXT NOT NULL DEFAULT '', image_32 TEXT NOT NULL DEFAULT '', image_48 TEXT NOT NULL DEFAULT '',
 image_72 TEXT NOT NULL DEFAULT '', image_192 TEXT NOT NULL DEFAULT '', image_512 TEXT NOT NULL DEFAULT '', image_1024 TEXT NOT NULL DEFAULT '',
 deleted INTEGER NOT NULL DEFAULT 0, presence TEXT NOT NULL DEFAULT 'auto'
);
CREATE TABLE IF NOT EXISTS user_expirations (user_id TEXT PRIMARY KEY REFERENCES users(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), expiration_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS workspace_members (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 role TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE IF NOT EXISTS tokens (
 token_hash TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
 session_hash TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL DEFAULT '', expires_at TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0,
	oidc_provider TEXT NOT NULL DEFAULT '', oidc_id_token TEXT NOT NULL DEFAULT '', oidc_subject TEXT NOT NULL DEFAULT '', oidc_sid TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS auth_methods (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, enabled INTEGER NOT NULL, PRIMARY KEY(workspace_id, provider));
CREATE TABLE IF NOT EXISTS external_identities (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, subject TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY(workspace_id, provider, subject));
CREATE TABLE IF NOT EXISTS conversations (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 name TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '', purpose TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0, is_private INTEGER NOT NULL DEFAULT 0, is_direct INTEGER NOT NULL DEFAULT 0, is_group_direct INTEGER NOT NULL DEFAULT 0, direct_key TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS workspace_default_channels (workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (workspace_id, conversation_id));
CREATE TABLE IF NOT EXISTS conversation_teams (conversation_id TEXT NOT NULL REFERENCES conversations(id), team_id TEXT NOT NULL REFERENCES workspaces(id), org_channel INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (conversation_id, team_id));
CREATE TABLE IF NOT EXISTS oauth_clients (id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL, app_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS oauth_codes (code TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES oauth_clients(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, redirect_uri TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS rtm_connections (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS app_tokens (token_hash TEXT PRIMARY KEY, app_id TEXT NOT NULL, scopes TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS socket_mode_connections (id TEXT PRIMARY KEY, app_id TEXT NOT NULL, expires_at INTEGER NOT NULL, consumed_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS socket_mode_cursors (app_id TEXT PRIMARY KEY, sequence INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS socket_mode_responses (app_id TEXT NOT NULL, envelope_id TEXT NOT NULL, payload TEXT NOT NULL, received_at INTEGER NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at INTEGER NOT NULL DEFAULT 0, acknowledged_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (app_id, envelope_id));
CREATE TABLE IF NOT EXISTS conversation_prefs (
 conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),
 can_thread_types TEXT NOT NULL DEFAULT '[]', can_thread_users TEXT NOT NULL DEFAULT '[]',
 who_can_post_types TEXT NOT NULL DEFAULT '[]', who_can_post_users TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS invite_requests (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), email TEXT NOT NULL, requested_by TEXT NOT NULL REFERENCES users(id), channel_ids TEXT NOT NULL DEFAULT '[]', custom_message TEXT NOT NULL DEFAULT '', real_name TEXT NOT NULL DEFAULT '', resend INTEGER NOT NULL DEFAULT 0, restricted INTEGER NOT NULL DEFAULT 0, ultra_restricted INTEGER NOT NULL DEFAULT 0, guest_expiration_at INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, created_at INTEGER NOT NULL, reviewed_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS app_approvals (app_id TEXT PRIMARY KEY, request_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), status TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS app_installations (app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, PRIMARY KEY (app_id, workspace_id));
CREATE TABLE IF NOT EXISTS app_permission_requests (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), requester_id TEXT NOT NULL REFERENCES users(id), target_user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, trigger_id TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS views (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), type TEXT NOT NULL, external_id TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL, hash TEXT NOT NULL, root_view_id TEXT NOT NULL, previous_view_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS views_workspace_external ON views(workspace_id, external_id) WHERE external_id <> '';
CREATE INDEX IF NOT EXISTS views_published_user ON views(workspace_id, user_id, type, updated_at);
CREATE TABLE IF NOT EXISTS workflow_steps (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), edit_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, inputs TEXT NOT NULL DEFAULT '{}', outputs TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '', step_name TEXT NOT NULL DEFAULT '', image_url TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS dialogs (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), payload TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS bots (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), app_id TEXT NOT NULL DEFAULT '', user_id TEXT NOT NULL REFERENCES users(id), name TEXT NOT NULL, image_36 TEXT NOT NULL DEFAULT '', image_48 TEXT NOT NULL DEFAULT '', image_72 TEXT NOT NULL DEFAULT '', deleted INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_migrations (workspace_id TEXT NOT NULL REFERENCES workspaces(id), old_id TEXT NOT NULL, global_id TEXT NOT NULL, PRIMARY KEY (workspace_id, old_id), UNIQUE (workspace_id, global_id));
CREATE INDEX IF NOT EXISTS app_approvals_workspace_status ON app_approvals(workspace_id, status, app_id);
CREATE TABLE IF NOT EXISTS conversation_members (
 conversation_id TEXT NOT NULL REFERENCES conversations(id),
 user_id TEXT NOT NULL REFERENCES users(id),
 PRIMARY KEY (conversation_id, user_id)
);
CREATE TABLE IF NOT EXISTS messages (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	conversation TEXT NOT NULL REFERENCES conversations(id), author_id TEXT NOT NULL REFERENCES users(id),
	text TEXT NOT NULL, thread_timestamp TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, unfurls TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS messages_conversation_created ON messages(conversation, created_at, id);
CREATE TABLE IF NOT EXISTS reactions (
 message_id TEXT NOT NULL REFERENCES messages(id), name TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL,
 PRIMARY KEY (message_id, name, user_id)
);
CREATE TABLE IF NOT EXISTS pins (
 message_id TEXT NOT NULL REFERENCES messages(id), user_id TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL,
 PRIMARY KEY (message_id, user_id)
);
CREATE TABLE IF NOT EXISTS files (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), uploader_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, title TEXT NOT NULL, mime_type TEXT NOT NULL, blob_key TEXT NOT NULL UNIQUE,
 size INTEGER NOT NULL, created_at TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, public_token TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS file_comments (
 id TEXT PRIMARY KEY, file_id TEXT NOT NULL REFERENCES files(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, created_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS remote_files (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), external_id TEXT NOT NULL,
 title TEXT NOT NULL, file_type TEXT NOT NULL DEFAULT '', external_url TEXT NOT NULL,
 preview_image TEXT NOT NULL DEFAULT '', indexable_contents TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS remote_files_workspace_external ON remote_files(workspace_id, external_id);
CREATE TABLE IF NOT EXISTS remote_file_shares (
 remote_file_id TEXT NOT NULL REFERENCES remote_files(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 PRIMARY KEY (remote_file_id, conversation_id)
);
CREATE INDEX IF NOT EXISTS files_workspace_id ON files(workspace_id, id);
CREATE TABLE IF NOT EXISTS outbox (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, workspace_id TEXT NOT NULL, topic TEXT NOT NULL,
 actor_id TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL, created_at TEXT NOT NULL, delivered INTEGER NOT NULL DEFAULT 0,
 lease_owner TEXT NOT NULL DEFAULT '', lease_until TEXT NOT NULL DEFAULT '', next_attempt_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS access_logs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 username TEXT NOT NULL, created_at INTEGER NOT NULL, ip TEXT NOT NULL, user_agent TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS access_logs_workspace_created ON access_logs(workspace_id, created_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS lifecycle_state (
 id INTEGER PRIMARY KEY CHECK(id = 1), state TEXT NOT NULL, generation INTEGER NOT NULL,
 wake_deadline TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS idempotency (
 workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 message_id TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY (workspace_id, user_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS read_cursors (
 workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, conversation_id TEXT NOT NULL,
 last_read TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY (workspace_id, user_id, conversation_id)
);
CREATE TABLE IF NOT EXISTS do_not_disturb (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 enabled INTEGER NOT NULL DEFAULT 0, snooze_until INTEGER NOT NULL DEFAULT 0,
 next_start_at INTEGER NOT NULL DEFAULT 0, next_end_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE IF NOT EXISTS stars (
 user_id TEXT NOT NULL REFERENCES users(id), message_id TEXT NOT NULL REFERENCES messages(id), created_at TEXT NOT NULL,
 PRIMARY KEY (user_id, message_id)
);
CREATE INDEX IF NOT EXISTS stars_user_created ON stars(user_id, created_at, message_id);
CREATE TABLE IF NOT EXISTS reminders (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), creator_id TEXT NOT NULL REFERENCES users(id),
 user_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, due_at INTEGER NOT NULL, complete_at INTEGER NOT NULL DEFAULT 0,
 recurring INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS reminders_user_due ON reminders(workspace_id, user_id, due_at, id);
CREATE TABLE IF NOT EXISTS scheduled_messages (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), channel_id TEXT NOT NULL REFERENCES conversations(id),
 author_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, post_at INTEGER NOT NULL, created_at INTEGER NOT NULL,
 delivered INTEGER NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS scheduled_messages_owner ON scheduled_messages(workspace_id, author_id, id);
CREATE TABLE IF NOT EXISTS user_groups (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), name TEXT NOT NULL, handle TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '', creator_id TEXT NOT NULL REFERENCES users(id), updated_by TEXT NOT NULL REFERENCES users(id),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS conversation_access_groups (conversation_id TEXT NOT NULL REFERENCES conversations(id), group_id TEXT NOT NULL REFERENCES user_groups(id), PRIMARY KEY (conversation_id, group_id));
CREATE UNIQUE INDEX IF NOT EXISTS user_groups_workspace_handle ON user_groups(workspace_id, handle);
CREATE TABLE IF NOT EXISTS user_group_users (
 group_id TEXT NOT NULL REFERENCES user_groups(id), user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY (group_id, user_id)
);
CREATE TABLE IF NOT EXISTS user_group_channels (
 group_id TEXT NOT NULL REFERENCES user_groups(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (group_id, conversation_id)
);
CREATE TABLE IF NOT EXISTS calls (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), external_unique_id TEXT NOT NULL,
 external_display_id TEXT NOT NULL DEFAULT '', join_url TEXT NOT NULL, desktop_app_join_url TEXT NOT NULL DEFAULT '',
 title TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL REFERENCES users(id), started_at INTEGER NOT NULL,
 ended_at INTEGER NOT NULL DEFAULT 0, duration_seconds INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS calls_workspace_external ON calls(workspace_id, external_unique_id);
CREATE TABLE IF NOT EXISTS call_participants (
 call_id TEXT NOT NULL REFERENCES calls(id), user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY (call_id, user_id)
);
CREATE TABLE IF NOT EXISTS custom_emoji (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), name TEXT NOT NULL, url TEXT NOT NULL DEFAULT '', alias_for TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (workspace_id, name)
);
`

const schemaVersion = 64

const legacySessionScopes = "chat:write channels:history users:read users:read.email users:write channels:read channels:manage reactions:write reactions:read pins:write pins:read search:read files:write files:read team:read"

type queryExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Store struct{ db *sql.DB }

var _ store.Store = (*Store)(nil)

func (s *Store) AppendEvent(ctx context.Context, event events.Event) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) RecordAccess(ctx context.Context, value domain.AccessLog) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_logs(workspace_id, user_id, username, created_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?)`, value.WorkspaceID, value.UserID, value.Username, value.CreatedAt.UTC().Unix(), value.IP, value.UserAgent)
	return err
}
func (s *Store) ListAccessLogs(ctx context.Context, workspace domain.WorkspaceID, before time.Time, limit, page int) ([]domain.AccessLog, bool, error) {
	if limit <= 0 || limit > 1000 || page <= 0 {
		return nil, false, errors.New("access log page parameters are invalid")
	}
	query := `SELECT workspace_id, user_id, username, created_at, ip, user_agent FROM access_logs WHERE workspace_id = ?`
	args := []any{workspace}
	if !before.IsZero() {
		query += ` AND created_at <= ?`
		args = append(args, before.UTC().Unix())
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit+1, (page-1)*limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	values := make([]domain.AccessLog, 0, limit+1)
	for rows.Next() {
		var value domain.AccessLog
		var created int64
		if err := rows.Scan(&value.WorkspaceID, &value.UserID, &value.Username, &created, &value.IP, &value.UserAgent); err != nil {
			return nil, false, err
		}
		value.CreatedAt = time.Unix(created, 0).UTC()
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	return values, hasMore, nil
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s, err := FromDB(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func FromDB(ctx context.Context, db *sql.DB) (*Store, error) {
	return fromDB(ctx, db, true)
}

// FromDqliteDB initializes the repository against a dqlite-managed database.
// dqlite owns connection configuration and rejects SQLite-only PRAGMA statements.
func FromDqliteDB(ctx context.Context, db *sql.DB) (*Store, error) {
	return fromDB(ctx, db, false)
}

// FromPostgresDB initializes the repository against a PostgreSQL database
// opened by the PostgreSQL adapter. The adapter owns PostgreSQL-specific
// connection settings and SQL translation.
func FromPostgresDB(ctx context.Context, db *sql.DB) (*Store, error) {
	return fromDB(ctx, db, false)
}

func fromDB(ctx context.Context, db *sql.DB, configureSQLite bool) (*Store, error) {
	if db == nil {
		return nil, errors.New("SQLite store requires a database handle")
	}
	s := &Store{db: db}
	if configureSQLite {
		if err := s.configure(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.Migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) configure(ctx context.Context) error {
	for _, statement := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL"} {
		deadline := time.Now().Add(5 * time.Second)
		backoff := 5 * time.Millisecond
		for {
			if _, err := s.db.ExecContext(ctx, statement); err == nil {
				break
			} else if !sqliteBusy(err) || time.Now().After(deadline) {
				return fmt.Errorf("configure sqlite (%s): %w", statement, err)
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("configure sqlite (%s): %w", statement, ctx.Err())
			case <-timer.C:
			}
			if backoff < 100*time.Millisecond {
				backoff *= 2
			}
		}
	}
	return nil
}

func sqliteBusy(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy")
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("sqlite integrity check failed: %s", result)
	}
	return nil
}

func (s *Store) SeedWorkspace(ctx context.Context, value domain.Workspace) error {
	discoverability := value.Discoverability
	if discoverability == "" {
		discoverability = domain.WorkspaceDiscoverabilityOpen
	}
	if !discoverability.Valid() {
		return errors.New("invalid workspace discoverability")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces(id, domain, name, description, discoverability, icon_url) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET domain = excluded.domain, name = excluded.name, description = excluded.description, discoverability = excluded.discoverability, icon_url = excluded.icon_url`, value.ID, value.Domain, value.Name, value.Description, discoverability, value.IconURL)
	return err
}

func (s *Store) SeedUser(ctx context.Context, value domain.User) error {
	deleted := 0
	if value.Deleted {
		deleted = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	presence := value.Presence
	if presence == "" {
		presence = domain.PresenceAuto
	}
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		return errors.New("invalid user presence")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET workspace_id = excluded.workspace_id, email = excluded.email, name = excluded.name, real_name = excluded.real_name, display_name = excluded.display_name, status_text = excluded.status_text, status_emoji = excluded.status_emoji, image_24 = excluded.image_24, image_32 = excluded.image_32, image_48 = excluded.image_48, image_72 = excluded.image_72, image_192 = excluded.image_192, image_512 = excluded.image_512, image_1024 = excluded.image_1024, deleted = excluded.deleted, presence = excluded.presence`, value.ID, value.WorkspaceID, strings.ToLower(strings.TrimSpace(value.Email)), value.Name, value.RealName, value.Profile.DisplayName, value.Profile.StatusText, value.Profile.StatusEmoji, value.Profile.Image24, value.Profile.Image32, value.Profile.Image48, value.Profile.Image72, value.Profile.Image192, value.Profile.Image512, value.Profile.Image1024, deleted, presence); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_members(workspace_id, user_id, role, active) VALUES (?, ?, 'member', 1) ON CONFLICT(workspace_id, user_id) DO NOTHING`, value.WorkspaceID, value.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SeedToken(ctx context.Context, token string, record domain.TokenRecord) error {
	privateScopes := strings.Join(domain.NormalizeScopes(record.Scopes), " ")
	revoked := 0
	if record.Revoked {
		revoked = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens(token_hash, workspace_id, user_id, scopes, revoked) VALUES (?, ?, ?, ?, ?) ON CONFLICT(token_hash) DO NOTHING`, domain.HashToken(token), record.WorkspaceID, record.UserID, privateScopes, revoked)
	return err
}

func (s *Store) SeedConversation(ctx context.Context, value domain.Conversation) error {
	private := 0
	if value.IsPrivate {
		private = 1
	}
	direct := 0
	if value.IsDirect {
		direct = 1
	}
	groupDirect := 0
	if value.IsGroupDirect {
		groupDirect = 1
	}
	archived := 0
	if value.Archived {
		archived = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET workspace_id = excluded.workspace_id, name = excluded.name, topic = excluded.topic, purpose = excluded.purpose, archived = excluded.archived, is_private = excluded.is_private, is_direct = excluded.is_direct, is_group_direct = excluded.is_group_direct`, value.ID, value.WorkspaceID, value.Name, value.Topic, value.Purpose, archived, private, direct, groupDirect)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO conversation_teams(conversation_id, team_id, org_channel) VALUES (?, ?, 0) ON CONFLICT(conversation_id, team_id) DO NOTHING`, value.ID, value.WorkspaceID)
	return err
}

func (s *Store) SeedConversationMember(ctx context.Context, conversation domain.ConversationID, user domain.UserID) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) VALUES (?, ?) ON CONFLICT(conversation_id, user_id) DO NOTHING`, conversation, user)
	return err
}

func (s *Store) Migrate(ctx context.Context) error {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite migration connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("acquire sqlite migration lock: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := s.migrateOn(ctx, connection); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit sqlite migration: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) migrateOn(ctx context.Context, db queryExecutor) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read sqlite schema version: %w", err)
	}
	if version < 2 {
		columns, err := s.outboxColumns(ctx, db)
		if err != nil {
			return err
		}
		for _, column := range []string{"lease_owner", "lease_until"} {
			if !columns[column] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE outbox ADD COLUMN `+column+` TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migrate outbox %s: %w", column, err)
				}
			}
		}
	}
	if version < 3 {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO lifecycle_state(id, state, generation) VALUES (1, 'hibernated', 0)`); err != nil {
			return fmt.Errorf("initialize lifecycle state: %w", err)
		}
	}
	if version < 4 {
		columns, err := s.messageColumns(ctx, db)
		if err != nil {
			return err
		}
		if !columns["thread_timestamp"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN thread_timestamp TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate messages thread timestamp: %w", err)
			}
			if columns["thread_id"] {
				if _, err := db.ExecContext(ctx, `UPDATE messages SET thread_timestamp = thread_id WHERE thread_timestamp = ''`); err != nil {
					return fmt.Errorf("copy message thread timestamps: %w", err)
				}
			}
		}
	}
	if version < 6 {
		columns, err := s.outboxColumns(ctx, db)
		if err != nil {
			return err
		}
		if !columns["next_attempt_at"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE outbox ADD COLUMN next_attempt_at TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate outbox retry schedule: %w", err)
			}
		}
	}
	if version < 11 {
		columns, err := s.sessionColumns(ctx, db)
		if err != nil {
			return err
		}
		if !columns["scopes"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN scopes TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate session scopes: %w", err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE sessions SET scopes = ? WHERE scopes = ''`, legacySessionScopes); err != nil {
				return fmt.Errorf("normalize legacy session scopes: %w", err)
			}
		}
	}
	if version < 12 {
		columns, err := s.tableColumns(ctx, db, "users")
		if err != nil {
			return err
		}
		for _, column := range []string{"display_name", "status_text", "status_emoji", "image_24", "image_32", "image_48", "image_72", "image_192", "image_512", "image_1024"} {
			if !columns[column] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN `+column+` TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migrate user profile %s: %w", column, err)
				}
			}
		}
	}
	if version < 13 {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO workspace_members(workspace_id, user_id, role, active) SELECT workspace_id, id, 'member', 1 FROM users`); err != nil {
			return fmt.Errorf("backfill workspace memberships: %w", err)
		}
	}
	if version < 14 {
		columns, err := s.tableColumns(ctx, db, "conversations")
		if err != nil {
			return err
		}
		for _, column := range []string{"is_direct", "is_group_direct"} {
			if !columns[column] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE conversations ADD COLUMN `+column+` INTEGER NOT NULL DEFAULT 0`); err != nil {
					return fmt.Errorf("migrate direct conversation flag %s: %w", column, err)
				}
			}
		}
	}
	if version < 15 {
		columns, err := s.tableColumns(ctx, db, "conversations")
		if err != nil {
			return err
		}
		if !columns["direct_key"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE conversations ADD COLUMN direct_key TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate direct conversation key: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS conversations_direct_key ON conversations(direct_key) WHERE direct_key <> ''`); err != nil {
			return fmt.Errorf("index direct conversation key: %w", err)
		}
	}
	if version < 16 {
		columns, err := s.tableColumns(ctx, db, "users")
		if err != nil {
			return err
		}
		if !columns["email"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate user email: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS users_workspace_email ON users(workspace_id, email)`); err != nil {
			return fmt.Errorf("index user email: %w", err)
		}
	}
	if version < 17 {
		columns, err := s.tableColumns(ctx, db, "conversations")
		if err != nil {
			return err
		}
		if !columns["topic"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE conversations ADD COLUMN topic TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate conversation topic: %w", err)
			}
		}
	}
	if version < 18 {
		columns, err := s.tableColumns(ctx, db, "conversations")
		if err != nil {
			return err
		}
		if !columns["purpose"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE conversations ADD COLUMN purpose TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate conversation purpose: %w", err)
			}
		}
	}
	if version < 19 {
		columns, err := s.tableColumns(ctx, db, "conversations")
		if err != nil {
			return err
		}
		if !columns["archived"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE conversations ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("migrate conversation archive state: %w", err)
			}
		}
	}
	if version < 20 {
		columns, err := s.tableColumns(ctx, db, "users")
		if err != nil {
			return err
		}
		if !columns["presence"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN presence TEXT NOT NULL DEFAULT 'auto'`); err != nil {
				return fmt.Errorf("migrate user presence: %w", err)
			}
		}
	}
	if version < 21 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS do_not_disturb (workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), enabled INTEGER NOT NULL DEFAULT 0, snooze_until INTEGER NOT NULL DEFAULT 0, next_start_at INTEGER NOT NULL DEFAULT 0, next_end_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (workspace_id, user_id))`); err != nil {
			return fmt.Errorf("migrate do not disturb state: %w", err)
		}
	}
	if version < 22 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS stars (user_id TEXT NOT NULL REFERENCES users(id), message_id TEXT NOT NULL REFERENCES messages(id), created_at TEXT NOT NULL, PRIMARY KEY (user_id, message_id))`); err != nil {
			return fmt.Errorf("migrate stars: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS stars_user_created ON stars(user_id, created_at, message_id)`); err != nil {
			return fmt.Errorf("index stars: %w", err)
		}
	}
	if version < 23 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS reminders (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), creator_id TEXT NOT NULL REFERENCES users(id), user_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, due_at INTEGER NOT NULL, complete_at INTEGER NOT NULL DEFAULT 0, recurring INTEGER NOT NULL DEFAULT 0)`); err != nil {
			return fmt.Errorf("migrate reminders: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS reminders_user_due ON reminders(workspace_id, user_id, due_at, id)`); err != nil {
			return fmt.Errorf("index reminders: %w", err)
		}
	}
	if version < 24 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS scheduled_messages (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), channel_id TEXT NOT NULL REFERENCES conversations(id), author_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, post_at INTEGER NOT NULL, created_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate scheduled messages: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS scheduled_messages_owner ON scheduled_messages(workspace_id, author_id, id)`); err != nil {
			return fmt.Errorf("index scheduled messages: %w", err)
		}
	}
	if version < 25 {
		columns, err := s.tableColumns(ctx, db, "scheduled_messages")
		if err != nil {
			return err
		}
		for _, column := range []string{"delivered", "lease_owner", "lease_until", "next_attempt_at"} {
			if columns[column] {
				continue
			}
			definition := `INTEGER NOT NULL DEFAULT 0`
			if column == "lease_owner" {
				definition = `TEXT NOT NULL DEFAULT ''`
			}
			if _, err := db.ExecContext(ctx, `ALTER TABLE scheduled_messages ADD COLUMN `+column+` `+definition); err != nil {
				return fmt.Errorf("migrate scheduled message %s: %w", column, err)
			}
		}
	}
	if version < 26 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS user_groups (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), name TEXT NOT NULL, handle TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', creator_id TEXT NOT NULL REFERENCES users(id), updated_by TEXT NOT NULL REFERENCES users(id), created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1)`); err != nil {
			return fmt.Errorf("migrate user groups: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS user_groups_workspace_handle ON user_groups(workspace_id, handle)`); err != nil {
			return fmt.Errorf("index user group handles: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS user_group_users (group_id TEXT NOT NULL REFERENCES user_groups(id), user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY (group_id, user_id))`); err != nil {
			return fmt.Errorf("migrate user group users: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS user_group_channels (group_id TEXT NOT NULL REFERENCES user_groups(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (group_id, conversation_id))`); err != nil {
			return fmt.Errorf("migrate user group channels: %w", err)
		}
	}
	if version < 27 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS calls (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), external_unique_id TEXT NOT NULL, external_display_id TEXT NOT NULL DEFAULT '', join_url TEXT NOT NULL, desktop_app_join_url TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL REFERENCES users(id), started_at INTEGER NOT NULL, ended_at INTEGER NOT NULL DEFAULT 0, duration_seconds INTEGER NOT NULL DEFAULT 0)`); err != nil {
			return fmt.Errorf("migrate calls: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS calls_workspace_external ON calls(workspace_id, external_unique_id)`); err != nil {
			return fmt.Errorf("index calls: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS call_participants (call_id TEXT NOT NULL REFERENCES calls(id), user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY (call_id, user_id))`); err != nil {
			return fmt.Errorf("migrate call participants: %w", err)
		}
	}
	if version < 28 {
		columns, err := s.tableColumns(ctx, db, "files")
		if err != nil {
			return err
		}
		if !columns["public_token"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE files ADD COLUMN public_token TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate file public token: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS files_public_token ON files(public_token) WHERE public_token <> ''`); err != nil {
			return fmt.Errorf("index file public tokens: %w", err)
		}
	}
	if version < 29 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS access_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), username TEXT NOT NULL, created_at INTEGER NOT NULL, ip TEXT NOT NULL, user_agent TEXT NOT NULL)`); err != nil {
			return fmt.Errorf("migrate access logs: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS access_logs_workspace_created ON access_logs(workspace_id, created_at DESC, id DESC)`); err != nil {
			return fmt.Errorf("index access logs: %w", err)
		}
	}
	if version < 30 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS custom_emoji (workspace_id TEXT NOT NULL REFERENCES workspaces(id), name TEXT NOT NULL, url TEXT NOT NULL DEFAULT '', alias_for TEXT NOT NULL DEFAULT '', PRIMARY KEY (workspace_id, name))`); err != nil {
			return fmt.Errorf("migrate custom emoji: %w", err)
		}
	}
	if version < 31 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS user_group_channels (group_id TEXT NOT NULL REFERENCES user_groups(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (group_id, conversation_id))`); err != nil {
			return fmt.Errorf("migrate user group channels: %w", err)
		}
	}
	if version < 32 {
		columns, err := s.tableColumns(ctx, db, "workspaces")
		if err != nil {
			return err
		}
		if !columns["description"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN description TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate workspace description: %w", err)
			}
		}
	}
	if version < 33 {
		columns, err := s.tableColumns(ctx, db, "workspaces")
		if err != nil {
			return err
		}
		if !columns["discoverability"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN discoverability TEXT NOT NULL DEFAULT 'open'`); err != nil {
				return fmt.Errorf("migrate workspace discoverability: %w", err)
			}
		}
	}
	if version < 34 {
		columns, err := s.tableColumns(ctx, db, "workspaces")
		if err != nil {
			return err
		}
		if !columns["icon_url"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN icon_url TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate workspace icon: %w", err)
			}
		}
	}
	if version < 35 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workspace_default_channels (workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (workspace_id, conversation_id))`); err != nil {
			return fmt.Errorf("migrate workspace default channels: %w", err)
		}
	}
	if version < 36 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS conversation_prefs (conversation_id TEXT PRIMARY KEY REFERENCES conversations(id), can_thread_types TEXT NOT NULL DEFAULT '[]', can_thread_users TEXT NOT NULL DEFAULT '[]', who_can_post_types TEXT NOT NULL DEFAULT '[]', who_can_post_users TEXT NOT NULL DEFAULT '[]')`); err != nil {
			return fmt.Errorf("migrate conversation preferences: %w", err)
		}
	}
	if version < 37 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS remote_files (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), external_id TEXT NOT NULL, title TEXT NOT NULL, file_type TEXT NOT NULL DEFAULT '', external_url TEXT NOT NULL, preview_image TEXT NOT NULL DEFAULT '', indexable_contents TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0)`); err != nil {
			return fmt.Errorf("migrate remote files: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS remote_files_workspace_external ON remote_files(workspace_id, external_id)`); err != nil {
			return fmt.Errorf("index remote files: %w", err)
		}
	}
	if version < 38 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS remote_file_shares (remote_file_id TEXT NOT NULL REFERENCES remote_files(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (remote_file_id, conversation_id))`); err != nil {
			return fmt.Errorf("migrate remote file shares: %w", err)
		}
	}
	if version < 39 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS user_expirations (user_id TEXT PRIMARY KEY REFERENCES users(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), expiration_ts INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate user expirations: %w", err)
		}
	}
	if version < 40 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS conversation_access_groups (conversation_id TEXT NOT NULL REFERENCES conversations(id), group_id TEXT NOT NULL REFERENCES user_groups(id), PRIMARY KEY (conversation_id, group_id))`); err != nil {
			return fmt.Errorf("migrate conversation access groups: %w", err)
		}
	}
	if version < 41 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS invite_requests (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), email TEXT NOT NULL, requested_by TEXT NOT NULL REFERENCES users(id), status TEXT NOT NULL, created_at INTEGER NOT NULL, reviewed_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
			return fmt.Errorf("migrate invite requests: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS invite_requests_workspace_status ON invite_requests(workspace_id, status, id)`); err != nil {
			return fmt.Errorf("index invite requests: %w", err)
		}
	}
	if version < 42 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_approvals (app_id TEXT PRIMARY KEY, request_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), status TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate app approvals: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS app_approvals_workspace_status ON app_approvals(workspace_id, status, app_id)`); err != nil {
			return fmt.Errorf("index app approvals: %w", err)
		}
	}
	if version < 43 {
		columns, err := s.tableColumns(ctx, db, "invite_requests")
		if err != nil {
			return err
		}
		for _, column := range []string{
			"channel_ids TEXT NOT NULL DEFAULT '[]'",
			"custom_message TEXT NOT NULL DEFAULT ''",
			"real_name TEXT NOT NULL DEFAULT ''",
			"resend INTEGER NOT NULL DEFAULT 0",
			"restricted INTEGER NOT NULL DEFAULT 0",
			"ultra_restricted INTEGER NOT NULL DEFAULT 0",
			"guest_expiration_at INTEGER NOT NULL DEFAULT 0",
		} {
			name := strings.Fields(column)[0]
			if !columns[name] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE invite_requests ADD COLUMN `+column); err != nil {
					return fmt.Errorf("migrate invite request %s: %w", name, err)
				}
			}
		}
	}
	if version < 44 {
		columns, err := s.tableColumns(ctx, db, "messages")
		if err != nil {
			return err
		}
		if !columns["unfurls"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN unfurls TEXT NOT NULL DEFAULT '{}'`); err != nil {
				return fmt.Errorf("migrate message unfurls: %w", err)
			}
		}
	}
	if version < 45 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS file_comments (id TEXT PRIMARY KEY, file_id TEXT NOT NULL REFERENCES files(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, created_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0)`); err != nil {
			return fmt.Errorf("migrate file comments: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS file_comments_file ON file_comments(file_id, id)`); err != nil {
			return fmt.Errorf("index file comments: %w", err)
		}
	}
	if version < 46 {
		columns, err := s.tableColumns(ctx, db, "workspaces")
		if err != nil {
			return err
		}
		if !columns["domain"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN domain TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate workspace domain: %w", err)
			}
		}
	}
	if version < 47 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_permission_requests (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), requester_id TEXT NOT NULL REFERENCES users(id), target_user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, trigger_id TEXT NOT NULL, created_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate app permission requests: %w", err)
		}
	}
	if version < 48 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS views (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), type TEXT NOT NULL, external_id TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL, hash TEXT NOT NULL, root_view_id TEXT NOT NULL, previous_view_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate views: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS views_workspace_external`); err != nil {
			return fmt.Errorf("replace views external id index: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS views_workspace_external ON views(workspace_id, external_id) WHERE external_id <> ''`); err != nil {
			return fmt.Errorf("index views external id: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS views_published_user ON views(workspace_id, user_id, type, updated_at)`); err != nil {
			return fmt.Errorf("index published views: %w", err)
		}
	}
	if version < 49 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workflow_steps (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), edit_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, inputs TEXT NOT NULL DEFAULT '{}', outputs TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '', step_name TEXT NOT NULL DEFAULT '', image_url TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate workflow steps: %w", err)
		}
	}
	if version < 50 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS dialogs (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), payload TEXT NOT NULL, created_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate dialogs: %w", err)
		}
	}
	if version < 51 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bots (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), app_id TEXT NOT NULL DEFAULT '', user_id TEXT NOT NULL REFERENCES users(id), name TEXT NOT NULL, image_36 TEXT NOT NULL DEFAULT '', image_48 TEXT NOT NULL DEFAULT '', image_72 TEXT NOT NULL DEFAULT '', deleted INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate bots: %w", err)
		}
	}
	if version < 52 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS user_migrations (workspace_id TEXT NOT NULL REFERENCES workspaces(id), old_id TEXT NOT NULL, global_id TEXT NOT NULL, PRIMARY KEY (workspace_id, old_id), UNIQUE (workspace_id, global_id))`); err != nil {
			return fmt.Errorf("migrate user migrations: %w", err)
		}
	}
	if version < 53 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS conversation_teams (conversation_id TEXT NOT NULL REFERENCES conversations(id), team_id TEXT NOT NULL REFERENCES workspaces(id), org_channel INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (conversation_id, team_id))`); err != nil {
			return fmt.Errorf("migrate conversation teams: %w", err)
		}
	}
	if version < 54 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS oauth_clients (id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL, app_id TEXT NOT NULL)`); err != nil {
			return fmt.Errorf("migrate oauth clients: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS oauth_codes (code TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES oauth_clients(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, redirect_uri TEXT NOT NULL DEFAULT '')`); err != nil {
			return fmt.Errorf("migrate oauth codes: %w", err)
		}
	}
	if version < 55 {
		columns, err := s.outboxColumns(ctx, db)
		if err != nil {
			return err
		}
		if !columns["actor_id"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE outbox ADD COLUMN actor_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate outbox actor: %w", err)
			}
		}
	}
	if version < 56 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS rtm_connections (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), expires_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate RTM connections: %w", err)
		}
	}
	if version < 57 {
		columns, err := s.tableColumns(ctx, db, "lifecycle_state")
		if err != nil {
			return err
		}
		if !columns["wake_deadline"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE lifecycle_state ADD COLUMN wake_deadline TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate lifecycle wake deadline: %w", err)
			}
		}
	}
	if version < 58 {
		var workspaceID, email string
		err := db.QueryRowContext(ctx, `SELECT workspace_id, MIN(email) FROM users WHERE email <> '' GROUP BY workspace_id, lower(email) HAVING COUNT(*) > 1 LIMIT 1`).Scan(&workspaceID, &email)
		if err == nil {
			return fmt.Errorf("migrate user email uniqueness: duplicate email %q in workspace %q", email, workspaceID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check user email uniqueness: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS users_workspace_email`); err != nil {
			return fmt.Errorf("replace user email index: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX users_workspace_email ON users(workspace_id, lower(email)) WHERE email <> ''`); err != nil {
			return fmt.Errorf("index user emails: %w", err)
		}
	}
	if version < 59 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_tokens (token_hash TEXT PRIMARY KEY, app_id TEXT NOT NULL, scopes TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0)`); err != nil {
			return fmt.Errorf("migrate app tokens: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS socket_mode_connections (id TEXT PRIMARY KEY, app_id TEXT NOT NULL, expires_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate Socket Mode connections: %w", err)
		}
	}
	if version < 60 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_installations (app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, PRIMARY KEY (app_id, workspace_id))`); err != nil {
			return fmt.Errorf("migrate app installations: %w", err)
		}
	}
	if version < 61 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS socket_mode_cursors (app_id TEXT PRIMARY KEY, sequence INTEGER NOT NULL DEFAULT 0)`); err != nil {
			return fmt.Errorf("migrate Socket Mode cursors: %w", err)
		}
	}
	if version < 62 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS socket_mode_responses (app_id TEXT NOT NULL, envelope_id TEXT NOT NULL, payload TEXT NOT NULL, received_at INTEGER NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at INTEGER NOT NULL DEFAULT 0, acknowledged_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (app_id, envelope_id))`); err != nil {
			return fmt.Errorf("migrate Socket Mode responses: %w", err)
		}
	}
	if version < 63 {
		columns, err := s.tableColumns(ctx, db, "socket_mode_connections")
		if err != nil {
			return fmt.Errorf("inspect Socket Mode connection state: %w", err)
		}
		if !columns["consumed_at"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE socket_mode_connections ADD COLUMN consumed_at INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("migrate Socket Mode connection state: %w", err)
			}
		}
	}
	if version < 64 {
		columns, err := s.sessionColumns(ctx, db)
		if err != nil {
			return err
		}
		for _, column := range []string{"oidc_provider", "oidc_id_token", "oidc_subject", "oidc_sid"} {
			if !columns[column] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN `+column+` TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migrate session %s: %w", column, err)
				}
			}
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)`, schemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record sqlite schema version: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read sqlite schema version: %w", err)
	}
	if version != schemaVersion {
		return fmt.Errorf("unsupported sqlite schema version %d (want %d)", version, schemaVersion)
	}
	return nil
}

func (s *Store) Load() (lifecycle.StateRecord, error) {
	var state string
	var generation uint64
	var wakeDeadline string
	if err := s.db.QueryRow(`SELECT state, generation, wake_deadline FROM lifecycle_state WHERE id = 1`).Scan(&state, &generation, &wakeDeadline); err != nil {
		return lifecycle.StateRecord{}, err
	}
	deadline, err := parseLifecycleWakeDeadline(wakeDeadline)
	if err != nil {
		return lifecycle.StateRecord{}, err
	}
	return lifecycle.StateRecord{State: lifecycle.State(state), Generation: generation, WakeDeadline: deadline}, nil
}

func (s *Store) CompareAndSwap(expected, next lifecycle.StateRecord) error {
	result, err := s.db.Exec(`UPDATE lifecycle_state SET state = ?, generation = ?, wake_deadline = ? WHERE id = 1 AND state = ? AND generation = ? AND wake_deadline = ?`, next.State, next.Generation, formatLifecycleWakeDeadline(next.WakeDeadline), expected.State, expected.Generation, formatLifecycleWakeDeadline(expected.WakeDeadline))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return lifecycle.ErrStateConflict
	}
	return nil
}

func formatLifecycleWakeDeadline(deadline time.Time) string {
	if deadline.IsZero() {
		return ""
	}
	return deadline.UTC().Format(time.RFC3339Nano)
}

func parseLifecycleWakeDeadline(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode lifecycle wake deadline: %w", err)
	}
	return deadline.UTC(), nil
}

func (s *Store) outboxColumns(ctx context.Context, db queryExecutor) (map[string]bool, error) {
	return s.tableColumns(ctx, db, "outbox")
}

func (s *Store) messageColumns(ctx context.Context, db queryExecutor) (map[string]bool, error) {
	return s.tableColumns(ctx, db, "messages")
}

func (s *Store) sessionColumns(ctx context.Context, db queryExecutor) (map[string]bool, error) {
	return s.tableColumns(ctx, db, "sessions")
}

func (s *Store) tableColumns(ctx context.Context, db queryExecutor, table string) (map[string]bool, error) {
	if table != "outbox" && table != "messages" && table != "sessions" && table != "users" && table != "workspaces" && table != "conversations" && table != "scheduled_messages" && table != "files" && table != "invite_requests" && table != "lifecycle_state" && table != "socket_mode_connections" {
		return nil, errors.New("unsupported schema table")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var index int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (s *Store) GetWorkspace(ctx context.Context, id domain.WorkspaceID) (domain.Workspace, error) {
	var value domain.Workspace
	err := s.db.QueryRowContext(ctx, `SELECT id, domain, name, description, discoverability, icon_url FROM workspaces WHERE id = ?`, id).Scan(&value.ID, &value.Domain, &value.Name, &value.Description, &value.Discoverability, &value.IconURL)
	if err := translateNotFound(err); err != nil {
		return domain.Workspace{}, err
	}
	return s.withDefaultChannels(ctx, s.db, value)
}

func (s *Store) CreateWorkspace(ctx context.Context, value domain.Workspace, event events.Event) error {
	if value.ID == "" || strings.TrimSpace(value.Domain) == "" || strings.TrimSpace(value.Name) == "" || !value.Discoverability.Valid() {
		return errors.New("invalid workspace")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces(id, domain, name, description, discoverability, icon_url) VALUES (?, ?, ?, ?, ?, ?)`, value.ID, value.Domain, value.Name, value.Description, value.Discoverability, value.IconURL); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) withDefaultChannels(ctx context.Context, db queryExecutor, value domain.Workspace) (domain.Workspace, error) {
	rows, err := db.QueryContext(ctx, `SELECT conversation_id FROM workspace_default_channels WHERE workspace_id = ? ORDER BY conversation_id`, value.ID)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer rows.Close()
	value.DefaultChannelIDs = make([]domain.ConversationID, 0)
	for rows.Next() {
		var channel domain.ConversationID
		if err := rows.Scan(&channel); err != nil {
			return domain.Workspace{}, err
		}
		value.DefaultChannelIDs = append(value.DefaultChannelIDs, channel)
	}
	if err := rows.Err(); err != nil {
		return domain.Workspace{}, err
	}
	return value, nil
}

func (s *Store) SetWorkspaceName(ctx context.Context, id domain.WorkspaceID, name string, event events.Event) (domain.Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return domain.Workspace{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Workspace{}, err
	}
	if changed != 1 {
		return domain.Workspace{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	var value domain.Workspace
	if err := s.db.QueryRowContext(ctx, `SELECT id, domain, name, description, discoverability, icon_url FROM workspaces WHERE id = ?`, id).Scan(&value.ID, &value.Domain, &value.Name, &value.Description, &value.Discoverability, &value.IconURL); err != nil {
		return domain.Workspace{}, err
	}
	return s.withDefaultChannels(ctx, s.db, value)
}

func (s *Store) SetWorkspaceDescription(ctx context.Context, id domain.WorkspaceID, description string, event events.Event) (domain.Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET description = ? WHERE id = ?`, description, id)
	if err != nil {
		return domain.Workspace{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Workspace{}, err
	}
	if changed != 1 {
		return domain.Workspace{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	var value domain.Workspace
	if err := s.db.QueryRowContext(ctx, `SELECT id, domain, name, description, discoverability, icon_url FROM workspaces WHERE id = ?`, id).Scan(&value.ID, &value.Domain, &value.Name, &value.Description, &value.Discoverability, &value.IconURL); err != nil {
		return domain.Workspace{}, err
	}
	return s.withDefaultChannels(ctx, s.db, value)
}

func (s *Store) SetWorkspaceDiscoverability(ctx context.Context, id domain.WorkspaceID, discoverability domain.WorkspaceDiscoverability, event events.Event) (domain.Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET discoverability = ? WHERE id = ?`, discoverability, id)
	if err != nil {
		return domain.Workspace{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Workspace{}, err
	}
	if changed != 1 {
		return domain.Workspace{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	var value domain.Workspace
	if err := s.db.QueryRowContext(ctx, `SELECT id, domain, name, description, discoverability, icon_url FROM workspaces WHERE id = ?`, id).Scan(&value.ID, &value.Domain, &value.Name, &value.Description, &value.Discoverability, &value.IconURL); err != nil {
		return domain.Workspace{}, err
	}
	return s.withDefaultChannels(ctx, s.db, value)
}

func (s *Store) SetWorkspaceIcon(ctx context.Context, id domain.WorkspaceID, iconURL string, event events.Event) (domain.Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET icon_url = ? WHERE id = ?`, iconURL, id)
	if err != nil {
		return domain.Workspace{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Workspace{}, err
	}
	if changed != 1 {
		return domain.Workspace{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	var value domain.Workspace
	if err := s.db.QueryRowContext(ctx, `SELECT id, domain, name, description, discoverability, icon_url FROM workspaces WHERE id = ?`, id).Scan(&value.ID, &value.Domain, &value.Name, &value.Description, &value.Discoverability, &value.IconURL); err != nil {
		return domain.Workspace{}, err
	}
	return s.withDefaultChannels(ctx, s.db, value)
}

func (s *Store) SetWorkspaceDefaultChannels(ctx context.Context, id domain.WorkspaceID, channels []domain.ConversationID, event events.Event) (domain.Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ?`, id).Scan(&exists); err != nil {
		return domain.Workspace{}, translateNotFound(err)
	}
	for _, channel := range channels {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ? AND workspace_id = ? AND is_private = 0 AND is_direct = 0 AND is_group_direct = 0`, channel, id).Scan(&exists); err != nil {
			return domain.Workspace{}, translateNotFound(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_default_channels WHERE workspace_id = ?`, id); err != nil {
		return domain.Workspace{}, err
	}
	for _, channel := range channels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_default_channels(workspace_id, conversation_id) VALUES (?, ?)`, id, channel); err != nil {
			return domain.Workspace{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	var value domain.Workspace
	if err := s.db.QueryRowContext(ctx, `SELECT id, domain, name, description, discoverability, icon_url FROM workspaces WHERE id = ?`, id).Scan(&value.ID, &value.Domain, &value.Name, &value.Description, &value.Discoverability, &value.IconURL); err != nil {
		return domain.Workspace{}, err
	}
	return s.withDefaultChannels(ctx, s.db, value)
}

func (s *Store) GetWorkspaceMembership(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspaceMembership, error) {
	var value domain.WorkspaceMembership
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, user_id, role, active FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&value.WorkspaceID, &value.UserID, &value.Role, &active)
	value.Active = active != 0
	return value, translateNotFound(err)
}

func (s *Store) GetUser(ctx context.Context, id domain.UserID) (domain.User, error) {
	var value domain.User
	var deleted int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE id = ?`, id).Scan(&value.ID, &value.WorkspaceID, &value.Email, &value.Name, &value.RealName, &value.Profile.DisplayName, &value.Profile.StatusText, &value.Profile.StatusEmoji, &value.Profile.Image24, &value.Profile.Image32, &value.Profile.Image48, &value.Profile.Image72, &value.Profile.Image192, &value.Profile.Image512, &value.Profile.Image1024, &deleted, &value.Presence)
	value.Deleted = deleted != 0
	return value, translateNotFound(err)
}

func (s *Store) CreateUser(ctx context.Context, user domain.User, membership domain.WorkspaceMembership, event events.Event) error {
	if user.ID == "" || user.WorkspaceID == "" || user.Email == "" || user.Name == "" || membership.WorkspaceID != user.WorkspaceID || membership.UserID != user.ID || !membership.Active {
		return errors.New("user and active workspace membership are required")
	}
	if membership.Role != domain.WorkspaceRoleMember && membership.Role != domain.WorkspaceRoleAdmin {
		return errors.New("user membership role must be member or admin")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workspaceExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ?`, user.WorkspaceID).Scan(&workspaceExists); err != nil {
		return translateNotFound(err)
	}
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE workspace_id = ? AND lower(email) = lower(?) AND deleted = 0`, user.WorkspaceID, user.Email).Scan(&existing); err == nil {
		return store.ErrAlreadyExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if user.Presence == "" {
		user.Presence = domain.PresenceAuto
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, workspace_id, email, name, real_name, presence) VALUES (?, ?, ?, ?, ?, ?)`, user.ID, user.WorkspaceID, user.Email, user.Name, user.RealName, user.Presence); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role, active) VALUES (?, ?, ?, 1)`, membership.WorkspaceID, membership.UserID, membership.Role); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, actor_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.ActorID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FindUserByEmail(ctx context.Context, workspace domain.WorkspaceID, email string) (domain.User, error) {
	var value domain.User
	var deleted int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE workspace_id = ? AND lower(email) = lower(?) AND deleted = 0 LIMIT 1`, workspace, strings.TrimSpace(email)).Scan(&value.ID, &value.WorkspaceID, &value.Email, &value.Name, &value.RealName, &value.Profile.DisplayName, &value.Profile.StatusText, &value.Profile.StatusEmoji, &value.Profile.Image24, &value.Profile.Image32, &value.Profile.Image48, &value.Profile.Image72, &value.Profile.Image192, &value.Profile.Image512, &value.Profile.Image1024, &deleted, &value.Presence)
	value.Deleted = deleted != 0
	return value, translateNotFound(err)
}

func (s *Store) UpdateUserProfile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, profile domain.UserProfile, event events.Event) (domain.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET display_name = ?, status_text = ?, status_emoji = ?, image_24 = ?, image_32 = ?, image_48 = ?, image_72 = ?, image_192 = ?, image_512 = ?, image_1024 = ? WHERE id = ? AND workspace_id = ? AND deleted = 0 AND EXISTS (SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ? AND active = 1)`, profile.DisplayName, profile.StatusText, profile.StatusEmoji, profile.Image24, profile.Image32, profile.Image48, profile.Image72, profile.Image192, profile.Image512, profile.Image1024, userID, workspaceID, workspaceID, userID)
	if err != nil {
		return domain.User{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.User{}, err
	}
	if changed != 1 {
		return domain.User{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.User{}, err
	}
	var user domain.User
	var deleted int
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE id = ?`, userID).Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
		return domain.User{}, err
	}
	user.Deleted = deleted != 0
	if err := tx.Commit(); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Store) SetUserPresence(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, presence domain.Presence, event events.Event) (domain.User, error) {
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		return domain.User{}, errors.New("invalid user presence")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET presence = ? WHERE id = ? AND workspace_id = ? AND deleted = 0 AND EXISTS (SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ? AND active = 1)`, presence, userID, workspaceID, workspaceID, userID)
	if err != nil {
		return domain.User{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.User{}, err
	}
	if changed != 1 {
		return domain.User{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.User{}, err
	}
	var user domain.User
	var deleted int
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE id = ?`, userID).Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
		return domain.User{}, err
	}
	user.Deleted = deleted != 0
	if err := tx.Commit(); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Store) SetUserExpiration(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, expiration time.Time, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0`, userID, workspaceID).Scan(&exists); err != nil {
		return translateNotFound(err)
	}
	if expiration.IsZero() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_expirations WHERE user_id = ? AND workspace_id = ?`, userID, workspaceID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO user_expirations(user_id, workspace_id, expiration_ts) VALUES (?, ?, ?) ON CONFLICT(user_id) DO UPDATE SET workspace_id = excluded.workspace_id, expiration_ts = excluded.expiration_ts`, userID, workspaceID, expiration.UTC().Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetUserDeleted(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, deleted bool, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET deleted = ? WHERE id = ? AND workspace_id = ?`, boolInt(deleted), userID, workspaceID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET active = ? WHERE workspace_id = ? AND user_id = ?`, boolInt(!deleted), workspaceID, userID); err != nil {
		return err
	}
	if deleted {
		if _, err := tx.ExecContext(ctx, `UPDATE tokens SET revoked = 1 WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked = 1 WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AssignUser(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channels []domain.ConversationID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET deleted = 0 WHERE id = ? AND workspace_id = ?`, userID, workspaceID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	result, err = tx.ExecContext(ctx, `UPDATE workspace_members SET active = 1 WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	for _, channelID := range channels {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ? AND workspace_id = ? AND is_direct = 0`, channelID, workspaceID).Scan(&exists); err != nil {
			return translateNotFound(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) VALUES (?, ?) ON CONFLICT(conversation_id, user_id) DO NOTHING`, channelID, userID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetWorkspaceRole(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole, event events.Event) error {
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return errors.New("invalid workspace role")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workspace_members SET role = ?, active = 1 WHERE workspace_id = ? AND user_id = ?`, role, workspaceID, userID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetDoNotDisturb(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.DoNotDisturb, error) {
	var value domain.DoNotDisturb
	var enabled int
	var snooze, nextStart, nextEnd int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, user_id, enabled, snooze_until, next_start_at, next_end_at FROM do_not_disturb WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&value.WorkspaceID, &value.UserID, &enabled, &snooze, &nextStart, &nextEnd)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if lookupErr := s.db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE workspace_id = ? AND id = ? AND deleted = 0`, workspaceID, userID).Scan(&exists); lookupErr != nil {
			return domain.DoNotDisturb{}, store.ErrNotFound
		}
		return domain.DoNotDisturb{WorkspaceID: workspaceID, UserID: userID}, nil
	}
	if err != nil {
		return domain.DoNotDisturb{}, err
	}
	value.Enabled = enabled != 0
	value.SnoozeUntil = fromUnixSeconds(snooze)
	value.NextStartAt = fromUnixSeconds(nextStart)
	value.NextEndAt = fromUnixSeconds(nextEnd)
	return value, nil
}

func (s *Store) SetDoNotDisturb(ctx context.Context, value domain.DoNotDisturb, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE workspace_id = ? AND id = ? AND deleted = 0`, value.WorkspaceID, value.UserID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO do_not_disturb(workspace_id, user_id, enabled, snooze_until, next_start_at, next_end_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id) DO UPDATE SET enabled = excluded.enabled, snooze_until = excluded.snooze_until, next_start_at = excluded.next_start_at, next_end_at = excluded.next_end_at`, value.WorkspaceID, value.UserID, boolInt(value.Enabled), unixSeconds(value.SnoozeUntil), unixSeconds(value.NextStartAt), unixSeconds(value.NextEndAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func encodeUnfurls(value map[string]string) (string, error) {
	if value == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeUnfurls(raw string) (map[string]string, error) {
	value := make(map[string]string)
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return value, nil
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	if value == nil {
		return make(map[string]string), nil
	}
	return value, nil
}

func unixSeconds(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func fromUnixSeconds(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

func (s *Store) ListUsers(ctx context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.UserPage, error) {
	if request.Limit <= 0 {
		return domain.UserPage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	query := `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE workspace_id = ?`
	args := []any{workspace}
	if after != "" {
		query += ` AND id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.UserPage{}, err
	}
	defer rows.Close()
	users := make([]domain.User, 0, request.Limit)
	for rows.Next() {
		var user domain.User
		var deleted int
		if err := rows.Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
			return domain.UserPage{}, err
		}
		user.Deleted = deleted != 0
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return domain.UserPage{}, err
	}
	hasMore := len(users) > request.Limit
	if hasMore {
		users = users[:request.Limit]
	}
	page := domain.UserPage{Users: users, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(users[len(users)-1].ID))
	}
	return page, err
}

func (s *Store) ListAdminUsers(ctx context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.AdminUserPage, error) {
	if request.Limit <= 0 {
		return domain.AdminUserPage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.AdminUserPage{}, err
	}
	query := `SELECT u.id, u.workspace_id, u.email, u.name, u.real_name, u.display_name, u.status_text, u.status_emoji, u.image_24, u.image_32, u.image_48, u.image_72, u.image_192, u.image_512, u.image_1024, u.deleted, u.presence, m.role, m.active FROM users u JOIN workspace_members m ON m.user_id = u.id AND m.workspace_id = u.workspace_id WHERE u.workspace_id = ?`
	args := []any{workspace}
	if after != "" {
		query += ` AND u.id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY u.id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.AdminUserPage{}, err
	}
	defer rows.Close()
	values := make([]domain.AdminUser, 0, request.Limit+1)
	for rows.Next() {
		var value domain.AdminUser
		var deleted, active int
		if err := rows.Scan(&value.User.ID, &value.User.WorkspaceID, &value.User.Email, &value.User.Name, &value.User.RealName, &value.User.Profile.DisplayName, &value.User.Profile.StatusText, &value.User.Profile.StatusEmoji, &value.User.Profile.Image24, &value.User.Profile.Image32, &value.User.Profile.Image48, &value.User.Profile.Image72, &value.User.Profile.Image192, &value.User.Profile.Image512, &value.User.Profile.Image1024, &deleted, &value.User.Presence, &value.Membership.Role, &active); err != nil {
			return domain.AdminUserPage{}, err
		}
		value.User.Deleted = deleted != 0
		value.Membership.WorkspaceID = workspace
		value.Membership.UserID = value.User.ID
		value.Membership.Active = active != 0
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.AdminUserPage{}, err
	}
	page := domain.AdminUserPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
	}
	page.Users = values
	if page.HasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].User.ID))
	}
	return page, err
}

func (s *Store) ListUsersByRole(ctx context.Context, workspace domain.WorkspaceID, role domain.WorkspaceRole, request domain.PageRequest) (domain.UserPage, error) {
	if request.Limit <= 0 {
		return domain.UserPage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	query := `SELECT u.id, u.workspace_id, u.email, u.name, u.real_name, u.display_name, u.status_text, u.status_emoji, u.image_24, u.image_32, u.image_48, u.image_72, u.image_192, u.image_512, u.image_1024, u.deleted, u.presence FROM users u JOIN workspace_members m ON m.user_id = u.id AND m.workspace_id = u.workspace_id WHERE u.workspace_id = ? AND m.role = ? AND m.active = 1 AND u.deleted = 0`
	args := []any{workspace, role}
	if after != "" {
		query += ` AND u.id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY u.id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.UserPage{}, err
	}
	defer rows.Close()
	users := make([]domain.User, 0, request.Limit+1)
	for rows.Next() {
		var user domain.User
		var deleted int
		if err := rows.Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
			return domain.UserPage{}, err
		}
		user.Deleted = deleted != 0
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return domain.UserPage{}, err
	}
	page := domain.UserPage{HasMore: len(users) > request.Limit}
	if page.HasMore {
		users = users[:request.Limit]
	}
	page.Users = users
	if page.HasMore {
		page.NextCursor, err = domain.NewListCursor(string(users[len(users)-1].ID))
	}
	return page, err
}

func (s *Store) ListConversationMembers(ctx context.Context, conversation domain.ConversationID, request domain.PageRequest) (domain.UserPage, error) {
	if request.Limit <= 0 {
		return domain.UserPage{}, errors.New("page limit must be positive")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ?`, conversation).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return domain.UserPage{}, store.ErrNotFound
	} else if err != nil {
		return domain.UserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	query := `SELECT u.id, u.workspace_id, u.email, u.name, u.real_name, u.display_name, u.status_text, u.status_emoji, u.image_24, u.image_32, u.image_48, u.image_72, u.image_192, u.image_512, u.image_1024, u.deleted, u.presence FROM users u JOIN conversation_members m ON m.user_id = u.id WHERE m.conversation_id = ? AND u.deleted = 0`
	args := []any{conversation}
	if after != "" {
		query += ` AND u.id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY u.id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.UserPage{}, err
	}
	defer rows.Close()
	users := make([]domain.User, 0, request.Limit)
	for rows.Next() {
		var user domain.User
		var deleted int
		if err := rows.Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
			return domain.UserPage{}, err
		}
		user.Deleted = deleted != 0
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return domain.UserPage{}, err
	}
	hasMore := len(users) > request.Limit
	if hasMore {
		users = users[:request.Limit]
	}
	page := domain.UserPage{Users: users, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(users[len(users)-1].ID))
	}
	return page, err
}

func (s *Store) LookupToken(ctx context.Context, token string) (domain.TokenRecord, error) {
	var record domain.TokenRecord
	var scopes string
	var revoked int
	err := s.db.QueryRowContext(ctx, `SELECT t.workspace_id, t.user_id, t.scopes, t.revoked FROM tokens t WHERE t.token_hash = ? AND NOT EXISTS (SELECT 1 FROM user_expirations e WHERE e.user_id = t.user_id AND e.workspace_id = t.workspace_id AND e.expiration_ts > 0 AND e.expiration_ts <= ?)`, domain.HashToken(token), time.Now().UTC().Unix()).Scan(&record.WorkspaceID, &record.UserID, &scopes, &revoked)
	if err != nil {
		return domain.TokenRecord{}, translateNotFound(err)
	}
	record.Scopes = domain.NormalizeScopes(strings.Fields(scopes))
	record.Revoked = revoked != 0
	return record, nil
}

func (s *Store) SeedAppToken(ctx context.Context, token string, record domain.AppTokenRecord) error {
	if record.AppID == "" {
		return errors.New("app token requires an app ID")
	}
	revoked := 0
	if record.Revoked {
		revoked = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_tokens(token_hash, app_id, scopes, revoked) VALUES (?, ?, ?, ?) ON CONFLICT(token_hash) DO NOTHING`, domain.HashToken(token), record.AppID, strings.Join(domain.NormalizeScopes(record.Scopes), " "), revoked)
	return err
}

func (s *Store) LookupAppToken(ctx context.Context, token string) (domain.AppTokenRecord, error) {
	var record domain.AppTokenRecord
	var scopes string
	var revoked int
	err := s.db.QueryRowContext(ctx, `SELECT app_id, scopes, revoked FROM app_tokens WHERE token_hash = ?`, domain.HashToken(token)).Scan(&record.AppID, &scopes, &revoked)
	if err != nil {
		return domain.AppTokenRecord{}, translateNotFound(err)
	}
	record.Scopes = domain.NormalizeScopes(strings.Fields(scopes))
	record.Revoked = revoked != 0
	return record, nil
}

func (s *Store) RevokeToken(ctx context.Context, token string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked = 1 WHERE token_hash = ?`, domain.HashToken(token))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) RevokeAppToken(ctx context.Context, token string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE app_tokens SET revoked = 1 WHERE token_hash = ?`, domain.HashToken(token))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) SeedSession(ctx context.Context, token string, record domain.SessionRecord) error {
	revoked := 0
	if record.Revoked {
		revoked = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(session_hash, workspace_id, user_id, scopes, expires_at, revoked, oidc_provider, oidc_id_token, oidc_subject, oidc_sid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(session_hash) DO NOTHING`, domain.HashToken(token), record.WorkspaceID, record.UserID, strings.Join(domain.NormalizeScopes(record.Scopes), " "), record.ExpiresAt.UTC().Format(time.RFC3339Nano), revoked, record.OIDCProvider, record.OIDCIDToken, record.OIDCSubject, record.OIDCSID)
	return err
}

func (s *Store) CreateSession(ctx context.Context, token string, record domain.SessionRecord) error {
	if strings.TrimSpace(token) == "" || record.WorkspaceID == "" || record.UserID == "" || record.ExpiresAt.IsZero() || !record.ExpiresAt.After(time.Now().UTC()) || len(domain.NormalizeScopes(record.Scopes)) == 0 {
		return errors.New("invalid session")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO sessions(session_hash, workspace_id, user_id, scopes, expires_at, revoked, oidc_provider, oidc_id_token, oidc_subject, oidc_sid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(session_hash) DO NOTHING`, domain.HashToken(token), record.WorkspaceID, record.UserID, strings.Join(domain.NormalizeScopes(record.Scopes), " "), record.ExpiresAt.UTC().Format(time.RFC3339Nano), boolInt(record.Revoked), record.OIDCProvider, record.OIDCIDToken, record.OIDCSubject, record.OIDCSID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrAlreadyExists
	}
	return nil
}

func (s *Store) LookupSession(ctx context.Context, token string) (domain.SessionRecord, error) {
	var record domain.SessionRecord
	var scopes, expires string
	var revoked int
	err := s.db.QueryRowContext(ctx, `SELECT s.workspace_id, s.user_id, s.scopes, s.expires_at, s.revoked, s.oidc_provider, s.oidc_id_token, s.oidc_subject, s.oidc_sid FROM sessions s WHERE s.session_hash = ? AND NOT EXISTS (SELECT 1 FROM user_expirations e WHERE e.user_id = s.user_id AND e.workspace_id = s.workspace_id AND e.expiration_ts > 0 AND e.expiration_ts <= ?)`, domain.HashToken(token), time.Now().UTC().Unix()).Scan(&record.WorkspaceID, &record.UserID, &scopes, &expires, &revoked, &record.OIDCProvider, &record.OIDCIDToken, &record.OIDCSubject, &record.OIDCSID)
	if err != nil {
		return domain.SessionRecord{}, translateNotFound(err)
	}
	record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return domain.SessionRecord{}, err
	}
	record.Revoked = revoked != 0
	record.Scopes = domain.NormalizeScopes(strings.Fields(scopes))
	return record, nil
}

func (s *Store) GetAuthMethod(ctx context.Context, workspace domain.WorkspaceID, provider string) (domain.AuthMethod, error) {
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM auth_methods WHERE workspace_id = ? AND provider = ?`, workspace, provider).Scan(&enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AuthMethod{WorkspaceID: workspace, Provider: provider, Enabled: true}, nil
		}
		return domain.AuthMethod{}, err
	}
	return domain.AuthMethod{WorkspaceID: workspace, Provider: provider, Enabled: enabled != 0}, nil
}

func (s *Store) SetAuthMethod(ctx context.Context, value domain.AuthMethod) error {
	if value.WorkspaceID == "" || value.Provider == "" {
		return errors.New("invalid auth method")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_methods(workspace_id, provider, enabled) VALUES (?, ?, ?) ON CONFLICT(workspace_id, provider) DO UPDATE SET enabled = excluded.enabled`, value.WorkspaceID, value.Provider, boolInt(value.Enabled))
	return err
}

func (s *Store) GetExternalIdentity(ctx context.Context, workspace domain.WorkspaceID, provider, subject string) (domain.ExternalIdentity, error) {
	var value domain.ExternalIdentity
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, provider, subject, user_id FROM external_identities WHERE workspace_id = ? AND provider = ? AND subject = ?`, workspace, provider, subject).Scan(&value.WorkspaceID, &value.Provider, &value.Subject, &value.UserID)
	if err != nil {
		return domain.ExternalIdentity{}, translateNotFound(err)
	}
	return value, nil
}

func (s *Store) CreateExternalIdentity(ctx context.Context, value domain.ExternalIdentity) error {
	if value.WorkspaceID == "" || value.Provider == "" || value.Subject == "" || value.UserID == "" {
		return errors.New("invalid external identity")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO external_identities(workspace_id, provider, subject, user_id) VALUES (?, ?, ?, ?)`, value.WorkspaceID, value.Provider, value.Subject, value.UserID)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return store.ErrAlreadyExists
	}
	return err
}

func (s *Store) RevokeSession(ctx context.Context, token string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked = 1 WHERE session_hash = ?`, domain.HashToken(token))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) RevokeUserSessions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked = 1 WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetConversation(ctx context.Context, id domain.ConversationID) (domain.Conversation, error) {
	var value domain.Conversation
	var private, direct, groupDirect, archived int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE id = ?`, id).Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &archived, &private, &direct, &groupDirect)
	value.Archived = archived != 0
	value.IsPrivate = private != 0
	value.IsDirect = direct != 0
	value.IsGroupDirect = groupDirect != 0
	return value, translateNotFound(err)
}

func (s *Store) FindDirectConversation(ctx context.Context, workspaceID domain.WorkspaceID, members []domain.UserID) (domain.Conversation, error) {
	if len(members) < 2 {
		return domain.Conversation{}, store.ErrNotFound
	}
	seen := make(map[domain.UserID]struct{}, len(members))
	for _, member := range members {
		if _, exists := seen[member]; exists {
			return domain.Conversation{}, store.ErrNotFound
		}
		seen[member] = struct{}{}
	}
	query := `SELECT c.id, c.workspace_id, c.name, c.topic, c.purpose, c.archived, c.is_private, c.is_direct, c.is_group_direct FROM conversations c WHERE c.direct_key = ? LIMIT 1`
	args := []any{domain.DirectConversationKey(workspaceID, members)}
	var value domain.Conversation
	var private, direct, groupDirect, archived int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &archived, &private, &direct, &groupDirect)
	value.Archived = archived != 0
	value.IsPrivate = private != 0
	value.IsDirect = direct != 0
	value.IsGroupDirect = groupDirect != 0
	return value, translateNotFound(err)
}

func (s *Store) CreateDirectConversation(ctx context.Context, conversation domain.Conversation, members []domain.UserID, event events.Event) error {
	if !conversation.IsPrivate || (!conversation.IsDirect && !conversation.IsGroupDirect) || len(members) < 2 {
		return errors.New("invalid direct conversation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	direct, groupDirect := 0, 0
	if conversation.IsDirect {
		direct = 1
	}
	if conversation.IsGroupDirect {
		groupDirect = 1
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name, is_private, is_direct, is_group_direct, direct_key) VALUES (?, ?, ?, 1, ?, ?, ?) ON CONFLICT DO NOTHING`, conversation.ID, conversation.WorkspaceID, conversation.Name, direct, groupDirect, domain.DirectConversationKey(conversation.WorkspaceID, members))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrAlreadyExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_teams(conversation_id, team_id, org_channel) VALUES (?, ?, 0)`, conversation.ID, conversation.WorkspaceID); err != nil {
		return err
	}
	seen := make(map[domain.UserID]struct{}, len(members))
	for _, member := range members {
		if _, exists := seen[member]; exists {
			return errors.New("direct conversation contains duplicate members")
		}
		seen[member] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) SELECT ?, id FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0`, conversation.ID, member, conversation.WorkspaceID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_members WHERE conversation_id = ? AND user_id = ?`, conversation.ID, member).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return store.ErrNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateConversation(ctx context.Context, conversation domain.Conversation, creator domain.UserID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	private := 0
	if conversation.IsPrivate {
		private = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name, is_private) VALUES (?, ?, ?, ?)`, conversation.ID, conversation.WorkspaceID, conversation.Name, private); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_teams(conversation_id, team_id, org_channel) VALUES (?, ?, 0)`, conversation.ID, conversation.WorkspaceID); err != nil {
		return err
	}
	if conversation.IsPrivate {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) VALUES (?, ?)`, conversation.ID, creator); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RenameConversation(ctx context.Context, conversation domain.ConversationID, name string, event events.Event) (domain.Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET name = ? WHERE id = ?`, name, conversation)
	if err != nil {
		return domain.Conversation{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Conversation{}, err
	}
	if changed != 1 {
		return domain.Conversation{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) SELECT ?, workspace_id, ?, ?, ?, 0, '', '', '' FROM conversations WHERE id = ?`, event.ID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano), conversation); err != nil {
		return domain.Conversation{}, err
	}
	var value domain.Conversation
	var private, direct, groupDirect, archived int
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE id = ?`, conversation).Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &archived, &private, &direct, &groupDirect); err != nil {
		return domain.Conversation{}, err
	}
	value.Archived, value.IsPrivate, value.IsDirect, value.IsGroupDirect = archived != 0, private != 0, direct != 0, groupDirect != 0
	if err := tx.Commit(); err != nil {
		return domain.Conversation{}, err
	}
	return value, nil
}

func (s *Store) SetConversationTopic(ctx context.Context, conversation domain.ConversationID, topic string, event events.Event) (domain.Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET topic = ? WHERE id = ?`, topic, conversation)
	if err != nil {
		return domain.Conversation{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Conversation{}, err
	}
	if changed != 1 {
		return domain.Conversation{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) SELECT ?, workspace_id, ?, ?, ?, 0, '', '', '' FROM conversations WHERE id = ?`, event.ID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano), conversation); err != nil {
		return domain.Conversation{}, err
	}
	var value domain.Conversation
	var private, direct, groupDirect, archived int
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE id = ?`, conversation).Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &archived, &private, &direct, &groupDirect); err != nil {
		return domain.Conversation{}, err
	}
	value.Archived, value.IsPrivate, value.IsDirect, value.IsGroupDirect = archived != 0, private != 0, direct != 0, groupDirect != 0
	if err := tx.Commit(); err != nil {
		return domain.Conversation{}, err
	}
	return value, nil
}

func (s *Store) SetConversationPurpose(ctx context.Context, conversation domain.ConversationID, purpose string, event events.Event) (domain.Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET purpose = ? WHERE id = ?`, purpose, conversation)
	if err != nil {
		return domain.Conversation{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Conversation{}, err
	}
	if changed != 1 {
		return domain.Conversation{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) SELECT ?, workspace_id, ?, ?, ?, 0, '', '', '' FROM conversations WHERE id = ?`, event.ID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano), conversation); err != nil {
		return domain.Conversation{}, err
	}
	var value domain.Conversation
	var private, direct, groupDirect, archived int
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE id = ?`, conversation).Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &archived, &private, &direct, &groupDirect); err != nil {
		return domain.Conversation{}, err
	}
	value.Archived, value.IsPrivate, value.IsDirect, value.IsGroupDirect = archived != 0, private != 0, direct != 0, groupDirect != 0
	if err := tx.Commit(); err != nil {
		return domain.Conversation{}, err
	}
	return value, nil
}

func (s *Store) SetConversationArchived(ctx context.Context, conversation domain.ConversationID, archived bool, event events.Event) (domain.Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	valueArchived := 0
	if archived {
		valueArchived = 1
	}
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET archived = ? WHERE id = ?`, valueArchived, conversation)
	if err != nil {
		return domain.Conversation{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Conversation{}, err
	}
	if changed != 1 {
		return domain.Conversation{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) SELECT ?, workspace_id, ?, ?, ?, 0, '', '', '' FROM conversations WHERE id = ?`, event.ID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano), conversation); err != nil {
		return domain.Conversation{}, err
	}
	var value domain.Conversation
	var private, direct, groupDirect, storedArchived int
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE id = ?`, conversation).Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &storedArchived, &private, &direct, &groupDirect); err != nil {
		return domain.Conversation{}, err
	}
	value.Archived, value.IsPrivate, value.IsDirect, value.IsGroupDirect = storedArchived != 0, private != 0, direct != 0, groupDirect != 0
	if err := tx.Commit(); err != nil {
		return domain.Conversation{}, err
	}
	return value, nil
}

func (s *Store) DeleteConversation(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var private, direct, groupDirect int
	if err := tx.QueryRowContext(ctx, `SELECT is_private, is_direct, is_group_direct FROM conversations WHERE id = ? AND workspace_id = ?`, conversation, workspace).Scan(&private, &direct, &groupDirect); err != nil {
		return translateNotFound(err)
	}
	if direct != 0 || groupDirect != 0 {
		return store.ErrInvalidConversationType
	}
	statements := []string{
		`DELETE FROM remote_file_shares WHERE conversation_id = ?`,
		`DELETE FROM conversation_teams WHERE conversation_id = ?`,
		`DELETE FROM user_group_channels WHERE conversation_id = ?`,
		`DELETE FROM conversation_access_groups WHERE conversation_id = ?`,
		`DELETE FROM workspace_default_channels WHERE conversation_id = ?`,
		`DELETE FROM conversation_prefs WHERE conversation_id = ?`,
		`DELETE FROM read_cursors WHERE conversation_id = ?`,
		`DELETE FROM scheduled_messages WHERE channel_id = ?`,
		`DELETE FROM reactions WHERE message_id IN (SELECT id FROM messages WHERE conversation = ?)`,
		`DELETE FROM pins WHERE message_id IN (SELECT id FROM messages WHERE conversation = ?)`,
		`DELETE FROM stars WHERE message_id IN (SELECT id FROM messages WHERE conversation = ?)`,
		`DELETE FROM messages WHERE conversation = ?`,
		`DELETE FROM conversation_members WHERE conversation_id = ?`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, conversation); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id = ? AND workspace_id = ?`, conversation, workspace)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetConversationAccessGroups(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, groups []domain.UserGroupID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ? AND workspace_id = ?`, conversation, workspace).Scan(&exists); err != nil {
		return translateNotFound(err)
	}
	for _, groupID := range groups {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM user_groups WHERE id = ? AND workspace_id = ? AND deleted_at = 0`, groupID, workspace).Scan(&exists); err != nil {
			return translateNotFound(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_access_groups WHERE conversation_id = ?`, conversation); err != nil {
		return err
	}
	for _, groupID := range groups {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_access_groups(conversation_id, group_id) VALUES (?, ?)`, conversation, groupID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListConversationAccessGroups(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) ([]domain.UserGroupID, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ? AND workspace_id = ?`, conversation, workspace).Scan(&exists); err != nil {
		return nil, translateNotFound(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.group_id FROM conversation_access_groups a JOIN user_groups g ON g.id = a.group_id WHERE a.conversation_id = ? AND g.workspace_id = ? AND g.deleted_at = 0 ORDER BY a.group_id`, conversation, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make([]domain.UserGroupID, 0)
	for rows.Next() {
		var groupID domain.UserGroupID
		if err := rows.Scan(&groupID); err != nil {
			return nil, err
		}
		groups = append(groups, groupID)
	}
	return groups, rows.Err()
}

func (s *Store) CreateInviteRequest(ctx context.Context, value domain.InviteRequest, event events.Event) error {
	channelIDs, err := json.Marshal(value.ChannelIDs)
	if err != nil {
		return fmt.Errorf("encode invite request channels: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if value.Status != domain.InviteRequestPending {
		return store.ErrAlreadyExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO invite_requests(id, workspace_id, email, requested_by, channel_ids, custom_message, real_name, resend, restricted, ultra_restricted, guest_expiration_at, status, created_at, reviewed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, value.ID, value.WorkspaceID, value.Email, value.RequestedBy, string(channelIDs), value.CustomMessage, value.RealName, boolInt(value.Resend), boolInt(value.Restricted), boolInt(value.UltraRestricted), unixSeconds(value.GuestExpirationAt), value.Status, value.CreatedAt.UTC().Unix()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetInviteRequest(ctx context.Context, workspace domain.WorkspaceID, id domain.InviteRequestID) (domain.InviteRequest, error) {
	var value domain.InviteRequest
	var created, reviewed, expiration int64
	var channelIDs string
	var resend, restricted, ultraRestricted int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, email, requested_by, channel_ids, custom_message, real_name, resend, restricted, ultra_restricted, guest_expiration_at, status, created_at, reviewed_at FROM invite_requests WHERE id = ? AND workspace_id = ?`, id, workspace).Scan(&value.ID, &value.WorkspaceID, &value.Email, &value.RequestedBy, &channelIDs, &value.CustomMessage, &value.RealName, &resend, &restricted, &ultraRestricted, &expiration, &value.Status, &created, &reviewed)
	if err != nil {
		return domain.InviteRequest{}, translateNotFound(err)
	}
	if err := json.Unmarshal([]byte(channelIDs), &value.ChannelIDs); err != nil {
		return domain.InviteRequest{}, fmt.Errorf("decode invite request channels: %w", err)
	}
	value.Resend, value.Restricted, value.UltraRestricted = resend != 0, restricted != 0, ultraRestricted != 0
	if expiration != 0 {
		value.GuestExpirationAt = time.Unix(expiration, 0).UTC()
	}
	value.CreatedAt = time.Unix(created, 0).UTC()
	if reviewed != 0 {
		value.ReviewedAt = time.Unix(reviewed, 0).UTC()
	}
	return value, nil
}

func (s *Store) SetInviteRequestStatus(ctx context.Context, workspace domain.WorkspaceID, id domain.InviteRequestID, status domain.InviteRequestStatus, reviewedAt time.Time, event events.Event) error {
	if status != domain.InviteRequestApproved && status != domain.InviteRequestDenied {
		return store.ErrInvalidInviteRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE invite_requests SET status = ?, reviewed_at = ? WHERE id = ? AND workspace_id = ? AND status = ?`, status, reviewedAt.UTC().Unix(), id, workspace, domain.InviteRequestPending)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListInviteRequests(ctx context.Context, workspace domain.WorkspaceID, status domain.InviteRequestStatus, request domain.PageRequest) (domain.InviteRequestPage, error) {
	if request.Limit <= 0 {
		return domain.InviteRequestPage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.InviteRequestPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, email, requested_by, channel_ids, custom_message, real_name, resend, restricted, ultra_restricted, guest_expiration_at, status, created_at, reviewed_at FROM invite_requests WHERE workspace_id = ? AND status = ? AND id > ? ORDER BY id LIMIT ?`, workspace, status, after, request.Limit+1)
	if err != nil {
		return domain.InviteRequestPage{}, err
	}
	defer rows.Close()
	values := make([]domain.InviteRequest, 0, request.Limit+1)
	for rows.Next() {
		var value domain.InviteRequest
		var created, reviewed, expiration int64
		var channelIDs string
		var resend, restricted, ultraRestricted int
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Email, &value.RequestedBy, &channelIDs, &value.CustomMessage, &value.RealName, &resend, &restricted, &ultraRestricted, &expiration, &value.Status, &created, &reviewed); err != nil {
			return domain.InviteRequestPage{}, err
		}
		if err := json.Unmarshal([]byte(channelIDs), &value.ChannelIDs); err != nil {
			return domain.InviteRequestPage{}, fmt.Errorf("decode invite request channels: %w", err)
		}
		value.Resend, value.Restricted, value.UltraRestricted = resend != 0, restricted != 0, ultraRestricted != 0
		if expiration != 0 {
			value.GuestExpirationAt = time.Unix(expiration, 0).UTC()
		}
		value.CreatedAt = time.Unix(created, 0).UTC()
		if reviewed != 0 {
			value.ReviewedAt = time.Unix(reviewed, 0).UTC()
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.InviteRequestPage{}, err
	}
	page := domain.InviteRequestPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.InviteRequestPage{}, err
		}
	}
	page.Requests = values
	return page, nil
}

func validAppApprovalStatusSQL(status domain.AppApprovalStatus) bool {
	return status == domain.AppApprovalRequested || status == domain.AppApprovalApproved || status == domain.AppApprovalRestricted
}

func (s *Store) SetAppApproval(ctx context.Context, workspace domain.WorkspaceID, appID domain.AppID, requestID domain.AppRequestID, approvalStatus domain.AppApprovalStatus, updatedAt time.Time, event events.Event) error {
	if strings.TrimSpace(string(workspace)) == "" || strings.TrimSpace(string(appID)) == "" || !validAppApprovalStatusSQL(approvalStatus) {
		return store.ErrInvalidAppApproval
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	created := updatedAt.UTC().Unix()
	result, err := tx.ExecContext(ctx, `INSERT INTO app_approvals(app_id, request_id, workspace_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(app_id) DO UPDATE SET request_id = excluded.request_id, status = excluded.status, updated_at = excluded.updated_at WHERE app_approvals.workspace_id = excluded.workspace_id`, appID, requestID, workspace, approvalStatus, created, created)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return store.ErrInvalidAppApproval
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, actor_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.ActorID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListAppApprovals(ctx context.Context, workspace domain.WorkspaceID, approvalStatus domain.AppApprovalStatus, request domain.PageRequest) (domain.AppApprovalPage, error) {
	if request.Limit <= 0 || !validAppApprovalStatusSQL(approvalStatus) {
		return domain.AppApprovalPage{}, store.ErrInvalidAppApproval
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.AppApprovalPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT app_id, request_id, workspace_id, status, created_at, updated_at FROM app_approvals WHERE workspace_id = ? AND status = ? AND app_id > ? ORDER BY app_id LIMIT ?`, workspace, approvalStatus, after, request.Limit+1)
	if err != nil {
		return domain.AppApprovalPage{}, err
	}
	defer rows.Close()
	values := make([]domain.AppApproval, 0, request.Limit+1)
	for rows.Next() {
		var value domain.AppApproval
		var created, updated int64
		if err := rows.Scan(&value.ID, &value.RequestID, &value.WorkspaceID, &value.Status, &created, &updated); err != nil {
			return domain.AppApprovalPage{}, err
		}
		value.CreatedAt = time.Unix(created, 0).UTC()
		value.UpdatedAt = time.Unix(updated, 0).UTC()
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.AppApprovalPage{}, err
	}
	page := domain.AppApprovalPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.AppApprovalPage{}, err
		}
	}
	page.Apps = values
	return page, nil
}

func (s *Store) CreateAppInstallation(ctx context.Context, value domain.AppInstallation) error {
	if value.AppID == "" || value.WorkspaceID == "" || value.CreatedAt.IsZero() {
		return store.ErrInvalidAppApproval
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_installations(app_id, workspace_id, enabled, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(app_id, workspace_id) DO UPDATE SET enabled = excluded.enabled`, value.AppID, value.WorkspaceID, boolInt(value.Enabled), value.CreatedAt.UTC().UnixNano())
	return err
}

func (s *Store) ListAppInstallations(ctx context.Context, appID domain.AppID) ([]domain.AppInstallation, error) {
	if appID == "" {
		return nil, store.ErrInvalidAppApproval
	}
	rows, err := s.db.QueryContext(ctx, `SELECT app_id, workspace_id, enabled, created_at FROM app_installations WHERE app_id = ? AND enabled = 1 ORDER BY workspace_id`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.AppInstallation, 0)
	for rows.Next() {
		var value domain.AppInstallation
		var enabled int
		var created int64
		if err := rows.Scan(&value.AppID, &value.WorkspaceID, &enabled, &created); err != nil {
			return nil, err
		}
		value.Enabled = enabled != 0
		value.CreatedAt = time.Unix(0, created).UTC()
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) CreateAppPermissionRequest(ctx context.Context, value domain.AppPermissionRequest, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.RequesterID == "" || value.TargetUserID == "" || value.TriggerID == "" || len(value.Scopes) == 0 {
		return errors.New("invalid app permission request")
	}
	scopes, err := json.Marshal(domain.NormalizeScopes(value.Scopes))
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_permission_requests(id, workspace_id, requester_id, target_user_id, scopes, trigger_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.RequesterID, value.TargetUserID, string(scopes), value.TriggerID, value.CreatedAt.UTC().Unix()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, actor_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.ActorID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateView(ctx context.Context, value domain.View, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Type == "" || value.Payload == "" || value.Hash == "" || value.CreatedAt.IsZero() {
		return errors.New("invalid view")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO views(id, workspace_id, user_id, type, external_id, payload, hash, root_view_id, previous_view_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.UserID, value.Type, value.ExternalID, value.Payload, value.Hash, value.RootViewID, value.PreviousViewID, value.CreatedAt.UTC().UnixNano(), value.UpdatedAt.UTC().UnixNano()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func scanView(row interface{ Scan(...any) error }) (domain.View, error) {
	var value domain.View
	var created, updated int64
	if err := row.Scan(&value.ID, &value.WorkspaceID, &value.UserID, &value.Type, &value.ExternalID, &value.Payload, &value.Hash, &value.RootViewID, &value.PreviousViewID, &created, &updated); err != nil {
		return domain.View{}, err
	}
	value.CreatedAt = time.Unix(0, created).UTC()
	value.UpdatedAt = time.Unix(0, updated).UTC()
	return value, nil
}

const viewColumns = `id, workspace_id, user_id, type, external_id, payload, hash, root_view_id, previous_view_id, created_at, updated_at`

func (s *Store) GetView(ctx context.Context, workspace domain.WorkspaceID, id domain.ViewID) (domain.View, error) {
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND id = ?`, workspace, id))
	return value, translateNotFound(err)
}

func (s *Store) GetViewByExternalID(ctx context.Context, workspace domain.WorkspaceID, externalID string) (domain.View, error) {
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND external_id = ?`, workspace, externalID))
	return value, translateNotFound(err)
}

func (s *Store) GetPublishedView(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID) (domain.View, error) {
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND user_id = ? AND type = 'home' ORDER BY updated_at DESC, id DESC LIMIT 1`, workspace, user))
	return value, translateNotFound(err)
}

func (s *Store) GetLatestView(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, viewType string) (domain.View, error) {
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND user_id = ? AND type = ? ORDER BY updated_at DESC, id DESC LIMIT 1`, workspace, user, viewType))
	return value, translateNotFound(err)
}

func (s *Store) UpdateView(ctx context.Context, value domain.View, expectedHash string, event events.Event) (domain.View, error) {
	if value.ID == "" || value.WorkspaceID == "" || value.Payload == "" || value.Hash == "" {
		return domain.View{}, errors.New("invalid view")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.View{}, err
	}
	defer tx.Rollback()
	query := `UPDATE views SET type = ?, external_id = ?, payload = ?, hash = ?, root_view_id = ?, previous_view_id = ?, updated_at = ? WHERE workspace_id = ? AND id = ?`
	args := []any{value.Type, value.ExternalID, value.Payload, value.Hash, value.RootViewID, value.PreviousViewID, value.UpdatedAt.UTC().UnixNano(), value.WorkspaceID, value.ID}
	if expectedHash != "" {
		query += ` AND hash = ?`
		args = append(args, expectedHash)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.View{}, store.ErrAlreadyExists
		}
		return domain.View{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.View{}, err
	}
	if changed != 1 {
		if expectedHash != "" {
			return domain.View{}, store.ErrConflict
		}
		return domain.View{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.View{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND id = ?`, value.WorkspaceID, value.ID).Scan(&value.ID, &value.WorkspaceID, &value.UserID, &value.Type, &value.ExternalID, &value.Payload, &value.Hash, &value.RootViewID, &value.PreviousViewID, new(int64), new(int64)); err != nil {
		return domain.View{}, err
	}
	// Read the committed value through the transaction so callers receive the
	// canonical timestamps and ownership fields.
	var created, updated int64
	if err := tx.QueryRowContext(ctx, `SELECT created_at, updated_at FROM views WHERE workspace_id = ? AND id = ?`, value.WorkspaceID, value.ID).Scan(&created, &updated); err != nil {
		return domain.View{}, err
	}
	value.CreatedAt = time.Unix(0, created).UTC()
	value.UpdatedAt = time.Unix(0, updated).UTC()
	if err := tx.Commit(); err != nil {
		return domain.View{}, err
	}
	return value, nil
}

func scanWorkflowStep(row interface{ Scan(...any) error }) (domain.WorkflowStep, error) {
	var value domain.WorkflowStep
	var created, updated int64
	if err := row.Scan(&value.ID, &value.WorkspaceID, &value.UserID, &value.EditID, &value.Status, &value.Inputs, &value.Outputs, &value.Error, &value.StepName, &value.ImageURL, &created, &updated); err != nil {
		return domain.WorkflowStep{}, err
	}
	value.CreatedAt = time.Unix(0, created).UTC()
	value.UpdatedAt = time.Unix(0, updated).UTC()
	return value, nil
}

const workflowStepColumns = `id, workspace_id, user_id, edit_id, status, inputs, outputs, error, step_name, image_url, created_at, updated_at`

func (s *Store) SetWorkflowStep(ctx context.Context, value domain.WorkflowStep, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Status == "" || value.UpdatedAt.IsZero() {
		return errors.New("invalid workflow step")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentWorkspace string
	var created int64
	lookupErr := tx.QueryRowContext(ctx, `SELECT workspace_id, created_at FROM workflow_steps WHERE id = ?`, value.ID).Scan(&currentWorkspace, &created)
	if lookupErr == nil {
		if domain.WorkspaceID(currentWorkspace) != value.WorkspaceID {
			return store.ErrNotFound
		}
		value.CreatedAt = time.Unix(0, created).UTC()
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	} else if value.CreatedAt.IsZero() {
		value.CreatedAt = value.UpdatedAt
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_steps(id, workspace_id, user_id, edit_id, status, inputs, outputs, error, step_name, image_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET user_id = excluded.user_id, edit_id = excluded.edit_id, status = excluded.status, inputs = excluded.inputs, outputs = excluded.outputs, error = excluded.error, step_name = excluded.step_name, image_url = excluded.image_url, updated_at = excluded.updated_at`, value.ID, value.WorkspaceID, value.UserID, value.EditID, value.Status, value.Inputs, value.Outputs, value.Error, value.StepName, value.ImageURL, value.CreatedAt.UTC().UnixNano(), value.UpdatedAt.UTC().UnixNano())
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetWorkflowStep(ctx context.Context, workspace domain.WorkspaceID, id domain.WorkflowStepID) (domain.WorkflowStep, error) {
	value, err := scanWorkflowStep(s.db.QueryRowContext(ctx, `SELECT `+workflowStepColumns+` FROM workflow_steps WHERE workspace_id = ? AND id = ?`, workspace, id))
	return value, translateNotFound(err)
}

func (s *Store) CreateDialog(ctx context.Context, value domain.Dialog, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Payload == "" || value.CreatedAt.IsZero() {
		return errors.New("invalid dialog")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO dialogs(id, workspace_id, user_id, payload, created_at) VALUES (?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.UserID, value.Payload, value.CreatedAt.UTC().UnixNano()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetDialog(ctx context.Context, workspace domain.WorkspaceID, id domain.DialogID) (domain.Dialog, error) {
	var value domain.Dialog
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, payload, created_at FROM dialogs WHERE workspace_id = ? AND id = ?`, workspace, id).Scan(&value.ID, &value.WorkspaceID, &value.UserID, &value.Payload, &created)
	if err := translateNotFound(err); err != nil {
		return domain.Dialog{}, err
	}
	value.CreatedAt = time.Unix(0, created).UTC()
	return value, nil
}

func (s *Store) CreateBot(ctx context.Context, value domain.Bot) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Name == "" || value.UpdatedAt.IsZero() {
		return errors.New("invalid bot")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO bots(id, workspace_id, app_id, user_id, name, image_36, image_48, image_72, deleted, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.AppID, value.UserID, value.Name, value.Image36, value.Image48, value.Image72, boolInt(value.Deleted), value.UpdatedAt.UTC().Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *Store) GetBot(ctx context.Context, workspace domain.WorkspaceID, id domain.BotID) (domain.Bot, error) {
	var value domain.Bot
	var deleted, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, app_id, user_id, name, image_36, image_48, image_72, deleted, updated_at FROM bots WHERE workspace_id = ? AND id = ?`, workspace, id).Scan(&value.ID, &value.WorkspaceID, &value.AppID, &value.UserID, &value.Name, &value.Image36, &value.Image48, &value.Image72, &deleted, &updated)
	if err := translateNotFound(err); err != nil {
		return domain.Bot{}, err
	}
	value.Deleted = deleted != 0
	value.UpdatedAt = time.Unix(updated, 0).UTC()
	return value, nil
}

func (s *Store) CreateUserMigration(ctx context.Context, value domain.UserMigration, event events.Event) error {
	if value.WorkspaceID == "" || value.OldID == "" || value.GlobalID == "" {
		return errors.New("invalid user migration")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_migrations(workspace_id, old_id, global_id) VALUES (?, ?, ?)`, value.WorkspaceID, value.OldID, value.GlobalID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FindUserMigration(ctx context.Context, workspace domain.WorkspaceID, id domain.UserID) (domain.UserMigration, error) {
	var value domain.UserMigration
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, old_id, global_id FROM user_migrations WHERE workspace_id = ? AND (old_id = ? OR global_id = ?)`, workspace, id, id).Scan(&value.WorkspaceID, &value.OldID, &value.GlobalID)
	if err := translateNotFound(err); err != nil {
		return domain.UserMigration{}, err
	}
	return value, nil
}

func (s *Store) SetConversationTeams(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, teams []domain.WorkspaceID, orgChannel bool, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner domain.WorkspaceID
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM conversations WHERE id = ?`, conversation).Scan(&owner); err != nil {
		return translateNotFound(err)
	}
	if owner != workspace {
		return store.ErrNotFound
	}
	if len(teams) == 0 && !orgChannel {
		return errors.New("conversation team association is empty")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_teams WHERE conversation_id = ?`, conversation); err != nil {
		return err
	}
	seen := make(map[domain.WorkspaceID]struct{}, len(teams))
	for _, team := range teams {
		if team == "" {
			return errors.New("invalid conversation team")
		}
		if _, exists := seen[team]; exists {
			continue
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ?`, team).Scan(&exists); err != nil {
			return translateNotFound(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_teams(conversation_id, team_id, org_channel) VALUES (?, ?, ?)`, conversation, team, boolInt(orgChannel)); err != nil {
			return err
		}
		seen[team] = struct{}{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListConversationTeams(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) ([]domain.WorkspaceID, bool, error) {
	var owner domain.WorkspaceID
	if err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM conversations WHERE id = ?`, conversation).Scan(&owner); err != nil {
		return nil, false, translateNotFound(err)
	}
	if owner != workspace {
		return nil, false, store.ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT team_id, org_channel FROM conversation_teams WHERE conversation_id = ? ORDER BY team_id`, conversation)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	teams := make([]domain.WorkspaceID, 0)
	org := false
	for rows.Next() {
		var team string
		var isOrg int
		if err := rows.Scan(&team, &isOrg); err != nil {
			return nil, false, err
		}
		teams = append(teams, domain.WorkspaceID(team))
		org = org || isOrg != 0
	}
	return teams, org, rows.Err()
}

func (s *Store) DisconnectConversationTeams(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, leaving []domain.WorkspaceID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner domain.WorkspaceID
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM conversations WHERE id = ?`, conversation).Scan(&owner); err != nil {
		return translateNotFound(err)
	}
	if owner != workspace {
		return store.ErrNotFound
	}
	if len(leaving) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_teams WHERE conversation_id = ?`, conversation); err != nil {
			return err
		}
	} else {
		for _, team := range leaving {
			if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_teams WHERE conversation_id = ? AND team_id = ?`, conversation, team); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListConnectedChannelInfo(ctx context.Context, workspace domain.WorkspaceID, channels []domain.ConversationID, teams []domain.WorkspaceID, request domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error) {
	if request.Limit <= 0 {
		return nil, false, "", errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, false, "", err
	}
	channelFilter := make(map[domain.ConversationID]struct{}, len(channels))
	for _, channel := range channels {
		channelFilter[channel] = struct{}{}
	}
	teamFilter := make(map[domain.WorkspaceID]struct{}, len(teams))
	for _, team := range teams {
		teamFilter[team] = struct{}{}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, ct.team_id FROM conversation_teams ct JOIN conversations c ON c.id = ct.conversation_id WHERE c.workspace_id = ? AND c.id > ? ORDER BY c.id, ct.team_id`, workspace, after)
	if err != nil {
		return nil, false, "", err
	}
	defer rows.Close()
	grouped := make(map[domain.ConversationID][]domain.WorkspaceID)
	for rows.Next() {
		var channel, team string
		if err := rows.Scan(&channel, &team); err != nil {
			return nil, false, "", err
		}
		cid := domain.ConversationID(channel)
		tid := domain.WorkspaceID(team)
		if len(channelFilter) > 0 {
			if _, ok := channelFilter[cid]; !ok {
				continue
			}
		}
		if len(teamFilter) > 0 {
			if _, ok := teamFilter[tid]; !ok {
				continue
			}
		}
		grouped[cid] = append(grouped[cid], tid)
	}
	if err := rows.Err(); err != nil {
		return nil, false, "", err
	}
	values := make([]domain.ConnectedChannelInfo, 0, len(grouped))
	for channel, associated := range grouped {
		sort.Slice(associated, func(i, j int) bool { return associated[i] < associated[j] })
		values = append(values, domain.ConnectedChannelInfo{ChannelID: channel, InternalTeamIDs: associated, OriginalConnectedChannelID: channel, OriginalConnectedHostID: workspace})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ChannelID < values[j].ChannelID })
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		next, err = domain.NewListCursor(string(values[len(values)-1].ChannelID))
		if err != nil {
			return nil, false, "", err
		}
	}
	return values, hasMore, next, nil
}

func (s *Store) CreateOAuthClient(ctx context.Context, value domain.OAuthClient) error {
	if value.ID == "" || value.SecretHash == "" || value.AppID == "" {
		return errors.New("invalid oauth client")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_clients(id, secret_hash, app_id) VALUES (?, ?, ?)`, value.ID, value.SecretHash, value.AppID)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return store.ErrAlreadyExists
	}
	return err
}

func (s *Store) GetOAuthClient(ctx context.Context, id string) (domain.OAuthClient, error) {
	var value domain.OAuthClient
	err := s.db.QueryRowContext(ctx, `SELECT id, secret_hash, app_id FROM oauth_clients WHERE id = ?`, id).Scan(&value.ID, &value.SecretHash, &value.AppID)
	if err := translateNotFound(err); err != nil {
		return domain.OAuthClient{}, err
	}
	return value, nil
}

func (s *Store) CreateOAuthCode(ctx context.Context, value domain.OAuthCode) error {
	if value.Code == "" || value.ClientID == "" || value.WorkspaceID == "" || value.UserID == "" {
		return errors.New("invalid oauth code")
	}
	scopes, err := json.Marshal(domain.NormalizeScopes(value.Scopes))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_codes(code, client_id, workspace_id, user_id, scopes, redirect_uri) VALUES (?, ?, ?, ?, ?, ?)`, value.Code, value.ClientID, value.WorkspaceID, value.UserID, string(scopes), value.RedirectURI)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return store.ErrAlreadyExists
	}
	return err
}

func (s *Store) ExchangeOAuthCode(ctx context.Context, clientID, secret, code, redirect, accessToken string, token domain.OAuthToken) (domain.OAuthToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OAuthToken{}, err
	}
	defer tx.Rollback()
	var appID domain.AppID
	if err := tx.QueryRowContext(ctx, `SELECT app_id FROM oauth_clients WHERE id = ? AND secret_hash = ?`, clientID, domain.HashToken(secret)).Scan(&appID); err != nil {
		return domain.OAuthToken{}, translateNotFound(err)
	}
	var grant domain.OAuthCode
	var scopes string
	if err := tx.QueryRowContext(ctx, `SELECT code, client_id, workspace_id, user_id, scopes, redirect_uri FROM oauth_codes WHERE code = ? AND client_id = ? AND redirect_uri = ?`, code, clientID, redirect).Scan(&grant.Code, &grant.ClientID, &grant.WorkspaceID, &grant.UserID, &scopes, &grant.RedirectURI); err != nil {
		return domain.OAuthToken{}, translateNotFound(err)
	}
	if err := json.Unmarshal([]byte(scopes), &grant.Scopes); err != nil {
		return domain.OAuthToken{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM oauth_codes WHERE code = ? AND client_id = ? AND redirect_uri = ?`, code, clientID, redirect)
	if err != nil {
		return domain.OAuthToken{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.OAuthToken{}, err
	}
	if changed != 1 {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(token_hash, workspace_id, user_id, scopes, revoked) VALUES (?, ?, ?, ?, 0)`, domain.HashToken(accessToken), grant.WorkspaceID, grant.UserID, strings.Join(domain.NormalizeScopes(grant.Scopes), " ")); err != nil {
		return domain.OAuthToken{}, err
	}
	token.AccessToken = accessToken
	token.AppID = appID
	token.ClientID = clientID
	token.WorkspaceID = grant.WorkspaceID
	token.UserID = grant.UserID
	token.Scopes = grant.Scopes
	if err := tx.Commit(); err != nil {
		return domain.OAuthToken{}, err
	}
	return token, nil
}

func (s *Store) CreateRTMConnection(ctx context.Context, value domain.RTMConnection) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.ExpiresAt.IsZero() {
		return errors.New("invalid RTM connection")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO rtm_connections(id, workspace_id, user_id, expires_at) VALUES (?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.UserID, value.ExpiresAt.UTC().UnixNano())
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return store.ErrAlreadyExists
	}
	return err
}

func (s *Store) ConsumeRTMConnection(ctx context.Context, id string) (domain.RTMConnection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RTMConnection{}, err
	}
	defer tx.Rollback()
	var value domain.RTMConnection
	var expiresAt int64
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, expires_at FROM rtm_connections WHERE id = ?`, id).Scan(&value.ID, &value.WorkspaceID, &value.UserID, &expiresAt); err != nil {
		return domain.RTMConnection{}, translateNotFound(err)
	}
	value.ExpiresAt = time.Unix(0, expiresAt).UTC()
	result, err := tx.ExecContext(ctx, `DELETE FROM rtm_connections WHERE id = ?`, id)
	if err != nil {
		return domain.RTMConnection{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.RTMConnection{}, err
	}
	if changed != 1 || !value.ExpiresAt.After(time.Now().UTC()) {
		if changed == 1 {
			if err := tx.Commit(); err != nil {
				return domain.RTMConnection{}, err
			}
		}
		return domain.RTMConnection{}, store.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return domain.RTMConnection{}, err
	}
	return value, nil
}

func (s *Store) CreateSocketModeConnection(ctx context.Context, value domain.SocketModeConnection) error {
	if value.ID == "" || value.AppID == "" || !value.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("invalid Socket Mode connection")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM socket_mode_connections WHERE app_id = ? AND consumed_at > 0 AND expires_at > ?`, value.AppID, time.Now().UTC().UnixNano()).Scan(&active); err != nil {
		return err
	}
	if active >= domain.SocketModeConnectionLimit {
		return store.ErrSocketModeConnectionLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO socket_mode_connections(id, app_id, expires_at) VALUES (?, ?, ?)`, value.ID, value.AppID, value.ExpiresAt.UTC().UnixNano())
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return store.ErrAlreadyExists
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConsumeSocketModeConnection(ctx context.Context, id string) (domain.SocketModeConnection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SocketModeConnection{}, err
	}
	defer tx.Rollback()
	var value domain.SocketModeConnection
	var expiresAt, consumedAt int64
	if err := tx.QueryRowContext(ctx, `SELECT id, app_id, expires_at, consumed_at FROM socket_mode_connections WHERE id = ?`, id).Scan(&value.ID, &value.AppID, &expiresAt, &consumedAt); err != nil {
		return domain.SocketModeConnection{}, translateNotFound(err)
	}
	value.ExpiresAt = time.Unix(0, expiresAt).UTC()
	if consumedAt != 0 || !value.ExpiresAt.After(time.Now().UTC()) {
		return domain.SocketModeConnection{}, store.ErrNotFound
	}
	result, err := tx.ExecContext(ctx, `UPDATE socket_mode_connections SET consumed_at = ? WHERE id = ? AND consumed_at = 0`, time.Now().UTC().UnixNano(), id)
	if err != nil {
		return domain.SocketModeConnection{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.SocketModeConnection{}, err
	}
	if changed != 1 {
		return domain.SocketModeConnection{}, store.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return domain.SocketModeConnection{}, err
	}
	return value, nil
}

func (s *Store) RenewSocketModeConnection(ctx context.Context, id string, expiresAt time.Time) error {
	if !expiresAt.After(time.Now().UTC()) {
		return errors.New("invalid Socket Mode connection renewal")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE socket_mode_connections SET expires_at = ? WHERE id = ? AND consumed_at > 0`, expiresAt.UTC().UnixNano(), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) ReleaseSocketModeConnection(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM socket_mode_connections WHERE id = ? AND consumed_at > 0`, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) CountSocketModeConnections(ctx context.Context, appID domain.AppID) (int, error) {
	if appID == "" {
		return 0, store.ErrInvalidAppApproval
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM socket_mode_connections WHERE app_id = ? AND consumed_at > 0 AND expires_at > ?`, appID, time.Now().UTC().UnixNano()).Scan(&count)
	return count, err
}

func (s *Store) RecordSocketModeResponse(ctx context.Context, value domain.SocketModeResponse) error {
	if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" || strings.TrimSpace(value.Payload) == "" || value.ReceivedAt.IsZero() {
		return errors.New("invalid Socket Mode response")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO socket_mode_responses(app_id, envelope_id, payload, received_at) VALUES (?, ?, ?, ?) ON CONFLICT(app_id, envelope_id) DO NOTHING`, value.AppID, value.EnvelopeID, value.Payload, value.ReceivedAt.UTC().UnixNano())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	var payload string
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM socket_mode_responses WHERE app_id = ? AND envelope_id = ?`, value.AppID, value.EnvelopeID).Scan(&payload); err != nil {
		return translateNotFound(err)
	}
	if payload != value.Payload {
		return store.ErrConflict
	}
	return nil
}

func (s *Store) ClaimSocketModeResponses(ctx context.Context, appID domain.AppID, owner string, limit int, lease time.Duration) ([]domain.SocketModeResponse, error) {
	if appID == "" || strings.TrimSpace(owner) == "" || limit < 1 || limit > 1000 || lease <= 0 {
		return nil, errors.New("invalid Socket Mode response lease")
	}
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT app_id, envelope_id, payload, received_at, lease_owner, lease_expires_at, acknowledged_at FROM socket_mode_responses WHERE app_id = ? AND acknowledged_at = 0 AND lease_expires_at <= ? ORDER BY received_at, envelope_id LIMIT ?`, appID, now.UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []domain.SocketModeResponse
	for rows.Next() {
		var value domain.SocketModeResponse
		var receivedAt, leaseExpiresAt, acknowledgedAt int64
		if err := rows.Scan(&value.AppID, &value.EnvelopeID, &value.Payload, &receivedAt, &value.LeaseOwner, &leaseExpiresAt, &acknowledgedAt); err != nil {
			return nil, err
		}
		value.ReceivedAt = time.Unix(0, receivedAt).UTC()
		value.LeaseExpiresAt = time.Unix(0, leaseExpiresAt).UTC()
		if acknowledgedAt != 0 {
			value.AcknowledgedAt = time.Unix(0, acknowledgedAt).UTC()
		}
		candidates = append(candidates, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	expiresAt := now.Add(lease)
	claimed := make([]domain.SocketModeResponse, 0, len(candidates))
	for _, value := range candidates {
		result, err := s.db.ExecContext(ctx, `UPDATE socket_mode_responses SET lease_owner = ?, lease_expires_at = ? WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND lease_expires_at <= ?`, owner, expiresAt.UnixNano(), value.AppID, value.EnvelopeID, now.UnixNano())
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed == 1 {
			value.LeaseOwner = owner
			value.LeaseExpiresAt = expiresAt
			claimed = append(claimed, value)
		}
	}
	return claimed, nil
}

func (s *Store) AckSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse) error {
	if strings.TrimSpace(owner) == "" || len(values) == 0 {
		return errors.New("invalid Socket Mode response acknowledgement")
	}
	now := time.Now().UTC().UnixNano()
	for _, value := range values {
		if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" {
			return errors.New("invalid Socket Mode response key")
		}
		result, err := s.db.ExecContext(ctx, `UPDATE socket_mode_responses SET acknowledged_at = ?, lease_owner = '', lease_expires_at = 0 WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND lease_owner = ? AND lease_expires_at > ?`, now, value.AppID, value.EnvelopeID, owner, now)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 1 {
			continue
		}
		var acknowledgedAt int64
		if err := s.db.QueryRowContext(ctx, `SELECT acknowledged_at FROM socket_mode_responses WHERE app_id = ? AND envelope_id = ?`, value.AppID, value.EnvelopeID).Scan(&acknowledgedAt); err != nil {
			return translateNotFound(err)
		}
		if acknowledgedAt == 0 {
			return store.ErrConflict
		}
	}
	return nil
}

func (s *Store) RenewSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, lease time.Duration) error {
	if strings.TrimSpace(owner) == "" || len(values) == 0 || lease <= 0 {
		return errors.New("invalid Socket Mode response renewal")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	expiresAt := now.Add(lease).UnixNano()
	for _, value := range values {
		if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" {
			return errors.New("invalid Socket Mode response key")
		}
		result, err := tx.ExecContext(ctx, `UPDATE socket_mode_responses SET lease_expires_at = ? WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND lease_owner = ? AND lease_expires_at > ?`, expiresAt, value.AppID, value.EnvelopeID, owner, now.UnixNano())
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 1 {
			continue
		}
		var acknowledgedAt int64
		if err := tx.QueryRowContext(ctx, `SELECT acknowledged_at FROM socket_mode_responses WHERE app_id = ? AND envelope_id = ?`, value.AppID, value.EnvelopeID).Scan(&acknowledgedAt); err != nil {
			return translateNotFound(err)
		}
		return store.ErrConflict
	}
	return tx.Commit()
}

func (s *Store) ReleaseSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, retryAt time.Time) error {
	if strings.TrimSpace(owner) == "" || len(values) == 0 || retryAt.IsZero() {
		return errors.New("invalid Socket Mode response release")
	}
	for _, value := range values {
		if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" {
			return errors.New("invalid Socket Mode response key")
		}
		result, err := s.db.ExecContext(ctx, `UPDATE socket_mode_responses SET lease_owner = '', lease_expires_at = ? WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND lease_owner = ?`, retryAt.UTC().UnixNano(), value.AppID, value.EnvelopeID, owner)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 1 {
			continue
		}
		var acknowledgedAt int64
		if err := s.db.QueryRowContext(ctx, `SELECT acknowledged_at FROM socket_mode_responses WHERE app_id = ? AND envelope_id = ?`, value.AppID, value.EnvelopeID).Scan(&acknowledgedAt); err != nil {
			return translateNotFound(err)
		}
		if acknowledgedAt != 0 {
			continue
		}
		return store.ErrConflict
	}
	return nil
}

func (s *Store) GetSocketModeCursor(ctx context.Context, appID domain.AppID) (uint64, error) {
	if appID == "" {
		return 0, store.ErrInvalidAppApproval
	}
	var cursor uint64
	err := s.db.QueryRowContext(ctx, `SELECT sequence FROM socket_mode_cursors WHERE app_id = ?`, appID).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return cursor, err
}

func (s *Store) SetSocketModeCursor(ctx context.Context, appID domain.AppID, cursor uint64) error {
	if appID == "" {
		return store.ErrInvalidAppApproval
	}
	current, err := s.GetSocketModeCursor(ctx, appID)
	if err != nil {
		return err
	}
	if cursor < current {
		return store.ErrConflict
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO socket_mode_cursors(app_id, sequence) VALUES (?, ?) ON CONFLICT(app_id) DO UPDATE SET sequence = excluded.sequence WHERE socket_mode_cursors.sequence <= excluded.sequence`, appID, cursor)
	return err
}

func (s *Store) SetConversationPrivate(ctx context.Context, conversation domain.ConversationID, event events.Event) (domain.Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET is_private = 1 WHERE id = ? AND is_private = 0 AND is_direct = 0 AND is_group_direct = 0`, conversation)
	if err != nil {
		return domain.Conversation{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Conversation{}, err
	}
	if changed != 1 {
		return domain.Conversation{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) SELECT ?, workspace_id, ?, ?, ?, 0, '', '', '' FROM conversations WHERE id = ?`, event.ID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano), conversation); err != nil {
		return domain.Conversation{}, err
	}
	var value domain.Conversation
	var private, direct, groupDirect, archived int
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE id = ?`, conversation).Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &archived, &private, &direct, &groupDirect); err != nil {
		return domain.Conversation{}, err
	}
	value.Archived, value.IsPrivate, value.IsDirect, value.IsGroupDirect = archived != 0, private != 0, direct != 0, groupDirect != 0
	if err := tx.Commit(); err != nil {
		return domain.Conversation{}, err
	}
	return value, nil
}

func (s *Store) GetConversationPrefs(ctx context.Context, conversation domain.ConversationID) (domain.ConversationPrefs, error) {
	if _, err := s.GetConversation(ctx, conversation); err != nil {
		return domain.ConversationPrefs{}, err
	}
	var canThreadTypes, canThreadUsers, whoCanPostTypes, whoCanPostUsers string
	err := s.db.QueryRowContext(ctx, `SELECT can_thread_types, can_thread_users, who_can_post_types, who_can_post_users FROM conversation_prefs WHERE conversation_id = ?`, conversation).Scan(&canThreadTypes, &canThreadUsers, &whoCanPostTypes, &whoCanPostUsers)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationPrefs{ConversationID: conversation, CanThread: domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{}, Users: []domain.UserID{}}, WhoCanPost: domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{}, Users: []domain.UserID{}}}, nil
	}
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	canTypes, err := decodePreferenceTypeList(canThreadTypes)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	canUsers, err := decodeUserIDList(canThreadUsers)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	postTypes, err := decodePreferenceTypeList(whoCanPostTypes)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	postUsers, err := decodeUserIDList(whoCanPostUsers)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	return domain.ConversationPrefs{ConversationID: conversation, CanThread: domain.ConversationPreferenceList{Types: canTypes, Users: canUsers}, WhoCanPost: domain.ConversationPreferenceList{Types: postTypes, Users: postUsers}}, nil
}

func (s *Store) SetConversationPrefs(ctx context.Context, conversation domain.ConversationID, value domain.ConversationPrefs, event events.Event) (domain.ConversationPrefs, error) {
	if _, err := s.GetConversation(ctx, conversation); err != nil {
		return domain.ConversationPrefs{}, err
	}
	canTypes, err := json.Marshal(value.CanThread.Types)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	canUsers, err := json.Marshal(userIDStrings(value.CanThread.Users))
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	postTypes, err := json.Marshal(value.WhoCanPost.Types)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	postUsers, err := json.Marshal(userIDStrings(value.WhoCanPost.Users))
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationPrefs{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_prefs(conversation_id, can_thread_types, can_thread_users, who_can_post_types, who_can_post_users) VALUES (?, ?, ?, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET can_thread_types = excluded.can_thread_types, can_thread_users = excluded.can_thread_users, who_can_post_types = excluded.who_can_post_types, who_can_post_users = excluded.who_can_post_users`, conversation, canTypes, canUsers, postTypes, postUsers); err != nil {
		return domain.ConversationPrefs{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.ConversationPrefs{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationPrefs{}, err
	}
	value.ConversationID = conversation
	return value, nil
}

func decodeStringList(value string) ([]string, error) {
	var result []string
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return nil, err
	}
	if result == nil {
		return []string{}, nil
	}
	return result, nil
}

func decodePreferenceTypeList(value string) ([]domain.ConversationPreferenceType, error) {
	values, err := decodeStringList(value)
	if err != nil {
		return nil, err
	}
	result := make([]domain.ConversationPreferenceType, 0, len(values))
	for _, value := range values {
		result = append(result, domain.ConversationPreferenceType(value))
	}
	return result, nil
}

func decodeUserIDList(value string) ([]domain.UserID, error) {
	values, err := decodeStringList(value)
	if err != nil {
		return nil, err
	}
	result := make([]domain.UserID, 0, len(values))
	for _, value := range values {
		result = append(result, domain.UserID(value))
	}
	return result, nil
}

func userIDStrings(values []domain.UserID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}

func (s *Store) AddEmoji(ctx context.Context, value domain.CustomEmoji, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO custom_emoji(workspace_id, name, url, alias_for) VALUES (?, ?, ?, ?)`, value.WorkspaceID, value.Name, value.URL, value.AliasFor); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListEmojis(ctx context.Context, workspace domain.WorkspaceID) ([]domain.CustomEmoji, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace_id, name, url, alias_for FROM custom_emoji WHERE workspace_id = ? ORDER BY name`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.CustomEmoji, 0)
	for rows.Next() {
		var value domain.CustomEmoji
		if err := rows.Scan(&value.WorkspaceID, &value.Name, &value.URL, &value.AliasFor); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) RemoveEmoji(ctx context.Context, workspace domain.WorkspaceID, name string, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM custom_emoji WHERE workspace_id = ? AND name = ?`, workspace, name)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RenameEmoji(ctx context.Context, workspace domain.WorkspaceID, oldName, newName string, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE custom_emoji SET name = ? WHERE workspace_id = ? AND name = ? AND NOT EXISTS (SELECT 1 FROM custom_emoji WHERE workspace_id = ? AND name = ?)`, newName, workspace, oldName, workspace, newName)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		var exists int
		if lookupErr := tx.QueryRowContext(ctx, `SELECT 1 FROM custom_emoji WHERE workspace_id = ? AND name = ?`, workspace, oldName).Scan(&exists); lookupErr != nil {
			return store.ErrNotFound
		}
		return store.ErrAlreadyExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddConversationMember(ctx context.Context, conversation domain.ConversationID, user domain.UserID, event events.Event) error {
	var private int
	if err := s.db.QueryRowContext(ctx, `SELECT is_private FROM conversations WHERE id = ?`, conversation).Scan(&private); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	} else if private != 0 {
		return store.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) VALUES (?, ?) ON CONFLICT(conversation_id, user_id) DO NOTHING`, conversation, user)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) InviteConversationMembers(ctx context.Context, conversation domain.ConversationID, users []domain.UserID, event events.Event) error {
	if len(users) == 0 {
		return errors.New("conversation invite requires at least one user")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workspace domain.WorkspaceID
	var private int
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id, is_private FROM conversations WHERE id = ?`, conversation).Scan(&workspace, &private); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	} else if private != 0 {
		return store.ErrNotFound
	}
	for _, user := range users {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) SELECT ?, id FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0 ON CONFLICT(conversation_id, user_id) DO NOTHING`, conversation, user, workspace); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_members WHERE conversation_id = ? AND user_id = ?`, conversation, user).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return store.ErrNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveConversationMember(ctx context.Context, conversation domain.ConversationID, user domain.UserID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM conversation_members WHERE conversation_id = ? AND user_id = ?`, conversation, user)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) SELECT ?, workspace_id, ?, ?, ?, 0, '', '', '' FROM conversations WHERE id = ?`, event.ID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano), conversation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetReadCursor(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) (domain.ReadCursor, error) {
	var cursor domain.ReadCursor
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, user_id, conversation_id, last_read, updated_at FROM read_cursors WHERE workspace_id = ? AND user_id = ? AND conversation_id = ?`, workspace, user, conversation).Scan(&cursor.WorkspaceID, &cursor.UserID, &cursor.Conversation, &cursor.LastRead, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReadCursor{}, store.ErrNotFound
	}
	if err != nil {
		return domain.ReadCursor{}, err
	}
	cursor.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return cursor, err
}

func (s *Store) SetReadCursor(ctx context.Context, cursor domain.ReadCursor, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO read_cursors(workspace_id, user_id, conversation_id, last_read, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, conversation_id) DO UPDATE SET last_read = excluded.last_read, updated_at = excluded.updated_at`, cursor.WorkspaceID, cursor.UserID, cursor.Conversation, cursor.LastRead, cursor.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListConversations(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.ConversationListRequest) (domain.ConversationPage, error) {
	if request.Limit <= 0 {
		return domain.ConversationPage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ConversationPage{}, err
	}
	memberUser := user
	if request.MemberUserID != "" {
		memberUser = request.MemberUserID
	}
	query := `SELECT c.id, c.workspace_id, c.name, c.topic, c.purpose, c.archived, c.is_private, c.is_direct, c.is_group_direct FROM conversations c WHERE c.workspace_id = ? AND ((c.is_private = 0 AND c.is_direct = 0 AND c.is_group_direct = 0) OR (EXISTS (SELECT 1 FROM conversation_members subject_member WHERE subject_member.conversation_id = c.id AND subject_member.user_id = ?) AND EXISTS (SELECT 1 FROM conversation_members viewer_member WHERE viewer_member.conversation_id = c.id AND viewer_member.user_id = ?)))`
	args := []any{workspace, memberUser, user}
	if request.ExcludeArchived {
		query += ` AND c.archived = 0`
	}
	if len(request.Types) > 0 {
		clauses := make([]string, 0, len(request.Types))
		for _, typeValue := range request.Types {
			switch typeValue {
			case domain.ConversationTypePublic:
				clauses = append(clauses, `(c.is_private = 0 AND c.is_direct = 0 AND c.is_group_direct = 0)`)
			case domain.ConversationTypePrivate:
				clauses = append(clauses, `(c.is_private = 1 AND c.is_direct = 0 AND c.is_group_direct = 0)`)
			case domain.ConversationTypeIM:
				clauses = append(clauses, `c.is_direct = 1`)
			case domain.ConversationTypeMPIM:
				clauses = append(clauses, `c.is_group_direct = 1`)
			default:
				return domain.ConversationPage{}, errors.New("invalid conversation type")
			}
		}
		query += ` AND (` + strings.Join(clauses, ` OR `) + `)`
	}
	if after != "" {
		query += ` AND c.id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY c.id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ConversationPage{}, err
	}
	defer rows.Close()
	conversations := make([]domain.Conversation, 0, request.Limit)
	for rows.Next() {
		var conversation domain.Conversation
		var private, direct, groupDirect, archived int
		if err := rows.Scan(&conversation.ID, &conversation.WorkspaceID, &conversation.Name, &conversation.Topic, &conversation.Purpose, &archived, &private, &direct, &groupDirect); err != nil {
			return domain.ConversationPage{}, err
		}
		conversation.Archived = archived != 0
		conversation.IsPrivate = private != 0
		conversation.IsDirect = direct != 0
		conversation.IsGroupDirect = groupDirect != 0
		conversation.UnreadCount, err = s.unreadCount(ctx, workspace, user, conversation.ID)
		if err != nil {
			return domain.ConversationPage{}, err
		}
		conversations = append(conversations, conversation)
	}
	if err := rows.Err(); err != nil {
		return domain.ConversationPage{}, err
	}
	hasMore := len(conversations) > request.Limit
	if hasMore {
		conversations = conversations[:request.Limit]
	}
	page := domain.ConversationPage{Conversations: conversations, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(conversations[len(conversations)-1].ID))
	}
	return page, err
}

func (s *Store) SearchConversations(ctx context.Context, workspace domain.WorkspaceID, query string, request domain.PageRequest) (domain.ConversationPage, error) {
	if request.Limit <= 0 {
		return domain.ConversationPage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ConversationPage{}, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return domain.ConversationPage{}, errors.New("conversation search query is required")
	}
	sqlQuery := `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE workspace_id = ? AND (lower(name) LIKE ? OR lower(topic) LIKE ? OR lower(purpose) LIKE ?)`
	pattern := "%" + query + "%"
	args := []any{workspace, pattern, pattern, pattern}
	if after != "" {
		sqlQuery += ` AND id > ?`
		args = append(args, after)
	}
	sqlQuery += ` ORDER BY id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return domain.ConversationPage{}, err
	}
	defer rows.Close()
	values := make([]domain.Conversation, 0, request.Limit+1)
	for rows.Next() {
		var value domain.Conversation
		var archived, private, direct, groupDirect int
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &archived, &private, &direct, &groupDirect); err != nil {
			return domain.ConversationPage{}, err
		}
		value.Archived, value.IsPrivate, value.IsDirect, value.IsGroupDirect = archived != 0, private != 0, direct != 0, groupDirect != 0
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.ConversationPage{}, err
	}
	page := domain.ConversationPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
	}
	page.Conversations = values
	if page.HasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func (s *Store) unreadCount(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) (int, error) {
	lastRead := ""
	cursor, err := s.GetReadCursor(ctx, workspace, user, conversation)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	if err == nil {
		parsed, parseErr := domain.ParseMessageTimestamp(cursor.LastRead)
		if parseErr != nil {
			return 0, parseErr
		}
		lastRead = parsed.UTC().Format(time.RFC3339Nano)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE workspace_id = ? AND conversation = ? AND deleted = 0 AND created_at > ?`, workspace, conversation, lastRead).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) IsConversationMember(ctx context.Context, conversation domain.ConversationID, user domain.UserID) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM conversation_members WHERE conversation_id = ? AND user_id = ?`, conversation, user).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return exists == 1, err
}

func (s *Store) CreateMessage(ctx context.Context, message domain.Message, event events.Event, idempotencyKey string) error {
	unfurls, err := encodeUnfurls(message.Unfurls)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if idempotencyKey != "" {
		result, err := tx.ExecContext(ctx, `INSERT INTO idempotency (workspace_id, user_id, idempotency_key, message_id, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, idempotency_key) DO NOTHING`, message.WorkspaceID, message.AuthorID, idempotencyKey, message.ID, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return store.ErrIdempotencyConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO messages (id, workspace_id, conversation, author_id, text, thread_timestamp, created_at, deleted, unfurls) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`, message.ID, message.WorkspaceID, message.Conversation, message.AuthorID, message.Text, message.ThreadTimestamp, message.CreatedAt.UTC().Format(time.RFC3339Nano), unfurls); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) GetMessageByCreatedAt(ctx context.Context, conversation domain.ConversationID, createdAt time.Time) (domain.Message, error) {
	var message domain.Message
	var deleted int
	var stored string
	var unfurls string
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, conversation, author_id, text, thread_timestamp, created_at, deleted, unfurls FROM messages WHERE conversation = ? AND created_at = ? ORDER BY id LIMIT 1`, conversation, createdAt.UTC().Format(time.RFC3339Nano)).Scan(&message.ID, &message.WorkspaceID, &message.Conversation, &message.AuthorID, &message.Text, &message.ThreadTimestamp, &stored, &deleted, &unfurls)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Message{}, err
	}
	message.CreatedAt, err = time.Parse(time.RFC3339Nano, stored)
	if err != nil {
		return domain.Message{}, err
	}
	message.Deleted = deleted != 0
	message.Unfurls, err = decodeUnfurls(unfurls)
	if err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func (s *Store) UpdateMessage(ctx context.Context, message domain.Message, event events.Event) error {
	unfurls, err := encodeUnfurls(message.Unfurls)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	deleted := 0
	if message.Deleted {
		deleted = 1
	}
	result, err := tx.ExecContext(ctx, `UPDATE messages SET text = ?, deleted = ?, unfurls = ? WHERE id = ? AND workspace_id = ? AND conversation = ?`, message.Text, deleted, unfurls, message.ID, message.WorkspaceID, message.Conversation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddReaction(ctx context.Context, reaction domain.Reaction, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO reactions(message_id, name, user_id, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(message_id, name, user_id) DO NOTHING`, reaction.Message, reaction.Name, reaction.UserID, reaction.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrAlreadyExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveReaction(ctx context.Context, reaction domain.Reaction, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM reactions WHERE message_id = ? AND name = ? AND user_id = ?`, reaction.Message, reaction.Name, reaction.UserID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListReactions(ctx context.Context, message domain.MessageID, request domain.PageRequest) ([]domain.Reaction, domain.Cursor, bool, error) {
	if request.Limit <= 0 {
		return nil, "", false, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, "", false, err
	}
	query := `SELECT message_id, name, user_id, created_at FROM reactions WHERE message_id = ?`
	args := []any{message}
	if after != "" {
		separator := strings.IndexByte(after, 0)
		if separator < 1 || separator == len(after)-1 {
			return nil, "", false, domain.ErrInvalidCursor
		}
		query += ` AND (name > ? OR (name = ? AND user_id > ?))`
		name, user := after[:separator], after[separator+1:]
		args = append(args, name, name, user)
	}
	query += ` ORDER BY name, user_id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	values := make([]domain.Reaction, 0, request.Limit+1)
	for rows.Next() {
		var reaction domain.Reaction
		var created string
		if err := rows.Scan(&reaction.Message, &reaction.Name, &reaction.UserID, &created); err != nil {
			return nil, "", false, err
		}
		reaction.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, "", false, err
		}
		values = append(values, reaction)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		next, err = domain.NewListCursor(domain.ReactionKey(values[len(values)-1]))
		if err != nil {
			return nil, "", false, err
		}
	}
	return values, next, hasMore, nil
}

func (s *Store) ListUserReactions(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.UserReactionPage, error) {
	if request.Limit <= 0 {
		return domain.UserReactionPage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserReactionPage{}, err
	}
	query := `SELECT m.conversation, m.id, m.workspace_id, m.author_id, m.text, m.thread_timestamp, m.created_at, m.deleted, r.name, r.user_id, r.created_at FROM reactions r JOIN messages m ON m.id = r.message_id JOIN conversations c ON c.id = m.conversation WHERE m.workspace_id = ? AND r.user_id = ? AND (c.is_private = 0 OR EXISTS (SELECT 1 FROM conversation_members cm WHERE cm.conversation_id = m.conversation AND cm.user_id = ?))`
	args := []any{workspace, user, user}
	if after != "" {
		parts := strings.Split(after, "\x00")
		if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
			return domain.UserReactionPage{}, errors.New("invalid user reaction cursor")
		}
		query += ` AND (m.created_at > ? OR (m.created_at = ? AND r.message_id > ?) OR (m.created_at = ? AND r.message_id = ? AND r.name > ?) OR (m.created_at = ? AND r.message_id = ? AND r.name = ? AND r.user_id > ?))`
		args = append(args, parts[0], parts[0], parts[1], parts[0], parts[1], parts[2], parts[0], parts[1], parts[2], parts[3])
	}
	query += ` ORDER BY m.created_at, r.message_id, r.name, r.user_id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.UserReactionPage{}, err
	}
	defer rows.Close()
	items := make([]domain.UserReaction, 0, request.Limit)
	for rows.Next() {
		var item domain.UserReaction
		var deleted int
		var messageCreated, reactionCreated string
		if err := rows.Scan(&item.Conversation, &item.Message.ID, &item.Message.WorkspaceID, &item.Message.AuthorID, &item.Message.Text, &item.Message.ThreadTimestamp, &messageCreated, &deleted, &item.Reaction.Name, &item.Reaction.UserID, &reactionCreated); err != nil {
			return domain.UserReactionPage{}, err
		}
		item.Message.Deleted = deleted != 0
		item.Message.CreatedAt, err = time.Parse(time.RFC3339Nano, messageCreated)
		if err != nil {
			return domain.UserReactionPage{}, err
		}
		item.Reaction.Message = item.Message.ID
		item.Reaction.CreatedAt, err = time.Parse(time.RFC3339Nano, reactionCreated)
		if err != nil {
			return domain.UserReactionPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.UserReactionPage{}, err
	}
	hasMore := len(items) > request.Limit
	if hasMore {
		items = items[:request.Limit]
	}
	page := domain.UserReactionPage{Items: items, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(userReactionCursorKey(items[len(items)-1]))
	}
	return page, err
}

func userReactionCursorKey(value domain.UserReaction) string {
	return value.Message.CreatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + string(value.Message.ID) + "\x00" + value.Reaction.Name + "\x00" + string(value.Reaction.UserID)
}

func (s *Store) AddPin(ctx context.Context, pin domain.Pin, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO pins(message_id, user_id, created_at) VALUES (?, ?, ?) ON CONFLICT(message_id, user_id) DO NOTHING`, pin.Message, pin.UserID, pin.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrAlreadyExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemovePin(ctx context.Context, pin domain.Pin, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM pins WHERE message_id = ? AND user_id = ?`, pin.Message, pin.UserID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListPins(ctx context.Context, conversation domain.ConversationID, request domain.PageRequest) ([]domain.Pin, domain.Cursor, bool, error) {
	if request.Limit <= 0 {
		return nil, "", false, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, "", false, err
	}
	query := `SELECT p.message_id, p.user_id, p.created_at FROM pins p JOIN messages m ON m.id = p.message_id WHERE m.conversation = ?`
	args := []any{conversation}
	if after != "" {
		separator := strings.IndexByte(after, 0)
		if separator < 1 || separator == len(after)-1 {
			return nil, "", false, domain.ErrInvalidCursor
		}
		query += ` AND (p.message_id > ? OR (p.message_id = ? AND p.user_id > ?))`
		message, user := after[:separator], after[separator+1:]
		args = append(args, message, message, user)
	}
	query += ` ORDER BY p.message_id, p.user_id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	values := make([]domain.Pin, 0, request.Limit+1)
	for rows.Next() {
		var pin domain.Pin
		var created string
		if err := rows.Scan(&pin.Message, &pin.UserID, &created); err != nil {
			return nil, "", false, err
		}
		pin.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, "", false, err
		}
		values = append(values, pin)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		key := string(values[len(values)-1].Message) + "\x00" + string(values[len(values)-1].UserID)
		next, err = domain.NewListCursor(key)
		if err != nil {
			return nil, "", false, err
		}
	}
	return values, next, hasMore, nil
}

func (s *Store) AddStar(ctx context.Context, star domain.Star, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO stars(user_id, message_id, created_at) VALUES (?, ?, ?) ON CONFLICT(user_id, message_id) DO NOTHING`, star.UserID, star.Message.ID, star.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrAlreadyExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveStar(ctx context.Context, star domain.Star, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM stars WHERE user_id = ? AND message_id = ?`, star.UserID, star.Message.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListStars(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) ([]domain.Star, domain.Cursor, bool, error) {
	if request.Limit <= 0 {
		return nil, "", false, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, "", false, err
	}
	query := `SELECT s.created_at, m.id, m.workspace_id, m.conversation, m.author_id, m.text, m.thread_timestamp, m.created_at, m.deleted FROM stars s JOIN messages m ON m.id = s.message_id WHERE s.user_id = ? AND m.workspace_id = ? AND m.deleted = 0`
	args := []any{user, workspace}
	if after != "" {
		separator := strings.IndexByte(after, 0)
		if separator < 1 || separator == len(after)-1 {
			return nil, "", false, domain.ErrInvalidCursor
		}
		created, messageID := after[:separator], after[separator+1:]
		query += ` AND (s.created_at > ? OR (s.created_at = ? AND s.message_id > ?))`
		args = append(args, created, created, messageID)
	}
	query += ` ORDER BY s.created_at, s.message_id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	values := make([]domain.Star, 0, request.Limit+1)
	for rows.Next() {
		var star domain.Star
		var starCreated, messageCreated string
		var deleted int
		if err := rows.Scan(&starCreated, &star.Message.ID, &star.Message.WorkspaceID, &star.Message.Conversation, &star.Message.AuthorID, &star.Message.Text, &star.Message.ThreadTimestamp, &messageCreated, &deleted); err != nil {
			return nil, "", false, err
		}
		star.UserID = user
		star.Conversation = star.Message.Conversation
		star.Message.Deleted = deleted != 0
		star.CreatedAt, err = time.Parse(time.RFC3339Nano, starCreated)
		if err != nil {
			return nil, "", false, err
		}
		star.Message.CreatedAt, err = time.Parse(time.RFC3339Nano, messageCreated)
		if err != nil {
			return nil, "", false, err
		}
		values = append(values, star)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	var next domain.Cursor
	if hasMore {
		next, err = domain.NewListCursor(values[len(values)-1].CreatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + string(values[len(values)-1].Message.ID))
		if err != nil {
			return nil, "", false, err
		}
	}
	return values, next, hasMore, nil
}

func (s *Store) CreateReminder(ctx context.Context, reminder domain.Reminder, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO reminders(id, workspace_id, creator_id, user_id, text, due_at, complete_at, recurring) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, reminder.ID, reminder.WorkspaceID, reminder.Creator, reminder.User, reminder.Text, reminder.Time.Unix(), unixSeconds(reminder.CompleteAt), boolInt(reminder.Recurring)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetReminder(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ReminderID) (domain.Reminder, error) {
	var reminder domain.Reminder
	var due, complete, recurring int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, creator_id, user_id, text, due_at, complete_at, recurring FROM reminders WHERE id = ? AND workspace_id = ? AND user_id = ?`, id, workspace, user).Scan(&reminder.ID, &reminder.WorkspaceID, &reminder.Creator, &reminder.User, &reminder.Text, &due, &complete, &recurring)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Reminder{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Reminder{}, err
	}
	reminder.Time = time.Unix(due, 0).UTC()
	if complete != 0 {
		reminder.CompleteAt = time.Unix(complete, 0).UTC()
	}
	reminder.Recurring = recurring != 0
	return reminder, nil
}

func (s *Store) ListReminders(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.ReminderPage, error) {
	if request.Limit <= 0 {
		return domain.ReminderPage{}, errors.New("reminder list limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ReminderPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, creator_id, user_id, text, due_at, complete_at, recurring FROM reminders WHERE workspace_id = ? AND user_id = ? AND id > ? ORDER BY id LIMIT ?`, workspace, user, after, request.Limit+1)
	if err != nil {
		return domain.ReminderPage{}, err
	}
	defer rows.Close()
	values := make([]domain.Reminder, 0, request.Limit+1)
	for rows.Next() {
		var reminder domain.Reminder
		var due, complete, recurring int64
		if err := rows.Scan(&reminder.ID, &reminder.WorkspaceID, &reminder.Creator, &reminder.User, &reminder.Text, &due, &complete, &recurring); err != nil {
			return domain.ReminderPage{}, err
		}
		reminder.Time = time.Unix(due, 0).UTC()
		if complete != 0 {
			reminder.CompleteAt = time.Unix(complete, 0).UTC()
		}
		reminder.Recurring = recurring != 0
		values = append(values, reminder)
	}
	if err := rows.Err(); err != nil {
		return domain.ReminderPage{}, err
	}
	page := domain.ReminderPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.ReminderPage{}, err
		}
	}
	page.Reminders = values
	return page, nil
}

func (s *Store) CompleteReminder(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ReminderID, completed time.Time, event events.Event) error {
	return s.updateReminderCompletion(ctx, workspace, user, id, completed, event)
}

func (s *Store) updateReminderCompletion(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ReminderID, completed time.Time, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE reminders SET complete_at = ? WHERE id = ? AND workspace_id = ? AND user_id = ?`, unixSeconds(completed), id, workspace, user)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteReminder(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ReminderID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM reminders WHERE id = ? AND workspace_id = ? AND user_id = ?`, id, workspace, user)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateScheduledMessage(ctx context.Context, value domain.ScheduledMessage, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_messages(id, workspace_id, channel_id, author_id, text, post_at, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', 0, 0)`, value.ID, value.WorkspaceID, value.Channel, value.Author, value.Text, value.PostAt.Unix(), value.CreatedAt.Unix()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func scheduledUnixSecondCeil(value time.Time) int64 {
	seconds := value.UTC().Unix()
	if value.UTC().Nanosecond() != 0 {
		return seconds + 1
	}
	return seconds
}

func (s *Store) ListScheduledMessages(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, channel domain.ConversationID, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	if request.Limit <= 0 {
		return domain.ScheduledMessagePage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	query := `SELECT id, workspace_id, channel_id, author_id, text, post_at, created_at FROM scheduled_messages WHERE workspace_id = ? AND author_id = ? AND delivered = 0`
	args := []any{workspace, user}
	if channel != "" {
		query += ` AND channel_id = ?`
		args = append(args, channel)
	}
	if after != "" {
		query += ` AND id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	defer rows.Close()
	items := make([]domain.ScheduledMessage, 0, request.Limit+1)
	for rows.Next() {
		var value domain.ScheduledMessage
		var postAt, createdAt int64
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Channel, &value.Author, &value.Text, &postAt, &createdAt); err != nil {
			return domain.ScheduledMessagePage{}, err
		}
		value.PostAt = time.Unix(postAt, 0).UTC()
		value.CreatedAt = time.Unix(createdAt, 0).UTC()
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	page := domain.ScheduledMessagePage{Items: items, HasMore: len(items) > request.Limit}
	if page.HasMore {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(page.Items[len(page.Items)-1].ID))
	}
	return page, err
}

func (s *Store) EarliestScheduledMessage(ctx context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	var postAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(CASE WHEN next_attempt_at > post_at THEN next_attempt_at ELSE post_at END) FROM scheduled_messages WHERE workspace_id = ? AND delivered = 0`, workspace).Scan(&postAt); err != nil {
		return time.Time{}, err
	}
	if !postAt.Valid || postAt.Int64 == 0 {
		return time.Time{}, nil
	}
	return time.Unix(postAt.Int64, 0).UTC(), nil
}

func (s *Store) DeleteScheduledMessage(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, channel domain.ConversationID, id domain.ScheduledMessageID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `DELETE FROM scheduled_messages WHERE id = ? AND workspace_id = ? AND author_id = ? AND channel_id = ? AND (lease_until = 0 OR lease_until <= ?)`, id, workspace, user, channel, now.Unix())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimScheduledMessages(ctx context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration) ([]domain.ScheduledMessage, error) {
	if owner == "" || limit <= 0 || lease <= 0 {
		return nil, errors.New("scheduled claim requires owner, positive limit, and lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	rows, err := tx.QueryContext(ctx, `SELECT id, workspace_id, channel_id, author_id, text, post_at, created_at FROM scheduled_messages WHERE workspace_id = ? AND delivered = 0 AND post_at <= ? AND (lease_until = 0 OR lease_until <= ?) AND (next_attempt_at = 0 OR next_attempt_at <= ?) ORDER BY id LIMIT ?`, workspace, now.Unix(), now.Unix(), now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.ScheduledMessage, 0, limit)
	for rows.Next() {
		var value domain.ScheduledMessage
		var postAt, createdAt int64
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Channel, &value.Author, &value.Text, &postAt, &createdAt); err != nil {
			return nil, err
		}
		value.PostAt = time.Unix(postAt, 0).UTC()
		value.CreatedAt = time.Unix(createdAt, 0).UTC()
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	expires := scheduledUnixSecondCeil(now.Add(lease))
	for _, value := range values {
		result, err := tx.ExecContext(ctx, `UPDATE scheduled_messages SET lease_owner = ?, lease_until = ? WHERE id = ? AND delivered = 0 AND (lease_until = 0 OR lease_until <= ?)`, owner, expires, value.ID, now.Unix())
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return nil, err
			}
			return nil, store.ErrLeaseConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

func (s *Store) RenewScheduledMessage(ctx context.Context, owner string, id domain.ScheduledMessageID, lease time.Duration) error {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE scheduled_messages SET lease_until = ? WHERE id = ? AND lease_owner = ? AND delivered = 0 AND lease_until > ?`, scheduledUnixSecondCeil(now.Add(lease)), id, owner, now.Unix())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrLeaseConflict
	}
	return nil
}

func (s *Store) MarkScheduledMessageDelivered(ctx context.Context, owner string, id domain.ScheduledMessageID) error {
	result, err := s.db.ExecContext(ctx, `UPDATE scheduled_messages SET delivered = 1, lease_owner = '', lease_until = 0, next_attempt_at = 0 WHERE id = ? AND lease_owner = ? AND delivered = 0 AND lease_until > ?`, id, owner, time.Now().UTC().Unix())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrLeaseConflict
	}
	return nil
}

func (s *Store) ReleaseScheduledMessage(ctx context.Context, owner string, id domain.ScheduledMessageID, next time.Time) error {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE scheduled_messages SET lease_owner = '', lease_until = 0, next_attempt_at = ? WHERE id = ? AND lease_owner = ? AND delivered = 0 AND lease_until > ?`, scheduledUnixSecondCeil(next), id, owner, now.Unix())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrLeaseConflict
	}
	return nil
}

func (s *Store) CreateUserGroup(ctx context.Context, value domain.UserGroup, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO user_groups(id, workspace_id, name, handle, description, creator_id, updated_by, created_at, updated_at, deleted_at, enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`, value.ID, value.WorkspaceID, value.Name, value.Handle, value.Description, value.Creator, value.UpdatedBy, value.CreatedAt.Unix(), value.UpdatedAt.Unix(), boolInt(value.Enabled))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetUserGroup(ctx context.Context, workspace domain.WorkspaceID, id domain.UserGroupID) (domain.UserGroup, error) {
	var value domain.UserGroup
	var created, updated, deleted int64
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, name, handle, description, creator_id, updated_by, created_at, updated_at, deleted_at, enabled FROM user_groups WHERE workspace_id = ? AND id = ?`, workspace, id).Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Handle, &value.Description, &value.Creator, &value.UpdatedBy, &created, &updated, &deleted, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UserGroup{}, store.ErrNotFound
	}
	if err != nil {
		return domain.UserGroup{}, err
	}
	value.CreatedAt = time.Unix(created, 0).UTC()
	value.UpdatedAt = time.Unix(updated, 0).UTC()
	if deleted != 0 {
		value.DeletedAt = time.Unix(deleted, 0).UTC()
	}
	value.Enabled = enabled != 0
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM user_group_users WHERE group_id = ? ORDER BY user_id`, id)
	if err != nil {
		return domain.UserGroup{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID domain.UserID
		if err := rows.Scan(&userID); err != nil {
			return domain.UserGroup{}, err
		}
		value.Users = append(value.Users, userID)
	}
	if err := rows.Err(); err != nil {
		return domain.UserGroup{}, err
	}
	channelRows, err := s.db.QueryContext(ctx, `SELECT c.id FROM user_group_channels g JOIN conversations c ON c.id = g.conversation_id WHERE g.group_id = ? ORDER BY c.id`, id)
	if err != nil {
		return domain.UserGroup{}, err
	}
	defer channelRows.Close()
	for channelRows.Next() {
		var channel domain.ConversationID
		if err := channelRows.Scan(&channel); err != nil {
			return domain.UserGroup{}, err
		}
		value.Channels = append(value.Channels, channel)
	}
	if err := channelRows.Err(); err != nil {
		return domain.UserGroup{}, err
	}
	return value, nil
}

func (s *Store) ListUserGroups(ctx context.Context, workspace domain.WorkspaceID, includeDisabled bool, request domain.PageRequest) (domain.UserGroupPage, error) {
	if request.Limit <= 0 {
		return domain.UserGroupPage{}, errors.New("user group list limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserGroupPage{}, err
	}
	query := `SELECT id, workspace_id, name, handle, description, creator_id, updated_by, created_at, updated_at, deleted_at, enabled FROM user_groups WHERE workspace_id = ?`
	args := []any{workspace}
	if !includeDisabled {
		query += ` AND enabled = 1`
	}
	query += ` AND id > ? ORDER BY id LIMIT ?`
	args = append(args, after, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.UserGroupPage{}, err
	}
	defer rows.Close()
	values := make([]domain.UserGroup, 0, request.Limit+1)
	for rows.Next() {
		var value domain.UserGroup
		var created, updated, deleted int64
		var enabled int
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Handle, &value.Description, &value.Creator, &value.UpdatedBy, &created, &updated, &deleted, &enabled); err != nil {
			return domain.UserGroupPage{}, err
		}
		value.CreatedAt = time.Unix(created, 0).UTC()
		value.UpdatedAt = time.Unix(updated, 0).UTC()
		if deleted != 0 {
			value.DeletedAt = time.Unix(deleted, 0).UTC()
		}
		value.Enabled = enabled != 0
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.UserGroupPage{}, err
	}
	page := domain.UserGroupPage{HasMore: len(values) > request.Limit}
	if page.HasMore {
		values = values[:request.Limit]
	}
	for index := range values {
		value, err := s.GetUserGroup(ctx, workspace, values[index].ID)
		if err != nil {
			return domain.UserGroupPage{}, err
		}
		values[index].Users = value.Users
		values[index].Channels = value.Channels
	}
	if page.HasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.UserGroupPage{}, err
		}
	}
	page.Groups = values
	return page, nil
}

func (s *Store) SetUserGroupChannels(ctx context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, channels []domain.ConversationID, actor domain.UserID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM user_groups WHERE id = ? AND workspace_id = ?`, id, workspace).Scan(&exists); err != nil {
		return translateNotFound(err)
	}
	for _, channel := range channels {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ? AND workspace_id = ?`, channel, workspace).Scan(&exists); err != nil {
			return translateNotFound(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_channels WHERE group_id = ?`, id); err != nil {
		return err
	}
	for _, channel := range channels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_group_channels(group_id, conversation_id) VALUES (?, ?)`, id, channel); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_groups SET updated_by = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, actor, time.Now().UTC().Unix(), id, workspace); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateUserGroup(ctx context.Context, value domain.UserGroup, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE user_groups SET name = ?, handle = ?, description = ?, updated_by = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, value.Name, value.Handle, value.Description, value.UpdatedBy, value.UpdatedAt.Unix(), value.ID, value.WorkspaceID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetUserGroupEnabled(ctx context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, enabled bool, actor domain.UserID, event events.Event) error {
	now := time.Now().UTC()
	deleted := int64(0)
	if !enabled {
		deleted = now.Unix()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE user_groups SET enabled = ?, deleted_at = ?, updated_by = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, boolInt(enabled), deleted, actor, now.Unix(), id, workspace)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetUserGroupUsers(ctx context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, users []domain.UserID, actor domain.UserID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM user_groups WHERE id = ? AND workspace_id = ?`, id, workspace).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_users WHERE group_id = ?`, id); err != nil {
		return err
	}
	for _, userID := range users {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_group_users(group_id, user_id) SELECT ?, id FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0`, id, userID, workspace); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_groups SET updated_by = ?, updated_at = ? WHERE id = ?`, actor, time.Now().UTC().Unix(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateCall(ctx context.Context, value domain.Call, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO calls(id, workspace_id, external_unique_id, external_display_id, join_url, desktop_app_join_url, title, created_by, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.ExternalUniqueID, value.ExternalDisplayID, value.JoinURL, value.DesktopAppJoinURL, value.Title, value.CreatedBy, value.StartedAt.Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	for _, userID := range value.Participants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO call_participants(call_id, user_id) SELECT ?, id FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0`, value.ID, userID, value.WorkspaceID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetCall(ctx context.Context, workspace domain.WorkspaceID, id domain.CallID) (domain.Call, error) {
	var value domain.Call
	var started, ended int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, external_unique_id, external_display_id, join_url, desktop_app_join_url, title, created_by, started_at, ended_at, duration_seconds FROM calls WHERE workspace_id = ? AND id = ?`, workspace, id).Scan(&value.ID, &value.WorkspaceID, &value.ExternalUniqueID, &value.ExternalDisplayID, &value.JoinURL, &value.DesktopAppJoinURL, &value.Title, &value.CreatedBy, &started, &ended, &value.DurationSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Call{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Call{}, err
	}
	value.StartedAt = time.Unix(started, 0).UTC()
	if ended != 0 {
		value.EndedAt = time.Unix(ended, 0).UTC()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM call_participants WHERE call_id = ? ORDER BY user_id`, id)
	if err != nil {
		return domain.Call{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID domain.UserID
		if err := rows.Scan(&userID); err != nil {
			return domain.Call{}, err
		}
		value.Participants = append(value.Participants, userID)
	}
	if err := rows.Err(); err != nil {
		return domain.Call{}, err
	}
	return value, nil
}

func (s *Store) UpdateCall(ctx context.Context, value domain.Call, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE calls SET external_display_id = CASE WHEN ? = '' THEN external_display_id ELSE ? END, join_url = CASE WHEN ? = '' THEN join_url ELSE ? END, desktop_app_join_url = CASE WHEN ? = '' THEN desktop_app_join_url ELSE ? END, title = CASE WHEN ? = '' THEN title ELSE ? END WHERE workspace_id = ? AND id = ?`, value.ExternalDisplayID, value.ExternalDisplayID, value.JoinURL, value.JoinURL, value.DesktopAppJoinURL, value.DesktopAppJoinURL, value.Title, value.Title, value.WorkspaceID, value.ID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EndCall(ctx context.Context, workspace domain.WorkspaceID, id domain.CallID, duration int64, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE calls SET ended_at = ?, duration_seconds = ? WHERE workspace_id = ? AND id = ? AND ended_at = 0`, now, duration, workspace, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetCallParticipants(ctx context.Context, workspace domain.WorkspaceID, id domain.CallID, users []domain.UserID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM calls WHERE workspace_id = ? AND id = ?`, workspace, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM call_participants WHERE call_id = ?`, id); err != nil {
		return err
	}
	for _, userID := range users {
		if _, err := tx.ExecContext(ctx, `INSERT INTO call_participants(call_id, user_id) SELECT ?, id FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0`, id, userID, workspace); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateFile(ctx context.Context, file domain.File, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO files(id, workspace_id, uploader_id, name, title, mime_type, blob_key, size, created_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, file.ID, file.WorkspaceID, file.Uploader, file.Name, file.Title, file.MIMEType, file.BlobKey, file.Size, file.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SeedFileComment(ctx context.Context, value domain.FileComment) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO file_comments(id, file_id, workspace_id, user_id, text, created_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET file_id = excluded.file_id, workspace_id = excluded.workspace_id, user_id = excluded.user_id, text = excluded.text, created_at = excluded.created_at, deleted = excluded.deleted`, value.ID, value.File, value.WorkspaceID, value.UserID, value.Text, value.CreatedAt.UTC().Unix(), boolInt(value.Deleted))
	return err
}

func (s *Store) DeleteFileComment(ctx context.Context, workspace domain.WorkspaceID, fileID domain.FileID, commentID domain.FileCommentID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE file_comments SET deleted = 1 WHERE id = ? AND file_id = ? AND workspace_id = ? AND deleted = 0 AND EXISTS (SELECT 1 FROM files WHERE id = ? AND workspace_id = ? AND deleted = 0)`, commentID, fileID, workspace, fileID, workspace)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetFile(ctx context.Context, id domain.FileID) (domain.File, error) {
	var file domain.File
	var created string
	var deleted int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, uploader_id, name, title, mime_type, blob_key, size, created_at, deleted, public_token FROM files WHERE id = ? AND deleted = 0`, id).Scan(&file.ID, &file.WorkspaceID, &file.Uploader, &file.Name, &file.Title, &file.MIMEType, &file.BlobKey, &file.Size, &created, &deleted, &file.PublicToken)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.File{}, store.ErrNotFound
	}
	if err != nil {
		return domain.File{}, err
	}
	file.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	file.Deleted = deleted != 0
	return file, err
}

func (s *Store) DeleteFile(ctx context.Context, id domain.FileID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE files SET deleted = 1 WHERE id = ? AND deleted = 0`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ShareFilePublic(ctx context.Context, workspace domain.WorkspaceID, id domain.FileID, token string, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE files SET public_token = ? WHERE id = ? AND workspace_id = ? AND deleted = 0`, token, id, workspace)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.ErrAlreadyExists
		}
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeFilePublic(ctx context.Context, workspace domain.WorkspaceID, id domain.FileID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE files SET public_token = '' WHERE id = ? AND workspace_id = ? AND deleted = 0`, id, workspace)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetPublicFile(ctx context.Context, token string) (domain.File, error) {
	var file domain.File
	var created string
	var deleted int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, uploader_id, name, title, mime_type, blob_key, size, created_at, deleted, public_token FROM files WHERE public_token = ? AND public_token <> '' AND deleted = 0`, token).Scan(&file.ID, &file.WorkspaceID, &file.Uploader, &file.Name, &file.Title, &file.MIMEType, &file.BlobKey, &file.Size, &created, &deleted, &file.PublicToken)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.File{}, store.ErrNotFound
	}
	if err != nil {
		return domain.File{}, err
	}
	file.Deleted = deleted != 0
	file.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return file, err
}

func (s *Store) ListFiles(ctx context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.FilePage, error) {
	if request.Limit <= 0 {
		return domain.FilePage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.FilePage{}, err
	}
	query := `SELECT id, workspace_id, uploader_id, name, title, mime_type, blob_key, size, created_at, deleted, public_token FROM files WHERE workspace_id = ? AND deleted = 0`
	args := []any{workspace}
	if after != "" {
		query += ` AND id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.FilePage{}, err
	}
	defer rows.Close()
	values := make([]domain.File, 0, request.Limit+1)
	for rows.Next() {
		var file domain.File
		var created string
		var deleted int
		if err := rows.Scan(&file.ID, &file.WorkspaceID, &file.Uploader, &file.Name, &file.Title, &file.MIMEType, &file.BlobKey, &file.Size, &created, &deleted, &file.PublicToken); err != nil {
			return domain.FilePage{}, err
		}
		file.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return domain.FilePage{}, err
		}
		file.Deleted = deleted != 0
		values = append(values, file)
	}
	if err := rows.Err(); err != nil {
		return domain.FilePage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.FilePage{Files: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.FilePage{}, err
		}
	}
	return page, nil
}

func (s *Store) WalkBlobReferences(ctx context.Context, workspace domain.WorkspaceID, visit func(string) error) error {
	if visit == nil {
		return errors.New("blob reference visitor is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT blob_key FROM files WHERE workspace_id = ? AND deleted = 0`, workspace)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var reference string
		if err := rows.Scan(&reference); err != nil {
			return err
		}
		if reference == "" {
			return errors.New("database contains an empty blob reference")
		}
		if err := visit(reference); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id, image_24 FROM users WHERE workspace_id = ? AND deleted = 0 AND image_24 <> ''`, workspace)
	if err != nil {
		return err
	}
	defer rows.Close()
	prefix := "/users/" + string(workspace) + "/"
	for rows.Next() {
		var userID, imageURL string
		if err := rows.Scan(&userID, &imageURL); err != nil {
			return err
		}
		if !strings.HasPrefix(imageURL, prefix) {
			return fmt.Errorf("user %q has an invalid photo URL", userID)
		}
		photo := strings.TrimPrefix(imageURL, prefix)
		parts := strings.Split(photo, "/photo/")
		if len(parts) != 2 || parts[0] != userID || parts[1] == "" || strings.Contains(parts[1], "/") {
			return fmt.Errorf("user %q has an invalid photo URL", userID)
		}
		if err := visit(string(workspace) + "/users/" + userID + "/" + parts[1]); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) AddRemoteFile(ctx context.Context, value domain.RemoteFile, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO remote_files(id, workspace_id, external_id, title, file_type, external_url, preview_image, indexable_contents, created_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, value.ID, value.WorkspaceID, value.ExternalID, value.Title, value.FileType, value.ExternalURL, value.PreviewImage, value.IndexableContents, value.CreatedAt.UTC().Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return store.ErrAlreadyExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetRemoteFile(ctx context.Context, workspace domain.WorkspaceID, lookup domain.RemoteFileLookup) (domain.RemoteFile, error) {
	query := `SELECT id, workspace_id, external_id, title, file_type, external_url, preview_image, indexable_contents, created_at, deleted FROM remote_files WHERE workspace_id = ? AND deleted = 0 AND id = ?`
	args := []any{workspace, lookup.ID}
	if lookup.ID == "" {
		query = `SELECT id, workspace_id, external_id, title, file_type, external_url, preview_image, indexable_contents, created_at, deleted FROM remote_files WHERE workspace_id = ? AND deleted = 0 AND external_id = ?`
		args = []any{workspace, lookup.ExternalID}
	}
	var value domain.RemoteFile
	var created int64
	var deleted int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&value.ID, &value.WorkspaceID, &value.ExternalID, &value.Title, &value.FileType, &value.ExternalURL, &value.PreviewImage, &value.IndexableContents, &created, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RemoteFile{}, store.ErrNotFound
	}
	if err != nil {
		return domain.RemoteFile{}, err
	}
	value.CreatedAt = time.Unix(created, 0).UTC()
	value.Deleted = deleted != 0
	value.SharedChannels, err = s.remoteFileShares(ctx, s.db, value.ID)
	if err != nil {
		return domain.RemoteFile{}, err
	}
	return value, nil
}

func (s *Store) remoteFileShares(ctx context.Context, db queryExecutor, id domain.FileID) ([]domain.ConversationID, error) {
	rows, err := db.QueryContext(ctx, `SELECT conversation_id FROM remote_file_shares WHERE remote_file_id = ? ORDER BY conversation_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.ConversationID, 0)
	for rows.Next() {
		var channel domain.ConversationID
		if err := rows.Scan(&channel); err != nil {
			return nil, err
		}
		values = append(values, channel)
	}
	return values, rows.Err()
}

func (s *Store) ListRemoteFiles(ctx context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.RemoteFilePage, error) {
	if request.Limit <= 0 {
		return domain.RemoteFilePage{}, errors.New("page limit must be positive")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.RemoteFilePage{}, err
	}
	query := `SELECT id, workspace_id, external_id, title, file_type, external_url, preview_image, indexable_contents, created_at, deleted FROM remote_files WHERE workspace_id = ? AND deleted = 0`
	args := []any{workspace}
	if after != "" {
		query += ` AND id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.RemoteFilePage{}, err
	}
	defer rows.Close()
	values := make([]domain.RemoteFile, 0, request.Limit+1)
	for rows.Next() {
		var value domain.RemoteFile
		var created int64
		var deleted int
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.ExternalID, &value.Title, &value.FileType, &value.ExternalURL, &value.PreviewImage, &value.IndexableContents, &created, &deleted); err != nil {
			return domain.RemoteFilePage{}, err
		}
		value.CreatedAt = time.Unix(created, 0).UTC()
		value.Deleted = deleted != 0
		value.SharedChannels, err = s.remoteFileShares(ctx, s.db, value.ID)
		if err != nil {
			return domain.RemoteFilePage{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.RemoteFilePage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.RemoteFilePage{Files: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.RemoteFilePage{}, err
		}
	}
	return page, nil
}

func (s *Store) RemoveRemoteFile(ctx context.Context, workspace domain.WorkspaceID, lookup domain.RemoteFileLookup, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `UPDATE remote_files SET deleted = 1 WHERE workspace_id = ? AND id = ? AND deleted = 0`
	args := []any{workspace, lookup.ID}
	if lookup.ID == "" {
		query = `UPDATE remote_files SET deleted = 1 WHERE workspace_id = ? AND external_id = ? AND deleted = 0`
		args = []any{workspace, lookup.ExternalID}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetRemoteFileShares(ctx context.Context, workspace domain.WorkspaceID, lookup domain.RemoteFileLookup, channels []domain.ConversationID, event events.Event) (domain.RemoteFile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RemoteFile{}, err
	}
	defer tx.Rollback()
	query := `SELECT id FROM remote_files WHERE workspace_id = ? AND id = ? AND deleted = 0`
	args := []any{workspace, lookup.ID}
	if lookup.ID == "" {
		query = `SELECT id FROM remote_files WHERE workspace_id = ? AND external_id = ? AND deleted = 0`
		args = []any{workspace, lookup.ExternalID}
	}
	var id domain.FileID
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
		return domain.RemoteFile{}, translateNotFound(err)
	}
	for _, channel := range channels {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ? AND workspace_id = ? AND is_direct = 0 AND is_group_direct = 0`, channel, workspace).Scan(&exists); err != nil {
			return domain.RemoteFile{}, translateNotFound(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM remote_file_shares WHERE remote_file_id = ?`, id); err != nil {
		return domain.RemoteFile{}, err
	}
	for _, channel := range channels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO remote_file_shares(remote_file_id, conversation_id) VALUES (?, ?)`, id, channel); err != nil {
			return domain.RemoteFile{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.RemoteFile{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RemoteFile{}, err
	}
	return s.GetRemoteFile(ctx, workspace, domain.RemoteFileLookup{ID: id})
}

func (s *Store) UpdateRemoteFile(ctx context.Context, workspace domain.WorkspaceID, value domain.RemoteFile, event events.Event) (domain.RemoteFile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RemoteFile{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE remote_files SET title = ?, file_type = ?, external_url = ?, preview_image = ?, indexable_contents = ? WHERE id = ? AND workspace_id = ? AND deleted = 0`, value.Title, value.FileType, value.ExternalURL, value.PreviewImage, value.IndexableContents, value.ID, workspace)
	if err != nil {
		return domain.RemoteFile{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.RemoteFile{}, err
	}
	if changed != 1 {
		return domain.RemoteFile{}, store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (id, workspace_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, 0, '', '', '')`, event.ID, event.WorkspaceID, event.Topic, event.Payload, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return domain.RemoteFile{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RemoteFile{}, err
	}
	return s.GetRemoteFile(ctx, workspace, domain.RemoteFileLookup{ID: value.ID})
}

func (s *Store) GetMessage(ctx context.Context, id domain.MessageID) (domain.Message, error) {
	var message domain.Message
	var deleted int
	var created string
	var unfurls string
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, conversation, author_id, text, thread_timestamp, created_at, deleted, unfurls FROM messages WHERE id = ?`, id).Scan(&message.ID, &message.WorkspaceID, &message.Conversation, &message.AuthorID, &message.Text, &message.ThreadTimestamp, &created, &deleted, &unfurls)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Message{}, err
	}
	message.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return domain.Message{}, err
	}
	message.Deleted = deleted != 0
	message.Unfurls, err = decodeUnfurls(unfurls)
	if err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func (s *Store) GetIdempotentMessage(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, key string) (domain.Message, error) {
	var id domain.MessageID
	err := s.db.QueryRowContext(ctx, `SELECT message_id FROM idempotency WHERE workspace_id = ? AND user_id = ? AND idempotency_key = ?`, workspace, user, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Message{}, err
	}
	return s.GetMessage(ctx, id)
}

func (s *Store) ClaimEvents(ctx context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	return s.claimEvents(ctx, workspace, "", owner, limit, lease)
}

func (s *Store) ClaimEventsForTopic(ctx context.Context, workspace domain.WorkspaceID, topic, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	if topic == "" {
		return nil, errors.New("topic is required")
	}
	return s.claimEvents(ctx, workspace, topic, owner, limit, lease)
}

func (s *Store) claimEvents(ctx context.Context, workspace domain.WorkspaceID, topic, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	if workspace == "" || owner == "" || limit <= 0 || lease <= 0 {
		return nil, errors.New("workspace, owner, positive limit, and positive lease are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	expiresText := now.Add(lease).Format(time.RFC3339Nano)
	query := `SELECT sequence, id, workspace_id, topic, payload, created_at FROM outbox WHERE workspace_id = ? AND delivered = 0`
	args := []any{workspace}
	if topic == "" {
		query += ` AND topic <> ?`
		args = append(args, events.FileBlobDeleteTopic)
	} else {
		query += ` AND topic = ?`
		args = append(args, topic)
	}
	query += ` AND (lease_until = '' OR lease_until <= ?) AND (next_attempt_at = '' OR next_attempt_at <= ?) ORDER BY sequence LIMIT ?`
	args = append(args, nowText, nowText, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		sequence uint64
		event    events.Event
		created  string
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.sequence, &item.event.ID, &item.event.WorkspaceID, &item.event.Topic, &item.event.Payload, &item.created); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	result := make([]events.Record, 0, len(candidates))
	for _, item := range candidates {
		updated, err := tx.ExecContext(ctx, `UPDATE outbox SET lease_owner = ?, lease_until = ? WHERE sequence = ? AND delivered = 0 AND (lease_until = '' OR lease_until <= ?) AND (next_attempt_at = '' OR next_attempt_at <= ?)`, owner, expiresText, item.sequence, nowText, nowText)
		if err != nil {
			return nil, err
		}
		count, err := updated.RowsAffected()
		if err != nil || count != 1 {
			return nil, store.ErrLeaseConflict
		}
		item.event.CreatedAt, err = time.Parse(time.RFC3339Nano, item.created)
		if err != nil {
			return nil, err
		}
		result = append(result, events.Record{Sequence: item.sequence, Event: item.event})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) AckEvents(ctx context.Context, owner string, sequences []uint64) error {
	if owner == "" || len(sequences) == 0 {
		return errors.New("owner and event sequences are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	for _, sequence := range sequences {
		updated, err := tx.ExecContext(ctx, `UPDATE outbox SET delivered = 1, lease_owner = '', lease_until = '', next_attempt_at = '' WHERE sequence = ? AND delivered = 0 AND lease_owner = ? AND lease_until > ?`, sequence, owner, nowText)
		if err != nil {
			return err
		}
		count, err := updated.RowsAffected()
		if err != nil || count != 1 {
			return store.ErrLeaseConflict
		}
	}
	return tx.Commit()
}

func (s *Store) RenewEvents(ctx context.Context, owner string, sequences []uint64, lease time.Duration) error {
	if owner == "" || len(sequences) == 0 || lease <= 0 {
		return errors.New("owner, event sequences, and positive lease are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	expiresText := now.Add(lease).Format(time.RFC3339Nano)
	for _, sequence := range sequences {
		updated, err := tx.ExecContext(ctx, `UPDATE outbox SET lease_until = ? WHERE sequence = ? AND delivered = 0 AND lease_owner = ? AND lease_until > ?`, expiresText, sequence, owner, nowText)
		if err != nil {
			return err
		}
		count, err := updated.RowsAffected()
		if err != nil || count != 1 {
			return store.ErrLeaseConflict
		}
	}
	return tx.Commit()
}

func (s *Store) ReleaseEvents(ctx context.Context, owner string, sequences []uint64, retryAt time.Time) error {
	if owner == "" || len(sequences) == 0 || !retryAt.After(time.Now().UTC()) {
		return errors.New("owner, event sequences, and a future retry time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	retryText := retryAt.UTC().Format(time.RFC3339Nano)
	for _, sequence := range sequences {
		updated, err := tx.ExecContext(ctx, `UPDATE outbox SET lease_owner = '', lease_until = '', next_attempt_at = ? WHERE sequence = ? AND delivered = 0 AND lease_owner = ? AND lease_until > ?`, retryText, sequence, owner, nowText)
		if err != nil {
			return err
		}
		count, err := updated.RowsAffected()
		if err != nil || count != 1 {
			return store.ErrLeaseConflict
		}
	}
	return tx.Commit()
}

func (s *Store) ListEventsAfter(ctx context.Context, workspace domain.WorkspaceID, after uint64, limit int) ([]events.Record, error) {
	if limit <= 0 {
		return nil, errors.New("event limit must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, id, workspace_id, actor_id, topic, payload, created_at FROM outbox WHERE workspace_id = ? AND sequence > ? AND topic <> ? ORDER BY sequence LIMIT ?`, workspace, after, events.FileBlobDeleteTopic, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]events.Record, 0, limit)
	for rows.Next() {
		var sequence uint64
		var event events.Event
		var created string
		if err := rows.Scan(&sequence, &event.ID, &event.WorkspaceID, &event.ActorID, &event.Topic, &event.Payload, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, events.Record{Sequence: sequence, Event: event})
	}
	return result, rows.Err()
}

func (s *Store) ListAppEventsAfter(ctx context.Context, appID domain.AppID, after uint64, limit int) ([]events.Record, error) {
	if appID == "" || limit <= 0 {
		return nil, errors.New("app ID and positive event limit are required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.sequence, o.id, o.workspace_id, o.actor_id, o.topic, o.payload, o.created_at FROM outbox o JOIN app_installations i ON i.workspace_id = o.workspace_id WHERE i.app_id = ? AND i.enabled = 1 AND o.sequence > ? AND o.topic <> ? ORDER BY o.sequence LIMIT ?`, appID, after, events.FileBlobDeleteTopic, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]events.Record, 0, limit)
	for rows.Next() {
		var record events.Record
		var created string
		if err := rows.Scan(&record.Sequence, &record.Event.ID, &record.Event.WorkspaceID, &record.Event.ActorID, &record.Event.Topic, &record.Event.Payload, &created); err != nil {
			return nil, err
		}
		record.Event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) ListMessages(ctx context.Context, conversation domain.ConversationID, request domain.PageRequest) (domain.MessagePage, error) {
	if request.Limit <= 0 {
		return domain.MessagePage{}, errors.New("page limit must be positive")
	}
	query := `SELECT id, workspace_id, conversation, author_id, text, thread_timestamp, created_at, deleted, unfurls FROM messages WHERE conversation = ?`
	args := []any{conversation}
	if request.Cursor != "" {
		createdAt, id, err := domain.DecodeMessageCursor(request.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
		query += ` AND (created_at > ? OR (created_at = ? AND id > ?))`
		created := createdAt.UTC().Format(time.RFC3339Nano)
		args = append(args, created, created, id)
	}
	query += ` ORDER BY created_at, id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.MessagePage{}, err
	}
	defer rows.Close()
	var values []domain.Message
	for rows.Next() {
		var value domain.Message
		var created, unfurls string
		var deleted int
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Conversation, &value.AuthorID, &value.Text, &value.ThreadTimestamp, &created, &deleted, &unfurls); err != nil {
			return domain.MessagePage{}, err
		}
		value.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return domain.MessagePage{}, err
		}
		value.Deleted = deleted != 0
		value.Unfurls, err = decodeUnfurls(unfurls)
		if err != nil {
			return domain.MessagePage{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.MessagePage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.MessagePage{Messages: values, HasMore: hasMore}
	if hasMore {
		cursor, err := domain.NewMessageCursor(values[len(values)-1])
		if err != nil {
			return domain.MessagePage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

func (s *Store) SearchMessages(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, query string, request domain.PageRequest) (domain.MessagePage, error) {
	if request.Limit <= 0 {
		return domain.MessagePage{}, errors.New("page limit must be positive")
	}
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return domain.MessagePage{}, errors.New("search query must not be empty")
	}
	querySQL := `SELECT m.id, m.workspace_id, m.conversation, m.author_id, m.text, m.thread_timestamp, m.created_at, m.deleted, m.unfurls FROM messages m JOIN conversations c ON c.id = m.conversation WHERE m.workspace_id = ? AND m.deleted = 0 AND (c.is_private = 0 OR EXISTS (SELECT 1 FROM conversation_members cm WHERE cm.conversation_id = m.conversation AND cm.user_id = ?))`
	args := []any{workspace, user}
	for _, term := range terms {
		querySQL += ` AND LOWER(m.text) LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLikeTerm(term)+"%")
	}
	if request.Cursor != "" {
		createdAt, id, err := domain.DecodeMessageCursor(request.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
		created := createdAt.UTC().Format(time.RFC3339Nano)
		querySQL += ` AND (m.created_at > ? OR (m.created_at = ? AND m.id > ?))`
		args = append(args, created, created, id)
	}
	querySQL += ` ORDER BY m.created_at, m.id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return domain.MessagePage{}, err
	}
	defer rows.Close()
	values := make([]domain.Message, 0, request.Limit+1)
	for rows.Next() {
		var message domain.Message
		var created, unfurls string
		var deleted int
		if err := rows.Scan(&message.ID, &message.WorkspaceID, &message.Conversation, &message.AuthorID, &message.Text, &message.ThreadTimestamp, &created, &deleted, &unfurls); err != nil {
			return domain.MessagePage{}, err
		}
		message.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return domain.MessagePage{}, err
		}
		message.Deleted = deleted != 0
		message.Unfurls, err = decodeUnfurls(unfurls)
		if err != nil {
			return domain.MessagePage{}, err
		}
		values = append(values, message)
	}
	if err := rows.Err(); err != nil {
		return domain.MessagePage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.MessagePage{Messages: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewMessageCursor(values[len(values)-1])
		if err != nil {
			return domain.MessagePage{}, err
		}
	}
	return page, nil
}

func escapeLikeTerm(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, `%`, `\%`)
	return strings.ReplaceAll(term, `_`, `\_`)
}

func (s *Store) ListThreadMessages(ctx context.Context, conversation domain.ConversationID, timestamp domain.MessageTimestamp, request domain.PageRequest) (domain.MessagePage, error) {
	if request.Limit <= 0 {
		return domain.MessagePage{}, errors.New("page limit must be positive")
	}
	createdAt, err := domain.ParseMessageTimestamp(timestamp)
	if err != nil {
		return domain.MessagePage{}, err
	}
	query := `SELECT id, workspace_id, conversation, author_id, text, thread_timestamp, created_at, deleted, unfurls FROM messages WHERE conversation = ? AND ((created_at = ? AND thread_timestamp = '') OR thread_timestamp = ?)`
	created := createdAt.UTC().Format(time.RFC3339Nano)
	args := []any{conversation, created, string(timestamp)}
	if request.Cursor != "" {
		cursorTime, id, cursorRoot, err := domain.DecodeMessageCursorWithRoot(request.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
		cursorCreated := cursorTime.UTC().Format(time.RFC3339Nano)
		if cursorRoot {
			query += ` AND (thread_timestamp <> '' OR (thread_timestamp = '' AND (created_at > ? OR (created_at = ? AND id > ?))))`
		} else {
			query += ` AND thread_timestamp <> '' AND (created_at > ? OR (created_at = ? AND id > ?))`
		}
		args = append(args, cursorCreated, cursorCreated, id)
	}
	query += ` ORDER BY CASE WHEN thread_timestamp = '' THEN 0 ELSE 1 END, created_at, id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.MessagePage{}, err
	}
	defer rows.Close()
	values := make([]domain.Message, 0, request.Limit+1)
	for rows.Next() {
		var value domain.Message
		var stored string
		var deleted int
		var unfurls string
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Conversation, &value.AuthorID, &value.Text, &value.ThreadTimestamp, &stored, &deleted, &unfurls); err != nil {
			return domain.MessagePage{}, err
		}
		value.CreatedAt, err = time.Parse(time.RFC3339Nano, stored)
		if err != nil {
			return domain.MessagePage{}, err
		}
		value.Deleted = deleted != 0
		value.Unfurls, err = decodeUnfurls(unfurls)
		if err != nil {
			return domain.MessagePage{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.MessagePage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.MessagePage{Messages: values, HasMore: hasMore}
	if hasMore {
		cursor, err := domain.NewMessageCursor(values[len(values)-1])
		if err != nil {
			return domain.MessagePage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

func translateNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}
