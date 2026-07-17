package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

type failingTokenStore struct{ err error }

func (s failingTokenStore) LookupToken(context.Context, string) (domain.TokenRecord, error) {
	return domain.TokenRecord{}, s.err
}

type failingSessionStore struct{ err error }

func (s failingSessionStore) LookupSession(context.Context, string) (domain.SessionRecord, error) {
	return domain.SessionRecord{}, s.err
}

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

func TestAuthenticatorsPropagateBackendFailures(t *testing.T) {
	backendErr := errors.New("database unavailable")
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer token")
	stored, err := NewStored(failingTokenStore{err: backendErr})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stored.Authenticate(request); !errors.Is(err, backendErr) {
		t.Fatalf("stored authentication error = %v, want %v", err, backendErr)
	}
	browser, err := NewBrowser(failingSessionStore{err: backendErr})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session"})
	if _, err := browser.Authenticate(request); !errors.Is(err, backendErr) {
		t.Fatalf("browser authentication error = %v, want %v", err, backendErr)
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
