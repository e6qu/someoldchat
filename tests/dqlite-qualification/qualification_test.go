//go:build dqlite

package qualification

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/canonical/go-dqlite/v3/app"
	"github.com/canonical/go-dqlite/v3/client"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	adapter "github.com/sameoldchat/sameoldchat/internal/store/dqlite"
	"github.com/sameoldchat/sameoldchat/internal/store/dqlitetest"
)

func TestThreeNodeClusterCommitReadAndHandover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	network := newTestNetwork(t, 3)
	connections := network.Connections()
	addresses := connectionAddresses(connections)
	directories := []string{t.TempDir(), t.TempDir(), t.TempDir()}

	first, err := app.New(directories[0], app.WithAddress(addresses[0]), connections[0].Option(), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	connections[0].Activate()
	firstClosed := false
	t.Cleanup(func() {
		if !firstClosed {
			_ = closeDqliteApp(connections[0], first)
		}
	})
	second, err := app.New(directories[1], app.WithAddress(addresses[1]), connections[1].Option(), app.WithCluster([]string{addresses[0]}), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	connections[1].Activate()
	defer func() { _ = closeDqliteApp(connections[1], second) }()
	third, err := app.New(directories[2], app.WithAddress(addresses[2]), connections[2].Option(), app.WithCluster([]string{addresses[0]}), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	connections[2].Activate()
	defer func() { _ = closeDqliteApp(connections[2], third) }()

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
	if err := closeDqliteApp(connections[0], first); err != nil {
		t.Fatal(err)
	}
	firstClosed = true
	if err := secondDB.QueryRowContext(ctx, "SELECT value FROM qualification WHERE id = 1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "committed" {
		t.Fatalf("value after handover=%q, want committed", value)
	}
}

func TestDqliteLeaderFailurePreservesCommittedData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	network := newTestNetwork(t, 3)
	connections := network.Connections()
	addresses := connectionAddresses(connections)
	directories := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	databaseName := fmt.Sprintf("leader_failure_%d", time.Now().UnixNano())
	first, err := adapter.Open(ctx, storeConfig(connections[0], directories[0], databaseName, nil))
	if err != nil {
		t.Fatal(err)
	}
	firstClosed := false
	defer func() {
		if !firstClosed {
			_ = first.Close()
		}
	}()
	second, err := adapter.Open(ctx, storeConfig(connections[1], directories[1], databaseName, []string{addresses[0]}))
	if err != nil {
		t.Fatal(err)
	}
	secondClosed := false
	defer func() {
		if !secondClosed {
			_ = second.Close()
		}
	}()
	third, err := adapter.Open(ctx, storeConfig(connections[2], directories[2], databaseName, []string{addresses[0]}))
	if err != nil {
		t.Fatal(err)
	}
	thirdClosed := false
	defer func() {
		if !thirdClosed {
			_ = third.Close()
		}
	}()

	health := waitForQuorum(t, ctx, first)
	if health.Leader != addresses[0] {
		t.Fatalf("initial health=%+v, want bootstrap node %q as leader", health, addresses[0])
	}
	workspace := domain.Workspace{ID: "T-leader-failure", Domain: "leader-failure.example.test", Name: "leader failure", Discoverability: domain.WorkspaceDiscoverabilityOpen}
	if err := first.SeedWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	firstClosed = true
	if remote, err := connections[1].Dial(ctx, addresses[0]); err == nil {
		_ = remote.Close()
		t.Fatal("peer dial reached a deactivated dqlite transport")
	}

	degraded := waitForLeaderChange(t, ctx, second, addresses[0])
	if degraded.Nodes != 3 || degraded.Voters != 3 || degraded.ReachableVoters != 2 || !degraded.Quorum {
		t.Fatalf("post-failure health=%+v, want three configured voters, two reachable voters, and quorum", degraded)
	}
	loaded, err := second.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != workspace.Name {
		t.Fatalf("workspace after leader failure=%+v, want name %q", loaded, workspace.Name)
	}

	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	secondClosed = true
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	thirdClosed = true
}

func TestDqliteAdapterHealthReportsLeaderAndQuorum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	network := newTestNetwork(t, 3)
	connections := network.Connections()
	addresses := connectionAddresses(connections)
	directories := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	databaseName := fmt.Sprintf("adapter_health_%d", time.Now().UnixNano())
	first, err := adapter.Open(ctx, storeConfig(connections[0], directories[0], databaseName, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := adapter.Open(ctx, storeConfig(connections[1], directories[1], databaseName, []string{addresses[0]}))
	if err != nil {
		t.Fatal(err)
	}
	secondClosed := false
	defer func() {
		if !secondClosed {
			_ = second.Close()
		}
	}()
	third, err := adapter.Open(ctx, storeConfig(connections[2], directories[2], databaseName, []string{addresses[0]}))
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()

	health := waitForQuorum(t, ctx, first)
	if health.Leader == "" || health.Nodes != 3 || health.Voters != 3 || health.ReachableVoters != 3 || !health.Quorum {
		t.Fatalf("health=%+v, want one leader, three nodes, three configured and reachable voters, and quorum", health)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	secondClosed = true
	degraded := waitForReachableVoters(t, ctx, first, 2)
	if degraded.Nodes != 3 || degraded.Voters != 3 || degraded.ReachableVoters != 2 || !degraded.Quorum {
		t.Fatalf("health=%+v, want three configured voters, two reachable voters, and quorum", degraded)
	}
}

func waitForQuorum(t *testing.T, ctx context.Context, store *adapter.Store) adapter.Health {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		health, err := store.Health(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if health.Quorum {
			return health
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

func waitForReachableVoters(t *testing.T, ctx context.Context, store *adapter.Store, want int) adapter.Health {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		health, err := store.Health(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if health.ReachableVoters == want {
			return health
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("dqlite reachable voters did not become %d; last health=%+v", want, health)
		case <-ticker.C:
		}
	}
}

func waitForLeaderChange(t *testing.T, ctx context.Context, store *adapter.Store, failedAddress string) adapter.Health {
	t.Helper()
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastHealth := adapter.Health{}
	lastError := "no health error"
	for {
		health, err := store.Health(ctx)
		if err == nil {
			lastHealth = health
			if health.Leader != failedAddress && health.Quorum {
				return health
			}
		} else {
			lastError = err.Error()
		}
		select {
		case <-ctx.Done():
			t.Fatalf("leader did not change before context deadline: last health=%+v last error=%s", lastHealth, lastError)
		case <-deadline.C:
			t.Fatalf("leader did not change within qualification deadline: last health=%+v last error=%s", lastHealth, lastError)
		case <-ticker.C:
		}
	}
}

func TestDqliteAdapterReplicatesRepositoryWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	network := newTestNetwork(t, 3)
	connections := network.Connections()
	addresses := connectionAddresses(connections)
	directories := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	databaseName := fmt.Sprintf("adapter_repository_%d", time.Now().UnixNano())
	first, err := adapter.Open(ctx, storeConfig(connections[0], directories[0], databaseName, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := adapter.Open(ctx, storeConfig(connections[1], directories[1], databaseName, []string{addresses[0]}))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	third, err := adapter.Open(ctx, storeConfig(connections[2], directories[2], databaseName, []string{addresses[0]}))
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	waitForQuorum(t, ctx, first)

	workspace := domain.Workspace{ID: "T-dqlite", Domain: "dqlite.example.test", Name: "dqlite qualification", Discoverability: domain.WorkspaceDiscoverabilityOpen}
	if err := first.SeedWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	user := domain.User{ID: "U-dqlite", WorkspaceID: workspace.ID, Email: "user@example.test", Name: "user", RealName: "Qualification User"}
	if err := first.SeedUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	conversation := domain.Conversation{ID: "C-dqlite", WorkspaceID: workspace.ID, Name: "general"}
	if err := first.SeedConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}

	gotWorkspace, err := second.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotWorkspace.Name != workspace.Name {
		t.Fatalf("workspace=%+v, want name %q", gotWorkspace, workspace.Name)
	}
	gotUser, err := third.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotUser.Email != user.Email || gotUser.RealName != user.RealName {
		t.Fatalf("user=%+v, want email %q and real name %q", gotUser, user.Email, user.RealName)
	}
	gotConversation, err := second.GetConversation(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotConversation.Name != conversation.Name {
		t.Fatalf("conversation=%+v, want name %q", gotConversation, conversation.Name)
	}
}

func TestDqliteStateDirectorySnapshotRestoresCluster(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	network := newTestNetwork(t, 3)
	connections := network.Connections()
	addresses := connectionAddresses(connections)
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source-cluster")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"node-a", "node-b", "node-c"} {
		if err := os.MkdirAll(filepath.Join(sourceRoot, node), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	databaseName := fmt.Sprintf("directory_restore_%d", time.Now().UnixNano())
	first, err := adapter.Open(ctx, storeConfig(connections[0], filepath.Join(sourceRoot, "node-a"), databaseName, nil))
	if err != nil {
		t.Fatal(err)
	}
	firstClosed := false
	defer func() {
		if !firstClosed {
			_ = first.Close()
		}
	}()
	second, err := adapter.Open(ctx, storeConfig(connections[1], filepath.Join(sourceRoot, "node-b"), databaseName, []string{addresses[0]}))
	if err != nil {
		t.Fatal(err)
	}
	secondClosed := false
	defer func() {
		if !secondClosed {
			_ = second.Close()
		}
	}()
	third, err := adapter.Open(ctx, storeConfig(connections[2], filepath.Join(sourceRoot, "node-c"), databaseName, []string{addresses[0]}))
	if err != nil {
		t.Fatal(err)
	}
	thirdClosed := false
	defer func() {
		if !thirdClosed {
			_ = third.Close()
		}
	}()
	waitForQuorum(t, ctx, first)
	workspace := domain.Workspace{ID: "T-directory-restore", Domain: "restore.example.test", Name: "restored workspace"}
	if err := first.SeedWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	firstClosed = true
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	secondClosed = true
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	thirdClosed = true

	manager, err := lifecycle.NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{8}, 32), "dqlite-directory-key", 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := lifecycle.NewDirectorySnapshotter(manager, sourceRoot, filepath.Join(root, "restored-cluster"), lifecycle.Manifest{Backend: "dqlite", SchemaVersion: 1, ApplicationVersion: "qualification", MinRestorerVersion: "qualification", MaxRestorerVersion: "qualification"}, lifecycle.DirectorySnapshotSourceStopped)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := snapshotter.Create(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Restore(ctx, manifest); err != nil {
		t.Fatal(err)
	}

	restoredRoot := filepath.Join(root, "restored-cluster")
	restoredConnections := network.Connections()
	restoredFirst, err := app.New(filepath.Join(restoredRoot, "node-a"), app.WithAddress(addresses[0]), restoredConnections[0].Option(), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	restoredConnections[0].Activate()
	defer func() { _ = closeDqliteApp(restoredConnections[0], restoredFirst) }()
	restoredSecond, err := app.New(filepath.Join(restoredRoot, "node-b"), app.WithAddress(addresses[1]), restoredConnections[1].Option(), app.WithCluster([]string{addresses[0]}), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	restoredConnections[1].Activate()
	defer func() { _ = closeDqliteApp(restoredConnections[1], restoredSecond) }()
	restoredThird, err := app.New(filepath.Join(restoredRoot, "node-c"), app.WithAddress(addresses[2]), restoredConnections[2].Option(), app.WithCluster([]string{addresses[0]}), app.WithVoters(3))
	if err != nil {
		t.Fatal(err)
	}
	restoredConnections[2].Activate()
	defer func() { _ = closeDqliteApp(restoredConnections[2], restoredThird) }()
	for _, node := range []*app.App{restoredFirst, restoredSecond, restoredThird} {
		if err := node.Ready(ctx); err != nil {
			t.Fatal(err)
		}
	}
	restoredDatabase, err := restoredSecond.Open(ctx, databaseName)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDatabase.Close()
	var name string
	if err := restoredDatabase.QueryRowContext(ctx, "SELECT name FROM workspaces WHERE id = ?", workspace.ID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != workspace.Name {
		t.Fatalf("restored workspace name=%q, want %q", name, workspace.Name)
	}
}

func TestDqliteRecoveryChangesTopologyAfterDirectoryRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	oldNetwork := newTestNetwork(t, 3)
	oldConnections := oldNetwork.Connections()
	oldAddresses := connectionAddresses(oldConnections)
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source-cluster")
	directories := []string{filepath.Join(sourceRoot, "node-a"), filepath.Join(sourceRoot, "node-b"), filepath.Join(sourceRoot, "node-c")}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	databaseName := fmt.Sprintf("topology_restore_%d", time.Now().UnixNano())
	first, err := adapter.Open(ctx, storeConfig(oldConnections[0], directories[0], databaseName, nil))
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Open(ctx, storeConfig(oldConnections[1], directories[1], databaseName, []string{oldAddresses[0]}))
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	third, err := adapter.Open(ctx, storeConfig(oldConnections[2], directories[2], databaseName, []string{oldAddresses[0]}))
	if err != nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal(err)
	}
	waitForQuorum(t, ctx, first)
	workspace := domain.Workspace{ID: "T-topology-restore", Domain: "topology.example.test", Name: "topology restored"}
	if err := first.SeedWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	leader, err := client.New(ctx, oldAddresses[0], client.WithDialFunc(oldConnections[0].Dial))
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := leader.Cluster(ctx)
	closeLeaderErr := leader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeLeaderErr != nil {
		t.Fatal(closeLeaderErr)
	}
	if len(cluster) != 3 {
		t.Fatalf("cluster members=%d, want 3", len(cluster))
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := oldNetwork.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err := lifecycle.NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{15}, 32), bytes.Repeat([]byte{16}, 32), "topology-key", 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := lifecycle.NewDirectorySnapshotter(manager, sourceRoot, filepath.Join(root, "restored-cluster"), lifecycle.Manifest{Backend: "dqlite", SchemaVersion: 1}, lifecycle.DirectorySnapshotSourceStopped)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := snapshotter.Create(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Restore(ctx, manifest); err != nil {
		t.Fatal(err)
	}

	memberByAddress := make(map[string]client.NodeInfo, len(cluster))
	for _, member := range cluster {
		memberByAddress[member.Address] = member
	}
	restoredRoot := filepath.Join(root, "restored-cluster")
	newNetwork := newTestNetwork(t, 3)
	newConnections := newNetwork.Connections()
	newAddresses := connectionAddresses(newConnections)
	recoveryNodes := make([]adapter.RecoveryNode, 0, len(directories))
	for i, oldAddress := range oldAddresses {
		member, ok := memberByAddress[oldAddress]
		if !ok {
			t.Fatalf("cluster did not return original member %q", oldAddress)
		}
		directory := filepath.Join(restoredRoot, filepath.Base(directories[i]))
		recoveryNodes = append(recoveryNodes, adapter.RecoveryNode{Directory: directory, ID: member.ID, Address: newAddresses[i], Role: member.Role})
	}
	if err := adapter.RecoverTopology(ctx, recoveryNodes); err != nil {
		t.Fatal(err)
	}

	apps := make([]*app.App, 0, len(recoveryNodes))
	for i, node := range recoveryNodes {
		instance, err := app.New(node.Directory, app.WithAddress(node.Address), newConnections[i].Option(), app.WithVoters(3))
		if err != nil {
			t.Fatal(err)
		}
		newConnections[i].Activate()
		apps = append(apps, instance)
		defer func(connection dqlitetest.Connection, application *app.App) {
			_ = closeDqliteApp(connection, application)
		}(newConnections[i], instance)
	}
	for _, instance := range apps {
		if err := instance.Ready(ctx); err != nil {
			t.Fatal(err)
		}
	}
	database, err := apps[1].Open(ctx, databaseName)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var name string
	if err := database.QueryRowContext(ctx, "SELECT name FROM workspaces WHERE id = ?", workspace.ID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != workspace.Name {
		t.Fatalf("workspace name=%q, want %q", name, workspace.Name)
	}
}

func newTestNetwork(t *testing.T, size int) *dqlitetest.Network {
	t.Helper()
	network, err := dqlitetest.NewNetwork(size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = network.Close() })
	return network
}

func connectionAddresses(connections []dqlitetest.Connection) []string {
	addresses := make([]string, len(connections))
	for i, connection := range connections {
		addresses[i] = connection.Address
	}
	return addresses
}

func storeConfig(connection dqlitetest.Connection, directory, database string, cluster []string) adapter.Config {
	return adapter.Config{
		Directory:      directory,
		Address:        connection.Address,
		Cluster:        cluster,
		Database:       database,
		ExternalDial:   connection.Dial,
		ExternalAccept: connection.Accept,
		ExternalReady:  connection.Activate,
		ExternalClose:  connection.Deactivate,
	}
}

func closeDqliteApp(connection dqlitetest.Connection, application *app.App) error {
	return errors.Join(connection.Deactivate(), application.Close())
}

func openDatabase(t *testing.T, ctx context.Context, node *app.App, name string) *sql.DB {
	t.Helper()
	database, err := node.Open(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return database
}
