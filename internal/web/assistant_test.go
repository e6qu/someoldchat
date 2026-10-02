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
		if err := s.SetAssistantThread(context.Background(), value, domain.AssistantThreadStatus, events.Event{ID: domain.EventID("Eassistant-" + status), WorkspaceID: "T1", Topic: "assistant.thread_updated", Payload: "{}", CreatedAt: time.Now().UTC()}); err != nil {
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
