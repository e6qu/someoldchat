package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// registeredPostRoutes reads every POST pattern this package registers from its
// own source, so a route added tomorrow is swept without anyone remembering to
// list it here.
func registeredPostRoutes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`mux\.Handle(?:Func)?\("POST ([^"]+)"`)
	var routes []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			routes = append(routes, match[1])
		}
	}
	slices.Sort(routes)
	return slices.Compact(routes)
}

// csrfExemptRoutes are the POST routes a browser session cookie does not
// authorize, so a forged cross-site request carries nothing to exploit. Each
// one authenticates by something a cross-site page cannot supply.
var csrfExemptRoutes = map[string]string{
	"/app-response/{token}":         "authorized by the unguessable response token in its path, which a cross-site page does not have",
	"/auth/oidc/backchannel-logout": "called server-to-server by the identity provider and authorized by its signed logout token",
}

// forgedMutationBodies are well-formed bodies in both browser encodings, with
// every field a mutation shape asks for — form fields, a modal's view_id, a file
// part — and a forged CSRF token, so each route gets as far as its CSRF check
// instead of refusing the shape.
func forgedMutationBodies(t *testing.T) map[string]func() (string, *bytes.Buffer) {
	t.Helper()
	fields := map[string]string{"channel": "Cdev", "conversation": "Cdev", "call_id": "x", "text": "hi", "view_id": "V1", auth.CSRFTokenFieldName: "forged"}
	return map[string]func() (string, *bytes.Buffer){
		"urlencoded": func() (string, *bytes.Buffer) {
			values := url.Values{}
			for name, value := range fields {
				values.Set(name, value)
			}
			return "application/x-www-form-urlencoded", bytes.NewBufferString(values.Encode())
		},
		"multipart": func() (string, *bytes.Buffer) {
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			for name, value := range fields {
				if err := writer.WriteField(name, value); err != nil {
					t.Fatal(err)
				}
			}
			part, err := writer.CreateFormFile("file", "note.txt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("hello")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			return writer.FormDataContentType(), body
		},
	}
}

// Every POST route a signed-in browser can reach must refuse a request that
// carries the session cookie but not the session's CSRF token, whether it
// claims to come from this origin or arrives from another site. The huddle
// media and presence routes and the developer-apps reload skipped the check,
// so any page a member visited could broadcast huddle presence on their behalf
// or drive their media connection.
func TestEveryBrowserPostRouteRequiresTheCSRFToken(t *testing.T) {
	routes := registeredPostRoutes(t)
	if len(routes) < 100 {
		t.Fatalf("found only %d POST routes; the route scan is broken", len(routes))
	}
	// The strongest reader, with every scope, and the provider-backed routes
	// registered, so a refusal cannot come from a role, scope or missing-route
	// answer that happens to run before the CSRF check.
	mux, _ := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleOwner)
	parameter := regexp.MustCompile(`\{[^}]+\}`)
	for _, route := range routes {
		if _, exempt := csrfExemptRoutes[route]; exempt {
			continue
		}
		path := parameter.ReplaceAllString(strings.TrimSuffix(route, "{$}"), "x")
		for _, site := range []string{"same-origin", "cross-site"} {
			// Every encoding must be refused, and at least one of them by the
			// CSRF check itself: a route may reject an encoding it does not
			// accept before it reads the token, which is still a refusal.
			refusedForCSRF := false
			for encoding, build := range forgedMutationBodies(t) {
				contentType, body := build()
				request := httptest.NewRequest(http.MethodPost, path, body)
				request.Header.Set("Content-Type", contentType)
				request.Header.Set("Sec-Fetch-Site", site)
				request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
				recorder := httptest.NewRecorder()
				mux.ServeHTTP(recorder, request)
				switch {
				case recorder.Code == http.StatusForbidden || recorder.Code == http.StatusUnauthorized:
					refusedForCSRF = true
				case recorder.Code < 400 || recorder.Code >= 500:
					t.Errorf("POST %s (%s, %s, forged CSRF token) answered %d, want a refusal", route, site, encoding, recorder.Code)
				}
			}
			if !refusedForCSRF {
				t.Errorf("POST %s (%s, forged CSRF token) was never refused by a CSRF check", route, site)
			}
		}
	}
}
