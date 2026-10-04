package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// The member's VIPs are listed where their notifications are managed, each
// removable in place, and removing one returns to that list rather than to
// the directory.
func TestNotificationPreferencesListAndRemoveVIPs(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: s}
	requireContains(t, "no VIPs", get(t, mux, "/app/notifications?channel=Cdev").Body.String(), "You have no VIPs.")
	if err := messages.SetNotificationVIP(context.Background(), "T1", "U1", "U2", true); err != nil {
		t.Fatal(err)
	}
	requireContains(t, "VIP list", get(t, mux, "/app/notifications?channel=Cdev").Body.String(),
		`id="notification-vips-heading"`,
		"<span>Bob Builder</span>",
		`aria-label="Remove Bob Builder from VIPs"`,
	)

	returnTo := "/app/notifications?channel=Cdev#notification-vips-heading"
	removed := postForm(t, mux, "/app/notifications/vips", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "target": {"U2"}, "add": {"false"}, "return": {returnTo},
	}.Encode(), false)
	if removed.Code != http.StatusSeeOther || !strings.HasPrefix(removed.Header().Get("Location"), "/app/notifications") {
		t.Fatalf("removing answered %d to %q", removed.Code, removed.Header().Get("Location"))
	}
	requireContains(t, "VIP removed", get(t, mux, "/app/notifications?channel=Cdev").Body.String(), "You have no VIPs.")
}
