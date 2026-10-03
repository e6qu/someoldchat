package web

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// An app's remote file found by a search opens at the app: it names no
// uploader, size or type here, and offers nothing to download.
func TestSearchShowsAnAppsRemoteFile(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	ctx := context.Background()
	messages := service.Messages{Store: s}
	if _, err := messages.AddRemoteFile(ctx, "T1", "U1", domain.RemoteFile{
		ExternalID: "plan-1", Title: "Roadmap", FileType: "gdoc", ExternalURL: "https://docs.example/plan", IndexableContents: "quarterly hiring",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ShareRemoteFile(ctx, "T1", "U1", domain.RemoteFileLookup{ExternalID: "plan-1"}, []domain.ConversationID{"Cdev"}); err != nil {
		t.Fatal(err)
	}
	page := get(t, mux, "/app/search?q=quarterly&type=files&channel=Cdev").Body.String()
	requireContains(t, "remote file result", page, `<a href="https://docs.example/plan">Roadmap</a>`, "<span>Remote file</span>")
	requireMissing(t, "remote file result", page, "Download Roadmap", "Shared by")
}
