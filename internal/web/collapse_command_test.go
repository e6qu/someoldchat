package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
)

// Slack's /collapse and /expand change only the member's own view: nothing is
// posted, /collapse returns to the conversation with inline media collapsed,
// and /expand returns to it as the member's preferences show it.
func TestCollapseAndExpandChangeOnlyTheView(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	send := func(text string) (int, string) {
		response := postForm(t, mux, "/app/message?channel=Cdev", url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "text": {text}}.Encode(), true)
		return response.Code, response.Header().Get("HX-Redirect")
	}
	if code, _ := send("an ordinary message"); code != http.StatusNoContent && code != http.StatusOK && code != http.StatusSeeOther {
		t.Fatalf("posting the baseline message answered %d", code)
	}
	before := get(t, mux, "/app?channel=Cdev").Body.String()

	code, collapsed := send("/collapse")
	if code != http.StatusNoContent || !strings.Contains(collapsed, "media=collapsed") || !strings.Contains(collapsed, "notice=") {
		t.Fatalf("/collapse status=%d redirect=%q", code, collapsed)
	}
	page := get(t, mux, collapsed).Body.String()
	requireContains(t, "collapsed view", page, `id="timeline"`, `data-media="collapsed"`, "Use /expand to show them again.")

	code, expanded := send("/expand")
	if code != http.StatusNoContent || strings.Contains(expanded, "media=collapsed") {
		t.Fatalf("/expand status=%d redirect=%q", code, expanded)
	}
	requireMissing(t, "expanded view", get(t, mux, expanded).Body.String(), `data-media="collapsed"`)

	if after := get(t, mux, "/app?channel=Cdev").Body.String(); strings.Count(after, `data-message-id=`) != strings.Count(before, `data-message-id=`) || strings.Count(before, `data-message-id=`) == 0 {
		t.Fatal("/collapse or /expand posted a message")
	}
}
