package slack

import (
	"context"
	"encoding/json"
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

// storedTokenMux serves the Web API over a memory store whose seeded tokens
// authenticate, so one test can call as several principals.
func storedTokenMux(t *testing.T, repository *memory.Store, tokens map[string]domain.TokenRecord) *http.ServeMux {
	t.Helper()
	for token, record := range tokens {
		if err := repository.SeedToken(context.Background(), token, record); err != nil {
			t.Fatal(err)
		}
	}
	authenticator, err := auth.NewStored(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: repository}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux
}

func callWith(t *testing.T, mux *http.ServeMux, verb, method, token, contentType, body string) map[string]any {
	t.Helper()
	target := "/api/" + method
	var reader *strings.Reader
	if verb == http.MethodGet {
		target += "?" + body
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(verb, target, reader)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

const formEncoded = "application/x-www-form-urlencoded"

// TestCanvasesGetContentReturnsMarkdownAndHTML reads a canvas written with
// canvases.create back in both documented formats: markdown by default, in
// the form canvases.create accepts, and HTML on request, through a JSON body
// and a GET as well as a form. A canvas that does not exist and one the caller
// cannot view are both canvas_not_found.
func TestCanvasesGetContentReturnsMarkdownAndHTML(t *testing.T) {
	repository := memory.New()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}),
		repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	mux := storedTokenMux(t, repository, map[string]domain.TokenRecord{
		"xoxp-alice":  {WorkspaceID: "T1", UserID: "U1", TokenType: domain.TokenUser, Scopes: []string{"canvases:read", "canvases:write"}},
		"xoxp-bob":    {WorkspaceID: "T1", UserID: "U2", TokenType: domain.TokenUser, Scopes: []string{"canvases:read"}},
		"xoxp-writer": {WorkspaceID: "T1", UserID: "U1", TokenType: domain.TokenUser, Scopes: []string{"canvases:write"}},
	})
	markdown := "# Project plan\n\n- [ ] Draft spec\n- [x] Kickoff meeting\n"
	document, err := json.Marshal(map[string]string{"type": "markdown", "markdown": markdown})
	if err != nil {
		t.Fatal(err)
	}
	created := callWith(t, mux, http.MethodPost, "canvases.create", "xoxp-alice", formEncoded, url.Values{"title": {"Plan"}, "document_content": {string(document)}}.Encode())
	canvasID, _ := created["canvas_id"].(string)
	if canvasID == "" {
		t.Fatalf("create=%v", created)
	}

	read := callWith(t, mux, http.MethodPost, "canvases.getContent", "xoxp-alice", formEncoded, url.Values{"canvas_id": {canvasID}}.Encode())
	if read["ok"] != true || read["content"] != markdown || len(read) != 2 {
		t.Fatalf("default read=%v, want the reference's markdown %q", read, markdown)
	}
	explicit := callWith(t, mux, http.MethodGet, "canvases.getContent", "xoxp-alice", "", url.Values{"canvas_id": {canvasID}, "content_type": {"markdown"}}.Encode())
	if explicit["content"] != markdown {
		t.Fatalf("GET markdown read=%v", explicit)
	}
	body, err := json.Marshal(map[string]string{"canvas_id": canvasID, "content_type": "html"})
	if err != nil {
		t.Fatal(err)
	}
	html := callWith(t, mux, http.MethodPost, "canvases.getContent", "xoxp-alice", "application/json", string(body))
	content, _ := html["content"].(string)
	if html["ok"] != true || !strings.HasPrefix(content, "<h1>Project plan</h1>") || !strings.Contains(content, `<ul class="checklist">`) ||
		!strings.Contains(content, `<input type="checkbox" checked disabled> Kickoff meeting`) {
		t.Fatalf("html read=%v", html)
	}
	// What the canvas says after an edit is what the next read says.
	changes := `[{"operation":"insert_at_end","document_content":{"type":"markdown","markdown":"## Notes\n\nShip it."}}]`
	if edited := callWith(t, mux, http.MethodPost, "canvases.edit", "xoxp-alice", formEncoded, url.Values{"canvas_id": {canvasID}, "changes": {changes}}.Encode()); edited["ok"] != true {
		t.Fatalf("edit=%v", edited)
	}
	if again := callWith(t, mux, http.MethodPost, "canvases.getContent", "xoxp-alice", formEncoded, url.Values{"canvas_id": {canvasID}}.Encode()); again["content"] != markdown+"\n## Notes\n\nShip it.\n" {
		t.Fatalf("read after edit=%q", again["content"])
	}

	for name, call := range map[string]struct {
		token, body, want string
	}{
		"bob cannot view it":     {"xoxp-bob", url.Values{"canvas_id": {canvasID}}.Encode(), "canvas_not_found"},
		"unknown canvas":         {"xoxp-alice", url.Values{"canvas_id": {"F0NOTHERE"}}.Encode(), "canvas_not_found"},
		"no canvas_id":           {"xoxp-alice", "", "invalid_arguments"},
		"unlisted content_type":  {"xoxp-alice", url.Values{"canvas_id": {canvasID}, "content_type": {"pdf"}}.Encode(), "invalid_arguments"},
		"no canvases:read scope": {"xoxp-writer", url.Values{"canvas_id": {canvasID}}.Encode(), "missing_scope"},
		"unknown token":          {"xoxp-nobody", url.Values{"canvas_id": {canvasID}}.Encode(), "invalid_auth"},
	} {
		if got := callWith(t, mux, http.MethodPost, "canvases.getContent", call.token, formEncoded, call.body); got["ok"] != false || got["error"] != call.want {
			t.Errorf("%s: %v, want %s", name, got, call.want)
		}
	}
	// Shared with bob, the canvas is his to read.
	if shared := callWith(t, mux, http.MethodPost, "canvases.access.set", "xoxp-alice", formEncoded, url.Values{"canvas_id": {canvasID}, "access_level": {"read"}, "user_ids": {"U2"}}.Encode()); shared["ok"] != true {
		t.Fatalf("share=%v", shared)
	}
	if read := callWith(t, mux, http.MethodPost, "canvases.getContent", "xoxp-bob", formEncoded, url.Values{"canvas_id": {canvasID}}.Encode()); read["ok"] != true {
		t.Fatalf("bob's read after sharing=%v", read)
	}
}
