PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE schema_migration_lock (id INTEGER PRIMARY KEY, acquired INTEGER NOT NULL DEFAULT 0);
INSERT INTO schema_migration_lock VALUES(1,1);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations VALUES(101,'2026-08-01T19:36:19.950860000Z');
CREATE TABLE schema_backfills (name TEXT PRIMARY KEY, cursor TEXT NOT NULL DEFAULT '', done INTEGER NOT NULL DEFAULT 0, rejected INTEGER NOT NULL DEFAULT 0);
INSERT INTO schema_backfills VALUES('messages.created_at.identity','',1,0);
INSERT INTO schema_backfills VALUES('messages.text_folded','',1,0);
INSERT INTO schema_backfills VALUES('conversations.name_folded','',1,0);
INSERT INTO schema_backfills VALUES('conversations.topic_folded','',1,0);
INSERT INTO schema_backfills VALUES('conversations.purpose_folded','',1,0);
CREATE TABLE schema_migration_notices (kind TEXT NOT NULL, subject TEXT NOT NULL, detail TEXT NOT NULL, observed_at TEXT NOT NULL, PRIMARY KEY (kind, subject));
CREATE TABLE workspaces (id TEXT PRIMARY KEY, domain TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', discoverability TEXT NOT NULL DEFAULT 'open', icon_url TEXT NOT NULL DEFAULT '');
CREATE TABLE users (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 email TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, real_name TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
 status_text TEXT NOT NULL DEFAULT '', status_emoji TEXT NOT NULL DEFAULT '',
 image_24 TEXT NOT NULL DEFAULT '', image_32 TEXT NOT NULL DEFAULT '', image_48 TEXT NOT NULL DEFAULT '',
 image_72 TEXT NOT NULL DEFAULT '', image_192 TEXT NOT NULL DEFAULT '', image_512 TEXT NOT NULL DEFAULT '', image_1024 TEXT NOT NULL DEFAULT '',
 deleted INTEGER NOT NULL DEFAULT 0, presence TEXT NOT NULL DEFAULT 'auto'
);
CREATE TABLE user_expirations (user_id TEXT PRIMARY KEY REFERENCES users(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), expiration_ts INTEGER NOT NULL);
CREATE TABLE workspace_members (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 role TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE tokens (
 token_hash TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), app_id TEXT NOT NULL DEFAULT '', bot_id TEXT NOT NULL DEFAULT '',
 scopes TEXT NOT NULL, token_type TEXT NOT NULL DEFAULT 'user',
 expires_at INTEGER NOT NULL DEFAULT 0, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE sessions (
 session_hash TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL DEFAULT '', expires_at TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0,
	oidc_provider TEXT NOT NULL DEFAULT '', oidc_id_token TEXT NOT NULL DEFAULT '', oidc_subject TEXT NOT NULL DEFAULT '', oidc_sid TEXT NOT NULL DEFAULT ''
);
CREATE TABLE oidc_logout_tokens (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, token_id TEXT NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY (workspace_id, provider, token_id));
CREATE TABLE auth_methods (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, enabled INTEGER NOT NULL, PRIMARY KEY(workspace_id, provider));
CREATE TABLE external_identities (workspace_id TEXT NOT NULL REFERENCES workspaces(id), provider TEXT NOT NULL, subject TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY(workspace_id, provider, subject));
CREATE TABLE conversations (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 name TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '', purpose TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0, is_private INTEGER NOT NULL DEFAULT 0, is_direct INTEGER NOT NULL DEFAULT 0, is_group_direct INTEGER NOT NULL DEFAULT 0, direct_key TEXT NOT NULL DEFAULT '',
 name_folded TEXT NOT NULL DEFAULT '', topic_folded TEXT NOT NULL DEFAULT '', purpose_folded TEXT NOT NULL DEFAULT ''
);
CREATE TABLE workspace_default_channels (workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (workspace_id, conversation_id));
CREATE TABLE conversation_teams (conversation_id TEXT NOT NULL REFERENCES conversations(id), team_id TEXT NOT NULL REFERENCES workspaces(id), org_channel INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (conversation_id, team_id));
CREATE TABLE slack_apps (
 id TEXT PRIMARY KEY, development_workspace_id TEXT NOT NULL REFERENCES workspaces(id), owner_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', client_id TEXT NOT NULL UNIQUE, signing_secret_hash TEXT NOT NULL,
 signing_secret_ciphertext TEXT NOT NULL DEFAULT '',
 verification_token_hash TEXT NOT NULL, verification_token_ciphertext TEXT NOT NULL DEFAULT '',
 manifest_version INTEGER NOT NULL, distribution TEXT NOT NULL DEFAULT 'private',
 socket_mode_enabled INTEGER NOT NULL DEFAULT 0, token_rotation_enabled INTEGER NOT NULL DEFAULT 0,
 deleted INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE app_manifest_revisions (
 app_id TEXT NOT NULL REFERENCES slack_apps(id), version INTEGER NOT NULL, manifest TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL, PRIMARY KEY (app_id, version)
);
CREATE TABLE app_datastore_items (
 app_id TEXT NOT NULL REFERENCES slack_apps(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 datastore TEXT NOT NULL, item_id TEXT NOT NULL, item TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY (app_id, workspace_id, datastore, item_id)
);
CREATE TABLE app_event_cursors (
 app_id TEXT NOT NULL REFERENCES slack_apps(id), surface TEXT NOT NULL, sequence INTEGER NOT NULL DEFAULT 0,
 leased_sequence INTEGER NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0, retry_count INTEGER NOT NULL DEFAULT 0, retry_reason TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (app_id, surface)
);
CREATE TABLE app_triggers (
 token_hash TEXT PRIMARY KEY, app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
 consumed_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE app_response_urls (
 token_hash TEXT PRIMARY KEY, app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 original_message_id TEXT NOT NULL DEFAULT '', thread_timestamp TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, uses_remaining INTEGER NOT NULL
);
CREATE TABLE app_configuration_tokens (
 access_hash TEXT PRIMARY KEY, refresh_hash TEXT NOT NULL UNIQUE, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), expires_at TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE oauth_clients (id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL, app_id TEXT NOT NULL);
CREATE TABLE oauth_codes (code TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES oauth_clients(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, bot_id TEXT NOT NULL DEFAULT '', bot_user_id TEXT NOT NULL DEFAULT '', bot_scopes TEXT NOT NULL DEFAULT '[]', user_scopes TEXT NOT NULL DEFAULT '[]', redirect_uri TEXT NOT NULL DEFAULT '', code_challenge TEXT NOT NULL DEFAULT '', code_challenge_method TEXT NOT NULL DEFAULT '', expires_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE oauth_refresh_tokens (
 refresh_hash TEXT PRIMARY KEY, access_hash TEXT NOT NULL, client_id TEXT NOT NULL REFERENCES oauth_clients(id),
 app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 installer_id TEXT NOT NULL REFERENCES users(id), bot_id TEXT NOT NULL DEFAULT '', scopes TEXT NOT NULL, token_type TEXT NOT NULL,
 access_expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE openid_refresh_tokens (token_hash TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES oauth_clients(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE rtm_connections (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), expires_at INTEGER NOT NULL);
CREATE TABLE app_tokens (token_hash TEXT PRIMARY KEY, app_id TEXT NOT NULL, scopes TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE socket_mode_connections (id TEXT PRIMARY KEY, app_id TEXT NOT NULL, expires_at INTEGER NOT NULL, consumed_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE socket_mode_admission (app_id TEXT PRIMARY KEY, ticket INTEGER NOT NULL DEFAULT 0);
CREATE TABLE socket_mode_cursors (app_id TEXT PRIMARY KEY, sequence INTEGER NOT NULL DEFAULT 0);
CREATE TABLE socket_mode_responses (app_id TEXT NOT NULL, envelope_id TEXT NOT NULL, payload TEXT NOT NULL, received_at INTEGER NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at INTEGER NOT NULL DEFAULT 0, acknowledged_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (app_id, envelope_id));
CREATE TABLE socket_mode_interactions (
 envelope_id TEXT PRIMARY KEY, app_id TEXT NOT NULL REFERENCES slack_apps(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), type TEXT NOT NULL, payload TEXT NOT NULL,
 response_token_hash TEXT NOT NULL REFERENCES app_response_urls(token_hash), created_at INTEGER NOT NULL,
 lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0, retry_count INTEGER NOT NULL DEFAULT 0, retry_reason TEXT NOT NULL DEFAULT '',
 acknowledged_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE conversation_prefs (
 conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),
 can_thread_types TEXT NOT NULL DEFAULT '[]', can_thread_users TEXT NOT NULL DEFAULT '[]',
 who_can_post_types TEXT NOT NULL DEFAULT '[]', who_can_post_users TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE invite_requests (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), email TEXT NOT NULL, requested_by TEXT NOT NULL REFERENCES users(id), channel_ids TEXT NOT NULL DEFAULT '[]', custom_message TEXT NOT NULL DEFAULT '', real_name TEXT NOT NULL DEFAULT '', resend INTEGER NOT NULL DEFAULT 0, restricted INTEGER NOT NULL DEFAULT 0, ultra_restricted INTEGER NOT NULL DEFAULT 0, guest_expiration_at INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, created_at INTEGER NOT NULL, reviewed_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE app_approvals (app_id TEXT PRIMARY KEY, request_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), status TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE app_installations (app_id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, PRIMARY KEY (app_id, workspace_id));
CREATE TABLE incoming_webhooks (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), app_id TEXT NOT NULL, conversation_id TEXT NOT NULL REFERENCES conversations(id), user_id TEXT NOT NULL REFERENCES users(id), secret_hash TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL);
CREATE TABLE app_permission_requests (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), requester_id TEXT NOT NULL REFERENCES users(id), target_user_id TEXT NOT NULL REFERENCES users(id), scopes TEXT NOT NULL, trigger_id TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE views (id TEXT PRIMARY KEY, app_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), type TEXT NOT NULL, external_id TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL, state TEXT NOT NULL DEFAULT '', errors TEXT NOT NULL DEFAULT '{}', hash TEXT NOT NULL, root_view_id TEXT NOT NULL, previous_view_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE workflow_steps (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), edit_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, inputs TEXT NOT NULL DEFAULT '{}', outputs TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '', step_name TEXT NOT NULL DEFAULT '', image_url TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE dialogs (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id), payload TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE bots (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), app_id TEXT NOT NULL DEFAULT '', user_id TEXT NOT NULL REFERENCES users(id), name TEXT NOT NULL, image_36 TEXT NOT NULL DEFAULT '', image_48 TEXT NOT NULL DEFAULT '', image_72 TEXT NOT NULL DEFAULT '', deleted INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL);
CREATE TABLE user_migrations (workspace_id TEXT NOT NULL REFERENCES workspaces(id), old_id TEXT NOT NULL, global_id TEXT NOT NULL, PRIMARY KEY (workspace_id, old_id), UNIQUE (workspace_id, global_id));
CREATE TABLE conversation_members (
 conversation_id TEXT NOT NULL REFERENCES conversations(id),
 user_id TEXT NOT NULL REFERENCES users(id),
 PRIMARY KEY (conversation_id, user_id)
);
CREATE TABLE messages (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	conversation TEXT NOT NULL REFERENCES conversations(id), author_id TEXT NOT NULL REFERENCES users(id),
	app_id TEXT NOT NULL DEFAULT '', text TEXT NOT NULL, blocks TEXT NOT NULL DEFAULT '', attachments TEXT NOT NULL DEFAULT '[]',
	metadata TEXT NOT NULL DEFAULT '', stream_state TEXT NOT NULL DEFAULT '', thread_timestamp TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, unfurls TEXT NOT NULL DEFAULT '{}',
	text_folded TEXT NOT NULL DEFAULT ''
);
CREATE TABLE ephemeral_messages (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 conversation_id TEXT NOT NULL REFERENCES conversations(id), author_id TEXT NOT NULL REFERENCES users(id),
 app_id TEXT NOT NULL DEFAULT '', recipient_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL,
 blocks TEXT NOT NULL DEFAULT '', attachments TEXT NOT NULL DEFAULT '[]', timestamp TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE reactions (
 message_id TEXT NOT NULL REFERENCES messages(id), name TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL,
 PRIMARY KEY (message_id, name, user_id)
);
CREATE TABLE pins (
 message_id TEXT NOT NULL REFERENCES messages(id), user_id TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL,
 PRIMARY KEY (message_id, user_id)
);
CREATE TABLE files (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), uploader_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, title TEXT NOT NULL, mime_type TEXT NOT NULL, blob_key TEXT NOT NULL UNIQUE,
 size INTEGER NOT NULL, created_at TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, public_token TEXT NOT NULL DEFAULT ''
);
CREATE TABLE external_uploads (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), uploader_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, title TEXT NOT NULL, mime_type TEXT NOT NULL, blob_key TEXT NOT NULL UNIQUE, size INTEGER NOT NULL,
 status TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL, uploaded_at TEXT NOT NULL DEFAULT '', completed_at TEXT NOT NULL DEFAULT ''
, file_id TEXT NOT NULL DEFAULT '');
CREATE TABLE file_comments (
 id TEXT PRIMARY KEY, file_id TEXT NOT NULL REFERENCES files(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 user_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, created_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE remote_files (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), external_id TEXT NOT NULL,
 title TEXT NOT NULL, file_type TEXT NOT NULL DEFAULT '', external_url TEXT NOT NULL,
 preview_image TEXT NOT NULL DEFAULT '', indexable_contents TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE remote_file_shares (
 remote_file_id TEXT NOT NULL REFERENCES remote_files(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 PRIMARY KEY (remote_file_id, conversation_id)
);
CREATE TABLE outbox (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, workspace_id TEXT NOT NULL, topic TEXT NOT NULL,
 actor_id TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL, created_at TEXT NOT NULL, delivered INTEGER NOT NULL DEFAULT 0,
 lease_owner TEXT NOT NULL DEFAULT '', lease_until TEXT NOT NULL DEFAULT '', next_attempt_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE access_logs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 username TEXT NOT NULL, created_at INTEGER NOT NULL, ip TEXT NOT NULL, user_agent TEXT NOT NULL
);
CREATE TABLE lifecycle_state (
 id INTEGER PRIMARY KEY CHECK(id = 1), state TEXT NOT NULL, generation INTEGER NOT NULL,
 wake_deadline TEXT NOT NULL DEFAULT ''
);
INSERT INTO lifecycle_state VALUES(1,'hibernated',0,'');
CREATE TABLE idempotency (
 workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 message_id TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY (workspace_id, user_id, idempotency_key)
);
CREATE TABLE read_cursors (
 workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, conversation_id TEXT NOT NULL,
 last_read TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY (workspace_id, user_id, conversation_id)
);
CREATE TABLE do_not_disturb (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 enabled INTEGER NOT NULL DEFAULT 0, snooze_until INTEGER NOT NULL DEFAULT 0,
 next_start_at INTEGER NOT NULL DEFAULT 0, next_end_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE stars (
 user_id TEXT NOT NULL REFERENCES users(id), message_id TEXT NOT NULL REFERENCES messages(id), created_at TEXT NOT NULL,
 PRIMARY KEY (user_id, message_id)
);
CREATE TABLE bookmarks (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 title TEXT NOT NULL, type TEXT NOT NULL, link TEXT NOT NULL DEFAULT '', emoji TEXT NOT NULL DEFAULT '', entity_id TEXT NOT NULL DEFAULT '',
 access_level TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 updated_by TEXT NOT NULL REFERENCES users(id)
);
CREATE TABLE reminders (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), creator_id TEXT NOT NULL REFERENCES users(id),
 user_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, due_at INTEGER NOT NULL, complete_at INTEGER NOT NULL DEFAULT 0,
 recurring INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE scheduled_messages (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), channel_id TEXT NOT NULL REFERENCES conversations(id),
 author_id TEXT NOT NULL REFERENCES users(id), text TEXT NOT NULL, blocks TEXT NOT NULL DEFAULT '', post_at INTEGER NOT NULL, created_at INTEGER NOT NULL,
 delivered INTEGER NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL DEFAULT 0
, attachments TEXT NOT NULL DEFAULT '[]');
CREATE TABLE user_groups (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), name TEXT NOT NULL, handle TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '', creator_id TEXT NOT NULL REFERENCES users(id), updated_by TEXT NOT NULL REFERENCES users(id),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE conversation_access_groups (conversation_id TEXT NOT NULL REFERENCES conversations(id), group_id TEXT NOT NULL REFERENCES user_groups(id), PRIMARY KEY (conversation_id, group_id));
CREATE TABLE user_group_users (
 group_id TEXT NOT NULL REFERENCES user_groups(id), user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY (group_id, user_id)
);
CREATE TABLE user_group_channels (
 group_id TEXT NOT NULL REFERENCES user_groups(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (group_id, conversation_id)
);
CREATE TABLE calls (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), external_unique_id TEXT NOT NULL,
 external_display_id TEXT NOT NULL DEFAULT '', join_url TEXT NOT NULL, desktop_app_join_url TEXT NOT NULL DEFAULT '',
 title TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL REFERENCES users(id), started_at INTEGER NOT NULL,
 ended_at INTEGER NOT NULL DEFAULT 0, duration_seconds INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE call_participants (
 call_id TEXT NOT NULL REFERENCES calls(id), user_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY (call_id, user_id)
);
CREATE TABLE custom_emoji (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), name TEXT NOT NULL, url TEXT NOT NULL DEFAULT '', alias_for TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (workspace_id, name)
);
CREATE TABLE lists (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), owner_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, description_blocks TEXT NOT NULL DEFAULT '[]', schema_json TEXT NOT NULL DEFAULT '[]', todo_mode INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE list_items (
 id TEXT PRIMARY KEY, list_id TEXT NOT NULL REFERENCES lists(id), parent_item_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 fields TEXT NOT NULL DEFAULT '[]', created_by TEXT NOT NULL REFERENCES users(id), updated_by TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE list_access (
 list_id TEXT NOT NULL REFERENCES lists(id), entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, access_level TEXT NOT NULL,
 PRIMARY KEY (list_id, entity_type, entity_id)
);
CREATE TABLE list_downloads (
 id TEXT PRIMARY KEY, list_id TEXT NOT NULL REFERENCES lists(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 status TEXT NOT NULL, url TEXT NOT NULL DEFAULT '', include_archived INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE TABLE canvases (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), owner_id TEXT NOT NULL REFERENCES users(id), title TEXT NOT NULL, document_content TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE canvas_access (canvas_id TEXT NOT NULL REFERENCES canvases(id), entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, access_level TEXT NOT NULL, PRIMARY KEY (canvas_id, entity_type, entity_id));
CREATE TABLE file_shares (file_id TEXT NOT NULL REFERENCES files(id), conversation_id TEXT NOT NULL REFERENCES conversations(id), PRIMARY KEY (file_id, conversation_id));
CREATE TABLE message_files (
			message_id TEXT NOT NULL REFERENCES messages(id), file_id TEXT NOT NULL REFERENCES files(id), position INTEGER NOT NULL,
			PRIMARY KEY (message_id, file_id), UNIQUE (message_id, position)
		);
DELETE FROM sqlite_sequence;
CREATE INDEX slack_apps_owner ON slack_apps(development_workspace_id, owner_id, deleted, name, id);
CREATE INDEX app_datastore_items_lookup ON app_datastore_items(app_id, workspace_id, datastore, item_id);
CREATE INDEX oauth_refresh_identity ON oauth_refresh_tokens(client_id, workspace_id, user_id, bot_id, token_type, created_at DESC);
CREATE INDEX socket_mode_interactions_claim ON socket_mode_interactions(app_id, acknowledged_at, retry_at, lease_expires_at, created_at, envelope_id);
CREATE INDEX incoming_webhooks_lookup ON incoming_webhooks(workspace_id, app_id, secret_hash, enabled);
CREATE INDEX views_published_user_app ON views(workspace_id, user_id, app_id, type, updated_at);
CREATE INDEX app_approvals_workspace_status ON app_approvals(workspace_id, status, app_id);
CREATE INDEX messages_conversation_created ON messages(conversation, created_at, id);
CREATE INDEX ephemeral_messages_recipient_conversation_created ON ephemeral_messages(workspace_id, recipient_id, conversation_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX remote_files_workspace_external ON remote_files(workspace_id, external_id);
CREATE INDEX files_workspace_id ON files(workspace_id, id);
CREATE INDEX access_logs_workspace_created ON access_logs(workspace_id, created_at DESC, id DESC);
CREATE INDEX stars_user_created ON stars(user_id, created_at, message_id);
CREATE INDEX bookmarks_conversation_rank ON bookmarks(workspace_id, conversation_id, created_at, id);
CREATE INDEX reminders_user_due ON reminders(workspace_id, user_id, due_at, id);
CREATE INDEX scheduled_messages_owner ON scheduled_messages(workspace_id, author_id, id);
CREATE UNIQUE INDEX user_groups_workspace_handle ON user_groups(workspace_id, handle);
CREATE UNIQUE INDEX calls_workspace_external ON calls(workspace_id, external_unique_id);
CREATE INDEX list_items_list_id ON list_items(list_id, id);
CREATE UNIQUE INDEX conversations_direct_key ON conversations(direct_key) WHERE direct_key <> '';
CREATE UNIQUE INDEX files_public_token ON files(public_token) WHERE public_token <> '';
CREATE INDEX invite_requests_workspace_status ON invite_requests(workspace_id, status, id);
CREATE INDEX file_comments_file ON file_comments(file_id, id);
CREATE UNIQUE INDEX views_workspace_external ON views(workspace_id, external_id) WHERE external_id <> '';
CREATE INDEX canvases_workspace_updated ON canvases(workspace_id, updated_at, id);
CREATE UNIQUE INDEX users_workspace_email_normalized ON users(workspace_id, email) WHERE email <> '';
CREATE UNIQUE INDEX conversations_workspace_name ON conversations(workspace_id, name) WHERE is_direct = 0 AND is_group_direct = 0;
CREATE INDEX message_files_file ON message_files(file_id, message_id);
CREATE UNIQUE INDEX messages_conversation_created_unique ON messages(conversation, created_at);
COMMIT;
