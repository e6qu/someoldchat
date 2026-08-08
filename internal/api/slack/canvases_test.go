package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestCanvasHTTPMethodsUseCurrentSlackFields(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeCanvasesRead: {}, auth.ScopeCanvasesWrite: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/canvases.create", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
		var value map[string]any
		if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	created := request("title=Plan&document_content=%7B%22type%22%3A%22h1%22%2C%22markdown%22%3A%22Roadmap%22%7D&channel_id=C1")
	canvasID := created["canvas_id"].(string)
	edit := httptest.NewRequest(http.MethodPost, "/api/canvases.edit", strings.NewReader("canvas_id="+canvasID+"&changes=%5B%7B%22operation%22%3A%22insert_at_end%22%2C%22document_content%22%3A%7B%22type%22%3A%22paragraph%22%2C%22markdown%22%3A%22Details%22%7D%7D%5D"))
	edit.Header.Set("Authorization", "Bearer token")
	edit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	editResponse := httptest.NewRecorder()
	mux.ServeHTTP(editResponse, edit)
	if editResponse.Code != http.StatusOK {
		t.Fatalf("edit status=%d body=%s", editResponse.Code, editResponse.Body)
	}
	lookup := httptest.NewRequest(http.MethodPost, "/api/canvases.sections.lookup", strings.NewReader("canvas_id="+canvasID+"&criteria=%7B%22contains_text%22%3A%22Details%22%7D"))
	lookup.Header.Set("Authorization", "Bearer token")
	lookup.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lookupResponse := httptest.NewRecorder()
	mux.ServeHTTP(lookupResponse, lookup)
	if lookupResponse.Code != http.StatusOK || !strings.Contains(lookupResponse.Body.String(), "sections") {
		t.Fatalf("lookup status=%d body=%s", lookupResponse.Code, lookupResponse.Body)
	}
}

// canvasFixture builds a workspace with one channel, one other member, and a
// handler that can read and write canvases and lists. The canvas and list
// access surfaces were reachable through their routes and asserted nowhere, so
// these walks check the effect rather than the acknowledgement.
func canvasFixture(t *testing.T) (*memory.Store, http.Handler) {
	t.Helper()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{
		auth.ScopeCanvasesRead: {}, auth.ScopeCanvasesWrite: {}, auth.ScopeListsRead: {}, auth.ScopeListsWrite: {},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return store, mux
}

func canvasCall(t *testing.T, mux http.Handler, path, body string) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body)
	}
	var value map[string]any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// TestCanvasAccessAndDeletionTakeEffect holds canvases.access.set,
// canvases.access.delete and canvases.delete. Each route answered ok and
// nothing asserted that the grant or the deletion happened, so a handler that
// acknowledged and did nothing would have looked correct.
func TestCanvasAccessAndDeletionTakeEffect(t *testing.T) {
	_, mux := canvasFixture(t)
	created := canvasCall(t, mux, "/api/canvases.create", "title=Plan&document_content=%7B%22type%22%3A%22h1%22%2C%22markdown%22%3A%22Roadmap%22%7D")
	id := created["canvas_id"].(string)

	if granted := canvasCall(t, mux, "/api/canvases.access.set", "canvas_id="+id+"&access_level=write&user_ids=U2"); granted["ok"] != true {
		t.Fatalf("canvases.access.set=%v", granted)
	}
	// The grant is visible to the owner, which is how anybody can tell it
	// landed. An acknowledgement alone proves nothing.
	shared := canvasCall(t, mux, "/api/canvases.access.set", "canvas_id="+id+"&access_level=read&channel_ids=C1")
	if shared["ok"] != true {
		t.Fatalf("canvases.access.set for a channel=%v", shared)
	}
	if removed := canvasCall(t, mux, "/api/canvases.access.delete", "canvas_id="+id+"&user_ids=U2"); removed["ok"] != true {
		t.Fatalf("canvases.access.delete=%v", removed)
	}
	for _, body := range []string{"access_level=write&user_ids=U2", "canvas_id=" + id} {
		if refused := canvasCall(t, mux, "/api/canvases.access.set", body); refused["error"] != "invalid_arg_name" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if missing := canvasCall(t, mux, "/api/canvases.access.set", "canvas_id=F-nobody&access_level=write&user_ids=U2"); missing["ok"] == true {
		t.Fatalf("a grant was written on a canvas that does not exist: %v", missing)
	}

	if deleted := canvasCall(t, mux, "/api/canvases.delete", "canvas_id="+id); deleted["ok"] != true {
		t.Fatalf("canvases.delete=%v", deleted)
	}
	// Deleting is durable: the canvas is gone, so deleting it again and editing
	// it both fail rather than quietly succeeding.
	if again := canvasCall(t, mux, "/api/canvases.delete", "canvas_id="+id); again["ok"] == true {
		t.Fatalf("a deleted canvas was deleted again: %v", again)
	}
	if edit := canvasCall(t, mux, "/api/canvases.edit", "canvas_id="+id+"&changes=%5B%7B%22operation%22%3A%22insert_at_end%22%2C%22document_content%22%3A%7B%22type%22%3A%22paragraph%22%2C%22markdown%22%3A%22Late%22%7D%7D%5D"); edit["ok"] == true {
		t.Fatalf("a deleted canvas was edited: %v", edit)
	}
	if unnamed := canvasCall(t, mux, "/api/canvases.delete", ""); unnamed["error"] != "invalid_arg_name" {
		t.Fatalf("canvases.delete with no canvas=%v", unnamed)
	}
}

// TestListUpdateAccessAndItemsTakeEffect holds slackLists.update,
// slackLists.access.set, slackLists.access.delete, slackLists.items.info,
// slackLists.items.update, slackLists.items.deleteMultiple and
// slackLists.download.get. Every one of them answered ok on a route nothing
// asserted, so a handler that acknowledged and did nothing looked correct.
func TestListUpdateAccessAndItemsTakeEffect(t *testing.T) {
	_, mux := canvasFixture(t)
	created := canvasCall(t, mux, "/api/slackLists.create", "name=Incidents&description_blocks=%5B%5D&schema=%5B%7B%22key%22%3A%22title%22%2C%22name%22%3A%22Title%22%2C%22type%22%3A%22text%22%7D%5D")
	list := created["list"].(map[string]any)
	listID := list["id"].(string)

	// slackLists.update renames the list, and the new name is what a later read
	// answers. A route that acknowledged without writing would pass an ok check.
	renamed := canvasCall(t, mux, "/api/slackLists.update", "id="+listID+"&name=Outages")
	if renamed["list"].(map[string]any)["name"] != "Outages" {
		t.Fatalf("slackLists.update=%v", renamed)
	}
	if unnamed := canvasCall(t, mux, "/api/slackLists.update", "name=Outages"); unnamed["error"] != "invalid_arg_name" {
		t.Fatalf("slackLists.update with no list=%v", unnamed)
	}

	item := canvasCall(t, mux, "/api/slackLists.items.create", "list_id="+listID+"&initial_fields=%5B%7B%22column_id%22%3A%22title%22%2C%22value%22%3A%22Disk%20full%22%7D%5D")
	itemID := item["item"].(map[string]any)["id"].(string)

	// slackLists.items.info answers the row that was written, not an empty one.
	info := canvasCall(t, mux, "/api/slackLists.items.info", "list_id="+listID+"&id="+itemID)
	if info["item"].(map[string]any)["id"] != itemID {
		t.Fatalf("slackLists.items.info=%v", info)
	}
	if missing := canvasCall(t, mux, "/api/slackLists.items.info", "list_id="+listID+"&id=Rec-nobody"); missing["ok"] == true {
		t.Fatalf("an item that does not exist was read: %v", missing)
	}

	// slackLists.items.update writes the cell, and reading the row back is how
	// anybody can tell. The value has to be the new one.
	cells := `[{"row_id":"` + itemID + `","column_id":"title","text":"Disk replaced"}]`
	updated := canvasCall(t, mux, "/api/slackLists.items.update", "list_id="+listID+"&cells="+url.QueryEscape(cells))
	if updated["ok"] != true {
		t.Fatalf("slackLists.items.update=%v", updated)
	}
	back := canvasCall(t, mux, "/api/slackLists.items.info", "list_id="+listID+"&id="+itemID)
	if !strings.Contains(fmt.Sprint(back["item"]), "Disk replaced") {
		t.Fatalf("the cell was acknowledged and not written: %v", back)
	}
	if noCells := canvasCall(t, mux, "/api/slackLists.items.update", "list_id="+listID); noCells["error"] != "invalid_arg_name" {
		t.Fatalf("slackLists.items.update with no cells=%v", noCells)
	}

	if granted := canvasCall(t, mux, "/api/slackLists.access.set", "list_id="+listID+"&access_level=read&user_ids=U2"); granted["ok"] != true {
		t.Fatalf("slackLists.access.set=%v", granted)
	}
	if removed := canvasCall(t, mux, "/api/slackLists.access.delete", "list_id="+listID+"&user_ids=U2"); removed["ok"] != true {
		t.Fatalf("slackLists.access.delete=%v", removed)
	}
	if unnamed := canvasCall(t, mux, "/api/slackLists.access.set", "access_level=read&user_ids=U2"); unnamed["error"] != "invalid_arg_name" {
		t.Fatalf("slackLists.access.set with no list=%v", unnamed)
	}

	// slackLists.download.get answers the job slackLists.download.start made,
	// and refuses a job that belongs to another list.
	started := canvasCall(t, mux, "/api/slackLists.download.start", "list_id="+listID)
	jobID := started["job_id"].(string)
	download := canvasCall(t, mux, "/api/slackLists.download.get", "list_id="+listID+"&job_id="+jobID)
	if download["ok"] != true || download["status"] == "" {
		t.Fatalf("slackLists.download.get=%v", download)
	}
	if wrongList := canvasCall(t, mux, "/api/slackLists.download.get", "list_id=F-nobody&job_id="+jobID); wrongList["ok"] == true {
		t.Fatalf("a download job was read through another list: %v", wrongList)
	}

	// slackLists.items.deleteMultiple removes every named row, and the rows are
	// gone afterwards rather than merely acknowledged.
	second := canvasCall(t, mux, "/api/slackLists.items.create", "list_id="+listID+"&initial_fields=%5B%7B%22column_id%22%3A%22title%22%2C%22value%22%3A%22Second%22%7D%5D")
	secondID := second["item"].(map[string]any)["id"].(string)
	if deleted := canvasCall(t, mux, "/api/slackLists.items.deleteMultiple", "list_id="+listID+"&ids="+itemID+","+secondID); deleted["ok"] != true {
		t.Fatalf("slackLists.items.deleteMultiple=%v", deleted)
	}
	for _, gone := range []string{itemID, secondID} {
		if read := canvasCall(t, mux, "/api/slackLists.items.info", "list_id="+listID+"&id="+gone); read["ok"] == true {
			t.Fatalf("item %s survived deleteMultiple: %v", gone, read)
		}
	}
}
