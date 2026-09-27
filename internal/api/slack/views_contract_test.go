package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func viewsFormCall(t *testing.T, handler http.Handler, path string, values url.Values) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s body=%s: %v", path, recorder.Body.String(), err)
	}
	return body
}

func expectSlackError(t *testing.T, label string, body map[string]any, code string) {
	t.Helper()
	if body["ok"] != false || body["error"] != code {
		t.Fatalf("%s = %v, want error %q", label, body, code)
	}
}

func seedExpiredHTTPInteractionTrigger(t *testing.T, target *memory.Store, plaintext string) {
	t.Helper()
	past := time.Now().UTC().Add(-time.Minute)
	if err := target.CreateAppInteractionCapabilities(context.Background(),
		domain.AppTrigger{TokenHash: domain.HashToken(plaintext), AppID: "A1", WorkspaceID: "T1", UserID: "U1", CreatedAt: past, ExpiresAt: past.Add(3 * time.Second)},
		domain.AppResponseURL{TokenHash: domain.HashToken("response-" + plaintext), AppID: "A1", WorkspaceID: "T1", UserID: "U1", ConversationID: "C1", CreatedAt: past, ExpiresAt: past.Add(30 * time.Minute), UsesRemaining: 5},
	); err != nil {
		t.Fatal(err)
	}
}

func testModal(title, extra string) string {
	return `{"type":"modal","title":{"type":"plain_text","text":"` + title + `"}` + extra + `,"blocks":[]}`
}

// The views.* methods answer with the codes Slack's method references name
// for each failure, so Bolt's error handling (and an app's retry on a fresh
// trigger) can tell them apart.
func TestViewsMethodsNameEachFailureAsSlackDoes(t *testing.T) {
	handler, st := testHandlerWithStore()
	call := func(path string, values url.Values) map[string]any { return viewsFormCall(t, handler, path, values) }

	seedHTTPInteractionTrigger(t, st, "t-invalid")
	expectSlackError(t, "view without title", call("/api/views.open", url.Values{"trigger_id": {"t-invalid"}, "view": {`{"type":"modal","blocks":[]}`}}), "invalid_arguments")
	if body := call("/api/views.open", url.Values{"trigger_id": {"t-invalid"}, "view": {testModal("x", "")}}); body["ok"] != true {
		t.Fatalf("a rejected view spent the trigger: %v", body)
	}
	seedHTTPInteractionTrigger(t, st, "t-home")
	expectSlackError(t, "views.open type home", call("/api/views.open", url.Values{"trigger_id": {"t-home"}, "view": {`{"type":"home","blocks":[]}`}}), "invalid_arguments")
	expectSlackError(t, "views.open unknown block", call("/api/views.open", url.Values{"trigger_id": {"t-home"}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"x"},"blocks":[{"type":"bogus"}]}`}}), "invalid_arguments")
	expectSlackError(t, "views.open section without text", call("/api/views.open", url.Values{"trigger_id": {"t-home"}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"x"},"blocks":[{"type":"section"}]}`}}), "invalid_arguments")
	expectSlackError(t, "unknown trigger", call("/api/views.open", url.Values{"trigger_id": {"never-issued"}, "view": {testModal("x", "")}}), "invalid_trigger_id")
	expectSlackError(t, "views.update unknown view", call("/api/views.update", url.Values{"view_id": {"V999"}, "view": {testModal("x", "")}}), "not_found")

	seedHTTPInteractionTrigger(t, st, "t-a")
	opened := call("/api/views.open", url.Values{"trigger_id": {"t-a"}, "view": {testModal("x", `,"external_id":"ext1"`)}})
	view, _ := opened["view"].(map[string]any)
	viewID, _ := view["id"].(string)
	hash, _ := view["hash"].(string)
	if opened["ok"] != true || viewID == "" {
		t.Fatalf("open = %v", opened)
	}
	if body := call("/api/views.update", url.Values{"view_id": {viewID}, "hash": {hash}, "view": {testModal("y", "")}}); body["ok"] != true {
		t.Fatalf("first update = %v", body)
	}
	expectSlackError(t, "stale hash", call("/api/views.update", url.Values{"view_id": {viewID}, "hash": {hash}, "view": {testModal("z", "")}}), "hash_conflict")
	expectSlackError(t, "reused trigger", call("/api/views.open", url.Values{"trigger_id": {"t-a"}, "view": {testModal("x", "")}}), "exchanged_trigger_id")
	seedExpiredHTTPInteractionTrigger(t, st, "t-exp")
	expectSlackError(t, "expired trigger", call("/api/views.open", url.Values{"trigger_id": {"t-exp"}, "view": {testModal("x", "")}}), "expired_trigger_id")
	seedHTTPInteractionTrigger(t, st, "t-dup")
	expectSlackError(t, "duplicate external_id", call("/api/views.open", url.Values{"trigger_id": {"t-dup"}, "view": {testModal("x", `,"external_id":"ext1"`)}}), "duplicate_external_id")
	expectSlackError(t, "modal to home", call("/api/views.update", url.Values{"view_id": {viewID}, "view": {`{"type":"home","blocks":[]}`}}), "invalid_arguments")

	for index, trigger := range []string{"t-push-1", "t-push-2", "t-push-3"} {
		seedHTTPInteractionTrigger(t, st, trigger)
		body := call("/api/views.push", url.Values{"trigger_id": {trigger}, "view": {testModal("p", "")}})
		if index < 2 && body["ok"] != true {
			t.Fatalf("push %d = %v", index, body)
		}
		if index == 2 {
			expectSlackError(t, "fourth view on the stack", body, "push_limit_reached")
		}
	}
	expectSlackError(t, "publish unknown user", call("/api/views.publish", url.Values{"user_id": {"UNOPE"}, "view": {`{"type":"home","blocks":[]}`}}), "invalid_arguments")
	expectSlackError(t, "publish modal", call("/api/views.publish", url.Values{"user_id": {"U1"}, "view": {testModal("x", "")}}), "invalid_arguments")

	seedHTTPInteractionTrigger(t, st, "t-dialog")
	for name, invalid := range map[string]string{
		"element without a name": `{"callback_id":"c","title":"T","elements":[{"type":"text","label":"L"}]}`,
		"title over 24":          `{"callback_id":"c","title":"` + strings.Repeat("t", 25) + `","elements":[{"type":"text","label":"L","name":"n"}]}`,
		"eleven elements":        `{"callback_id":"c","title":"T","elements":[` + strings.TrimSuffix(strings.Repeat(`{"type":"text","label":"L","name":"n"},`, 11), ",") + `]}`,
		"select without options": `{"callback_id":"c","title":"T","elements":[{"type":"select","label":"L","name":"n"}]}`,
	} {
		expectSlackError(t, "dialog.open "+name, call("/api/dialog.open", url.Values{"trigger_id": {"t-dialog"}, "dialog": {invalid}}), "validation_errors")
	}
	dialog := `{"callback_id":"c","title":"T","elements":[{"type":"text","label":"L","name":"n"}]}`
	if body := call("/api/dialog.open", url.Values{"trigger_id": {"t-dialog"}, "dialog": {dialog}}); body["ok"] != true {
		t.Fatalf("dialog.open = %v", body)
	}
	expectSlackError(t, "dialog reused trigger", call("/api/dialog.open", url.Values{"trigger_id": {"t-dialog"}, "dialog": {dialog}}), "trigger_exchanged")
	seedExpiredHTTPInteractionTrigger(t, st, "t-dialog-expired")
	expectSlackError(t, "dialog expired trigger", call("/api/dialog.open", url.Values{"trigger_id": {"t-dialog-expired"}, "dialog": {dialog}}), "trigger_expired")
}

// views.update carries entered values for elements the new view keeps.
func TestViewsUpdatePreservesEnteredState(t *testing.T) {
	handler, st := testHandlerWithStore()
	seedHTTPInteractionTrigger(t, st, "t-state")
	input := `{"type":"input","block_id":"b","label":{"type":"plain_text","text":"L"},"element":{"type":"plain_text_input","action_id":"a"}}`
	opened := viewsFormCall(t, handler, "/api/views.open", url.Values{"trigger_id": {"t-state"}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"x"},"submit":{"type":"plain_text","text":"Go"},"blocks":[` + input + `]}`}})
	view, _ := opened["view"].(map[string]any)
	stored, err := st.GetView(context.Background(), "T1", domain.ViewID(view["id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	stored.State = `{"values":{"b":{"a":{"type":"plain_text_input","value":"typed"}}}}`
	if _, err := st.UpdateView(context.Background(), stored, "", events.Event{ID: "E-state", WorkspaceID: "T1", ActorID: "U1", Topic: "view.updated", Payload: `{"view_id":"x"}`, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	updated := viewsFormCall(t, handler, "/api/views.update", url.Values{"view_id": {string(stored.ID)}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"x"},"submit":{"type":"plain_text","text":"Go"},"blocks":[` + input + `,{"type":"section","text":{"type":"mrkdwn","text":"added"}}]}`}})
	state, _ := json.Marshal(updated["view"].(map[string]any)["state"])
	if string(state) != `{"values":{"b":{"a":{"type":"plain_text_input","value":"typed"}}}}` {
		t.Fatalf("state after update = %s", state)
	}
}
