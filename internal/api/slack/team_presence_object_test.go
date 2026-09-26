package slack

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTeamInfoReportsDomainAndFetchableIcons(t *testing.T) {
	handler, _ := testHandlerWithStore()
	call := apiCaller(t, handler)
	team := call("team.info", url.Values{})["team"].(map[string]any)
	requirePinnedFields(t, "team.info", team, pinnedDefinition(t, "objs_team", 0))
	// The fixture workspace was seeded without a domain.
	if team["domain"] != "t1" || team["email_domain"] != "" || team["url"] != "http://example.com/" {
		t.Fatalf("team=%v", team)
	}
	icon := team["icon"].(map[string]any)
	if icon["image_default"] != true {
		t.Fatalf("icon=%v", icon)
	}
	for _, size := range []string{"34", "44", "68", "88", "102", "132", "230"} {
		value, _ := icon["image_"+size].(string)
		if !strings.HasPrefix(value, "http://example.com/") {
			t.Fatalf("image_%s=%q", size, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(value, "http://example.com"), nil))
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("image_%s status=%d headers=%v", size, response.Code, response.Header())
		}
	}
	if identity := call("auth.test", url.Values{}); identity["url"] != "http://example.com/" {
		t.Fatalf("auth.test url=%v", identity["url"])
	}
}

func TestGetPresenceReportsDetailOnlyForTheCaller(t *testing.T) {
	handler, _ := testHandlerWithStore()
	call := apiCaller(t, handler)
	own := call("users.getPresence", url.Values{})
	for _, field := range []string{"online", "auto_away", "manual_away", "connection_count", "last_activity"} {
		if _, present := own[field]; !present {
			t.Fatalf("the caller's presence lacks %s: %v", field, own)
		}
	}
	if set := call("users.setPresence", url.Values{"presence": {"away"}}); set["ok"] != true {
		t.Fatalf("setPresence=%v", set)
	}
	away := call("users.getPresence", url.Values{})
	if away["presence"] != "away" || away["manual_away"] != true || away["auto_away"] != false || away["online"] != false || away["connection_count"] != float64(0) {
		t.Fatalf("manually away presence=%v", away)
	}
	other := call("users.getPresence", url.Values{"user": {"U2"}})
	if _, present := other["manual_away"]; present || other["presence"] == nil {
		t.Fatalf("another member's presence carries the caller-only detail: %v", other)
	}
}
