package web

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
)

var (
	renderedScript = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)
	renderedHref   = regexp.MustCompile(`href="(/[^"#]*)`)
)

// TestEveryReachablePageRunsItsOwnScripts crawls the workspace the way a member
// does — from /app, following every same-origin link — and checks that every
// inline script each page actually renders is allowed by the Content Security
// Policy served with that page. It checks the rendered output rather than the
// template source, so a script that gained a template action (and so a hash
// nobody computed), or a page whose policy was never told about its script,
// fails here instead of silently in the browser. The notification preferences
// page shipped with its only script blocked for exactly that reason.
func TestEveryReachablePageRunsItsOwnScripts(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	queue := []string{"/app", "/app?channel=Cdev", "/app/notifications", "/app/preferences"}
	seen := map[string]bool{}
	checked := 0
	for len(queue) > 0 && len(seen) < 250 {
		target := queue[0]
		queue = queue[1:]
		if seen[target] || strings.Contains(target, "/download") || strings.Contains(target, "/files/") || strings.HasPrefix(target, "/events") || strings.Contains(target, "session/revoke") || strings.Contains(target, "/logout") {
			continue
		}
		seen[target] = true
		response := get(t, mux, target)
		if response.Code != 200 || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
			continue
		}
		policy := response.Header().Get("Content-Security-Policy")
		body := response.Body.String()
		for _, match := range renderedScript.FindAllStringSubmatch(body, -1) {
			if strings.Contains(match[1], "src=") {
				t.Fatalf("%s loads an external script, which the policy does not allow: %s", target, match[0][:min(len(match[0]), 120)])
			}
			digest := sha256.Sum256([]byte(match[2]))
			hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
			if !strings.Contains(policy, hash) {
				t.Fatalf("%s renders an inline script its policy blocks (%s): %.160s", target, hash, match[2])
			}
			checked++
		}
		for _, link := range renderedHref.FindAllStringSubmatch(body, -1) {
			next := strings.ReplaceAll(link[1], "&amp;", "&")
			if _, err := url.Parse(next); err == nil && !seen[next] {
				queue = append(queue, next)
			}
		}
	}
	if checked == 0 || !seen["/app/notifications"] {
		t.Fatalf("the crawl checked %d scripts across %d pages; it must reach the workspace and the notification preferences", checked, len(seen))
	}
}
