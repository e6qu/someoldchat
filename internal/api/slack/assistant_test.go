package slack

import (
	"context"
	"net/url"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// TestAssistantThreadsSetStatusTakesAnIdentityOverride drives
// assistant.threads.setStatus's icon_emoji, icon_url and username over HTTP as
// python-slack-sdk 3.45 and slack-bolt 1.30 send them: chat:write.customize
// on top of chat:write, missing_scope naming it otherwise, and a JSON null the
// same as leaving the argument out — so a call whose three are null sets the
// status without an override. Every refusal is an HTTP 200 Slack error.
func TestAssistantThreadsSetStatusTakesAnIdentityOverride(t *testing.T) {
	mux, thread, repository := agentSessionAPI(t)
	key := `"channel_id":"C1","thread_ts":"` + string(thread) + `"`
	read := func() domain.AssistantThread {
		t.Helper()
		value, err := repository.GetAssistantThread(context.Background(), "T1", "C1", thread)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}

	customized := callAgentSessionMethod(t, mux, "xoxb-custom", "assistant.threads.setStatus",
		`{`+key+`,"status":"is thinking...","icon_emoji":":robot_face:","icon_url":"https://example.test/bot.png","username":"Deploy bot"}`, false)
	if customized["ok"] != true {
		t.Fatalf("customized=%v", customized)
	}
	want := domain.AgentIdentity{Username: "Deploy bot", IconEmoji: ":robot_face:", IconURL: "https://example.test/bot.png"}
	if value := read(); value.Status != "is thinking..." || value.StatusIdentity != want || value.StatusUserID != "UB1" {
		t.Fatalf("stored=%+v, want the override and its setter", value)
	}

	// The form encoding carries the same three as plain fields.
	form := callAgentSessionMethod(t, mux, "xoxb-custom", "assistant.threads.setStatus",
		"channel_id=C1&thread_ts="+url.QueryEscape(string(thread))+"&status=working&username=Form+bot", true)
	if form["ok"] != true || read().StatusIdentity != (domain.AgentIdentity{Username: "Form bot"}) {
		t.Fatalf("form=%v stored=%+v", form, read())
	}

	nulls := callAgentSessionMethod(t, mux, "xoxb-custom", "assistant.threads.setStatus",
		`{`+key+`,"status":"still working","icon_emoji":null,"icon_url":null,"username":null}`, false)
	if nulls["ok"] != true {
		t.Fatalf("all three null=%v", nulls)
	}
	if value := read(); value.Status != "still working" || !value.StatusIdentity.Empty() {
		t.Fatalf("stored after nulls=%+v, want no override", value)
	}

	for name, test := range map[string]struct {
		token string
		body  string
		want  string
	}{
		"customizing without scope":    {"xoxb-agent", `{` + key + `,"status":"working","username":"Someone"}`, "missing_scope"},
		"an emoji without scope":       {"xoxb-agent", `{` + key + `,"status":"working","icon_emoji":":x:"}`, "missing_scope"},
		"an icon that is not a URL":    {"xoxb-custom", `{` + key + `,"status":"working","icon_url":"javascript:alert(1)"}`, "invalid_arguments"},
		"a null elsewhere is no value": {"xoxb-custom", `{` + key + `,"status":null}`, "invalid_arg_name"},
	} {
		t.Run(name, func(t *testing.T) {
			result := callAgentSessionMethod(t, mux, test.token, "assistant.threads.setStatus", test.body, false)
			if result["ok"] != false || result["error"] != test.want {
				t.Fatalf("result=%v, want %s", result, test.want)
			}
		})
	}
	scope := callAgentSessionMethod(t, mux, "xoxb-agent", "assistant.threads.setStatus", `{`+key+`,"status":"working","icon_url":"https://example.test/bot.png"}`, false)
	if scope["needed"] != "chat:write.customize" {
		t.Fatalf("missing_scope names the scope: %v", scope)
	}
	// Without an override chat:write alone still sets a status.
	plain := callAgentSessionMethod(t, mux, "xoxb-agent", "assistant.threads.setStatus", `{`+key+`,"status":"plain"}`, false)
	if plain["ok"] != true || read().Status != "plain" {
		t.Fatalf("plain=%v", plain)
	}
	// The other two assistant writes still refuse a null identity argument
	// as they refuse any null: only setStatus takes the override.
	title := callAgentSessionMethod(t, mux, "xoxb-custom", "assistant.threads.setTitle", `{`+key+`,"title":"Deploy","username":null}`, false)
	if title["ok"] != false || title["error"] != "invalid_arg_name" {
		t.Fatalf("setTitle with a null username=%v", title)
	}
}
