package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// Language & region's time zone: the heartbeat keeps the profile's zone with
// the browser's until the member sets one by hand, which turns the automatic
// zone off; turning it back on lets the browser's zone in again.
func TestAManualTimeZoneOutlastsTheHeartbeat(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	zone := func() string {
		t.Helper()
		user, err := messages.UserInfo(context.Background(), "T1", "U1", "U1")
		if err != nil {
			t.Fatal(err)
		}
		return user.Profile.Timezone
	}
	csrf := auth.CSRFToken("session")
	beat := func(browserZone string) {
		t.Helper()
		if response := postForm(t, mux, "/app/active?channel=Cdev", url.Values{auth.CSRFTokenFieldName: {csrf}, "timezone": {browserZone}}.Encode(), false); response.Code != http.StatusNoContent {
			t.Fatalf("heartbeat answered %d", response.Code)
		}
	}

	beat("America/New_York")
	if got := zone(); got != "America/New_York" {
		t.Fatalf("the automatic zone did not follow the browser: %q", got)
	}

	if response := postForm(t, mux, "/app/preferences/timezone?channel=Cdev", url.Values{auth.CSRFTokenFieldName: {csrf}, "timezone": {"Not/AZone"}}.Encode(), false); response.Code != http.StatusBadRequest {
		t.Fatalf("an unknown zone answered %d", response.Code)
	}
	if got := zone(); got != "America/New_York" {
		t.Fatalf("a refused zone changed the profile: %q", got)
	}

	if response := postForm(t, mux, "/app/preferences/timezone?channel=Cdev", url.Values{auth.CSRFTokenFieldName: {csrf}, "timezone": {"Europe/Berlin"}}.Encode(), false); response.Code != http.StatusSeeOther {
		t.Fatalf("setting a zone answered %d", response.Code)
	}
	beat("America/New_York")
	if got := zone(); got != "Europe/Berlin" {
		t.Fatalf("the heartbeat overwrote a zone set by hand: %q", got)
	}
	requireContains(t, "Language & region", get(t, mux, "/app?channel=Cdev").Body.String(), `name="timezone" value="Europe/Berlin"`)

	if err := messages.SetMemberPreference(context.Background(), "T1", "U1", timezoneAutomaticPreference, "true"); err != nil {
		t.Fatal(err)
	}
	beat("America/New_York")
	if got := zone(); got != "America/New_York" {
		t.Fatalf("turning the automatic zone back on did not follow the browser: %q", got)
	}
}
