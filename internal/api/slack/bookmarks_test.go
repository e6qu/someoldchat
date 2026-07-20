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

func TestBookmarkLifecycleUsesChannelIDAndDurableResponse(t *testing.T) {
	handler, _ := testHandlerWithStore()
	request := func(method, path, body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", method, path, response.Code, response.Body)
		}
		var value map[string]any
		if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		if value["ok"] != true {
			t.Fatalf("%s %s body=%v", method, path, value)
		}
		return value
	}
	added := request(http.MethodPost, "/api/bookmarks.add", "channel_id=C1&title=Docs&type=link&link=https%3A%2F%2Fdocs.example%2F&emoji=%3Abooks%3A")
	bookmark := added["bookmark"].(map[string]any)
	id := bookmark["id"].(string)
	if bookmark["channel_id"] != "C1" || bookmark["link"] != "https://docs.example/" {
		t.Fatalf("bookmark=%v", bookmark)
	}
	listed := request(http.MethodPost, "/api/bookmarks.list", "channel_id=C1")
	items := listed["bookmarks"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Fatalf("listed=%v", listed)
	}
	edited := request(http.MethodPost, "/api/bookmarks.edit", "channel_id=C1&bookmark_id="+id+"&title=Updated")
	if edited["bookmark"].(map[string]any)["title"] != "Updated" {
		t.Fatalf("edited=%v", edited)
	}
	request(http.MethodPost, "/api/bookmarks.remove", "channel_id=C1&bookmark_id="+id)
	if items := request(http.MethodGet, "/api/bookmarks.list?channel_id=C1", "")["bookmarks"].([]any); len(items) != 0 {
		t.Fatalf("bookmarks after removal=%v", items)
	}
}

func TestBookmarkRequiresWriteScope(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeBookmarksRead: {}}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := NewHandler(service.Messages{Store: store}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.NewServeMux()
	value.Register(handler)
	req := httptest.NewRequest(http.MethodPost, "/api/bookmarks.add", strings.NewReader("channel_id=C1&title=Docs&type=link&link=https%3A%2F%2Fdocs.example%2F"))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}
