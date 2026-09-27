package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// seedSocketModeModalApp installs a Socket Mode app with interactivity so a
// test can read the interaction payloads the web client produces.
func seedSocketModeModalApp(t *testing.T, s *memory.Store, features string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	key := []byte(strings.Repeat("k", 32))
	signing, err := secretbox.Seal(key, "app:A1:signing-secret", "signing-secret")
	if err != nil {
		t.Fatal(err)
	}
	verification, err := secretbox.Seal(key, "app:A1:verification-token", "verification-token")
	if err != nil {
		t.Fatal(err)
	}
	if features != "" {
		features = `"features":` + features + `,`
	}
	manifest := `{"display_information":{"name":"Modal app"},` + features + `"oauth_config":{"scopes":{"bot":["commands"]}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
	if err := s.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Modal app", ClientID: "modal-client",
		SigningSecretHash: domain.HashToken("signing-secret"), SigningSecretCiphertext: signing,
		VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verification,
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "modal-client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func seedOpenModal(t *testing.T, s *memory.Store, id domain.ViewID, payload string) {
	t.Helper()
	now := time.Now().UTC()
	view := domain.View{ID: id, AppID: "A1", WorkspaceID: "T1", UserID: "U1", Type: "modal", Payload: payload, Hash: "h-" + string(id), RootViewID: id, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateView(context.Background(), view, events.Event{ID: domain.EventID("E-" + string(id)), WorkspaceID: "T1", Topic: "view.opened", Payload: string(id), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func claimInteraction(t *testing.T, s *memory.Store) map[string]any {
	t.Helper()
	interaction, found, err := s.ClaimSocketModeInteraction(context.Background(), "A1", "modal-client", time.Minute)
	if err != nil || !found {
		t.Fatalf("no interaction: found=%v err=%v", found, err)
	}
	if err := s.AckSocketModeInteraction(context.Background(), "A1", interaction.EnvelopeID, "modal-client"); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(interaction.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func viewState(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	view, _ := payload["view"].(map[string]any)
	state, ok := view["state"].(map[string]any)
	if !ok {
		t.Fatalf("interaction view has no state: %v", payload)
	}
	values, ok := state["values"].(map[string]any)
	if !ok {
		t.Fatalf("interaction view.state has no values: %v", state)
	}
	return values
}

// Every view an interaction payload carries includes view.state, as Slack's
// does; Bolt reads view.state.values without checking for it.
func TestViewBlockActionCarriesViewState(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vb", `{"type":"modal","title":{"type":"plain_text","text":"P"},"submit":{"type":"plain_text","text":"Go"},"blocks":[{"type":"input","block_id":"b","label":{"type":"plain_text","text":"L"},"element":{"type":"plain_text_input","action_id":"a"}},{"type":"actions","block_id":"act","elements":[{"type":"button","action_id":"btn","text":{"type":"plain_text","text":"Btn"},"value":"v"}]}]}`)
	response := postForm(t, mux, "/app/view/action?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vb"}, "modal_action": {"0"}, "input_0": {"typed"}}.Encode(), false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	values := viewState(t, claimInteraction(t, s))
	if value := values["b"].(map[string]any)["a"].(map[string]any)["value"]; value != "typed" {
		t.Fatalf("view.state.values = %v", values)
	}
}

// Closing is the user's action: it works after the app is uninstalled, and
// no view_closed is attempted for an app that can no longer receive one.
func TestModalClosesAfterItsAppIsUninstalled(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vc", `{"type":"modal","title":{"type":"plain_text","text":"Info"},"notify_on_close":true,"blocks":[]}`)
	if err := s.UninstallApp(context.Background(), "T1", "A1"); err != nil {
		t.Fatal(err)
	}
	response := postForm(t, mux, "/app/view/close?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vc"}, "clear": {"false"}}.Encode(), false)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("close status=%d body=%s", response.Code, response.Body)
	}
	requireMissing(t, "closed modal", get(t, mux, "/app?channel=Cdev").Body.String(), `role="dialog"`)
}

// An option loaded through block_suggestion reports its text in the
// submission, as Slack does; text the browser changed is dropped.
func TestExternalSelectSubmissionCarriesLoadedOptionText(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vx", `{"type":"modal","title":{"type":"plain_text","text":"Ext"},"submit":{"type":"plain_text","text":"Go"},"blocks":[{"type":"input","block_id":"ex","label":{"type":"plain_text","text":"Ext"},"element":{"type":"external_select","action_id":"exa","min_query_length":0}}]}`)
	answered := answerNextInteraction(s, `{"options":[{"text":{"type":"plain_text","text":"Option One"},"value":"one"}]}`)
	loaded := postForm(t, mux, "/app/options?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "app_id": {"A1"}, "view_id": {"Vx"}, "block_id": {"ex"}, "action_id": {"exa"}, "channel": {"Cdev"}, "query": {"on"},
	}.Encode(), false)
	if suggestion := <-answered; suggestion.err != nil {
		t.Fatal(suggestion.err)
	}
	var options struct {
		Options []struct{ Text, Value, Choice string } `json:"options"`
	}
	if loaded.Code != http.StatusOK || json.Unmarshal(loaded.Body.Bytes(), &options) != nil || len(options.Options) != 1 || options.Options[0].Choice == "" {
		t.Fatalf("options status=%d body=%s", loaded.Code, loaded.Body)
	}
	submit := func(choice string) map[string]any {
		response := postForm(t, mux, "/app/view/submit?channel=Cdev", url.Values{
			"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vx"}, "input_0": {choice},
		}.Encode(), false)
		if response.Code != http.StatusAccepted {
			t.Fatalf("submit status=%d body=%s", response.Code, response.Body)
		}
		values := viewState(t, claimInteraction(t, s))
		return values["ex"].(map[string]any)["exa"].(map[string]any)["selected_option"].(map[string]any)
	}
	selected := submit(options.Options[0].Choice)
	if selected["value"] != "one" || selected["text"].(map[string]any)["text"] != "Option One" || selected["token"] != nil {
		t.Fatalf("selected_option = %v", selected)
	}
	forged, _ := decodeExternalChoice(options.Options[0].Choice)
	forged.Text = "Something else"
	selected = submit(encodeExternalChoice(forged))
	if selected["value"] != "one" || selected["text"] != nil {
		t.Fatalf("forged selected_option = %v", selected)
	}
}

// answered is what answerNextInteraction saw: the payload of the interaction
// it answered, or why it could not.
type answered struct {
	payload map[string]any
	err     error
}

// answerNextInteraction plays a Socket Mode app: it claims the next queued
// interaction and acknowledges it with body, as an app answering an options
// request does while the client waits for the response.
func answerNextInteraction(s *memory.Store, body string) <-chan answered {
	result := make(chan answered, 1)
	messages := service.Messages{Store: s, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			interaction, found, err := s.ClaimSocketModeInteraction(context.Background(), "A1", "modal-client", time.Minute)
			if err != nil {
				result <- answered{err: err}
				return
			}
			if found {
				var payload map[string]any
				if err := json.Unmarshal([]byte(interaction.Payload), &payload); err != nil {
					result <- answered{err: err}
					return
				}
				if err := messages.HandleSocketModeResponse(context.Background(), "A1", interaction.EnvelopeID, []byte(body)); err != nil {
					result <- answered{err: err}
					return
				}
				result <- answered{payload: payload, err: s.AckSocketModeInteraction(context.Background(), "A1", interaction.EnvelopeID, "modal-client")}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		result <- answered{err: errors.New("no interaction arrived")}
	}()
	return result
}

// An external select in a message reports the chosen option's text in
// block_actions, as Slack does, vouched for by the token the service issued
// when the option was loaded; a token for another message, or text the
// browser changed, reports the value alone.
func TestMessageExternalSelectActionCarriesLoadedOptionText(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	now := time.Now().UTC()
	for index, id := range []domain.MessageID{"Mext", "Mother"} {
		message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", AppID: "A1", Text: "pick", CreatedAt: now.Add(time.Duration(index) * time.Millisecond),
			Blocks: `[{"type":"actions","block_id":"eb","elements":[{"type":"external_select","action_id":"ea","min_query_length":0},{"type":"multi_external_select","action_id":"ma","min_query_length":0}]}]`}
		if err := s.CreateMessage(context.Background(), message, events.Event{ID: domain.EventID("E-" + string(id)), WorkspaceID: "T1", Topic: "message.created", Payload: string(id), CreatedAt: now}, ""); err != nil {
			t.Fatal(err)
		}
	}
	load := func(messageID, actionID string) []string {
		t.Helper()
		suggestion := answerNextInteraction(s, `{"options":[{"text":{"type":"plain_text","text":"Option One"},"value":"one"},{"text":{"type":"plain_text","text":"Option Two"},"value":"two"}]}`)
		loaded := postForm(t, mux, "/app/options?channel=Cdev", url.Values{
			"_csrf": {auth.CSRFToken("session")}, "app_id": {"A1"}, "message_id": {messageID}, "block_id": {"eb"}, "action_id": {actionID}, "channel": {"Cdev"}, "query": {"o"},
		}.Encode(), false)
		if answer := <-suggestion; answer.err != nil || answer.payload["type"] != "block_suggestion" {
			t.Fatalf("block_suggestion = %v err=%v", answer.payload, answer.err)
		}
		var options struct {
			Options []struct{ Text, Value, Choice string } `json:"options"`
		}
		if loaded.Code != http.StatusOK || json.Unmarshal(loaded.Body.Bytes(), &options) != nil || len(options.Options) != 2 {
			t.Fatalf("options status=%d body=%s", loaded.Code, loaded.Body)
		}
		choices := make([]string, 0, len(options.Options))
		for _, option := range options.Options {
			if option.Choice == "" {
				t.Fatalf("a message option carries no vouched choice: %s", loaded.Body)
			}
			choices = append(choices, option.Choice)
		}
		return choices
	}
	act := func(actionType, actionID string, values ...string) map[string]any {
		t.Helper()
		response := postForm(t, mux, "/app/interaction", url.Values{
			"_csrf": {auth.CSRFToken("session")}, "message_id": {"Mext"}, "app_id": {"A1"}, "block_id": {"eb"}, "action_id": {actionID},
			"action_type": {actionType}, "channel": {"Cdev"}, "value": values,
		}.Encode(), false)
		if response.Code >= 400 {
			t.Fatalf("dispatch status=%d body=%s", response.Code, response.Body)
		}
		payload := claimInteraction(t, s)
		action := payload["actions"].([]any)[0].(map[string]any)
		state := payload["state"].(map[string]any)["values"].(map[string]any)["eb"].(map[string]any)[actionID].(map[string]any)
		if actionType == "external_select" {
			if state["selected_option"] == nil {
				t.Fatalf("state carries no selected_option: %v", payload["state"])
			}
			return action["selected_option"].(map[string]any)
		}
		return map[string]any{"selected_options": action["selected_options"]}
	}
	choices := load("Mext", "ea")
	if selected := act("external_select", "ea", choices[0]); selected["value"] != "one" || selected["text"].(map[string]any)["text"] != "Option One" || selected["token"] != nil {
		t.Fatalf("selected_option = %v", selected)
	}
	forged, _ := decodeExternalChoice(choices[0])
	forged.Text = "Something else"
	if selected := act("external_select", "ea", encodeExternalChoice(forged)); selected["value"] != "one" || selected["text"] != nil {
		t.Fatalf("forged selected_option = %v", selected)
	}
	elsewhere := load("Mother", "ea")
	if selected := act("external_select", "ea", elsewhere[0]); selected["value"] != "one" || selected["text"] != nil {
		t.Fatalf("another message's option = %v", selected)
	}
	// An ephemeral app message's external select loads options too: the
	// options request resolves the message the way the action does.
	ephemeral := domain.EphemeralMessage{ID: "Meph", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", AppID: "A1", RecipientID: "U1", Text: "only you",
		Blocks: `[{"type":"actions","block_id":"eb","elements":[{"type":"external_select","action_id":"ea","min_query_length":0}]}]`, Timestamp: domain.NewMessageTimestamp(now), CreatedAt: now}
	if err := s.CreateEphemeralMessage(context.Background(), ephemeral, events.Event{ID: "E-Meph", WorkspaceID: "T1", Topic: "ephemeral_message", Payload: `{"type":"ephemeral_message","user_id":"U1"}`, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	load("Meph", "ea")
	multi := load("Mext", "ma")
	selected := act("multi_external_select", "ma", multi...)["selected_options"].([]any)
	if len(selected) != 2 || selected[0].(map[string]any)["text"].(map[string]any)["text"] != "Option One" || selected[1].(map[string]any)["value"] != "two" || selected[1].(map[string]any)["text"].(map[string]any)["text"] != "Option Two" {
		t.Fatalf("selected_options = %v", selected)
	}
}

// A dialog.open dialog renders in the client, submits dialog_submission with
// Slack's submission map, shows the errors the app answers with, closes on an
// empty acknowledgement, and sends dialog_cancellation when asked to.
func TestLegacyDialogRendersSubmitsAndCancels(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	ctx := context.Background()
	messages := service.Messages{Store: s, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	openDialog := func(id domain.DialogID, payload string) {
		t.Helper()
		if err := s.CreateDialog(ctx, domain.Dialog{ID: id, WorkspaceID: "T1", UserID: "U1", AppID: "A1", Payload: payload, CreatedAt: time.Now().UTC()},
			events.Event{ID: domain.EventID("E-" + string(id)), WorkspaceID: "T1", Topic: "dialog.opened", Payload: `{"type":"dialog.opened","user_id":"U1"}`, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	openDialog("Dl1", `{"callback_id":"ticket","title":"File a ticket","submit_label":"File","state":"s1","elements":[`+
		`{"type":"text","name":"summary","label":"Summary","min_length":3},`+
		`{"type":"textarea","name":"details","label":"Details","optional":true},`+
		`{"type":"select","name":"priority","label":"Priority","options":[{"label":"High","value":"high"},{"label":"Low","value":"low"}]},`+
		`{"type":"select","name":"owner","label":"Owner","data_source":"users","optional":true},`+
		`{"type":"select","name":"remote","label":"Remote","data_source":"external","optional":true}]}`)
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "dialog", body, "File a ticket", `action="/app/dialog/submit?channel=Cdev"`, `name="dialog_id" value="Dl1"`,
		"Summary", "Details", `<option value="high"`, "Priority", "Owner", ">File</button>",
		`data-dialog-id="Dl1" data-block-id="remote" data-action-id="remote"`, `data-min-query="1"`)
	requireMissing(t, "dialog", body, "cannot load this app")
	// The external select loads its options through dialog_suggestion: the
	// app answers with Slack's label/value options, and the choice the member
	// makes is reported in dialog_submission as its value.
	suggestion := answerNextInteraction(s, `{"option_groups":[{"label":"Hosts","options":[{"label":"Build host","value":"build-1"}]}]}`)
	loaded := postForm(t, mux, "/app/options?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "app_id": {"A1"}, "dialog_id": {"Dl1"}, "block_id": {"remote"}, "action_id": {"remote"}, "channel": {"Cdev"}, "query": {"bu"},
	}.Encode(), false)
	answer := <-suggestion
	if answer.err != nil {
		t.Fatal(answer.err)
	}
	if answer.payload["type"] != "dialog_suggestion" || answer.payload["name"] != "remote" || answer.payload["value"] != "bu" ||
		answer.payload["callback_id"] != "ticket" || answer.payload["state"] != "s1" || answer.payload["token"] != "verification-token" ||
		answer.payload["team"].(map[string]any)["id"] != "T1" || answer.payload["user"].(map[string]any)["id"] != "U1" ||
		answer.payload["channel"].(map[string]any)["id"] != "Cdev" || answer.payload["action_ts"] == nil || answer.payload["response_url"] != nil {
		t.Fatalf("dialog_suggestion = %v", answer.payload)
	}
	var options struct {
		Options []struct{ Text, Value, Group, Choice string } `json:"options"`
	}
	if loaded.Code != http.StatusOK || json.Unmarshal(loaded.Body.Bytes(), &options) != nil || len(options.Options) != 1 ||
		options.Options[0].Text != "Build host" || options.Options[0].Group != "Hosts" || options.Options[0].Choice == "" {
		t.Fatalf("dialog options status=%d body=%s", loaded.Code, loaded.Body)
	}
	remote := options.Options[0].Choice
	// An element that is not an external select asks for nothing.
	if refused := postForm(t, mux, "/app/options?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "app_id": {"A1"}, "dialog_id": {"Dl1"}, "block_id": {"priority"}, "action_id": {"priority"}, "channel": {"Cdev"}, "query": {"bu"},
	}.Encode(), false); refused.Code != http.StatusNotFound {
		t.Fatalf("options for a static select status=%d body=%s", refused.Code, refused.Body)
	}
	submit := func(values url.Values) *httptest.ResponseRecorder {
		values.Set("_csrf", auth.CSRFToken("session"))
		values.Set("dialog_id", "Dl1")
		return postForm(t, mux, "/app/dialog/submit?channel=Cdev", values.Encode(), false)
	}
	if response := submit(url.Values{"input_0": {"ab"}, "input_2": {"high"}, "input_4": {remote}}); response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), "Enter between 3 and 150 characters.") || !strings.Contains(response.Body.String(), ">Build host</option>") {
		t.Fatalf("short summary status=%d (the loaded choice must survive the re-render)", response.Code)
	}
	// The re-rendered page answers a POST, so it moves its history entry to
	// the conversation: a live reload or a refresh must not submit again.
	if response := submit(url.Values{"input_0": {"ab"}, "input_2": {"high"}}); !strings.Contains(response.Body.String(), `data-canonical-url="/app?channel=Cdev"`) {
		t.Fatalf("a POST-rendered page names no canonical URL")
	}
	requireMissing(t, "a GET page", get(t, mux, "/app?channel=Cdev").Body.String(), `data-canonical-url="`)
	if response := submit(url.Values{"input_0": {"Broken build"}, "input_2": {"high"}, "input_4": {remote}}); response.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body)
	}
	interaction, found, err := s.ClaimSocketModeInteraction(ctx, "A1", "modal-client", time.Minute)
	if err != nil || !found {
		t.Fatalf("no dialog_submission: found=%v err=%v", found, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(interaction.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	submission, _ := payload["submission"].(map[string]any)
	if payload["type"] != "dialog_submission" || payload["callback_id"] != "ticket" || payload["state"] != "s1" ||
		submission["summary"] != "Broken build" || submission["priority"] != "high" || submission["details"] != nil || submission["remote"] != "build-1" ||
		payload["response_url"] == "" || payload["channel"].(map[string]any)["id"] != "Cdev" {
		t.Fatalf("dialog_submission = %v", payload)
	}
	// The app's acknowledgement is applied to the member's open dialog.
	if err := messages.HandleSocketModeResponse(ctx, "A1", interaction.EnvelopeID, []byte(`{"errors":[{"name":"summary","error":"Be more specific"}]}`)); err != nil {
		t.Fatal(err)
	}
	requireContains(t, "dialog errors", get(t, mux, "/app?channel=Cdev").Body.String(), "Be more specific")
	if err := messages.HandleSocketModeResponse(ctx, "A1", interaction.EnvelopeID, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.AckSocketModeInteraction(ctx, "A1", interaction.EnvelopeID, "modal-client"); err != nil {
		t.Fatal(err)
	}
	requireMissing(t, "closed dialog", get(t, mux, "/app?channel=Cdev").Body.String(), "File a ticket")

	openDialog("Dl2", `{"callback_id":"feedback","title":"Feedback","notify_on_cancel":true,"elements":[{"type":"text","name":"note","label":"Note"}]}`)
	response := postForm(t, mux, "/app/dialog/close?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "dialog_id": {"Dl2"}}.Encode(), false)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("cancel status=%d body=%s", response.Code, response.Body)
	}
	if cancelled := claimInteraction(t, s); cancelled["type"] != "dialog_cancellation" || cancelled["callback_id"] != "feedback" {
		t.Fatalf("dialog_cancellation = %v", cancelled)
	}
	requireMissing(t, "cancelled dialog", get(t, mux, "/app?channel=Cdev").Body.String(), "Feedback")
}

// A rich_text_input in a message renders as a text area (not an empty
// select) and dispatches its value as a rich_text object.
func TestMessageRichTextInputDispatchesRichText(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	now := time.Now().UTC()
	message := domain.Message{ID: "Mrt", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", AppID: "A1", Text: "form", CreatedAt: now,
		Blocks: `[{"type":"input","block_id":"b","dispatch_action":true,"label":{"type":"plain_text","text":"Note"},"element":{"type":"rich_text_input","action_id":"a"}},{"type":"input","block_id":"f","label":{"type":"plain_text","text":"Attach"},"element":{"type":"file_input","action_id":"fa"}}]`}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "E-Mrt", WorkspaceID: "T1", Topic: "message.created", Payload: "Mrt", CreatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "rich text input", body, `<textarea class="block-action" name="value"`, "Files are attached in an app")
	response := postForm(t, mux, "/app/interaction", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "message_id": {"Mrt"}, "app_id": {"A1"}, "block_id": {"b"}, "action_id": {"a"},
		"action_type": {"rich_text_input"}, "channel": {"Cdev"}, "value": {"hello"},
	}.Encode(), false)
	if response.Code >= 400 {
		t.Fatalf("dispatch status=%d body=%s", response.Code, response.Body)
	}
	payload := claimInteraction(t, s)
	actions, _ := payload["actions"].([]any)
	action, _ := actions[0].(map[string]any)
	rich, _ := json.Marshal(action["rich_text_value"])
	if !strings.Contains(string(rich), `"text":"hello"`) {
		t.Fatalf("rich_text_value = %s (payload %v)", rich, payload)
	}
}

// The input script is served under the workspace policy, and the live stream
// does not reload the page for a record that only saved what the viewer
// entered (a reload would discard focus and newer typing).
func TestViewInputScriptIsPermittedAndStateSavesDoNotReload(t *testing.T) {
	if !strings.Contains(workspaceContentSecurityPolicy, inlineScriptHashes(viewInputScript)[0]) {
		t.Fatal("the view input script is not permitted by the workspace policy")
	}
	if !strings.Contains(progressiveEnhancementScript, "viewFrame.state_only)return;if(event.type==='dialog.updated'&&viewFrame&&patchDialogErrors(viewFrame.dialog_id))return;window.location.reload()") {
		t.Fatal("a state-only view record still reloads the workspace page")
	}
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vp", elementsModal)
	requireContains(t, "workspace scripts", get(t, mux, "/app?channel=Cdev").Body.String(), "window.sameoldchatLocalizeViews=localize")
	postForm(t, mux, "/app/view/action?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vp"}, "modal_input_action": {"9"}, "input_0": {"x"}, "input_9": {"typed"},
	}.Encode(), false)
	records, err := s.ListEventsAfter(context.Background(), "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	saved := false
	for _, record := range records {
		saved = saved || record.Event.Topic == "view.updated" && strings.Contains(record.Event.Payload, `"state_only":true`)
	}
	if !saved {
		t.Fatal("the block action's state save is not marked state_only")
	}
}

const elementsModal = `{"type":"modal","title":{"type":"plain_text","text":"Probe"},"submit":{"type":"plain_text","text":"Go"},"blocks":[` +
	`{"type":"input","block_id":"rt","label":{"type":"plain_text","text":"RichLabel"},"element":{"type":"rich_text_input","action_id":"rta","initial_value":{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Seed"}]}]}}},` +
	`{"type":"input","block_id":"fi","optional":true,"label":{"type":"plain_text","text":"FileLabel"},"element":{"type":"file_input","action_id":"fia"}},` +
	`{"type":"input","block_id":"dt","optional":true,"label":{"type":"plain_text","text":"When"},"element":{"type":"datetimepicker","action_id":"dta","initial_date_time":1700000000}},` +
	`{"type":"input","block_id":"txt","optional":true,"label":{"type":"plain_text","text":"Txt"},"element":{"type":"plain_text_input","action_id":"ta","max_length":5,"min_length":2}},` +
	`{"type":"input","block_id":"num","optional":true,"label":{"type":"plain_text","text":"Num"},"element":{"type":"number_input","action_id":"na","is_decimal_allowed":true,"min_value":"1","max_value":"9"}},` +
	`{"type":"input","block_id":"dp","optional":true,"label":{"type":"plain_text","text":"Day"},"element":{"type":"datepicker","action_id":"dpa"}},` +
	`{"type":"input","block_id":"us","optional":true,"label":{"type":"plain_text","text":"User"},"element":{"type":"users_select","action_id":"usa"}},` +
	`{"type":"input","block_id":"ss","optional":true,"label":{"type":"plain_text","text":"Sel"},"element":{"type":"static_select","action_id":"ssa","options":[{"text":{"type":"plain_text","text":"One"},"value":"1"}]}},` +
	`{"type":"input","block_id":"ex","optional":true,"label":{"type":"plain_text","text":"Ext"},"element":{"type":"external_select","action_id":"exa"}},` +
	`{"type":"input","block_id":"disp","dispatch_action":true,"optional":true,"label":{"type":"plain_text","text":"Dispatch"},"element":{"type":"plain_text_input","action_id":"dispa","dispatch_action_config":{"trigger_actions_on":["on_character_entered"]}}}` +
	`]}`

// Every input element renders with its constraints, and a submission reports
// entered values in Slack's shapes: rich_text for rich text, Unix seconds read
// in the viewer's zone for a datetimepicker, and null for what was left empty.
func TestModalInputElementsRenderAndSubmitInSlackShapes(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vp", elementsModal)
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "modal elements", body,
		"RichLabel", ">Seed</textarea>", "FileLabel", `type="file" name="input_1_upload" multiple`, `enctype="multipart/form-data"`,
		`value="2023-11-14T22:13" data-unix="1700000000"`, `minlength="2" maxlength="5"`,
		`step="any" min="1" max="9"`, `data-dispatch-input="9" data-dispatch-on="character"`,
		`name="modal_input_action" value="9"`, `name="timezone" data-browser-timezone value="UTC"`,
	)
	response := postForm(t, mux, "/app/view/submit?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vp"}, "timezone": {"America/New_York"},
		"input_0": {"Hello\nworld"}, "input_2": {"2023-11-14T17:13"}, "input_3": {""}, "input_4": {"1.5"},
		"input_5": {""}, "input_6": {""}, "input_7": {""}, "input_8": {""}, "input_9": {""},
	}.Encode(), false)
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body)
	}
	values := viewState(t, claimInteraction(t, s))
	entry := func(block, action string) map[string]any {
		return values[block].(map[string]any)[action].(map[string]any)
	}
	rich, _ := json.Marshal(entry("rt", "rta")["rich_text_value"])
	if string(rich) != `{"elements":[{"elements":[{"text":"Hello\nworld","type":"text"}],"type":"rich_text_section"}],"type":"rich_text"}` {
		t.Fatalf("rich_text_value = %s", rich)
	}
	if files, ok := entry("fi", "fia")["files"].([]any); !ok || len(files) != 0 {
		t.Fatalf("optional file input = %v", entry("fi", "fia"))
	}
	if at := entry("dt", "dta")["selected_date_time"]; at != float64(1700000000-20) {
		t.Fatalf("selected_date_time = %v, want the New York wall time as Unix seconds", at)
	}
	for _, empty := range []struct{ block, action, field string }{
		{"txt", "ta", "value"}, {"dp", "dpa", "selected_date"}, {"us", "usa", "selected_user"},
		{"ss", "ssa", "selected_option"}, {"ex", "exa", "selected_option"}, {"disp", "dispa", "value"},
	} {
		value, present := entry(empty.block, empty.action)[empty.field]
		if !present || value != nil {
			t.Fatalf("%s.%s = %v (present %v), want null", empty.block, empty.field, value, present)
		}
	}
	if value := entry("num", "na")["value"]; value != "1.5" {
		t.Fatalf("number value = %v", value)
	}
}

// Slack's client refuses a submission that breaks an element's declared
// constraints; the app is not asked.
func TestModalSubmissionEnforcesElementConstraints(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vp", elementsModal)
	for _, bad := range []struct {
		field, value, message string
	}{
		{"input_3", "x", "Enter at least 2 characters."},
		{"input_3", "toolong", "Enter no more than 5 characters."},
		{"input_4", "12", "Enter a number no larger than 9."},
		{"input_0", "", "This field is required."},
	} {
		form := url.Values{"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vp"}, "input_0": {"x"}}
		form.Set(bad.field, bad.value)
		response := postForm(t, mux, "/app/view/submit?channel=Cdev", form.Encode(), false)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s=%q status=%d", bad.field, bad.value, response.Code)
		}
		requireContains(t, "constraint error", response.Body.String(), bad.message)
	}
	if _, found, _ := s.ClaimSocketModeInteraction(context.Background(), "A1", "modal-client", time.Minute); found {
		t.Fatal("an invalid submission reached the app")
	}
	seedOpenModal(t, s, "Vf", `{"type":"modal","title":{"type":"plain_text","text":"Files"},"submit":{"type":"plain_text","text":"Go"},"blocks":[{"type":"input","block_id":"fi","label":{"type":"plain_text","text":"Attach"},"element":{"type":"file_input","action_id":"fia"}}]}`)
	response := postForm(t, mux, "/app/view/submit?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vf"}}.Encode(), false)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("required file input status=%d", response.Code)
	}
	requireContains(t, "required file input", response.Body.String(), "This field is required.")
}

// An input block with dispatch_action sends block_actions for its own
// element.
func TestDispatchingInputSendsBlockActions(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	seedOpenModal(t, s, "Vp", elementsModal)
	response := postForm(t, mux, "/app/view/action?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vp"}, "modal_input_action": {"9"}, "input_0": {"x"}, "input_9": {"typed"},
	}.Encode(), false)
	if response.Code != http.StatusOK {
		t.Fatalf("dispatch status=%d body=%s", response.Code, response.Body)
	}
	payload := claimInteraction(t, s)
	actions, _ := payload["actions"].([]any)
	if len(actions) != 1 {
		t.Fatalf("dispatched payload = %v", payload)
	}
	action, _ := actions[0].(map[string]any)
	if payload["type"] != "block_actions" || action["action_id"] != "dispa" || action["block_id"] != "disp" || action["value"] != "typed" {
		t.Fatalf("dispatched payload = %v", payload)
	}
	// An input block without dispatch_action cannot be dispatched.
	response = postForm(t, mux, "/app/view/action?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vp"}, "modal_input_action": {"3"}, "input_0": {"x"},
	}.Encode(), false)
	if response.Code != http.StatusNotFound {
		t.Fatalf("non-dispatching input status=%d", response.Code)
	}
}
