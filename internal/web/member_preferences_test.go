package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
)

// A preference the page's script sends is kept for the member, and every
// page carries what the member keeps, so a new browser takes it on load. A
// malformed one is refused without being kept.
func TestPreferencesAreKeptForTheMember(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")
	for _, preference := range []url.Values{
		{"_csrf": {csrf}, "name": {"theme"}, "value": {"dark"}},
		{"_csrf": {csrf}, "name": {"section-sort:starred"}, "value": {"recent"}},
	} {
		if kept := postForm(t, mux, "/app/preferences", preference.Encode(), false); kept.Code != http.StatusNoContent {
			t.Fatalf("keep %v=%d: %s", preference, kept.Code, kept.Body)
		}
	}
	if refused := postForm(t, mux, "/app/preferences", url.Values{"_csrf": {csrf}, "name": {"Not A Name"}, "value": {"x"}}.Encode(), false); refused.Code != http.StatusBadRequest {
		t.Fatalf("a malformed preference=%d", refused.Code)
	}
	kept, err := s.MemberPreferences(context.Background(), "T1", "U1")
	if err != nil || len(kept) != 2 || kept["theme"] != "dark" {
		t.Fatalf("kept=%v err=%v", kept, err)
	}
	requireContains(t, "the page", get(t, mux, "/app?channel=Cdev").Body.String(),
		`data-preferences="{&#34;section-sort:starred&#34;:&#34;recent&#34;,&#34;theme&#34;:&#34;dark&#34;}" data-preferences-csrf="`+csrf+`"`)
}
