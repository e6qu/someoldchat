package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// viewStateSavedPayload is the view.updated record for a change to what the
// user entered and nothing else. state_only lets the browser that saved it
// keep the modal it is showing instead of reloading and losing focus; a
// change the app makes (views.update, response_action) carries no such flag.
func viewStateSavedPayload(value domain.View) events.Payload {
	return events.NewPayload("view.updated",
		events.String("view_id", string(value.ID)), events.String("app_id", string(value.AppID)),
		events.String("user_id", string(value.UserID)), events.Bool("state_only", true),
	)
}

// withAcceptedOptionText gives a dispatched external-select action the text
// sanitizeViewState accepted for it, as Slack's block_actions payload does.
func withAcceptedOptionText(actionPayload map[string]any, stateJSON, blockID, actionID string) {
	var state struct {
		Values map[string]map[string]map[string]any `json:"values"`
	}
	if json.Unmarshal([]byte(stateJSON), &state) != nil {
		return
	}
	texts := optionTexts(state.Values[blockID][actionID])
	attach := func(raw any) {
		option, _ := raw.(map[string]any)
		if option == nil {
			return
		}
		if text := texts[stringValue(option["value"])]; text != "" {
			option["text"] = map[string]any{"type": "plain_text", "text": text, "emoji": true}
		}
	}
	attach(actionPayload["selected_option"])
	if selected, ok := actionPayload["selected_options"].([]map[string]any); ok {
		for _, option := range selected {
			attach(option)
		}
	}
}

// viewOptionSignatureField is the key a client uses to hand back the token of
// an option it loaded through block_suggestion. It never reaches an app: the
// service verifies and removes it before the state is stored or sent.
const viewOptionSignatureField = "token"

// optionContainer names the surface an external select lives in: one view
// or one message. A token signed for one never vouches for another.
type optionContainer struct {
	label string
	id    string
}

func viewOptionContainer(id domain.ViewID) optionContainer {
	return optionContainer{label: "sameoldchat view option text\x00", id: string(id)}
}

func messageOptionContainer(id domain.MessageID) optionContainer {
	return optionContainer{label: "sameoldchat message option text\x00", id: string(id)}
}

// loadedOptionContainer is the container an options request was made for.
func loadedOptionContainer(query domain.AppOptionQuery) optionContainer {
	if query.ViewID != "" {
		return viewOptionContainer(query.ViewID)
	}
	return messageOptionContainer(query.MessageID)
}

// signOption returns the token that vouches for an external option's text
// inside one element of one view or message. Slack's view_submission and
// block_actions payloads carry the chosen option's text, but the browser only
// knows it because the app sent it, so the text is accepted back only with
// this server-held signature.
func (m Messages) signOption(container optionContainer, blockID, actionID, value, text string) string {
	if len(m.AppCredentialKey) == 0 {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(m.optionMAC(container, blockID, actionID, value, text))
}

// vouchLoadedOptions signs each option loaded for an external select so the
// client can report the chosen option's text back with it.
func (m Messages) vouchLoadedOptions(query domain.AppOptionQuery) func([]domain.AppOption, error) ([]domain.AppOption, error) {
	return func(options []domain.AppOption, err error) ([]domain.AppOption, error) {
		if err != nil {
			return options, err
		}
		container := loadedOptionContainer(query)
		for index := range options {
			options[index].Token = m.signOption(container, query.BlockID, query.ActionID, options[index].Value, options[index].Text)
		}
		return options, nil
	}
}

func (m Messages) optionMAC(container optionContainer, blockID, actionID, value, text string) []byte {
	derived := sha256.Sum256(append([]byte(container.label), m.AppCredentialKey...))
	mac := hmac.New(sha256.New, derived[:])
	for _, part := range []string{container.id, blockID, actionID, value, text} {
		mac.Write([]byte(part))
		mac.Write([]byte{0})
	}
	return mac.Sum(nil)
}

func (m Messages) validOptionToken(container optionContainer, blockID, actionID, value, text, token string) bool {
	if len(m.AppCredentialKey) == 0 || token == "" {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && hmac.Equal(decoded, m.optionMAC(container, blockID, actionID, value, text))
}

// withChosenOptionText gives a message external-select action the text of
// each chosen option the client vouched for with the token issued when the
// option was loaded, as Slack's block_actions payload carries it. An option
// without a valid token keeps only its value.
func (m Messages) withChosenOptionText(actionPayload map[string]any, action domain.AppBlockAction) {
	if action.Type != "external_select" && action.Type != "multi_external_select" {
		return
	}
	container := messageOptionContainer(action.MessageID)
	texts := make(map[string]string, len(action.ChosenOptions))
	for _, chosen := range action.ChosenOptions {
		if chosen.Text != "" && m.validOptionToken(container, action.BlockID, action.ActionID, chosen.Value, chosen.Text, chosen.Token) {
			texts[chosen.Value] = chosen.Text
		}
	}
	attach := func(option map[string]any) {
		if option == nil {
			return
		}
		if _, known := option["text"]; known {
			return
		}
		if text := texts[stringValue(option["value"])]; text != "" {
			option["text"] = map[string]any{"type": "plain_text", "text": text, "emoji": true}
		}
	}
	if selected, ok := actionPayload["selected_option"].(map[string]any); ok {
		attach(selected)
	}
	if selected, ok := actionPayload["selected_options"].([]map[string]any); ok {
		for _, option := range selected {
			attach(option)
		}
	}
}

// sanitizeViewState checks the option text a client reports for external
// selects before the state is stored or sent to the app. Text is kept when
// it is vouched for by a token signed when the options were loaded, by the
// view's own initial option, or by the state this service already accepted;
// anything else is dropped so an app never receives text the user's browser
// invented. Tokens are removed in every case.
func (m Messages) sanitizeViewState(current domain.View, stateJSON string) (string, error) {
	var state struct {
		Values map[string]map[string]map[string]any `json:"values"`
	}
	if json.Unmarshal([]byte(stateJSON), &state) != nil {
		return "", ErrInvalidAppResponse
	}
	var accepted struct {
		Values map[string]map[string]map[string]any `json:"values"`
	}
	_ = json.Unmarshal([]byte(current.State), &accepted)
	for blockID, actions := range state.Values {
		for actionID, action := range actions {
			elementType := strings.TrimSpace(stringValue(action["type"]))
			if elementType != "external_select" && elementType != "multi_external_select" {
				continue
			}
			known := viewInitialOptionTexts(current.Payload, blockID, actionID)
			for value, text := range optionTexts(accepted.Values[blockID][actionID]) {
				known[value] = text
			}
			check := func(raw any) any {
				option, ok := raw.(map[string]any)
				if !ok {
					return raw
				}
				value := stringValue(option["value"])
				token := stringValue(option[viewOptionSignatureField])
				delete(option, viewOptionSignatureField)
				textObject, _ := option["text"].(map[string]any)
				text := stringValue(textObject["text"])
				if text == "" {
					delete(option, "text")
					return option
				}
				if known[value] != text && !m.validOptionToken(viewOptionContainer(current.ID), blockID, actionID, value, text, token) {
					delete(option, "text")
					return option
				}
				option["text"] = map[string]any{"type": "plain_text", "text": text, "emoji": true}
				return option
			}
			if elementType == "external_select" {
				if selected, exists := action["selected_option"]; exists && selected != nil {
					action["selected_option"] = check(selected)
				}
				continue
			}
			if selected, ok := action["selected_options"].([]any); ok {
				for index := range selected {
					selected[index] = check(selected[index])
				}
			}
		}
	}
	if state.Values == nil {
		state.Values = map[string]map[string]map[string]any{}
	}
	encoded, err := json.Marshal(map[string]any{"values": state.Values})
	return string(encoded), err
}

// optionTexts maps each option value in an already-accepted state entry to
// its text.
func optionTexts(action map[string]any) map[string]string {
	result := make(map[string]string)
	add := func(raw any) {
		option, _ := raw.(map[string]any)
		textObject, _ := option["text"].(map[string]any)
		if value, text := stringValue(option["value"]), stringValue(textObject["text"]); value != "" && text != "" {
			result[value] = text
		}
	}
	add(action["selected_option"])
	if selected, ok := action["selected_options"].([]any); ok {
		for _, raw := range selected {
			add(raw)
		}
	}
	return result
}

// viewInitialOptionTexts maps the values of an element's initial_option(s)
// to their text, as the app declared them in the view.
func viewInitialOptionTexts(payload, blockID, actionID string) map[string]string {
	result := make(map[string]string)
	var view struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if json.Unmarshal([]byte(payload), &view) != nil {
		return result
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
			if strings.TrimSpace(stringValue(element["action_id"])) != actionID {
				continue
			}
			for value, text := range optionTexts(map[string]any{"selected_option": element["initial_option"], "selected_options": element["initial_options"]}) {
				result[value] = text
			}
		}
	}
	return result
}
