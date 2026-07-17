//go:build dqlite

package dqlite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/canonical/go-dqlite/v3"
	"github.com/canonical/go-dqlite/v3/client"
	"gopkg.in/yaml.v2"
)

// RecoveryNode describes one stopped dqlite state directory and its desired
// identity in the recovered cluster.
type RecoveryNode struct {
	Directory string
	ID        uint64
	Address   string
	Role      client.NodeRole
}

// RecoverTopology applies Canonical's documented stopped-node recovery
// procedure. It selects the node with the newest persistent Raft entry,
// reconfigures membership exactly once on that node, and copies its data to
// every recovered node without copying metadata1 or metadata2.
//
// The caller must stop every node before calling this function. The dqlite
// binding cannot safely determine whether another process is serving a state
// directory, so the stopped requirement is explicit at this boundary.
func RecoverTopology(ctx context.Context, nodes []RecoveryNode) (returnErr error) {
	if err := validateRecoveryNodes(nodes); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	entries := make([]dqlite.LastEntryInfo, len(nodes))
	for i, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := dqlite.ReadLastEntryInfo(node.Directory)
		if err != nil {
			return fmt.Errorf("read last entry for %q: %w", node.Directory, err)
		}
		entries[i] = entry
	}
	template := newestRecoveryNode(nodes, entries)
	cluster := make([]client.NodeInfo, len(nodes))
	for i, node := range nodes {
		cluster[i] = client.NodeInfo{ID: node.ID, Address: node.Address, Role: node.Role}
	}
	if err := dqlite.ReconfigureMembershipExt(template.Directory, cluster); err != nil {
		return fmt.Errorf("reconfigure dqlite membership using %q: %w", template.Directory, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	stages := make([]string, len(nodes))
	cleanupStages := true
	defer func() {
		if cleanupStages {
			for _, stage := range stages {
				if stage != "" {
					if err := os.RemoveAll(stage); err != nil {
						returnErr = errors.Join(returnErr, fmt.Errorf("remove recovery stage %q: %w", stage, err))
					}
				}
			}
		}
	}()
	for i, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		stage, err := stageRecoveryNode(node.Directory)
		if err != nil {
			return err
		}
		stages[i] = stage
		preserveMetadata := node.Directory == template.Directory
		if err := copyRecoveryData(template.Directory, stage, preserveMetadata); err != nil {
			return fmt.Errorf("stage recovered data for %q: %w", node.Directory, err)
		}
		if err := writeRecoveryMetadata(stage, cluster, cluster[i]); err != nil {
			return fmt.Errorf("write recovered metadata for %q: %w", node.Directory, err)
		}
		if err := syncRecoveryDirectory(stage); err != nil {
			return fmt.Errorf("sync staged recovered node %q: %w", node.Directory, err)
		}
	}
	for i, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := replaceRecoveryNode(stages[i], node.Directory); err != nil {
			return fmt.Errorf("replace recovered node %q: %w", node.Directory, err)
		}
		stages[i] = ""
	}
	cleanupStages = false
	return nil
}

func validateRecoveryNodes(nodes []RecoveryNode) error {
	if len(nodes) == 0 {
		return errors.New("dqlite recovery requires at least one node")
	}
	seenDirectories := make(map[string]struct{}, len(nodes))
	seenIDs := make(map[uint64]struct{}, len(nodes))
	seenAddresses := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if strings.TrimSpace(node.Directory) == "" || !filepath.IsAbs(node.Directory) {
			return errors.New("dqlite recovery node directories must be absolute")
		}
		if node.ID == 0 || strings.TrimSpace(node.Address) == "" {
			return errors.New("dqlite recovery nodes require non-zero IDs and addresses")
		}
		if node.Role != client.Voter && node.Role != client.StandBy && node.Role != client.Spare {
			return fmt.Errorf("dqlite recovery node %d has an unknown role", node.ID)
		}
		if _, exists := seenDirectories[node.Directory]; exists {
			return fmt.Errorf("dqlite recovery directory %q is duplicated", node.Directory)
		}
		if _, exists := seenIDs[node.ID]; exists {
			return fmt.Errorf("dqlite recovery node ID %d is duplicated", node.ID)
		}
		if _, exists := seenAddresses[node.Address]; exists {
			return fmt.Errorf("dqlite recovery address %q is duplicated", node.Address)
		}
		seenDirectories[node.Directory] = struct{}{}
		seenIDs[node.ID] = struct{}{}
		seenAddresses[node.Address] = struct{}{}
	}
	return nil
}

func newestRecoveryNode(nodes []RecoveryNode, entries []dqlite.LastEntryInfo) RecoveryNode {
	newest := 0
	for i := 1; i < len(nodes); i++ {
		if entries[newest].Before(entries[i]) || (entries[newest] == entries[i] && nodes[i].Directory < nodes[newest].Directory) {
			newest = i
		}
	}
	return nodes[newest]
}

func stageRecoveryNode(destination string) (string, error) {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, ".dqlite-recovery-")
}

func copyRecoveryData(source, destination string, preserveMetadata bool) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		metadataFile := entry.Name() == "metadata1" || entry.Name() == "metadata2"
		if (!preserveMetadata && metadataFile) || entry.Name() == "cluster.yaml" || entry.Name() == "info.yaml" || entry.Name() == "join" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("state directory contains unsupported entry %q", path)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyRecoveryFile(path, target, entry)
	})
}

func copyRecoveryFile(source, destination string, entry os.DirEntry) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	info, err := entry.Info()
	if err != nil {
		return errors.Join(err, input.Close())
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return errors.Join(err, input.Close())
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	inputCloseErr := input.Close()
	return errors.Join(copyErr, syncErr, closeErr, inputCloseErr)
}

func writeRecoveryMetadata(directory string, cluster []client.NodeInfo, node client.NodeInfo) error {
	clusterBytes, err := yaml.Marshal(cluster)
	if err != nil {
		return err
	}
	nodeBytes, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	if err := writeRecoveryFile(filepath.Join(directory, "cluster.yaml"), clusterBytes); err != nil {
		return err
	}
	return writeRecoveryFile(filepath.Join(directory, "info.yaml"), nodeBytes)
}

func writeRecoveryFile(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dqlite-recovery-metadata-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	cleanup := func(cause error) error {
		var cleanupErr error
		if !closed {
			cleanupErr = temporary.Close()
			closed = true
		}
		cleanupErr = errors.Join(cleanupErr, os.Remove(temporaryPath))
		return errors.Join(cause, cleanupErr)
	}
	if err := temporary.Chmod(0o600); err != nil {
		return cleanup(err)
	}
	if _, err := temporary.Write(data); err != nil {
		return cleanup(err)
	}
	if err := temporary.Sync(); err != nil {
		return cleanup(err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return errors.Join(err, os.Remove(temporaryPath))
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.Join(err, os.Remove(temporaryPath))
	}
	return nil
}

func syncRecoveryDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func replaceRecoveryNode(source, destination string) error {
	backup := destination + ".previous"
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		return errors.Join(err, os.Rename(backup, destination))
	}
	return os.RemoveAll(backup)
}
