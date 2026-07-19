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
	time.Sleep(300 * time.Millisecond)
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
	request := httptest.NewRequest(http.MethodPost, "/api/message", nil)
	id, err := spool.Enqueue(context.Background(), request, []byte("renew me"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Claim(context.Background(), "slow-owner", 1, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if err := spool.Renew(context.Background(), "slow-owner", []uint64{id}, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
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

func TestSQLiteSpoolExpiredOwnerCannotRenewOrDelete(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/message", nil)
	id, err := spool.Enqueue(context.Background(), request, []byte("expired"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Claim(context.Background(), "expired-owner", 1, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := spool.Renew(context.Background(), "expired-owner", []uint64{id}, time.Minute); !errors.Is(err, ErrSpoolLeaseLost) {
		t.Fatalf("expired renewal error=%v, want ErrSpoolLeaseLost", err)
	}
	if err := spool.Delete(context.Background(), "expired-owner", id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired deletion error=%v, want sql.ErrNoRows", err)
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
