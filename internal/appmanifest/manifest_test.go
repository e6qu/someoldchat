package appmanifest

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseCanonicalizesCurrentManifestAndExtractsConfiguration(t *testing.T) {
	raw := `{
	  "display_information": {"name": "Alerts", "description": "Posts alerts"},
	  "features": {"slash_commands": [{"command": "/alert", "url": "https://example.com/slack/command", "description": "Post an alert", "usage_hint": "message", "should_escape": true}]},
	  "oauth_config": {
	    "redirect_urls": ["https://example.com/slack/oauth"],
	    "scopes": {"bot": ["chat:write", "chat:write"], "user": ["users:read"]}
	  },
	  "settings": {
	    "event_subscriptions": {"request_url": "https://example.com/slack/events", "bot_events": ["app_mention"]},
	    "interactivity": {"is_enabled": true, "request_url": "https://example.com/slack/actions", "message_menu_options_url": "https://example.com/slack/options"},
	    "token_rotation_enabled": true
	  }
	}`
	parsed, problems := Parse(raw)
	if len(problems) != 0 {
		t.Fatalf("problems=%+v", problems)
	}
	if parsed.Name != "Alerts" || !parsed.TokenRotationEnabled || len(parsed.BotScopes) != 1 || parsed.EventRequestURL != "https://example.com/slack/events" || parsed.BotEvents[0] != "app_mention" || !parsed.InteractivityEnabled || parsed.InteractivityRequestURL != "https://example.com/slack/actions" || parsed.MessageMenuOptionsURL != "https://example.com/slack/options" || len(parsed.SlashCommands) != 1 || parsed.SlashCommands[0].Command != "/alert" || !parsed.SlashCommands[0].ShouldEscape {
		t.Fatalf("parsed=%+v", parsed)
	}
	if strings.Contains(parsed.JSON, "\n") {
		t.Fatalf("manifest was not compacted: %q", parsed.JSON)
	}
}

func TestParseReportsSlackCrossFieldPointers(t *testing.T) {
	_, problems := Parse(`{
	  "display_information": {"name": "Broken"},
	  "features": {"slash_commands": [{"command": "broken"}]},
	  "settings": {
	    "event_subscriptions": {"bot_events": ["message.channels"]},
	    "interactivity": {"is_enabled": true}
	  }
	}`)
	pointers := make(map[string]bool)
	for _, problem := range problems {
		pointers[problem.Pointer] = true
	}
	for _, pointer := range []string{
		"/settings/event_subscriptions",
		"/settings/interactivity",
		"/features/slash_commands/0/command",
		"/features/slash_commands/0/url",
		"/features/slash_commands/0/description",
	} {
		if !pointers[pointer] {
			t.Fatalf("missing %s in %+v", pointer, problems)
		}
	}
}

func TestParseRejectsNonHTTPSRedirectsAndMultipleDocuments(t *testing.T) {
	_, problems := Parse(`{"display_information":{"name":"Bad"},"oauth_config":{"redirect_urls":["http://example.com"]}}`)
	if len(problems) != 1 || problems[0].Pointer != "/oauth_config/redirect_urls/0" {
		t.Fatalf("problems=%+v", problems)
	}
	_, problems = Parse(`{} {}`)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "multiple") {
		t.Fatalf("problems=%+v", problems)
	}
}

func TestParseExtractsAndValidatesSlackShortcutLimits(t *testing.T) {
	parsed, problems := Parse(`{
		"display_information":{"name":"Shortcuts"},
		"features":{"shortcuts":[
			{"name":"Create ticket","callback_id":"create_ticket","description":"Create a ticket","type":"global"},
			{"name":"Attach message","callback_id":"attach_message","description":"Attach this message","type":"message"}
		]},
		"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}
	}`)
	if len(problems) != 0 {
		t.Fatalf("problems=%+v", problems)
	}
	if len(parsed.Shortcuts) != 2 || parsed.Shortcuts[0].CallbackID != "create_ticket" || parsed.Shortcuts[1].Type != "message" {
		t.Fatalf("shortcuts=%+v", parsed.Shortcuts)
	}

	_, problems = Parse(`{
		"display_information":{"name":"Broken shortcuts"},
		"features":{"shortcuts":[
			{"name":"","callback_id":"duplicate","description":"","type":"global"},
			{"name":"Second","callback_id":"duplicate","description":"Second","type":"other"}
		]},
		"settings":{"socket_mode_enabled":true}
	}`)
	pointers := map[string]bool{}
	for _, problem := range problems {
		pointers[problem.Pointer] = true
	}
	for _, pointer := range []string{
		"/features/shortcuts/0/name",
		"/features/shortcuts/0/description",
		"/features/shortcuts/1/callback_id",
		"/features/shortcuts/1/type",
		"/settings/interactivity/is_enabled",
	} {
		if !pointers[pointer] {
			t.Fatalf("missing %s in %+v", pointer, problems)
		}
	}
}

func TestParseCurrentDisplayHostedWebhookAndAgentManifestFields(t *testing.T) {
	longDescription := strings.TrimSpace(strings.Repeat("Current Slack manifest field. ", 7))
	raw := `{
		"_metadata":{"major_version":2,"minor_version":1},
		"display_information":{
			"name":"Agent app",
			"description":"A current app",
			"long_description":` + strconv.Quote(longDescription) + `,
			"background_color":"#4A154B"
		},
		"features":{
			"bot_user":{"display_name":"Agent bot","always_online":true},
			"agent_view":{
				"agent_description":"Helps investigate production incidents.",
				"suggested_prompts":[{"title":"Investigate","message":"What changed in production?"}]
			}
		},
		"oauth_config":{"scopes":{"bot":["datastore:read","datastore:write"]}},
		"settings":{
			"incoming_webhooks":{"incoming_webhooks_enabled":true},
			"is_hosted":true,
			"function_runtime":"slack"
		},
		"functions":{"investigate":{"title":"Investigate","description":"Investigates an incident"}},
		"datastores":{
			"incidents":{
				"primary_key":"id",
				"time_to_live_attribute":"expires_at",
				"attributes":{
					"id":{"type":"string"},
					"title":{"type":"string"},
					"expires_at":{"type":"slack#/types/timestamp"}
				}
			}
		}
	}`
	parsed, problems := Parse(raw)
	if len(problems) != 0 {
		t.Fatalf("problems=%+v", problems)
	}
	if parsed.LongDescription != longDescription || parsed.BackgroundColor != "#4A154B" ||
		!parsed.IncomingWebhooksEnabled || !parsed.IsHosted || parsed.FunctionRuntime != "slack" ||
		parsed.AgentView == nil || parsed.AgentView.Description != "Helps investigate production incidents." ||
		len(parsed.AgentView.SuggestedPrompts) != 1 || parsed.Datastores["incidents"].PrimaryKey != "id" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseRejectsMalformedCurrentManifestFields(t *testing.T) {
	_, problems := Parse(`{
		"display_information":{"name":"Bad","long_description":"too short","background_color":"purple"},
		"features":{
			"bot_user":{},
			"agent_view":{"agent_description":"Agent"},
			"assistant_view":{"assistant_description":"Assistant","suggested_prompts":[{"title":"","message":""}]}
		},
		"settings":{"incoming_webhooks":{"incoming_webhooks_enabled":"yes"},"function_runtime":"browser"},
		"functions":{"run":{"title":"Run"}},
		"datastores":{"bad":{"primary_key":"missing","time_to_live_attribute":"title","attributes":{"title":{"type":"string"}}}}
	}`)
	pointers := map[string]bool{}
	for _, problem := range problems {
		pointers[problem.Pointer] = true
	}
	for _, pointer := range []string{
		"/display_information/long_description",
		"/display_information/background_color",
		"/features/bot_user/display_name",
		"/features",
		"/features/assistant_view/suggested_prompts/0/title",
		"/features/assistant_view/suggested_prompts/0/message",
		"/settings/incoming_webhooks/incoming_webhooks_enabled",
		"/settings/function_runtime",
		"/settings/is_hosted",
		"/oauth_config/scopes/bot",
		"/datastores/bad/primary_key",
		"/datastores/bad/time_to_live_attribute",
	} {
		if !pointers[pointer] {
			t.Fatalf("missing %s in %+v", pointer, problems)
		}
	}
}
