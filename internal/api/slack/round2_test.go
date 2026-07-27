package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// errorCode reads the `error` member of a Slack error envelope.
func errorCode(t *testing.T, result *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", result.Body.String(), err)
	}
	if body.OK {
		return ""
	}
	return body.Error
}

func getAPI(handler http.Handler, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}

func postForm(handler http.Handler, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}

type filesListBody struct {
	OK    bool `json:"ok"`
	Files []struct {
		ID       string `json:"id"`
		MIMEType string `json:"mimetype"`
	} `json:"files"`
	Paging struct {
		Count int `json:"count"`
		Page  int `json:"page"`
		Pages int `json:"pages"`
		Total int `json:"total"`
	} `json:"paging"`
}

func decodeFilesListBody(t *testing.T, result *httptest.ResponseRecorder) filesListBody {
	t.Helper()
	var body filesListBody
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", result.Body.String(), err)
	}
	return body
}

// seedFiles adds count files whose ids sort in creation order, applying decorate
// to each one first. The memory repository pages files by id, so the position of
// a file in the scan is its index here.
func seedFiles(t *testing.T, s *memory.Store, count int, decorate func(index int, file *domain.File)) {
	t.Helper()
	ctx := context.Background()
	base := time.Unix(1600000000, 0).UTC()
	for index := 0; index < count; index++ {
		file := domain.File{
			ID: domain.FileID(fmt.Sprintf("Fseed%06d", index)), WorkspaceID: "T1", Uploader: "U1",
			Name: fmt.Sprintf("seed-%06d.bin", index), MIMEType: "application/octet-stream",
			BlobKey: fmt.Sprintf("blob-%06d", index), CreatedAt: base.Add(time.Duration(index) * time.Second),
		}
		if decorate != nil {
			decorate(index, &file)
		}
		event := events.Event{ID: domain.EventID(fmt.Sprintf("Eseed%06d", index)), WorkspaceID: "T1", Topic: "file.created", Payload: string(file.ID), CreatedAt: file.CreatedAt}
		if err := s.CreateFile(ctx, file, event); err != nil {
			t.Fatalf("seed file %d: %v", index, err)
		}
	}
}

// A filter that matches only past the first scan window used to be answered with
// `ok:true` and an empty list, so files.list reported "no such file" for a file
// that exists. The bounded scan is an implementation limit on how much can be
// read, never a limit on what the answer is allowed to describe.
func TestFilesListFindsAMatchMatchingOnlyBeyondTheFirstScanWindow(t *testing.T) {
	handler, s := testHandlerWithStore()
	const total = 2500
	seedFiles(t, s, total, func(index int, file *domain.File) {
		if index == total-1 {
			file.MIMEType = "image/png"
		}
	})
	result := getAPI(handler, "/api/files.list?types=images")
	if result.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	body := decodeFilesListBody(t, result)
	if !body.OK || len(body.Files) != 1 || body.Files[0].MIMEType != "image/png" {
		t.Fatalf("types=images did not return the matching image: %s", result.Body)
	}
	if body.Paging.Total != 1 || body.Paging.Pages != 1 || body.Paging.Page != 1 || body.Paging.Count != 100 {
		t.Fatalf("paging describes the scanned window rather than the collection: %+v", body.Paging)
	}
}

// The pinned 200 schema is additionalProperties:false over exactly
// {ok, files, paging}; a strict decoder rejects anything else.
func TestFilesListEmitsOnlyThePinnedSuccessMembers(t *testing.T) {
	handler, _ := testHandlerWithStore()
	result := getAPI(handler, "/api/files.list")
	if result.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for name := range body {
		switch name {
		case "ok", "files", "paging":
		default:
			t.Errorf("files.list emits %q, which its pinned 200 schema forbids", name)
		}
	}
	var paging map[string]json.RawMessage
	if err := json.Unmarshal(body["paging"], &paging); err != nil {
		t.Fatal(err)
	}
	for name := range paging {
		switch name {
		case "count", "page", "pages", "per_page", "spill", "total":
		default:
			t.Errorf("paging emits %q, which objs_paging forbids", name)
		}
	}
}

// paging must describe the whole filtered collection, not the returned slice.
func TestFilesListPagingDescribesTheWholeCollection(t *testing.T) {
	handler, s := testHandlerWithStore()
	seedFiles(t, s, 25, nil)
	result := getAPI(handler, "/api/files.list?count=10&page=3")
	body := decodeFilesListBody(t, result)
	// The shared fixture seeds one file of its own, so the collection is 26.
	if body.Paging.Total != 26 || body.Paging.Pages != 3 || body.Paging.Page != 3 || body.Paging.Count != 10 {
		t.Fatalf("paging=%+v body=%s", body.Paging, result.Body)
	}
	if len(body.Files) != 6 {
		t.Fatalf("page three of 26 has six files, got %d", len(body.Files))
	}
}

// boundedFileService answers every Files call with another page, so the scan
// bound is always reached.
type boundedFileService struct {
	chatapi.Service
	calls int
}

func (s *boundedFileService) Files(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.FilePage, error) {
	s.calls++
	return domain.FilePage{HasMore: true, NextCursor: domain.Cursor("more")}, nil
}

func (s *boundedFileService) RecordAccess(context.Context, domain.WorkspaceID, domain.UserID, string, string) error {
	return nil
}

// When the scan bound is reached the collection has not been read, so there is no
// complete answer to give. It used to answer ok:true with an empty list and
// `has_more:true` beside an empty `next_cursor` — a truncation indistinguishable
// from a complete result, and a page no caller could ever reach.
func TestFilesListReportsRequestTimeoutRatherThanATruncatedSuccess(t *testing.T) {
	service := &boundedFileService{}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeFilesRead: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	result := getAPI(mux, "/api/files.list")
	if result.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	if code := errorCode(t, result); code != "request_timeout" {
		t.Fatalf("want request_timeout, got %q (%s)", code, result.Body)
	}
	if service.calls != fileFilterScanLimit/fileFilterScanPage {
		t.Fatalf("scan read %d pages, want %d", service.calls, fileFilterScanLimit/fileFilterScanPage)
	}
}

// unknown_type appears in exactly one enum in the whole pinned snapshot and it is
// /files.list's. Matching nothing answered ok:true with an empty list, the answer
// for "no such file" rather than "no such type".
func TestFilesListRefusesAnUnknownType(t *testing.T) {
	handler, _ := testHandlerWithStore()
	for _, target := range []string{"/api/files.list?types=bogus_type", "/api/files.list?types=images,bogus_type"} {
		result := getAPI(handler, target)
		if code := errorCode(t, result); code != "unknown_type" {
			t.Errorf("%s: want unknown_type, got %q (%s)", target, code, result.Body)
		}
	}
	if code := errorCode(t, getAPI(handler, "/api/files.list?types=images,pdfs,zips,all")); code != "" {
		t.Errorf("declared type names must be accepted, got %q", code)
	}
}

// An archive is an ordinary upload this system stores; `types=zips` used to
// answer an empty list for a workspace full of them.
func TestFilesListZipsMatchArchiveUploads(t *testing.T) {
	handler, s := testHandlerWithStore()
	seedFiles(t, s, 3, func(index int, file *domain.File) {
		switch index {
		case 0:
			file.MIMEType = "application/zip"
		case 1:
			file.MIMEType = "application/x-zip-compressed"
		default:
			file.MIMEType = "text/plain"
		}
	})
	body := decodeFilesListBody(t, getAPI(handler, "/api/files.list?types=zips"))
	if body.Paging.Total != 2 || len(body.Files) != 2 {
		t.Fatalf("types=zips returned %d files (total %d)", len(body.Files), body.Paging.Total)
	}
}

// A snippet is authored inside Slack; a .txt or .csv upload is a file. `snippets`
// used to match every text/* upload.
func TestFilesListSnippetsDoNotMatchTextUploads(t *testing.T) {
	handler, s := testHandlerWithStore()
	seedFiles(t, s, 2, func(_ int, file *domain.File) { file.MIMEType = "text/plain" })
	body := decodeFilesListBody(t, getAPI(handler, "/api/files.list?types=snippets"))
	if body.Paging.Total != 0 || len(body.Files) != 0 {
		t.Fatalf("types=snippets matched %d ordinary text uploads", body.Paging.Total)
	}
}

// /files.list declares user_not_found; a filter naming a user who does not exist
// used to be answered with an empty list.
func TestFilesListNamesAMissingUser(t *testing.T) {
	handler, _ := testHandlerWithStore()
	if code := errorCode(t, getAPI(handler, "/api/files.list?user=UNOSUCH")); code != "user_not_found" {
		t.Fatalf("want user_not_found, got %q", code)
	}
	if code := errorCode(t, getAPI(handler, "/api/files.list?user=U1")); code != "" {
		t.Fatalf("an existing user must not be refused, got %q", code)
	}
}

// Neither `limit` nor `cursor` is a parameter of /files.list. The handler refused
// a malformed `limit` it should never have read, and accepted a `cursor` it could
// never echo back.
func TestFilesListIgnoresUndeclaredPaginationArguments(t *testing.T) {
	handler, _ := testHandlerWithStore()
	if code := errorCode(t, getAPI(handler, "/api/files.list?limit=abc&cursor=%21%21%21")); code != "" {
		t.Fatalf("undeclared arguments must be ignored, got %q", code)
	}
}

// Both bounds are declared inclusive, so truncating the fraction dropped a file
// that is genuinely before ts_to.
func TestFilesListTimestampBoundsAreInclusiveBelowOneSecond(t *testing.T) {
	handler, s := testHandlerWithStore()
	moment := time.Unix(100, 500000000).UTC()
	seedFiles(t, s, 1, func(_ int, file *domain.File) { file.CreatedAt = moment })
	body := decodeFilesListBody(t, getAPI(handler, "/api/files.list?ts_from=100.1&ts_to=100.9"))
	if body.Paging.Total != 1 {
		t.Fatalf("a file at 100.5 is inside [100.1, 100.9]: %+v", body.Paging)
	}
	if excluded := decodeFilesListBody(t, getAPI(handler, "/api/files.list?ts_to=100.4")); excluded.Paging.Total != 0 {
		t.Fatalf("a file at 100.5 is outside [-, 100.4]: total=%d", excluded.Paging.Total)
	}
}

// A negative epoch is not a timestamp, and every other timestamp path in this
// transport refuses one.
func TestFilesListRefusesANegativeTimestampBound(t *testing.T) {
	handler, _ := testHandlerWithStore()
	for _, target := range []string{"/api/files.list?ts_from=-5", "/api/files.list?ts_to=-5"} {
		if code := errorCode(t, getAPI(handler, target)); code != "invalid_arg_name" {
			t.Errorf("%s: want invalid_arg_name, got %q", target, code)
		}
	}
}

// show_files_hidden_by_limit is a declared boolean; accepting any value silently
// hid a caller's mistake.
func TestFilesListValidatesShowFilesHiddenByLimit(t *testing.T) {
	handler, _ := testHandlerWithStore()
	if code := errorCode(t, getAPI(handler, "/api/files.list?show_files_hidden_by_limit=perhaps")); code != "invalid_arg_name" {
		t.Fatalf("want invalid_arg_name, got %q", code)
	}
	if code := errorCode(t, getAPI(handler, "/api/files.list?show_files_hidden_by_limit=true")); code != "" {
		t.Fatalf("a legal boolean must be accepted, got %q", code)
	}
}

// countTempSpools reports how many upload spool files exist under dir.
func countTempSpools(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "sameoldchat-upload-") {
			found++
		}
	}
	return found
}

// The spool file's removal used to be registered only after the deferred
// authentication, so every rejected anonymous upload left a file of up to
// maxUploadBytes on disk that nothing ever reclaimed.
func TestRejectedDeferredAuthUploadLeavesNoSpoolFile(t *testing.T) {
	spoolDir := t.TempDir()
	t.Setenv("TMPDIR", spoolDir)
	handler, _ := testHandlerWithStoredTokenAuth(auth.ScopeFilesWrite, auth.ScopeUsersProfileWrite)
	for _, route := range []struct {
		path  string
		field string
	}{
		{"/api/files.upload", "file"},
		{"/api/users.setPhoto", "image"},
	} {
		for attempt := 0; attempt < 3; attempt++ {
			contentType, body := multipartUpload(t, map[string]string{"token": "not-a-real-token"}, route.field, "payload.bin", []byte("payload"))
			request := httptest.NewRequest(http.MethodPost, route.path, bytes.NewReader(body))
			request.Header.Set("Content-Type", contentType)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if code := errorCode(t, result); code == "" {
				t.Fatalf("%s accepted a bogus token: %s", route.path, result.Body)
			}
		}
		if remaining := countTempSpools(t, spoolDir); remaining != 0 {
			t.Fatalf("%s left %d spool files behind after three rejected uploads", route.path, remaining)
		}
	}
}

// Per-part ceilings bound nothing while the number of parts is unbounded, and
// neither the multipart reader nor r.Body was capped, so the whole cost was
// payable by an anonymous caller.
func TestUploadBoundsTheWholeRequestAndNotOnlyEachField(t *testing.T) {
	spoolDir := t.TempDir()
	t.Setenv("TMPDIR", spoolDir)
	handler, _ := testHandlerWithStoredTokenAuth(auth.ScopeFilesWrite)
	var buffer strings.Builder
	writer := multipart.NewWriter(&buffer)
	// Well under the per-field ceiling each, and far past any sane total.
	filler := strings.Repeat("x", 4<<10)
	for index := 0; index < maxUploadFields*4; index++ {
		if err := writer.WriteField(fmt.Sprintf("field%d", index), filler); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/files.upload", strings.NewReader(buffer.String()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if code := errorCode(t, result); code == "" {
		t.Fatalf("an unbounded field count was accepted: %s", result.Body)
	}
	if remaining := countTempSpools(t, spoolDir); remaining != 0 {
		t.Fatalf("%d spool files left behind", remaining)
	}
}

// The request body itself must be bounded, so an anonymous caller cannot make the
// server read an arbitrary number of bytes.
func TestUploadRequestBodyIsBounded(t *testing.T) {
	if maxUploadRequestBytes <= maxUploadBytes {
		t.Fatalf("the whole-request bound must leave room for the file: %d", maxUploadRequestBytes)
	}
	spoolDir := t.TempDir()
	t.Setenv("TMPDIR", spoolDir)
	handler, _ := testHandlerWithStoredTokenAuth(auth.ScopeFilesWrite)
	// A body that claims to be a multipart upload and simply never ends. The
	// reader must stop at the bound rather than read it all.
	contentType, body := multipartUpload(t, map[string]string{"token": "not-a-real-token"}, "file", "payload.bin", make([]byte, 1<<20))
	request := httptest.NewRequest(http.MethodPost, "/api/files.upload", &endlessReader{prefix: string(body)})
	request.Header.Set("Content-Type", contentType)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if code := errorCode(t, result); code == "" {
		t.Fatalf("an unbounded body was accepted: %s", result.Body)
	}
	if remaining := countTempSpools(t, spoolDir); remaining != 0 {
		t.Fatalf("%d spool files left behind", remaining)
	}
}

// endlessReader replays prefix and then emits filler forever, so a reader with no
// bound never reaches EOF.
type endlessReader struct {
	prefix string
	offset int
}

func (r *endlessReader) Read(p []byte) (int, error) {
	if r.offset < len(r.prefix) {
		n := copy(p, r.prefix[r.offset:])
		r.offset += n
		return n, nil
	}
	for index := range p {
		p[index] = 'x'
	}
	return len(p), nil
}

// msg_too_long is declared by every operation that writes a message and was
// emitted by none of them, so an unbounded body was stored and then re-served in
// full by every later read of the channel.
func TestMessageWritersRefuseAnOversizedBody(t *testing.T) {
	handler, _ := testHandlerWithStore()
	oversized := strings.Repeat("a", maxMessageTextRunes+1)
	cases := []struct {
		path string
		body string
	}{
		{"/api/chat.postMessage", "channel=C1&text=" + oversized},
		{"/api/chat.meMessage", "channel=C1&text=" + oversized},
		{"/api/chat.postEphemeral", "channel=C1&user=U2&text=" + oversized},
		{"/api/chat.scheduleMessage", "channel=C1&post_at=4102444800&text=" + oversized},
	}
	for _, item := range cases {
		if code := errorCode(t, postForm(handler, item.path, item.body)); code != "msg_too_long" {
			t.Errorf("%s: want msg_too_long, got %q", item.path, code)
		}
	}
	posted := postForm(handler, "/api/chat.postMessage", "channel=C1&text=hello")
	var created struct {
		OK bool   `json:"ok"`
		TS string `json:"ts"`
	}
	if err := json.Unmarshal(posted.Body.Bytes(), &created); err != nil || !created.OK {
		t.Fatalf("seed post failed: %s", posted.Body)
	}
	update := postForm(handler, "/api/chat.update", "channel=C1&ts="+created.TS+"&text="+oversized)
	if code := errorCode(t, update); code != "msg_too_long" {
		t.Errorf("chat.update: want msg_too_long, got %q", code)
	}
}

// A message at the ceiling is still accepted; the bound must not be a regression
// for legitimate long messages.
func TestMessageAtTheCeilingIsAccepted(t *testing.T) {
	handler, _ := testHandlerWithStore()
	body := postForm(handler, "/api/chat.postMessage", "channel=C1&text="+strings.Repeat("a", maxMessageTextRunes))
	if code := errorCode(t, body); code != "" {
		t.Fatalf("a message at the ceiling was refused with %q", code)
	}
}

func postJSON(handler http.Handler, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}

// jsonIsComposite accepted `[`, so a JSON array was forwarded verbatim and echoed
// back as the error code. Both operations that take `error` declare it
// `type: string`, and 83 pinned enums declare invalid_array_arg for this.
func TestJSONArrayArgumentIsRefusedAsInvalidArrayArg(t *testing.T) {
	handler, _ := testHandlerWithStore()
	result := postJSON(handler, "/api/api.test", `{"error":[1]}`)
	if code := errorCode(t, result); code != "invalid_array_arg" {
		t.Fatalf("want invalid_array_arg, got %q (%s)", code, result.Body)
	}
	// The object form must still be forwarded verbatim: workflows.stepFailed
	// declares an object carrying a `message`.
	if body := postJSON(handler, "/api/api.test", `{"error":{"message":"x"}}`).Body.String(); !strings.Contains(body, `message`) {
		t.Fatalf("an object argument was flattened: %s", body)
	}
	// And the scalar form must still flatten, or the JSON and form encodings
	// disagree about the same value.
	if body := postJSON(handler, "/api/api.test", `{"error":"boom"}`).Body.String(); !strings.Contains(body, `"error":"boom"`) {
		t.Fatalf("a scalar argument was not flattened: %s", body)
	}
}

// A JSON null used to decode to the empty string and read as "the argument was
// not sent", which silently discards an argument workflows.stepFailed declares
// required.
func TestJSONNullArgumentIsNotReadAsAbsent(t *testing.T) {
	handler, _ := testHandlerWithStore()
	if code := errorCode(t, postJSON(handler, "/api/api.test", `{"error":null}`)); code != "invalid_arg_name" {
		t.Fatalf("want invalid_arg_name, got %q", code)
	}
	if code := errorCode(t, postJSON(handler, "/api/workflows.stepFailed", `{"workflow_step_execute_id":"x","error":null}`)); code == "" {
		t.Fatal("workflows.stepFailed accepted a null for its required error argument")
	}
}

// postSeed posts one message and returns its timestamp.
func postSeed(t *testing.T, handler http.Handler) string {
	t.Helper()
	result := postForm(handler, "/api/chat.postMessage", "channel=C1&text=hello")
	var body struct {
		OK bool   `json:"ok"`
		TS string `json:"ts"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil || !body.OK {
		t.Fatalf("seed post: %s", result.Body)
	}
	return body.TS
}

// store.ErrAlreadyExists was mapped globally to `already_reacted`, so every
// collision in the system reported a reaction error. Each of these three codes
// appears in exactly one of the 99 pinned enums, and it is the operation's own.
func TestCollisionsNameTheirOwnOperationsCode(t *testing.T) {
	for _, item := range []struct{ path, want string }{
		{"/api/pins.add", "already_pinned"},
		{"/api/stars.add", "already_starred"},
		{"/api/reactions.add", "already_reacted"},
	} {
		handler, _ := testHandlerWithStore()
		timestamp := postSeed(t, handler)
		form := "channel=C1&timestamp=" + timestamp
		if item.path == "/api/reactions.add" {
			form += "&name=thumbsup"
		}
		if code := errorCode(t, postForm(handler, item.path, form)); code != "" {
			t.Fatalf("%s first call: %q", item.path, code)
		}
		if code := errorCode(t, postForm(handler, item.path, form)); code != item.want {
			t.Errorf("%s duplicate: want %q, got %q", item.path, item.want, code)
		}
	}
}

// Both branches returned before r.ParseForm ever ran, so `?channel=C1` with a
// JSON or multipart payload was answered `no_text` — naming an argument that was
// present, because the one that was missing had been discarded.
func TestQueryArgumentsSurviveEveryBodyEncoding(t *testing.T) {
	handler, _ := testHandlerWithStore()
	if code := errorCode(t, postJSON(handler, "/api/chat.postMessage?channel=C1", `{"text":"hi"}`)); code != "" {
		t.Errorf("JSON body + query channel: %q", code)
	}
	if code := errorCode(t, postJSON(handler, "/api/chat.postMessage?text=hi", `{"channel":"C1"}`)); code != "" {
		t.Errorf("JSON channel + query text: %q", code)
	}
	contentType, body := multipartUpload(t, map[string]string{"text": "hi"}, "unused", "", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage?channel=C1", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", contentType)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if code := errorCode(t, result); code != "" {
		t.Errorf("multipart body + query channel: %q (%s)", code, result.Body)
	}
	// The body wins over the query, and the same argument in both places is no
	// longer a conflicting duplicate.
	if code := errorCode(t, postForm(handler, "/api/chat.postMessage?channel=C2", "channel=C1&text=hi")); code != "" {
		t.Errorf("form body overriding the query: %q", code)
	}
}

// The service rejects an empty channel as ErrInvalidMessage, which
// postMessageError renamed `no_text` — so a request carrying text and no channel
// was told its text was missing.
func TestPostMessageWithoutAChannelNamesTheChannel(t *testing.T) {
	handler, _ := testHandlerWithStore()
	for _, path := range []string{"/api/chat.postMessage", "/api/chat.meMessage"} {
		if code := errorCode(t, postForm(handler, path, "text=hi")); code != "channel_not_found" {
			t.Errorf("%s: want channel_not_found, got %q", path, code)
		}
	}
}

// A tampered cursor is a client argument, not a server fault. pins.list and
// stars.list answered `fatal_error`, and six operations answered `invalid_cursor`
// which their own enums do not declare.
func TestTamperedCursorsAreRefusedWithADeclaredCode(t *testing.T) {
	handler, _ := testHandlerWithStore()
	cases := []struct{ target, want string }{
		{"/api/users.list", "invalid_cursor"},
		{"/api/conversations.members?channel=C1", "invalid_cursor"},
		{"/api/usergroups.list", "invalid_arg_name"},
		{"/api/conversations.list", "invalid_arg_name"},
		{"/api/reminders.list", "invalid_arg_name"},
		{"/api/pins.list?channel=C1", "invalid_arg_name"},
		{"/api/stars.list", "invalid_arg_name"},
		{"/api/conversations.history?channel=C1", "invalid_arg_name"},
		{"/api/reactions.get?channel=C1&timestamp=1", "invalid_arg_name"},
		{"/api/chat.scheduledMessages.list", "invalid_arg_name"},
		{"/api/search.messages?query=x", "invalid_arg_name"},
	}
	for _, item := range cases {
		separator := "?"
		if strings.Contains(item.target, "?") {
			separator = "&"
		}
		result := getAPI(handler, item.target+separator+"cursor=%21%21%21%21")
		if code := errorCode(t, result); code != item.want {
			t.Errorf("%s: want %q, got %q", item.target, item.want, code)
		}
	}
}

// slackLists.items.list was the one list decoder that skipped cursor validation,
// so a tampered cursor reached the store and was reported as list_not_found.
func TestListItemsValidatesItsCursor(t *testing.T) {
	handler, _ := testHandlerWithStore()
	created := postForm(handler, "/api/slackLists.create", "name=cursor-list")
	var list struct {
		List struct {
			ID string `json:"id"`
		} `json:"list"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &list); err != nil || list.List.ID == "" {
		t.Fatalf("create: %s", created.Body)
	}
	result := postForm(handler, "/api/slackLists.items.list", "list_id="+list.List.ID+"&cursor=%21%21%21%21")
	if code := errorCode(t, result); code != "invalid_arg_name" {
		t.Fatalf("want invalid_arg_name, got %q (%s)", code, result.Body)
	}
}

// Codes each operation's own enum declares, where a code from another
// operation's enum used to be emitted.
func TestOperationsNameCodesTheirOwnEnumDeclares(t *testing.T) {
	handler, _ := testHandlerWithStore()
	cases := []struct{ path, form, want string }{
		{"/api/chat.unfurl", "channel=C1&ts=1", "missing_unfurls"},
		{"/api/dnd.setSnooze", "", "missing_duration"},
		{"/api/dialog.open", "dialog=%7B%7D", "missing_trigger"},
		{"/api/dialog.open", "trigger_id=T", "missing_dialog"},
		{"/api/migration.exchange", "team_id=TOTHER&users=U1", "invalid_arg_name"},
	}
	for _, item := range cases {
		if code := errorCode(t, postForm(handler, item.path, item.form)); code != item.want {
			t.Errorf("%s %q: want %q, got %q", item.path, item.form, item.want, code)
		}
	}
}

// files.upload's sharing refusal was raised inside the spool shared with
// users.setPhoto, whose enum declares bad_image, too_large and not_found and no
// invalid_channel at all.
func TestUsersSetPhotoDoesNotBorrowFilesUploadsChannelCode(t *testing.T) {
	handler, _ := testHandlerWithStoredTokenAuth(auth.ScopeUsersProfileWrite, auth.ScopeFilesWrite)
	contentType, body := multipartUpload(t, map[string]string{"token": "token", "channels": "C1"}, "image", "photo.png", []byte("\x89PNG\r\n\x1a\n"))
	request := httptest.NewRequest(http.MethodPost, "/api/users.setPhoto", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if code := errorCode(t, result); code == "invalid_channel" {
		t.Fatalf("users.setPhoto emitted files.upload's invalid_channel: %s", result.Body)
	}
	// files.upload still refuses the sharing arguments it cannot honour.
	contentType, body = multipartUpload(t, map[string]string{"token": "token", "channels": "C1"}, "file", "f.txt", []byte("hello"))
	request = httptest.NewRequest(http.MethodPost, "/api/files.upload", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if code := errorCode(t, result); code != "invalid_channel" {
		t.Fatalf("files.upload: want invalid_channel, got %q (%s)", code, result.Body)
	}
}

// net/http.ServeMux answered a typo with text/plain 404 and a wrong verb with
// text/plain 405. No Slack SDK can parse either: every one decodes the body as
// JSON and keys on `ok`.
func TestUnroutedAPIRequestsAnswerAJSONEnvelope(t *testing.T) {
	handler, _ := testHandlerWithStore()
	for _, probe := range []struct{ method, target string }{
		{http.MethodDelete, "/api/chat.postMessage"},
		{http.MethodGet, "/api/does.not.exist"},
		{http.MethodPut, "/api/conversations.list"},
	} {
		request := httptest.NewRequest(probe.method, probe.target, nil)
		request.Header.Set("Authorization", "Bearer token")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != http.StatusOK {
			t.Errorf("%s %s: status=%d, want 200", probe.method, probe.target, result.Code)
		}
		if contentType := result.Header().Get("Content-Type"); contentType != "application/json" {
			t.Errorf("%s %s: content-type=%q", probe.method, probe.target, contentType)
		}
		if code := errorCode(t, result); code != "unknown_method" {
			t.Errorf("%s %s: want unknown_method, got %q", probe.method, probe.target, code)
		}
	}
	// The catch-all must not shadow a registered route.
	if code := errorCode(t, getAPI(handler, "/api/conversations.list")); code != "" {
		t.Errorf("the catch-all shadowed a registered route: %q", code)
	}
	// apps.uninstall was POST-only while its 32 apps.*/auth.* siblings register
	// both verbs, so a GET reached the catch-all.
	if code := errorCode(t, getAPI(handler, "/api/apps.uninstall")); code == "unknown_method" {
		t.Error("GET /api/apps.uninstall is not registered")
	}
}

// /chat.scheduledMessages.list declares security [{slackAuth: ["none"]}] and
// "Requires scope: `none`", so a token that may read scheduled messages without
// being able to write them must be accepted.
func TestScheduledMessagesListRequiresNoScope(t *testing.T) {
	handler, _ := testHandlerWithScopes(auth.ScopeChannelsHistory)
	result := getAPI(handler, "/api/chat.scheduledMessages.list")
	if code := errorCode(t, result); code != "" {
		t.Fatalf("a scopeless operation refused a narrow token with %q (%s)", code, result.Body)
	}
}

// dnd.teamInfo with no `users` argument names the whole workspace. It read one
// page of a thousand, discarded the cursor and answered ok:true, so a larger
// workspace was told an arbitrary subset was its full membership.
func TestDndTeamInfoNamesEveryMemberOfALargeWorkspace(t *testing.T) {
	handler, s := testHandlerWithStore()
	const extra = 1500
	for index := 0; index < extra; index++ {
		s.SeedUser(domain.User{ID: domain.UserID(fmt.Sprintf("Useed%06d", index)), WorkspaceID: "T1", Name: fmt.Sprintf("seed%06d", index)})
	}
	result := getAPI(handler, "/api/dnd.teamInfo")
	if result.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	var body struct {
		OK    bool                       `json:"ok"`
		Users map[string]json.RawMessage `json:"users"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// The shared fixture seeds U1 and U2 as well.
	if !body.OK || len(body.Users) != extra+2 {
		t.Fatalf("dnd.teamInfo described %d of %d members", len(body.Users), extra+2)
	}
}

// promoteQueryToken shared the body-emptying copy used by the deferred
// authentication, so an upload whose token was in the URL query was answered
// invalid_form_data: the body had been discarded before the multipart reader saw
// it.
func TestUploadWithTheTokenInTheQueryKeepsItsBody(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	if err := s.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeFilesWrite)}}); err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStored(s)
	if err != nil {
		t.Fatal(err)
	}
	built, err := NewHandler(service.Messages{Store: s, Blob: blobs}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	built.Register(mux)
	var handler http.Handler = mux
	contentType, body := multipartUpload(t, nil, "file", "payload.txt", []byte("hello"))
	request := httptest.NewRequest(http.MethodPost, "/api/files.upload?token=token", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if code := errorCode(t, result); code != "" {
		t.Fatalf("query-token upload was refused with %q (%s)", code, result.Body)
	}
	var uploaded struct {
		File struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"file"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.File.Name != "payload.txt" || uploaded.File.Size != int64(len("hello")) {
		t.Fatalf("the body did not survive: %+v", uploaded.File)
	}
}
