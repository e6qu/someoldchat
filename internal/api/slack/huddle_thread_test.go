package slack

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// A huddle posts a huddle_thread message into its conversation, as Slack's
// does, and the huddle's chat is that message's thread: conversations.history
// reports the subtype and the thread, and conversations.replies reads the chat.
func TestHuddleMessageCarriesTheHuddleChat(t *testing.T) {
	handler, s := testHandlerWithStore()
	ctx := context.Background()
	messages := service.Messages{Store: s}
	call, err := messages.StartHuddle(ctx, "T1", "U1", "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	if call.ThreadTimestamp == "" {
		t.Fatal("the huddle has no thread")
	}
	if _, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{Conversation: "C1", Text: "notes from the huddle", ThreadTimestamp: call.ThreadTimestamp}); err != nil {
		t.Fatal(err)
	}
	decode := func(method string, form url.Values) map[string]any {
		t.Helper()
		response := callSlackForm(t, handler, "/api/"+method, form.Encode())
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload["ok"] != true {
			t.Fatalf("%s: %s", method, response.Body)
		}
		return payload
	}
	history := decode("conversations.history", url.Values{"channel": {"C1"}})
	var huddle map[string]any
	for _, raw := range history["messages"].([]any) {
		if message := raw.(map[string]any); message["subtype"] == "huddle_thread" {
			huddle = message
		}
	}
	if huddle == nil {
		t.Fatalf("history has no huddle_thread message: %v", history["messages"])
	}
	if huddle["ts"] != string(call.ThreadTimestamp) || huddle["user"] != "U1" || huddle["text"] != "<@U1> started a huddle" {
		t.Fatalf("huddle message=%v", huddle)
	}
	if huddle["thread_ts"] != string(call.ThreadTimestamp) || huddle["reply_count"] != float64(1) {
		t.Fatalf("the huddle message does not carry its chat: %v", huddle)
	}
	replies := decode("conversations.replies", url.Values{"channel": {"C1"}, "ts": {string(call.ThreadTimestamp)}})
	thread := replies["messages"].([]any)
	if len(thread) != 2 || thread[1].(map[string]any)["text"] != "notes from the huddle" {
		t.Fatalf("replies=%v", thread)
	}

	// Joining the running huddle posts nothing more.
	if _, err := messages.StartHuddle(ctx, "T1", "U2", "C1", ""); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, raw := range decode("conversations.history", url.Values{"channel": {"C1"}})["messages"].([]any) {
		if raw.(map[string]any)["subtype"] == "huddle_thread" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("history holds %d huddle messages, want the one the start posted", count)
	}
}
