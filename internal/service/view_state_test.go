package service

import (
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A chosen external option keeps its text only when the service vouched for
// it: a token signed when the options were loaded, the view's own initial
// option, or text this service already accepted into the view's state.
func TestSanitizeViewStateKeepsOnlyVouchedExternalOptionText(t *testing.T) {
	m := Messages{AppCredentialKey: []byte(strings.Repeat("k", 32))}
	current := domain.View{
		ID:      "V1",
		Payload: `{"type":"modal","blocks":[{"type":"input","block_id":"b","element":{"type":"external_select","action_id":"a","initial_option":{"text":{"type":"plain_text","text":"Initial"},"value":"init"}}},{"type":"input","block_id":"m","element":{"type":"multi_external_select","action_id":"a"}}]}`,
		State:   `{"values":{"m":{"a":{"type":"multi_external_select","selected_options":[{"text":{"type":"plain_text","text":"Kept"},"value":"kept"}]}}}}`,
	}
	token := m.signViewOption("V1", "b", "a", "v1", "Loaded")
	if token == "" {
		t.Fatal("no token signed")
	}
	cases := []struct {
		name, state, want string
	}{
		{"signed", `{"values":{"b":{"a":{"type":"external_select","selected_option":{"value":"v1","text":{"type":"plain_text","text":"Loaded"},"token":"` + token + `"}}}}}`, `"text":{"emoji":true,"text":"Loaded","type":"plain_text"},"value":"v1"`},
		{"forged text", `{"values":{"b":{"a":{"type":"external_select","selected_option":{"value":"v1","text":{"type":"plain_text","text":"Forged"},"token":"` + token + `"}}}}}`, `"selected_option":{"value":"v1"}`},
		{"token for another element", `{"values":{"m":{"a":{"type":"multi_external_select","selected_options":[{"value":"v1","text":{"type":"plain_text","text":"Loaded"},"token":"` + token + `"}]}}}}`, `"selected_options":[{"value":"v1"}]`},
		{"initial option", `{"values":{"b":{"a":{"type":"external_select","selected_option":{"value":"init","text":{"type":"plain_text","text":"Initial"}}}}}}`, `"text":{"emoji":true,"text":"Initial","type":"plain_text"},"value":"init"`},
		{"accepted before", `{"values":{"m":{"a":{"type":"multi_external_select","selected_options":[{"value":"kept","text":{"type":"plain_text","text":"Kept"}}]}}}}`, `"text":{"emoji":true,"text":"Kept","type":"plain_text"},"value":"kept"`},
		{"empty", `{"values":{"b":{"a":{"type":"external_select","selected_option":null}}}}`, `"selected_option":null`},
	}
	for _, test := range cases {
		got, err := m.sanitizeViewState(current, test.state)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if !strings.Contains(got, test.want) || strings.Contains(got, `"token"`) {
			t.Fatalf("%s: state = %s, want it to contain %s and no token", test.name, got, test.want)
		}
	}
	if (Messages{}).signViewOption("V1", "b", "a", "v", "t") != "" {
		t.Fatal("a service without a credential key signed an option")
	}
}
