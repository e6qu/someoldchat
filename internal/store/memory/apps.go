package memory

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (s *Store) CreateOAuthAuthorization(_ context.Context, botUser domain.User, bot domain.Bot, code domain.OAuthCode) error {
	if code.Code == "" || code.ClientID == "" || code.WorkspaceID == "" || code.UserID == "" {
		return store.InvalidArgument("invalid oauth authorization")
	}
	withBot := len(domain.NormalizeScopes(code.BotScopes)) != 0
	if withBot && (botUser.ID == "" || botUser.WorkspaceID != code.WorkspaceID || bot.ID == "" || bot.WorkspaceID != code.WorkspaceID || bot.UserID != botUser.ID || bot.UpdatedAt.IsZero() || strings.TrimSpace(bot.Name) == "") {
		return store.InvalidArgument("invalid oauth bot authorization")
	}
	if !withBot && (botUser.ID != "" || bot.ID != "" || code.BotID != "" || code.BotUserID != "") {
		return store.InvalidArgument("unexpected oauth bot authorization")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, exists := s.oauthClients[code.ClientID]
	if !exists || withBot && client.AppID != bot.AppID {
		return store.ErrNotFound
	}
	if withBot && (code.BotID != bot.ID || code.BotUserID != botUser.ID) {
		return store.InvalidArgument("oauth bot does not match grant")
	}
	if workspace, exists := s.workspaces[code.WorkspaceID]; !exists || workspace.ID == "" {
		return store.ErrNotFound
	}
	if user, exists := s.users[code.UserID]; !exists || user.WorkspaceID != code.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	codeHash := domain.HashToken(code.Code)
	if _, exists := s.oauthCodes[codeHash]; exists {
		return store.ErrAlreadyExists
	}
	if withBot {
		if _, exists := s.users[botUser.ID]; exists {
			return store.ErrAlreadyExists
		}
		if _, exists := s.bots[bot.ID]; exists {
			return store.ErrAlreadyExists
		}
		botUser.Presence = domain.PresenceAuto
		s.users[botUser.ID] = botUser
		s.members[string(code.WorkspaceID)+"\x00"+string(botUser.ID)] = domain.WorkspaceMembership{WorkspaceID: code.WorkspaceID, UserID: botUser.ID, Role: domain.WorkspaceRoleMember, Active: true}
		s.bots[bot.ID] = bot
	}
	code.Scopes = domain.NormalizeScopes(code.Scopes)
	code.BotScopes = domain.NormalizeScopes(code.BotScopes)
	code.UserScopes = domain.NormalizeScopes(code.UserScopes)
	code.Code = codeHash
	s.oauthCodes[codeHash] = memoryOAuthCode{grant: code, expiresAt: time.Now().UTC().Add(store.OAuthCodeLifetime)}
	return nil
}

func (s *Store) CreateAppConfigurationToken(_ context.Context, accessToken, refreshToken string, value domain.AppConfigurationToken) error {
	if strings.TrimSpace(accessToken) == "" || strings.TrimSpace(refreshToken) == "" || value.WorkspaceID == "" || value.UserID == "" || !value.ExpiresAt.After(time.Now().UTC()) {
		return store.InvalidArgument("invalid app configuration token")
	}
	accessHash := domain.HashToken(accessToken)
	refreshHash := domain.HashToken(refreshToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appConfigurationTokens == nil {
		s.appConfigurationTokens = make(map[string]domain.AppConfigurationToken)
	}
	if s.appConfigurationRefreshTokens == nil {
		s.appConfigurationRefreshTokens = make(map[string]string)
	}
	if _, exists := s.appConfigurationTokens[accessHash]; exists {
		return store.ErrAlreadyExists
	}
	if _, exists := s.appConfigurationRefreshTokens[refreshHash]; exists {
		return store.ErrAlreadyExists
	}
	if user, exists := s.users[value.UserID]; !exists || user.WorkspaceID != value.WorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	s.appConfigurationTokens[accessHash] = value
	s.appConfigurationRefreshTokens[refreshHash] = accessHash
	return nil
}

func (s *Store) LookupAppConfigurationToken(_ context.Context, accessToken string) (domain.AppConfigurationToken, error) {
	s.mu.RLock()
	value, exists := s.appConfigurationTokens[domain.HashToken(accessToken)]
	s.mu.RUnlock()
	if !exists || value.Revoked || !value.ExpiresAt.After(time.Now().UTC()) {
		return domain.AppConfigurationToken{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) LookupAppConfigurationRefreshToken(_ context.Context, refreshToken string) (domain.AppConfigurationToken, error) {
	s.mu.RLock()
	accessHash, exists := s.appConfigurationRefreshTokens[domain.HashToken(refreshToken)]
	value, tokenExists := s.appConfigurationTokens[accessHash]
	s.mu.RUnlock()
	if !exists || !tokenExists || value.Revoked {
		return domain.AppConfigurationToken{}, store.ErrNotFound
	}
	return value, nil
}

func (s *Store) RotateAppConfigurationToken(_ context.Context, oldRefreshToken, nextAccessToken, nextRefreshToken string, next domain.AppConfigurationToken) error {
	if strings.TrimSpace(oldRefreshToken) == "" || strings.TrimSpace(nextAccessToken) == "" || strings.TrimSpace(nextRefreshToken) == "" || !next.ExpiresAt.After(time.Now().UTC()) {
		return store.InvalidArgument("invalid app configuration token rotation")
	}
	oldRefreshHash := domain.HashToken(oldRefreshToken)
	nextAccessHash := domain.HashToken(nextAccessToken)
	nextRefreshHash := domain.HashToken(nextRefreshToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	oldAccessHash, exists := s.appConfigurationRefreshTokens[oldRefreshHash]
	if !exists {
		return store.ErrNotFound
	}
	old, exists := s.appConfigurationTokens[oldAccessHash]
	if !exists || old.Revoked || old.WorkspaceID != next.WorkspaceID || old.UserID != next.UserID {
		return store.ErrNotFound
	}
	if _, exists := s.appConfigurationTokens[nextAccessHash]; exists {
		return store.ErrAlreadyExists
	}
	if _, exists := s.appConfigurationRefreshTokens[nextRefreshHash]; exists {
		return store.ErrAlreadyExists
	}
	delete(s.appConfigurationRefreshTokens, oldRefreshHash)
	delete(s.appConfigurationTokens, oldAccessHash)
	s.appConfigurationTokens[nextAccessHash] = next
	s.appConfigurationRefreshTokens[nextRefreshHash] = nextAccessHash
	return nil
}

func (s *Store) CreateApp(_ context.Context, app domain.App, revision domain.AppManifestRevision, client domain.OAuthClient) error {
	if app.ID == "" || app.DevelopmentWorkspaceID == "" || app.OwnerID == "" || strings.TrimSpace(app.Name) == "" || app.ClientID == "" || app.SigningSecretHash == "" || app.SigningSecretCiphertext == "" || app.VerificationTokenCiphertext == "" || app.ManifestVersion != 1 || app.CreatedAt.IsZero() || app.UpdatedAt.IsZero() || revision.AppID != app.ID || revision.Version != 1 || revision.CreatedBy != app.OwnerID || strings.TrimSpace(revision.Manifest) == "" || client.ID != app.ClientID || client.AppID != app.ID || client.SecretHash == "" {
		return store.InvalidArgument("invalid app")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[app.ID]; exists {
		return store.ErrAlreadyExists
	}
	if _, exists := s.oauthClients[client.ID]; exists {
		return store.ErrAlreadyExists
	}
	if user, exists := s.users[app.OwnerID]; !exists || user.WorkspaceID != app.DevelopmentWorkspaceID || user.Deleted {
		return store.ErrNotFound
	}
	s.apps[app.ID] = app
	s.appManifestRevisions[app.ID] = []domain.AppManifestRevision{revision}
	s.oauthClients[client.ID] = client
	return nil
}

func (s *Store) GetApp(_ context.Context, appID domain.AppID) (domain.App, domain.AppManifestRevision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	app, exists := s.apps[appID]
	if !exists || app.Deleted {
		return domain.App{}, domain.AppManifestRevision{}, store.ErrNotFound
	}
	revisions := s.appManifestRevisions[appID]
	if len(revisions) == 0 || revisions[len(revisions)-1].Version != app.ManifestVersion {
		return domain.App{}, domain.AppManifestRevision{}, store.ErrNotFound
	}
	return app, revisions[len(revisions)-1], nil
}

func (s *Store) GetAppByClientID(_ context.Context, clientID string) (domain.App, domain.AppManifestRevision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for appID, app := range s.apps {
		if app.ClientID != clientID || app.Deleted {
			continue
		}
		revisions := s.appManifestRevisions[appID]
		if len(revisions) == 0 || revisions[len(revisions)-1].Version != app.ManifestVersion {
			return domain.App{}, domain.AppManifestRevision{}, store.ErrNotFound
		}
		return app, revisions[len(revisions)-1], nil
	}
	return domain.App{}, domain.AppManifestRevision{}, store.ErrNotFound
}

func (s *Store) ListDeveloperApps(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.App, error) {
	if workspaceID == "" || userID == "" {
		return nil, store.InvalidArgument("developer workspace and user are required")
	}
	s.mu.RLock()
	values := make([]domain.App, 0)
	for _, app := range s.apps {
		if app.DevelopmentWorkspaceID == workspaceID && app.OwnerID == userID && !app.Deleted {
			values = append(values, app)
		}
	}
	s.mu.RUnlock()
	slices.SortFunc(values, func(left, right domain.App) int {
		if order := strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)); order != 0 {
			return order
		}
		return strings.Compare(string(left.ID), string(right.ID))
	})
	return values, nil
}

func (s *Store) ListInstalledApps(_ context.Context) ([]domain.AppManifestSnapshot, error) {
	s.mu.RLock()
	installed := make(map[domain.AppID]bool)
	for _, installation := range s.appInstallations {
		if installation.Enabled {
			installed[installation.AppID] = true
		}
	}
	values := make([]domain.AppManifestSnapshot, 0, len(installed))
	for appID := range installed {
		app, exists := s.apps[appID]
		revisions := s.appManifestRevisions[appID]
		if !exists || app.Deleted || len(revisions) == 0 || revisions[len(revisions)-1].Version != app.ManifestVersion {
			continue
		}
		values = append(values, domain.AppManifestSnapshot{App: app, Manifest: revisions[len(revisions)-1].Manifest})
	}
	s.mu.RUnlock()
	slices.SortFunc(values, func(left, right domain.AppManifestSnapshot) int {
		return strings.Compare(string(left.App.ID), string(right.App.ID))
	})
	return values, nil
}

func (s *Store) CreateAppInteractionCapabilities(_ context.Context, trigger domain.AppTrigger, response domain.AppResponseURL) error {
	if trigger.TokenHash == "" || response.TokenHash == "" || trigger.AppID == "" || trigger.WorkspaceID == "" || trigger.UserID == "" ||
		response.AppID != trigger.AppID || response.WorkspaceID != trigger.WorkspaceID || response.UserID != trigger.UserID ||
		trigger.CreatedAt.IsZero() || !trigger.ExpiresAt.After(trigger.CreatedAt) || response.CreatedAt.IsZero() ||
		!response.ExpiresAt.After(response.CreatedAt) || response.ConversationID == "" || response.UsesRemaining != 5 {
		return store.InvalidArgument("invalid application interaction capabilities")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.appTriggers[trigger.TokenHash]; exists {
		return store.ErrAlreadyExists
	}
	if _, exists := s.appResponseURLs[response.TokenHash]; exists {
		return store.ErrAlreadyExists
	}
	s.appTriggers[trigger.TokenHash] = trigger
	s.appResponseURLs[response.TokenHash] = response
	return nil
}

func (s *Store) CreateAppTrigger(_ context.Context, trigger domain.AppTrigger) error {
	if trigger.TokenHash == "" || trigger.AppID == "" || trigger.WorkspaceID == "" || trigger.UserID == "" ||
		trigger.CreatedAt.IsZero() || !trigger.ExpiresAt.After(trigger.CreatedAt) {
		return store.InvalidArgument("invalid application trigger")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.appTriggers[trigger.TokenHash]; exists {
		return store.ErrAlreadyExists
	}
	s.appTriggers[trigger.TokenHash] = trigger
	return nil
}

func (s *Store) ConsumeAppTrigger(_ context.Context, tokenHash string, appID domain.AppID) (domain.AppTrigger, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.appTriggers[tokenHash]
	if !exists || value.AppID != appID || !value.ExpiresAt.After(now) || !value.ConsumedAt.IsZero() {
		return domain.AppTrigger{}, store.ErrNotFound
	}
	value.ConsumedAt = now
	s.appTriggers[tokenHash] = value
	return value, nil
}

func (s *Store) UseAppResponseURL(_ context.Context, tokenHash string) (domain.AppResponseURL, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.appResponseURLs[tokenHash]
	if !exists || !value.ExpiresAt.After(now) || value.UsesRemaining <= 0 {
		return domain.AppResponseURL{}, store.ErrNotFound
	}
	value.UsesRemaining--
	s.appResponseURLs[tokenHash] = value
	return value, nil
}

func (s *Store) UpdateApp(_ context.Context, app domain.App, revision domain.AppManifestRevision) error {
	if app.ID == "" || app.OwnerID == "" || app.ManifestVersion < 2 || revision.AppID != app.ID || revision.Version != app.ManifestVersion || revision.CreatedBy == "" || strings.TrimSpace(revision.Manifest) == "" || revision.CreatedAt.IsZero() {
		return store.InvalidArgument("invalid app revision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.apps[app.ID]
	if !exists || current.Deleted {
		return store.ErrNotFound
	}
	if current.OwnerID != app.OwnerID || current.DevelopmentWorkspaceID != app.DevelopmentWorkspaceID || current.ClientID != app.ClientID || current.SigningSecretHash != app.SigningSecretHash || current.SigningSecretCiphertext != app.SigningSecretCiphertext || current.VerificationTokenHash != app.VerificationTokenHash || current.VerificationTokenCiphertext != app.VerificationTokenCiphertext || current.ManifestVersion+1 != app.ManifestVersion || revision.CreatedBy != app.OwnerID {
		return store.ErrConflict
	}
	s.apps[app.ID] = app
	s.appManifestRevisions[app.ID] = append(s.appManifestRevisions[app.ID], revision)
	return nil
}

func (s *Store) DeleteApp(_ context.Context, appID domain.AppID, ownerID domain.UserID, deletedAt time.Time) error {
	if appID == "" || ownerID == "" || deletedAt.IsZero() {
		return store.InvalidArgument("app deletion identity is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, exists := s.apps[appID]
	if !exists || app.Deleted {
		return store.ErrNotFound
	}
	if app.OwnerID != ownerID {
		return store.ErrNotFound
	}
	app.Deleted = true
	app.UpdatedAt = deletedAt.UTC()
	s.apps[appID] = app
	for key, installation := range s.appInstallations {
		if installation.AppID == appID {
			installation.Enabled = false
			s.appInstallations[key] = installation
		}
	}
	for key, token := range s.tokens {
		if token.AppID == appID {
			token.Revoked = true
			s.tokens[key] = token
		}
	}
	for key, token := range s.appTokens {
		if token.AppID == appID {
			token.Revoked = true
			s.appTokens[key] = token
		}
	}
	for id, webhook := range s.incomingWebhooks {
		if webhook.AppID == appID {
			webhook.Enabled = false
			s.incomingWebhooks[id] = webhook
		}
	}
	for id, bot := range s.bots {
		if bot.AppID == appID {
			bot.Deleted = true
			bot.UpdatedAt = deletedAt.UTC()
			s.bots[id] = bot
			s.deactivateBotUserLocked(bot)
		}
	}
	for key, item := range s.appDatastoreItems {
		if item.AppID == appID {
			delete(s.appDatastoreItems, key)
		}
	}
	for codeHash, code := range s.oauthCodes {
		if code.grant.ClientID == app.ClientID {
			delete(s.oauthCodes, codeHash)
		}
	}
	delete(s.oauthClients, app.ClientID)
	return nil
}

// deactivateBotUserLocked removes an uninstalled app identity from every live
// membership surface while retaining the user record for historical message
// authorship. Callers hold s.mu.
func (s *Store) deactivateBotUserLocked(bot domain.Bot) {
	if user, ok := s.users[bot.UserID]; ok && user.WorkspaceID == bot.WorkspaceID {
		user.Deleted = true
		s.users[bot.UserID] = user
	}
	membershipKey := string(bot.WorkspaceID) + "\x00" + string(bot.UserID)
	if membership, ok := s.members[membershipKey]; ok {
		membership.Active = false
		s.members[membershipKey] = membership
	}
	for _, members := range s.memberships {
		delete(members, bot.UserID)
	}
}
