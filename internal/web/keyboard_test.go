package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
)

var advertisedShortcuts = regexp.MustCompile(`aria-keyshortcuts="([^"]*)"`)

// TestEveryAdvertisedShortcutIsDocumented is the invariant that makes one whole
// class of defect impossible: a page that announces a chord to assistive
// technology which nothing implements and nothing documents.
//
// `aria-keyshortcuts` is a promise. A screen reader reads it out, so a stale or
// aspirational value is worse than no value at all — the member is told the
// binding exists and it does nothing. Before keyboard.go the attributes were
// written by hand next to the markup and the handlers were written elsewhere,
// with nothing tying them together.
//
// This asserts the weaker, checkable half of the promise: every chord the page
// advertises appears in keyboardSections, so it is in the help dialog too. The
// other half — that the handler exists — is asserted by the browser suite,
// which presses the keys.
func TestEveryAdvertisedShortcutIsDocumented(t *testing.T) {
	documented := map[string]bool{}
	for _, section := range keyboardSections() {
		for _, shortcut := range section.Shortcuts {
			for _, chord := range strings.Fields(shortcut.Apple) {
				documented[chord] = true
			}
			for _, chord := range strings.Fields(shortcut.Other) {
				documented[chord] = true
			}
		}
	}

	body := renderWorkspacePage(t)
	if strings.Contains(body, "data-unknown-shortcut") {
		t.Fatalf("the page asked for a shortcut keyboardSections does not declare; ariaKeyshortcuts marked it in the markup")
	}
	matches := advertisedShortcuts.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatalf("the workspace page advertised no keyboard shortcuts at all, which means this test is asserting nothing")
	}
	for _, match := range matches {
		for _, chord := range strings.Fields(match[1]) {
			if !documented[chord] {
				t.Errorf("the page announces %q but keyboardSections does not describe it, so it is missing from the shortcuts dialog", chord)
			}
		}
	}
}

// TestKeyboardHelpNamesBothPlatforms checks the dialog carries the display form
// for each platform. The client hides the one that does not apply; if only one
// were rendered, half the members would be shown the wrong chord.
func TestKeyboardHelpNamesBothPlatforms(t *testing.T) {
	body := renderWorkspacePage(t)
	for _, want := range []string{
		`<kbd data-keyboard-apple>⌘K</kbd>`,
		`<kbd data-keyboard-other>Ctrl+K</kbd>`,
		`<kbd data-keyboard-apple>⌘⇧S</kbd>`,
		`<kbd data-keyboard-other>Ctrl+Shift+S</kbd>`,
		`<kbd data-keyboard-apple>⌥⇧↓</kbd>`,
		// NAV-02 requires section movement and nothing implemented it until
		// this change; the dialog is where a member finds out it exists.
		`<kbd data-keyboard-other>Ctrl+F6</kbd>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the shortcuts dialog is missing %s", want)
		}
	}
}

// TestKeyboardHelpIsReachableWithoutAKeyboardShortcut covers the circularity a
// shortcuts dialog invites: if the only way to learn the shortcuts is a
// shortcut, a member who does not know it cannot find out.
func TestKeyboardHelpIsReachableWithoutAKeyboardShortcut(t *testing.T) {
	body := renderWorkspacePage(t)
	if !strings.Contains(body, `id="open-keyboard-help"`) {
		t.Fatal("the shortcuts dialog has no visible control, so it is reachable only by already knowing the shortcut")
	}
	if !strings.Contains(body, `aria-controls="keyboard-help"`) {
		t.Error("the control does not name the dialog it opens")
	}
}

func renderWorkspacePage(t *testing.T) string {
	t.Helper()
	_, mux := browserWorkspace(t, auth.AllScopes())
	request := httptest.NewRequest(http.MethodGet, "/app?channel=Cdev", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /app returned %d", recorder.Code)
	}
	// html/template escapes `+` to `&#43;` even in text content. A browser
	// decodes it, so the member reads "Ctrl+K" and a DOM query matches
	// "Ctrl+K"; asserting on the entity here would be asserting on the
	// escaper's choices rather than on what the page says.
	return html.UnescapeString(recorder.Body.String())
}
