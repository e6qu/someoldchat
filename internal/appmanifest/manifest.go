package appmanifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const maxManifestBytes = 1 << 20

type Error struct {
	Message string `json:"message"`
	Pointer string `json:"pointer"`
}

type Parsed struct {
	JSON                    string
	Name                    string
	Description             string
	LongDescription         string
	BackgroundColor         string
	SocketModeEnabled       bool
	TokenRotationEnabled    bool
	IncomingWebhooksEnabled bool
	IsHosted                bool
	FunctionRuntime         string
	BotScopes               []string
	UserScopes              []string
	RedirectURLs            []string
	EventRequestURL         string
	BotEvents               []string
	UserEvents              []string
	InteractivityEnabled    bool
	InteractivityRequestURL string
	MessageMenuOptionsURL   string
	SlashCommands           []SlashCommand
	Shortcuts               []Shortcut
	HomeTabEnabled          bool
	MessagesTabEnabled      bool
	MessagesTabReadOnly     bool
	BotDisplayName          string
	BotAlwaysOnline         bool
	AgentView               *AgentView
	AssistantView           *AgentView
	Datastores              map[string]Datastore
	Functions               map[string]Function
}

type AgentView struct {
	Description      string
	SuggestedPrompts []SuggestedPrompt
}

type Function struct {
	CallbackID       string
	Title            string
	Description      string
	InputParameters  []FunctionParameter
	OutputParameters []FunctionParameter
}

type FunctionParameter struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	IsRequired  bool   `json:"is_required"`
}

type SuggestedPrompt struct {
	Title   string
	Message string
}

type Datastore struct {
	Name                string
	PrimaryKey          string
	TimeToLiveAttribute string
	Attributes          map[string]DatastoreAttribute
}

type DatastoreAttribute struct {
	Type string
}

type SlashCommand struct {
	Command      string
	URL          string
	Description  string
	UsageHint    string
	ShouldEscape bool
}

type Shortcut struct {
	Name        string
	CallbackID  string
	Description string
	Type        string
}

// Parse validates the cross-field invariants in Slack's current app-manifest
// contract and returns canonical JSON for durable versioning. The API contract
// accepts a JSON manifest encoded as a string; YAML conversion belongs to the
// developer UI before this boundary.
func Parse(raw string) (Parsed, []Error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Parsed{}, []Error{{Message: "Manifest is required", Pointer: ""}}
	}
	if len(raw) > maxManifestBytes {
		return Parsed{}, []Error{{Message: "Manifest exceeds the 1 MiB limit", Pointer: ""}}
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return Parsed{}, []Error{{Message: "Manifest must be a JSON object: " + err.Error(), Pointer: ""}}
	}
	if document == nil {
		return Parsed{}, []Error{{Message: "Manifest must be a JSON object", Pointer: ""}}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Parsed{}, []Error{{Message: "Manifest contains multiple JSON values", Pointer: ""}}
	} else if !errors.Is(err, io.EOF) {
		return Parsed{}, []Error{{Message: "Manifest contains invalid trailing JSON: " + err.Error(), Pointer: ""}}
	}

	var problems []Error
	display := object(document, "display_information")
	name := stringField(display, "name")
	if name == "" {
		problems = append(problems, Error{Message: "App name is required", Pointer: "/display_information/name"})
	} else if len([]rune(name)) > 35 {
		problems = append(problems, Error{Message: "App name must be 35 characters or fewer", Pointer: "/display_information/name"})
	}
	description := stringField(display, "description")
	if len([]rune(description)) > 140 {
		problems = append(problems, Error{Message: "Description must be 140 characters or fewer", Pointer: "/display_information/description"})
	}
	longDescription := stringField(display, "long_description")
	if longDescription != "" && (len([]rune(longDescription)) < 174 || len([]rune(longDescription)) > 4000) {
		problems = append(problems, Error{Message: "Long description must be between 174 and 4000 characters", Pointer: "/display_information/long_description"})
	}
	backgroundColor := stringField(display, "background_color")
	if backgroundColor != "" && !manifestColor.MatchString(backgroundColor) {
		problems = append(problems, Error{Message: "Background color must be a 3- or 6-digit hexadecimal color", Pointer: "/display_information/background_color"})
	}

	settings := object(document, "settings")
	socketMode := boolField(settings, "socket_mode_enabled")
	tokenRotation := boolField(settings, "token_rotation_enabled")
	incomingWebhooks := object(settings, "incoming_webhooks")
	incomingWebhooksEnabled := boolField(incomingWebhooks, "incoming_webhooks_enabled")
	if _, exists := settings["incoming_webhooks"]; exists {
		if _, ok := settings["incoming_webhooks"].(map[string]any); !ok {
			problems = append(problems, Error{Message: "Incoming webhooks settings must be an object", Pointer: "/settings/incoming_webhooks"})
		} else if _, ok := incomingWebhooks["incoming_webhooks_enabled"].(bool); !ok {
			problems = append(problems, Error{Message: "incoming_webhooks_enabled must be a boolean", Pointer: "/settings/incoming_webhooks/incoming_webhooks_enabled"})
		}
	}
	isHosted := boolField(settings, "is_hosted")
	functionRuntime := stringField(settings, "function_runtime")
	if functionRuntime != "" && functionRuntime != "remote" && functionRuntime != "slack" {
		problems = append(problems, Error{Message: "Function runtime must be remote or slack", Pointer: "/settings/function_runtime"})
	}
	events := object(settings, "event_subscriptions")
	eventRequestURL := stringField(events, "request_url")
	botEvents := stringSlice(events, "bot_events", "/settings/event_subscriptions/bot_events", &problems)
	userEvents := stringSlice(events, "user_events", "/settings/event_subscriptions/user_events", &problems)
	if len(botEvents) > 100 {
		problems = append(problems, Error{Message: "Bot event subscriptions can contain at most 100 events", Pointer: "/settings/event_subscriptions/bot_events"})
	}
	if len(userEvents) > 100 {
		problems = append(problems, Error{Message: "User event subscriptions can contain at most 100 events", Pointer: "/settings/event_subscriptions/user_events"})
	}
	if (len(botEvents) > 0 || len(userEvents) > 0) && eventRequestURL == "" && !socketMode {
		problems = append(problems, Error{
			Message: "Event Subscription requires either Request URL or Socket Mode Enabled",
			Pointer: "/settings/event_subscriptions",
		})
	}
	if eventRequestURL != "" {
		validateHTTPSURL(eventRequestURL, "/settings/event_subscriptions/request_url", &problems)
	}

	interactivity := object(settings, "interactivity")
	interactivityEnabled := boolField(interactivity, "is_enabled")
	interactivityRequestURL := stringField(interactivity, "request_url")
	messageMenuOptionsURL := stringField(interactivity, "message_menu_options_url")
	if interactivityEnabled {
		requestURL := interactivityRequestURL
		if requestURL == "" && !socketMode {
			problems = append(problems, Error{
				Message: "Interactivity requires either a Request URL or Socket Mode Enabled",
				Pointer: "/settings/interactivity",
			})
		}
		if requestURL != "" {
			validateHTTPSURL(requestURL, "/settings/interactivity/request_url", &problems)
		}
	}
	if messageMenuOptionsURL != "" {
		validateHTTPSURL(messageMenuOptionsURL, "/settings/interactivity/message_menu_options_url", &problems)
		if len(messageMenuOptionsURL) > 150 {
			problems = append(problems, Error{Message: "Options Load URL must be 150 characters or fewer", Pointer: "/settings/interactivity/message_menu_options_url"})
		}
	}

	oauth := object(document, "oauth_config")
	redirects := stringSlice(oauth, "redirect_urls", "/oauth_config/redirect_urls", &problems)
	for index, redirect := range redirects {
		validateHTTPSURL(redirect, fmt.Sprintf("/oauth_config/redirect_urls/%d", index), &problems)
	}
	scopes := object(oauth, "scopes")
	botScopes := normalizedUnique(stringSlice(scopes, "bot", "/oauth_config/scopes/bot", &problems))
	userScopes := normalizedUnique(stringSlice(scopes, "user", "/oauth_config/scopes/user", &problems))
	if len(botScopes) > 255 {
		problems = append(problems, Error{Message: "Bot scopes can contain at most 255 scopes", Pointer: "/oauth_config/scopes/bot"})
	}
	if len(userScopes) > 255 {
		problems = append(problems, Error{Message: "User scopes can contain at most 255 scopes", Pointer: "/oauth_config/scopes/user"})
	}

	features := object(document, "features")
	appHome := object(features, "app_home")
	homeTabEnabled := boolField(appHome, "home_tab_enabled")
	messagesTabEnabled := boolField(appHome, "messages_tab_enabled")
	messagesTabReadOnly := boolField(appHome, "messages_tab_read_only_enabled")
	if messagesTabReadOnly && !messagesTabEnabled {
		problems = append(problems, Error{
			Message: "Read-only Messages tab requires the Messages tab to be enabled",
			Pointer: "/features/app_home/messages_tab_read_only_enabled",
		})
	}
	botUser := object(features, "bot_user")
	botDisplayName := stringField(botUser, "display_name")
	if _, exists := features["bot_user"]; exists && botDisplayName == "" {
		problems = append(problems, Error{Message: "Bot display name is required", Pointer: "/features/bot_user/display_name"})
	} else if len([]rune(botDisplayName)) > 80 {
		problems = append(problems, Error{Message: "Bot display name must be 80 characters or fewer", Pointer: "/features/bot_user/display_name"})
	}
	botAlwaysOnline := boolField(botUser, "always_online")
	agentView := parseAgentView(features, "agent_view", "agent_description", &problems)
	assistantView := parseAgentView(features, "assistant_view", "assistant_description", &problems)
	if agentView != nil && assistantView != nil {
		problems = append(problems, Error{Message: "agent_view and assistant_view are mutually exclusive", Pointer: "/features"})
	}
	var shortcuts []Shortcut
	if values, ok := features["shortcuts"].([]any); ok {
		if len(values) > 10 {
			problems = append(problems, Error{Message: "A manifest can contain at most 10 shortcuts", Pointer: "/features/shortcuts"})
		}
		typeCounts := map[string]int{}
		callbacks := map[string]bool{}
		for index, value := range values {
			shortcut, ok := value.(map[string]any)
			pointer := fmt.Sprintf("/features/shortcuts/%d", index)
			if !ok {
				problems = append(problems, Error{Message: "Shortcut must be an object", Pointer: pointer})
				continue
			}
			name := stringField(shortcut, "name")
			if name == "" {
				problems = append(problems, Error{Message: "Shortcut name is required", Pointer: pointer + "/name"})
			}
			callbackID := stringField(shortcut, "callback_id")
			if callbackID == "" {
				problems = append(problems, Error{Message: "Shortcut callback_id is required", Pointer: pointer + "/callback_id"})
			} else if len([]rune(callbackID)) > 255 {
				problems = append(problems, Error{Message: "Shortcut callback_id must be 255 characters or fewer", Pointer: pointer + "/callback_id"})
			} else if callbacks[callbackID] {
				problems = append(problems, Error{Message: "Shortcut callback_id must be unique", Pointer: pointer + "/callback_id"})
			}
			callbacks[callbackID] = true
			description := stringField(shortcut, "description")
			if description == "" {
				problems = append(problems, Error{Message: "Shortcut description is required", Pointer: pointer + "/description"})
			} else if len([]rune(description)) > 150 {
				problems = append(problems, Error{Message: "Shortcut description must be 150 characters or fewer", Pointer: pointer + "/description"})
			}
			shortcutType := stringField(shortcut, "type")
			if shortcutType != "global" && shortcutType != "message" {
				problems = append(problems, Error{Message: "Shortcut type must be global or message", Pointer: pointer + "/type"})
			} else {
				typeCounts[shortcutType]++
				if typeCounts[shortcutType] > 5 {
					problems = append(problems, Error{Message: "An app can contain at most 5 " + shortcutType + " shortcuts", Pointer: "/features/shortcuts"})
				}
			}
			shortcuts = append(shortcuts, Shortcut{Name: name, CallbackID: callbackID, Description: description, Type: shortcutType})
		}
		if len(values) != 0 && !interactivityEnabled {
			problems = append(problems, Error{Message: "Shortcuts require Interactivity to be enabled", Pointer: "/settings/interactivity/is_enabled"})
		}
	}
	var slashCommands []SlashCommand
	if commands, ok := features["slash_commands"].([]any); ok {
		if len(commands) > 50 {
			problems = append(problems, Error{Message: "A manifest can contain at most 50 slash commands", Pointer: "/features/slash_commands"})
		}
		for index, value := range commands {
			command, ok := value.(map[string]any)
			pointer := fmt.Sprintf("/features/slash_commands/%d", index)
			if !ok {
				problems = append(problems, Error{Message: "Slash command must be an object", Pointer: pointer})
				continue
			}
			name := stringField(command, "command")
			if !strings.HasPrefix(name, "/") || len(name) < 2 {
				problems = append(problems, Error{Message: "Slash command must begin with /", Pointer: pointer + "/command"})
			} else if len([]rune(name)) > 32 {
				problems = append(problems, Error{Message: "Slash command must be 32 characters or fewer", Pointer: pointer + "/command"})
			}
			requestURL := stringField(command, "url")
			if requestURL == "" && !socketMode {
				problems = append(problems, Error{Message: "Slash command requires a Request URL or Socket Mode Enabled", Pointer: pointer + "/url"})
			}
			if requestURL != "" {
				validateHTTPSURL(requestURL, pointer+"/url", &problems)
			}
			description := stringField(command, "description")
			if description == "" {
				problems = append(problems, Error{Message: "Slash command description is required", Pointer: pointer + "/description"})
			} else if len([]rune(description)) > 2000 {
				problems = append(problems, Error{Message: "Slash command description must be 2000 characters or fewer", Pointer: pointer + "/description"})
			}
			usageHint := stringField(command, "usage_hint")
			if len([]rune(usageHint)) > 1000 {
				problems = append(problems, Error{Message: "Slash command usage hint must be 1000 characters or fewer", Pointer: pointer + "/usage_hint"})
			}
			slashCommands = append(slashCommands, SlashCommand{
				Command:      name,
				URL:          requestURL,
				Description:  description,
				UsageHint:    usageHint,
				ShouldEscape: boolField(command, "should_escape"),
			})
		}
	}
	functions := parseFunctions(document["functions"], &problems)
	if len(functions) != 0 && functionRuntime == "" {
		problems = append(problems, Error{Message: "function_runtime is required when functions are declared", Pointer: "/settings/function_runtime"})
	}
	datastores := parseDatastores(document["datastores"], &problems)
	if len(datastores) != 0 {
		if !isHosted || functionRuntime != "slack" {
			problems = append(problems, Error{Message: "Datastores require a Slack-hosted app runtime", Pointer: "/settings/is_hosted"})
		}
		if !containsString(botScopes, "datastore:read") || !containsString(botScopes, "datastore:write") {
			problems = append(problems, Error{Message: "Datastores require datastore:read and datastore:write bot scopes", Pointer: "/oauth_config/scopes/bot"})
		}
	}

	if len(problems) > 0 {
		return Parsed{}, problems
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(raw)); err != nil {
		return Parsed{}, []Error{{Message: "Manifest is not valid JSON", Pointer: ""}}
	}
	return Parsed{
		JSON:                    compact.String(),
		Name:                    name,
		Description:             description,
		LongDescription:         longDescription,
		BackgroundColor:         backgroundColor,
		SocketModeEnabled:       socketMode,
		TokenRotationEnabled:    tokenRotation,
		IncomingWebhooksEnabled: incomingWebhooksEnabled,
		IsHosted:                isHosted,
		FunctionRuntime:         functionRuntime,
		BotScopes:               botScopes,
		UserScopes:              userScopes,
		RedirectURLs:            redirects,
		EventRequestURL:         eventRequestURL,
		BotEvents:               normalizedUnique(botEvents),
		UserEvents:              normalizedUnique(userEvents),
		InteractivityEnabled:    interactivityEnabled,
		InteractivityRequestURL: interactivityRequestURL,
		MessageMenuOptionsURL:   messageMenuOptionsURL,
		SlashCommands:           slashCommands,
		Shortcuts:               shortcuts,
		HomeTabEnabled:          homeTabEnabled,
		MessagesTabEnabled:      messagesTabEnabled,
		MessagesTabReadOnly:     messagesTabReadOnly,
		BotDisplayName:          botDisplayName,
		BotAlwaysOnline:         botAlwaysOnline,
		AgentView:               agentView,
		AssistantView:           assistantView,
		Datastores:              datastores,
		Functions:               functions,
	}, nil
}

func parseFunctions(raw any, problems *[]Error) map[string]Function {
	if raw == nil {
		return map[string]Function{}
	}
	values, ok := raw.(map[string]any)
	if !ok {
		*problems = append(*problems, Error{Message: "Functions must be an object", Pointer: "/functions"})
		return map[string]Function{}
	}
	result := make(map[string]Function, len(values))
	for callbackID, rawFunction := range values {
		pointer := "/functions/" + callbackID
		value, ok := rawFunction.(map[string]any)
		if !ok {
			*problems = append(*problems, Error{Message: "Function must be an object", Pointer: pointer})
			continue
		}
		title := strings.TrimSpace(stringField(value, "title"))
		description := strings.TrimSpace(stringField(value, "description"))
		if strings.TrimSpace(callbackID) == "" {
			*problems = append(*problems, Error{Message: "Function callback ID is required", Pointer: pointer})
			continue
		}
		if title == "" {
			*problems = append(*problems, Error{Message: "Function title is required", Pointer: pointer + "/title"})
		}
		result[callbackID] = Function{
			CallbackID: callbackID, Title: title, Description: description,
			InputParameters:  parseFunctionParameters(value["input_parameters"], pointer+"/input_parameters", problems),
			OutputParameters: parseFunctionParameters(value["output_parameters"], pointer+"/output_parameters", problems),
		}
	}
	return result
}

func parseFunctionParameters(raw any, pointer string, problems *[]Error) []FunctionParameter {
	if raw == nil {
		return []FunctionParameter{}
	}
	schema, ok := raw.(map[string]any)
	if !ok {
		*problems = append(*problems, Error{Message: "Function parameters must be an object", Pointer: pointer})
		return []FunctionParameter{}
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok && schema["properties"] != nil {
		*problems = append(*problems, Error{Message: "Function parameter properties must be an object", Pointer: pointer + "/properties"})
		return []FunctionParameter{}
	}
	required := map[string]bool{}
	if values, ok := schema["required"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]FunctionParameter, 0, len(names))
	for _, name := range names {
		value, ok := properties[name].(map[string]any)
		if !ok {
			*problems = append(*problems, Error{Message: "Function parameter must be an object", Pointer: pointer + "/properties/" + name})
			continue
		}
		typeName := strings.TrimSpace(stringField(value, "type"))
		title := strings.TrimSpace(stringField(value, "title"))
		if typeName == "" {
			*problems = append(*problems, Error{Message: "Function parameter type is required", Pointer: pointer + "/properties/" + name + "/type"})
		}
		if title == "" {
			title = name
		}
		result = append(result, FunctionParameter{
			Name: name, Type: typeName, Title: title, Description: strings.TrimSpace(stringField(value, "description")),
			IsRequired: required[name],
		})
	}
	return result
}

var manifestColor = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

func parseAgentView(features map[string]any, field, descriptionField string, problems *[]Error) *AgentView {
	raw, exists := features[field]
	if !exists {
		return nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		*problems = append(*problems, Error{Message: field + " must be an object", Pointer: "/features/" + field})
		return nil
	}
	description := stringField(value, descriptionField)
	if description == "" {
		*problems = append(*problems, Error{Message: descriptionField + " is required", Pointer: "/features/" + field + "/" + descriptionField})
	} else if field == "agent_view" && len([]rune(description)) > 300 {
		*problems = append(*problems, Error{Message: "Agent description must be 300 characters or fewer", Pointer: "/features/" + field + "/" + descriptionField})
	}
	var prompts []SuggestedPrompt
	if rawPrompts, exists := value["suggested_prompts"]; exists {
		values, ok := rawPrompts.([]any)
		if !ok {
			*problems = append(*problems, Error{Message: "suggested_prompts must be an array", Pointer: "/features/" + field + "/suggested_prompts"})
		} else {
			for index, rawPrompt := range values {
				prompt, ok := rawPrompt.(map[string]any)
				pointer := fmt.Sprintf("/features/%s/suggested_prompts/%d", field, index)
				if !ok {
					*problems = append(*problems, Error{Message: "Suggested prompt must be an object", Pointer: pointer})
					continue
				}
				title, message := stringField(prompt, "title"), stringField(prompt, "message")
				if title == "" {
					*problems = append(*problems, Error{Message: "Suggested prompt title is required", Pointer: pointer + "/title"})
				}
				if message == "" {
					*problems = append(*problems, Error{Message: "Suggested prompt message is required", Pointer: pointer + "/message"})
				}
				prompts = append(prompts, SuggestedPrompt{Title: title, Message: message})
			}
		}
	}
	return &AgentView{Description: description, SuggestedPrompts: prompts}
}

func parseDatastores(raw any, problems *[]Error) map[string]Datastore {
	if raw == nil {
		return nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		*problems = append(*problems, Error{Message: "Datastores must be an object", Pointer: "/datastores"})
		return nil
	}
	result := make(map[string]Datastore, len(values))
	for rawName, rawValue := range values {
		name := strings.TrimSpace(rawName)
		pointer := "/datastores/" + escapeJSONPointer(rawName)
		value, ok := rawValue.(map[string]any)
		if !ok || name == "" {
			*problems = append(*problems, Error{Message: "Datastore must be a named object", Pointer: pointer})
			continue
		}
		primaryKey := stringField(value, "primary_key")
		if primaryKey == "" {
			*problems = append(*problems, Error{Message: "Datastore primary_key is required", Pointer: pointer + "/primary_key"})
		}
		rawAttributes, ok := value["attributes"].(map[string]any)
		if !ok || len(rawAttributes) == 0 {
			*problems = append(*problems, Error{Message: "Datastore attributes must be a non-empty object", Pointer: pointer + "/attributes"})
			continue
		}
		attributes := make(map[string]DatastoreAttribute, len(rawAttributes))
		for rawAttributeName, rawAttribute := range rawAttributes {
			attributeName := strings.TrimSpace(rawAttributeName)
			attributePointer := pointer + "/attributes/" + escapeJSONPointer(rawAttributeName)
			definition, ok := rawAttribute.(map[string]any)
			attributeType := stringField(definition, "type")
			if !ok || attributeName == "" || attributeType == "" {
				*problems = append(*problems, Error{Message: "Datastore attribute requires a type", Pointer: attributePointer + "/type"})
				continue
			}
			attributes[attributeName] = DatastoreAttribute{Type: attributeType}
		}
		if primary, exists := attributes[primaryKey]; primaryKey != "" && (!exists || primary.Type != "string") {
			*problems = append(*problems, Error{Message: "Datastore primary_key must name a string attribute", Pointer: pointer + "/primary_key"})
		}
		ttl := stringField(value, "time_to_live_attribute")
		if ttl != "" {
			attribute, exists := attributes[ttl]
			if !exists || attribute.Type != "timestamp" && !strings.HasSuffix(attribute.Type, "/timestamp") {
				*problems = append(*problems, Error{Message: "time_to_live_attribute must name a timestamp attribute", Pointer: pointer + "/time_to_live_attribute"})
			}
		}
		result[name] = Datastore{Name: name, PrimaryKey: primaryKey, TimeToLiveAttribute: ttl, Attributes: attributes}
	}
	return result
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func object(parent map[string]any, name string) map[string]any {
	value, _ := parent[name].(map[string]any)
	return value
}

func stringField(parent map[string]any, name string) string {
	value, _ := parent[name].(string)
	return strings.TrimSpace(value)
}

func boolField(parent map[string]any, name string) bool {
	value, _ := parent[name].(bool)
	return value
}

func stringSlice(parent map[string]any, name, pointer string, problems *[]Error) []string {
	raw, exists := parent[name]
	if !exists {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		*problems = append(*problems, Error{Message: name + " must be an array of strings", Pointer: pointer})
		return nil
	}
	result := make([]string, 0, len(values))
	for index, value := range values {
		text, ok := value.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" {
			*problems = append(*problems, Error{
				Message: name + " must contain non-empty strings",
				Pointer: fmt.Sprintf("%s/%d", pointer, index),
			})
			continue
		}
		result = append(result, text)
	}
	return result
}

func normalizedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validateHTTPSURL(value, pointer string, problems *[]Error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		*problems = append(*problems, Error{Message: "URL must be an absolute HTTPS URL", Pointer: pointer})
	}
}
