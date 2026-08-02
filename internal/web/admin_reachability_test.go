package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The administration surfaces used to be registered only when an identity
// provider was configured, so a deployment running on the static development
// session had no administration at all — the pages answered 404. These assert
// the three outcomes that distinguishes: reachable for an administrator,
// refused for a member, and provider administration still requiring one.
func TestWorkspaceAdministrationIsReachableWithoutAnIdentityProvider(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/app/admin/settings", "/app/admin/analytics", "/app/admin/audit"} {
		if code := adminStatus(t, mux, path); code != http.StatusOK {
			t.Errorf("GET %s returned %d for a workspace administrator, want 200", path, code)
		}
	}
}

// A member is refused, and refused as a member rather than as a missing page:
// 404 tells an administrator their deployment is broken, 403 tells them their
// account is not privileged, and those are different problems.
func TestWorkspaceAdministrationRefusesAMember(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/app/admin/settings", "/app/admin/analytics", "/app/admin/audit"} {
		if code := adminStatus(t, mux, path); code != http.StatusForbidden {
			t.Errorf("GET %s returned %d for a plain member, want 403", path, code)
		}
	}
}

// Identity-provider administration keeps the dependency it actually has.
func TestIdentityProviderAdministrationStillRequiresAProvider(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if code := adminStatus(t, mux, "/app/admin/auth"); code != http.StatusNotFound {
		t.Errorf("GET /app/admin/auth returned %d with no provider configured, want 404", code)
	}
}

// The sidebar has to lead to what is now reachable, or the surfaces exist and
// nobody finds them.
func TestSidebarLinksWorkspaceAdministrationForAnAdministrator(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	body := renderWorkspacePage(t, mux)
	if !contains(body, `href="/app/admin/settings"`) {
		t.Error("the sidebar does not link workspace settings for an administrator")
	}
	if contains(body, `href="/app/admin/auth"`) {
		t.Error("the sidebar links identity-provider administration with no provider configured")
	}
}

func adminStatus(t *testing.T, mux *http.ServeMux, path string) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder.Code
}

func contains(body, needle string) bool { return len(body) > 0 && indexOf(body, needle) >= 0 }

func indexOf(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}
