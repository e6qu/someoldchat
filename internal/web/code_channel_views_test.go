package web

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// A code channel shows its agent's context bar and a tab per view; an html
// view renders in a sandboxed frame whose document has an opaque origin, and
// the other kinds render in the page.
func TestCodeChannelShowsItsAgentsViews(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	conversation := domain.Conversation{ID: "Ccode", WorkspaceID: "T1", Name: "coverage-work", Kind: domain.ConversationKindFor(false, false, false), CreatorID: "U1"}
	record := domain.CodeChannel{WorkspaceID: "T1", Conversation: "Ccode", AppID: "A1", BotUserID: "U1", CreatedAt: now, UpdatedAt: now,
		ContextBar:    []domain.CodeChannelContextItem{{Key: "branch", Label: "agent/coverage", BotUserID: "U1"}, {Key: "ci", Label: "CI", URL: "https://ci.example.com/run/7", BotUserID: "U1"}},
		AgentResource: domain.AgentResource{URL: "https://github.com/borant/billing/pull/42", Title: "Raise coverage"}}
	if err := s.CreateCodeChannel(ctx, conversation, []domain.UserID{"U1"}, record, []events.Event{{ID: "Ecode", WorkspaceID: "T1", Topic: "channel.created", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	view := func(id domain.CodeChannelViewID, key string, kind domain.CodeChannelViewType, label string, set func(*domain.CodeChannelView)) {
		value := domain.CodeChannelView{WorkspaceID: "T1", Conversation: "Ccode", ID: id, FileID: domain.FileID("F" + string(id)), Key: key, Type: kind, Label: label, AppID: "A1", BotUserID: "U1", CreatedAt: now, UpdatedAt: now}
		set(&value)
		if _, err := s.SetCodeChannelView(ctx, value, events.Event{ID: domain.EventID("E" + string(id)), WorkspaceID: "T1", Topic: events.CodeChannelViewSetTopic, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	view("Ct1", "reports/coverage.html", domain.CodeChannelViewHTML, "coverage", func(v *domain.CodeChannelView) {
		v.Content = "<!doctype html><script>document.body.textContent='81%'</script>"
		v.CSP = domain.CodeChannelViewCSP{ResourceDomains: []string{"https://charts.example.com"}, ConnectDomains: []string{"https://api.example.com"}}
	})
	view("Ct2", "diff", domain.CodeChannelViewDiff, "Diff", func(v *domain.CodeChannelView) {
		v.Content, v.BaseBranch, v.HeadBranch = "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old line\n+new line\n", "main", "agent/coverage"
	})
	view("Ct3", "status", domain.CodeChannelViewBlockKit, "Status", func(v *domain.CodeChannelView) {
		v.Blocks = `[{"type":"section","text":{"type":"plain_text","text":"Tests are green"}}]`
	})

	page := get(t, mux, "/app?channel=Ccode").Body.String()
	requireContains(t, "code channel header", page,
		`<ul class="code-context-bar" aria-label="Agent context">`, `href="https://github.com/borant/billing/pull/42"`, "Raise coverage",
		"<span>agent/coverage</span>", `href="https://ci.example.com/run/7"`,
		`href="/app?channel=Ccode&amp;tab=view&amp;view=Ct1"`, "<span>coverage</span>", "<span>Diff</span>", "<span>Status</span>")
	requireMissing(t, "a channel that is not a code channel", get(t, mux, "/app?channel=Cdev").Body.String(), `class="code-context-bar"`, "tab=view")

	html := get(t, mux, "/app?channel=Ccode&tab=view&view=Ct1").Body.String()
	requireContains(t, "html view", html, `href="/app?channel=Ccode&amp;tab=view&amp;view=Ct1" aria-current="page"`, `sandbox="allow-scripts"`, `src="/app/code-views/content?channel=Ccode&amp;view=Ct1&amp;v=1"`)
	requireMissing(t, "html view", html, `id="timeline"`, `allow-same-origin`)
	frame := regexp.MustCompile(`<iframe[^>]*>`).FindString(html)
	if strings.Contains(frame, "allow-same-origin") {
		t.Fatalf("the frame shares this origin: %s", frame)
	}

	content := get(t, mux, "/app/code-views/content?channel=Ccode&view=Ct1")
	if content.Code != http.StatusOK || !strings.Contains(content.Body.String(), "81%") {
		t.Fatalf("content status=%d body=%s", content.Code, content.Body)
	}
	policy := content.Header().Get("Content-Security-Policy")
	for _, want := range []string{"sandbox allow-scripts;", "https://charts.example.com", "connect-src 'none'", "frame-ancestors 'self'"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("the view's policy %q lacks %q", policy, want)
		}
	}
	if strings.Contains(policy, "allow-same-origin") || strings.Contains(policy, "https://api.example.com") {
		t.Fatalf("the view's policy %q grants what it must not", policy)
	}
	if got := content.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options=%q", got)
	}
	if !strings.Contains(get(t, mux, "/app?channel=Ccode").Header().Get("Content-Security-Policy"), "frame-src 'self'") {
		t.Fatal("the workspace page cannot frame its views")
	}
	for _, missing := range []string{"/app/code-views/content?channel=Ccode&view=Ct2", "/app/code-views/content?channel=Ccode&view=Ctnone", "/app/code-views/content?channel=Cdev&view=Ct1"} {
		if code := get(t, mux, missing).Code; code != http.StatusNotFound {
			t.Fatalf("%s status=%d, want 404", missing, code)
		}
	}

	diff := get(t, mux, "/app?channel=Ccode&tab=view&view=Ct2").Body.String()
	requireContains(t, "diff view", diff, `<span class="diff-remove">-old line</span>`, `<span class="diff-add">&#43;new line</span>`, `<span class="diff-hunk">@@ -1 &#43;1 @@</span>`, "main ← agent/coverage")
	requireContains(t, "block_kit view", get(t, mux, "/app?channel=Ccode&tab=view&view=Ct3").Body.String(), "Tests are green")
	requireContains(t, "an unknown view shows the messages", get(t, mux, "/app?channel=Ccode&tab=view&view=Ctnone").Body.String(), `id="timeline"`)
}

// A code channel's agent commands are offered in its composer under the
// agent's name, and nowhere else.
func TestCodeChannelComposerOffersItsAgentsCommands(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	if err := s.SeedUser(domain.User{ID: "UAGENT", WorkspaceID: "T1", Name: "reviewer-bot", RealName: "Reviewer"}); err != nil {
		t.Fatal(err)
	}
	conversation := domain.Conversation{ID: "Ccode", WorkspaceID: "T1", Name: "review-work", Kind: domain.ConversationKindFor(false, false, false), CreatorID: "UAGENT"}
	record := domain.CodeChannel{WorkspaceID: "T1", Conversation: "Ccode", AppID: "A1", BotUserID: "UAGENT", CreatedAt: now, UpdatedAt: now,
		Commands: []domain.CodeChannelCommand{{Name: "review", Description: "Review the diff", ArgumentHint: "[path]", AppID: "A1", BotUserID: "UAGENT"}}}
	if err := s.CreateCodeChannel(ctx, conversation, []domain.UserID{"U1", "UAGENT"}, record, []events.Event{{ID: "Ecode", WorkspaceID: "T1", Topic: "channel.created", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	requireContains(t, "code channel composer", get(t, mux, "/app?channel=Ccode").Body.String(),
		`<i data-kind="command" data-name="/review" data-description="Review the diff" data-hint="[path]" data-app="Reviewer"></i>`)
	requireMissing(t, "another channel's composer", get(t, mux, "/app?channel=Cdev").Body.String(), `data-name="/review"`)
}

// Every Slack command this client implements is one an agent may not take,
// so an agent command can never shadow a built-in.
func TestEveryBuiltInCommandIsReservedFromAgents(t *testing.T) {
	for _, command := range builtInSlashCommands() {
		if !domain.IsSlackBuiltinSlashCommand(command.Command) {
			t.Errorf("%s is built in here but an agent could register it", command.Command)
		}
	}
}
