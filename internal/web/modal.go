package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type modalView struct {
	ID              string
	AppID           string
	Title           string
	Close           string
	Submit          string
	CallbackID      string
	PrivateMetadata string
	ClearOnClose    bool
	SubmitDisabled  bool
	Error           string
	Blocks          []modalBlockView
}

type modalBlockView struct {
	ID        string
	Kind      string
	Text      string
	HTML      template.HTML
	Fields    []string
	FieldHTML []template.HTML
	ImageURL  string
	ImageAlt  string
	Table     [][]string
	Caption   string
	HeaderRow bool
	Input     *modalInputView
	Actions   []modalActionView
	Error     string
}

type modalActionView struct {
	Index int
	messageActionView
}

type modalInputView struct {
	Index          int
	BlockID        string
	ActionID       string
	Type           string
	Label          string
	Hint           string
	Placeholder    string
	Control        string
	Value          string
	Values         []string
	Options        []messageActionOptionView
	Multiple       bool
	Optional       bool
	MinQueryLength int
}

type modalFormState map[int][]string

func (h Handler) newModalView(ctx context.Context, principal auth.Principal, value domain.View, failures map[string]string, submitted modalFormState) (*modalView, error) {
	var envelope struct {
		Type            string           `json:"type"`
		Title           map[string]any   `json:"title"`
		Close           map[string]any   `json:"close"`
		Submit          map[string]any   `json:"submit"`
		CallbackID      string           `json:"callback_id"`
		PrivateMetadata string           `json:"private_metadata"`
		ClearOnClose    bool             `json:"clear_on_close"`
		SubmitDisabled  bool             `json:"submit_disabled"`
		Blocks          []map[string]any `json:"blocks"`
	}
	if json.Unmarshal([]byte(value.Payload), &envelope) != nil || envelope.Type != "modal" {
		return nil, errors.New("stored modal view is invalid")
	}
	result := &modalView{
		ID: string(value.ID), AppID: string(value.AppID), Title: textObjectValue(envelope.Title),
		Close: textObjectValue(envelope.Close), Submit: textObjectValue(envelope.Submit),
		CallbackID: envelope.CallbackID, PrivateMetadata: envelope.PrivateMetadata,
		ClearOnClose: envelope.ClearOnClose, SubmitDisabled: envelope.SubmitDisabled,
		Error: strings.TrimSpace(failures[""]),
	}
	var persisted struct {
		Values map[string]map[string]map[string]any `json:"values"`
	}
	if strings.TrimSpace(value.State) != "" {
		_ = json.Unmarshal([]byte(value.State), &persisted)
	}
	if result.Title == "" {
		result.Title = "App"
	}
	if result.Close == "" {
		result.Close = "Cancel"
	}
	catalog := appActionOptionCatalog{}
	inputIndex := 0
	actionIndex := 0
	for _, raw := range envelope.Blocks {
		blockID := strings.TrimSpace(stringValue(raw["block_id"]))
		if strings.TrimSpace(stringValue(raw["type"])) == "input" {
			element, ok := raw["element"].(map[string]any)
			if !ok {
				continue
			}
			actions := actionElementList([]any{element}, blockID)
			if len(actions) != 1 {
				continue
			}
			holder := []messageBlockView{{Actions: actions}}
			catalog.enrich(ctx, h, principal, holder)
			action := holder[0].Actions[0]
			input := modalInputView{
				Index: inputIndex, BlockID: blockID, ActionID: action.ActionID, Type: action.Type,
				Label: textObjectValue(raw["label"]), Hint: textObjectValue(raw["hint"]),
				Placeholder: action.Text, Control: action.Control, Value: action.Value,
				Values: append([]string(nil), action.InitialValues...), Options: action.Options,
				Multiple: action.Multiple, Optional: boolValue(raw["optional"]),
				MinQueryLength: action.MinQueryLength,
			}
			values, hasSubmitted := submitted[inputIndex]
			if !hasSubmitted && submitted == nil {
				if actionState := persisted.Values[blockID][action.ActionID]; actionState != nil {
					values, hasSubmitted = modalActionValues(action.Type, actionState)
				}
			}
			if hasSubmitted {
				input.Values = append([]string(nil), values...)
				input.Value = ""
				if len(values) != 0 {
					input.Value = values[0]
				}
				for optionIndex := range input.Options {
					input.Options[optionIndex].Selected = containsValue(values, input.Options[optionIndex].Value)
				}
			}
			result.Blocks = append(result.Blocks, modalBlockView{
				ID: blockID, Kind: "input", Input: &input, Error: strings.TrimSpace(failures[blockID]),
			})
			inputIndex++
			continue
		}
		block, ok := newMessageBlockView(raw)
		if !ok {
			continue
		}
		holder := []messageBlockView{block}
		catalog.enrich(ctx, h, principal, holder)
		block = holder[0]
		renderedBlock := modalBlockView{
			ID: blockID, Kind: block.Kind, Text: block.Text, HTML: block.HTML,
			Fields: block.Fields, FieldHTML: block.FieldHTML,
			ImageURL: block.ImageURL, ImageAlt: block.ImageAlt, Table: block.Table,
			Caption: block.Caption, HeaderRow: block.HeaderRow,
		}
		for _, action := range block.Actions {
			if actionState := persisted.Values[blockID][action.ActionID]; actionState != nil {
				if values, ok := modalActionValues(action.Type, actionState); ok {
					action.InitialValues = append([]string(nil), values...)
					action.Value = firstValue(values)
					markSelectedOptions(action.Options, values)
				}
			}
			renderedBlock.Actions = append(renderedBlock.Actions, modalActionView{
				Index: actionIndex, messageActionView: action,
			})
			actionIndex++
		}
		result.Blocks = append(result.Blocks, renderedBlock)
	}
	return result, nil
}

func modalActionValues(actionType string, state map[string]any) ([]string, bool) {
	stringField := func(name string) ([]string, bool) {
		value, exists := state[name]
		if !exists {
			return nil, false
		}
		if value == nil {
			return nil, true
		}
		if text, ok := value.(string); ok {
			return []string{text}, true
		}
		if number, ok := value.(float64); ok {
			if actionType == "datetimepicker" {
				return []string{time.Unix(int64(number), 0).In(time.Local).Format("2006-01-02T15:04")}, true
			}
			return []string{strconv.FormatInt(int64(number), 10)}, true
		}
		return nil, true
	}
	listField := func(name string) ([]string, bool) {
		raw, exists := state[name]
		if !exists {
			return nil, false
		}
		items, _ := raw.([]any)
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result, true
	}
	optionField := func(name string, multiple bool) ([]string, bool) {
		raw, exists := state[name]
		if !exists {
			return nil, false
		}
		items := []any{raw}
		if multiple {
			items, _ = raw.([]any)
		}
		result := make([]string, 0, len(items))
		for _, item := range items {
			if option, ok := item.(map[string]any); ok {
				if value := strings.TrimSpace(stringValue(option["value"])); value != "" {
					result = append(result, value)
				}
			}
		}
		return result, true
	}
	switch actionType {
	case "static_select", "overflow", "radio_buttons", "external_select":
		return optionField("selected_option", false)
	case "multi_static_select", "multi_external_select", "checkboxes":
		return optionField("selected_options", true)
	case "users_select":
		return stringField("selected_user")
	case "multi_users_select":
		return listField("selected_users")
	case "conversations_select":
		return stringField("selected_conversation")
	case "multi_conversations_select":
		return listField("selected_conversations")
	case "channels_select":
		return stringField("selected_channel")
	case "multi_channels_select":
		return listField("selected_channels")
	case "datepicker":
		return stringField("selected_date")
	case "timepicker":
		return stringField("selected_time")
	case "datetimepicker":
		return stringField("selected_date_time")
	default:
		return stringField("value")
	}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func containsValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func modalStateJSON(modal *modalView, values map[string][]string) (string, modalFormState, error) {
	state := make(map[string]map[string]any)
	submitted := make(modalFormState)
	if modal == nil {
		return "", submitted, errors.New("modal is required")
	}
	for _, block := range modal.Blocks {
		if block.Input != nil {
			input := block.Input
			field := fmt.Sprintf("input_%d", input.Index)
			selected := append([]string(nil), values[field]...)
			submitted[input.Index] = selected
			setModalStateValue(state, input.BlockID, input.ActionID, modalStateAction(input.Type, input.Options, selected))
		}
		for _, action := range block.Actions {
			if action.Control == "button" {
				continue
			}
			selected := append([]string(nil), values[fmt.Sprintf("action_%d", action.Index)]...)
			setModalStateValue(state, action.BlockID, action.ActionID, modalStateAction(action.Type, action.Options, selected))
		}
	}
	encoded, err := json.Marshal(map[string]any{"values": state})
	return string(encoded), submitted, err
}

func setModalStateValue(state map[string]map[string]any, blockID, actionID string, action map[string]any) {
	if state[blockID] == nil {
		state[blockID] = make(map[string]any)
	}
	state[blockID][actionID] = action
}

func modalStateAction(actionType string, options []messageActionOptionView, selected []string) map[string]any {
	action := map[string]any{"type": actionType}
	switch actionType {
	case "static_select", "overflow", "radio_buttons", "external_select":
		action["selected_option"] = selectedOption(options, selected)
	case "multi_static_select", "multi_external_select", "checkboxes":
		action["selected_options"] = selectedOptions(options, selected)
	case "users_select":
		action["selected_user"] = firstValue(selected)
	case "multi_users_select":
		action["selected_users"] = selected
	case "conversations_select":
		action["selected_conversation"] = firstValue(selected)
	case "multi_conversations_select":
		action["selected_conversations"] = selected
	case "channels_select":
		action["selected_channel"] = firstValue(selected)
	case "multi_channels_select":
		action["selected_channels"] = selected
	case "datepicker":
		action["selected_date"] = firstValue(selected)
	case "timepicker":
		action["selected_time"] = firstValue(selected)
	case "datetimepicker":
		raw := firstValue(selected)
		if unix, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
			action["selected_date_time"] = unix
		} else if parsed, err := time.ParseInLocation("2006-01-02T15:04", raw, time.Local); err == nil {
			action["selected_date_time"] = parsed.Unix()
		} else {
			action["selected_date_time"] = raw
		}
	default:
		action["value"] = firstValue(selected)
	}
	return action
}

func modalActionAt(modal *modalView, index int) (modalActionView, bool) {
	if modal == nil || index < 0 {
		return modalActionView{}, false
	}
	for _, block := range modal.Blocks {
		for _, action := range block.Actions {
			if action.Index == index {
				return action, true
			}
		}
	}
	return modalActionView{}, false
}

func selectedOption(options []messageActionOptionView, selected []string) any {
	if len(selected) == 0 {
		return nil
	}
	for _, option := range options {
		if option.Value == selected[0] {
			return map[string]any{"text": map[string]any{"type": "plain_text", "text": option.Text, "emoji": true}, "value": option.Value}
		}
	}
	return map[string]any{"value": selected[0]}
}

func selectedOptions(options []messageActionOptionView, selected []string) []any {
	result := make([]any, 0, len(selected))
	for _, value := range selected {
		result = append(result, selectedOption(options, []string{value}))
	}
	return result
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (h Handler) viewSubmit(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	values, ok := h.decodeModalMutation(w, r)
	if !ok {
		return
	}
	if len(values["view_id"]) != 1 || strings.TrimSpace(values["view_id"][0]) == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app modal could not be read", "Reload the workspace and try again.")
		return
	}
	viewID := domain.ViewID(strings.TrimSpace(values["view_id"][0]))
	current, err := h.Messages.CurrentModalView(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil || current.ID != viewID {
		h.writeMutationError(w, r, http.StatusNotFound, "That app modal is no longer open", "Reload the workspace to see the app's current view.")
		return
	}
	rendered, err := h.newModalView(r.Context(), principal, current, nil, nil)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadGateway, "That app modal is invalid", "The app supplied a view that SameOldChat could not render safely.")
		return
	}
	stateJSON, submitted, err := modalStateJSON(rendered, values)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app form could not be read", "Review the fields and submit the modal again.")
		return
	}
	result, err := h.Messages.SubmitView(
		r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), viewID,
		stateJSON, h.responseBaseURL(r),
	)
	if err != nil {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusBadGateway, ModalSubmitted: submitted,
			ModalErrors: map[string]string{"": modalInteractionError(err)},
		})
		return
	}
	if len(result.Errors) != 0 {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusUnprocessableEntity, ModalSubmitted: submitted, ModalErrors: result.Errors,
		})
		return
	}
	if result.Pending {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusAccepted, ModalSubmitted: submitted,
			Notice: "The app is checking that modal. Its response will update this page.",
		})
		return
	}
	http.Redirect(w, r, appURL(string(h.requestChannel(r)), "", "", "", ""), http.StatusSeeOther)
}

func (h Handler) viewAction(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	values, ok := h.decodeModalMutation(w, r)
	if !ok {
		return
	}
	if len(values["modal_action"]) != 1 {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Reload the modal and try again.")
		return
	}
	if len(values["view_id"]) != 1 || strings.TrimSpace(values["view_id"][0]) == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app modal could not be read", "Reload the workspace and try again.")
		return
	}
	actionIndex, err := strconv.Atoi(strings.TrimSpace(values["modal_action"][0]))
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Reload the modal and try again.")
		return
	}
	viewID := domain.ViewID(strings.TrimSpace(values["view_id"][0]))
	current, err := h.Messages.CurrentModalView(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil || current.ID != viewID {
		h.writeMutationError(w, r, http.StatusNotFound, "That app modal is no longer open", "Reload the workspace to see the app's current view.")
		return
	}
	rendered, err := h.newModalView(r.Context(), principal, current, nil, nil)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadGateway, "That app modal is invalid", "The app supplied a view that SameOldChat could not render safely.")
		return
	}
	action, exists := modalActionAt(rendered, actionIndex)
	if !exists {
		h.writeMutationError(w, r, http.StatusNotFound, "That app action is no longer available", "The app changed its modal. Reload it and try again.")
		return
	}
	stateJSON, submitted, err := modalStateJSON(rendered, values)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Review the fields and try the action again.")
		return
	}
	selected := append([]string(nil), values[fmt.Sprintf("action_%d", action.Index)]...)
	value, err := modalActionDispatchValue(action, selected)
	if err != nil {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusBadRequest, ModalSubmitted: submitted,
			ModalErrors: map[string]string{"": "Choose a valid value for that app action."},
		})
		return
	}
	err = h.Messages.DispatchViewBlockAction(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), domain.AppViewBlockAction{
		ViewID: viewID, BlockID: action.BlockID, ActionID: action.ActionID,
		Type: action.Type, Value: value, State: stateJSON,
	}, h.responseBaseURL(r))
	if err != nil {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusBadGateway, ModalSubmitted: submitted,
			ModalErrors: map[string]string{"": modalInteractionError(err)},
		})
		return
	}
	h.renderModalResult(w, r, principal, composerState{
		Status: http.StatusOK, ModalSubmitted: submitted, Notice: "The app action ran.",
	})
}

func modalActionDispatchValue(action modalActionView, selected []string) (string, error) {
	if action.Control == "button" {
		return action.Value, nil
	}
	if action.Multiple || action.Control == "checkbox" {
		encoded, err := json.Marshal(selected)
		return string(encoded), err
	}
	value := firstValue(selected)
	if action.Type != "datetimepicker" || strings.TrimSpace(value) == "" {
		return value, nil
	}
	if _, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		return value, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(value), time.Local)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(parsed.Unix(), 10), nil
}

func (h Handler) viewClose(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "That app modal could not be closed. Reload the workspace and try again.")
	if !ok {
		return
	}
	viewID := domain.ViewID(strings.TrimSpace(fields["view_id"]))
	clear := strings.EqualFold(strings.TrimSpace(fields["clear"]), "true")
	err = h.Messages.CloseView(
		r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), viewID, clear, h.responseBaseURL(r),
	)
	if err != nil {
		current, currentErr := h.Messages.CurrentModalView(r.Context(), principal.WorkspaceID, principal.UserID)
		if errors.Is(currentErr, store.ErrNotFound) || (currentErr == nil && current.ID != viewID) {
			h.renderModalResult(w, r, principal, composerState{
				Status: http.StatusOK, Notice: "The modal closed, but its app could not be notified.",
			})
			return
		}
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusBadGateway, ModalErrors: map[string]string{"": modalInteractionError(err)},
		})
		return
	}
	http.Redirect(w, r, appURL(string(h.requestChannel(r)), "", "", "", ""), http.StatusSeeOther)
}

func (h Handler) renderModalResult(w http.ResponseWriter, r *http.Request, principal auth.Principal, state composerState) {
	reader, err := requireHistoryReader(principal)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	h.renderApp(w, r, reader, state)
}

func (h Handler) decodeModalMutation(w http.ResponseWriter, r *http.Request) (map[string][]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	var err error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		err = r.ParseMultipartForm(maxFormBody)
	} else {
		err = r.ParseForm()
	}
	if err != nil || len(r.Form["view_id"]) != 1 || len(r.Form["_csrf"]) != 1 {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app form could not be read", "Reload the workspace and submit the modal again.")
		return nil, false
	}
	if !h.requireCSRF(w, r) {
		return nil, false
	}
	return r.Form, true
}

func modalInteractionError(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "This modal or its app is no longer available."
	case errors.Is(err, service.ErrAppInteractionUnavailable):
		return "The app did not respond in time. Your entries are still here; try again."
	case errors.Is(err, service.ErrInvalidAppResponse):
		return "The app returned an invalid modal response. Your entries are still here."
	default:
		return "The app modal could not be submitted. Your entries are still here; try again."
	}
}
