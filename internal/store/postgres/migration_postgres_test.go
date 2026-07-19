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
