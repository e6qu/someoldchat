package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// The composer's + menu offers the member's own recent files, newest first,
// and choosing one shares it into the conversation — or into the thread a
// reply composer belongs to — as its own message. Someone else's file is
// neither offered nor shareable.
func TestRecentFilesAreSharedFromTheComposer(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	s.SeedConversationMember("Cdev", "U2")
	for index, file := range []domain.File{
		{ID: "Fold", Uploader: "U1", Name: "old.txt", Title: "Old notes", MIMEType: "text/plain"},
		{ID: "Fnew", Uploader: "U1", Name: "new.pdf", Title: "New plan", MIMEType: "application/pdf"},
		{ID: "Fbob", Uploader: "U2", Name: "bob.txt", Title: "Bob's file", MIMEType: "text/plain", SharedChannels: []domain.ConversationID{"Cdev"}},
	} {
		file.WorkspaceID, file.BlobKey = "T1", string(file.ID)
		file.CreatedAt = time.Unix(1_700_000_000+int64(index)*60, 0).UTC()
		if err := s.CreateFile(context.Background(), file, events.Event{ID: domain.EventID("E" + string(file.ID)), WorkspaceID: "T1", Topic: "file.created", CreatedAt: file.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	page := get(t, mux, "/app?channel=Cdev").Body.String()
	requireContains(t, "the + menu", page, `>Recent files</p>`,
		`form="share-recent-file" name="file" value="Fnew" aria-label="Share New plan"`,
		`form="share-recent-file" name="file" value="Fold" aria-label="Share Old notes"`,
		`<form id="share-recent-file" method="post" action="/app/file/share?channel=Cdev" hidden>`)
	requireMissing(t, "someone else's file", page, `value="Fbob"`)
	if first, second := indexOf(page, `value="Fnew"`), indexOf(page, `value="Fold"`); first < 0 || second < first {
		t.Fatalf("recent files are not newest first: Fnew at %d, Fold at %d", first, second)
	}

	csrf := auth.CSRFToken("session")
	shared := postForm(t, mux, "/app/file/share?channel=Cdev", url.Values{"_csrf": {csrf}, "file": {"Fnew"}}.Encode(), false)
	if shared.Code != http.StatusSeeOther {
		t.Fatalf("share=%d: %s", shared.Code, shared.Body)
	}
	history, err := s.ListMessages(context.Background(), "Cdev", domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}, RootsOnly: true})
	if err != nil || len(history.Messages) != 1 || len(history.Messages[0].Files) != 1 || history.Messages[0].Files[0].ID != "Fnew" || history.Messages[0].AuthorID != "U1" {
		t.Fatalf("history=%+v err=%v, want one message sharing Fnew", history.Messages, err)
	}

	root := domain.NewMessageTimestamp(history.Messages[0].CreatedAt)
	threaded := postForm(t, mux, "/app/file/share?"+url.Values{"channel": {"Cdev"}, "thread": {string(root)}}.Encode(), url.Values{"_csrf": {csrf}, "file": {"Fold"}}.Encode(), false)
	if threaded.Code != http.StatusSeeOther {
		t.Fatalf("share into a thread=%d: %s", threaded.Code, threaded.Body)
	}
	replies, err := s.ListThreadMessages(context.Background(), "Cdev", root, domain.ThreadRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(replies.Messages) < 2 || replies.Messages[len(replies.Messages)-1].Files[0].ID != "Fold" {
		t.Fatalf("thread=%+v err=%v, want the reply sharing Fold", replies.Messages, err)
	}

	if refused := postForm(t, mux, "/app/file/share?channel=Cdev", url.Values{"_csrf": {csrf}, "file": {"Fbob"}}.Encode(), false); refused.Code != http.StatusNotFound {
		t.Fatalf("sharing someone else's file=%d", refused.Code)
	}
}

