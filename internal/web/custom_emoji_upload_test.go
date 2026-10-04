package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"regexp"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// An administrator adds a custom emoji by uploading its image, as in Slack's
// "Add custom emoji" dialog; the list then shows it from the image URL this
// server minted, and an image over Slack's 128 KB limit is refused with the
// rule rather than an outage.
func TestCustomEmojiCanBeAddedByUploadingAnImage(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	upload := func(name string, image []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for field, value := range map[string]string{"_csrf": auth.CSRFToken("session"), "name": name} {
			if err := form.WriteField(field, value); err != nil {
				t.Fatal(err)
			}
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="image"; filename="emoji.gif"`)
		header.Set("Content-Type", "image/gif")
		part, err := form.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		part.Write(image)
		form.Close()
		request := httptest.NewRequest(http.MethodPost, "/app/customize/emoji/add", &body)
		request.Header.Set("Content-Type", form.FormDataContentType())
		addBrowserCookies(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	image := append([]byte("GIF89a"), bytes.Repeat([]byte{1}, 32)...)
	if response := upload("dancing-cat", image); response.Code != http.StatusSeeOther {
		t.Fatalf("upload answered %d: %s", response.Code, response.Body)
	}
	page := get(t, mux, "/app/customize/emoji").Body.String()
	if !regexp.MustCompile(`<img src="/emoji/T1/emoji_[A-Za-z0-9_-]+" alt=":dancing-cat:"`).MatchString(page) {
		t.Fatalf("the uploaded emoji is not listed from its image URL:\n%s", page)
	}
	requireContains(t, "upload form", page, `enctype="multipart/form-data"`, `name="image" type="file" accept="image/png,image/gif,image/jpeg"`)

	tooLarge := upload("too-big", append(append([]byte(nil), image...), make([]byte, domain.MaxCustomEmojiBytes)...))
	if tooLarge.Code != http.StatusBadRequest || !strings.Contains(tooLarge.Body.String(), "128 KB or less") {
		t.Fatalf("an oversized image answered %d: %s", tooLarge.Code, tooLarge.Body)
	}
}
