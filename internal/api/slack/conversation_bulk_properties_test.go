package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestAdminConversationsBulkSetPropertiesSetsTheChannelsThatExist drives the
// method with the reference's example property, as a JSON object in a JSON
// body and as the encoded string a form carries. The channels of the workspace
// are set and the others skipped; a request in which every channel is invalid
// is no_valid_channels, and every other declared refusal is answered with its
// own code.
func TestAdminConversationsBulkSetPropertiesSetsTheChannelsThatExist(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}),
		repository.SeedWorkspace(domain.Workspace{ID: "T2", Name: "Elsewhere"}),
		repository.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "member"}),
		repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}),
		repository.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "random", Kind: domain.ConversationTypePrivate}),
		repository.SeedConversation(domain.Conversation{ID: "CX", WorkspaceID: "T2", Name: "theirs"}),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	if err := repository.SeedWorkspaceRole("T1", "UA", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	scopes := []string{"admin.conversations:write"}
	mux := storedTokenMux(t, repository, map[string]domain.TokenRecord{
		"xoxp-admin":  {WorkspaceID: "T1", UserID: "UA", TokenType: domain.TokenUser, Scopes: scopes},
		"xoxp-member": {WorkspaceID: "T1", UserID: "U1", TokenType: domain.TokenUser, Scopes: scopes},
		"xoxb-bot":    {WorkspaceID: "T1", UserID: "U1", AppID: "A1", BotID: "B1", TokenType: domain.TokenBot, Scopes: scopes},
		"xoxp-reader": {WorkspaceID: "T1", UserID: "UA", TokenType: domain.TokenUser, Scopes: []string{"admin.conversations:read"}},
	})
	excluded := func() []domain.ConversationID {
		t.Helper()
		ids, err := repository.ConversationsExcludedFromAI(ctx, "T1", []domain.ConversationID{"C1", "C2"})
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}

	body, err := json.Marshal(map[string]any{"channel_ids": []string{"C1", "C-nobody", "CX"}, "property": map[string]any{"exclude_from_slack_ai": true}})
	if err != nil {
		t.Fatal(err)
	}
	set := callWith(t, mux, http.MethodPost, "admin.conversations.bulkSetProperties", "xoxp-admin", "application/json", string(body))
	if set["ok"] != true || len(set) != 1 {
		t.Fatalf("JSON request=%v", set)
	}
	if got := excluded(); !reflect.DeepEqual(got, []domain.ConversationID{"C1"}) {
		t.Fatalf("excluded after the JSON request=%v, want C1 alone", got)
	}
	form := url.Values{"channel_ids": {"C1,C2"}, "property": {`{"exclude_from_slack_ai": true }`}}
	if set := callWith(t, mux, http.MethodPost, "admin.conversations.bulkSetProperties", "xoxp-admin", formEncoded, form.Encode()); set["ok"] != true {
		t.Fatalf("form request=%v", set)
	}
	if got := excluded(); !reflect.DeepEqual(got, []domain.ConversationID{"C1", "C2"}) {
		t.Fatalf("excluded after the form request=%v", got)
	}
	query := url.Values{"channel_ids": {`["C2"]`}, "property": {`{"exclude_from_slack_ai":false}`}}
	if set := callWith(t, mux, http.MethodGet, "admin.conversations.bulkSetProperties", "xoxp-admin", "", query.Encode()); set["ok"] != true {
		t.Fatalf("GET request=%v", set)
	}
	if got := excluded(); !reflect.DeepEqual(got, []domain.ConversationID{"C1"}) {
		t.Fatalf("excluded after including C2 again=%v", got)
	}

	tooMany := make([]string, 101)
	for index := range tooMany {
		tooMany[index] = "C1"
	}
	for name, call := range map[string]struct {
		token, channels, property, want string
	}{
		"two properties":               {"xoxp-admin", "C1", `{"exclude_from_slack_ai":true,"is_archived":true}`, "too_many_properties"},
		"a property not allowed":       {"xoxp-admin", "C1", `{"is_archived":true}`, "property_not_allowed"},
		"not a JSON object":            {"xoxp-admin", "C1", `exclude_from_slack_ai=true`, "invalid_arguments"},
		"a value of the wrong type":    {"xoxp-admin", "C1", `{"exclude_from_slack_ai":"maybe"}`, "invalid_arguments"},
		"no property":                  {"xoxp-admin", "C1", ``, "invalid_arguments"},
		"no channel":                   {"xoxp-admin", "", `{"exclude_from_slack_ai":true}`, "invalid_arguments"},
		"more than 100 channels":       {"xoxp-admin", strings.Join(tooMany, ","), `{"exclude_from_slack_ai":true}`, "invalid_arguments"},
		"every channel invalid":        {"xoxp-admin", "C-nobody,CX", `{"exclude_from_slack_ai":true}`, "no_valid_channels"},
		"a member, not an admin":       {"xoxp-member", "C1", `{"exclude_from_slack_ai":true}`, "restricted_action"},
		"a bot token":                  {"xoxb-bot", "C1", `{"exclude_from_slack_ai":true}`, "not_allowed_token_type"},
		"no admin.conversations:write": {"xoxp-reader", "C1", `{"exclude_from_slack_ai":true}`, "missing_scope"},
	} {
		values := url.Values{}
		if call.channels != "" {
			values.Set("channel_ids", call.channels)
		}
		if call.property != "" {
			values.Set("property", call.property)
		}
		got := callWith(t, mux, http.MethodPost, "admin.conversations.bulkSetProperties", call.token, formEncoded, values.Encode())
		if got["ok"] != false || got["error"] != call.want {
			t.Errorf("%s: %v, want %s", name, got, call.want)
		}
	}
	// None of the refusals changed anything.
	if got := excluded(); !reflect.DeepEqual(got, []domain.ConversationID{"C1"}) {
		t.Fatalf("a refused request changed the channels: %v", got)
	}
}
