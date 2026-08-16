package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestExternalAuthConnectSurfaceOffersAndStartsAConnection covers the member's
// side of external auth in the browser: the app's About tab shows a connect
// button for each declared provider, starting one redirects to the provider, and
// a callback that cannot complete returns to the About tab with a notice.
func TestExternalAuthConnectSurfaceOffersAndStartsAConnection(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Connector", ClientID: "client",
		SigningSecretHash: "sh", SigningSecretCiphertext: "v1.s", VerificationTokenHash: "vh", VerificationTokenCiphertext: "v1.v",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: `{"display_information":{"name":"Connector"}}`, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetExternalAuthProvider(ctx, domain.ExternalAuthProvider{
		AppID: "A1", Name: "acme", ClientID: "acme-client", ClientSecretCiphertext: "v1.sealed",
		AuthorizationURL: "https://acme.test/authorize", TokenURL: "https://acme.test/token", Scopes: []string{"read"}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	session := "browser-session"
	if err := repository.SeedSession(ctx, session, domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	browser, err := auth.NewBrowser(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, browser, repository, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: session}

	about := httptest.NewRequest(http.MethodGet, "https://chat.example/app/apps/A1?tab=about", nil)
	about.AddCookie(cookie)
	aboutRec := httptest.NewRecorder()
	mux.ServeHTTP(aboutRec, about)
	if body := aboutRec.Body.String(); aboutRec.Code != http.StatusOK || !strings.Contains(body, "Connect an account") || !strings.Contains(body, "Connect acme") {
		t.Fatalf("about tab status=%d body=%s", aboutRec.Code, aboutRec.Body.String())
	}

	start := httptest.NewRequest(http.MethodPost, "https://chat.example/app/apps/external-auth/start", strings.NewReader(url.Values{"_csrf": {auth.CSRFToken(session)}, "app_id": {"A1"}, "provider": {"acme"}}.Encode()))
	start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start.Header.Set("Sec-Fetch-Site", "same-origin")
	start.AddCookie(cookie)
	startRec := httptest.NewRecorder()
	mux.ServeHTTP(startRec, start)
	if startRec.Code != http.StatusSeeOther {
		t.Fatalf("start status=%d body=%s", startRec.Code, startRec.Body.String())
	}
	location, err := url.Parse(startRec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Host != "acme.test" || location.Query().Get("client_id") != "acme-client" || location.Query().Get("state") == "" {
		t.Fatalf("start redirect = %s", startRec.Header().Get("Location"))
	}
	// The callback carries the same app and provider; the redirect_uri the token
	// exchange must echo is rebuilt from them.
	if !strings.Contains(location.Query().Get("redirect_uri"), "/app/apps/external-auth/callback") {
		t.Fatalf("redirect_uri = %q", location.Query().Get("redirect_uri"))
	}

	// A callback that cannot verify its state returns to the About tab with a
	// failure notice rather than an error page.
	callback := httptest.NewRequest(http.MethodGet, "https://chat.example/app/apps/external-auth/callback?app=A1&provider=acme&code=x&state=forged", nil)
	callback.AddCookie(cookie)
	callbackRec := httptest.NewRecorder()
	mux.ServeHTTP(callbackRec, callback)
	if callbackRec.Code != http.StatusSeeOther || !strings.Contains(callbackRec.Header().Get("Location"), "notice=connect_failed") {
		t.Fatalf("callback status=%d location=%q", callbackRec.Code, callbackRec.Header().Get("Location"))
	}
}
