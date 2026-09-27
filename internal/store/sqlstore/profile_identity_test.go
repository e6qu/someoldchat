package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// TestSQLiteProfileTitlePronounsAndTimezoneAreDurable pins schema 192: the
// title, pronouns and time zone a member sets survive a reopen of the
// database and come back on every user read.
func TestSQLiteProfileTitlePronounsAndTimezoneAreDurable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "profile.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "ana"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := domain.UserProfile{DisplayName: "Ana", Title: "Design lead", Pronouns: "she/her", Timezone: "Europe/Lisbon"}
	if _, err := s.UpdateUserProfile(ctx, "T1", "U1", profile, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "user.profile_changed", Payload: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	user, err := reopened.GetUser(ctx, "U1")
	if err != nil {
		t.Fatal(err)
	}
	if user.Profile.Title != "Design lead" || user.Profile.Pronouns != "she/her" || user.Profile.Timezone != "Europe/Lisbon" {
		t.Fatalf("profile after reopen = %+v", user.Profile)
	}
	page, err := reopened.ListUsers(ctx, "T1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Users) != 1 || page.Users[0].Profile.Title != "Design lead" {
		t.Fatalf("users.list read = %+v", page.Users)
	}
}
