package sqlstore

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS schema_migration_lock (id INTEGER PRIMARY KEY, acquired INTEGER NOT NULL DEFAULT 0);
-- schema_backfills is the durable progress of the column-wide data rewrites that
-- deliberately run outside the migration transaction. See backfill.go: without a
-- persisted cursor a crash at 80 % of a five-million-row rewrite discarded all of
-- it, and the whole rewrite held the migration fence while it ran.
CREATE TABLE IF NOT EXISTS schema_backfills (name TEXT PRIMARY KEY, cursor TEXT NOT NULL DEFAULT '', done INTEGER NOT NULL DEFAULT 0, rejected INTEGER NOT NULL DEFAULT 0);
-- schema_migration_notices is what a data migration records instead of aborting.
-- An upgrade that stops for ever on one unparseable value, or on two accounts an
-- older release admitted, is an upgrade with no operator remedy.
CREATE TABLE IF NOT EXISTS schema_migration_notices (kind TEXT NOT NULL, subject TEXT NOT NULL, detail TEXT NOT NULL, observed_at TEXT NOT NULL, PRIMARY KEY (kind, subject));
CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, domain TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', discoverability TEXT NOT NULL DEFAULT 'open', icon_url TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS users (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 email TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, real_name TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
 status_text TEXT NOT NULL DEFAULT '', status_emoji TEXT NOT NULL DEFAULT '', status_expiration INTEGER NOT NULL DEFAULT 0, active_scheduled_status_id TEXT NOT NULL DEFAULT '',
 image_24 TEXT NOT NULL DEFAULT '', image_32 TEXT NOT NULL DEFAULT '', image_48 TEXT NOT NULL DEFAULT '',
 image_72 TEXT NOT NULL DEFAULT '', image_192 TEXT NOT NULL DEFAULT '', image_512 TEXT NOT NULL DEFAULT '', image_1024 TEXT NOT NULL DEFAULT '',
 deleted INTEGER NOT NULL DEFAULT 0, presence TEXT NOT NULL DEFAULT 'auto'
);
CREATE TABLE IF NOT EXISTS user_expirations (user_id TEXT PRIMARY KEY REFERENCES users(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), expiration_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS scheduled_statuses (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 status_text TEXT NOT NULL DEFAULT '', status_emoji TEXT NOT NULL DEFAULT '',
 starts_at INTEGER NOT NULL, ends_at INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS scheduled_statuses_owner_start ON scheduled_statuses(workspace_id, user_id, starts_at, id);
CREATE INDEX IF NOT EXISTS scheduled_statuses_due ON scheduled_statuses(starts_at, id);
CREATE TABLE IF NOT EXISTS workspace_members (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 role TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1,
 restricted INTEGER NOT NULL DEFAULT 0, ultra_restricted INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE IF NOT EXISTS tokens (
 token_hash TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), app_id TEXT NOT NULL DEFAULT '', bot_id TEXT NOT NULL DEFAULT '',
 scopes TEXT NOT NULL, token_type TEXT NOT NULL DEFAULT 'user',
 expires_at INTEGER NOT NULL DEFAULT 0, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
 session_hash TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL DEFAULT '', expires_at TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0,
	oidc_provider TEXT NOT NULL DEFAULT '', oidc_id_token TEXT NOT NULL DEFAULT '', oidc_subject TEXT NOT NULL DEFAULT '', oidc_sid TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS oidc_logout_tokens (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, token_id TEXT NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY (workspace_id, provider, token_id));
CREATE TABLE IF NOT EXISTS auth_methods (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, enabled INTEGER NOT NULL, PRIMARY KEY(workspace_id, provider));
CREATE TABLE IF NOT EXISTS external_identities (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, subject TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY(workspace_id, provider, subject));
CREATE TABLE IF NOT EXISTS conversations (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 name TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '', purpose TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0, is_private INTEGER NOT NULL DEFAULT 0, is_direct INTEGER NOT NULL DEFAULT 0, is_group_direct INTEGER NOT NULL DEFAULT 0, direct_key TEXT NOT NULL DEFAULT '',
 name_folded TEXT NOT NULL DEFAULT '', topic_folded TEXT NOT NULL DEFAULT '', purpose_folded TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS workspace_default_channels (workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (workspace_id, conversation_id));
CREATE TABLE IF NOT EXISTS conversation_teams (conversation_id TEXT NOT NULL REFERENCES conversations(id), team_id TEXT NOT NULL REFERENCES workspaces(id), org_channel INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (conversation_id, team_id));
CREATE TABLE IF NOT EXISTS slack_apps (
 id TEXT PRIMARY KEY, development_workspace_id TEXT NOT NULL REFERENCES workspaces(id), owner_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', client_id TEXT NOT NULL UNIQUE, signing_secret_hash TEXT NOT NULL,
 signing_secret_ciphertext TEXT NOT NULL DEFAULT '',
 verification_token_hash TEXT NOT NULL, verification_token_ciphertext TEXT NOT NULL DEFAULT '',
 manifest_version INTEGER NOT NULL, distribution TEXT NOT NULL DEFAULT 'private',
 socket_mode_enabled INTEGER NOT NULL DEFAULT 0, token_rotation_enabled INTEGER NOT NULL DEFAULT 0,
 deleted INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS slack_apps_owner ON slack_apps(development_workspace_id, owner_id, deleted, name, id);
CREATE TABLE IF NOT EXISTS app_manifest_revisions (
 app_id TEXT NOT NULL REFERENCES slack_apps(id), version INTEGER NOT NULL, manifest TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL, PRIMARY KEY (app_id, version)
);
CREATE TABLE IF NOT EXISTS app_datastore_items (
 app_id TEXT NOT NULL REFERENCES slack_apps(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 datastore TEXT NOT NULL, item_id TEXT NOT NULL, item TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY (app_id, workspace_id, datastore, item_id)
);
CREATE INDEX IF NOT EXISTS app_datastore_items_lookup ON app_datastore_items(app_id, workspace_id, datastore, item_id);
CREATE TABLE IF NOT EXISTS app_event_cursors (
 app_id TEXT NOT NULL REFERENCES slack_apps(id), surface TEXT NOT NULL, sequence INTEGER NOT NULL DEFAULT 0,
 leased_sequence INTEGER NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0, retry_count INTEGER NOT NULL DEFAULT 0, retry_reason TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (app_id, surface)
);
CREATE TABLE IF NOT EXISTS app_triggers (
 token_hash TEXT PRIMARY KEY, app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
 consumed_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS app_response_urls (
 token_hash TEXT PRIMARY KEY, app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 original_message_id TEXT NOT NULL DEFAULT '', thread_timestamp TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, uses_remaining INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS app_configuration_tokens (
 access_hash TEXT PRIMARY KEY, refresh_hash TEXT NOT NULL UNIQUE, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), expires_at TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS oauth_clients (id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL, app_id TEXT NOT NULL);
-- oauth_codes.code holds domain.HashToken(code), never the code itself: an
-- authorization code is a bearer credential, and a database copy, backup or
-- replica of it is enough to redeem the grant. expires_at is UnixNano and bounds
-- redemption to store.OAuthCodeLifetime.
CREATE TABLE IF NOT EXISTS oauth_codes (code TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES oauth_clients(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, bot_id TEXT NOT NULL DEFAULT '', bot_user_id TEXT NOT NULL DEFAULT '', bot_scopes TEXT NOT NULL DEFAULT '[]', user_scopes TEXT NOT NULL DEFAULT '[]', redirect_uri TEXT NOT NULL DEFAULT '', code_challenge TEXT NOT NULL DEFAULT '', code_challenge_method TEXT NOT NULL DEFAULT '', expires_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
 refresh_hash TEXT PRIMARY KEY, access_hash TEXT NOT NULL, client_id TEXT NOT NULL REFERENCES oauth_clients(id),
 app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 installer_id TEXT NOT NULL REFERENCES users(id), bot_id TEXT NOT NULL DEFAULT '', scopes TEXT NOT NULL, token_type TEXT NOT NULL,
 access_expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS oauth_refresh_identity ON oauth_refresh_tokens(client_id, workspace_id, user_id, bot_id, token_type, created_at DESC);
CREATE TABLE IF NOT EXISTS openid_refresh_tokens (token_hash TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES oauth_clients(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS rtm_connections (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS app_tokens (token_hash TEXT PRIMARY KEY, app_id TEXT NOT NULL, scopes TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS socket_mode_connections (id TEXT PRIMARY KEY, app_id TEXT NOT NULL, expires_at INTEGER NOT NULL, consumed_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS socket_mode_admission (app_id TEXT PRIMARY KEY, ticket INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS socket_mode_cursors (app_id TEXT PRIMARY KEY, sequence INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS socket_mode_responses (app_id TEXT NOT NULL, envelope_id TEXT NOT NULL, payload TEXT NOT NULL, received_at INTEGER NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at INTEGER NOT NULL DEFAULT 0, acknowledged_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (app_id, envelope_id));
CREATE TABLE IF NOT EXISTS socket_mode_interactions (
 envelope_id TEXT PRIMARY KEY, app_id TEXT NOT NULL REFERENCES slack_apps(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), type TEXT NOT NULL, payload TEXT NOT NULL,
 response_token_hash TEXT NOT NULL REFERENCES app_response_urls(token_hash), created_at INTEGER NOT NULL,
 lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0, retry_count INTEGER NOT NULL DEFAULT 0, retry_reason TEXT NOT NULL DEFAULT '',
 acknowledged_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS socket_mode_interactions_claim ON socket_mode_interactions(app_id, acknowledged_at, retry_at, lease_expires_at, created_at, envelope_id);
CREATE TABLE IF NOT EXISTS conversation_prefs (
 conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),
 can_thread_types TEXT NOT NULL DEFAULT '[]', can_thread_users TEXT NOT NULL DEFAULT '[]',
 who_can_post_types TEXT NOT NULL DEFAULT '[]', who_can_post_users TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS invite_requests (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), email TEXT NOT NULL, requested_by TEXT NOT NULL REFERENCES users(id), channel_ids TEXT NOT NULL DEFAULT '[]', custom_message TEXT NOT NULL DEFAULT '', real_name TEXT NOT NULL DEFAULT '', resend INTEGER NOT NULL DEFAULT 0, restricted INTEGER NOT NULL DEFAULT 0, ultra_restricted INTEGER NOT NULL DEFAULT 0, guest_expiration_at INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, created_at INTEGER NOT NULL, reviewed_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS app_approvals (app_id TEXT PRIMARY KEY, request_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), status TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS app_installations (app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, PRIMARY KEY (app_id, workspace_id));
CREATE TABLE IF NOT EXISTS incoming_webhooks (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), app_id TEXT NOT NULL, conversation_id TEXT NOT NULL REFERENCES conversations(id), user_id TEXT NOT NULL REFERENCES users(id), secret_hash TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS incoming_webhooks_lookup ON incoming_webhooks(workspace_id, app_id, secret_hash, enabled);
CREATE TABLE IF NOT EXISTS app_permission_requests (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), requester_id TEXT NOT NULL REFERENCES users(id), target_user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, trigger_id TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS views (id TEXT PRIMARY KEY, app_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), type TEXT NOT NULL, external_id TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL, state TEXT NOT NULL DEFAULT '', errors TEXT NOT NULL DEFAULT '{}', hash TEXT NOT NULL, root_view_id TEXT NOT NULL, previous_view_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS views_workspace_external ON views(workspace_id, external_id) WHERE external_id <> '';
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
CREATE TABLE IF NOT EXISTS closed_direct_conversations (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 conversation_id TEXT NOT NULL REFERENCES conversations(id), closed_at TEXT NOT NULL,
 PRIMARY KEY (workspace_id, user_id, conversation_id)
);
CREATE INDEX IF NOT EXISTS closed_direct_conversations_conversation ON closed_direct_conversations(conversation_id, user_id);
CREATE TABLE IF NOT EXISTS messages (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	conversation TEXT NOT NULL REFERENCES conversations(id), author_id TEXT NOT NULL REFERENCES users(id),
	app_id TEXT NOT NULL DEFAULT '', text TEXT NOT NULL, blocks TEXT NOT NULL DEFAULT '', attachments TEXT NOT NULL DEFAULT '[]',
	metadata TEXT NOT NULL DEFAULT '', stream_state TEXT NOT NULL DEFAULT '', thread_timestamp TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, unfurls TEXT NOT NULL DEFAULT '{}',
	text_folded TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS messages_conversation_created ON messages(conversation, created_at, id);
CREATE TABLE IF NOT EXISTS ephemeral_messages (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 conversation_id TEXT NOT NULL REFERENCES conversations(id), author_id TEXT NOT NULL REFERENCES users(id),
 app_id TEXT NOT NULL DEFAULT '', recipient_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL,
 blocks TEXT NOT NULL DEFAULT '', attachments TEXT NOT NULL DEFAULT '[]', timestamp TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ephemeral_messages_recipient_conversation_created ON ephemeral_messages(workspace_id, recipient_id, conversation_id, created_at DESC, id DESC);
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
 size INTEGER NOT NULL, created_at TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, public_token TEXT NOT NULL DEFAULT '',
 name_folded TEXT NOT NULL DEFAULT '', title_folded TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS external_uploads (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), uploader_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, title TEXT NOT NULL, mime_type TEXT NOT NULL, blob_key TEXT NOT NULL UNIQUE, size INTEGER NOT NULL,
 status TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL, uploaded_at TEXT NOT NULL DEFAULT '', completed_at TEXT NOT NULL DEFAULT ''
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
CREATE TABLE IF NOT EXISTS notification_preferences (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 level TEXT NOT NULL, keywords TEXT NOT NULL DEFAULT '[]',
 activity_channels INTEGER NOT NULL DEFAULT 1, activity_reminders INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE IF NOT EXISTS conversation_notification_preferences (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 conversation_id TEXT NOT NULL REFERENCES conversations(id), level TEXT NOT NULL DEFAULT 'inherit',
 follow_every_thread INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (workspace_id, user_id, conversation_id)
);
CREATE INDEX IF NOT EXISTS conversation_notification_preferences_conversation ON conversation_notification_preferences(conversation_id, user_id);
CREATE TABLE IF NOT EXISTS thread_follows (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 conversation_id TEXT NOT NULL REFERENCES conversations(id), root_timestamp TEXT NOT NULL,
 PRIMARY KEY (workspace_id, user_id, conversation_id, root_timestamp)
);
CREATE INDEX IF NOT EXISTS thread_follows_conversation_root ON thread_follows(conversation_id, root_timestamp, user_id);
CREATE TABLE IF NOT EXISTS activity_items (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 actor_id TEXT NOT NULL DEFAULT '', conversation_id TEXT NOT NULL DEFAULT '', message_id TEXT NOT NULL DEFAULT '',
 reminder_id TEXT NOT NULL DEFAULT '', reaction_name TEXT NOT NULL DEFAULT '', occurred_at INTEGER NOT NULL,
 read_at INTEGER NOT NULL DEFAULT 0, cleared_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS activity_items_user_time ON activity_items(workspace_id, user_id, cleared_at, occurred_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS activity_item_kinds (
 activity_id TEXT NOT NULL REFERENCES activity_items(id) ON DELETE CASCADE, kind TEXT NOT NULL,
 PRIMARY KEY (activity_id, kind)
);
CREATE INDEX IF NOT EXISTS activity_item_kinds_filter ON activity_item_kinds(kind, activity_id);
CREATE TABLE IF NOT EXISTS activity_preferences (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 layout TEXT NOT NULL, PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE IF NOT EXISTS recent_searches (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 query TEXT NOT NULL, searched_at INTEGER NOT NULL,
 PRIMARY KEY (workspace_id, user_id, query)
);
CREATE INDEX IF NOT EXISTS recent_searches_user_time ON recent_searches(workspace_id, user_id, searched_at DESC, query);
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
CREATE TABLE IF NOT EXISTS saved_items (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 message_id TEXT NOT NULL REFERENCES messages(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE (workspace_id, user_id, message_id)
);
CREATE INDEX IF NOT EXISTS saved_items_user_state_updated ON saved_items(workspace_id, user_id, state, updated_at, id);
CREATE TABLE IF NOT EXISTS bookmarks (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 title TEXT NOT NULL, type TEXT NOT NULL, link TEXT NOT NULL DEFAULT '', emoji TEXT NOT NULL DEFAULT '', entity_id TEXT NOT NULL DEFAULT '',
 access_level TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 updated_by TEXT NOT NULL REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS bookmarks_conversation_rank ON bookmarks(workspace_id, conversation_id, created_at, id);
CREATE TABLE IF NOT EXISTS reminders (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), creator_id TEXT NOT NULL REFERENCES users(id),
 user_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, due_at INTEGER NOT NULL, complete_at INTEGER NOT NULL DEFAULT 0,
 recurring INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS reminders_user_due ON reminders(workspace_id, user_id, due_at, id);
CREATE TABLE IF NOT EXISTS later_reminders (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), creator_id TEXT NOT NULL REFERENCES users(id),
 user_id TEXT NOT NULL DEFAULT '', channel_id TEXT NOT NULL DEFAULT '', source_message_id TEXT NOT NULL DEFAULT '',
 source_conversation_id TEXT NOT NULL DEFAULT '', source_timestamp TEXT NOT NULL DEFAULT '', target TEXT NOT NULL, text TEXT NOT NULL, due_at INTEGER NOT NULL,
 timezone TEXT NOT NULL, recurrence TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 completed_at INTEGER NOT NULL DEFAULT 0, last_delivered_at INTEGER NOT NULL DEFAULT 0, acknowledged_at INTEGER NOT NULL DEFAULT 0, failed_at INTEGER NOT NULL DEFAULT 0,
 failure_code TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS later_reminders_owner_due ON later_reminders(workspace_id, target, user_id, creator_id, due_at, id);
CREATE INDEX IF NOT EXISTS later_reminders_delivery ON later_reminders(workspace_id, completed_at, failed_at, due_at, id);
CREATE TABLE IF NOT EXISTS scheduled_messages (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), channel_id TEXT NOT NULL REFERENCES conversations(id),
 author_id TEXT NOT NULL REFERENCES users(id), app_id TEXT NOT NULL DEFAULT '', bot_id TEXT NOT NULL DEFAULT '',
 credential_hash TEXT NOT NULL DEFAULT '', text TEXT NOT NULL, blocks TEXT NOT NULL DEFAULT '', attachments TEXT NOT NULL DEFAULT '',
 metadata TEXT NOT NULL DEFAULT '', stream_state TEXT NOT NULL DEFAULT '',
 thread_ts TEXT NOT NULL DEFAULT '', post_at INTEGER NOT NULL, created_at INTEGER NOT NULL,
 delivered INTEGER NOT NULL DEFAULT 0, delivered_at INTEGER NOT NULL DEFAULT 0, failed_at INTEGER NOT NULL DEFAULT 0,
 failure_code TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS scheduled_messages_owner ON scheduled_messages(workspace_id, author_id, post_at, id);
CREATE INDEX IF NOT EXISTS scheduled_messages_credential ON scheduled_messages(workspace_id, credential_hash, post_at, id);
CREATE TABLE IF NOT EXISTS drafts (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 conversation_id TEXT NOT NULL REFERENCES conversations(id), thread_ts TEXT NOT NULL DEFAULT '',
 text TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY (workspace_id, user_id, conversation_id, thread_ts)
);
CREATE INDEX IF NOT EXISTS drafts_owner_updated ON drafts(workspace_id, user_id, updated_at, conversation_id, thread_ts);
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
CREATE TABLE IF NOT EXISTS lists (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), owner_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, description_blocks TEXT NOT NULL DEFAULT '[]', schema_json TEXT NOT NULL DEFAULT '[]', todo_mode INTEGER NOT NULL DEFAULT 0,
 version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS list_items (
 id TEXT PRIMARY KEY, list_id TEXT NOT NULL REFERENCES lists(id), parent_item_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 fields TEXT NOT NULL DEFAULT '[]', created_by TEXT NOT NULL REFERENCES users(id), updated_by TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0, version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS list_items_list_id ON list_items(list_id, id);
CREATE TABLE IF NOT EXISTS list_access (
 list_id TEXT NOT NULL REFERENCES lists(id), entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, access_level TEXT NOT NULL,
 PRIMARY KEY (list_id, entity_type, entity_id)
);
CREATE TABLE IF NOT EXISTS list_downloads (
 id TEXT PRIMARY KEY, list_id TEXT NOT NULL REFERENCES lists(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 status TEXT NOT NULL, url TEXT NOT NULL DEFAULT '', include_archived INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
`

const schemaVersion = 116

// storedTimestampColumns lists every TEXT column that holds an encoded instant.
// Each of them takes part in an ORDER BY, a keyset-pagination predicate, a
// lease-fencing comparison, or an equality lookup, so each of them has to carry
// domain.StoredTime's fixed-width encoding for byte order to equal time order.
// Migration step 78 rewrites any value still in the variable-width
// time.RFC3339Nano form this repository used to write.
// schema_migrations.applied_at is deliberately absent: it is bookkeeping that no
// query ever compares, and databases in the field carry free-form text there.
var storedTimestampColumns = []struct{ table, column string }{
	{"sessions", "expires_at"},
	{"messages", "created_at"},
	{"drafts", "updated_at"},
	{"ephemeral_messages", "created_at"},
	{"reactions", "created_at"},
	{"pins", "created_at"},
	{"stars", "created_at"},
	{"files", "created_at"},
	{"external_uploads", "created_at"},
	{"external_uploads", "expires_at"},
	{"external_uploads", "uploaded_at"},
	{"external_uploads", "completed_at"},
	{"outbox", "created_at"},
	{"outbox", "lease_until"},
	{"outbox", "next_attempt_at"},
	{"lifecycle_state", "wake_deadline"},
	{"idempotency", "created_at"},
	{"read_cursors", "updated_at"},
	{"lists", "created_at"},
	{"lists", "updated_at"},
	{"list_items", "created_at"},
	{"list_items", "updated_at"},
	{"list_downloads", "created_at"},
}

const legacySessionScopes = "chat:write channels:history users:read users:read.email users:write channels:read channels:manage reactions:write reactions:read pins:write pins:read bookmarks:read bookmarks:write search:read files:write files:read canvases:read canvases:write lists:read lists:write team:read"

type queryExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// insertOutboxStatement is the durable-event enqueue used by every store
// mutation whose event already carries its workspace. There used to be six
// textual variants of it: four that omitted actor_id entirely — so every
// mutation routed through them silently dropped events.Event.ActorID — plus an
// actor_id form repeated verbatim at five call sites and a SELECT form repeated
// at six. Two helpers now cover both shapes, and both take the whole event so a
// column cannot be forgotten again.
const insertOutboxStatement = `INSERT INTO outbox (id, workspace_id, actor_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, 0, '', '', '')`

// insertOutboxForConversationStatement derives the workspace from the
// conversation being mutated, for the events whose caller does not carry it.
const insertOutboxForConversationStatement = `INSERT INTO outbox (id, workspace_id, actor_id, topic, payload, created_at, delivered, lease_owner, lease_until, next_attempt_at) SELECT ?, workspace_id, ?, ?, ?, ?, 0, '', '', '' FROM conversations WHERE id = ?`

func insertOutbox(ctx context.Context, tx *sql.Tx, event events.Event) error {
	_, err := tx.ExecContext(ctx, insertOutboxStatement, event.ID, event.WorkspaceID, event.ActorID, event.Topic, event.Payload, domain.NewStoredTime(event.CreatedAt))
	return err
}

func insertOutboxForConversation(ctx context.Context, tx *sql.Tx, event events.Event, conversation domain.ConversationID) error {
	_, err := tx.ExecContext(ctx, insertOutboxForConversationStatement, event.ID, event.ActorID, event.Topic, event.Payload, domain.NewStoredTime(event.CreatedAt), conversation)
	return err
}

type Store struct {
	db                     *sql.DB
	migrationLockStatement string
	// sqliteDialect records whether the underlying engine is SQLite-compatible, so
	// SQLite-only statements are attempted only where they mean something.
	sqliteDialect bool
	// now is the clock the lease and expiry predicates compare against. It is a
	// seam so the fencing contract can be asserted at an exact instant instead of
	// being sampled from the wall clock; internal/activator/spool.go uses the
	// same shape.
	now func() time.Time
	// backfills owns the data-migration drain that Migrate starts and Close
	// stops. See AwaitBackfills.
	backfills backfillDrain
}

// backfillDrain is the running state of the data migrations. The drain used to
// be part of Migrate's own call, so every replica of every binary sat inside
// Open until every column of a five-million-row database had been rewritten —
// the outage the design was written to remove, minus the fence. It now runs on
// its own goroutine with its own context, and the two things a caller can want
// from it are separable: "is the schema current" is answered by Migrate
// returning, "is the rewrite finished" by AwaitBackfills.
type backfillDrain struct {
	mutex  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func systemClock() time.Time { return time.Now().UTC() }

var _ store.Store = (*Store)(nil)

func (s *Store) AppendEvent(ctx context.Context, event events.Event) error {
	_, err := s.db.ExecContext(ctx, insertOutboxStatement, event.ID, event.WorkspaceID, event.ActorID, event.Topic, event.Payload, domain.NewStoredTime(event.CreatedAt))
	return err
}

func (s *Store) RecordAccess(ctx context.Context, value domain.AccessLog) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_logs(workspace_id, user_id, username, created_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?)`, value.WorkspaceID, value.UserID, value.Username, value.CreatedAt.UTC().Unix(), value.IP, value.UserAgent)
	return err
}
func (s *Store) ListAccessLogs(ctx context.Context, workspace domain.WorkspaceID, before time.Time, limit, page int) ([]domain.AccessLog, bool, error) {
	if limit <= 0 || limit > 1000 || page <= 0 {
		return nil, false, store.InvalidArgument("access log page parameters are invalid")
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

// sqlitePragmas are connection settings, not database settings: SQLite applies
// foreign_keys and busy_timeout per connection. Executing them once against a
// pooled *sql.DB configures whichever connection the pool happened to hand out
// and leaves every other connection with foreign keys OFF and no busy timeout,
// which silently turned every REFERENCES clause in the shared schema into a
// comment. Passing them in the DSN makes the driver apply them on every connect,
// so the setting cannot depend on which connection serves a statement.
// journal_mode is deliberately not in this list: it is a database-level setting
// that persists in the file, and applying it on every connect makes a concurrent
// first open fail with SQLITE_BUSY because switching journal mode needs
// exclusive access. It is applied once, with a retry, by configure.
// sqliteDSN is the single writer of these; see the note there on why an
// operator-supplied value of either is removed rather than shadowed.

const sqliteJournalPragma = "PRAGMA journal_mode = WAL"

// requiredSQLitePragmas is the enforced subset: journal_mode persists in the
// database file, but these two are per-connection and are what the schema's
// integrity depends on. foreign_keys is an exact requirement; busy_timeout is a
// floor, so an operator may raise it.
const requiredBusyTimeout = 5000

// sqliteDSN adds the required pragmas to a caller-supplied DSN.
//
// The previous version appended unconditionally and assumed the driver resolves
// duplicate _pragma parameters last-wins. It does for foreign_keys and it does
// NOT for busy_timeout: measured, a DSN carrying _pragma=busy_timeout(1) made
// Open fail with "sqlite busy_timeout is 1 on connection 0, want 5000" — a
// perfectly ordinary operator DSN refusing to start the product, with an error
// naming an internal invariant instead of the parameter responsible.
//
// So the conflicting parameters are removed rather than shadowed. A
// busy_timeout the operator set at or above the floor is kept, because raising
// it is a legitimate tuning decision; a lower one, and any foreign_keys
// setting at all, is dropped, because the schema's referential integrity is not
// an operator preference.
func sqliteDSN(dsn string) string {
	base, query, hasQuery := strings.Cut(dsn, "?")
	kept := make([]string, 0)
	operatorBusyTimeout := 0
	if hasQuery {
		for _, parameter := range strings.Split(query, "&") {
			value, isPragma := strings.CutPrefix(parameter, "_pragma=")
			if !isPragma {
				kept = append(kept, parameter)
				continue
			}
			switch {
			case strings.HasPrefix(value, "foreign_keys("):
			case strings.HasPrefix(value, "busy_timeout("):
				if parsed, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(value, "busy_timeout("), ")")); err == nil && parsed > operatorBusyTimeout {
					operatorBusyTimeout = parsed
				}
			default:
				kept = append(kept, parameter)
			}
		}
	}
	busyTimeout := requiredBusyTimeout
	if operatorBusyTimeout > busyTimeout {
		busyTimeout = operatorBusyTimeout
	}
	kept = append(kept, "_pragma=foreign_keys(1)", fmt.Sprintf("_pragma=busy_timeout(%d)", busyTimeout))
	return base + "?" + strings.Join(kept, "&")
}

// privateInMemoryDSN reports a DSN that names an in-memory database WITHOUT
// shared cache. SQLite gives every such connection its own private, empty
// database, so a pooled handle over one is not one database at all: the schema
// is created on whichever connection ran the migration and every other
// connection sees nothing. This was found by VerifyReferentialIntegrity, which
// probes several connections at once and hit "no such table" on the second.
//
// Pinning the pool to one connection is the repair that keeps the DSN meaning
// what the caller asked for. Rewriting it to the shared-cache form would make
// every ":memory:" open in the process collide on one database instead.
func privateInMemoryDSN(dsn string) bool {
	base, query, _ := strings.Cut(dsn, "?")
	inMemory := strings.Contains(base, ":memory:") || strings.Contains(query, "mode=memory")
	return inMemory && !strings.Contains(query, "cache=shared")
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", sqliteDSN(dsn))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if privateInMemoryDSN(dsn) {
		db.SetMaxOpenConns(1)
	}
	s, err := fromDB(ctx, db, true, []string{sqliteJournalPragma}, sqliteMigrationLockStatement)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// FromDB initializes the repository against a SQLite handle the caller opened.
// The DSN is not ours to amend, so the pool is pinned to a single connection and
// the pragmas are issued on it; that is the only way to guarantee the settings
// apply to every statement. internal/activator/spool.go pins its spool handle
// the same way. Prefer Open, which keeps the pool unbounded.
func FromDB(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("SQL store requires a database handle")
	}
	db.SetMaxOpenConns(1)
	return fromDB(ctx, db, true, []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000", sqliteJournalPragma}, sqliteMigrationLockStatement)
}

// FromDqliteDB initializes the repository against a dqlite-managed database.
//
// dqlite owns connection configuration: it opens the underlying SQLite handles
// itself, so this process cannot set a per-connection pragma on them and no
// pragma statements are issued here. What it CAN do — and what the previous
// version of this constructor did not do — is check the one property the shared
// schema depends on. Passing nil pragmas skipped every startup verification, so
// whether foreign keys were enforced on this profile was unknown: SQLite's own
// default is OFF, and the shared qualification suite asserts that a referential
// failure surfaces as store.ErrNotFound, which is unreachable without them.
// VerifyReferentialIntegrity settles it by behaviour rather than by comment, and
// fails closed if the answer is no.
func FromDqliteDB(ctx context.Context, db *sql.DB) (*Store, error) {
	return fromDB(ctx, db, true, nil, sqliteMigrationLockStatement)
}

// FromPostgresDB initializes the repository against a PostgreSQL database
// opened by the PostgreSQL adapter. The adapter owns PostgreSQL-specific
// connection settings and SQL translation.
func FromPostgresDB(ctx context.Context, db *sql.DB) (*Store, error) {
	return fromDB(ctx, db, false, nil, `SELECT pg_advisory_xact_lock(hashtext(current_database()), hashtext('sameoldchat-schema-migration'))`)
}

func fromDB(ctx context.Context, db *sql.DB, sqliteDialect bool, pragmas []string, migrationLockStatement string) (*Store, error) {
	if db == nil {
		return nil, errors.New("SQL store requires a database handle")
	}
	s := &Store{db: db, migrationLockStatement: migrationLockStatement, sqliteDialect: sqliteDialect, now: systemClock}
	if pragmas != nil {
		if err := s.configure(ctx, pragmas...); err != nil {
			return nil, err
		}
		// Refuse to hand out a repository whose foreign keys are inert.
		if err := s.VerifyConnectionSettings(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.Migrate(ctx); err != nil {
		return nil, err
	}
	// After the schema exists, and on every profile including the two that take
	// no pragmas. A repository whose REFERENCES clauses are inert is a repository
	// that reports success for writes that orphan rows.
	if err := s.VerifyReferentialIntegrity(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) configure(ctx context.Context, statements ...string) error {
	for _, statement := range statements {
		deadline := time.Now().Add(5 * time.Second)
		backoff := 5 * time.Millisecond
		for {
			if _, err := s.db.ExecContext(ctx, statement); err == nil {
				break
			} else if !contended(err) || time.Now().After(deadline) {
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

// VerifyConnectionSettings checks the per-connection SQLite settings the schema
// depends on, on as many simultaneous connections as the pool allows. Checking
// one connection proves nothing: the defect this guards against was a pragma
// that applied to exactly one pooled connection.
func (s *Store) VerifyConnectionSettings(ctx context.Context) error {
	return s.onEachPooledConnection(ctx, func(index int, connection *sql.Conn) error {
		var foreignKeys int64
		if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			return fmt.Errorf("read sqlite foreign_keys on connection %d: %w", index, err)
		}
		if foreignKeys != 1 {
			return fmt.Errorf("sqlite foreign_keys is %d on connection %d, want 1", foreignKeys, index)
		}
		var busyTimeout int64
		if err := connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			return fmt.Errorf("read sqlite busy_timeout on connection %d: %w", index, err)
		}
		// A floor, not an equality: an operator raising the timeout in the DSN is
		// tuning, and refusing to start over it was a defect of its own.
		if busyTimeout < requiredBusyTimeout {
			return fmt.Errorf("sqlite busy_timeout is %d on connection %d, want at least %d", busyTimeout, index, requiredBusyTimeout)
		}
		return nil
	})
}

// referentialProbeStatement violates conversation_members' foreign keys with
// identifiers no repository path can mint. It is executed inside a transaction
// that is always rolled back, so it can never leave a row behind.
const referentialProbeStatement = `INSERT INTO conversation_members(conversation_id, user_id) VALUES ('__referential probe__', '__referential probe__')`

// VerifyReferentialIntegrity proves, by behaviour, that the REFERENCES clauses
// in the shared schema are enforced by the engine actually serving statements.
//
// It exists because the dqlite profile is constructed with no pragmas and
// therefore skipped VerifyConnectionSettings entirely, on the strength of a
// comment saying dqlite owns connection configuration. That left the profile's
// referential integrity as an assumption: SQLite's own default for foreign_keys
// is OFF, and classify's SQLITE_CONSTRAINT_FOREIGNKEY -> store.ErrNotFound path
// is only reachable if it is ON. Either dqlite enables it and nothing said so,
// or it does not and every REFERENCES clause on that profile was a comment.
//
// Reading PRAGMA foreign_keys would answer that on SQLite and is not portable —
// PostgreSQL has no such pragma, and dqlite's statement handling is not ours to
// assume. Attempting a write the schema must reject answers it on every engine,
// which is why this is a probe and not a setting check.
func (s *Store) VerifyReferentialIntegrity(ctx context.Context) error {
	return s.onEachPooledConnection(ctx, func(index int, connection *sql.Conn) error {
		// The probe holds every pooled connection at once and writes on each, so
		// on a profile that reports contention rather than waiting through it the
		// probe competes with itself. Losing that race says nothing about whether
		// the schema is enforced, so it is waited out rather than read as an
		// answer — a contended write is not a missing constraint.
		var execErr error
		if err := underContention(ctx, func() error {
			tx, beginErr := connection.BeginTx(ctx, nil)
			if beginErr != nil {
				return fmt.Errorf("begin referential probe on connection %d: %w", index, beginErr)
			}
			_, execErr = tx.ExecContext(ctx, referentialProbeStatement)
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				return fmt.Errorf("roll back referential probe on connection %d: %w", index, rollbackErr)
			}
			if execErr != nil && contended(execErr) {
				return execErr
			}
			return nil
		}); err != nil {
			return err
		}
		if execErr == nil {
			return fmt.Errorf("connection %d accepted a row referencing a missing conversation and a missing user: this storage profile does not enforce the schema's REFERENCES clauses, so every relationship in it is unguarded and store.ErrNotFound is unreachable for referential failures", index)
		}
		if !errors.Is(classify(execErr), store.ErrNotFound) {
			return fmt.Errorf("connection %d rejected the referential probe with %v, which does not classify as a referential failure", index, execErr)
		}
		return nil
	})
}

// onEachPooledConnection runs a check on as many simultaneously held connections
// as the pool allows. Holding them at once is what forces the pool to open
// distinct connections; checking one connection proves nothing about the others.
func (s *Store) onEachPooledConnection(ctx context.Context, check func(int, *sql.Conn) error) error {
	probes := 4
	if limit := s.db.Stats().MaxOpenConnections; limit > 0 && limit < probes {
		probes = limit
	}
	held := make([]*sql.Conn, 0, probes)
	defer func() {
		for _, connection := range held {
			_ = connection.Close()
		}
	}()
	for index := 0; index < probes; index++ {
		connection, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("acquire connection %d: %w", index, err)
		}
		held = append(held, connection)
		if err := check(index, connection); err != nil {
			return err
		}
	}
	return nil
}

// contendedSQLStates are the SQLSTATEs that mean "another transaction got there
// first". PostgreSQL raises 40001 when a serializable read/write dependency
// cannot be resolved, 40P01 when it picks this transaction as the deadlock
// victim, and 55P03 when a NOWAIT lock request loses. All three are defined by
// the engine as retryable by the client, and the client here is this process.
var contendedSQLStates = map[string]bool{"40001": true, "40P01": true, "55P03": true}

// contendedMessages is the signal of last resort, for the drivers that forward a
// failure with no machine-readable classification at all. dqlite forwards a
// leadership change that way, and a PostgreSQL error that has been flattened to
// text on its way through a wrapper carries its SQLSTATE in the message.
var contendedMessages = []string{
	"database is locked",
	"database table is locked",
	"sqlite_busy",
	"sqlstate 40001",
	"sqlstate 40p01",
	"sqlstate 55p03",
	"could not serialize access",
	"deadlock detected",
	"leadership lost",
	"not leader",
	"leader changed",
}

// contended reports a failure that means "another writer got there first; the
// same call may succeed if it is made again".
//
// It used to be sqliteBusy, and it matched only SQLite result codes and SQLite
// English — on the very profile the retry was written for, the replicated one,
// and on PostgreSQL, it returned false for every contention the engine can
// raise. underContention therefore made exactly one attempt there and handed the
// raw error back, so a routine serialization failure surfaced as an unclassified
// driver error at the transport. The concept is cross-engine and so is the name.
func contended(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrTransient) {
		return true
	}
	var typed *sqlite.Error
	if errors.As(err, &typed) {
		// The driver reports both the primary and the extended result code
		// through this method, and SQLITE_BUSY_SNAPSHOT — the WAL writer that
		// lost — is an extended form of SQLITE_BUSY.
		switch typed.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			return true
		}
		return false
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return contendedSQLStates[strings.ToUpper(state.SQLState())]
	}
	// The driver reports contention raised inside a statement as a plain error,
	// so the message check stays as a fallback rather than the only signal.
	message := strings.ToLower(err.Error())
	for _, marker := range contendedMessages {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// underContention runs a write that may lose a race for the engine's write lock.
//
// SQLite absorbs contention inside the driver: busy_timeout makes a losing
// writer wait up to requiredBusyTimeout before failing. The replicated profile
// has no equivalent — it reports the contention to the caller — so a write that
// would simply have waited on one profile failed on another. That is the same
// operation answering differently per deployment.
//
// The budget is deliberately the SAME as the busy timeout the SQLite profiles
// are configured with, so the two profiles wait equally long before giving up.
// An earlier version of this used a handful of short retries, which is not the
// behaviour SQLite has: it exhausted roughly a twenty-fifth of the timeout and
// then reported contention that SQLite would have waited out.
//
// The budget is bounded rather than unlimited because contention is transient by
// definition: an operation still losing after the full timeout is reporting
// something other than contention and must surface rather than spin.
func underContention(ctx context.Context, attempt func() error) error {
	deadline := time.Now().Add(requiredBusyTimeout * time.Millisecond)
	delay := time.Millisecond
	var err error
	for {
		if err = attempt(); err == nil || !contended(err) {
			return err
		}
		if !time.Now().Add(delay).Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

// Close stops the data-migration drain before it closes the handle. Closing the
// database out from under a running drain would surface as "database is closed"
// from a goroutine nobody is watching.
func (s *Store) Close() error {
	s.stopBackfills()
	return s.db.Close()
}

// startBackfills runs the registered data migrations on a goroutine. Any drain
// still running from an earlier Migrate is stopped and awaited first, so a
// repeated Migrate — which the qualification suite does — never has two drains
// writing the same cursor.
func (s *Store) startBackfills() {
	s.stopBackfills()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.backfills.mutex.Lock()
	s.backfills.cancel = cancel
	s.backfills.done = done
	s.backfills.err = nil
	s.backfills.mutex.Unlock()
	go func() {
		err := s.runPendingBackfills(ctx)
		s.backfills.mutex.Lock()
		s.backfills.err = err
		s.backfills.mutex.Unlock()
		close(done)
	}()
}

func (s *Store) stopBackfills() {
	s.backfills.mutex.Lock()
	cancel, done := s.backfills.cancel, s.backfills.done
	s.backfills.cancel, s.backfills.done = nil, nil
	s.backfills.mutex.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// AwaitBackfills blocks until the data migrations this store started have
// finished, and reports what they finished with. A caller that must see the
// rewritten encoding — a readiness probe, an operator command, a test — waits
// here; a caller that only needs the schema does not wait at all.
//
// It returns nil immediately when nothing is running, which is the normal state:
// the drain is registered work, not a permanent worker.
func (s *Store) AwaitBackfills(ctx context.Context) error {
	s.backfills.mutex.Lock()
	done := s.backfills.done
	s.backfills.mutex.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	s.backfills.mutex.Lock()
	defer s.backfills.mutex.Unlock()
	return s.backfills.err
}

// ErrIntegrityCheckUnsupported reports a storage profile with no physical
// integrity check. It replaces a rewrite that turned PRAGMA integrity_check into
// SELECT 'ok' on PostgreSQL — a check that could never fail and therefore said
// nothing. A stated limitation is auditable; a tautology is not.
var ErrIntegrityCheckUnsupported = errors.New("integrity check is not available on this storage profile")

func (s *Store) IntegrityCheck(ctx context.Context) error {
	if !s.sqliteDialect {
		return ErrIntegrityCheckUnsupported
	}
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
		return store.InvalidArgument("invalid workspace discoverability")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces(id, domain, name, description, discoverability, icon_url) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET domain = excluded.domain, name = excluded.name, description = excluded.description, discoverability = excluded.discoverability, icon_url = excluded.icon_url`, value.ID, value.Domain, value.Name, value.Description, discoverability, value.IconURL)
	return err
}

// SeedUser creates the bootstrap identity and then leaves it alone. It is called
// on every process start by every binary that opens the store — cmd/worker,
// cmd/blobgc and cmd/socketmode-worker among them, none of which is given the
// bootstrap administrator e-mail — so an upsert that overwrote the row destroyed
// operator state on a shared database at every restart: the administrator's
// e-mail and profile were blanked, and `deleted = excluded.deleted` silently
// undid an administrative deactivation.
//
// The conflict clause therefore preserves every column an operator can change.
// The one exception is an e-mail that is still unset: the only writer of
// users.email is CreateUser, so there is no administrative path that can attach
// an address to an already seeded bootstrap identity, and refusing to fill a
// blank would make the -bootstrap-admin-email flag inert on any database created
// before it was set. Filling a blank is not overwriting a decision; replacing a
// stored address would be.
func (s *Store) SeedUser(ctx context.Context, value domain.User) error {
	return s.seedUser(ctx, value, domain.WorkspaceRoleMember, false)
}

// SeedBootstrapAdministrator creates the configured initial administrator as
// one transaction. The role and e-mail must not be separate startup writes: a
// process failure between them would leave an addressed bootstrap identity that
// could never be promoted on retry without also overriding a later deliberate
// demotion. An existing addressed identity keeps its operator-managed role; a
// legacy blank identity is promoted once when its e-mail is first filled.
func (s *Store) SeedBootstrapAdministrator(ctx context.Context, value domain.User) error {
	if domain.NormalizeEmail(value.Email) == "" {
		return store.InvalidArgument("bootstrap administrator email is required")
	}
	return s.seedUser(ctx, value, domain.WorkspaceRoleAdmin, true)
}

func (s *Store) seedUser(ctx context.Context, value domain.User, initialRole domain.WorkspaceRole, promoteBlankIdentity bool) error {
	deleted := 0
	if value.Deleted {
		deleted = 1
	}
	// Validate before opening the transaction. SeedWorkspace already does; this
	// one opened and rolled one back for every invalid presence.
	presence := value.Presence
	if presence == "" {
		presence = domain.PresenceAuto
	}
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		return store.InvalidArgument("invalid user presence")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	promoteExisting := false
	if promoteBlankIdentity {
		var previousEmail string
		switch err := tx.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ? AND workspace_id = ?`, value.ID, value.WorkspaceID).Scan(&previousEmail); {
		case err == nil:
			promoteExisting = domain.NormalizeEmail(previousEmail) == ""
		case errors.Is(err, sql.ErrNoRows):
		default:
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, status_expiration, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET email = CASE WHEN users.email = '' THEN excluded.email ELSE users.email END`, value.ID, value.WorkspaceID, domain.NormalizeEmail(value.Email), value.Name, value.RealName, value.Profile.DisplayName, value.Profile.StatusText, value.Profile.StatusEmoji, unixSeconds(value.Profile.StatusExpiration), value.Profile.Image24, value.Profile.Image32, value.Profile.Image48, value.Profile.Image72, value.Profile.Image192, value.Profile.Image512, value.Profile.Image1024, deleted, presence); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_members(workspace_id, user_id, role, active) VALUES (?, ?, ?, 1) ON CONFLICT(workspace_id, user_id) DO NOTHING`, value.WorkspaceID, value.ID, initialRole); err != nil {
		return err
	}
	if promoteExisting {
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET role = ?, active = 1 WHERE workspace_id = ? AND user_id = ?`, domain.WorkspaceRoleAdmin, value.WorkspaceID, value.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SeedToken(ctx context.Context, token string, record domain.TokenRecord) error {
	privateScopes := strings.Join(domain.NormalizeScopes(record.Scopes), " ")
	tokenType := strings.TrimSpace(record.TokenType)
	if tokenType == "" {
		tokenType = "user"
	}
	var expiresAt int64
	if !record.ExpiresAt.IsZero() {
		expiresAt = record.ExpiresAt.UTC().UnixNano()
	}
	revoked := 0
	if record.Revoked {
		revoked = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens(token_hash, workspace_id, user_id, app_id, bot_id, scopes, token_type, expires_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(token_hash) DO NOTHING`, domain.HashToken(token), record.WorkspaceID, record.UserID, record.AppID, record.BotID, privateScopes, tokenType, expiresAt, revoked)
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct, name_folded, topic_folded, purpose_folded) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET workspace_id = excluded.workspace_id, name = excluded.name, topic = excluded.topic, purpose = excluded.purpose, archived = excluded.archived, is_private = excluded.is_private, is_direct = excluded.is_direct, is_group_direct = excluded.is_group_direct, name_folded = excluded.name_folded, topic_folded = excluded.topic_folded, purpose_folded = excluded.purpose_folded`, value.ID, value.WorkspaceID, value.Name, value.Topic, value.Purpose, archived, private, direct, groupDirect, domain.FoldSearchText(value.Name), domain.FoldSearchText(value.Topic), domain.FoldSearchText(value.Purpose))
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

// sqliteMigrationLockStatement fences the migration on the SQLite-family
// profiles. BEGIN IMMEDIATE is not enough on its own for dqlite, which does not
// honour SQLite's locking verbs, and the multi-node profile is precisely the one
// where replicas start concurrently. Updating a sentinel row inside the
// migration transaction takes a real write lock on every SQLite-family engine.
const sqliteMigrationLockStatement = `UPDATE schema_migration_lock SET acquired = acquired + 1 WHERE id = 1`

func (s *Store) Migrate(ctx context.Context) error {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Close()
	// The fence table has to exist before the transaction that locks it.
	//
	// CREATE TABLE IF NOT EXISTS is NOT safe against a concurrent creator on
	// PostgreSQL: the existence check and the catalog insert are not atomic, so
	// two replicas starting together can both pass the check and one then fails
	// on the pg_type unique index. That is precisely the situation this fence
	// exists to survive, so a losing creator is an expected outcome rather than
	// an error: if the table is there afterwards, whoever created it is
	// immaterial.
	//
	// PostgreSQL fences with pg_advisory_xact_lock and never touches this table,
	// so it is created and seeded only where it is the fence. Doing it on every
	// profile cost two DDL statements and an insert on every PostgreSQL start and
	// left a permanently unused table behind.
	if s.migrationLockStatement == sqliteMigrationLockStatement {
		if _, err := connection.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migration_lock (id INTEGER PRIMARY KEY, acquired INTEGER NOT NULL DEFAULT 0)`); err != nil {
			if !errors.Is(classify(err), store.ErrAlreadyExists) {
				return fmt.Errorf("create migration fence: %w", err)
			}
			if _, verifyErr := connection.ExecContext(ctx, `SELECT 1 FROM schema_migration_lock WHERE 1 = 0`); verifyErr != nil {
				return fmt.Errorf("create migration fence: %w", errors.Join(err, verifyErr))
			}
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO schema_migration_lock(id, acquired) VALUES (1, 0) ON CONFLICT(id) DO NOTHING`); err != nil {
			return fmt.Errorf("initialize migration fence: %w", err)
		}
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if s.migrationLockStatement != "" {
		if _, err := connection.ExecContext(ctx, s.migrationLockStatement); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
	}
	if err := s.migrateOn(ctx, connection); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	committed = true
	// Release the fence BEFORE the column-wide rewrites run, and do not wait for
	// them. The schema is current and recorded at this point, so every replica
	// and every binary can start and serve; the rewriting is chunked, resumable
	// and idempotent, so it can be interleaved with them and with a restart.
	//
	// Draining here instead — which is what shipped — put the whole rewrite back
	// on the path Open must complete, on every replica, so the deployment had no
	// serving capacity for its duration. AwaitBackfills is for the callers that
	// genuinely need the rewrite finished.
	connection.Close()
	s.startBackfills()
	return nil
}

func (s *Store) migrateOn(ctx context.Context, db queryExecutor) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	var hasMessages, hasConversations bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages LIMIT 1)`).Scan(&hasMessages); err != nil {
		return fmt.Errorf("check for messages before migration: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversations LIMIT 1)`).Scan(&hasConversations); err != nil {
		return fmt.Errorf("check for conversations before migration: %w", err)
	}
	freshDatabase := version == 0 && !hasMessages && !hasConversations
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
		if _, err := db.ExecContext(ctx, `INSERT INTO lifecycle_state(id, state, generation) VALUES (1, 'hibernated', 0) ON CONFLICT(id) DO NOTHING`); err != nil {
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
		// The membership backfill is scoped to users whose workspace row exists.
		// SQLite's INSERT OR IGNORE silently skipped orphans while PostgreSQL's
		// ON CONFLICT DO NOTHING still raised the foreign-key violation, so the
		// same database upgraded on one profile and failed on the other.
		if _, err := db.ExecContext(ctx, `INSERT INTO workspace_members(workspace_id, user_id, role, active) SELECT u.workspace_id, u.id, 'member', 1 FROM users u WHERE EXISTS (SELECT 1 FROM workspaces w WHERE w.id = u.workspace_id) ON CONFLICT(workspace_id, user_id) DO NOTHING`); err != nil {
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
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS views (id TEXT PRIMARY KEY, app_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), type TEXT NOT NULL, external_id TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL, hash TEXT NOT NULL, root_view_id TEXT NOT NULL, previous_view_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
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
		// IF NOT EXISTS matters on the multi-node profile: replicas start
		// concurrently, and a lost race on a bare CREATE UNIQUE INDEX is a hard
		// startup failure instead of a no-op.
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS users_workspace_email ON users(workspace_id, lower(email)) WHERE email <> ''`); err != nil {
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
	if version < 65 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bookmarks (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), title TEXT NOT NULL, type TEXT NOT NULL, link TEXT NOT NULL DEFAULT '', emoji TEXT NOT NULL DEFAULT '', entity_id TEXT NOT NULL DEFAULT '', access_level TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL REFERENCES users(id))`); err != nil {
			return fmt.Errorf("migrate bookmarks: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS bookmarks_conversation_rank ON bookmarks(workspace_id, conversation_id, created_at, id)`); err != nil {
			return fmt.Errorf("index bookmarks: %w", err)
		}
	}
	if version < 66 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS canvases (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), owner_id TEXT NOT NULL REFERENCES users(id), title TEXT NOT NULL, document_content TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate canvases: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS canvas_access (canvas_id TEXT NOT NULL REFERENCES canvases(id), entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, access_level TEXT NOT NULL, PRIMARY KEY (canvas_id, entity_type, entity_id))`); err != nil {
			return fmt.Errorf("migrate canvas access: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS canvases_workspace_updated ON canvases(workspace_id, updated_at, id)`); err != nil {
			return fmt.Errorf("migrate canvas index: %w", err)
		}
	}
	if version < 67 {
		for _, statement := range []string{
			`CREATE TABLE IF NOT EXISTS lists (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), owner_id TEXT NOT NULL REFERENCES users(id), name TEXT NOT NULL, description_blocks TEXT NOT NULL DEFAULT '[]', schema_json TEXT NOT NULL DEFAULT '[]', todo_mode INTEGER NOT NULL DEFAULT 0, version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS list_items (id TEXT PRIMARY KEY, list_id TEXT NOT NULL REFERENCES lists(id), parent_item_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), fields TEXT NOT NULL DEFAULT '[]', created_by TEXT NOT NULL REFERENCES users(id), updated_by TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0, version INTEGER NOT NULL DEFAULT 1)`,
			`CREATE INDEX IF NOT EXISTS list_items_list_id ON list_items(list_id, id)`,
			`CREATE TABLE IF NOT EXISTS list_access (list_id TEXT NOT NULL REFERENCES lists(id), entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, access_level TEXT NOT NULL, PRIMARY KEY (list_id, entity_type, entity_id))`,
			`CREATE TABLE IF NOT EXISTS list_downloads (id TEXT PRIMARY KEY, list_id TEXT NOT NULL REFERENCES lists(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), status TEXT NOT NULL, url TEXT NOT NULL DEFAULT '', include_archived INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL)`,
		} {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate Lists: %w", err)
			}
		}
	}
	if version < 68 {
		columns, err := s.tableColumns(ctx, db, "list_downloads")
		if err != nil {
			return fmt.Errorf("inspect List downloads: %w", err)
		}
		if !columns["include_archived"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE list_downloads ADD COLUMN include_archived INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("migrate List download archive option: %w", err)
			}
		}
	}
	if version < 69 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS openid_refresh_tokens (token_hash TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES oauth_clients(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, expires_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate OpenID Connect refresh tokens: %w", err)
		}
	}
	if version < 70 {
		columns, err := s.tableColumns(ctx, db, "oauth_codes")
		if err != nil {
			return fmt.Errorf("inspect OAuth authorization codes: %w", err)
		}
		for _, column := range []string{"code_challenge", "code_challenge_method"} {
			if !columns[column] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE oauth_codes ADD COLUMN `+column+` TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migrate OAuth authorization code %s: %w", column, err)
				}
			}
		}
	}
	if version < 71 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS incoming_webhooks (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), app_id TEXT NOT NULL, conversation_id TEXT NOT NULL REFERENCES conversations(id), user_id TEXT NOT NULL REFERENCES users(id), secret_hash TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("migrate incoming webhooks: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS incoming_webhooks_lookup ON incoming_webhooks(workspace_id, app_id, secret_hash, enabled)`); err != nil {
			return fmt.Errorf("index incoming webhooks: %w", err)
		}
	}
	if version < 72 {
		columns, err := s.tableColumns(ctx, db, "messages")
		if err != nil {
			return fmt.Errorf("inspect message blocks: %w", err)
		}
		if !columns["blocks"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN blocks TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate message blocks: %w", err)
			}
		}
	}
	if version < 73 {
		columns, err := s.tableColumns(ctx, db, "scheduled_messages")
		if err != nil {
			return err
		}
		if !columns["blocks"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE scheduled_messages ADD COLUMN blocks TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate scheduled message blocks: %w", err)
			}
		}
	}
	if version < 74 {
		columns, err := s.tableColumns(ctx, db, "messages")
		if err != nil {
			return err
		}
		if !columns["attachments"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]'`); err != nil {
				return fmt.Errorf("migrate message attachments: %w", err)
			}
		}
		columns, err = s.tableColumns(ctx, db, "scheduled_messages")
		if err != nil {
			return err
		}
		if !columns["attachments"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE scheduled_messages ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]'`); err != nil {
				return fmt.Errorf("migrate scheduled message attachments: %w", err)
			}
		}
	}
	if version < 75 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS external_uploads (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), uploader_id TEXT NOT NULL REFERENCES users(id), name TEXT NOT NULL, title TEXT NOT NULL, mime_type TEXT NOT NULL, blob_key TEXT NOT NULL UNIQUE, size INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL, uploaded_at TEXT NOT NULL DEFAULT '', completed_at TEXT NOT NULL DEFAULT '')`); err != nil {
			return fmt.Errorf("migrate external uploads: %w", err)
		}
	}
	if version < 76 {
		columns, err := s.tableColumns(ctx, db, "external_uploads")
		if err != nil {
			return err
		}
		if !columns["file_id"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE external_uploads ADD COLUMN file_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate external upload file reference: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS file_shares (file_id TEXT NOT NULL REFERENCES files(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (file_id, conversation_id))`); err != nil {
			return fmt.Errorf("migrate file shares: %w", err)
		}
	}
	if version < 77 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS oidc_logout_tokens (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, token_id TEXT NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY (workspace_id, provider, token_id))`); err != nil {
			return fmt.Errorf("migrate OpenID Connect logout tokens: %w", err)
		}
	}
	if version < 78 && version > 0 {
		// Registering the rewrite rather than performing it. The rewrite itself
		// runs after this transaction commits and the fence is released; see
		// backfill.go for why an upgrade must not be an outage.
		//
		// A database at version 0 is one this release just created, so it cannot
		// contain a value in the old encoding. Registering twenty columns for it
		// left twenty rows in schema_backfills for ever and made
		// PendingBackfills report twenty pending rewrites on a database that has
		// never held a row.
		if err := registerBackfills(ctx, db, reEncodingBackfillNames()); err != nil {
			return err
		}
	}
	if version < 79 {
		if err := s.normalizeUserEmails(ctx, db); err != nil {
			return err
		}
	}
	if version < 80 {
		// Authorization codes were stored in plaintext and never expired. The
		// column cannot be converted in SQL — the hash is computed in Go — and a
		// plaintext credential must not survive the upgrade, so the outstanding
		// codes are discarded. An authorization code is single use and lives for
		// minutes, so the worst outcome is that an authorization started
		// seconds before the restart has to be restarted; leaving redeemable
		// plaintext in the database is not an acceptable alternative. Both
		// statements are idempotent, so concurrently starting replicas race
		// safely.
		columns, err := s.tableColumns(ctx, db, "oauth_codes")
		if err != nil {
			return err
		}
		if !columns["expires_at"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE oauth_codes ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("migrate oauth code expiry: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM oauth_codes`); err != nil {
			return fmt.Errorf("discard plaintext oauth codes: %w", err)
		}
	}
	// Step 81 registered a rewrite that truncated messages.created_at to the
	// microsecond in place. It is deliberately not reproduced here: step 83
	// registers the pass that replaces it, and running the truncation first would
	// merge identifiers that step 83 would then be unable to tell apart. A
	// deployment that already ran 81 arrives here with rows already merged; see
	// runMessageIdentityBackfill for what that pass can and cannot recover.
	if version < 82 {
		// One row per app to serialize Socket Mode admission. The previous
		// serializing statement wrote every live ticket of the app, which takes
		// the single write lock on the SQLite family but takes a row lock per
		// ticket on PostgreSQL — and concurrent admissions visited those rows in
		// different orders, so they deadlocked. A single row per app is ordered
		// by construction.
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS socket_mode_admission (app_id TEXT PRIMARY KEY, ticket INTEGER NOT NULL DEFAULT 0)`); err != nil {
			if !errors.Is(classify(err), store.ErrAlreadyExists) {
				return fmt.Errorf("create socket mode admission: %w", err)
			}
		}
	}
	if version < 83 {
		// schema_backfills.rejected exists so a pass can say "finished, and
		// skipped four values it could not decode". The shipped shape reported
		// nothing pending in exactly that state, which made the skip permanent
		// and invisible.
		columns, err := s.tableColumns(ctx, db, "schema_backfills")
		if err != nil {
			return err
		}
		if !columns["rejected"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE schema_backfills ADD COLUMN rejected INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("migrate backfill rejects: %w", err)
			}
		}
		// The pass that gives every message an identifier of its own and then
		// makes a merged identifier impossible. It is registered for a fresh
		// database too, because the UNIQUE index it installs is part of the
		// schema this release promises and a fresh database reaches it in one
		// empty scan.
		if err := registerBackfills(ctx, db, []string{messagesIdentityBackfill}); err != nil {
			return err
		}
	}
	if version < 84 {
		// The folded copies of every searchable value. Case-insensitive matching
		// cannot be delegated to the engine — SQLite and dqlite fold ASCII only,
		// PostgreSQL folds by locale, and the search paths folded the term in Go —
		// so a workspace could not find its own non-ASCII data on two of the four
		// profiles. See domain.FoldSearchText.
		//
		// The columns are added empty and filled by a registered pass rather than
		// by an UPDATE here: filling messages.text_folded for five million rows
		// inside the migration fence is the outage class backfill.go exists to
		// remove. While a pass is pending its rows are invisible to a search that
		// their unfolded text would have matched, which is a strictly smaller
		// window than the permanent divergence it replaces.
		for _, target := range foldedColumns {
			existing, err := s.tableColumns(ctx, db, target.table)
			if err != nil {
				return err
			}
			if existing[target.folded] {
				continue
			}
			if _, err := db.ExecContext(ctx, `ALTER TABLE `+target.table+` ADD COLUMN `+target.folded+` TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate %s.%s: %w", target.table, target.folded, err)
			}
		}
		// Registered for a fresh database too: the pass is one empty scan there,
		// and registering unconditionally keeps "which passes exist" a property of
		// the release rather than of the upgrade path a deployment happened to take.
		if err := registerBackfills(ctx, db, foldBackfillNames()); err != nil {
			return err
		}
		// The schema statement above created these tables empty. Marking their
		// passes complete inside the migration transaction avoids launching four
		// background writers that can race the first application writes on
		// SQLite. Existing databases still drain outside the startup fence.
		if freshDatabase {
			for _, name := range foldBackfillNames() {
				if _, err := db.ExecContext(ctx, `UPDATE schema_backfills SET done = 1 WHERE name = ?`, name); err != nil {
					return fmt.Errorf("complete empty backfill %s: %w", name, err)
				}
			}
		}
	}
	if version < 85 {
		// Channel names are workspace addresses, not display labels: creating a
		// second channel with the same normalized name made both the browser and
		// Slack API accept an address that could not identify one destination.
		// Direct conversations are excluded because their stored name is
		// deliberately the shared implementation value "direct".
		//
		// Releases before this constraint admitted duplicates. Rename only the
		// later rows in each duplicate group, record every repair for operators,
		// and then let the index make the invariant race-free in every profile.
		if err := repairDuplicateConversationNames(ctx, db); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS conversations_workspace_name ON conversations(workspace_id, name) WHERE is_direct = 0 AND is_group_direct = 0`); err != nil {
			return fmt.Errorf("index unique conversation names: %w", err)
		}
	}
	if version < 86 {
		columns, err := s.tableColumns(ctx, db, "tokens")
		if err != nil {
			return err
		}
		for _, column := range []string{
			"app_id TEXT NOT NULL DEFAULT ''",
			"bot_id TEXT NOT NULL DEFAULT ''",
			"token_type TEXT NOT NULL DEFAULT 'user'",
			"expires_at INTEGER NOT NULL DEFAULT 0",
		} {
			name := strings.Fields(column)[0]
			if columns[name] {
				continue
			}
			if _, err := db.ExecContext(ctx, `ALTER TABLE tokens ADD COLUMN `+column); err != nil {
				return fmt.Errorf("migrate token %s: %w", name, err)
			}
		}
		oauthColumns, err := s.tableColumns(ctx, db, "oauth_codes")
		if err != nil {
			return err
		}
		for _, column := range []string{
			"bot_id TEXT NOT NULL DEFAULT ''",
			"bot_user_id TEXT NOT NULL DEFAULT ''",
			"bot_scopes TEXT NOT NULL DEFAULT '[]'",
			"user_scopes TEXT NOT NULL DEFAULT '[]'",
		} {
			name := strings.Fields(column)[0]
			if oauthColumns[name] {
				continue
			}
			if _, err := db.ExecContext(ctx, `ALTER TABLE oauth_codes ADD COLUMN `+column); err != nil {
				return fmt.Errorf("migrate oauth code %s: %w", name, err)
			}
		}
	}
	if version < 89 {
		columns, err := s.tableColumns(ctx, db, "slack_apps")
		if err != nil {
			return err
		}
		if !columns["signing_secret_ciphertext"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE slack_apps ADD COLUMN signing_secret_ciphertext TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate application signing-secret ciphertext: %w", err)
			}
		}
	}
	if version < 90 {
		columns, err := s.tableColumns(ctx, db, "slack_apps")
		if err != nil {
			return err
		}
		if !columns["verification_token_ciphertext"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE slack_apps ADD COLUMN verification_token_ciphertext TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate application verification-token ciphertext: %w", err)
			}
		}
	}
	if version < 92 {
		columns, err := s.tableColumns(ctx, db, "messages")
		if err != nil {
			return err
		}
		if !columns["app_id"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN app_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate message application provenance: %w", err)
			}
		}
	}
	if version < 95 {
		columns, err := s.tableColumns(ctx, db, "views")
		if err != nil {
			return err
		}
		if !columns["app_id"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE views ADD COLUMN app_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate view application provenance: %w", err)
			}
		}
	}
	if version < 96 {
		if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS views_published_user`); err != nil {
			return fmt.Errorf("drop app-agnostic published-view index: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS views_workspace_app_external`); err != nil {
			return fmt.Errorf("drop invalid app-scoped view external-id index: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS views_workspace_external ON views(workspace_id, external_id) WHERE external_id <> ''`); err != nil {
			return fmt.Errorf("index workspace-scoped view external ids: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS views_published_user_app ON views(workspace_id, user_id, app_id, type, updated_at)`); err != nil {
			return fmt.Errorf("index app-scoped published views: %w", err)
		}
	}
	if version < 97 {
		columns, err := s.tableColumns(ctx, db, "views")
		if err != nil {
			return err
		}
		if !columns["errors"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE views ADD COLUMN errors TEXT NOT NULL DEFAULT '{}'`); err != nil {
				return fmt.Errorf("migrate durable view validation errors: %w", err)
			}
		}
	}
	if version < 98 {
		columns, err := s.tableColumns(ctx, db, "views")
		if err != nil {
			return err
		}
		if !columns["state"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE views ADD COLUMN state TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate durable view submission state: %w", err)
			}
		}
	}
	if version < 99 {
		columns, err := s.tableColumns(ctx, db, "messages")
		if err != nil {
			return err
		}
		for _, column := range []string{
			"metadata TEXT NOT NULL DEFAULT ''",
			"stream_state TEXT NOT NULL DEFAULT ''",
		} {
			name := strings.Fields(column)[0]
			if columns[name] {
				continue
			}
			if _, err := db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN `+column); err != nil {
				return fmt.Errorf("migrate message stream field %s: %w", name, err)
			}
		}
	}
	if version < 100 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_datastore_items (
			app_id TEXT NOT NULL REFERENCES slack_apps(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
			datastore TEXT NOT NULL, item_id TEXT NOT NULL, item TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY (app_id, workspace_id, datastore, item_id)
		)`); err != nil {
			return fmt.Errorf("migrate app datastore items: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS app_datastore_items_lookup ON app_datastore_items(app_id, workspace_id, datastore, item_id)`); err != nil {
			return fmt.Errorf("index app datastore items: %w", err)
		}
	}
	if version < 101 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS message_files (
			message_id TEXT NOT NULL REFERENCES messages(id), file_id TEXT NOT NULL REFERENCES files(id), position INTEGER NOT NULL,
			PRIMARY KEY (message_id, file_id), UNIQUE (message_id, position)
		)`); err != nil {
			return fmt.Errorf("migrate message file shares: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS message_files_file ON message_files(file_id, message_id)`); err != nil {
			return fmt.Errorf("index message file shares: %w", err)
		}
	}
	if version < 102 {
		columns, err := s.tableColumns(ctx, db, "scheduled_messages")
		if err != nil {
			return err
		}
		for _, column := range []string{
			"app_id TEXT NOT NULL DEFAULT ''",
			"bot_id TEXT NOT NULL DEFAULT ''",
			"credential_hash TEXT NOT NULL DEFAULT ''",
			"thread_ts TEXT NOT NULL DEFAULT ''",
			"delivered_at INTEGER NOT NULL DEFAULT 0",
			"failed_at INTEGER NOT NULL DEFAULT 0",
			"failure_code TEXT NOT NULL DEFAULT ''",
		} {
			name := strings.Fields(column)[0]
			if columns[name] {
				continue
			}
			if _, err := db.ExecContext(ctx, `ALTER TABLE scheduled_messages ADD COLUMN `+column); err != nil {
				return fmt.Errorf("migrate scheduled message field %s: %w", name, err)
			}
		}
		if _, err := db.ExecContext(ctx, `UPDATE scheduled_messages SET delivered_at = post_at WHERE delivered = 1 AND delivered_at = 0`); err != nil {
			return fmt.Errorf("backfill scheduled delivery state: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS scheduled_messages_credential ON scheduled_messages(workspace_id, credential_hash, post_at, id)`); err != nil {
			return fmt.Errorf("index scheduled message credentials: %w", err)
		}
	}
	if version < 103 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS saved_items (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			message_id TEXT NOT NULL REFERENCES messages(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
			state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE (workspace_id, user_id, message_id)
		)`); err != nil {
			return fmt.Errorf("migrate saved items: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS saved_items_user_state_updated ON saved_items(workspace_id, user_id, state, updated_at, id)`); err != nil {
			return fmt.Errorf("index saved items: %w", err)
		}
	}
	if version < 104 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS later_reminders (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), creator_id TEXT NOT NULL REFERENCES users(id),
			user_id TEXT NOT NULL DEFAULT '', channel_id TEXT NOT NULL DEFAULT '', source_message_id TEXT NOT NULL DEFAULT '',
			source_conversation_id TEXT NOT NULL DEFAULT '', source_timestamp TEXT NOT NULL DEFAULT '', target TEXT NOT NULL, text TEXT NOT NULL, due_at INTEGER NOT NULL,
			timezone TEXT NOT NULL, recurrence TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
			completed_at INTEGER NOT NULL DEFAULT 0, last_delivered_at INTEGER NOT NULL DEFAULT 0, acknowledged_at INTEGER NOT NULL DEFAULT 0, failed_at INTEGER NOT NULL DEFAULT 0,
			failure_code TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
			return fmt.Errorf("migrate first-party Later reminders: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS later_reminders_owner_due ON later_reminders(workspace_id, target, user_id, creator_id, due_at, id)`); err != nil {
			return fmt.Errorf("index first-party Later reminder owners: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS later_reminders_delivery ON later_reminders(workspace_id, completed_at, failed_at, due_at, id)`); err != nil {
			return fmt.Errorf("index first-party Later reminder delivery: %w", err)
		}
	}
	if version < 105 {
		columns, err := s.tableColumns(ctx, db, "workspace_members")
		if err != nil {
			return err
		}
		for _, column := range []string{
			"restricted INTEGER NOT NULL DEFAULT 0",
			"ultra_restricted INTEGER NOT NULL DEFAULT 0",
		} {
			name := strings.Fields(column)[0]
			if !columns[name] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE workspace_members ADD COLUMN `+column); err != nil {
					return fmt.Errorf("migrate workspace member %s: %w", name, err)
				}
			}
		}
	}
	if version < 106 {
		columns, err := s.tableColumns(ctx, db, "later_reminders")
		if err != nil {
			return err
		}
		if !columns["acknowledged_at"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE later_reminders ADD COLUMN acknowledged_at INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("migrate Later reminder acknowledgement: %w", err)
			}
		}
	}
	if version < 107 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS activity_items (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			actor_id TEXT NOT NULL DEFAULT '', conversation_id TEXT NOT NULL DEFAULT '', message_id TEXT NOT NULL DEFAULT '',
			reminder_id TEXT NOT NULL DEFAULT '', reaction_name TEXT NOT NULL DEFAULT '', occurred_at INTEGER NOT NULL,
			read_at INTEGER NOT NULL DEFAULT 0, cleared_at INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
			return fmt.Errorf("migrate Activity items: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS activity_items_user_time ON activity_items(workspace_id, user_id, cleared_at, occurred_at DESC, id DESC)`); err != nil {
			return fmt.Errorf("index Activity items: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS activity_item_kinds (
			activity_id TEXT NOT NULL REFERENCES activity_items(id) ON DELETE CASCADE, kind TEXT NOT NULL,
			PRIMARY KEY (activity_id, kind)
		)`); err != nil {
			return fmt.Errorf("migrate Activity item kinds: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS activity_item_kinds_filter ON activity_item_kinds(kind, activity_id)`); err != nil {
			return fmt.Errorf("index Activity kinds: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS activity_preferences (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			layout TEXT NOT NULL, PRIMARY KEY (workspace_id, user_id)
		)`); err != nil {
			return fmt.Errorf("migrate Activity preferences: %w", err)
		}
	}
	if version < 108 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS notification_preferences (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			level TEXT NOT NULL, keywords TEXT NOT NULL DEFAULT '[]',
			activity_channels INTEGER NOT NULL DEFAULT 1, activity_reminders INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (workspace_id, user_id)
		)`); err != nil {
			return fmt.Errorf("migrate workspace notification preferences: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS conversation_notification_preferences (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			conversation_id TEXT NOT NULL REFERENCES conversations(id), level TEXT NOT NULL DEFAULT 'inherit',
			follow_every_thread INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (workspace_id, user_id, conversation_id)
		)`); err != nil {
			return fmt.Errorf("migrate conversation notification preferences: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS conversation_notification_preferences_conversation ON conversation_notification_preferences(conversation_id, user_id)`); err != nil {
			return fmt.Errorf("index conversation notification preferences: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS thread_follows (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			conversation_id TEXT NOT NULL REFERENCES conversations(id), root_timestamp TEXT NOT NULL,
			PRIMARY KEY (workspace_id, user_id, conversation_id, root_timestamp)
		)`); err != nil {
			return fmt.Errorf("migrate thread follows: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS thread_follows_conversation_root ON thread_follows(conversation_id, root_timestamp, user_id)`); err != nil {
			return fmt.Errorf("index thread follows: %w", err)
		}
	}
	if version < 109 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS drafts (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			conversation_id TEXT NOT NULL REFERENCES conversations(id), thread_ts TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY (workspace_id, user_id, conversation_id, thread_ts)
		)`); err != nil {
			return fmt.Errorf("migrate drafts: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS drafts_owner_updated ON drafts(workspace_id, user_id, updated_at, conversation_id, thread_ts)`); err != nil {
			return fmt.Errorf("index drafts: %w", err)
		}
	}
	if version < 110 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS closed_direct_conversations (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			conversation_id TEXT NOT NULL REFERENCES conversations(id), closed_at TEXT NOT NULL,
			PRIMARY KEY (workspace_id, user_id, conversation_id)
		)`); err != nil {
			return fmt.Errorf("migrate closed direct conversations: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS closed_direct_conversations_conversation ON closed_direct_conversations(conversation_id, user_id)`); err != nil {
			return fmt.Errorf("index closed direct conversations: %w", err)
		}
	}
	if version < 111 {
		for _, target := range foldedColumns {
			if target.table != "files" {
				continue
			}
			existing, err := s.tableColumns(ctx, db, target.table)
			if err != nil {
				return err
			}
			if !existing[target.folded] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE files ADD COLUMN `+target.folded+` TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("migrate files.%s: %w", target.folded, err)
				}
			}
			name := target.table + "." + target.folded
			if err := registerBackfills(ctx, db, []string{name}); err != nil {
				return err
			}
			if freshDatabase {
				if _, err := db.ExecContext(ctx, `UPDATE schema_backfills SET done = 1 WHERE name = ?`, name); err != nil {
					return fmt.Errorf("complete empty backfill %s: %w", name, err)
				}
			}
		}
	}
	if version < 112 {
		existing, err := s.tableColumns(ctx, db, "users")
		if err != nil {
			return err
		}
		if !existing["status_expiration"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN status_expiration INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("migrate users.status_expiration: %w", err)
			}
		}
	}
	if version < 113 {
		existing, err := s.tableColumns(ctx, db, "users")
		if err != nil {
			return err
		}
		if !existing["active_scheduled_status_id"] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN active_scheduled_status_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("migrate users.active_scheduled_status_id: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS scheduled_statuses (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			status_text TEXT NOT NULL DEFAULT '', status_emoji TEXT NOT NULL DEFAULT '',
			starts_at INTEGER NOT NULL, ends_at INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
		)`); err != nil {
			return fmt.Errorf("migrate scheduled statuses: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS scheduled_statuses_owner_start ON scheduled_statuses(workspace_id, user_id, starts_at, id)`); err != nil {
			return fmt.Errorf("index scheduled status owners: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS scheduled_statuses_due ON scheduled_statuses(starts_at, id)`); err != nil {
			return fmt.Errorf("index scheduled statuses due: %w", err)
		}
	}
	if version < 114 {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS recent_searches (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
			query TEXT NOT NULL, searched_at INTEGER NOT NULL,
			PRIMARY KEY (workspace_id, user_id, query)
		)`); err != nil {
			return fmt.Errorf("migrate recent searches: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS recent_searches_user_time ON recent_searches(workspace_id, user_id, searched_at DESC, query)`); err != nil {
			return fmt.Errorf("index recent searches: %w", err)
		}
	}
	if version < 115 {
		for _, table := range []string{"canvases", "lists", "list_items"} {
			existing, err := s.tableColumns(ctx, db, table)
			if err != nil {
				return err
			}
			if !existing["version"] {
				if _, err := db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN version INTEGER NOT NULL DEFAULT 1`); err != nil {
					return fmt.Errorf("migrate %s optimistic revision: %w", table, err)
				}
			}
		}
	}
	if version < 116 {
		columns, err := s.tableColumns(ctx, db, "scheduled_messages")
		if err != nil {
			return err
		}
		for _, column := range []string{
			"metadata TEXT NOT NULL DEFAULT ''",
			"stream_state TEXT NOT NULL DEFAULT ''",
		} {
			name := strings.Fields(column)[0]
			if columns[name] {
				continue
			}
			if _, err := db.ExecContext(ctx, `ALTER TABLE scheduled_messages ADD COLUMN `+column); err != nil {
				return fmt.Errorf("migrate scheduled message field %s: %w", name, err)
			}
		}
	}
	// ON CONFLICT DO NOTHING rather than INSERT OR IGNORE: SQLite's OR IGNORE
	// suppresses every constraint class, while the PostgreSQL rewrite of it only
	// suppresses unique conflicts, so the two profiles disagreed about which
	// failures a backfill was allowed to skip.
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?) ON CONFLICT(version) DO NOTHING`, schemaVersion, domain.NewStoredTime(time.Now())); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version != schemaVersion {
		return fmt.Errorf("unsupported schema version %d (want %d)", version, schemaVersion)
	}
	return nil
}

func repairDuplicateConversationNames(ctx context.Context, db queryExecutor) error {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.workspace_id, c.name
		FROM conversations c
		JOIN (
			SELECT workspace_id, name
			FROM conversations
			WHERE is_direct = 0 AND is_group_direct = 0
			GROUP BY workspace_id, name
			HAVING COUNT(*) > 1
		) duplicates ON duplicates.workspace_id = c.workspace_id AND duplicates.name = c.name
		WHERE c.is_direct = 0 AND c.is_group_direct = 0
		ORDER BY c.workspace_id, c.name, c.id`)
	if err != nil {
		return fmt.Errorf("find duplicate conversation names: %w", err)
	}
	type duplicate struct {
		id        string
		workspace string
		name      string
	}
	values := make([]duplicate, 0)
	for rows.Next() {
		var value duplicate
		if err := rows.Scan(&value.id, &value.workspace, &value.name); err != nil {
			rows.Close()
			return fmt.Errorf("scan duplicate conversation name: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close duplicate conversation names: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read duplicate conversation names: %w", err)
	}
	previousKey := ""
	for _, value := range values {
		key := value.workspace + "\x00" + value.name
		if key != previousKey {
			previousKey = key
			continue
		}
		candidate := ""
		for attempt := 1; ; attempt++ {
			candidate = migrationConversationName(value.name, value.id, attempt)
			var taken int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE workspace_id = ? AND name = ?`, value.workspace, candidate).Scan(&taken); err != nil {
				return fmt.Errorf("check repaired conversation name %s: %w", value.id, err)
			}
			if taken == 0 {
				break
			}
		}
		if _, err := db.ExecContext(ctx, `UPDATE conversations SET name = ?, name_folded = ? WHERE id = ?`, candidate, domain.FoldSearchText(candidate), value.id); err != nil {
			return fmt.Errorf("disambiguate conversation %s: %w", value.id, err)
		}
		detail := fmt.Sprintf("renamed duplicate channel %q to %q before enforcing workspace name uniqueness", value.name, candidate)
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migration_notices(kind, subject, detail, observed_at) VALUES ('conversation_name_disambiguated', ?, ?, ?) ON CONFLICT(kind, subject) DO UPDATE SET detail = excluded.detail, observed_at = excluded.observed_at`, value.id, detail, domain.NewStoredTime(time.Now())); err != nil {
			return fmt.Errorf("record conversation name repair %s: %w", value.id, err)
		}
	}
	return nil
}

func migrationConversationName(name, id string, attempt int) string {
	suffix := "-" + strings.ToLower(strings.TrimSpace(id))
	if attempt > 1 {
		suffix += "-" + strconv.Itoa(attempt)
	}
	const maxChannelNameBytes = 80
	limit := maxChannelNameBytes - len(suffix)
	if limit < 1 {
		return strings.TrimPrefix(suffix[len(suffix)-maxChannelNameBytes:], "-")
	}
	prefix := strings.TrimSpace(name)
	if len(prefix) > limit {
		prefix = prefix[:limit]
		for prefix != "" && !utf8.ValidString(prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	prefix = strings.TrimRight(prefix, "-")
	if prefix == "" {
		prefix = "channel"
	}
	return prefix + suffix
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

func formatLifecycleWakeDeadline(deadline time.Time) domain.StoredTime {
	if deadline.IsZero() {
		return ""
	}
	return domain.NewStoredTime(deadline)
}

func parseLifecycleWakeDeadline(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	deadline, err := domain.ParseStoredTime(value)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode lifecycle wake deadline: %w", err)
	}
	return deadline.UTC(), nil
}

// normalizeUserEmails moves workspace e-mail identity off the database's
// lower() semantics and onto domain.NormalizeEmail. It rewrites stored
// addresses into the canonical form, resolves the collisions the old per-engine
// rule allowed, and replaces the expression index with a plain-column unique
// index so SQLite, dqlite and PostgreSQL enforce the same identity.
//
// Collisions are not hypothetical and they are not the operator's fault. The
// uniqueness guard before this step was the expression index users(workspace_id,
// lower(email)), and SQLite's and dqlite's lower() folds ASCII only — which is
// the exact divergence domain.NormalizeEmail exists to remove. So any SQLite or
// dqlite deployment that accepted "Ä@x.test" alongside "ä@x.test" is a
// deployment that the release which introduced this step could not start, ever,
// with an error that named two identifiers and stopped. There was no flag, no
// repair mode and no supported remedy short of hand-editing the database.
//
// The resolution is deterministic and reversible by an administrator: the lowest
// user identifier keeps the address, every other colliding account has its
// address cleared, and each cleared address is recorded as a migration notice
// naming the account that kept it. Clearing an address does not delete an
// account or its content; it removes one sign-in path that was already ambiguous
// on this database, and an administrator can reassign it. Refusing to start
// removes every sign-in path for everybody.
func (s *Store) normalizeUserEmails(ctx context.Context, db queryExecutor) error {
	rows, err := db.QueryContext(ctx, `SELECT id, workspace_id, email FROM users WHERE email <> ''`)
	if err != nil {
		return fmt.Errorf("read user emails: %w", err)
	}
	type account struct{ id, workspace, email string }
	var accounts []account
	for rows.Next() {
		var value account
		if err := rows.Scan(&value.id, &value.workspace, &value.email); err != nil {
			rows.Close()
			return fmt.Errorf("read user emails: %w", err)
		}
		accounts = append(accounts, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read user emails: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Sorted in Go, not by ORDER BY id. "The lowest identifier keeps the address"
	// is a byte rule, and ORDER BY evaluates it in the column's collation:
	// SQLite and dqlite use BINARY, PostgreSQL uses the database's default
	// collation, under which '_', '-' and case sort differently. Two deployments
	// of the same data could clear DIFFERENT accounts' addresses — the same
	// per-engine identity rule this whole step exists to abolish.
	sort.Slice(accounts, func(first, second int) bool { return accounts[first].id < accounts[second].id })
	type rewrite struct {
		id      string
		email   string
		cleared string
	}
	var rewrites []rewrite
	owners := make(map[string]string)
	for _, value := range accounts {
		normalized := domain.NormalizeEmail(value.email)
		key := value.workspace + "\x00" + normalized
		if existing, taken := owners[key]; taken {
			rewrites = append(rewrites, rewrite{id: value.id, email: "", cleared: fmt.Sprintf("%s (kept by user %q)", normalized, existing)})
			continue
		}
		owners[key] = value.id
		if normalized != value.email {
			rewrites = append(rewrites, rewrite{id: value.id, email: normalized})
		}
	}
	observed := s.now().UTC()
	for _, value := range rewrites {
		if _, err := db.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, value.email, value.id); err != nil {
			return fmt.Errorf("normalize user email %q: %w", value.id, err)
		}
		if value.cleared == "" {
			continue
		}
		if err := recordMigrationNotice(ctx, db, MigrationNoticeEmailCleared, value.id, value.cleared, observed); err != nil {
			return fmt.Errorf("record cleared user email %q: %w", value.id, err)
		}
		// Clearing an address removes a sign-in path. The notice is durable, but
		// an upgrade that silently takes a user's way in has to say so where an
		// operator is already looking.
		slog.Warn("upgrade cleared a colliding workspace e-mail address", "user", value.id, "detail", value.cleared, "remedy", "an administrator can reassign the address")
	}
	// The plain-column index is the portable identity: every backend compares the
	// same canonical bytes.
	if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS users_workspace_email_normalized ON users(workspace_id, email) WHERE email <> ''`); err != nil {
		return fmt.Errorf("index normalized user emails: %w", err)
	}
	// The lower(email) expression index was kept as "a backstop that catches an
	// ASCII case variant written by a path that skipped the normalizer". After
	// this change there is no such path — CreateUser, FindUserByEmail and
	// SeedUser all normalize, and the in-memory repository does too — and keeping
	// it left PostgreSQL applying its locale-aware lower() to the identity rule
	// while every other profile applied strings.ToLower. That is the last
	// per-engine identity rule in the schema, and it is exactly the divergence
	// class this step removes everywhere else.
	if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS users_workspace_email`); err != nil {
		return fmt.Errorf("drop the per-engine user email index: %w", err)
	}
	return nil
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
	if table != "outbox" && table != "messages" && table != "ephemeral_messages" && table != "sessions" && table != "users" && table != "workspace_members" && table != "workspaces" && table != "conversations" && table != "scheduled_messages" && table != "drafts" && table != "later_reminders" && table != "files" && table != "external_uploads" && table != "invite_requests" && table != "lifecycle_state" && table != "socket_mode_connections" && table != "list_downloads" && table != "oauth_codes" && table != "schema_backfills" && table != "tokens" && table != "slack_apps" && table != "views" && table != "recent_searches" && table != "canvases" && table != "lists" && table != "list_items" {
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
		return store.InvalidArgument("invalid workspace")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces(id, domain, name, description, discoverability, icon_url) VALUES (?, ?, ?, ?, ?, ?)`, value.ID, value.Domain, value.Name, value.Description, value.Discoverability, value.IconURL); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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

// workspaceColumn is the closed set of single-column workspace settings.
// SetWorkspaceName, SetWorkspaceDescription, SetWorkspaceDiscoverability and
// SetWorkspaceIcon were four byte-for-byte identical 26-line bodies differing
// only in this fragment, and all four re-read the row after COMMIT on s.db, so a
// concurrent setter's value could be returned to the caller that just wrote a
// different one. Collapsing them fixes the lost read once instead of four times.
type workspaceColumn string

const (
	workspaceColumnName            workspaceColumn = "name"
	workspaceColumnDescription     workspaceColumn = "description"
	workspaceColumnDiscoverability workspaceColumn = "discoverability"
	workspaceColumnIcon            workspaceColumn = "icon_url"
)

const selectWorkspaceStatement = `SELECT id, domain, name, description, discoverability, icon_url FROM workspaces WHERE id = ?`

func (s *Store) setWorkspaceColumn(ctx context.Context, id domain.WorkspaceID, column workspaceColumn, value any, event events.Event) (domain.Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET `+string(column)+` = ? WHERE id = ?`, value, id)
	if err != nil {
		return domain.Workspace{}, classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Workspace{}, err
	}
	if changed != 1 {
		return domain.Workspace{}, store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.Workspace{}, err
	}
	return s.commitWorkspace(ctx, tx, id)
}

// commitWorkspace reads the row back inside the transaction and only then
// commits, so the returned value is the value this call wrote.
func (s *Store) commitWorkspace(ctx context.Context, tx *sql.Tx, id domain.WorkspaceID) (domain.Workspace, error) {
	var value domain.Workspace
	if err := tx.QueryRowContext(ctx, selectWorkspaceStatement, id).Scan(&value.ID, &value.Domain, &value.Name, &value.Description, &value.Discoverability, &value.IconURL); err != nil {
		return domain.Workspace{}, translateNotFound(err)
	}
	value, err := s.withDefaultChannels(ctx, tx, value)
	if err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	return value, nil
}

func (s *Store) SetWorkspaceName(ctx context.Context, id domain.WorkspaceID, name string, event events.Event) (domain.Workspace, error) {
	return s.setWorkspaceColumn(ctx, id, workspaceColumnName, name, event)
}

func (s *Store) SetWorkspaceDescription(ctx context.Context, id domain.WorkspaceID, description string, event events.Event) (domain.Workspace, error) {
	return s.setWorkspaceColumn(ctx, id, workspaceColumnDescription, description, event)
}

func (s *Store) SetWorkspaceDiscoverability(ctx context.Context, id domain.WorkspaceID, discoverability domain.WorkspaceDiscoverability, event events.Event) (domain.Workspace, error) {
	if !discoverability.Valid() {
		return domain.Workspace{}, store.ErrInvalidArgument
	}
	return s.setWorkspaceColumn(ctx, id, workspaceColumnDiscoverability, discoverability, event)
}

func (s *Store) SetWorkspaceIcon(ctx context.Context, id domain.WorkspaceID, iconURL string, event events.Event) (domain.Workspace, error) {
	return s.setWorkspaceColumn(ctx, id, workspaceColumnIcon, iconURL, event)
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
	seen := make(map[domain.ConversationID]struct{}, len(channels))
	for _, channel := range channels {
		// A repeated channel violates the primary key, which used to surface as a
		// raw driver error; reject it as invalid input instead.
		if _, repeated := seen[channel]; repeated {
			return domain.Workspace{}, store.ErrInvalidArgument
		}
		seen[channel] = struct{}{}
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ? AND workspace_id = ? AND is_private = 0 AND is_direct = 0 AND is_group_direct = 0`, channel, id).Scan(&exists); err != nil {
			return domain.Workspace{}, translateNotFound(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_default_channels WHERE workspace_id = ?`, id); err != nil {
		return domain.Workspace{}, err
	}
	for _, channel := range channels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_default_channels(workspace_id, conversation_id) VALUES (?, ?)`, id, channel); err != nil {
			return domain.Workspace{}, classify(err)
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.Workspace{}, err
	}
	return s.commitWorkspace(ctx, tx, id)
}

func (s *Store) GetWorkspaceMembership(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.WorkspaceMembership, error) {
	var value domain.WorkspaceMembership
	var active, restricted, ultraRestricted int
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, user_id, role, active, restricted, ultra_restricted FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&value.WorkspaceID, &value.UserID, &value.Role, &active, &restricted, &ultraRestricted)
	value.Active = active != 0
	value.Restricted = restricted != 0
	value.UltraRestricted = ultraRestricted != 0
	return value, translateNotFound(err)
}

func (s *Store) GetUser(ctx context.Context, id domain.UserID) (domain.User, error) {
	var value domain.User
	var deleted int
	var statusExpiration int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, status_expiration, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE id = ?`, id).Scan(&value.ID, &value.WorkspaceID, &value.Email, &value.Name, &value.RealName, &value.Profile.DisplayName, &value.Profile.StatusText, &value.Profile.StatusEmoji, &statusExpiration, &value.Profile.Image24, &value.Profile.Image32, &value.Profile.Image48, &value.Profile.Image72, &value.Profile.Image192, &value.Profile.Image512, &value.Profile.Image1024, &deleted, &value.Presence)
	value.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
	value.Deleted = deleted != 0
	return value, translateNotFound(err)
}

func (s *Store) CreateUser(ctx context.Context, user domain.User, membership domain.WorkspaceMembership, event events.Event) error {
	if user.ID == "" || user.WorkspaceID == "" || user.Email == "" || user.Name == "" || membership.WorkspaceID != user.WorkspaceID || membership.UserID != user.ID || !membership.Active {
		return store.InvalidArgument("user and active workspace membership are required")
	}
	if membership.Role != domain.WorkspaceRoleMember && membership.Role != domain.WorkspaceRoleAdmin {
		return store.InvalidArgument("user membership role must be member or admin")
	}
	if (membership.Restricted && membership.UltraRestricted) || (membership.Guest() && membership.Role != domain.WorkspaceRoleMember) {
		return store.InvalidArgument("guest membership must have exactly one guest tier and the member role")
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
	user.Email = domain.NormalizeEmail(user.Email)
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE workspace_id = ? AND email = ? AND deleted = 0`, user.WorkspaceID, user.Email).Scan(&existing); err == nil {
		return store.ErrAlreadyExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if user.Presence == "" {
		user.Presence = domain.PresenceAuto
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, workspace_id, email, name, real_name, presence) VALUES (?, ?, ?, ?, ?, ?)`, user.ID, user.WorkspaceID, user.Email, user.Name, user.RealName, user.Presence); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role, active, restricted, ultra_restricted) VALUES (?, ?, ?, 1, ?, ?)`, membership.WorkspaceID, membership.UserID, membership.Role, boolInt(membership.Restricted), boolInt(membership.UltraRestricted)); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FindUserByEmail(ctx context.Context, workspace domain.WorkspaceID, email string) (domain.User, error) {
	var value domain.User
	var deleted int
	var statusExpiration int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, status_expiration, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE workspace_id = ? AND email = ? AND deleted = 0 LIMIT 1`, workspace, domain.NormalizeEmail(email)).Scan(&value.ID, &value.WorkspaceID, &value.Email, &value.Name, &value.RealName, &value.Profile.DisplayName, &value.Profile.StatusText, &value.Profile.StatusEmoji, &statusExpiration, &value.Profile.Image24, &value.Profile.Image32, &value.Profile.Image48, &value.Profile.Image72, &value.Profile.Image192, &value.Profile.Image512, &value.Profile.Image1024, &deleted, &value.Presence)
	value.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
	value.Deleted = deleted != 0
	return value, translateNotFound(err)
}

// UpdateUserProfile commits the profile change and every event given with it in
// one transaction. See store.Store.UpdateUserProfile for why the blob-delete
// instruction has to travel with the change rather than after it.
func (s *Store) UpdateUserProfile(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, profile domain.UserProfile, changes ...events.Event) (domain.User, error) {
	if len(changes) == 0 {
		return domain.User{}, store.InvalidArgument("a profile change requires at least one event")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET display_name = ?, active_scheduled_status_id = CASE WHEN status_text = ? AND status_emoji = ? AND status_expiration = ? THEN active_scheduled_status_id ELSE '' END, status_text = ?, status_emoji = ?, status_expiration = ?, image_24 = ?, image_32 = ?, image_48 = ?, image_72 = ?, image_192 = ?, image_512 = ?, image_1024 = ? WHERE id = ? AND workspace_id = ? AND deleted = 0 AND EXISTS (SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ? AND active = 1)`, profile.DisplayName, profile.StatusText, profile.StatusEmoji, unixSeconds(profile.StatusExpiration), profile.StatusText, profile.StatusEmoji, unixSeconds(profile.StatusExpiration), profile.Image24, profile.Image32, profile.Image48, profile.Image72, profile.Image192, profile.Image512, profile.Image1024, userID, workspaceID, workspaceID, userID)
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
	for _, change := range changes {
		if err := insertOutbox(ctx, tx, change); err != nil {
			return domain.User{}, err
		}
	}
	var user domain.User
	var deleted int
	var statusExpiration int64
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, status_expiration, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE id = ?`, userID).Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &statusExpiration, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
		return domain.User{}, err
	}
	user.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
	user.Deleted = deleted != 0
	if err := tx.Commit(); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Store) DueUserStatuses(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time, limit int) ([]domain.User, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("status expiration limit must be positive")
	}
	query := `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, status_expiration, active_scheduled_status_id, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence
		FROM users WHERE deleted = 0 AND status_expiration > 0 AND status_expiration <= ?`
	args := []any{now.UTC().Unix()}
	if workspaceID != "" {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}
	query += ` ORDER BY status_expiration, id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]domain.User, 0, limit)
	for rows.Next() {
		var user domain.User
		var deleted int
		var statusExpiration int64
		if err := rows.Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &statusExpiration, &user.Profile.ActiveScheduledStatusID, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
			return nil, err
		}
		user.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
		user.Deleted = deleted != 0
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

func (s *Store) EarliestUserStatusExpiration(ctx context.Context, workspaceID domain.WorkspaceID) (time.Time, error) {
	query := `SELECT COALESCE(MIN(status_expiration), 0) FROM users WHERE deleted = 0 AND status_expiration > 0`
	args := []any{}
	if workspaceID != "" {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}
	var expiration int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&expiration); err != nil {
		return time.Time{}, err
	}
	return fromUnixSeconds(expiration), nil
}

func (s *Store) ExpireUserStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, expected time.Time, expectedScheduledID domain.ScheduledStatusID, now time.Time, event events.Event) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET status_text = '', status_emoji = '', status_expiration = 0, active_scheduled_status_id = ''
		WHERE id = ? AND workspace_id = ? AND deleted = 0 AND status_expiration = ? AND active_scheduled_status_id = ? AND status_expiration > 0 AND status_expiration <= ?`,
		userID, workspaceID, expected.UTC().Unix(), expectedScheduledID, now.UTC().Unix())
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, nil
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func scanScheduledStatus(scanner interface{ Scan(...any) error }) (domain.ScheduledStatus, error) {
	var value domain.ScheduledStatus
	var startsAt, endsAt, createdAt, updatedAt int64
	if err := scanner.Scan(&value.ID, &value.WorkspaceID, &value.UserID, &value.StatusText, &value.StatusEmoji, &startsAt, &endsAt, &createdAt, &updatedAt); err != nil {
		return domain.ScheduledStatus{}, translateNotFound(err)
	}
	value.StartsAt = fromUnixSeconds(startsAt)
	value.EndsAt = fromUnixSeconds(endsAt)
	value.CreatedAt = time.Unix(0, createdAt).UTC()
	value.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return value, nil
}

func (s *Store) CreateScheduledStatus(ctx context.Context, value domain.ScheduledStatus) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lock, err := tx.ExecContext(ctx, `UPDATE users SET id = id WHERE id = ? AND workspace_id = ? AND deleted = 0 AND EXISTS (
		SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ? AND active = 1
	)`, value.UserID, value.WorkspaceID, value.WorkspaceID, value.UserID)
	if err != nil {
		return err
	}
	if changed, err := lock.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrNotFound
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_statuses WHERE workspace_id = ? AND user_id = ?`, value.WorkspaceID, value.UserID).Scan(&count); err != nil {
		return err
	}
	if count >= 5 {
		return store.ErrScheduledStatusLimit
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO scheduled_statuses(id, workspace_id, user_id, status_text, status_emoji, starts_at, ends_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.UserID, value.StatusText, value.StatusEmoji, value.StartsAt.UTC().Unix(), value.EndsAt.UTC().Unix(), value.CreatedAt.UTC().UnixNano(), value.UpdatedAt.UTC().UnixNano())
	if err != nil {
		return classify(err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) GetScheduledStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID) (domain.ScheduledStatus, error) {
	return scanScheduledStatus(s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, status_text, status_emoji, starts_at, ends_at, created_at, updated_at
		FROM scheduled_statuses WHERE id = ? AND workspace_id = ? AND user_id = ?`, id, workspaceID, userID))
}

func (s *Store) ListScheduledStatuses(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.ScheduledStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, status_text, status_emoji, starts_at, ends_at, created_at, updated_at
		FROM scheduled_statuses WHERE workspace_id = ? AND user_id = ? ORDER BY starts_at, id`, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.ScheduledStatus, 0, 5)
	for rows.Next() {
		value, err := scanScheduledStatus(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) UpdateScheduledStatus(ctx context.Context, value domain.ScheduledStatus) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_statuses SET status_text = ?, status_emoji = ?, starts_at = ?, ends_at = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND user_id = ?`, value.StatusText, value.StatusEmoji, value.StartsAt.UTC().Unix(), value.EndsAt.UTC().Unix(), value.UpdatedAt.UTC().UnixNano(), value.ID, value.WorkspaceID, value.UserID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) DeleteScheduledStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM scheduled_statuses WHERE id = ? AND workspace_id = ? AND user_id = ?`, id, workspaceID, userID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) DueScheduledStatuses(ctx context.Context, workspaceID domain.WorkspaceID, now time.Time, limit int) ([]domain.ScheduledStatus, error) {
	if limit <= 0 {
		return nil, store.InvalidArgument("scheduled status limit must be positive")
	}
	query := `SELECT id, workspace_id, user_id, status_text, status_emoji, starts_at, ends_at, created_at, updated_at
		FROM scheduled_statuses WHERE starts_at <= ?`
	args := []any{now.UTC().Unix()}
	if workspaceID != "" {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}
	query += ` ORDER BY starts_at, id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.ScheduledStatus, 0, limit)
	for rows.Next() {
		value, err := scanScheduledStatus(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) EarliestScheduledStatusStart(ctx context.Context, workspaceID domain.WorkspaceID) (time.Time, error) {
	query := `SELECT COALESCE(MIN(starts_at), 0) FROM scheduled_statuses`
	args := []any{}
	if workspaceID != "" {
		query += ` WHERE workspace_id = ?`
		args = append(args, workspaceID)
	}
	var startsAt int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&startsAt); err != nil {
		return time.Time{}, err
	}
	return fromUnixSeconds(startsAt), nil
}

func (s *Store) ActivateScheduledStatus(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ScheduledStatusID, expectedUpdatedAt, now time.Time, event events.Event) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	value, err := scanScheduledStatus(tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, status_text, status_emoji, starts_at, ends_at, created_at, updated_at
		FROM scheduled_statuses WHERE id = ? AND workspace_id = ? AND user_id = ?`, id, workspaceID, userID))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !value.UpdatedAt.Equal(expectedUpdatedAt.UTC()) || value.StartsAt.After(now.UTC()) {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM scheduled_statuses WHERE id = ? AND workspace_id = ? AND user_id = ? AND updated_at = ? AND starts_at <= ?`,
		id, workspaceID, userID, expectedUpdatedAt.UTC().UnixNano(), now.UTC().Unix())
	if err != nil {
		return false, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return false, err
	} else if changed != 1 {
		return false, nil
	}
	if value.EndsAt.After(now.UTC()) {
		result, err = tx.ExecContext(ctx, `UPDATE users SET status_text = ?, status_emoji = ?, status_expiration = ?, active_scheduled_status_id = ?
			WHERE id = ? AND workspace_id = ? AND deleted = 0`, value.StatusText, value.StatusEmoji, value.EndsAt.UTC().Unix(), value.ID, userID, workspaceID)
		if err != nil {
			return false, err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return false, err
		} else if changed != 1 {
			return false, store.ErrNotFound
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) SetUserPresence(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, presence domain.Presence, event events.Event) (domain.User, error) {
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		return domain.User{}, store.InvalidArgument("invalid user presence")
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.User{}, err
	}
	var user domain.User
	var deleted int
	var statusExpiration int64
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, status_expiration, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE id = ?`, userID).Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &statusExpiration, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
		return domain.User{}, err
	}
	user.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		if _, err := tx.ExecContext(ctx, revokeSessionsStatement+` WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID); err != nil {
			return err
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetWorkspaceRole(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole, event events.Event) error {
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return store.InvalidArgument("invalid workspace role")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var restricted, ultraRestricted int
	if err := tx.QueryRowContext(ctx, `SELECT restricted, ultra_restricted FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&restricted, &ultraRestricted); err != nil {
		return translateNotFound(err)
	}
	if (restricted != 0 || ultraRestricted != 0) && role != domain.WorkspaceRoleMember {
		return store.InvalidArgument("guest membership cannot be promoted")
	}
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	query := `SELECT id, workspace_id, email, name, real_name, display_name, status_text, status_emoji, status_expiration, image_24, image_32, image_48, image_72, image_192, image_512, image_1024, deleted, presence FROM users WHERE workspace_id = ?`
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
		var statusExpiration int64
		if err := rows.Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &statusExpiration, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
			return domain.UserPage{}, err
		}
		user.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.AdminUserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.AdminUserPage{}, err
	}
	query := `SELECT u.id, u.workspace_id, u.email, u.name, u.real_name, u.display_name, u.status_text, u.status_emoji, u.status_expiration, u.image_24, u.image_32, u.image_48, u.image_72, u.image_192, u.image_512, u.image_1024, u.deleted, u.presence, m.role, m.active, m.restricted, m.ultra_restricted FROM users u JOIN workspace_members m ON m.user_id = u.id AND m.workspace_id = u.workspace_id WHERE u.workspace_id = ?`
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
		var deleted, active, restricted, ultraRestricted int
		var statusExpiration int64
		if err := rows.Scan(&value.User.ID, &value.User.WorkspaceID, &value.User.Email, &value.User.Name, &value.User.RealName, &value.User.Profile.DisplayName, &value.User.Profile.StatusText, &value.User.Profile.StatusEmoji, &statusExpiration, &value.User.Profile.Image24, &value.User.Profile.Image32, &value.User.Profile.Image48, &value.User.Profile.Image72, &value.User.Profile.Image192, &value.User.Profile.Image512, &value.User.Profile.Image1024, &deleted, &value.User.Presence, &value.Membership.Role, &active, &restricted, &ultraRestricted); err != nil {
			return domain.AdminUserPage{}, err
		}
		value.User.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
		value.User.Deleted = deleted != 0
		value.Membership.WorkspaceID = workspace
		value.Membership.UserID = value.User.ID
		value.Membership.Active = active != 0
		value.Membership.Restricted = restricted != 0
		value.Membership.UltraRestricted = ultraRestricted != 0
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserPage{}, err
	}
	query := `SELECT u.id, u.workspace_id, u.email, u.name, u.real_name, u.display_name, u.status_text, u.status_emoji, u.status_expiration, u.image_24, u.image_32, u.image_48, u.image_72, u.image_192, u.image_512, u.image_1024, u.deleted, u.presence FROM users u JOIN workspace_members m ON m.user_id = u.id AND m.workspace_id = u.workspace_id WHERE u.workspace_id = ? AND m.role = ? AND m.active = 1 AND u.deleted = 0`
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
		var statusExpiration int64
		if err := rows.Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &statusExpiration, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
			return domain.UserPage{}, err
		}
		user.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserPage{}, err
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
	query := `SELECT u.id, u.workspace_id, u.email, u.name, u.real_name, u.display_name, u.status_text, u.status_emoji, u.status_expiration, u.image_24, u.image_32, u.image_48, u.image_72, u.image_192, u.image_512, u.image_1024, u.deleted, u.presence FROM users u JOIN conversation_members m ON m.user_id = u.id WHERE m.conversation_id = ? AND u.deleted = 0`
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
		var statusExpiration int64
		if err := rows.Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.Name, &user.RealName, &user.Profile.DisplayName, &user.Profile.StatusText, &user.Profile.StatusEmoji, &statusExpiration, &user.Profile.Image24, &user.Profile.Image32, &user.Profile.Image48, &user.Profile.Image72, &user.Profile.Image192, &user.Profile.Image512, &user.Profile.Image1024, &deleted, &user.Presence); err != nil {
			return domain.UserPage{}, err
		}
		user.Profile.StatusExpiration = fromUnixSeconds(statusExpiration)
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
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT t.workspace_id, t.user_id, t.app_id, t.bot_id, t.scopes, t.token_type, t.expires_at, t.revoked FROM tokens t WHERE t.token_hash = ? AND NOT EXISTS (SELECT 1 FROM user_expirations e WHERE e.user_id = t.user_id AND e.workspace_id = t.workspace_id AND e.expiration_ts > 0 AND e.expiration_ts <= ?)`, domain.HashToken(token), time.Now().UTC().Unix()).Scan(&record.WorkspaceID, &record.UserID, &record.AppID, &record.BotID, &scopes, &record.TokenType, &expiresAt, &revoked)
	if err != nil {
		return domain.TokenRecord{}, translateNotFound(err)
	}
	record.Scopes = domain.NormalizeScopes(strings.Fields(scopes))
	record.Revoked = revoked != 0
	if expiresAt > 0 {
		record.ExpiresAt = time.Unix(0, expiresAt).UTC()
	}
	return record, nil
}

func (s *Store) SeedAppToken(ctx context.Context, token string, record domain.AppTokenRecord) error {
	if record.AppID == "" {
		return store.InvalidArgument("app token requires an app ID")
	}
	revoked := 0
	if record.Revoked {
		revoked = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_tokens(token_hash, app_id, scopes, revoked) VALUES (?, ?, ?, ?) ON CONFLICT(token_hash) DO NOTHING`, domain.HashToken(token), record.AppID, strings.Join(domain.NormalizeScopes(record.Scopes), " "), revoked)
	return err
}

func (s *Store) CreateAppToken(ctx context.Context, token string, record domain.AppTokenRecord) error {
	record.Scopes = domain.NormalizeScopes(record.Scopes)
	if strings.TrimSpace(token) == "" || record.AppID == "" || len(record.Scopes) == 0 || record.Revoked {
		return store.InvalidArgument("invalid app token")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO app_tokens(token_hash, app_id, scopes, revoked) SELECT ?, ?, ?, 0 WHERE EXISTS (SELECT 1 FROM slack_apps WHERE id = ? AND deleted = 0)`, domain.HashToken(token), record.AppID, strings.Join(record.Scopes, " "), record.AppID)
	if err != nil {
		return classify(err)
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(session_hash, workspace_id, user_id, scopes, expires_at, revoked, oidc_provider, oidc_id_token, oidc_subject, oidc_sid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(session_hash) DO NOTHING`, domain.HashToken(token), record.WorkspaceID, record.UserID, strings.Join(domain.NormalizeScopes(record.Scopes), " "), domain.NewStoredTime(record.ExpiresAt), revoked, record.OIDCProvider, record.OIDCIDToken, record.OIDCSubject, record.OIDCSID)
	return err
}

func (s *Store) CreateSession(ctx context.Context, token string, record domain.SessionRecord) error {
	if strings.TrimSpace(token) == "" || record.WorkspaceID == "" || record.UserID == "" || record.ExpiresAt.IsZero() || !record.ExpiresAt.After(time.Now().UTC()) || len(domain.NormalizeScopes(record.Scopes)) == 0 {
		return store.InvalidArgument("invalid session")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO sessions(session_hash, workspace_id, user_id, scopes, expires_at, revoked, oidc_provider, oidc_id_token, oidc_subject, oidc_sid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(session_hash) DO NOTHING`, domain.HashToken(token), record.WorkspaceID, record.UserID, strings.Join(domain.NormalizeScopes(record.Scopes), " "), domain.NewStoredTime(record.ExpiresAt), boolInt(record.Revoked), record.OIDCProvider, record.OIDCIDToken, record.OIDCSubject, record.OIDCSID)
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
	record.ExpiresAt, err = domain.ParseStoredTime(expires)
	if err != nil {
		return domain.SessionRecord{}, err
	}
	record.Revoked = revoked != 0
	record.Scopes = domain.NormalizeScopes(strings.Fields(scopes))
	return record, nil
}

// GetAuthMethod reports the stored administrative override for an authorization
// provider. A provider with no row reports Enabled: true and a nil error. See
// store.Store.GetAuthMethod for the decision and the reason: absence means "no
// administrator has turned this provider off", not "no such provider", because
// provider existence is decided by the operator's startup configuration and
// absence-means-disabled locks a fresh deployment out with no bootstrap path.
//
// This is the documented exception to the repository's absent-row-is-ErrNotFound
// convention, and it is deliberate.
func (s *Store) GetAuthMethod(ctx context.Context, workspace domain.WorkspaceID, provider string) (domain.AuthMethod, error) {
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM auth_methods WHERE workspace_id = ? AND provider = ?`, workspace, provider).Scan(&enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Absence means "no administrative override", not "disabled". This
			// table records an administrator's decision to turn a provider OFF;
			// whether a provider exists at all is decided by the operator's
			// startup configuration (issuer, client id), so a provider with no
			// row is reachable only if the operator configured it.
			//
			// Do not invert this to fail closed. A new deployment has no rows,
			// so absence-means-disabled disables every provider, and the
			// administrator who would re-enable them cannot sign in either.
			// There is no bootstrap path out of that state.
			return domain.AuthMethod{WorkspaceID: workspace, Provider: provider, Enabled: true}, nil
		}
		return domain.AuthMethod{}, err
	}
	return domain.AuthMethod{WorkspaceID: workspace, Provider: provider, Enabled: enabled != 0}, nil
}

func (s *Store) SetAuthMethod(ctx context.Context, value domain.AuthMethod) error {
	if value.WorkspaceID == "" || value.Provider == "" {
		return store.InvalidArgument("invalid auth method")
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
		return store.InvalidArgument("invalid external identity")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO external_identities(workspace_id, provider, subject, user_id) VALUES (?, ?, ?, ?)`, value.WorkspaceID, value.Provider, value.Subject, value.UserID)
	return classify(err)
}

// revokeSessionsStatement is the only way a session is revoked. Revocation
// clears oidc_id_token in the same statement: a revoked session must retain no
// provider credential, and the identity token left behind by the previous
// UPDATE was a signed bearer assertion for the user that outlived the session it
// belonged to. Every revocation path shares the fragment so a new one cannot
// forget the credential.
const revokeSessionsStatement = `UPDATE sessions SET revoked = 1, oidc_id_token = ''`

// deleteExpiredSessionsStatement drops sessions whose expiry has passed. It runs
// on the same schedule as the oidc_logout_tokens sweep in RevokeOIDCSessions,
// which is the only maintenance schedule this table has; an expired session is
// unusable, so retaining its provider metadata serves nothing.
const deleteExpiredSessionsStatement = `DELETE FROM sessions WHERE expires_at <> '' AND expires_at <= ?`

func (s *Store) RevokeSession(ctx context.Context, token string) error {
	result, err := s.db.ExecContext(ctx, revokeSessionsStatement+` WHERE session_hash = ?`, domain.HashToken(token))
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

func (s *Store) RevokeOIDCSessions(ctx context.Context, workspaceID domain.WorkspaceID, provider, subject, sid, tokenID string, expiresAt time.Time, event events.Event) error {
	if workspaceID == "" || strings.TrimSpace(provider) == "" || (strings.TrimSpace(subject) == "" && strings.TrimSpace(sid) == "") || strings.TrimSpace(tokenID) == "" || !expiresAt.After(time.Now().UTC()) {
		return store.ErrInvalidArgument
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Unix()
	if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_logout_tokens WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, deleteExpiredSessionsStatement, domain.NewStoredTime(time.Now().UTC())); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oidc_logout_tokens(workspace_id, provider, token_id, expires_at) VALUES (?, ?, ?, ?)`, workspaceID, provider, tokenID, expiresAt.UTC().Unix()); err != nil {
		// A replayed logout token is a state conflict, not a duplicate resource.
		if classified := classify(err); errors.Is(classified, store.ErrAlreadyExists) {
			return store.ErrConflict
		}
		return classify(err)
	}
	statement := revokeSessionsStatement + ` WHERE workspace_id = ? AND oidc_provider = ? AND revoked = 0`
	arguments := []any{workspaceID, provider}
	if sid != "" {
		statement += ` AND oidc_sid = ?`
		arguments = append(arguments, sid)
	}
	if subject != "" {
		statement += ` AND oidc_subject = ?`
		arguments = append(arguments, subject)
	}
	result, err := tx.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed > 0 {
		if err := insertOutbox(ctx, tx, event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RevokeUserSessions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, revokeSessionsStatement+` WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return store.InvalidArgument("invalid direct conversation")
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
	result, err := tx.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name, is_private, is_direct, is_group_direct, direct_key, name_folded) VALUES (?, ?, ?, 1, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, conversation.ID, conversation.WorkspaceID, conversation.Name, direct, groupDirect, domain.DirectConversationKey(conversation.WorkspaceID, members), domain.FoldSearchText(conversation.Name))
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
			return store.InvalidArgument("direct conversation contains duplicate members")
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ExpandDirectConversation(ctx context.Context, expansion domain.DirectConversationExpansion, emitted []events.Event) error {
	if !expansion.History.Valid() || len(emitted) != 3 || !expansion.Target.IsPrivate || expansion.Target.IsDirect || !expansion.Target.IsGroupDirect || len(expansion.Members) < 3 || len(expansion.Members) > 9 {
		return store.InvalidArgument("invalid direct conversation expansion")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var sourceWorkspace domain.WorkspaceID
	var sourceDirect, sourceGroupDirect int
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id, is_direct, is_group_direct FROM conversations WHERE id = ?`, expansion.Source).Scan(&sourceWorkspace, &sourceDirect, &sourceGroupDirect); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
	if sourceWorkspace != expansion.Target.WorkspaceID || sourceDirect == 0 && sourceGroupDirect == 0 {
		return store.ErrNotFound
	}
	sourceMembers, err := conversationMemberSet(ctx, tx, expansion.Source)
	if err != nil {
		return err
	}
	memberSet := make(map[domain.UserID]struct{}, len(expansion.Members))
	for _, member := range expansion.Members {
		if _, duplicate := memberSet[member]; duplicate {
			return store.InvalidArgument("expanded conversation contains duplicate members")
		}
		memberSet[member] = struct{}{}
	}
	for member := range sourceMembers {
		if _, retained := memberSet[member]; !retained {
			return store.InvalidArgument("expanded conversation removed a source member")
		}
	}
	if len(memberSet) <= len(sourceMembers) {
		return store.InvalidArgument("expanded conversation adds no members")
	}
	if expansion.SourceNotice.Conversation != expansion.Source || expansion.TargetNotice.Conversation != expansion.Target.ID ||
		expansion.SourceNotice.WorkspaceID != expansion.Target.WorkspaceID || expansion.TargetNotice.WorkspaceID != expansion.Target.WorkspaceID {
		return store.InvalidArgument("invalid direct conversation notices")
	}

	result, err := tx.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name, is_private, is_direct, is_group_direct, direct_key, name_folded) VALUES (?, ?, ?, 1, 0, 1, ?, ?) ON CONFLICT DO NOTHING`,
		expansion.Target.ID, expansion.Target.WorkspaceID, expansion.Target.Name, domain.DirectConversationKey(expansion.Target.WorkspaceID, expansion.Members), domain.FoldSearchText(expansion.Target.Name))
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrAlreadyExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_teams(conversation_id, team_id, org_channel) VALUES (?, ?, 0)`, expansion.Target.ID, expansion.Target.WorkspaceID); err != nil {
		return classify(err)
	}
	for _, member := range expansion.Members {
		result, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id)
			SELECT ?, u.id FROM users u JOIN workspace_members wm ON wm.user_id = u.id AND wm.workspace_id = u.workspace_id
			WHERE u.id = ? AND u.workspace_id = ? AND u.deleted = 0 AND wm.active = 1`,
			expansion.Target.ID, member, expansion.Target.WorkspaceID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return err
		} else if changed != 1 {
			return store.ErrNotFound
		}
	}
	if expansion.History == domain.DirectHistoryAll {
		history, err := directHistoryRows(ctx, tx, expansion.Source)
		if err != nil {
			return err
		}
		for _, original := range history {
			copyID, err := domain.NewMessageID()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, conversation, author_id, app_id, text, blocks, attachments, metadata, stream_state, thread_timestamp, created_at, deleted, unfurls, text_folded)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
				copyID, original.workspaceID, expansion.Target.ID, original.authorID, original.appID, original.text, original.blocks, original.attachments, original.metadata, original.streamState, original.threadTimestamp, original.createdAt, original.unfurls, original.textFolded); err != nil {
				return classify(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO message_files(message_id, file_id, position)
				SELECT ?, file_id, position FROM message_files WHERE message_id = ?`, copyID, original.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO file_shares(file_id, conversation_id)
				SELECT file_id, ? FROM message_files WHERE message_id = ? ON CONFLICT(file_id, conversation_id) DO NOTHING`, expansion.Target.ID, original.id); err != nil {
				return err
			}
		}
	}
	if err := insertOutbox(ctx, tx, emitted[0]); err != nil {
		return err
	}
	if err := insertFileShareMessage(ctx, tx, expansion.SourceNotice, emitted[1]); err != nil {
		return err
	}
	if err := insertFileShareMessage(ctx, tx, expansion.TargetNotice, emitted[2]); err != nil {
		return err
	}
	return tx.Commit()
}

type directHistoryRow struct {
	id              domain.MessageID
	workspaceID     domain.WorkspaceID
	authorID        domain.UserID
	appID           domain.AppID
	text            string
	blocks          string
	attachments     string
	metadata        string
	streamState     string
	threadTimestamp domain.MessageTimestamp
	createdAt       string
	unfurls         string
	textFolded      string
}

func directHistoryRows(ctx context.Context, tx *sql.Tx, conversation domain.ConversationID) ([]directHistoryRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, workspace_id, author_id, app_id, text, blocks, attachments, metadata, stream_state, thread_timestamp, created_at, unfurls, text_folded
		FROM messages WHERE conversation = ? AND deleted = 0 ORDER BY created_at, id`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]directHistoryRow, 0)
	for rows.Next() {
		var value directHistoryRow
		if err := rows.Scan(&value.id, &value.workspaceID, &value.authorID, &value.appID, &value.text, &value.blocks, &value.attachments, &value.metadata, &value.streamState, &value.threadTimestamp, &value.createdAt, &value.unfurls, &value.textFolded); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func conversationMemberSet(ctx context.Context, tx *sql.Tx, conversation domain.ConversationID) (map[domain.UserID]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id FROM conversation_members WHERE conversation_id = ?`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[domain.UserID]struct{})
	for rows.Next() {
		var user domain.UserID
		if err := rows.Scan(&user); err != nil {
			return nil, err
		}
		values[user] = struct{}{}
	}
	return values, rows.Err()
}

func (s *Store) SetDirectConversationOpen(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, open bool, event events.Event) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var direct, groupDirect int
	if err := tx.QueryRowContext(ctx, `SELECT c.is_direct, c.is_group_direct
		FROM conversations c
		JOIN conversation_members m ON m.conversation_id = c.id AND m.user_id = ?
		WHERE c.id = ? AND c.workspace_id = ?`, user, conversation, workspace).Scan(&direct, &groupDirect); errors.Is(err, sql.ErrNoRows) {
		return false, store.ErrNotFound
	} else if err != nil {
		return false, err
	}
	if direct == 0 && groupDirect == 0 {
		return false, store.ErrNotFound
	}
	var result sql.Result
	if open {
		result, err = tx.ExecContext(ctx, `DELETE FROM closed_direct_conversations WHERE workspace_id = ? AND user_id = ? AND conversation_id = ?`, workspace, user, conversation)
	} else {
		result, err = tx.ExecContext(ctx, `INSERT INTO closed_direct_conversations(workspace_id, user_id, conversation_id, closed_at)
			VALUES (?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, conversation_id) DO NOTHING`, workspace, user, conversation, domain.NewStoredTime(time.Now()))
	}
	if err != nil {
		return false, classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, nil
	}
	if err := insertOutboxForConversation(ctx, tx, event, conversation); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name, is_private, name_folded) VALUES (?, ?, ?, ?, ?)`, conversation.ID, conversation.WorkspaceID, conversation.Name, private, domain.FoldSearchText(conversation.Name)); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_teams(conversation_id, team_id, org_channel) VALUES (?, ?, 0)`, conversation.ID, conversation.WorkspaceID); err != nil {
		return classify(err)
	}
	// The creator joins the conversation, public or private. See the note on the
	// in-memory repository: joining only private conversations left the creator
	// of a public channel unable to act on it.
	//
	// ON CONFLICT DO NOTHING like every other membership insert in this file. The
	// in-memory repository is idempotent by construction, so without it the two
	// profiles disagree about a second attempt.
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) VALUES (?, ?) ON CONFLICT(conversation_id, user_id) DO NOTHING`, conversation.ID, creator); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET name = ?, name_folded = ? WHERE id = ?`, name, domain.FoldSearchText(name), conversation)
	if err != nil {
		return domain.Conversation{}, classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Conversation{}, err
	}
	if changed != 1 {
		return domain.Conversation{}, store.ErrNotFound
	}
	if err := insertOutboxForConversation(ctx, tx, event, conversation); err != nil {
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
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET topic = ?, topic_folded = ? WHERE id = ?`, topic, domain.FoldSearchText(topic), conversation)
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
	if err := insertOutboxForConversation(ctx, tx, event, conversation); err != nil {
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
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET purpose = ?, purpose_folded = ? WHERE id = ?`, purpose, domain.FoldSearchText(purpose), conversation)
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
	if err := insertOutboxForConversation(ctx, tx, event, conversation); err != nil {
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
	if err := insertOutboxForConversation(ctx, tx, event, conversation); err != nil {
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
		`DELETE FROM file_shares WHERE conversation_id = ?`,
		`DELETE FROM conversation_teams WHERE conversation_id = ?`,
		`DELETE FROM user_group_channels WHERE conversation_id = ?`,
		`DELETE FROM conversation_access_groups WHERE conversation_id = ?`,
		`DELETE FROM workspace_default_channels WHERE conversation_id = ?`,
		`DELETE FROM conversation_prefs WHERE conversation_id = ?`,
		`DELETE FROM read_cursors WHERE conversation_id = ?`,
		`DELETE FROM drafts WHERE conversation_id = ?`,
		`DELETE FROM closed_direct_conversations WHERE conversation_id = ?`,
		`DELETE FROM scheduled_messages WHERE channel_id = ?`,
		`DELETE FROM reactions WHERE message_id IN (SELECT id FROM messages WHERE conversation = ?)`,
		`DELETE FROM pins WHERE message_id IN (SELECT id FROM messages WHERE conversation = ?)`,
		`DELETE FROM stars WHERE message_id IN (SELECT id FROM messages WHERE conversation = ?)`,
		`DELETE FROM message_files WHERE message_id IN (SELECT id FROM messages WHERE conversation = ?)`,
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListInviteRequests(ctx context.Context, workspace domain.WorkspaceID, status domain.InviteRequestStatus, request domain.PageRequest) (domain.InviteRequestPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.InviteRequestPage{}, err
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListAppApprovals(ctx context.Context, workspace domain.WorkspaceID, approvalStatus domain.AppApprovalStatus, request domain.PageRequest) (domain.AppApprovalPage, error) {
	if !validAppApprovalStatusSQL(approvalStatus) {
		return domain.AppApprovalPage{}, store.ErrInvalidAppApproval
	}
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.AppApprovalPage{}, err
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_installations(app_id, workspace_id, enabled, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(app_id, workspace_id) DO UPDATE SET created_at = CASE WHEN app_installations.enabled = 0 AND excluded.enabled = 1 THEN excluded.created_at ELSE app_installations.created_at END, enabled = excluded.enabled`, value.AppID, value.WorkspaceID, boolInt(value.Enabled), value.CreatedAt.UTC().UnixNano())
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

func (s *Store) UninstallApp(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID) error {
	if workspaceID == "" || appID == "" {
		return store.InvalidArgument("app installation identity is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE app_installations SET enabled = 0 WHERE workspace_id = ? AND app_id = ? AND enabled = 1`, workspaceID, appID)
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
	if _, err := tx.ExecContext(ctx, `UPDATE tokens SET revoked = 1 WHERE workspace_id = ? AND app_id = ?`, workspaceID, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE incoming_webhooks SET enabled = 0 WHERE workspace_id = ? AND app_id = ?`, workspaceID, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_datastore_items WHERE workspace_id = ? AND app_id = ?`, workspaceID, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_members WHERE user_id IN (SELECT user_id FROM bots WHERE workspace_id = ? AND app_id = ?)`, workspaceID, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET active = 0 WHERE workspace_id = ? AND user_id IN (SELECT user_id FROM bots WHERE workspace_id = ? AND app_id = ?)`, workspaceID, workspaceID, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET deleted = 1 WHERE workspace_id = ? AND id IN (SELECT user_id FROM bots WHERE workspace_id = ? AND app_id = ?)`, workspaceID, workspaceID, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bots SET deleted = 1 WHERE workspace_id = ? AND app_id = ?`, workspaceID, appID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateIncomingWebhook(ctx context.Context, value domain.IncomingWebhook) error {
	if value.ID == "" || value.WorkspaceID == "" || value.AppID == "" || value.ConversationID == "" || value.UserID == "" || value.SecretHash == "" || value.CreatedAt.IsZero() {
		return store.ErrInvalidAppApproval
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO incoming_webhooks(id, workspace_id, app_id, conversation_id, user_id, secret_hash, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.AppID, value.ConversationID, value.UserID, value.SecretHash, boolInt(value.Enabled), value.CreatedAt.UTC().UnixNano())
	return classify(err)
}

func (s *Store) LookupIncomingWebhook(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, secret string) (domain.IncomingWebhook, error) {
	if workspaceID == "" || appID == "" || secret == "" {
		return domain.IncomingWebhook{}, store.ErrNotFound
	}
	var value domain.IncomingWebhook
	var enabled int
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, app_id, conversation_id, user_id, secret_hash, enabled, created_at FROM incoming_webhooks WHERE workspace_id = ? AND app_id = ? AND secret_hash = ? AND enabled = 1`, workspaceID, appID, domain.HashToken(secret)).Scan(&value.ID, &value.WorkspaceID, &value.AppID, &value.ConversationID, &value.UserID, &value.SecretHash, &enabled, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.IncomingWebhook{}, store.ErrNotFound
		}
		return domain.IncomingWebhook{}, err
	}
	value.Enabled = enabled != 0
	value.CreatedAt = time.Unix(0, created).UTC()
	return value, nil
}

func (s *Store) SetIncomingWebhookEnabled(ctx context.Context, workspaceID domain.WorkspaceID, id domain.IncomingWebhookID, enabled bool, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE incoming_webhooks SET enabled = ? WHERE id = ? AND workspace_id = ?`, boolInt(enabled), id, workspaceID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return err
		}
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateAppPermissionRequest(ctx context.Context, value domain.AppPermissionRequest, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.RequesterID == "" || value.TargetUserID == "" || value.TriggerID == "" || len(value.Scopes) == 0 {
		return store.InvalidArgument("invalid app permission request")
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
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateView(ctx context.Context, value domain.View, event events.Event) error {
	if value.ID == "" || value.AppID == "" || value.WorkspaceID == "" || value.UserID == "" || value.Type == "" || value.Payload == "" || value.Hash == "" || value.CreatedAt.IsZero() {
		return store.InvalidArgument("invalid view")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	encodedErrors, err := json.Marshal(value.Errors)
	if err != nil {
		return store.InvalidArgument("invalid view errors")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO views(id, app_id, workspace_id, user_id, type, external_id, payload, state, errors, hash, root_view_id, previous_view_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.AppID, value.WorkspaceID, value.UserID, value.Type, value.ExternalID, value.Payload, value.State, string(encodedErrors), value.Hash, value.RootViewID, value.PreviousViewID, value.CreatedAt.UTC().UnixNano(), value.UpdatedAt.UTC().UnixNano()); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func scanView(row interface{ Scan(...any) error }) (domain.View, error) {
	var value domain.View
	var encodedErrors string
	var created, updated int64
	if err := row.Scan(&value.ID, &value.AppID, &value.WorkspaceID, &value.UserID, &value.Type, &value.ExternalID, &value.Payload, &value.State, &encodedErrors, &value.Hash, &value.RootViewID, &value.PreviousViewID, &created, &updated); err != nil {
		return domain.View{}, err
	}
	if err := json.Unmarshal([]byte(encodedErrors), &value.Errors); err != nil {
		return domain.View{}, err
	}
	value.CreatedAt = time.Unix(0, created).UTC()
	value.UpdatedAt = time.Unix(0, updated).UTC()
	return value, nil
}

const viewColumns = `id, app_id, workspace_id, user_id, type, external_id, payload, state, errors, hash, root_view_id, previous_view_id, created_at, updated_at`

func (s *Store) GetView(ctx context.Context, workspace domain.WorkspaceID, id domain.ViewID) (domain.View, error) {
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND id = ?`, workspace, id))
	return value, translateNotFound(err)
}

func (s *Store) GetViewByExternalID(ctx context.Context, workspace domain.WorkspaceID, appID domain.AppID, externalID string) (domain.View, error) {
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND app_id = ? AND external_id = ?`, workspace, appID, externalID))
	return value, translateNotFound(err)
}

func (s *Store) GetPublishedView(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, appID domain.AppID) (domain.View, error) {
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND user_id = ? AND app_id = ? AND type = 'home' ORDER BY updated_at DESC, id DESC LIMIT 1`, workspace, user, appID))
	return value, translateNotFound(err)
}

func (s *Store) GetLatestView(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, appID domain.AppID, viewType string) (domain.View, error) {
	order := "updated_at"
	if viewType == "modal" {
		order = "created_at"
	}
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND user_id = ? AND app_id = ? AND type = ? ORDER BY `+order+` DESC, id DESC LIMIT 1`, workspace, user, appID, viewType))
	return value, translateNotFound(err)
}

func (s *Store) GetCurrentView(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, viewType string) (domain.View, error) {
	order := "updated_at"
	if viewType == "modal" {
		order = "created_at"
	}
	value, err := scanView(s.db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND user_id = ? AND type = ? ORDER BY `+order+` DESC, id DESC LIMIT 1`, workspace, user, viewType))
	return value, translateNotFound(err)
}

func (s *Store) UpdateView(ctx context.Context, value domain.View, expectedHash string, event events.Event) (domain.View, error) {
	if value.ID == "" || value.AppID == "" || value.WorkspaceID == "" || value.Payload == "" || value.Hash == "" {
		return domain.View{}, store.InvalidArgument("invalid view")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.View{}, err
	}
	defer tx.Rollback()
	encodedErrors, err := json.Marshal(value.Errors)
	if err != nil {
		return domain.View{}, store.InvalidArgument("invalid view errors")
	}
	query := `UPDATE views SET type = ?, external_id = ?, payload = ?, state = ?, errors = ?, hash = ?, root_view_id = ?, previous_view_id = ?, updated_at = ? WHERE workspace_id = ? AND app_id = ? AND id = ?`
	args := []any{value.Type, value.ExternalID, value.Payload, value.State, string(encodedErrors), value.Hash, value.RootViewID, value.PreviousViewID, value.UpdatedAt.UTC().UnixNano(), value.WorkspaceID, value.AppID, value.ID}
	if expectedHash != "" {
		query += ` AND hash = ?`
		args = append(args, expectedHash)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return domain.View{}, classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.View{}, err
	}
	if changed != 1 {
		var exists int
		ownershipErr := tx.QueryRowContext(ctx, `SELECT 1 FROM views WHERE workspace_id = ? AND app_id = ? AND id = ?`, value.WorkspaceID, value.AppID, value.ID).Scan(&exists)
		if errors.Is(ownershipErr, sql.ErrNoRows) {
			return domain.View{}, store.ErrNotFound
		}
		if ownershipErr != nil {
			return domain.View{}, ownershipErr
		}
		if expectedHash != "" {
			return domain.View{}, store.ErrConflict
		}
		return domain.View{}, store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.View{}, err
	}
	var storedErrors string
	if err := tx.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND id = ?`, value.WorkspaceID, value.ID).Scan(&value.ID, &value.AppID, &value.WorkspaceID, &value.UserID, &value.Type, &value.ExternalID, &value.Payload, &value.State, &storedErrors, &value.Hash, &value.RootViewID, &value.PreviousViewID, new(int64), new(int64)); err != nil {
		return domain.View{}, err
	}
	if err := json.Unmarshal([]byte(storedErrors), &value.Errors); err != nil {
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

func (s *Store) DeleteView(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ViewID, clear bool, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanView(tx.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM views WHERE workspace_id = ? AND user_id = ? AND id = ?`, workspace, user, id))
	if err != nil {
		return translateNotFound(err)
	}
	var result sql.Result
	if clear {
		result, err = tx.ExecContext(ctx, `DELETE FROM views WHERE workspace_id = ? AND user_id = ? AND app_id = ? AND root_view_id = ?`, workspace, user, current.AppID, current.RootViewID)
	} else {
		result, err = tx.ExecContext(ctx, `DELETE FROM views WHERE workspace_id = ? AND user_id = ? AND id = ?`, workspace, user, id)
	}
	if err != nil {
		return classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
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
		return store.InvalidArgument("invalid workflow step")
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return store.InvalidArgument("invalid dialog")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO dialogs(id, workspace_id, user_id, payload, created_at) VALUES (?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.UserID, value.Payload, value.CreatedAt.UTC().UnixNano()); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return store.InvalidArgument("invalid bot")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO bots(id, workspace_id, app_id, user_id, name, image_36, image_48, image_72, deleted, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.AppID, value.UserID, value.Name, value.Image36, value.Image48, value.Image72, boolInt(value.Deleted), value.UpdatedAt.UTC().Unix())
	if err != nil {
		return classify(err)
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

func (s *Store) GetBotByApp(ctx context.Context, workspace domain.WorkspaceID, appID domain.AppID) (domain.Bot, error) {
	var value domain.Bot
	var deleted, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, app_id, user_id, name, image_36, image_48, image_72, deleted, updated_at FROM bots WHERE workspace_id = ? AND app_id = ? AND deleted = 0 ORDER BY id LIMIT 1`, workspace, appID).
		Scan(&value.ID, &value.WorkspaceID, &value.AppID, &value.UserID, &value.Name, &value.Image36, &value.Image48, &value.Image72, &deleted, &updated)
	if err := translateNotFound(err); err != nil {
		return domain.Bot{}, err
	}
	value.Deleted = deleted != 0
	value.UpdatedAt = time.Unix(updated, 0).UTC()
	return value, nil
}

func (s *Store) CreateUserMigration(ctx context.Context, value domain.UserMigration, event events.Event) error {
	if value.WorkspaceID == "" || value.OldID == "" || value.GlobalID == "" {
		return store.InvalidArgument("invalid user migration")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_migrations(workspace_id, old_id, global_id) VALUES (?, ?, ?)`, value.WorkspaceID, value.OldID, value.GlobalID); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return store.InvalidArgument("conversation team association is empty")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_teams WHERE conversation_id = ?`, conversation); err != nil {
		return err
	}
	seen := make(map[domain.WorkspaceID]struct{}, len(teams))
	for _, team := range teams {
		if team == "" {
			return store.InvalidArgument("invalid conversation team")
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// ListConnectedChannelInfo pages the connected channels of one workspace.
//
// The filters and the page bound are applied in SQL. They used to be applied in
// Go over the result of an unbounded query, so a request for one page of one
// channel still materialised every conversation-to-team row in the workspace
// past the cursor — the cost of the call was the size of the workspace rather
// than the size of the page, and the LIMIT the caller asked for did nothing to
// protect the database.
//
// It is two queries because the unit of paging is a CHANNEL and the unit of the
// join is a channel-team pair: a LIMIT on the joined rows would cut a channel's
// team list in half. The first query bounds the page to channel identifiers, the
// second reads the teams of exactly those channels.
func (s *Store) ListConnectedChannelInfo(ctx context.Context, workspace domain.WorkspaceID, channels []domain.ConversationID, teams []domain.WorkspaceID, request domain.PageRequest) ([]domain.ConnectedChannelInfo, bool, domain.Cursor, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, false, "", err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, false, "", err
	}
	channelPredicate, channelArgs := inPredicate("c.id", len(channels))
	for _, channel := range channels {
		channelArgs = append(channelArgs, string(channel))
	}
	teamPredicate, teamArgs := inPredicate("ct.team_id", len(teams))
	for _, team := range teams {
		teamArgs = append(teamArgs, string(team))
	}

	pageArgs := append([]any{workspace, after}, channelArgs...)
	pageArgs = append(pageArgs, teamArgs...)
	pageArgs = append(pageArgs, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT c.id FROM conversation_teams ct JOIN conversations c ON c.id = ct.conversation_id WHERE c.workspace_id = ? AND c.id > ?`+channelPredicate+teamPredicate+` ORDER BY c.id LIMIT ?`, pageArgs...)
	if err != nil {
		return nil, false, "", err
	}
	page := make([]domain.ConversationID, 0, request.Limit+1)
	for rows.Next() {
		var channel string
		if err := rows.Scan(&channel); err != nil {
			rows.Close()
			return nil, false, "", err
		}
		page = append(page, domain.ConversationID(channel))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, "", err
	}
	if err := rows.Close(); err != nil {
		return nil, false, "", err
	}
	hasMore := len(page) > request.Limit
	if hasMore {
		page = page[:request.Limit]
	}
	if len(page) == 0 {
		return []domain.ConnectedChannelInfo{}, false, "", nil
	}

	pagePredicate, memberArgs := inPredicate("ct.conversation_id", len(page))
	for _, channel := range page {
		memberArgs = append(memberArgs, string(channel))
	}
	memberArgs = append(memberArgs, teamArgs...)
	rows, err = s.db.QueryContext(ctx, `SELECT ct.conversation_id, ct.team_id FROM conversation_teams ct WHERE 1 = 1`+pagePredicate+teamPredicate+` ORDER BY ct.conversation_id, ct.team_id`, memberArgs...)
	if err != nil {
		return nil, false, "", err
	}
	defer rows.Close()
	grouped := make(map[domain.ConversationID][]domain.WorkspaceID, len(page))
	for rows.Next() {
		var channel, team string
		if err := rows.Scan(&channel, &team); err != nil {
			return nil, false, "", err
		}
		grouped[domain.ConversationID(channel)] = append(grouped[domain.ConversationID(channel)], domain.WorkspaceID(team))
	}
	if err := rows.Err(); err != nil {
		return nil, false, "", err
	}
	values := make([]domain.ConnectedChannelInfo, 0, len(page))
	for _, channel := range page {
		values = append(values, domain.ConnectedChannelInfo{ChannelID: channel, InternalTeamIDs: grouped[channel], OriginalConnectedChannelID: channel, OriginalConnectedHostID: workspace})
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

// inPredicate builds " AND <column> IN (?, ?, …)" for a filter of the given
// size, or the empty string when the filter is absent. It returns the argument
// slice the caller appends its values to, so the placeholders and the arguments
// are produced in one place.
func inPredicate(column string, size int) (string, []any) {
	if size == 0 {
		return "", make([]any, 0)
	}
	var builder strings.Builder
	builder.WriteString(" AND ")
	builder.WriteString(column)
	builder.WriteString(" IN (")
	for index := 0; index < size; index++ {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString("?")
	}
	builder.WriteString(")")
	return builder.String(), make([]any, 0, size)
}

func (s *Store) CreateOAuthClient(ctx context.Context, value domain.OAuthClient) error {
	if value.ID == "" || value.SecretHash == "" || value.AppID == "" {
		return store.InvalidArgument("invalid oauth client")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_clients(id, secret_hash, app_id) VALUES (?, ?, ?)`, value.ID, value.SecretHash, value.AppID)
	return classify(err)
}

func (s *Store) GetOAuthClient(ctx context.Context, id string) (domain.OAuthClient, error) {
	var value domain.OAuthClient
	err := s.db.QueryRowContext(ctx, `SELECT id, secret_hash, app_id FROM oauth_clients WHERE id = ?`, id).Scan(&value.ID, &value.SecretHash, &value.AppID)
	if err := translateNotFound(err); err != nil {
		return domain.OAuthClient{}, err
	}
	return value, nil
}

// CreateOAuthCode stores the authorization code as a digest with a bounded
// lifetime. The code itself never reaches a column: every other credential in
// this schema — session, token, application token, incoming webhook secret,
// OpenID refresh token — is stored as domain.HashToken of itself, and the
// authorization code was the one exception, so a database copy was enough to
// redeem an outstanding grant. The expiry is derived here rather than accepted
// from the caller so no caller can issue a code that outlives
// store.OAuthCodeLifetime.
func (s *Store) CreateOAuthCode(ctx context.Context, value domain.OAuthCode) error {
	if value.Code == "" || value.ClientID == "" || value.WorkspaceID == "" || value.UserID == "" {
		return store.InvalidArgument("invalid oauth code")
	}
	if value.BotID != "" || value.BotUserID != "" || len(value.BotScopes) != 0 {
		if value.BotID == "" || value.BotUserID == "" || len(domain.NormalizeScopes(value.BotScopes)) == 0 {
			return store.InvalidArgument("incomplete oauth bot grant")
		}
		var matches int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bots b JOIN oauth_clients c ON c.app_id = b.app_id WHERE c.id = ? AND b.id = ? AND b.workspace_id = ? AND b.user_id = ? AND b.deleted = 0`, value.ClientID, value.BotID, value.WorkspaceID, value.BotUserID).Scan(&matches); err != nil {
			return err
		}
		if matches != 1 {
			return store.InvalidArgument("oauth bot grant does not match the app installation")
		}
	}
	scopes, err := json.Marshal(domain.NormalizeScopes(value.Scopes))
	if err != nil {
		return err
	}
	botScopes, err := json.Marshal(domain.NormalizeScopes(value.BotScopes))
	if err != nil {
		return err
	}
	userScopes, err := json.Marshal(domain.NormalizeScopes(value.UserScopes))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_codes(code, client_id, workspace_id, user_id, scopes, bot_id, bot_user_id, bot_scopes, user_scopes, redirect_uri, code_challenge, code_challenge_method, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, domain.HashToken(value.Code), value.ClientID, value.WorkspaceID, value.UserID, string(scopes), value.BotID, value.BotUserID, string(botScopes), string(userScopes), value.RedirectURI, value.CodeChallenge, value.CodeChallengeMethod, time.Now().UTC().Add(store.OAuthCodeLifetime).UnixNano())
	return classify(err)
}

func (s *Store) ExchangeOAuthCode(ctx context.Context, clientID, secret, code, redirect, accessToken string, token domain.OAuthToken) (domain.OAuthToken, error) {
	var exchanged domain.OAuthToken
	err := underContention(ctx, func() error {
		value, err := s.exchangeOAuthCodeOnce(ctx, clientID, secret, code, redirect, accessToken, token)
		if err == nil {
			exchanged = value
		}
		return err
	})
	return exchanged, err
}

func (s *Store) exchangeOAuthCodeOnce(ctx context.Context, clientID, secret, code, redirect, accessToken string, token domain.OAuthToken) (domain.OAuthToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OAuthToken{}, err
	}
	defer tx.Rollback()
	// The client secret digest is compared in Go with hmac.Equal rather than in
	// a WHERE clause. A database equality test is not constant time — the engine
	// may compare byte by byte, short circuit, and reveal through timing how
	// much of the digest matched — and it also hands the secret to the query log
	// and the statement cache.
	var appID domain.AppID
	var storedSecretHash string
	if err := tx.QueryRowContext(ctx, `SELECT app_id, secret_hash FROM oauth_clients WHERE id = ?`, clientID).Scan(&appID, &storedSecretHash); err != nil {
		return domain.OAuthToken{}, translateNotFound(err)
	}
	if !hmac.Equal([]byte(storedSecretHash), []byte(domain.HashToken(secret))) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	codeHash := domain.HashToken(code)
	now := time.Now().UTC()
	// An expired code is unredeemable, and clearing the expired rows on the
	// redemption path is the only schedule this table has.
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_codes WHERE expires_at <= ?`, now.UnixNano()); err != nil {
		return domain.OAuthToken{}, err
	}
	var grant domain.OAuthCode
	var scopes, botScopes, userScopes string
	if err := tx.QueryRowContext(ctx, `SELECT code, client_id, workspace_id, user_id, scopes, bot_id, bot_user_id, bot_scopes, user_scopes, redirect_uri, code_challenge, code_challenge_method FROM oauth_codes WHERE code = ? AND client_id = ? AND redirect_uri = ? AND expires_at > ?`, codeHash, clientID, redirect, now.UnixNano()).Scan(&grant.Code, &grant.ClientID, &grant.WorkspaceID, &grant.UserID, &scopes, &grant.BotID, &grant.BotUserID, &botScopes, &userScopes, &grant.RedirectURI, &grant.CodeChallenge, &grant.CodeChallengeMethod); err != nil {
		return domain.OAuthToken{}, translateNotFound(err)
	}
	if err := json.Unmarshal([]byte(scopes), &grant.Scopes); err != nil {
		return domain.OAuthToken{}, err
	}
	if err := json.Unmarshal([]byte(botScopes), &grant.BotScopes); err != nil {
		return domain.OAuthToken{}, err
	}
	if err := json.Unmarshal([]byte(userScopes), &grant.UserScopes); err != nil {
		return domain.OAuthToken{}, err
	}
	if !domain.VerifyPKCE(grant.CodeChallenge, grant.CodeChallengeMethod, token.CodeVerifier) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	token.CodeVerifier = ""
	tokenType := strings.TrimSpace(token.TokenType)
	if tokenType == "" {
		tokenType = "user"
	}
	subjectID := grant.UserID
	var tokenBotID domain.BotID
	tokenScopes := grant.UserScopes
	if len(tokenScopes) == 0 {
		tokenScopes = grant.Scopes
	}
	if tokenType == "bot" {
		if grant.BotID == "" || grant.BotUserID == "" {
			return domain.OAuthToken{}, store.ErrNotFound
		}
		subjectID = grant.BotUserID
		tokenBotID = grant.BotID
		tokenScopes = grant.BotScopes
	}
	if len(tokenScopes) == 0 {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	normalizedUserScopes := domain.NormalizeScopes(grant.UserScopes)
	if tokenType == "bot" && len(normalizedUserScopes) != 0 && strings.TrimSpace(token.AuthedUserAccessToken) == "" {
		return domain.OAuthToken{}, store.InvalidArgument("missing installer user access token")
	}
	rotating := strings.TrimSpace(token.RefreshToken) != ""
	if rotating && (!token.ExpiresAt.After(now) || tokenType == "bot" && len(normalizedUserScopes) != 0 && (strings.TrimSpace(token.AuthedUserRefreshToken) == "" || !token.AuthedUserExpiresAt.After(now))) {
		return domain.OAuthToken{}, store.InvalidArgument("invalid rotating OAuth credentials")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM oauth_codes WHERE code = ? AND client_id = ? AND redirect_uri = ?`, codeHash, clientID, redirect)
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
	accessHash := domain.HashToken(accessToken)
	accessExpiresAt := int64(0)
	if !token.ExpiresAt.IsZero() {
		accessExpiresAt = token.ExpiresAt.UTC().UnixNano()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(token_hash, workspace_id, user_id, app_id, bot_id, scopes, token_type, expires_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`, accessHash, grant.WorkspaceID, subjectID, appID, tokenBotID, strings.Join(domain.NormalizeScopes(tokenScopes), " "), tokenType, accessExpiresAt); err != nil {
		return domain.OAuthToken{}, err
	}
	if rotating {
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens(refresh_hash, access_hash, client_id, app_id, workspace_id, user_id, installer_id, bot_id, scopes, token_type, access_expires_at, created_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, domain.HashToken(token.RefreshToken), accessHash, clientID, appID, grant.WorkspaceID, subjectID, grant.UserID, tokenBotID, strings.Join(domain.NormalizeScopes(tokenScopes), " "), tokenType, accessExpiresAt, now.UnixNano()); err != nil {
			return domain.OAuthToken{}, err
		}
	}
	if tokenType == "bot" && len(normalizedUserScopes) != 0 {
		userAccessHash := domain.HashToken(token.AuthedUserAccessToken)
		userExpiresAt := int64(0)
		if !token.AuthedUserExpiresAt.IsZero() {
			userExpiresAt = token.AuthedUserExpiresAt.UTC().UnixNano()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(token_hash, workspace_id, user_id, app_id, bot_id, scopes, token_type, expires_at, revoked) VALUES (?, ?, ?, ?, '', ?, 'user', ?, 0)`, userAccessHash, grant.WorkspaceID, grant.UserID, appID, strings.Join(normalizedUserScopes, " "), userExpiresAt); err != nil {
			return domain.OAuthToken{}, err
		}
		if rotating {
			if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens(refresh_hash, access_hash, client_id, app_id, workspace_id, user_id, installer_id, bot_id, scopes, token_type, access_expires_at, created_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, 'user', ?, ?, 0)`, domain.HashToken(token.AuthedUserRefreshToken), userAccessHash, clientID, appID, grant.WorkspaceID, grant.UserID, grant.UserID, strings.Join(normalizedUserScopes, " "), userExpiresAt, now.UnixNano()); err != nil {
				return domain.OAuthToken{}, err
			}
		}
		token.AuthedUserScopes = append([]string(nil), normalizedUserScopes...)
	} else {
		token.AuthedUserAccessToken = ""
		token.AuthedUserScopes = nil
	}
	// The authorization code, access token, and installation are one durable
	// state transition. Keeping this in the same transaction prevents an
	// installation write failure from consuming the one-time code while
	// leaving an orphaned live token behind.
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_installations(app_id, workspace_id, enabled, created_at) VALUES (?, ?, 1, ?) ON CONFLICT(app_id, workspace_id) DO UPDATE SET created_at = CASE WHEN app_installations.enabled = 0 THEN excluded.created_at ELSE app_installations.created_at END, enabled = 1`, appID, grant.WorkspaceID, now.UnixNano()); err != nil {
		return domain.OAuthToken{}, err
	}
	token.AccessToken = accessToken
	token.AppID = appID
	token.ClientID = clientID
	token.WorkspaceID = grant.WorkspaceID
	token.UserID = subjectID
	token.InstallerID = grant.UserID
	token.BotID = tokenBotID
	token.Scopes = domain.NormalizeScopes(tokenScopes)
	token.TokenType = tokenType
	if err := tx.Commit(); err != nil {
		return domain.OAuthToken{}, err
	}
	return token, nil
}

func (s *Store) LookupOAuthRefreshToken(ctx context.Context, clientID, refreshToken string) (domain.OAuthRefreshGrant, error) {
	var value domain.OAuthRefreshGrant
	var scopes string
	var accessExpiresAt, createdAt int64
	var revoked int
	err := s.db.QueryRowContext(ctx, `SELECT refresh_hash, access_hash, client_id, app_id, workspace_id, user_id, installer_id, bot_id, scopes, token_type, access_expires_at, created_at, revoked FROM oauth_refresh_tokens WHERE refresh_hash = ? AND client_id = ?`, domain.HashToken(strings.TrimSpace(refreshToken)), strings.TrimSpace(clientID)).Scan(
		&value.TokenHash, &value.AccessTokenHash, &value.ClientID, &value.AppID, &value.WorkspaceID, &value.UserID, &value.InstallerID, &value.BotID, &scopes, &value.TokenType, &accessExpiresAt, &createdAt, &revoked,
	)
	if err := translateNotFound(err); err != nil {
		return domain.OAuthRefreshGrant{}, err
	}
	if revoked != 0 {
		return domain.OAuthRefreshGrant{}, store.ErrNotFound
	}
	value.Scopes = domain.NormalizeScopes(strings.Fields(scopes))
	value.AccessExpiresAt = time.Unix(0, accessExpiresAt).UTC()
	value.CreatedAt = time.Unix(0, createdAt).UTC()
	return value, nil
}

func (s *Store) ExchangeOAuthRefreshToken(ctx context.Context, clientID, secret, oldRefreshToken, nextAccessToken, nextRefreshToken string, expiresAt time.Time) (domain.OAuthToken, error) {
	var exchanged domain.OAuthToken
	err := underContention(ctx, func() error {
		value, err := s.exchangeOAuthRefreshTokenOnce(ctx, clientID, secret, oldRefreshToken, nextAccessToken, nextRefreshToken, expiresAt)
		if err == nil {
			exchanged = value
		}
		return err
	})
	return exchanged, err
}

func (s *Store) exchangeOAuthRefreshTokenOnce(ctx context.Context, clientID, secret, oldRefreshToken, nextAccessToken, nextRefreshToken string, expiresAt time.Time) (domain.OAuthToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OAuthToken{}, err
	}
	defer tx.Rollback()
	var storedSecretHash string
	if err := tx.QueryRowContext(ctx, `SELECT secret_hash FROM oauth_clients WHERE id = ?`, strings.TrimSpace(clientID)).Scan(&storedSecretHash); err != nil {
		return domain.OAuthToken{}, translateNotFound(err)
	}
	if !hmac.Equal([]byte(storedSecretHash), []byte(domain.HashToken(strings.TrimSpace(secret)))) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	now := time.Now().UTC()
	if strings.TrimSpace(nextAccessToken) == "" || strings.TrimSpace(nextRefreshToken) == "" || !expiresAt.After(now) {
		return domain.OAuthToken{}, store.InvalidArgument("invalid rotating OAuth credentials")
	}
	oldHash := domain.HashToken(strings.TrimSpace(oldRefreshToken))
	var grant domain.OAuthRefreshGrant
	var scopes string
	var oldAccessExpiresAt, createdAt int64
	var revoked int
	err = tx.QueryRowContext(ctx, `SELECT refresh_hash, access_hash, client_id, app_id, workspace_id, user_id, installer_id, bot_id, scopes, token_type, access_expires_at, created_at, revoked FROM oauth_refresh_tokens WHERE refresh_hash = ? AND client_id = ?`, oldHash, clientID).Scan(
		&grant.TokenHash, &grant.AccessTokenHash, &grant.ClientID, &grant.AppID, &grant.WorkspaceID, &grant.UserID, &grant.InstallerID, &grant.BotID, &scopes, &grant.TokenType, &oldAccessExpiresAt, &createdAt, &revoked,
	)
	if err := translateNotFound(err); err != nil {
		return domain.OAuthToken{}, err
	}
	if revoked != 0 {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	grant.Scopes = domain.NormalizeScopes(strings.Fields(scopes))
	result, err := tx.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked = 1 WHERE refresh_hash = ? AND revoked = 0`, oldHash)
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
	nextAccessHash := domain.HashToken(nextAccessToken)
	nextRefreshHash := domain.HashToken(nextRefreshToken)
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(token_hash, workspace_id, user_id, app_id, bot_id, scopes, token_type, expires_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`, nextAccessHash, grant.WorkspaceID, grant.UserID, grant.AppID, grant.BotID, strings.Join(grant.Scopes, " "), grant.TokenType, expiresAt.UTC().UnixNano()); err != nil {
		return domain.OAuthToken{}, classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens(refresh_hash, access_hash, client_id, app_id, workspace_id, user_id, installer_id, bot_id, scopes, token_type, access_expires_at, created_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, nextRefreshHash, nextAccessHash, grant.ClientID, grant.AppID, grant.WorkspaceID, grant.UserID, grant.InstallerID, grant.BotID, strings.Join(grant.Scopes, " "), grant.TokenType, expiresAt.UTC().UnixNano(), now.UnixNano()); err != nil {
		return domain.OAuthToken{}, classify(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT access_hash FROM oauth_refresh_tokens WHERE client_id = ? AND workspace_id = ? AND user_id = ? AND bot_id = ? AND token_type = ? ORDER BY created_at DESC, access_hash DESC LIMIT -1 OFFSET 2`, grant.ClientID, grant.WorkspaceID, grant.UserID, grant.BotID, grant.TokenType)
	if err != nil {
		return domain.OAuthToken{}, err
	}
	var staleAccessHashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return domain.OAuthToken{}, err
		}
		staleAccessHashes = append(staleAccessHashes, hash)
	}
	if err := rows.Close(); err != nil {
		return domain.OAuthToken{}, err
	}
	for _, hash := range staleAccessHashes {
		if _, err := tx.ExecContext(ctx, `UPDATE tokens SET revoked = 1 WHERE token_hash = ?`, hash); err != nil {
			return domain.OAuthToken{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.OAuthToken{}, err
	}
	return domain.OAuthToken{AccessToken: nextAccessToken, RefreshToken: nextRefreshToken, ExpiresAt: expiresAt.UTC(), ClientID: clientID, AppID: grant.AppID, WorkspaceID: grant.WorkspaceID, UserID: grant.UserID, InstallerID: grant.InstallerID, BotID: grant.BotID, Scopes: append([]string(nil), grant.Scopes...), TokenType: grant.TokenType}, nil
}

func (s *Store) ExchangeOAuthAccessToken(ctx context.Context, clientID, secret, oldAccessToken, nextAccessToken, nextRefreshToken string, expiresAt time.Time) (domain.OAuthToken, error) {
	var exchanged domain.OAuthToken
	err := underContention(ctx, func() error {
		value, err := s.exchangeOAuthAccessTokenOnce(ctx, clientID, secret, oldAccessToken, nextAccessToken, nextRefreshToken, expiresAt)
		if err == nil {
			exchanged = value
		}
		return err
	})
	return exchanged, err
}

func (s *Store) exchangeOAuthAccessTokenOnce(ctx context.Context, clientID, secret, oldAccessToken, nextAccessToken, nextRefreshToken string, expiresAt time.Time) (domain.OAuthToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OAuthToken{}, err
	}
	defer tx.Rollback()
	var appID domain.AppID
	var storedSecretHash string
	if err := tx.QueryRowContext(ctx, `SELECT app_id, secret_hash FROM oauth_clients WHERE id = ?`, strings.TrimSpace(clientID)).Scan(&appID, &storedSecretHash); err != nil {
		return domain.OAuthToken{}, translateNotFound(err)
	}
	if !hmac.Equal([]byte(storedSecretHash), []byte(domain.HashToken(strings.TrimSpace(secret)))) {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	now := time.Now().UTC()
	if strings.TrimSpace(nextAccessToken) == "" || strings.TrimSpace(nextRefreshToken) == "" || !expiresAt.After(now) {
		return domain.OAuthToken{}, store.InvalidArgument("invalid rotating OAuth credentials")
	}
	oldAccessHash := domain.HashToken(strings.TrimSpace(oldAccessToken))
	var record domain.TokenRecord
	var scopes string
	var oldExpiresAt int64
	var revoked int
	err = tx.QueryRowContext(ctx, `SELECT workspace_id, user_id, app_id, bot_id, scopes, token_type, expires_at, revoked FROM tokens WHERE token_hash = ?`, oldAccessHash).Scan(&record.WorkspaceID, &record.UserID, &record.AppID, &record.BotID, &scopes, &record.TokenType, &oldExpiresAt, &revoked)
	if err := translateNotFound(err); err != nil {
		return domain.OAuthToken{}, err
	}
	if revoked != 0 || record.AppID != appID || oldExpiresAt != 0 || record.TokenType != "bot" && record.TokenType != "user" {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	var alreadyExchanged int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_refresh_tokens WHERE access_hash = ?`, oldAccessHash).Scan(&alreadyExchanged); err != nil {
		return domain.OAuthToken{}, err
	}
	if alreadyExchanged != 0 {
		return domain.OAuthToken{}, store.ErrNotFound
	}
	record.Scopes = domain.NormalizeScopes(strings.Fields(scopes))
	nextAccessHash := domain.HashToken(nextAccessToken)
	nextRefreshHash := domain.HashToken(nextRefreshToken)
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(token_hash, workspace_id, user_id, app_id, bot_id, scopes, token_type, expires_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`, nextAccessHash, record.WorkspaceID, record.UserID, record.AppID, record.BotID, strings.Join(record.Scopes, " "), record.TokenType, expiresAt.UTC().UnixNano()); err != nil {
		return domain.OAuthToken{}, classify(err)
	}
	legacyHash := "legacy:" + oldAccessHash
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens(refresh_hash, access_hash, client_id, app_id, workspace_id, user_id, installer_id, bot_id, scopes, token_type, access_expires_at, created_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 1)`, legacyHash, oldAccessHash, clientID, record.AppID, record.WorkspaceID, record.UserID, record.UserID, record.BotID, strings.Join(record.Scopes, " "), record.TokenType, now.Add(-time.Nanosecond).UnixNano()); err != nil {
		return domain.OAuthToken{}, classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens(refresh_hash, access_hash, client_id, app_id, workspace_id, user_id, installer_id, bot_id, scopes, token_type, access_expires_at, created_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, nextRefreshHash, nextAccessHash, clientID, record.AppID, record.WorkspaceID, record.UserID, record.UserID, record.BotID, strings.Join(record.Scopes, " "), record.TokenType, expiresAt.UTC().UnixNano(), now.UnixNano()); err != nil {
		return domain.OAuthToken{}, classify(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.OAuthToken{}, err
	}
	return domain.OAuthToken{AccessToken: nextAccessToken, RefreshToken: nextRefreshToken, ExpiresAt: expiresAt.UTC(), ClientID: clientID, AppID: record.AppID, WorkspaceID: record.WorkspaceID, UserID: record.UserID, InstallerID: record.UserID, BotID: record.BotID, Scopes: append([]string(nil), record.Scopes...), TokenType: record.TokenType}, nil
}

func (s *Store) CreateOpenIDRefreshToken(ctx context.Context, value domain.OpenIDRefreshToken) error {
	if value.TokenHash == "" || value.ClientID == "" || value.WorkspaceID == "" || value.UserID == "" || !value.ExpiresAt.After(time.Now().UTC()) || len(domain.NormalizeScopes(value.Scopes)) == 0 {
		return store.InvalidArgument("invalid OpenID Connect refresh token")
	}
	scopes, err := json.Marshal(domain.NormalizeScopes(value.Scopes))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO openid_refresh_tokens(token_hash, client_id, workspace_id, user_id, scopes, expires_at) VALUES (?, ?, ?, ?, ?, ?)`, value.TokenHash, value.ClientID, value.WorkspaceID, value.UserID, string(scopes), value.ExpiresAt.UTC().Unix())
	return classify(err)
}

func (s *Store) ExchangeOpenIDRefreshToken(ctx context.Context, clientID, oldToken, accessToken, refreshToken string, token domain.OpenIDToken) (domain.OpenIDToken, error) {
	var exchanged domain.OpenIDToken
	err := underContention(ctx, func() error {
		value, err := s.exchangeOpenIDRefreshTokenOnce(ctx, clientID, oldToken, accessToken, refreshToken, token)
		if err == nil {
			exchanged = value
		}
		return err
	})
	return exchanged, err
}

func (s *Store) exchangeOpenIDRefreshTokenOnce(ctx context.Context, clientID, oldToken, accessToken, refreshToken string, token domain.OpenIDToken) (domain.OpenIDToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	defer tx.Rollback()
	var value domain.OpenIDRefreshToken
	var scopes string
	var expiresAt int64
	err = tx.QueryRowContext(ctx, `SELECT token_hash, client_id, workspace_id, user_id, scopes, expires_at FROM openid_refresh_tokens WHERE token_hash = ? AND client_id = ?`, domain.HashToken(oldToken), clientID).Scan(&value.TokenHash, &value.ClientID, &value.WorkspaceID, &value.UserID, &scopes, &expiresAt)
	if err := translateNotFound(err); err != nil {
		return domain.OpenIDToken{}, err
	}
	if err := json.Unmarshal([]byte(scopes), &value.Scopes); err != nil {
		return domain.OpenIDToken{}, err
	}
	value.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	if !value.ExpiresAt.After(time.Now().UTC()) {
		return domain.OpenIDToken{}, store.ErrNotFound
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM openid_refresh_tokens WHERE token_hash = ?`, domain.HashToken(oldToken))
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return domain.OpenIDToken{}, store.ErrNotFound
	}
	newScopes, err := json.Marshal(domain.NormalizeScopes(value.Scopes))
	if err != nil {
		return domain.OpenIDToken{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO openid_refresh_tokens(token_hash, client_id, workspace_id, user_id, scopes, expires_at) VALUES (?, ?, ?, ?, ?, ?)`, domain.HashToken(refreshToken), value.ClientID, value.WorkspaceID, value.UserID, string(newScopes), time.Now().UTC().Add(30*24*time.Hour).Unix()); err != nil {
		return domain.OpenIDToken{}, err
	}
	// The minted access token has to be persisted in the same transaction as the
	// rotation. It was not, so the token this method returned authenticated
	// nothing: openid.connect.userInfo resolves the bearer through LookupToken,
	// which reads this table, and every refreshed token was rejected. Committing
	// it separately would leave a rotated refresh token whose access token does
	// not exist, so the insert belongs here, mirroring ExchangeOAuthCode.
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(token_hash, workspace_id, user_id, app_id, scopes, token_type, expires_at, revoked) VALUES (?, ?, ?, '', ?, 'user', 0, 0)`, domain.HashToken(accessToken), value.WorkspaceID, value.UserID, strings.Join(domain.NormalizeScopes(value.Scopes), " ")); err != nil {
		return domain.OpenIDToken{}, classify(err)
	}
	token.AccessToken = accessToken
	token.RefreshToken = refreshToken
	token.ClientID = value.ClientID
	token.WorkspaceID = value.WorkspaceID
	token.UserID = value.UserID
	token.Scopes = value.Scopes
	if err := tx.Commit(); err != nil {
		return domain.OpenIDToken{}, err
	}
	return token, nil
}

func (s *Store) CreateRTMConnection(ctx context.Context, value domain.RTMConnection) error {
	if value.ID == "" || value.WorkspaceID == "" || value.UserID == "" || value.ExpiresAt.IsZero() {
		return store.InvalidArgument("invalid RTM connection")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO rtm_connections(id, workspace_id, user_id, expires_at) VALUES (?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.UserID, value.ExpiresAt.UTC().UnixNano())
	return classify(err)
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
	return underContention(ctx, func() error { return s.createSocketModeConnectionOnce(ctx, value) })
}

func (s *Store) createSocketModeConnectionOnce(ctx context.Context, value domain.SocketModeConnection) error {
	// s.now(), not the wall clock: the admission decision two lines down is made
	// against the injected clock, and a validation that disagrees with it makes
	// the limit correct only while the two happen to coincide — which is exactly
	// why the conformance test could not fail on this.
	if value.ID == "" || value.AppID == "" || !value.ExpiresAt.After(s.now().UTC()) {
		return store.InvalidArgument("invalid Socket Mode connection")
	}
	if err := s.ensureSocketModeAdmissionRow(ctx, value.AppID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Same shape as ConsumeSocketModeConnection: take the admission lock before
	// reading the count, so the count cannot be stale by the time it is acted on.
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, socketModeAdmissionLockStatement, value.AppID); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM socket_mode_connections WHERE app_id = ? AND consumed_at > 0 AND expires_at > ?`, value.AppID, now.UnixNano()).Scan(&active); err != nil {
		return err
	}
	if active >= domain.SocketModeConnectionLimit {
		return store.ErrSocketModeConnectionLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO socket_mode_connections(id, app_id, expires_at) VALUES (?, ?, ?)`, value.ID, value.AppID, value.ExpiresAt.UTC().UnixNano())
	if err != nil {
		return classify(err)
	}
	return tx.Commit()
}

// socketModeAdmissionLockStatement serializes admission for one app before the
// active-connection count is read.
//
// Counting and then updating in a transaction whose first statement is a READ is
// check-then-act: the transaction begins deferred, so every concurrent dialler
// takes its read snapshot before any of them writes, every one of them sees the
// same count, and every one of them is admitted. Measured, 64 concurrent
// diallers against a limit of 10 admitted 11-15. Writing first takes the
// engine's write lock before anything is read, so the count each caller reads
// already includes every admission that committed before it.
//
// The write must touch exactly ONE row. An earlier version wrote every live
// ticket of the app, which is a single lock on the SQLite family but a row lock
// per ticket on PostgreSQL — concurrent admissions acquired those rows in
// different orders and deadlocked, and on the replicated profile the breadth of
// the write showed up as lock contention. One row per app is ordered by
// construction, so admissions queue instead of colliding.
const socketModeAdmissionLockStatement = `UPDATE socket_mode_admission SET ticket = ticket + 1 WHERE app_id = ?`

// ensureSocketModeAdmissionRow creates the app's admission row if it is absent.
// It runs outside the admission transaction: the lock statement takes no lock at
// all when it matches no row, so the row has to exist before it runs.
func (s *Store) ensureSocketModeAdmissionRow(ctx context.Context, appID domain.AppID) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO socket_mode_admission(app_id, ticket) VALUES (?, 0) ON CONFLICT(app_id) DO NOTHING`, appID)
	return err
}

func (s *Store) ConsumeSocketModeConnection(ctx context.Context, id string) (domain.SocketModeConnection, error) {
	var value domain.SocketModeConnection
	err := underContention(ctx, func() error {
		result, err := s.consumeSocketModeConnectionOnce(ctx, id)
		value = result
		return err
	})
	return value, err
}

func (s *Store) consumeSocketModeConnectionOnce(ctx context.Context, id string) (domain.SocketModeConnection, error) {
	// Resolve the ticket's app before the transaction opens, so the transaction's
	// first statement is the admission lock rather than a read.
	var appID domain.AppID
	if err := s.db.QueryRowContext(ctx, `SELECT app_id FROM socket_mode_connections WHERE id = ?`, id).Scan(&appID); err != nil {
		return domain.SocketModeConnection{}, translateNotFound(err)
	}
	if err := s.ensureSocketModeAdmissionRow(ctx, appID); err != nil {
		return domain.SocketModeConnection{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SocketModeConnection{}, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, socketModeAdmissionLockStatement, appID); err != nil {
		return domain.SocketModeConnection{}, err
	}
	var value domain.SocketModeConnection
	var expiresAt, consumedAt int64
	if err := tx.QueryRowContext(ctx, `SELECT id, app_id, expires_at, consumed_at FROM socket_mode_connections WHERE id = ?`, id).Scan(&value.ID, &value.AppID, &expiresAt, &consumedAt); err != nil {
		return domain.SocketModeConnection{}, translateNotFound(err)
	}
	value.ExpiresAt = time.Unix(0, expiresAt).UTC()
	if consumedAt != 0 || !value.ExpiresAt.After(now) {
		return domain.SocketModeConnection{}, store.ErrNotFound
	}
	// Consumption is what makes a connection active, so it is the only place
	// the concurrent-connection limit can be enforced. Checking it when the
	// ticket is issued counts nothing, because a ticket is inactive until it is
	// dialled: an app could take unbounded tickets first and dial them all.
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM socket_mode_connections WHERE app_id = ? AND consumed_at > 0 AND expires_at > ?`, value.AppID, now.UnixNano()).Scan(&active); err != nil {
		return domain.SocketModeConnection{}, err
	}
	if active >= domain.SocketModeConnectionLimit {
		return domain.SocketModeConnection{}, store.ErrSocketModeConnectionLimit
	}
	result, err := tx.ExecContext(ctx, `UPDATE socket_mode_connections SET consumed_at = ? WHERE id = ? AND consumed_at = 0`, now.UnixNano(), id)
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
		return store.InvalidArgument("invalid Socket Mode connection renewal")
	}
	// An already-expired connection must not be resurrected: its slot has been
	// released, so a replacement may already have been admitted and reviving this
	// one would put the app over the concurrent-connection limit.
	result, err := s.db.ExecContext(ctx, `UPDATE socket_mode_connections SET expires_at = ? WHERE id = ? AND consumed_at > 0 AND expires_at > ?`, expiresAt.UTC().UnixNano(), id, s.now().UnixNano())
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
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM socket_mode_connections WHERE app_id = ? AND consumed_at > 0 AND expires_at > ?`, appID, s.now().UTC().UnixNano()).Scan(&count)
	return count, err
}

func (s *Store) RecordSocketModeResponse(ctx context.Context, value domain.SocketModeResponse) error {
	if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" || strings.TrimSpace(value.Payload) == "" || value.ReceivedAt.IsZero() {
		return store.InvalidArgument("invalid Socket Mode response")
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

func (s *Store) GetSocketModeResponse(ctx context.Context, appID domain.AppID, envelopeID string) (domain.SocketModeResponse, error) {
	if appID == "" || strings.TrimSpace(envelopeID) == "" {
		return domain.SocketModeResponse{}, store.InvalidArgument("Socket Mode response identity is required")
	}
	var value domain.SocketModeResponse
	var receivedAt, leaseExpiresAt, acknowledgedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT app_id, envelope_id, payload, received_at, lease_owner, lease_expires_at, acknowledged_at FROM socket_mode_responses WHERE app_id = ? AND envelope_id = ?`, appID, strings.TrimSpace(envelopeID)).
		Scan(&value.AppID, &value.EnvelopeID, &value.Payload, &receivedAt, &value.LeaseOwner, &leaseExpiresAt, &acknowledgedAt)
	if err != nil {
		return domain.SocketModeResponse{}, translateNotFound(err)
	}
	value.ReceivedAt = time.Unix(0, receivedAt).UTC()
	if leaseExpiresAt != 0 {
		value.LeaseExpiresAt = time.Unix(0, leaseExpiresAt).UTC()
	}
	if acknowledgedAt != 0 {
		value.AcknowledgedAt = time.Unix(0, acknowledgedAt).UTC()
	}
	return value, nil
}

func (s *Store) ClaimSocketModeResponses(ctx context.Context, appID domain.AppID, owner string, limit int, lease time.Duration) ([]domain.SocketModeResponse, error) {
	if appID == "" || strings.TrimSpace(owner) == "" || limit < 1 || limit > 1000 || lease <= 0 {
		return nil, store.InvalidArgument("invalid Socket Mode response lease")
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

// AckSocketModeResponses acknowledges a batch atomically. It used to issue one
// statement per envelope against s.db with no transaction, so a batch whose
// third envelope had an expired lease committed the first two and then returned
// ErrConflict — a partial effect the signature does not admit to, and one the
// caller cannot distinguish from a batch that did nothing.
func (s *Store) AckSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse) error {
	if strings.TrimSpace(owner) == "" || len(values) == 0 {
		return store.InvalidArgument("invalid Socket Mode response acknowledgement")
	}
	for _, value := range values {
		if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" {
			return store.InvalidArgument("invalid Socket Mode response key")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UnixNano()
	for _, value := range values {
		result, err := tx.ExecContext(ctx, `UPDATE socket_mode_responses SET acknowledged_at = ?, lease_owner = '', lease_expires_at = 0 WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND lease_owner = ? AND lease_expires_at > ?`, now, value.AppID, value.EnvelopeID, owner, now)
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
		if acknowledgedAt == 0 {
			return store.ErrConflict
		}
	}
	return tx.Commit()
}

func (s *Store) RenewSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, lease time.Duration) error {
	if strings.TrimSpace(owner) == "" || len(values) == 0 || lease <= 0 {
		return store.InvalidArgument("invalid Socket Mode response renewal")
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
			return store.InvalidArgument("invalid Socket Mode response key")
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

// ReleaseSocketModeResponses releases a batch atomically, for the same reason
// AckSocketModeResponses does.
func (s *Store) ReleaseSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, retryAt time.Time) error {
	if strings.TrimSpace(owner) == "" || len(values) == 0 || retryAt.IsZero() {
		return store.InvalidArgument("invalid Socket Mode response release")
	}
	for _, value := range values {
		if value.AppID == "" || strings.TrimSpace(value.EnvelopeID) == "" {
			return store.InvalidArgument("invalid Socket Mode response key")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, value := range values {
		result, err := tx.ExecContext(ctx, `UPDATE socket_mode_responses SET lease_owner = '', lease_expires_at = ? WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND lease_owner = ?`, retryAt.UTC().UnixNano(), value.AppID, value.EnvelopeID, owner)
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
		if acknowledgedAt != 0 {
			continue
		}
		return store.ErrConflict
	}
	return tx.Commit()
}

func validSocketModeInteractionValue(value domain.SocketModeInteraction) bool {
	if value.EnvelopeID == "" || value.AppID == "" || value.WorkspaceID == "" || value.UserID == "" ||
		(value.Type != "slash_commands" && value.Type != "interactive") || strings.TrimSpace(value.Payload) == "" ||
		value.Response.TokenHash == "" || value.Response.AppID != value.AppID ||
		value.Response.WorkspaceID != value.WorkspaceID || value.Response.UserID != value.UserID ||
		value.Response.ConversationID == "" || value.CreatedAt.IsZero() {
		return false
	}
	var payload map[string]any
	return json.Unmarshal([]byte(value.Payload), &payload) == nil && payload != nil
}

func (s *Store) CreateSocketModeInteraction(ctx context.Context, value domain.SocketModeInteraction) error {
	if !validSocketModeInteractionValue(value) {
		return store.InvalidArgument("invalid Socket Mode interaction")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO socket_mode_interactions(envelope_id, app_id, workspace_id, user_id, type, payload, response_token_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		value.EnvelopeID, value.AppID, value.WorkspaceID, value.UserID, value.Type, value.Payload, value.Response.TokenHash, value.CreatedAt.UTC().UnixNano())
	return classify(err)
}

func scanSocketModeInteraction(scanner interface{ Scan(...any) error }) (domain.SocketModeInteraction, error) {
	var value domain.SocketModeInteraction
	var createdAt, leaseExpiresAt, retryAt, acknowledgedAt int64
	var responseCreatedAt, responseExpiresAt int64
	err := scanner.Scan(
		&value.EnvelopeID, &value.AppID, &value.WorkspaceID, &value.UserID, &value.Type, &value.Payload,
		&createdAt, &value.LeaseOwner, &leaseExpiresAt, &retryAt, &value.RetryCount, &value.RetryReason, &acknowledgedAt,
		&value.Response.TokenHash, &value.Response.AppID, &value.Response.WorkspaceID, &value.Response.UserID,
		&value.Response.ConversationID, &value.Response.OriginalMessageID, &value.Response.ThreadTimestamp,
		&responseCreatedAt, &responseExpiresAt, &value.Response.UsesRemaining,
	)
	if err != nil {
		return domain.SocketModeInteraction{}, translateNotFound(err)
	}
	value.CreatedAt = time.Unix(0, createdAt).UTC()
	if leaseExpiresAt != 0 {
		value.LeaseExpiresAt = time.Unix(0, leaseExpiresAt).UTC()
	}
	if retryAt != 0 {
		value.RetryAt = time.Unix(0, retryAt).UTC()
	}
	if acknowledgedAt != 0 {
		value.AcknowledgedAt = time.Unix(0, acknowledgedAt).UTC()
	}
	value.Response.CreatedAt = time.Unix(0, responseCreatedAt).UTC()
	value.Response.ExpiresAt = time.Unix(0, responseExpiresAt).UTC()
	return value, nil
}

const socketModeInteractionSelect = `SELECT i.envelope_id, i.app_id, i.workspace_id, i.user_id, i.type, i.payload,
	i.created_at, i.lease_owner, i.lease_expires_at, i.retry_at, i.retry_count, i.retry_reason, i.acknowledged_at,
	r.token_hash, r.app_id, r.workspace_id, r.user_id, r.conversation_id, r.original_message_id, r.thread_timestamp,
	r.created_at, r.expires_at, r.uses_remaining
	FROM socket_mode_interactions i JOIN app_response_urls r ON r.token_hash = i.response_token_hash`

func (s *Store) GetSocketModeInteraction(ctx context.Context, appID domain.AppID, envelopeID string) (domain.SocketModeInteraction, error) {
	return scanSocketModeInteraction(s.db.QueryRowContext(ctx, socketModeInteractionSelect+` WHERE i.app_id = ? AND i.envelope_id = ?`, appID, envelopeID))
}

func (s *Store) ClaimSocketModeInteraction(ctx context.Context, appID domain.AppID, owner string, lease time.Duration) (domain.SocketModeInteraction, bool, error) {
	if appID == "" || strings.TrimSpace(owner) == "" || lease <= 0 {
		return domain.SocketModeInteraction{}, false, store.InvalidArgument("invalid Socket Mode interaction lease")
	}
	var claimed domain.SocketModeInteraction
	var found bool
	err := underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		now := time.Now().UTC()
		var envelopeID string
		err = tx.QueryRowContext(ctx, `SELECT envelope_id FROM socket_mode_interactions
			WHERE app_id = ? AND acknowledged_at = 0 AND retry_at <= ? AND lease_expires_at <= ?
			ORDER BY created_at, envelope_id LIMIT 1`, appID, now.UnixNano(), now.UnixNano()).Scan(&envelopeID)
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE socket_mode_interactions SET lease_owner = ?, lease_expires_at = ?
			WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND retry_at <= ? AND lease_expires_at <= ?`,
			owner, now.Add(lease).UnixNano(), appID, envelopeID, now.UnixNano(), now.UnixNano())
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return err
		}
		claimed, err = scanSocketModeInteraction(tx.QueryRowContext(ctx, socketModeInteractionSelect+` WHERE i.app_id = ? AND i.envelope_id = ?`, appID, envelopeID))
		if err != nil {
			return err
		}
		found = true
		return tx.Commit()
	})
	return claimed, found, err
}

func (s *Store) AckSocketModeInteraction(ctx context.Context, appID domain.AppID, envelopeID, owner string) error {
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, `UPDATE socket_mode_interactions SET acknowledged_at = ?, lease_owner = '', lease_expires_at = 0
		WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND lease_owner = ? AND lease_expires_at > ?`, now, appID, envelopeID, owner, now)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrLeaseConflict
	}
	return nil
}

func (s *Store) ReleaseSocketModeInteraction(ctx context.Context, appID domain.AppID, envelopeID, owner, reason string, retryAt time.Time) error {
	if strings.TrimSpace(reason) == "" || retryAt.IsZero() {
		return store.InvalidArgument("invalid Socket Mode interaction release")
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, `UPDATE socket_mode_interactions SET lease_owner = '', lease_expires_at = 0,
		retry_at = ?, retry_count = retry_count + 1, retry_reason = ?
		WHERE app_id = ? AND envelope_id = ? AND acknowledged_at = 0 AND lease_owner = ? AND lease_expires_at > ?`,
		retryAt.UTC().UnixNano(), strings.TrimSpace(reason), appID, envelopeID, owner, now)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrLeaseConflict
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
	if err := insertOutboxForConversation(ctx, tx, event, conversation); err != nil {
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

func (s *Store) ConvertGroupDirectToPrivate(ctx context.Context, conversion domain.GroupDirectConversion, emitted []events.Event) (domain.Conversation, error) {
	if len(emitted) != 2 || strings.TrimSpace(conversion.Name) == "" {
		return domain.Conversation{}, store.InvalidArgument("invalid group DM conversion")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	var workspace domain.WorkspaceID
	var private, direct, groupDirect int
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id, is_private, is_direct, is_group_direct FROM conversations WHERE id = ?`, conversion.Conversation).Scan(&workspace, &private, &direct, &groupDirect); errors.Is(err, sql.ErrNoRows) {
		return domain.Conversation{}, store.ErrNotFound
	} else if err != nil {
		return domain.Conversation{}, err
	}
	if private == 0 || direct != 0 || groupDirect == 0 {
		return domain.Conversation{}, store.ErrInvalidConversationType
	}
	if conversion.Notice.Conversation != conversion.Conversation || conversion.Notice.WorkspaceID != workspace {
		return domain.Conversation{}, store.InvalidArgument("invalid group DM conversion notice")
	}
	result, err := tx.ExecContext(ctx, `UPDATE conversations
		SET name = ?, name_folded = ?, is_private = 1, is_direct = 0, is_group_direct = 0, direct_key = ''
		WHERE id = ? AND is_private = 1 AND is_direct = 0 AND is_group_direct = 1`,
		conversion.Name, domain.FoldSearchText(conversion.Name), conversion.Conversation)
	if err != nil {
		return domain.Conversation{}, classify(err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return domain.Conversation{}, err
	} else if changed != 1 {
		return domain.Conversation{}, store.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM closed_direct_conversations WHERE conversation_id = ?`, conversion.Conversation); err != nil {
		return domain.Conversation{}, err
	}
	if err := insertOutbox(ctx, tx, emitted[0]); err != nil {
		return domain.Conversation{}, err
	}
	if err := insertFileShareMessage(ctx, tx, conversion.Notice, emitted[1]); err != nil {
		return domain.Conversation{}, err
	}
	var value domain.Conversation
	var archived int
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE id = ?`, conversion.Conversation).
		Scan(&value.ID, &value.WorkspaceID, &value.Name, &value.Topic, &value.Purpose, &archived, &private, &direct, &groupDirect); err != nil {
		return domain.Conversation{}, err
	}
	value.Archived, value.IsPrivate, value.IsDirect, value.IsGroupDirect = archived != 0, private != 0, direct != 0, groupDirect != 0
	if err := tx.Commit(); err != nil {
		return domain.Conversation{}, err
	}
	return value, nil
}

func (s *Store) GetConversationPrefs(ctx context.Context, conversation domain.ConversationID) (domain.ConversationPrefs, error) {
	// One statement instead of a GetConversation round trip followed by a second
	// read: the pair could observe two different database states, and the join
	// answers both questions at once.
	var canThreadTypes, canThreadUsers, whoCanPostTypes, whoCanPostUsers string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(p.can_thread_types, '[]'), COALESCE(p.can_thread_users, '[]'), COALESCE(p.who_can_post_types, '[]'), COALESCE(p.who_can_post_users, '[]') FROM conversations c LEFT JOIN conversation_prefs p ON p.conversation_id = c.id WHERE c.id = ?`, conversation).Scan(&canThreadTypes, &canThreadUsers, &whoCanPostTypes, &whoCanPostUsers)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationPrefs{}, store.ErrNotFound
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
	// Prove the conversation exists inside the transaction that writes its
	// preferences; the existence check used to run on s.db beforehand, so the
	// conversation could be deleted in between.
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ?`, conversation).Scan(&exists); err != nil {
		return domain.ConversationPrefs{}, translateNotFound(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_prefs(conversation_id, can_thread_types, can_thread_users, who_can_post_types, who_can_post_users) VALUES (?, ?, ?, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET can_thread_types = excluded.can_thread_types, can_thread_users = excluded.can_thread_users, who_can_post_types = excluded.who_can_post_types, who_can_post_users = excluded.who_can_post_users`, conversation, canTypes, canUsers, postTypes, postUsers); err != nil {
		return domain.ConversationPrefs{}, classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddConversationMember(ctx context.Context, conversation domain.ConversationID, user domain.UserID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The privacy check has to happen inside the transaction that writes the
	// membership. Reading it on s.db first left a window in which
	// conversations.setPrivate could commit between the check and the insert, so a
	// non-member was added to a channel that had become private — exactly what the
	// check exists to prevent. InviteConversationMembers already does it this way.
	var private int
	if err := tx.QueryRowContext(ctx, `SELECT is_private FROM conversations WHERE id = ?`, conversation).Scan(&private); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	} else if private != 0 {
		return store.ErrNotFound
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) VALUES (?, ?) ON CONFLICT(conversation_id, user_id) DO NOTHING`, conversation, user)
	if err != nil {
		return classify(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) InviteConversationMembers(ctx context.Context, conversation domain.ConversationID, users []domain.UserID, event events.Event) error {
	if len(users) == 0 {
		return store.InvalidArgument("conversation invite requires at least one user")
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM thread_follows WHERE conversation_id = ? AND user_id = ?`, conversation, user); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_notification_preferences WHERE conversation_id = ? AND user_id = ?`, conversation, user); err != nil {
		return err
	}
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
	if err := insertOutboxForConversation(ctx, tx, event, conversation); err != nil {
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
	cursor.UpdatedAt, err = domain.ParseStoredTime(updated)
	return cursor, err
}

func (s *Store) GetWorkspaceNotificationPreferences(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID) (domain.WorkspaceNotificationPreferences, error) {
	preferences := domain.DefaultWorkspaceNotificationPreferences(workspace, user)
	var keywords string
	var activityChannels, activityReminders int
	err := s.db.QueryRowContext(ctx, `SELECT level, keywords, activity_channels, activity_reminders FROM notification_preferences WHERE workspace_id = ? AND user_id = ?`, workspace, user).
		Scan(&preferences.Level, &keywords, &activityChannels, &activityReminders)
	if errors.Is(err, sql.ErrNoRows) {
		return preferences, nil
	}
	if err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	if err := json.Unmarshal([]byte(keywords), &preferences.Keywords); err != nil {
		return domain.WorkspaceNotificationPreferences{}, err
	}
	preferences.Keywords = domain.NormalizeNotificationKeywords(preferences.Keywords)
	preferences.ActivityChannels = activityChannels != 0
	preferences.ActivityReminders = activityReminders != 0
	if !preferences.Valid() {
		return domain.WorkspaceNotificationPreferences{}, errors.New("stored workspace notification preferences are invalid")
	}
	return preferences, nil
}

func (s *Store) SetWorkspaceNotificationPreferences(ctx context.Context, preferences domain.WorkspaceNotificationPreferences, event events.Event) error {
	preferences.Keywords = domain.NormalizeNotificationKeywords(preferences.Keywords)
	if !preferences.Valid() {
		return store.InvalidArgument("workspace notification preferences are invalid")
	}
	keywords, err := json.Marshal(preferences.Keywords)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_preferences(workspace_id, user_id, level, keywords, activity_channels, activity_reminders)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id) DO UPDATE SET level = excluded.level, keywords = excluded.keywords, activity_channels = excluded.activity_channels, activity_reminders = excluded.activity_reminders`,
		preferences.WorkspaceID, preferences.UserID, preferences.Level, string(keywords), boolInt(preferences.ActivityChannels), boolInt(preferences.ActivityReminders)); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetConversationNotificationPreferences(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) (domain.ConversationNotificationPreferences, error) {
	var member int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations c JOIN conversation_members cm ON cm.conversation_id = c.id AND cm.user_id = ? WHERE c.id = ? AND c.workspace_id = ?`, user, conversation, workspace).Scan(&member); err != nil {
		return domain.ConversationNotificationPreferences{}, err
	}
	if member != 1 {
		return domain.ConversationNotificationPreferences{}, store.ErrNotFound
	}
	preferences := domain.DefaultConversationNotificationPreferences(workspace, user, conversation)
	var followEveryThread int
	err := s.db.QueryRowContext(ctx, `SELECT level, follow_every_thread FROM conversation_notification_preferences WHERE workspace_id = ? AND user_id = ? AND conversation_id = ?`, workspace, user, conversation).
		Scan(&preferences.Level, &followEveryThread)
	if errors.Is(err, sql.ErrNoRows) {
		return preferences, nil
	}
	if err != nil {
		return domain.ConversationNotificationPreferences{}, err
	}
	preferences.FollowEveryThread = followEveryThread != 0
	if !preferences.Valid() {
		return domain.ConversationNotificationPreferences{}, errors.New("stored conversation notification preferences are invalid")
	}
	return preferences, nil
}

func (s *Store) SetConversationNotificationPreferences(ctx context.Context, preferences domain.ConversationNotificationPreferences, event events.Event) error {
	if !preferences.Valid() {
		return store.InvalidArgument("conversation notification preferences are invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var member int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations c JOIN conversation_members cm ON cm.conversation_id = c.id AND cm.user_id = ? WHERE c.id = ? AND c.workspace_id = ?`, preferences.UserID, preferences.Conversation, preferences.WorkspaceID).Scan(&member); err != nil {
		return err
	}
	if member != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_notification_preferences(workspace_id, user_id, conversation_id, level, follow_every_thread)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, conversation_id) DO UPDATE SET level = excluded.level, follow_every_thread = excluded.follow_every_thread`,
		preferences.WorkspaceID, preferences.UserID, preferences.Conversation, preferences.Level, boolInt(preferences.FollowEveryThread)); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) IsThreadFollowed(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, root domain.MessageTimestamp) (bool, error) {
	if _, err := domain.ParseMessageTimestamp(root); err != nil {
		return false, err
	}
	var member, followed int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations c JOIN conversation_members cm ON cm.conversation_id = c.id AND cm.user_id = ? WHERE c.id = ? AND c.workspace_id = ?`, user, conversation, workspace).Scan(&member); err != nil {
		return false, err
	}
	if member != 1 {
		return false, store.ErrNotFound
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_follows WHERE workspace_id = ? AND user_id = ? AND conversation_id = ? AND root_timestamp = ?`, workspace, user, conversation, root).Scan(&followed); err != nil {
		return false, err
	}
	return followed != 0, nil
}

func (s *Store) SetThreadFollowed(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, root domain.MessageTimestamp, followed bool, event events.Event) error {
	if _, err := domain.ParseMessageTimestamp(root); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var member int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations c JOIN conversation_members cm ON cm.conversation_id = c.id AND cm.user_id = ? WHERE c.id = ? AND c.workspace_id = ?`, user, conversation, workspace).Scan(&member); err != nil {
		return err
	}
	if member != 1 {
		return store.ErrNotFound
	}
	if followed {
		_, err = tx.ExecContext(ctx, `INSERT INTO thread_follows(workspace_id, user_id, conversation_id, root_timestamp) VALUES (?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, conversation_id, root_timestamp) DO NOTHING`, workspace, user, conversation, root)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM thread_follows WHERE workspace_id = ? AND user_id = ? AND conversation_id = ? AND root_timestamp = ?`, workspace, user, conversation, root)
	}
	if err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetReadCursor(ctx context.Context, cursor domain.ReadCursor, event events.Event) error {
	readAt, err := domain.ParseMessageTimestamp(cursor.LastRead)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO read_cursors(workspace_id, user_id, conversation_id, last_read, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, conversation_id) DO UPDATE SET last_read = excluded.last_read, updated_at = excluded.updated_at`, cursor.WorkspaceID, cursor.UserID, cursor.Conversation, cursor.LastRead, domain.NewStoredTime(cursor.UpdatedAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE activity_items SET read_at = ? WHERE workspace_id = ? AND user_id = ? AND conversation_id = ? AND occurred_at <= ? AND read_at = 0`,
		cursor.UpdatedAt.UTC().UnixNano(), cursor.WorkspaceID, cursor.UserID, cursor.Conversation, readAt.UTC().UnixNano()); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func activityCursor(item domain.ActivityItem) string {
	return fmt.Sprintf("%020d:%s", item.OccurredAt.UTC().UnixNano(), item.ID)
}

func parseActivityCursor(value domain.Cursor) (int64, domain.ActivityID, error) {
	decoded, err := domain.DecodeListCursor(value)
	if err != nil || decoded == "" {
		return 0, "", err
	}
	parts := strings.SplitN(decoded, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return 0, "", domain.ErrInvalidCursor
	}
	occurred, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", domain.ErrInvalidCursor
	}
	return occurred, domain.ActivityID(parts[1]), nil
}

func (s *Store) ListActivity(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.ActivityQuery) (domain.ActivityPage, error) {
	if err := store.CheckPage(request.Page); err != nil {
		return domain.ActivityPage{}, err
	}
	if !request.Valid() {
		return domain.ActivityPage{}, store.InvalidArgument("activity filter is invalid")
	}
	query := `SELECT a.id, a.workspace_id, a.user_id, a.actor_id, a.conversation_id, a.message_id, a.reminder_id, a.reaction_name, a.occurred_at, a.read_at, a.cleared_at
		FROM activity_items a WHERE a.workspace_id = ? AND a.user_id = ?`
	args := []any{workspace, user}
	if request.ClearedOnly {
		query += ` AND a.cleared_at <> 0`
	} else {
		query += ` AND a.cleared_at = 0`
	}
	if request.UnreadOnly {
		query += ` AND a.read_at = 0`
	}
	if len(request.Kinds) > 0 {
		query += ` AND EXISTS (SELECT 1 FROM activity_item_kinds k WHERE k.activity_id = a.id AND k.kind IN (` + placeholders(len(request.Kinds)) + `))`
		for _, kind := range request.Kinds {
			args = append(args, kind)
		}
	}
	if request.Page.Cursor != "" {
		occurred, id, err := parseActivityCursor(request.Page.Cursor)
		if err != nil {
			return domain.ActivityPage{}, err
		}
		query += ` AND (a.occurred_at < ? OR (a.occurred_at = ? AND a.id < ?))`
		args = append(args, occurred, occurred, id)
	}
	query += ` ORDER BY a.occurred_at DESC, a.id DESC LIMIT ?`
	args = append(args, request.Page.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ActivityPage{}, err
	}
	items := make([]domain.ActivityItem, 0, request.Page.Limit+1)
	for rows.Next() {
		var item domain.ActivityItem
		var occurredAt, readAt, clearedAt int64
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.UserID, &item.ActorID, &item.Conversation, &item.MessageID, &item.ReminderID, &item.ReactionName, &occurredAt, &readAt, &clearedAt); err != nil {
			rows.Close()
			return domain.ActivityPage{}, err
		}
		item.OccurredAt = time.Unix(0, occurredAt).UTC()
		if readAt != 0 {
			item.ReadAt = time.Unix(0, readAt).UTC()
		}
		if clearedAt != 0 {
			item.ClearedAt = time.Unix(0, clearedAt).UTC()
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return domain.ActivityPage{}, err
	}
	if err := rows.Err(); err != nil {
		return domain.ActivityPage{}, err
	}
	page := domain.ActivityPage{Items: items, HasMore: len(items) > request.Page.Limit}
	if page.HasMore {
		page.Items = page.Items[:request.Page.Limit]
		page.NextCursor, err = domain.NewListCursor(activityCursor(page.Items[len(page.Items)-1]))
		if err != nil {
			return domain.ActivityPage{}, err
		}
	}
	for index := range page.Items {
		item := &page.Items[index]
		kindRows, err := s.db.QueryContext(ctx, `SELECT kind FROM activity_item_kinds WHERE activity_id = ? ORDER BY kind`, item.ID)
		if err != nil {
			return domain.ActivityPage{}, err
		}
		for kindRows.Next() {
			var kind domain.ActivityKind
			if err := kindRows.Scan(&kind); err != nil {
				kindRows.Close()
				return domain.ActivityPage{}, err
			}
			item.Kinds = append(item.Kinds, kind)
		}
		if err := kindRows.Close(); err != nil {
			return domain.ActivityPage{}, err
		}
		if item.ReminderID != "" {
			if reminder, reminderErr := s.GetLaterReminder(ctx, workspace, user, item.ReminderID); reminderErr == nil {
				item.Reminder = reminder
				item.SourceAvailable = true
			} else if !errors.Is(reminderErr, store.ErrNotFound) {
				return domain.ActivityPage{}, reminderErr
			}
		}
		if item.MessageID != "" {
			visible, visibilityErr := s.activitySourceVisible(ctx, workspace, user, item.Conversation)
			if visibilityErr != nil {
				return domain.ActivityPage{}, visibilityErr
			}
			if visible {
				if message, messageErr := s.GetMessage(ctx, item.MessageID); messageErr == nil && !message.Deleted {
					item.Message = message
					item.SourceAvailable = true
				} else if messageErr != nil && !errors.Is(messageErr, store.ErrNotFound) {
					return domain.ActivityPage{}, messageErr
				}
			}
		}
	}
	return page, nil
}

func (s *Store) activitySourceVisible(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID) (bool, error) {
	var visible int
	err := s.db.QueryRowContext(ctx, `SELECT CASE
		WHEN c.is_private = 0 AND c.is_direct = 0 AND c.is_group_direct = 0 THEN 1
		WHEN EXISTS (
			SELECT 1 FROM conversation_members cm
			WHERE cm.conversation_id = c.id AND cm.user_id = wm.user_id
		) AND (
			NOT EXISTS (
				SELECT 1 FROM conversation_access_groups cag
				WHERE cag.conversation_id = c.id
			)
			OR EXISTS (
				SELECT 1
				FROM conversation_access_groups cag
				JOIN user_groups ug ON ug.id = cag.group_id
					AND ug.workspace_id = c.workspace_id
					AND ug.enabled = 1
					AND ug.deleted_at = 0
				JOIN user_group_users ugu ON ugu.group_id = ug.id
					AND ugu.user_id = wm.user_id
				WHERE cag.conversation_id = c.id
			)
		) THEN 1
		ELSE 0
	END
	FROM conversations c
	JOIN workspace_members wm ON wm.workspace_id = c.workspace_id
		AND wm.user_id = ?
		AND wm.active = 1
	JOIN users u ON u.workspace_id = wm.workspace_id
		AND u.id = wm.user_id
		AND u.deleted = 0
	WHERE c.id = ? AND c.workspace_id = ?`, user, conversation, workspace).Scan(&visible)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return visible != 0, err
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func (s *Store) MutateActivity(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, ids []domain.ActivityID, mutation domain.ActivityMutation, changedAt time.Time) error {
	if workspace == "" || user == "" || len(ids) == 0 || !mutation.Valid() || changedAt.IsZero() {
		return store.InvalidArgument("activity mutation is incomplete")
	}
	unique := make([]domain.ActivityID, 0, len(ids))
	seen := make(map[domain.ActivityID]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return store.InvalidArgument("activity item id is required")
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	args := []any{workspace, user}
	for _, id := range unique {
		args = append(args, id)
	}
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_items WHERE workspace_id = ? AND user_id = ? AND id IN (`+placeholders(len(unique))+`)`, args...).Scan(&found); err != nil {
		return err
	}
	if found != len(unique) {
		return store.ErrNotFound
	}
	set := ""
	changed := changedAt.UTC().UnixNano()
	updateArgs := []any{}
	switch mutation {
	case domain.ActivityMarkRead:
		set, updateArgs = `read_at = ?`, []any{changed}
	case domain.ActivityMarkUnread:
		set = `read_at = 0`
	case domain.ActivityClear:
		set, updateArgs = `read_at = ?, cleared_at = ?`, []any{changed, changed}
	case domain.ActivityRestore:
		set = `cleared_at = 0`
	}
	updateArgs = append(updateArgs, workspace, user)
	for _, id := range unique {
		updateArgs = append(updateArgs, id)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE activity_items SET `+set+` WHERE workspace_id = ? AND user_id = ? AND id IN (`+placeholders(len(unique))+`)`, updateArgs...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetActivityPreferences(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID) (domain.ActivityPreferences, error) {
	preferences := domain.ActivityPreferences{WorkspaceID: workspace, UserID: user, Layout: domain.ActivityDetailed}
	err := s.db.QueryRowContext(ctx, `SELECT layout FROM activity_preferences WHERE workspace_id = ? AND user_id = ?`, workspace, user).Scan(&preferences.Layout)
	if errors.Is(err, sql.ErrNoRows) {
		return preferences, nil
	}
	return preferences, err
}

func (s *Store) SetActivityPreferences(ctx context.Context, preferences domain.ActivityPreferences) error {
	if preferences.WorkspaceID == "" || preferences.UserID == "" || !preferences.Layout.Valid() {
		return store.InvalidArgument("activity preferences are invalid")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO activity_preferences(workspace_id, user_id, layout) VALUES (?, ?, ?) ON CONFLICT(workspace_id, user_id) DO UPDATE SET layout = excluded.layout`,
		preferences.WorkspaceID, preferences.UserID, preferences.Layout)
	return classify(err)
}

func (s *Store) ListConversations(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.ConversationListRequest) (domain.ConversationPage, error) {
	if request.Limit <= 0 {
		return domain.ConversationPage{}, store.InvalidArgument("page limit must be positive")
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
	if !request.IncludeClosedDirects {
		query += ` AND ((c.is_direct = 0 AND c.is_group_direct = 0) OR NOT EXISTS (SELECT 1 FROM closed_direct_conversations closed WHERE closed.workspace_id = c.workspace_id AND closed.user_id = ? AND closed.conversation_id = c.id))`
		args = append(args, user)
	}
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
				return domain.ConversationPage{}, store.InvalidArgument("invalid conversation type")
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ConversationPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ConversationPage{}, err
	}
	query = domain.FoldSearchText(strings.TrimSpace(query))
	if query == "" {
		return domain.ConversationPage{}, store.InvalidArgument("conversation search query is required")
	}
	// escapeLikeTerm plus an explicit ESCAPE clause: without them "%" matched
	// every conversation in the workspace, "_" matched any single character, and
	// a backslash was a literal on SQLite but the default escape character on
	// PostgreSQL, so one query returned three different result sets.
	sqlQuery := `SELECT id, workspace_id, name, topic, purpose, archived, is_private, is_direct, is_group_direct FROM conversations WHERE workspace_id = ? AND (name_folded LIKE ? ESCAPE '\' OR topic_folded LIKE ? ESCAPE '\' OR purpose_folded LIKE ? ESCAPE '\')`
	pattern := "%" + escapeLikeTerm(query) + "%"
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
	var lastRead domain.StoredTime
	cursor, err := s.GetReadCursor(ctx, workspace, user, conversation)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	if err == nil {
		parsed, parseErr := domain.ParseMessageTimestamp(cursor.LastRead)
		if parseErr != nil {
			return 0, parseErr
		}
		lastRead = domain.NewStoredTime(parsed)
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
	// A message may not be stored at a finer resolution than its own timestamp
	// can express, or a read cursor built from that timestamp can never cover it
	// — and it may not be stored at an instant another message in the same
	// conversation already owns, or the two share one public identifier. See
	// runMessageIdentityBackfill: truncating alone merged identities.
	message.CreatedAt = domain.MessageInstant(message.CreatedAt)
	blocks, err := domain.NormalizeBlocks([]byte(message.Blocks))
	if err != nil {
		return err
	}
	attachments, err := domain.NormalizeAttachments([]byte(message.Attachments))
	if err != nil {
		return err
	}
	if attachments == "" {
		attachments = "[]"
	}
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
		result, err := tx.ExecContext(ctx, `INSERT INTO idempotency (workspace_id, user_id, idempotency_key, message_id, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, idempotency_key) DO NOTHING`, message.WorkspaceID, message.AuthorID, idempotencyKey, message.ID, domain.NewStoredTime(time.Now()))
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
	stored := domain.NewStoredTime(message.CreatedAt)
	// The check inside the transaction is what makes the common case a clean,
	// named answer instead of a constraint failure; the UNIQUE index installed by
	// runMessageIdentityBackfill is what makes it correct when two writers race on
	// an engine that lets both transactions read before either writes.
	var owner domain.MessageID
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE conversation = ? AND created_at = ?`, message.Conversation, stored).Scan(&owner); {
	case err == nil:
		_ = tx.Rollback()
		if owner == message.ID {
			// The same message, not a contested instant: the caller's remedy is a
			// different identifier, not a different microsecond.
			return store.ErrAlreadyExists
		}
		return store.ErrMessageTimestampTaken
	case errors.Is(err, sql.ErrNoRows):
	default:
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO messages (id, workspace_id, conversation, author_id, app_id, text, blocks, attachments, metadata, stream_state, thread_timestamp, created_at, deleted, unfurls, text_folded) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`, message.ID, message.WorkspaceID, message.Conversation, message.AuthorID, message.AppID, message.Text, blocks, attachments, message.Metadata, message.StreamState, message.ThreadTimestamp, stored, unfurls, domain.FoldSearchText(message.Text)); err != nil {
		_ = tx.Rollback()
		// A duplicate identifier is ErrAlreadyExists and a missing conversation,
		// author or workspace is ErrNotFound; neither may reach the caller as a raw
		// driver error naming the constraint. A duplicate that is the CONVERSATION
		// TIMESTAMP rather than the message id has its own answer, because the
		// caller's remedy is different: it must pick another instant, not another
		// identifier. The two are told apart by re-reading after the rollback
		// rather than by reading the driver's English.
		classified := classify(err)
		if errors.Is(classified, store.ErrAlreadyExists) && s.messageTimestampTaken(ctx, message.Conversation, message.ID, stored) {
			return store.ErrMessageTimestampTaken
		}
		return classified
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM closed_direct_conversations WHERE conversation_id = ?`, message.Conversation); err != nil {
		return err
	}
	if err := insertMessageFiles(ctx, tx, message); err != nil {
		return err
	}
	if err := insertMessageActivity(ctx, tx, message); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func insertMessageFiles(ctx context.Context, tx *sql.Tx, message domain.Message) error {
	seen := make(map[domain.FileID]struct{}, len(message.Files))
	for position, file := range message.Files {
		if file.ID == "" {
			return store.InvalidArgument("message file id is required")
		}
		if _, duplicate := seen[file.ID]; duplicate {
			return store.InvalidArgument("message file ids must be unique")
		}
		seen[file.ID] = struct{}{}
		var workspace domain.WorkspaceID
		var deleted int
		if err := tx.QueryRowContext(ctx, `SELECT workspace_id, deleted FROM files WHERE id = ?`, file.ID).Scan(&workspace, &deleted); errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		} else if err != nil {
			return err
		}
		if workspace != message.WorkspaceID || deleted != 0 {
			return store.ErrNotFound
		}
		var shared int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM file_shares WHERE file_id = ? AND conversation_id = ?`, file.ID, message.Conversation).Scan(&shared); errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_files(message_id, file_id, position) VALUES (?, ?, ?)`, message.ID, file.ID, position); err != nil {
			return classify(err)
		}
	}
	return nil
}

func insertFileShareMessage(ctx context.Context, tx *sql.Tx, message domain.Message, event events.Event) error {
	message.CreatedAt = domain.MessageInstant(message.CreatedAt)
	blocks, err := domain.NormalizeBlocks([]byte(message.Blocks))
	if err != nil {
		return err
	}
	attachments, err := domain.NormalizeAttachments([]byte(message.Attachments))
	if err != nil {
		return err
	}
	if attachments == "" {
		attachments = "[]"
	}
	unfurls, err := encodeUnfurls(message.Unfurls)
	if err != nil {
		return err
	}
	stored := domain.NewStoredTime(message.CreatedAt)
	var owner domain.MessageID
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE conversation = ? AND created_at = ?`, message.Conversation, stored).Scan(&owner); {
	case err == nil:
		if owner == message.ID {
			return store.ErrAlreadyExists
		}
		return store.ErrMessageTimestampTaken
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages (id, workspace_id, conversation, author_id, app_id, text, blocks, attachments, metadata, stream_state, thread_timestamp, created_at, deleted, unfurls, text_folded) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		message.ID, message.WorkspaceID, message.Conversation, message.AuthorID, message.AppID, message.Text, blocks, attachments, message.Metadata, message.StreamState, message.ThreadTimestamp, stored, unfurls, domain.FoldSearchText(message.Text)); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM closed_direct_conversations WHERE conversation_id = ?`, message.Conversation); err != nil {
		return err
	}
	if err := insertMessageFiles(ctx, tx, message); err != nil {
		return err
	}
	if err := insertMessageActivity(ctx, tx, message); err != nil {
		return err
	}
	return insertOutbox(ctx, tx, event)
}

func insertMessageActivity(ctx context.Context, tx *sql.Tx, message domain.Message) error {
	var private, direct, groupDirect int
	if err := tx.QueryRowContext(ctx, `SELECT is_private, is_direct, is_group_direct FROM conversations WHERE id = ? AND workspace_id = ?`, message.Conversation, message.WorkspaceID).Scan(&private, &direct, &groupDirect); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
	recipients := make(map[domain.UserID]map[domain.ActivityKind]struct{})
	visible := make(map[domain.UserID]struct{})
	add := func(user domain.UserID, kind domain.ActivityKind) bool {
		if user == "" || user == message.AuthorID {
			return false
		}
		if _, ok := visible[user]; !ok {
			return false
		}
		if recipients[user] == nil {
			recipients[user] = make(map[domain.ActivityKind]struct{})
		}
		recipients[user][kind] = struct{}{}
		return true
	}
	rows, err := tx.QueryContext(ctx, `SELECT cm.user_id
		FROM conversation_members cm
		JOIN workspace_members wm ON wm.workspace_id = ? AND wm.user_id = cm.user_id AND wm.active = 1
		JOIN users u ON u.workspace_id = wm.workspace_id AND u.id = wm.user_id AND u.deleted = 0
		WHERE cm.conversation_id = ?
		ORDER BY cm.user_id`, message.WorkspaceID, message.Conversation)
	if err != nil {
		return err
	}
	members := make([]domain.UserID, 0)
	for rows.Next() {
		var user domain.UserID
		if err := rows.Scan(&user); err != nil {
			rows.Close()
			return err
		}
		members = append(members, user)
		visible[user] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Private access-group restrictions remain authoritative for notification
	// creation. A stale association to a missing/disabled group withholds source
	// access instead of silently becoming unrestricted.
	if private != 0 || direct != 0 || groupDirect != 0 {
		var restrictionCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_access_groups WHERE conversation_id = ?`, message.Conversation).Scan(&restrictionCount); err != nil {
			return err
		}
		if restrictionCount > 0 {
			allowed := make(map[domain.UserID]struct{})
			groupRows, err := tx.QueryContext(ctx, `SELECT DISTINCT ugu.user_id
				FROM conversation_access_groups cag
				JOIN user_groups ug ON ug.id = cag.group_id AND ug.workspace_id = ? AND ug.enabled = 1 AND ug.deleted_at = 0
				JOIN user_group_users ugu ON ugu.group_id = ug.id
				WHERE cag.conversation_id = ?`, message.WorkspaceID, message.Conversation)
			if err != nil {
				return err
			}
			for groupRows.Next() {
				var user domain.UserID
				if err := groupRows.Scan(&user); err != nil {
					groupRows.Close()
					return err
				}
				allowed[user] = struct{}{}
			}
			if err := groupRows.Close(); err != nil {
				return err
			}
			for user := range visible {
				if _, ok := allowed[user]; !ok {
					delete(visible, user)
				}
			}
		}
	}
	mentioned := make(map[domain.UserID]struct{})
	messageMentions := domain.MentionsInMessage(message.Text, message.Blocks)
	for _, user := range messageMentions.Users {
		mentioned[user] = struct{}{}
	}
	groupIDs := messageMentions.UserGroups
	if len(groupIDs) > 0 {
		arguments := make([]any, 0, len(groupIDs)+1)
		arguments = append(arguments, message.WorkspaceID)
		for _, groupID := range groupIDs {
			arguments = append(arguments, groupID)
		}
		groupRows, err := tx.QueryContext(ctx, `SELECT DISTINCT ugu.user_id
			FROM user_groups ug
			JOIN user_group_users ugu ON ugu.group_id = ug.id
			WHERE ug.workspace_id = ? AND ug.enabled = 1 AND ug.deleted_at = 0
			  AND ug.id IN (`+placeholders(len(groupIDs))+`)`, arguments...)
		if err != nil {
			return err
		}
		for groupRows.Next() {
			var user domain.UserID
			if err := groupRows.Scan(&user); err != nil {
				groupRows.Close()
				return err
			}
			mentioned[user] = struct{}{}
		}
		if err := groupRows.Close(); err != nil {
			return err
		}
	}
	// Slack exposes public channels to workspace members before they join and
	// delivers a mention into Activity. Resolve only mentioned non-members here;
	// ordinary messages retain the bounded conversation-member query above.
	if private == 0 && direct == 0 && groupDirect == 0 && len(mentioned) > 0 {
		arguments := make([]any, 0, len(mentioned)+1)
		arguments = append(arguments, message.WorkspaceID)
		for user := range mentioned {
			arguments = append(arguments, user)
		}
		activeRows, err := tx.QueryContext(ctx, `SELECT wm.user_id
			FROM workspace_members wm
			JOIN users u ON u.workspace_id = wm.workspace_id AND u.id = wm.user_id AND u.deleted = 0
			WHERE wm.workspace_id = ? AND wm.active = 1
			  AND wm.user_id IN (`+placeholders(len(mentioned))+`)`, arguments...)
		if err != nil {
			return err
		}
		for activeRows.Next() {
			var user domain.UserID
			if err := activeRows.Scan(&user); err != nil {
				activeRows.Close()
				return err
			}
			visible[user] = struct{}{}
		}
		if err := activeRows.Close(); err != nil {
			return err
		}
	}
	root := domain.NewMessageTimestamp(message.CreatedAt)
	if message.ThreadTimestamp != "" {
		root = message.ThreadTimestamp
	}
	follow := func(user domain.UserID) error {
		if user == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO thread_follows(workspace_id, user_id, conversation_id, root_timestamp) VALUES (?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, conversation_id, root_timestamp) DO NOTHING`,
			message.WorkspaceID, user, message.Conversation, root)
		return err
	}
	if err := follow(message.AuthorID); err != nil {
		return err
	}
	addMention := func(user domain.UserID) error {
		if !add(user, domain.ActivityMention) {
			return nil
		}
		if message.ThreadTimestamp != "" {
			add(user, domain.ActivityThread)
			return follow(user)
		}
		return nil
	}
	for user := range mentioned {
		if err := addMention(user); err != nil {
			return err
		}
	}
	for _, user := range members {
		if direct != 0 || groupDirect != 0 {
			add(user, domain.ActivityDM)
		}
		workspacePreferences := domain.DefaultWorkspaceNotificationPreferences(message.WorkspaceID, user)
		var keywordsJSON string
		var activityChannels, activityReminders int
		err := tx.QueryRowContext(ctx, `SELECT level, keywords, activity_channels, activity_reminders FROM notification_preferences WHERE workspace_id = ? AND user_id = ?`, message.WorkspaceID, user).
			Scan(&workspacePreferences.Level, &keywordsJSON, &activityChannels, &activityReminders)
		if err == nil {
			if err := json.Unmarshal([]byte(keywordsJSON), &workspacePreferences.Keywords); err != nil {
				return err
			}
			workspacePreferences.Keywords = domain.NormalizeNotificationKeywords(workspacePreferences.Keywords)
			workspacePreferences.ActivityChannels = activityChannels != 0
			workspacePreferences.ActivityReminders = activityReminders != 0
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		conversationPreferences := domain.DefaultConversationNotificationPreferences(message.WorkspaceID, user, message.Conversation)
		var followEveryThread int
		err = tx.QueryRowContext(ctx, `SELECT level, follow_every_thread FROM conversation_notification_preferences WHERE workspace_id = ? AND user_id = ? AND conversation_id = ?`, message.WorkspaceID, user, message.Conversation).
			Scan(&conversationPreferences.Level, &followEveryThread)
		if err == nil {
			conversationPreferences.FollowEveryThread = followEveryThread != 0
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		effective := conversationPreferences.EffectiveLevel(workspacePreferences)
		if message.ThreadTimestamp == "" && direct == 0 && groupDirect == 0 {
			if effective == domain.NotificationAll && workspacePreferences.ActivityChannels {
				add(user, domain.ActivityChannel)
			}
			if effective != domain.NotificationMute && domain.MatchesNotificationKeyword(message.Text, workspacePreferences.Keywords) {
				add(user, domain.ActivityKeyword)
			}
		}
		if message.ThreadTimestamp != "" {
			var followed int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_follows WHERE workspace_id = ? AND user_id = ? AND conversation_id = ? AND root_timestamp = ?`, message.WorkspaceID, user, message.Conversation, root).Scan(&followed); err != nil {
				return err
			}
			if conversationPreferences.FollowEveryThread || followed != 0 {
				add(user, domain.ActivityThread)
			}
		}
	}
	if message.ThreadTimestamp != "" {
		rootAt, err := domain.ParseMessageTimestamp(message.ThreadTimestamp)
		if err != nil {
			return err
		}
		var author domain.UserID
		err = tx.QueryRowContext(ctx, `SELECT m.author_id FROM messages m JOIN conversation_members cm ON cm.conversation_id = m.conversation AND cm.user_id = m.author_id WHERE m.conversation = ? AND m.created_at = ?`, message.Conversation, domain.NewStoredTime(rootAt)).Scan(&author)
		if err == nil {
			add(author, domain.ActivityThread)
			if err := follow(author); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	for user, set := range recipients {
		kinds := make([]domain.ActivityKind, 0, len(set)+1)
		for kind := range set {
			kinds = append(kinds, kind)
		}
		if message.AppID != "" {
			kinds = append(kinds, domain.ActivityApp)
		}
		slices.Sort(kinds)
		id := domain.ActivityIDFor(user, "message:"+string(message.ID))
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity_items(id, workspace_id, user_id, actor_id, conversation_id, message_id, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			id, message.WorkspaceID, user, message.AuthorID, message.Conversation, message.ID, message.CreatedAt.UTC().UnixNano()); err != nil {
			return err
		}
		for _, kind := range kinds {
			if _, err := tx.ExecContext(ctx, `INSERT INTO activity_item_kinds(activity_id, kind) VALUES (?, ?) ON CONFLICT(activity_id, kind) DO NOTHING`, id, kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) CreateFileShareMessage(ctx context.Context, fileIDs []domain.FileID, message domain.Message, event events.Event) error {
	if len(fileIDs) == 0 {
		return store.InvalidArgument("a file share message requires a file")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	message.Files = make([]domain.File, len(fileIDs))
	for index, fileID := range fileIDs {
		var workspace domain.WorkspaceID
		var deleted int
		if err := tx.QueryRowContext(ctx, `SELECT workspace_id, deleted FROM files WHERE id = ?`, fileID).Scan(&workspace, &deleted); errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		} else if err != nil {
			return err
		}
		if workspace != message.WorkspaceID || deleted != 0 {
			return store.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO file_shares(file_id, conversation_id) VALUES (?, ?) ON CONFLICT(file_id, conversation_id) DO NOTHING`, fileID, message.Conversation); err != nil {
			return classify(err)
		}
		message.Files[index].ID = fileID
	}
	if err := insertFileShareMessage(ctx, tx, message, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateEphemeralMessage(ctx context.Context, value domain.EphemeralMessage, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.Conversation == "" || value.AuthorID == "" || value.RecipientID == "" || value.CreatedAt.IsZero() || value.Timestamp == "" {
		return store.InvalidArgument("invalid ephemeral message")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO ephemeral_messages(id, workspace_id, conversation_id, author_id, app_id, recipient_id, text, blocks, attachments, timestamp, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.WorkspaceID, value.Conversation, value.AuthorID, value.AppID, value.RecipientID, value.Text, value.Blocks, value.Attachments, value.Timestamp, domain.NewStoredTime(value.CreatedAt)); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListEphemeralMessages(ctx context.Context, workspaceID domain.WorkspaceID, recipientID domain.UserID, conversationID domain.ConversationID, limit int) ([]domain.EphemeralMessage, error) {
	if workspaceID == "" || recipientID == "" || conversationID == "" || limit <= 0 || limit > 1000 {
		return nil, store.InvalidArgument("invalid ephemeral message page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, conversation_id, author_id, app_id, recipient_id, text, blocks, attachments, timestamp, created_at
		FROM ephemeral_messages WHERE workspace_id = ? AND recipient_id = ? AND conversation_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, workspaceID, recipientID, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.EphemeralMessage, 0, limit)
	for rows.Next() {
		var value domain.EphemeralMessage
		var createdAt string
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Conversation, &value.AuthorID, &value.AppID, &value.RecipientID, &value.Text, &value.Blocks, &value.Attachments, &value.Timestamp, &createdAt); err != nil {
			return nil, err
		}
		value.CreatedAt, err = domain.ParseStoredTime(createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(result)
	return result, nil
}

func scanEphemeralMessage(scanner interface{ Scan(...any) error }) (domain.EphemeralMessage, error) {
	var value domain.EphemeralMessage
	var createdAt string
	if err := scanner.Scan(&value.ID, &value.WorkspaceID, &value.Conversation, &value.AuthorID, &value.AppID, &value.RecipientID, &value.Text, &value.Blocks, &value.Attachments, &value.Timestamp, &createdAt); err != nil {
		return domain.EphemeralMessage{}, translateNotFound(err)
	}
	parsed, err := domain.ParseStoredTime(createdAt)
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	value.CreatedAt = parsed
	return value, nil
}

func (s *Store) GetEphemeralMessage(ctx context.Context, workspaceID domain.WorkspaceID, recipientID domain.UserID, id domain.MessageID) (domain.EphemeralMessage, error) {
	if workspaceID == "" || recipientID == "" || id == "" {
		return domain.EphemeralMessage{}, store.InvalidArgument("invalid ephemeral message key")
	}
	return scanEphemeralMessage(s.db.QueryRowContext(ctx, `SELECT id, workspace_id, conversation_id, author_id, app_id, recipient_id, text, blocks, attachments, timestamp, created_at
		FROM ephemeral_messages WHERE workspace_id = ? AND recipient_id = ? AND id = ?`, workspaceID, recipientID, id))
}

func (s *Store) UpdateEphemeralMessage(ctx context.Context, value domain.EphemeralMessage, event events.Event) error {
	if value.ID == "" || value.WorkspaceID == "" || value.Conversation == "" || value.AuthorID == "" || value.RecipientID == "" || value.CreatedAt.IsZero() || value.Timestamp == "" {
		return store.InvalidArgument("invalid ephemeral message")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE ephemeral_messages SET text = ?, blocks = ?, attachments = ?
		WHERE id = ? AND workspace_id = ? AND recipient_id = ? AND conversation_id = ? AND author_id = ? AND app_id = ? AND timestamp = ? AND created_at = ?`,
		value.Text, value.Blocks, value.Attachments, value.ID, value.WorkspaceID, value.RecipientID, value.Conversation, value.AuthorID, value.AppID, value.Timestamp, domain.NewStoredTime(value.CreatedAt))
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrConflict
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteEphemeralMessage(ctx context.Context, workspaceID domain.WorkspaceID, recipientID domain.UserID, id domain.MessageID, event events.Event) error {
	if workspaceID == "" || recipientID == "" || id == "" {
		return store.InvalidArgument("invalid ephemeral message key")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM ephemeral_messages WHERE workspace_id = ? AND recipient_id = ? AND id = ?`, workspaceID, recipientID, id)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// messageTimestampTaken reports whether some OTHER message in the conversation
// already owns the instant. It runs after the failed insert has been rolled
// back, because a PostgreSQL transaction is unusable once a statement in it has
// failed.
func (s *Store) messageTimestampTaken(ctx context.Context, conversation domain.ConversationID, id domain.MessageID, at domain.StoredTime) bool {
	var taken int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE conversation = ? AND created_at = ? AND id <> ?`, conversation, at, id).Scan(&taken)
	return err == nil
}

// GetMessageByCreatedAt resolves a message by the instant behind its public
// timestamp. The lookup key is truncated with domain.MessageInstant exactly as
// CreateMessage truncates the value it stores: the write invariant was enforced
// and the matching read invariant was not, so the same call answered ErrNotFound
// on the SQL profiles and returned the message on the memory profile whenever the
// caller's instant carried sub-microsecond precision.
func (s *Store) GetMessageByCreatedAt(ctx context.Context, conversation domain.ConversationID, createdAt time.Time) (domain.Message, error) {
	var message domain.Message
	var deleted int
	var stored string
	var blocks, attachments, unfurls string
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, conversation, author_id, app_id, text, blocks, attachments, metadata, stream_state, thread_timestamp, created_at, deleted, unfurls FROM messages WHERE conversation = ? AND created_at = ? ORDER BY id LIMIT 1`, conversation, domain.NewStoredTime(domain.MessageInstant(createdAt))).Scan(&message.ID, &message.WorkspaceID, &message.Conversation, &message.AuthorID, &message.AppID, &message.Text, &blocks, &attachments, &message.Metadata, &message.StreamState, &message.ThreadTimestamp, &stored, &deleted, &unfurls)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Message{}, err
	}
	message.CreatedAt, err = domain.ParseStoredTime(stored)
	if err != nil {
		return domain.Message{}, err
	}
	message.Deleted = deleted != 0
	message.Blocks = blocks
	message.Attachments = attachments
	message.Unfurls, err = decodeUnfurls(unfurls)
	if err != nil {
		return domain.Message{}, err
	}
	values := []domain.Message{message}
	if err := s.hydrateMessageFiles(ctx, values); err != nil {
		return domain.Message{}, err
	}
	message = values[0]
	return message, nil
}

func (s *Store) UpdateMessage(ctx context.Context, message domain.Message, event events.Event) error {
	blocks, err := domain.NormalizeBlocks([]byte(message.Blocks))
	if err != nil {
		return err
	}
	attachments, err := domain.NormalizeAttachments([]byte(message.Attachments))
	if err != nil {
		return err
	}
	if attachments == "" {
		attachments = "[]"
	}
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
	result, err := tx.ExecContext(ctx, `UPDATE messages SET text = ?, text_folded = ?, blocks = ?, attachments = ?, metadata = ?, stream_state = ?, deleted = ?, unfurls = ? WHERE id = ? AND workspace_id = ? AND conversation = ?`, message.Text, domain.FoldSearchText(message.Text), blocks, attachments, message.Metadata, message.StreamState, deleted, unfurls, message.ID, message.WorkspaceID, message.Conversation)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	result, err := tx.ExecContext(ctx, `INSERT INTO reactions(message_id, name, user_id, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(message_id, name, user_id) DO NOTHING`, reaction.Message, reaction.Name, reaction.UserID, domain.NewStoredTime(reaction.CreatedAt))
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
	var message domain.Message
	if err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, conversation, author_id FROM messages WHERE id = ?`, reaction.Message).
		Scan(&message.ID, &message.WorkspaceID, &message.Conversation, &message.AuthorID); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
	if message.AuthorID != reaction.UserID {
		var member int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversation_members WHERE conversation_id = ? AND user_id = ?`, message.Conversation, message.AuthorID).Scan(&member)
		if err == nil {
			id := domain.ActivityIDFor(message.AuthorID, "reaction:"+string(reaction.Message)+":"+reaction.Name+":"+string(reaction.UserID))
			if _, err := tx.ExecContext(ctx, `INSERT INTO activity_items(id, workspace_id, user_id, actor_id, conversation_id, message_id, reaction_name, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
				id, message.WorkspaceID, message.AuthorID, reaction.UserID, message.Conversation, reaction.Message, reaction.Name, reaction.CreatedAt.UTC().UnixNano()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO activity_item_kinds(activity_id, kind) VALUES (?, ?) ON CONFLICT(activity_id, kind) DO NOTHING`, id, domain.ActivityReaction); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	var author domain.UserID
	if err := tx.QueryRowContext(ctx, `SELECT author_id FROM messages WHERE id = ?`, reaction.Message).Scan(&author); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_items WHERE id = ?`, domain.ActivityIDFor(author, "reaction:"+string(reaction.Message)+":"+reaction.Name+":"+string(reaction.UserID))); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListReactions(ctx context.Context, message domain.MessageID, request domain.PageRequest) ([]domain.Reaction, domain.Cursor, bool, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, "", false, err
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
		reaction.CreatedAt, err = domain.ParseStoredTime(created)
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserReactionPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.UserReactionPage{}, err
	}
	query := `SELECT m.conversation, m.id, m.workspace_id, m.author_id, m.app_id, m.text, m.blocks, m.attachments, m.thread_timestamp, m.created_at, m.deleted, r.name, r.user_id, r.created_at FROM reactions r JOIN messages m ON m.id = r.message_id JOIN conversations c ON c.id = m.conversation WHERE m.workspace_id = ? AND r.user_id = ? AND (c.is_private = 0 OR EXISTS (SELECT 1 FROM conversation_members cm WHERE cm.conversation_id = m.conversation AND cm.user_id = ?))`
	args := []any{workspace, user, user}
	if after != "" {
		parts := strings.Split(after, "\x00")
		if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
			return domain.UserReactionPage{}, store.InvalidArgument("invalid user reaction cursor")
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
		if err := rows.Scan(&item.Conversation, &item.Message.ID, &item.Message.WorkspaceID, &item.Message.AuthorID, &item.Message.AppID, &item.Message.Text, &item.Message.Blocks, &item.Message.Attachments, &item.Message.ThreadTimestamp, &messageCreated, &deleted, &item.Reaction.Name, &item.Reaction.UserID, &reactionCreated); err != nil {
			return domain.UserReactionPage{}, err
		}
		item.Message.Deleted = deleted != 0
		item.Message.CreatedAt, err = domain.ParseStoredTime(messageCreated)
		if err != nil {
			return domain.UserReactionPage{}, err
		}
		item.Reaction.Message = item.Message.ID
		item.Reaction.CreatedAt, err = domain.ParseStoredTime(reactionCreated)
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
	messages := make([]domain.Message, len(items))
	for index := range items {
		messages[index] = items[index].Message
	}
	if err := s.hydrateMessageFiles(ctx, messages); err != nil {
		return domain.UserReactionPage{}, err
	}
	for index := range items {
		items[index].Message = messages[index]
	}
	page := domain.UserReactionPage{Items: items, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(userReactionCursorKey(items[len(items)-1]))
	}
	return page, err
}

func userReactionCursorKey(value domain.UserReaction) string {
	return string(domain.NewStoredTime(value.Message.CreatedAt)) + "\x00" + string(value.Message.ID) + "\x00" + value.Reaction.Name + "\x00" + string(value.Reaction.UserID)
}

func (s *Store) AddPin(ctx context.Context, pin domain.Pin, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO pins(message_id, user_id, created_at) VALUES (?, ?, ?) ON CONFLICT(message_id, user_id) DO NOTHING`, pin.Message, pin.UserID, domain.NewStoredTime(pin.CreatedAt))
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListPins(ctx context.Context, conversation domain.ConversationID, request domain.PageRequest) ([]domain.Pin, domain.Cursor, bool, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, "", false, err
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
		pin.CreatedAt, err = domain.ParseStoredTime(created)
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
	result, err := tx.ExecContext(ctx, `INSERT INTO stars(user_id, message_id, created_at) VALUES (?, ?, ?) ON CONFLICT(user_id, message_id) DO NOTHING`, star.UserID, star.Message.ID, domain.NewStoredTime(star.CreatedAt))
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListStars(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) ([]domain.Star, domain.Cursor, bool, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, "", false, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return nil, "", false, err
	}
	query := `SELECT s.created_at, m.id, m.workspace_id, m.conversation, m.author_id, m.app_id, m.text, m.blocks, m.attachments, m.thread_timestamp, m.created_at, m.deleted FROM stars s JOIN messages m ON m.id = s.message_id WHERE s.user_id = ? AND m.workspace_id = ? AND m.deleted = 0`
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
		if err := rows.Scan(&starCreated, &star.Message.ID, &star.Message.WorkspaceID, &star.Message.Conversation, &star.Message.AuthorID, &star.Message.AppID, &star.Message.Text, &star.Message.Blocks, &star.Message.Attachments, &star.Message.ThreadTimestamp, &messageCreated, &deleted); err != nil {
			return nil, "", false, err
		}
		star.UserID = user
		star.Conversation = star.Message.Conversation
		star.Message.Deleted = deleted != 0
		star.CreatedAt, err = domain.ParseStoredTime(starCreated)
		if err != nil {
			return nil, "", false, err
		}
		star.Message.CreatedAt, err = domain.ParseStoredTime(messageCreated)
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
	messages := make([]domain.Message, len(values))
	for index := range values {
		messages[index] = values[index].Message
	}
	if err := s.hydrateMessageFiles(ctx, messages); err != nil {
		return nil, "", false, err
	}
	for index := range values {
		values[index].Message = messages[index]
	}
	var next domain.Cursor
	if hasMore {
		next, err = domain.NewListCursor(string(domain.NewStoredTime(values[len(values)-1].CreatedAt)) + "\x00" + string(values[len(values)-1].Message.ID))
		if err != nil {
			return nil, "", false, err
		}
	}
	return values, next, hasMore, nil
}

const savedItemColumns = `id, workspace_id, user_id, message_id, conversation_id, state, created_at, updated_at`

func scanSavedItem(row rowScanner) (domain.SavedItem, error) {
	var item domain.SavedItem
	var createdAt, updatedAt string
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.UserID, &item.MessageID, &item.Conversation, &item.State, &createdAt, &updatedAt); err != nil {
		return domain.SavedItem{}, err
	}
	var err error
	item.CreatedAt, err = domain.ParseStoredTime(createdAt)
	if err != nil {
		return domain.SavedItem{}, err
	}
	item.UpdatedAt, err = domain.ParseStoredTime(updatedAt)
	if err != nil {
		return domain.SavedItem{}, err
	}
	return item, nil
}

func (s *Store) CreateSavedItem(ctx context.Context, item domain.SavedItem, event events.Event) (domain.SavedItem, bool, error) {
	if !item.State.Valid() {
		return domain.SavedItem{}, false, store.InvalidArgument("saved item state is invalid")
	}
	item.Message = domain.Message{}
	item.SourceAvailable = false
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SavedItem{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO saved_items(id, workspace_id, user_id, message_id, conversation_id, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, message_id) DO NOTHING`,
		item.ID, item.WorkspaceID, item.UserID, item.MessageID, item.Conversation, item.State,
		domain.NewStoredTime(item.CreatedAt), domain.NewStoredTime(item.UpdatedAt))
	if err != nil {
		return domain.SavedItem{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.SavedItem{}, false, err
	}
	if count == 0 {
		existing, err := scanSavedItem(tx.QueryRowContext(ctx, `SELECT `+savedItemColumns+` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND message_id = ?`, item.WorkspaceID, item.UserID, item.MessageID))
		return existing, false, err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.SavedItem{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SavedItem{}, false, err
	}
	return item, true, nil
}

func (s *Store) GetSavedItem(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID) (domain.SavedItem, error) {
	item, err := scanSavedItem(s.db.QueryRowContext(ctx, `SELECT `+savedItemColumns+` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND id = ?`, workspace, user, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SavedItem{}, store.ErrNotFound
	}
	return item, err
}

func (s *Store) GetSavedItemByMessage(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, message domain.MessageID) (domain.SavedItem, error) {
	item, err := scanSavedItem(s.db.QueryRowContext(ctx, `SELECT `+savedItemColumns+` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND message_id = ?`, workspace, user, message))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SavedItem{}, store.ErrNotFound
	}
	return item, err
}

func (s *Store) ListSavedItemsForMessages(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, messages []domain.MessageID) ([]domain.SavedItem, error) {
	if len(messages) == 0 {
		return []domain.SavedItem{}, nil
	}
	query := `SELECT ` + savedItemColumns + ` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND message_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(messages)), ",") + `)`
	args := make([]any, 0, len(messages)+2)
	args = append(args, workspace, user)
	for _, message := range messages {
		args = append(args, message)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.SavedItem, 0, len(messages))
	for rows.Next() {
		item, err := scanSavedItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) ListSavedItems(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, state domain.SavedItemState, request domain.PageRequest) (domain.SavedItemPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.SavedItemPage{}, err
	}
	if !state.Valid() {
		return domain.SavedItemPage{}, store.InvalidArgument("saved item state is invalid")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	query := `SELECT ` + savedItemColumns + ` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND state = ?`
	args := []any{workspace, user, state}
	if after != "" {
		separator := strings.IndexByte(after, 0)
		if separator < 1 || separator == len(after)-1 {
			return domain.SavedItemPage{}, domain.ErrInvalidCursor
		}
		updatedAt, id := after[:separator], after[separator+1:]
		query += ` AND (updated_at > ? OR (updated_at = ? AND id > ?))`
		args = append(args, updatedAt, updatedAt, id)
	}
	query += ` ORDER BY updated_at, id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	defer rows.Close()
	items := make([]domain.SavedItem, 0, request.Limit+1)
	for rows.Next() {
		item, err := scanSavedItem(rows)
		if err != nil {
			return domain.SavedItemPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.SavedItemPage{}, err
	}
	more := len(items) > request.Limit
	if more {
		items = items[:request.Limit]
	}
	var next domain.Cursor
	if more {
		last := items[len(items)-1]
		next, err = domain.NewListCursor(string(domain.NewStoredTime(last.UpdatedAt)) + "\x00" + string(last.ID))
		if err != nil {
			return domain.SavedItemPage{}, err
		}
	}
	return domain.SavedItemPage{Items: items, NextCursor: next, HasMore: more}, nil
}

func (s *Store) UpdateSavedItem(ctx context.Context, item domain.SavedItem, event events.Event) (domain.SavedItem, error) {
	if !item.State.Valid() {
		return domain.SavedItem{}, store.InvalidArgument("saved item state is invalid")
	}
	item.Message = domain.Message{}
	item.SourceAvailable = false
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SavedItem{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE saved_items SET state = ?, updated_at = ? WHERE id = ? AND workspace_id = ? AND user_id = ?`,
		item.State, domain.NewStoredTime(item.UpdatedAt), item.ID, item.WorkspaceID, item.UserID)
	if err != nil {
		return domain.SavedItem{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.SavedItem{}, err
	}
	if count != 1 {
		return domain.SavedItem{}, store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.SavedItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SavedItem{}, err
	}
	return item, nil
}

func (s *Store) DeleteSavedItem(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM saved_items WHERE workspace_id = ? AND user_id = ? AND id = ?`, workspace, user, id)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateBookmark(ctx context.Context, bookmark domain.Bookmark, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookmarks WHERE workspace_id = ? AND conversation_id = ?`, bookmark.WorkspaceID, bookmark.Conversation).Scan(&count); err != nil {
		return err
	}
	if count >= domain.MaxBookmarksPerConversation {
		return store.ErrBookmarkLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bookmarks (id, workspace_id, conversation_id, title, type, link, emoji, entity_id, access_level, parent_id, created_at, updated_at, updated_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, bookmark.ID, bookmark.WorkspaceID, bookmark.Conversation, bookmark.Title, bookmark.Type, bookmark.Link, bookmark.Emoji, bookmark.EntityID, bookmark.AccessLevel, bookmark.ParentID, bookmark.CreatedAt.UTC().Unix(), bookmark.UpdatedAt.UTC().Unix(), bookmark.UpdatedBy)
	if err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetBookmark(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, id domain.BookmarkID) (domain.Bookmark, error) {
	return s.getBookmarkOn(ctx, s.db, workspace, conversation, id)
}

// getBookmarkOn reads through the caller's executor so a mutation can return the
// row it just wrote from inside its own transaction. Committing and then reading
// through s.db returns whatever a concurrent writer left behind: two overlapping
// bookmarks.edit calls each returned the OTHER caller's title as the result of
// their own write.
func (s *Store) getBookmarkOn(ctx context.Context, db queryExecutor, workspace domain.WorkspaceID, conversation domain.ConversationID, id domain.BookmarkID) (domain.Bookmark, error) {
	var bookmark domain.Bookmark
	var created, updated int64
	err := db.QueryRowContext(ctx, `SELECT id, workspace_id, conversation_id, title, type, link, emoji, entity_id, access_level, parent_id, created_at, updated_at, updated_by FROM bookmarks WHERE id = ? AND workspace_id = ? AND conversation_id = ?`, id, workspace, conversation).Scan(&bookmark.ID, &bookmark.WorkspaceID, &bookmark.Conversation, &bookmark.Title, &bookmark.Type, &bookmark.Link, &bookmark.Emoji, &bookmark.EntityID, &bookmark.AccessLevel, &bookmark.ParentID, &created, &updated, &bookmark.UpdatedBy)
	if err := translateNotFound(err); err != nil {
		return domain.Bookmark{}, err
	}
	bookmark.CreatedAt = time.Unix(created, 0).UTC()
	bookmark.UpdatedAt = time.Unix(updated, 0).UTC()
	return bookmark, nil
}

func (s *Store) ListBookmarks(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) ([]domain.Bookmark, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, conversation_id, title, type, link, emoji, entity_id, access_level, parent_id, created_at, updated_at, updated_by FROM bookmarks WHERE workspace_id = ? AND conversation_id = ? ORDER BY created_at, id LIMIT ?`, workspace, conversation, domain.MaxBookmarksPerConversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.Bookmark, 0)
	for rows.Next() {
		var bookmark domain.Bookmark
		var created, updated int64
		if err := rows.Scan(&bookmark.ID, &bookmark.WorkspaceID, &bookmark.Conversation, &bookmark.Title, &bookmark.Type, &bookmark.Link, &bookmark.Emoji, &bookmark.EntityID, &bookmark.AccessLevel, &bookmark.ParentID, &created, &updated, &bookmark.UpdatedBy); err != nil {
			return nil, err
		}
		bookmark.CreatedAt = time.Unix(created, 0).UTC()
		bookmark.UpdatedAt = time.Unix(updated, 0).UTC()
		values = append(values, bookmark)
	}
	return values, rows.Err()
}

func (s *Store) UpdateBookmark(ctx context.Context, bookmark domain.Bookmark, event events.Event) (domain.Bookmark, error) {
	var updated domain.Bookmark
	err := underContention(ctx, func() error {
		value, err := s.updateBookmarkOnce(ctx, bookmark, event)
		updated = value
		return err
	})
	return updated, err
}

func (s *Store) updateBookmarkOnce(ctx context.Context, bookmark domain.Bookmark, event events.Event) (domain.Bookmark, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Bookmark{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE bookmarks SET title = ?, link = ?, emoji = ?, entity_id = ?, access_level = ?, parent_id = ?, updated_at = ?, updated_by = ? WHERE id = ? AND workspace_id = ? AND conversation_id = ?`, bookmark.Title, bookmark.Link, bookmark.Emoji, bookmark.EntityID, bookmark.AccessLevel, bookmark.ParentID, bookmark.UpdatedAt.UTC().Unix(), bookmark.UpdatedBy, bookmark.ID, bookmark.WorkspaceID, bookmark.Conversation)
	if err != nil {
		return domain.Bookmark{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Bookmark{}, err
	}
	if changed != 1 {
		return domain.Bookmark{}, store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.Bookmark{}, err
	}
	updated, err := s.getBookmarkOn(ctx, tx, bookmark.WorkspaceID, bookmark.Conversation, bookmark.ID)
	if err != nil {
		return domain.Bookmark{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Bookmark{}, err
	}
	return updated, nil
}

func (s *Store) DeleteBookmark(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, id domain.BookmarkID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ? AND workspace_id = ? AND conversation_id = ?`, id, workspace, conversation)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateCanvas(ctx context.Context, canvas domain.Canvas, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if canvas.Version == 0 {
		canvas.Version = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO canvases (id, workspace_id, owner_id, title, document_content, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, canvas.ID, canvas.WorkspaceID, canvas.OwnerID, canvas.Title, canvas.DocumentContent, canvas.Version, canvas.CreatedAt.UTC().Unix(), canvas.UpdatedAt.UTC().Unix())
	if err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateCanvasWithAccess(ctx context.Context, canvas domain.Canvas, event events.Event, access domain.CanvasAccess, accessEvent events.Event) error {
	if access.CanvasID != canvas.ID || access.EntityType == "" || access.EntityID == "" || access.Access == "" {
		return store.ErrInvalidArgument
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if canvas.Version == 0 {
		canvas.Version = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canvases (id, workspace_id, owner_id, title, document_content, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, canvas.ID, canvas.WorkspaceID, canvas.OwnerID, canvas.Title, canvas.DocumentContent, canvas.Version, canvas.CreatedAt.UTC().Unix(), canvas.UpdatedAt.UTC().Unix()); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canvas_access(canvas_id, entity_type, entity_id, access_level) VALUES (?, ?, ?, ?)`, access.CanvasID, access.EntityType, access.EntityID, access.Access); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, accessEvent); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateChannelCanvas(ctx context.Context, canvas domain.Canvas, event events.Event, channel domain.ConversationID, accessEvent events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET id = id WHERE id = ? AND workspace_id = ?`, channel, canvas.WorkspaceID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return store.ErrNotFound
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT canvas_id FROM canvas_access WHERE entity_type = 'channel_canvas' AND entity_id = ? LIMIT 1`, channel).Scan(&existing)
	if err == nil {
		return store.ErrAlreadyExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if canvas.Version == 0 {
		canvas.Version = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canvases (id, workspace_id, owner_id, title, document_content, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, canvas.ID, canvas.WorkspaceID, canvas.OwnerID, canvas.Title, canvas.DocumentContent, canvas.Version, canvas.CreatedAt.UTC().Unix(), canvas.UpdatedAt.UTC().Unix()); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canvas_access(canvas_id, entity_type, entity_id, access_level) VALUES (?, 'channel_canvas', ?, ?)`, canvas.ID, channel, store.AccessWrite); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, accessEvent); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetChannelCanvas(ctx context.Context, workspace domain.WorkspaceID, channel domain.ConversationID) (domain.Canvas, error) {
	var canvas domain.Canvas
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT c.id, c.workspace_id, c.owner_id, c.title, c.document_content, c.version, c.created_at, c.updated_at
		FROM canvases c JOIN canvas_access a ON a.canvas_id = c.id
		JOIN conversations ch ON ch.id = a.entity_id
		WHERE a.entity_type = 'channel_canvas' AND a.entity_id = ? AND c.workspace_id = ? AND ch.workspace_id = c.workspace_id
		LIMIT 1`, channel, workspace).Scan(&canvas.ID, &canvas.WorkspaceID, &canvas.OwnerID, &canvas.Title, &canvas.DocumentContent, &canvas.Version, &createdAt, &updatedAt)
	if err != nil {
		return domain.Canvas{}, translateNotFound(err)
	}
	canvas.CreatedAt = time.Unix(createdAt, 0).UTC()
	canvas.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return canvas, nil
}

func (s *Store) GetCanvas(ctx context.Context, workspace domain.WorkspaceID, id domain.CanvasID) (domain.Canvas, error) {
	var canvas domain.Canvas
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, owner_id, title, document_content, version, created_at, updated_at FROM canvases WHERE id = ? AND workspace_id = ?`, id, workspace).Scan(&canvas.ID, &canvas.WorkspaceID, &canvas.OwnerID, &canvas.Title, &canvas.DocumentContent, &canvas.Version, &created, &updated)
	if err := translateNotFound(err); err != nil {
		return domain.Canvas{}, err
	}
	canvas.CreatedAt = time.Unix(created, 0).UTC()
	canvas.UpdatedAt = time.Unix(updated, 0).UTC()
	return canvas, nil
}

func (s *Store) ListCanvases(ctx context.Context, workspace domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.CanvasPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.CanvasPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.CanvasPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.workspace_id, d.owner_id, d.title, d.document_content, d.version, d.created_at, d.updated_at
		FROM canvases d
		WHERE d.workspace_id = ? AND d.id > ?
		  AND EXISTS (SELECT 1 FROM users u WHERE u.id = ? AND u.workspace_id = d.workspace_id AND u.deleted = 0)
		  AND (d.owner_id = ? OR EXISTS (
		    SELECT 1 FROM canvas_access a WHERE a.canvas_id = d.id
		      AND ((a.entity_type = 'user' AND a.entity_id = ?)
		        OR (a.entity_type IN ('channel', 'channel_canvas') AND EXISTS (
		          SELECT 1 FROM conversation_members m JOIN conversations c ON c.id = m.conversation_id
		          WHERE m.conversation_id = a.entity_id AND m.user_id = ? AND c.workspace_id = d.workspace_id)))))
		ORDER BY d.id LIMIT ?`, workspace, after, userID, userID, userID, userID, request.Limit+1)
	if err != nil {
		return domain.CanvasPage{}, err
	}
	defer rows.Close()
	values := make([]domain.Canvas, 0, request.Limit+1)
	for rows.Next() {
		var value domain.Canvas
		var created, updated int64
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.OwnerID, &value.Title, &value.DocumentContent, &value.Version, &created, &updated); err != nil {
			return domain.CanvasPage{}, err
		}
		value.CreatedAt, value.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.CanvasPage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.CanvasPage{Canvases: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func (s *Store) UpdateCanvas(ctx context.Context, canvas domain.Canvas, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE canvases SET title = ?, document_content = ?, version = ?, updated_at = ? WHERE id = ? AND workspace_id = ? AND version = ?`, canvas.Title, canvas.DocumentContent, canvas.Version, canvas.UpdatedAt.UTC().Unix(), canvas.ID, canvas.WorkspaceID, canvas.Version-1)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM canvases WHERE id = ? AND workspace_id = ?)`, canvas.ID, canvas.WorkspaceID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return store.ErrConflict
		}
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteCanvas(ctx context.Context, workspace domain.WorkspaceID, id domain.CanvasID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM canvas_access WHERE canvas_id = ?`, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM canvases WHERE id = ? AND workspace_id = ?`, id, workspace)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetCanvasAccess(ctx context.Context, access domain.CanvasAccess, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO canvas_access (canvas_id, entity_type, entity_id, access_level) VALUES (?, ?, ?, ?) ON CONFLICT(canvas_id, entity_type, entity_id) DO UPDATE SET access_level = excluded.access_level`, access.CanvasID, access.EntityType, access.EntityID, access.Access)
	if err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteCanvasAccess(ctx context.Context, access domain.CanvasAccess, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM canvas_access WHERE canvas_id = ? AND entity_type = ? AND entity_id = ?`, access.CanvasID, access.EntityType, access.EntityID)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// accessScope names the three tables an access resolution needs. Lists and
// canvases model access identically — an owner column on the document and
// (entity_type, entity_id, access_level) grants beside it — so the resolution is
// written once. The fields are only ever set from the two package-level values
// below, so no caller-supplied string reaches the statement.
type accessScope struct {
	documentTable string
	accessTable   string
	keyColumn     string
}

var (
	listAccessScope   = accessScope{documentTable: "lists", accessTable: "list_access", keyColumn: "list_id"}
	canvasAccessScope = accessScope{documentTable: "canvases", accessTable: "canvas_access", keyColumn: "canvas_id"}
)

// resolveAccess reports the highest ranked grant a user holds on one document.
// Ownership counts as an owner grant, a grant recorded for the user counts
// directly, and a grant recorded for a channel counts when the user is a member
// of that channel in the document's workspace. Anything else is
// store.ErrNotFound, including a document that does not exist and a user who is
// not a live member of its workspace, so a caller cannot mistake "no grant" for
// an empty grant.
func (s *Store) resolveAccess(ctx context.Context, scope accessScope, id string, userID domain.UserID) (string, string, string, error) {
	var owner domain.UserID
	if err := s.db.QueryRowContext(ctx, `SELECT d.owner_id FROM `+scope.documentTable+` d WHERE d.id = ? AND EXISTS (SELECT 1 FROM users u WHERE u.id = ? AND u.workspace_id = d.workspace_id AND u.deleted = 0)`, id, userID).Scan(&owner); err != nil {
		return "", "", "", translateNotFound(err)
	}
	bestType, bestID, bestLevel := "", "", ""
	if owner == userID {
		bestType, bestID, bestLevel = "user", string(userID), store.AccessOwner
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.entity_type, a.entity_id, a.access_level FROM `+scope.accessTable+` a JOIN `+scope.documentTable+` d ON d.id = a.`+scope.keyColumn+` WHERE a.`+scope.keyColumn+` = ? AND ((a.entity_type = 'user' AND a.entity_id = ?) OR (a.entity_type IN ('channel', 'channel_canvas') AND EXISTS (SELECT 1 FROM conversation_members m JOIN conversations c ON c.id = m.conversation_id WHERE m.conversation_id = a.entity_id AND m.user_id = ? AND c.workspace_id = d.workspace_id)))`, id, string(userID), userID)
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		var entityType, entityID, level string
		if err := rows.Scan(&entityType, &entityID, &level); err != nil {
			return "", "", "", err
		}
		if store.BetterAccessGrant(entityType, entityID, level, bestType, bestID, bestLevel) {
			bestType, bestID, bestLevel = entityType, entityID, level
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", "", err
	}
	if bestLevel == "" {
		return "", "", "", store.ErrNotFound
	}
	return bestType, bestID, bestLevel, nil
}

// GetListAccess resolves the effective access one user has to one list. The
// grants written by SetListAccess had no reader, so nothing could enforce them
// and every workspace member could read and delete every other member's list.
func (s *Store) GetListAccess(ctx context.Context, listID domain.ListID, userID domain.UserID) (domain.ListAccess, error) {
	entityType, entityID, level, err := s.resolveAccess(ctx, listAccessScope, string(listID), userID)
	if err != nil {
		return domain.ListAccess{}, err
	}
	return domain.ListAccess{ListID: listID, EntityType: entityType, EntityID: entityID, Access: level}, nil
}

// GetCanvasAccess resolves the effective access one user has to one canvas, by
// the same rules as GetListAccess.
func (s *Store) GetCanvasAccess(ctx context.Context, canvasID domain.CanvasID, userID domain.UserID) (domain.CanvasAccess, error) {
	entityType, entityID, level, err := s.resolveAccess(ctx, canvasAccessScope, string(canvasID), userID)
	if err != nil {
		return domain.CanvasAccess{}, err
	}
	return domain.CanvasAccess{CanvasID: canvasID, EntityType: entityType, EntityID: entityID, Access: level}, nil
}

func (s *Store) CreateReminder(ctx context.Context, reminder domain.Reminder, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO reminders(id, workspace_id, creator_id, user_id, text, due_at, complete_at, recurring) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, reminder.ID, reminder.WorkspaceID, reminder.Creator, reminder.User, reminder.Text, reminder.Time.Unix(), unixSeconds(reminder.CompleteAt), boolInt(reminder.Recurring)); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ReminderPage{}, err
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

const laterReminderColumns = `id, workspace_id, creator_id, user_id, channel_id, source_message_id, source_conversation_id, source_timestamp, target, text, due_at, timezone, recurrence, created_at, updated_at, completed_at, last_delivered_at, acknowledged_at, failed_at, failure_code`

func scanLaterReminder(scanner interface{ Scan(...any) error }) (domain.LaterReminder, error) {
	var value domain.LaterReminder
	var due, created, updated, completed, delivered, acknowledged, failed int64
	if err := scanner.Scan(
		&value.ID, &value.WorkspaceID, &value.Creator, &value.UserID, &value.Channel,
		&value.SourceMessageID, &value.SourceConversation, &value.SourceTimestamp, &value.Target, &value.Text,
		&due, &value.TimeZone, &value.Recurrence, &created, &updated, &completed,
		&delivered, &acknowledged, &failed, &value.FailureCode,
	); err != nil {
		return domain.LaterReminder{}, err
	}
	if !value.Target.Valid() || !value.Recurrence.Valid() {
		return domain.LaterReminder{}, errors.New("stored Later reminder has invalid target or recurrence")
	}
	value.DueAt = time.Unix(due, 0).UTC()
	value.CreatedAt = time.Unix(created, 0).UTC()
	value.UpdatedAt = time.Unix(updated, 0).UTC()
	if completed != 0 {
		value.CompletedAt = time.Unix(completed, 0).UTC()
	}
	if delivered != 0 {
		value.LastDeliveredAt = time.Unix(delivered, 0).UTC()
	}
	if acknowledged != 0 {
		value.AcknowledgedAt = time.Unix(acknowledged, 0).UTC()
	}
	if failed != 0 {
		value.FailedAt = time.Unix(failed, 0).UTC()
	}
	return value, nil
}

func (s *Store) CreateLaterReminder(ctx context.Context, reminder domain.LaterReminder, event events.Event) error {
	if !reminder.Target.Valid() || !reminder.Recurrence.Valid() {
		return store.InvalidArgument("later reminder target or recurrence is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO later_reminders(
		id, workspace_id, creator_id, user_id, channel_id, source_message_id, source_conversation_id, source_timestamp,
		target, text, due_at, timezone, recurrence, created_at, updated_at,
		completed_at, last_delivered_at, acknowledged_at, failed_at, failure_code
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reminder.ID, reminder.WorkspaceID, reminder.Creator, reminder.UserID, reminder.Channel,
		reminder.SourceMessageID, reminder.SourceConversation, reminder.SourceTimestamp, reminder.Target, reminder.Text,
		reminder.DueAt.Unix(), reminder.TimeZone, reminder.Recurrence, reminder.CreatedAt.Unix(),
		reminder.UpdatedAt.Unix(), unixSeconds(reminder.CompletedAt), unixSeconds(reminder.LastDeliveredAt), unixSeconds(reminder.AcknowledgedAt),
		unixSeconds(reminder.FailedAt), reminder.FailureCode,
	); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetLaterReminder(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.LaterReminderID) (domain.LaterReminder, error) {
	value, err := scanLaterReminder(s.db.QueryRowContext(ctx, `SELECT `+laterReminderColumns+`
		FROM later_reminders
		WHERE id = ? AND workspace_id = ?
		  AND ((target = ? AND user_id = ?) OR (target = ? AND creator_id = ?))`,
		id, workspace, domain.LaterReminderPersonal, user, domain.LaterReminderChannel, user,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LaterReminder{}, store.ErrNotFound
	}
	return value, err
}

func (s *Store) ListLaterReminders(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, target domain.LaterReminderTarget, request domain.PageRequest) (domain.LaterReminderPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.LaterReminderPage{}, err
	}
	if !target.Valid() {
		return domain.LaterReminderPage{}, store.InvalidArgument("later reminder target is invalid")
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.LaterReminderPage{}, err
	}
	ownerColumn := "user_id"
	if target == domain.LaterReminderChannel {
		ownerColumn = "creator_id"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+laterReminderColumns+`
		FROM later_reminders
		WHERE workspace_id = ? AND target = ? AND `+ownerColumn+` = ? AND id > ?
		ORDER BY id LIMIT ?`, workspace, target, user, after, request.Limit+1)
	if err != nil {
		return domain.LaterReminderPage{}, err
	}
	defer rows.Close()
	items := make([]domain.LaterReminder, 0, request.Limit+1)
	for rows.Next() {
		value, scanErr := scanLaterReminder(rows)
		if scanErr != nil {
			return domain.LaterReminderPage{}, scanErr
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return domain.LaterReminderPage{}, err
	}
	page := domain.LaterReminderPage{Items: items, HasMore: len(items) > request.Limit}
	if page.HasMore {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(page.Items[len(page.Items)-1].ID))
	}
	return page, err
}

func (s *Store) UpdateLaterReminder(ctx context.Context, reminder domain.LaterReminder, event events.Event) (domain.LaterReminder, error) {
	if reminder.Target != domain.LaterReminderPersonal || !reminder.Recurrence.Valid() {
		return domain.LaterReminder{}, store.InvalidArgument("only personal Later reminders can be edited")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LaterReminder{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE later_reminders
		SET text = ?, due_at = ?, timezone = ?, recurrence = ?, updated_at = ?,
		    last_delivered_at = 0, acknowledged_at = 0, failed_at = 0, failure_code = '', lease_owner = '', lease_until = 0, next_attempt_at = 0
		WHERE id = ? AND workspace_id = ? AND target = ? AND user_id = ? AND (lease_until = 0 OR lease_until <= ?)`,
		reminder.Text, reminder.DueAt.Unix(), reminder.TimeZone, reminder.Recurrence,
		reminder.UpdatedAt.Unix(), reminder.ID, reminder.WorkspaceID, domain.LaterReminderPersonal, reminder.Creator, s.now().Unix(),
	)
	if err != nil {
		return domain.LaterReminder{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.LaterReminder{}, err
	}
	if count != 1 {
		return domain.LaterReminder{}, store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.LaterReminder{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.LaterReminder{}, err
	}
	return s.GetLaterReminder(ctx, reminder.WorkspaceID, reminder.Creator, reminder.ID)
}

func (s *Store) AcknowledgeLaterReminders(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, acknowledged time.Time, event events.Event) error {
	if workspace == "" || user == "" || acknowledged.IsZero() {
		return store.InvalidArgument("Later reminder acknowledgement is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE later_reminders
		SET acknowledged_at = last_delivered_at, updated_at = ?
		WHERE workspace_id = ? AND target = ? AND user_id = ?
		  AND last_delivered_at > acknowledged_at`,
		acknowledged.UTC().Unix(), workspace, domain.LaterReminderPersonal, user)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE activity_items SET read_at = ? WHERE workspace_id = ? AND user_id = ? AND reminder_id <> '' AND read_at = 0`,
			acknowledged.UTC().UnixNano(), workspace, user); err != nil {
			return err
		}
		if err := insertOutbox(ctx, tx, event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CompleteLaterReminder(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.LaterReminderID, completed time.Time, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE later_reminders
		SET completed_at = CASE WHEN completed_at = 0 THEN ? ELSE completed_at END,
		    updated_at = CASE WHEN completed_at = 0 THEN ? ELSE updated_at END
		WHERE id = ? AND workspace_id = ? AND target = ? AND user_id = ? AND (lease_until = 0 OR lease_until <= ?)`,
		completed.Unix(), completed.Unix(), id, workspace, domain.LaterReminderPersonal, user, s.now().Unix(),
	)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteLaterReminder(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.LaterReminderID, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	result, err := tx.ExecContext(ctx, `DELETE FROM later_reminders
		WHERE id = ? AND workspace_id = ?
		  AND ((target = ? AND user_id = ?) OR (target = ? AND creator_id = ?))
		  AND (lease_until = 0 OR lease_until <= ?)`,
		id, workspace, domain.LaterReminderPersonal, user, domain.LaterReminderChannel, user, now,
	)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_items WHERE workspace_id = ? AND user_id = ? AND reminder_id = ?`, workspace, user, id); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EarliestLaterReminder(ctx context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	var dueAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(CASE WHEN next_attempt_at > due_at THEN next_attempt_at ELSE due_at END)
		FROM later_reminders
		WHERE (? = '' OR workspace_id = ?) AND completed_at = 0 AND failed_at = 0`, workspace, workspace).Scan(&dueAt); err != nil {
		return time.Time{}, err
	}
	if !dueAt.Valid || dueAt.Int64 == 0 {
		return time.Time{}, nil
	}
	return time.Unix(dueAt.Int64, 0).UTC(), nil
}

func (s *Store) ClaimDueLaterReminders(ctx context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration, now time.Time) ([]domain.LaterReminder, error) {
	if owner == "" || limit <= 0 || lease <= 0 || now.IsZero() {
		return nil, store.InvalidArgument("Later reminder claim requires owner, positive limit, lease, and current time")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+laterReminderColumns+`
		FROM later_reminders
		WHERE (? = '' OR workspace_id = ?) AND completed_at = 0 AND failed_at = 0
		  AND due_at <= ? AND (lease_until = 0 OR lease_until <= ?)
		  AND (next_attempt_at = 0 OR next_attempt_at <= ?)
		ORDER BY due_at, id LIMIT ?`, workspace, workspace, now.Unix(), now.Unix(), now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	values := make([]domain.LaterReminder, 0, limit)
	for rows.Next() {
		value, scanErr := scanLaterReminder(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	expires := scheduledUnixSecondCeil(now.Add(lease))
	for _, reminder := range values {
		result, updateErr := tx.ExecContext(ctx, `UPDATE later_reminders
			SET lease_owner = ?, lease_until = ?
			WHERE id = ? AND completed_at = 0 AND failed_at = 0 AND (lease_until = 0 OR lease_until <= ?)`,
			owner, expires, reminder.ID, now.Unix())
		if updateErr != nil {
			return nil, updateErr
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, rowsErr
		}
		if changed != 1 {
			return nil, store.ErrLeaseConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

func (s *Store) RenewLaterReminder(ctx context.Context, owner string, id domain.LaterReminderID, lease time.Duration, now time.Time) error {
	if owner == "" || lease <= 0 || now.IsZero() {
		return store.InvalidArgument("Later reminder renewal requires owner, lease, and current time")
	}
	now = now.UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE later_reminders SET lease_until = ?
		WHERE id = ? AND lease_owner = ? AND completed_at = 0 AND failed_at = 0 AND lease_until > ?`,
		scheduledUnixSecondCeil(now.Add(lease)), id, owner, now.Unix())
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

func (s *Store) MarkLaterReminderDelivered(ctx context.Context, owner string, id domain.LaterReminderID, deliveredAt, nextDue time.Time, event events.Event) error {
	if owner == "" || deliveredAt.IsZero() {
		return store.InvalidArgument("Later reminder delivery requires owner and delivery time")
	}
	deliveredAt = deliveredAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var result sql.Result
	if nextDue.IsZero() {
		result, err = tx.ExecContext(ctx, `UPDATE later_reminders
			SET completed_at = ?, last_delivered_at = ?, updated_at = ?, lease_owner = '', lease_until = 0, next_attempt_at = 0
			WHERE id = ? AND lease_owner = ? AND recurrence = '' AND completed_at = 0 AND failed_at = 0 AND lease_until > ?`,
			deliveredAt.Unix(), deliveredAt.Unix(), deliveredAt.Unix(), id, owner, deliveredAt.Unix())
	} else {
		nextDue = nextDue.UTC()
		result, err = tx.ExecContext(ctx, `UPDATE later_reminders
			SET due_at = ?, last_delivered_at = ?, updated_at = ?, lease_owner = '', lease_until = 0, next_attempt_at = 0
			WHERE id = ? AND lease_owner = ? AND recurrence <> '' AND due_at < ? AND completed_at = 0 AND failed_at = 0 AND lease_until > ?`,
			nextDue.Unix(), deliveredAt.Unix(), deliveredAt.Unix(), id, owner, nextDue.Unix(), deliveredAt.Unix())
	}
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
	var reminder domain.LaterReminder
	var target domain.LaterReminderTarget
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id, user_id, source_message_id, source_conversation_id, target FROM later_reminders WHERE id = ?`, id).
		Scan(&reminder.WorkspaceID, &reminder.UserID, &reminder.SourceMessageID, &reminder.SourceConversation, &target); err != nil {
		return err
	}
	activityReminders := 1
	if err := tx.QueryRowContext(ctx, `SELECT activity_reminders FROM notification_preferences WHERE workspace_id = ? AND user_id = ?`, reminder.WorkspaceID, reminder.UserID).Scan(&activityReminders); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if target == domain.LaterReminderPersonal && reminder.UserID != "" && activityReminders != 0 {
		activityID := domain.ActivityIDFor(reminder.UserID, "reminder:"+string(id)+":"+string(domain.NewStoredTime(deliveredAt)))
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity_items(id, workspace_id, user_id, conversation_id, message_id, reminder_id, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			activityID, reminder.WorkspaceID, reminder.UserID, reminder.SourceConversation, reminder.SourceMessageID, id, deliveredAt.UnixNano()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity_item_kinds(activity_id, kind) VALUES (?, ?) ON CONFLICT(activity_id, kind) DO NOTHING`, activityID, domain.ActivityReminder); err != nil {
			return err
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkLaterReminderFailed(ctx context.Context, owner string, id domain.LaterReminderID, failureCode string, failedAt time.Time, event events.Event) error {
	if owner == "" || failureCode == "" || failedAt.IsZero() {
		return store.InvalidArgument("Later reminder failure requires owner, code, and failure time")
	}
	failedAt = failedAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE later_reminders
		SET failed_at = ?, failure_code = ?, updated_at = ?, lease_owner = '', lease_until = 0, next_attempt_at = 0
		WHERE id = ? AND lease_owner = ? AND completed_at = 0 AND failed_at = 0 AND lease_until > ?`,
		failedAt.Unix(), failureCode, failedAt.Unix(), id, owner, failedAt.Unix())
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReleaseLaterReminder(ctx context.Context, owner string, id domain.LaterReminderID, next, now time.Time) error {
	if owner == "" || next.IsZero() || now.IsZero() {
		return store.InvalidArgument("Later reminder release requires owner, retry time, and current time")
	}
	now = now.UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE later_reminders
		SET lease_owner = '', lease_until = 0, next_attempt_at = ?
		WHERE id = ? AND lease_owner = ? AND completed_at = 0 AND failed_at = 0 AND lease_until > ?`,
		scheduledUnixSecondCeil(next), id, owner, now.Unix())
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

func (s *Store) CreateScheduledMessage(ctx context.Context, value domain.ScheduledMessage, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_messages(id, workspace_id, channel_id, author_id, app_id, bot_id, credential_hash, text, blocks, attachments, metadata, stream_state, thread_ts, post_at, created_at, delivered, delivered_at, failed_at, failure_code, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, '', '', 0, 0)`, value.ID, value.WorkspaceID, value.Channel, value.Author, value.AppID, value.BotID, value.CredentialHash, value.Text, value.Blocks, value.Attachments, value.Metadata, value.StreamState, value.ThreadTimestamp, value.PostAt.Unix(), value.CreatedAt.Unix()); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateScheduledMessageWithinLimit(ctx context.Context, value domain.ScheduledMessage, window time.Duration, limit int, event events.Event) error {
	if window <= 0 || limit <= 0 {
		return store.InvalidArgument("scheduled-message window and limit must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A no-op update takes the conversation row lock on PostgreSQL and the
	// database write lock on SQLite before the count. Concurrent schedulers for
	// one channel therefore cannot both observe slot 30 and commit slot 31.
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET id = id WHERE id = ? AND workspace_id = ?`, value.Channel, value.WorkspaceID)
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
	rows, err := tx.QueryContext(ctx, `SELECT post_at FROM scheduled_messages WHERE workspace_id = ? AND channel_id = ? AND delivered = 0 AND failed_at = 0 AND post_at >= ? AND post_at <= ? ORDER BY post_at`, value.WorkspaceID, value.Channel, value.PostAt.Add(-window).Unix(), value.PostAt.Add(window).Unix())
	if err != nil {
		return err
	}
	var nearby []time.Time
	for rows.Next() {
		var postAt int64
		if err := rows.Scan(&postAt); err != nil {
			rows.Close()
			return err
		}
		nearby = append(nearby, time.Unix(postAt, 0).UTC())
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if store.ScheduledMessageLimitExceeded(nearby, value.PostAt, window, limit) {
		return store.ErrScheduledMessageLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_messages(id, workspace_id, channel_id, author_id, app_id, bot_id, credential_hash, text, blocks, attachments, metadata, stream_state, thread_ts, post_at, created_at, delivered, delivered_at, failed_at, failure_code, lease_owner, lease_until, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, '', '', 0, 0)`, value.ID, value.WorkspaceID, value.Channel, value.Author, value.AppID, value.BotID, value.CredentialHash, value.Text, value.Blocks, value.Attachments, value.Metadata, value.StreamState, value.ThreadTimestamp, value.PostAt.Unix(), value.CreatedAt.Unix()); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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

type rowScanner interface {
	Scan(...any) error
}

func scanScheduledMessage(row rowScanner) (domain.ScheduledMessage, error) {
	var value domain.ScheduledMessage
	var postAt, createdAt, deliveredAt, failedAt int64
	err := row.Scan(
		&value.ID, &value.WorkspaceID, &value.Channel, &value.Author, &value.AppID, &value.BotID,
		&value.CredentialHash, &value.Text, &value.Blocks, &value.Attachments, &value.Metadata, &value.StreamState, &value.ThreadTimestamp,
		&postAt, &createdAt, &deliveredAt, &failedAt, &value.FailureCode,
	)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	value.PostAt = time.Unix(postAt, 0).UTC()
	value.CreatedAt = time.Unix(createdAt, 0).UTC()
	if deliveredAt != 0 {
		value.DeliveredAt = time.Unix(deliveredAt, 0).UTC()
	}
	if failedAt != 0 {
		value.FailedAt = time.Unix(failedAt, 0).UTC()
	}
	return value, nil
}

const scheduledMessageColumns = `id, workspace_id, channel_id, author_id, app_id, bot_id, credential_hash, text, blocks, attachments, metadata, stream_state, thread_ts, post_at, created_at, delivered_at, failed_at, failure_code`

func (s *Store) ListScheduledMessages(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, channel domain.ConversationID, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	query := `SELECT ` + scheduledMessageColumns + ` FROM scheduled_messages WHERE workspace_id = ? AND author_id = ? AND delivered = 0 AND failed_at = 0`
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
		value, err := scanScheduledMessage(rows)
		if err != nil {
			return domain.ScheduledMessagePage{}, err
		}
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

func (s *Store) ListScheduledMessagesForCredential(ctx context.Context, workspace domain.WorkspaceID, filter domain.ScheduledMessageQuery) (domain.ScheduledMessagePage, error) {
	if filter.CredentialHash == "" {
		return domain.ScheduledMessagePage{}, store.InvalidArgument("scheduled-message credential is required")
	}
	if err := store.CheckAscendingPage(filter.Page); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	afterTime, afterID, err := store.ParseScheduledMessageCursor(filter.Page.Cursor)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	query := `SELECT ` + scheduledMessageColumns + ` FROM scheduled_messages WHERE workspace_id = ? AND credential_hash = ? AND delivered = 0 AND failed_at = 0`
	args := []any{workspace, filter.CredentialHash}
	if filter.Channel != "" {
		query += ` AND channel_id = ?`
		args = append(args, filter.Channel)
	}
	if !filter.Oldest.IsZero() {
		query += ` AND post_at >= ?`
		args = append(args, filter.Oldest.UTC().Unix())
	}
	if !filter.Latest.IsZero() {
		query += ` AND post_at <= ?`
		args = append(args, filter.Latest.UTC().Unix())
	}
	if !afterTime.IsZero() {
		query += ` AND (post_at > ? OR (post_at = ? AND id > ?))`
		args = append(args, afterTime.Unix(), afterTime.Unix(), afterID)
	}
	query += ` ORDER BY post_at, id LIMIT ?`
	args = append(args, filter.Page.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	defer rows.Close()
	items := make([]domain.ScheduledMessage, 0, filter.Page.Limit+1)
	for rows.Next() {
		value, scanErr := scanScheduledMessage(rows)
		if scanErr != nil {
			return domain.ScheduledMessagePage{}, scanErr
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	page := domain.ScheduledMessagePage{Items: items, HasMore: len(items) > filter.Page.Limit}
	if page.HasMore {
		page.Items = page.Items[:filter.Page.Limit]
		page.NextCursor, err = domain.NewListCursor(store.ScheduledMessageCursorKey(page.Items[len(page.Items)-1]))
	}
	return page, err
}

func (s *Store) ListScheduledMessageHistory(ctx context.Context, workspace domain.WorkspaceID, credentialHash string, includeDelivered bool, request domain.PageRequest) (domain.ScheduledMessagePage, error) {
	if credentialHash == "" {
		return domain.ScheduledMessagePage{}, store.InvalidArgument("scheduled-message credential is required")
	}
	if err := store.CheckPage(request); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	query := `SELECT ` + scheduledMessageColumns + ` FROM scheduled_messages WHERE workspace_id = ? AND credential_hash = ?`
	args := []any{workspace, credentialHash}
	if !includeDelivered {
		query += ` AND delivered = 0`
	}
	if request.Cursor != "" {
		afterTime, afterID, err := store.ParseScheduledMessageCursor(request.Cursor)
		if err != nil {
			return domain.ScheduledMessagePage{}, err
		}
		if request.Descending {
			query += ` AND (post_at < ? OR (post_at = ? AND id < ?))`
		} else {
			query += ` AND (post_at > ? OR (post_at = ? AND id > ?))`
		}
		args = append(args, afterTime.Unix(), afterTime.Unix(), afterID)
	}
	if request.Descending {
		query += ` ORDER BY post_at DESC, id DESC LIMIT ?`
	} else {
		query += ` ORDER BY post_at, id LIMIT ?`
	}
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	defer rows.Close()
	items := make([]domain.ScheduledMessage, 0, request.Limit+1)
	for rows.Next() {
		value, scanErr := scanScheduledMessage(rows)
		if scanErr != nil {
			return domain.ScheduledMessagePage{}, scanErr
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return domain.ScheduledMessagePage{}, err
	}
	page := domain.ScheduledMessagePage{Items: items, HasMore: len(items) > request.Limit}
	if page.HasMore {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(store.ScheduledMessageCursorKey(page.Items[len(page.Items)-1]))
	}
	return page, err
}

func (s *Store) EarliestScheduledMessage(ctx context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	var postAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(CASE WHEN next_attempt_at > post_at THEN next_attempt_at ELSE post_at END) FROM scheduled_messages WHERE (? = '' OR workspace_id = ?) AND delivered = 0 AND failed_at = 0`, workspace, workspace).Scan(&postAt); err != nil {
		return time.Time{}, err
	}
	if !postAt.Valid || postAt.Int64 == 0 {
		return time.Time{}, nil
	}
	return time.Unix(postAt.Int64, 0).UTC(), nil
}

func (s *Store) UpdateScheduledMessageWithinLimit(ctx context.Context, update domain.ScheduledMessageUpdate, window time.Duration, limit int, event events.Event) (domain.ScheduledMessage, error) {
	if update.WorkspaceID == "" || update.ID == "" || update.Channel == "" || update.CredentialHash == "" || update.Text == "" || update.PostAt.IsZero() || window <= 0 || limit <= 0 {
		return domain.ScheduledMessage{}, store.InvalidArgument("scheduled-message update is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET id = id WHERE id = ? AND workspace_id = ?`, update.Channel, update.WorkspaceID)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if changed != 1 {
		return domain.ScheduledMessage{}, store.ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT post_at FROM scheduled_messages WHERE workspace_id = ? AND channel_id = ? AND id <> ? AND delivered = 0 AND failed_at = 0 AND post_at >= ? AND post_at <= ? ORDER BY post_at`, update.WorkspaceID, update.Channel, update.ID, update.PostAt.Add(-window).Unix(), update.PostAt.Add(window).Unix())
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	nearby := make([]time.Time, 0, limit)
	for rows.Next() {
		var postAt int64
		if err := rows.Scan(&postAt); err != nil {
			rows.Close()
			return domain.ScheduledMessage{}, err
		}
		nearby = append(nearby, time.Unix(postAt, 0).UTC())
	}
	if err := rows.Close(); err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := rows.Err(); err != nil {
		return domain.ScheduledMessage{}, err
	}
	if store.ScheduledMessageLimitExceeded(nearby, update.PostAt, window, limit) {
		return domain.ScheduledMessage{}, store.ErrScheduledMessageLimit
	}
	now := time.Now().UTC().Unix()
	result, err = tx.ExecContext(ctx, `UPDATE scheduled_messages SET text = ?, post_at = ?, failed_at = 0, failure_code = '', next_attempt_at = 0 WHERE id = ? AND workspace_id = ? AND channel_id = ? AND credential_hash = ? AND delivered = 0 AND (lease_until = 0 OR lease_until <= ?)`, update.Text, update.PostAt.UTC().Unix(), update.ID, update.WorkspaceID, update.Channel, update.CredentialHash, now)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if changed != 1 {
		return domain.ScheduledMessage{}, store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.ScheduledMessage{}, err
	}
	value, err := scanScheduledMessage(tx.QueryRowContext(ctx, `SELECT `+scheduledMessageColumns+` FROM scheduled_messages WHERE id = ?`, update.ID))
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledMessage{}, err
	}
	return value, nil
}

func (s *Store) ClaimScheduledMessageForCredential(ctx context.Context, workspace domain.WorkspaceID, credentialHash string, id domain.ScheduledMessageID, owner string, lease time.Duration) (domain.ScheduledMessage, error) {
	if workspace == "" || credentialHash == "" || id == "" || owner == "" || lease <= 0 {
		return domain.ScheduledMessage{}, store.InvalidArgument("scheduled-message claim is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_messages SET lease_owner = ?, lease_until = ? WHERE id = ? AND workspace_id = ? AND credential_hash = ? AND delivered = 0 AND (lease_until = 0 OR lease_until <= ?)`, owner, scheduledUnixSecondCeil(now.Add(lease)), id, workspace, credentialHash, now.Unix())
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if changed != 1 {
		return domain.ScheduledMessage{}, store.ErrNotFound
	}
	value, err := scanScheduledMessage(tx.QueryRowContext(ctx, `SELECT `+scheduledMessageColumns+` FROM scheduled_messages WHERE id = ?`, id))
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ScheduledMessage{}, err
	}
	return value, nil
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteScheduledMessageForCredential(ctx context.Context, workspace domain.WorkspaceID, credentialHash string, channel domain.ConversationID, id domain.ScheduledMessageID, event events.Event) error {
	if credentialHash == "" {
		return store.InvalidArgument("scheduled-message credential is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `DELETE FROM scheduled_messages WHERE id = ? AND workspace_id = ? AND credential_hash = ? AND channel_id = ? AND delivered = 0 AND (lease_until = 0 OR lease_until <= ?)`, id, workspace, credentialHash, channel, now.Unix())
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

const draftColumns = `workspace_id, user_id, conversation_id, thread_ts, text, updated_at`

func scanDraft(row rowScanner) (domain.Draft, error) {
	var value domain.Draft
	var updatedAt string
	if err := row.Scan(&value.WorkspaceID, &value.UserID, &value.ConversationID, &value.ThreadTimestamp, &value.Text, &updatedAt); err != nil {
		return domain.Draft{}, err
	}
	var err error
	value.UpdatedAt, err = domain.ParseStoredTime(updatedAt)
	return value, err
}

func draftCursorKey(value domain.Draft) string {
	return string(domain.NewStoredTime(value.UpdatedAt)) + "\x00" + string(value.ConversationID) + "\x00" + string(value.ThreadTimestamp)
}

func parseDraftCursor(cursor domain.Cursor) (string, domain.ConversationID, domain.MessageTimestamp, error) {
	decoded, err := domain.DecodeListCursor(cursor)
	if err != nil || decoded == "" {
		return decoded, "", "", err
	}
	parts := strings.Split(decoded, "\x00")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", "", domain.ErrInvalidCursor
	}
	if _, err := domain.ParseStoredTime(parts[0]); err != nil {
		return "", "", "", domain.ErrInvalidCursor
	}
	return parts[0], domain.ConversationID(parts[1]), domain.MessageTimestamp(parts[2]), nil
}

func (s *Store) UpsertDraft(ctx context.Context, value domain.Draft, event events.Event) (domain.Draft, error) {
	if value.WorkspaceID == "" || value.UserID == "" || value.ConversationID == "" || strings.TrimSpace(value.Text) == "" || value.UpdatedAt.IsZero() {
		return domain.Draft{}, store.InvalidArgument("draft is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Draft{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO drafts(workspace_id, user_id, conversation_id, thread_ts, text, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, user_id, conversation_id, thread_ts) DO UPDATE SET text = excluded.text, updated_at = excluded.updated_at`,
		value.WorkspaceID, value.UserID, value.ConversationID, value.ThreadTimestamp, value.Text, domain.NewStoredTime(value.UpdatedAt))
	if err != nil {
		return domain.Draft{}, classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.Draft{}, err
	}
	if changed != 1 {
		return domain.Draft{}, store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.Draft{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Draft{}, err
	}
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value, nil
}

func (s *Store) GetDraft(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.Draft, error) {
	value, err := scanDraft(s.db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE workspace_id = ? AND user_id = ? AND conversation_id = ? AND thread_ts = ?`, workspace, user, conversation, thread))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Draft{}, store.ErrNotFound
	}
	return value, err
}

func (s *Store) ListDrafts(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.DraftPage, error) {
	if err := store.CheckPage(request); err != nil {
		return domain.DraftPage{}, err
	}
	updatedAt, conversation, thread, err := parseDraftCursor(request.Cursor)
	if err != nil {
		return domain.DraftPage{}, err
	}
	query := `SELECT ` + draftColumns + ` FROM drafts WHERE workspace_id = ? AND user_id = ?`
	args := []any{workspace, user}
	if updatedAt != "" {
		if request.Descending {
			query += ` AND (updated_at < ? OR (updated_at = ? AND (conversation_id < ? OR (conversation_id = ? AND thread_ts < ?))))`
		} else {
			query += ` AND (updated_at > ? OR (updated_at = ? AND (conversation_id > ? OR (conversation_id = ? AND thread_ts > ?))))`
		}
		args = append(args, updatedAt, updatedAt, conversation, conversation, thread)
	}
	if request.Descending {
		query += ` ORDER BY updated_at DESC, conversation_id DESC, thread_ts DESC LIMIT ?`
	} else {
		query += ` ORDER BY updated_at, conversation_id, thread_ts LIMIT ?`
	}
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.DraftPage{}, err
	}
	defer rows.Close()
	items := make([]domain.Draft, 0, request.Limit+1)
	for rows.Next() {
		item, err := scanDraft(rows)
		if err != nil {
			return domain.DraftPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.DraftPage{}, err
	}
	page := domain.DraftPage{Items: items, HasMore: len(items) > request.Limit}
	if page.HasMore {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(draftCursorKey(page.Items[len(page.Items)-1]))
	}
	return page, err
}

func (s *Store) DeleteDraft(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM drafts WHERE workspace_id = ? AND user_id = ? AND conversation_id = ? AND thread_ts = ?`, workspace, user, conversation, thread)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimScheduledMessages(ctx context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration) ([]domain.ScheduledMessage, error) {
	if owner == "" || limit <= 0 || lease <= 0 {
		return nil, store.InvalidArgument("scheduled claim requires owner, positive limit, and lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	rows, err := tx.QueryContext(ctx, `SELECT `+scheduledMessageColumns+` FROM scheduled_messages WHERE (? = '' OR workspace_id = ?) AND delivered = 0 AND failed_at = 0 AND post_at <= ? AND (lease_until = 0 OR lease_until <= ?) AND (next_attempt_at = 0 OR next_attempt_at <= ?) ORDER BY post_at, id LIMIT ?`, workspace, workspace, now.Unix(), now.Unix(), now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.ScheduledMessage, 0, limit)
	for rows.Next() {
		value, scanErr := scanScheduledMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
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
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE scheduled_messages SET delivered = 1, delivered_at = ?, failed_at = 0, failure_code = '', lease_owner = '', lease_until = 0, next_attempt_at = 0 WHERE id = ? AND lease_owner = ? AND delivered = 0 AND lease_until > ?`, now.Unix(), id, owner, now.Unix())
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

func (s *Store) MarkScheduledMessageFailed(ctx context.Context, owner string, id domain.ScheduledMessageID, failureCode string, failedAt time.Time, event events.Event) error {
	if failureCode == "" {
		return store.InvalidArgument("scheduled failure code is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_messages SET failed_at = ?, failure_code = ?, lease_owner = '', lease_until = 0, next_attempt_at = 0 WHERE id = ? AND lease_owner = ? AND delivered = 0 AND failed_at = 0 AND lease_until > ?`, failedAt.UTC().Unix(), failureCode, id, owner, now.Unix())
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
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
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.UserGroupPage{}, err
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return classify(err)
	}
	for _, userID := range value.Participants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO call_participants(call_id, user_id) SELECT ?, id FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0`, value.ID, userID, value.WorkspaceID); err != nil {
			return err
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		// The guarded UPDATE cannot distinguish "no such call" from "already
		// ended", and reporting ErrNotFound for both told the caller its call had
		// vanished when it had simply already finished. The in-memory repository
		// reported ErrAlreadyExists for the second case all along, so the two
		// profiles answered the same request differently.
		var ended int64
		if err := tx.QueryRowContext(ctx, `SELECT ended_at FROM calls WHERE workspace_id = ? AND id = ?`, workspace, id).Scan(&ended); err != nil {
			return translateNotFound(err)
		}
		if ended != 0 {
			return store.ErrAlreadyExists
		}
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO files(id, workspace_id, uploader_id, name, title, mime_type, blob_key, size, created_at, deleted, name_folded, title_folded) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`, file.ID, file.WorkspaceID, file.Uploader, file.Name, file.Title, file.MIMEType, file.BlobKey, file.Size, domain.NewStoredTime(file.CreatedAt), domain.FoldSearchText(file.Name), domain.FoldSearchText(file.Title)); err != nil {
		return err
	}
	seen := make(map[domain.ConversationID]struct{}, len(file.SharedChannels))
	for _, conversationID := range file.SharedChannels {
		if _, duplicate := seen[conversationID]; duplicate {
			continue
		}
		seen[conversationID] = struct{}{}
		result, err := tx.ExecContext(ctx, `INSERT INTO file_shares(file_id, conversation_id)
			SELECT ?, id FROM conversations WHERE id = ? AND workspace_id = ?`, file.ID, conversationID, file.WorkspaceID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return err
		} else if changed != 1 {
			return store.ErrNotFound
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateExternalUpload(ctx context.Context, value domain.ExternalUpload) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO external_uploads(id, workspace_id, uploader_id, name, title, mime_type, blob_key, file_id, size, status, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.Uploader, value.Name, value.Title, value.MIMEType, value.BlobKey, value.FileID, value.Size, value.Status, domain.NewStoredTime(value.CreatedAt), domain.NewStoredTime(value.ExpiresAt))
	return classify(err)
}

func (s *Store) GetExternalUpload(ctx context.Context, id domain.ExternalUploadID) (domain.ExternalUpload, error) {
	var value domain.ExternalUpload
	var created, expires, uploaded, completed string
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, uploader_id, name, title, mime_type, blob_key, file_id, size, status, created_at, expires_at, uploaded_at, completed_at FROM external_uploads WHERE id = ?`, id).Scan(&value.ID, &value.WorkspaceID, &value.Uploader, &value.Name, &value.Title, &value.MIMEType, &value.BlobKey, &value.FileID, &value.Size, &value.Status, &created, &expires, &uploaded, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExternalUpload{}, store.ErrNotFound
	}
	if err != nil {
		return domain.ExternalUpload{}, err
	}
	for field, target := range map[string]*time.Time{"created_at": &value.CreatedAt, "expires_at": &value.ExpiresAt, "uploaded_at": &value.UploadedAt, "completed_at": &value.CompletedAt} {
		text := map[string]string{"created_at": created, "expires_at": expires, "uploaded_at": uploaded, "completed_at": completed}[field]
		if text == "" {
			continue
		}
		parsed, parseErr := domain.ParseStoredTime(text)
		if parseErr != nil {
			return domain.ExternalUpload{}, fmt.Errorf("parse external upload %s: %w", field, parseErr)
		}
		*target = parsed
	}
	return value, nil
}

func (s *Store) MarkExternalUploadUploaded(ctx context.Context, id domain.ExternalUploadID, uploadedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE external_uploads SET status = ?, uploaded_at = ? WHERE id = ? AND status = ? AND expires_at > ?`, domain.ExternalUploadUploaded, domain.NewStoredTime(uploadedAt), id, domain.ExternalUploadPending, domain.NewStoredTime(time.Now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrConflict
	}
	return nil
}

func (s *Store) CompleteExternalUpload(ctx context.Context, id domain.ExternalUploadID, file domain.File, channels []domain.ConversationID, event events.Event) error {
	return s.CompleteExternalUploads(ctx, []domain.ExternalUploadCompletion{{ID: id, Title: file.Title}}, []domain.File{file}, channels, []events.Event{event}, nil, nil)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	file.CreatedAt, err = domain.ParseStoredTime(created)
	file.Deleted = deleted != 0
	if err == nil {
		file.SharedChannels, err = s.listFileShares(ctx, file.WorkspaceID, file.ID)
		if errors.Is(err, store.ErrNotFound) {
			err = nil
		}
	}
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
		return classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	file.CreatedAt, err = domain.ParseStoredTime(created)
	return file, err
}

func (s *Store) ListFiles(ctx context.Context, workspace domain.WorkspaceID, request domain.PageRequest) (domain.FilePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.FilePage{}, err
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
		file.CreatedAt, err = domain.ParseStoredTime(created)
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

func (s *Store) ListVisibleFiles(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.FilePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.FilePage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.FilePage{}, err
	}
	query := `SELECT f.id, f.workspace_id, f.uploader_id, f.name, f.title, f.mime_type, f.blob_key, f.size, f.created_at, f.deleted, f.public_token
		FROM files f WHERE f.workspace_id = ? AND f.deleted = 0 AND ` + visibleFilePredicate("f")
	args := []any{workspace, user, user}
	if after != "" {
		query += ` AND f.id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY f.id LIMIT ?`
	args = append(args, request.Limit+1)
	values, err := s.readFiles(ctx, query, args...)
	if err != nil {
		return domain.FilePage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	if err := s.hydrateFileShares(ctx, values); err != nil {
		return domain.FilePage{}, err
	}
	page := domain.FilePage{Files: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func (s *Store) SearchFiles(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, search domain.FileSearch) (domain.FilePage, error) {
	if search.Count <= 0 || search.Page <= 0 || search.Page > 100 {
		return domain.FilePage{}, store.InvalidArgument("file search page is invalid")
	}
	where := `f.workspace_id = ? AND f.deleted = 0 AND ` + visibleFilePredicate("f")
	args := []any{workspace, user, user}
	for _, term := range search.Terms {
		where += ` AND (f.name_folded LIKE ? ESCAPE '\' OR f.title_folded LIKE ? ESCAPE '\')`
		pattern := "%" + escapeLikeTerm(domain.FoldSearchText(term)) + "%"
		args = append(args, pattern, pattern)
	}
	for _, term := range search.ExcludedTerms {
		where += ` AND f.name_folded NOT LIKE ? ESCAPE '\' AND f.title_folded NOT LIKE ? ESCAPE '\'`
		pattern := "%" + escapeLikeTerm(domain.FoldSearchText(term)) + "%"
		args = append(args, pattern, pattern)
	}
	if search.Uploader != "" {
		where += ` AND f.uploader_id = ?`
		args = append(args, search.Uploader)
	}
	if search.ExcludedUploader != "" {
		where += ` AND f.uploader_id <> ?`
		args = append(args, search.ExcludedUploader)
	}
	if search.Conversation != "" {
		where += ` AND EXISTS (SELECT 1 FROM file_shares fs_search WHERE fs_search.file_id = f.id AND fs_search.conversation_id = ?)`
		args = append(args, search.Conversation)
	}
	if search.ExcludedConversation != "" {
		where += ` AND NOT EXISTS (SELECT 1 FROM file_shares fs_excluded WHERE fs_excluded.file_id = f.id AND fs_excluded.conversation_id = ?)`
		args = append(args, search.ExcludedConversation)
	}
	if search.FileType != "" {
		kind := strings.TrimPrefix(domain.FoldSearchText(search.FileType), ".")
		where += ` AND (f.name_folded LIKE ? ESCAPE '\' OR f.mime_type LIKE ? ESCAPE '\')`
		args = append(args, "%."+escapeLikeTerm(kind), "%"+escapeLikeTerm(kind)+"%")
	}
	if !search.After.IsZero() {
		where += ` AND f.created_at >= ?`
		args = append(args, domain.NewStoredTime(search.After))
	}
	if !search.Before.IsZero() {
		where += ` AND f.created_at < ?`
		args = append(args, domain.NewStoredTime(search.Before))
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files f WHERE `+where, args...).Scan(&total); err != nil {
		return domain.FilePage{}, err
	}
	direction := "ASC"
	if search.Direction == domain.SearchDirectionDescending {
		direction = "DESC"
	}
	query := `SELECT f.id, f.workspace_id, f.uploader_id, f.name, f.title, f.mime_type, f.blob_key, f.size, f.created_at, f.deleted, f.public_token
		FROM files f WHERE ` + where + ` ORDER BY f.created_at ` + direction + `, f.id ` + direction + ` LIMIT ? OFFSET ?`
	queryArgs := append(append([]any(nil), args...), search.Count, (search.Page-1)*search.Count)
	values, err := s.readFiles(ctx, query, queryArgs...)
	if err != nil {
		return domain.FilePage{}, err
	}
	if err := s.hydrateFileShares(ctx, values); err != nil {
		return domain.FilePage{}, err
	}
	return domain.FilePage{Files: values, HasMore: search.Page*search.Count < total, Total: total}, nil
}

func (s *Store) RecordSearchHistory(ctx context.Context, value domain.SearchHistoryEntry) error {
	value.Query = strings.TrimSpace(value.Query)
	if value.WorkspaceID == "" || value.UserID == "" || value.Query == "" || utf8.RuneCountInString(value.Query) > 500 || value.SearchedAt.IsZero() {
		return store.InvalidArgument("search history entry is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO recent_searches(workspace_id, user_id, query, searched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, user_id, query) DO UPDATE SET searched_at = excluded.searched_at`,
		value.WorkspaceID, value.UserID, value.Query, value.SearchedAt.UTC().UnixNano()); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recent_searches
		WHERE workspace_id = ? AND user_id = ? AND query NOT IN (
			SELECT query FROM recent_searches WHERE workspace_id = ? AND user_id = ?
			ORDER BY searched_at DESC, query LIMIT ?
		)`, value.WorkspaceID, value.UserID, value.WorkspaceID, value.UserID, store.MaxSearchHistoryEntries); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListSearchHistory(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, limit int) ([]domain.SearchHistoryEntry, error) {
	if workspace == "" || user == "" || limit <= 0 || limit > store.MaxSearchHistoryEntries {
		return nil, store.InvalidArgument("search history request is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT query, searched_at FROM recent_searches
		WHERE workspace_id = ? AND user_id = ? ORDER BY searched_at DESC, query LIMIT ?`, workspace, user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.SearchHistoryEntry, 0, limit)
	for rows.Next() {
		var value domain.SearchHistoryEntry
		var searchedAt int64
		if err := rows.Scan(&value.Query, &searchedAt); err != nil {
			return nil, err
		}
		value.WorkspaceID = workspace
		value.UserID = user
		value.SearchedAt = time.Unix(0, searchedAt).UTC()
		values = append(values, value)
	}
	return values, rows.Err()
}

func visibleFilePredicate(alias string) string {
	return `(` + alias + `.uploader_id = ? OR EXISTS (
		SELECT 1 FROM file_shares fs_visible
		JOIN conversations c_visible ON c_visible.id = fs_visible.conversation_id
		WHERE fs_visible.file_id = ` + alias + `.id
		AND (c_visible.is_private = 0 OR EXISTS (
			SELECT 1 FROM conversation_members cm_visible
			WHERE cm_visible.conversation_id = c_visible.id AND cm_visible.user_id = ?
		))
	))`
}

func (s *Store) readFiles(ctx context.Context, query string, args ...any) ([]domain.File, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.File, 0)
	for rows.Next() {
		var file domain.File
		var created string
		var deleted int
		if err := rows.Scan(&file.ID, &file.WorkspaceID, &file.Uploader, &file.Name, &file.Title, &file.MIMEType, &file.BlobKey, &file.Size, &created, &deleted, &file.PublicToken); err != nil {
			return nil, err
		}
		file.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			return nil, err
		}
		file.Deleted = deleted != 0
		values = append(values, file)
	}
	return values, rows.Err()
}

func (s *Store) hydrateFileShares(ctx context.Context, files []domain.File) error {
	for index := range files {
		shares, err := s.listFileShares(ctx, files[index].WorkspaceID, files[index].ID)
		if err != nil {
			return err
		}
		files[index].SharedChannels = shares
	}
	return nil
}

func (s *Store) WalkBlobReferences(ctx context.Context, workspace domain.WorkspaceID, visit func(string) error) error {
	if visit == nil {
		return store.InvalidArgument("blob reference visitor is required")
	}
	// Collect first, then visit. The previous shape kept the files result set open
	// across the users query and across every visitor call, so blob garbage
	// collection pinned two pooled connections for the duration of blob-store I/O.
	// The in-memory repository already collects before visiting.
	references, err := s.collectBlobReferences(ctx, workspace)
	if err != nil {
		return err
	}
	for _, reference := range references {
		if err := visit(reference); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) collectBlobReferences(ctx context.Context, workspace domain.WorkspaceID) ([]string, error) {
	references := make([]string, 0)
	rows, err := s.db.QueryContext(ctx, `SELECT blob_key FROM files WHERE workspace_id = ? AND deleted = 0`, workspace)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var reference string
		if err := rows.Scan(&reference); err != nil {
			rows.Close()
			return nil, err
		}
		if reference == "" {
			rows.Close()
			return nil, errors.New("database contains an empty blob reference")
		}
		references = append(references, reference)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id, image_24 FROM users WHERE workspace_id = ? AND deleted = 0 AND image_24 <> ''`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID, imageURL string
		if err := rows.Scan(&userID, &imageURL); err != nil {
			return nil, err
		}
		// users.profile.set accepts image_24 as free text, so a member can store
		// an ordinary external avatar URL there. Failing the walk on one made
		// blob garbage collection for the whole workspace unrunnable, and any
		// member could trigger it from their own profile. A URL this deployment
		// did not mint names no blob of ours, so it is not a reference.
		if key, ok := domain.UserPhotoBlobKey(workspace, domain.UserID(userID), imageURL); ok {
			references = append(references, key)
		}
	}
	return references, rows.Err()
}

func (s *Store) AddRemoteFile(ctx context.Context, value domain.RemoteFile, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO remote_files(id, workspace_id, external_id, title, file_type, external_url, preview_image, indexable_contents, created_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, value.ID, value.WorkspaceID, value.ExternalID, value.Title, value.FileType, value.ExternalURL, value.PreviewImage, value.IndexableContents, value.CreatedAt.UTC().Unix())
	if err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetRemoteFile(ctx context.Context, workspace domain.WorkspaceID, lookup domain.RemoteFileLookup) (domain.RemoteFile, error) {
	return s.getRemoteFileOn(ctx, s.db, workspace, lookup)
}

// getRemoteFileOn reads through the caller's executor. See getBookmarkOn: a
// mutation that commits and then re-reads through s.db can return a concurrent
// writer's value as the result of its own write.
func (s *Store) getRemoteFileOn(ctx context.Context, db queryExecutor, workspace domain.WorkspaceID, lookup domain.RemoteFileLookup) (domain.RemoteFile, error) {
	query := `SELECT id, workspace_id, external_id, title, file_type, external_url, preview_image, indexable_contents, created_at, deleted FROM remote_files WHERE workspace_id = ? AND deleted = 0 AND id = ?`
	args := []any{workspace, lookup.ID}
	if lookup.ID == "" {
		query = `SELECT id, workspace_id, external_id, title, file_type, external_url, preview_image, indexable_contents, created_at, deleted FROM remote_files WHERE workspace_id = ? AND deleted = 0 AND external_id = ?`
		args = []any{workspace, lookup.ExternalID}
	}
	var value domain.RemoteFile
	var created int64
	var deleted int
	err := db.QueryRowContext(ctx, query, args...).Scan(&value.ID, &value.WorkspaceID, &value.ExternalID, &value.Title, &value.FileType, &value.ExternalURL, &value.PreviewImage, &value.IndexableContents, &created, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RemoteFile{}, store.ErrNotFound
	}
	if err != nil {
		return domain.RemoteFile{}, err
	}
	value.CreatedAt = time.Unix(created, 0).UTC()
	value.Deleted = deleted != 0
	value.SharedChannels, err = s.remoteFileShares(ctx, db, value.ID)
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
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.RemoteFilePage{}, err
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
	if err := insertOutbox(ctx, tx, event); err != nil {
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.RemoteFile{}, err
	}
	shared, err := s.getRemoteFileOn(ctx, tx, workspace, domain.RemoteFileLookup{ID: id})
	if err != nil {
		return domain.RemoteFile{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RemoteFile{}, err
	}
	return shared, nil
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.RemoteFile{}, err
	}
	updated, err := s.getRemoteFileOn(ctx, tx, workspace, domain.RemoteFileLookup{ID: value.ID})
	if err != nil {
		return domain.RemoteFile{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RemoteFile{}, err
	}
	return updated, nil
}

func (s *Store) GetMessage(ctx context.Context, id domain.MessageID) (domain.Message, error) {
	var message domain.Message
	var deleted int
	var created string
	var unfurls string
	var attachments string
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, conversation, author_id, app_id, text, blocks, attachments, metadata, stream_state, thread_timestamp, created_at, deleted, unfurls FROM messages WHERE id = ?`, id).Scan(&message.ID, &message.WorkspaceID, &message.Conversation, &message.AuthorID, &message.AppID, &message.Text, &message.Blocks, &attachments, &message.Metadata, &message.StreamState, &message.ThreadTimestamp, &created, &deleted, &unfurls)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Message{}, err
	}
	message.CreatedAt, err = domain.ParseStoredTime(created)
	if err != nil {
		return domain.Message{}, err
	}
	message.Deleted = deleted != 0
	message.Attachments = attachments
	message.Unfurls, err = decodeUnfurls(unfurls)
	if err != nil {
		return domain.Message{}, err
	}
	values := []domain.Message{message}
	if err := s.hydrateMessageFiles(ctx, values); err != nil {
		return domain.Message{}, err
	}
	message = values[0]
	return message, nil
}

func (s *Store) hydrateMessageFiles(ctx context.Context, messages []domain.Message) error {
	if len(messages) == 0 {
		return nil
	}
	messageIndexes := make(map[domain.MessageID]int, len(messages))
	arguments := make([]any, 0, len(messages))
	placeholders := make([]string, 0, len(messages))
	for index := range messages {
		messageIndexes[messages[index].ID] = index
		arguments = append(arguments, messages[index].ID)
		placeholders = append(placeholders, "?")
		messages[index].Files = nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT mf.message_id, f.id, f.workspace_id, f.uploader_id, f.name, f.title, f.mime_type, f.blob_key, f.size, f.created_at, f.deleted, f.public_token
		FROM message_files mf JOIN files f ON f.id = mf.file_id
		WHERE mf.message_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY mf.message_id, mf.position`, arguments...)
	if err != nil {
		return err
	}
	type filePosition struct {
		message int
		file    int
	}
	positions := make(map[domain.FileID][]filePosition)
	fileIDs := make([]domain.FileID, 0)
	for rows.Next() {
		var messageID domain.MessageID
		var file domain.File
		var created string
		var deleted int
		if err := rows.Scan(&messageID, &file.ID, &file.WorkspaceID, &file.Uploader, &file.Name, &file.Title, &file.MIMEType, &file.BlobKey, &file.Size, &created, &deleted, &file.PublicToken); err != nil {
			rows.Close()
			return err
		}
		file.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			rows.Close()
			return err
		}
		file.Deleted = deleted != 0
		messageIndex, exists := messageIndexes[messageID]
		if !exists {
			rows.Close()
			return errors.New("message file relationship names an unknown message")
		}
		fileIndex := len(messages[messageIndex].Files)
		messages[messageIndex].Files = append(messages[messageIndex].Files, file)
		if _, exists := positions[file.ID]; !exists {
			fileIDs = append(fileIDs, file.ID)
		}
		positions[file.ID] = append(positions[file.ID], filePosition{message: messageIndex, file: fileIndex})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(fileIDs) == 0 {
		return nil
	}
	arguments = arguments[:0]
	placeholders = placeholders[:0]
	for _, fileID := range fileIDs {
		arguments = append(arguments, fileID)
		placeholders = append(placeholders, "?")
	}
	rows, err = s.db.QueryContext(ctx, `SELECT file_id, conversation_id FROM file_shares WHERE file_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY file_id, conversation_id`, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var fileID domain.FileID
		var conversation domain.ConversationID
		if err := rows.Scan(&fileID, &conversation); err != nil {
			return err
		}
		for _, position := range positions[fileID] {
			messages[position.message].Files[position.file].SharedChannels = append(messages[position.message].Files[position.file].SharedChannels, conversation)
		}
	}
	return rows.Err()
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

// internalTopics carry repository-internal payloads — a blob storage key — that
// exist only so a dedicated cleanup worker can claim them by topic. They must not
// be claimed by the general outbox worker and must not appear in any client-facing
// replay: user.photo_blob_delete was excluded from neither, so the general worker
// consumed and acknowledged it (the blob was then never deleted) and the internal
// storage key was delivered to third-party Events API subscribers.
func internalTopicPredicate(column string) (string, []any) {
	topics := store.InternalTopics()
	placeholders := make([]string, len(topics))
	args := make([]any, 0, len(topics))
	for index, topic := range topics {
		placeholders[index] = "?"
		args = append(args, topic)
	}
	return " AND " + column + " NOT IN (" + strings.Join(placeholders, ", ") + ")", args
}

func (s *Store) ClaimEvents(ctx context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	return s.claimEvents(ctx, workspace, "", owner, limit, lease)
}

func (s *Store) ClaimEventsForTopic(ctx context.Context, workspace domain.WorkspaceID, topic, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	if topic == "" {
		return nil, store.InvalidArgument("topic is required")
	}
	return s.claimEvents(ctx, workspace, topic, owner, limit, lease)
}

func (s *Store) claimEvents(ctx context.Context, workspace domain.WorkspaceID, topic, owner string, limit int, lease time.Duration) ([]events.Record, error) {
	if workspace == "" || owner == "" || limit <= 0 || lease <= 0 {
		return nil, store.InvalidArgument("workspace, owner, positive limit, and positive lease are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now()
	nowText := domain.NewStoredTime(now)
	expiresText := domain.NewStoredTime(now.Add(lease))
	query := `SELECT sequence, id, workspace_id, topic, payload, created_at FROM outbox WHERE workspace_id = ? AND delivered = 0`
	args := []any{workspace}
	if topic == "" {
		predicate, excluded := internalTopicPredicate("topic")
		query += predicate
		args = append(args, excluded...)
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
		item.event.CreatedAt, err = domain.ParseStoredTime(item.created)
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
		return store.InvalidArgument("owner and event sequences are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowText := domain.NewStoredTime(s.now())
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
		return store.InvalidArgument("owner, event sequences, and positive lease are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now()
	nowText := domain.NewStoredTime(now)
	expiresText := domain.NewStoredTime(now.Add(lease))
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
	if owner == "" || len(sequences) == 0 || !retryAt.After(s.now()) {
		return store.InvalidArgument("owner, event sequences, and a future retry time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowText := domain.NewStoredTime(s.now())
	retryText := domain.NewStoredTime(retryAt)
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
		return nil, store.InvalidArgument("event limit must be positive")
	}
	predicate, excluded := internalTopicPredicate("topic")
	args := append([]any{workspace, after}, excluded...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, id, workspace_id, actor_id, topic, payload, created_at FROM outbox WHERE workspace_id = ? AND sequence > ?`+predicate+` ORDER BY sequence LIMIT ?`, args...)
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
		event.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			return nil, err
		}
		result = append(result, events.Record{Sequence: sequence, Event: event})
	}
	return result, rows.Err()
}

func (s *Store) ListAppEventsAfter(ctx context.Context, appID domain.AppID, after uint64, limit int) ([]events.Record, error) {
	if appID == "" || limit <= 0 {
		return nil, store.InvalidArgument("app ID and positive event limit are required")
	}
	predicate, excluded := internalTopicPredicate("o.topic")
	args := append([]any{appID, after}, excluded...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT o.sequence, o.id, o.workspace_id, o.actor_id, o.topic, o.payload, o.created_at FROM outbox o JOIN app_installations i ON i.workspace_id = o.workspace_id WHERE i.app_id = ? AND i.enabled = 1 AND o.sequence > ?`+predicate+` ORDER BY o.sequence LIMIT ?`, args...)
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
		record.Event.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func validAppEventSurface(surface string) bool {
	return surface == "http" || surface == "socket"
}

func (s *Store) ClaimAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, lease time.Duration) (events.Record, int, string, bool, error) {
	if appID == "" || !validAppEventSurface(surface) || strings.TrimSpace(owner) == "" || lease <= 0 {
		return events.Record{}, 0, "", false, store.InvalidArgument("app event claim fields are invalid")
	}
	var claimed events.Record
	var retryCount int
	var retryReason string
	var found bool
	err := underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, `INSERT INTO app_event_cursors(app_id, surface) SELECT ?, ? WHERE EXISTS (SELECT 1 FROM slack_apps a JOIN app_installations i ON i.app_id = a.id AND i.enabled = 1 WHERE a.id = ? AND a.deleted = 0) ON CONFLICT(app_id, surface) DO NOTHING`, appID, surface, appID)
		if err != nil {
			return classify(err)
		}
		var sequence, leasedSequence uint64
		var leaseOwner string
		var leaseUntil, retryAt int64
		err = tx.QueryRowContext(ctx, `SELECT sequence, leased_sequence, lease_owner, lease_until, retry_at, retry_count, retry_reason FROM app_event_cursors WHERE app_id = ? AND surface = ?`, appID, surface).
			Scan(&sequence, &leasedSequence, &leaseOwner, &leaseUntil, &retryAt, &retryCount, &retryReason)
		if err != nil {
			return translateNotFound(err)
		}
		now := time.Now().UTC()
		if (leasedSequence != 0 && leaseUntil > now.UnixNano()) || retryAt > now.UnixNano() {
			return tx.Commit()
		}
		predicate, excluded := internalTopicPredicate("o.topic")
		args := append([]any{appID, sequence}, excluded...)
		var created string
		err = tx.QueryRowContext(ctx, `SELECT o.sequence, o.id, o.workspace_id, o.actor_id, o.topic, o.payload, o.created_at
			FROM outbox o JOIN app_installations i ON i.workspace_id = o.workspace_id AND i.app_id = ? AND i.enabled = 1
			WHERE o.sequence > ?`+predicate+` ORDER BY o.sequence LIMIT 1`, args...).
			Scan(&claimed.Sequence, &claimed.Event.ID, &claimed.Event.WorkspaceID, &claimed.Event.ActorID, &claimed.Event.Topic, &claimed.Event.Payload, &created)
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		if err != nil {
			return err
		}
		claimed.Event.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			return err
		}
		update, err := tx.ExecContext(ctx, `UPDATE app_event_cursors SET leased_sequence = ?, lease_owner = ?, lease_until = ? WHERE app_id = ? AND surface = ? AND sequence = ? AND (leased_sequence = 0 OR lease_until <= ?) AND retry_at <= ?`,
			claimed.Sequence, owner, now.Add(lease).UnixNano(), appID, surface, sequence, now.UnixNano(), now.UnixNano())
		if err != nil {
			return err
		}
		changed, err := update.RowsAffected()
		if err != nil {
			return err
		}
		found = changed == 1
		return tx.Commit()
	})
	if err != nil {
		return events.Record{}, 0, "", false, err
	}
	return claimed, retryCount, retryReason, found, nil
}

func (s *Store) AckAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, sequence uint64) error {
	if appID == "" || !validAppEventSurface(surface) || strings.TrimSpace(owner) == "" || sequence == 0 {
		return store.InvalidArgument("app event acknowledgement fields are invalid")
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, `UPDATE app_event_cursors SET sequence = ?, leased_sequence = 0, lease_owner = '', lease_until = 0, retry_at = 0, retry_count = 0, retry_reason = '' WHERE app_id = ? AND surface = ? AND leased_sequence = ? AND lease_owner = ? AND lease_until > ?`, sequence, appID, surface, sequence, owner, now)
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
	return s.appEventLeaseError(ctx, appID, surface, owner, sequence, now)
}

func (s *Store) ReleaseAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, sequence uint64, reason string, retryAt time.Time) error {
	if appID == "" || !validAppEventSurface(surface) || strings.TrimSpace(owner) == "" || sequence == 0 || strings.TrimSpace(reason) == "" || retryAt.IsZero() {
		return store.InvalidArgument("app event release fields are invalid")
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, `UPDATE app_event_cursors SET leased_sequence = 0, lease_owner = '', lease_until = 0, retry_at = ?, retry_count = retry_count + 1, retry_reason = ? WHERE app_id = ? AND surface = ? AND leased_sequence = ? AND lease_owner = ? AND lease_until > ?`, retryAt.UTC().UnixNano(), strings.TrimSpace(reason), appID, surface, sequence, owner, now)
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
	return s.appEventLeaseError(ctx, appID, surface, owner, sequence, now)
}

func (s *Store) appEventLeaseError(ctx context.Context, appID domain.AppID, surface, owner string, sequence uint64, now int64) error {
	var leasedSequence uint64
	var leaseOwner string
	var leaseUntil int64
	err := s.db.QueryRowContext(ctx, `SELECT leased_sequence, lease_owner, lease_until FROM app_event_cursors WHERE app_id = ? AND surface = ?`, appID, surface).Scan(&leasedSequence, &leaseOwner, &leaseUntil)
	if err := translateNotFound(err); err != nil {
		return err
	}
	if leasedSequence == sequence && leaseOwner == owner && leaseUntil <= now {
		return store.ErrLeaseConflict
	}
	return store.ErrLeaseConflict
}

// ListMessages pages one conversation in either direction; see the port for why
// the descending direction exists.
//
// Both directions are one keyset read over messages(conversation, created_at,
// id), which the engine walks backwards for the descending order, so the newest
// window costs one index seek rather than a walk of the whole conversation. The
// predicate and the ORDER BY are flipped together and nothing else changes: the
// cursor encoding, the Limit+1 probe for HasMore and NextCursor are identical,
// which is what makes a cursor minted by one direction usable by the other.
func (s *Store) ListMessages(ctx context.Context, conversation domain.ConversationID, request domain.PageRequest) (domain.MessagePage, error) {
	if err := store.CheckPage(request); err != nil {
		return domain.MessagePage{}, err
	}
	query := `SELECT id, workspace_id, conversation, author_id, app_id, text, blocks, attachments, metadata, stream_state, thread_timestamp, created_at, deleted, unfurls FROM messages WHERE conversation = ? AND deleted = 0`
	args := []any{conversation}
	if request.Cursor != "" {
		createdAt, id, err := domain.DecodeMessageCursor(request.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
		if request.Descending {
			query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		} else {
			query += ` AND (created_at > ? OR (created_at = ? AND id > ?))`
		}
		created := domain.NewStoredTime(createdAt)
		args = append(args, created, created, id)
	}
	if request.Descending {
		query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	} else {
		query += ` ORDER BY created_at, id LIMIT ?`
	}
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.MessagePage{}, err
	}
	defer rows.Close()
	var values []domain.Message
	for rows.Next() {
		var value domain.Message
		var created, attachments, unfurls string
		var deleted int
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Conversation, &value.AuthorID, &value.AppID, &value.Text, &value.Blocks, &attachments, &value.Metadata, &value.StreamState, &value.ThreadTimestamp, &created, &deleted, &unfurls); err != nil {
			return domain.MessagePage{}, err
		}
		value.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			return domain.MessagePage{}, err
		}
		value.Deleted = deleted != 0
		value.Attachments = attachments
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
	if err := s.hydrateMessageFiles(ctx, values); err != nil {
		return domain.MessagePage{}, err
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

func (s *Store) ListAuthoredMessages(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.MessagePage, error) {
	if err := store.CheckPage(request); err != nil {
		return domain.MessagePage{}, err
	}
	query := `SELECT m.id, m.workspace_id, m.conversation, m.author_id, m.app_id, m.text, m.blocks, m.attachments, m.metadata, m.stream_state, m.thread_timestamp, m.created_at, m.deleted, m.unfurls
		FROM messages m
		JOIN conversations c ON c.id = m.conversation
		WHERE m.workspace_id = ? AND m.author_id = ? AND m.deleted = 0
		AND (c.is_private = 0 OR EXISTS (
			SELECT 1 FROM conversation_members cm
			WHERE cm.conversation_id = m.conversation AND cm.user_id = ?
		))`
	args := []any{workspace, user, user}
	if request.Cursor != "" {
		createdAt, id, err := domain.DecodeMessageCursor(request.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
		if request.Descending {
			query += ` AND (m.created_at < ? OR (m.created_at = ? AND m.id < ?))`
		} else {
			query += ` AND (m.created_at > ? OR (m.created_at = ? AND m.id > ?))`
		}
		created := domain.NewStoredTime(createdAt)
		args = append(args, created, created, id)
	}
	if request.Descending {
		query += ` ORDER BY m.created_at DESC, m.id DESC LIMIT ?`
	} else {
		query += ` ORDER BY m.created_at, m.id LIMIT ?`
	}
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.MessagePage{}, err
	}
	defer rows.Close()
	values := make([]domain.Message, 0, request.Limit+1)
	for rows.Next() {
		var value domain.Message
		var created, attachments, unfurls string
		var deleted int
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Conversation, &value.AuthorID, &value.AppID, &value.Text, &value.Blocks, &attachments, &value.Metadata, &value.StreamState, &value.ThreadTimestamp, &created, &deleted, &unfurls); err != nil {
			return domain.MessagePage{}, err
		}
		value.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			return domain.MessagePage{}, err
		}
		value.Deleted = deleted != 0
		value.Attachments = attachments
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
	if err := s.hydrateMessageFiles(ctx, values); err != nil {
		return domain.MessagePage{}, err
	}
	page := domain.MessagePage{Messages: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewMessageCursor(values[len(values)-1])
	}
	return page, err
}

func (s *Store) SearchMessages(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, search domain.MessageSearch) (domain.MessagePage, error) {
	if err := store.CheckPage(search.Page); err != nil {
		return domain.MessagePage{}, err
	}
	if len(search.Terms) == 0 && search.Conversation == "" && search.Author == "" && search.WithUser == "" && search.After.IsZero() && search.Before.IsZero() && !search.ThreadOnly && !search.HasFiles && !search.HasPins && !search.HasReactions && search.SavedBy == "" {
		return domain.MessagePage{}, store.InvalidArgument("search query must not be empty")
	}
	querySQL := `SELECT m.id, m.workspace_id, m.conversation, m.author_id, m.app_id, m.text, m.blocks, m.attachments, m.metadata, m.stream_state, m.thread_timestamp, m.created_at, m.deleted, m.unfurls FROM messages m JOIN conversations c ON c.id = m.conversation WHERE m.workspace_id = ? AND m.deleted = 0 AND (c.is_private = 0 OR EXISTS (SELECT 1 FROM conversation_members cm WHERE cm.conversation_id = m.conversation AND cm.user_id = ?))`
	args := []any{workspace, user}
	for _, term := range search.Terms {
		querySQL += ` AND m.text_folded LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLikeTerm(domain.FoldSearchText(term))+"%")
	}
	for _, term := range search.ExcludedTerms {
		querySQL += ` AND m.text_folded NOT LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLikeTerm(domain.FoldSearchText(term))+"%")
	}
	if search.Conversation != "" {
		querySQL += ` AND m.conversation = ?`
		args = append(args, search.Conversation)
	}
	if search.ExcludedConversation != "" {
		querySQL += ` AND m.conversation <> ?`
		args = append(args, search.ExcludedConversation)
	}
	if search.Author != "" {
		querySQL += ` AND m.author_id = ?`
		args = append(args, search.Author)
	}
	if search.ExcludedAuthor != "" {
		querySQL += ` AND m.author_id <> ?`
		args = append(args, search.ExcludedAuthor)
	}
	if search.WithUser != "" {
		querySQL += ` AND EXISTS (SELECT 1 FROM conversation_members cm_with WHERE cm_with.conversation_id = m.conversation AND cm_with.user_id = ?)`
		args = append(args, search.WithUser)
	}
	if !search.After.IsZero() {
		querySQL += ` AND m.created_at >= ?`
		args = append(args, domain.NewStoredTime(search.After))
	}
	if !search.Before.IsZero() {
		querySQL += ` AND m.created_at < ?`
		args = append(args, domain.NewStoredTime(search.Before))
	}
	if search.ThreadOnly {
		querySQL += ` AND m.thread_timestamp <> ''`
	}
	if search.HasFiles {
		querySQL += ` AND EXISTS (SELECT 1 FROM message_files mf_search WHERE mf_search.message_id = m.id)`
	}
	if search.HasPins {
		querySQL += ` AND EXISTS (SELECT 1 FROM pins p_search WHERE p_search.message_id = m.id)`
	}
	if search.HasReactions {
		querySQL += ` AND EXISTS (SELECT 1 FROM reactions r_search WHERE r_search.message_id = m.id)`
	}
	if search.SavedBy != "" {
		querySQL += ` AND EXISTS (SELECT 1 FROM saved_items si_search WHERE si_search.message_id = m.id AND si_search.user_id = ?)`
		args = append(args, search.SavedBy)
	}
	countSQL := strings.Replace(querySQL, `SELECT m.id, m.workspace_id, m.conversation, m.author_id, m.app_id, m.text, m.blocks, m.attachments, m.metadata, m.stream_state, m.thread_timestamp, m.created_at, m.deleted, m.unfurls`, `SELECT COUNT(*)`, 1)
	var total int
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return domain.MessagePage{}, err
	}
	if search.Page.Cursor != "" {
		createdAt, id, err := domain.DecodeMessageCursor(search.Page.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
		created := domain.NewStoredTime(createdAt)
		operator := ">"
		if search.Page.Descending {
			operator = "<"
		}
		querySQL += ` AND (m.created_at ` + operator + ` ? OR (m.created_at = ? AND m.id ` + operator + ` ?))`
		args = append(args, created, created, id)
	}
	direction := "ASC"
	if search.Page.Descending {
		direction = "DESC"
	}
	querySQL += ` ORDER BY m.created_at ` + direction + `, m.id ` + direction + ` LIMIT ?`
	args = append(args, search.Page.Limit+1)
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return domain.MessagePage{}, err
	}
	defer rows.Close()
	values := make([]domain.Message, 0, search.Page.Limit+1)
	for rows.Next() {
		var message domain.Message
		var created, attachments, unfurls string
		var deleted int
		if err := rows.Scan(&message.ID, &message.WorkspaceID, &message.Conversation, &message.AuthorID, &message.AppID, &message.Text, &message.Blocks, &attachments, &message.Metadata, &message.StreamState, &message.ThreadTimestamp, &created, &deleted, &unfurls); err != nil {
			return domain.MessagePage{}, err
		}
		message.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			return domain.MessagePage{}, err
		}
		message.Deleted = deleted != 0
		message.Attachments = attachments
		message.Unfurls, err = decodeUnfurls(unfurls)
		if err != nil {
			return domain.MessagePage{}, err
		}
		values = append(values, message)
	}
	if err := rows.Err(); err != nil {
		return domain.MessagePage{}, err
	}
	hasMore := len(values) > search.Page.Limit
	if hasMore {
		values = values[:search.Page.Limit]
	}
	if err := s.hydrateMessageFiles(ctx, values); err != nil {
		return domain.MessagePage{}, err
	}
	page := domain.MessagePage{Messages: values, HasMore: hasMore, Total: total}
	if hasMore {
		page.NextCursor, err = domain.NewMessageCursor(values[len(values)-1])
		if err != nil {
			return domain.MessagePage{}, err
		}
	}
	return page, nil
}

func (s *Store) CreateList(ctx context.Context, value domain.List, event events.Event) error {
	return s.CreateListWithItems(ctx, value, event, nil)
}

// CreateListWithItems creates a list and its initial items in one transaction.
// See store.Store.CreateListWithItems: the copy_from journey used to publish
// list.created and then copy items one call at a time, so a failure partway left
// a half-copied list that clients had already been told about.
func (s *Store) CreateListWithItems(ctx context.Context, value domain.List, event events.Event, items []store.ListItemCreation) error {
	for _, creation := range items {
		if creation.Item.ID == "" {
			return fmt.Errorf("%w: a list item created with the list must carry an identifier", store.ErrInvalidArgument)
		}
		if creation.Item.ListID != value.ID || creation.Item.WorkspaceID != value.WorkspaceID {
			return fmt.Errorf("%w: list item %q does not belong to the list being created", store.ErrInvalidArgument, creation.Item.ID)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if value.Version == 0 {
		value.Version = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lists(id, workspace_id, owner_id, name, description_blocks, schema_json, todo_mode, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.OwnerID, value.Name, value.DescriptionBlocks, value.Schema, boolInt(value.TodoMode), value.Version, domain.NewStoredTime(value.CreatedAt), domain.NewStoredTime(value.UpdatedAt))
	if err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	for _, creation := range items {
		if err := insertListItem(ctx, tx, creation.Item); err != nil {
			return err
		}
		if err := insertOutbox(ctx, tx, creation.Event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetList(ctx context.Context, workspace domain.WorkspaceID, id domain.ListID) (domain.List, error) {
	var value domain.List
	var createdAt, updatedAt string
	var todoMode int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, owner_id, name, description_blocks, schema_json, todo_mode, version, created_at, updated_at FROM lists WHERE id = ? AND workspace_id = ?`, id, workspace).Scan(&value.ID, &value.WorkspaceID, &value.OwnerID, &value.Name, &value.DescriptionBlocks, &value.Schema, &todoMode, &value.Version, &createdAt, &updatedAt)
	if err != nil {
		return domain.List{}, translateNotFound(err)
	}
	value.TodoMode = todoMode != 0
	value.CreatedAt, err = domain.ParseStoredTime(createdAt)
	if err != nil {
		return domain.List{}, err
	}
	value.UpdatedAt, err = domain.ParseStoredTime(updatedAt)
	if err != nil {
		return domain.List{}, err
	}
	return value, nil
}

func (s *Store) ListLists(ctx context.Context, workspace domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.ListPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ListPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ListPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.workspace_id, d.owner_id, d.name, d.description_blocks, d.schema_json, d.todo_mode, d.version, d.created_at, d.updated_at
		FROM lists d
		WHERE d.workspace_id = ? AND d.id > ?
		  AND EXISTS (SELECT 1 FROM users u WHERE u.id = ? AND u.workspace_id = d.workspace_id AND u.deleted = 0)
		  AND (d.owner_id = ? OR EXISTS (
		    SELECT 1 FROM list_access a WHERE a.list_id = d.id
		      AND ((a.entity_type = 'user' AND a.entity_id = ?)
		        OR (a.entity_type = 'channel' AND EXISTS (
		          SELECT 1 FROM conversation_members m JOIN conversations c ON c.id = m.conversation_id
		          WHERE m.conversation_id = a.entity_id AND m.user_id = ? AND c.workspace_id = d.workspace_id)))))
		ORDER BY d.id LIMIT ?`, workspace, after, userID, userID, userID, userID, request.Limit+1)
	if err != nil {
		return domain.ListPage{}, err
	}
	defer rows.Close()
	values := make([]domain.List, 0, request.Limit+1)
	for rows.Next() {
		var value domain.List
		var todo int
		var created, updated string
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.OwnerID, &value.Name, &value.DescriptionBlocks, &value.Schema, &todo, &value.Version, &created, &updated); err != nil {
			return domain.ListPage{}, err
		}
		value.TodoMode = todo != 0
		value.CreatedAt, err = domain.ParseStoredTime(created)
		if err != nil {
			return domain.ListPage{}, err
		}
		value.UpdatedAt, err = domain.ParseStoredTime(updated)
		if err != nil {
			return domain.ListPage{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.ListPage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.ListPage{Lists: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
	}
	return page, err
}

func (s *Store) UpdateList(ctx context.Context, value domain.List, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE lists SET name = ?, description_blocks = ?, schema_json = ?, todo_mode = ?, version = ?, updated_at = ? WHERE id = ? AND workspace_id = ? AND version = ?`, value.Name, value.DescriptionBlocks, value.Schema, boolInt(value.TodoMode), value.Version, domain.NewStoredTime(value.UpdatedAt), value.ID, value.WorkspaceID, value.Version-1)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lists WHERE id = ? AND workspace_id = ?)`, value.ID, value.WorkspaceID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return store.ErrConflict
		}
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateListItem(ctx context.Context, value domain.ListItem, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertListItem(ctx, tx, value); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// insertListItem is shared by the single-item creation and the bulk copy, so the
// parent check and the column list cannot drift apart between them.
func insertListItem(ctx context.Context, tx *sql.Tx, value domain.ListItem) error {
	if value.ParentItemID != "" {
		var parent string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM list_items WHERE id = ? AND list_id = ?`, value.ParentItemID, value.ListID).Scan(&parent); err != nil {
			return translateNotFound(err)
		}
	}
	if value.Version == 0 {
		value.Version = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO list_items(id, list_id, parent_item_id, workspace_id, fields, created_by, updated_by, created_at, updated_at, archived, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.ListID, value.ParentItemID, value.WorkspaceID, value.Fields, value.CreatedBy, value.UpdatedBy, domain.NewStoredTime(value.CreatedAt), domain.NewStoredTime(value.UpdatedAt), boolInt(value.Archived), value.Version); err != nil {
		return classify(err)
	}
	return nil
}

func scanListItem(scanner interface{ Scan(...any) error }) (domain.ListItem, error) {
	var value domain.ListItem
	var createdAt, updatedAt string
	var archived int
	if err := scanner.Scan(&value.ID, &value.ListID, &value.ParentItemID, &value.WorkspaceID, &value.Fields, &value.CreatedBy, &value.UpdatedBy, &createdAt, &updatedAt, &archived, &value.Version); err != nil {
		return domain.ListItem{}, err
	}
	var err error
	value.CreatedAt, err = domain.ParseStoredTime(createdAt)
	if err != nil {
		return domain.ListItem{}, err
	}
	value.UpdatedAt, err = domain.ParseStoredTime(updatedAt)
	if err != nil {
		return domain.ListItem{}, err
	}
	value.Archived = archived != 0
	return value, nil
}

func (s *Store) GetListItem(ctx context.Context, workspace domain.WorkspaceID, listID domain.ListID, id domain.ListItemID) (domain.ListItem, error) {
	value, err := scanListItem(s.db.QueryRowContext(ctx, `SELECT id, list_id, parent_item_id, workspace_id, fields, created_by, updated_by, created_at, updated_at, archived, version FROM list_items WHERE id = ? AND list_id = ? AND workspace_id = ?`, id, listID, workspace))
	if err != nil {
		return domain.ListItem{}, translateNotFound(err)
	}
	return value, nil
}

func (s *Store) ListItems(ctx context.Context, workspace domain.WorkspaceID, listID domain.ListID, request domain.PageRequest, archived bool) (domain.ListItemPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ListItemPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ListItemPage{}, err
	}
	query := `SELECT id, list_id, parent_item_id, workspace_id, fields, created_by, updated_by, created_at, updated_at, archived, version FROM list_items WHERE list_id = ? AND workspace_id = ? AND id > ?`
	args := []any{listID, workspace, after}
	if !archived {
		query += ` AND archived = 0`
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ListItemPage{}, err
	}
	defer rows.Close()
	values := make([]domain.ListItem, 0, request.Limit+1)
	for rows.Next() {
		value, err := scanListItem(rows)
		if err != nil {
			return domain.ListItemPage{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return domain.ListItemPage{}, err
	}
	hasMore := len(values) > request.Limit
	if hasMore {
		values = values[:request.Limit]
	}
	page := domain.ListItemPage{Items: values, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = domain.NewListCursor(string(values[len(values)-1].ID))
		if err != nil {
			return domain.ListItemPage{}, err
		}
	}
	return page, nil
}

func (s *Store) UpdateListItem(ctx context.Context, value domain.ListItem, event events.Event) error {
	return s.UpdateListItems(ctx, []domain.ListItem{value}, []events.Event{event})
}

func (s *Store) UpdateListItems(ctx context.Context, values []domain.ListItem, records []events.Event) error {
	if len(values) == 0 || len(values) != len(records) {
		return store.ErrInvalidArgument
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := make(map[domain.ListItemID]struct{}, len(values))
	for index, value := range values {
		if _, duplicate := seen[value.ID]; duplicate {
			return store.ErrInvalidArgument
		}
		seen[value.ID] = struct{}{}
		result, updateErr := tx.ExecContext(ctx, `UPDATE list_items SET parent_item_id = ?, fields = ?, updated_by = ?, updated_at = ?, archived = ?, version = ? WHERE id = ? AND list_id = ? AND workspace_id = ? AND version = ?`, value.ParentItemID, value.Fields, value.UpdatedBy, domain.NewStoredTime(value.UpdatedAt), boolInt(value.Archived), value.Version, value.ID, value.ListID, value.WorkspaceID, value.Version-1)
		if updateErr != nil {
			return updateErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if count != 1 {
			var exists bool
			if existsErr := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM list_items WHERE id = ? AND list_id = ? AND workspace_id = ?)`, value.ID, value.ListID, value.WorkspaceID).Scan(&exists); existsErr != nil {
				return existsErr
			}
			if exists {
				return store.ErrConflict
			}
			return store.ErrNotFound
		}
		if err := insertOutbox(ctx, tx, records[index]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) DeleteListItem(ctx context.Context, workspace domain.WorkspaceID, listID domain.ListID, id domain.ListItemID, event events.Event) error {
	return s.DeleteListItems(ctx, workspace, listID, []domain.ListItemID{id}, event)
}

func (s *Store) DeleteListItems(ctx context.Context, workspace domain.WorkspaceID, listID domain.ListID, ids []domain.ListItemID, event events.Event) error {
	if len(ids) == 0 {
		return store.InvalidArgument("list item IDs are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `DELETE FROM list_items WHERE id = ? AND list_id = ? AND workspace_id = ?`, id, listID, workspace)
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
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetListAccess(ctx context.Context, value domain.ListAccess, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO list_access(list_id, entity_type, entity_id, access_level) VALUES (?, ?, ?, ?) ON CONFLICT(list_id, entity_type, entity_id) DO UPDATE SET access_level = excluded.access_level`, value.ListID, value.EntityType, value.EntityID, value.Access); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteListAccess(ctx context.Context, value domain.ListAccess, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM list_access WHERE list_id = ? AND entity_type = ? AND entity_id = ?`, value.ListID, value.EntityType, value.EntityID)
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
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateListDownload(ctx context.Context, value domain.ListDownload, event events.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO list_downloads(id, list_id, workspace_id, status, url, include_archived, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, value.ID, value.ListID, value.WorkspaceID, value.Status, value.URL, boolInt(value.IncludeArchived), domain.NewStoredTime(value.CreatedAt)); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetListDownload(ctx context.Context, workspace domain.WorkspaceID, id domain.ListDownloadID) (domain.ListDownload, error) {
	var value domain.ListDownload
	var createdAt string
	var includeArchived int
	err := s.db.QueryRowContext(ctx, `SELECT id, list_id, workspace_id, status, url, include_archived, created_at FROM list_downloads WHERE id = ? AND workspace_id = ?`, id, workspace).Scan(&value.ID, &value.ListID, &value.WorkspaceID, &value.Status, &value.URL, &includeArchived, &createdAt)
	if err != nil {
		return domain.ListDownload{}, translateNotFound(err)
	}
	value.IncludeArchived = includeArchived != 0
	value.CreatedAt, err = domain.ParseStoredTime(createdAt)
	if err != nil {
		return domain.ListDownload{}, err
	}
	return value, nil
}

func escapeLikeTerm(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, `%`, `\%`)
	return strings.ReplaceAll(term, `_`, `\_`)
}

func (s *Store) ListThreadMessages(ctx context.Context, conversation domain.ConversationID, timestamp domain.MessageTimestamp, request domain.PageRequest) (domain.MessagePage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.MessagePage{}, err
	}
	createdAt, err := domain.ParseMessageTimestamp(timestamp)
	if err != nil {
		return domain.MessagePage{}, err
	}
	query := `SELECT id, workspace_id, conversation, author_id, app_id, text, blocks, attachments, metadata, stream_state, thread_timestamp, created_at, deleted, unfurls FROM messages WHERE conversation = ? AND deleted = 0 AND ((created_at = ? AND thread_timestamp = '') OR thread_timestamp = ?)`
	created := domain.NewStoredTime(createdAt)
	args := []any{conversation, created, string(timestamp)}
	if request.Cursor != "" {
		cursorTime, id, cursorRoot, err := domain.DecodeMessageCursorWithRoot(request.Cursor)
		if err != nil {
			return domain.MessagePage{}, err
		}
		cursorCreated := domain.NewStoredTime(cursorTime)
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
		var attachments, unfurls string
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &value.Conversation, &value.AuthorID, &value.AppID, &value.Text, &value.Blocks, &attachments, &value.Metadata, &value.StreamState, &value.ThreadTimestamp, &stored, &deleted, &unfurls); err != nil {
			return domain.MessagePage{}, err
		}
		value.CreatedAt, err = domain.ParseStoredTime(stored)
		if err != nil {
			return domain.MessagePage{}, err
		}
		value.Deleted = deleted != 0
		value.Attachments = attachments
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
	if err := s.hydrateMessageFiles(ctx, values); err != nil {
		return domain.MessagePage{}, err
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

// classify maps a driver-level constraint violation onto the repository's
// sentinel errors. Without it a foreign-key or primary-key violation escaped as
// a raw driver error, the transport's fall-through turned that into a retryable
// codes.Unavailable carrying the table and constraint name, and well-behaved
// clients retried a permanently failing request forever. It also removes the
// per-call-site string matching that only ever recognized "unique".
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	// Contention is a HANDLED condition on every engine, and it is checked before
	// the constraint classes because it is the one class whose correct treatment
	// is "the caller may try again". Returning it raw — which is what the
	// previous shape did for every code outside the four constraint classes —
	// puts a routine serialization failure or a lost dqlite leader in front of
	// the transport as an unclassified error, and AGENTS.md reserves HTTP 500 for
	// the unhandled.
	if contended(err) {
		return fmt.Errorf("%w: %w", store.ErrTransient, err)
	}
	var typed *sqlite.Error
	if errors.As(err, &typed) {
		switch typed.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return store.ErrAlreadyExists
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return store.ErrNotFound
		case sqlite3.SQLITE_CONSTRAINT_NOTNULL, sqlite3.SQLITE_CONSTRAINT_CHECK:
			return store.ErrInvalidArgument
		}
		// The driver already told us exactly what happened and it is not one of
		// the four classes, so the text of the message is not evidence of
		// anything. Falling through to a substring search here reclassified a
		// syntax error whose text quotes a UNIQUE clause as ErrAlreadyExists,
		// which reaches the client as "the resource already exists" for a query
		// that is simply broken.
		return err
	}
	// pgx reports the SQLSTATE through this method; matching the interface keeps
	// the shared repository free of a PostgreSQL driver import.
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		switch state.SQLState() {
		case "23505":
			return store.ErrAlreadyExists
		case "23503":
			return store.ErrNotFound
		case "23502", "23514":
			return store.ErrInvalidArgument
		}
		// Same reasoning: a deliberate SQLSTATE that is not one of the four —
		// 23P01 exclusion_violation, 42601 syntax_error — must not be
		// re-decided by its own English.
		return err
	}
	// Only for errors that carried NO machine-readable classification at all.
	// dqlite forwards constraint failures without a typed error, so this is the
	// signal of last resort rather than a second opinion on a typed one.
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unique"), strings.Contains(message, "primary key"):
		return store.ErrAlreadyExists
	case strings.Contains(message, "foreign key"):
		return store.ErrNotFound
	case strings.Contains(message, "not null"), strings.Contains(message, "check constraint"):
		return store.ErrInvalidArgument
	}
	return err
}

func (s *Store) listFileShares(ctx context.Context, workspace domain.WorkspaceID, id domain.FileID) ([]domain.ConversationID, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fs.conversation_id FROM file_shares fs JOIN files f ON f.id = fs.file_id WHERE fs.file_id = ? AND f.workspace_id = ? AND f.deleted = 0 ORDER BY fs.conversation_id`, id, workspace)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM files WHERE id = ? AND workspace_id = ? AND deleted = 0`, id, workspace).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		} else if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *Store) CompleteExternalUploads(ctx context.Context, completions []domain.ExternalUploadCompletion, files []domain.File, channels []domain.ConversationID, emitted []events.Event, messages []domain.Message, messageEvents []events.Event) error {
	if len(completions) == 0 || len(completions) != len(files) || len(files) != len(emitted) || len(messages) != len(messageEvents) {
		return store.ErrInvalidArgument
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seenUploads := make(map[domain.ExternalUploadID]struct{}, len(completions))
	seenFiles := make(map[domain.FileID]struct{}, len(files))
	now := domain.NewStoredTime(time.Now())
	for index, completion := range completions {
		if _, exists := seenUploads[completion.ID]; exists {
			return store.ErrInvalidArgument
		}
		if _, exists := seenFiles[files[index].ID]; exists {
			return store.ErrInvalidArgument
		}
		seenUploads[completion.ID] = struct{}{}
		seenFiles[files[index].ID] = struct{}{}
		result, err := tx.ExecContext(ctx, `UPDATE external_uploads SET status = ?, file_id = ?, completed_at = ? WHERE id = ? AND status = ? AND expires_at > ?`, domain.ExternalUploadCompleted, files[index].ID, domain.NewStoredTime(files[index].CreatedAt), completion.ID, domain.ExternalUploadUploaded, now)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return store.ErrConflict
		}
		file := files[index]
		if _, err := tx.ExecContext(ctx, `INSERT INTO files(id, workspace_id, uploader_id, name, title, mime_type, blob_key, size, created_at, deleted, name_folded, title_folded) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`, file.ID, file.WorkspaceID, file.Uploader, file.Name, file.Title, file.MIMEType, file.BlobKey, file.Size, domain.NewStoredTime(file.CreatedAt), domain.FoldSearchText(file.Name), domain.FoldSearchText(file.Title)); err != nil {
			return classify(err)
		}
		for _, channel := range channels {
			if _, err := tx.ExecContext(ctx, `INSERT INTO file_shares(file_id, conversation_id) VALUES (?, ?)`, file.ID, channel); err != nil {
				return classify(err)
			}
		}
		if err := insertOutbox(ctx, tx, emitted[index]); err != nil {
			return err
		}
	}
	for index, message := range messages {
		if err := insertFileShareMessage(ctx, tx, message, messageEvents[index]); err != nil {
			return err
		}
	}
	return tx.Commit()
}
