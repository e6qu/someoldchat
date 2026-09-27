package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// FileAccessGrantRepository is the part of store.Store the file grant check
// drives.
type FileAccessGrantRepository interface {
	CreateFile(context.Context, domain.File, events.Event) error
	GrantFileAccess(context.Context, []domain.FileAccessGrant) error
	FileReadableViaGrant(context.Context, domain.WorkspaceID, domain.UserID, domain.FileID) (bool, error)
}

// CheckFileAccessGrants requires durable, idempotent read grants scoped to
// one grantee and one existing file. The repository must hold workspace T1
// with members U1 and UB.
func CheckFileAccessGrants(t *testing.T, repository FileAccessGrantRepository) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	created, err := events.New("Ev-file", "T1", "U1", events.NewPayload("file.created", events.String("file_id", "Fgrant")), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateFile(ctx, domain.File{ID: "Fgrant", WorkspaceID: "T1", Uploader: "U1", Name: "evidence.pdf", BlobKey: "blob-grant", CreatedAt: now}, created); err != nil {
		t.Fatal(err)
	}
	grant := domain.FileAccessGrant{FileID: "Fgrant", WorkspaceID: "T1", UserID: "UB", GrantedAt: now}
	for range 2 {
		if err := repository.GrantFileAccess(ctx, []domain.FileAccessGrant{grant}); err != nil {
			t.Fatalf("grant (repeated grants are not an error): %v", err)
		}
	}
	for _, probe := range []struct {
		workspace domain.WorkspaceID
		user      domain.UserID
		want      bool
	}{{"T1", "UB", true}, {"T1", "U1", false}, {"T2", "UB", false}} {
		readable, err := repository.FileReadableViaGrant(ctx, probe.workspace, probe.user, "Fgrant")
		if err != nil || readable != probe.want {
			t.Fatalf("%s/%s readable=%v err=%v, want %v", probe.workspace, probe.user, readable, err, probe.want)
		}
	}
	missing := grant
	missing.FileID = "Fmissing"
	if err := repository.GrantFileAccess(ctx, []domain.FileAccessGrant{missing}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("grant on an unknown file err=%v, want ErrNotFound", err)
	}
}
