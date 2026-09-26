package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// The contracts in this file are the message-object shapes and message-method
// outcomes the official SDKs depend on, each of which this transport used to
// get wrong: history listed thread replies, windows were applied after the page
// was fetched, parents carried no thread summary, messages carried no
// reactions, pins or bot identity, and several refusals used codes the
// operation does not declare.

const contractOrigin = "https://chat.example.test"

// slackCall posts a form to one method with a bearer token and decodes the
// envelope. Every request is addressed to contractOrigin, so any absolute URL
// in a response can be checked against the origin the client used.
func slackCall(t *testing.T, handler http.Handler, token, method string, form url.Values) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, contractOrigin+"/api/"+method, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s decode %q: %v", method, response.Body.String(), err)
	}
	return body
}

func requireOK(t *testing.T, method string, body map[string]any) {
	t.Helper()
	if body["ok"] != true {
		t.Fatalf("%s failed: %v", method, body)
	}
}

func requireError(t *testing.T, method string, body map[string]any, code string) {
	t.Helper()
	if body["ok"] != false || body["error"] != code {
		t.Fatalf("%s = %v, want error %q", method, body, code)
	}
}

// post sends chat.postMessage with the fixture's bot token and answers the ts.
func post(t *testing.T, handler http.Handler, form url.Values) string {
	t.Helper()
	body := slackCall(t, handler, "token", "chat.postMessage", form)
	requireOK(t, "chat.postMessage", body)
	return body["ts"].(string)
}

func messagesOf(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, _ := body["messages"].([]any)
	result := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		result = append(result, value.(map[string]any))
	}
	return result
}

func timestampsOf(messages []map[string]any) []string {
	result := make([]string, 0, len(messages))
	for _, message := range messages {
		result = append(result, message["ts"].(string))
	}
	return result
}

// userTokenHandler serves the same repository with a user token, which is the
// only credential that sees `subscribed`, and the one as_user applies to.
func userTokenHandler(t *testing.T, repository *memory.Store) http.Handler {
	t.Helper()
	return userSearchHandler(t, repository)
}

func TestHistoryListsRootsAndBroadcastRepliesOnly(t *testing.T) {
	handler := testHandler()
	root := post(t, handler, url.Values{"channel": {"C1"}, "text": {"root"}})
	reply := post(t, handler, url.Values{"channel": {"C1"}, "text": {"quiet reply"}, "thread_ts": {root}})
	broadcast := post(t, handler, url.Values{"channel": {"C1"}, "text": {"loud reply"}, "thread_ts": {root}, "reply_broadcast": {"true"}})
	history := slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}})
	requireOK(t, "conversations.history", history)
	got := timestampsOf(messagesOf(t, history))
	if strings.Join(got, ",") != broadcast+","+root {
		t.Fatalf("history = %v, want the broadcast reply and the root only (reply %s must be absent)", got, reply)
	}
	listed := messagesOf(t, history)
	if listed[0]["subtype"] != "thread_broadcast" || listed[0]["reply_broadcast"] != true || listed[0]["thread_ts"] != root {
		t.Fatalf("broadcast reply = %v", listed[0])
	}
	// A limit counts listed messages only: one root-sized page is exactly the
	// broadcast, with more history behind it.
	page := slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}, "limit": {"1"}})
	if got := timestampsOf(messagesOf(t, page)); len(got) != 1 || got[0] != broadcast || page["has_more"] != true {
		t.Fatalf("limit=1 page = %v has_more=%v", got, page["has_more"])
	}
}

func TestHistoryWindowIsPartOfTheRead(t *testing.T) {
	handler := testHandler()
	first := post(t, handler, url.Values{"channel": {"C1"}, "text": {"one"}})
	second := post(t, handler, url.Values{"channel": {"C1"}, "text": {"two"}})
	third := post(t, handler, url.Values{"channel": {"C1"}, "text": {"three"}})

	// The newest message is outside this window, so a filter applied after
	// fetching one row answered an empty page.
	exact := slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}, "latest": {second}, "inclusive": {"true"}, "limit": {"1"}})
	requireOK(t, "conversations.history", exact)
	if got := timestampsOf(messagesOf(t, exact)); len(got) != 1 || got[0] != second || exact["has_more"] != true {
		t.Fatalf("latest=%s inclusive limit=1 = %v has_more=%v", second, got, exact["has_more"])
	}
	between := slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}, "oldest": {first}, "latest": {third}})
	if got := timestampsOf(messagesOf(t, between)); len(got) != 1 || got[0] != second || between["has_more"] != false {
		t.Fatalf("exclusive window = %v has_more=%v, want only %s", got, between["has_more"], second)
	}
	// Slack's default oldest is 0, and SDKs send it.
	everything := slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}, "oldest": {"0"}})
	if got := timestampsOf(messagesOf(t, everything)); len(got) != 3 {
		t.Fatalf("oldest=0 = %v, want every message", got)
	}
	requireError(t, "conversations.history", slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}, "latest": {"soon"}}), "invalid_ts_latest")

	// A replies window narrows the replies; the root always leads.
	firstReply := post(t, handler, url.Values{"channel": {"C1"}, "text": {"r1"}, "thread_ts": {first}})
	secondReply := post(t, handler, url.Values{"channel": {"C1"}, "text": {"r2"}, "thread_ts": {first}})
	replies := slackCall(t, handler, "token", "conversations.replies", url.Values{"channel": {"C1"}, "ts": {first}, "oldest": {firstReply}})
	if got := timestampsOf(messagesOf(t, replies)); strings.Join(got, ",") != first+","+secondReply {
		t.Fatalf("replies oldest=%s = %v", firstReply, got)
	}
}

func TestThreadParentsCarryTheirSummary(t *testing.T) {
	handler, repository := testHandlerWithStore()
	root := post(t, handler, url.Values{"channel": {"C1"}, "text": {"root"}})
	post(t, handler, url.Values{"channel": {"C1"}, "text": {"first"}, "thread_ts": {root}})
	latest := post(t, handler, url.Values{"channel": {"C1"}, "text": {"second"}, "thread_ts": {root}})
	messages := service.Messages{Store: repository}
	if err := messages.SetThreadFollowed(context.Background(), "T1", "U1", "C1", domain.MessageTimestamp(root), true); err != nil {
		t.Fatal(err)
	}
	parent := messagesOf(t, slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}}))[0]
	if parent["ts"] != root || parent["thread_ts"] != root || parent["reply_count"] != float64(2) ||
		parent["reply_users_count"] != float64(1) || parent["latest_reply"] != latest {
		t.Fatalf("parent = %v", parent)
	}
	if users, _ := parent["reply_users"].([]any); len(users) != 1 || users[0] != "U1" {
		t.Fatalf("reply_users = %v", parent["reply_users"])
	}
	if _, present := parent["subscribed"]; present {
		t.Fatalf("a bot token was told whether the member follows the thread: %v", parent)
	}
	thread := messagesOf(t, slackCall(t, userTokenHandler(t, repository), "user-token", "conversations.replies", url.Values{"channel": {"C1"}, "ts": {root}}))
	if len(thread) != 3 || thread[0]["thread_ts"] != root || thread[0]["subscribed"] != true || thread[1]["parent_user_id"] != "U1" {
		t.Fatalf("replies = %v", thread)
	}
}

func TestMessagesCarryReactionsAndPins(t *testing.T) {
	handler, repository := testHandlerWithStore()
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"react to me"}})
	messages := service.Messages{Store: repository}
	for _, reaction := range []struct {
		user domain.UserID
		name string
	}{{"U1", "eyes"}, {"U2", "eyes"}, {"U2", "tada"}} {
		if err := messages.AddReaction(context.Background(), "T1", reaction.user, "C1", domain.MessageTimestamp(ts), reaction.name); err != nil {
			t.Fatal(err)
		}
	}
	requireOK(t, "pins.add", slackCall(t, handler, "token", "pins.add", url.Values{"channel": {"C1"}, "timestamp": {ts}}))
	message := messagesOf(t, slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}}))[0]
	encoded, _ := json.Marshal(message["reactions"])
	if string(encoded) != `[{"count":2,"name":"eyes","users":["U1","U2"]},{"count":1,"name":"tada","users":["U2"]}]` {
		t.Fatalf("reactions = %s", encoded)
	}
	if pinned, _ := message["pinned_to"].([]any); len(pinned) != 1 || pinned[0] != "C1" {
		t.Fatalf("pinned_to = %v", message["pinned_to"])
	}
}

func TestReplyingToAReplyJoinsTheRootThread(t *testing.T) {
	handler, repository := testHandlerWithStore()
	root := post(t, handler, url.Values{"channel": {"C1"}, "text": {"root"}})
	reply := post(t, handler, url.Values{"channel": {"C1"}, "text": {"reply"}, "thread_ts": {root}})
	nested := slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"reply to the reply"}, "thread_ts": {reply}})
	requireOK(t, "chat.postMessage", nested)
	if nested["message"].(map[string]any)["thread_ts"] != root {
		t.Fatalf("a reply to a reply opened a nested thread: %v", nested["message"])
	}
	// conversations.replies named by any member of the thread answers all of it.
	for _, ts := range []string{root, reply} {
		thread := slackCall(t, handler, "token", "conversations.replies", url.Values{"channel": {"C1"}, "ts": {ts}})
		if got := timestampsOf(messagesOf(t, thread)); len(got) != 3 || got[0] != root {
			t.Fatalf("replies ts=%s = %v", ts, got)
		}
	}
	gone := post(t, handler, url.Values{"channel": {"C1"}, "text": {"about to go"}})
	if _, err := (service.Messages{Store: repository}).Delete(context.Background(), "T1", "U1", "C1", domain.MessageTimestamp(gone)); err != nil {
		t.Fatal(err)
	}
	requireError(t, "chat.postMessage", slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"late"}, "thread_ts": {gone}}), "thread_not_found")
	requireError(t, "chat.postMessage", slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"nowhere"}, "thread_ts": {"1000000000.000001"}}), "thread_not_found")
}

func TestChatUpdateKeepsWhatItWasNotGiven(t *testing.T) {
	handler := testHandler()
	blocks := `[{"type":"section","text":{"type":"mrkdwn","text":"rich"}}]`
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"fallback"}, "blocks": {blocks}})
	updated := slackCall(t, handler, "token", "chat.update", url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"new fallback"}})
	requireOK(t, "chat.update", updated)
	var want any
	if err := json.Unmarshal([]byte(blocks), &want); err != nil {
		t.Fatal(err)
	}
	wantEncoded, _ := json.Marshal(want)
	if encoded, _ := json.Marshal(updated["message"].(map[string]any)["blocks"]); string(encoded) != string(wantEncoded) {
		t.Fatalf("a text-only update changed the blocks to %s", encoded)
	}
	textOnly := post(t, handler, url.Values{"channel": {"C1"}, "text": {"only text"}})
	requireError(t, "chat.update", slackCall(t, handler, "token", "chat.update", url.Values{"channel": {"C1"}, "ts": {textOnly}, "text": {""}}), "no_text")
	requireError(t, "chat.update", slackCall(t, handler, "token", "chat.update", url.Values{"channel": {"C1"}, "ts": {textOnly}, "text": {""}, "attachments": {"[]"}}), "no_text")
	requireError(t, "chat.update", slackCall(t, handler, "token", "chat.update", url.Values{"channel": {"C1"}, "ts": {textOnly}}), "no_text")
}

func TestEditingSomebodyElsesMessageUsesSlacksCodes(t *testing.T) {
	handler, repository := testHandlerWithStore()
	theirs, err := (service.Messages{Store: repository}).Post(context.Background(), "T1", "U2", "C1", "not yours", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ts := string(domain.NewMessageTimestamp(theirs.CreatedAt))
	requireError(t, "chat.update", slackCall(t, handler, "token", "chat.update", url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"mine now"}}), "cant_update_message")
	requireError(t, "chat.delete", slackCall(t, handler, "token", "chat.delete", url.Values{"channel": {"C1"}, "ts": {ts}}), "cant_delete_message")
}

func TestBotTokenMessagesCarryBotIdentity(t *testing.T) {
	handler := testHandler()
	posted := slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"from the bot"}})
	requireOK(t, "chat.postMessage", posted)
	for _, message := range []map[string]any{
		posted["message"].(map[string]any),
		messagesOf(t, slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}}))[0],
	} {
		profile, _ := message["bot_profile"].(map[string]any)
		if message["bot_id"] != "B1" || message["app_id"] != "A1" || message["team"] != "T1" ||
			profile["id"] != "B1" || profile["app_id"] != "A1" || profile["name"] != "testbot" || profile["team_id"] != "T1" || profile["deleted"] != false {
			t.Fatalf("bot message = %v", message)
		}
		if _, ok := profile["icons"].(map[string]any); !ok {
			t.Fatalf("bot_profile has no icons: %v", profile)
		}
	}
}

func TestPostMessageResolvesNamesAndUsers(t *testing.T) {
	handler := testHandler()
	byName := slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"#general"}, "text": {"hash"}})
	requireOK(t, "chat.postMessage", byName)
	bare := slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"general"}, "text": {"bare"}})
	if byName["channel"] != "C1" || bare["channel"] != "C1" {
		t.Fatalf("channel names resolved to %v and %v, want C1", byName["channel"], bare["channel"])
	}
	direct := slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"U2"}, "text": {"hello bob"}})
	requireOK(t, "chat.postMessage", direct)
	again := slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"U2"}, "text": {"hello again"}})
	if direct["channel"] == "U2" || direct["channel"] != again["channel"] {
		t.Fatalf("a user ID did not post to one direct conversation: %v, %v", direct["channel"], again["channel"])
	}
	requireError(t, "chat.postMessage", slackCall(t, handler, "token", "chat.postMessage", url.Values{"channel": {"#no-such-channel"}, "text": {"x"}}), "channel_not_found")
	scheduled := slackCall(t, handler, "token", "chat.scheduleMessage", url.Values{"channel": {"#general"}, "text": {"later"}, "post_at": {strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}})
	if scheduled["ok"] != true || scheduled["channel"] != "C1" {
		t.Fatalf("chat.scheduleMessage by name = %v", scheduled)
	}
}

func TestChatWritePublicPostsWithoutJoining(t *testing.T) {
	without, repository := testHandlerWithStore()
	repository.SeedConversation(domain.Conversation{ID: "C3", WorkspaceID: "T1", Name: "announcements"})
	repository.SeedConversation(domain.Conversation{ID: "G3", WorkspaceID: "T1", Name: "secret", Kind: domain.ConversationTypePrivate})
	requireError(t, "chat.postMessage", slackCall(t, without, "token", "chat.postMessage", url.Values{"channel": {"C3"}, "text": {"hi"}}), "not_in_channel")

	with, repository := testHandlerWithScopes(append(defaultTestScopes(), auth.ScopeChatWritePublic)...)
	repository.SeedConversation(domain.Conversation{ID: "C3", WorkspaceID: "T1", Name: "announcements"})
	repository.SeedConversation(domain.Conversation{ID: "G3", WorkspaceID: "T1", Name: "secret", Kind: domain.ConversationTypePrivate})
	requireOK(t, "chat.postMessage", slackCall(t, with, "token", "chat.postMessage", url.Values{"channel": {"C3"}, "text": {"hi"}}))
	if body := slackCall(t, with, "token", "chat.postMessage", url.Values{"channel": {"G3"}, "text": {"hi"}}); body["ok"] != false {
		t.Fatalf("chat:write.public reached a private channel: %v", body)
	}
}

func TestPinsListCarriesTheMessage(t *testing.T) {
	handler := testHandler()
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"pin me"}})
	requireOK(t, "pins.add", slackCall(t, handler, "token", "pins.add", url.Values{"channel": {"C1"}, "timestamp": {ts}}))
	listed := slackCall(t, handler, "token", "pins.list", url.Values{"channel": {"C1"}})
	requireOK(t, "pins.list", listed)
	items, _ := listed["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("pins.list items = %v", listed["items"])
	}
	item := items[0].(map[string]any)
	message, _ := item["message"].(map[string]any)
	if item["type"] != "message" || item["channel"] != "C1" || item["created_by"] != "U1" || item["created"] == nil ||
		message["ts"] != ts || message["text"] != "pin me" ||
		message["permalink"] != contractOrigin+"/archives/C1/p"+strings.ReplaceAll(ts, ".", "") {
		t.Fatalf("pins.list item = %v", item)
	}
	if pinned, _ := message["pinned_to"].([]any); len(pinned) != 1 || pinned[0] != "C1" {
		t.Fatalf("pinned message has no pinned_to: %v", message)
	}
}

func TestURLsHandedToClientsAreAbsolute(t *testing.T) {
	handler, repository := testHandlerWithStore()
	identity := slackCall(t, handler, "token", "auth.test", url.Values{})
	if identity["url"] != contractOrigin+"/" {
		t.Fatalf("auth.test url = %v", identity["url"])
	}
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"link me"}})
	permalink := slackCall(t, handler, "token", "chat.getPermalink", url.Values{"channel": {"C1"}, "message_ts": {ts}})
	if permalink["permalink"] != contractOrigin+"/archives/C1/p"+strings.ReplaceAll(ts, ".", "") {
		t.Fatalf("chat.getPermalink = %v", permalink)
	}
	reply := post(t, handler, url.Values{"channel": {"C1"}, "text": {"in thread"}, "thread_ts": {ts}})
	threaded := slackCall(t, handler, "token", "chat.getPermalink", url.Values{"channel": {"C1"}, "message_ts": {reply}})
	if link, _ := threaded["permalink"].(string); !strings.HasPrefix(link, contractOrigin+"/archives/C1/p") || !strings.Contains(link, "thread_ts="+ts) {
		t.Fatalf("a reply's permalink does not name its thread: %v", threaded)
	}
	search := slackCall(t, userTokenHandler(t, repository), "user-token", "search.messages", url.Values{"query": {"link"}})
	matches, _ := search["messages"].(map[string]any)["matches"].([]any)
	if len(matches) == 0 || !strings.HasPrefix(matches[0].(map[string]any)["permalink"].(string), contractOrigin+"/archives/") {
		t.Fatalf("search.messages permalink = %v", search["messages"])
	}
	info := slackCall(t, handler, "token", "files.info", url.Values{"file": {"F1"}})
	if file, _ := info["file"].(map[string]any); !strings.HasPrefix(file["url_private"].(string), contractOrigin+"/api/files/") {
		t.Fatalf("files.info url_private = %v", info["file"])
	}
}

func TestConfiguredPublicURLWinsOverTheRequestHost(t *testing.T) {
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", TokenType: "user", Scopes: map[auth.Scope]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: repository}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	// Configured the way slack.Mount configures it from -auth-public-url.
	if err := handler.SetPublicURL("https://public.example.test/"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	if identity := slackCall(t, mux, "token", "auth.test", url.Values{}); identity["url"] != "https://public.example.test/" {
		t.Fatalf("auth.test url = %v", identity["url"])
	}
}

func TestReactionsGetAndListReturnWholeItems(t *testing.T) {
	handler, repository := testHandlerWithStore()
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"react"}})
	for _, name := range []string{"eyes", "tada"} {
		requireOK(t, "reactions.add", slackCall(t, handler, "token", "reactions.add", url.Values{"channel": {"C1"}, "timestamp": {ts}, "name": {name}}))
	}
	if err := (service.Messages{Store: repository}).AddReaction(context.Background(), "T1", "U2", "C1", domain.MessageTimestamp(ts), "eyes"); err != nil {
		t.Fatal(err)
	}
	got := slackCall(t, handler, "token", "reactions.get", url.Values{"channel": {"C1"}, "timestamp": {ts}})
	requireOK(t, "reactions.get", got)
	message, _ := got["message"].(map[string]any)
	if got["type"] != "message" || got["channel"] != "C1" || message["ts"] != ts || message["text"] != "react" || message["user"] != "U1" || message["permalink"] == nil {
		t.Fatalf("reactions.get = %v", got)
	}
	if encoded, _ := json.Marshal(message["reactions"]); string(encoded) != `[{"count":2,"name":"eyes","users":["U1","U2"]},{"count":1,"name":"tada","users":["U1"]}]` {
		t.Fatalf("reactions.get reactions = %s", encoded)
	}
	listed := slackCall(t, handler, "token", "reactions.list", url.Values{})
	requireOK(t, "reactions.list", listed)
	items, _ := listed["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("reactions.list gave one item per reaction instead of per message: %v", listed["items"])
	}
	item := items[0].(map[string]any)
	reactions, _ := item["message"].(map[string]any)["reactions"].([]any)
	if item["type"] != "message" || item["channel"] != "C1" || len(reactions) != 2 || reactions[0].(map[string]any)["count"] != float64(2) {
		t.Fatalf("reactions.list item = %v", item)
	}
}

func TestPostEphemeralContract(t *testing.T) {
	handler, repository := testHandlerWithStore()
	repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"})
	requireError(t, "chat.postEphemeral", slackCall(t, handler, "token", "chat.postEphemeral", url.Values{"channel": {"C1"}, "user": {"U3"}, "text": {"psst"}}), "user_not_in_channel")
	requireError(t, "chat.postEphemeral", slackCall(t, handler, "token", "chat.postEphemeral", url.Values{"channel": {"C1"}, "user": {"U2"}}), "no_text")
	root := post(t, handler, url.Values{"channel": {"C1"}, "text": {"root"}})
	threaded := slackCall(t, handler, "token", "chat.postEphemeral", url.Values{"channel": {"C1"}, "user": {"U2"}, "text": {"in thread"}, "thread_ts": {root}})
	requireOK(t, "chat.postEphemeral", threaded)
	stored, err := repository.ListEphemeralMessages(context.Background(), "T1", "U2", "C1", 10)
	if err != nil || len(stored) != 1 || stored[0].ThreadTimestamp != domain.MessageTimestamp(root) {
		t.Fatalf("ephemeral thread_ts was not kept: %+v err=%v", stored, err)
	}
}

func TestAsUserIsAcceptedFromAUserToken(t *testing.T) {
	_, repository := testHandlerWithStore()
	body := slackCall(t, userTokenHandler(t, repository), "user-token", "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"as me"}, "as_user": {"true"}})
	requireOK(t, "chat.postMessage", body)
	if message := body["message"].(map[string]any); message["user"] != "U1" || message["bot_id"] != nil {
		t.Fatalf("as_user post = %v", message)
	}
}

func TestTimestampArgumentsUseTheirOperationsCodes(t *testing.T) {
	handler := testHandler()
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"stamp"}})
	whole := strings.Split(ts, ".")[0]
	requireError(t, "reactions.add", slackCall(t, handler, "token", "reactions.add", url.Values{"channel": {"C1"}, "timestamp": {whole}, "name": {"eyes"}}), "bad_timestamp")
	requireError(t, "conversations.mark", slackCall(t, handler, "token", "conversations.mark", url.Values{"channel": {"C1"}, "ts": {"bogus"}}), "invalid_timestamp")
	requireOK(t, "conversations.mark", slackCall(t, handler, "token", "conversations.mark", url.Values{"channel": {"C1"}, "ts": {ts}}))
}

func TestLimitZeroIsTheDefault(t *testing.T) {
	handler := testHandler()
	post(t, handler, url.Values{"channel": {"C1"}, "text": {"counted"}})
	for _, call := range []struct {
		method string
		form   url.Values
	}{
		{"users.list", url.Values{"limit": {"0"}}},
		{"conversations.list", url.Values{"limit": {"0"}}},
		{"conversations.history", url.Values{"channel": {"C1"}, "limit": {"0"}}},
		{"conversations.members", url.Values{"channel": {"C1"}, "limit": {"0"}}},
	} {
		requireOK(t, call.method+" limit=0", slackCall(t, handler, "token", call.method, call.form))
	}
}
