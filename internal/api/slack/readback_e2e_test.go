package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A mutation that answers ok:true has told you it was accepted. It has not told
// you it happened.
//
// Six of the seven functional holes this project found by hand had a passing
// test in front of them, and the shape was always the same: the test called the
// mutation, saw ok:true, and stopped. Restricting an app answered ok:true and
// changed nothing. Setting a channel's posting policy answered ok:true and
// changed nothing. Approving a lapsed invitation answered ok:true. Each of
// those tests would still pass today if the handler were replaced by one that
// writes nothing and returns ok:true.
//
// Every journey below therefore mutates through the HTTP API and then reads the
// state back through a DIFFERENT method, asserting the value it wrote. Reading
// back through the same method would only prove the method agrees with itself.
type readBack struct {
	// name says what a reader should conclude when it fails.
	name string
	// mutate is the call under test.
	mutate string
	form   url.Values
	// read is the method that must be able to see the effect, and it must not
	// be the method that made it.
	read     string
	readForm url.Values
	// expect is what the read must contain. A substring rather than a shape:
	// the shape is pinned by the method's own tests, and what this asks is
	// whether the value crossed from one method to another at all.
	expect string
}

func TestEveryMutationIsVisibleToAnotherMethod(t *testing.T) {
	for _, journey := range readBackJourneys() {
		t.Run(journey.name, func(t *testing.T) {
			if journey.mutate == journey.read {
				t.Fatalf("%s reads back through the method that wrote it, which only proves the method agrees with itself", journey.name)
			}
			handler, _ := testHandlerWithStore()
			mutation := readBackCall(t, handler, journey.mutate, journey.form)
			if ok, _ := mutation["ok"].(bool); !ok {
				t.Fatalf("%s answered %v", journey.mutate, mutation)
			}
			readback := readBackCall(t, handler, journey.read, journey.readForm)
			if ok, _ := readback["ok"].(bool); !ok {
				t.Fatalf("%s answered %v", journey.read, readback)
			}
			encoded, err := json.Marshal(readback)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), journey.expect) {
				t.Fatalf("%s accepted the change and %s cannot see it: expected %q in %s",
					journey.mutate, journey.read, journey.expect, encoded)
			}
		})
	}
}

func readBackJourneys() []readBack {
	return []readBack{
		{
			name:   "a posted message is in the conversation's history",
			mutate: "/api/chat.postMessage", form: url.Values{"channel": {"C1"}, "text": {"readback-message"}},
			read: "/api/conversations.history", readForm: url.Values{"channel": {"C1"}},
			expect: "readback-message",
		},
		{
			name:   "a created channel is in the conversation list",
			mutate: "/api/conversations.create", form: url.Values{"name": {"readback-channel"}},
			read: "/api/conversations.list", readForm: url.Values{},
			expect: "readback-channel",
		},
		{
			name:   "a channel's new topic is on the channel",
			mutate: "/api/conversations.setTopic", form: url.Values{"channel": {"C1"}, "topic": {"readback-topic"}},
			read: "/api/conversations.info", readForm: url.Values{"channel": {"C1"}},
			expect: "readback-topic",
		},
		{
			name:   "a channel's new purpose is on the channel",
			mutate: "/api/conversations.setPurpose", form: url.Values{"channel": {"C1"}, "purpose": {"readback-purpose"}},
			read: "/api/conversations.info", readForm: url.Values{"channel": {"C1"}},
			expect: "readback-purpose",
		},
		{
			name:   "a renamed channel answers to its new name",
			mutate: "/api/conversations.rename", form: url.Values{"channel": {"C1"}, "name": {"readback-renamed"}},
			read: "/api/conversations.info", readForm: url.Values{"channel": {"C1"}},
			expect: "readback-renamed",
		},
		{
			name:   "a bookmark is in the channel's bookmark list",
			mutate: "/api/bookmarks.add", form: url.Values{"channel_id": {"C1"}, "title": {"readback-bookmark"}, "type": {"link"}, "link": {"https://example.test/x"}},
			read: "/api/bookmarks.list", readForm: url.Values{"channel_id": {"C1"}},
			expect: "readback-bookmark",
		},
		{
			name:   "a user group is in the user group list",
			mutate: "/api/usergroups.create", form: url.Values{"name": {"readback-group"}, "handle": {"readback-group"}},
			read: "/api/usergroups.list", readForm: url.Values{},
			expect: "readback-group",
		},
		{
			name:   "a changed profile field is on the user",
			mutate: "/api/users.profile.set", form: url.Values{"profile": {`{"status_text":"readback-status"}`}},
			read: "/api/users.info", readForm: url.Values{"user": {"U1"}},
			expect: "readback-status",
		},
		{
			name:   "a do-not-disturb snooze is visible to the DND read",
			mutate: "/api/dnd.setSnooze", form: url.Values{"num_minutes": {"30"}},
			read: "/api/dnd.info", readForm: url.Values{},
			expect: `"snooze_enabled":true`,
		},
		{
			name:   "a custom emoji is in the emoji list",
			mutate: "/api/admin.emoji.add", form: url.Values{"name": {"readback-emoji"}, "url": {"https://example.test/e.png"}},
			read: "/api/emoji.list", readForm: url.Values{},
			expect: "readback-emoji",
		},
	}
}

func readBackCall(t *testing.T, handler http.Handler, path string, form url.Values) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
	}
	return payload
}
