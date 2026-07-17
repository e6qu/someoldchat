package main

import (
	"os"
	"strings"
	"testing"
)

func qualifiedSDK(t *testing.T, suitePath string) sdk {
	t.Helper()
	return sdk{
		ID:        "example-sdk",
		Ecosystem: "npm",
		Package:   "@example/sdk",
		Upstream:  "https://example.com/sdk",
		Release:   "1.0.0",
		Revision:  "0123456789abcdef0123456789abcdef01234567",
		Artifact:  "example-sdk-1.0.0.tgz",
		SHA256:    strings.Repeat("a", 64),
		License:   "MIT",
		Suite:     "passed",
		SuitePath: suitePath,
	}
}

func TestValidateInventoryRequiresRecordedSuiteFiles(t *testing.T) {
	suite := t.TempDir() + "/qualification.test"
	if err := os.WriteFile(suite, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	value := inventory{
		Version:     1,
		Project:     "example",
		RetrievedAt: "2026-07-17T00:00:00Z",
		Status:      "qualified",
		SDKs:        []sdk{qualifiedSDK(t, suite)},
		Bolt:        []sdk{func() sdk { item := qualifiedSDK(t, suite); item.ID = "example-bolt"; return item }()},
	}
	if err := validateInventory(value, true); err != nil {
		t.Fatalf("validateInventory() error = %v", err)
	}

	value.SDKs[0].SuitePath = suite + ".missing"
	if err := validateInventory(value, true); err == nil {
		t.Fatal("validateInventory accepted an unavailable qualification suite")
	}
}

func TestValidateInventoryRejectsIncompleteImmutableFields(t *testing.T) {
	value := inventory{
		Version:     1,
		Project:     "example",
		RetrievedAt: "2026-07-17T00:00:00Z",
		Status:      "pending",
		SDKs: []sdk{{
			ID: "example-sdk", Ecosystem: "npm", Package: "@example/sdk", Upstream: "https://example.com/sdk", Release: "1.0.0", Revision: "revision", Artifact: "example.tgz", SHA256: strings.Repeat("a", 64), License: "MIT", Suite: "pending",
		}},
		Bolt: []sdk{{
			ID: "example-bolt", Ecosystem: "npm", Package: "@example/bolt", Upstream: "https://example.com/bolt", Release: "1.0.0", Revision: "revision", Artifact: "example-bolt.tgz", SHA256: "not-a-digest", License: "MIT", Suite: "pending",
		}},
	}
	if err := validateInventory(value, false); err == nil {
		t.Fatal("validateInventory accepted an invalid SHA-256 digest")
	}
}
