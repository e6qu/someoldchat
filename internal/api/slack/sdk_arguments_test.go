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
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// These tests cover arguments the pinned SDKs send (python slack_sdk 3.45.0,
// the Java client 1.52.0's RequestFormBuilder, @slack/web-api 8.2.0) that the
// handlers used to drop without a word.

func storedMessage(t *testing.T, repository *memory.Store, channel domain.ConversationID, ts string) domain.Message {
	t.Helper()
	createdAt, err := domain.ParseMessageTimestamp(domain.MessageTimestamp(ts))
	if err != nil {
		t.Fatal(err)
	}
	message, err := repository.GetMessageByCreatedAt(context.Background(), channel, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func streamStateOf(t *testing.T, raw string) domain.MessageStreamState {
	t.Helper()
	var state domain.MessageStreamState
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func TestChatUpdateTakesEverySDKArgument(t *testing.T) {
	handler, repository := testHandlerWithStore()
	update := func(form url.Values) map[string]any {
		return slackCall(t, handler, "token", "chat.update", form)
	}
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"plain"}})

	// markdown_text alone is an edit, not a missing text.
	edited := update(url.Values{"channel": {"C1"}, "ts": {ts}, "markdown_text": {"**bold**"}})
	requireOK(t, "chat.update markdown_text", edited)
	if message := storedMessage(t, repository, "C1", ts); message.Text != "**bold**" || !streamStateOf(t, message.StreamState).MarkdownText {
		t.Fatalf("markdown edit stored %+v", message)
	}
	requireError(t, "chat.update markdown_text with text", update(url.Values{"channel": {"C1"}, "ts": {ts}, "markdown_text": {"x"}, "text": {"y"}}), "markdown_text_conflict")
	// A text edit replaces the markdown presentation with markup.
	requireOK(t, "chat.update text", update(url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"markup again"}}))
	if state := streamStateOf(t, storedMessage(t, repository, "C1", ts).StreamState); state.MarkdownText {
		t.Fatalf("a text edit kept markdown presentation: %+v", state)
	}

	// parse describes the edit, and only none or full are parse values.
	requireError(t, "chat.update parse", update(url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"t"}, "parse": {"sometimes"}}), "invalid_arg_name")
	requireOK(t, "chat.update parse=full", update(url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"<raw>"}, "parse": {"full"}}))
	if state := streamStateOf(t, storedMessage(t, repository, "C1", ts).StreamState); state.Parse != "full" {
		t.Fatalf("parse=full not recorded: %+v", state)
	}
	requireOK(t, "chat.update without parse", update(url.Values{"channel": {"C1"}, "ts": {ts}, "text": {"default"}}))
	if state := streamStateOf(t, storedMessage(t, repository, "C1", ts).StreamState); state.Parse != "" {
		t.Fatalf("an edit without parse kept parse=%q", state.Parse)
	}

	// metadata replaces the message's metadata, and an edit carrying only
	// metadata is an edit.
	withMetadata := update(url.Values{"channel": {"C1"}, "ts": {ts}, "metadata": {`{"event_type":"task_updated","event_payload":{"id":"T7"}}`}})
	requireOK(t, "chat.update metadata", withMetadata)
	metadata, _ := withMetadata["message"].(map[string]any)["metadata"].(map[string]any)
	if metadata["event_type"] != "task_updated" {
		t.Fatalf("updated metadata = %v", withMetadata["message"])
	}
	if refused := update(url.Values{"channel": {"C1"}, "ts": {ts}, "metadata": {`{"event_payload":{}}`}}); refused["ok"] != false {
		t.Fatalf("metadata without an event_type = %v", refused)
	}

	// reply_broadcast sends an existing reply to its channel.
	reply := post(t, handler, url.Values{"channel": {"C1"}, "text": {"in the thread"}, "thread_ts": {ts}})
	historyHas := func(want string) bool {
		for _, message := range messagesOf(t, slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}})) {
			if message["ts"] == want {
				return message["subtype"] == "thread_broadcast"
			}
		}
		return false
	}
	if historyHas(reply) {
		t.Fatal("a plain reply is in channel history")
	}
	requireOK(t, "chat.update reply_broadcast", update(url.Values{"channel": {"C1"}, "ts": {reply}, "reply_broadcast": {"true"}}))
	if !historyHas(reply) {
		t.Fatal("reply_broadcast did not send the reply to its channel")
	}
	requireError(t, "chat.update reply_broadcast", update(url.Values{"channel": {"C1"}, "ts": {reply}, "text": {"x"}, "reply_broadcast": {"perhaps"}}), "invalid_arg_name")

	// file_ids replaces the files the message carries, sharing what it adds
	// and ending the share of what it drops.
	now := time.Now().UTC()
	if err := repository.CreateFile(context.Background(), domain.File{ID: "F2", WorkspaceID: "T1", Uploader: "U1", Name: "second.txt", MIMEType: "text/plain", BlobKey: "blob-2", CreatedAt: now}, events.Event{ID: "EF2", WorkspaceID: "T1", Topic: "file.created", Payload: "F2", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateFile(context.Background(), domain.File{ID: "F3", WorkspaceID: "T1", Uploader: "U2", Name: "theirs.txt", MIMEType: "text/plain", BlobKey: "blob-3", CreatedAt: now}, events.Event{ID: "EF3", WorkspaceID: "T1", Topic: "file.created", Payload: "F3", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sharedIn := func(id domain.FileID) []domain.ConversationID {
		file, err := repository.GetFile(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return file.SharedChannels
	}
	withFile := update(url.Values{"channel": {"C1"}, "ts": {ts}, "file_ids": {"F1"}})
	requireOK(t, "chat.update file_ids", withFile)
	files, _ := withFile["message"].(map[string]any)["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["id"] != "F1" || len(sharedIn("F1")) != 1 {
		t.Fatalf("file_ids=F1 gave files %v shared in %v", files, sharedIn("F1"))
	}
	requireOK(t, "chat.update file_ids replaced", update(url.Values{"channel": {"C1"}, "ts": {ts}, "file_ids": {`["F2"]`}}))
	if carried := storedMessage(t, repository, "C1", ts).Files; len(carried) != 1 || carried[0].ID != "F2" || len(sharedIn("F1")) != 0 || len(sharedIn("F2")) != 1 {
		t.Fatalf("file_ids=F2 left files %+v, F1 shared in %v, F2 in %v", carried, sharedIn("F1"), sharedIn("F2"))
	}
	// Somebody else's file, or no file, is not the caller's to share.
	requireError(t, "chat.update file_ids of another's file", update(url.Values{"channel": {"C1"}, "ts": {ts}, "file_ids": {"F3"}}), "invalid_arg_name")
	requireError(t, "chat.update file_ids of no file", update(url.Values{"channel": {"C1"}, "ts": {ts}, "file_ids": {"FNOPE"}}), "invalid_arg_name")
	requireError(t, "chat.update file_ids with a hole", update(url.Values{"channel": {"C1"}, "ts": {ts}, "file_ids": {"F2,,F1"}}), "invalid_arg_name")
}

func TestChatPostEphemeralTakesEverySDKArgument(t *testing.T) {
	ephemeralFor := func(t *testing.T, repository *memory.Store) domain.EphemeralMessage {
		t.Helper()
		values, err := repository.ListEphemeralMessages(context.Background(), "T1", "U2", "C1", 10)
		if err != nil || len(values) == 0 {
			t.Fatalf("ephemeral messages=%v err=%v", values, err)
		}
		return values[len(values)-1]
	}
	handler, repository := testHandlerWithStore()
	base := url.Values{"channel": {"C1"}, "user": {"U2"}}
	with := func(pairs ...string) url.Values {
		form := url.Values{}
		for key, values := range base {
			form[key] = values
		}
		for index := 0; index+1 < len(pairs); index += 2 {
			form.Set(pairs[index], pairs[index+1])
		}
		return form
	}
	// A markdown-only ephemeral message used to be refused no_text.
	requireOK(t, "chat.postEphemeral markdown_text", slackCall(t, handler, "token", "chat.postEphemeral", with("markdown_text", "**only for you**")))
	if value := ephemeralFor(t, repository); value.Text != "**only for you**" || !streamStateOf(t, value.StreamState).MarkdownText {
		t.Fatalf("markdown ephemeral = %+v", value)
	}
	requireError(t, "chat.postEphemeral markdown_text with text", slackCall(t, handler, "token", "chat.postEphemeral", with("markdown_text", "a", "text", "b")), "markdown_text_conflict")
	requireError(t, "chat.postEphemeral parse", slackCall(t, handler, "token", "chat.postEphemeral", with("text", "a", "parse", "loose")), "invalid_arg_name")
	// A custom identity takes chat:write.customize, as on chat.postMessage.
	requireError(t, "chat.postEphemeral username without customize", slackCall(t, handler, "token", "chat.postEphemeral", with("text", "a", "username", "Deploy bot")), "missing_scope")

	customized, customizedRepository := testHandlerWithScopes(append(defaultTestScopes(), auth.ScopeChatWriteCustomize)...)
	requireOK(t, "chat.postEphemeral custom identity", slackCall(t, customized, "token", "chat.postEphemeral", with("text", "deployed", "username", "Deploy bot", "icon_emoji", ":rocket:", "icon_url", "https://example.com/icon.png", "parse", "full")))
	state := streamStateOf(t, ephemeralFor(t, customizedRepository).StreamState)
	if state.Username != "Deploy bot" || state.IconEmoji != ":rocket:" || state.IconURL != "" || state.Parse != "full" {
		t.Fatalf("custom identity stored %+v", state)
	}
	requireError(t, "chat.postEphemeral bad icon", slackCall(t, customized, "token", "chat.postEphemeral", with("text", "a", "icon_emoji", "rocket")), "invalid_arg_name")
}

func TestChatUnfurlTakesWorkObjectMetadata(t *testing.T) {
	handler := testHandler()
	link := "https://tasks.example/task/7"
	ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"see " + link}})
	entity := func(appUnfurlURL string) string {
		return `{"entity_type":"slack#/entities/task","url":"` + link + `","app_unfurl_url":"` + appUnfurlURL + `","external_ref":{"id":"7"},"entity_payload":{"attributes":{"title":{"text":"Ship it"}}}}`
	}
	unfurl := func(form url.Values) map[string]any {
		form.Set("channel", "C1")
		form.Set("ts", ts)
		return slackCall(t, handler, "token", "chat.unfurl", form)
	}
	requireError(t, "chat.unfurl without unfurls or metadata", unfurl(url.Values{}), "missing_unfurls")
	requireError(t, "chat.unfurl incomplete entity", unfurl(url.Values{"metadata": {`{"entities":[{"entity_type":"slack#/entities/task","app_unfurl_url":"` + link + `"}]}`}}), "invalid_arg_name")
	requireError(t, "chat.unfurl entity for a link not in the message", unfurl(url.Values{"metadata": {`{"entities":[` + entity("https://tasks.example/other") + `]}`}}), "cannot_unfurl_url")
	requireError(t, "chat.unfurl unfurls and an entity for one link", unfurl(url.Values{"unfurls": {`{"` + link + `":{"title":"T"}}`}, "metadata": {`{"entities":[` + entity(link) + `]}`}}), "invalid_arg_name")
	requireOK(t, "chat.unfurl metadata only", unfurl(url.Values{"metadata": {`{"entities":[` + entity(link) + `]}`}}))
	message := messagesOf(t, slackCall(t, handler, "token", "conversations.history", url.Values{"channel": {"C1"}, "limit": {"1"}}))[0]
	attachments, _ := message["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("unfurled message = %v", message)
	}
	preview := attachments[0].(map[string]any)
	if preview["is_app_unfurl"] != true || preview["app_unfurl_url"] != link || preview["entity_type"] != "slack#/entities/task" {
		t.Fatalf("work object unfurl = %v", preview)
	}
}

func TestUploadURLExternalCarriesAltTextAndSnippetType(t *testing.T) {
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "U1")
	objects, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeFilesRead: {}, auth.ScopeFilesWrite: {}, auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := NewHandler(service.Messages{Store: repository, Blob: objects}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	value.Register(mux)
	upload := func(form url.Values, content string) map[string]any {
		t.Helper()
		form.Set("length", strconv.Itoa(len(content)))
		ticket := slackCall(t, mux, "token", "files.getUploadURLExternal", form)
		requireOK(t, "files.getUploadURLExternal", ticket)
		request := httptest.NewRequest(http.MethodPost, ticket["upload_url"].(string), strings.NewReader(content))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("upload status=%d body=%s", response.Code, response.Body)
		}
		fileID := ticket["file_id"].(string)
		requireOK(t, "files.completeUploadExternal", slackCall(t, mux, "token", "files.completeUploadExternal", url.Values{"files": {`[{"id":"` + fileID + `"}]`}, "channel_id": {"C1"}}))
		info := slackCall(t, mux, "token", "files.info", url.Values{"file": {fileID}})
		requireOK(t, "files.info", info)
		return info["file"].(map[string]any)
	}
	// python slack_sdk and the Java client send alt_txt.
	image := upload(url.Values{"filename": {"chart.png"}, "alt_txt": {"Sales rose in May"}}, "\x89PNG\r\n\x1a\n")
	if image["alt_txt"] != "Sales rose in May" {
		t.Fatalf("alt_txt file = %v", image)
	}
	// @slack/web-api 8.2.0 sends the same value as alt_text.
	if node := upload(url.Values{"filename": {"chart.png"}, "alt_text": {"Costs fell"}}, "\x89PNG\r\n\x1a\n"); node["alt_txt"] != "Costs fell" {
		t.Fatalf("alt_text file = %v", node)
	}
	// snippet_type uploads the bytes as a snippet in that syntax.
	snippet := upload(url.Values{"filename": {"main.py"}, "snippet_type": {"python"}}, "print('hi')\n")
	if snippet["filetype"] != "python" || snippet["editable"] != true {
		t.Fatalf("snippet file = %v", snippet)
	}
	if plain := upload(url.Values{"filename": {"notes.txt"}}, "notes"); plain["editable"] != false || plain["alt_txt"] != nil {
		t.Fatalf("plain file = %v", plain)
	}
	requireError(t, "files.getUploadURLExternal long alt_txt", slackCall(t, mux, "token", "files.getUploadURLExternal", url.Values{"filename": {"a.png"}, "length": {"4"}, "alt_txt": {strings.Repeat("x", service.FileDescriptionLimit+1)}}), "invalid_arg_name")
}

func TestRemoteFilesListFiltersByChannelAndTime(t *testing.T) {
	handler := testHandler()
	requireOK(t, "files.remote.add", slackCall(t, handler, "token", "files.remote.add", url.Values{"external_id": {"shared"}, "title": {"Shared"}, "external_url": {"https://files.example/shared"}}))
	requireOK(t, "files.remote.add", slackCall(t, handler, "token", "files.remote.add", url.Values{"external_id": {"kept"}, "title": {"Kept"}, "external_url": {"https://files.example/kept"}}))
	requireOK(t, "files.remote.share", slackCall(t, handler, "token", "files.remote.share", url.Values{"external_id": {"shared"}, "channels": {"C1"}}))
	list := func(form url.Values) []any {
		t.Helper()
		body := slackCall(t, handler, "token", "files.remote.list", form)
		requireOK(t, "files.remote.list", body)
		files, _ := body["files"].([]any)
		return files
	}
	if files := list(url.Values{}); len(files) != 2 {
		t.Fatalf("unfiltered = %v", files)
	}
	if files := list(url.Values{"channel": {"C1"}}); len(files) != 1 || files[0].(map[string]any)["external_id"] != "shared" {
		t.Fatalf("channel=C1 = %v", files)
	}
	if files := list(url.Values{"channel": {"C2"}}); len(files) != 0 {
		t.Fatalf("channel=C2 = %v", files)
	}
	now := time.Now().Unix()
	if files := list(url.Values{"ts_from": {strconv.FormatInt(now-60, 10)}, "ts_to": {strconv.FormatInt(now+60, 10)}}); len(files) != 2 {
		t.Fatalf("window around now = %v", files)
	}
	if files := list(url.Values{"ts_from": {strconv.FormatInt(now+3600, 10)}}); len(files) != 0 {
		t.Fatalf("ts_from in the future = %v", files)
	}
	if files := list(url.Values{"ts_to": {strconv.FormatInt(now-3600, 10)}}); len(files) != 0 {
		t.Fatalf("ts_to in the past = %v", files)
	}
	requireError(t, "files.remote.list ts_from", slackCall(t, handler, "token", "files.remote.list", url.Values{"ts_from": {"yesterday"}}), "invalid_arg_name")
}

func TestConversationReadsReturnOtherAppsMetadataOnlyOnRequest(t *testing.T) {
	handler, repository := testHandlerWithStore()
	root := post(t, handler, url.Values{"channel": {"C1"}, "text": {"ours"}, "metadata": {`{"event_type":"ours","event_payload":{}}`}})
	for _, request := range []domain.MessagePostRequest{
		{Conversation: "C1", Text: "theirs", AppID: "A2", Metadata: `{"event_type":"theirs","event_payload":{}}`},
		{Conversation: "C1", Text: "theirs in the thread", AppID: "A2", Metadata: `{"event_type":"theirs","event_payload":{}}`, ThreadTimestamp: domain.MessageTimestamp(root)},
	} {
		if _, err := (service.Messages{Store: repository}).PostMessageAs(context.Background(), "T1", "U1", request); err != nil {
			t.Fatal(err)
		}
	}
	metadataTypes := func(method string, form url.Values) map[string]string {
		t.Helper()
		form.Set("channel", "C1")
		body := slackCall(t, handler, "token", method, form)
		requireOK(t, method, body)
		result := make(map[string]string)
		for _, message := range messagesOf(t, body) {
			metadata, _ := message["metadata"].(map[string]any)
			eventType, _ := metadata["event_type"].(string)
			result[message["text"].(string)] = eventType
		}
		return result
	}
	for _, read := range []struct {
		method string
		form   url.Values
		theirs string
	}{
		{"conversations.history", url.Values{}, "theirs"},
		{"conversations.replies", url.Values{"ts": {root}}, "theirs in the thread"},
	} {
		got := metadataTypes(read.method, read.form)
		if got["ours"] != "ours" || got[read.theirs] != "" {
			t.Fatalf("%s without include_all_metadata = %v", read.method, got)
		}
		all := url.Values{"include_all_metadata": {"true"}}
		for key, values := range read.form {
			all[key] = values
		}
		if got := metadataTypes(read.method, all); got["ours"] != "ours" || got[read.theirs] != "theirs" {
			t.Fatalf("%s with include_all_metadata = %v", read.method, got)
		}
		bad := url.Values{"channel": {"C1"}, "include_all_metadata": {"all"}}
		for key, values := range read.form {
			bad[key] = values
		}
		requireError(t, read.method, slackCall(t, handler, "token", read.method, bad), "invalid_arg_name")
	}
}
