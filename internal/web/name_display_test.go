package web

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// "Just display names" hides the full name the directory shows beside a
// display name, so the directory must mark that line as the full name.
func TestDirectoryMarksTheFullNameBesideADisplayName(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U9", WorkspaceID: "T1", Name: "ana", RealName: "Ana Lucía Ortega", Profile: domain.UserProfile{DisplayName: "ana"}}); err != nil {
		t.Fatal(err)
	}
	page := get(t, mux, "/app/members").Body.String()
	requireContains(t, "directory marks the full name", page, `<span class="person-line full-name">Ana Lucía Ortega</span>`)
}
