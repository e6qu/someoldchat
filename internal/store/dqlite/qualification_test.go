//go:build dqlite

package dqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/canonical/go-dqlite/v3/app"
)

func TestThreeNodeClusterCommitReadAndHandover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	addresses := []string{freeAddress(t), freeAddress(t), freeAddress(t)}
	directories := []string{t.TempDir(), t.TempDir(), t.TempDir()}

	first, err := app.New(directories[0], app.WithAddress(addresses[0]), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := app.New(directories[1], app.WithAddress(addresses[1]), app.WithCluster([]string{addresses[0]}), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	third, err := app.New(directories[2], app.WithAddress(addresses[2]), app.WithCluster([]string{addresses[0]}), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()

	for _, node := range []*app.App{first, second, third} {
		if err := node.Ready(ctx); err != nil {
			t.Fatal(err)
		}
	}

	databaseName := fmt.Sprintf("qualification_%d", time.Now().UnixNano())
	firstDB := openDatabase(t, ctx, first, databaseName)
	secondDB := openDatabase(t, ctx, second, databaseName)
	thirdDB := openDatabase(t, ctx, third, databaseName)
	defer firstDB.Close()
	defer secondDB.Close()
	defer thirdDB.Close()

	if _, err := firstDB.ExecContext(ctx, "CREATE TABLE qualification (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := firstDB.ExecContext(ctx, "INSERT INTO qualification(id, value) VALUES (1, 'committed')"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := secondDB.QueryRowContext(ctx, "SELECT value FROM qualification WHERE id = 1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "committed" {
		t.Fatalf("value=%q, want committed", value)
	}

	if err := first.Handover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secondDB.QueryRowContext(ctx, "SELECT value FROM qualification WHERE id = 1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "committed" {
		t.Fatalf("value after handover=%q, want committed", value)
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func openDatabase(t *testing.T, ctx context.Context, node *app.App, name string) *sql.DB {
	t.Helper()
	database, err := node.Open(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return database
}
