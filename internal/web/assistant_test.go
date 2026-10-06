package web

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// An assistant's status shows in the thread with the first of its loading
// messages beneath it, and the whole list rides on the element for the page
// script to rotate. The rotating line is hidden from assistive technology,
// which reads the status line instead of a line that changes every few
// seconds; a cleared status takes the loading messages with it.
func TestThreadShowsTheAssistantStatusWithItsLoadingMessages(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	root := seedMessage(t, s, "Massistant-root", "help me deploy", time.Now().UTC().Add(-time.Minute))
	thread := domain.NewMessageTimestamp(root.CreatedAt)
	write := func(status string, loading []string) {
		t.Helper()
		value := domain.AssistantThread{WorkspaceID: "T1", Conversation: "Cdev", ThreadTimestamp: thread, Status: status, LoadingMessages: loading, UpdatedAt: time.Now().UTC()}
		if err := s.SetAssistantThread(context.Background(), value, domain.AssistantThreadStatus, events.Event{ID: domain.EventID("Eassistant-" + status), WorkspaceID: "T1", Topic: events.AssistantThreadUpdatedTopic, Payload: "{}", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	page := func() string {
		t.Helper()
		return get(t, mux, "/app?channel=Cdev&thread="+url.QueryEscape(string(thread))).Body.String()
	}
	write("is thinking...", []string{"Reading the runbook", "Checking the deploy"})
	requireContains(t, "assistant status", page(),
		`<p class="assistant-status" role="status">is thinking...</p>`,
		`<p class="assistant-loading" aria-hidden="true" data-assistant-loading="[&#34;Reading the runbook&#34;,&#34;Checking the deploy&#34;]">Reading the runbook</p>`,
	)
	write("", nil)
	if body := page(); strings.Contains(body, `class="assistant-loading"`) || strings.Contains(body, `class="assistant-status"`) {
		t.Fatal("a cleared status left its loading messages on the page")
	}
}

// An assistant's status shows under the name it was set as: the app user that
// set it, or the username of the identity override it was set with, with the
// override's icon before it — the icon_emoji drawn, or the icon_url as a
// decorative image with an empty alt, since the name beside it is the text.
// The state is its own live region, so the fragment shows each write.
func TestTheAssistantStatusShowsTheIdentityItWasSetWith(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	chat := service.Messages{Store: s}
	seedAgentApp(t, s, "AG1", "UB1", "Cdev", false)
	root, err := chat.Post(ctx, "T1", "U1", "Cdev", "help me deploy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.NewMessageTimestamp(root.CreatedAt)
	fragment := "/app/assistant-thread?" + url.Values{"channel": {"Cdev"}, "thread": {string(thread)}}.Encode()
	page := get(t, mux, "/app?channel=Cdev&thread="+url.QueryEscape(string(thread))).Body.String()
	requireContains(t, "thread without assistant state", page, `id="assistant-thread"`, `data-fragment="/app/assistant-thread?channel=Cdev&amp;thread=`)
	requireMissing(t, "thread without assistant state", page, `class="assistant-state"`)

	set := func(status string, identity domain.AgentIdentity) string {
		t.Helper()
		if err := chat.SetAssistantThreadStatus(ctx, "T1", "UB1", "Cdev", thread, status, nil, identity); err != nil {
			t.Fatal(err)
		}
		return get(t, mux, fragment).Body.String()
	}
	requireContains(t, "the app's own name", set("is thinking...", domain.AgentIdentity{}),
		`<p class="assistant-status" role="status"><span class="assistant-status-name">bot-AG1</span> is thinking...</p>`)

	body := set("is thinking...", domain.AgentIdentity{Username: "Deploy bot", IconEmoji: ":robot_face:", IconURL: "https://example.test/bot.png"})
	requireContains(t, "an emoji override", body, `<span aria-hidden="true">🤖</span> <span class="assistant-status-name">Deploy bot</span> is thinking...`)
	requireMissing(t, "an emoji override", body, "bot-AG1", `example.test/bot.png`)

	body = set("is reading...", domain.AgentIdentity{IconURL: "https://example.test/bot.png"})
	requireContains(t, "an image override", body, `<img class="agent-identity-icon" src="https://example.test/bot.png" alt="" loading="lazy"> <span class="assistant-status-name">bot-AG1</span> is reading...`)

	body = set("", domain.AgentIdentity{Username: "Deploy bot"})
	requireMissing(t, "a cleared status", body, `class="assistant-status"`, "Deploy bot", "agent-identity-icon")
	requireContains(t, "a cleared status", body, `class="assistant-state"`)
}

// The agent session panel shows an agent's icon_url the way the assistant
// status does; before, an override with only an image showed no icon at all.
func TestTheAgentSessionPanelShowsAnIconURLOverride(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	chat := service.Messages{Store: s}
	seedAgentApp(t, s, "AG1", "UB1", "Cdev", false)
	root, err := chat.Post(ctx, "T1", "U1", "Cdev", "plan a trip", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.NewMessageTimestamp(root.CreatedAt)
	if _, err := chat.SetAgentSessionStatus(ctx, "T1", "UB1", "AG1", "Cdev", thread, domain.AgentSessionStatusRequest{Status: domain.AgentSessionActive, Identity: domain.AgentIdentity{IconURL: "https://example.test/agent.png"}}); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app/agent-session?"+url.Values{"channel": {"Cdev"}, "thread": {string(thread)}}.Encode()).Body.String()
	requireContains(t, "an image override", body, `<img class="agent-identity-icon" src="https://example.test/agent.png" alt="" loading="lazy"> <span class="agent-session-agent">Trip Agent</span>`)
}
