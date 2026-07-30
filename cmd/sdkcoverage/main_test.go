package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareCoverageRejectsUnobservedClaim(t *testing.T) {
	root := t.TempDir()
	inventory := filepath.Join(root, "compatibility.yaml")
	log := filepath.Join(root, "coverage")
	if err := os.WriteFile(inventory, []byte("operations:\n  - method: api.test\n    status: sdk-compatible\n  - method: chat.postMessage\n    status: behavior-compatible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte("api.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := compareCoverage(log, inventory, true)
	if err == nil || !strings.Contains(err.Error(), "1 SDK-compatible-or-better") {
		t.Fatalf("error = %v, want missing claimed method", err)
	}
}

func TestCompareCoverageRejectsUntrackedMethod(t *testing.T) {
	root := t.TempDir()
	inventory := filepath.Join(root, "compatibility.yaml")
	log := filepath.Join(root, "coverage")
	if err := os.WriteFile(inventory, []byte("operations:\n  - method: api.test\n    status: sdk-compatible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte("future.method\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := compareCoverage(log, inventory, false)
	if err == nil || !strings.Contains(err.Error(), "untracked method") {
		t.Fatalf("error = %v, want untracked method", err)
	}
}
