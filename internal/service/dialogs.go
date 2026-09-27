package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// DialogDefinition is a legacy dialog as dialog.open accepts it. The
// first-party client renders it with the modal machinery; the service
// validates it once when it is opened and again reads it at submission.
type DialogDefinition struct {
	CallbackID     string          `json:"callback_id"`
	Title          string          `json:"title"`
	SubmitLabel    string          `json:"submit_label"`
	NotifyOnCancel bool            `json:"notify_on_cancel"`
	State          string          `json:"state"`
	Elements       []DialogElement `json:"elements"`
}

// DialogElement is one text, textarea, or select element of a dialog.
type DialogElement struct {
	Type            string         `json:"type"`
	Label           string         `json:"label"`
	Name            string         `json:"name"`
	Placeholder     string         `json:"placeholder"`
	Hint            string         `json:"hint"`
	Subtype         string         `json:"subtype"`
	Value           string         `json:"value"`
	Optional        bool           `json:"optional"`
	MinLength       *int           `json:"min_length"`
	MaxLength       *int           `json:"max_length"`
	DataSource      string         `json:"data_source"`
	MinQueryLength  *int           `json:"min_query_length"`
	Options         []DialogOption `json:"options"`
	OptionGroups    []DialogGroup  `json:"option_groups"`
	SelectedOptions []DialogOption `json:"selected_options"`
}

type DialogOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type DialogGroup struct {
	Label   string         `json:"label"`
	Options []DialogOption `json:"options"`
}

// Limits returns an element's effective minimum and maximum length: text
// defaults to 0..150, textarea to 0..3000.
func (element DialogElement) Limits() (int, int) {
	minimum, maximum := 0, 150
	if element.Type == "textarea" {
		maximum = 3000
	}
	if element.MinLength != nil {
		minimum = *element.MinLength
	}
	if element.MaxLength != nil {
		maximum = *element.MaxLength
	}
	return minimum, maximum
}

// ParseDialog decodes and validates a dialog against Slack's documented
// dialog.open limits.
func ParseDialog(payload string) (DialogDefinition, error) {
	var dialog DialogDefinition
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(payload)))
	if strings.TrimSpace(payload) == "" || decoder.Decode(&dialog) != nil {
		return DialogDefinition{}, ErrInvalidDialog
	}
	runes := utf8.RuneCountInString
	if title := strings.TrimSpace(dialog.Title); title == "" || runes(title) > 24 ||
		strings.TrimSpace(dialog.CallbackID) == "" || runes(dialog.CallbackID) > 255 ||
		runes(dialog.SubmitLabel) > 48 || strings.ContainsAny(strings.TrimSpace(dialog.SubmitLabel), " \t\n") ||
		runes(dialog.State) > 3000 || len(dialog.Elements) == 0 || len(dialog.Elements) > 10 {
		return DialogDefinition{}, ErrInvalidDialog
	}
	names := make(map[string]bool, len(dialog.Elements))
	for _, element := range dialog.Elements {
		name := strings.TrimSpace(element.Name)
		if name == "" || runes(name) > 300 || names[name] || strings.TrimSpace(element.Label) == "" || runes(element.Label) > 48 ||
			runes(element.Placeholder) > 150 || runes(element.Hint) > 150 {
			return DialogDefinition{}, ErrInvalidDialog
		}
		names[name] = true
		switch element.Type {
		case "text", "textarea":
			limit := 150
			if element.Type == "textarea" {
				limit = 3000
			}
			minimum, maximum := element.Limits()
			if minimum < 0 || maximum < 1 || maximum > limit || minimum > maximum || runes(element.Value) > limit ||
				(element.Subtype != "" && element.Subtype != "email" && element.Subtype != "number" && element.Subtype != "tel" && element.Subtype != "url") {
				return DialogDefinition{}, ErrInvalidDialog
			}
		case "select":
			switch element.DataSource {
			case "", "static":
				count := len(element.Options)
				for _, group := range element.OptionGroups {
					count += len(group.Options)
				}
				if count == 0 || len(element.Options) > 100 || len(element.OptionGroups) > 100 || (len(element.Options) != 0 && len(element.OptionGroups) != 0) {
					return DialogDefinition{}, ErrInvalidDialog
				}
			case "users", "channels", "conversations", "external":
			default:
				return DialogDefinition{}, ErrInvalidDialog
			}
		default:
			return DialogDefinition{}, ErrInvalidDialog
		}
	}
	return dialog, nil
}

// CurrentDialog is the dialog the member has open, if any.
func (m Messages) CurrentDialog(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.Dialog, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Dialog{}, err
	}
	return m.Store.GetCurrentDialog(ctx, workspaceID, userID)
}

func (m Messages) ownedDialog(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, dialogID domain.DialogID) (domain.Dialog, DialogDefinition, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Dialog{}, DialogDefinition{}, err
	}
	current, err := m.Store.GetDialog(ctx, workspaceID, dialogID)
	if err != nil || current.UserID != userID {
		return domain.Dialog{}, DialogDefinition{}, store.ErrNotFound
	}
	definition, err := ParseDialog(current.Payload)
	if err != nil {
		return domain.Dialog{}, DialogDefinition{}, ErrInvalidAppResponse
	}
	return current, definition, nil
}

// SubmitDialog delivers dialog_submission for the values the member entered,
// keyed by element name. An element the member left empty is reported as
// null. Values that break an element's own limits are refused before the app
// is asked, as Slack's client does. The dialog closes when the app
// acknowledges without errors; errors keep it open (Result.Errors, keyed by
// element name). Over Socket Mode the result is Pending and the
// acknowledgement is applied by HandleSocketModeResponse.
func (m Messages) SubmitDialog(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, dialogID domain.DialogID, values map[string]string, responseBaseURL string) (domain.ViewInteractionResult, error) {
	current, definition, err := m.ownedDialog(ctx, workspaceID, userID, dialogID)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	submission := make(map[string]any, len(definition.Elements))
	failures := make(map[string]string)
	for _, element := range definition.Elements {
		value := values[element.Name]
		if element.Type != "textarea" {
			value = strings.TrimSpace(value)
		}
		if value == "" {
			submission[element.Name] = nil
			if !element.Optional {
				failures[element.Name] = "This field is required."
			}
			continue
		}
		submission[element.Name] = value
		if element.Type == "text" || element.Type == "textarea" {
			minimum, maximum := element.Limits()
			if length := utf8.RuneCountInString(value); length < minimum || length > maximum {
				failures[element.Name] = "Enter between " + strconv.Itoa(minimum) + " and " + strconv.Itoa(maximum) + " characters."
			}
		} else if element.DataSource == "" || element.DataSource == "static" {
			if !dialogOptionExists(element, value) {
				failures[element.Name] = "Choose one of the listed options."
			}
		}
	}
	if len(failures) != 0 {
		return domain.ViewInteractionResult{Errors: failures}, nil
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, current.AppID)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	if !parsed.InteractivityEnabled || (!parsed.SocketModeEnabled && parsed.InteractivityRequestURL == "") {
		return domain.ViewInteractionResult{}, ErrAppInteractionUnavailable
	}
	payload, capability, err := m.dialogPayload(ctx, current, definition, "dialog_submission", workspaceID, userID, conversationID, responseBaseURL)
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	payload["submission"] = submission
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
	return m.applyDialogResponse(ctx, current, body)
}

// applyDialogResponse applies an app's answer to dialog_submission: an empty
// body closes the dialog, {"errors":[{"name","error"}]} keeps it open with
// those messages.
func (m Messages) applyDialogResponse(ctx context.Context, current domain.Dialog, body []byte) (domain.ViewInteractionResult, error) {
	body = bytes.TrimSpace(body)
	var response struct {
		Errors []struct {
			Name  string `json:"name"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if len(body) != 0 && json.Unmarshal(body, &response) != nil {
		return domain.ViewInteractionResult{}, ErrInvalidAppResponse
	}
	if len(response.Errors) == 0 {
		err := m.closeDialog(ctx, current)
		if errors.Is(err, store.ErrNotFound) {
			err = nil
		}
		return domain.ViewInteractionResult{}, err
	}
	failures := make(map[string]string, len(response.Errors))
	for _, failure := range response.Errors {
		if strings.TrimSpace(failure.Name) == "" || strings.TrimSpace(failure.Error) == "" {
			return domain.ViewInteractionResult{}, ErrInvalidAppResponse
		}
		failures[failure.Name] = failure.Error
	}
	current.Errors = failures
	event, err := newEvent(current.WorkspaceID, current.UserID, dialogEventPayload("dialog.updated", current), time.Now().UTC())
	if err != nil {
		return domain.ViewInteractionResult{}, err
	}
	if err := m.Store.SetDialogErrors(ctx, current, event); err != nil {
		return domain.ViewInteractionResult{}, err
	}
	return domain.ViewInteractionResult{Errors: failures}, nil
}

// CancelDialog closes a dialog the member owns. Like closing a modal it
// succeeds whatever state the app is in; dialog_cancellation is delivered
// afterwards only when the dialog asked for it (notify_on_cancel) and the app
// can still receive it.
func (m Messages) CancelDialog(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, dialogID domain.DialogID, responseBaseURL string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	current, err := m.Store.GetDialog(ctx, workspaceID, dialogID)
	if err != nil || current.UserID != userID {
		return store.ErrNotFound
	}
	if err := m.closeDialog(ctx, current); err != nil {
		return err
	}
	definition, err := ParseDialog(current.Payload)
	if err != nil || !definition.NotifyOnCancel {
		return nil
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, current.AppID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !parsed.InteractivityEnabled || (!parsed.SocketModeEnabled && parsed.InteractivityRequestURL == "") {
		return nil
	}
	payload, capability, err := m.dialogPayload(ctx, current, definition, "dialog_cancellation", workspaceID, userID, conversationID, responseBaseURL)
	if err != nil {
		return err
	}
	if parsed.SocketModeEnabled {
		return m.enqueueSocketModeInteraction(ctx, current.AppID, workspaceID, userID, "interactive", payload, capability)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = m.postSignedAppForm(ctx, parsed.InteractivityRequestURL, snapshot.App, url.Values{"payload": {string(encoded)}})
	return err
}

func (m Messages) closeDialog(ctx context.Context, current domain.Dialog) error {
	event, err := newEvent(current.WorkspaceID, current.UserID, dialogEventPayload("dialog.closed", current), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteDialog(ctx, current.WorkspaceID, current.UserID, current.ID, event)
}

func dialogEventPayload(topic string, current domain.Dialog) events.Payload {
	return events.NewPayload(topic,
		events.String("dialog_id", string(current.ID)), events.String("app_id", string(current.AppID)),
		events.String("user_id", string(current.UserID)),
	)
}

// dialogPayload is the envelope Slack sends for dialog_submission and
// dialog_cancellation: identity, channel, callback_id, state, and a
// response_url the app may post to.
func (m Messages) dialogPayload(ctx context.Context, current domain.Dialog, definition DialogDefinition, kind string, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, responseBaseURL string) (map[string]any, domain.AppResponseURL, error) {
	snapshot, _, err := m.installedApp(ctx, workspaceID, current.AppID)
	if err != nil {
		return nil, domain.AppResponseURL{}, err
	}
	payload, conversationID, err := m.dialogEnvelope(ctx, current, definition, kind, workspaceID, userID, conversationID)
	if err != nil {
		return nil, domain.AppResponseURL{}, err
	}
	_, responseURL, capability, err := m.createInteractionCapabilities(ctx, current.AppID, workspaceID, userID, conversationID, "", "", responseBaseURL)
	if err != nil {
		return nil, domain.AppResponseURL{}, err
	}
	verificationToken, err := m.openAppVerificationToken(snapshot.App)
	if err != nil {
		return nil, domain.AppResponseURL{}, err
	}
	payload["token"] = verificationToken
	payload["response_url"] = responseURL
	return payload, capability, nil
}

// dialogEnvelope is what every dialog interaction carries: identity, the
// channel when the member is in it, callback_id and state. It returns the
// conversation the interaction is scoped to, which is empty when the member
// is not in the one the request named.
func (m Messages) dialogEnvelope(ctx context.Context, current domain.Dialog, definition DialogDefinition, kind string, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID) (map[string]any, domain.ConversationID, error) {
	workspace, err := m.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	user, err := m.Store.GetUser(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	channel := map[string]any{"id": conversationID}
	if m.requireConversationMembership(ctx, workspaceID, userID, conversationID) != nil {
		conversationID, channel = "", nil
	} else if conversation, err := m.Store.GetConversation(ctx, conversationID); err == nil {
		channel["name"] = conversation.Name
	}
	payload := map[string]any{
		"type": kind, "api_app_id": current.AppID,
		"action_ts":   domain.NewMessageTimestamp(time.Now().UTC()),
		"team":        map[string]any{"id": workspace.ID, "domain": workspace.SlackDomain()},
		"user":        map[string]any{"id": user.ID, "name": user.Name},
		"callback_id": definition.CallbackID,
		"state":       definition.State,
	}
	if channel != nil {
		payload["channel"] = channel
	}
	return payload, conversationID, nil
}

// dialogSuggestionPayload is Slack's dialog_suggestion for a select with
// data_source "external" in the member's open dialog: the envelope every
// dialog interaction carries plus the element's name and what the member
// typed. It carries no response_url, as Slack's does not. The element's name
// is both the query's block and action, as the client renders it.
func (m Messages) dialogSuggestionPayload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, query domain.AppOptionQuery) (map[string]any, error) {
	current, definition, err := m.ownedDialog(ctx, workspaceID, userID, query.DialogID)
	if err != nil {
		return nil, err
	}
	element, found := definition.element(query.BlockID)
	if current.AppID != query.AppID || query.ActionID != query.BlockID || !found || element.Type != "select" || element.DataSource != "external" {
		return nil, store.ErrNotFound
	}
	payload, _, err := m.dialogEnvelope(ctx, current, definition, "dialog_suggestion", workspaceID, userID, conversationID)
	if err != nil {
		return nil, err
	}
	payload["name"] = element.Name
	payload["value"] = query.Value
	return payload, nil
}

func (definition DialogDefinition) element(name string) (DialogElement, bool) {
	for _, element := range definition.Elements {
		if element.Name == name {
			return element, true
		}
	}
	return DialogElement{}, false
}

// parseDialogOptions reads an app's answer to dialog_suggestion: Slack's
// {"options":[{"label","value"}]} or {"option_groups":[{"label","options"}]},
// with the dialog limits of 100 options and 75-character labels and values.
func parseDialogOptions(body []byte) ([]domain.AppOption, error) {
	type option struct {
		Label string `json:"label"`
		Value string `json:"value"`
	}
	var response struct {
		Options      []option `json:"options"`
		OptionGroups []struct {
			Label   string   `json:"label"`
			Options []option `json:"options"`
		} `json:"option_groups"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &response) != nil ||
		(response.Options == nil && response.OptionGroups == nil) ||
		(len(response.Options) != 0 && len(response.OptionGroups) != 0) ||
		len(response.Options) > 100 || len(response.OptionGroups) > 100 {
		return nil, ErrInvalidAppResponse
	}
	result := make([]domain.AppOption, 0, len(response.Options))
	add := func(value option, group string) error {
		label, id := strings.TrimSpace(value.Label), strings.TrimSpace(value.Value)
		if label == "" || id == "" || utf8.RuneCountInString(label) > 75 || utf8.RuneCountInString(id) > 75 {
			return ErrInvalidAppResponse
		}
		result = append(result, domain.AppOption{Text: label, Value: id, Group: group})
		return nil
	}
	for _, value := range response.Options {
		if err := add(value, ""); err != nil {
			return nil, err
		}
	}
	for _, group := range response.OptionGroups {
		label := strings.TrimSpace(group.Label)
		if label == "" || utf8.RuneCountInString(label) > 75 || len(group.Options) > 100 {
			return nil, ErrInvalidAppResponse
		}
		for _, value := range group.Options {
			if err := add(value, label); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func dialogOptionExists(element DialogElement, value string) bool {
	for _, option := range element.Options {
		if option.Value == value {
			return true
		}
	}
	for _, group := range element.OptionGroups {
		for _, option := range group.Options {
			if option.Value == value {
				return true
			}
		}
	}
	return false
}
