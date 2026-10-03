package qualification

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// remoteFilesAreSearchable holds the storage behind remote files in
// search.files: an app's remote file is found by its title and indexable
// contents only once it is shared where the searcher can read, never for a
// search by uploader, and in the order and window every profile agrees on.
func remoteFilesAreSearchable(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	outsider := domain.UserID("U-remote-outsider-" + f.suffix)
	private := domain.ConversationID("G-remote-" + f.suffix)
	if err := f.repository.SeedUser(ctx, domain.User{ID: outsider, WorkspaceID: f.workspaceID, Name: "outsider"}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: private, WorkspaceID: f.workspaceID, Name: "remote-private-" + f.suffix, Kind: domain.ConversationTypePrivate}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SeedConversationMember(ctx, private, f.userID); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	add := func(name, title, fileType, contents string, created time.Time, shares ...domain.ConversationID) domain.FileID {
		t.Helper()
		file := domain.RemoteFile{
			ID: domain.FileID("F-remote-" + name + "-" + f.suffix), WorkspaceID: f.workspaceID, ExternalID: "remote-" + name + "-" + f.suffix,
			Title: title, FileType: fileType, ExternalURL: "https://docs.example/" + name, IndexableContents: contents, CreatedAt: created,
		}
		if err := f.repository.AddRemoteFile(ctx, file, f.event("remote-"+name, "file.remote_added", string(file.ID))); err != nil {
			t.Fatal(err)
		}
		if len(shares) > 0 {
			if _, err := f.repository.SetRemoteFileShares(ctx, f.workspaceID, domain.RemoteFileLookup{ExternalID: file.ExternalID}, shares, f.event("remote-share-"+name, "file.remote_shared", string(file.ID))); err != nil {
				t.Fatal(err)
			}
		}
		return file.ID
	}
	public := add("public", "Roadmap.gdoc", "gdoc", "Quarterly hiring", base, f.channelID)
	hidden := add("private", "Budget", "gsheet", "quarterly budget", base.Add(time.Second), private)
	add("unshared", "Draft", "gdoc", "quarterly draft", base.Add(2*time.Second))

	search := func(user domain.UserID, value domain.FileSearch, limit int) ([]domain.FileID, int) {
		t.Helper()
		if value.Direction == "" {
			value.Direction = domain.SearchDirectionDescending
		}
		found, total, err := f.repository.SearchRemoteFiles(ctx, f.workspaceID, user, value, limit)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]domain.FileID, 0, len(found))
		for _, file := range found {
			ids = append(ids, file.ID)
			if len(file.SharedChannels) == 0 || file.ExternalURL == "" {
				t.Fatalf("a found remote file lacks its shares or link: %+v", file)
			}
		}
		return ids, total
	}
	same := func(name string, got []domain.FileID, total int, want []domain.FileID, wantTotal int) {
		t.Helper()
		if total != wantTotal || len(got) != len(want) {
			t.Fatalf("%s: %v of %d, want %v of %d", name, got, total, want, wantTotal)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("%s: %v, want %v", name, got, want)
			}
		}
	}
	terms := domain.FileSearch{Terms: []string{"QUARTERLY"}}
	got, total := search(f.userID, terms, 10)
	same("a member, newest first", got, total, []domain.FileID{hidden, public}, 2)
	got, total = search(f.userID, terms, 1)
	same("a window of one", got, total, []domain.FileID{hidden}, 2)
	got, total = search(outsider, terms, 10)
	same("an outsider, who reads only the public channel", got, total, []domain.FileID{public}, 1)
	got, total = search(f.userID, domain.FileSearch{Terms: []string{"roadmap"}, Direction: domain.SearchDirectionAscending}, 10)
	same("a title", got, total, []domain.FileID{public}, 1)
	got, total = search(f.userID, domain.FileSearch{Terms: []string{"quarterly"}, ExcludedTerms: []string{"budget"}}, 10)
	same("an excluded term", got, total, []domain.FileID{public}, 1)
	got, total = search(f.userID, domain.FileSearch{Terms: []string{"quarterly"}, FileType: "gdoc"}, 10)
	same("a declared type", got, total, []domain.FileID{public}, 1)
	got, total = search(f.userID, domain.FileSearch{Terms: []string{"quarterly"}, Conversation: private}, 10)
	same("one conversation", got, total, []domain.FileID{hidden}, 1)
	got, total = search(f.userID, domain.FileSearch{Terms: []string{"quarterly"}, After: base.Add(time.Second)}, 10)
	same("after an instant", got, total, []domain.FileID{hidden}, 1)
	got, total = search(f.userID, domain.FileSearch{Terms: []string{"quarterly"}, Uploader: f.userID}, 10)
	same("by uploader", got, total, nil, 0)
}
