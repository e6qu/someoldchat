package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	// Dialog marks a legacy dialog (dialog.open) shown with the modal
	// machinery: it submits to /app/dialog/submit and closes through
	// /app/dialog/close, identified by dialog_id.
	Dialog bool
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
	MinLength      int
	MaxLength      int
	MinValue       string
	MaxValue       string
	Step           string
	DecimalAllowed bool
	DateTimeUnix   string
	// Dispatch is the input block's dispatch_action: the element sends
	// block_actions itself, on the triggers DispatchOn names.
	Dispatch   bool
	DispatchOn string
	// Unsupported explains an element this client cannot collect a value
	// for; it is shown instead of a control, never silently dropped.
	Unsupported string
}

// DispatchTriggers is when an input block with dispatch_action sends
// block_actions: text inputs on their configured triggers, every other
// control as soon as its value changes.
func (input modalInputView) DispatchTriggers() string {
	if input.DispatchOn != "" {
		return input.DispatchOn
	}
	return "change"
}

type modalFormState map[int][]string

// viewInputScript serves the app surfaces (modals and App Home) three ways:
//
//   - it reports the browser's time zone in every data-browser-timezone
//     field, so a datetime-local value is read in the zone it was typed in;
//   - it rewrites each datetimepicker rendered as UTC (data-unix) in the
//     viewer's local time, which is what Slack's client shows;
//   - it sends block_actions for an input block with dispatch_action on the
//     triggers the element declares (data-dispatch-on: enter, character, or
//     change) by posting the form in the background, falling back to the
//     visible Apply button, which also serves a client without script.
//
// window.sameoldchatLocalizeViews lets a script that replaces a region (the
// live Home refresh) localize the new controls. The explanation lives here
// because the script's bytes are hashed for the Content-Security-Policy.
const viewInputScript = `<script>(function(){
var zone='UTC';try{zone=Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC'}catch(error){}
function pad(number){return(number<10?'0':'')+number}
function localize(root){
Array.prototype.forEach.call(root.querySelectorAll('[data-browser-timezone]'),function(input){input.value=zone});
Array.prototype.forEach.call(root.querySelectorAll('input[data-unix]'),function(input){var at=new Date(parseInt(input.getAttribute('data-unix'),10)*1000);if(isNaN(at.getTime()))return;input.value=at.getFullYear()+'-'+pad(at.getMonth()+1)+'-'+pad(at.getDate())+'T'+pad(at.getHours())+':'+pad(at.getMinutes());input.removeAttribute('data-unix')});
}
window.sameoldchatLocalizeViews=localize;
localize(document);
function triggers(container){return ' '+(container.getAttribute('data-dispatch-on')||'')+' '}
function dispatch(container){
var button=container.querySelector('[name="modal_input_action"]');var form=container.closest('form');
if(!button||!form)return;
if(!window.fetch||!window.FormData){button.click();return}
var body=new FormData(form);body.set('modal_input_action',button.value);
fetch(button.getAttribute('formaction'),{method:'POST',body:body,credentials:'same-origin'}).then(function(response){if(!response.ok)throw new Error()}).catch(function(){button.click()});
}
var timers=new WeakMap();
document.addEventListener('keydown',function(event){
if(event.key!=='Enter'||event.isComposing)return;
var container=event.target.closest?event.target.closest('[data-dispatch-input]'):null;
if(!container||triggers(container).indexOf(' enter ')<0||!event.target.matches('input,textarea'))return;
if(event.target.tagName==='TEXTAREA'&&!(event.ctrlKey||event.metaKey))return;
event.preventDefault();dispatch(container);
});
document.addEventListener('input',function(event){
var container=event.target.closest?event.target.closest('[data-dispatch-input]'):null;
if(!container||triggers(container).indexOf(' character ')<0)return;
window.clearTimeout(timers.get(container));timers.set(container,window.setTimeout(function(){dispatch(container)},500));
});
document.addEventListener('change',function(event){
var container=event.target.closest?event.target.closest('[data-dispatch-input]'):null;
if(!container||triggers(container).indexOf(' change ')<0)return;
dispatch(container);
});
})();</script>`

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
				MinQueryLength: action.MinQueryLength, MinLength: action.MinLength, MaxLength: action.MaxLength,
				MinValue: action.MinValue, MaxValue: action.MaxValue, Step: action.Step,
				DecimalAllowed: action.DecimalAllowed, DateTimeUnix: action.DateTimeUnix,
				Dispatch: boolValue(raw["dispatch_action"]), DispatchOn: action.DispatchOn,
			}
			if input.Control == "file" {
				input.Unsupported = fileInputUnsupported
			}
			values, hasSubmitted := submitted[inputIndex]
			if !hasSubmitted && submitted == nil {
				if actionState := persisted.Values[blockID][action.ActionID]; actionState != nil {
					values, hasSubmitted = modalActionValues(action.Type, actionState)
					input.Options = withStateOptions(input.Options, actionState)
				}
			}
			if hasSubmitted {
				input.Values = append([]string(nil), values...)
				input.Value = ""
				input.DateTimeUnix = ""
				if len(values) != 0 {
					input.Value = values[0]
				}
				if input.Control == "datetime" && submitted == nil && input.Value != "" {
					view := messageActionView{}
					view.setDateTime(input.Value)
					input.Value, input.DateTimeUnix = view.Value, view.DateTimeUnix
				}
				if input.Control == "external" {
					input.Options = withChosenOptions(input.Options, values)
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
			applyPersistedActionState(&action, persisted.Values[blockID][action.ActionID])
			renderedBlock.Actions = append(renderedBlock.Actions, modalActionView{
				Index: actionIndex, messageActionView: action,
			})
			actionIndex++
		}
		result.Blocks = append(result.Blocks, renderedBlock)
	}
	return result, nil
}

// applyPersistedActionState shows what the user last chose for an element of
// an actions block or accessory, as recorded in the view's state.
func applyPersistedActionState(action *messageActionView, actionState map[string]any) {
	if actionState == nil {
		return
	}
	values, ok := modalActionValues(action.Type, actionState)
	if !ok {
		return
	}
	action.InitialValues = append([]string(nil), values...)
	if action.Control == "datetime" {
		action.setDateTime(firstValue(values))
	} else {
		action.Value = firstValue(values)
	}
	if action.Control == "external" {
		action.Options = withStateOptions(action.Options, actionState)
	}
	markSelectedOptions(action.Options, values)
}

// withStateOptions adds the options recorded in a view's accepted state to an
// external select, whose options are otherwise only its initial ones: the
// choice the user made must still render as chosen.
func withStateOptions(options []messageActionOptionView, actionState map[string]any) []messageActionOptionView {
	add := func(raw any) {
		option, _ := raw.(map[string]any)
		value := strings.TrimSpace(stringValue(option["value"]))
		if value == "" {
			return
		}
		for _, existing := range options {
			if existing.Value == value {
				return
			}
		}
		text := textObjectValue(option["text"])
		if text == "" {
			text = value
		}
		options = append(options, messageActionOptionView{Text: text, Value: value})
	}
	add(actionState["selected_option"])
	if selected, ok := actionState["selected_options"].([]any); ok {
		for _, raw := range selected {
			add(raw)
		}
	}
	return options
}

// withChosenOptions adds submitted external-select choices (which carry the
// loaded option's text and token) to the options a re-rendered form offers,
// so a form returned with errors keeps them selected.
func withChosenOptions(options []messageActionOptionView, values []string) []messageActionOptionView {
	for _, value := range values {
		choice, ok := decodeExternalChoice(value)
		if !ok {
			continue
		}
		present := false
		for _, existing := range options {
			present = present || existing.Value == value
		}
		if !present {
			options = append(options, messageActionOptionView{Text: choice.Text, Value: value})
		}
	}
	return options
}

// externalChoice is an option a browser loaded for an external select in a
// view or a message:
// its value, its text, and the service's token vouching for that text.
type externalChoice struct {
	Value string `json:"value"`
	Text  string `json:"text"`
	Token string `json:"token"`
}

// externalChoicePrefix marks an <option value> that carries an
// externalChoice rather than a bare option value.
const externalChoicePrefix = "choice:"

func encodeExternalChoice(choice externalChoice) string {
	encoded, _ := json.Marshal(choice)
	return externalChoicePrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeExternalChoice(raw string) (externalChoice, bool) {
	if !strings.HasPrefix(raw, externalChoicePrefix) {
		return externalChoice{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, externalChoicePrefix))
	var choice externalChoice
	if err != nil || json.Unmarshal(decoded, &choice) != nil || strings.TrimSpace(choice.Value) == "" {
		return externalChoice{}, false
	}
	return choice, true
}

// externalChoiceValue is the option value an external-select entry names.
func externalChoiceValue(raw string) string {
	if choice, ok := decodeExternalChoice(raw); ok {
		return choice.Value
	}
	return raw
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
			// A datetimepicker's selected_date_time is Unix seconds; the
			// renderer turns it into a local time in the viewer's browser.
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
	case "rich_text_input":
		raw, exists := state["rich_text_value"]
		if !exists {
			return nil, false
		}
		if raw == nil {
			return nil, true
		}
		return []string{richTextPlain(raw)}, true
	case "file_input":
		return nil, false
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
	location := viewerLocation(values)
	for _, block := range modal.Blocks {
		if block.Input != nil {
			input := block.Input
			field := fmt.Sprintf("input_%d", input.Index)
			selected := append([]string(nil), values[field]...)
			submitted[input.Index] = selected
			setModalStateValue(state, input.BlockID, input.ActionID, modalStateAction(input.Type, input.Options, selected, location))
		}
		for _, action := range block.Actions {
			if action.Control == "button" {
				continue
			}
			selected := append([]string(nil), values[fmt.Sprintf("action_%d", action.Index)]...)
			setModalStateValue(state, action.BlockID, action.ActionID, modalStateAction(action.Type, action.Options, selected, location))
		}
	}
	encoded, err := json.Marshal(map[string]any{"values": state})
	return string(encoded), submitted, err
}

// viewerLocation is the time zone a view form's local date and time fields
// were entered in: the browser reports it in the timezone field. Without
// script the field stays "UTC", which is also the zone the renderer used for
// the values it filled in, so both directions agree.
func viewerLocation(values map[string][]string) *time.Location {
	if name := strings.TrimSpace(firstValue(values["timezone"])); name != "" {
		if location, err := time.LoadLocation(name); err == nil {
			return location
		}
	}
	return time.UTC
}

// parseViewDateTime reads a datetimepicker value: Unix seconds, or a
// datetime-local value in the viewer's zone.
func parseViewDateTime(raw string, location *time.Location) (int64, error) {
	raw = strings.TrimSpace(raw)
	if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return unix, nil
	}
	parsed, err := time.ParseInLocation(dateTimeLocalLayout, raw, location)
	if err != nil {
		return 0, err
	}
	return parsed.Unix(), nil
}

func setModalStateValue(state map[string]map[string]any, blockID, actionID string, action map[string]any) {
	if state[blockID] == nil {
		state[blockID] = make(map[string]any)
	}
	state[blockID][actionID] = action
}

// modalStateAction is one element's entry in view.state.values. An element the
// user left empty reports null for its single value — Slack sends
// "value": null, "selected_option": null, "selected_date": null and so on,
// never an empty string — and an empty list for a multi-value element.
func modalStateAction(actionType string, options []messageActionOptionView, selected []string, location *time.Location) map[string]any {
	action := map[string]any{"type": actionType}
	nonEmpty := make([]string, 0, len(selected))
	for _, value := range selected {
		if strings.TrimSpace(value) != "" {
			nonEmpty = append(nonEmpty, value)
		}
	}
	single := func() any {
		if len(nonEmpty) == 0 {
			return nil
		}
		return nonEmpty[0]
	}
	switch actionType {
	case "static_select", "overflow", "radio_buttons", "external_select":
		action["selected_option"] = selectedOption(options, nonEmpty)
	case "multi_static_select", "multi_external_select", "checkboxes":
		action["selected_options"] = selectedOptions(options, nonEmpty)
	case "users_select":
		action["selected_user"] = single()
	case "multi_users_select":
		action["selected_users"] = nonEmpty
	case "conversations_select":
		action["selected_conversation"] = single()
	case "multi_conversations_select":
		action["selected_conversations"] = nonEmpty
	case "channels_select":
		action["selected_channel"] = single()
	case "multi_channels_select":
		action["selected_channels"] = nonEmpty
	case "datepicker":
		action["selected_date"] = single()
	case "timepicker":
		action["selected_time"] = single()
	case "datetimepicker":
		action["selected_date_time"] = nil
		if len(nonEmpty) != 0 {
			if unix, err := parseViewDateTime(nonEmpty[0], location); err == nil {
				action["selected_date_time"] = unix
			}
		}
	case "rich_text_input":
		action["rich_text_value"] = richTextValue(firstValue(selected))
	case "file_input":
		// This client attaches no files to app forms (see fileInputError).
		action["files"] = []any{}
	default:
		action["value"] = nil
		if value := firstValue(selected); value != "" {
			action["value"] = value
		}
	}
	return action
}

// richTextValue is the rich_text object Slack reports for a rich_text_input:
// the entered text as one rich_text_section. An empty input reports an
// empty rich_text object.
func richTextValue(text string) map[string]any {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	elements := []any{}
	if strings.TrimSpace(text) != "" {
		elements = append(elements, map[string]any{
			"type":     "rich_text_section",
			"elements": []any{map[string]any{"type": "text", "text": text}},
		})
	}
	return map[string]any{"type": "rich_text", "elements": elements}
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
	// An option loaded through block_suggestion carries its text and the
	// service's token; the service keeps the text only if the token holds.
	if choice, ok := decodeExternalChoice(selected[0]); ok {
		return map[string]any{
			"text":  map[string]any{"type": "plain_text", "text": choice.Text, "emoji": true},
			"value": choice.Value, "token": choice.Token,
		}
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
		h.writeAuthError(w, r, err)
		return
	}
	values, ok := h.decodeModalMutation(w, r, "view_id")
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
	// Slack's client refuses a submission that breaks an element's own
	// constraints before the app hears of it; so does this one, whether or
	// not the browser enforced the same attributes.
	if failures := modalInputFailures(rendered, submitted); len(failures) != 0 {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusUnprocessableEntity, ModalSubmitted: submitted, ModalErrors: failures,
		})
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

// modalInputFailures checks each input against the constraints its element
// declares, keyed by block_id like an app's response_action "errors".
func modalInputFailures(modal *modalView, submitted modalFormState) map[string]string {
	failures := make(map[string]string)
	for _, block := range modal.Blocks {
		input := block.Input
		if input == nil {
			continue
		}
		values := submitted[input.Index]
		entered := false
		for _, value := range values {
			entered = entered || strings.TrimSpace(value) != ""
		}
		if input.Unsupported != "" {
			if !input.Optional {
				failures[input.BlockID] = input.Unsupported + " " + unsupportedRequired
			}
			continue
		}
		if !entered {
			if !input.Optional {
				failures[input.BlockID] = "This field is required."
			}
			continue
		}
		value := firstValue(values)
		switch input.Control {
		case "text", "textarea":
			length := utf8.RuneCountInString(value)
			if input.MinLength > 0 && length < input.MinLength {
				failures[input.BlockID] = fmt.Sprintf("Enter at least %d characters.", input.MinLength)
			} else if input.MaxLength > 0 && length > input.MaxLength {
				failures[input.BlockID] = fmt.Sprintf("Enter no more than %d characters.", input.MaxLength)
			}
		case "number":
			number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			switch {
			case err != nil || math.IsNaN(number) || math.IsInf(number, 0):
				failures[input.BlockID] = "Enter a number."
			case !input.DecimalAllowed && number != math.Trunc(number):
				failures[input.BlockID] = "Enter a whole number."
			default:
				if minimum, err := strconv.ParseFloat(input.MinValue, 64); err == nil && number < minimum {
					failures[input.BlockID] = "Enter a number no smaller than " + input.MinValue + "."
				} else if maximum, err := strconv.ParseFloat(input.MaxValue, 64); err == nil && number > maximum {
					failures[input.BlockID] = "Enter a number no larger than " + input.MaxValue + "."
				}
			}
		}
	}
	return failures
}

// fileInputUnsupported explains a file_input: this client cannot attach files
// to an app form, and saying so is better than submitting the form without a
// value the app requires.
const fileInputUnsupported = "This client cannot attach files to app forms yet."

// unsupportedRequired completes the explanation for a required element this
// client cannot fill.
const unsupportedRequired = "The app requires a value here, so this form cannot be submitted from this client."

func (h Handler) viewAction(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	values, ok := h.decodeModalMutation(w, r, "view_id")
	if !ok {
		return
	}
	actionField, inputField := len(values["modal_action"]) == 1, len(values["modal_input_action"]) == 1
	if actionField == inputField {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Reload the modal and try again.")
		return
	}
	if len(values["view_id"]) != 1 || strings.TrimSpace(values["view_id"][0]) == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app modal could not be read", "Reload the workspace and try again.")
		return
	}
	rawIndex := firstValue(values["modal_action"])
	if inputField {
		rawIndex = firstValue(values["modal_input_action"])
	}
	actionIndex, err := strconv.Atoi(strings.TrimSpace(rawIndex))
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
	var action modalActionView
	var selected []string
	exists := false
	if inputField {
		// An input block with dispatch_action sends its own element's
		// block_actions, as Slack does on Enter, per character, or on change.
		var input modalInputView
		input, exists = modalDispatchInputAt(rendered, actionIndex)
		action = modalActionView{Index: -1, messageActionView: messageActionView{
			Type: input.Type, ActionID: input.ActionID, BlockID: input.BlockID,
			Control: input.Control, Multiple: input.Multiple, Options: input.Options,
		}}
		selected = append(selected, values[fmt.Sprintf("input_%d", input.Index)]...)
	} else {
		action, exists = modalActionAt(rendered, actionIndex)
		selected = append(selected, values[fmt.Sprintf("action_%d", action.Index)]...)
	}
	if !exists {
		h.writeMutationError(w, r, http.StatusNotFound, "That app action is no longer available", "The app changed its modal. Reload it and try again.")
		return
	}
	stateJSON, submitted, err := modalStateJSON(rendered, values)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Review the fields and try the action again.")
		return
	}
	value, err := modalActionDispatchValue(action, selected, viewerLocation(values))
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

// modalDispatchInputAt finds the input with this index whose block asks to
// dispatch actions.
func modalDispatchInputAt(modal *modalView, index int) (modalInputView, bool) {
	if modal == nil || index < 0 {
		return modalInputView{}, false
	}
	for _, block := range modal.Blocks {
		if block.Input != nil && block.Input.Index == index && block.Input.Dispatch && block.Input.Unsupported == "" {
			return *block.Input, true
		}
	}
	return modalInputView{}, false
}

// modalActionDispatchValue is the single value domain.AppBlockAction carries
// for an element: a JSON array for a multi-value control, the rich_text object
// for a rich text input, Unix seconds for a datetimepicker, and the bare
// option value for an external select.
func modalActionDispatchValue(action modalActionView, selected []string, location *time.Location) (string, error) {
	if action.Control == "button" {
		return action.Value, nil
	}
	if action.Control == "external" {
		for index := range selected {
			selected[index] = externalChoiceValue(selected[index])
		}
	}
	if action.Multiple || action.Control == "checkbox" {
		encoded, err := json.Marshal(selected)
		return string(encoded), err
	}
	value := firstValue(selected)
	switch {
	case action.Type == "rich_text_input":
		encoded, err := json.Marshal(richTextValue(value))
		return string(encoded), err
	case action.Type != "datetimepicker" || strings.TrimSpace(value) == "":
		return value, nil
	}
	unix, err := parseViewDateTime(value, location)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(unix, 10), nil
}

func (h Handler) viewClose(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
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
		h.writeAuthError(w, r, err)
		return
	}
	h.renderApp(w, r, reader, state)
}

func (h Handler) decodeModalMutation(w http.ResponseWriter, r *http.Request, idField string) (map[string][]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	var err error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		err = r.ParseMultipartForm(maxFormBody)
	} else {
		err = r.ParseForm()
	}
	if err != nil || len(r.Form["_csrf"]) != 1 {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app form could not be read", "Reload the workspace and submit the modal again.")
		return nil, false
	}
	// The CSRF check precedes every other check on the form, so a forged
	// request is refused as forged rather than as incomplete.
	if !h.requireCSRF(w, r) {
		return nil, false
	}
	if len(r.Form[idField]) != 1 || strings.TrimSpace(r.Form[idField][0]) == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app form could not be read", "Reload the workspace and submit the form again.")
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
