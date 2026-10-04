package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/l10n"
)

// keyReference finds every place this package names a message: {{t "key"}}
// and {{tn "key" n}} in markup, .T("key") and .N("key") in Go, and
// sameoldchatT('key') in scripts.
var keyReference = regexp.MustCompile(`(?:\{\{-?\s*tn?\s+"|\.(?:T|N)\("|sameoldchatT\(')([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)`)

// Every message the markup and scripts name is in the source catalog, and
// every message in it is named somewhere: a key with no message renders as
// the key, and a message nothing names is a translation nobody will see.
func TestEveryMessageKeyIsDefinedAndUsed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string][]string{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range keyReference.FindAllStringSubmatch(string(source), -1) {
			used[match[1]] = append(used[match[1]], file)
		}
	}
	defined := map[string]bool{}
	for _, key := range l10n.Keys() {
		defined[key] = true
	}
	var undefined, unused []string
	for key, where := range used {
		if !defined[key] {
			undefined = append(undefined, key+" ("+strings.Join(where, ", ")+")")
		}
	}
	for key := range defined {
		if _, ok := used[key]; !ok {
			unused = append(unused, key)
		}
	}
	sort.Strings(undefined)
	sort.Strings(unused)
	if len(undefined) > 0 {
		t.Errorf("named but not in internal/l10n/locales/en.json: %v", undefined)
	}
	if len(unused) > 0 {
		t.Errorf("in internal/l10n/locales/en.json but named nowhere: %v", unused)
	}
}

// A page answers in the reader's locale: the member's chosen one first, else
// the browser's, else English. Its lang, its Content-Language and its
// localized text all agree, the scripts receive the same locale's client
// messages, and the language picker offers the listed languages only.
func TestPagesRenderInTheReadersLocale(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	render := func(cookie, accept string) (*httptest.ResponseRecorder, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/app?channel=Cdev", nil)
		addBrowserCookies(request)
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: localeCookie, Value: cookie})
		}
		if accept != "" {
			request.Header.Set("Accept-Language", accept)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d", response.Code)
		}
		return response, response.Body.String()
	}
	english := l10n.For(l10n.Default)
	pseudo := l10n.For(l10n.Pseudo)

	response, body := render("", "fr-FR,fr;q=0.9")
	if response.Header().Get("Content-Language") != "en" || !strings.Contains(body, `<html lang="en"`) {
		t.Fatalf("a language with no catalog was not answered in English: %s", response.Header().Get("Content-Language"))
	}
	if !strings.Contains(body, ">"+html.EscapeString(english.T("prefs.tab.region"))+"<") {
		t.Fatal("the English page lacks its Language & region tab")
	}
	if !strings.Contains(body, `<option value="en" lang="en" selected>English</option>`) || strings.Contains(body, `value="en-XA"`) {
		t.Fatal("the language picker does not offer exactly the listed languages")
	}

	for name, item := range map[string][2]string{"chosen": {string(l10n.Pseudo), "fr"}, "accepted": {"", "en-XA"}} {
		response, body = render(item[0], item[1])
		if response.Header().Get("Content-Language") != string(l10n.Pseudo) || !strings.Contains(body, `<html lang="en-XA"`) {
			t.Fatalf("%s: the page is not in the pseudo-locale", name)
		}
		for _, key := range []string{"prefs.tab.region", "prefs.tab.notifications", "prefs.region.timezone_note"} {
			if !strings.Contains(body, html.EscapeString(pseudo.T(key))) {
				t.Errorf("%s: %s is not localized: want %q", name, key, pseudo.T(key))
			}
		}
		if !strings.Contains(body, `client.prefs.saved&#34;:&#34;`+pseudo.T("client.prefs.saved")) {
			t.Errorf("%s: the scripts do not receive the locale's client messages", name)
		}
	}
}

// A response behind the localizing routes can still stream and take over its
// connection: wrapping the writer must not hide what it can do.
func TestTheLocaleWriterKeepsStreamingAndHijacking(t *testing.T) {
	recorder := httptest.NewRecorder()
	var flushed bool
	withLocale(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("the localized writer cannot flush")
		}
		if _, ok := w.(http.Hijacker); !ok {
			t.Error("the localized writer cannot hijack")
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController cannot flush through the localized writer: %v", err)
		}
		flushed = true
		if localeOf(w) != l10n.Default || requestLocale(r) != l10n.Default {
			t.Error("the request's locale is not the source locale")
		}
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app", nil))
	if !flushed || !recorder.Flushed {
		t.Fatal("the response was not flushed")
	}
}
