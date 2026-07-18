package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestStaticAuthenticatorReturnsTypedPrincipal(t *testing.T) {
	principal := Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[Scope]struct{}{ScopeChatWrite: {}}}
	authenticator, err := NewStatic("token", principal)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("Authorization", "Bearer token")
	got, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != domain.WorkspaceID("T1") || got.UserID != domain.UserID("U1") || !got.HasScope(ScopeChatWrite) {
		t.Fatalf("principal = %+v", got)
	}
}

func TestStaticAuthenticatorRejectsWrongToken(t *testing.T) {
	authenticator, err := NewStatic("token", Principal{WorkspaceID: "T1", UserID: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	if _, err := authenticator.Authenticate(request); err != ErrNotAuthenticated {
		t.Fatalf("err = %v", err)
	}
}

func TestStoredAuthenticatorUsesPersistedScopes(t *testing.T) {
	store := memory.New()
	store.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(ScopeChannelsHistory)}})
	authenticator, err := NewStored(store)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer token")
	principal, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.HasScope(ScopeChannelsHistory) || principal.HasScope(ScopeChatWrite) {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestBrowserAuthenticatorUsesPersistedScopes(t *testing.T) {
	store := memory.New()
	if err := store.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(ScopeChannelsHistory)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
	principal, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.HasScope(ScopeChannelsHistory) || principal.HasScope(ScopeChatWrite) {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestSessionCookieDomainIsExplicitAndShared(t *testing.T) {
	for _, domain := range []string{"example.com", "apps.example.com"} {
		if err := ValidateSessionCookieDomain(domain); err != nil {
			t.Fatalf("validate %q: %v", domain, err)
		}
		cookie := SessionCookie("session", 86400, domain)
		if cookie.Domain != domain || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("cookie = %+v", cookie)
		}
	}
	for _, domain := range []string{".example.com", "https://example.com", "example.com:443", "127.0.0.1"} {
		if err := ValidateSessionCookieDomain(domain); err == nil {
			t.Fatalf("domain %q was accepted", domain)
		}
	}
}
