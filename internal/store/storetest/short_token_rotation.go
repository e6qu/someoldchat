package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// ShortTokenRotationRepository is the part of store.Store the short-secret
// rotation check drives.
type ShortTokenRotationRepository interface {
	SeedToken(context.Context, string, domain.TokenRecord) error
	RevokeToken(context.Context, string) error
	LookupToken(context.Context, string) (domain.TokenRecord, error)
	BeginShortTokenRotation(context.Context, domain.ShortTokenRotation) error
	CompleteShortTokenRotation(ctx context.Context, tokenHash, newTokenHash string, appID domain.AppID, now time.Time) error
}

// CheckShortTokenRotation requires a begun rotation to be completable only
// within its window, only for its app and only with the replacement it named,
// to be replaced by beginning again, and on completion to move the original
// credential to the replacement: the original stops authenticating and the
// replacement authenticates as the same user, app and scopes. The repository
// must hold workspace T1 with member U1.
func CheckShortTokenRotation(t *testing.T, repository ShortTokenRotationRepository) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_758_000_000, 0).UTC()
	const original = "xoxp-111-222-333-d6bc76"
	first := "xoxp-111-222-333-" + "11111111111111111111111111111111"
	second := "xoxp-111-222-333-" + "22222222222222222222222222222222"
	if err := repository.SeedToken(ctx, original, domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: domain.TokenUser, Scopes: []string{"chat:write", "users:read"}}); err != nil {
		t.Fatal(err)
	}
	begin := func(replacement string, expires time.Time) {
		t.Helper()
		if err := repository.BeginShortTokenRotation(ctx, domain.ShortTokenRotation{
			TokenHash: domain.HashToken(original), NewTokenHash: domain.HashToken(replacement), AppID: "A1", ExpiresAt: expires,
		}); err != nil {
			t.Fatal(err)
		}
	}
	complete := func(replacement string, app domain.AppID, at time.Time) error {
		return repository.CompleteShortTokenRotation(ctx, domain.HashToken(original), domain.HashToken(replacement), app, at)
	}

	if err := complete(first, "A1", now); !errors.Is(err, domain.ErrShortTokenRotationNotFound) {
		t.Fatalf("completion with nothing begun err=%v", err)
	}
	// A rotation completed after its window has to start over.
	begin(first, now.Add(domain.ShortTokenRotationWindow))
	if err := complete(first, "A1", now.Add(domain.ShortTokenRotationWindow)); !errors.Is(err, domain.ErrShortTokenRotationNotFound) {
		t.Fatalf("completion after the window err=%v", err)
	}
	if err := complete(first, "A1", now); !errors.Is(err, domain.ErrShortTokenRotationNotFound) {
		t.Fatalf("an expired rotation survived its refusal: err=%v", err)
	}
	// Beginning again replaces the pending replacement.
	begin(first, now.Add(domain.ShortTokenRotationWindow))
	begin(second, now.Add(domain.ShortTokenRotationWindow))
	if err := complete(first, "A1", now); !errors.Is(err, domain.ErrShortTokenRotationMismatch) {
		t.Fatalf("completion with a superseded replacement err=%v", err)
	}
	if err := complete(second, "A2", now); !errors.Is(err, domain.ErrOAuthAppMismatch) {
		t.Fatalf("completion by another app err=%v", err)
	}
	if record, err := repository.LookupToken(ctx, original); err != nil || record.UserID != "U1" {
		t.Fatalf("the original stopped authenticating before completion: %+v err=%v", record, err)
	}
	if _, err := repository.LookupToken(ctx, second); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the replacement authenticated before completion: err=%v", err)
	}
	if err := complete(second, "A1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.LookupToken(ctx, original); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the original still authenticates: err=%v", err)
	}
	record, err := repository.LookupToken(ctx, second)
	if err != nil || record.WorkspaceID != "T1" || record.UserID != "U1" || record.AppID != "A1" || record.TokenType != domain.TokenUser ||
		len(record.Scopes) != 2 || record.Scopes[0] != "chat:write" || record.Scopes[1] != "users:read" {
		t.Fatalf("replacement record = %+v err=%v", record, err)
	}
	// The rotation is spent.
	if err := complete(second, "A1", now); !errors.Is(err, domain.ErrShortTokenRotationNotFound) {
		t.Fatalf("a completed rotation completed twice: err=%v", err)
	}

	// A token revoked while its rotation was pending is not resurrected.
	const revoked = "xoxp-444-555-666-abcdef"
	if err := repository.SeedToken(ctx, revoked, domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: domain.TokenUser, Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	replacement := "xoxp-444-555-666-" + "33333333333333333333333333333333"
	if err := repository.BeginShortTokenRotation(ctx, domain.ShortTokenRotation{TokenHash: domain.HashToken(revoked), NewTokenHash: domain.HashToken(replacement), AppID: "A1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repository.RevokeToken(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteShortTokenRotation(ctx, domain.HashToken(revoked), domain.HashToken(replacement), "A1", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("completion of a revoked token err=%v", err)
	}
	if _, err := repository.LookupToken(ctx, replacement); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a revoked token's replacement authenticates: err=%v", err)
	}
}
