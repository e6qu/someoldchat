package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestHTTPAppInteractionsUseSignedSlackPayloadsAndDurableCapabilities(t *testing.T) {
	const (
		signingSecret     = "interaction-signing-secret"
		verificationToken = "interaction-verification-token"
	)
	type observed struct {
		form      url.Values
		body      []byte
		timestamp string
		signature string
	}
	var (
		mu       sync.Mutex
		requests []observed
	)
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "read request", http.StatusInternalServerError)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Error(err)
			http.Error(w, "parse request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, observed{
			form: form, body: body,
			timestamp: r.Header.Get("X-Slack-Request-Timestamp"),
			signature: r.Header.Get("X-Slack-Signature"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if form.Get("command") != "" {
			_, _ = io.WriteString(w, `{"response_type":"in_channel","text":"command received"}`)
			return
		}
		if strings.Contains(form.Get("payload"), `"type":"view_submission"`) {
			if strings.Contains(form.Get("payload"), `"value":"bad"`) {
				_, _ = io.WriteString(w, `{"response_action":"errors","errors":{"answer_block":"Use a production name"}}`)
				return
			}
			_, _ = io.WriteString(w, `{}`)
			return
		}
		if strings.Contains(form.Get("payload"), `"type":"block_suggestion"`) {
			_, _ = io.WriteString(w, `{"option_groups":[{"label":{"type":"plain_text","text":"Projects"},"options":[{"text":{"type":"plain_text","text":"Production API"},"value":"api-prod","description":{"type":"plain_text","text":"Primary service"}}]}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"replace_original":true,"text":"deployment opened"}`)
	}))
	defer receiver.Close()

	ctx := context.Background()
	repository := memory.New()
	for _, seed := range []func() error{
		func() error {
			return repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test", Domain: "test"})
		},
		func() error { return repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}) },
		func() error {
			return repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "interaction-bot"})
		},
		func() error {
			return repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
		},
		func() error { return repository.SeedConversationMember("C1", "U1") },
		func() error { return repository.SeedConversationMember("C1", "UBOT") },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	key := []byte(strings.Repeat("i", 32))
	signingCiphertext, err := secretbox.Seal(key, appSigningSecretAssociatedData("A1"), signingSecret)
	if err != nil {
		t.Fatal(err)
	}
	verificationCiphertext, err := secretbox.Seal(key, appVerificationTokenAssociatedData("A1"), verificationToken)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Interactions"},"features":{"slash_commands":[{"command":"/deploy","url":"` + receiver.URL + `","description":"Deploy"}],"shortcuts":[{"name":"Create deployment","callback_id":"create_deployment","description":"Create a deployment","type":"global"},{"name":"Attach deployment","callback_id":"attach_deployment","description":"Attach this message","type":"message"}]},"oauth_config":{"scopes":{"bot":["commands"]}},"settings":{"interactivity":{"is_enabled":true,"request_url":"` + receiver.URL + `","message_menu_options_url":"` + receiver.URL + `"}}}`
	if err := repository.CreateApp(ctx,
		domain.App{
			ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Interactions", ClientID: "client",
			SigningSecretHash: domain.HashToken(signingSecret), SigningSecretCiphertext: signingCiphertext,
			VerificationTokenHash: domain.HashToken(verificationToken), VerificationTokenCiphertext: verificationCiphertext,
			ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
		},
		domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"},
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", AppID: "A1", WorkspaceID: "T1", UserID: "UBOT", Name: "interaction-bot", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, AppCredentialKey: key, AppHTTPClient: receiver.Client()}

	if err := messages.DispatchSlashCommand(ctx, "T1", "U1", "C1", "", "/deploy", "production", "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	slash := requests[0]
	mu.Unlock()
	assertSlackInteractionSignature(t, signingSecret, slash.body, slash.timestamp, slash.signature)
	for field, want := range map[string]string{
		"api_app_id": "A1", "team_id": "T1", "team_domain": "test",
		"channel_id": "C1", "channel_name": "general", "user_id": "U1",
		"user_name": "alice", "command": "/deploy", "text": "production",
		"token": verificationToken,
	} {
		if got := slash.form.Get(field); got != want {
			t.Errorf("slash field %s=%q, want %q", field, got, want)
		}
	}
	triggerID := slash.form.Get("trigger_id")
	if triggerID == "" || !strings.HasPrefix(slash.form.Get("response_url"), "https://chat.example.test/app-response/") {
		t.Fatalf("slash capabilities trigger=%q response_url=%q", triggerID, slash.form.Get("response_url"))
	}
	openedView, err := messages.OpenView(ctx, "T1", "UBOT", "A1", triggerID, `{"type":"modal","title":{"type":"plain_text","text":"Deploy"},"submit":{"type":"plain_text","text":"Deploy"},"blocks":[{"type":"input","block_id":"answer_block","label":{"type":"plain_text","text":"Environment"},"element":{"type":"plain_text_input","action_id":"answer"}}]}`)
	if err != nil {
		t.Fatalf("consume trigger: %v", err)
	}
	if _, err := messages.OpenView(ctx, "T1", "UBOT", "A1", triggerID, `{"type":"modal","title":{"type":"plain_text","text":"Replay"},"blocks":[]}`); err != ErrInvalidTrigger {
		t.Fatalf("trigger replay error=%v, want %v", err, ErrInvalidTrigger)
	}

	blocks := `[{"type":"actions","block_id":"deployment","elements":[{"type":"static_select","action_id":"view_build","placeholder":{"type":"plain_text","text":"View build"},"options":[{"text":{"type":"plain_text","text":"Build 842"},"value":"842"}]}]}]`
	original, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "UBOT", "C1", "Deployment", blocks, "", "", "", "A1")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: original.ID, BlockID: "deployment", ActionID: "view_build", Type: "static_select", Value: "842",
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	actionRequest := requests[1]
	mu.Unlock()
	assertSlackInteractionSignature(t, signingSecret, actionRequest.body, actionRequest.timestamp, actionRequest.signature)
	var actionPayload struct {
		Type        string `json:"type"`
		APIAppID    string `json:"api_app_id"`
		ResponseURL string `json:"response_url"`
		Actions     []struct {
			Type           string `json:"type"`
			ActionID       string `json:"action_id"`
			BlockID        string `json:"block_id"`
			SelectedOption struct {
				Value string `json:"value"`
				Text  struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"selected_option"`
		} `json:"actions"`
		State struct {
			Values map[string]map[string]json.RawMessage `json:"values"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(actionRequest.form.Get("payload")), &actionPayload); err != nil {
		t.Fatal(err)
	}
	if actionPayload.Type != "block_actions" || actionPayload.APIAppID != "A1" || len(actionPayload.Actions) != 1 ||
		actionPayload.Actions[0].Type != "static_select" || actionPayload.Actions[0].ActionID != "view_build" ||
		actionPayload.Actions[0].BlockID != "deployment" || actionPayload.Actions[0].SelectedOption.Value != "842" ||
		actionPayload.Actions[0].SelectedOption.Text.Text != "Build 842" ||
		actionPayload.State.Values["deployment"]["view_build"] == nil {
		t.Fatalf("block action payload=%s", actionRequest.form.Get("payload"))
	}
	updated, err := repository.GetMessage(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Text != "deployment opened" || updated.AppID != "A1" {
		t.Fatalf("updated message=%+v", updated)
	}

	globalShortcuts, err := messages.ListAppShortcuts(ctx, "T1", "U1", "global")
	if err != nil || len(globalShortcuts) != 1 || globalShortcuts[0].CallbackID != "create_deployment" {
		t.Fatalf("global shortcuts=%+v err=%v", globalShortcuts, err)
	}
	if err := messages.DispatchAppShortcut(ctx, "T1", "U1", "C1", "A1", "create_deployment", "", "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := messages.DispatchAppShortcut(ctx, "T1", "U1", "C1", "A1", "attach_deployment", original.ID, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	globalRequest, messageRequest := requests[2], requests[3]
	mu.Unlock()
	assertSlackInteractionSignature(t, signingSecret, globalRequest.body, globalRequest.timestamp, globalRequest.signature)
	assertSlackInteractionSignature(t, signingSecret, messageRequest.body, messageRequest.timestamp, messageRequest.signature)
	var globalPayload, messageShortcutPayload map[string]any
	if err := json.Unmarshal([]byte(globalRequest.form.Get("payload")), &globalPayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(messageRequest.form.Get("payload")), &messageShortcutPayload); err != nil {
		t.Fatal(err)
	}
	if globalPayload["type"] != "shortcut" || globalPayload["callback_id"] != "create_deployment" ||
		globalPayload["channel"] != nil || globalPayload["response_url"] != nil {
		t.Fatalf("global shortcut payload=%s", globalRequest.form.Get("payload"))
	}
	if messageShortcutPayload["type"] != "message_action" || messageShortcutPayload["callback_id"] != "attach_deployment" ||
		messageShortcutPayload["channel"] == nil || messageShortcutPayload["message"] == nil ||
		!strings.HasPrefix(slackString(messageShortcutPayload["response_url"]), "https://chat.example.test/app-response/") {
		t.Fatalf("message shortcut payload=%s", messageRequest.form.Get("payload"))
	}
	badResult, err := messages.SubmitView(ctx, "T1", "U1", "C1", openedView.ID, `{"values":{"answer_block":{"answer":{"type":"plain_text_input","value":"bad"}}}}`, "https://chat.example.test")
	if err != nil || badResult.Errors["answer_block"] != "Use a production name" {
		t.Fatalf("bad modal result=%+v err=%v", badResult, err)
	}
	if current, err := messages.CurrentModalView(ctx, "T1", "U1"); err != nil || current.ID != openedView.ID {
		t.Fatalf("modal disappeared after validation error: current=%+v err=%v", current, err)
	}
	goodResult, err := messages.SubmitView(ctx, "T1", "U1", "C1", openedView.ID, `{"values":{"answer_block":{"answer":{"type":"plain_text_input","value":"production"}}}}`, "https://chat.example.test")
	if err != nil || len(goodResult.Errors) != 0 {
		t.Fatalf("good modal result=%+v err=%v", goodResult, err)
	}
	if _, err := messages.CurrentModalView(ctx, "T1", "U1"); err == nil {
		t.Fatal("empty modal acknowledgement did not close the submitted view")
	}
	mu.Lock()
	badSubmission, goodSubmission := requests[4], requests[5]
	mu.Unlock()
	assertSlackInteractionSignature(t, signingSecret, badSubmission.body, badSubmission.timestamp, badSubmission.signature)
	assertSlackInteractionSignature(t, signingSecret, goodSubmission.body, goodSubmission.timestamp, goodSubmission.signature)
	var submittedPayload struct {
		Type     string `json:"type"`
		APIAppID string `json:"api_app_id"`
		View     struct {
			ID              string `json:"id"`
			AppID           string `json:"app_id"`
			PrivateMetadata string `json:"private_metadata"`
			State           struct {
				Values map[string]map[string]struct {
					Type  string `json:"type"`
					Value string `json:"value"`
				} `json:"values"`
			} `json:"state"`
		} `json:"view"`
	}
	if err := json.Unmarshal([]byte(goodSubmission.form.Get("payload")), &submittedPayload); err != nil {
		t.Fatal(err)
	}
	if submittedPayload.Type != "view_submission" || submittedPayload.APIAppID != "A1" ||
		submittedPayload.View.ID != string(openedView.ID) || submittedPayload.View.AppID != "A1" ||
		submittedPayload.View.State.Values["answer_block"]["answer"].Type != "plain_text_input" ||
		submittedPayload.View.State.Values["answer_block"]["answer"].Value != "production" {
		t.Fatalf("view submission payload=%s", goodSubmission.form.Get("payload"))
	}

	externalBlocks := `[{"type":"actions","block_id":"project","elements":[{"type":"external_select","action_id":"project_select","placeholder":{"type":"plain_text","text":"Find a project"},"min_query_length":2}]}]`
	externalMessage, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "UBOT", "C1", "Choose a project", externalBlocks, "", "", "", "A1")
	if err != nil {
		t.Fatal(err)
	}
	options, err := messages.LoadAppOptions(ctx, "T1", "U1", "C1", domain.AppOptionQuery{
		AppID: "A1", MessageID: externalMessage.ID, BlockID: "project", ActionID: "project_select", Value: "prod",
	}, "https://chat.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Text != "Production API" || options[0].Value != "api-prod" ||
		options[0].Description != "Primary service" || options[0].Group != "Projects" {
		t.Fatalf("dynamic options=%+v", options)
	}
	mu.Lock()
	optionsRequest := requests[len(requests)-1]
	mu.Unlock()
	assertSlackInteractionSignature(t, signingSecret, optionsRequest.body, optionsRequest.timestamp, optionsRequest.signature)
	var suggestionPayload struct {
		Type      string `json:"type"`
		APIAppID  string `json:"api_app_id"`
		BlockID   string `json:"block_id"`
		ActionID  string `json:"action_id"`
		Value     string `json:"value"`
		Container struct {
			Type      string `json:"type"`
			ChannelID string `json:"channel_id"`
		} `json:"container"`
		Message map[string]any `json:"message"`
	}
	if err := json.Unmarshal([]byte(optionsRequest.form.Get("payload")), &suggestionPayload); err != nil {
		t.Fatal(err)
	}
	if suggestionPayload.Type != "block_suggestion" || suggestionPayload.APIAppID != "A1" ||
		suggestionPayload.BlockID != "project" || suggestionPayload.ActionID != "project_select" ||
		suggestionPayload.Value != "prod" || suggestionPayload.Container.Type != "message" ||
		suggestionPayload.Container.ChannelID != "C1" || suggestionPayload.Message["blocks"] == nil {
		t.Fatalf("block suggestion payload=%s", optionsRequest.form.Get("payload"))
	}

	feedbackBlocks := `[{"type":"context_actions","block_id":"answer-feedback","elements":[{"type":"feedback_buttons","action_id":"feedback","positive_button":{"text":{"type":"plain_text","text":"Good"},"value":"positive"},"negative_button":{"text":{"type":"plain_text","text":"Bad"},"value":"negative"}}]}]`
	feedbackMessage, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "UBOT", "C1", "Answer", feedbackBlocks, "", "", "", "A1")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: feedbackMessage.ID, BlockID: "answer-feedback", ActionID: "feedback",
		Type: "feedback_buttons", Value: "positive",
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	feedbackRequest := requests[len(requests)-1]
	mu.Unlock()
	assertSlackInteractionSignature(t, signingSecret, feedbackRequest.body, feedbackRequest.timestamp, feedbackRequest.signature)
	var feedbackPayload struct {
		Type    string `json:"type"`
		Actions []struct {
			Type     string `json:"type"`
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(feedbackRequest.form.Get("payload")), &feedbackPayload); err != nil {
		t.Fatal(err)
	}
	if feedbackPayload.Type != "block_actions" || len(feedbackPayload.Actions) != 1 ||
		feedbackPayload.Actions[0].Type != "feedback_buttons" ||
		feedbackPayload.Actions[0].ActionID != "feedback" || feedbackPayload.Actions[0].Value != "positive" {
		t.Fatalf("feedback payload=%s", feedbackRequest.form.Get("payload"))
	}

	responseURL, err := url.Parse(actionPayload.ResponseURL)
	if err != nil {
		t.Fatal(err)
	}
	responseToken := strings.TrimPrefix(responseURL.Path, "/app-response/")
	for index := 0; index < appResponseUses; index++ {
		if err := messages.HandleAppResponse(ctx, responseToken, `{"response_type":"in_channel","text":"late response"}`); err != nil {
			t.Fatalf("response URL use %d: %v", index+1, err)
		}
	}
	if err := messages.HandleAppResponse(ctx, responseToken, `{"response_type":"in_channel","text":"exhausted"}`); err != ErrInvalidAppResponse {
		t.Fatalf("exhausted response URL error=%v, want %v", err, ErrInvalidAppResponse)
	}
}

func TestSocketModeInteractionsQueueSlackEnvelopesAndApplyAcknowledgementPayloads(t *testing.T) {
	const verificationToken = "socket-verification-token"
	ctx := context.Background()
	repository := memory.New()
	for _, seed := range []func() error{
		func() error {
			return repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test", Domain: "test"})
		},
		func() error { return repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}) },
		func() error {
			return repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "socket-bot"})
		},
		func() error {
			return repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
		},
		func() error { return repository.SeedConversationMember("C1", "U1") },
		func() error { return repository.SeedConversationMember("C1", "UBOT") },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	key := []byte(strings.Repeat("s", 32))
	signingCiphertext, err := secretbox.Seal(key, appSigningSecretAssociatedData("A1"), "signing-secret")
	if err != nil {
		t.Fatal(err)
	}
	verificationCiphertext, err := secretbox.Seal(key, appVerificationTokenAssociatedData("A1"), verificationToken)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Socket Interactions"},"features":{"slash_commands":[{"command":"/deploy","description":"Deploy"}],"shortcuts":[{"name":"Create deployment","callback_id":"create_deployment","description":"Create a deployment","type":"global"}]},"oauth_config":{"scopes":{"bot":["commands"]}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
	if err := repository.CreateApp(ctx,
		domain.App{
			ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Socket Interactions", ClientID: "client",
			SigningSecretHash: domain.HashToken("signing-secret"), SigningSecretCiphertext: signingCiphertext,
			VerificationTokenHash: domain.HashToken(verificationToken), VerificationTokenCiphertext: verificationCiphertext,
			ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
		},
		domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("client-secret"), AppID: "A1"},
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", AppID: "A1", WorkspaceID: "T1", UserID: "UBOT", Name: "socket-bot", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, AppCredentialKey: key}

	unsubscribed, err := events.New("event-unsubscribed", "T1", "U1", events.NewPayload("reaction.added",
		events.String("channel_id", "C1"), events.String("user_id", "U1"), events.String("reaction", "wave"),
		events.String("ts", "1700000000.000000"),
	), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendEvent(ctx, unsubscribed); err != nil {
		t.Fatal(err)
	}
	if record, _, _, found, err := messages.ClaimAppEvent(ctx, "A1", "socket", "socket-filter", time.Minute); err != nil || found {
		t.Fatalf("unsubscribed Socket Mode event leaked: record=%+v found=%v err=%v", record, found, err)
	}

	if err := messages.DispatchSlashCommand(ctx, "T1", "U1", "C1", "", "/deploy", "production", "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	slash, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket-1", time.Minute)
	if err != nil || !found {
		t.Fatalf("slash interaction=%+v found=%v err=%v", slash, found, err)
	}
	var slashPayload map[string]any
	if err := json.Unmarshal([]byte(slash.Payload), &slashPayload); err != nil {
		t.Fatal(err)
	}
	if slash.Type != "slash_commands" || slashPayload["api_app_id"] != "A1" || slashPayload["command"] != "/deploy" ||
		slashPayload["text"] != "production" || slashPayload["token"] != verificationToken ||
		!strings.HasPrefix(slackString(slashPayload["trigger_id"]), "trigger_") ||
		!strings.HasPrefix(slackString(slashPayload["response_url"]), "https://chat.example.test/app-response/") {
		t.Fatalf("slash payload=%s", slash.Payload)
	}
	openedModal, err := messages.OpenView(ctx, "T1", "UBOT", "A1", slackString(slashPayload["trigger_id"]), `{"type":"modal","title":{"type":"plain_text","text":"Socket modal"},"submit":{"type":"plain_text","text":"Save"},"blocks":[{"type":"input","block_id":"name","label":{"type":"plain_text","text":"Name"},"element":{"type":"plain_text_input","action_id":"name_input"}},{"type":"actions","block_id":"preview","elements":[{"type":"button","action_id":"preview_button","text":{"type":"plain_text","text":"Preview"},"value":"current"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	actionState := `{"values":{"name":{"name_input":{"type":"plain_text_input","value":"draft"}}}}`
	if err := messages.DispatchViewBlockAction(ctx, "T1", "U1", "C1", domain.AppViewBlockAction{
		ViewID: openedModal.ID, BlockID: "preview", ActionID: "preview_button",
		Type: "button", Value: "current", State: actionState,
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	viewAction, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket-view-action", time.Minute)
	if err != nil || !found {
		t.Fatalf("view action=%+v found=%v err=%v", viewAction, found, err)
	}
	var viewActionPayload struct {
		Type      string `json:"type"`
		APIAppID  string `json:"api_app_id"`
		Container struct {
			Type   string        `json:"type"`
			ViewID domain.ViewID `json:"view_id"`
		} `json:"container"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
		State map[string]any `json:"state"`
	}
	if err := json.Unmarshal([]byte(viewAction.Payload), &viewActionPayload); err != nil {
		t.Fatal(err)
	}
	if viewActionPayload.Type != "block_actions" || viewActionPayload.APIAppID != "A1" ||
		viewActionPayload.Container.Type != "view" || viewActionPayload.Container.ViewID != openedModal.ID ||
		len(viewActionPayload.Actions) != 1 || viewActionPayload.Actions[0].ActionID != "preview_button" ||
		viewActionPayload.Actions[0].Value != "current" || viewActionPayload.State["values"] == nil ||
		strings.Contains(viewAction.Payload, `"response_url"`) || strings.Contains(viewAction.Payload, `"channel"`) {
		t.Fatalf("view action payload=%s", viewAction.Payload)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", viewAction.EnvelopeID, []byte(`{"text":"ack bodies do not become messages"}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.AckSocketModeInteraction(ctx, "A1", viewAction.EnvelopeID, "socket-view-action"); err != nil {
		t.Fatal(err)
	}
	if current, err := messages.CurrentModalView(ctx, "T1", "U1"); err != nil || current.State != actionState {
		t.Fatalf("view action state=%+v err=%v", current, err)
	}
	pending, err := messages.SubmitView(ctx, "T1", "U1", "C1", openedModal.ID, `{"values":{"name":{"name_input":{"type":"plain_text_input","value":"bad"}}}}`, "https://chat.example.test")
	if err != nil || !pending.Pending {
		t.Fatalf("pending modal=%+v err=%v", pending, err)
	}
	modalInteraction, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket-modal", time.Minute)
	if err != nil || !found {
		t.Fatalf("modal interaction=%+v found=%v err=%v", modalInteraction, found, err)
	}
	var modalPayload map[string]any
	if err := json.Unmarshal([]byte(modalInteraction.Payload), &modalPayload); err != nil {
		t.Fatal(err)
	}
	if modalInteraction.Type != "interactive" || modalPayload["type"] != "view_submission" {
		t.Fatalf("modal payload=%s", modalInteraction.Payload)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", modalInteraction.EnvelopeID, []byte(`{"response_action":"errors","errors":{"name":"Choose another name"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.AckSocketModeInteraction(ctx, "A1", modalInteraction.EnvelopeID, "socket-modal"); err != nil {
		t.Fatal(err)
	}
	if current, err := messages.CurrentModalView(ctx, "T1", "U1"); err != nil || current.ID != openedModal.ID {
		t.Fatalf("validation errors closed modal: current=%+v err=%v", current, err)
	}
	pending, err = messages.SubmitView(ctx, "T1", "U1", "C1", openedModal.ID, `{"values":{"name":{"name_input":{"type":"plain_text_input","value":"good"}}}}`, "https://chat.example.test")
	if err != nil || !pending.Pending {
		t.Fatalf("second pending modal=%+v err=%v", pending, err)
	}
	modalInteraction, found, err = repository.ClaimSocketModeInteraction(ctx, "A1", "socket-modal", time.Minute)
	if err != nil || !found {
		t.Fatalf("second modal interaction=%+v found=%v err=%v", modalInteraction, found, err)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", modalInteraction.EnvelopeID, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", modalInteraction.EnvelopeID, []byte(`{}`)); err != nil {
		t.Fatalf("retried modal acknowledgement: %v", err)
	}
	if err := repository.AckSocketModeInteraction(ctx, "A1", modalInteraction.EnvelopeID, "socket-modal"); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CurrentModalView(ctx, "T1", "U1"); err == nil {
		t.Fatal("Socket Mode modal acknowledgement did not close the view")
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", slash.EnvelopeID, []byte(`{"text":"deployment queued"}`)); err != nil {
		t.Fatal(err)
	}
	// A queue acknowledgement can fail after the app response was committed.
	// Slack then retries the same envelope; the recipient must still see one
	// response, not one response per delivery attempt.
	if err := messages.HandleSocketModeResponse(ctx, "A1", slash.EnvelopeID, []byte(`{"text":"deployment queued"}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.AckSocketModeInteraction(ctx, "A1", slash.EnvelopeID, "socket-1"); err != nil {
		t.Fatal(err)
	}
	ephemeral, err := messages.ListEphemeralMessages(ctx, "T1", "U1", "C1", 10)
	if err != nil || len(ephemeral) != 1 || ephemeral[0].Text != "deployment queued" || ephemeral[0].AppID != "A1" {
		t.Fatalf("ephemeral=%+v err=%v", ephemeral, err)
	}

	privateBlocks := `[{"type":"actions","block_id":"private","elements":[{"type":"button","action_id":"confirm","text":{"type":"plain_text","text":"Confirm"},"value":"yes"}]}]`
	privateMessage, err := messages.PostEphemeralWithBlocksAndAttachments(ctx, "T1", "UBOT", "C1", "U1", "Private deployment", privateBlocks, "", "A1")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: privateMessage.ID, BlockID: "private", ActionID: "confirm", Type: "button", Value: "yes",
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	privateAction, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket-1", time.Minute)
	if err != nil || !found {
		t.Fatalf("ephemeral interaction=%+v found=%v err=%v", privateAction, found, err)
	}
	var privatePayload struct {
		ResponseURL string `json:"response_url"`
		Container   struct {
			IsEphemeral bool `json:"is_ephemeral"`
		} `json:"container"`
	}
	if err := json.Unmarshal([]byte(privateAction.Payload), &privatePayload); err != nil {
		t.Fatal(err)
	}
	if !privatePayload.Container.IsEphemeral {
		t.Fatalf("ephemeral action payload=%s", privateAction.Payload)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", privateAction.EnvelopeID, []byte(`{"replace_original":true,"text":"Private deployment confirmed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.AckSocketModeInteraction(ctx, "A1", privateAction.EnvelopeID, "socket-1"); err != nil {
		t.Fatal(err)
	}
	replacedPrivate, err := repository.GetEphemeralMessage(ctx, "T1", "U1", privateMessage.ID)
	if err != nil || replacedPrivate.Text != "Private deployment confirmed" || replacedPrivate.Blocks != "" {
		t.Fatalf("replaced ephemeral=%+v err=%v", replacedPrivate, err)
	}
	privateResponseURL, err := url.Parse(privatePayload.ResponseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.HandleAppResponse(ctx, strings.TrimPrefix(privateResponseURL.Path, "/app-response/"), `{"delete_original":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetEphemeralMessage(ctx, "T1", "U1", privateMessage.ID); err == nil {
		t.Fatal("deleted ephemeral message still exists")
	}
	if err := messages.DispatchAppShortcut(ctx, "T1", "U1", "C1", "A1", "create_deployment", "", "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	shortcut, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket-1", time.Minute)
	if err != nil || !found {
		t.Fatalf("shortcut interaction=%+v found=%v err=%v", shortcut, found, err)
	}
	var shortcutPayload map[string]any
	if err := json.Unmarshal([]byte(shortcut.Payload), &shortcutPayload); err != nil {
		t.Fatal(err)
	}
	if shortcut.Type != "interactive" || shortcutPayload["type"] != "shortcut" ||
		shortcutPayload["callback_id"] != "create_deployment" || shortcutPayload["channel"] != nil ||
		shortcutPayload["response_url"] != nil {
		t.Fatalf("shortcut payload=%s", shortcut.Payload)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", shortcut.EnvelopeID, []byte(`{"text":"this acknowledgement is not a message response"}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.AckSocketModeInteraction(ctx, "A1", shortcut.EnvelopeID, "socket-1"); err != nil {
		t.Fatal(err)
	}
	afterShortcut, err := messages.ListEphemeralMessages(ctx, "T1", "U1", "C1", 10)
	if err != nil || len(afterShortcut) != 1 || afterShortcut[0].Text != "deployment queued" {
		t.Fatalf("shortcut acknowledgement leaked into messages: values=%+v err=%v", afterShortcut, err)
	}

	blocks := `[{"type":"actions","block_id":"deployment","elements":[{"type":"button","action_id":"open","text":{"type":"plain_text","text":"Open"},"value":"842"}]}]`
	original, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "UBOT", "C1", "Deployment", blocks, "", "", "", "A1")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: original.ID, BlockID: "deployment", ActionID: "open", Type: "button", Value: "842",
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	action, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket-1", time.Minute)
	if err != nil || !found {
		t.Fatalf("block interaction=%+v found=%v err=%v", action, found, err)
	}
	var actionPayload struct {
		Type    string `json:"type"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(action.Payload), &actionPayload); err != nil {
		t.Fatal(err)
	}
	if action.Type != "interactive" || actionPayload.Type != "block_actions" || len(actionPayload.Actions) != 1 ||
		actionPayload.Actions[0].ActionID != "open" || actionPayload.Actions[0].Value != "842" {
		t.Fatalf("action payload=%s", action.Payload)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", action.EnvelopeID, []byte(`{"replace_original":true,"text":"deployment opened"}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.AckSocketModeInteraction(ctx, "A1", action.EnvelopeID, "socket-1"); err != nil {
		t.Fatal(err)
	}
	updated, err := repository.GetMessage(ctx, original.ID)
	if err != nil || updated.Text != "deployment opened" || updated.AppID != "A1" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}

	externalBlocks := `[{"type":"actions","block_id":"project","elements":[{"type":"multi_external_select","action_id":"project_select","placeholder":{"type":"plain_text","text":"Find projects"}}]}]`
	externalMessage, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "UBOT", "C1", "Choose projects", externalBlocks, "", "", "", "A1")
	if err != nil {
		t.Fatal(err)
	}
	type optionResult struct {
		options []domain.AppOption
		err     error
	}
	result := make(chan optionResult, 1)
	go func() {
		options, loadErr := messages.LoadAppOptions(ctx, "T1", "U1", "C1", domain.AppOptionQuery{
			AppID: "A1", MessageID: externalMessage.ID, BlockID: "project", ActionID: "project_select", Value: "prod",
		}, "https://chat.example.test")
		result <- optionResult{options: options, err: loadErr}
	}()
	var suggestion domain.SocketModeInteraction
	for attempt := 0; attempt < 100; attempt++ {
		suggestion, found, err = repository.ClaimSocketModeInteraction(ctx, "A1", "socket-options", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatal("Socket Mode block suggestion was not queued")
	}
	var suggestionPayload map[string]any
	if err := json.Unmarshal([]byte(suggestion.Payload), &suggestionPayload); err != nil {
		t.Fatal(err)
	}
	if suggestion.Type != "interactive" || suggestionPayload["type"] != "block_suggestion" ||
		suggestionPayload["block_id"] != "project" || suggestionPayload["action_id"] != "project_select" ||
		suggestionPayload["value"] != "prod" {
		t.Fatalf("Socket Mode block suggestion=%s", suggestion.Payload)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", suggestion.EnvelopeID, []byte(
		`{"options":[{"text":{"type":"plain_text","text":"Production API"},"value":"api-prod"}]}`,
	)); err != nil {
		t.Fatal(err)
	}
	select {
	case loaded := <-result:
		if loaded.err != nil || len(loaded.options) != 1 || loaded.options[0].Value != "api-prod" {
			t.Fatalf("Socket Mode options=%+v err=%v", loaded.options, loaded.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Socket Mode option acknowledgement was not returned to the caller")
	}
	if err := repository.AckSocketModeInteraction(ctx, "A1", suggestion.EnvelopeID, "socket-options"); err != nil {
		t.Fatal(err)
	}
}

func slackString(value any) string {
	text, _ := value.(string)
	return text
}

func TestAppBlockActionPayloadPreservesMultiValueAndPickerShapes(t *testing.T) {
	multi := appBlockActionPayload(`[{"type":"actions","block_id":"filters","elements":[{"type":"checkboxes","action_id":"regions","options":[{"text":{"type":"plain_text","text":"Europe"},"value":"eu"},{"text":{"type":"plain_text","text":"US"},"value":"us"}]}]}]`, domain.AppBlockAction{
		BlockID: "filters", ActionID: "regions", Type: "checkboxes", Value: `["eu","us"]`,
	})
	options, ok := multi["selected_options"].([]map[string]any)
	if !ok || len(options) != 2 || options[0]["value"] != "eu" || options[1]["value"] != "us" {
		t.Fatalf("multi action=%+v", multi)
	}
	if text, _ := options[0]["text"].(map[string]any); text["text"] != "Europe" {
		t.Fatalf("selected option text=%+v", options[0])
	}
	dateTime := appBlockActionPayload("", domain.AppBlockAction{BlockID: "schedule", ActionID: "when", Type: "datetimepicker", Value: "1700000000"})
	if dateTime["selected_date_time"] != int64(1700000000) {
		t.Fatalf("datetime action=%+v", dateTime)
	}
}

func TestParseAppOptionsEnforcesSlackOptionContracts(t *testing.T) {
	valid := []byte(`{"options":[{"text":{"type":"plain_text","text":"One"},"value":"one"}]}`)
	options, err := parseAppOptions(valid)
	if err != nil || len(options) != 1 || options[0].Text != "One" {
		t.Fatalf("valid options=%+v err=%v", options, err)
	}
	for name, body := range map[string]string{
		"mixed response shapes": `{"options":[{"text":{"type":"plain_text","text":"One"},"value":"one"}],"option_groups":[{"label":{"type":"plain_text","text":"Group"},"options":[]}]}`,
		"markdown option text":  `{"options":[{"text":{"type":"mrkdwn","text":"One"},"value":"one"}]}`,
		"missing value":         `{"options":[{"text":{"type":"plain_text","text":"One"}}]}`,
		"empty object":          `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAppOptions([]byte(body)); err != ErrInvalidAppResponse {
				t.Fatalf("error=%v, want %v", err, ErrInvalidAppResponse)
			}
		})
	}
}

func TestOnlyAuthoredDispatchableBlockActionsCanBeSent(t *testing.T) {
	blocks := `[{"type":"actions","block_id":"actions","elements":[{"type":"button","action_id":"run","text":{"type":"plain_text","text":"Run"}}]},{"type":"input","block_id":"quiet","label":{"type":"plain_text","text":"Draft"},"element":{"type":"plain_text_input","action_id":"draft"}},{"type":"input","block_id":"live","dispatch_action":true,"label":{"type":"plain_text","text":"Filter"},"element":{"type":"plain_text_input","action_id":"filter"}}]`
	if !blocksContainDispatchableAction(blocks, "actions", "run", "button") {
		t.Fatal("authored action-block button was not dispatchable")
	}
	if blocksContainDispatchableAction(blocks, "actions", "forged", "button") {
		t.Fatal("an action absent from the authored blocks was dispatchable")
	}
	if blocksContainDispatchableAction(blocks, "quiet", "draft", "plain_text_input") {
		t.Fatal("an input block without dispatch_action emitted an interaction")
	}
	if !blocksContainDispatchableAction(blocks, "live", "filter", "plain_text_input") {
		t.Fatal("an input block with dispatch_action was not dispatchable")
	}
}

func assertSlackInteractionSignature(t *testing.T, secret string, body []byte, timestamp, signature string) {
	t.Helper()
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q: %v", timestamp, err)
	}
	want, err := events.SlackSignature(secret, time.Unix(seconds, 0).UTC(), body)
	if err != nil {
		t.Fatal(err)
	}
	if signature != want {
		t.Fatalf("signature=%q, want %q", signature, want)
	}
}
