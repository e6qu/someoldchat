package slack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// fileFixture is a workspace with one member in one channel, a real blob store,
// and a Slack handler over it.
type fileFixture struct {
	t     *testing.T
	store *memory.Store
	mux   *http.ServeMux
}

func newFileFixture(t *testing.T, publicURL string, withBlob bool) fileFixture {
	t.Helper()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "hideout", Kind: domain.ConversationTypePrivate})
	s.SeedConversationMember("C2", "U1")
	messages := service.Messages{Store: s}
	if withBlob {
		objects, err := blob.NewFilesystem(t.TempDir(), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		messages.Blob = objects
	}
	// groups:write is the scope conversations.leave requires for a private
	// channel; the shares test leaves one.
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{
		auth.ScopeFilesRead: {}, auth.ScopeFilesWrite: {}, auth.ScopeChannelsHistory: {}, auth.ScopeChatWrite: {}, auth.ScopeChannelsManage: {}, auth.ScopeGroupsWrite: {},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.SetPublicURL(publicURL); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return fileFixture{t: t, store: s, mux: mux}
}

func (f fileFixture) do(request *http.Request) *httptest.ResponseRecorder {
	f.t.Helper()
	if request.Header.Get("Authorization") == "" {
		request.Header.Set("Authorization", "Bearer token")
	}
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, request)
	return response
}

func (f fileFixture) call(method string, values url.Values, header http.Header) map[string]any {
	f.t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://chat.test/api/"+method, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, value := range header {
		request.Header[name] = value
	}
	response := f.do(request)
	if response.Code != http.StatusOK {
		f.t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		f.t.Fatalf("%s body=%s: %v", method, response.Body, err)
	}
	return body
}

// uploadV2 runs the files.uploadV2 sequence the official SDKs use and returns
// the completed file object.
func (f fileFixture) uploadV2(name, content string, extra url.Values) map[string]any {
	f.t.Helper()
	ticket := f.call("files.getUploadURLExternal", url.Values{"filename": {name}, "length": {strconv.Itoa(len(content))}}, nil)
	if ticket["ok"] != true {
		f.t.Fatalf("ticket=%v", ticket)
	}
	uploaded := f.do(httptest.NewRequest(http.MethodPost, ticket["upload_url"].(string), strings.NewReader(content)))
	if uploaded.Code != http.StatusOK {
		f.t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body)
	}
	files, _ := json.Marshal([]map[string]string{{"id": ticket["file_id"].(string), "title": name}})
	values := url.Values{"files": {string(files)}}
	for key, value := range extra {
		values[key] = value
	}
	completed := f.call("files.completeUploadExternal", values, nil)
	list, _ := completed["files"].([]any)
	if completed["ok"] != true || len(list) != 1 {
		f.t.Fatalf("complete=%v", completed)
	}
	return list[0].(map[string]any)
}

// Official SDKs fetch url_private, url_private_download and the upload URL
// verbatim, so every one must be absolute and on the origin clients reach.
func TestFileURLsAreAbsoluteOnTheConfiguredPublicURL(t *testing.T) {
	f := newFileFixture(t, "https://chat.example.com/", true)
	ticket := f.call("files.getUploadURLExternal", url.Values{"filename": {"a.txt"}, "length": {"1"}}, nil)
	if uploadURL, _ := ticket["upload_url"].(string); !strings.HasPrefix(uploadURL, "https://chat.example.com/internal/files/external/") {
		t.Fatalf("upload_url=%v", ticket["upload_url"])
	}
	file := f.uploadV2("report.txt", "hello", url.Values{"channel_id": {"C1"}})
	id := file["id"].(string)
	want := map[string]string{
		"url_private":          "https://chat.example.com/api/files/" + id,
		"url_private_download": "https://chat.example.com/api/files/" + id,
		"permalink":            "https://chat.example.com/app/files/" + id,
	}
	check := func(label string, object map[string]any) {
		t.Helper()
		for field, value := range want {
			if object[field] != value {
				t.Fatalf("%s %s=%v, want %s: %v", label, field, object[field], value, object)
			}
		}
	}
	check("completeUploadExternal", file)
	check("files.info", f.call("files.info", url.Values{"file": {id}}, nil)["file"].(map[string]any))
	check("files.list", f.call("files.list", nil, nil)["files"].([]any)[0].(map[string]any))
	history := f.call("conversations.history", url.Values{"channel": {"C1"}}, nil)
	for _, raw := range history["messages"].([]any) {
		message := raw.(map[string]any)
		if files, ok := message["files"].([]any); ok {
			check("conversations.history", files[0].(map[string]any))
		}
	}
	shared := f.call("files.sharedPublicURL", url.Values{"file": {id}}, nil)
	public, _ := shared["file"].(map[string]any)["permalink_public"].(string)
	if !strings.HasPrefix(public, "https://chat.example.com/files/public/") || shared["permalink_public"] != public {
		t.Fatalf("sharedPublicURL=%v", shared)
	}
	// The absolute URL is the one a client downloads through.
	download := httptest.NewRequest(http.MethodGet, want["url_private"], nil)
	if response := f.do(download); response.Code != http.StatusOK || response.Body.String() != "hello" {
		t.Fatalf("download status=%d body=%q", response.Code, response.Body)
	}
}

// Without a configured public URL the origin is the request's own, and a
// forwarded scheme is honoured only when it is exactly http or https.
func TestFileURLsFollowTheRequestOriginAndRejectForgedSchemes(t *testing.T) {
	f := newFileFixture(t, "", true)
	for _, testCase := range []struct {
		forwarded string
		want      string
	}{
		{"", "http://chat.test/internal/files/external/"},
		{"https", "https://chat.test/internal/files/external/"},
		{"HTTPS, http", "https://chat.test/internal/files/external/"},
		{"javascript", "http://chat.test/internal/files/external/"},
		{"ftp", "http://chat.test/internal/files/external/"},
	} {
		header := http.Header{}
		if testCase.forwarded != "" {
			header.Set("X-Forwarded-Proto", testCase.forwarded)
		}
		ticket := f.call("files.getUploadURLExternal", url.Values{"filename": {"a.txt"}, "length": {"1"}}, header)
		if uploadURL, _ := ticket["upload_url"].(string); !strings.HasPrefix(uploadURL, testCase.want) {
			t.Fatalf("X-Forwarded-Proto %q: upload_url=%v, want prefix %s", testCase.forwarded, ticket["upload_url"], testCase.want)
		}
	}
}

func TestSetPublicURLRefusesAnOriginThatCannotPrefixAURL(t *testing.T) {
	for _, value := range []string{"chat.example.com", "ftp://chat.example.com", "https://", "https://chat.example.com/?x=1", "https://user:pass@chat.example.com", "https://chat.example.com/#top"} {
		var handler Handler
		if err := handler.SetPublicURL(value); err == nil {
			t.Fatalf("SetPublicURL(%q) accepted", value)
		}
	}
}

// The upload URL is not a Web API method: the SDKs read its HTTP status and
// nothing else, so a refusal must not be a 200.
func TestExternalUploadURLRefusesWithHTTPStatus(t *testing.T) {
	f := newFileFixture(t, "", true)
	ticket := f.call("files.getUploadURLExternal", url.Values{"filename": {"a.txt"}, "length": {"5"}}, nil)
	uploadURL := ticket["upload_url"].(string)
	post := func(target, body string) *httptest.ResponseRecorder {
		return f.do(httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)))
	}
	if response := post(uploadURL, "toolong"); response.Code != http.StatusBadRequest {
		t.Fatalf("wrong length status=%d body=%s", response.Code, response.Body)
	}
	var short bytes.Buffer
	writer := multipart.NewWriter(&short)
	part, _ := writer.CreateFormFile("body", "a.txt")
	_, _ = io.WriteString(part, "ab")
	_ = writer.Close()
	shortRequest := httptest.NewRequest(http.MethodPost, uploadURL, &short)
	shortRequest.Header.Set("Content-Type", writer.FormDataContentType())
	if response := f.do(shortRequest); response.Code != http.StatusBadRequest {
		t.Fatalf("short multipart status=%d body=%s", response.Code, response.Body)
	}
	if response := post("http://chat.test/internal/files/external/Fnope", "hello"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown ticket status=%d body=%s", response.Code, response.Body)
	}
	if response := post(uploadURL, "hello"); response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body)
	}
	if response := post(uploadURL, "hello"); response.Code != http.StatusNotFound {
		t.Fatalf("reused ticket status=%d body=%s", response.Code, response.Body)
	}

	unavailable := newFileFixture(t, "", false)
	storageless := unavailable.call("files.getUploadURLExternal", url.Values{"filename": {"a.txt"}, "length": {"5"}}, nil)
	if response := unavailable.do(httptest.NewRequest(http.MethodPost, storageless["upload_url"].(string), strings.NewReader("hello"))); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("no blob store status=%d body=%s", response.Code, response.Body)
	}
}

// Completing a ticket whose bytes never arrived names a file that does not
// exist yet.
func TestCompleteUploadExternalBeforeUploadIsFileNotFound(t *testing.T) {
	f := newFileFixture(t, "", true)
	ticket := f.call("files.getUploadURLExternal", url.Values{"filename": {"a.txt"}, "length": {"5"}}, nil)
	files, _ := json.Marshal([]map[string]string{{"id": ticket["file_id"].(string)}})
	completed := f.call("files.completeUploadExternal", url.Values{"files": {string(files)}}, nil)
	if completed["ok"] != false || completed["error"] != "file_not_found" {
		t.Fatalf("complete=%v", completed)
	}
}

// files.uploadV2 never states a media type; the name does.
func TestUploadsInferTheirMediaTypeFromTheName(t *testing.T) {
	f := newFileFixture(t, "", true)
	// The SDKs call files.getUploadURLExternal without mime_type.
	if ticket := f.call("files.getUploadURLExternal", url.Values{"filename": {"x.md"}, "length": {"1"}}, nil); ticket["ok"] != true {
		t.Fatalf("ticket=%v", ticket)
	}
	for _, testCase := range []struct {
		name, mimeType, fileType, prettyType string
	}{
		{"notes.md", "text/markdown", "markdown", "Markdown (raw)"},
		{"photo.PNG", "image/png", "png", "PNG"},
		{"report.txt", "text/plain", "text", "Plain Text"},
		{"archive.unknownext", "application/octet-stream", "unknownext", "UNKNOWNEXT"},
		{"README", "application/octet-stream", "binary", "Binary"},
	} {
		file := f.uploadV2(testCase.name, "x", nil)
		if file["mimetype"] != testCase.mimeType || file["filetype"] != testCase.fileType || file["pretty_type"] != testCase.prettyType {
			t.Fatalf("%s: mimetype=%v filetype=%v pretty_type=%v", testCase.name, file["mimetype"], file["filetype"], file["pretty_type"])
		}
	}
	// The classic files.upload reads an application/octet-stream part as
	// undeclared, which is what @slack/web-api sends for a Buffer.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "diagram.png")
	_, _ = part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "http://chat.test/api/files.upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := f.do(request)
	var uploaded struct {
		File map[string]any `json:"file"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &uploaded); err != nil || uploaded.File["mimetype"] != "image/png" {
		t.Fatalf("files.upload=%s", response.Body)
	}
}

// A deleted file leaves a tombstone in every message that shared it, and the
// deletion is announced as file_deleted in the same commit.
func TestDeletedFileIsATombstoneAndIsAnnounced(t *testing.T) {
	f := newFileFixture(t, "", true)
	file := f.uploadV2("report.txt", "hello", url.Values{"channel_id": {"C1"}, "initial_comment": {"see attached"}})
	id := file["id"].(string)
	if deleted := f.call("files.delete", url.Values{"file": {id}}, nil); deleted["ok"] != true {
		t.Fatalf("delete=%v", deleted)
	}
	history := f.call("conversations.history", url.Values{"channel": {"C1"}}, nil)
	found := false
	for _, raw := range history["messages"].([]any) {
		files, ok := raw.(map[string]any)["files"].([]any)
		if !ok {
			continue
		}
		found = true
		tombstone := files[0].(map[string]any)
		if len(tombstone) != 2 || tombstone["id"] != id || tombstone["mode"] != "tombstone" {
			t.Fatalf("history file=%v", tombstone)
		}
	}
	if !found {
		t.Fatalf("history carried no file share: %v", history)
	}
	announced := false
	for _, event := range f.store.Outbox() {
		if event.Topic == "file.deleted" && strings.Contains(event.Payload, id) {
			announced = true
		}
	}
	if !announced {
		t.Fatal("files.delete did not journal file.deleted")
	}
}

// files.list is newest first with a stable order across pages.
func TestFilesListIsNewestFirstAndPagesStably(t *testing.T) {
	f := newFileFixture(t, "", true)
	var ids []string
	for index := 0; index < 5; index++ {
		ids = append(ids, f.uploadV2("f"+strconv.Itoa(index)+".txt", "x", nil)["id"].(string))
		time.Sleep(2 * time.Millisecond)
	}
	listed := f.call("files.list", nil, nil)["files"].([]any)
	if len(listed) != len(ids) {
		t.Fatalf("listed=%d", len(listed))
	}
	for index, raw := range listed {
		if want := ids[len(ids)-1-index]; raw.(map[string]any)["id"] != want {
			t.Fatalf("files.list[%d]=%v, want %s", index, raw.(map[string]any)["id"], want)
		}
	}
	var paged []string
	for page := 1; page <= 3; page++ {
		for _, raw := range f.call("files.list", url.Values{"count": {"2"}, "page": {strconv.Itoa(page)}}, nil)["files"].([]any) {
			paged = append(paged, raw.(map[string]any)["id"].(string))
		}
	}
	for index, id := range paged {
		if id != ids[len(ids)-1-index] {
			t.Fatalf("paged=%v, want newest first of %v", paged, ids)
		}
	}
}

// files.info requires comments in its pinned 200 schema.
func TestFilesInfoCarriesTheRequiredCommentsArray(t *testing.T) {
	f := newFileFixture(t, "", true)
	id := f.uploadV2("a.txt", "x", nil)["id"].(string)
	info := f.call("files.info", url.Values{"file": {id}}, nil)
	if comments, ok := info["comments"].([]any); !ok || len(comments) != 0 {
		t.Fatalf("files.info=%v", info)
	}
	if metadata, ok := info["response_metadata"].(map[string]any); !ok || metadata["next_cursor"] != "" {
		t.Fatalf("files.info=%v", info)
	}
}

// files.info reports the messages that shared the file under shares, split
// into public and private, and only in conversations the reader can see.
func TestFilesInfoReportsItsSharesByVisibility(t *testing.T) {
	f := newFileFixture(t, "", true)
	id := f.uploadV2("a.txt", "x", url.Values{"channels": {"C1,C2"}, "initial_comment": {"see attached"}})["id"].(string)
	history := f.call("conversations.history", url.Values{"channel": {"C1"}}, nil)["messages"].([]any)
	shareTS := history[0].(map[string]any)["ts"]
	shares, _ := f.call("files.info", url.Values{"file": {id}}, nil)["file"].(map[string]any)["shares"].(map[string]any)
	public, _ := shares["public"].(map[string]any)["C1"].([]any)
	private, _ := shares["private"].(map[string]any)["C2"].([]any)
	if len(public) != 1 || len(private) != 1 {
		t.Fatalf("shares=%v", shares)
	}
	entry := public[0].(map[string]any)
	if entry["ts"] != shareTS || entry["channel_name"] != "general" || entry["team_id"] != "T1" || entry["share_user_id"] != "U1" {
		t.Fatalf("public share=%v, want ts %v", entry, shareTS)
	}
	if private[0].(map[string]any)["channel_name"] != "hideout" {
		t.Fatalf("private share=%v", private[0])
	}
	if _, present := entry["reply_count"]; present {
		t.Fatalf("a share without replies carried a thread summary: %v", entry)
	}

	// A sharing message that starts a thread carries its summary, as the
	// message object does.
	if reply := f.call("chat.postMessage", url.Values{"channel": {"C1"}, "thread_ts": {shareTS.(string)}, "text": {"thanks"}}, nil); reply["ok"] != true {
		t.Fatalf("reply=%v", reply)
	}
	threaded, _ := f.call("files.info", url.Values{"file": {id}}, nil)["file"].(map[string]any)["shares"].(map[string]any)
	entry = threaded["public"].(map[string]any)["C1"].([]any)[0].(map[string]any)
	if entry["thread_ts"] != shareTS || entry["reply_count"] != float64(1) || entry["reply_users_count"] != float64(1) ||
		fmt.Sprint(entry["reply_users"]) != "[U1]" || entry["latest_reply"] == nil {
		t.Fatalf("share with a reply=%v", entry)
	}
	if left := f.call("conversations.leave", url.Values{"channel": {"C2"}}, nil); left["ok"] != true {
		t.Fatalf("leave=%v", left)
	}
	after, _ := f.call("files.info", url.Values{"file": {id}}, nil)["file"].(map[string]any)["shares"].(map[string]any)
	if _, present := after["private"]; present || after["public"] == nil {
		t.Fatalf("shares after leaving the private channel=%v", after)
	}
}
