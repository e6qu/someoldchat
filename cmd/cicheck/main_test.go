package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckWorkflowAcceptsPullRequestWorkflowWithSHAAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ci.yml")
	content := "on:\n  pull_request:\nsteps:\n  - uses: actions/checkout@0123456789abcdef0123456789abcdef01234567\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkWorkflow(path); err != nil {
		t.Fatal(err)
	}
}

func TestCheckWorkflowRejectsPushTriggerAndMutableAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ci.yml")
	content := "on:\n  push:\n  pull_request:\nsteps:\n  - uses: actions/checkout@main\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkWorkflow(path); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("checkWorkflow() error = %v", err)
	}
}
