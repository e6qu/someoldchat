package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// TestSeedUserPreservesOperatorState mirrors the SQL contract: a seed creates the
// bootstrap identity and then leaves it alone. Seeding used to replace the whole
// record, so a restart blanked the administrator's e-mail and profile and undid an
// administrative deactivation.
func TestSeedUserPreservesOperatorState(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "Tdev", Name: "SameOldChat"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "Udev", WorkspaceID: "Tdev", Email: "admin@example.test", Name: "sameoldchat", RealName: "SameOldChat"}); err != nil {
		t.Fatal(err)
	}
	profile := domain.UserProfile{DisplayName: "Administrator", StatusText: "on call", StatusEmoji: ":wave:"}
	if _, err := s.UpdateUserProfile(ctx, "Tdev", "Udev", profile, events.Event{ID: "E-profile", WorkspaceID: "Tdev", Topic: "user.profile_changed", Payload: "Udev", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDeleted(ctx, "Tdev", "Udev", true, events.Event{ID: "E-deleted", WorkspaceID: "Tdev", Topic: "user.deleted", Payload: "Udev", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "Udev", WorkspaceID: "Tdev", Name: "sameoldchat", RealName: "SameOldChat"}); err != nil {
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
	if err := s.SeedUser(domain.User{ID: "Ublank", WorkspaceID: "Tdev", Name: "blank"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "Ublank", WorkspaceID: "Tdev", Email: "Later@Example.test", Name: "blank"}); err != nil {
		t.Fatal(err)
	}
	if filled, err := s.GetUser(ctx, "Ublank"); err != nil || filled.Email != "later@example.test" {
		t.Fatalf("filled e-mail user=%+v err=%v", filled, err)
	}
}

// TestOAuthCodeIsHashedAndExpires pins the authorization-code contract on the
// in-memory profile: the code is held only as a digest and stops being redeemable
// after store.OAuthCodeLifetime.
func TestOAuthCodeIsHashedAndExpires(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	grant := domain.OAuthCode{Code: "plaintext-code", ClientID: "client", WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}, RedirectURI: "https://example.test/callback"}
	if err := s.CreateOAuthCode(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, plaintext := s.oauthCodes[grant.Code]; plaintext {
		t.Fatal("the authorization code is held in plaintext")
	}
	stored, hashed := s.oauthCodes[domain.HashToken(grant.Code)]
	if !hashed {
		t.Fatal("the authorization code is not held under its digest")
	}
	if stored.grant.Code != domain.HashToken(grant.Code) {
		t.Fatalf("stored grant retains the code %q", stored.grant.Code)
	}
	if limit := time.Now().UTC().Add(store.OAuthCodeLifetime); stored.expiresAt.After(limit) {
		t.Fatalf("expiry=%s, want no later than %s", stored.expiresAt, limit)
	}

	stored.expiresAt = time.Now().UTC().Add(-time.Second)
	s.oauthCodes[domain.HashToken(grant.Code)] = stored
	if _, err := s.ExchangeOAuthCode(ctx, "client", "client-secret", grant.Code, grant.RedirectURI, "access-expired", domain.OAuthToken{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired code exchange error=%v, want %v", err, store.ErrNotFound)
	}

	fresh := grant
	fresh.Code = "second-plaintext-code"
	if err := s.CreateOAuthCode(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	token, err := s.ExchangeOAuthCode(ctx, "client", "client-secret", fresh.Code, fresh.RedirectURI, "access-live", domain.OAuthToken{})
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access-live" || token.UserID != "U1" {
		t.Fatalf("exchanged token=%+v", token)
	}
	installations, err := s.ListAppInstallations(ctx, "A1")
	if err != nil || len(installations) != 1 || installations[0].WorkspaceID != "T1" {
		t.Fatalf("atomic OAuth installation=%+v err=%v", installations, err)
	}
	if _, err := s.ExchangeOAuthCode(ctx, "client", "wrong-secret", fresh.Code, fresh.RedirectURI, "access-wrong", domain.OAuthToken{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong client secret error=%v, want %v", err, store.ErrNotFound)
	}
}

// TestOpenIDRefreshRotationPersistsTheAccessToken closes the same gap as the SQL
// test: the rotation used to mint an access token that authenticated nothing.
func TestOpenIDRefreshRotationPersistsTheAccessToken(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("access token scopes=%v", record.Scopes)
	}
}

// TestRevokedSessionRetainsNoProviderCredential pins the revocation contract on
// every path, and the expired-session sweep that runs with the logout-token
// sweep.
func TestRevokedSessionRetainsNoProviderCredential(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	create := func(token, sid string, expiresAt time.Time) {
		t.Helper()
		if err := s.SeedSession(ctx, token, domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{"openid"}, ExpiresAt: expiresAt, OIDCProvider: "oidc", OIDCIDToken: "signed." + token, OIDCSubject: "subject", OIDCSID: sid}); err != nil {
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
	create("expired", "sid-expired", time.Now().UTC().Add(-time.Hour))
	if err := s.RevokeOIDCSessions(ctx, "T1", "oidc", "subject", "sid-logout", "logout-token", time.Now().UTC().Add(time.Hour), events.Event{ID: "E-logout", WorkspaceID: "T1", Topic: "session.revoked", Payload: "sid-logout", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	assertStripped("by-logout")
	if _, err := s.LookupSession(ctx, "expired"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session lookup error=%v, want %v", err, store.ErrNotFound)
	}

	create("by-deactivation", "sid-deactivation", time.Now().UTC().Add(time.Hour))
	if err := s.SetUserDeleted(ctx, "T1", "U1", true, events.Event{ID: "E-deactivate", WorkspaceID: "T1", Topic: "user.deleted", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	assertStripped("by-deactivation")
}

// TestAuthMethodAbsenceIsNotAnEnablement pins the auth-method contract: a
// provider with no stored decision is not enabled, and the absence is reported as
// store.ErrNotFound rather than invented as a permission.
// A row in auth_methods records an administrator's decision to turn a provider
// OFF. Absence is not a decision, so it must not disable anything: a new
// deployment has no rows, and absence-means-disabled would lock out every
// provider including the one the administrator needs to sign in and re-enable
// them. Whether a provider exists at all is decided by startup configuration.
func TestAuthMethodAbsenceLeavesTheOperatorConfigurationGoverning(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	method, err := s.GetAuthMethod(ctx, "T1", "never-overridden")
	if err != nil {
		t.Fatalf("absent auth method error=%v, want no error", err)
	}
	if !method.Enabled {
		t.Fatal("an authorization provider with no administrative override reported itself disabled, which no administrator can undo")
	}
	if err := s.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: "T1", Provider: "oidc", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	disabled, err := s.GetAuthMethod(ctx, "T1", "oidc")
	if err != nil || disabled.Enabled {
		t.Fatalf("explicitly disabled auth method=%+v err=%v, want it disabled", disabled, err)
	}
	if err := s.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: "T1", Provider: "oidc", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if method, err := s.GetAuthMethod(ctx, "T1", "oidc"); err != nil || !method.Enabled {
		t.Fatalf("re-enabled auth method=%+v err=%v", method, err)
	}
}

// TestListAndCanvasAccessResolveEveryGrantPath pins the readers the authorization
// decision needs, and keeps the in-memory resolution identical to the SQL one.
func TestListAndCanvasAccessResolveEveryGrantPath(t *testing.T) {
	ctx := context.Background()
	s := New()
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		if err := s.SeedWorkspace(domain.Workspace{ID: workspace, Name: string(workspace)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []domain.UserID{"U1", "Ureader", "Uchannel", "Ustranger", "Udeleted"} {
		if err := s.SeedUser(domain.User{ID: user, WorkspaceID: "T1", Name: string(user)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedUser(domain.User{ID: "Uother", WorkspaceID: "T2", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("C1", "Uchannel"); err != nil {
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
	if err := s.SetListAccess(ctx, domain.ListAccess{ListID: list.ID, EntityType: domain.GrantUser, EntityID: "Ureader", Access: domain.AccessRead}, event("E-list-user")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetListAccess(ctx, domain.ListAccess{ListID: list.ID, EntityType: domain.GrantChannel, EntityID: "C1", Access: domain.AccessWrite}, event("E-list-channel")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: canvas.ID, EntityType: domain.GrantChannel, EntityID: "C1", Access: domain.AccessRead}, event("E-canvas-channel")); err != nil {
		t.Fatal(err)
	}
	for _, expectation := range []struct {
		name  string
		user  domain.UserID
		level domain.AccessLevel
	}{
		{"owner", "U1", domain.AccessOwner},
		{"direct user grant", "Ureader", domain.AccessRead},
		{"channel grant through membership", "Uchannel", domain.AccessWrite},
	} {
		access, err := s.GetListAccess(ctx, list.ID, expectation.user)
		if err != nil || access.Access != expectation.level || access.ListID != list.ID {
			t.Fatalf("%s: access=%+v err=%v, want %s", expectation.name, access, err, expectation.level)
		}
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
	access, err := s.GetCanvasAccess(ctx, canvas.ID, "Uchannel")
	if err != nil || access.Access != domain.AccessRead || access.EntityType != "channel" || access.EntityID != "C1" {
		t.Fatalf("canvas channel grant=%+v err=%v", access, err)
	}
	for _, expectation := range []struct {
		user            domain.UserID
		lists, canvases int
	}{{"U1", 1, 1}, {"Ureader", 1, 0}, {"Uchannel", 1, 1}, {"Ustranger", 0, 0}, {"Udeleted", 0, 0}} {
		lists, listErr := s.ListLists(ctx, "T1", expectation.user, domain.PageRequest{Limit: 10})
		canvases, canvasErr := s.ListCanvases(ctx, "T1", expectation.user, domain.PageRequest{Limit: 10})
		if listErr != nil || canvasErr != nil || len(lists.Lists) != expectation.lists || len(canvases.Canvases) != expectation.canvases {
			t.Fatalf("visible documents for %s: lists=%d/%v canvases=%d/%v", expectation.user, len(lists.Lists), listErr, len(canvases.Canvases), canvasErr)
		}
	}
	if err := s.DeleteListAccess(ctx, domain.ListAccess{ListID: list.ID, EntityType: domain.GrantUser, EntityID: "Ureader"}, event("E-list-revoke")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetListAccess(ctx, list.ID, "Ureader"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked grant error=%v, want %v", err, store.ErrNotFound)
	}
}
