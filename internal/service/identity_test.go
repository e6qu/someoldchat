package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// contestedInstantStore reports the first attempts of every message creation as
// a contested microsecond, exactly as a repository does when another message in
// the conversation already owns the instant.
type contestedInstantStore struct {
	store.Store
	refusals int
	instants []time.Time
	events   []events.Event
}

func (s *contestedInstantStore) CreateMessage(ctx context.Context, message domain.Message, event events.Event, key string) error {
	s.instants = append(s.instants, message.CreatedAt)
	s.events = append(s.events, event)
	if len(s.instants) <= s.refusals {
		return store.ErrMessageTimestampTaken
	}
	return s.Store.CreateMessage(ctx, message, event, key)
}

// TestPostAdvancesToAFreeMicrosecondWhenOneIsTaken is the write half of "a
// message's timestamp is unique by construction".
//
// Before this change the service asked for one instant, the repository truncated
// it and stored it whatever was already there, and two messages posted inside
// one microsecond shared one public identifier — so chat.update, chat.delete and
// reactions.add against that ts all resolved to whichever sorted first by id.
func TestPostAdvancesToAFreeMicrosecondWhenOneIsTaken(t *testing.T) {
	ctx := context.Background()
	base := memory.New()
	if err := base.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := base.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := base.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := base.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
	contested := &contestedInstantStore{Store: base, refusals: 2}
	messages := Messages{Store: contested}

	posted, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "U1", "C1", "hello", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(contested.instants) != 3 {
		t.Fatalf("the repository saw %d attempts, want one per contested microsecond plus the one that landed", len(contested.instants))
	}
	for index := 1; index < len(contested.instants); index++ {
		if step := contested.instants[index].Sub(contested.instants[index-1]); step != time.Microsecond {
			t.Fatalf("attempt %d advanced by %s, want exactly one microsecond", index, step)
		}
	}
	if !posted.CreatedAt.Equal(contested.instants[2]) {
		t.Fatalf("the returned message carries %s, want the instant that was actually stored (%s)", posted.CreatedAt, contested.instants[2])
	}
	// The event the outbox carries has to name the identifier the row really has,
	// or every consumer addresses a message that is not there.
	want := string(domain.NewMessageTimestamp(posted.CreatedAt))
	if final := contested.events[2]; !strings.Contains(final.Payload, want) {
		t.Fatalf("the published event payload %q does not carry the stored timestamp %q", final.Payload, want)
	}
	stored, err := messages.Store.GetMessageByCreatedAt(ctx, "C1", posted.CreatedAt)
	if err != nil || stored.ID != posted.ID {
		t.Fatalf("the posted message is not addressable by its own timestamp: %+v err=%v", stored, err)
	}
}

// failingItemStore fails the one-item-at-a-time copy path. The transactional
// method must be used instead, so this never fires.
type failingItemStore struct {
	store.Store
	calls int
}

func (s *failingItemStore) CreateListItem(context.Context, domain.ListItem, events.Event) error {
	s.calls++
	return errors.New("one-at-a-time copy is not the supported path")
}

// TestListCopyIsOneTransaction pins that lists.create with copy_from uses the
// transactional store method.
//
// CreateListWithItems, both implementations and its conformance contract shipped
// while the service still called CreateList and then looped CreateListItem, so
// the half-copied-list defect the port documents was fully live. Before this
// change this test failed with
//
//	copy failed: one-at-a-time copy is not the supported path
//
// and the copy existed with zero records after a list.created had already been
// published for it.
func TestListCopyIsOneTransaction(t *testing.T) {
	ctx := context.Background()
	base := memory.New()
	if err := base.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := base.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	seeding := Messages{Store: base}
	source, err := seeding.CreateList(ctx, "T1", "U1", "Source", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if _, err := seeding.CreateListItem(ctx, "T1", "U1", source.ID, "", `[{"column_id":"title","value":"row"}]`); err != nil {
			t.Fatal(err)
		}
	}

	guarded := &failingItemStore{Store: base}
	messages := Messages{Store: guarded}
	copied, err := messages.CreateList(ctx, "T1", "U1", "Copy", "", "", source.ID, true, false)
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if guarded.calls != 0 {
		t.Fatalf("the copy issued %d one-at-a-time item writes, want none", guarded.calls)
	}
	page, err := messages.ListItems(ctx, "T1", "U1", copied.ID, domain.PageRequest{Limit: 10}, false)
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("copied page=%+v err=%v, want all three records", page, err)
	}
}

// TestUserPhotoRefusesAStreamThatIsNotTheImageItClaims is the confirmed session
// takeover.
//
// SetUserPhoto validated the DECLARED content type only, so a member could
// upload an HTML document labelled image/png; the public capability URL then
// served it as a document on the application origin and the script in it read
// the victim's CSRF token. Before this change every case below was accepted and
// the assertion read
//
//	an HTML document declared as image/png was accepted
func TestUserPhotoRefusesAStreamThatIsNotTheImageItClaims(t *testing.T) {
	ctx := context.Background()
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "photos"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name     string
		declared string
		content  []byte
	}{
		{"an HTML document declared as image/png", "image/png", []byte("<html><script>fetch('/api/auth.test')</script></html>")},
		{"an SVG script container declared as image/svg+xml", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"a PNG declared as a JPEG", "image/jpeg", testImageBytes("png bytes")},
		{"plain text declared as image/gif", "image/gif", []byte("this is not a gif at all, it is text")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := memory.New()
			if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
				t.Fatal(err)
			}
			if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
				t.Fatal(err)
			}
			messages := Messages{Store: s, Blob: objects}
			if _, err := messages.SetUserPhoto(ctx, "T1", "U1", testCase.declared, int64(len(testCase.content)), bytes.NewReader(testCase.content)); !errors.Is(err, ErrInvalidProfile) {
				t.Fatalf("%s was accepted (err=%v)", testCase.name, err)
			}
			user, err := s.GetUser(ctx, "U1")
			if err != nil {
				t.Fatal(err)
			}
			if user.Profile.Image24 != "" {
				t.Fatalf("a refused upload still reached the profile: %q", user.Profile.Image24)
			}
		})
	}

	// The allow-listed types, sniffed and declared in agreement, still pass, and
	// the bytes are not consumed by the sniff.
	for _, testCase := range []struct {
		declared string
		content  []byte
	}{
		{"image/png", append(testImageBytes("real png"), bytes.Repeat([]byte("x"), 600)...)},
		{"image/gif", []byte("GIF89a" + strings.Repeat("y", 40))},
	} {
		s := memory.New()
		if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
			t.Fatal(err)
		}
		messages := Messages{Store: s, Blob: objects}
		user, err := messages.SetUserPhoto(ctx, "T1", "U1", testCase.declared, int64(len(testCase.content)), bytes.NewReader(testCase.content))
		if err != nil {
			t.Fatalf("a genuine %s was refused: %v", testCase.declared, err)
		}
		token := strings.TrimPrefix(user.Profile.Image24, "/users/T1/U1/photo/")
		_, reader, err := messages.OpenUserPhoto(ctx, "T1", "U1", token)
		if err != nil {
			t.Fatal(err)
		}
		round := make([]byte, len(testCase.content))
		if _, err := io.ReadFull(reader, round); err != nil {
			reader.Close()
			t.Fatal(err)
		}
		reader.Close()
		if !bytes.Equal(round, testCase.content) {
			t.Fatalf("the stored %s lost bytes to the sniff: %q", testCase.declared, round)
		}
	}
}

// TestUploadedFileTypeIsDecidedByItsBytes is the metadata half of the same
// defect. files.upload recorded whatever the client declared and published it to
// every API client as mimetype and filetype, so an HTML document uploaded as
// image/png was described to every consumer as an image. Before this change the
// first case below recorded "image/png".
func TestUploadedFileTypeIsDecidedByItsBytes(t *testing.T) {
	ctx := context.Background()
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "files"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name     string
		declared string
		content  []byte
		want     string
	}{
		{"html declared as an image", "image/png", []byte("<html><body>hi</body></html>"), "text/html"},
		{"svg declared as a png", "image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "text/plain"},
		{"a genuine png", "image/png", testImageBytes("real"), "image/png"},
		{"a type the sniffer does not know keeps its declaration", "application/vnd.custom", []byte{0x01, 0x02, 0x03, 0x04}, "application/vnd.custom"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := memory.New()
			if err := s.SeedWorkspace(domain.Workspace{ID: "T1"}); err != nil {
				t.Fatal(err)
			}
			if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
				t.Fatal(err)
			}
			messages := Messages{Store: s, Blob: objects}
			file, err := messages.UploadFile(ctx, "T1", "U1", "f.bin", "f", testCase.declared, "", int64(len(testCase.content)), bytes.NewReader(testCase.content))
			if err != nil {
				t.Fatal(err)
			}
			if file.MIMEType != testCase.want {
				t.Fatalf("recorded type = %q, want %q", file.MIMEType, testCase.want)
			}
			stored, reader, err := messages.OpenFile(ctx, "T1", "U1", file.ID)
			if err != nil {
				t.Fatal(err)
			}
			round := make([]byte, len(testCase.content))
			if _, err := io.ReadFull(reader, round); err != nil {
				reader.Close()
				t.Fatal(err)
			}
			reader.Close()
			if !bytes.Equal(round, testCase.content) {
				t.Fatalf("stored bytes = %q, want the upload unchanged", round)
			}
			if stored.MIMEType != testCase.want {
				t.Fatalf("read-back type = %q, want %q", stored.MIMEType, testCase.want)
			}
		})
	}
}
