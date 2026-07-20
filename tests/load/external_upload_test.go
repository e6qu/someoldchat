package load

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// The external upload flow is a two-phase commit across three client calls:
// a ticket is issued, bytes arrive, and completion turns the ticket into a
// file. A client that retries the third call — or two clients racing on the
// same ticket — must not produce two files, and every caller must be told the
// same identifier, because that identifier was handed out before the bytes
// existed and callers may have stored it.

func uploadService(t *testing.T) (service.Messages, context.Context) {
	t.Helper()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "load"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return service.Messages{Store: repository, Blob: objects}, context.Background()
}

func TestConcurrentExternalUploadCompletionYieldsOneFile(t *testing.T) {
	const (
		uploads   = 16
		attempts  = 8
		blobBytes = "external upload payload"
	)
	messages, ctx := uploadService(t)

	tickets := make([]domain.ExternalUpload, 0, uploads)
	for index := 0; index < uploads; index++ {
		ticket, err := messages.CreateExternalUpload(ctx, "T1", "U1", fmt.Sprintf("file-%d.txt", index), "text/plain", int64(len(blobBytes)), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := messages.UploadExternalFile(ctx, ticket.ID, int64(len(blobBytes)), bytes.NewReader([]byte(blobBytes))); err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, ticket)
	}

	type outcome struct {
		file domain.File
		err  error
	}
	results := make([][]outcome, uploads)
	var group sync.WaitGroup
	start := make(chan struct{})
	for index, ticket := range tickets {
		results[index] = make([]outcome, attempts)
		for attempt := 0; attempt < attempts; attempt++ {
			group.Add(1)
			go func(index, attempt int, ticket domain.ExternalUpload) {
				defer group.Done()
				<-start
				file, err := messages.CompleteExternalUpload(ctx, "T1", "U1", ticket.ID, "Concurrent", []domain.ConversationID{"C1"}, "", "", "")
				results[index][attempt] = outcome{file: file, err: err}
			}(index, attempt, ticket)
		}
	}
	close(start)
	group.Wait()

	for index, ticket := range tickets {
		for attempt, got := range results[index] {
			if got.err != nil {
				t.Fatalf("upload %d attempt %d failed: %v", index, attempt, got.err)
			}
			if got.file.ID != domain.FileID(ticket.ID) {
				t.Fatalf("upload %d attempt %d produced file %q, want the ticket identifier %q", index, attempt, got.file.ID, ticket.ID)
			}
		}
	}

	// One ticket must leave exactly one file behind, however many callers raced.
	page, err := messages.Files(ctx, "T1", "U1", domain.PageRequest{Limit: uploads * attempts})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Files) != uploads {
		t.Fatalf("%d concurrent completions over %d tickets left %d files, want %d", uploads*attempts, uploads, len(page.Files), uploads)
	}
	seen := make(map[domain.FileID]struct{}, len(page.Files))
	for _, file := range page.Files {
		if _, duplicate := seen[file.ID]; duplicate {
			t.Fatalf("file %q was stored twice", file.ID)
		}
		seen[file.ID] = struct{}{}
	}
}

// Completion shares the file into a conversation and posts its comment. Racing
// callers must not multiply that message.
func TestConcurrentExternalUploadCompletionPostsOneComment(t *testing.T) {
	const attempts = 12
	messages, ctx := uploadService(t)

	ticket, err := messages.CreateExternalUpload(ctx, "T1", "U1", "shared.txt", "text/plain", 4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.UploadExternalFile(ctx, ticket.ID, 4, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, attempts)
	for attempt := 0; attempt < attempts; attempt++ {
		group.Add(1)
		go func(attempt int) {
			defer group.Done()
			<-start
			_, err := messages.CompleteExternalUpload(ctx, "T1", "U1", ticket.ID, "Shared", []domain.ConversationID{"C1"}, "here it is", "", "")
			errs[attempt] = err
		}(attempt)
	}
	close(start)
	group.Wait()

	for attempt, err := range errs {
		if err != nil {
			t.Fatalf("attempt %d failed: %v", attempt, err)
		}
	}
	history, err := messages.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: attempts + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 1 {
		t.Fatalf("%d concurrent completions posted %d messages, want 1", attempts, len(history.Messages))
	}
}
