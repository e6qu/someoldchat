package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

const (
	appTriggerLifetime  = 3 * time.Second
	appResponseLifetime = 30 * time.Minute
	appResponseUses     = 5
	appRequestTimeout   = 3 * time.Second
)

var (
	ErrAppInteractionUnavailable = errors.New("application interaction is unavailable")
	ErrSlashCommandNotFound      = errors.New("slash command was not found")
	ErrSlashCommandInThread      = errors.New("slash commands cannot be invoked in threads")
	ErrInvalidAppResponse        = errors.New("application response is invalid")
	ErrInvalidTrigger            = errors.New("trigger_id is invalid or expired")
)

func (m Messages) consumeAppTrigger(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, triggerID string) (domain.AppTrigger, error) {
	if appID == "" || strings.TrimSpace(triggerID) == "" {
		return domain.AppTrigger{}, ErrInvalidTrigger
	}
	trigger, err := m.Store.ConsumeAppTrigger(ctx, domain.HashToken(strings.TrimSpace(triggerID)), appID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.AppTrigger{}, ErrInvalidTrigger
	}
	if err != nil {
		return domain.AppTrigger{}, err
	}
	if trigger.WorkspaceID != workspaceID {
		return domain.AppTrigger{}, ErrInvalidTrigger
	}
	return trigger, nil
}

func (m Messages) DispatchSlashCommand(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, threadTimestamp domain.MessageTimestamp, command, text, responseBaseURL string) error {
	if threadTimestamp != "" {
		return ErrSlashCommandInThread
	}
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return err
	}
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(command, "/") || strings.ContainsAny(command, " \t\r\n") {
		return ErrSlashCommandNotFound
	}
	snapshot, parsed, slash, err := m.slashCommandApp(ctx, workspaceID, command)
	if err != nil {
		return err
	}
	if slash.ShouldEscape {
		text, err = m.escapeSlashCommandText(ctx, workspaceID, userID, text)
		if err != nil {
			return err
		}
	}
	if !parsed.SocketModeEnabled && slash.URL == "" {
		return ErrAppInteractionUnavailable
	}
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	triggerID, responseURL, capability, err := m.createInteractionCapabilities(ctx, snapshot.App.ID, workspaceID, userID, conversationID, "", "", responseBaseURL)
	if err != nil {
		return err
	}
	verificationToken, err := m.openAppVerificationToken(snapshot.App)
	if err != nil {
		return err
	}
	form := url.Values{
		"api_app_id":   {string(snapshot.App.ID)},
		"channel_id":   {string(conversation.ID)},
		"channel_name": {conversation.Name},
		"command":      {command},
		"response_url": {responseURL},
		"team_domain":  {workspace.Domain},
		"team_id":      {string(workspace.ID)},
		"text":         {strings.TrimSpace(text)},
		"token":        {verificationToken},
		"trigger_id":   {triggerID},
		"user_id":      {string(user.ID)},
		"user_name":    {user.Name},
	}
	if parsed.SocketModeEnabled {
		return m.enqueueSocketModeInteraction(ctx, snapshot.App.ID, workspaceID, userID, "slash_commands", formValuesObject(form), capability)
	}
	body, err := m.postSignedAppForm(ctx, slash.URL, snapshot.App, form)
	if err != nil {
		return err
	}
	return m.applyAppResponse(ctx, domain.AppResponseURL{
		AppID: snapshot.App.ID, WorkspaceID: workspaceID, UserID: userID, ConversationID: conversationID,
	}, body, "")
}

func (m Messages) DispatchBlockAction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, action domain.AppBlockAction, responseBaseURL string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if action.MessageID == "" || strings.TrimSpace(action.ActionID) == "" || strings.TrimSpace(action.Type) == "" {
		return ErrAppInteractionUnavailable
	}
	message, err := m.Store.GetMessage(ctx, action.MessageID)
	ephemeral := false
	if errors.Is(err, store.ErrNotFound) {
		value, ephemeralErr := m.Store.GetEphemeralMessage(ctx, workspaceID, userID, action.MessageID)
		if ephemeralErr != nil {
			return store.ErrNotFound
		}
		message = ephemeralAsMessage(value)
		ephemeral = true
	} else if err != nil {
		return err
	}
	if message.WorkspaceID != workspaceID || message.AppID == "" {
		return store.ErrNotFound
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, message.Conversation); err != nil {
		return err
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, message.AppID)
	if err != nil {
		return err
	}
	if !parsed.InteractivityEnabled || (!parsed.SocketModeEnabled && parsed.InteractivityRequestURL == "") {
		return ErrAppInteractionUnavailable
	}
	if !blocksContainDispatchableAction(message.Blocks, action.BlockID, action.ActionID, action.Type) {
		return store.ErrNotFound
	}
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, message.Conversation)
	if err != nil {
		return err
	}
	triggerID, responseURL, capability, err := m.createInteractionCapabilities(ctx, snapshot.App.ID, workspaceID, userID, message.Conversation, message.ID, message.ThreadTimestamp, responseBaseURL)
	if err != nil {
		return err
	}
	verificationToken, err := m.openAppVerificationToken(snapshot.App)
	if err != nil {
		return err
	}
	actionPayload := appBlockActionPayload(message.Blocks, action)
	payload := map[string]any{
		"type":       "block_actions",
		"api_app_id": snapshot.App.ID,
		"token":      verificationToken,
		"trigger_id": triggerID,
		"team":       map[string]any{"id": workspace.ID, "domain": workspace.Domain},
		"user":       map[string]any{"id": user.ID, "username": user.Name, "name": user.Name, "team_id": workspace.ID},
		"channel":    map[string]any{"id": conversation.ID, "name": conversation.Name},
		"container": map[string]any{
			"type": "message", "message_ts": domain.NewMessageTimestamp(message.CreatedAt),
			"channel_id": message.Conversation, "is_ephemeral": ephemeral,
		},
		"message":      appInteractionMessage(message),
		"response_url": responseURL,
		"actions":      []any{actionPayload},
		"state":        appBlockActionState(actionPayload),
	}
	if parsed.SocketModeEnabled {
		return m.enqueueSocketModeInteraction(ctx, snapshot.App.ID, workspaceID, userID, "interactive", payload, capability)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body, err := m.postSignedAppForm(ctx, parsed.InteractivityRequestURL, snapshot.App, url.Values{"payload": {string(encoded)}})
	if err != nil {
		return err
	}
	return m.applyAppResponse(ctx, domain.AppResponseURL{
		AppID: snapshot.App.ID, WorkspaceID: workspaceID, UserID: userID, ConversationID: message.Conversation,
		OriginalMessageID: message.ID, ThreadTimestamp: message.ThreadTimestamp,
	}, body, "")
}

// DispatchViewBlockAction delivers the block_actions contract for an
// interactive element inside a modal or App Home view. Unlike a message
// action, an acknowledgement body does not replace a message; the app uses the
// trigger_id to open or push a view and views.update/views.publish for
// asynchronous changes.
func (m Messages) DispatchViewBlockAction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, action domain.AppViewBlockAction, responseBaseURL string) error {
	if action.ViewID == "" || strings.TrimSpace(action.BlockID) == "" || strings.TrimSpace(action.ActionID) == "" || strings.TrimSpace(action.Type) == "" {
		return ErrAppInteractionUnavailable
	}
	current, snapshot, parsed, workspace, user, err := m.viewInteractionContext(ctx, workspaceID, userID, conversationID, action.ViewID, "")
	if err != nil {
		return err
	}
	if !viewContainsDispatchableAction(current.Payload, action.BlockID, action.ActionID, action.Type) {
		return store.ErrNotFound
	}
	var state map[string]any
	if json.Unmarshal([]byte(action.State), &state) != nil || state == nil {
		return ErrInvalidAppResponse
	}
	current.State = action.State
	current.UpdatedAt = time.Now().UTC()
	stateEvent, err := newEvent(current.WorkspaceID, userID, events.NewPayload("view.updated",
		events.String("view_id", string(current.ID)), events.String("app_id", string(current.AppID)),
		events.String("user_id", string(current.UserID)),
	), current.UpdatedAt)
	if err != nil {
		return err
	}
	current, err = m.Store.UpdateView(ctx, current, "", stateEvent)
	if err != nil {
		return err
	}
	triggerID, _, capability, err := m.createInteractionCapabilities(
		ctx, current.AppID, workspaceID, userID, conversationID, "", "", responseBaseURL,
	)
	if err != nil {
		return err
	}
	verificationToken, err := m.openAppVerificationToken(snapshot.App)
	if err != nil {
		return err
	}
	view, err := appInteractionView(current)
	if err != nil {
		return err
	}
	actionPayload := appBlockActionPayload(current.Payload, domain.AppBlockAction{
		BlockID: action.BlockID, ActionID: action.ActionID, Type: action.Type, Value: action.Value,
	})
	payload := map[string]any{
		"type":       "block_actions",
		"api_app_id": snapshot.App.ID,
		"token":      verificationToken,
		"trigger_id": triggerID,
		"team":       map[string]any{"id": workspace.ID, "domain": workspace.Domain},
		"user":       map[string]any{"id": user.ID, "username": user.Name, "name": user.Name, "team_id": workspace.ID},
		"container":  map[string]any{"type": "view", "view_id": current.ID},
		"view":       view,
		"actions":    []any{actionPayload},
		"state":      state,
	}
	if parsed.SocketModeEnabled {
		return m.enqueueSocketModeInteraction(ctx, snapshot.App.ID, workspaceID, userID, "interactive", payload, capability)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = m.postSignedAppForm(ctx, parsed.InteractivityRequestURL, snapshot.App, url.Values{"payload": {string(encoded)}})
	return err
}

// LoadAppOptions sends Slack's block_suggestion payload for a dynamic select
// and validates the owning app's response before it reaches the first-party
// client.
func (m Messages) LoadAppOptions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, query domain.AppOptionQuery, responseBaseURL string) ([]domain.AppOption, error) {
	query.BlockID = strings.TrimSpace(query.BlockID)
	query.ActionID = strings.TrimSpace(query.ActionID)
	query.Value = strings.TrimSpace(query.Value)
	if query.AppID == "" || query.BlockID == "" || query.ActionID == "" ||
		(query.MessageID == "" && query.ViewID == "") || (query.MessageID != "" && query.ViewID != "") ||
		len(query.Value) > 2000 {
		return nil, ErrAppInteractionUnavailable
	}
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return nil, err
	}
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, query.AppID)
	if err != nil {
		return nil, err
	}
	if !parsed.InteractivityEnabled || (!parsed.SocketModeEnabled && parsed.MessageMenuOptionsURL == "") {
		return nil, ErrAppInteractionUnavailable
	}
	payload := map[string]any{
		"type": "block_suggestion", "api_app_id": query.AppID,
		"team":      map[string]any{"id": workspace.ID, "domain": workspace.Domain},
		"user":      map[string]any{"id": user.ID, "username": user.Name, "name": user.Name, "team_id": workspace.ID},
		"block_id":  query.BlockID,
		"action_id": query.ActionID,
		"value":     query.Value,
	}
	if query.MessageID != "" {
		message, messageErr := m.Store.GetMessage(ctx, query.MessageID)
		if messageErr != nil || message.WorkspaceID != workspaceID || message.Conversation != conversationID || message.AppID != query.AppID {
			return nil, store.ErrNotFound
		}
		if !blocksContainAction(message.Blocks, query.BlockID, query.ActionID, "external_select", "multi_external_select") {
			return nil, store.ErrNotFound
		}
		payload["container"] = map[string]any{
			"type": "message", "message_ts": domain.NewMessageTimestamp(message.CreatedAt),
			"channel_id": message.Conversation, "is_ephemeral": false,
		}
		payload["channel"] = map[string]any{"id": message.Conversation}
		payload["message"] = appInteractionMessage(message)
	} else {
		current, _, _, _, _, contextErr := m.viewInteractionContext(ctx, workspaceID, userID, conversationID, query.ViewID, "")
		if contextErr != nil {
			return nil, contextErr
		}
		if current.AppID != query.AppID ||
			(!viewContainsAction(current.Payload, query.BlockID, query.ActionID, "external_select") &&
				!viewContainsAction(current.Payload, query.BlockID, query.ActionID, "multi_external_select")) {
			return nil, store.ErrNotFound
		}
		view, renderErr := appInteractionView(current)
		if renderErr != nil {
			return nil, renderErr
		}
		payload["container"] = map[string]any{"type": "view", "view_id": current.ID}
		payload["view"] = view
	}
	verificationToken, err := m.openAppVerificationToken(snapshot.App)
	if err != nil {
		return nil, err
	}
	payload["token"] = verificationToken
	if !parsed.SocketModeEnabled {
		encoded, encodeErr := json.Marshal(payload)
		if encodeErr != nil {
			return nil, encodeErr
		}
		body, requestErr := m.postSignedAppForm(ctx, parsed.MessageMenuOptionsURL, snapshot.App, url.Values{"payload": {string(encoded)}})
		if requestErr != nil {
			return nil, requestErr
		}
		return parseAppOptions(body)
	}
	_, _, capability, err := m.createInteractionCapabilities(ctx, query.AppID, workspaceID, userID, conversationID, "", "", responseBaseURL)
	if err != nil {
		return nil, err
	}
	envelopeID, err := m.enqueueSocketModeInteractionWithID(ctx, query.AppID, workspaceID, userID, "interactive", payload, capability)
	if err != nil {
		return nil, err
	}
	deadline := time.NewTimer(appRequestTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, responseErr := m.Store.GetSocketModeResponse(ctx, query.AppID, envelopeID)
		if responseErr == nil {
			return parseAppOptions([]byte(response.Payload))
		}
		if !errors.Is(responseErr, store.ErrNotFound) {
			return nil, responseErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, ErrAppInteractionUnavailable
		case <-ticker.C:
		}
	}
}

func blocksContainAction(blocks, blockID, actionID string, actionTypes ...string) bool {
	return viewContainsAction(`{"blocks":`+blocks+`}`, blockID, actionID, actionTypes...)
}

func blocksContainDispatchableAction(blocks, blockID, actionID string, actionTypes ...string) bool {
	return viewContainsDispatchableAction(`{"blocks":`+blocks+`}`, blockID, actionID, actionTypes...)
}

func parseAppOptions(body []byte) ([]domain.AppOption, error) {
	var response struct {
		Options      []map[string]any `json:"options"`
		OptionGroups []struct {
			Label   map[string]any   `json:"label"`
			Options []map[string]any `json:"options"`
		} `json:"option_groups"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &response) != nil ||
		(response.Options == nil && response.OptionGroups == nil) ||
		(len(response.Options) != 0 && len(response.OptionGroups) != 0) ||
		len(response.Options) > 100 || len(response.OptionGroups) > 100 {
		return nil, ErrInvalidAppResponse
	}
	result := make([]domain.AppOption, 0, len(response.Options))
	appendOption := func(raw map[string]any, group string) error {
		text := appOptionTextObject(raw["text"])
		value := strings.TrimSpace(stringValue(raw["value"]))
		description := appOptionTextObject(raw["description"])
		if text == "" || value == "" || len([]rune(text)) > 75 || len([]rune(value)) > 75 || len([]rune(description)) > 75 {
			return ErrInvalidAppResponse
		}
		result = append(result, domain.AppOption{Text: text, Value: value, Description: description, Group: group})
		return nil
	}
	for _, option := range response.Options {
		if err := appendOption(option, ""); err != nil {
			return nil, err
		}
	}
	for _, group := range response.OptionGroups {
		label := appOptionTextObject(group.Label)
		if label == "" || len([]rune(label)) > 75 || len(group.Options) > 100 {
			return nil, ErrInvalidAppResponse
		}
		for _, option := range group.Options {
			if err := appendOption(option, label); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func appOptionTextObject(value any) string {
	object, _ := value.(map[string]any)
	if kind := strings.TrimSpace(stringValue(object["type"])); kind != "plain_text" {
		return ""
	}
	return strings.TrimSpace(stringValue(object["text"]))
}

func viewContainsAction(payload, blockID, actionID string, actionTypes ...string) bool {
	var view struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if json.Unmarshal([]byte(payload), &view) != nil {
		return false
	}
	for _, block := range view.Blocks {
		if strings.TrimSpace(stringValue(block["block_id"])) != blockID {
			continue
		}
		var candidates []any
		if elements, ok := block["elements"].([]any); ok {
			candidates = append(candidates, elements...)
		}
		for _, name := range []string{"element", "accessory"} {
			if element, ok := block[name].(map[string]any); ok {
				candidates = append(candidates, element)
			}
		}
		for _, raw := range candidates {
			element, _ := raw.(map[string]any)
			if strings.TrimSpace(stringValue(element["action_id"])) == actionID {
				elementType := strings.TrimSpace(stringValue(element["type"]))
				for _, actionType := range actionTypes {
					if elementType == actionType {
						return true
					}
				}
			}
		}
	}
	return false
}

func viewContainsDispatchableAction(payload, blockID, actionID string, actionTypes ...string) bool {
	var view struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if json.Unmarshal([]byte(payload), &view) != nil {
		return false
	}
	for _, block := range view.Blocks {
		if strings.TrimSpace(stringValue(block["block_id"])) != blockID {
			continue
		}
		if strings.TrimSpace(stringValue(block["type"])) == "input" {
			dispatch, _ := block["dispatch_action"].(bool)
			if !dispatch {
				return false
			}
		}
		encoded, err := json.Marshal(map[string]any{"blocks": []map[string]any{block}})
		return err == nil && viewContainsAction(string(encoded), blockID, actionID, actionTypes...)
	}
	return false
}

func (m Messages) SubmitView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, viewID domain.ViewID, stateJSON, responseBaseURL string) (domain.ViewInteractionResult, error) {
	current, snapshot, parsed, workspace, user, err := m.viewInteractionContext(ctx, workspaceID, userID, conversationID, viewID, "modal")
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	var state map[string]any
	if json.Unmarshal([]byte(stateJSON), &state) != nil || state == nil {
		return domain.ViewInteractionResult{}, ErrInvalidAppResponse
	}
	current.State = stateJSON
	current.Errors = nil
	current.UpdatedAt = time.Now().UTC()
	stateEvent, err := newEvent(current.WorkspaceID, userID, events.NewPayload("view.updated",
		events.String("view_id", string(current.ID)), events.String("app_id", string(current.AppID)),
		events.String("user_id", string(current.UserID)),
	), current.UpdatedAt)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	current, err = m.Store.UpdateView(ctx, current, "", stateEvent)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	view, err := appInteractionView(current)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	view["state"] = state
	_, _, capability, err := m.createInteractionCapabilities(ctx, current.AppID, workspaceID, userID, conversationID, "", "", responseBaseURL)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	verificationToken, err := m.openAppVerificationToken(snapshot.App)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	payload := map[string]any{
		"type": "view_submission", "api_app_id": current.AppID, "token": verificationToken,
		"team": map[string]any{"id": workspace.ID, "domain": workspace.Domain},
		"user": map[string]any{"id": user.ID, "username": user.Name, "name": user.Name, "team_id": workspace.ID},
		"view": view,
	}
	if parsed.SocketModeEnabled {
		if err := m.enqueueSocketModeInteraction(ctx, current.AppID, workspaceID, userID, "interactive", payload, capability); err != nil {
			return domain.ViewInteractionResult{}, err
		}
		return domain.ViewInteractionResult{Pending: true}, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	body, err := m.postSignedAppForm(ctx, parsed.InteractivityRequestURL, snapshot.App, url.Values{"payload": {string(encoded)}})
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	return m.applyViewSubmissionResponse(ctx, current, userID, body, "")
}

func (m Messages) CloseView(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, viewID domain.ViewID, clear bool, responseBaseURL string) error {
	current, snapshot, parsed, workspace, user, err := m.viewInteractionContext(ctx, workspaceID, userID, conversationID, viewID, "modal")
	if err != nil {
		return err
	}
	var envelope struct {
		NotifyOnClose bool `json:"notify_on_close"`
	}
	_ = json.Unmarshal([]byte(current.Payload), &envelope)
	var notifyErr error
	if envelope.NotifyOnClose {
		view, renderErr := appInteractionView(current)
		if renderErr != nil {
			return renderErr
		}
		_, _, capability, capabilityErr := m.createInteractionCapabilities(ctx, current.AppID, workspaceID, userID, conversationID, "", "", responseBaseURL)
		if capabilityErr != nil {
			return capabilityErr
		}
		verificationToken, tokenErr := m.openAppVerificationToken(snapshot.App)
		if tokenErr != nil {
			return tokenErr
		}
		payload := map[string]any{
			"type": "view_closed", "api_app_id": current.AppID, "token": verificationToken, "is_cleared": clear,
			"team": map[string]any{"id": workspace.ID, "domain": workspace.Domain},
			"user": map[string]any{"id": user.ID, "username": user.Name, "name": user.Name, "team_id": workspace.ID},
			"view": view,
		}
		if parsed.SocketModeEnabled {
			notifyErr = m.enqueueSocketModeInteraction(ctx, current.AppID, workspaceID, userID, "interactive", payload, capability)
		} else {
			encoded, encodeErr := json.Marshal(payload)
			if encodeErr != nil {
				return encodeErr
			}
			_, notifyErr = m.postSignedAppForm(ctx, parsed.InteractivityRequestURL, snapshot.App, url.Values{"payload": {string(encoded)}})
		}
	}
	if err := m.deleteView(ctx, workspaceID, userID, current, clear, "view.closed"); err != nil {
		return err
	}
	return notifyErr
}

func (m Messages) viewInteractionContext(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, viewID domain.ViewID, requiredType string) (domain.View, domain.AppManifestSnapshot, appmanifest.Parsed, domain.Workspace, domain.User, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.View{}, domain.AppManifestSnapshot{}, appmanifest.Parsed{}, domain.Workspace{}, domain.User{}, err
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return domain.View{}, domain.AppManifestSnapshot{}, appmanifest.Parsed{}, domain.Workspace{}, domain.User{}, err
	}
	current, err := m.Store.GetView(ctx, workspaceID, viewID)
	if err != nil || current.UserID != userID ||
		(requiredType != "" && current.Type != requiredType) ||
		(requiredType == "" && current.Type != "modal" && current.Type != "home") {
		return domain.View{}, domain.AppManifestSnapshot{}, appmanifest.Parsed{}, domain.Workspace{}, domain.User{}, store.ErrNotFound
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, current.AppID)
	if err != nil {
		return domain.View{}, domain.AppManifestSnapshot{}, appmanifest.Parsed{}, domain.Workspace{}, domain.User{}, err
	}
	if !parsed.InteractivityEnabled || (!parsed.SocketModeEnabled && parsed.InteractivityRequestURL == "") {
		return domain.View{}, domain.AppManifestSnapshot{}, appmanifest.Parsed{}, domain.Workspace{}, domain.User{}, ErrAppInteractionUnavailable
	}
	if current.Type == "home" {
		if !parsed.HomeTabEnabled {
			return domain.View{}, domain.AppManifestSnapshot{}, appmanifest.Parsed{}, domain.Workspace{}, domain.User{}, ErrAppHomeNotEnabled
		}
		published, err := m.Store.GetPublishedView(ctx, workspaceID, userID, current.AppID)
		if err != nil || published.ID != current.ID {
			return domain.View{}, domain.AppManifestSnapshot{}, appmanifest.Parsed{}, domain.Workspace{}, domain.User{}, store.ErrNotFound
		}
	}
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return domain.View{}, domain.AppManifestSnapshot{}, appmanifest.Parsed{}, domain.Workspace{}, domain.User{}, err
	}
	user, err := m.Store.GetUser(ctx, userID)
	return current, snapshot, parsed, workspace, user, err
}

func appInteractionView(value domain.View) (map[string]any, error) {
	result := make(map[string]any)
	if json.Unmarshal([]byte(value.Payload), &result) != nil || result == nil {
		return nil, ErrInvalidAppResponse
	}
	result["id"] = value.ID
	result["team_id"] = value.WorkspaceID
	result["app_id"] = value.AppID
	result["hash"] = value.Hash
	result["root_view_id"] = value.RootViewID
	result["previous_view_id"] = value.PreviousViewID
	result["external_id"] = value.ExternalID
	return result, nil
}

type viewSubmissionResponse struct {
	ResponseAction string            `json:"response_action"`
	Errors         map[string]string `json:"errors"`
	View           json.RawMessage   `json:"view"`
}

func (m Messages) applyViewSubmissionResponse(ctx context.Context, current domain.View, actor domain.UserID, body []byte, idempotencyKey string) (domain.ViewInteractionResult, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		err := m.deleteView(ctx, current.WorkspaceID, actor, current, false, "view.submitted")
		if idempotencyKey != "" && errors.Is(err, store.ErrNotFound) {
			err = nil
		}
		return domain.ViewInteractionResult{}, err
	}
	var response viewSubmissionResponse
	if json.Unmarshal(body, &response) != nil {
		return domain.ViewInteractionResult{}, ErrInvalidAppResponse
	}
	if response.ResponseAction == "" && len(response.Errors) == 0 && len(response.View) == 0 {
		err := m.deleteView(ctx, current.WorkspaceID, actor, current, false, "view.submitted")
		if idempotencyKey != "" && errors.Is(err, store.ErrNotFound) {
			err = nil
		}
		return domain.ViewInteractionResult{}, err
	}
	switch response.ResponseAction {
	case "errors":
		if len(response.Errors) == 0 {
			return domain.ViewInteractionResult{}, ErrInvalidAppResponse
		}
		for blockID, message := range response.Errors {
			if strings.TrimSpace(blockID) == "" || strings.TrimSpace(message) == "" {
				return domain.ViewInteractionResult{}, ErrInvalidAppResponse
			}
		}
		if idempotencyKey == "" || !reflect.DeepEqual(current.Errors, response.Errors) {
			value := current
			value.Errors = response.Errors
			value.UpdatedAt = time.Now().UTC()
			event, err := newEvent(value.WorkspaceID, actor, events.NewPayload("view.updated",
				events.String("view_id", string(value.ID)), events.String("app_id", string(value.AppID)),
				events.String("user_id", string(value.UserID)),
			), value.UpdatedAt)
			if err != nil {
				return domain.ViewInteractionResult{}, err
			}
			if _, err := m.Store.UpdateView(ctx, value, "", event); err != nil {
				return domain.ViewInteractionResult{}, err
			}
		}
		return domain.ViewInteractionResult{Errors: response.Errors}, nil
	case "clear":
		err := m.deleteView(ctx, current.WorkspaceID, actor, current, true, "view.submitted")
		if idempotencyKey != "" && errors.Is(err, store.ErrNotFound) {
			err = nil
		}
		return domain.ViewInteractionResult{}, err
	case "update":
		if len(response.View) == 0 {
			return domain.ViewInteractionResult{}, ErrInvalidAppResponse
		}
		payload := string(response.View)
		if idempotencyKey != "" && strings.TrimSpace(current.Payload) == strings.TrimSpace(payload) {
			return domain.ViewInteractionResult{}, nil
		}
		_, err := m.updateView(ctx, current.WorkspaceID, actor, current, payload, "", "view.updated")
		return domain.ViewInteractionResult{}, err
	case "push":
		if len(response.View) == 0 {
			return domain.ViewInteractionResult{}, ErrInvalidAppResponse
		}
		if idempotencyKey != "" {
			latest, err := m.Store.GetLatestView(ctx, current.WorkspaceID, current.UserID, current.AppID, "modal")
			if err == nil && latest.PreviousViewID == current.ID && strings.TrimSpace(latest.Payload) == strings.TrimSpace(string(response.View)) {
				return domain.ViewInteractionResult{}, nil
			}
		}
		if depth, err := m.viewStackDepth(ctx, current); err != nil {
			return domain.ViewInteractionResult{}, err
		} else if depth >= 3 {
			return domain.ViewInteractionResult{}, ErrInvalidAppResponse
		}
		_, err := m.createView(ctx, current.WorkspaceID, current.AppID, current.UserID, string(response.View), current.RootViewID, current.ID, "", "view.pushed")
		return domain.ViewInteractionResult{}, err
	default:
		return domain.ViewInteractionResult{}, ErrInvalidAppResponse
	}
}

func (m Messages) viewStackDepth(ctx context.Context, current domain.View) (int, error) {
	depth := 1
	for current.PreviousViewID != "" {
		parent, err := m.Store.GetView(ctx, current.WorkspaceID, current.PreviousViewID)
		if err != nil {
			return 0, err
		}
		if parent.AppID != current.AppID || parent.UserID != current.UserID || parent.RootViewID != current.RootViewID {
			return 0, ErrInvalidAppResponse
		}
		depth++
		current = parent
		if depth > 3 {
			return depth, nil
		}
	}
	return depth, nil
}

func (m Messages) ListAppShortcuts(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, shortcutType string) ([]domain.AppShortcut, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	shortcutType = strings.TrimSpace(shortcutType)
	if shortcutType != "global" && shortcutType != "message" && shortcutType != "slash" {
		return nil, store.InvalidArgument("shortcut type must be global, message, or slash")
	}
	snapshots, err := m.Store.ListInstalledApps(ctx)
	if err != nil {
		return nil, err
	}
	var result []domain.AppShortcut
	type slashCandidate struct {
		shortcut    domain.AppShortcut
		installedAt time.Time
	}
	slashCommands := make(map[string]slashCandidate)
	for _, snapshot := range snapshots {
		installation, installed, err := m.appInstallationInWorkspace(ctx, snapshot.App.ID, workspaceID)
		if err != nil {
			return nil, err
		}
		if !installed {
			continue
		}
		parsed, problems := appmanifest.Parse(snapshot.Manifest)
		if len(problems) != 0 || !containsString(parsed.BotScopes, "commands") {
			continue
		}
		if shortcutType == "slash" {
			for _, command := range parsed.SlashCommands {
				candidate := slashCandidate{
					shortcut: domain.AppShortcut{
						AppID: snapshot.App.ID, AppName: snapshot.App.Name, Name: command.Command,
						Description: command.Description, Type: "slash", Command: command.Command,
						UsageHint: command.UsageHint, ShouldEscape: command.ShouldEscape,
					},
					installedAt: installation.CreatedAt,
				}
				current, exists := slashCommands[command.Command]
				if !exists || candidate.installedAt.After(current.installedAt) ||
					(candidate.installedAt.Equal(current.installedAt) && string(candidate.shortcut.AppID) > string(current.shortcut.AppID)) {
					slashCommands[command.Command] = candidate
				}
			}
			continue
		}
		if !parsed.InteractivityEnabled {
			continue
		}
		for _, shortcut := range parsed.Shortcuts {
			if shortcut.Type == shortcutType {
				result = append(result, domain.AppShortcut{
					AppID: snapshot.App.ID, AppName: snapshot.App.Name, Name: shortcut.Name,
					CallbackID: shortcut.CallbackID, Description: shortcut.Description, Type: shortcut.Type,
				})
			}
		}
	}
	if shortcutType == "slash" {
		result = make([]domain.AppShortcut, 0, len(slashCommands))
		for _, candidate := range slashCommands {
			result = append(result, candidate.shortcut)
		}
	}
	slices.SortFunc(result, func(left, right domain.AppShortcut) int {
		if order := strings.Compare(strings.ToLower(left.Command), strings.ToLower(right.Command)); order != 0 {
			return order
		}
		if order := strings.Compare(strings.ToLower(left.AppName), strings.ToLower(right.AppName)); order != 0 {
			return order
		}
		if order := strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)); order != 0 {
			return order
		}
		return strings.Compare(left.CallbackID, right.CallbackID)
	})
	return result, nil
}

func (m Messages) DispatchAppShortcut(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, appID domain.AppID, callbackID string, messageID domain.MessageID, responseBaseURL string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	conversation, err := m.Store.GetConversation(ctx, conversationID)
	if err != nil || conversation.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversationID); err != nil {
		return err
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, appID)
	if err != nil {
		return err
	}
	if !parsed.InteractivityEnabled || (!parsed.SocketModeEnabled && parsed.InteractivityRequestURL == "") ||
		!containsString(parsed.BotScopes, "commands") {
		return ErrAppInteractionUnavailable
	}
	callbackID = strings.TrimSpace(callbackID)
	shortcutType := "global"
	if messageID != "" {
		shortcutType = "message"
	}
	var matched *appmanifest.Shortcut
	for index := range parsed.Shortcuts {
		if parsed.Shortcuts[index].CallbackID == callbackID && parsed.Shortcuts[index].Type == shortcutType {
			if matched != nil {
				return store.ErrConflict
			}
			matched = &parsed.Shortcuts[index]
		}
	}
	if matched == nil {
		return store.ErrNotFound
	}
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	var source domain.Message
	if shortcutType == "message" {
		source, err = m.Store.GetMessage(ctx, messageID)
		if err != nil || source.WorkspaceID != workspaceID || source.Conversation != conversationID {
			return store.ErrNotFound
		}
	}
	triggerID, responseURL, capability, err := m.createInteractionCapabilities(ctx, appID, workspaceID, userID, conversationID, messageID, source.ThreadTimestamp, responseBaseURL)
	if err != nil {
		return err
	}
	verificationToken, err := m.openAppVerificationToken(snapshot.App)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"type": shortcutPayloadType(shortcutType), "token": verificationToken,
		"action_ts": domain.NewMessageTimestamp(time.Now().UTC()), "callback_id": callbackID,
		"trigger_id": triggerID, "team": map[string]any{"id": workspace.ID, "domain": workspace.Domain},
		"user":       map[string]any{"id": user.ID, "username": user.Name, "name": user.Name, "team_id": workspace.ID},
		"api_app_id": snapshot.App.ID,
	}
	if shortcutType == "message" {
		payload["channel"] = map[string]any{"id": conversation.ID, "name": conversation.Name}
		payload["message"] = appInteractionMessage(source)
		payload["message_ts"] = domain.NewMessageTimestamp(source.CreatedAt)
		payload["response_url"] = responseURL
	}
	if parsed.SocketModeEnabled {
		return m.enqueueSocketModeInteraction(ctx, snapshot.App.ID, workspaceID, userID, "interactive", payload, capability)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = m.postSignedAppForm(ctx, parsed.InteractivityRequestURL, snapshot.App, url.Values{"payload": {string(encoded)}})
	return err
}

func shortcutPayloadType(shortcutType string) string {
	if shortcutType == "message" {
		return "message_action"
	}
	return "shortcut"
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// appBlockActionPayload mirrors Slack's per-element action shape. A generic
// value field is only correct for buttons and text inputs: select menus,
// pickers, and multi-value controls use distinct fields that official Bolt
// adapters deserialize into distinct action types.
func appBlockActionPayload(blocks string, action domain.AppBlockAction) map[string]any {
	result := map[string]any{
		"action_id": action.ActionID,
		"block_id":  action.BlockID,
		"type":      action.Type,
		"action_ts": domain.NewMessageTimestamp(time.Now().UTC()),
	}
	option := func(value string) map[string]any {
		selected := map[string]any{"value": value}
		if text := appBlockOptionText(blocks, action.BlockID, action.ActionID, value); text != "" {
			selected["text"] = map[string]any{"type": "plain_text", "text": text, "emoji": true}
		}
		return selected
	}
	switch action.Type {
	case "static_select", "overflow", "radio_buttons", "external_select":
		if action.Value != "" {
			result["selected_option"] = option(action.Value)
		}
	case "multi_static_select", "multi_external_select", "checkboxes":
		values := splitActionValues(action.Value)
		selected := make([]map[string]any, 0, len(values))
		for _, value := range values {
			selected = append(selected, option(value))
		}
		result["selected_options"] = selected
	case "datepicker":
		result["selected_date"] = action.Value
	case "timepicker":
		result["selected_time"] = action.Value
	case "datetimepicker":
		if unix, err := strconv.ParseInt(strings.TrimSpace(action.Value), 10, 64); err == nil {
			result["selected_date_time"] = unix
		}
	case "users_select":
		result["selected_user"] = action.Value
	case "multi_users_select":
		result["selected_users"] = splitActionValues(action.Value)
	case "conversations_select":
		result["selected_conversation"] = action.Value
	case "multi_conversations_select":
		result["selected_conversations"] = splitActionValues(action.Value)
	case "channels_select":
		result["selected_channel"] = action.Value
	case "multi_channels_select":
		result["selected_channels"] = splitActionValues(action.Value)
	case "plain_text_input":
		result["value"] = action.Value
	case "rich_text_input":
		var value any
		if json.Unmarshal([]byte(action.Value), &value) == nil {
			result["rich_text_value"] = value
		}
	default:
		if action.Value != "" {
			result["value"] = action.Value
		}
	}
	return result
}

func splitActionValues(value string) []string {
	var values []string
	if json.Unmarshal([]byte(value), &values) == nil {
		result := values[:0]
		for _, candidate := range values {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				result = append(result, candidate)
			}
		}
		return result
	}
	var result []string
	for _, candidate := range strings.Split(value, ",") {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			result = append(result, candidate)
		}
	}
	return result
}

func appBlockOptionText(blocks, blockID, actionID, selectedValue string) string {
	var values []map[string]any
	if json.Unmarshal([]byte(blocks), &values) != nil {
		return ""
	}
	for _, block := range values {
		if strings.TrimSpace(fmt.Sprint(block["block_id"])) != blockID {
			continue
		}
		var elements []map[string]any
		if rawElements, ok := block["elements"].([]any); ok {
			for _, rawElement := range rawElements {
				if element, ok := rawElement.(map[string]any); ok {
					elements = append(elements, element)
				}
			}
		}
		for _, field := range []string{"element", "accessory"} {
			if element, ok := block[field].(map[string]any); ok {
				elements = append(elements, element)
			}
		}
		for _, element := range elements {
			if strings.TrimSpace(fmt.Sprint(element["action_id"])) != actionID {
				continue
			}
			optionLists := []any{element["options"]}
			if groups, ok := element["option_groups"].([]any); ok {
				for _, rawGroup := range groups {
					if group, ok := rawGroup.(map[string]any); ok {
						optionLists = append(optionLists, group["options"])
					}
				}
			}
			for _, rawList := range optionLists {
				options, _ := rawList.([]any)
				for _, rawOption := range options {
					option, _ := rawOption.(map[string]any)
					if fmt.Sprint(option["value"]) != selectedValue {
						continue
					}
					text, _ := option["text"].(map[string]any)
					return strings.TrimSpace(fmt.Sprint(text["text"]))
				}
			}
		}
	}
	return ""
}

func appBlockActionState(action map[string]any) map[string]any {
	blockID, _ := action["block_id"].(string)
	actionID, _ := action["action_id"].(string)
	state := make(map[string]any, len(action))
	for key, value := range action {
		if key != "block_id" && key != "action_ts" {
			state[key] = value
		}
	}
	return map[string]any{"values": map[string]any{blockID: map[string]any{actionID: state}}}
}

func (m Messages) HandleAppResponse(ctx context.Context, responseToken, payload string) error {
	response, err := m.Store.UseAppResponseURL(ctx, domain.HashToken(strings.TrimSpace(responseToken)))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrInvalidAppResponse
		}
		return err
	}
	return m.applyAppResponse(ctx, response, []byte(payload), "")
}

func (m Messages) HandleSocketModeResponse(ctx context.Context, appID domain.AppID, envelopeID string, payload []byte) error {
	interaction, err := m.Store.GetSocketModeInteraction(ctx, appID, strings.TrimSpace(envelopeID))
	if errors.Is(err, store.ErrNotFound) {
		return m.Store.RecordSocketModeResponse(ctx, domain.SocketModeResponse{
			AppID: appID, EnvelopeID: strings.TrimSpace(envelopeID), Payload: string(payload), ReceivedAt: time.Now().UTC(),
		})
	}
	if err != nil {
		return err
	}
	var interactionPayload struct {
		Type      string `json:"type"`
		Container struct {
			Type string `json:"type"`
		} `json:"container"`
	}
	if json.Unmarshal([]byte(interaction.Payload), &interactionPayload) == nil &&
		(interactionPayload.Type == "shortcut" || interactionPayload.Type == "message_action" ||
			(interactionPayload.Type == "block_actions" && interactionPayload.Container.Type == "view") ||
			interactionPayload.Type == "view_closed") {
		return nil
	}
	if interactionPayload.Type == "block_suggestion" {
		return m.Store.RecordSocketModeResponse(ctx, domain.SocketModeResponse{
			AppID: appID, EnvelopeID: strings.TrimSpace(envelopeID), Payload: string(payload), ReceivedAt: time.Now().UTC(),
		})
	}
	if interactionPayload.Type == "view_submission" {
		var submitted struct {
			View struct {
				ID domain.ViewID `json:"id"`
			} `json:"view"`
		}
		if json.Unmarshal([]byte(interaction.Payload), &submitted) != nil || submitted.View.ID == "" {
			return ErrInvalidAppResponse
		}
		current, err := m.Store.GetView(ctx, interaction.WorkspaceID, submitted.View.ID)
		if errors.Is(err, store.ErrNotFound) && emptyViewAcknowledgement(payload) {
			return nil
		}
		if err != nil || current.AppID != appID || current.UserID != interaction.UserID {
			return ErrInvalidAppResponse
		}
		_, err = m.applyViewSubmissionResponse(ctx, current, interaction.UserID, payload, "socket-mode:"+string(appID)+":"+interaction.EnvelopeID)
		return err
	}
	return m.applyAppResponse(ctx, interaction.Response, payload, "socket-mode:"+string(appID)+":"+interaction.EnvelopeID)
}

func emptyViewAcknowledgement(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return true
	}
	var response viewSubmissionResponse
	return json.Unmarshal(payload, &response) == nil && response.ResponseAction == "" && len(response.Errors) == 0 && len(response.View) == 0
}

func (m Messages) createInteractionCapabilities(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, originalMessageID domain.MessageID, threadTimestamp domain.MessageTimestamp, responseBaseURL string) (string, string, domain.AppResponseURL, error) {
	base, err := url.Parse(strings.TrimSpace(responseBaseURL))
	if err != nil || !base.IsAbs() || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", "", domain.AppResponseURL{}, ErrAppInteractionUnavailable
	}
	triggerID, err := domain.PublicID("trigger_")
	if err != nil {
		return "", "", domain.AppResponseURL{}, err
	}
	responseToken, err := domain.PublicID("response_")
	if err != nil {
		return "", "", domain.AppResponseURL{}, err
	}
	now := time.Now().UTC()
	trigger := domain.AppTrigger{
		TokenHash: domain.HashToken(triggerID), AppID: appID, WorkspaceID: workspaceID, UserID: userID,
		CreatedAt: now, ExpiresAt: now.Add(appTriggerLifetime),
	}
	response := domain.AppResponseURL{
		TokenHash: domain.HashToken(responseToken), AppID: appID, WorkspaceID: workspaceID, UserID: userID,
		ConversationID: conversationID, OriginalMessageID: originalMessageID, ThreadTimestamp: threadTimestamp,
		CreatedAt: now, ExpiresAt: now.Add(appResponseLifetime), UsesRemaining: appResponseUses,
	}
	if err := m.Store.CreateAppInteractionCapabilities(ctx, trigger, response); err != nil {
		return "", "", domain.AppResponseURL{}, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/app-response/" + url.PathEscape(responseToken)
	base.RawQuery = ""
	base.Fragment = ""
	return triggerID, base.String(), response, nil
}

func formValuesObject(values url.Values) map[string]any {
	result := make(map[string]any, len(values))
	for name, entries := range values {
		if len(entries) != 0 {
			result[name] = entries[0]
		}
	}
	return result
}

func (m Messages) enqueueSocketModeInteraction(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID, userID domain.UserID, interactionType string, payload map[string]any, response domain.AppResponseURL) error {
	_, err := m.enqueueSocketModeInteractionWithID(ctx, appID, workspaceID, userID, interactionType, payload, response)
	return err
}

func (m Messages) enqueueSocketModeInteractionWithID(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID, userID domain.UserID, interactionType string, payload map[string]any, response domain.AppResponseURL) (string, error) {
	envelopeID, err := domain.PublicID("envelope_")
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	err = m.Store.CreateSocketModeInteraction(ctx, domain.SocketModeInteraction{
		EnvelopeID: envelopeID, AppID: appID, WorkspaceID: workspaceID, UserID: userID,
		Type: interactionType, Payload: string(encoded), Response: response, CreatedAt: time.Now().UTC(),
	})
	return envelopeID, err
}

func (m Messages) slashCommandApp(ctx context.Context, workspaceID domain.WorkspaceID, command string) (domain.AppManifestSnapshot, appmanifest.Parsed, appmanifest.SlashCommand, error) {
	snapshots, err := m.Store.ListInstalledApps(ctx)
	if err != nil {
		return domain.AppManifestSnapshot{}, appmanifest.Parsed{}, appmanifest.SlashCommand{}, err
	}
	var matchedSnapshot domain.AppManifestSnapshot
	var matchedParsed appmanifest.Parsed
	var matchedCommand appmanifest.SlashCommand
	var installedAt time.Time
	for _, snapshot := range snapshots {
		parsed, problems := appmanifest.Parse(snapshot.Manifest)
		if len(problems) != 0 {
			continue
		}
		installation, installed, err := m.appInstallationInWorkspace(ctx, snapshot.App.ID, workspaceID)
		if err != nil {
			return domain.AppManifestSnapshot{}, appmanifest.Parsed{}, appmanifest.SlashCommand{}, err
		}
		if !installed {
			continue
		}
		for _, candidate := range parsed.SlashCommands {
			if candidate.Command == command {
				if matchedSnapshot.App.ID == "" || installation.CreatedAt.After(installedAt) ||
					(installation.CreatedAt.Equal(installedAt) && string(snapshot.App.ID) > string(matchedSnapshot.App.ID)) {
					matchedSnapshot, matchedParsed, matchedCommand = snapshot, parsed, candidate
					installedAt = installation.CreatedAt
				}
			}
		}
	}
	if matchedSnapshot.App.ID == "" {
		return domain.AppManifestSnapshot{}, appmanifest.Parsed{}, appmanifest.SlashCommand{}, ErrSlashCommandNotFound
	}
	return matchedSnapshot, matchedParsed, matchedCommand, nil
}

func (m Messages) installedApp(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID) (domain.AppManifestSnapshot, appmanifest.Parsed, error) {
	snapshots, err := m.Store.ListInstalledApps(ctx)
	if err != nil {
		return domain.AppManifestSnapshot{}, appmanifest.Parsed{}, err
	}
	for _, snapshot := range snapshots {
		if snapshot.App.ID != appID {
			continue
		}
		installed, err := m.appInstalledInWorkspace(ctx, appID, workspaceID)
		if err != nil {
			return domain.AppManifestSnapshot{}, appmanifest.Parsed{}, err
		}
		if !installed {
			break
		}
		parsed, problems := appmanifest.Parse(snapshot.Manifest)
		if len(problems) != 0 {
			return domain.AppManifestSnapshot{}, appmanifest.Parsed{}, ErrAppInteractionUnavailable
		}
		return snapshot, parsed, nil
	}
	return domain.AppManifestSnapshot{}, appmanifest.Parsed{}, store.ErrNotFound
}

func (m Messages) appInstalledInWorkspace(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID) (bool, error) {
	_, installed, err := m.appInstallationInWorkspace(ctx, appID, workspaceID)
	return installed, err
}

func (m Messages) appInstallationInWorkspace(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID) (domain.AppInstallation, bool, error) {
	installations, err := m.Store.ListAppInstallations(ctx, appID)
	if err != nil {
		return domain.AppInstallation{}, false, err
	}
	for _, installation := range installations {
		if installation.WorkspaceID == workspaceID && installation.Enabled {
			return installation, true, nil
		}
	}
	return domain.AppInstallation{}, false, nil
}

var (
	slashUserReference    = regexp.MustCompile(`(^|[[:space:](])@([[:alnum:]_.-]+)`)
	slashChannelReference = regexp.MustCompile(`(^|[[:space:](])#([[:alnum:]_-]+)`)
	slashURLReference     = regexp.MustCompile(`https?://[^\s<>]+`)
)

// escapeSlashCommandText implements the manifest's should_escape contract
// before either HTTP or Socket Mode delivery. Slack resolves human-readable
// mentions to stable IDs and wraps links; keeping this in the service makes
// local and remote composition produce the same app payload.
func (m Messages) escapeSlashCommandText(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, text string) (string, error) {
	users := make(map[string]domain.UserID)
	var cursor domain.Cursor
	for {
		page, err := m.Store.ListUsers(ctx, workspaceID, domain.PageRequest{Limit: 200, Cursor: cursor})
		if err != nil {
			return "", err
		}
		for _, user := range page.Users {
			if user.Deleted {
				continue
			}
			for _, name := range []string{user.Name, user.Profile.DisplayName} {
				if name = strings.ToLower(strings.TrimSpace(name)); name != "" && !strings.ContainsAny(name, " \t\r\n") {
					users[name] = user.ID
				}
			}
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	channels := make(map[string]domain.Conversation)
	cursor = ""
	for {
		page, err := m.Store.ListConversations(ctx, workspaceID, userID, domain.ConversationListRequest{Limit: 200, Cursor: cursor})
		if err != nil {
			return "", err
		}
		for _, conversation := range page.Conversations {
			if conversation.Name != "" && !conversation.IsDirect && !conversation.IsGroupDirect {
				channels[strings.ToLower(conversation.Name)] = conversation
			}
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	text = slashUserReference.ReplaceAllStringFunc(text, func(match string) string {
		prefixLength := 0
		if match[0] != '@' {
			prefixLength = 1
		}
		name := strings.ToLower(match[prefixLength+1:])
		if id := users[name]; id != "" {
			return match[:prefixLength] + "<@" + string(id) + ">"
		}
		return match
	})
	text = slashChannelReference.ReplaceAllStringFunc(text, func(match string) string {
		prefixLength := 0
		if match[0] != '#' {
			prefixLength = 1
		}
		name := strings.ToLower(match[prefixLength+1:])
		conversation, ok := channels[name]
		if !ok {
			return match
		}
		if conversation.IsPrivate {
			return match[:prefixLength] + "<#" + string(conversation.ID) + "|>"
		}
		return match[:prefixLength] + "<#" + string(conversation.ID) + "|" + conversation.Name + ">"
	})
	indices := slashURLReference.FindAllStringIndex(text, -1)
	if len(indices) == 0 {
		return strings.TrimSpace(text), nil
	}
	var escaped strings.Builder
	last := 0
	for _, index := range indices {
		escaped.WriteString(text[last:index[0]])
		link := text[index[0]:index[1]]
		if index[0] > 0 && text[index[0]-1] == '<' {
			escaped.WriteString(link)
		} else {
			escaped.WriteByte('<')
			escaped.WriteString(link)
			escaped.WriteByte('>')
		}
		last = index[1]
	}
	escaped.WriteString(text[last:])
	return strings.TrimSpace(escaped.String()), nil
}

func (m Messages) postSignedAppForm(ctx context.Context, target string, app domain.App, form url.Values) ([]byte, error) {
	body := []byte(form.Encode())
	signingSecret, err := m.OpenAppSigningSecret(app)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	signature, err := events.SlackSignature(signingSecret, now, body)
	if err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, appRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(now.Unix(), 10))
	request.Header.Set("X-Slack-Signature", signature)
	client := m.AppHTTPClient
	if client == nil {
		client = &http.Client{Timeout: appRequestTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAppInteractionUnavailable, err)
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: app returned HTTP %d", ErrAppInteractionUnavailable, response.StatusCode)
	}
	return responseBody, nil
}

type appResponsePayload struct {
	ResponseType    string          `json:"response_type"`
	Text            string          `json:"text"`
	Blocks          json.RawMessage `json:"blocks"`
	Attachments     json.RawMessage `json:"attachments"`
	ReplaceOriginal bool            `json:"replace_original"`
	DeleteOriginal  bool            `json:"delete_original"`
}

func (m Messages) applyAppResponse(ctx context.Context, capability domain.AppResponseURL, body []byte, idempotencyKey string) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	response := appResponsePayload{}
	if body[0] == '{' {
		if err := json.Unmarshal(body, &response); err != nil {
			return ErrInvalidAppResponse
		}
	} else {
		response.Text = string(body)
	}
	blocks, err := domain.NormalizeBlocks(response.Blocks)
	if err != nil {
		return ErrInvalidAppResponse
	}
	attachments, err := domain.NormalizeAttachments(response.Attachments)
	if err != nil {
		return ErrInvalidAppResponse
	}
	if strings.TrimSpace(response.Text) == "" && blocks == "" && attachments == "" && !response.DeleteOriginal {
		return nil
	}
	bot, err := m.Store.GetBotByApp(ctx, capability.WorkspaceID, capability.AppID)
	if err != nil {
		return err
	}
	if response.DeleteOriginal || response.ReplaceOriginal {
		if capability.OriginalMessageID == "" {
			return ErrInvalidAppResponse
		}
		original, err := m.Store.GetMessage(ctx, capability.OriginalMessageID)
		var ephemeralOriginal *domain.EphemeralMessage
		if errors.Is(err, store.ErrNotFound) {
			value, ephemeralErr := m.Store.GetEphemeralMessage(ctx, capability.WorkspaceID, capability.UserID, capability.OriginalMessageID)
			if ephemeralErr != nil {
				if idempotencyKey != "" && response.DeleteOriginal && errors.Is(ephemeralErr, store.ErrNotFound) {
					return nil
				}
				return ErrInvalidAppResponse
			}
			ephemeralOriginal = &value
			original = ephemeralAsMessage(value)
		} else if err != nil {
			return err
		}
		if original.AppID != capability.AppID || original.WorkspaceID != capability.WorkspaceID {
			return ErrInvalidAppResponse
		}
		timestamp := domain.NewMessageTimestamp(original.CreatedAt)
		if ephemeralOriginal != nil {
			event, err := ephemeralMessageMutationEvent(*ephemeralOriginal, response.DeleteOriginal)
			if err != nil {
				return err
			}
			if response.DeleteOriginal {
				err = m.Store.DeleteEphemeralMessage(ctx, capability.WorkspaceID, capability.UserID, capability.OriginalMessageID, event)
				if idempotencyKey != "" && errors.Is(err, store.ErrNotFound) {
					return nil
				}
				return err
			}
			ephemeralOriginal.Text = response.Text
			ephemeralOriginal.Blocks = blocks
			ephemeralOriginal.Attachments = attachments
			event, err = ephemeralMessageMutationEvent(*ephemeralOriginal, false)
			if err != nil {
				return err
			}
			return m.Store.UpdateEphemeralMessage(ctx, *ephemeralOriginal, event)
		}
		if response.DeleteOriginal {
			_, err = m.Delete(ctx, original.WorkspaceID, bot.UserID, original.Conversation, timestamp)
			if idempotencyKey != "" && errors.Is(err, ErrMessageAlreadyDeleted) {
				return nil
			}
			return err
		}
		text := response.Text
		returnValue, err := m.UpdateMessage(ctx, original.WorkspaceID, bot.UserID, original.Conversation, timestamp, domain.MessagePatch{
			Text: &text, Blocks: &blocks, Attachments: &attachments,
		})
		_ = returnValue
		return err
	}
	if response.ResponseType == "in_channel" {
		_, err := m.PostWithBlocksAndAttachments(ctx, capability.WorkspaceID, bot.UserID, capability.ConversationID, response.Text, blocks, attachments, capability.ThreadTimestamp, idempotencyKey, capability.AppID)
		return err
	}
	_, err = m.postEphemeralWithBlocksAndAttachments(ctx, capability.WorkspaceID, bot.UserID, capability.ConversationID, capability.UserID, response.Text, blocks, attachments, capability.AppID, idempotencyKey)
	return err
}

func ephemeralAsMessage(value domain.EphemeralMessage) domain.Message {
	return domain.Message{
		ID: value.ID, WorkspaceID: value.WorkspaceID, Conversation: value.Conversation,
		AuthorID: value.AuthorID, AppID: value.AppID, Text: value.Text, Blocks: value.Blocks,
		Attachments: value.Attachments, CreatedAt: value.CreatedAt,
	}
}

func ephemeralMessageMutationEvent(value domain.EphemeralMessage, deleted bool) (events.Event, error) {
	payload := events.NewPayload(events.EphemeralMessageTopic,
		events.String("workspace_id", string(value.WorkspaceID)),
		events.String("channel_id", string(value.Conversation)),
		events.String("author_id", string(value.AuthorID)),
		events.String("app_id", string(value.AppID)),
		events.String("user_id", string(value.RecipientID)),
		events.String("text", value.Text),
		events.String("blocks", value.Blocks),
		events.String("attachments", value.Attachments),
		events.String("ts", string(value.Timestamp)),
		events.Bool("deleted", deleted),
	)
	return newEvent(value.WorkspaceID, value.AuthorID, payload, time.Now().UTC())
}

func appInteractionMessage(message domain.Message) map[string]any {
	result := map[string]any{
		"type": "message", "user": message.AuthorID, "app_id": message.AppID, "text": message.Text,
		"ts": domain.NewMessageTimestamp(message.CreatedAt),
	}
	if message.ThreadTimestamp != "" {
		result["thread_ts"] = message.ThreadTimestamp
	}
	if message.Blocks != "" {
		result["blocks"] = json.RawMessage(message.Blocks)
	}
	if message.Attachments != "" && message.Attachments != "[]" {
		result["attachments"] = json.RawMessage(message.Attachments)
	}
	return result
}
