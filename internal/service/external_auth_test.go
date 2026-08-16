package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

type externalAuthRoundTripper func(*http.Request) (*http.Response, error)

func (f externalAuthRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func externalAuthWorld(t *testing.T) (context.Context, *memory.Store, Messages) {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "Uowner", WorkspaceID: "T1", Name: "owner"})
	s.SeedUser(domain.User{ID: "Umember", WorkspaceID: "T1", Name: "member"})
	now := time.Now().UTC()
	if err := s.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "Uowner", Name: "App", ClientID: "client",
		SigningSecretHash: "sh", SigningSecretCiphertext: "v1.s", VerificationTokenHash: "vh", VerificationTokenCiphertext: "v1.v",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: `{"display_information":{"name":"App"}}`, CreatedBy: "Uowner", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: externalAuthRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == "https://acme.test/token" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"access_token":"provider-token","expires_in":3600}`))}, nil
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("no"))}, nil
	})}
	return ctx, s, Messages{Store: s, AppCredentialKey: []byte("0123456789abcdef0123456789abcdef"), AppHTTPClient: client}
}

func declareAcme(t *testing.T, ctx context.Context, m Messages) {
	t.Helper()
	configuration, err := m.IssueAppConfigurationToken(ctx, "T1", "Uowner")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetAppExternalAuthProvider(ctx, configuration.Token, "A1", domain.ExternalAuthProviderConfig{
		Name: "acme", ClientID: "acme-client", ClientSecret: "acme-secret",
		AuthorizationURL: "https://acme.test/authorize", TokenURL: "https://acme.test/token", Scopes: []string{"read"},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestExternalAuthConnectFlowStoresAToken drives the whole flow: the owner
// declares a provider, a member starts a connection, the sealed state comes back
// on the callback, the code is exchanged at the provider, and a credential is
// stored — the one apps.auth.external.get then reports.
func TestExternalAuthConnectFlowStoresAToken(t *testing.T) {
	ctx, _, m := externalAuthWorld(t)
	declareAcme(t, ctx, m)

	// The provider lists without its secret.
	providers, err := m.AppExternalAuthProviders(ctx, "T1", "Umember", "A1")
	if err != nil || len(providers) != 1 || providers[0].Name != "acme" || providers[0].ClientID != "acme-client" || providers[0].ClientSecretCiphertext != "" {
		t.Fatalf("providers = %+v err=%v", providers, err)
	}

	callback := "https://chat.example/app/apps/external-auth/callback?app=A1&provider=acme"
	authorizeURL, err := m.StartExternalAuthConnection(ctx, "T1", "Umember", "A1", "acme", callback)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "acme.test" || parsed.Query().Get("client_id") != "acme-client" || parsed.Query().Get("redirect_uri") != callback {
		t.Fatalf("authorize url = %s", authorizeURL)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("no state on the authorize url")
	}

	if err := m.CompleteExternalAuthConnection(ctx, "T1", "Umember", "A1", "acme", "the-code", state, callback); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// A credential was stored: deleting the app's tokens finds it.
	if err := m.DeleteExternalAuthToken(ctx, "T1", "Umember", "A1", ""); err != nil {
		t.Fatalf("connected credential not stored: %v", err)
	}
}

// TestExternalAuthConnectRefusesForgedStateAndForeignMembers covers the guards:
// a callback whose state names a different member or app is refused, and a
// provider declaration is owner-only.
func TestExternalAuthConnectRefusesForgedStateAndForeignMembers(t *testing.T) {
	ctx, _, m := externalAuthWorld(t)
	declareAcme(t, ctx, m)
	callback := "https://chat.example/app/apps/external-auth/callback?app=A1&provider=acme"
	authorizeURL, err := m.StartExternalAuthConnection(ctx, "T1", "Umember", "A1", "acme", callback)
	if err != nil {
		t.Fatal(err)
	}
	state := mustQuery(t, authorizeURL, "state")

	// A non-member is refused before the flow begins, even though the app is
	// installed and the provider declared: workspace membership is the guard.
	if _, err := m.AppExternalAuthProviders(ctx, "T1", "U-stranger", "A1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stranger list = %v, want ErrNotFound", err)
	}
	if _, err := m.StartExternalAuthConnection(ctx, "T1", "U-stranger", "A1", "acme", callback); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stranger start = %v, want ErrNotFound", err)
	}
	if err := m.CompleteExternalAuthConnection(ctx, "T1", "U-stranger", "A1", "acme", "code", state, callback); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stranger complete = %v, want ErrNotFound", err)
	}

	// The state is bound to Umember: another member cannot spend it.
	if err := m.CompleteExternalAuthConnection(ctx, "T1", "Uowner", "A1", "acme", "code", state, callback); !errors.Is(err, ErrExternalAuthConnection) {
		t.Fatalf("cross-member complete = %v, want ErrExternalAuthConnection", err)
	}
	// A garbage state is refused.
	if err := m.CompleteExternalAuthConnection(ctx, "T1", "Umember", "A1", "acme", "code", "forged", callback); !errors.Is(err, ErrExternalAuthConnection) {
		t.Fatalf("forged state complete = %v, want ErrExternalAuthConnection", err)
	}

	// Only the owner may declare a provider: a config token for a non-owner is
	// refused as a missing app.
	otherConfiguration, err := m.IssueAppConfigurationToken(ctx, "T1", "Umember")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetAppExternalAuthProvider(ctx, otherConfiguration.Token, "A1", domain.ExternalAuthProviderConfig{
		Name: "evil", ClientID: "c", ClientSecret: "s", AuthorizationURL: "https://evil.test/a", TokenURL: "https://evil.test/t",
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-owner declare = %v, want ErrNotFound", err)
	}
	// A malformed provider (non-https URL) is refused.
	ownerConfiguration, err := m.IssueAppConfigurationToken(ctx, "T1", "Uowner")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetAppExternalAuthProvider(ctx, ownerConfiguration.Token, "A1", domain.ExternalAuthProviderConfig{
		Name: "acme", ClientID: "c", ClientSecret: "s", AuthorizationURL: "http://acme.test/a", TokenURL: "https://acme.test/t",
	}); !errors.Is(err, ErrInvalidExternalAuthProvider) {
		t.Fatalf("non-https provider = %v, want ErrInvalidExternalAuthProvider", err)
	}
}

func mustQuery(t *testing.T, rawURL, key string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	value := parsed.Query().Get(key)
	if value == "" {
		t.Fatalf("no %s on %s", key, rawURL)
	}
	return value
}
