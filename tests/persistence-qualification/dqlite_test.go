//go:build dqlite

package qualification

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	adapter "github.com/sameoldchat/sameoldchat/internal/store/dqlite"
)

func openStore(t *testing.T, ctx context.Context) (qualificationStore, func()) {
	t.Helper()
	addresses := []string{freeAddress(t), freeAddress(t), freeAddress(t)}
	directories := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	database := fmt.Sprintf("shared_qualification_%d", time.Now().UnixNano())
	first, err := adapter.Open(ctx, adapter.Config{Directory: directories[0], Address: addresses[0], Database: database})
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Open(ctx, adapter.Config{Directory: directories[1], Address: addresses[1], Cluster: []string{addresses[0]}, Database: database})
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	third, err := adapter.Open(ctx, adapter.Config{Directory: directories[2], Address: addresses[2], Cluster: []string{addresses[0]}, Database: database})
	if err != nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal(err)
	}
	waitForQuorum(t, ctx, first)
	return first, func() {
		_ = third.Close()
		_ = second.Close()
		_ = first.Close()
	}
}

func waitForQuorum(t *testing.T, ctx context.Context, repository *adapter.Store) {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		health, err := repository.Health(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if health.Quorum {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("dqlite quorum did not become ready; last health=%+v", health)
		case <-ticker.C:
		}
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
