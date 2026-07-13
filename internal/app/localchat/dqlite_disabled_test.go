//go:build !dqlite

package localchat

import (
	"context"
	"strings"
	"testing"
)

func TestDqliteRequiresExplicitBuildProfile(t *testing.T) {
	_, err := Open(context.Background(), Config{Backend: BackendDqlite, DqliteDirectory: "/tmp/state", DqliteAddress: "127.0.0.1:19001", DqliteCluster: []string{"127.0.0.1:19001", "127.0.0.1:19002", "127.0.0.1:19003"}, DqliteDatabase: "sameoldchat"})
	if err == nil || !strings.Contains(err.Error(), "dqlite build profile") {
		t.Fatalf("error = %v, want explicit dqlite profile failure", err)
	}
}
