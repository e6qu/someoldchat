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
