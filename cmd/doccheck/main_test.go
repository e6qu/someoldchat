package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckFileRejectsMissingRelativeLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte("[missing](absent.md)"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkFile(path); err == nil || !strings.Contains(err.Error(), "absent.md") {
		t.Fatalf("checkFile() error = %v", err)
	}
}

func TestCheckFileIgnoresExternalAndFragmentLinks(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "README.md")
	if err := os.WriteFile(path, []byte("[external](https://example.test) [section](#section)"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkFile(path); err != nil {
		t.Fatal(err)
	}
}
