package web

import (
	"context"
	"net/url"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// HUDDLE-02: a huddle's chat is the thread of the message it posts, as in
// Slack. The timeline names the message and opens the thread, the joined
// huddle window opens it too, and once someone replies the message counts the
// replies like any thread.
func TestHuddleMessageOpensTheHuddleThread(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	messages := service.Messages{Store: s}
	call, err := messages.StartHuddle(ctx, "T1", "U1", "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := url.QueryEscape(string(call.ThreadTimestamp))
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	article := articleFor(t, body, call.ThreadTimestamp)
	requireContains(t, "huddle message", article, `data-subtype="huddle_thread"`, "started a huddle.", ">Open the huddle thread<", "thread="+thread)
	requireContains(t, "huddle window", body, `aria-label="Open the huddle thread"`)

	if _, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{Conversation: "Cdev", Text: "agenda first", ThreadTimestamp: call.ThreadTimestamp}); err != nil {
		t.Fatal(err)
	}
	article = articleFor(t, get(t, mux, "/app?channel=Cdev").Body.String(), call.ThreadTimestamp)
	requireContains(t, "huddle message after a reply", article, `class="thread-summary"`, "1 reply")
	requireMissing(t, "huddle message after a reply", article, "Open the huddle thread")

	threadPage := get(t, mux, "/app?channel=Cdev&thread="+thread).Body.String()
	requireContains(t, "huddle thread", threadPage, "agenda first", `data-subtype="huddle_thread"`)
}
