//go:build dqlite

package qualification

import (
	"context"
	"fmt"
	"testing"
	"time"

	adapter "github.com/sameoldchat/sameoldchat/internal/store/dqlite"
	"github.com/sameoldchat/sameoldchat/internal/store/dqlitetest"
)

func openStore(t *testing.T, ctx context.Context) (qualificationStore, func()) {
	t.Helper()
	network, err := dqlitetest.NewNetwork(3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = network.Close() })
	connections := network.Connections()
	addresses := []string{connections[0].Address, connections[1].Address, connections[2].Address}
	directories := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	database := fmt.Sprintf("shared_qualification_%d", time.Now().UnixNano())
	first, err := adapter.Open(ctx, adapter.Config{Directory: directories[0], Address: addresses[0], Database: database, ExternalDial: connections[0].Dial, ExternalAccept: connections[0].Accept, ExternalReady: connections[0].Activate, ExternalClose: connections[0].Deactivate})
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Open(ctx, adapter.Config{Directory: directories[1], Address: addresses[1], Cluster: []string{addresses[0]}, Database: database, ExternalDial: connections[1].Dial, ExternalAccept: connections[1].Accept, ExternalReady: connections[1].Activate, ExternalClose: connections[1].Deactivate})
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	third, err := adapter.Open(ctx, adapter.Config{Directory: directories[2], Address: addresses[2], Cluster: []string{addresses[0]}, Database: database, ExternalDial: connections[2].Dial, ExternalAccept: connections[2].Accept, ExternalReady: connections[2].Activate, ExternalClose: connections[2].Deactivate})
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
