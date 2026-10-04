package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// An uploaded custom emoji is listed with an absolute URL, as Slack's are, and
// that URL serves the image to a client holding no credential. Once the emoji
// is removed the URL stops answering.
func TestAnUploadedCustomEmojiIsListedAbsoluteAndServedPublicly(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: s, Blob: blobs}
	image := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 40)...)
	if err := messages.AdminUploadEmoji(ctx, "T1", "U1", "shipit-cat", "image/png", image); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeEmojiRead: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)

	list := httptest.NewRequest(http.MethodGet, "http://chat.example/api/emoji.list", nil)
	list.Header.Set("Authorization", "Bearer token")
	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, list)
	var response struct {
		OK    bool              `json:"ok"`
		Emoji map[string]string `json:"emoji"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil || !response.OK {
		t.Fatalf("emoji.list=%s err=%v", listed.Body, err)
	}
	imageURL := response.Emoji["shipit-cat"]
	if !strings.HasPrefix(imageURL, "http://chat.example/emoji/T1/") {
		t.Fatalf("an uploaded emoji was listed as %q, not an absolute URL on this origin", imageURL)
	}

	fetch := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, imageURL, nil)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	served := fetch()
	body, _ := io.ReadAll(served.Body)
	if served.Code != http.StatusOK || served.Header().Get("Content-Type") != "image/png" || !bytes.Equal(body, image) {
		t.Fatalf("image status=%d type=%q bytes=%d", served.Code, served.Header().Get("Content-Type"), len(body))
	}
	if err := messages.AdminRemoveEmoji(ctx, "T1", "U1", "shipit-cat"); err != nil {
		t.Fatal(err)
	}
	if gone := fetch(); gone.Code != http.StatusNotFound {
		t.Fatalf("a removed emoji's image answered %d", gone.Code)
	}
}
