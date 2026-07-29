package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// TestSQLiteSeedUserPreservesOperatorState pins the seed contract. Every binary
// that opens the store seeds the bootstrap identity on start, and only two of
// them are given the bootstrap administrator e-mail, so an upsert that replaced
// the row blanked the administrator's e-mail and profile on every worker restart
// and — through `deleted = excluded.deleted` — silently reactivated a user an
// administrator had deactivated.
func TestSQLiteSeedUserPreservesOperatorState(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "seed-user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "Tdev", Name: "SameOldChat"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "Udev", WorkspaceID: "Tdev", Email: "admin@example.test", Name: "sameoldchat", RealName: "SameOldChat"}); err != nil {
		t.Fatal(err)
	}
	profile := domain.UserProfile{DisplayName: "Administrator", StatusText: "on call", StatusEmoji: ":wave:", Image24: "https://example.test/24.png"}
	if _, err := s.UpdateUserProfile(ctx, "Tdev", "Udev", profile, events.Event{ID: "E-profile", WorkspaceID: "Tdev", Topic: "user.profile_changed", Payload: "Udev", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDeleted(ctx, "Tdev", "Udev", true, events.Event{ID: "E-deleted", WorkspaceID: "Tdev", Topic: "user.deleted", Payload: "Udev", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	// The restart of a binary that has no bootstrap administrator e-mail.
	if err := s.SeedUser(ctx, domain.User{ID: "Udev", WorkspaceID: "Tdev", Name: "sameoldchat", RealName: "SameOldChat"}); err != nil {
		t.Fatal(err)
	}
	user, err := s.GetUser(ctx, "Udev")
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "admin@example.test" {
		t.Fatalf("e-mail after re-seeding=%q, want the seeded administrator address", user.Email)
	}
	if user.Profile != profile {
		t.Fatalf("profile after re-seeding=%+v, want %+v", user.Profile, profile)
	}
	if !user.Deleted {
		t.Fatal("re-seeding reactivated a deactivated user")
	}
	membership, err := s.GetWorkspaceMembership(ctx, "Tdev", "Udev")
	if err != nil {
		t.Fatal(err)
	}
	if membership.Active {
		t.Fatal("re-seeding reactivated a deactivated membership")
	}

	// A never-set e-mail is still fillable, so the bootstrap administrator flag
	// is not inert on a database created before it was set.
	if err := s.SeedUser(ctx, domain.User{ID: "Ublank", WorkspaceID: "Tdev", Name: "blank"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "Ublank", WorkspaceID: "Tdev", Email: "Later@Example.test", Name: "blank"}); err != nil {
		t.Fatal(err)
	}
	filled, err := s.GetUser(ctx, "Ublank")
	if err != nil {
		t.Fatal(err)
	}
	if filled.Email != "later@example.test" {
		t.Fatalf("filled e-mail=%q, want the normalized address", filled.Email)
	}
}

// TestSQLiteOAuthCodeIsHashedAndExpires pins the authorization-code contract: the
// code is a bearer credential, so the database holds only its digest, and it is
// redeemable for a bounded time. It used to be stored verbatim with no expiry at
// all, so a database copy could redeem any outstanding grant forever.
func TestSQLiteOAuthCodeIsHashedAndExpires(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "oauth-code.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	grant := domain.OAuthCode{Code: "plaintext-code", ClientID: "client", WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}, RedirectURI: "https://example.test/callback"}
	if err := s.CreateOAuthCode(ctx, grant); err != nil {
		t.Fatal(err)
	}
	var plaintext int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_codes WHERE code = ?`, grant.Code).Scan(&plaintext); err != nil {
		t.Fatal(err)
	}
	if plaintext != 0 {
		t.Fatal("the authorization code is stored in plaintext")
	}
	var hashed, expiresAt int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(expires_at), 0) FROM oauth_codes WHERE code = ?`, domain.HashToken(grant.Code)).Scan(&hashed, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if hashed != 1 {
		t.Fatalf("hashed authorization code rows=%d, want 1", hashed)
	}
	if limit := time.Now().UTC().Add(store.OAuthCodeLifetime).UnixNano(); expiresAt <= 0 || expiresAt > limit {
		t.Fatalf("expiry=%d, want a positive value no later than %d", expiresAt, limit)
	}

	// An expired code cannot be redeemed even though it is still present.
	if _, err := s.db.ExecContext(ctx, `UPDATE oauth_codes SET expires_at = ?`, time.Now().UTC().Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExchangeOAuthCode(ctx, "client", "client-secret", grant.Code, grant.RedirectURI, "access-expired", domain.OAuthToken{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired code exchange error=%v, want %v", err, store.ErrNotFound)
	}

	// A live code is redeemable exactly once, and the access token it mints
	// authenticates.
	fresh := grant
	fresh.Code = "second-plaintext-code"
	if err := s.CreateOAuthCode(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	token, err := s.ExchangeOAuthCode(ctx, "client", "client-secret", fresh.Code, fresh.RedirectURI, "access-live", domain.OAuthToken{})
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access-live" || token.UserID != "U1" || token.AppID != "A1" {
		t.Fatalf("exchanged token=%+v", token)
	}
	if record, err := s.LookupToken(ctx, "access-live"); err != nil || record.UserID != "U1" {
		t.Fatalf("minted access token record=%+v err=%v", record, err)
	}
	installations, err := s.ListAppInstallations(ctx, "A1")
	if err != nil || len(installations) != 1 || installations[0].WorkspaceID != "T1" {
		t.Fatalf("atomic OAuth installation=%+v err=%v", installations, err)
	}
	if _, err := s.ExchangeOAuthCode(ctx, "client", "client-secret", fresh.Code, fresh.RedirectURI, "access-replay", domain.OAuthToken{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replayed code error=%v, want %v", err, store.ErrNotFound)
	}
}

func TestSQLiteOAuthBotGrantPersistsBotIdentity(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "oauth-bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)
	if err := s.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "U1", Name: "app", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthCode(ctx, domain.OAuthCode{
		Code:        "bot-code",
		ClientID:    "client",
		WorkspaceID: "T1",
		UserID:      "U1",
		BotID:       "B1",
		BotUserID:   "U1",
		BotScopes:   []string{"chat:write"},
		RedirectURI: "https://example.test/callback",
	}); err != nil {
		t.Fatal(err)
	}
	token, err := s.ExchangeOAuthCode(ctx, "client", "client-secret", "bot-code", "https://example.test/callback", "xoxb-issued", domain.OAuthToken{TokenType: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if token.BotID != "B1" || token.UserID != "U1" || token.InstallerID != "U1" || token.TokenType != "bot" {
		t.Fatalf("bot token=%+v", token)
	}
	record, err := s.LookupToken(ctx, token.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if record.AppID != "A1" || record.BotID != "B1" || record.UserID != "U1" || record.TokenType != "bot" || strings.Join(record.Scopes, " ") != "chat:write" {
		t.Fatalf("durable bot token=%+v", record)
	}
	if err := s.UninstallApp(ctx, "T1", "A1"); err != nil {
		t.Fatal(err)
	}
	record, err = s.LookupToken(ctx, token.AccessToken)
	if err != nil || !record.Revoked {
		t.Fatalf("uninstalled token=%+v err=%v", record, err)
	}
	bot, err := s.GetBot(ctx, "T1", "B1")
	if err != nil || !bot.Deleted {
		t.Fatalf("uninstalled bot=%+v err=%v", bot, err)
	}
	if installations, err := s.ListAppInstallations(ctx, "A1"); err != nil || len(installations) != 0 {
		t.Fatalf("uninstalled installations=%+v err=%v", installations, err)
	}
}

// TestSQLiteOAuthCodeConcurrentRedemptionIsSingleUse exercises the transaction
// race the HTTP authorization-code endpoint sees when two clients redeem the
// same code together. One caller may mint the token and every loser must get the
// portable not-found contract; a backend lock error is neither a valid OAuth
// response nor proof that the code was consumed.
func TestSQLiteOAuthCodeConcurrentRedemptionIsSingleUse(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "oauth-code-concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	const callers = 24
	for iteration := range 8 {
		grant := domain.OAuthCode{
			Code:        fmt.Sprintf("concurrent-code-%d", iteration),
			ClientID:    "client",
			WorkspaceID: "T1",
			UserID:      "U1",
			Scopes:      []string{"chat:write"},
			RedirectURI: "https://example.test/callback",
		}
		if err := s.CreateOAuthCode(ctx, grant); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, callers)
		var group sync.WaitGroup
		for caller := range callers {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				_, err := s.ExchangeOAuthCode(ctx, "client", "client-secret", grant.Code, grant.RedirectURI, fmt.Sprintf("access-%d-%d", iteration, caller), domain.OAuthToken{})
				results <- err
			}()
		}
		close(start)
		group.Wait()
		close(results)
		succeeded := 0
		notFound := 0
		for err := range results {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, store.ErrNotFound):
				notFound++
			default:
				t.Fatalf("concurrent redemption returned backend error: %v", err)
			}
		}
		if succeeded != 1 || notFound != callers-1 {
			t.Fatalf("successful redemptions=%d not-found redemptions=%d, want 1 and %d", succeeded, notFound, callers-1)
		}
	}
}

// TestSQLiteOpenIDRefreshConcurrentRotationIsSingleUse applies the same
// portable contention contract to refresh tokens. Rotation consumes the old
// credential and mints two new credentials atomically, so a racing loser must
// observe an already-consumed token instead of a storage-engine lock.
func TestSQLiteOpenIDRefreshConcurrentRotationIsSingleUse(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "openid-refresh-concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	const callers = 24
	for iteration := range 8 {
		oldToken := fmt.Sprintf("old-refresh-%d", iteration)
		if err := s.CreateOpenIDRefreshToken(ctx, domain.OpenIDRefreshToken{
			TokenHash:   domain.HashToken(oldToken),
			ClientID:    "client",
			WorkspaceID: "T1",
			UserID:      "U1",
			Scopes:      []string{"openid"},
			ExpiresAt:   time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, callers)
		var group sync.WaitGroup
		for caller := range callers {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				_, err := s.ExchangeOpenIDRefreshToken(ctx, "client", oldToken, fmt.Sprintf("openid-access-%d-%d", iteration, caller), fmt.Sprintf("new-refresh-%d-%d", iteration, caller), domain.OpenIDToken{})
				results <- err
			}()
		}
		close(start)
		group.Wait()
		close(results)
		succeeded := 0
		notFound := 0
		for err := range results {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, store.ErrNotFound):
				notFound++
			default:
				t.Fatalf("concurrent refresh rotation returned backend error: %v", err)
			}
		}
		if succeeded != 1 || notFound != callers-1 {
			t.Fatalf("successful rotations=%d not-found rotations=%d, want 1 and %d", succeeded, notFound, callers-1)
		}
	}
}

// TestSQLiteOAuthCodeMigrationDiscardsPlaintextAndAddsExpiry runs the migration
// against a populated database written by the previous schema, then against a
// fresh one, and twice over, because concurrently starting replicas run it
// repeatedly.
func TestSQLiteOAuthCodeMigrationDiscardsPlaintextAndAddsExpiry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-oauth-codes.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedConversationFixture(t, ctx, first)
	if err := first.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	// A row exactly as the previous schema wrote it: plaintext code, no expiry.
	if _, err := first.db.ExecContext(ctx, `INSERT INTO oauth_codes(code, client_id, workspace_id, user_id, scopes, redirect_uri, code_challenge, code_challenge_method, expires_at) VALUES ('legacy-plaintext', 'client', 'T1', 'U1', '["chat:write"]', 'https://example.test/callback', '', '', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 79 WHERE version = ?`, schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var remaining int
	if err := second.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_codes`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d plaintext authorization codes survived the upgrade", remaining)
	}
	if _, err := second.ExchangeOAuthCode(ctx, "client", "client-secret", "legacy-plaintext", "https://example.test/callback", "access-legacy", domain.OAuthToken{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("discarded legacy code error=%v, want %v", err, store.ErrNotFound)
	}
	columns, err := second.tableColumns(ctx, second.db, "oauth_codes")
	if err != nil {
		t.Fatal(err)
	}
	if !columns["expires_at"] {
		t.Fatal("the upgraded oauth_codes table has no expiry column")
	}
	if err := second.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration failed: %v", err)
	}
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh-oauth-codes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := fresh.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration on a fresh database failed: %v", err)
	}
}

// TestSQLiteOpenIDRefreshRotationPersistsTheAccessToken closes the gap that made
// the documented refresh flow unusable: the rotation minted an access token and
// inserted no row for it, so openid.connect.userInfo could never authenticate the
// token the caller was handed.
func TestSQLiteOpenIDRefreshRotationPersistsTheAccessToken(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "openid-refresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOpenIDRefreshToken(ctx, domain.OpenIDRefreshToken{TokenHash: domain.HashToken("refresh-one"), ClientID: "client", WorkspaceID: "T1", UserID: "U1", Scopes: []string{"openid", "profile"}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rotated, err := s.ExchangeOpenIDRefreshToken(ctx, "client", "refresh-one", "access-two", "refresh-two", domain.OpenIDToken{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.LookupToken(ctx, rotated.AccessToken)
	if err != nil {
		t.Fatalf("the rotated access token does not authenticate: err=%v", err)
	}
	if record.WorkspaceID != "T1" || record.UserID != "U1" || record.Revoked {
		t.Fatalf("access token record=%+v", record)
	}
	if strings.Join(record.Scopes, " ") != "openid profile" {
		t.Fatalf("access token scopes=%v, want the rotated grant's scopes", record.Scopes)
	}
}

// TestSQLiteRevokedSessionRetainsNoProviderCredential pins the revocation
// contract: a revoked session keeps no provider identity token, on every
// revocation path, and expired sessions are swept on the schedule the logout
// tokens already use.
func TestSQLiteRevokedSessionRetainsNoProviderCredential(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "revoke-session.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)
	create := func(token, sid string, expiresAt time.Time) {
		t.Helper()
		record := domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{"openid"}, ExpiresAt: expiresAt, OIDCProvider: "oidc", OIDCIDToken: "signed." + token, OIDCSubject: "subject", OIDCSID: sid}
		if err := s.CreateSession(ctx, token, record); err != nil {
			t.Fatal(err)
		}
	}
	assertStripped := func(token string) {
		t.Helper()
		record, err := s.LookupSession(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		if !record.Revoked {
			t.Fatalf("session %q is not revoked", token)
		}
		if record.OIDCIDToken != "" {
			t.Fatalf("revoked session %q still holds the provider identity token %q", token, record.OIDCIDToken)
		}
	}

	create("direct", "sid-direct", time.Now().UTC().Add(time.Hour))
	if err := s.RevokeSession(ctx, "direct"); err != nil {
		t.Fatal(err)
	}
	assertStripped("direct")

	create("by-user", "sid-user", time.Now().UTC().Add(time.Hour))
	if err := s.RevokeUserSessions(ctx, "T1", "U1", events.Event{ID: "E-user-revoke", WorkspaceID: "T1", Topic: "session.revoked", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	assertStripped("by-user")

	create("by-logout", "sid-logout", time.Now().UTC().Add(time.Hour))
	// CreateSession refuses an already expired session, so the row that the
	// sweep has to collect is written directly.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(session_hash, workspace_id, user_id, scopes, expires_at, revoked, oidc_provider, oidc_id_token, oidc_subject, oidc_sid) VALUES (?, 'T1', 'U1', 'openid', ?, 0, 'oidc', 'signed.expired', 'subject', 'sid-expired')`, domain.HashToken("expired"), domain.NewStoredTime(time.Now().UTC().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeOIDCSessions(ctx, "T1", "oidc", "subject", "sid-logout", "logout-token", time.Now().UTC().Add(time.Hour), events.Event{ID: "E-logout", WorkspaceID: "T1", Topic: "session.revoked", Payload: "sid-logout", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	assertStripped("by-logout")
	var expired int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE session_hash = ?`, domain.HashToken("expired")).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 0 {
		t.Fatal("an expired session survived the logout sweep")
	}
}

// TestSQLiteAuthMethodAbsenceIsNotAnEnablement pins the auth-method contract. A
// missing row used to report Enabled: true, so a provider nobody had configured
// read as live and an unwritten SetAuthMethod read as a permission.
// See the note on the memory backend's equivalent test: absence of a row means
// "no administrative override", and inverting it to fail closed produces a
// deployment nobody can sign in to, including to undo it.
func TestSQLiteAuthMethodAbsenceLeavesTheOperatorConfigurationGoverning(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "auth-method.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	method, err := s.GetAuthMethod(ctx, "T1", "never-overridden")
	if err != nil {
		t.Fatalf("absent auth method error=%v, want no error", err)
	}
	if !method.Enabled {
		t.Fatal("an authorization provider with no administrative override reported itself disabled, which no administrator can undo")
	}
	if err := s.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: "T1", Provider: "oidc", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if method, err := s.GetAuthMethod(ctx, "T1", "oidc"); err != nil || !method.Enabled {
		t.Fatalf("configured auth method=%+v err=%v", method, err)
	}
	if err := s.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: "T1", Provider: "oidc", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if method, err := s.GetAuthMethod(ctx, "T1", "oidc"); err != nil || method.Enabled {
		t.Fatalf("disabled auth method=%+v err=%v", method, err)
	}
}

// TestSQLiteClientSecretIsNotComparedByTheDatabase covers item six two ways: the
// behaviour (a wrong secret is refused, the right one is accepted) and the
// invariant behind it — no statement identifies an OAuth client by its secret
// digest, because a SQL equality test is not constant time and puts the digest in
// the query log. Timing itself is not asserted; the guard is on the code shape.
func TestSQLiteClientSecretIsNotComparedByTheDatabase(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "client-secret.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	grant := domain.OAuthCode{Code: "code", ClientID: "client", WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}, RedirectURI: "https://example.test/callback"}
	if err := s.CreateOAuthCode(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExchangeOAuthCode(ctx, "client", "wrong-secret", grant.Code, grant.RedirectURI, "access-wrong", domain.OAuthToken{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong client secret error=%v, want %v", err, store.ErrNotFound)
	}
	if _, err := s.ExchangeOAuthCode(ctx, "client", "client-secret", grant.Code, grant.RedirectURI, "access-right", domain.OAuthToken{}); err != nil {
		t.Fatalf("correct client secret was refused: %v", err)
	}

	file, err := parser.ParseFile(token.NewFileSet(), "sqlstore.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		if strings.Contains(literal.Value, "oauth_clients") && strings.Contains(literal.Value, "secret_hash = ?") {
			t.Fatalf("sqlstore.go identifies an OAuth client by its secret digest: %s", literal.Value)
		}
		return true
	})
}

// TestSQLiteListAndCanvasAccessResolveEveryGrantPath pins the readers the
// authorization decision needs. Without them the grants written by
// SetListAccess/SetCanvasAccess were unenforceable and every workspace member
// could read and delete every other member's list and canvas.
func TestSQLiteListAndCanvasAccessResolveEveryGrantPath(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "access-readers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)
	for _, user := range []domain.UserID{"Ureader", "Uchannel", "Ustranger", "Udeleted"} {
		if err := s.SeedUser(ctx, domain.User{ID: user, WorkspaceID: "T1", Name: string(user)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T2", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "Uother", WorkspaceID: "T2", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember(ctx, "C1", "Uchannel"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDeleted(ctx, "T1", "Udeleted", true, events.Event{ID: "E-deactivate", WorkspaceID: "T1", Topic: "user.deleted", Payload: "Udeleted", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	event := func(id string) events.Event {
		return events.Event{ID: domain.EventID(id), WorkspaceID: "T1", Topic: "access.set", Payload: id, CreatedAt: now}
	}
	list := domain.List{ID: "F1", WorkspaceID: "T1", OwnerID: "U1", Name: "Roadmap", DescriptionBlocks: "[]", Schema: "[]", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateList(ctx, list, event("E-list")); err != nil {
		t.Fatal(err)
	}
	canvas := domain.Canvas{ID: "Cv1", WorkspaceID: "T1", OwnerID: "U1", Title: "Notes", DocumentContent: "{}", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateCanvas(ctx, canvas, event("E-canvas")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetListAccess(ctx, domain.ListAccess{ListID: list.ID, EntityType: "user", EntityID: "Ureader", Access: store.AccessRead}, event("E-list-user")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetListAccess(ctx, domain.ListAccess{ListID: list.ID, EntityType: "channel", EntityID: "C1", Access: store.AccessWrite}, event("E-list-channel")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: canvas.ID, EntityType: "channel", EntityID: "C1", Access: store.AccessRead}, event("E-canvas-channel")); err != nil {
		t.Fatal(err)
	}

	for _, expectation := range []struct {
		name  string
		user  domain.UserID
		level string
	}{
		{"owner", "U1", store.AccessOwner},
		{"direct user grant", "Ureader", store.AccessRead},
		{"channel grant through membership", "Uchannel", store.AccessWrite},
	} {
		access, err := s.GetListAccess(ctx, list.ID, expectation.user)
		if err != nil {
			t.Fatalf("%s: err=%v", expectation.name, err)
		}
		if access.Access != expectation.level || access.ListID != list.ID {
			t.Fatalf("%s: access=%+v, want %s", expectation.name, access, expectation.level)
		}
	}
	// The owner of the channel-granted list is also a member of C1, so the
	// highest ranked grant has to win over the channel's write grant.
	if access, err := s.GetListAccess(ctx, list.ID, "U1"); err != nil || access.Access != store.AccessOwner {
		t.Fatalf("owner and channel member access=%+v err=%v, want %s", access, err, store.AccessOwner)
	}
	for _, refusal := range []struct {
		name string
		user domain.UserID
	}{
		{"workspace member with no grant", "Ustranger"},
		{"user in another workspace", "Uother"},
		{"deactivated user", "Udeleted"},
	} {
		if _, err := s.GetListAccess(ctx, list.ID, refusal.user); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("list access for a %s: err=%v, want %v", refusal.name, err, store.ErrNotFound)
		}
		if _, err := s.GetCanvasAccess(ctx, canvas.ID, refusal.user); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("canvas access for a %s: err=%v, want %v", refusal.name, err, store.ErrNotFound)
		}
	}
	if _, err := s.GetListAccess(ctx, "F-missing", "U1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing list error=%v, want %v", err, store.ErrNotFound)
	}
	if _, err := s.GetCanvasAccess(ctx, "Cv-missing", "U1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing canvas error=%v, want %v", err, store.ErrNotFound)
	}
	canvasAccess, err := s.GetCanvasAccess(ctx, canvas.ID, "Uchannel")
	if err != nil || canvasAccess.Access != store.AccessRead || canvasAccess.EntityType != "channel" || canvasAccess.EntityID != "C1" {
		t.Fatalf("canvas channel grant=%+v err=%v", canvasAccess, err)
	}
	// A revoked grant stops resolving.
	if err := s.DeleteListAccess(ctx, domain.ListAccess{ListID: list.ID, EntityType: "user", EntityID: "Ureader"}, event("E-list-revoke")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetListAccess(ctx, list.ID, "Ureader"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked grant error=%v, want %v", err, store.ErrNotFound)
	}
	if got := fmt.Sprintf("%d", store.AccessRank("unknown")); got != "0" {
		t.Fatalf("unknown access level ranks %s, want 0", got)
	}
}
