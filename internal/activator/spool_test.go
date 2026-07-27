package activator

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteSpoolEncryptsAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	key := []byte("01234567890123456789012345678901")
	request := httptest.NewRequest(http.MethodPost, "https://app.invalid/api/message?x=1", nil)
	request.Header.Set("Authorization", "Bearer secret")
	first, err := OpenSQLiteSpool(path, key, SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	id, err := first.Enqueue(context.Background(), request, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenSQLiteSpool(path, key, SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	values, err := second.List(context.Background(), 10)
	if err != nil || len(values) != 1 || values[0].ID != id || string(values[0].Body) != "hello" || values[0].Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("values=%+v err=%v", values, err)
	}
	claimed, err := second.Claim(context.Background(), "test-owner", 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != id {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	other, err := second.Claim(context.Background(), "other-owner", 10, time.Minute)
	if err != nil || len(other) != 0 {
		t.Fatalf("lease was not exclusive: other=%+v err=%v", other, err)
	}
	if err := second.Delete(context.Background(), "test-owner", id); err != nil {
		t.Fatal(err)
	}
	values, err = second.List(context.Background(), 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("remaining=%+v err=%v", values, err)
	}
}

func TestSQLiteSpoolRejectsWrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	first, err := OpenSQLiteSpool(path, []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/message", nil)
	if _, err := first.Enqueue(context.Background(), request, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenSQLiteSpool(path, []byte("abcdefghijklmnopqrstuvwxyz123456"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := second.List(context.Background(), 10); err == nil {
		t.Fatal("spool accepted ciphertext encrypted with a different key")
	}
}

func TestSQLiteSpoolLeaseExpiresForCrashRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	spool, err := OpenSQLiteSpool(path, []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	now := time.Date(2026, time.July, 20, 7, 0, 0, 0, time.UTC)
	spool.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodPost, "/api/message", nil)
	id, err := spool.Enqueue(context.Background(), request, []byte("recover me"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := spool.Claim(context.Background(), "crashed-owner", 1, 250*time.Millisecond)
	if err != nil || len(claimed) != 1 || claimed[0].ID != id {
		t.Fatalf("initial claim=%+v err=%v", claimed, err)
	}
	claimed, err = spool.Claim(context.Background(), "replacement-owner", 1, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("unexpired lease was not exclusive: %+v err=%v", claimed, err)
	}
	now = now.Add(300 * time.Millisecond)
	claimed, err = spool.Claim(context.Background(), "replacement-owner", 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != id {
		t.Fatalf("expired lease was not reclaimable: %+v err=%v", claimed, err)
	}
	if err := spool.Delete(context.Background(), "replacement-owner", id); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSpoolRenewKeepsLeaseWithSlowDelivery(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	now := time.Date(2026, time.July, 20, 7, 0, 0, 0, time.UTC)
	spool.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodPost, "/api/message", nil)
	id, err := spool.Enqueue(context.Background(), request, []byte("renew me"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Claim(context.Background(), "slow-owner", 1, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Millisecond)
	if err := spool.Renew(context.Background(), "slow-owner", []uint64{id}, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	now = now.Add(50 * time.Millisecond)
	claimed, err := spool.Claim(context.Background(), "replacement-owner", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("renewed lease was reclaimed: %+v", claimed)
	}
}

func TestSQLiteSpoolRenewRequiresOwnership(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/message", nil)
	id, err := spool.Enqueue(context.Background(), request, []byte("owned"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Claim(context.Background(), "owner-a", 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := spool.Renew(context.Background(), "owner-b", []uint64{id}, time.Minute); !errors.Is(err, ErrSpoolLeaseLost) {
		t.Fatalf("renewal error=%v, want ErrSpoolLeaseLost", err)
	}
}

// This replaces an assertion that an expired lease also refuses the delete.
// That refusal was the mechanism behind duplicate delivery: a mutation applied
// while the lease lapsed could not be removed, so the entry was delivered again
// after the lease expired. Renewal must still require a live lease, and another
// owner taking the entry must still make the delete fail.
func TestSQLiteSpoolExpiredLeaseStillRemovesADeliveredRequest(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	now := time.Date(2026, time.July, 20, 7, 0, 0, 0, time.UTC)
	spool.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodPost, "/api/files.upload", nil)
	id, err := spool.Enqueue(context.Background(), request, []byte("applied once"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Claim(context.Background(), "expired-owner", 1, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	now = now.Add(150 * time.Millisecond)
	if err := spool.Renew(context.Background(), "expired-owner", []uint64{id}, time.Minute); !errors.Is(err, ErrSpoolLeaseLost) {
		t.Fatalf("expired renewal error=%v, want ErrSpoolLeaseLost", err)
	}
	if err := spool.Delete(context.Background(), "expired-owner", id); err != nil {
		t.Fatalf("delete after a lapsed lease error=%v, want the delivered request removed", err)
	}
	pending, err := spool.Pending(context.Background())
	if err != nil || pending != 0 {
		t.Fatalf("pending=%d err=%v, want the applied request gone instead of replayed", pending, err)
	}
}

func TestSQLiteSpoolDeleteRequiresOwnership(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	now := time.Date(2026, time.July, 20, 7, 0, 0, 0, time.UTC)
	spool.now = func() time.Time { return now }
	id, err := spool.Enqueue(context.Background(), httptest.NewRequest(http.MethodPost, "/api/message", nil), []byte("owned"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Claim(context.Background(), "owner-a", 1, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	now = now.Add(150 * time.Millisecond)
	if _, err := spool.Claim(context.Background(), "owner-b", 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := spool.Delete(context.Background(), "owner-a", id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("delete by a superseded owner error=%v, want sql.ErrNoRows", err)
	}
}

// Two claims by the same owner must never return the same rows. Lease identity
// used to be string equality on the formatted expiry timestamp, so with a frozen
// clock a second claim re-selected the first claim's rows.
func TestSQLiteSpoolClaimIdentityDoesNotDependOnTheClock(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	frozen := time.Date(2026, time.July, 20, 7, 0, 0, 0, time.UTC)
	spool.now = func() time.Time { return frozen }
	if _, err := spool.Enqueue(context.Background(), httptest.NewRequest(http.MethodPost, "/api/message", nil), []byte("one")); err != nil {
		t.Fatal(err)
	}
	first, err := spool.Claim(context.Background(), "same-owner", 10, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	second, err := spool.Claim(context.Background(), "same-owner", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second claim=%+v, want no rows: the first claim still holds the lease", second)
	}
}

// One row written under a rotated key used to abort every claim, and nothing ever
// removed it, so the whole durable queue was down permanently.
func TestSQLiteSpoolQuarantinesUndecodableRowAndKeepsDelivering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	stale, err := OpenSQLiteSpool(path, []byte("abcdefghijklmnopqrstuvwxyz123456"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Enqueue(context.Background(), httptest.NewRequest(http.MethodPost, "/api/message", nil), []byte("written under the old key")); err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	rotated, err := OpenSQLiteSpool(path, []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Close()
	good, err := rotated.Enqueue(context.Background(), httptest.NewRequest(http.MethodPost, "/api/message", nil), []byte("deliverable"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := rotated.Claim(context.Background(), "owner", 10, time.Minute)
	if err != nil {
		t.Fatalf("claim error=%v, want the undecodable row skipped", err)
	}
	if len(claimed) != 1 || claimed[0].ID != good {
		t.Fatalf("claimed=%+v, want only the deliverable request", claimed)
	}
	quarantined, err := rotated.Quarantined(context.Background())
	if err != nil || quarantined != 1 {
		t.Fatalf("quarantined=%d err=%v, want the poisoned row visible to operators", quarantined, err)
	}
	// A later claim must also make progress rather than re-reading the bad row.
	if err := rotated.Delete(context.Background(), "owner", good); err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Claim(context.Background(), "owner", 10, time.Minute); err != nil {
		t.Fatalf("claim after quarantine error=%v", err)
	}
	pending, err := rotated.Pending(context.Background())
	if err != nil || pending != 0 {
		t.Fatalf("pending=%d err=%v, want the poisoned row excluded from delivery", pending, err)
	}
}

func TestSQLiteSpoolExpiredOwnerCannotRenew(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	now := time.Date(2026, time.July, 20, 7, 0, 0, 0, time.UTC)
	spool.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodPost, "/api/message", nil)
	id, err := spool.Enqueue(context.Background(), request, []byte("expired"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Claim(context.Background(), "expired-owner", 1, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	now = now.Add(150 * time.Millisecond)
	if err := spool.Renew(context.Background(), "expired-owner", []uint64{id}, time.Minute); !errors.Is(err, ErrSpoolLeaseLost) {
		t.Fatalf("expired renewal error=%v, want ErrSpoolLeaseLost", err)
	}
	claimed, err := spool.Claim(context.Background(), "replacement-owner", 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != id {
		t.Fatalf("expired request was not recoverable: claimed=%+v err=%v", claimed, err)
	}
}

func TestSQLiteSpoolRejectsQueueOverflowBeforeAccepting(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 8, MaxQueuedBytes: 8, MaxQueuedRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/message", nil)
	if _, err := spool.Enqueue(context.Background(), request, []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Enqueue(context.Background(), request, []byte("x")); !errors.Is(err, ErrSpoolCapacity) {
		t.Fatalf("overflow error=%v, want ErrSpoolCapacity", err)
	}
}
