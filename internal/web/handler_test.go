package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/scheduler"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestValidatePublicURLAllowsHTTPSAndExplicitLoopbackOnly(t *testing.T) {
	for _, value := range []string{
		"https://chat.example.test",
		"http://localhost:8080",
		"http://someoldchat-primary.localhost:18080",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
	} {
		if err := ValidatePublicURL(value); err != nil {
			t.Errorf("%q rejected: %v", value, err)
		}
	}
	for _, value := range []string{
		"http://chat.example.test",
		"http://localhost.attacker.test",
		"https://user@chat.example.test",
		"https://chat.example.test?redirect=https://attacker.test",
		"https://chat.example.test/#fragment",
	} {
		if err := ValidatePublicURL(value); err == nil {
			t.Errorf("%q accepted", value)
		}
	}
}

// addBrowserCookies makes a request look like the one a current browser sends.
//
// Sec-Fetch-Site is part of that shape and it is set here rather than in the
// two tests that care: without it every request in this package travelled the
// "neither Sec-Fetch-Site nor Origin, so this is not a browser" fall-through in
// auth.validateRequestSite, and the branch a real browser takes was exercised
// by exactly one test. A caller that is deliberately testing another site sets
// the header first, and this leaves it alone.
func addBrowserCookies(request *http.Request) {
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	token := auth.CSRFToken("session")
	request.Header.Set(auth.CSRFTokenHeaderName, token)
	if request.Header.Get("Sec-Fetch-Site") == "" {
		request.Header.Set("Sec-Fetch-Site", "same-origin")
	}
}

// browserWorkspace seeds the smallest workspace a browser journey needs and
// returns a mux with the web handler registered.
func browserWorkspace(t *testing.T, scopes []string) (*memory.Store, *http.ServeMux) {
	t.Helper()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer", RealName: "Ada Developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general", Topic: "Everything else"})
	// Posting, and marking read, require membership of the conversation.
	s.SeedConversationMember("Cdev", "U1")
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: scopes, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(t.TempDir(), 100<<20)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s, Blob: objects, AppCredentialKey: []byte(strings.Repeat("k", 32))}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return s, mux
}

type failingSearchHistoryStore struct {
	store.Store
	recordErr error
	listErr   error
}

func (s failingSearchHistoryStore) RecordSearchHistory(context.Context, domain.SearchHistoryEntry) error {
	return s.recordErr
}

func (s failingSearchHistoryStore) ListSearchHistory(context.Context, domain.WorkspaceID, domain.UserID, int) ([]domain.SearchHistoryEntry, error) {
	return nil, s.listErr
}

func seedMessage(t *testing.T, s *memory.Store, id domain.MessageID, text string, createdAt time.Time) domain.Message {
	t.Helper()
	message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: text, CreatedAt: createdAt}
	event := events.Event{ID: domain.EventID("E" + string(id)), WorkspaceID: "T1", Topic: "message.created", Payload: string(id), CreatedAt: createdAt}
	if err := s.CreateMessage(context.Background(), message, event, ""); err != nil {
		t.Fatal(err)
	}
	return message
}

func get(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func postForm(t *testing.T, mux *http.ServeMux, target, body string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestCanvasAndListJourneysUseDurableDocuments(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	csrf := auth.CSRFToken("session")

	canvasCreated := postForm(t, mux, "/app/canvases/create", url.Values{
		"_csrf": {csrf}, "title": {"Release plan"}, "body": {"Ship the durable UI"},
	}.Encode(), false)
	if canvasCreated.Code != http.StatusSeeOther {
		t.Fatalf("create canvas = %d: %s", canvasCreated.Code, canvasCreated.Body)
	}
	canvasURL := canvasCreated.Header().Get("Location")
	canvasPage := get(t, mux, canvasURL)
	if canvasPage.Code != http.StatusOK {
		t.Fatalf("open canvas = %d: %s", canvasPage.Code, canvasPage.Body)
	}
	// Each section carries its own editor, so the control names the section it
	// saves rather than the whole canvas.
	requireContains(t, "canvas", canvasPage.Body.String(), "Release plan", "Ship the durable UI", "Save section 1")
	sectionMatch := regexp.MustCompile(`name="section_id" value="([^"]+)"`).FindStringSubmatch(canvasPage.Body.String())
	if len(sectionMatch) != 2 {
		t.Fatalf("canvas editor section is missing: %s", canvasPage.Body)
	}

	canvasSaved := postForm(t, mux, canvasURL+"/update", url.Values{
		"_csrf": {csrf}, "section_id": {sectionMatch[1]}, "title": {"Release plan v2"}, "body": {"Shipped atomically"},
	}.Encode(), false)
	if canvasSaved.Code != http.StatusSeeOther {
		t.Fatalf("save canvas = %d: %s", canvasSaved.Code, canvasSaved.Body)
	}
	canvasPage = get(t, mux, strings.Split(canvasSaved.Header().Get("Location"), "?")[0])
	requireContains(t, "saved canvas", canvasPage.Body.String(), "Release plan v2", "Shipped atomically")

	listCreated := postForm(t, mux, "/app/lists/create", url.Values{
		"_csrf": {csrf}, "title": {"Launch"}, "todo_mode": {"true"},
	}.Encode(), false)
	if listCreated.Code != http.StatusSeeOther {
		t.Fatalf("create list = %d: %s", listCreated.Code, listCreated.Body)
	}
	listURL := listCreated.Header().Get("Location")
	itemCreated := postForm(t, mux, listURL+"/items/create", url.Values{
		"_csrf": {csrf}, "title": {"Verify persistence"},
	}.Encode(), false)
	if itemCreated.Code != http.StatusSeeOther {
		t.Fatalf("create list item = %d: %s", itemCreated.Code, itemCreated.Body)
	}
	listPage := get(t, mux, listURL)
	requireContains(t, "list", listPage.Body.String(), "Launch", "To-do list", "Verify persistence", "Complete")

	itemPath := regexp.MustCompile(`/items/([^/]+)/toggle`).FindStringSubmatch(listPage.Body.String())
	if len(itemPath) != 2 {
		t.Fatalf("list item toggle is missing: %s", listPage.Body)
	}
	listID := domain.ListID(strings.TrimPrefix(listURL, "/app/lists/"))
	itemID := domain.ListItemID(itemPath[1])
	messages := service.Messages{Store: s}
	customFields := `[{"column_id":"title","value":"Verify persistence"},{"column_id":"owner","value":"U1"}]`
	if _, err := messages.UpdateListItem(context.Background(), "T1", "U1", listID, itemID, customFields, false); err != nil {
		t.Fatal(err)
	}
	toggled := postForm(t, mux, listURL+"/items/"+itemPath[1]+"/toggle", url.Values{
		"_csrf": {csrf}, "archived": {"true"},
	}.Encode(), false)
	if toggled.Code != http.StatusSeeOther {
		t.Fatalf("toggle list item = %d: %s", toggled.Code, toggled.Body)
	}
	listPage = get(t, mux, listURL)
	requireContains(t, "completed list item", listPage.Body.String(), "Verify persistence", "Restore")
	item, err := s.GetListItem(context.Background(), "T1", listID, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Fields != customFields {
		t.Fatalf("complete replaced custom fields: got %s want %s", item.Fields, customFields)
	}

	canvases := get(t, mux, "/app/canvases")
	lists := get(t, mux, "/app/lists")
	requireContains(t, "canvas directory", canvases.Body.String(), "Release plan v2", "Shipped atomically")
	requireContains(t, "list directory", lists.Body.String(), "Launch", "To-do list")
}

// TestCanvasSharingShowsWhoCanOpenItAndOnlyTheOwnerChangesIt covers the surface
// that was missing entirely: the grant mechanism has existed since canvases
// did, but nothing in this client showed a grant or made one, so a canvas could
// only ever be shared by an app calling the API. The two halves are separate
// claims — everyone who may open the canvas sees the list, and only its owner
// gets the controls — so both are asserted here.
func TestCanvasSharingShowsWhoCanOpenItAndOnlyTheOwnerChangesIt(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "reviewer", RealName: "Bea Reviewer"})
	messages := service.Messages{Store: s}
	value, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Release plan", `{"type":"markdown","markdown":"Ship it"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)
	csrf := auth.CSRFToken("session")

	page := get(t, mux, target)
	// The owner is a line of the sharing list even with no grants: "shared with
	// nobody" and "shared with everyone" must not look the same.
	requireContains(t, "unshared canvas", page.Body.String(), "Sharing", "Ada Developer", "Owner", "Share canvas", `value="user:U2"`)

	shared := postForm(t, mux, target+"/share", url.Values{
		"_csrf": {csrf}, "target": {"user:U2"}, "access": {"write"},
	}.Encode(), false)
	if shared.Code != http.StatusSeeOther {
		t.Fatalf("share canvas = %d: %s", shared.Code, shared.Body)
	}
	page = get(t, mux, target)
	requireContains(t, "shared canvas", page.Body.String(), "Bea Reviewer", "Can edit", "Stop sharing with Bea Reviewer")
	// Somebody already shared with is not offered again: the only effect would
	// be to change the level, which the level control on the row does.
	requireMissing(t, "shared canvas", page.Body.String(), `<option value="user:U2"`)
	if _, err := messages.Canvas(context.Background(), "T1", "U2", value.ID); err != nil {
		t.Fatalf("the share did not grant access: %v", err)
	}

	revoked := postForm(t, mux, target+"/share/revoke", url.Values{
		"_csrf": {csrf}, "target": {"user:U2"},
	}.Encode(), false)
	if revoked.Code != http.StatusSeeOther {
		t.Fatalf("revoke share = %d: %s", revoked.Code, revoked.Body)
	}
	if _, err := messages.Canvas(context.Background(), "T1", "U2", value.ID); err == nil {
		t.Fatal("revoking the share left the canvas readable")
	}

	// A reader sees who it is shared with and gets no controls, because the
	// service refuses a grant from anyone but the owner and a control that only
	// ever produces a refusal is worse than no control.
	if err := messages.SetCanvasAccess(context.Background(), "T1", "U1", value.ID, "read", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(context.Background(), "reader-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "reader-session"})
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	readerPage := httptest.NewRecorder()
	mux.ServeHTTP(readerPage, request)
	if readerPage.Code != http.StatusOK {
		t.Fatalf("reader open canvas = %d: %s", readerPage.Code, readerPage.Body)
	}
	requireContains(t, "reader view", readerPage.Body.String(), "Ada Developer", "Bea Reviewer", "Only the owner can change who this canvas is shared with")
	requireMissing(t, "reader view", readerPage.Body.String(), "Share canvas", "Stop sharing with")
}

// A list carries the same grant model as a canvas and had the same hole: the
// API could share one and nothing in the client could show or change that. The
// surface is now one surface, so this asserts the list half reaches it and
// keeps the same two rules.
func TestListSharingShowsWhoCanOpenItAndOnlyTheOwnerChangesIt(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "reviewer", RealName: "Bea Reviewer"})
	messages := service.Messages{Store: s}
	value, err := messages.CreateList(context.Background(), "T1", "U1", "Launch", "", "[]", "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/lists/" + string(value.ID)
	csrf := auth.CSRFToken("session")

	page := get(t, mux, target)
	requireContains(t, "unshared list", page.Body.String(), "Sharing", "Ada Developer", "Owner", "Share list", `value="user:U2"`)

	shared := postForm(t, mux, target+"/share", url.Values{
		"_csrf": {csrf}, "target": {"user:U2"}, "access": {"read"},
	}.Encode(), false)
	if shared.Code != http.StatusSeeOther {
		t.Fatalf("share list = %d: %s", shared.Code, shared.Body)
	}
	page = get(t, mux, target)
	requireContains(t, "shared list", page.Body.String(), "Bea Reviewer", "Can view", "Stop sharing with Bea Reviewer")
	if _, err := messages.List(context.Background(), "T1", "U2", value.ID); err != nil {
		t.Fatalf("the share did not grant access: %v", err)
	}

	// A reader sees the list of grants and none of the controls.
	if err := s.SeedSession(context.Background(), "reader-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "reader-session"})
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	readerPage := httptest.NewRecorder()
	mux.ServeHTTP(readerPage, request)
	if readerPage.Code != http.StatusOK {
		t.Fatalf("reader open list = %d: %s", readerPage.Code, readerPage.Body)
	}
	requireContains(t, "reader view", readerPage.Body.String(), "Ada Developer", "Bea Reviewer", "Only the owner can change who this list is shared with")
	requireMissing(t, "reader view", readerPage.Body.String(), "Share list", "Stop sharing with")

	revoked := postForm(t, mux, target+"/share/revoke", url.Values{
		"_csrf": {csrf}, "target": {"user:U2"},
	}.Encode(), false)
	if revoked.Code != http.StatusSeeOther {
		t.Fatalf("revoke share = %d: %s", revoked.Code, revoked.Body)
	}
	if _, err := messages.List(context.Background(), "T1", "U2", value.ID); err == nil {
		t.Fatal("revoking the share left the list readable")
	}
}

// A channel could be made private and never made public again from any
// first-party surface: admin.conversations.convertToPublic did not exist, and
// nothing in the client offered either direction for a channel.
func TestChannelVisibilityChangesBothWaysForAnAdministrator(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SetWorkspaceRole(context.Background(), "T1", "U1", domain.WorkspaceRoleAdmin, events.Event{}); err != nil {
		t.Fatal(err)
	}
	csrf := auth.CSRFToken("session")
	details := func() string {
		t.Helper()
		return get(t, mux, "/app?channel=Cdev&details=1").Body.String()
	}
	// The warning is the point of the control: going public cannot be undone in
	// effect, and the page says so before it is used.
	requireContains(t, "public channel details", details(), "Make this channel private", "Only members will be able to read")

	private := postForm(t, mux, "/app/conversation/visibility?channel=Cdev", url.Values{"_csrf": {csrf}, "private": {"true"}}.Encode(), false)
	if private.Code != http.StatusSeeOther {
		t.Fatalf("make private = %d: %s", private.Code, private.Body)
	}
	requireContains(t, "private channel details", details(), "Make this channel public", "will be able to read everything already said")

	public := postForm(t, mux, "/app/conversation/visibility?channel=Cdev", url.Values{"_csrf": {csrf}, "private": {"false"}}.Encode(), false)
	if public.Code != http.StatusSeeOther {
		t.Fatalf("make public = %d: %s", public.Code, public.Body)
	}
	conversation, err := s.GetConversation(context.Background(), "Cdev")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.IsPrivate {
		t.Fatalf("channel = %+v, want it public again", conversation)
	}

	// A member who is not an administrator is not offered the control and is
	// refused if they post anyway.
	if err := s.SetWorkspaceRole(context.Background(), "T1", "U1", domain.WorkspaceRoleMember, events.Event{}); err != nil {
		t.Fatal(err)
	}
	requireMissing(t, "member details", details(), "Make this channel private")
	refused := postForm(t, mux, "/app/conversation/visibility?channel=Cdev", url.Values{"_csrf": {csrf}, "private": {"true"}}.Encode(), false)
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("member conversion = %d: %s", refused.Code, refused.Body)
	}
}

// A conversation's own canvas existed in the API and nowhere else: the store,
// the service and conversations.canvases.create have carried it since canvases
// were built, and no first-party surface offered or opened one.
// A list whose primary column is named anything but "title" rendered every item
// blank whenever the row was not drawn as cells. That is every list built
// through AddListColumn or through the API, and it needed no removal to reach:
// one declared column is not a structure, so the row falls back to the item's
// name, and the name was looked up under a column that list does not have.
func TestListItemIsNamedByItsPrimaryColumn(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	value, err := messages.CreateList(context.Background(), "T1", "U1", "Launch", "", "[]", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AddListColumn(context.Background(), "T1", "U1", value.ID, "Task", domain.ListColumnText, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateListItem(context.Background(), "T1", "U1", value.ID, "", `[{"column_id":"task","value":"ship it"}]`); err != nil {
		t.Fatal(err)
	}
	requireContains(t, "list", get(t, mux, "/app/lists/"+string(value.ID)).Body.String(), "ship it")
}

// Declaring a column was offered and removing one was not, which left a list
// stuck with any column anybody had ever added. Removing one deletes what every
// item recorded under it, so it says so, and the column that names the item
// stays because a list without one renders as unlabelled cells.
func TestListColumnCanBeRemovedAndTakesItsCellsWithIt(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	value, err := messages.CreateList(context.Background(), "T1", "U1", "Launch", "", "[]", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/lists/" + string(value.ID)
	csrf := auth.CSRFToken("session")
	for _, column := range []url.Values{
		{"_csrf": {csrf}, "name": {"Task"}, "type": {"text"}},
		{"_csrf": {csrf}, "name": {"Status"}, "type": {"select"}, "options": {"open, done"}},
	} {
		if added := postForm(t, mux, target+"/columns", column.Encode(), false); added.Code != http.StatusSeeOther {
			t.Fatalf("add column = %d: %s", added.Code, added.Body)
		}
	}
	item, err := messages.CreateListItem(context.Background(), "T1", "U1", value.ID, "", `[{"column_id":"task","value":"ship it"},{"column_id":"status","value":"open"}]`)
	if err != nil {
		t.Fatal(err)
	}

	page := get(t, mux, target)
	requireContains(t, "list", page.Body.String(), "Remove a column", "Remove Status", "names the item", "ship it")
	// The column that names the item is not offered for removal.
	requireMissing(t, "list", page.Body.String(), "Remove Task")

	removed := postForm(t, mux, target+"/columns/remove", url.Values{"_csrf": {csrf}, "key": {"status"}}.Encode(), false)
	if removed.Code != http.StatusSeeOther {
		t.Fatalf("remove column = %d: %s", removed.Code, removed.Body)
	}
	// What is left is one column naming the item, which is what a list without
	// a declared structure is, so the row goes back to showing its name rather
	// than a one-cell table.
	after := get(t, mux, target)
	requireContains(t, "list after removal", after.Body.String(), "ship it")
	requireMissing(t, "list after removal", after.Body.String(), "Status (select)", "Remove Status")

	// The values go with the column. A cell under no column is invisible, is
	// carried by every later edit, and would come back the day a new column
	// minted the same key.
	stored, err := s.GetListItem(context.Background(), "T1", value.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.Fields, "status") {
		t.Fatalf("the removed column's cell survived: %s", stored.Fields)
	}
	if !strings.Contains(stored.Fields, "ship it") {
		t.Fatalf("removing one column took another column's value: %s", stored.Fields)
	}

	// Removing it again is refused, and so is the primary column.
	if repeat := postForm(t, mux, target+"/columns/remove", url.Values{"_csrf": {csrf}, "key": {"status"}}.Encode(), false); repeat.Code != http.StatusBadRequest {
		t.Fatalf("second removal = %d: %s", repeat.Code, repeat.Body)
	}
	if primary := postForm(t, mux, target+"/columns/remove", url.Values{"_csrf": {csrf}, "key": {"task"}}.Encode(), false); primary.Code != http.StatusBadRequest {
		t.Fatalf("primary removal = %d: %s", primary.Code, primary.Body)
	}
}

// Completing an item hides it and can be undone; deleting one cannot. The
// client only ever offered the first, so an item added by mistake stayed in the
// list forever with a line through it. slackLists.items.delete has existed
// throughout.
func TestListItemCanBeDeletedForGoodAndCompletionStillCannot(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	value, err := messages.CreateList(context.Background(), "T1", "U1", "Launch", "", "[]", "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/lists/" + string(value.ID)
	csrf := auth.CSRFToken("session")

	if created := postForm(t, mux, target+"/items/create", url.Values{"_csrf": {csrf}, "title": {"added by mistake"}}.Encode(), false); created.Code != http.StatusSeeOther {
		t.Fatalf("create item = %d: %s", created.Code, created.Body)
	}
	page := get(t, mux, target)
	requireContains(t, "list", page.Body.String(), "added by mistake", "Delete this item", "for good")
	match := regexp.MustCompile(`/items/([^/]+)/delete`).FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatalf("no delete control: %s", page.Body)
	}

	// Completing it keeps it, which is the distinction the two controls exist
	// to draw.
	if done := postForm(t, mux, target+"/items/"+match[1]+"/toggle", url.Values{"_csrf": {csrf}, "archived": {"true"}}.Encode(), false); done.Code != http.StatusSeeOther {
		t.Fatalf("complete item = %d: %s", done.Code, done.Body)
	}
	requireContains(t, "completed list", get(t, mux, target).Body.String(), "added by mistake", "Restore")

	deleted := postForm(t, mux, target+"/items/"+match[1]+"/delete", url.Values{"_csrf": {csrf}}.Encode(), false)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete item = %d: %s", deleted.Code, deleted.Body)
	}
	after := get(t, mux, target)
	requireMissing(t, "list after deletion", after.Body.String(), "added by mistake")
	requireContains(t, "list after deletion", after.Body.String(), "No items yet.")

	// Deleting it again answers as it would for an item somebody else has
	// already deleted, rather than reporting a failure the member caused.
	repeat := postForm(t, mux, target+"/items/"+match[1]+"/delete", url.Values{"_csrf": {csrf}}.Encode(), false)
	if repeat.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d: %s", repeat.Code, repeat.Body)
	}
}

func TestChannelCanvasIsOfferedToMembersAndCreatedOnPurpose(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())

	// The conversation offers its canvas to a member.
	page := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "conversation", page.Body.String(), `href="/app/channel-canvas?channel=Cdev"`)

	// Opening it says there is none rather than making one: a canvas appearing
	// because somebody followed a link would put an edit in the channel's
	// history that nobody made.
	empty := get(t, mux, "/app/channel-canvas?channel=Cdev")
	if empty.Code != http.StatusOK {
		t.Fatalf("open channel canvas = %d: %s", empty.Code, empty.Body)
	}
	requireContains(t, "empty channel canvas", empty.Body.String(), "has no canvas yet", "Create the canvas")
	messages := service.Messages{Store: s}
	if _, err := messages.ConversationCanvas(context.Background(), "T1", "U1", "Cdev"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("opening the empty state created a canvas: %v", err)
	}

	created := postForm(t, mux, "/app/channel-canvas/create", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "channel": {"Cdev"}, "title": {"general"},
	}.Encode(), false)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create channel canvas = %d: %s", created.Code, created.Body)
	}
	canvasURL := strings.Split(created.Header().Get("Location"), "?")[0]
	requireContains(t, "canvas page", get(t, mux, canvasURL).Body.String(), "general", "Sharing")

	// The conversation now goes straight to the canvas it has.
	again := get(t, mux, "/app/channel-canvas?channel=Cdev")
	if again.Code != http.StatusSeeOther || again.Header().Get("Location") != canvasURL {
		t.Fatalf("second open = %d %q, want a redirect to %s", again.Code, again.Header().Get("Location"), canvasURL)
	}

	// A conversation has exactly one. A second attempt is not the member's
	// mistake — somebody else made it first — so it arrives at the canvas that
	// exists rather than at an error.
	second := postForm(t, mux, "/app/channel-canvas/create", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "channel": {"Cdev"}, "title": {"general again"},
	}.Encode(), false)
	if second.Code != http.StatusSeeOther || strings.Split(second.Header().Get("Location"), "?")[0] != canvasURL {
		t.Fatalf("second create = %d %q, want the existing canvas", second.Code, second.Header().Get("Location"))
	}
}

func TestCanvasEditorRefusesToFlattenStructuredContent(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: s}
	value, err := messages.CreateCanvas(context.Background(), "T1", "U1", "Structured plan", `{"type":"heading","text":"Keep this heading"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	target := "/app/canvases/" + string(value.ID)

	page := get(t, mux, target)
	if page.Code != http.StatusOK {
		t.Fatalf("open structured canvas = %d: %s", page.Code, page.Body)
	}
	// The section is shown as stored and carries no editor: this client has no
	// heading editor, and a plain textarea for one would flatten it on save.
	// That is narrower than the whole canvas being read-only, which is what
	// this used to assert — a canvas with a heading and a paragraph left the
	// paragraph uneditable too.
	requireContains(t, "structured canvas", page.Body.String(), "Keep this heading", "editing it here would flatten it")
	requireMissing(t, "structured canvas", page.Body.String(), "Save section")

	response := postForm(t, mux, target+"/update", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "title": {"Flattened"}, "body": {"Lost content"},
	}.Encode(), false)
	if response.Code != http.StatusConflict {
		t.Fatalf("structured save = %d: %s", response.Code, response.Body)
	}
	stored, err := s.GetCanvas(context.Background(), "T1", value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != value.Title || stored.DocumentContent != value.DocumentContent || stored.Version != value.Version {
		t.Fatalf("refused structured save changed canvas: got %#v want %#v", stored, value)
	}
}

func requireContains(t *testing.T, what, body string, expected ...string) {
	t.Helper()
	for _, value := range expected {
		if !strings.Contains(body, value) {
			t.Fatalf("%s is missing %q: %s", what, value, body)
		}
	}
}

func requireMissing(t *testing.T, what, body string, unexpected ...string) {
	t.Helper()
	for _, value := range unexpected {
		if strings.Contains(body, value) {
			t.Fatalf("%s still contains %q: %s", what, value, body)
		}
	}
}

// TestThreadViewRendersTheThreadAndItsComposer covers the defect that made
// every "Reply in thread" link answer 503: the message partial was invoked with
// one type for the timeline and another for the thread pane, so the pane could
// not resolve the CSRF token and the whole page render failed.
func TestThreadViewRendersTheThreadAndItsComposer(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "thread root", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	response := get(t, mux, "/app?channel=Cdev&thread="+timestamp)
	if response.Code != http.StatusOK {
		t.Fatalf("thread view status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "thread view", body,
		`<h2 id="thread-heading">Thread</h2>`,
		`id="thread-messages"`,
		`name="thread_ts" value="`+timestamp+`"`,
		`<button class="send" type="submit">Send</button>`,
		"thread root",
	)
	// The reply composer has to post into the thread pane, not the channel
	// timeline, or a reply never appears where it was written.
	requireContains(t, "thread composer", body, `hx-target="#thread-messages"`)
	// The pane repeats a message the timeline already shows, so the document must
	// not carry the same identifier twice.
	requireContains(t, "thread pane", body, `id="thread-message-M1"`)
	if strings.Count(body, `id="message-M1"`) != 1 {
		t.Fatalf("message anchor is duplicated %d times: %s", strings.Count(body, `id="message-M1"`), body)
	}
}

func TestWorkspaceUploadsSharesRendersAndDownloadsAFile(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	page := get(t, mux, "/app?channel=Cdev")
	if page.Code != http.StatusOK {
		t.Fatalf("workspace status=%d body=%s", page.Code, page.Body)
	}
	requireContains(t, "workspace file control", page.Body.String(), "Attach a file", `action="/app/file/stage?channel=Cdev"`, `enctype="multipart/form-data"`)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("_csrf", auth.CSRFToken("session")); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("title", "Quarterly report"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("real file contents")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/app/file?channel=Cdev", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body)
	}

	history, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 || len(history.Messages[0].Files) != 1 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	file := history.Messages[0].Files[0]
	rendered := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "shared file message", rendered.Body.String(), "Quarterly report", "report.txt", `/app/files/`+string(file.ID), "Download")

	download := get(t, mux, "/app/files/"+string(file.ID))
	if download.Code != http.StatusOK || download.Body.String() != "real file contents" {
		t.Fatalf("download status=%d body=%q", download.Code, download.Body.String())
	}
	if disposition := download.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "report.txt") {
		t.Fatalf("content disposition=%q", disposition)
	}
	if download.Header().Get("X-Content-Type-Options") != "nosniff" || download.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unsafe download headers=%v", download.Header())
	}
}

func TestComposerStagesPastedAndDroppedFilesIntoOneAtomicMessage(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if _, err := (service.Messages{Store: s}).SaveDraft(context.Background(), "T1", "U1", "Cdev", "", "old durable draft"); err != nil {
		t.Fatal(err)
	}
	page := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "composer file staging", page.Body.String(),
		`id="upload-form"`,
		`id="upload-comment"`,
		`id="upload-clear"`,
		`name="file" multiple`,
		`id="clip-recorder"`,
		`data-record-clip="audio"`,
		`data-record-clip="video"`,
		"You can also paste or drop files into the composer.",
	)
	requireContains(t, "composer paste and drop behavior", progressiveEnhancementScript,
		"text.addEventListener('paste'",
		"composer.addEventListener('drop'",
		"existing.concat(Array.prototype.slice.call(fileList)).slice(0,10)",
		"stageSelectedFiles()",
		"body.set('draft_attachments',JSON.stringify(draftAttachments))",
		"navigator.mediaDevices.getUserMedia",
		"clipRecorder.start(1000)",
		"},300000)",
		"stageFiles([file])",
	)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		auth.CSRFTokenFieldName: auth.CSRFToken("session"),
		"initial_comment":       "Two staged files with one message",
	} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []struct{ name, contents string }{
		{name: "first.txt", contents: "first contents"},
		{name: "second.txt", contents: "second contents"},
	} {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(file.contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/app/file?channel=Cdev", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body)
	}
	history, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Text != "Two staged files with one message" || len(history.Messages[0].Files) != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	if history.Messages[0].Files[0].Name != "first.txt" || history.Messages[0].Files[1].Name != "second.txt" {
		t.Fatalf("files=%+v", history.Messages[0].Files)
	}
	if _, err := s.GetDraft(context.Background(), "T1", "U1", "Cdev", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sent attachment message left its old draft: %v", err)
	}
}

func TestComposerDraftAttachmentsSurviveReloadAndSendOnce(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		auth.CSRFTokenFieldName: auth.CSRFToken("session"),
		"text":                  "recover this text and its files",
		"draft_attachments":     "[]",
	} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []struct{ name, contents string }{
		{name: "durable-one.txt", contents: "first durable blob"},
		{name: "durable-two.txt", contents: "second durable blob"},
	} {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(file.contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/app/file/stage?channel=Cdev", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("HX-Request", "true")
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("stage status=%d body=%s", response.Code, response.Body)
	}
	var staged struct {
		Attachments []draftAttachmentView `json:"attachments"`
	}
	if err := json.NewDecoder(response.Body).Decode(&staged); err != nil || len(staged.Attachments) != 2 {
		t.Fatalf("staged=%+v err=%v", staged, err)
	}
	draft, err := (service.Messages{Store: s}).Draft(context.Background(), "T1", "U1", "Cdev", "")
	if err != nil || draft.Text != "recover this text and its files" || len(draft.Attachments) != 2 {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	if history, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10}); err != nil || len(history.Messages) != 0 {
		t.Fatalf("staging posted early: history=%+v err=%v", history, err)
	}
	reloaded := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "DRAFT-01 durable attachment reload", reloaded.Body.String(),
		"recover this text and its files",
		"durable-one.txt",
		"durable-two.txt",
		"has a draft",
		`class="draft-badge"`,
	)
	drafts := get(t, mux, "/app/drafts?channel=Cdev&tab=drafts")
	requireContains(t, "DRAFT-02 attachment count", drafts.Body.String(), "2 attachments", "recover this text and its files")

	encoded, err := json.Marshal(staged.Attachments)
	if err != nil {
		t.Fatal(err)
	}
	sent := postForm(t, mux, "/app/message?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"recover this text and its files"},
		"draft_attachments":     {string(encoded)},
	}.Encode(), false)
	if sent.Code != http.StatusSeeOther {
		t.Fatalf("send status=%d body=%s", sent.Code, sent.Body)
	}
	history, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 || len(history.Messages[0].Files) != 2 || history.Messages[0].Text != "recover this text and its files" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	if _, err := s.GetDraft(context.Background(), "T1", "U1", "Cdev", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sent draft remains: %v", err)
	}
}

// TestThreadRepliesRenderThroughTheSameTypeAsTheTimeline pins the invariant
// behind the fix: one type feeds every message region, so no region can be
// rendered with a value that is missing a field the partial needs.
func TestThreadRepliesRenderThroughTheSameTypeAsTheTimeline(t *testing.T) {
	list := messageList{
		Messages:    []messageView{{ID: "M1", AuthorName: "developer", AuthorInitial: "D", Text: "hello", Anchor: "message-M1"}},
		ChannelName: "general",
		CSRFToken:   "token",
		CanReact:    true,
		CanPin:      true,
	}
	var timeline, thread bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&timeline, "messages", list); err != nil {
		t.Fatalf("timeline fragment: %v", err)
	}
	if err := pageTemplate.ExecuteTemplate(&thread, "messages", list); err != nil {
		t.Fatalf("thread fragment: %v", err)
	}
	if timeline.String() != thread.String() || !strings.Contains(timeline.String(), `value="token"`) {
		t.Fatalf("timeline=%s thread=%s", timeline.String(), thread.String())
	}
}

// TestTimelineRendersTheNewestMessagesWithWorkingPagination covers the defect
// that made recent history unreachable: the page asked for the first 100
// messages of an ascending conversation and discarded the cursor.
func TestTimelineRendersTheNewestMessagesWithWorkingPagination(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeChannelsHistory)})
	base := time.Unix(1700000000, 0).UTC()
	for index := 1; index <= 130; index++ {
		seedMessage(t, s, domain.MessageID(fmt.Sprintf("M%03d", index)), fmt.Sprintf("note-%03d", index), base.Add(time.Duration(index)*time.Second))
	}
	response := get(t, mux, "/app?channel=Cdev")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "latest timeline", body, "note-130", "note-081", "Show older messages")
	requireMissing(t, "latest timeline", body, "note-001", "note-080", "Jump to the latest messages")

	older := regexp.MustCompile(`<a href="(/app\?before=[^"]+)">Show older messages</a>`).FindStringSubmatch(body)
	if older == nil {
		t.Fatalf("no older-history link: %s", body)
	}
	previous := get(t, mux, strings.ReplaceAll(older[1], "&amp;", "&"))
	if previous.Code != http.StatusOK {
		t.Fatalf("older history status=%d body=%s", previous.Code, previous.Body)
	}
	requireContains(t, "older history", previous.Body.String(), "note-031", "note-080", "Jump to the latest messages", "Show older messages")
	requireMissing(t, "older history", previous.Body.String(), "note-130", "note-081")
}

// TestUnknownChannelIsReportedAsNotFound covers the defect that answered a
// stale bookmark with a 503 blaming the message store.
func TestUnknownChannelIsReportedAsNotFound(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	response := get(t, mux, "/app?channel=Cmissing")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "not-found page", response.Body.String(), "That conversation is not available", `href="/app"`)
}

// TestWorkspaceShellNamesConversationsAndAuthors covers the defect that put raw
// identifiers where names belong: the header, the document title, the composer
// placeholder, the author line and the avatar tile.
func TestWorkspaceShellNamesConversationsAndAuthors(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	response := get(t, mux, "/app?channel=Cdev")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "workspace shell", body,
		`<h1 class="channel-title"># general</h1>`,
		"<title>#general · SameOldChat</title>",
		`placeholder="Message #general"`,
		`role="toolbar" aria-label="Message formatting and insertions"`,
		`aria-label="Mention a person or user group"`,
		`data-mention-user="U1"`,
		`id="upload-preview" role="status">No files selected. You can also paste or drop files into the composer.`,
		`<span class="author">Ada Developer</span>`,
		`<div class="avatar" aria-hidden="true">A</div>`,
		`<span class="signed-in-avatar" aria-hidden="true">A</span>`,
	)
	requireMissing(t, "workspace shell", body, "# Cdev", "Message #Cdev", ">U1<")
	// The machine timestamp stays in datetime= while the reader sees a short time.
	requireContains(t, "message time", body, `datetime="2023-11-14T22:13:20Z">Nov 14, 22:13 UTC<`)
}

func TestWorkspaceRendersEphemeralAppResponsesOnlyToTheirRecipient(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "helper-bot", RealName: "Helper Bot"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "UOTHER", WorkspaceID: "T1", Name: "other", RealName: "Other Reader"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cdev", "UBOT"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cdev", "UOTHER"); err != nil {
		t.Fatal(err)
	}
	if _, err := (service.Messages{Store: s}).PostEphemeralWithBlocksAndAttachments(
		context.Background(), "T1", "UBOT", "Cdev", "U1", "Private result",
		`[{"type":"section","text":{"type":"plain_text","text":"Build is ready"}},{"type":"actions","block_id":"private-result","elements":[{"type":"button","action_id":"acknowledge","text":{"type":"plain_text","text":"Acknowledge"},"value":"yes"}]}]`, "", "A1",
	); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "recipient workspace", body, "Build is ready", "Only visible to you", "Private message only visible to you", "Acknowledge", `action="/app/interaction"`)
	requireMissing(t, "ephemeral controls", body, "Reply in thread")

	if err := s.SeedSession(context.Background(), "other-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "UOTHER", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/app?channel=Cdev", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "other-session"})
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("other reader status=%d body=%s", response.Code, response.Body)
	}
	requireMissing(t, "non-recipient workspace", response.Body.String(), "Build is ready", "Only visible to you")
}

// A public channel may be read before it is joined, but every conversational
// mutation requires membership. The workspace used to ignore that distinction:
// it rendered a working-looking composer, reaction inputs, pin controls, and an
// automatic read marker, then refused each action after the user tried it.
func TestPublicChannelPreviewJoinsBeforeOfferingMutationControls(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "M1", "readable before joining", time.Unix(1700000000, 0).UTC())
	if err := (service.Messages{Store: s}).LeaveConversation(context.Background(), "T1", "U1", "Cdev"); err != nil {
		t.Fatal(err)
	}

	preview := get(t, mux, "/app?channel=Cdev")
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body)
	}
	requireContains(t, "public-channel preview", preview.Body.String(),
		"readable before joining",
		`<span class="membership-pill">Not joined</span>`,
		`action="/app/join?channel=Cdev"`,
		"Join channel",
		"View thread",
	)
	requireMissing(t, "public-channel preview", preview.Body.String(),
		`id="composer"`,
		`id="mark-read"`,
		`aria-label="Add reaction"`,
		`>Pin</button>`,
	)

	joined := postForm(t, mux, "/app/join?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), false)
	if joined.Code != http.StatusSeeOther {
		t.Fatalf("join status=%d body=%s", joined.Code, joined.Body)
	}
	workspace := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "joined channel", workspace.Body.String(),
		`<span class="membership-pill joined">Joined</span>`,
		`id="composer"`,
		"Reply in thread",
	)
}

// TestSidebarSeparatesDirectMessagesAndClearsTheOpenChannelBadge covers two
// defects: every direct conversation was listed as "# direct" among the
// channels, and the channel being read kept its own unread badge.
func TestSidebarSeparatesDirectMessagesAndClearsTheOpenChannelBadge(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	s.SeedConversation(domain.Conversation{ID: "Cother", WorkspaceID: "T1", Name: "release"})
	s.SeedConversation(domain.Conversation{ID: "Cdm", WorkspaceID: "T1", Name: "direct", IsDirect: true, IsPrivate: true})
	s.SeedConversationMember("Cdm", "U1")
	s.SeedConversationMember("Cdm", "U2")
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	other := domain.Message{ID: "M2", WorkspaceID: "T1", Conversation: "Cother", AuthorID: "U1", Text: "unread one", CreatedAt: time.Unix(1700000100, 0).UTC()}
	if err := s.CreateMessage(context.Background(), other, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: other.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	response := get(t, mux, "/app?channel=Cdev")
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	requireContains(t, "sidebar", body,
		`aria-label="Direct messages"`,
		`aria-label="Bob Builder"`,
		`aria-label="release, 1 unread messages"`,
	)
	requireMissing(t, "sidebar", body, `>direct<`, `aria-label="general, `)
}

func TestActivityShowsDurableMentionWithFiltersAndTriage(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cdev", "U2"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1700000200, 0).UTC()
	message := domain.Message{ID: "Mmention", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U2", Text: "Please review this, <@U1>", CreatedAt: created}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "Emention", WorkspaceID: "T1", Topic: "message.created", Payload: "Mmention", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	s.SeedConversation(domain.Conversation{ID: "Cprivate", WorkspaceID: "T1", Name: "launch-room", IsPrivate: true})
	s.SeedConversationMember("Cprivate", "U2")
	if err := s.InviteConversationMembers(context.Background(), "Cprivate", []domain.UserID{"U1"}, events.Event{
		ID: "Einvitation", WorkspaceID: "T1", ActorID: "U2", Topic: "conversation.members_invited", CreatedAt: created.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	workspace := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "activity navigation", workspace,
		`id="activity-link"`,
		`href="/app/activity?channel=Cdev"`,
		`aria-keyshortcuts="Control+3 Control+Shift+3"`,
	)
	activity := get(t, mux, "/app/activity?channel=Cdev")
	if activity.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", activity.Code, activity.Body)
	}
	requireContains(t, "activity page", activity.Body.String(),
		"<title>Activity · SameOldChat</title>",
		`aria-label="Activity filters"`,
		">Unread</a>",
		">Cleared</a>",
		">Detailed</button>",
		">Dense</button>",
		"#general",
		"Mentions",
		"Invitations",
		"Bob Builder",
		"Please review this",
		"Added you to #launch-room.",
		`class="slack-mention">@Ada Developer</span>`,
		`data-read-button`,
		`data-clear-button`,
	)
	invitations := get(t, mux, "/app/activity?channel=Cdev&kind=invitation")
	requireContains(t, "invitation activity filter", invitations.Body.String(),
		`aria-current="page">Invitations</a>`,
		`href="/app?channel=Cprivate"`,
		"Added you to #launch-room.",
	)
	requireContains(t, "activity shortcut", progressiveEnhancementScript, "key==='3'", "activityLink", "window.location.assign(activityHref)")
}

func TestComposerUserGroupMentionRendersAndNotifiesEligibleMembers(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000250, 0).UTC()
	group := domain.UserGroup{
		ID: "SSUPPORT", WorkspaceID: "T1", Name: "Support rotation", Handle: "support",
		Description: "People handling the current support queue", Creator: "U1", UpdatedBy: "U1",
		CreatedAt: now, UpdatedAt: now, Enabled: true,
	}
	if err := s.CreateUserGroup(context.Background(), group, events.Event{ID: "Egroup", WorkspaceID: "T1", Topic: "usergroup.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserGroupUsers(context.Background(), "T1", group.ID, []domain.UserID{"U2"}, "U1", events.Event{ID: "Egroup-users", WorkspaceID: "T1", Topic: "usergroup.users_changed", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	page := get(t, mux, "/app?channel=Cdev")
	if page.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", page.Code, page.Body)
	}
	requireContains(t, "user group composer option", page.Body.String(),
		`data-mention-group="SSUPPORT"`,
		`data-mention-name="support"`,
		"Support rotation · 1 members",
		`aria-label="Mention a person or user group"`,
	)
	requireContains(t, "user group transport selection", progressiveEnhancementScript,
		"'<!subteam^'+group+'>'",
		"[data-mention-user],[data-mention-group]",
	)

	result := postForm(t, mux, "/app/message?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"Please review <!subteam^SSUPPORT>"},
	}.Encode(), false)
	if result.Code != http.StatusSeeOther {
		t.Fatalf("post status=%d body=%s", result.Code, result.Body)
	}
	rendered := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "rendered user group handle", rendered.Body.String(),
		`class="slack-mention">@support</span>`,
	)
	requireMissing(t, "raw user group identifier", rendered.Body.String(), "@subteam", "@SSUPPORT")

	activity, err := s.ListActivity(context.Background(), "T1", "U2", domain.ActivityQuery{
		Kinds: []domain.ActivityKind{domain.ActivityMention}, Page: domain.PageRequest{Limit: 10},
	})
	if err != nil || len(activity.Items) != 1 || !activity.Items[0].SourceAvailable {
		t.Fatalf("group mention activity=%+v err=%v", activity, err)
	}

	if err := s.SetUserGroupEnabled(context.Background(), "T1", group.ID, false, "U1", events.Event{ID: "Egroup-disabled", WorkspaceID: "T1", Topic: "usergroup.enabled_changed", CreatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	disabled := get(t, mux, "/app?channel=Cdev")
	requireMissing(t, "disabled groups are not mentionable", disabled.Body.String(), `data-mention-group="SSUPPORT"`)
}

func TestComposerUserGroupSuggestionsPageThroughTheWorkspaceCatalog(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	now := time.Unix(1700000250, 0).UTC()
	for index := 0; index <= memberWindow; index++ {
		id := domain.UserGroupID(fmt.Sprintf("S%03d", index))
		if err := s.CreateUserGroup(context.Background(), domain.UserGroup{
			ID: id, WorkspaceID: "T1", Name: fmt.Sprintf("Group %03d", index), Handle: fmt.Sprintf("group-%03d", index),
			Creator: "U1", UpdatedBy: "U1", CreatedAt: now, UpdatedAt: now, Enabled: true,
		}, events.Event{ID: domain.EventID(fmt.Sprintf("Egroup-%03d", index)), WorkspaceID: "T1", Topic: "usergroup.created", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	page := get(t, mux, "/app?channel=Cdev")
	if page.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", page.Code, page.Body)
	}
	requireContains(t, "user groups beyond the first store page", page.Body.String(),
		`data-mention-group="S100"`,
		`data-mention-name="group-100"`,
	)
}

func TestResolveSlackReferenceJSONLeavesOpaqueValuesAndPlainTextLiteral(t *testing.T) {
	names := &userNames{
		cache: map[domain.UserID]string{"U1": "Ada"},
		groups: map[domain.UserGroupID]string{
			"SSUPPORT": "support",
		},
		groupsLoaded: true,
	}
	raw := `[
		{"type":"section","text":{"type":"mrkdwn","text":"notify <!subteam^SSUPPORT>"}},
		{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"literal <!subteam^SSUPPORT>"},"value":"route-<!subteam^SSUPPORT>"}]}
	]`
	resolved := resolveSlackReferenceJSON(raw, names)
	requireContains(t, "mrkdwn reference rendering", resolved, `notify \u003c!subteam^SSUPPORT|@support\u003e`)
	requireContains(t, "opaque Block Kit fields", resolved,
		`"text":"literal \u003c!subteam^SSUPPORT\u003e"`,
		`"value":"route-\u003c!subteam^SSUPPORT\u003e"`,
	)
}

func TestActivityPersistsClearRestoreReadAndLayoutActions(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cdev", "U2"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1700000300, 0).UTC()
	message := domain.Message{ID: "Mactivity-actions", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U2", Text: "<@U1> triage me", CreatedAt: created}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "Eactivity-actions", WorkspaceID: "T1", Topic: "message.created", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	id := domain.ActivityIDFor("U1", "message:"+string(message.ID))
	active := get(t, mux, "/app/activity?channel=Cdev")
	reactionURL := "/app/reaction?channel=Cdev&amp;ts=" + url.QueryEscape(string(domain.NewMessageTimestamp(created)))
	requireContains(t, "Activity message actions", active.Body.String(),
		"triage me",
		`data-activity-react="`+reactionURL+`"`,
		`id="activity-reaction-dialog"`,
		`id="activity-reaction-category"`,
		`id="activity-reaction-tone"`,
	)
	reaction := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+url.QueryEscape(string(domain.NewMessageTimestamp(created))), url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "name": {"wave::skin-tone-3"},
	}.Encode(), true)
	if reaction.Code != http.StatusNoContent {
		t.Fatalf("Activity reaction status=%d body=%s", reaction.Code, reaction.Body)
	}
	renderedMessage := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "Activity reaction persisted", renderedMessage, `aria-label=":wave::skin-tone-3:">👋🏼</span>`)

	clear := postForm(t, mux, "/app/activity/mutate?channel=Cdev&mutation=clear", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "single_id": {string(id)},
	}.Encode(), false)
	if clear.Code != http.StatusSeeOther {
		t.Fatalf("clear status=%d body=%s", clear.Code, clear.Body)
	}
	active = get(t, mux, "/app/activity?channel=Cdev")
	requireMissing(t, "cleared item leaves active Activity", active.Body.String(), "triage me")
	cleared := get(t, mux, "/app/activity?channel=Cdev&cleared=1")
	requireContains(t, "cleared item is recoverable", cleared.Body.String(), "triage me", "Restore")

	restore := postForm(t, mux, "/app/activity/mutate?channel=Cdev&mutation=restore", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "single_id": {string(id)}, "cleared": {"1"},
	}.Encode(), false)
	if restore.Code != http.StatusSeeOther {
		t.Fatalf("restore status=%d body=%s", restore.Code, restore.Body)
	}
	unread := get(t, mux, "/app/activity?channel=Cdev&unread=1")
	requireMissing(t, "restoring does not undo clear-implied read", unread.Body.String(), "triage me")
	markUnread := postForm(t, mux, "/app/activity/mutate?channel=Cdev&mutation=unread", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "single_id": {string(id)},
	}.Encode(), false)
	if markUnread.Code != http.StatusSeeOther {
		t.Fatalf("mark unread status=%d body=%s", markUnread.Code, markUnread.Body)
	}
	unread = get(t, mux, "/app/activity?channel=Cdev&unread=1")
	requireContains(t, "mark unread restores unread Activity", unread.Body.String(), "triage me", "Mark this activity read")

	layout := postForm(t, mux, "/app/activity/preferences?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "layout": {"dense"}, "kind": {"mention"}, "unread": {"1"},
	}.Encode(), false)
	if layout.Code != http.StatusSeeOther {
		t.Fatalf("layout status=%d body=%s", layout.Code, layout.Body)
	}
	if location := layout.Header().Get("Location"); location != "/app/activity?channel=Cdev&kind=mention&unread=1" {
		t.Fatalf("layout redirect lost Activity filters: %q", location)
	}
	dense := get(t, mux, "/app/activity?channel=Cdev&kind=mention&unread=1")
	requireContains(t, "dense layout persisted", dense.Body.String(), `class="activity-list dense"`, `value="dense"><button type="submit" aria-pressed="true"`)
	requireContains(t, "Activity keyboard contract", activityMarkup,
		"event.key==='ArrowDown'", "event.key==='ArrowUp'", "event.key==='Enter'",
		"event.key==='x'", "event.key==='c'", "event.key==='r'",
		"new EventSource('/events'",
		"data-activity-id",
		"focusedID",
		"focus({preventScroll:true})",
		"feed.replaceWith(replacement)",
	)
}

func TestNotificationPreferencesDNDConversationExceptionAndThreadFollowJourney(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	notifications := get(t, mux, "/app/notifications?channel=Cdev")
	if notifications.Code != http.StatusOK {
		t.Fatalf("notifications status=%d body=%s", notifications.Code, notifications.Body)
	}
	requireContains(t, "notification preferences", notifications.Body.String(),
		"Notification preferences", "Mentions and direct messages", "Channel keywords",
		"Show channels set to All new posts in Activity", "Pause notifications",
		"No conversation-specific exceptions",
	)

	saved := postForm(t, mux, "/app/notifications/preferences?channel=Cdev", url.Values{
		"_csrf":             {auth.CSRFToken("session")},
		"level":             {"all"},
		"keywords":          {" Release , customer escalation, RELEASE "},
		"activity_channels": {"true"},
	}.Encode(), false)
	if saved.Code != http.StatusSeeOther || !strings.Contains(saved.Header().Get("Location"), "status=saved") {
		t.Fatalf("save status=%d location=%q body=%s", saved.Code, saved.Header().Get("Location"), saved.Body)
	}
	preferences, err := s.GetWorkspaceNotificationPreferences(context.Background(), "T1", "U1")
	if err != nil || preferences.Level != domain.NotificationAll || len(preferences.Keywords) != 2 || preferences.ActivityReminders {
		t.Fatalf("workspace preferences=%+v err=%v", preferences, err)
	}

	exception := postForm(t, mux, "/app/conversation/notifications?channel=Cdev", url.Values{
		"_csrf":               {auth.CSRFToken("session")},
		"level":               {"mute"},
		"follow_every_thread": {"true"},
	}.Encode(), false)
	if exception.Code != http.StatusSeeOther {
		t.Fatalf("exception status=%d body=%s", exception.Code, exception.Body)
	}
	notifications = get(t, mux, "/app/notifications?channel=Cdev")
	requireContains(t, "notification exception list", notifications.Body.String(), "#general", "mute", "following every thread")
	details := get(t, mux, "/app?channel=Cdev&details=1")
	requireContains(t, "conversation notification controls", details.Body.String(),
		`id="conversation-notifications"`, `<option value="mute" selected>Mute conversation</option>`,
		`name="follow_every_thread" value="true" checked`,
	)

	paused := postForm(t, mux, "/app/notifications/dnd?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "action": {"pause"}, "minutes": {"30"},
	}.Encode(), false)
	if paused.Code != http.StatusSeeOther || !strings.Contains(paused.Header().Get("Location"), "status=paused") {
		t.Fatalf("pause status=%d location=%q body=%s", paused.Code, paused.Header().Get("Location"), paused.Body)
	}
	dnd, err := s.GetDoNotDisturb(context.Background(), "T1", "U1")
	if err != nil || !dnd.SnoozeUntil.After(time.Now()) {
		t.Fatalf("dnd=%+v err=%v", dnd, err)
	}
	resumed := postForm(t, mux, "/app/notifications/dnd?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "action": {"resume"},
	}.Encode(), false)
	if resumed.Code != http.StatusSeeOther {
		t.Fatalf("resume status=%d body=%s", resumed.Code, resumed.Body)
	}
	resetFollowAll := postForm(t, mux, "/app/conversation/notifications?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "level": {"mute"},
	}.Encode(), false)
	if resetFollowAll.Code != http.StatusSeeOther {
		t.Fatalf("reset follow-every-thread status=%d body=%s", resetFollowAll.Code, resetFollowAll.Body)
	}

	root := seedMessage(t, s, "Mfollow-root", "follow this thread", time.Now().UTC().Add(-time.Minute))
	rootTimestamp := domain.NewMessageTimestamp(root.CreatedAt)
	threadPage := get(t, mux, "/app?channel=Cdev&thread="+url.QueryEscape(string(rootTimestamp)))
	requireContains(t, "thread author follows by default", threadPage.Body.String(), ">Following</button>", `aria-pressed="true"`)
	unfollowed := postForm(t, mux, "/app/thread/follow?channel=Cdev&thread="+url.QueryEscape(string(rootTimestamp)), url.Values{
		"_csrf": {auth.CSRFToken("session")}, "followed": {"false"},
	}.Encode(), false)
	if unfollowed.Code != http.StatusSeeOther {
		t.Fatalf("unfollow status=%d body=%s", unfollowed.Code, unfollowed.Body)
	}
	if stored, err := s.IsThreadFollowed(context.Background(), "T1", "U1", "Cdev", rootTimestamp); err != nil || stored {
		t.Fatalf("followed after unfollow=%v err=%v", stored, err)
	}
	threadPage = get(t, mux, "/app?channel=Cdev&thread="+url.QueryEscape(string(rootTimestamp)))
	requireContains(t, "thread unfollow persisted", threadPage.Body.String(), ">Follow thread</button>", `aria-pressed="false"`)
	followed := postForm(t, mux, "/app/thread/follow?channel=Cdev&thread="+url.QueryEscape(string(rootTimestamp)), url.Values{
		"_csrf": {auth.CSRFToken("session")}, "followed": {"true"},
	}.Encode(), false)
	if followed.Code != http.StatusSeeOther {
		t.Fatalf("follow status=%d body=%s", followed.Code, followed.Body)
	}
	if stored, err := s.IsThreadFollowed(context.Background(), "T1", "U1", "Cdev", rootTimestamp); err != nil || !stored {
		t.Fatalf("followed=%v err=%v", stored, err)
	}
	threadPage = get(t, mux, "/app?channel=Cdev&thread="+url.QueryEscape(string(rootTimestamp)))
	requireContains(t, "thread follow persisted", threadPage.Body.String(), ">Following</button>", `aria-pressed="true"`)
}

// TestNarrowNavigationKeepsConversationNamesReachable covers the responsive
// rail that rendered every channel as the same # glyph and every DM as the same
// @ glyph. Accessible names alone did not let a sighted mobile reader choose a
// conversation; the narrow shell now opens the full named navigation as a
// focus-managed drawer.
func TestNarrowNavigationKeepsConversationNamesReachable(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	tags := regexp.MustCompile(`<(?:a|button)[^>]*class="side-link"[^>]*>`).FindAllString(body, -1)
	if len(tags) < 3 {
		t.Fatalf("expected the members, channel and sign-out controls, found %d: %s", len(tags), body)
	}
	for _, tag := range tags {
		if !strings.Contains(tag, "aria-label=") {
			t.Fatalf("collapsed sidebar control has no accessible name: %s", tag)
		}
	}
	requireContains(t, "navigation drawer", body,
		`id="nav-toggle"`,
		`aria-controls="workspace-sidebar"`,
		`aria-label="Open navigation"`,
		`id="workspace-sidebar"`,
		`.sidebar.is-open{transform:translateX(0)}`,
		`.side-label,.side-text,.signed-in-name{display:block}`,
	)
	requireMissing(t, "navigation drawer", body, `.side-text,.signed-in-name{display:none}`)
	if strings.Contains(body, ".thread{display:none}") {
		t.Fatal("narrow viewports delete the thread pane instead of reflowing it")
	}
}

// TestReactionsAndPinsAreRenderedAndReversible covers the defect where both
// controls mutated durable state that no template ever displayed.
func TestReactionsAndPinsAreRenderedAndReversible(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	reaction := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp, "name=%3Awave%3A", true)
	if reaction.Code != http.StatusNoContent {
		t.Fatalf("reaction status=%d body=%s", reaction.Code, reaction.Body)
	}
	pin := postForm(t, mux, "/app/pin?channel=Cdev&ts="+timestamp, "", false)
	if pin.Code != http.StatusSeeOther {
		t.Fatalf("pin status=%d body=%s", pin.Code, pin.Body)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "message", body,
		`aria-pressed="true"`,
		":wave:",
		`<span class="chip-count">1</span>`,
		`<span class="pinned">Pinned</span>`,
		">Unpin<",
	)

	removal := postForm(t, mux, "/app/reaction/remove?channel=Cdev&ts="+timestamp, "name=%3Awave%3A", true)
	if removal.Code != http.StatusNoContent {
		t.Fatalf("reaction removal status=%d body=%s", removal.Code, removal.Body)
	}
	unpin := postForm(t, mux, "/app/pin/remove?channel=Cdev&ts="+timestamp, "", true)
	if unpin.Code != http.StatusNoContent {
		t.Fatalf("unpin status=%d body=%s", unpin.Code, unpin.Body)
	}
	after := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "message", after, `<span class="chip-count">`, `<span class="pinned">Pinned</span>`)
	reactions, _, _, err := s.ListReactions(context.Background(), "M1", domain.PageRequest{Limit: 10})
	if err != nil || len(reactions) != 0 {
		t.Fatalf("reactions=%+v err=%v", reactions, err)
	}
}

func TestComposerAndMessagesUseWorkspaceEmojiAndVisibleChannelReferences(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	custom := domain.CustomEmoji{WorkspaceID: "T1", Name: "party_parrot", URL: "https://cdn.example/party.png"}
	event := events.Event{ID: "Eemoji", WorkspaceID: "T1", Topic: "emoji.added", CreatedAt: time.Now().UTC()}
	if err := s.AddEmoji(context.Background(), custom, event); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "Memoji", "Ship it :party_parrot: :tada: in <#Cdev>", created)

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "emoji composer and rendering", body,
		`id="emoji-picker-dialog"`,
		`id="emoji-picker-category"`,
		`id="emoji-picker-tone"`,
		`data-channel-id="Cdev"`,
		`data-channel-name="general"`,
		`class="custom-emoji" src="https://cdn.example/party.png" alt=":party_parrot:"`,
		`aria-label=":tada:"`,
		`class="slack-mention">#general</span>`,
	)
	requireMissing(t, "rendered channel reference", body, `class="message-text">Ship it :party_parrot:`)

	options := get(t, mux, "/app/emoji/options?q=party_parrot")
	if options.Code != http.StatusOK {
		t.Fatalf("options status=%d body=%s", options.Code, options.Body)
	}
	requireContains(t, "emoji options", options.Body.String(),
		`"name":"party_parrot"`,
		`"image_url":"https://cdn.example/party.png"`,
		`"category":"Custom"`,
		`"categories":["Recent","Custom"`,
	)

	timestamp := string(domain.NewMessageTimestamp(created))
	added := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp, "name=%3Aparty_parrot%3A", true)
	if added.Code != http.StatusNoContent {
		t.Fatalf("custom reaction status=%d body=%s", added.Code, added.Body)
	}
	reactionBody := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "custom reaction", reactionBody,
		`aria-label="Remove your party_parrot reaction`,
		`src="https://cdn.example/party.png" alt=":party_parrot:"`,
	)

	invalid := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp, "name=not_a_real_emoji", true)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("unknown reaction status=%d body=%s", invalid.Code, invalid.Body)
	}

	toned := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp, "name=wave%3A%3Askin-tone-3", true)
	if toned.Code != http.StatusNoContent {
		t.Fatalf("skin-tone reaction status=%d body=%s", toned.Code, toned.Body)
	}
	tonedBody := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "skin-tone reaction", tonedBody, `aria-label=":wave::skin-tone-3:">👋🏼</span>`)
}

// Editing and deleting were complete Slack API operations with no browser
// journey. Rendering the controls only on the signed-in author's messages is a
// convenience; the mutation handlers also enforce ownership server-side.
func TestOwnMessageCanBeEditedAndDeleted(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "before", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "own message", body,
		`aria-label="Edit message"`,
		`action="/app/message/update?channel=Cdev&amp;ts=`+timestamp,
		`aria-label="Delete message"`,
	)

	updated := postForm(t, mux, "/app/message/update?channel=Cdev&ts="+timestamp, "text=after", true)
	if updated.Code != http.StatusNoContent {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	message, err := s.GetMessage(context.Background(), "M1")
	if err != nil || message.Text != "after" {
		t.Fatalf("updated message=%+v err=%v", message, err)
	}

	deleted := postForm(t, mux, "/app/message/delete?channel=Cdev&ts="+timestamp, "", true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body)
	}
	after := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "deleted message", after, `data-message-id="M1"`)
}

func TestAnotherMembersMessageCannotBeChanged(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	created := time.Unix(1700000000, 123456000).UTC()
	message := domain.Message{ID: "M2", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U2", Text: "belongs to Bob", CreatedAt: created}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	timestamp := string(domain.NewMessageTimestamp(created))

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "another member's message", body, `action="/app/message/update?channel=Cdev&amp;ts=`+timestamp, `action="/app/message/delete?channel=Cdev&amp;ts=`+timestamp)
	refused := postForm(t, mux, "/app/message/update?channel=Cdev&ts="+timestamp, "text=stolen", true)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("update status=%d body=%s", refused.Code, refused.Body)
	}
	stored, err := s.GetMessage(context.Background(), "M2")
	if err != nil || stored.Text != "belongs to Bob" {
		t.Fatalf("message=%+v err=%v", stored, err)
	}
}

// Channel creation existed at /api/conversations.create but the signed-in
// workspace did not expose it, leaving users dependent on an API client for a
// basic product journey.
func TestWorkspaceCanCreateAChannel(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "workspace", body, `action="/app/conversation/create"`, `hx-post="/app/conversation/create"`, `name="is_private"`, "Add channel")

	created := postForm(t, mux, "/app/conversation/create", "name=Product+Launch&is_private=true", false)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	location := created.Header().Get("Location")
	target, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	channel := domain.ConversationID(target.Query().Get("channel"))
	conversation, err := s.GetConversation(context.Background(), channel)
	if err != nil || conversation.Name != "product-launch" || !conversation.IsPrivate {
		t.Fatalf("conversation=%+v err=%v", conversation, err)
	}
	member, err := s.IsConversationMember(context.Background(), channel, "U1")
	if err != nil || !member {
		t.Fatalf("member=%t err=%v", member, err)
	}

	enhanced := postForm(t, mux, "/app/conversation/create", "name=Enhanced", true)
	if enhanced.Code != http.StatusNoContent {
		t.Fatalf("enhanced create status=%d body=%s", enhanced.Code, enhanced.Body)
	}
	if target := enhanced.Header().Get("HX-Redirect"); !strings.Contains(target, "channel=") {
		t.Fatalf("enhanced create did not name its destination: %q", target)
	}
}

func TestConversationDetailsManageTheWholeChannelJourney(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "builder", RealName: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "reviewer", RealName: "Rae Reviewer"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cdev", "U2"); err != nil {
		t.Fatal(err)
	}

	body := get(t, mux, "/app?channel=Cdev&details=1").Body.String()
	requireContains(t, "conversation details", body,
		`role="dialog" aria-modal="true" aria-labelledby="conversation-details-title"`,
		"Everything else",
		"Bob Builder",
		"Rae Reviewer",
		`action="/app/conversation/invite?channel=Cdev"`,
		`action="/app/conversation/rename?channel=Cdev"`,
		`action="/app/conversation/topic?channel=Cdev"`,
		`action="/app/conversation/purpose?channel=Cdev"`,
		`action="/app/conversation/archive?channel=Cdev"`,
		`action="/app/conversation/leave?channel=Cdev"`,
	)

	for _, mutation := range []struct {
		target string
		body   string
	}{
		{target: "/app/conversation/rename?channel=Cdev", body: "name=Product+Launch"},
		{target: "/app/conversation/topic?channel=Cdev", body: "topic=Shipping+this+week"},
		{target: "/app/conversation/purpose?channel=Cdev", body: "purpose=Coordinate+the+release"},
		{target: "/app/conversation/invite?channel=Cdev", body: "user=U3"},
	} {
		result := postForm(t, mux, mutation.target, mutation.body, false)
		if result.Code != http.StatusSeeOther || !strings.Contains(result.Header().Get("Location"), "details=1") {
			t.Fatalf("%s status=%d location=%q body=%s", mutation.target, result.Code, result.Header().Get("Location"), result.Body)
		}
	}
	conversation, err := s.GetConversation(context.Background(), "Cdev")
	if err != nil || conversation.Name != "product-launch" || conversation.Topic != "Shipping this week" || conversation.Purpose != "Coordinate the release" {
		t.Fatalf("conversation=%+v err=%v", conversation, err)
	}
	member, err := s.IsConversationMember(context.Background(), "Cdev", "U3")
	if err != nil || !member {
		t.Fatalf("invited member=%t err=%v", member, err)
	}

	archived := postForm(t, mux, "/app/conversation/archive?channel=Cdev", "archived=true", false)
	if archived.Code != http.StatusSeeOther {
		t.Fatalf("archive status=%d body=%s", archived.Code, archived.Body)
	}
	body = get(t, mux, "/app?channel=Cdev&details=1").Body.String()
	requireContains(t, "archived conversation", body, "Archived", "Unarchive channel")
	requireMissing(t, "archived conversation", body, `form class="composer`, `action="/app/conversation/invite?channel=Cdev"`)

	unarchived := postForm(t, mux, "/app/conversation/archive?channel=Cdev", "archived=false", false)
	if unarchived.Code != http.StatusSeeOther {
		t.Fatalf("unarchive status=%d body=%s", unarchived.Code, unarchived.Body)
	}
	left := postForm(t, mux, "/app/conversation/leave?channel=Cdev", "", false)
	if left.Code != http.StatusSeeOther {
		t.Fatalf("leave status=%d body=%s", left.Code, left.Body)
	}
	member, err = s.IsConversationMember(context.Background(), "Cdev", "U1")
	if err != nil || member {
		t.Fatalf("membership after leave=%t err=%v", member, err)
	}
}

// TestReactionKeepsTheOpenViewInsteadOfNavigating covers the defect where a
// reaction always answered with a full-page redirect that dropped the open
// thread and the composer draft, while declaring HTMX attributes that the
// server never honoured.
func TestReactionKeepsTheOpenViewInsteadOfNavigating(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	enhanced := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp+"&thread="+timestamp, "name=%3Awave%3A", true)
	if enhanced.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", enhanced.Code, enhanced.Body)
	}
	if redirect := enhanced.Header().Get("HX-Redirect"); redirect != "" {
		t.Fatalf("enhanced reaction still navigates the whole page to %q", redirect)
	}
	plain := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp+"&thread="+timestamp, "name=%3Atada%3A", false)
	if plain.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", plain.Code, plain.Body)
	}
	location := plain.Header().Get("Location")
	if !strings.Contains(location, "thread="+timestamp) || !strings.Contains(location, "channel=Cdev") {
		t.Fatalf("reaction redirect dropped the open thread: %q", location)
	}
}

// TestTimelineFragmentServesTheLiveRegion covers the live-delivery fix: the page
// re-renders the message regions the server owns instead of reloading and
// discarding the composer draft and the scroll position.
func TestTimelineFragmentServesTheLiveRegion(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	page := get(t, mux, "/app?channel=Cdev&thread="+timestamp).Body.String()
	for _, fragment := range regexp.MustCompile(`data-fragment="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		target := strings.ReplaceAll(fragment[1], "&amp;", "&")
		response := get(t, mux, target)
		if response.Code != http.StatusOK {
			t.Fatalf("fragment %s status=%d body=%s", target, response.Code, response.Body)
		}
		// Every live region must answer a bare fragment rather than a whole
		// document; only the message regions carry messages.
		requireMissing(t, "fragment "+target, response.Body.String(), "<html", "<body")
		if strings.HasPrefix(target, "/app/timeline") {
			requireContains(t, "fragment "+target, response.Body.String(), `class="message"`, "hello")
		}
	}
	if !strings.Contains(page, `data-live="true"`) {
		t.Fatalf("the timeline is not marked live: %s", page)
	}
}

func TestLaterJourneySavesOrganizesAndRemovesAMessage(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	message := seedMessage(t, s, "M-later", "review the release", created)
	timestamp := string(domain.NewMessageTimestamp(created))

	page := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "Later message action", page.Body.String(),
		`href="/app/later?channel=Cdev"`,
		`data-message-save`,
		`Save for later`,
		` A`,
	)
	saved := postForm(t, mux, "/app/later/save?channel=Cdev&ts="+url.QueryEscape(timestamp), "", true)
	if saved.Code != http.StatusNoContent {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body)
	}
	chat := service.Messages{Store: s}
	item, err := chat.SavedItemForMessage(context.Background(), "T1", "U1", message.ID)
	if err != nil {
		t.Fatal(err)
	}
	refreshed := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "saved message action", refreshed.Body.String(), `Remove from Later`, `aria-pressed="true"`)

	later := get(t, mux, "/app/later?channel=Cdev")
	requireContains(t, "LATER-02 In progress", later.Body.String(),
		`aria-current="page">In progress`,
		`review the release`,
		`#general`,
		`Mark complete`,
		`Archive`,
	)
	moved := postForm(t, mux, "/app/later/state?channel=Cdev&id="+url.QueryEscape(string(item.ID))+"&state=completed&return_state=in_progress", "", false)
	if moved.Code != http.StatusSeeOther {
		t.Fatalf("complete status=%d body=%s", moved.Code, moved.Body)
	}
	completed := get(t, mux, "/app/later?channel=Cdev&state=completed")
	requireContains(t, "LATER-03 Completed", completed.Body.String(), `aria-current="page">Completed`, `review the release`, `Move to in progress`)
	removed := postForm(t, mux, "/app/later/remove?channel=Cdev&id="+url.QueryEscape(string(item.ID))+"&return_state=completed", "", false)
	if removed.Code != http.StatusSeeOther {
		t.Fatalf("remove status=%d body=%s", removed.Code, removed.Body)
	}
	empty := get(t, mux, "/app/later?channel=Cdev&state=completed")
	requireContains(t, "empty Completed", empty.Body.String(), `No items in Completed.`)
	requireMissing(t, "removed Later item", empty.Body.String(), `review the release`)
}

func TestReminderJourneysCreateFromMessageAndManageInLater(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	message := seedMessage(t, s, "M-reminder", "review the launch", created)
	timestamp := domain.NewMessageTimestamp(created)

	workspace := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "REMIND-01 message action", workspace.Body.String(),
		"Remind me about this", `data-reminder-menu`, ` M`, `name="preset" value="20m"`,
		`name="preset" value="tomorrow"`, `data-browser-timezone`,
	)
	createdResponse := postForm(t, mux, "/app/reminders/create?channel=Cdev&ts="+url.QueryEscape(string(timestamp)), url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"preset":                {"20m"},
		"timezone":              {"Europe/Bucharest"},
	}.Encode(), false)
	if createdResponse.Code != http.StatusSeeOther {
		t.Fatalf("create reminder status=%d body=%s", createdResponse.Code, createdResponse.Body)
	}
	chat := service.Messages{Store: s}
	page, err := chat.LaterReminders(context.Background(), "T1", "U1", domain.LaterReminderPersonal, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("reminders=%+v err=%v", page, err)
	}
	reminder := page.Items[0]
	if reminder.SourceMessageID != message.ID || reminder.SourceConversation != "Cdev" || reminder.SourceTimestamp != timestamp || reminder.TimeZone != "Europe/Bucharest" {
		t.Fatalf("message reminder lost source/time zone: %+v", reminder)
	}
	later := get(t, mux, createdResponse.Header().Get("Location"))
	requireContains(t, "REMIND-02 Later", later.Body.String(),
		"Reminder saved.", "Message reminder", "View source message", "Mark complete",
		"Edit", "Delete reminder", "Upcoming reminders", "Add a reminder",
	)

	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	update := postForm(t, mux, "/app/reminders/update?channel=Cdev&id="+url.QueryEscape(string(reminder.ID))+"&return_state=in_progress", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"review every launch"},
		"date":                  {tomorrow.Format("2006-01-02")},
		"time":                  {"12:30"},
		"timezone":              {"UTC"},
		"recurrence":            {"weekly"},
	}.Encode(), false)
	if update.Code != http.StatusSeeOther {
		t.Fatalf("update reminder status=%d body=%s", update.Code, update.Body)
	}
	updated, err := chat.LaterReminderInfo(context.Background(), "T1", "U1", reminder.ID)
	if err != nil || updated.Text != "review every launch" || updated.Recurrence != domain.ReminderWeekly || updated.TimeZone != "UTC" {
		t.Fatalf("updated reminder=%+v err=%v", updated, err)
	}
	complete := postForm(t, mux, "/app/reminders/complete?channel=Cdev&id="+url.QueryEscape(string(reminder.ID))+"&return_state=in_progress", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
	}.Encode(), false)
	if complete.Code != http.StatusSeeOther {
		t.Fatalf("complete reminder status=%d body=%s", complete.Code, complete.Body)
	}
	completed := get(t, mux, "/app/later?channel=Cdev&state=completed")
	requireContains(t, "completed reminder", completed.Body.String(), "review every launch", "Completed", "Delete reminder")
	requireMissing(t, "completed reminder", completed.Body.String(), `action="/app/reminders/update?`)

	deleted := postForm(t, mux, "/app/reminders/delete?channel=Cdev&id="+url.QueryEscape(string(reminder.ID))+"&return_state=completed", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
	}.Encode(), false)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete reminder status=%d body=%s", deleted.Code, deleted.Body)
	}
	if _, err := chat.LaterReminderInfo(context.Background(), "T1", "U1", reminder.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted reminder lookup=%v", err)
	}
}

func TestRemindSlashCommandCreatesPrivateChannelReminderListWithoutPosting(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	response := postForm(t, mux, "/app/message?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"/remind #general deploy tomorrow at 9am"},
		"timezone":              {"Europe/Bucharest"},
	}.Encode(), false)
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "filter=channel-reminders") {
		t.Fatalf("/remind status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body)
	}
	history, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 0 {
		t.Fatalf("/remind was posted as chat: messages=%+v err=%v", history.Messages, err)
	}
	page, err := (service.Messages{Store: s}).LaterReminders(context.Background(), "T1", "U1", domain.LaterReminderChannel, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Channel != "Cdev" || page.Items[0].Text != "deploy" || page.Items[0].TimeZone != "Europe/Bucharest" {
		t.Fatalf("channel reminders=%+v err=%v", page, err)
	}
	list := get(t, mux, response.Header().Get("Location"))
	requireContains(t, "REMIND-03 private channel list", list.Body.String(),
		"Channel reminders you created", "deploy", "#general", "Delete reminder",
	)
	requireMissing(t, "channel reminder editability", list.Body.String(), "Mark complete", ">Edit<")

	listCommand := postForm(t, mux, "/app/message?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")}, "text": {"/remind list"}, "timezone": {"UTC"},
	}.Encode(), false)
	if listCommand.Code != http.StatusSeeOther || !strings.Contains(listCommand.Header().Get("Location"), "filter=channel-reminders") {
		t.Fatalf("/remind list status=%d location=%q", listCommand.Code, listCommand.Header().Get("Location"))
	}
}

func TestDeliveredPersonalReminderAppearsInActivityWithItsSource(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	messageTime := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	message := seedMessage(t, s, "M-activity-reminder", "source", messageTime)
	due := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	reminder := domain.LaterReminder{
		ID: "later_reminder_activity", WorkspaceID: "T1", Creator: "U1", UserID: "U1",
		SourceMessageID: message.ID, SourceConversation: "Cdev", SourceTimestamp: domain.NewMessageTimestamp(messageTime),
		Target: domain.LaterReminderPersonal, Text: "review the source", DueAt: due,
		TimeZone: "UTC", CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour),
	}
	if err := s.CreateLaterReminder(context.Background(), reminder, events.Event{
		ID: "event-reminder-activity", WorkspaceID: "T1", ActorID: "U1",
		Topic: "later_reminder.created", Payload: "{}", CreatedAt: reminder.CreatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	now := due.Add(time.Minute)
	worker, err := scheduler.NewReminderWorker(s, service.Messages{Store: s}, "worker", 10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(context.Background(), "T1"); err != nil || count != 1 {
		t.Fatalf("delivery count=%d err=%v", count, err)
	}
	activity := get(t, mux, "/app/activity?channel=Cdev")
	requireContains(t, "REMIND-04 Activity projection", activity.Body.String(),
		">Reminders</a>", "review the source",
		`datetime="`+now.Format(time.RFC3339)+`"`, "#message-M-activity-reminder",
		"Mark selected read",
	)
	workspace := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "REMIND-04 due badges", workspace.Body.String(),
		`aria-label="Activity, reminder due"`, `aria-label="Later, reminder due"`,
	)
	acknowledged := postForm(t, mux, "/app/activity/read?channel=Cdev", "", false)
	if acknowledged.Code != http.StatusSeeOther || acknowledged.Header().Get("Location") != "/app/activity?channel=Cdev" {
		t.Fatalf("acknowledge status=%d location=%q body=%s", acknowledged.Code, acknowledged.Header().Get("Location"), acknowledged.Body)
	}
	stored, err := (service.Messages{Store: s}).LaterReminderInfo(context.Background(), "T1", "U1", reminder.ID)
	if err != nil || !stored.AcknowledgedAt.Equal(stored.LastDeliveredAt) {
		t.Fatalf("acknowledged reminder=%+v err=%v", stored, err)
	}
	after := get(t, mux, "/app?channel=Cdev")
	requireMissing(t, "acknowledged reminder badges", after.Body.String(), "reminder due")
}

func TestChannelReminderParserRejectsAmbiguityAndPreservesCalendarMeaning(t *testing.T) {
	now := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		expression string
		text       string
		recurrence domain.ReminderRecurrence
		hour       int
		wantError  error
	}{
		{expression: "stand-up tomorrow at 9am", text: "stand-up", hour: 9},
		{expression: "stand-up in 20 minutes", text: "stand-up", hour: 8},
		{expression: "stand-up every Thursday at 9am", text: "stand-up", recurrence: domain.ReminderWeekly, hour: 9},
		{expression: "stand-up every week at 10:30", text: "stand-up", recurrence: domain.ReminderWeekly, hour: 10},
		{expression: "stand-up sometime soon", wantError: service.ErrInvalidLaterReminder},
		{expression: "stand-up at 7am", wantError: service.ErrReminderTimeInPast},
	} {
		t.Run(testCase.expression, func(t *testing.T) {
			text, due, recurrence, err := parseChannelReminderExpression(testCase.expression, now, time.UTC)
			if testCase.wantError != nil {
				if !errors.Is(err, testCase.wantError) {
					t.Fatalf("error=%v want=%v", err, testCase.wantError)
				}
				return
			}
			if err != nil || text != testCase.text || recurrence != testCase.recurrence || due.Hour() != testCase.hour || !due.After(now) {
				t.Fatalf("text=%q due=%s recurrence=%q err=%v", text, due, recurrence, err)
			}
		})
	}
}

// TestLiveUpdatesSubscribeToExactlyTheEmittedTopics covers the defect where the
// page listened for "message.updated", which nothing publishes, and therefore
// never saw an edit.
func TestLiveUpdatesSubscribeToExactlyTheEmittedTopics(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "invitee"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("Cdev", "U1")
	chat := service.Messages{Store: s}
	if _, err := chat.InviteConversationMembers(ctx, "T1", "U1", "Cdev", []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	message, err := chat.Post(ctx, "T1", "U1", "Cdev", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	if _, err := chat.Update(ctx, "T1", "U1", "Cdev", timestamp, "hello again"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Unfurl(ctx, "T1", "U1", "Cdev", timestamp, map[string]string{"https://example.test": `{"title":"x"}`}); err != nil {
		t.Fatal(err)
	}
	if err := chat.AddReaction(ctx, "T1", "U1", "Cdev", timestamp, ":wave:"); err != nil {
		t.Fatal(err)
	}
	if err := chat.RemoveReaction(ctx, "T1", "U1", "Cdev", timestamp, ":wave:"); err != nil {
		t.Fatal(err)
	}
	if err := chat.AddPin(ctx, "T1", "U1", "Cdev", timestamp); err != nil {
		t.Fatal(err)
	}
	if err := chat.RemovePin(ctx, "T1", "U1", "Cdev", timestamp); err != nil {
		t.Fatal(err)
	}
	saved, err := chat.SaveForLater(ctx, "T1", "U1", "Cdev", timestamp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.SetSavedItemState(ctx, "T1", "U1", saved.ID, domain.SavedItemCompleted); err != nil {
		t.Fatal(err)
	}
	if err := chat.RemoveSavedItem(ctx, "T1", "U1", saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Delete(ctx, "T1", "U1", "Cdev", timestamp); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	viewEvent := func(id, topic string, at time.Time) events.Event {
		event, err := events.New(domain.EventID(id), "T1", "U1", events.NewPayload(topic, events.String("view_id", id)), at)
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	firstView := domain.View{
		ID: "V-live-1", AppID: "A-live", WorkspaceID: "T1", UserID: "U1", Type: "modal",
		Payload: `{"type":"modal","title":{"type":"plain_text","text":"Live"},"blocks":[]}`,
		Hash:    "hash-1", RootViewID: "V-live-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateView(ctx, firstView, viewEvent("event-view-opened", "view.opened", now)); err != nil {
		t.Fatal(err)
	}
	firstView.Hash = "hash-2"
	firstView.UpdatedAt = now.Add(time.Second)
	if _, err := s.UpdateView(ctx, firstView, "hash-1", viewEvent("event-view-updated", "view.updated", firstView.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	secondView := domain.View{
		ID: "V-live-2", AppID: "A-live", WorkspaceID: "T1", UserID: "U1", Type: "modal",
		Payload: `{"type":"modal","title":{"type":"plain_text","text":"Pushed"},"blocks":[]}`,
		Hash:    "hash-3", RootViewID: firstView.ID, PreviousViewID: firstView.ID,
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second),
	}
	if err := s.CreateView(ctx, secondView, viewEvent("event-view-pushed", "view.pushed", secondView.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteView(ctx, "T1", "U1", secondView.ID, false, viewEvent("event-view-submitted", "view.submitted", now.Add(3*time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteView(ctx, "T1", "U1", firstView.ID, false, viewEvent("event-view-closed", "view.closed", now.Add(4*time.Second))); err != nil {
		t.Fatal(err)
	}
	// A huddle is started, joined by a second person and then left by both, so
	// all four huddle topics are emitted by real mutations rather than asserted
	// from a list.
	messages := service.Messages{Store: s}
	if _, err := messages.StartHuddle(ctx, "T1", "U1", "Cdev", ""); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "second"})
	s.SeedConversationMember("Cdev", "U2")
	if _, err := messages.JoinHuddle(ctx, "T1", "U2", "Cdev"); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.LeaveHuddle(ctx, "T1", "U2", "Cdev"); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.LeaveHuddle(ctx, "T1", "U1", "Cdev"); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListEventsAfter(ctx, "T1", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	emitted := map[string]bool{}
	for _, record := range records {
		topic := record.Event.Topic
		if strings.HasPrefix(topic, "message.") || strings.HasPrefix(topic, "reaction.") || strings.HasPrefix(topic, "conversation.") || strings.HasPrefix(topic, "pin.") || strings.HasPrefix(topic, "saved_item.") || strings.HasPrefix(topic, "view.") || strings.HasPrefix(topic, "huddle.") {
			emitted[topic] = true
		}
	}
	subscribed := map[string]bool{}
	for _, topic := range liveEventTopics {
		subscribed[topic] = true
		if !strings.Contains(progressiveEnhancementScript, "'"+topic+"'") {
			t.Fatalf("the page script does not subscribe to %q", topic)
		}
		if !emitted[topic] {
			t.Fatalf("the page subscribes to %q, which no chat mutation publishes", topic)
		}
	}
	for topic := range emitted {
		if !subscribed[topic] {
			t.Fatalf("the chat service publishes %q and the page ignores it", topic)
		}
	}
}

// TestTheDocumentAndItsContentSecurityPolicyAgree replaces a test that grepped
// the JavaScript source for twelve substrings.
//
// That test proved no behaviour: it passed on a script with no submit lock, no
// coalescing and no stream error handling, and it would have failed on the fix
// for any of them, because renaming a variable broke it. What it should have
// asserted is the contract the page depends on — that the script the document
// carries is the script the policy allows to run. A hash the policy does not
// cover silently disables the whole client in the browser and in nothing else,
// which is precisely the failure no unit test would otherwise see.
func TestTheDocumentAndItsContentSecurityPolicyAgree(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	for _, target := range []string{"/app?channel=Cdev", "/app/members", "/app/search?q=hello", "/app/activity?channel=Cdev", "/app/drafts?channel=Cdev"} {
		response := get(t, mux, target)
		policy := response.Header().Get("Content-Security-Policy")
		if policy == "" {
			t.Fatalf("%s carries no content security policy", target)
		}
		bodies := inlineScriptBodies(response.Body.String())
		if len(bodies) == 0 {
			t.Fatalf("%s renders no inline script", target)
		}
		for _, body := range bodies {
			digest := sha256.Sum256([]byte(body))
			hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
			if !strings.Contains(policy, hash) {
				t.Fatalf("%s serves an inline script the policy blocks: %s\npolicy=%s", target, hash, policy)
			}
		}
		if strings.Contains(policy, "'unsafe-inline'; script-src") || strings.Contains(policy, "script-src 'unsafe-inline'") {
			t.Fatalf("%s allows any inline script: %s", target, policy)
		}
	}
}

// TestTheInlineScriptParserRefusesWhatItCannotHash covers the shared blind spot
// of the policy builder and the test above: both read the document through
// inlineScriptBodies, which searched for the literal "<script>". A future
// `<script type="module">` would have been invisible to both, so the policy
// would omit its hash, the test would stay green, and the browser would block
// the script. A tag this cannot hash is now a build-time failure.
func TestTheInlineScriptParserRefusesWhatItCannotHash(t *testing.T) {
	for _, document := range []string{
		`<body><script type="module">alert(1)</script></body>`,
		`<body><script defer>alert(1)</script></body>`,
		`<body><script src="/x.js"></script></body>`,
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("inlineScriptBodies silently skipped a script it cannot hash: %s", document)
				}
			}()
			inlineScriptBodies(document)
		}()
	}
	if bodies := inlineScriptBodies(`<script>one</script>text<script>two</script>`); len(bodies) != 2 || bodies[0] != "one" || bodies[1] != "two" {
		t.Fatalf("plain inline scripts no longer parse: %q", bodies)
	}
}

// TestNewActivityIsAnnouncedForTheRegionItLandedIn covers two defects in the
// live-status announcement. messageCount() counted `#timeline .message` while
// refresh() re-renders every live region, so a reply arriving in an open thread
// pane — the one region that is live unconditionally — measured zero arrivals
// and was never announced. And the "New activity is available" fallback ran
// only when *no* region was live, so a reader in older history with a thread
// open (thread live, timeline not) got neither the count nor the fallback.
func TestNewActivityIsAnnouncedForTheRegionItLandedIn(t *testing.T) {
	requireContains(t, "client", progressiveEnhancementScript,
		`function messageCount(){return document.querySelectorAll('[data-fragment] .message').length}`,
		`var behind=document.querySelectorAll('[data-fragment]:not([data-live="true"])').length>0;`,
		`if(behind)announce('New activity is available in this conversation.');`,
	)
	requireMissing(t, "client", progressiveEnhancementScript, `document.querySelectorAll('#timeline .message')`)
}

// TestTheClientNeverFetchesAnOriginItWasNotGiven pins the one property of the
// client that no browser test can observe until it is already too late: every
// URL it fetches with credentials has to be a path on this origin. `hx-post`
// and `data-fragment` are not URL attributes to html/template and receive no
// filtering, so an absolute value would become a credentialed request to
// another host.
func TestTheClientNeverFetchesAnOriginItWasNotGiven(t *testing.T) {
	if !strings.Contains(progressiveEnhancementScript, "function ownPath(value){return typeof value==='string'&&value.charAt(0)==='/'&&value.charAt(1)!=='/'}") {
		t.Fatal("the client does not constrain the URLs it fetches")
	}
	_, mux := browserWorkspace(t, auth.AllScopes())
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	for _, attribute := range []string{"hx-post", "data-fragment", "data-newest"} {
		for _, match := range regexp.MustCompile(attribute+`="([^"]*)"`).FindAllStringSubmatch(body, -1) {
			value := match[1]
			if value == "" {
				continue
			}
			if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
				t.Fatalf("%s renders an off-origin target: %q", attribute, value)
			}
		}
	}
}

// TestWorkspacePagesCarryTheSameProtectionAsTheAdminPage covers the defect that
// left every workspace page framable and cacheable: the sibling admin page in
// this package set all five headers and the pages that render a conversation
// and a live CSRF token set none, so one click in an invisible frame landed on
// Sign out or Pin, and Back after sign-out replayed the conversation.
//
// It used to visit successful pages and one 404 only, which is how three whole
// classes of response kept answering with no headers at all: every fragment
// failure and every insufficient-scope refusal (bare http.Error), and the two
// signed-out entry pages, /login and /signed-out. Provider-backed versions of
// the second carry a sign-in link, so framing one starts an authorization flow
// the victim did not choose. Failures are now covered alongside successes.
func TestWorkspacePagesCarryTheSameProtectionAsTheAdminPage(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	requireProtected := func(target string, header http.Header) {
		t.Helper()
		for name, expected := range map[string]string{
			"X-Frame-Options":        "DENY",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"Cache-Control":          "no-store",
		} {
			if header.Get(name) != expected {
				t.Fatalf("%s %s=%q, want %q", target, name, header.Get(name), expected)
			}
		}
		if !strings.Contains(header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s is framable: %q", target, header.Get("Content-Security-Policy"))
		}
	}
	for _, target := range []string{
		"/app?channel=Cdev",
		"/app/timeline?channel=Cdev",
		"/app/members",
		"/app/remote-files",
		"/app/search?q=hello&channel=Cdev",
		"/app?channel=Cmissing",
		// Failures. Each of these answered with a bare line of text and no
		// header at all.
		"/app/timeline?channel=Cmissing",
		"/app/timeline?channel=Cdev&thread=not-a-timestamp",
		"/app?channel=Cdev&before=not-a-cursor",
		"/signed-out",
	} {
		requireProtected(target, get(t, mux, target).Header())
	}

	// An insufficient scope is refused by writeAuthError, which is the same
	// bare-text path.
	_, limited := browserWorkspace(t, []string{string(auth.ScopeChatWrite)})
	response := get(t, limited, "/app/timeline?channel=Cdev")
	if response.Code != http.StatusForbidden {
		t.Fatalf("insufficient scope status=%d", response.Code)
	}
	requireProtected("/app/timeline with no history scope", response.Header())
}

// TestTheSignedOutEntryPagesAreNotFramable covers /login, which is only
// registered when a provider is configured and therefore is not reachable from
// browserWorkspace.
func TestTheSignedOutEntryPagesAreNotFramable(t *testing.T) {
	login, err := NewLoginHandler(service.Messages{Store: memory.New()}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret",
		AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token",
		UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	login.Register(mux)
	for _, target := range []string{"/login"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", target, response.Code)
		}
		header := response.Header()
		if header.Get("X-Frame-Options") != "DENY" || !strings.Contains(header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s is framable: X-Frame-Options=%q CSP=%q", target, header.Get("X-Frame-Options"), header.Get("Content-Security-Policy"))
		}
		if header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s does not forbid sniffing", target)
		}
	}
}

// TestMutationsAreRefusedWhenTheBrowserReportsAnotherSite is the web half of
// the CSRF fix. The token is derived from the session cookie, so a page on a
// sibling host that could read the old, script-readable CSRF cookie held
// everything the server checked. Fetch metadata is the part it cannot forge.
func TestMutationsAreRefusedWhenTheBrowserReportsAnotherSite(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	for _, site := range []string{"same-site", "cross-site"} {
		request := httptest.NewRequest(http.MethodPost, "/app/message?channel=Cdev", strings.NewReader("text=forged"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("HX-Request", "true")
		request.Header.Set("Sec-Fetch-Site", site)
		addBrowserCookies(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("Sec-Fetch-Site=%s status=%d body=%s", site, response.Code, response.Body)
		}
		requireContains(t, "refusal", response.Body.String(), "not made from SameOldChat")
	}
	page, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("a forged request was stored: %+v err=%v", page, err)
	}
	// The token is bound to the session and needs no cookie of its own, so a
	// page open longer than any cookie lifetime still posts.
	if header := get(t, mux, "/app?channel=Cdev").Header().Get("Set-Cookie"); strings.Contains(header, "sameoldchat_csrf") {
		t.Fatalf("the workspace still publishes a CSRF cookie: %q", header)
	}
	accepted := httptest.NewRequest(http.MethodPost, "/app/message?channel=Cdev", strings.NewReader("text=hello&_csrf="+auth.CSRFToken("session")))
	accepted.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	accepted.Header.Set("HX-Request", "true")
	accepted.Header.Set("Sec-Fetch-Site", "same-origin")
	accepted.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, accepted)
	if response.Code != http.StatusOK {
		t.Fatalf("a same-origin post with only the session cookie was refused: %d %s", response.Code, response.Body)
	}
}

// TestPostMessageNeverRendersHistoryItCannotRead covers the authorization
// bypass: postMessage gates on chat:write and answered a rejected post with the
// whole workspace render, so a principal that GET /app answers with 403 read
// the conversation, every author, the sidebar and a live CSRF token out of a
// 400 body.
func TestPostMessageNeverRendersHistoryItCannotRead(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeChatWrite)})
	seedMessage(t, s, "M1", "confidential history", time.Unix(1700000000, 0).UTC())

	if denied := get(t, mux, "/app?channel=Cdev"); denied.Code != http.StatusForbidden {
		t.Fatalf("GET /app status=%d", denied.Code)
	}
	rejected := postForm(t, mux, "/app/message?channel=Cdev", "text=%20%20", false)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rejected.Code, rejected.Body)
	}
	requireMissing(t, "rejected post", rejected.Body.String(),
		"confidential history",
		`class="sidebar"`,
		`name="_csrf"`,
	)
	requireContains(t, "rejected post", rejected.Body.String(), "A message needs some text", `href="/app"`)
}

// TestReadingIsSafeAndMarkingReadIsAMutation covers the round-1 finding that
// #86 did not fix: GET /app advanced the read cursor for a channel named in the
// query string with no token, so `<img src="/app?channel=C…">` in any message
// silently wiped a victim's unread state under SameSite=Lax.
func TestReadingIsSafeAndMarkingReadIsAMutation(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "hello", created)

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	if _, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev"); err == nil {
		t.Fatal("a GET advanced the read cursor")
	}
	// The page offers the write as a form instead, so a reader without
	// JavaScript still has the control and every path through it is checked.
	requireContains(t, "workspace page", body, `action="/app/read?channel=Cdev"`, `name="ts"`, ">Mark as read<")

	timestamp := string(domain.NewMessageTimestamp(created))
	unchecked := httptest.NewRequest(http.MethodPost, "/app/read?channel=Cdev", strings.NewReader("ts="+timestamp))
	unchecked.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unchecked.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	refused := httptest.NewRecorder()
	mux.ServeHTTP(refused, unchecked)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("mark-read without a token status=%d", refused.Code)
	}
	if _, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev"); err == nil {
		t.Fatal("a request with no CSRF token advanced the read cursor")
	}

	marked := postForm(t, mux, "/app/read?channel=Cdev", "ts="+timestamp, true)
	if marked.Code != http.StatusNoContent {
		t.Fatalf("mark-read status=%d body=%s", marked.Code, marked.Body)
	}
	cursor, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev")
	if err != nil || cursor.LastRead != domain.MessageTimestamp(timestamp) {
		t.Fatalf("read cursor=%+v err=%v", cursor, err)
	}
}

// TestDeletedMessagesStopRendering covers the defect that kept a deleted
// message on screen forever. Deletion is soft and leaves the text in place, and
// message.deleted is a subscribed live topic, so deleting a password or a
// customer's name refreshed it back into every open tab.
func TestDeletedMessagesStopRendering(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 0).UTC()
	seedMessage(t, s, "M1", "a secret nobody should keep", created)
	seedMessage(t, s, "M2", "a message that stays", created.Add(time.Second))
	if _, err := (service.Messages{Store: s}).Delete(context.Background(), "T1", "U1", "Cdev", domain.NewMessageTimestamp(created)); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/app?channel=Cdev", "/app/timeline?channel=Cdev"} {
		response := get(t, mux, target)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body)
		}
		requireMissing(t, target, response.Body.String(), "a secret nobody should keep")
		requireContains(t, target, response.Body.String(), "a message that stays")
	}
}

// TestThreadViewCarriesNoDuplicateIdentifiers covers the half of the
// duplicate-id defect this package owns: the thread pane namespaced its anchors
// and nothing else, so `id="reaction-<message>"` and its `for=` label existed
// twice in one document and pointed at the wrong control.
func TestThreadViewCarriesNoDuplicateIdentifiers(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "thread root", created)
	timestamp := string(domain.NewMessageTimestamp(created))
	reply := domain.Message{ID: "M2", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: "a reply", ThreadTimestamp: domain.MessageTimestamp(timestamp), CreatedAt: created.Add(time.Second)}
	if err := s.CreateMessage(context.Background(), reply, events.Event{ID: "EM2", WorkspaceID: "T1", Topic: "message.created", Payload: "M2", CreatedAt: reply.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev&thread="+timestamp).Body.String()
	seen := map[string]int{}
	for _, match := range regexp.MustCompile(`(?:^|\s)id="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		seen[match[1]]++
		if seen[match[1]] > 1 {
			t.Fatalf("the document carries id=%q %d times", match[1], seen[match[1]])
		}
	}
	for _, match := range regexp.MustCompile(`for="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		if seen[match[1]] != 1 {
			t.Fatalf("label for=%q has %d targets", match[1], seen[match[1]])
		}
	}
}

// TestReactionChipsAreNotControlsForAReaderWhoCannotReact covers the defect
// where a read-only guest saw chips that looked like toggles, and clicking one
// answered a bare "forbidden".
func TestReactionChipsAreNotControlsForAReaderWhoCannotReact(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeChannelsHistory), string(auth.ScopeReactionsRead)})
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	if err := s.AddReaction(context.Background(), domain.Reaction{Message: "M1", UserID: "U1", Name: ":wave:"}, events.Event{ID: "ER1", WorkspaceID: "T1", Topic: "reaction.added", Payload: "M1", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "read-only chip", body, `<span class="chip" role="img" aria-label=":wave:, 1 reactions">`)
	requireMissing(t, "read-only chip", body, `<button class="chip"`, `aria-label="Add reaction"`)
}

// TestMutationFailuresWithoutJavaScriptAreNavigablePages covers the plain-text
// dead ends: a reader with JavaScript off landed on a white page reading
// "that message timestamp is not valid" with no way back.
func TestMutationFailuresWithoutJavaScriptAreNavigablePages(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	for _, mutation := range []struct{ target, body string }{
		{target: "/app/pin?channel=Cdev&ts=not-a-timestamp"},
		{target: "/app/reaction?channel=Cdev&ts=not-a-timestamp", body: "name=%3Awave%3A"},
		{target: "/app/reaction?channel=Cdev&ts=1700000000.000000", body: "name="},
		{target: "/app/conversation/open", body: "users="},
	} {
		page := postForm(t, mux, mutation.target, mutation.body, false)
		if page.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", mutation.target, page.Code, page.Body)
		}
		if contentType := page.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Fatalf("%s answered %s with no way back: %s", mutation.target, contentType, page.Body)
		}
		requireContains(t, mutation.target, page.Body.String(), `<a href="/app">Back to chat</a>`)
		if page.Header().Get("Vary") != "HX-Request" {
			t.Fatalf("%s does not vary on the header that decides its shape", mutation.target)
		}
		fragment := postForm(t, mux, mutation.target, mutation.body, true)
		if fragment.Code != http.StatusBadRequest || !strings.HasPrefix(fragment.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s enhanced status=%d type=%s", mutation.target, fragment.Code, fragment.Header().Get("Content-Type"))
		}
	}
}

// countingChat counts the History calls one render costs.
type countingChat struct {
	chatapi.Service
	calls *int
}

func (c countingChat) History(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, conversation domain.ConversationID, request domain.PageRequest) (domain.MessagePage, error) {
	*c.calls++
	return c.Service.History(ctx, workspace, user, conversation, request)
}

// longTimelineWorkspace seeds enough messages to prove that timeline cost and
// correctness do not depend on walking from the first row.
func longTimelineWorkspace(t *testing.T, calls *int) *http.ServeMux {
	t.Helper()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("Cdev", "U1")
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1700000000, 0).UTC()
	for index := 0; index < timelineWindow*4; index++ {
		message := domain.Message{ID: domain.MessageID(fmt.Sprintf("M%06d", index)), WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: fmt.Sprintf("note-%06d", index), CreatedAt: base.Add(time.Duration(index) * time.Second)}
		if err := s.CreateMessage(context.Background(), message, events.Event{ID: domain.EventID(fmt.Sprintf("E%06d", index)), WorkspaceID: "T1", Topic: "message.created", Payload: string(message.ID), CreatedAt: message.CreatedAt}, ""); err != nil {
			t.Fatal(err)
		}
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	var service chatapi.Service = service.Messages{Store: s}
	if calls != nil {
		service = countingChat{Service: service, calls: calls}
	}
	handler, err := NewHandler(service, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux
}

// The old bounded forward scan made the newest rows unreachable once a channel
// crossed its budget. A descending keyset page must make the same long channel
// an ordinary current view.
func TestALongTimelineReachesTheNewestMessages(t *testing.T) {
	mux := longTimelineWorkspace(t, nil)

	page := get(t, mux, "/app?channel=Cdev")
	if page.Code != http.StatusOK {
		t.Fatalf("status=%d", page.Code)
	}
	body := page.Body.String()
	requireContains(t, "long page", body,
		"note-000199",
		"note-000150",
		`data-live="true"`,
		"Show older messages",
	)
	requireMissing(t, "long page", body,
		"note-000149",
		"Jump to the latest messages",
	)

	fragment := get(t, mux, "/app/timeline?channel=Cdev")
	if fragment.Code != http.StatusOK {
		t.Fatalf("fragment status=%d", fragment.Code)
	}
	requireContains(t, "long fragment", fragment.Body.String(), "note-000199")
}

// One timeline render is one service call in both local and distributed
// composition; conversation length cannot multiply network round trips.
func TestTheTimelineUsesOneDescendingRead(t *testing.T) {
	calls := 0
	mux := longTimelineWorkspace(t, &calls)

	response := get(t, mux, "/app?channel=Cdev")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if calls != 1 {
		t.Fatalf("one render issued %d history calls, want 1", calls)
	}

	calls = 0
	if fragment := get(t, mux, "/app/timeline?channel=Cdev"); fragment.Code != http.StatusOK || calls != 1 {
		t.Fatalf("fragment status=%d calls=%d", fragment.Code, calls)
	}
}

// TestActionSurfacesMeetContrastInBothThemes computes the ratios rather than
// asserting a colour literal, so the palette can change and the contract
// cannot. Every value below was measured failing before the fix: the Send
// button at 2.31:1 and the /me Sign out button at 1.96:1 in the dark theme, and
// the focus ring over the light purple chrome at 1.65:1.
func TestActionSurfacesMeetContrastInBothThemes(t *testing.T) {
	for _, theme := range []struct {
		name   string
		tokens string
	}{{name: "light", tokens: lightTokens}, {name: "dark", tokens: darkTokens}} {
		token := func(name string) string {
			match := regexp.MustCompile(`--` + name + `:#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b`).FindStringSubmatch(theme.tokens)
			if match == nil {
				t.Fatalf("%s theme has no --%s", theme.name, name)
			}
			value := match[1]
			if len(value) == 3 {
				value = string([]byte{value[0], value[0], value[1], value[1], value[2], value[2]})
			}
			return "#" + value
		}
		// The two action buttons are read out of the stylesheet, so the
		// measurement follows the rule the browser applies rather than a token
		// the rule might stop using.
		resolve := func(value string) string {
			if strings.HasPrefix(value, "var(--") {
				return token(strings.TrimSuffix(strings.TrimPrefix(value, "var(--"), ")"))
			}
			if len(value) == 4 {
				return "#" + string([]byte{value[1], value[1], value[2], value[2], value[3], value[3]})
			}
			return value
		}
		for _, pair := range []struct {
			what       string
			foreground string
			background string
			minimum    float64
		}{
			{what: "Send button label", foreground: resolve(declaration(t, pageStyle, ".send", "color")), background: resolve(declaration(t, pageStyle, ".send", "background")), minimum: 4.5},
			{what: "Sign out button label", foreground: resolve(declaration(t, identityMarkup, ".button", "color")), background: resolve(declaration(t, identityMarkup, ".button", "background")), minimum: 4.5},
			{what: "focus ring on the chrome", foreground: token("focus-chrome"), background: token("accent"), minimum: 3},
			{what: "focus ring on the page", foreground: token("focus"), background: token("bg"), minimum: 3},
			{what: "control border", foreground: token("field-line"), background: token("panel"), minimum: 3},
			{what: "body text", foreground: token("text"), background: token("bg"), minimum: 4.5},
			{what: "secondary text", foreground: token("muted"), background: token("bg"), minimum: 4.5},
		} {
			if ratio := contrastRatio(t, pair.foreground, pair.background); ratio < pair.minimum {
				t.Fatalf("%s theme: %s is %.2f:1 on %s, needs %.1f:1", theme.name, pair.what, ratio, pair.background, pair.minimum)
			}
		}
	}
}

// declaration reads one property out of one CSS rule.
func declaration(t *testing.T, stylesheet, selector, property string) string {
	t.Helper()
	match := regexp.MustCompile(regexp.QuoteMeta(selector) + `\{([^}]*)\}`).FindStringSubmatch(stylesheet)
	if match == nil {
		t.Fatalf("no rule for %s", selector)
	}
	value := regexp.MustCompile(`(?:^|;)` + regexp.QuoteMeta(property) + `:([^;]+)`).FindStringSubmatch(match[1])
	if value == nil {
		t.Fatalf("%s declares no %s: %s", selector, property, match[1])
	}
	return strings.TrimSpace(value[1])
}

func contrastRatio(t *testing.T, foreground, background string) float64 {
	t.Helper()
	lighter, darker := relativeLuminance(t, foreground), relativeLuminance(t, background)
	if darker > lighter {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(t *testing.T, colour string) float64 {
	t.Helper()
	value, err := strconv.ParseUint(strings.TrimPrefix(colour, "#"), 16, 32)
	if err != nil {
		t.Fatalf("colour %q: %v", colour, err)
	}
	channel := func(shift uint) float64 {
		part := float64((value>>shift)&0xff) / 255
		if part <= 0.03928 {
			return part / 12.92
		}
		return math.Pow((part+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(16) + 0.7152*channel(8) + 0.0722*channel(0)
}

// TestDeactivatedMembersAreNotOfferedAsPeople covers the defect where a
// colleague who left six months ago was listed with no indication and offered
// a "Message <them>" button that opened a dead conversation.
func TestDeactivatedMembersAreNotOfferedAsPeople(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "gone", RealName: "Gone Person", Deleted: true})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "here", RealName: "Still Here"})
	body := get(t, mux, "/app/members").Body.String()
	requireContains(t, "members page", body, "Still Here", `name="users" value="U3"`)
	requireMissing(t, "members page", body, "Gone Person", `name="users" value="U2"`)
}

// TestFailedPostKeepsTheDraftAndExplainsTheFailure covers the defect where a
// rejected post produced no visible feedback and silently lost the message.
func TestFailedPostKeepsTheDraftAndExplainsTheFailure(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	enhanced := postForm(t, mux, "/app/message?channel=Cdev", "text=%20%20", true)
	if enhanced.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", enhanced.Code, enhanced.Body)
	}
	if !strings.Contains(enhanced.Body.String(), "A message needs some text") {
		t.Fatalf("enhanced failure body=%q", enhanced.Body.String())
	}
	plain := postForm(t, mux, "/app/message?channel=Cdev", "text=%20%20", false)
	if plain.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", plain.Code, plain.Body)
	}
	body := plain.Body.String()
	requireContains(t, "rejected post", body, `class="form-error" id="composer-error" role="alert"`, "A message needs some text", "<textarea")
	// The server-rendered failure has to match the enhanced one: the error is
	// visible, it is what the caret lands on, and the composer carries the
	// state its own stylesheet defines.
	region := regexp.MustCompile(`<p class="form-error" id="composer-error"[^>]*>`).FindString(body)
	if strings.Contains(region, "hidden") {
		t.Fatalf("the error region stayed hidden after a rejected post: %s", region)
	}
	if !strings.Contains(region, "autofocus") {
		t.Fatalf("the error region is not what the caret lands on: %s", region)
	}
	requireContains(t, "rejected post", body, `class="composer is-error"`)
	if strings.Contains(body, `name="text" required autofocus`) {
		t.Fatal("the composer takes focus past the error it just rendered")
	}
	page, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("messages=%+v err=%v", page, err)
	}
}

// TestFormBodyLimitIsInstalledBeforeCSRFValidation covers the defect where the
// declared 4 MB limit was unreachable: CSRF validation read a form value first,
// which parsed the whole body with net/http's own 32 MB default.
func TestFormBodyLimitIsInstalledBeforeCSRFValidation(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	body := "text=" + strings.Repeat("a", 5<<20)
	response := postForm(t, mux, "/app/message?channel=Cdev", body, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status=%d", response.Code)
	}
	page, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("an oversized body was still stored: messages=%+v err=%v", page, err)
	}
}

// TestThemeIsResolvedBeforeTheFirstPaint covers the defect where a dark-theme
// reader saw a full flash of the light theme on every navigation, and the toggle
// never reported which theme was active.
func TestThemeIsResolvedBeforeTheFirstPaint(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	for _, target := range []string{"/app?channel=Cdev", "/app/members", "/app/search?q=hello"} {
		body := get(t, mux, target).Body.String()
		bootstrap := strings.Index(body, "localStorage.getItem('sameoldchat-theme')")
		if bootstrap < 0 {
			t.Fatalf("%s does not bootstrap the theme: %s", target, body)
		}
		if bootstrap > strings.Index(body, "<body") {
			t.Fatalf("%s resolves the theme after the first paint", target)
		}
		requireContains(t, target, body, "prefers-color-scheme: dark", `id="theme-toggle"`, `aria-pressed="false"`)
	}
}

func TestHTMXPostMessage(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeChatWrite), string(auth.ScopeChannelsHistory)})
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("text", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("_csrf", auth.CSRFToken("session")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/app/message", &form)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	// The browser shim sends the token as a form field, not a header: that is the
	// path the body limit has to survive.
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "hello") {
		t.Fatalf("body = %s", res.Body)
	}
	indexResult := get(t, mux, "/app")
	body := indexResult.Body.String()
	if indexResult.Code != http.StatusOK {
		t.Fatalf("index status=%d body=%s", indexResult.Code, body)
	}
	requireContains(t, "index", body,
		"general",
		"hello",
		"theme-toggle",
		`data-theme="light"`,
		"HX-Request",
		"last_event_id",
		"sessionStorage",
		`method="get" action="/app/search"`,
		`name="q"`,
	)
	// The page is rendered from the template, not patched afterwards: the search
	// control is a real form and there is no second, superseded event stream.
	requireMissing(t, "index", body, `href="/me"`, `<label class="search"`)
	// Reading is a safe method and does not write; the read cursor is advanced
	// by the explicit, CSRF-checked POST the page carries a form for.
	if _, err := s.GetReadCursor(context.Background(), "T1", "U1", "Cdev"); err == nil {
		t.Fatal("GET /app advanced the read cursor")
	}
	page, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("messages=%+v err=%v", page, err)
	}
	thread := domain.NewMessageTimestamp(page.Messages[0].CreatedAt)
	replyResult := postForm(t, mux, "/app/message?channel=Cdev", "text=reply&thread_ts="+string(thread), false)
	if replyResult.Code != http.StatusSeeOther || !strings.Contains(replyResult.Header().Get("Location"), "thread=") {
		t.Fatalf("reply status=%d location=%s", replyResult.Code, replyResult.Header().Get("Location"))
	}
}

func TestScheduledMessageJourneyCreatesListsAndCancelsWithoutPostingEarly(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	rootCreated := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "Mroot", "thread root", rootCreated)
	thread := domain.NewMessageTimestamp(rootCreated)

	workspace := get(t, mux, "/app?channel=Cdev&thread="+url.QueryEscape(string(thread)))
	if workspace.Code != http.StatusOK {
		t.Fatalf("workspace status=%d body=%s", workspace.Code, workspace.Body)
	}
	requireContains(t, "SCHED-01 composer", workspace.Body.String(),
		`aria-label="Schedule message"`,
		`type="datetime-local" name="schedule_at"`,
		`formaction="/app/message/schedule?channel=Cdev&amp;thread=`+string(thread)+`"`,
		`href="/app/drafts?channel=Cdev&amp;tab=scheduled"`,
		`body.set('post_at',String(Math.floor(scheduleMillis/1000)))`,
	)

	postAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	form := url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"follow up later"},
		"thread_ts":             {string(thread)},
		"post_at":               {strconv.FormatInt(postAt.Unix(), 10)},
	}
	scheduled := postForm(t, mux, "/app/message/schedule?channel=Cdev&thread="+url.QueryEscape(string(thread)), form.Encode(), false)
	if scheduled.Code != http.StatusSeeOther || !strings.HasPrefix(scheduled.Header().Get("Location"), "/app/drafts?") || !strings.Contains(scheduled.Header().Get("Location"), "tab=scheduled") {
		t.Fatalf("schedule status=%d location=%q body=%s", scheduled.Code, scheduled.Header().Get("Location"), scheduled.Body)
	}
	history, err := s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("scheduled message posted early: history=%+v err=%v", history, err)
	}
	page, err := (service.Messages{Store: s}).ScheduledMessages(context.Background(), "T1", "U1", "", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ThreadTimestamp != thread || !page.Items[0].PostAt.Equal(postAt) {
		t.Fatalf("scheduled page=%+v err=%v", page, err)
	}

	list := get(t, mux, scheduled.Header().Get("Location"))
	if list.Code != http.StatusOK {
		t.Fatalf("scheduled list status=%d body=%s", list.Code, list.Body)
	}
	requireContains(t, "SCHED-02 scheduled list", list.Body.String(),
		"Message scheduled.",
		"follow up later",
		`datetime="`+postAt.Format(time.RFC3339Nano)+`"`,
		`href="/app?channel=Cdev&amp;thread=`+string(thread)+`"`,
		`>Send now</button>`,
		`>Save changes</button>`,
		`>Cancel message</button>`,
	)

	updatedAt := postAt.Add(time.Hour)
	updateTarget := "/app/message/schedule/update?channel=Cdev&id=" + url.QueryEscape(string(page.Items[0].ID)) + "&return_channel=Cdev"
	updated := postForm(t, mux, updateTarget, url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"edited follow up"},
		"post_at":               {strconv.FormatInt(updatedAt.Unix(), 10)},
	}.Encode(), false)
	if updated.Code != http.StatusSeeOther || !strings.Contains(updated.Header().Get("Location"), "updated=1") {
		t.Fatalf("update status=%d location=%q body=%s", updated.Code, updated.Header().Get("Location"), updated.Body)
	}
	page, err = (service.Messages{Store: s}).ScheduledMessages(context.Background(), "T1", "U1", "", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Text != "edited follow up" || !page.Items[0].PostAt.Equal(updatedAt) {
		t.Fatalf("updated scheduled page=%+v err=%v", page, err)
	}
	sendTarget := "/app/message/schedule/send-now?id=" + url.QueryEscape(string(page.Items[0].ID)) + "&return_channel=Cdev"
	sent := postForm(t, mux, sendTarget, url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}}.Encode(), false)
	if sent.Code != http.StatusSeeOther || !strings.Contains(sent.Header().Get("Location"), "tab=sent") || !strings.Contains(sent.Header().Get("Location"), "sent=1") {
		t.Fatalf("send-now status=%d location=%q body=%s", sent.Code, sent.Header().Get("Location"), sent.Body)
	}
	history, err = s.ListMessages(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 2 || history.Messages[1].Text != "edited follow up" {
		t.Fatalf("send-now history=%+v err=%v", history, err)
	}

	form.Set("text", "cancel this later")
	form.Set("post_at", strconv.FormatInt(postAt.Unix(), 10))
	rescheduled := postForm(t, mux, "/app/message/schedule?channel=Cdev&thread="+url.QueryEscape(string(thread)), form.Encode(), false)
	if rescheduled.Code != http.StatusSeeOther {
		t.Fatalf("second schedule status=%d body=%s", rescheduled.Code, rescheduled.Body)
	}
	page, err = (service.Messages{Store: s}).ScheduledMessages(context.Background(), "T1", "U1", "", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Text != "cancel this later" {
		t.Fatalf("second scheduled page=%+v err=%v", page, err)
	}
	cancelTarget := "/app/message/schedule/cancel?channel=Cdev&id=" + url.QueryEscape(string(page.Items[0].ID)) + "&return_channel=Cdev"
	cancelled := postForm(t, mux, cancelTarget, url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}}.Encode(), false)
	if cancelled.Code != http.StatusSeeOther || !strings.Contains(cancelled.Header().Get("Location"), "cancelled=1") {
		t.Fatalf("cancel status=%d location=%q body=%s", cancelled.Code, cancelled.Header().Get("Location"), cancelled.Body)
	}
	page, err = (service.Messages{Store: s}).ScheduledMessages(context.Background(), "T1", "U1", "", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("cancelled schedule remains: page=%+v err=%v", page, err)
	}
}

func TestScheduledComposerAttachmentJourneyPersistsListsAndSendsFile(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	now := time.Now().UTC()
	upload := domain.ExternalUpload{
		ID: "scheduled-browser-upload", WorkspaceID: "T1", Uploader: "U1", Name: "launch.txt", Title: "launch.txt",
		MIMEType: "text/plain", BlobKey: "T1/external/scheduled-browser-upload", Size: 6,
		Status: domain.ExternalUploadUploaded, CreatedAt: now, ExpiresAt: now.Add(time.Hour), UploadedAt: now,
	}
	if err := s.CreateExternalUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}
	draft, err := (service.Messages{Store: s}).SaveDraftWithAttachments(ctx, "T1", "U1", "Cdev", "", "", []domain.DraftAttachment{{
		UploadID: upload.ID, Title: "Launch plan",
	}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(newDraftAttachmentViews(draft.Attachments))
	if err != nil {
		t.Fatal(err)
	}
	postAt := now.Add(2 * time.Hour).Truncate(time.Second)
	scheduledResponse := postForm(t, mux, "/app/message/schedule?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"post_at":               {strconv.FormatInt(postAt.Unix(), 10)},
		"draft_attachments":     {string(encoded)},
	}.Encode(), false)
	if scheduledResponse.Code != http.StatusSeeOther {
		t.Fatalf("schedule status=%d body=%s", scheduledResponse.Code, scheduledResponse.Body)
	}
	page, err := (service.Messages{Store: s}).ScheduledMessages(ctx, "T1", "U1", "", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || len(page.Items[0].FileAttachments) != 1 || page.Items[0].Text != "" {
		t.Fatalf("scheduled page=%+v err=%v", page, err)
	}
	if _, err := s.GetDraft(ctx, "T1", "U1", "Cdev", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("scheduled attachment left draft: %v", err)
	}
	list := get(t, mux, scheduledResponse.Header().Get("Location"))
	requireContains(t, "SCHED-ATTACH-01 scheduled list", list.Body.String(),
		"File attachment", "1 attachment", "Attached files stay with this scheduled message.")
	sendTarget := "/app/message/schedule/send-now?id=" + url.QueryEscape(string(page.Items[0].ID)) + "&return_channel=Cdev"
	sent := postForm(t, mux, sendTarget, url.Values{auth.CSRFTokenFieldName: {auth.CSRFToken("session")}}.Encode(), false)
	if sent.Code != http.StatusSeeOther {
		t.Fatalf("send-now status=%d body=%s", sent.Code, sent.Body)
	}
	history, err := s.ListMessages(ctx, "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 || len(history.Messages[0].Files) != 1 ||
		history.Messages[0].Files[0].ID != domain.FileID(upload.ID) {
		t.Fatalf("sent history=%+v err=%v", history, err)
	}
}

func TestDraftsAndSentPersistsDraftsAndShowsSentMessages(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	form := url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"a durable unfinished message"},
	}
	saved := postForm(t, mux, "/app/draft?channel=Cdev", form.Encode(), true)
	if saved.Code != http.StatusNoContent {
		t.Fatalf("save draft status=%d body=%s", saved.Code, saved.Body)
	}
	draft, err := (service.Messages{Store: s}).Draft(context.Background(), "T1", "U1", "Cdev", "")
	if err != nil || draft.Text != "a durable unfinished message" {
		t.Fatalf("saved draft=%+v err=%v", draft, err)
	}
	workspace := get(t, mux, "/app?channel=Cdev")
	requireContains(t, "DRAFT-01 restored composer", workspace.Body.String(),
		`data-draft-url="/app/draft?channel=Cdev"`,
		`>a durable unfinished message</textarea>`,
		`saveDraftRemote(true)`,
	)
	drafts := get(t, mux, "/app/drafts?channel=Cdev&tab=drafts")
	if drafts.Code != http.StatusOK {
		t.Fatalf("drafts status=%d body=%s", drafts.Code, drafts.Body)
	}
	requireContains(t, "DRAFT-02 drafts tab", drafts.Body.String(),
		`aria-current="page">Drafts</a>`,
		"a durable unfinished message",
		">Continue</a>",
		`aria-label="Delete draft in general"`,
	)
	deleted := postForm(t, mux, "/app/draft/delete?channel=Cdev&return_channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
	}.Encode(), false)
	if deleted.Code != http.StatusSeeOther || !strings.Contains(deleted.Header().Get("Location"), "draft_deleted=1") {
		t.Fatalf("delete draft status=%d location=%q body=%s", deleted.Code, deleted.Header().Get("Location"), deleted.Body)
	}
	if _, err := (service.Messages{Store: s}).Draft(context.Background(), "T1", "U1", "Cdev", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted draft error=%v, want not found", err)
	}

	posted := postForm(t, mux, "/app/message?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"recently sent from the composer"},
	}.Encode(), false)
	if posted.Code != http.StatusSeeOther {
		t.Fatalf("post status=%d body=%s", posted.Code, posted.Body)
	}
	sent := get(t, mux, "/app/drafts?channel=Cdev&tab=sent")
	requireContains(t, "DRAFT-02 sent tab", sent.Body.String(),
		`aria-current="page">Sent</a>`,
		"recently sent from the composer",
		">View conversation</a>",
	)
}

func TestScheduledMessageValidationRetainsDraftAndClassifiesHandledErrors(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	past := strconv.FormatInt(time.Now().UTC().Add(-time.Hour).Unix(), 10)
	body := url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"do not lose this draft"},
		"post_at":               {past},
		"schedule_at":           {"2026-07-29T18:30"},
	}.Encode()
	response := postForm(t, mux, "/app/message/schedule?channel=Cdev", body, false)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("past schedule status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "SCHED-01 handled validation", response.Body.String(),
		"Choose a delivery time in the future.",
		`>do not lose this draft</textarea>`,
		`name="schedule_at" value="2026-07-29T18:30"`,
	)

	enhanced := postForm(t, mux, "/app/message/schedule?channel=Cdev", url.Values{
		auth.CSRFTokenFieldName: {auth.CSRFToken("session")},
		"text":                  {"still here"},
	}.Encode(), true)
	if enhanced.Code != http.StatusBadRequest || !strings.Contains(enhanced.Body.String(), "Choose a delivery date and time") {
		t.Fatalf("missing-time response status=%d body=%s", enhanced.Code, enhanced.Body)
	}
}

func TestApplicationRedirectsUnauthenticatedBrowserToLogin(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	authenticator, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "google", ClientID: "client", ClientSecret: "secret", AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated application response = %d location=%q", response.Code, response.Header().Get("Location"))
	}
}

// TestDeepApplicationPagesRedirectAnonymousVisitorsToSignIn is issue #131:
// only GET /app used to redirect an anonymous visitor into sign-in, so every
// deeper page — /app/members, a Later link a teammate shared — answered a
// bare text 401 to a browser. A person navigating to a page belongs on the
// sign-in flow with the destination preserved; the app's own fragment
// fetches keep the 401, because a redirect would swap the sign-in page into
// a fragment; and unknown paths stay 404 — the /api/* JSON envelope must
// never leak into the web tree.
func TestDeepApplicationPagesRedirectAnonymousVisitorsToSignIn(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	for _, target := range []string{"/app/members", "/app/later", "/app/workflows", "/app/canvases", "/app/remote-files", "/archives/Cdev/p1700000000000000"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusSeeOther {
			t.Fatalf("anonymous %s status=%d, want %d", target, response.Code, http.StatusSeeOther)
		}
		location := response.Header().Get("Location")
		if !strings.HasPrefix(location, "/login?return_to=") || !strings.Contains(location, url.QueryEscape(target)) {
			t.Fatalf("anonymous %s location=%q, want /login carrying return_to=%s", target, location, target)
		}
	}
	fragment := httptest.NewRequest(http.MethodGet, "/app/members", nil)
	fragment.Header.Set("HX-Request", "true")
	fragmentResult := httptest.NewRecorder()
	mux.ServeHTTP(fragmentResult, fragment)
	if fragmentResult.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous fragment fetch status=%d, want %d", fragmentResult.Code, http.StatusUnauthorized)
	}
	unknown := httptest.NewRecorder()
	mux.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/app/not-a-page", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("anonymous unknown page status=%d, want %d", unknown.Code, http.StatusNotFound)
	}
	if strings.Contains(unknown.Body.String(), "unknown_method") {
		t.Fatalf("the /api envelope leaked into the web tree: %s", unknown.Body)
	}
}

// A credential store that did not answer is server trouble: the browser gets
// a retryable 503, not a 401 and not a bounce through sign-in that would
// discard the person's place in the app for a transient backend failure.
func TestWebAuthErrorAnswersServiceUnavailableForAStoreOutage(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/app/members", nil)
	Handler{}.writeAuthError(recorder, request, fmt.Errorf("%w: connection refused", auth.ErrCredentialStoreUnavailable))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("store outage status=%d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestApplicationStartsShauthForUnauthenticatedDirectEntry(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	authenticator, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/auth/oidc" {
		t.Fatalf("unauthenticated application response = %d location=%q", response.Code, response.Header().Get("Location"))
	}
	// A provider the workspace has disabled cannot complete a sign-in, so entry
	// falls back to the catalog, which presents an honest unavailable state when
	// no provider remains, instead of a dead authorization link.
	if err := store.SetAuthMethod(context.Background(), domain.AuthMethod{WorkspaceID: "T1", Provider: "oidc", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewRecorder()
	mux.ServeHTTP(disabled, httptest.NewRequest(http.MethodGet, "/app", nil))
	if disabled.Code != http.StatusSeeOther || disabled.Header().Get("Location") != "/login" {
		t.Fatalf("disabled provider response = %d location=%q", disabled.Code, disabled.Header().Get("Location"))
	}
}

func TestShauthValidationAndMyProfileExposeVerifiedIdentityAndLogout(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "developer@example.test", Name: "local-seed", RealName: "Local Seed", Profile: domain.UserProfile{DisplayName: "developer"}})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := store.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	if err := handler.SetReleaseRevision("0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatal(err)
	}
	if handler.ReleaseRevision != "0123456789ab" {
		t.Fatalf("displayed release revision = %q, want the immutable 12-character image tag", handler.ReleaseRevision)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	for _, path := range []string{"/auth/validation", "/me"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		body := response.Body.String()
		for _, expected := range []string{`data-testid="validation-username">developer`, `data-testid="validation-email">developer@example.test`, `data-testid="validation-role">developer`, `data-testid="validation-release">0123456789ab`, `aria-label="Avatar for developer">D</span>`, `action="/logout"`, `>Sign out</button>`} {
			if response.Code != http.StatusOK || !strings.Contains(body, expected) {
				t.Fatalf("%s status=%d missing %q body=%s", path, response.Code, expected, body)
			}
		}
	}
	applicationRequest := httptest.NewRequest(http.MethodGet, "/app", nil)
	applicationRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	applicationResponse := httptest.NewRecorder()
	mux.ServeHTTP(applicationResponse, applicationRequest)
	// The workspace shell itself has to name the signed-in user and expose the
	// real sign-out control: post-deployment qualification reads them there, not
	// on the separate validation page.
	for _, expected := range []string{
		`href="/me" aria-label="My profile"`,
		`data-shauth-user="developer"`,
		`<span class="signed-in-name">developer</span>`,
		`data-shauth-sign-out`,
	} {
		if applicationResponse.Code != http.StatusOK || !strings.Contains(applicationResponse.Body.String(), expected) {
			t.Fatalf("authenticated application status=%d missing %q body=%s", applicationResponse.Code, expected, applicationResponse.Body)
		}
	}
	anonymous := httptest.NewRecorder()
	mux.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/auth/validation", nil))
	if anonymous.Code != http.StatusSeeOther || anonymous.Header().Get("Location") != "/signed-out" {
		t.Fatalf("anonymous validation status=%d location=%q", anonymous.Code, anonymous.Header().Get("Location"))
	}
	if err := handler.SetReleaseRevision("latest"); err == nil {
		t.Fatal("mutable release revision was accepted")
	}
	if err := handler.SetReleaseRevision("sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if handler.ReleaseRevision != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("image digest was shortened to %q", handler.ReleaseRevision)
	}
}

// TestProfileControlOnlyAppearsWhenTheIdentityPageCanRender covers the defect
// where the shell offered "My profile" on deployments whose /me always answered
// with a service-unavailable page.
func TestProfileControlOnlyAppearsWhenTheIdentityPageCanRender(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	// A Google-only deployment has no Shauth identity to validate.
	login, err := NewLoginHandler(service.Messages{Store: s}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "google", ClientID: "client", ClientSecret: "secret", AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "workspace shell", body, `href="/me"`)
	profile := get(t, mux, "/me")
	if profile.Code != http.StatusServiceUnavailable {
		t.Fatalf("identity status=%d body=%s", profile.Code, profile.Body)
	}
	requireContains(t, "identity page", profile.Body.String(), "Identity validation is unavailable", `href="/app"`)
}

// TestAuthorizationAdminIsReachableFromTheShell covers the defect where the
// documented administration page had no entry point in the interface.
func TestAuthorizationAdminIsReachableFromTheShell(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	if err := s.SetWorkspaceRole(context.Background(), "T1", "U1", domain.WorkspaceRoleAdmin, events.Event{}); err != nil {
		t.Fatal(err)
	}
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: s}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"}}})
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	requireContains(t, "workspace shell", get(t, mux, "/app?channel=Cdev").Body.String(), `href="/app/admin/auth"`, "Authorization")

	if err := s.SetWorkspaceRole(context.Background(), "T1", "U1", domain.WorkspaceRoleMember, events.Event{}); err != nil {
		t.Fatal(err)
	}
	requireMissing(t, "member workspace shell", get(t, mux, "/app?channel=Cdev").Body.String(), `href="/app/admin/auth"`)
}

func TestSearchPageUsesMessageSearchAndLinksToConversation(t *testing.T) {
	s, mux := browserWorkspace(t, []string{string(auth.ScopeSearchRead), string(auth.ScopeChannelsHistory)})
	seedMessage(t, s, "M1", "searchable hello", time.Unix(1700000000, 123456000).UTC())
	res := get(t, mux, "/app/search?q=hello&channel=Cdev")
	body := res.Body.String()
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, body)
	}
	requireContains(t, "search page", body,
		"searchable <mark>hello</mark>",
		"Search results",
		`href="/app?channel=Cdev"`,
		"Ada Developer",
		"#general",
	)
	// A result opens the message where it lives, anchored, instead of opening it
	// as an empty thread.
	result := regexp.MustCompile(`<a class="result" href="([^"]+)">`).FindStringSubmatch(body)
	if result == nil {
		t.Fatalf("no result link: %s", body)
	}
	link := strings.ReplaceAll(result[1], "&amp;", "&")
	if !strings.Contains(link, "channel=Cdev") || !strings.HasSuffix(link, "#message-M1") || strings.Contains(link, "thread=") {
		t.Fatalf("result link=%q", link)
	}
	// The link positions the timeline on the window that ends at the hit, so the
	// anchor is on the page it opens.
	if !strings.Contains(link, "before=") {
		t.Fatalf("result link does not position the timeline: %q", link)
	}
	opened := get(t, mux, strings.SplitN(link, "#", 2)[0])
	if opened.Code != http.StatusOK {
		t.Fatalf("open result status=%d body=%s", opened.Code, opened.Body)
	}
	requireContains(t, "opened search result", opened.Body.String(), `id="message-M1"`, "searchable hello")
}

func TestSearchRecentHistoryAndTypeaheadUseRealVisibleDestinations(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedMessage(t, s, "Mrecent", "deployment checklist", time.Unix(1_700_000_000, 0).UTC())
	file := domain.File{
		ID: "Fnotes", WorkspaceID: "T1", Uploader: "U1", Name: "release-notes.txt", Title: "Release notes",
		MIMEType: "text/plain", BlobKey: "notes", SharedChannels: []domain.ConversationID{"Cdev"}, CreatedAt: time.Unix(1_700_000_100, 0).UTC(),
	}
	if err := s.CreateFile(context.Background(), file, events.Event{ID: "EFnotes", WorkspaceID: "T1", Topic: "file.created", CreatedAt: file.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "private-owner"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "Cprivate", WorkspaceID: "T1", Name: "leadership", IsPrivate: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cprivate", "U2"); err != nil {
		t.Fatal(err)
	}
	privateFile := domain.File{
		ID: "Fprivate", WorkspaceID: "T1", Uploader: "U2", Name: "secret-roadmap.txt", Title: "Secret roadmap",
		MIMEType: "text/plain", BlobKey: "secret", SharedChannels: []domain.ConversationID{"Cprivate"}, CreatedAt: time.Unix(1_700_000_200, 0).UTC(),
	}
	if err := s.CreateFile(context.Background(), privateFile, events.Event{ID: "EFprivate", WorkspaceID: "T1", Topic: "file.created", CreatedAt: privateFile.CreatedAt}); err != nil {
		t.Fatal(err)
	}

	searched := get(t, mux, "/app/search?q=deployment&channel=Cdev")
	if searched.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", searched.Code, searched.Body)
	}
	blank := get(t, mux, "/app/search?channel=Cdev")
	if blank.Code != http.StatusOK {
		t.Fatalf("blank search status=%d body=%s", blank.Code, blank.Body)
	}
	requireContains(t, "recent search page", blank.Body.String(),
		"Recent searches", "deployment", `role="combobox"`, `aria-autocomplete="list"`, `aria-expanded="false"`,
	)

	decodeSuggestions := func(target string) []searchSuggestion {
		t.Helper()
		response := get(t, mux, target)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body)
		}
		var payload searchSuggestionsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v", target, err)
		}
		return payload.Items
	}
	recent := decodeSuggestions("/app/search/suggestions?channel=Cdev")
	if len(recent) != 1 || recent[0].Kind != "recent" || recent[0].Label != "deployment" || !strings.HasPrefix(recent[0].URL, "/app/search?") {
		t.Fatalf("recent suggestions = %+v", recent)
	}
	people := decodeSuggestions("/app/search/suggestions?q=Ada&channel=Cdev")
	channels := decodeSuggestions("/app/search/suggestions?q=gener&channel=Cdev")
	files := decodeSuggestions("/app/search/suggestions?q=release&channel=Cdev")
	if len(people) != 1 || people[0].Kind != "person" || people[0].URL != "/app/members?user=U1" {
		t.Fatalf("people suggestions = %+v", people)
	}
	if len(channels) != 1 || channels[0].Kind != "channel" || channels[0].URL != "/app?channel=Cdev" {
		t.Fatalf("channel suggestions = %+v", channels)
	}
	if len(files) != 1 || files[0].Kind != "file" || files[0].URL != "/api/files/Fnotes" {
		t.Fatalf("file suggestions = %+v", files)
	}
	private := decodeSuggestions("/app/search/suggestions?q=secret&channel=Cdev")
	if len(private) != 0 {
		t.Fatalf("private file leaked through typeahead: %+v", private)
	}

	invalid := get(t, mux, "/app/search/suggestions?q="+url.QueryEscape(strings.Repeat("x", 501)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("overlong query status=%d body=%s", invalid.Code, invalid.Body)
	}
}

func TestSearchHistoryFailureIsHandledWithoutDiscardingSearchResults(t *testing.T) {
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("Cdev", "U1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{
		WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	seedMessage(t, s, "Mhistory-failure", "search result survives", time.Unix(1_700_000_000, 0).UTC())
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	historyErr := errors.New("search history unavailable")
	handler, err := NewHandler(service.Messages{
		Store: failingSearchHistoryStore{Store: s, recordErr: historyErr, listErr: historyErr},
	}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)

	searched := get(t, mux, "/app/search?q=survives&channel=Cdev")
	if searched.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", searched.Code, searched.Body)
	}
	requireContains(t, "search history write failure", searched.Body.String(),
		"search result <mark>survives</mark>",
		"Search completed, but it could not be added to recent searches.",
		`role="status"`,
	)
	blank := get(t, mux, "/app/search?channel=Cdev")
	if blank.Code != http.StatusServiceUnavailable {
		t.Fatalf("recent history read status=%d body=%s", blank.Code, blank.Body)
	}
	requireContains(t, "search history read failure", blank.Body.String(), "Recent searches are temporarily unavailable.")
}

func TestSearchPageSupportsTypedResultsFiltersAndConversationScope(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedConversation(domain.Conversation{ID: "Cother", WorkspaceID: "T1", Name: "other"})
	s.SeedConversationMember("Cother", "U1")
	seedMessage(t, s, "Mscope", "needle in general", time.Unix(1_700_000_100, 0).UTC())
	other := domain.Message{ID: "Mother", WorkspaceID: "T1", Conversation: "Cother", AuthorID: "U1", Text: "needle elsewhere", CreatedAt: time.Unix(1_700_000_200, 0).UTC()}
	if err := s.CreateMessage(context.Background(), other, events.Event{ID: "EMother", WorkspaceID: "T1", Topic: "message.created", CreatedAt: other.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	file := domain.File{ID: "Fneedle", WorkspaceID: "T1", Uploader: "U1", Name: "needle.txt", Title: "Needle notes", MIMEType: "text/plain", BlobKey: "needle", SharedChannels: []domain.ConversationID{"Cdev"}, CreatedAt: time.Unix(1_700_000_300, 0).UTC()}
	if err := s.CreateFile(context.Background(), file, events.Event{ID: "EFneedle", WorkspaceID: "T1", Topic: "file.created", CreatedAt: file.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	otherFile := domain.File{ID: "Fother", WorkspaceID: "T1", Uploader: "U1", Name: "needle-other.txt", Title: "Needle elsewhere", MIMEType: "text/plain", BlobKey: "needle-other", SharedChannels: []domain.ConversationID{"Cother"}, CreatedAt: time.Unix(1_700_000_400, 0).UTC()}
	if err := s.CreateFile(context.Background(), otherFile, events.Event{ID: "EFother", WorkspaceID: "T1", Topic: "file.created", CreatedAt: otherFile.CreatedAt}); err != nil {
		t.Fatal(err)
	}

	blankScope := get(t, mux, "/app/search?type=messages&scope=channel&channel=Cdev")
	requireContains(t, "blank scoped search", blankScope.Body.String(), "Searching only this conversation", `name="scope" value="channel"`)

	scoped := get(t, mux, "/app/search?q=needle&type=messages&scope=channel&channel=Cdev&from=U1&order=newest")
	if scoped.Code != http.StatusOK {
		t.Fatalf("scoped status=%d body=%s", scoped.Code, scoped.Body)
	}
	requireContains(t, "scoped search", scoped.Body.String(), "<mark>needle</mark> in general", "Searching only this conversation", "Messages", "Files", "People", "Channels", `option value="U1" selected`)
	requireMissing(t, "scoped search", scoped.Body.String(), "<mark>needle</mark> elsewhere")

	files := get(t, mux, "/app/search?q=needle&type=files&has=text&channel=Cdev")
	if files.Code != http.StatusOK {
		t.Fatalf("files status=%d body=%s", files.Code, files.Body)
	}
	requireContains(t, "file search", files.Body.String(), "<mark>Needle</mark> notes", "<mark>Needle</mark> elsewhere", "text/plain", "/api/files/Fneedle", "2 results in files")
	scopedFiles := get(t, mux, "/app/search?q=needle&type=files&scope=channel&channel=Cdev")
	requireContains(t, "scoped file search", scopedFiles.Body.String(), "<mark>Needle</mark> notes", "1 results in files")
	requireMissing(t, "scoped file search", scopedFiles.Body.String(), "<mark>Needle</mark> elsewhere")

	people := get(t, mux, "/app/search?q=Ada&type=people&channel=Cdev")
	requireContains(t, "people search", people.Body.String(), "Ada Developer", `/app/members?user=U1`)
	excludedPeople := get(t, mux, "/app/search?q=Ada+-Developer&type=people&channel=Cdev")
	requireMissing(t, "excluded people search", excludedPeople.Body.String(), "Ada Developer")
	channels := get(t, mux, "/app/search?q=general&type=channels&channel=Cdev")
	requireContains(t, "channel search", channels.Body.String(), "# <mark>general</mark>", `/app?channel=Cdev`)
}

func TestWorkspaceRendersStructuredMessagesWithoutDestructiveEditor(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	message := domain.Message{
		ID:           "Mrich",
		WorkspaceID:  "T1",
		Conversation: "Cdev",
		AuthorID:     "U1",
		AppID:        "A1",
		Text:         "notification fallback must not be repeated",
		Blocks:       `[{"type":"header","text":{"type":"plain_text","text":"Deployment complete"}},{"type":"section","text":{"type":"mrkdwn","text":"*Production* is healthy"},"fields":[{"type":"plain_text","text":"Region: eu-west"}]},{"type":"actions","block_id":"deployment","elements":[{"type":"button","action_id":"view_build","text":{"type":"plain_text","text":"View build"},"value":"842"}]}]`,
		Attachments:  `[{"author_name":"CI","title":"Build 842","title_link":"https://example.com/build/842","text":"All checks passed","fields":[{"title":"Duration","value":"3m 12s"}],"footer":"Continuous delivery"}]`,
		Unfurls:      map[string]string{"https://example.com/runbook": `{"title":"Production runbook","text":"Recovery steps"}`},
		CreatedAt:    created,
	}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "Erich", WorkspaceID: "T1", Topic: "message.created", Payload: "Mrich", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "rich message", body,
		"Deployment complete",
		"<strong>Production</strong> is healthy",
		"Region: eu-west",
		"View build",
		"Build 842",
		"All checks passed",
		"Duration",
		"3m 12s",
		"Production runbook",
		"Recovery steps",
		`aria-label="Structured message"`,
		`aria-label="Attachments"`,
		`aria-label="Link previews"`,
	)
	requireMissing(t, "rich message", body, "notification fallback must not be repeated", `action="/app/message/update?channel=Cdev`)
	requireContains(t, "rich message deletion", body, `action="/app/message/delete?channel=Cdev`)
}

func TestWorkspaceDiscoversAndDispatchesInstalledAppShortcuts(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	now := time.Now().UTC()
	key := []byte(strings.Repeat("k", 32))
	verification, err := secretbox.Seal(key, "app:A1:verification-token", "verification-token")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Tickets"},"features":{"slash_commands":[{"command":"/ticket","description":"Create a ticket","usage_hint":"summary","should_escape":true}],"shortcuts":[{"name":"Create ticket","callback_id":"create_ticket","description":"Create a ticket","type":"global"},{"name":"Attach ticket","callback_id":"attach_ticket","description":"Attach this message","type":"message"}]},"oauth_config":{"scopes":{"bot":["commands"]}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
	if err := s.CreateApp(context.Background(), domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Tickets", ClientID: "shortcut-client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verification,
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "shortcut-client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: "Mshortcut", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", AppID: "A1", Text: "Attach me", CreatedAt: now}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "Eshortcut", WorkspaceID: "T1", Topic: "message.created", Payload: "Mshortcut", CreatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "app shortcuts", body,
		"Shortcuts", "Create ticket", "Create a ticket", "More actions", "Attach ticket", "Attach this message",
		`aria-label="Shortcuts and slash commands"`, `data-slash-command="/ticket"`, "summary", "Tickets",
		`data-slash-command="/shrug"`,
		`id="shortcut-browser"`, `id="shortcut-browser-query"`, `aria-label="Browse shortcuts"`,
		`data-browser-command="/ticket"`, `data-browser-command="/shrug"`,
		`action="/app/shortcut"`, `name="app_id" value="A1"`, `name="callback_id" value="create_ticket"`,
	)
	requireContains(t, "shortcut browser behavior", progressiveEnhancementScript,
		"shortcutBrowser.showModal()", "filterShortcuts()", "replaceComposerRange(start,end,command+' '",
	)

	builtIn := postForm(t, mux, "/app/message?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "text": {"/shrug release day"},
	}.Encode(), true)
	if builtIn.Code != http.StatusOK {
		t.Fatalf("built-in status=%d body=%s", builtIn.Code, builtIn.Body)
	}
	requireContains(t, "built-in slash response", builtIn.Body.String(), `release day ¯\_(ツ)_/¯`)

	response := postForm(t, mux, "/app/shortcut", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "channel": {"Cdev"}, "app_id": {"A1"}, "callback_id": {"create_ticket"},
	}.Encode(), true)
	if response.Code >= 400 {
		t.Fatalf("shortcut status=%d body=%s", response.Code, response.Body)
	}
	interaction, found, err := s.ClaimSocketModeInteraction(context.Background(), "A1", "socket", time.Minute)
	if err != nil || !found || !strings.Contains(interaction.Payload, `"type":"shortcut"`) || !strings.Contains(interaction.Payload, `"callback_id":"create_ticket"`) {
		t.Fatalf("interaction=%+v found=%v err=%v", interaction, found, err)
	}
}

func TestWorkspaceRendersAndSubmitsSocketModeModals(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	now := time.Now().UTC()
	key := []byte(strings.Repeat("k", 32))
	signing, err := secretbox.Seal(key, "app:A1:signing-secret", "signing-secret")
	if err != nil {
		t.Fatal(err)
	}
	verification, err := secretbox.Seal(key, "app:A1:verification-token", "verification-token")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Modal app"},"oauth_config":{"scopes":{"bot":["commands"]}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
	if err := s.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Modal app", ClientID: "modal-client",
		SigningSecretHash: domain.HashToken("signing-secret"), SigningSecretCiphertext: signing,
		VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verification,
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "modal-client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	view := domain.View{
		ID: "Vmodal", AppID: "A1", WorkspaceID: "T1", UserID: "U1", Type: "modal",
		Payload: `{"type":"modal","title":{"type":"plain_text","text":"Create release"},"close":{"type":"plain_text","text":"Cancel"},"submit":{"type":"plain_text","text":"Create"},"notify_on_close":true,"blocks":[{"type":"input","block_id":"release_name","label":{"type":"plain_text","text":"Release name"},"hint":{"type":"plain_text","text":"Use a descriptive name"},"element":{"type":"plain_text_input","action_id":"name","placeholder":{"type":"plain_text","text":"July release"}}},{"type":"input","block_id":"environment","label":{"type":"plain_text","text":"Environment"},"element":{"type":"static_select","action_id":"environment_select","placeholder":{"type":"plain_text","text":"Choose an environment"},"options":[{"text":{"type":"plain_text","text":"Production"},"value":"production"},{"text":{"type":"plain_text","text":"Staging"},"value":"staging"}]}},{"type":"actions","block_id":"preview","elements":[{"type":"button","action_id":"preview_release","text":{"type":"plain_text","text":"Preview release"},"value":"preview"}]}]}`,
		Hash:    "hash-modal", RootViewID: "Vmodal", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateView(ctx, view, events.Event{ID: "E-modal", WorkspaceID: "T1", Topic: "view.opened", Payload: "Vmodal", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "app modal", body,
		`role="dialog"`, `aria-modal="true"`, "Create release", "Release name", "Use a descriptive name",
		`name="input_0"`, `name="input_1"`, "Production", "Staging",
		"Preview release", `formaction="/app/view/action?channel=Cdev"`, `name="modal_action" value="0"`,
		`action="/app/view/submit?channel=Cdev"`, `action="/app/view/close?channel=Cdev"`,
	)
	response := postForm(t, mux, "/app/view/action?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vmodal"}, "modal_action": {"0"},
		"input_0": {"July launch"}, "input_1": {"production"},
	}.Encode(), false)
	if response.Code != http.StatusOK {
		t.Fatalf("modal action status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "modal action result", response.Body.String(), "The app action ran", `value="July launch"`, `value="production" selected`)
	interaction, found, err := s.ClaimSocketModeInteraction(ctx, "A1", "modal-client", time.Minute)
	if err != nil || !found || !strings.Contains(interaction.Payload, `"type":"block_actions"`) ||
		!strings.Contains(interaction.Payload, `"action_id":"preview_release"`) ||
		!strings.Contains(interaction.Payload, `"value":"July launch"`) {
		t.Fatalf("modal action interaction=%+v found=%v err=%v", interaction, found, err)
	}
	messages := service.Messages{Store: s, AppCredentialKey: key}
	if err := messages.HandleSocketModeResponse(ctx, "A1", interaction.EnvelopeID, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.AckSocketModeInteraction(ctx, "A1", interaction.EnvelopeID, "modal-client"); err != nil {
		t.Fatal(err)
	}
	response = postForm(t, mux, "/app/view/submit?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vmodal"},
		"input_0": {"July launch"}, "input_1": {"production"},
	}.Encode(), false)
	if response.Code != http.StatusAccepted {
		t.Fatalf("modal submission status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "pending modal", response.Body.String(), "The app is checking that modal", `value="July launch"`, `value="production" selected`)
	interaction, found, err = s.ClaimSocketModeInteraction(ctx, "A1", "modal-client", time.Minute)
	if err != nil || !found {
		t.Fatalf("modal interaction=%+v found=%v err=%v", interaction, found, err)
	}
	var payload struct {
		Type string `json:"type"`
		View struct {
			State struct {
				Values map[string]map[string]map[string]any `json:"values"`
			} `json:"state"`
		} `json:"view"`
	}
	if err := json.Unmarshal([]byte(interaction.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Type != "view_submission" ||
		payload.View.State.Values["release_name"]["name"]["value"] != "July launch" {
		t.Fatalf("modal payload=%s", interaction.Payload)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", interaction.EnvelopeID, []byte(`{"response_action":"errors","errors":{"release_name":"Use the full release name"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.AckSocketModeInteraction(ctx, "A1", interaction.EnvelopeID, "modal-client"); err != nil {
		t.Fatal(err)
	}
	body = get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "modal validation", body, "Use the full release name", `value="July launch"`, `aria-invalid="true"`)

	response = postForm(t, mux, "/app/view/submit?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vmodal"},
		"input_0": {"July 2026 launch"}, "input_1": {"production"},
	}.Encode(), false)
	if response.Code != http.StatusAccepted {
		t.Fatalf("second modal submission status=%d body=%s", response.Code, response.Body)
	}
	interaction, found, err = s.ClaimSocketModeInteraction(ctx, "A1", "modal-client", time.Minute)
	if err != nil || !found {
		t.Fatalf("second modal interaction=%+v found=%v err=%v", interaction, found, err)
	}
	if err := messages.HandleSocketModeResponse(ctx, "A1", interaction.EnvelopeID, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.AckSocketModeInteraction(ctx, "A1", interaction.EnvelopeID, "modal-client"); err != nil {
		t.Fatal(err)
	}
	requireMissing(t, "closed modal", get(t, mux, "/app?channel=Cdev").Body.String(), `role="dialog"`, "Create release")

	view.ID, view.RootViewID = "Vclose", "Vclose"
	view.Hash = "hash-close"
	view.CreatedAt, view.UpdatedAt = now.Add(time.Minute), now.Add(time.Minute)
	if err := s.CreateView(ctx, view, events.Event{ID: "E-close", WorkspaceID: "T1", Topic: "view.opened", Payload: "Vclose", CreatedAt: view.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	response = postForm(t, mux, "/app/view/close?channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "view_id": {"Vclose"}, "clear": {"false"},
	}.Encode(), false)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("modal close status=%d body=%s", response.Code, response.Body)
	}
	interaction, found, err = s.ClaimSocketModeInteraction(ctx, "A1", "modal-client", time.Minute)
	if err != nil || !found || !strings.Contains(interaction.Payload, `"type":"view_closed"`) {
		t.Fatalf("view_closed interaction=%+v found=%v err=%v", interaction, found, err)
	}
}

func TestMemberDirectoryDoesNotOfferProfileWritesWithoutScope(t *testing.T) {
	_, mux := browserWorkspace(t, []string{string(auth.ScopeUsersRead)})
	response := get(t, mux, "/app/members")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "read-only member directory", body, "allow viewing profiles but not changing yours")
	requireMissing(t, "read-only member directory", body, `action="/app/profile"`, "Save changes")
}

func TestSearchNamesDirectMessagesAfterTheirParticipants(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	s.SeedConversation(domain.Conversation{ID: "Cdm", WorkspaceID: "T1", Name: "direct", IsDirect: true, IsPrivate: true})
	s.SeedConversationMember("Cdm", "U1")
	s.SeedConversationMember("Cdm", "U2")
	created := time.Unix(1700000000, 123456000).UTC()
	message := domain.Message{ID: "Mdm", WorkspaceID: "T1", Conversation: "Cdm", AuthorID: "U2", Text: "private needle", CreatedAt: created}
	if err := s.CreateMessage(context.Background(), message, events.Event{ID: "Edm", WorkspaceID: "T1", Topic: "message.created", Payload: "Mdm", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}

	body := get(t, mux, "/app/search?q=needle&channel=Cdm").Body.String()
	requireContains(t, "direct-message search result", body, `<span class="channel">Bob Builder</span>`, "private <mark>needle</mark>")
	requireMissing(t, "direct-message search result", body, "#direct")
}

// TestSearchReportsValidationInsteadOfAnOutage covers the defect where an
// over-long query answered with a bare "search unavailable" page.
func TestSearchReportsValidationInsteadOfAnOutage(t *testing.T) {
	_, mux := browserWorkspace(t, []string{string(auth.ScopeSearchRead)})
	response := get(t, mux, "/app/search?q="+strings.Repeat("a", 600)+"&channel=Cdev")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "search page", response.Body.String(), "Enter between one and 500 characters", `maxlength="500"`, `href="/app?channel=Cdev"`)
}

func TestProgressiveEnhancementHandlesRedirectResponses(t *testing.T) {
	if !strings.Contains(progressiveEnhancementScript, "document.addEventListener('submit'") {
		t.Fatal("progressive enhancement does not delegate form handling")
	}
	if !strings.Contains(progressiveEnhancementScript, "response.headers.get('HX-Redirect')") {
		t.Fatal("progressive enhancement does not handle HX-Redirect")
	}
	if !strings.Contains(progressiveEnhancementScript, "if(response.status===204)return ''") {
		t.Fatal("progressive enhancement does not handle empty 204 responses")
	}
	if !strings.Contains(progressiveEnhancementScript, "form===composer?errorBox:actionBox") {
		t.Fatal("unrelated mutation failures are still rendered as composer errors")
	}
	if !strings.Contains(progressiveEnhancementScript, "clearError(form)") {
		t.Fatal("one mutation can still clear another control's error")
	}
	if !strings.Contains(progressiveEnhancementScript, "setNav(false,false)") || !strings.Contains(progressiveEnhancementScript, "navToggle.focus()") {
		t.Fatal("the narrow navigation does not close on Escape and restore focus")
	}
}

func TestWebFormRejectsRepeatedFields(t *testing.T) {
	_, mux := browserWorkspace(t, []string{string(auth.ScopeChatWrite), string(auth.ScopeChannelsHistory)})
	res := postForm(t, mux, "/app/message", "text=one&text=two", false)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestWebSessionRevocationClearsCookieAndDurablyInvalidates(t *testing.T) {
	s := memory.New()
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	addBrowserCookies(req)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/signed-out" {
		t.Fatalf("status=%d location=%q", res.Code, res.Header().Get("Location"))
	}
	if !strings.Contains(res.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("session cookie was not cleared: %q", res.Header().Get("Set-Cookie"))
	}
	if !strings.Contains(res.Header().Get("Set-Cookie"), "Domain=example.com") {
		t.Fatalf("shared session cookie domain was not cleared: %q", res.Header().Get("Set-Cookie"))
	}
	record, err := s.LookupSession(context.Background(), "session")
	if err != nil || !record.Revoked {
		t.Fatalf("session=%+v err=%v", record, err)
	}
}

type stubRevoker func(context.Context, string) error

func (f stubRevoker) RevokeSession(ctx context.Context, token string) error { return f(ctx, token) }

// TestSignOutAlwaysEndsTheBrowserSession covers the defect where a revocation
// failure left the session cookie in place and answered with bare status text,
// stranding the user in a session they asked to end.
func TestSignOutAlwaysEndsTheBrowserSession(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		err      error
		status   int
		location string
	}{
		{name: "store outage", err: errors.New("session store is down"), status: http.StatusServiceUnavailable},
		{name: "revoked", err: nil, status: http.StatusSeeOther, location: "/signed-out"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := memory.New()
			if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			authenticator, err := auth.NewBrowser(s)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := NewHandler(service.Messages{Store: s}, authenticator, stubRevoker(func(context.Context, string) error { return testCase.err }), "C1", "")
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			handler.Register(mux)
			request := httptest.NewRequest(http.MethodPost, "/logout", nil)
			addBrowserCookies(request)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != testCase.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if !strings.Contains(response.Header().Get("Set-Cookie"), "Max-Age=0") {
				t.Fatalf("the session cookie survived sign-out: %q", response.Header().Get("Set-Cookie"))
			}
			if testCase.location != "" && response.Header().Get("Location") != testCase.location {
				t.Fatalf("location=%q", response.Header().Get("Location"))
			}
			if testCase.status == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "You are signed out of this browser") {
				t.Fatalf("failed sign-out did not explain itself: %s", response.Body)
			}
		})
	}
}

// TestSignOutTreatsAnAbsentSessionRecordAsSuccess is the case the previous
// implementation reported as an outage.
func TestSignOutTreatsAnAbsentSessionRecordAsSuccess(t *testing.T) {
	s := memory.New()
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, stubRevoker(func(context.Context, string) error { return store.ErrNotFound }), "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/signed-out" {
		t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body)
	}
	if !strings.Contains(response.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("the session cookie survived sign-out: %q", response.Header().Get("Set-Cookie"))
	}
}

func TestMembersPageRendersDurableProfiles(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", RealName: "Alice Example", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:", StatusExpiration: time.Unix(4102444800, 0).UTC()}})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", Profile: domain.UserProfile{StatusText: "Heads down"}})
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{string(auth.ScopeUsersRead), string(auth.ScopeUsersWrite)}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator, s, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	res := get(t, mux, "/app/members")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Available") || !strings.Contains(res.Body.String(), ":wave:") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	// The form mirrors the limits the service enforces without exposing the
	// seven size-specific image fields in Slack's API model.
	requireContains(t, "profile form", res.Body.String(), `maxlength="80"`, `maxlength="100"`, `name="avatar_url"`, `type="url" maxlength="2048"`, `name="status_expiration" value="4102444800"`, `action="/app/presence"`, "Active (automatic)", "automatic; activity unavailable", "💬 Heads down", "Schedule a status", "No scheduled statuses.")
	requireMissing(t, "profile form", res.Body.String(), `name="image_24"`, `name="image_1024"`)
	updateResult := postForm(t, mux, "/app/profile", "display_name=updated&status_text=Ready&status_emoji=%3Aok%3A&status_expiration=4102444800&avatar_url=https%3A%2F%2Fexample.test%2Favatar.png", false)
	if updateResult.Code != http.StatusSeeOther {
		t.Fatalf("profile update status=%d body=%s", updateResult.Code, updateResult.Body)
	}
	stored, err := s.GetUser(context.Background(), "U1")
	if err != nil || stored.Profile.DisplayName != "updated" || stored.Profile.StatusText != "Ready" || stored.Profile.StatusExpiration.Unix() != 4102444800 || stored.Profile.Image24 != "https://example.test/avatar.png" || stored.Profile.Image1024 != "https://example.test/avatar.png" {
		t.Fatalf("updated profile=%+v err=%v", stored.Profile, err)
	}
	presenceResult := postForm(t, mux, "/app/presence", "presence=away", false)
	if presenceResult.Code != http.StatusSeeOther {
		t.Fatalf("presence update status=%d body=%s", presenceResult.Code, presenceResult.Body)
	}
	stored, _ = s.GetUser(context.Background(), "U1")
	if stored.Presence != domain.PresenceAway {
		t.Fatalf("updated presence=%q", stored.Presence)
	}
	clearResult := postForm(t, mux, "/app/profile", "display_name=updated&status_text=Ready&status_emoji=%3Aok%3A&status_expiration=4102444800&clear_status=true&avatar_url=https%3A%2F%2Fexample.test%2Favatar.png", false)
	if clearResult.Code != http.StatusSeeOther {
		t.Fatalf("clear status=%d body=%s", clearResult.Code, clearResult.Body)
	}
	stored, _ = s.GetUser(context.Background(), "U1")
	if stored.Profile.StatusText != "" || stored.Profile.StatusEmoji != "" || !stored.Profile.StatusExpiration.IsZero() {
		t.Fatalf("cleared profile=%+v", stored.Profile)
	}
	scheduleResult := postForm(t, mux, "/app/status/schedule", "status_text=Focus&status_emoji=%3Adart%3A&starts_at=4102448400&ends_at=4102452000", false)
	if scheduleResult.Code != http.StatusSeeOther {
		t.Fatalf("schedule status=%d body=%s", scheduleResult.Code, scheduleResult.Body)
	}
	scheduled, err := (service.Messages{Store: s}).ScheduledUserStatuses(context.Background(), "T1", "U1")
	if err != nil || len(scheduled) != 1 {
		t.Fatalf("scheduled=%+v err=%v", scheduled, err)
	}
	stored, _ = s.GetUser(context.Background(), "U1")
	if stored.Profile.StatusText != "" {
		t.Fatalf("future status changed current profile=%+v", stored.Profile)
	}
	scheduledPage := get(t, mux, "/app/members")
	requireContains(t, "scheduled status", scheduledPage.Body.String(), `value="Focus"`, `value=":dart:"`, `value="4102448400"`, `value="4102452000"`, "Cancel status")
	updateScheduled := postForm(t, mux, "/app/status/scheduled/update", "id="+url.QueryEscape(string(scheduled[0].ID))+"&status_text=Deep+work&status_emoji=%3Aheadphones%3A&starts_at=4102455600&ends_at=4102459200", false)
	if updateScheduled.Code != http.StatusSeeOther {
		t.Fatalf("update scheduled status=%d body=%s", updateScheduled.Code, updateScheduled.Body)
	}
	scheduled, _ = (service.Messages{Store: s}).ScheduledUserStatuses(context.Background(), "T1", "U1")
	if len(scheduled) != 1 || scheduled[0].StatusText != "Deep work" || scheduled[0].StartsAt.Unix() != 4102455600 {
		t.Fatalf("updated scheduled=%+v", scheduled)
	}
	cancelScheduled := postForm(t, mux, "/app/status/scheduled/delete", "id="+url.QueryEscape(string(scheduled[0].ID)), false)
	if cancelScheduled.Code != http.StatusSeeOther {
		t.Fatalf("cancel scheduled status=%d body=%s", cancelScheduled.Code, cancelScheduled.Body)
	}
	scheduled, _ = (service.Messages{Store: s}).ScheduledUserStatuses(context.Background(), "T1", "U1")
	if len(scheduled) != 0 {
		t.Fatalf("scheduled status survived cancel=%+v", scheduled)
	}
}

// TestRejectedProfileKeepsEveryFieldAndExplainsTheLimit covers the defect where
// a rejected save answered with bare status text and lost every field.
func TestRejectedProfileKeepsEveryFieldAndExplainsTheLimit(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	response := postForm(t, mux, "/app/profile", "display_name="+strings.Repeat("n", 81)+"&status_text=Ready&status_emoji=%3Aok%3A&avatar_url=https%3A%2F%2Fexample.test%2Favatar.png", false)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	requireContains(t, "rejected profile", body,
		"at most 80 characters",
		`value="`+strings.Repeat("n", 81)+`"`,
		`value="Ready"`,
		`value=":ok:"`,
		`value="https://example.test/avatar.png"`,
	)
}

func TestRejectedScheduledStatusKeepsEveryFieldAndExplainsTheContract(t *testing.T) {
	_, mux := browserWorkspace(t, auth.AllScopes())
	response := postForm(t, mux, "/app/status/schedule", "status_text=Focus&status_emoji=%3Anot_a_workspace_emoji%3A&starts_at=4102448400&ends_at=4102452000", false)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "rejected scheduled status", response.Body.String(),
		"valid workspace emoji",
		`value="Focus"`,
		`value=":not_a_workspace_emoji:"`,
		`value="4102448400"`,
		`value="4102452000"`,
	)
}

// TestMembersPageOffersADirectMessageAction covers the dead
// POST /app/conversation/open route: nothing in the interface reached it.
func TestMembersPageOffersADirectMessageAction(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	body := get(t, mux, "/app/members").Body.String()
	requireContains(t, "members page", body, `action="/app/conversation/open"`, `name="users" value="U2"`, "Message Bob Builder")
	if strings.Contains(body, `name="users" value="U1"`) {
		t.Fatal("the members page offers to open a direct conversation with the signed-in user")
	}
}

// TestTruncatedListsExposeTheirRemainder covers the defect where the sidebar,
// the member directory and the search results silently stopped at their page
// limit with no way to reach the rest.
func TestTruncatedListsExposeTheirRemainder(t *testing.T) {
	t.Run("members", func(t *testing.T) {
		s, mux := browserWorkspace(t, []string{string(auth.ScopeUsersRead)})
		for index := 0; index < memberWindow+1; index++ {
			s.SeedUser(domain.User{ID: domain.UserID(fmt.Sprintf("U1%03d", index)), WorkspaceID: "T1", Name: fmt.Sprintf("member-%03d", index)})
		}
		body := get(t, mux, "/app/members").Body.String()
		more := regexp.MustCompile(`<a href="(/app/members\?cursor=[^"]+)">Show more members</a>`).FindStringSubmatch(body)
		if more == nil {
			t.Fatalf("the member directory hides its remainder: %s", body)
		}
		second := get(t, mux, strings.ReplaceAll(more[1], "&amp;", "&"))
		if second.Code != http.StatusOK {
			t.Fatalf("second member page status=%d body=%s", second.Code, second.Body)
		}
	})
	t.Run("search", func(t *testing.T) {
		s, mux := browserWorkspace(t, []string{string(auth.ScopeSearchRead)})
		base := time.Unix(1700000000, 0).UTC()
		for index := 0; index < searchWindow+1; index++ {
			seedMessage(t, s, domain.MessageID(fmt.Sprintf("M%03d", index)), "needle", base.Add(time.Duration(index)*time.Second))
		}
		body := get(t, mux, "/app/search?q=needle&channel=Cdev").Body.String()
		more := regexp.MustCompile(`<a href="(/app/search\?[^"]+)">Show more results</a>`).FindStringSubmatch(body)
		if more == nil {
			t.Fatalf("search hides its remainder: %s", body)
		}
		second := get(t, mux, strings.ReplaceAll(more[1], "&amp;", "&"))
		if second.Code != http.StatusOK {
			t.Fatalf("second results page status=%d body=%s", second.Code, second.Body)
		}
	})
	t.Run("conversations", func(t *testing.T) {
		s, mux := browserWorkspace(t, []string{string(auth.ScopeChannelsHistory)})
		for index := 0; index < conversationWindow+1; index++ {
			s.SeedConversation(domain.Conversation{ID: domain.ConversationID(fmt.Sprintf("C%03d", index)), WorkspaceID: "T1", Name: fmt.Sprintf("channel-%03d", index)})
		}
		body := get(t, mux, "/app?channel=Cdev").Body.String()
		more := regexp.MustCompile(`<a class="side-more" href="(/app\?[^"]+)">More conversations</a>`).FindStringSubmatch(body)
		if more == nil {
			t.Fatalf("the sidebar hides its remainder: %s", body)
		}
		second := get(t, mux, strings.ReplaceAll(more[1], "&amp;", "&"))
		if second.Code != http.StatusOK {
			t.Fatalf("second sidebar page status=%d body=%s", second.Code, second.Body)
		}
	})
}

func TestReactionAndPinMutationsPersist(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	created := time.Unix(1700000000, 123456000).UTC()
	seedMessage(t, s, "M1", "hello", created)
	timestamp := string(domain.NewMessageTimestamp(created))
	reaction := postForm(t, mux, "/app/reaction?channel=Cdev&ts="+timestamp, "name=%3Awave%3A", true)
	if reaction.Code != http.StatusNoContent {
		t.Fatalf("reaction status=%d body=%s", reaction.Code, reaction.Body)
	}
	reactions, _, _, err := s.ListReactions(context.Background(), "M1", domain.PageRequest{Limit: 10})
	if err != nil || len(reactions) != 1 || reactions[0].Name != "wave" {
		t.Fatalf("reactions=%+v err=%v", reactions, err)
	}
	pin := postForm(t, mux, "/app/pin?channel=Cdev&ts="+timestamp, "", false)
	if pin.Code != http.StatusSeeOther {
		t.Fatalf("pin status=%d body=%s", pin.Code, pin.Body)
	}
	pins, _, _, err := s.ListPins(context.Background(), "Cdev", domain.PageRequest{Limit: 10})
	if err != nil || len(pins) != 1 || pins[0].Message != "M1" {
		t.Fatalf("pins=%+v err=%v", pins, err)
	}
}

func TestWebOpensNormalizedDirectConversation(t *testing.T) {
	ctx := context.Background()
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	res := postForm(t, mux, "/app/conversation/open", "users=U2%2C%20U2", false)
	if res.Code != http.StatusSeeOther || !strings.Contains(res.Header().Get("Location"), "channel=") {
		t.Fatalf("status=%d location=%q body=%s", res.Code, res.Header().Get("Location"), res.Body)
	}
	conversations, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(conversations.Conversations) != 2 {
		t.Fatalf("conversations=%+v err=%v", conversations, err)
	}
	var direct domain.Conversation
	for _, conversation := range conversations.Conversations {
		if conversation.IsDirect {
			direct = conversation
		}
	}
	if direct.ID == "" || !direct.IsPrivate {
		t.Fatalf("direct conversation=%+v", direct)
	}
}

func TestDirectMessagesSurfaceCreatesNamesClosesAndReopensCanonicalGroup(t *testing.T) {
	ctx := context.Background()
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol", RealName: "Carol Creator"})

	page := get(t, mux, "/app/dms")
	if page.Code != http.StatusOK {
		t.Fatalf("DM surface status=%d body=%s", page.Code, page.Body)
	}
	requireContains(t, "DM surface", page.Body.String(),
		"Direct messages",
		"up to nine people total",
		`name="user_U2"`,
		`name="user_U3"`,
		"Group DM name (optional)",
	)

	opened := postForm(t, mux, "/app/conversation/open", "user_U2=1&user_U3=1&name=Design+launch&return=dms", false)
	if opened.Code != http.StatusSeeOther {
		t.Fatalf("open group status=%d body=%s", opened.Code, opened.Body)
	}
	group, err := s.FindDirectConversation(ctx, "T1", []domain.UserID{"U1", "U2", "U3"})
	if err != nil || group.Name != "Design launch" || !group.IsGroupDirect {
		t.Fatalf("group=%+v err=%v", group, err)
	}
	recent := get(t, mux, "/app/dms")
	requireContains(t, "recent group DM", recent.Body.String(), "Design launch", "Save name")

	closed := postForm(t, mux, "/app/conversation/leave?channel="+string(group.ID), "", false)
	if closed.Code != http.StatusSeeOther || closed.Header().Get("Location") != "/app/dms" {
		t.Fatalf("close status=%d location=%q body=%s", closed.Code, closed.Header().Get("Location"), closed.Body)
	}
	if member, err := s.IsConversationMember(ctx, group.ID, "U1"); err != nil || !member {
		t.Fatalf("close removed membership: member=%v err=%v", member, err)
	}
	requireMissing(t, "closed DM surface", get(t, mux, "/app/dms").Body.String(), `href="/app?channel=`+string(group.ID)+`"`)
	search := get(t, mux, "/app/dms?q=Design")
	requireContains(t, "closed DM search", search.Body.String(), "Open Design launch", `name="users" value="U2,U3"`)

	reopened := postForm(t, mux, "/app/conversation/open", "user_U2=1&user_U3=1", false)
	if reopened.Code != http.StatusSeeOther || !strings.Contains(reopened.Header().Get("Location"), "channel="+string(group.ID)) {
		t.Fatalf("reopen status=%d location=%q body=%s", reopened.Code, reopened.Header().Get("Location"), reopened.Body)
	}
	same, err := s.FindDirectConversation(ctx, "T1", []domain.UserID{"U3", "U1", "U2"})
	if err != nil || same.ID != group.ID {
		t.Fatalf("reopen created a duplicate: same=%+v original=%+v err=%v", same, group, err)
	}
}

func TestDirectMessageDetailsReviewHistoryExpansionAndConvertInPlace(t *testing.T) {
	ctx := context.Background()
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Builder"})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol", RealName: "Carol Creator"})
	messages := service.Messages{Store: s}
	source, err := messages.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", source.ID, "context before adding Carol", "", ""); err != nil {
		t.Fatal(err)
	}

	details := get(t, mux, "/app?channel="+string(source.ID)+"&details=1")
	if details.Code != http.StatusOK {
		t.Fatalf("details status=%d body=%s", details.Code, details.Body)
	}
	requireContains(t, "DM add-people details", details.Body.String(),
		"Add people",
		`action="/app/conversation/add-people?channel=`+string(source.ID)+`"`,
		"Slack creates a new group DM",
	)
	requireMissing(t, "one-to-one conversion", details.Body.String(), "Change to Private")

	historyChoice := postForm(t, mux, "/app/conversation/add-people?channel="+string(source.ID), "user_U3=1", false)
	if historyChoice.Code != http.StatusOK {
		t.Fatalf("history choice status=%d body=%s", historyChoice.Code, historyChoice.Body)
	}
	requireContains(t, "DM expansion history choice", historyChoice.Body.String(),
		"Include conversation history",
		"Don’t include conversation history",
		"Include all conversation history and files",
		">Done</button>",
	)
	if existing, err := s.FindDirectConversation(ctx, "T1", []domain.UserID{"U1", "U2", "U3"}); !errors.Is(err, store.ErrNotFound) || existing.ID != "" {
		t.Fatalf("history choice mutated state: existing=%+v err=%v", existing, err)
	}

	review := postForm(t, mux, "/app/conversation/add-people?channel="+string(source.ID), "user_U3=1&history=all&stage=review", false)
	if review.Code != http.StatusOK {
		t.Fatalf("review status=%d body=%s", review.Code, review.Body)
	}
	requireContains(t, "DM expansion review", review.Body.String(),
		"Review new group DM",
		"Carol Creator",
		"All existing messages and shared files",
		"Confirm and create group DM",
	)
	if existing, err := s.FindDirectConversation(ctx, "T1", []domain.UserID{"U1", "U2", "U3"}); !errors.Is(err, store.ErrNotFound) || existing.ID != "" {
		t.Fatalf("review mutated state: existing=%+v err=%v", existing, err)
	}

	confirmed := postForm(t, mux, "/app/conversation/add-people?channel="+string(source.ID), "user_U3=1&history=all&confirm=true", false)
	if confirmed.Code != http.StatusSeeOther {
		t.Fatalf("confirm status=%d body=%s", confirmed.Code, confirmed.Body)
	}
	group, err := s.FindDirectConversation(ctx, "T1", []domain.UserID{"U1", "U2", "U3"})
	if err != nil || !group.IsGroupDirect {
		t.Fatalf("expanded group=%+v err=%v", group, err)
	}
	if !strings.Contains(confirmed.Header().Get("Location"), "channel="+string(group.ID)) {
		t.Fatalf("confirm location=%q, want group %s", confirmed.Header().Get("Location"), group.ID)
	}
	groupDetails := get(t, mux, "/app?channel="+string(group.ID)+"&details=1")
	requireContains(t, "group conversion settings", groupDetails.Body.String(),
		"Settings",
		`action="/app/conversation/convert-to-private?channel=`+string(group.ID)+`"`,
		"Change to a private channel",
		"Messages and files from this group DM will stay",
		"Change to Private",
	)

	convertedResponse := postForm(t, mux, "/app/conversation/convert-to-private?channel="+string(group.ID), "name=Project+Room", false)
	if convertedResponse.Code != http.StatusSeeOther || !strings.Contains(convertedResponse.Header().Get("Location"), "channel="+string(group.ID)) {
		t.Fatalf("conversion status=%d location=%q body=%s", convertedResponse.Code, convertedResponse.Header().Get("Location"), convertedResponse.Body)
	}
	converted, err := s.GetConversation(ctx, group.ID)
	if err != nil || converted.Name != "project-room" || !converted.IsPrivate || converted.IsGroupDirect || converted.IsDirect {
		t.Fatalf("converted=%+v err=%v", converted, err)
	}
	history, err := messages.History(ctx, "T1", "U1", group.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 3 || history.Messages[0].Text != "context before adding Carol" {
		t.Fatalf("converted history=%+v err=%v", history, err)
	}
}

// TestReadCursorFailureStillRendersTheConversation covers the defect where a
// failure to persist unread bookkeeping made the channel unreadable.
func TestReadCursorFailureStillRendersTheConversation(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "developer"})
	s.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("Cdev", "U1")
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	seedMessage(t, s, "M1", "hello", time.Unix(1700000000, 0).UTC())
	authenticator, err := auth.NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: readCursorOutage{Store: s}}, authenticator, s, "Cdev", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	response := get(t, mux, "/app?channel=Cdev")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "degraded page", response.Body.String(), "hello")
	// A read-cursor outage is reported where the write happens, in a sentence
	// that says nothing else was lost. It never blocks reading.
	marked := postForm(t, mux, "/app/read?channel=Cdev", "ts="+string(domain.NewMessageTimestamp(time.Unix(1700000000, 0).UTC())), true)
	if marked.Code != http.StatusServiceUnavailable {
		t.Fatalf("mark-read status=%d body=%s", marked.Code, marked.Body)
	}
	requireContains(t, "mark-read failure", marked.Body.String(), "The unread marker could not be moved")
}

type readCursorOutage struct {
	*memory.Store
}

func (readCursorOutage) SetReadCursor(context.Context, domain.ReadCursor, events.Event) error {
	return errors.New("read cursor store is unavailable")
}

// A permalink must open the conversation window that contains its target.
// Every permalink this product handed out used to name /archives/... on a
// host that exists nowhere AND a path no route served, so following one was
// impossible from inside or outside the app. NAV-05 requires the link to open
// the containing conversation and mark the target, with distinct safe
// outcomes for a target that is gone.
func TestArchivePermalinkOpensTheWindowContainingItsMessage(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	message := seedMessage(t, store, "Mperma", "the message a permalink names", time.Unix(1700000000, 0).UTC())
	permalink, err := (service.Messages{Store: store}).Permalink(context.Background(), "T1", "U1", "Cdev", domain.NewMessageTimestamp(message.CreatedAt))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(permalink, "/archives/Cdev/p") {
		t.Fatalf("permalink=%q, want Slack's /archives/<channel>/p<ts> shape", permalink)
	}
	response := get(t, mux, permalink)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("permalink status=%d, want %d: %s", response.Code, http.StatusSeeOther, response.Body)
	}
	location := response.Header().Get("Location")
	if !strings.HasPrefix(location, "/app?") || !strings.Contains(location, "channel=Cdev") ||
		!strings.Contains(location, "before=") || !strings.HasSuffix(location, messageAnchor(message.ID)) {
		t.Fatalf("permalink redirected to %q, want the containing window anchored on the message", location)
	}
	// Following the redirect actually shows the message: a window that does
	// not contain the target is the defect this route exists to prevent.
	// A browser sends the path and query and keeps the fragment to itself, so
	// the follow-up request drops it exactly as one would.
	page := get(t, mux, strings.Split(location, "#")[0])
	requireContains(t, "permalink window", page.Body.String(), "the message a permalink names")

	// A message that does not exist is a handled answer, not a broken page.
	missing := get(t, mux, "/archives/Cdev/p1700000000000999")
	if missing.Code == http.StatusOK || missing.Code >= http.StatusInternalServerError {
		t.Fatalf("missing permalink status=%d, want a handled failure", missing.Code)
	}
	malformed := get(t, mux, "/archives/Cdev/notatimestamp")
	if malformed.Code != http.StatusNotFound {
		t.Fatalf("malformed permalink status=%d, want %d", malformed.Code, http.StatusNotFound)
	}
}

// The fittings Slack shows around a message — the edited marker, the thread
// summary, day separators, the unread divider, the broadcast marker, and the
// system-message rendering — are all computed server-side, because the
// fragment refresh re-renders this same partial and anything computed in the
// browser is lost on the next live update. MSG-01 requires every one of them.
func TestTimelineRendersSlackMessageChrome(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: store}
	ctx := context.Background()
	yesterday := time.Now().UTC().Add(-26 * time.Hour)

	older := seedMessage(t, store, "Mold", "posted yesterday", yesterday)
	parent, err := messages.Post(ctx, "T1", "U1", "Cdev", "a message with replies", "", "")
	if err != nil {
		t.Fatal(err)
	}
	parentTS := domain.NewMessageTimestamp(parent.CreatedAt)
	if _, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{
		Conversation: "Cdev", Text: "a threaded reply", ThreadTimestamp: parentTS,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{
		Conversation: "Cdev", Text: "also to the channel", ThreadTimestamp: parentTS, ReplyBroadcast: true,
	}); err != nil {
		t.Fatal(err)
	}
	edited, err := messages.Post(ctx, "T1", "U1", "Cdev", "before the edit", "", "")
	if err != nil {
		t.Fatal(err)
	}
	changed := "after the edit"
	if _, err := messages.UpdateMessage(ctx, "T1", "U1", "Cdev", domain.NewMessageTimestamp(edited.CreatedAt), domain.MessagePatch{Text: &changed}); err != nil {
		t.Fatal(err)
	}
	// A join notice gives the system rendering something to show.
	if _, err := messages.SetConversationTopic(ctx, "T1", "U1", "Cdev", "chrome topic"); err != nil {
		t.Fatal(err)
	}
	// Read up to the older message only, so everything after it is unread.
	if _, err := messages.MarkRead(ctx, "T1", "U1", "Cdev", domain.NewMessageTimestamp(older.CreatedAt)); err != nil {
		t.Fatal(err)
	}

	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "message chrome", body,
		`class="day-separator"`,
		`class="unread-divider"`,
		`(edited)`,
		`class="thread-summary"`,
		"2 replies",
		"Also sent to the channel",
		`class="message system-message"`,
		`data-subtype="channel_topic"`,
		"Copy link",
		"Mark unread from here",
		"Forward",
		`/archives/Cdev/p`,
	)
	// A system message carries no author chrome and no actions: it is not
	// something a person said, so there is nothing to reply to or react to.
	systemStart := strings.Index(body, `class="message system-message"`)
	if systemStart < 0 {
		t.Fatal("no system message rendered")
	}
	systemEnd := strings.Index(body[systemStart:], "</article>")
	systemBlock := body[systemStart : systemStart+systemEnd]
	requireMissing(t, "system message", systemBlock, "message-actions", "Add reaction", `class="avatar`)
	// The keyboard contract advertises the two new one-key actions.
	requireContains(t, "message shortcuts", body, "aria-keyshortcuts=", " F", " U")
}

// Forwarding shares a message into another conversation with its original
// attribution and a link back, and marking unread from a message moves only
// this member's cursor so everything from there on is unread again.
func TestForwardAndMarkUnreadFromAMessage(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	messages := service.Messages{Store: store}
	ctx := context.Background()
	if _, err := messages.CreateConversation(ctx, "T1", "U1", "elsewhere", false); err != nil {
		t.Fatal(err)
	}
	target, err := messages.Post(ctx, "T1", "U1", "Cdev", "worth forwarding", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := string(domain.NewMessageTimestamp(target.CreatedAt))
	csrf := auth.CSRFToken("session")

	conversations, err := messages.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var destination domain.ConversationID
	for _, conversation := range conversations.Conversations {
		if conversation.Name == "elsewhere" {
			destination = conversation.ID
		}
	}
	if destination == "" {
		t.Fatal("the destination channel was not created")
	}
	forwarded := postForm(t, mux, "/app/message/forward?channel=Cdev&ts="+url.QueryEscape(timestamp), url.Values{
		"_csrf": {csrf}, "destination": {string(destination)}, "comment": {"look at this"},
	}.Encode(), false)
	if forwarded.Code != http.StatusSeeOther {
		t.Fatalf("forward=%d: %s", forwarded.Code, forwarded.Body)
	}
	page := get(t, mux, "/app?channel="+string(destination))
	requireContains(t, "forwarded message", page.Body.String(), "look at this", "worth forwarding", "Forwarded from")

	// Mark unread from the message: the cursor moves to just before it.
	if _, err := messages.MarkRead(ctx, "T1", "U1", "Cdev", domain.NewMessageTimestamp(target.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	unread := postForm(t, mux, "/app/read/unread?channel=Cdev&ts="+url.QueryEscape(timestamp), url.Values{"_csrf": {csrf}}.Encode(), false)
	if unread.Code != http.StatusSeeOther {
		t.Fatalf("mark unread=%d: %s", unread.Code, unread.Body)
	}
	cursor, err := messages.ReadCursor(ctx, "T1", "U1", "Cdev")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastRead >= domain.NewMessageTimestamp(target.CreatedAt) {
		t.Fatalf("cursor=%s, want it to sit before the message that was marked unread", cursor.LastRead)
	}
	requireContains(t, "timeline after marking unread", get(t, mux, "/app?channel=Cdev").Body.String(), `class="unread-divider"`)
}

// FILE-06: an uploader can delete a hosted file, and the confirmation names
// the workspace-wide consequence rather than asking a bare "are you sure".
// The control appears only for a file this reader may actually delete — the
// service is uploader-only, so rendering it for anyone else would be a button
// that always fails.
func TestFileDeleteIsOfferedOnlyToItsUploader(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	messages := service.Messages{Store: store}
	file := domain.File{
		ID: "Fdelete", WorkspaceID: "T1", Uploader: "U1", Name: "report.txt", Title: "Report",
		MIMEType: "text/plain", BlobKey: "blob/report", Size: 12, CreatedAt: time.Now().UTC(),
	}
	event := events.Event{ID: "Efile", WorkspaceID: "T1", Topic: "file.created", Payload: `{"type":"file.created","event_ts":"1700000000.000000","file_id":"Fdelete"}`, CreatedAt: time.Now().UTC()}
	if err := store.CreateFile(ctx, file, event); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: "Mfile", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: "sharing a file", Attachments: "[]", CreatedAt: domain.MessageInstant(time.Now().UTC())}
	shareEvent := events.Event{ID: "Eshare", WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created","event_ts":"1700000000.000001","message_id":"Mfile"}`, CreatedAt: message.CreatedAt}
	if err := store.CreateFileShareMessage(ctx, []domain.FileID{"Fdelete"}, message, []events.Event{shareEvent}); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "own file", body, "Delete file", "removes it from every message and search result")

	deleted := postForm(t, mux, "/app/files/delete?channel=Cdev&file=Fdelete", url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), false)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete=%d: %s", deleted.Code, deleted.Body)
	}
	if _, _, err := messages.OpenFile(ctx, "T1", "U1", "Fdelete"); err == nil {
		t.Fatal("the file is still readable after deletion")
	}
	after := get(t, mux, "/app?channel=Cdev").Body.String()
	requireMissing(t, "deleted file", after, "Delete file")
}

// Deleting a message that shares a file retracts the file's share into that
// conversation, so the control has to say so before it is used. A confirmation
// that names only the message understates what the button does.
func TestDeletingASharingMessageWarnsThatTheFileLeavesTheChannel(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	file := domain.File{
		ID: "Fshared", WorkspaceID: "T1", Uploader: "U1", Name: "plan.txt", Title: "Plan",
		MIMEType: "text/plain", BlobKey: "blob/plan", Size: 4, CreatedAt: time.Now().UTC(),
	}
	if err := store.CreateFile(ctx, file, events.Event{ID: "Efshared", WorkspaceID: "T1", Topic: "file.created", Payload: `{"type":"file.created","event_ts":"1700000000.000000","file_id":"Fshared"}`, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: "Mshared", WorkspaceID: "T1", Conversation: "Cdev", AuthorID: "U1", Text: "the plan", Attachments: "[]", CreatedAt: domain.MessageInstant(time.Now().UTC())}
	if err := store.CreateFileShareMessage(ctx, []domain.FileID{"Fshared"}, message,
		[]events.Event{{ID: "Emshared", WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created","event_ts":"1700000000.000001","message_id":"Mshared"}`, CreatedAt: message.CreatedAt}}); err != nil {
		t.Fatal(err)
	}
	requireContains(t, "sharing message", get(t, mux, "/app?channel=Cdev").Body.String(),
		"This message shares a file", "also removes the file from this conversation")

	timestamp := string(domain.NewMessageTimestamp(message.CreatedAt))
	deleted := postForm(t, mux, "/app/message/delete?channel=Cdev&ts="+url.QueryEscape(timestamp), url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), false)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete=%d: %s", deleted.Code, deleted.Body)
	}
	stored, err := store.GetFile(ctx, "Fshared")
	if err != nil {
		t.Fatalf("the file itself was destroyed by deleting a message that shared it: %v", err)
	}
	if len(stored.SharedChannels) != 0 {
		t.Fatalf("the file is still shared into %v with no message sharing it", stored.SharedChannels)
	}
}

// FILE-07: remote files are visible in the client, and the surface never
// claims to host bytes it does not have — it links out and says so.
func TestRemoteFilesAreVisibleAndNeverClaimToBeHosted(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	messages := service.Messages{Store: store}
	if _, err := messages.AddRemoteFile(ctx, "T1", "U1", domain.RemoteFile{
		ExternalID: "ext-1", Title: "Quarterly plan", FileType: "gdoc",
		ExternalURL: "https://files.example.test/quarterly", IndexableContents: "revenue and headcount",
	}); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app/remote-files?channel=Cdev").Body.String()
	requireContains(t, "remote files", body,
		"Quarterly plan", "gdoc", "ext-1",
		"https://files.example.test/quarterly",
		"Open where it is hosted", "Hosted elsewhere",
		"The contents stay with the app that hosts them",
	)
	// It must not offer a download: this deployment does not have the bytes.
	requireMissing(t, "remote files", body, "/app/files/", "download")

	shared := postForm(t, mux, "/app/remote-files/share?file=ext-1&channel=Cdev", url.Values{
		"_csrf": {auth.CSRFToken("session")}, "destination": {"Cdev"},
	}.Encode(), false)
	if shared.Code != http.StatusSeeOther {
		t.Fatalf("share=%d: %s", shared.Code, shared.Body)
	}
	requireContains(t, "shared remote file", get(t, mux, "/app/remote-files?channel=Cdev").Body.String(), "Shared in", "#general")
}

// HUDDLE-01 and HUDDLE-03: the huddle bar offers a real lifecycle and says, in
// the same breath, that this deployment carries no audio. A control named
// "Join huddle" that silently connected nothing would be exactly the promise
// the universal contract forbids.
func TestTheHuddleBarRunsTheLifecycleAndNeverPromisesAudio(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "second", RealName: "Second Person"})
	store.SeedConversationMember("Cdev", "U2")

	idle := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "idle huddle bar", idle, "Start a huddle", "carries no voice or video")
	requireMissing(t, "idle huddle bar", idle, "Leave huddle", "End for everyone")

	started := postForm(t, mux, "/app/huddle/start?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), false)
	if started.Code != http.StatusSeeOther {
		t.Fatalf("start=%d: %s", started.Code, started.Body)
	}
	active := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "active huddle bar", active,
		"Huddle in", "Leave huddle", "End for everyone",
		"No audio here", "Joining puts your name in the huddle and nothing else")
	requireMissing(t, "active huddle bar", active, "Start a huddle")

	// A second person joins through the service; the bar is a live fragment,
	// so the participant list follows without a page load.
	messages := service.Messages{Store: store}
	if _, err := messages.JoinHuddle(ctx, "T1", "U2", "Cdev"); err != nil {
		t.Fatal(err)
	}
	fragment := get(t, mux, "/app/huddle?channel=Cdev").Body.String()
	requireContains(t, "huddle fragment", fragment, "Second Person")
	requireMissing(t, "huddle fragment", fragment, "<html", "<body")

	left := postForm(t, mux, "/app/huddle/leave?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), false)
	if left.Code != http.StatusSeeOther {
		t.Fatalf("leave=%d: %s", left.Code, left.Body)
	}
	// One participant remains, so the huddle is still running and this reader
	// is offered the way back in rather than a way to start a second one.
	afterLeaving := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "after leaving", afterLeaving, "Join huddle")
	requireMissing(t, "after leaving", afterLeaving, "Start a huddle")

	if _, err := messages.LeaveHuddle(ctx, "T1", "U2", "Cdev"); err != nil {
		t.Fatal(err)
	}
	// The last person left, so the huddle ended with them: a conversation must
	// never show a huddle nobody can be in.
	if _, err := store.ActiveHuddle(ctx, "T1", "Cdev"); err == nil {
		t.Fatal("the huddle outlived its last participant")
	}
	requireContains(t, "after the last person left", get(t, mux, "/app?channel=Cdev").Body.String(), "Start a huddle")
}

// HUDDLE-01: two people pressing start at the same moment must end up in one
// huddle. A read-then-create would give them one each, and the second would be
// the one nobody else joined.
func TestConcurrentHuddleStartsConvergeOnOne(t *testing.T) {
	store, _ := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	messages := service.Messages{Store: store}
	starters := []domain.UserID{"U1"}
	for index := 2; index <= 6; index++ {
		id := domain.UserID("U" + strconv.Itoa(index))
		store.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: "member" + strconv.Itoa(index)})
		store.SeedConversationMember("Cdev", id)
		starters = append(starters, id)
	}
	results := make(chan domain.CallID, len(starters))
	var wait sync.WaitGroup
	for _, starter := range starters {
		wait.Add(1)
		go func(actor domain.UserID) {
			defer wait.Done()
			call, err := messages.StartHuddle(ctx, "T1", actor, "Cdev", "")
			if err != nil {
				results <- ""
				return
			}
			results <- call.ID
		}(starter)
	}
	wait.Wait()
	close(results)
	seen := map[domain.CallID]struct{}{}
	for id := range results {
		if id == "" {
			t.Fatal("a concurrent start failed")
		}
		seen[id] = struct{}{}
	}
	if len(seen) != 1 {
		t.Fatalf("%d huddles were started concurrently in one conversation, want 1", len(seen))
	}
	call, err := store.ActiveHuddle(ctx, "T1", "Cdev")
	if err != nil {
		t.Fatal(err)
	}
	if len(call.Participants) != len(starters) {
		t.Fatalf("participants=%v, want everyone who pressed start", call.Participants)
	}
}

// Ending a huddle removes everyone else from it, so a participant who did not
// start it is offered a way out rather than a way to end it for the others.
func TestOnlyTheStarterIsOfferedTheEndControl(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "second"})
	store.SeedConversationMember("Cdev", "U2")
	messages := service.Messages{Store: store}
	if _, err := messages.StartHuddle(ctx, "T1", "U2", "Cdev", ""); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "someone else's huddle", body, "Join huddle")
	requireMissing(t, "someone else's huddle", body, "End for everyone")

	refused := postForm(t, mux, "/app/huddle/end?channel=Cdev", url.Values{"_csrf": {auth.CSRFToken("session")}}.Encode(), false)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("end=%d: %s", refused.Code, refused.Body)
	}
	if _, err := store.ActiveHuddle(ctx, "T1", "Cdev"); err != nil {
		t.Fatalf("a refused end still ended the huddle: %v", err)
	}
}

// NOTIFY-04: a desktop notification needs three separate permissions and this
// product owns only two of them. The page carries its two so the client can
// decide, and says which is missing rather than reporting one silent "off".
func TestBrowserNotificationsCarryBothHalvesTheServerOwns(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	messages := service.Messages{Store: store}

	off := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "notifications off", off, `data-browser-notifications="false"`, `data-notifications-paused="false"`)

	if _, err := messages.SetWorkspaceNotificationPreferences(ctx, "T1", "U1", domain.NotificationMentions, nil, true, true, true); err != nil {
		t.Fatal(err)
	}
	on := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "notifications on", on, `data-browser-notifications="true"`)

	// Do Not Disturb was fetched for the preferences page and consulted
	// nowhere, so a paused workspace still raised every banner it could.
	if _, err := messages.SetSnooze(ctx, "T1", "U1", 60); err != nil {
		t.Fatal(err)
	}
	paused := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "notifications paused", paused, `data-notifications-paused="true"`)
}

// The preferences page names what this deployment does not deliver, rather
// than leaving a person to wonder why a phone never buzzes.
func TestTheNotificationsPageNamesWhatItCannotDeliver(t *testing.T) {
	store, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	if _, err := (service.Messages{Store: store}).SetWorkspaceNotificationPreferences(ctx, "T1", "U1", domain.NotificationMentions, nil, true, true, true); err != nil {
		t.Fatal(err)
	}
	body := get(t, mux, "/app/notifications?channel=Cdev").Body.String()
	requireContains(t, "notification preferences", body,
		"Show desktop notifications", `name="browser_notifications"`, "checked",
		"Not delivered here", "There is no mobile application", "sends no mail at all",
		"Your browser also has to allow notifications",
	)
}

// The Canvases tab is the newest search surface, and this is where its wiring is
// checked end to end through the handler: the tab exists, a canvas is found by
// its prose, and the stored JSON envelope is not part of the index.
func TestCanvasSearchTabFindsProseAndNotStoredSyntax(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	canvas, err := service.Messages{Store: s}.CreateCanvas(context.Background(), "T1", "U1", "Deployment runbook", `{"type":"markdown","markdown":"roll back the previous revision"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	byTitle := get(t, mux, "/app/search?q=runbook&type=canvases&channel=Cdev")
	if byTitle.Code != http.StatusOK {
		t.Fatalf("canvas search status=%d body=%s", byTitle.Code, byTitle.Body)
	}
	requireContains(t, "canvas search by title", byTitle.Body.String(), "Canvases", "Deployment <mark>runbook</mark>", string(canvas.ID))

	byBody := get(t, mux, "/app/search?q=roll+back&type=canvases&channel=Cdev")
	requireContains(t, "canvas search by body", byBody.Body.String(), "Deployment runbook", "<mark>roll</mark> <mark>back</mark>")

	syntax := get(t, mux, "/app/search?q=sections&type=canvases&channel=Cdev")
	requireContains(t, "canvas search for stored syntax", syntax.Body.String(), "No matching canvases.")
}
