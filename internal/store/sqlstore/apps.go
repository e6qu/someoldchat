package sqlstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (s *Store) CreateOAuthAuthorization(ctx context.Context, botUser domain.User, bot domain.Bot, code domain.OAuthCode) error {
	if code.Code == "" || code.ClientID == "" || code.WorkspaceID == "" || code.UserID == "" {
		return store.InvalidArgument("invalid oauth authorization")
	}
	code.BotScopes = domain.NormalizeScopes(code.BotScopes)
	code.UserScopes = domain.NormalizeScopes(code.UserScopes)
	code.Scopes = domain.NormalizeScopes(code.Scopes)
	withBot := len(code.BotScopes) != 0
	if withBot && (botUser.ID == "" || botUser.WorkspaceID != code.WorkspaceID || bot.ID == "" || bot.WorkspaceID != code.WorkspaceID || bot.UserID != botUser.ID || bot.UpdatedAt.IsZero() || strings.TrimSpace(bot.Name) == "") {
		return store.InvalidArgument("invalid oauth bot authorization")
	}
	if !withBot && (botUser.ID != "" || bot.ID != "" || code.BotID != "" || code.BotUserID != "") {
		return store.InvalidArgument("unexpected oauth bot authorization")
	}
	scopes, err := json.Marshal(code.Scopes)
	if err != nil {
		return err
	}
	botScopes, err := json.Marshal(code.BotScopes)
	if err != nil {
		return err
	}
	userScopes, err := json.Marshal(code.UserScopes)
	if err != nil {
		return err
	}
	return underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var appID domain.AppID
		if err := tx.QueryRowContext(ctx, `SELECT c.app_id FROM oauth_clients c JOIN slack_apps a ON a.id = c.app_id AND a.deleted = 0 WHERE c.id = ?`, code.ClientID).Scan(&appID); err != nil {
			return translateNotFound(err)
		}
		var actorCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u JOIN workspace_members m ON m.workspace_id = u.workspace_id AND m.user_id = u.id WHERE u.id = ? AND u.workspace_id = ? AND u.deleted = 0 AND m.active = 1`, code.UserID, code.WorkspaceID).Scan(&actorCount); err != nil {
			return err
		}
		if actorCount != 1 {
			return store.ErrNotFound
		}
		if withBot {
			if bot.AppID != appID || code.BotID != bot.ID || code.BotUserID != botUser.ID {
				return store.InvalidArgument("oauth bot does not match client")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, workspace_id, name, real_name, deleted, presence) VALUES (?, ?, ?, ?, 0, 'auto')`, botUser.ID, botUser.WorkspaceID, botUser.Name, botUser.RealName); err != nil {
				return classify(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_members(workspace_id, user_id, role, active) VALUES (?, ?, 'member', 1)`, botUser.WorkspaceID, botUser.ID); err != nil {
				return classify(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO bots(id, workspace_id, app_id, user_id, name, image_36, image_48, image_72, deleted, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`, bot.ID, bot.WorkspaceID, bot.AppID, bot.UserID, bot.Name, bot.Image36, bot.Image48, bot.Image72, bot.UpdatedAt.UTC().Unix()); err != nil {
				return classify(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_codes(code, client_id, workspace_id, user_id, scopes, bot_id, bot_user_id, bot_scopes, user_scopes, redirect_uri, code_challenge, code_challenge_method, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, domain.HashToken(code.Code), code.ClientID, code.WorkspaceID, code.UserID, string(scopes), code.BotID, code.BotUserID, string(botScopes), string(userScopes), code.RedirectURI, code.CodeChallenge, code.CodeChallengeMethod, time.Now().UTC().Add(store.OAuthCodeLifetime).UnixNano()); err != nil {
			return classify(err)
		}
		return tx.Commit()
	})
}

func (s *Store) CreateAppConfigurationToken(ctx context.Context, accessToken, refreshToken string, value domain.AppConfigurationToken) error {
	if strings.TrimSpace(accessToken) == "" || strings.TrimSpace(refreshToken) == "" || value.WorkspaceID == "" || value.UserID == "" || !value.ExpiresAt.After(time.Now().UTC()) {
		return store.InvalidArgument("invalid app configuration token")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO app_configuration_tokens(access_hash, refresh_hash, workspace_id, user_id, expires_at, revoked) SELECT ?, ?, ?, ?, ?, 0 WHERE EXISTS (SELECT 1 FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0)`,
		domain.HashToken(accessToken), domain.HashToken(refreshToken), value.WorkspaceID, value.UserID, domain.NewStoredTime(value.ExpiresAt), value.UserID, value.WorkspaceID)
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

func (s *Store) LookupAppConfigurationToken(ctx context.Context, accessToken string) (domain.AppConfigurationToken, error) {
	var value domain.AppConfigurationToken
	var expires string
	var revoked int
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, user_id, expires_at, revoked FROM app_configuration_tokens WHERE access_hash = ?`, domain.HashToken(accessToken)).Scan(&value.WorkspaceID, &value.UserID, &expires, &revoked)
	if err := translateNotFound(err); err != nil {
		return domain.AppConfigurationToken{}, err
	}
	value.ExpiresAt, err = domain.ParseStoredTime(expires)
	if err != nil {
		return domain.AppConfigurationToken{}, err
	}
	value.Revoked = revoked != 0
	if value.Revoked || !value.ExpiresAt.After(time.Now().UTC()) {
		return domain.AppConfigurationToken{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) LookupAppConfigurationRefreshToken(ctx context.Context, refreshToken string) (domain.AppConfigurationToken, error) {
	var value domain.AppConfigurationToken
	var expires string
	var revoked int
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, user_id, expires_at, revoked FROM app_configuration_tokens WHERE refresh_hash = ?`, domain.HashToken(refreshToken)).Scan(&value.WorkspaceID, &value.UserID, &expires, &revoked)
	if err := translateNotFound(err); err != nil {
		return domain.AppConfigurationToken{}, err
	}
	value.ExpiresAt, err = domain.ParseStoredTime(expires)
	if err != nil {
		return domain.AppConfigurationToken{}, err
	}
	value.Revoked = revoked != 0
	if value.Revoked {
		return domain.AppConfigurationToken{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) RotateAppConfigurationToken(ctx context.Context, oldRefreshToken, nextAccessToken, nextRefreshToken string, next domain.AppConfigurationToken) error {
	if strings.TrimSpace(oldRefreshToken) == "" || strings.TrimSpace(nextAccessToken) == "" || strings.TrimSpace(nextRefreshToken) == "" || next.WorkspaceID == "" || next.UserID == "" || !next.ExpiresAt.After(time.Now().UTC()) {
		return store.InvalidArgument("invalid app configuration token rotation")
	}
	return underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var workspaceID domain.WorkspaceID
		var userID domain.UserID
		var revoked int
		err = tx.QueryRowContext(ctx, `SELECT workspace_id, user_id, revoked FROM app_configuration_tokens WHERE refresh_hash = ?`, domain.HashToken(oldRefreshToken)).Scan(&workspaceID, &userID, &revoked)
		if err := translateNotFound(err); err != nil {
			return err
		}
		if revoked != 0 || workspaceID != next.WorkspaceID || userID != next.UserID {
			return store.ErrNotFound
		}
		result, err := tx.ExecContext(ctx, `UPDATE app_configuration_tokens SET access_hash = ?, refresh_hash = ?, expires_at = ?, revoked = 0 WHERE refresh_hash = ? AND revoked = 0`,
			domain.HashToken(nextAccessToken), domain.HashToken(nextRefreshToken), domain.NewStoredTime(next.ExpiresAt), domain.HashToken(oldRefreshToken))
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
		return tx.Commit()
	})
}

func (s *Store) CreateApp(ctx context.Context, app domain.App, revision domain.AppManifestRevision, client domain.OAuthClient) error {
	if app.ID == "" || app.DevelopmentWorkspaceID == "" || app.OwnerID == "" || strings.TrimSpace(app.Name) == "" || app.ClientID == "" || app.SigningSecretHash == "" || app.SigningSecretCiphertext == "" || app.VerificationTokenCiphertext == "" || app.ManifestVersion != 1 || app.CreatedAt.IsZero() || app.UpdatedAt.IsZero() || revision.AppID != app.ID || revision.Version != 1 || revision.CreatedBy != app.OwnerID || strings.TrimSpace(revision.Manifest) == "" || client.ID != app.ClientID || client.AppID != app.ID || client.SecretHash == "" {
		return store.InvalidArgument("invalid app")
	}
	return underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `INSERT INTO slack_apps(id, development_workspace_id, owner_id, name, description, client_id, signing_secret_hash, signing_secret_ciphertext, verification_token_hash, verification_token_ciphertext, manifest_version, distribution, socket_mode_enabled, token_rotation_enabled, deleted, created_at, updated_at)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ? WHERE EXISTS (SELECT 1 FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0)`,
			app.ID, app.DevelopmentWorkspaceID, app.OwnerID, app.Name, app.Description, app.ClientID, app.SigningSecretHash, app.SigningSecretCiphertext, app.VerificationTokenHash, app.VerificationTokenCiphertext, app.ManifestVersion, app.Distribution, boolInt(app.SocketModeEnabled), boolInt(app.TokenRotationEnabled), domain.NewStoredTime(app.CreatedAt), domain.NewStoredTime(app.UpdatedAt), app.OwnerID, app.DevelopmentWorkspaceID)
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_manifest_revisions(app_id, version, manifest, created_by, created_at) VALUES (?, ?, ?, ?, ?)`, revision.AppID, revision.Version, revision.Manifest, revision.CreatedBy, domain.NewStoredTime(revision.CreatedAt)); err != nil {
			return classify(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_clients(id, secret_hash, app_id) VALUES (?, ?, ?)`, client.ID, client.SecretHash, client.AppID); err != nil {
			return classify(err)
		}
		return tx.Commit()
	})
}

func (s *Store) GetApp(ctx context.Context, appID domain.AppID) (domain.App, domain.AppManifestRevision, error) {
	return s.getApp(ctx, `a.id = ?`, appID)
}

func (s *Store) GetAppByClientID(ctx context.Context, clientID string) (domain.App, domain.AppManifestRevision, error) {
	return s.getApp(ctx, `a.client_id = ?`, strings.TrimSpace(clientID))
}

func (s *Store) getApp(ctx context.Context, predicate string, argument any) (domain.App, domain.AppManifestRevision, error) {
	var app domain.App
	var revision domain.AppManifestRevision
	var socketMode, tokenRotation, deleted int
	var appCreated, appUpdated, revisionCreated string
	err := s.db.QueryRowContext(ctx, `SELECT a.id, a.development_workspace_id, a.owner_id, a.name, a.description, a.client_id, a.signing_secret_hash, a.signing_secret_ciphertext, a.verification_token_hash, a.verification_token_ciphertext, a.manifest_version, a.distribution, a.socket_mode_enabled, a.token_rotation_enabled, a.deleted, a.created_at, a.updated_at, r.app_id, r.version, r.manifest, r.created_by, r.created_at
		FROM slack_apps a JOIN app_manifest_revisions r ON r.app_id = a.id AND r.version = a.manifest_version WHERE `+predicate+` AND a.deleted = 0`, argument).
		Scan(&app.ID, &app.DevelopmentWorkspaceID, &app.OwnerID, &app.Name, &app.Description, &app.ClientID, &app.SigningSecretHash, &app.SigningSecretCiphertext, &app.VerificationTokenHash, &app.VerificationTokenCiphertext, &app.ManifestVersion, &app.Distribution, &socketMode, &tokenRotation, &deleted, &appCreated, &appUpdated, &revision.AppID, &revision.Version, &revision.Manifest, &revision.CreatedBy, &revisionCreated)
	if err := translateNotFound(err); err != nil {
		return domain.App{}, domain.AppManifestRevision{}, err
	}
	app.SocketModeEnabled = socketMode != 0
	app.TokenRotationEnabled = tokenRotation != 0
	app.Deleted = deleted != 0
	for encoded, target := range map[string]*time.Time{appCreated: &app.CreatedAt, appUpdated: &app.UpdatedAt, revisionCreated: &revision.CreatedAt} {
		parsed, err := domain.ParseStoredTime(encoded)
		if err != nil {
			return domain.App{}, domain.AppManifestRevision{}, err
		}
		*target = parsed
	}
	return app, revision, nil
}

func (s *Store) ListDeveloperApps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.App, error) {
	if workspaceID == "" || userID == "" {
		return nil, store.InvalidArgument("developer workspace and user are required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, development_workspace_id, owner_id, name, description, client_id, signing_secret_hash, signing_secret_ciphertext, verification_token_hash, verification_token_ciphertext, manifest_version, distribution, socket_mode_enabled, token_rotation_enabled, deleted, created_at, updated_at
		FROM slack_apps WHERE development_workspace_id = ? AND owner_id = ? AND deleted = 0 ORDER BY LOWER(name), id`, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.App, 0)
	for rows.Next() {
		var app domain.App
		var socketMode, tokenRotation, deleted int
		var created, updated string
		if err := rows.Scan(&app.ID, &app.DevelopmentWorkspaceID, &app.OwnerID, &app.Name, &app.Description, &app.ClientID, &app.SigningSecretHash, &app.SigningSecretCiphertext, &app.VerificationTokenHash, &app.VerificationTokenCiphertext, &app.ManifestVersion, &app.Distribution, &socketMode, &tokenRotation, &deleted, &created, &updated); err != nil {
			return nil, err
		}
		app.SocketModeEnabled = socketMode != 0
		app.TokenRotationEnabled = tokenRotation != 0
		app.Deleted = deleted != 0
		app.CreatedAt, err = domain.ParseStoredTime(created)
		if err == nil {
			app.UpdatedAt, err = domain.ParseStoredTime(updated)
		}
		if err != nil {
			return nil, err
		}
		values = append(values, app)
	}
	return values, rows.Err()
}

func (s *Store) ListInstalledApps(ctx context.Context) ([]domain.AppManifestSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT a.id, a.development_workspace_id, a.owner_id, a.name, a.description, a.client_id, a.signing_secret_hash, a.signing_secret_ciphertext, a.verification_token_hash, a.verification_token_ciphertext, a.manifest_version, a.distribution, a.socket_mode_enabled, a.token_rotation_enabled, a.deleted, a.created_at, a.updated_at, r.manifest
		FROM slack_apps a
		JOIN app_installations i ON i.app_id = a.id AND i.enabled = 1
		JOIN app_manifest_revisions r ON r.app_id = a.id AND r.version = a.manifest_version
		WHERE a.deleted = 0 ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.AppManifestSnapshot, 0)
	for rows.Next() {
		var snapshot domain.AppManifestSnapshot
		var socketMode, tokenRotation, deleted int
		var created, updated string
		if err := rows.Scan(&snapshot.App.ID, &snapshot.App.DevelopmentWorkspaceID, &snapshot.App.OwnerID, &snapshot.App.Name, &snapshot.App.Description, &snapshot.App.ClientID, &snapshot.App.SigningSecretHash, &snapshot.App.SigningSecretCiphertext, &snapshot.App.VerificationTokenHash, &snapshot.App.VerificationTokenCiphertext, &snapshot.App.ManifestVersion, &snapshot.App.Distribution, &socketMode, &tokenRotation, &deleted, &created, &updated, &snapshot.Manifest); err != nil {
			return nil, err
		}
		snapshot.App.SocketModeEnabled = socketMode != 0
		snapshot.App.TokenRotationEnabled = tokenRotation != 0
		snapshot.App.Deleted = deleted != 0
		snapshot.App.CreatedAt, err = domain.ParseStoredTime(created)
		if err == nil {
			snapshot.App.UpdatedAt, err = domain.ParseStoredTime(updated)
		}
		if err != nil {
			return nil, err
		}
		values = append(values, snapshot)
	}
	return values, rows.Err()
}

func (s *Store) CreateAppInteractionCapabilities(ctx context.Context, trigger domain.AppTrigger, response domain.AppResponseURL) error {
	if trigger.TokenHash == "" || response.TokenHash == "" || trigger.AppID == "" || trigger.WorkspaceID == "" || trigger.UserID == "" ||
		response.AppID != trigger.AppID || response.WorkspaceID != trigger.WorkspaceID || response.UserID != trigger.UserID ||
		trigger.CreatedAt.IsZero() || !trigger.ExpiresAt.After(trigger.CreatedAt) || response.CreatedAt.IsZero() ||
		!response.ExpiresAt.After(response.CreatedAt) || response.ConversationID == "" || response.UsesRemaining != 5 {
		return store.InvalidArgument("invalid application interaction capabilities")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_triggers(token_hash, app_id, workspace_id, user_id, created_at, expires_at, consumed_at) VALUES (?, ?, ?, ?, ?, ?, 0)`,
		trigger.TokenHash, trigger.AppID, trigger.WorkspaceID, trigger.UserID, trigger.CreatedAt.UTC().UnixNano(), trigger.ExpiresAt.UTC().UnixNano()); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_response_urls(token_hash, app_id, workspace_id, user_id, conversation_id, original_message_id, thread_timestamp, created_at, expires_at, uses_remaining) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		response.TokenHash, response.AppID, response.WorkspaceID, response.UserID, response.ConversationID, response.OriginalMessageID, response.ThreadTimestamp, response.CreatedAt.UTC().UnixNano(), response.ExpiresAt.UTC().UnixNano(), response.UsesRemaining); err != nil {
		return classify(err)
	}
	return tx.Commit()
}

func (s *Store) CreateAppTrigger(ctx context.Context, trigger domain.AppTrigger) error {
	if trigger.TokenHash == "" || trigger.AppID == "" || trigger.WorkspaceID == "" || trigger.UserID == "" ||
		trigger.CreatedAt.IsZero() || !trigger.ExpiresAt.After(trigger.CreatedAt) {
		return store.InvalidArgument("invalid application trigger")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_triggers(token_hash, app_id, workspace_id, user_id, created_at, expires_at, consumed_at) VALUES (?, ?, ?, ?, ?, ?, 0)`,
		trigger.TokenHash, trigger.AppID, trigger.WorkspaceID, trigger.UserID, trigger.CreatedAt.UTC().UnixNano(), trigger.ExpiresAt.UTC().UnixNano())
	return classify(err)
}

func (s *Store) ConsumeAppTrigger(ctx context.Context, tokenHash string, appID domain.AppID) (domain.AppTrigger, error) {
	var value domain.AppTrigger
	err := underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var createdAt, expiresAt, consumedAt int64
		if err := tx.QueryRowContext(ctx, `SELECT token_hash, app_id, workspace_id, user_id, created_at, expires_at, consumed_at FROM app_triggers WHERE token_hash = ? AND app_id = ?`, tokenHash, appID).
			Scan(&value.TokenHash, &value.AppID, &value.WorkspaceID, &value.UserID, &createdAt, &expiresAt, &consumedAt); err != nil {
			return translateNotFound(err)
		}
		now := time.Now().UTC()
		if consumedAt != 0 || expiresAt <= now.UnixNano() {
			return store.ErrNotFound
		}
		result, err := tx.ExecContext(ctx, `UPDATE app_triggers SET consumed_at = ? WHERE token_hash = ? AND app_id = ? AND consumed_at = 0 AND expires_at > ?`, now.UnixNano(), tokenHash, appID, now.UnixNano())
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
		value.CreatedAt = time.Unix(0, createdAt).UTC()
		value.ExpiresAt = time.Unix(0, expiresAt).UTC()
		value.ConsumedAt = now
		return tx.Commit()
	})
	if err != nil {
		return domain.AppTrigger{}, err
	}
	return value, nil
}

func (s *Store) UseAppResponseURL(ctx context.Context, tokenHash string) (domain.AppResponseURL, error) {
	var value domain.AppResponseURL
	err := underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var createdAt, expiresAt int64
		if err := tx.QueryRowContext(ctx, `SELECT token_hash, app_id, workspace_id, user_id, conversation_id, original_message_id, thread_timestamp, created_at, expires_at, uses_remaining FROM app_response_urls WHERE token_hash = ?`, tokenHash).
			Scan(&value.TokenHash, &value.AppID, &value.WorkspaceID, &value.UserID, &value.ConversationID, &value.OriginalMessageID, &value.ThreadTimestamp, &createdAt, &expiresAt, &value.UsesRemaining); err != nil {
			return translateNotFound(err)
		}
		now := time.Now().UTC().UnixNano()
		if value.UsesRemaining <= 0 || expiresAt <= now {
			return store.ErrNotFound
		}
		result, err := tx.ExecContext(ctx, `UPDATE app_response_urls SET uses_remaining = uses_remaining - 1 WHERE token_hash = ? AND uses_remaining > 0 AND expires_at > ?`, tokenHash, now)
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
		value.UsesRemaining--
		value.CreatedAt = time.Unix(0, createdAt).UTC()
		value.ExpiresAt = time.Unix(0, expiresAt).UTC()
		return tx.Commit()
	})
	if err != nil {
		return domain.AppResponseURL{}, err
	}
	return value, nil
}

func (s *Store) UpdateApp(ctx context.Context, app domain.App, revision domain.AppManifestRevision) error {
	if app.ID == "" || app.OwnerID == "" || app.ManifestVersion < 2 || revision.AppID != app.ID || revision.Version != app.ManifestVersion || revision.CreatedBy != app.OwnerID || strings.TrimSpace(revision.Manifest) == "" || revision.CreatedAt.IsZero() {
		return store.InvalidArgument("invalid app revision")
	}
	return underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `UPDATE slack_apps SET name = ?, description = ?, manifest_version = ?, distribution = ?, socket_mode_enabled = ?, token_rotation_enabled = ?, updated_at = ?
			WHERE id = ? AND owner_id = ? AND development_workspace_id = ? AND client_id = ? AND signing_secret_hash = ? AND signing_secret_ciphertext = ? AND verification_token_hash = ? AND verification_token_ciphertext = ? AND manifest_version = ? AND deleted = 0`,
			app.Name, app.Description, app.ManifestVersion, app.Distribution, boolInt(app.SocketModeEnabled), boolInt(app.TokenRotationEnabled), domain.NewStoredTime(app.UpdatedAt), app.ID, app.OwnerID, app.DevelopmentWorkspaceID, app.ClientID, app.SigningSecretHash, app.SigningSecretCiphertext, app.VerificationTokenHash, app.VerificationTokenCiphertext, app.ManifestVersion-1)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return store.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_manifest_revisions(app_id, version, manifest, created_by, created_at) VALUES (?, ?, ?, ?, ?)`, revision.AppID, revision.Version, revision.Manifest, revision.CreatedBy, domain.NewStoredTime(revision.CreatedAt)); err != nil {
			return classify(err)
		}
		return tx.Commit()
	})
}

func (s *Store) DeleteApp(ctx context.Context, appID domain.AppID, ownerID domain.UserID, deletedAt time.Time) error {
	if appID == "" || ownerID == "" || deletedAt.IsZero() {
		return store.InvalidArgument("app deletion identity is required")
	}
	return underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var clientID string
		err = tx.QueryRowContext(ctx, `SELECT client_id FROM slack_apps WHERE id = ? AND owner_id = ? AND deleted = 0`, appID, ownerID).Scan(&clientID)
		if err := translateNotFound(err); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE slack_apps SET deleted = 1, updated_at = ? WHERE id = ? AND owner_id = ? AND deleted = 0`, domain.NewStoredTime(deletedAt), appID, ownerID)
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
		for _, statement := range []string{
			`UPDATE app_installations SET enabled = 0 WHERE app_id = ?`,
			`UPDATE tokens SET revoked = 1 WHERE app_id = ?`,
			`UPDATE app_tokens SET revoked = 1 WHERE app_id = ?`,
			`UPDATE incoming_webhooks SET enabled = 0 WHERE app_id = ?`,
			`UPDATE bots SET deleted = 1, updated_at = ? WHERE app_id = ?`,
		} {
			var execErr error
			if strings.Contains(statement, "updated_at") {
				_, execErr = tx.ExecContext(ctx, statement, deletedAt.UTC().UnixNano(), appID)
			} else {
				_, execErr = tx.ExecContext(ctx, statement, appID)
			}
			if execErr != nil {
				return execErr
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_codes WHERE client_id = ?`, clientID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_datastore_items WHERE app_id = ?`, appID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_clients WHERE id = ?`, clientID); err != nil {
			return err
		}
		return tx.Commit()
	})
}
