package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// conversationScopeFixture seeds one conversation of every type U1 belongs to
// and grants the token exactly scopes, as a bot token or a user token.
func conversationScopeFixture(t *testing.T, bot bool, scopes ...auth.Scope) http.Handler {
	t.Helper()
	handler, store := testHandlerWithScopes(scopes...)
	seedEveryConversationType(store)
	if bot {
		return handler
	}
	granted := make(map[auth.Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		granted[scope] = struct{}{}
	}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", TokenType: "user", Scopes: granted})
	if err != nil {
		t.Fatal(err)
	}
	userHandler, err := NewHandler(service.Messages{Store: store}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	userHandler.Register(mux)
	return mux
}

func seedEveryConversationType(store *memory.Store) {
	store.SeedConversation(domain.Conversation{ID: "G1", WorkspaceID: "T1", Name: "secret", Kind: domain.ConversationTypePrivate})
	store.SeedConversationMember("G1", "U1")
	store.SeedConversationMember("G1", "U2")
	store.SeedConversation(domain.Conversation{ID: "D1", WorkspaceID: "T1", Kind: domain.ConversationTypeIM})
	store.SeedConversationMember("D1", "U1")
	store.SeedConversationMember("D1", "U2")
	store.SeedConversation(domain.Conversation{ID: "M1", WorkspaceID: "T1", Name: "mpdm-alice--bob-1", Kind: domain.ConversationTypeMPIM})
	store.SeedConversationMember("M1", "U1")
	store.SeedConversationMember("M1", "U2")
}

type scopeAnswer struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
	Needed string `json:"needed"`
}

func callForScope(t *testing.T, handler http.Handler, path, form string) (scopeAnswer, *httptest.ResponseRecorder) {
	t.Helper()
	response := callSlackForm(t, handler, path, form)
	var answer scopeAnswer
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatalf("%s: body=%s", path, response.Body)
	}
	return answer, response
}

// Reading a conversation takes the scope matching its type. channels:history
// used to read every conversation — private channels and DMs included — and a
// token correctly granted im:history could read no DM at all.
func TestConversationReadsRequireTheScopeOfTheConversationsType(t *testing.T) {
	for _, testCase := range []struct {
		granted auth.Scope
		path    string
		channel string
		needed  string
	}{
		{auth.ScopeChannelsHistory, "/api/conversations.history", "C1", ""},
		{auth.ScopeChannelsHistory, "/api/conversations.history", "G1", "groups:history"},
		{auth.ScopeChannelsHistory, "/api/conversations.history", "D1", "im:history"},
		{auth.ScopeChannelsHistory, "/api/conversations.history", "M1", "mpim:history"},
		{auth.ScopeGroupsHistory, "/api/conversations.history", "G1", ""},
		{auth.ScopeIMHistory, "/api/conversations.history", "D1", ""},
		{auth.ScopeIMHistory, "/api/conversations.history", "C1", "channels:history"},
		{auth.ScopeMPIMHistory, "/api/conversations.history", "M1", ""},
		{auth.ScopeIMHistory, "/api/conversations.replies", "G1", "groups:history"},
		{auth.ScopeChannelsRead, "/api/conversations.info", "C1", ""},
		{auth.ScopeChannelsRead, "/api/conversations.info", "G1", "groups:read"},
		{auth.ScopeGroupsRead, "/api/conversations.info", "G1", ""},
		{auth.ScopeIMRead, "/api/conversations.info", "D1", ""},
		{auth.ScopeMPIMRead, "/api/conversations.members", "M1", ""},
		{auth.ScopeMPIMRead, "/api/conversations.members", "D1", "im:read"},
	} {
		form := "channel=" + testCase.channel
		if testCase.path == "/api/conversations.replies" {
			form += "&ts=1700000000.000100"
		}
		answer, response := callForScope(t, conversationScopeFixture(t, true, testCase.granted), testCase.path, form)
		if testCase.needed == "" {
			if !answer.OK {
				t.Errorf("%s %s with %s: %s", testCase.path, testCase.channel, testCase.granted, response.Body)
			}
			continue
		}
		if answer.Error != "missing_scope" || answer.Needed != testCase.needed {
			t.Errorf("%s %s with %s: %s, want missing_scope needing %s", testCase.path, testCase.channel, testCase.granted, response.Body, testCase.needed)
		}
	}
}

// conversations.list and users.conversations narrow the listing to the
// requested types the token may read, and refuse only a request none of whose
// types it can read. `types` defaults to public_channel, as in Slack; an
// absent argument used to list every type.
func TestConversationListingNarrowsToTheTypesTheTokenMayRead(t *testing.T) {
	ids := func(t *testing.T, handler http.Handler, path, form string) []string {
		t.Helper()
		response := callSlackForm(t, handler, path, form)
		var body struct {
			OK       bool `json:"ok"`
			Channels []struct {
				ID string `json:"id"`
			} `json:"channels"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.OK {
			t.Fatalf("%s %s: %s", path, form, response.Body)
		}
		values := make([]string, 0, len(body.Channels))
		for _, channel := range body.Channels {
			values = append(values, channel.ID)
		}
		return values
	}
	publicOnly := conversationScopeFixture(t, true, auth.ScopeChannelsRead)
	for _, path := range []string{"/api/conversations.list", "/api/users.conversations"} {
		if got := strings.Join(ids(t, publicOnly, path, "types=public_channel,private_channel,im,mpim"), ","); strings.Contains(got, "G1") || strings.Contains(got, "D1") || strings.Contains(got, "M1") || !strings.Contains(got, "C1") {
			t.Errorf("%s with channels:read listed %s", path, got)
		}
		answer, response := callForScope(t, publicOnly, path, "types=private_channel,im")
		if answer.Error != "missing_scope" || answer.Needed != "groups:read,im:read" {
			t.Errorf("%s for types it cannot read: %s", path, response.Body)
		}
	}
	every := conversationScopeFixture(t, true, auth.ScopeChannelsRead, auth.ScopeGroupsRead, auth.ScopeIMRead, auth.ScopeMPIMRead)
	if got := strings.Join(ids(t, every, "/api/conversations.list", ""), ","); strings.Contains(got, "G1") || strings.Contains(got, "D1") || !strings.Contains(got, "C1") {
		t.Errorf("an absent types argument listed %s; Slack defaults to public_channel", got)
	}
	if got := strings.Join(ids(t, every, "/api/conversations.list", "types=private_channel,mpim"), ","); !strings.Contains(got, "G1") || !strings.Contains(got, "M1") || strings.Contains(got, "C1") {
		t.Errorf("types=private_channel,mpim listed %s", got)
	}
	privateOnly := conversationScopeFixture(t, true, auth.ScopeGroupsRead)
	if answer, response := callForScope(t, privateOnly, "/api/conversations.list", ""); answer.Error != "missing_scope" || answer.Needed != "channels:read" {
		t.Errorf("the default public listing with groups:read only: %s", response.Body)
	}
}

// Writes take the conversation write grant for the conversation's type: a
// public channel takes channels:manage from a bot token and channels:write
// from a user token, a private channel groups:write, a DM im:write and a group
// DM mpim:write.
func TestConversationWritesRequireTheScopeOfTheConversationsType(t *testing.T) {
	for _, testCase := range []struct {
		bot     bool
		granted auth.Scope
		path    string
		form    string
		needed  string
	}{
		{true, auth.ScopeChannelsManage, "/api/conversations.setTopic", "channel=C1&topic=t", ""},
		{true, auth.ScopeChannelsManage, "/api/conversations.setTopic", "channel=G1&topic=t", "groups:write"},
		{true, auth.ScopeChannelsWrite, "/api/conversations.setTopic", "channel=C1&topic=t", "channels:manage,groups:write,im:write,mpim:write"},
		{false, auth.ScopeChannelsWrite, "/api/conversations.setTopic", "channel=C1&topic=t", ""},
		{false, auth.ScopeChannelsWrite, "/api/conversations.setPurpose", "channel=G1&purpose=p", "groups:write"},
		{true, auth.ScopeGroupsWrite, "/api/conversations.rename", "channel=G1&name=renamed", ""},
		{true, auth.ScopeGroupsWrite, "/api/conversations.rename", "channel=C1&name=renamed", "channels:manage"},
		{true, auth.ScopeIMWrite, "/api/conversations.mark", "channel=D1&ts=1700000000.000100", ""},
		{true, auth.ScopeIMWrite, "/api/conversations.mark", "channel=M1&ts=1700000000.000100", "mpim:write"},
		{true, auth.ScopeGroupsWrite, "/api/conversations.create", "name=private-room&is_private=true", ""},
		{true, auth.ScopeGroupsWrite, "/api/conversations.create", "name=public-room", "channels:manage"},
		{false, auth.ScopeChannelsWrite, "/api/conversations.create", "name=public-room", ""},
		{true, auth.ScopeIMWrite, "/api/conversations.open", "users=U2", ""},
		{true, auth.ScopeIMWrite, "/api/conversations.open", "users=U2,U3", "mpim:write"},
		{true, auth.ScopeChannelsManage, "/api/conversations.kick", "channel=G1&user=U2", "groups:write"},
		{true, auth.ScopeChannelsManage, "/api/conversations.archive", "channel=G1", "groups:write"},
	} {
		handler := conversationScopeFixture(t, testCase.bot, testCase.granted)
		answer, response := callForScope(t, handler, testCase.path, testCase.form)
		if testCase.needed == "" {
			if answer.Error == "missing_scope" {
				t.Errorf("%s %s (bot=%v, %s) was refused: %s", testCase.path, testCase.form, testCase.bot, testCase.granted, response.Body)
			}
			continue
		}
		if answer.Error != "missing_scope" || answer.Needed != testCase.needed {
			t.Errorf("%s %s (bot=%v, %s): %s, want missing_scope needing %s", testCase.path, testCase.form, testCase.bot, testCase.granted, response.Body, testCase.needed)
		}
	}
}

// Authenticated responses carry X-OAuth-Scopes (what the token holds) and
// X-Accepted-OAuth-Scopes (what the method accepts), which @slack/web-api
// surfaces as response_metadata.scopes and acceptedScopes. Neither was sent.
func TestAuthenticatedResponsesCarryTheOAuthScopeHeaders(t *testing.T) {
	handler := conversationScopeFixture(t, true, auth.ScopeChannelsHistory, auth.ScopeChatWrite)
	_, history := callForScope(t, handler, "/api/conversations.history", "channel=C1")
	if got := history.Header().Get("X-OAuth-Scopes"); got != "channels:history,chat:write" {
		t.Errorf("history X-OAuth-Scopes=%q", got)
	}
	if got := history.Header().Get("X-Accepted-OAuth-Scopes"); got != "channels:history" {
		t.Errorf("history X-Accepted-OAuth-Scopes=%q, want the one scope the public channel needs", got)
	}
	_, refused := callForScope(t, handler, "/api/conversations.info", "channel=C1")
	if got := refused.Header().Get("X-Accepted-OAuth-Scopes"); got != "channels:read,groups:read,im:read,mpim:read" {
		t.Errorf("a missing_scope refusal X-Accepted-OAuth-Scopes=%q", got)
	}
	if got := refused.Header().Get("X-OAuth-Scopes"); got != "channels:history,chat:write" {
		t.Errorf("a missing_scope refusal X-OAuth-Scopes=%q", got)
	}
	_, posted := callForScope(t, handler, "/api/chat.postMessage", "channel=C1&text=hi")
	if got := posted.Header().Get("X-Accepted-OAuth-Scopes"); got != "chat:write" {
		t.Errorf("chat.postMessage X-Accepted-OAuth-Scopes=%q", got)
	}
	_, authTest := callForScope(t, handler, "/api/auth.test", "")
	if _, ok := authTest.Header()["X-Accepted-Oauth-Scopes"]; !ok || authTest.Header().Get("X-OAuth-Scopes") == "" {
		t.Errorf("auth.test headers=%v; a method needing no scope still reports the token's", authTest.Header())
	}
	// No credential was examined, so neither header is claimed.
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/api.test", nil))
	if unauthenticated.Header().Get("X-OAuth-Scopes") != "" || unauthenticated.Header().Get("X-Accepted-OAuth-Scopes") != "" {
		t.Errorf("api.test carried scope headers: %v", unauthenticated.Header())
	}
}
