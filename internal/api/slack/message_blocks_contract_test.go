package slack

import (
	"net/url"
	"strings"
	"testing"
)

// Message write methods answer invalid_blocks for blocks that are not valid
// Block Kit, including a message over Slack's 50-block limit and a blocks
// argument that is not a JSON array of objects.
func TestMessageMethodsAnswerInvalidBlocks(t *testing.T) {
	handler, _ := testHandlerWithStore()
	call := func(path string, values url.Values) map[string]any { return viewsFormCall(t, handler, path, values) }
	dividers := "[" + strings.TrimSuffix(strings.Repeat(`{"type":"divider"},`, 51), ",") + "]"
	for name, blocks := range map[string]string{
		"51 blocks":            dividers,
		"unknown block":        `[{"type":"bogus"},{"type":"section"}]`,
		"not an array":         `{"type":"x"}`,
		"section without text": `[{"type":"section"}]`,
	} {
		expectSlackError(t, "chat.postMessage "+name, call("/api/chat.postMessage", url.Values{"channel": {"C1"}, "text": {"x"}, "blocks": {blocks}}), "invalid_blocks")
		expectSlackError(t, "chat.postEphemeral "+name, call("/api/chat.postEphemeral", url.Values{"channel": {"C1"}, "user": {"U1"}, "text": {"x"}, "blocks": {blocks}}), "invalid_blocks")
		expectSlackError(t, "chat.scheduleMessage "+name, call("/api/chat.scheduleMessage", url.Values{"channel": {"C1"}, "text": {"x"}, "post_at": {"4102444800"}, "blocks": {blocks}}), "invalid_blocks")
	}
	posted := call("/api/chat.postMessage", url.Values{"channel": {"C1"}, "text": {"x"}, "blocks": {`[{"type":"section","text":{"type":"mrkdwn","text":"ok"}}]`}})
	if posted["ok"] != true {
		t.Fatalf("valid post = %v", posted)
	}
	ts, _ := posted["ts"].(string)
	expectSlackError(t, "chat.update malformed", call("/api/chat.update", url.Values{"channel": {"C1"}, "ts": {ts}, "blocks": {`{"type":"x"}`}}), "invalid_blocks")
	expectSlackError(t, "chat.update invalid", call("/api/chat.update", url.Values{"channel": {"C1"}, "ts": {ts}, "blocks": {`[{"type":"bogus"}]`}}), "invalid_blocks")
}
