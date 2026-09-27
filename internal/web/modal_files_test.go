package web

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// postModalFiles submits a modal form as a browser does when it holds a
// file_input: multipart, with each file under its input's upload field.
func postModalFiles(t *testing.T, mux *http.ServeMux, target string, fields map[string]string, files map[string][]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for field, names := range files {
		for _, name := range names {
			part, err := writer.CreateFormFile(field, name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("contents of " + name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

const fileInputModal = `{"type":"modal","title":{"type":"plain_text","text":"Evidence"},"submit":{"type":"plain_text","text":"Send"},"blocks":[` +
	`{"type":"input","block_id":"fi","label":{"type":"plain_text","text":"Evidence"},"element":{"type":"file_input","action_id":"fia","filetypes":["txt","pdf"],"max_files":2}},` +
	`{"type":"input","block_id":"why","label":{"type":"plain_text","text":"Why"},"element":{"type":"plain_text_input","action_id":"whya","min_length":3}}` +
	`]}`

// A member attaches files in a modal's file_input: they are stored private
// to the member, view_submission carries them as Slack file objects, and the
// app's bot may read them while nobody else can.
func TestModalFileInputUploadsFilesTheAppCanRead(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	seedSocketModeModalApp(t, s, "")
	s.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1", Name: "modal-bot"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bystander"})
	if err := s.CreateBot(context.Background(), domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "modal-bot", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	seedOpenModal(t, s, "Vf", fileInputModal)
	page := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "file input", page, `enctype="multipart/form-data"`, `type="file" name="input_0_upload" multiple accept=".txt,.pdf" required`, "Up to 2 files (txt, pdf).")

	fields := map[string]string{"_csrf": auth.CSRFToken("session"), "view_id": "Vf", "input_1": "because"}
	refused := postModalFiles(t, mux, "/app/view/submit?channel=Cdev", fields, map[string][]string{"input_0_upload": {"payload.exe"}})
	if refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong type status=%d", refused.Code)
	}
	requireContains(t, "wrong type", refused.Body.String(), "Attach only TXT, PDF files.")
	tooMany := postModalFiles(t, mux, "/app/view/submit?channel=Cdev", fields, map[string][]string{"input_0_upload": {"a.txt", "b.txt", "c.txt"}})
	if tooMany.Code != http.StatusUnprocessableEntity {
		t.Fatalf("too many status=%d", tooMany.Code)
	}
	requireContains(t, "too many", tooMany.Body.String(), "Attach no more than 2 files.")

	// A form returned for another field keeps the stored file attached.
	short := map[string]string{"_csrf": auth.CSRFToken("session"), "view_id": "Vf", "input_1": "no"}
	kept := postModalFiles(t, mux, "/app/view/submit?channel=Cdev", short, map[string][]string{"input_0_upload": {"notes.txt"}})
	if kept.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short reason status=%d", kept.Code)
	}
	requireContains(t, "kept attachment", kept.Body.String(), "Enter at least 3 characters.", `type="checkbox" name="input_0" value="file:`, "notes.txt")
	if _, found, _ := s.ClaimSocketModeInteraction(context.Background(), "A1", "modal-client", time.Minute); found {
		t.Fatal("a refused submission reached the app")
	}

	submitted := postModalFiles(t, mux, "/app/view/submit?channel=Cdev", fields, map[string][]string{"input_0_upload": {"report.pdf"}})
	if submitted.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", submitted.Code, submitted.Body)
	}
	values := viewState(t, claimInteraction(t, s))
	entry := values["fi"].(map[string]any)["fia"].(map[string]any)
	files, _ := entry["files"].([]any)
	if entry["type"] != "file_input" || len(files) != 1 {
		t.Fatalf("file_input state = %v", entry)
	}
	file := files[0].(map[string]any)
	if file["name"] != "report.pdf" || file["user"] != "U1" || file["url_private"] == "" || file["channels"] != nil {
		t.Fatalf("submitted file object = %v", file)
	}
	messages := service.Messages{Store: s}
	id := domain.FileID(file["id"].(string))
	if _, err := messages.FileInfo(context.Background(), "T1", "UB", id); err != nil {
		t.Fatalf("the app's bot cannot read the submitted file: %v", err)
	}
	if _, err := messages.FileInfo(context.Background(), "T1", "U2", id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member reads the private file: err=%v", err)
	}
}
