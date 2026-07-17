package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

type failingAuthenticator struct{}

func (failingAuthenticator) Authenticate(*http.Request) (auth.Principal, error) {
	return auth.Principal{}, errors.New("session store unavailable")
}

func TestHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Body.String(); got != "ok\n" {
		t.Fatalf("body = %q, want %q", got, "ok\\n")
	}
}

func TestRootRedirectsAuthenticatedUsersToApp(t *testing.T) {
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	rootHandler(authenticator, true).ServeHTTP(result, request)
	if result.Code != http.StatusFound || result.Header().Get("Location") != "/app" {
		t.Fatalf("status=%d location=%q", result.Code, result.Header().Get("Location"))
	}
}

func TestRootRedirectsUnauthenticatedUsersToLoginWhenEnabled(t *testing.T) {
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	result := httptest.NewRecorder()
	rootHandler(authenticator, true).ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/", nil))
	if result.Code != http.StatusFound || result.Header().Get("Location") != "/login" {
		t.Fatalf("status=%d location=%q", result.Code, result.Header().Get("Location"))
	}
}

func TestRootReturnsServiceUnavailableForAuthenticationBackendFailure(t *testing.T) {
	result := httptest.NewRecorder()
	rootHandler(failingAuthenticator{}, true).ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/", nil))
	if result.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", result.Code, http.StatusServiceUnavailable)
	}
}

func TestRootRedirectsToAppWhenExternalLoginIsDisabled(t *testing.T) {
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	result := httptest.NewRecorder()
	rootHandler(authenticator, false).ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/", nil))
	if result.Code != http.StatusFound || result.Header().Get("Location") != "/app" {
		t.Fatalf("status=%d location=%q", result.Code, result.Header().Get("Location"))
	}
}

func TestReadinessChecksTheSelectedService(t *testing.T) {
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "Tdev"})
	selected.SeedUser(domain.User{ID: "Udev", WorkspaceID: "Tdev"})
	selected.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "Tdev", Name: "general"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", readinessHandler(service.Messages{Store: selected}))
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, request)
	if result.Code != http.StatusOK || result.Body.String() != "ready\n" {
		t.Fatalf("ready status=%d body=%q", result.Code, result.Body.String())
	}
	mux = http.NewServeMux()
	mux.HandleFunc("GET /readyz", readinessHandler(service.Messages{Store: memory.New()}))
	result = httptest.NewRecorder()
	mux.ServeHTTP(result, request)
	if result.Code != http.StatusServiceUnavailable || result.Body.String() != "not ready\n" {
		t.Fatalf("not-ready status=%d body=%q", result.Code, result.Body.String())
	}
}

func TestParseClusterNormalizesAddresses(t *testing.T) {
	got, err := localchat.ParseCluster(" node-a:19001, node-b:19001 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"node-a:19001", "node-b:19001"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cluster = %#v, want %#v", got, want)
	}
}

func TestParseClusterRejectsEmptyAddress(t *testing.T) {
	if _, err := localchat.ParseCluster("node-a:19001,,node-b:19001"); err == nil {
		t.Fatal("empty cluster address was accepted")
	}
}
