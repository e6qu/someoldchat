package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
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
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// slackEnvelope is the shape every Slack Web API failure must have: HTTP 200 with
// a top-level `ok` boolean and, when ok is false, an `error` naming the failure.
type slackEnvelope struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
	Needed   string `json:"needed"`
	Provided string `json:"provided"`
}

func decodeEnvelope(t *testing.T, response *httptest.ResponseRecorder) slackEnvelope {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 with a Slack envelope", response.Code, response.Body)
	}
	body := response.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("response body is empty; a client cannot tell success from failure")
	}
	var envelope slackEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return envelope
}

func callAPI(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func callJSON(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// A handled failure is never signalled with a non-200 status. The Slack Web API
// reports every handled error as HTTP 200 plus `{"ok":false,"error":…}`, and an
// SDK keys its retry and rate-limit logic off the status code: a 503 makes it
// retry a request that can never succeed. Each row below returned a 4xx or 5xx
// before this change.
func TestHandledFailuresAreHTTP200WithAPinnedErrorCode(t *testing.T) {
	handler, _ := testHandlerWithStore()
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   string
	}{
		{"missing channel", http.MethodGet, "/api/conversations.history", "", "channel_not_found"},
		{"unknown channel", http.MethodGet, "/api/conversations.history?channel=CZZZ", "", "channel_not_found"},
		{"missing thread", http.MethodGet, "/api/conversations.replies?channel=C1", "", "thread_not_found"},
		{"unknown user", http.MethodGet, "/api/users.info?user=UZZZ", "", "user_not_found"},
		{"unknown file", http.MethodGet, "/api/files.info?file=FZZZ", "", "file_not_found"},
		{"missing reminder", http.MethodPost, "/api/reminders.info", "", "not_found"},
		{"unknown reminder", http.MethodPost, "/api/reminders.info", "reminder=Rnope", "not_found"},
		{"missing reaction item", http.MethodPost, "/api/reactions.add", "name=tada", "no_item_specified"},
		{"missing pin item", http.MethodPost, "/api/pins.add", "", "no_item_specified"},
		{"missing star item", http.MethodPost, "/api/stars.add", "", "no_item_specified"},
		{"missing emoji name", http.MethodPost, "/api/reactions.add", "channel=C1&timestamp=1700000000.000000", "invalid_name"},
		{"malformed timestamp", http.MethodPost, "/api/reactions.add", "channel=C1&timestamp=not-a-ts&name=tada", "bad_timestamp"},
		{"missing channel to join", http.MethodPost, "/api/conversations.join", "", "channel_not_found"},
		{"missing channel to leave", http.MethodPost, "/api/conversations.leave", "", "channel_not_found"},
		{"missing name to create", http.MethodPost, "/api/conversations.create", "", "invalid_name_required"},
		{"missing users to open", http.MethodPost, "/api/conversations.open", "", "users_list_not_supplied"},
		{"missing usergroup", http.MethodGet, "/api/usergroups.users.list", "", "invalid_arg_name"},
		{"missing call id", http.MethodPost, "/api/calls.update", "title=x", "invalid_arg_name"},
	}
	for _, testCase := range cases {
		response := callAPI(t, handler, testCase.method, testCase.path, testCase.body)
		envelope := decodeEnvelope(t, response)
		if envelope.OK || envelope.Error != testCase.want {
			t.Errorf("%s: body=%+v, want ok=false error=%q", testCase.name, envelope, testCase.want)
		}
	}
}

// Nineteen decode-failure paths returned HTTP 200 with a zero-byte body, so a
// client could not distinguish success from failure. Every one must now name the
// cause with a code from the pinned enums.
func TestDecodeFailuresNameTheirCauseInsteadOfReturningAnEmptyBody(t *testing.T) {
	handler, _ := testHandlerWithStore()
	cases := []struct {
		name string
		path string
		body string
		want string
	}{
		{"views.open array body", "/api/views.open", `[1,2]`, "json_not_object"},
		{"views.publish array body", "/api/views.publish", `[1,2]`, "json_not_object"},
		{"views.push array body", "/api/views.push", `[1,2]`, "json_not_object"},
		{"views.update array body", "/api/views.update", `[1,2]`, "json_not_object"},
		{"oauth.access array body", "/api/oauth.access", `[1,2]`, "json_not_object"},
		{"oauth.v2.access array body", "/api/oauth.v2.access", `[1,2]`, "json_not_object"},
		{"openid token array body", "/api/openid.connect.token", `[1,2]`, "json_not_object"},
		{"openid userinfo array body", "/api/openid.connect.userInfo", `[1,2]`, "json_not_object"},
		{"workflows.stepCompleted array body", "/api/workflows.stepCompleted", `[1,2]`, "json_not_object"},
		{"workflows.stepFailed array body", "/api/workflows.stepFailed", `[1,2]`, "json_not_object"},
		{"workflows.updateStep array body", "/api/workflows.updateStep", `[1,2]`, "json_not_object"},
		{"dialog.open array body", "/api/dialog.open", `[1,2]`, "json_not_object"},
		{"bots.info array body", "/api/bots.info", `[1,2]`, "json_not_object"},
		{"migration.exchange array body", "/api/migration.exchange", `[1,2]`, "json_not_object"},
		{"rtm.connect array body", "/api/rtm.connect", `[1,2]`, "json_not_object"},
		{"team.integrationLogs array body", "/api/team.integrationLogs", `[1,2]`, "json_not_object"},
		{"apps.permissions.request array body", "/api/apps.permissions.request", `[1,2]`, "json_not_object"},
		{"apps.permissions.users.request array body", "/api/apps.permissions.users.request", `[1,2]`, "json_not_object"},
		{"ekm list array body", "/api/admin.conversations.ekm.listOriginalConnectedChannelInfo", `[1,2]`, "json_not_object"},
		{"truncated json", "/api/views.open", `{"view":`, "invalid_json"},
		{"trailing value", "/api/views.open", `{} {}`, "invalid_json"},
		{"duplicate field", "/api/views.open", `{"a":1,"a":2}`, "invalid_json"},
	}
	for _, testCase := range cases {
		response := callJSON(t, handler, testCase.path, testCase.body)
		envelope := decodeEnvelope(t, response)
		if envelope.OK || envelope.Error != testCase.want {
			t.Errorf("%s: body=%+v, want ok=false error=%q", testCase.name, envelope, testCase.want)
		}
	}
}

// A POST that carries a payload with no Content-Type used to decode as no
// parameters at all, because Go's parsePostForm treats an unknown type as
// application/octet-stream and reads nothing without error.
func TestPostPayloadWithoutAUsableContentTypeIsNamed(t *testing.T) {
	handler, _ := testHandlerWithStore()
	cases := []struct {
		name        string
		contentType string
		want        string
	}{
		{"absent", "", "missing_post_type"},
		{"unsupported", "text/plain", "invalid_post_type"},
		{"bad charset", "application/x-www-form-urlencoded; charset=utf-16", "invalid_charset"},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=hi"))
		if testCase.contentType != "" {
			request.Header.Set("Content-Type", testCase.contentType)
		}
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		envelope := decodeEnvelope(t, response)
		if envelope.OK || envelope.Error != testCase.want {
			t.Errorf("%s content type: body=%+v, want %q", testCase.name, envelope, testCase.want)
		}
	}
	// A POST with no payload at all is legitimate: the bearer header is enough.
	response := callAPI(t, handler, http.MethodPost, "/api/auth.test", "")
	if envelope := decodeEnvelope(t, response); !envelope.OK {
		t.Fatalf("empty POST body: body=%+v, want ok=true", envelope)
	}
}

// The JSON body was consumed by the first decodeFields; a second call saw EOF and
// returned an empty map with no error, so `limit` and `cursor` were dropped and
// the response looked successful.
func TestJSONPaginationArgumentsSurviveTheSecondDecode(t *testing.T) {
	handler, store := testHandlerWithStore()
	for index := 0; index < 5; index++ {
		store.SeedUser(domain.User{ID: domain.UserID("UP" + strconv.Itoa(index)), WorkspaceID: "T1", Name: "user" + strconv.Itoa(index)})
	}
	for _, path := range []string{"/api/admin.users.list", "/api/users.list"} {
		response := callJSON(t, handler, path, `{"team_id":"T1","limit":2}`)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body)
		}
		var body struct {
			OK      bool             `json:"ok"`
			Users   []map[string]any `json:"users"`
			Members []map[string]any `json:"members"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		count := len(body.Users) + len(body.Members)
		if !body.OK || count != 2 {
			t.Errorf("%s returned %d rows for limit=2 (ok=%v)", path, count, body.OK)
		}
	}
}

// pageRequest returned a nil error for an out-of-range limit, so Limit: 0 reached
// the store, which answered with a bare errors.New that became
// `503 service_unavailable` — a handled validation failure reported as a
// dependency outage.
func TestOutOfRangeLimitIsAHandledArgumentErrorNotAStoreFailure(t *testing.T) {
	handler, _ := testHandlerWithStore()
	created := callAPI(t, handler, http.MethodPost, "/api/slackLists.create", "name=limits")
	var list struct {
		OK   bool `json:"ok"`
		List struct {
			ID string `json:"id"`
		} `json:"list"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &list); err != nil || !list.OK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	response := callAPI(t, handler, http.MethodPost, "/api/slackLists.items.list", "list_id="+url.QueryEscape(list.List.ID)+"&limit=0")
	envelope := decodeEnvelope(t, response)
	if envelope.OK || envelope.Error != "invalid_arg_name" {
		t.Fatalf("limit=0: body=%+v, want ok=false error=invalid_arg_name", envelope)
	}
	// An oversized limit is clamped, which is what Slack does, rather than rejected.
	clamped := callAPI(t, handler, http.MethodPost, "/api/slackLists.items.list", "list_id="+url.QueryEscape(list.List.ID)+"&limit=5000")
	if envelope := decodeEnvelope(t, clamped); !envelope.OK {
		t.Fatalf("limit=5000: body=%+v, want ok=true (clamped)", envelope)
	}
}

// conversations.history and .replies dropped `latest`, `oldest` and `inclusive`,
// so a range-limited request returned the channel's whole recent history with
// `"ok":true` — strictly more data than the caller asked for.
func TestHistoryHonoursTheRequestedTimeWindow(t *testing.T) {
	handler, _ := testHandlerWithStore()
	timestamps := make([]string, 0, 3)
	for _, text := range []string{"first", "second", "third"} {
		response := callAPI(t, handler, http.MethodPost, "/api/chat.postMessage", "channel=C1&text="+text)
		var posted struct {
			OK bool   `json:"ok"`
			TS string `json:"ts"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &posted); err != nil || !posted.OK {
			t.Fatalf("post %s status=%d body=%s", text, response.Code, response.Body)
		}
		timestamps = append(timestamps, posted.TS)
		time.Sleep(time.Millisecond)
	}
	read := func(query string) []string {
		response := callAPI(t, handler, http.MethodGet, "/api/conversations.history?channel=C1&"+query, "")
		var body struct {
			OK       bool `json:"ok"`
			Messages []struct {
				TS string `json:"ts"`
			} `json:"messages"`
		}
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", query, response.Code, response.Body)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.OK {
			t.Fatalf("%s body=%s", query, response.Body)
		}
		result := make([]string, 0, len(body.Messages))
		for _, message := range body.Messages {
			result = append(result, message.TS)
		}
		return result
	}
	if all := read("limit=100"); len(all) != 3 {
		t.Fatalf("unfiltered history returned %v, want 3 messages", all)
	}
	window := read("oldest=" + timestamps[1] + "&latest=" + timestamps[1] + "&inclusive=true")
	if len(window) != 1 || window[0] != timestamps[1] {
		t.Errorf("inclusive single-message window returned %v, want [%s]", window, timestamps[1])
	}
	if exclusive := read("oldest=" + timestamps[1] + "&latest=" + timestamps[1]); len(exclusive) != 0 {
		t.Errorf("exclusive single-point window returned %v, want none", exclusive)
	}
	if before := read("latest=" + timestamps[1] + "&inclusive=true"); len(before) != 2 {
		t.Errorf("latest window returned %v, want 2 messages", before)
	}
	for query, want := range map[string]string{
		"latest=nonsense": "invalid_ts_latest",
		"oldest=nonsense": "invalid_ts_oldest",
	} {
		response := callAPI(t, handler, http.MethodGet, "/api/conversations.history?channel=C1&"+query, "")
		envelope := decodeEnvelope(t, response)
		if envelope.OK || envelope.Error != want {
			t.Errorf("%s: body=%+v, want %q", query, envelope, want)
		}
	}
}

// files.list decoded only limit and cursor, so `?channel=C1` returned every file
// the principal could see across every channel while reporting success.
// files.list declares user, channel, ts_from, ts_to, types, count, page and
// show_files_hidden_by_limit. The handler decoded none of them, so a scoped
// request such as ?channel=C1 returned every file in the workspace with
// "ok":true. Refusing them was not the answer either: count is how the official
// SDK paginates this method, and refusing it broke that qualification suite.
// Each declared parameter now has to actually narrow the result.
func TestFilesListHonoursEveryDeclaredFilter(t *testing.T) {
	handler, store := testHandlerWithStore()
	ctx := context.Background()
	old := time.Now().UTC().Add(-48 * time.Hour)
	recent := time.Now().UTC()
	seed := func(id domain.FileID, uploader domain.UserID, mime string, at time.Time, channels []domain.ConversationID) {
		event, err := events.New(domain.EventID("E"+string(id)), "T1", uploader, events.NewPayload("file.created", events.String("file_id", string(id))), at)
		if err != nil {
			t.Fatal(err)
		}
		file := domain.File{ID: id, WorkspaceID: "T1", Uploader: uploader, Name: string(id) + ".bin", MIMEType: mime, BlobKey: "blob-" + string(id), CreatedAt: at, SharedChannels: channels}
		if err := store.CreateFile(ctx, file, event); err != nil {
			t.Fatal(err)
		}
	}
	seed("FIMG", "U1", "image/png", recent, []domain.ConversationID{"C1"})
	seed("FOLD", "U2", "application/pdf", old, []domain.ConversationID{"C2"})

	total := decodeFilesList(t, handler, "")
	if len(total) < 3 {
		t.Fatalf("unfiltered files.list returned %d files, want the whole fixture", len(total))
	}

	if got := decodeFilesList(t, handler, "user=U2"); len(got) != 1 || got[0] != "FOLD" {
		t.Errorf("user filter returned %v, want only the file U2 uploaded", got)
	}
	if got := decodeFilesList(t, handler, "channel=C2"); len(got) != 1 || got[0] != "FOLD" {
		t.Errorf("channel filter returned %v, want only the file shared into C2", got)
	}
	if got := decodeFilesList(t, handler, "types=images"); len(got) != 1 || got[0] != "FIMG" {
		t.Errorf("types filter returned %v, want only the image", got)
	}
	if got := decodeFilesList(t, handler, fmt.Sprintf("ts_from=%d", recent.Add(-time.Hour).Unix())); len(got) == 0 {
		t.Error("ts_from excluded every file, want the recent ones")
	} else {
		for _, id := range got {
			if id == "FOLD" {
				t.Error("ts_from included a file created before the window")
			}
		}
	}
	if got := decodeFilesList(t, handler, fmt.Sprintf("ts_to=%d", old.Add(time.Hour).Unix())); len(got) != 1 || got[0] != "FOLD" {
		t.Errorf("ts_to returned %v, want only the file created before the window", got)
	}

	// count is the parameter the official SDK sends, and page has to move.
	first := decodeFilesList(t, handler, "count=1")
	if len(first) != 1 {
		t.Fatalf("count=1 returned %d files, want 1", len(first))
	}
	second := decodeFilesList(t, handler, "count=1&page=2")
	if len(second) != 1 {
		t.Fatalf("count=1&page=2 returned %d files, want 1", len(second))
	}
	if first[0] == second[0] {
		t.Errorf("page 2 repeated page 1 (%q)", first[0])
	}

	// An unrecognised type name is refused with unknown_type. This assertion used
	// to require an empty `ok:true` list, which is the answer for "no such file"
	// and not for "no such type" — and `unknown_type` appears in exactly one enum
	// in the whole pinned snapshot, and it is this operation's. Requiring the
	// empty success made the one code the snapshot is unambiguous about
	// unreachable, so the assertion is corrected rather than relaxed.
	response := callAPI(t, handler, http.MethodGet, "/api/files.list?types=not-a-type", "")
	var refusal struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode %q: %v", response.Body, err)
	}
	if refusal.OK || refusal.Error != "unknown_type" {
		t.Errorf("unrecognised type answered %q, want unknown_type", response.Body)
	}
}

func decodeFilesList(t *testing.T, handler http.Handler, query string) []string {
	t.Helper()
	path := "/api/files.list"
	if query != "" {
		path += "?" + query
	}
	response := callAPI(t, handler, http.MethodGet, path, "")
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("files.list?%s: %v (%s)", query, err, response.Body)
	}
	if !body.OK {
		t.Fatalf("files.list?%s: error=%q", query, body.Error)
	}
	ids := make([]string, 0, len(body.Files))
	for _, file := range body.Files {
		ids = append(ids, file.ID)
	}
	return ids
}

// stars.list dropped the store's cursor and emitted an invented `spill` key, so a
// workspace with more stars than one page could never be read past page one.
func TestStarsListEmitsTheCursorThatReachesPageTwo(t *testing.T) {
	handler, _ := testHandlerWithStore()
	for _, text := range []string{"one", "two"} {
		posted := callAPI(t, handler, http.MethodPost, "/api/chat.postMessage", "channel=C1&text="+text)
		var body struct {
			OK bool   `json:"ok"`
			TS string `json:"ts"`
		}
		if err := json.Unmarshal(posted.Body.Bytes(), &body); err != nil || !body.OK {
			t.Fatalf("post status=%d body=%s", posted.Code, posted.Body)
		}
		if starred := callAPI(t, handler, http.MethodPost, "/api/stars.add", "channel=C1&timestamp="+body.TS); starred.Code != http.StatusOK {
			t.Fatalf("stars.add status=%d body=%s", starred.Code, starred.Body)
		}
	}
	response := callAPI(t, handler, http.MethodGet, "/api/stars.list?limit=1", "")
	var page struct {
		OK               bool             `json:"ok"`
		Items            []map[string]any `json:"items"`
		Paging           map[string]any   `json:"paging"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || !page.OK {
		t.Fatalf("stars.list status=%d body=%s", response.Code, response.Body)
	}
	if len(page.Items) != 1 {
		t.Fatalf("stars.list?limit=1 returned %d items", len(page.Items))
	}
	if _, invented := page.Paging["spill"]; invented {
		t.Error("stars.list still emits the invented `spill` paging key")
	}
	if page.ResponseMetadata.NextCursor == "" {
		t.Fatal("stars.list omitted response_metadata.next_cursor, so page two is unreachable")
	}
	second := callAPI(t, handler, http.MethodGet, "/api/stars.list?limit=1&cursor="+url.QueryEscape(page.ResponseMetadata.NextCursor), "")
	var next struct {
		OK    bool             `json:"ok"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &next); err != nil || !next.OK || len(next.Items) != 1 {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body)
	}
}

func multipartUpload(t *testing.T, fields map[string]string, fileField, filename string, content []byte) (string, []byte) {
	t.Helper()
	return multipartUploadTyped(t, fields, fileField, filename, "", content)
}

// multipartUploadTyped builds an upload whose file part declares partType. It is
// separate from multipartUpload because Go's CreateFormFile always labels the part
// application/octet-stream, which spoolUpload reads as "no type declared" — and
// users.setPhoto requires an image type, so the placement under test could not be
// reached through the untyped helper at all.
func multipartUploadTyped(t *testing.T, fields map[string]string, fileField, filename, partType string, content []byte) (string, []byte) {
	t.Helper()
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := createUploadPart(writer, fileField, filename, partType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), []byte(body.String())
}

func createUploadPart(writer *multipart.Writer, field, filename, partType string) (io.Writer, error) {
	if partType == "" {
		return writer.CreateFormFile(field, filename)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
	header.Set("Content-Type", partType)
	return writer.CreatePart(header)
}

// The pinned /files.upload and /users.setPhoto declare `token` as a formData
// parameter. auth.Stored.Authenticate reads it with r.FormValue, which calls
// ParseMultipartForm and consumes the stream, after which r.MultipartReader()
// fails and the upload was answered with `invalid_arguments` and the bytes
// discarded.
// storedTokenUploadHandler builds the fixture around auth.Stored — the
// authenticator that actually reads the legacy `token` form field — with a blob
// store so an upload can succeed. auth.Static ignores where the token came from,
// so it cannot exercise this placement at all.
func storedTokenUploadHandler(t *testing.T) http.Handler {
	t.Helper()
	memoryStore := memory.New()
	memoryStore.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	memoryStore.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	if err := memoryStore.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeFilesWrite), string(auth.ScopeUsersProfileWrite)}}); err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStored(memoryStore)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: memoryStore, Blob: blobs}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux
}

func TestMultipartUploadsAcceptATokenInTheRequestBody(t *testing.T) {
	handler := storedTokenUploadHandler(t)
	contentType, body := multipartUpload(t, map[string]string{"token": "token", "filename": "note.txt", "title": "note"}, "file", "note.txt", []byte("hello"))
	request := httptest.NewRequest(http.MethodPost, "/api/files.upload", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var uploaded struct {
		OK   bool `json:"ok"`
		File struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"file"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if !uploaded.OK || uploaded.File.ID == "" {
		t.Fatalf("files.upload with a body token: status=%d body=%s", response.Code, response.Body)
	}
	// files.upload declares channels/initial_comment/thread_ts but this path cannot
	// share, so the request is refused with the code the enum declares rather than
	// accepted and silently dropped.
	contentType, body = multipartUpload(t, map[string]string{"token": "token", "filename": "n.txt", "title": "n", "channels": "C1"}, "file", "n.txt", []byte("hello"))
	request = httptest.NewRequest(http.MethodPost, "/api/files.upload", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", contentType)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_channel" {
		t.Fatalf("sharing on upload: body=%+v, want invalid_channel", envelope)
	}
	// A body token that is not a real token must still be a clean auth failure.
	// `invalid_auth`, not `not_authed`: a credential was presented and this
	// deployment could not validate it. The pinned enums declare the two
	// separately (88 operations enumerate `invalid_auth`, 87 `not_authed`), and
	// this assertion used to accept the "no credential" code for a bad one.
	contentType, body = multipartUpload(t, map[string]string{"token": "not-a-token", "filename": "note.txt", "title": "note"}, "file", "note.txt", []byte("hello"))
	request = httptest.NewRequest(http.MethodPost, "/api/files.upload", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", contentType)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_auth" {
		t.Fatalf("bogus body token: body=%+v, want invalid_auth", envelope)
	}
	// A multipart upload with no credential at all must be `not_authed`, so the
	// two remain distinguishable on the body-token path too.
	contentType, body = multipartUpload(t, map[string]string{"filename": "note.txt", "title": "note"}, "file", "note.txt", []byte("hello"))
	request = httptest.NewRequest(http.MethodPost, "/api/files.upload", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", contentType)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "not_authed" {
		t.Fatalf("missing body token: body=%+v, want not_authed", envelope)
	}
}

// users.setPhoto declares `token` as a formData parameter exactly as files.upload
// does, and it is the second handler that reads the multipart stream itself. No
// test carried the token in the body for it, so the placement was only ever
// exercised on one of the two broken methods.
func TestUserPhotoAcceptsATokenInTheMultipartBody(t *testing.T) {
	handler := storedTokenUploadHandler(t)
	png := []byte("\x89PNG\r\n\x1a\nphoto bytes")
	contentType, body := multipartUploadTyped(t, map[string]string{"token": "token"}, "image", "avatar.png", "image/png", png)
	request := httptest.NewRequest(http.MethodPost, "/api/users.setPhoto", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var photo struct {
		OK      bool `json:"ok"`
		Profile struct {
			Image192 string `json:"image_192"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &photo); err != nil || !photo.OK || photo.Profile.Image192 == "" {
		t.Fatalf("users.setPhoto with a body token: status=%d body=%s", response.Code, response.Body)
	}
	// The bytes really were spooled rather than consumed by the authenticator: the
	// stored photo is served back through its capability URL.
	fetch := httptest.NewRequest(http.MethodGet, photo.Profile.Image192, nil)
	fetched := httptest.NewRecorder()
	handler.ServeHTTP(fetched, fetch)
	if fetched.Code != http.StatusOK || !bytes.Equal(fetched.Body.Bytes(), png) {
		t.Fatalf("photo download status=%d len=%d, want the uploaded bytes", fetched.Code, fetched.Body.Len())
	}
	contentType, body = multipartUploadTyped(t, map[string]string{"token": "not-a-token"}, "image", "avatar.png", "image/png", png)
	request = httptest.NewRequest(http.MethodPost, "/api/users.setPhoto", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", contentType)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_auth" {
		t.Fatalf("bogus body token: body=%+v, want invalid_auth", envelope)
	}
}

// reminders.add read `time` strictly as an absolute epoch, so the documented
// "seconds until" form created a 1970-era reminder and reported success.
func TestReminderTimeAcceptsTheRelativeFormAndNamesWhatItCannotParse(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	relative, err := reminderTime("300", now)
	if err != nil {
		t.Fatalf("relative time: %v", err)
	}
	if !relative.Equal(now.Add(5 * time.Minute)) {
		t.Errorf("time=300 resolved to %s, want %s", relative, now.Add(5*time.Minute))
	}
	absolute, err := reminderTime(strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10), now)
	if err != nil {
		t.Fatalf("absolute time: %v", err)
	}
	if absolute.Unix() != now.Add(48*time.Hour).Unix() {
		t.Errorf("absolute time resolved to %s", absolute)
	}
	for _, raw := range []string{"in 15 minutes", "", "0", "-5"} {
		if _, err := reminderTime(raw, now); decodeErrorCode(err) != "cannot_parse" {
			t.Errorf("reminderTime(%q) error code = %q, want cannot_parse", raw, decodeErrorCode(err))
		}
	}
	if _, err := reminderTime(strconv.FormatInt(now.AddDate(6, 0, 0).Unix(), 10), now); decodeErrorCode(err) != "cannot_parse" {
		t.Error("a time more than five years out must not be accepted")
	}
	handler, _ := testHandlerWithStore()
	response := callAPI(t, handler, http.MethodPost, "/api/reminders.add", "text=standup&time=300")
	var created struct {
		OK       bool `json:"ok"`
		Reminder struct {
			Time int64 `json:"time"`
		} `json:"reminder"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || !created.OK {
		t.Fatalf("reminders.add status=%d body=%s", response.Code, response.Body)
	}
	if created.Reminder.Time < time.Now().Unix() {
		t.Fatalf("time=300 stored %d, which is in the past", created.Reminder.Time)
	}
	if envelope := decodeEnvelope(t, callAPI(t, handler, http.MethodPost, "/api/reminders.add", "text=standup&time=in+15+minutes")); envelope.Error != "cannot_parse" {
		t.Fatalf("natural language time: body=%+v, want cannot_parse", envelope)
	}
}

func TestReminderUserTokenRejectsObsoleteOtherUserAndReturnsCompleteTimestamp(t *testing.T) {
	_, repository := testHandlerWithStore()
	granted := make(map[auth.Scope]struct{})
	for _, scope := range defaultTestScopes() {
		granted[scope] = struct{}{}
	}
	authenticator, err := auth.NewStatic("user-token", auth.Principal{
		WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user", Scopes: granted,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: repository}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	call := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/reminders.add", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer user-token")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	if envelope := decodeEnvelope(t, call("text=private&time=300&user=U2")); envelope.OK || envelope.Error != "cannot_add_others" {
		t.Fatalf("other-user reminder body=%+v, want cannot_add_others", envelope)
	}
	response := call("text=private&time=300")
	var created struct {
		OK       bool `json:"ok"`
		Reminder struct {
			Creator    string `json:"creator"`
			User       string `json:"user"`
			CompleteTS *int64 `json:"complete_ts"`
		} `json:"reminder"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response %q: %v", response.Body, err)
	}
	if !created.OK || created.Reminder.Creator != "U1" || created.Reminder.User != "U1" ||
		created.Reminder.CompleteTS == nil || *created.Reminder.CompleteTS != 0 {
		t.Fatalf("reminder shape=%+v, want self-owned active non-recurring reminder", created)
	}
}

// usergroups.users.update read an absent `users` as the empty list, so a request
// that omitted the required argument emptied the group and reported success.
func TestUserGroupUsersUpdateRequiresItsMandatoryArguments(t *testing.T) {
	handler, _ := testHandlerWithStore()
	created := callAPI(t, handler, http.MethodPost, "/api/usergroups.create", "name=team&handle=team")
	var group struct {
		OK        bool `json:"ok"`
		UserGroup struct {
			ID string `json:"id"`
		} `json:"usergroup"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &group); err != nil || !group.OK {
		t.Fatalf("usergroups.create status=%d body=%s", created.Code, created.Body)
	}
	if response := callAPI(t, handler, http.MethodPost, "/api/usergroups.users.update", "usergroup="+group.UserGroup.ID+"&users=U1,U2"); response.Code != http.StatusOK {
		t.Fatalf("seed members status=%d body=%s", response.Code, response.Body)
	}
	response := callAPI(t, handler, http.MethodPost, "/api/usergroups.users.update", "usergroup="+group.UserGroup.ID)
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_arg_name" {
		t.Fatalf("absent users: body=%+v, want ok=false error=invalid_arg_name", envelope)
	}
	members := callAPI(t, handler, http.MethodGet, "/api/usergroups.users.list?usergroup="+group.UserGroup.ID, "")
	var listed struct {
		OK    bool     `json:"ok"`
		Users []string `json:"users"`
	}
	if err := json.Unmarshal(members.Body.Bytes(), &listed); err != nil || !listed.OK {
		t.Fatalf("usergroups.users.list status=%d body=%s", members.Code, members.Body)
	}
	if len(listed.Users) != 2 {
		t.Fatalf("the rejected request still emptied the group: users=%v", listed.Users)
	}
	// Deliberately emptying the group remains possible, because present-and-empty is
	// distinguished from absent.
	if response := callAPI(t, handler, http.MethodPost, "/api/usergroups.users.update", "usergroup="+group.UserGroup.ID+"&users="); response.Code != http.StatusOK {
		t.Fatalf("explicit empty users status=%d body=%s", response.Code, response.Body)
	}
}

// usergroups.list compared `include_users` to the literal "true", so the
// documented boolean form `include_users=1` silently omitted the users array from
// a response that reported success.
func TestUserGroupListBooleansAcceptEveryDocumentedForm(t *testing.T) {
	handler, _ := testHandlerWithStore()
	if response := callAPI(t, handler, http.MethodPost, "/api/usergroups.create", "name=booleans&handle=booleans"); response.Code != http.StatusOK {
		t.Fatalf("usergroups.create status=%d body=%s", response.Code, response.Body)
	}
	for _, raw := range []string{"1", "true", "TRUE"} {
		response := callAPI(t, handler, http.MethodGet, "/api/usergroups.list?include_users="+raw, "")
		var body struct {
			OK         bool `json:"ok"`
			UserGroups []struct {
				Users []string `json:"users"`
			} `json:"usergroups"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.OK {
			t.Fatalf("include_users=%s status=%d body=%s", raw, response.Code, response.Body)
		}
		if len(body.UserGroups) == 0 {
			t.Fatalf("include_users=%s returned no usergroups", raw)
		}
		if body.UserGroups[0].Users == nil {
			t.Errorf("include_users=%s omitted the users array", raw)
		}
	}
	if envelope := decodeEnvelope(t, callAPI(t, handler, http.MethodGet, "/api/usergroups.list?include_users=maybe", "")); envelope.OK || envelope.Error != "invalid_arg_name" {
		t.Errorf("include_users=maybe: body=%+v, want invalid_arg_name", envelope)
	}
}

// A client-controlled header used to fail the read: RecordAccess rejects a
// User-Agent over 1024 bytes, and authenticate turned that into
// `503 access_logging_unavailable`.
func TestALongUserAgentDoesNotFailTheRead(t *testing.T) {
	handler, _ := testHandlerWithStore()
	request := httptest.NewRequest(http.MethodGet, "/api/conversations.history?channel=C1", nil)
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("User-Agent", strings.Repeat("a", 4096))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if envelope := decodeEnvelope(t, response); !envelope.OK {
		t.Fatalf("body=%+v, want ok=true; a long User-Agent must be truncated, not fatal", envelope)
	}
}

// openid.connect.token used to accept the client secret, the authorization code
// and the PKCE verifier in the URL query, where they reach access logs and the
// Referer header. RFC 6749 §3.2 requires POST.
func TestOpenIDConnectTokenRefusesCredentialsInTheURL(t *testing.T) {
	handler, _ := testHandlerWithStore()
	request := httptest.NewRequest(http.MethodGet, "/api/openid.connect.token?client_id=oauth-client&client_secret=oauth-secret&code=oauth-code&redirect_uri=https%3A%2F%2Fcallback&grant_type=authorization_code", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_request" {
		t.Fatalf("GET body=%+v, want ok=false error=invalid_request", envelope)
	}
}

// Incoming webhooks were untested at every layer, and the path secret is the only
// credential. A wrong secret, a wrong app, a foreign workspace and a disabled hook
// must all be indistinguishable rejections; anything else is an unauthenticated
// post-to-any-channel hole.
func TestIncomingWebhookRejectsEverySecretItDidNotIssue(t *testing.T) {
	handler, store := testHandlerWithStore()
	if err := store.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	wrongBot := callAPI(t, handler, http.MethodPost, "/internal/admin/incoming-webhooks/create", "app_id=A1&channel_id=C1&bot_user_id=U1")
	if envelope := decodeEnvelope(t, wrongBot); envelope.OK {
		t.Fatalf("created an A1 webhook using a user who is not A1's bot: %+v", envelope)
	}
	created := callAPI(t, handler, http.MethodPost, "/internal/admin/incoming-webhooks/create", "app_id=A1&channel_id=C1&bot_user_id=U2")
	var hook struct {
		OK              bool `json:"ok"`
		IncomingWebhook struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"incoming_webhook"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &hook); err != nil || !hook.OK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	secret := hook.IncomingWebhook.URL[strings.LastIndex(hook.IncomingWebhook.URL, "/")+1:]
	post := func(path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := post("/services/T1/A1/"+secret, `{"text":"hello"}`); response.Code != http.StatusOK || response.Body.String() != "ok" {
		t.Fatalf("valid webhook status=%d body=%s", response.Code, response.Body)
	}
	rejections := map[string]string{
		"wrong secret":      "/services/T1/A1/" + secret + "-wrong",
		"blank secret":      "/services/T1/A1/%20",
		"wrong app":         "/services/T1/A2/" + secret,
		"wrong workspace":   "/services/T2/A1/" + secret,
		"unknown workspace": "/services/TZZZ/A1/" + secret,
	}
	for name, path := range rejections {
		response := post(path, `{"text":"hello"}`)
		if response.Code != http.StatusNotFound || response.Body.String() != "no_team" {
			t.Errorf("%s: status=%d body=%s, want 404 no_team", name, response.Code, response.Body)
		}
	}
	if response := post("/services/T1/A1/"+secret, `{}`); response.Code != http.StatusBadRequest || response.Body.String() != "invalid_payload" {
		t.Errorf("empty payload: status=%d body=%s, want 400 invalid_payload", response.Code, response.Body)
	}
	if response := callAPI(t, handler, http.MethodPost, "/api/conversations.archive", "channel=C1"); response.Code != http.StatusOK {
		t.Fatalf("archive channel status=%d body=%s", response.Code, response.Body)
	}
	if response := post("/services/T1/A1/"+secret, `{"text":"hello"}`); response.Code != http.StatusGone || response.Body.String() != "channel_is_archived" {
		t.Errorf("archived webhook status=%d body=%s, want 410 channel_is_archived", response.Code, response.Body)
	}
	if response := callAPI(t, handler, http.MethodPost, "/api/conversations.unarchive", "channel=C1"); response.Code != http.StatusOK {
		t.Fatalf("unarchive channel status=%d body=%s", response.Code, response.Body)
	}
	// A disabled hook must stop accepting posts.
	if response := callAPI(t, handler, http.MethodPost, "/internal/admin/incoming-webhooks/enable", "webhook_id="+hook.IncomingWebhook.ID+"&enabled=0"); response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body)
	}
	if response := post("/services/T1/A1/"+secret, `{"text":"hello"}`); response.Code != http.StatusNotFound {
		t.Errorf("disabled webhook status=%d body=%s, want 404", response.Code, response.Body)
	}
}

// admin.conversations.setTeams forwarded target_team_ids verbatim, so a
// workspace-A token could attach A's channel to workspace B. The pinned parameter
// description requires every workspace to belong to the token's organization.
func TestAdminTeamMutationsRejectAForeignWorkspace(t *testing.T) {
	handler, store := testHandlerWithStore()
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "other"})
	response := callAPI(t, handler, http.MethodPost, "/api/admin.conversations.setTeams", "channel_id=C1&target_team_ids=T2")
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_team" {
		t.Fatalf("setTeams to a real foreign workspace: body=%+v, want invalid_team", envelope)
	}
	created := callAPI(t, handler, http.MethodPost, "/api/usergroups.create", "name=cross&handle=cross")
	var group struct {
		OK        bool `json:"ok"`
		UserGroup struct {
			ID string `json:"id"`
		} `json:"usergroup"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &group); err != nil || !group.OK {
		t.Fatalf("usergroups.create status=%d body=%s", created.Code, created.Body)
	}
	response = callAPI(t, handler, http.MethodPost, "/api/admin.usergroups.addTeams", "usergroup_id="+group.UserGroup.ID+"&team_ids=T2")
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_team" {
		t.Fatalf("addTeams to a real foreign workspace: body=%+v, want invalid_team", envelope)
	}
	response = callAPI(t, handler, http.MethodPost, "/api/admin.users.session.invalidate", "team_id=T2&session_id=abc")
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_team" {
		t.Fatalf("session.invalidate for a foreign workspace: body=%+v, want invalid_team", envelope)
	}
}

// A role denial on an admin.* operation whose pinned enum declares `not_an_admin`
// must be named that, not the generic `no_permission`, so a caller can tell which
// grant it is missing. No admin.* service method checks the actor's role yet; when
// one does, its sentinel has to reach the permission branch of mapServiceErrorNamed
// for this mapping to apply.
func TestAdminErrorsNameARoleDenial(t *testing.T) {
	if reason := mapAdminError(service.ErrMessageNotOwned, "channel_not_found"); reason != "not_an_admin" {
		t.Fatalf("mapAdminError for a permission denial = %q, want not_an_admin", reason)
	}
	if reason := mapAdminError(store.ErrNotFound, "channel_not_found"); reason != "channel_not_found" {
		t.Fatalf("mapAdminError for a missing channel = %q", reason)
	}
}

// api.test used to echo a JSON `error` argument with its quotes intact, because
// `error` was in the structured-JSON field list while the form-encoded path
// treated it as a plain string.
func TestAPITestEchoesTheSameErrorForJSONAndForm(t *testing.T) {
	handler, _ := testHandlerWithStore()
	fromJSON := callJSON(t, handler, "/api/api.test", `{"error":"my_error"}`)
	fromForm := callAPI(t, handler, http.MethodPost, "/api/api.test", "error=my_error")
	for name, response := range map[string]*httptest.ResponseRecorder{"json": fromJSON, "form": fromForm} {
		envelope := decodeEnvelope(t, response)
		if envelope.OK || envelope.Error != "my_error" {
			t.Errorf("%s: error=%q, want my_error", name, envelope.Error)
		}
	}
}

// users.profile.set rejected a profile whose only field was a documented boolean,
// because the boolean was parsed and then dropped before the emptiness check.
func TestProfileBooleansAreNotRejectedAsUnknownFields(t *testing.T) {
	if _, err := decodeProfileJSON(`{"always_active":true}`); err != nil {
		t.Fatalf("always_active: %v", err)
	}
	if _, err := decodeProfileJSON(`{"is_custom_image":false}`); err != nil {
		t.Fatalf("is_custom_image: %v", err)
	}
	if _, err := decodeProfileJSON(`{"nope":1}`); err == nil {
		t.Fatal("an unknown profile field must still be rejected")
	}
}

// slackLists.items.delete takes one id; the single form used to split on commas
// and delete several rows.
func TestListItemDeleteRefusesAListInTheSingleIDField(t *testing.T) {
	handler, _ := testHandlerWithStore()
	created := callAPI(t, handler, http.MethodPost, "/api/slackLists.create", "name=single")
	var list struct {
		OK   bool `json:"ok"`
		List struct {
			ID string `json:"id"`
		} `json:"list"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &list); err != nil || !list.OK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	response := callAPI(t, handler, http.MethodPost, "/api/slackLists.items.delete", "list_id="+url.QueryEscape(list.List.ID)+"&id=Rec1,Rec2")
	if envelope := decodeEnvelope(t, response); envelope.OK || envelope.Error != "invalid_arg_name" {
		t.Fatalf("comma-separated single id: body=%+v, want invalid_arg_name", envelope)
	}
}

// A non-threaded message used to serialise as `"thread_ts": ""`, which the
// strictly typed SDK models parse as a timestamp.
func TestMessageResponseOmitsAnEmptyThreadTimestamp(t *testing.T) {
	plain := messageResponse(domain.Message{AuthorID: "U1", Text: "hi", CreatedAt: time.Unix(1700000000, 0).UTC()})
	if _, present := plain["thread_ts"]; present {
		t.Errorf("thread_ts present on a non-threaded message: %v", plain)
	}
	threaded := messageResponse(domain.Message{AuthorID: "U1", Text: "hi", ThreadTimestamp: "1700000000.000000", CreatedAt: time.Unix(1700000001, 0).UTC()})
	if threaded["thread_ts"] != domain.MessageTimestamp("1700000000.000000") {
		t.Errorf("thread_ts missing on a threaded message: %v", threaded)
	}
}

// viewResponse panicked on a stored payload it could not render, which produced a
// bare HTTP 500 with no `ok` field and killed the serving goroutine.
func TestViewResponseReportsAnUnrenderablePayloadAsAHandledError(t *testing.T) {
	if _, err := viewResponse(domain.View{ID: "V1", Payload: `[1,2]`}); decodeErrorCode(err) != "invalid_view" {
		t.Fatalf("array payload error code = %q, want invalid_view", decodeErrorCode(err))
	}
	rendered, err := viewResponse(domain.View{ID: "V1", WorkspaceID: "T1", Payload: `{"type":"modal"}`})
	if err != nil {
		t.Fatalf("object payload: %v", err)
	}
	if rendered["id"] != domain.ViewID("V1") || rendered["type"] != "modal" {
		t.Fatalf("rendered=%v", rendered)
	}
}

// parseIDList replaced five splitters, only two of which understood the JSON-array
// form — which is why slackLists.access.set and canvases.access.set disagreed
// about `channel_ids=["C1"]`.
func TestParseIDListUnderstandsBothDocumentedListForms(t *testing.T) {
	if got := parseIDList[domain.ConversationID](`["C1","C2"]`); len(got) != 2 || got[0] != "C1" || got[1] != "C2" {
		t.Errorf("JSON array = %v", got)
	}
	if got := parseIDList[domain.ConversationID](`C1, C2 ,`); len(got) != 2 || got[1] != "C2" {
		t.Errorf("comma list = %v", got)
	}
	if got := parseIDList[domain.UserID](``); len(got) != 0 {
		t.Errorf("empty = %v", got)
	}
}

func TestParseSlackTimestampRejectsWhatIsNotATimestamp(t *testing.T) {
	if value, ok := parseSlackTimestamp("1700000000.000123"); !ok || value != 1700000000000123 {
		t.Errorf("value=%d ok=%v", value, ok)
	}
	if value, ok := parseSlackTimestamp("1700000000"); !ok || value != 1700000000000000 {
		t.Errorf("value=%d ok=%v", value, ok)
	}
	for _, raw := range []string{"", "abc", "-1", "1700000000.1234567", "1700000000.abc"} {
		if _, ok := parseSlackTimestamp(raw); ok {
			t.Errorf("parseSlackTimestamp(%q) accepted", raw)
		}
	}
}

// admin.users.list returned the plain user projection and omitted is_admin,
// is_owner, is_primary_owner, is_restricted, is_ultra_restricted and is_bot, all of
// which the pinned 200 example carries — even though the admin projection already
// existed and was already used by the web UI.
func TestAdminUsersListReturnsTheAdminProjection(t *testing.T) {
	handler, repository := testHandlerWithStore()
	guest := domain.User{ID: "UG", WorkspaceID: "T1", Email: "guest@example.com", Name: "guest"}
	if err := repository.CreateUser(context.Background(), guest, domain.WorkspaceMembership{
		WorkspaceID: "T1", UserID: guest.ID, Role: domain.WorkspaceRoleMember,
		Active: true, UltraRestricted: true,
	}, events.Event{ID: "E-admin-guest", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	response := callAPI(t, handler, http.MethodGet, "/api/admin.users.list?team_id=T1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var body struct {
		OK    bool             `json:"ok"`
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.OK {
		t.Fatalf("body=%s", response.Body)
	}
	if len(body.Users) == 0 {
		t.Fatal("admin.users.list returned no users")
	}
	for _, field := range []string{"is_admin", "is_owner", "is_primary_owner", "is_restricted", "is_ultra_restricted", "is_bot"} {
		if _, present := body.Users[0][field]; !present {
			t.Errorf("admin.users.list omits %s", field)
		}
	}
	var foundGuest bool
	for _, user := range body.Users {
		if user["id"] == "UG" {
			foundGuest = true
			if user["is_restricted"] != false || user["is_ultra_restricted"] != true {
				t.Errorf("guest flags=%v/%v, want false/true", user["is_restricted"], user["is_ultra_restricted"])
			}
		}
	}
	if !foundGuest {
		t.Fatal("admin.users.list omitted the guest")
	}
}
