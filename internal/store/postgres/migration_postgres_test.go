//go:build postgres

package postgres

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

func TestConcurrentOpenSerializesSchemaMigration(t *testing.T) {
	dsn := os.Getenv("SAMEOLDCHAT_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SAMEOLDCHAT_POSTGRES_DSN is required for PostgreSQL migration qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close PostgreSQL migration test connection: %v", err)
		}
	})
	schemaName := fmt.Sprintf("sameoldchat_migration_%d", time.Now().UnixNano())
	schemaIdentifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schemaIdentifier+" CASCADE"); err != nil {
			t.Errorf("drop migration test schema: %v", err)
		}
	})
	parsedDSN, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedDSN.Query()
	query.Set("search_path", schemaName)
	parsedDSN.RawQuery = query.Encode()

	const replicas = 8
	start := make(chan struct{})
	errorsFound := make(chan error, replicas)
	var workers sync.WaitGroup
	for range replicas {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			store, openErr := Open(ctx, parsedDSN.String())
			if openErr != nil {
				errorsFound <- openErr
				return
			}
			if closeErr := store.Close(); closeErr != nil {
				errorsFound <- closeErr
			}
		}()
	}
	close(start)
	workers.Wait()
	close(errorsFound)
	for openErr := range errorsFound {
		t.Errorf("concurrent PostgreSQL open failed: %v", openErr)
	}
}

// TestOpenUpgradesDatabaseMissingLadderAddedColumns reproduces issue #128 on
// the PostgreSQL dialect: a deployment at schema version 101 had a
// scheduled_messages table without credential_hash, and startup failed with
// SQLSTATE 42703 because the base schema created its index before the ladder
// step that adds the column. The equivalent whole-ladder replay for the
// shared migration logic is TestOpenUpgradesVersion101DatabaseToCurrentSchema
// in internal/store/sqlstore.
func TestOpenUpgradesDatabaseMissingLadderAddedColumns(t *testing.T) {
	dsn := os.Getenv("SAMEOLDCHAT_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SAMEOLDCHAT_POSTGRES_DSN is required for PostgreSQL migration qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close PostgreSQL migration test connection: %v", err)
		}
	})
	schemaName := fmt.Sprintf("sameoldchat_upgrade_%d", time.Now().UnixNano())
	schemaIdentifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schemaIdentifier+" CASCADE"); err != nil {
			t.Errorf("drop upgrade test schema: %v", err)
		}
	})
	parsedDSN, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedDSN.Query()
	query.Set("search_path", schemaName)
	parsedDSN.RawQuery = query.Encode()

	current, err := Open(ctx, parsedDSN.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	// Rewind to the shape issue #128 reported: version 101, before ladder
	// step 102 added scheduled_messages.credential_hash and its index.
	rewind := []string{
		"SET search_path TO " + schemaIdentifier,
		"DROP INDEX scheduled_messages_credential",
		"ALTER TABLE scheduled_messages DROP COLUMN credential_hash",
		"DELETE FROM schema_migrations",
		"INSERT INTO schema_migrations(version, applied_at) VALUES (101, '2026-01-01T00:00:00Z')",
	}
	for _, statement := range rewind {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	upgraded, err := Open(ctx, parsedDSN.String())
	if err != nil {
		t.Fatalf("open version 101 PostgreSQL database with current release: %v", err)
	}
	defer upgraded.Close()
	var columns, indexes int
	if err := admin.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'scheduled_messages' AND column_name = 'credential_hash'`, schemaName).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = $1 AND indexname = 'scheduled_messages_credential'`, schemaName).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if columns != 1 || indexes != 1 {
		t.Fatalf("upgrade restored credential_hash column=%d index=%d, want 1 and 1", columns, indexes)
	}
}

// TestUpgradeQuarantinesLegacyEventPayloadsOnPostgres is the PostgreSQL half
// of issue #111: pre-typed scalar event payloads must be durably quarantined
// by the upgrade so new event streams stop re-scanning and re-logging them.
// The SQLite half, with the full policy matrix, is
// TestSQLiteLegacyEventPayloadQuarantineStopsRescans in internal/store/sqlstore.
func TestUpgradeQuarantinesLegacyEventPayloadsOnPostgres(t *testing.T) {
	dsn := os.Getenv("SAMEOLDCHAT_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SAMEOLDCHAT_POSTGRES_DSN is required for PostgreSQL migration qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close PostgreSQL migration test connection: %v", err)
		}
	})
	schemaName := fmt.Sprintf("sameoldchat_quarantine_%d", time.Now().UnixNano())
	schemaIdentifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schemaIdentifier+" CASCADE"); err != nil {
			t.Errorf("drop quarantine test schema: %v", err)
		}
	})
	parsedDSN, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedDSN.Query()
	query.Set("search_path", schemaName)
	parsedDSN.RawQuery = query.Encode()

	first, err := Open(ctx, parsedDSN.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000000, 0).UTC()
	if err := first.AppendEvent(ctx, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "conversation.read", Payload: "C0123", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	healthy, err := events.New("E2", "T1", "U1", events.NewPayload("conversation.read"), at)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AppendEvent(ctx, healthy); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	// Rewind to the release before the quarantine pass existed.
	rewind := []string{
		"SET search_path TO " + schemaIdentifier,
		"ALTER TABLE outbox DROP COLUMN undeliverable",
		"DELETE FROM schema_backfills WHERE name = 'outbox.undeliverable'",
		"DELETE FROM schema_migrations WHERE version >= 127",
		"INSERT INTO schema_migrations(version, applied_at) VALUES (126, '2026-01-01T00:00:00Z') ON CONFLICT (version) DO NOTHING",
	}
	for _, statement := range rewind {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	upgraded, err := Open(ctx, parsedDSN.String())
	if err != nil {
		t.Fatalf("open pre-quarantine PostgreSQL database with current release: %v", err)
	}
	defer upgraded.Close()
	if err := upgraded.AwaitBackfills(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := upgraded.ListEventsAfter(ctx, "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Event.ID != "E2" || records[0].Sequence != 2 {
		t.Fatalf("after the upgrade the stream returned %+v, want only E2 at its original sequence 2", records)
	}
	notices, err := upgraded.MigrationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	quarantined := 0
	for _, notice := range notices {
		if notice.Kind == "event_payload_quarantined" && notice.Subject == "E1" {
			quarantined++
		}
	}
	if quarantined != 1 {
		t.Fatalf("quarantine notices for E1 = %d, want exactly 1 (notices: %+v)", quarantined, notices)
	}
}
