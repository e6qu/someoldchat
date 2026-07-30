package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyCatalogRejectsUnknownBrowserEvidence(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog")
	browser := filepath.Join(root, "browser")
	if err := os.MkdirAll(catalog, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(browser, 0o755); err != nil {
		t.Fatal(err)
	}
	document := "# Area\n\n## REAL-01 — Real journey\n\nSources checked 2026-07-29:\n\n- https://slack.com/help/example\n"
	if err := os.WriteFile(filepath.Join(catalog, "00-area.md"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browser, "area.spec.mjs"), []byte("test('[FAKE-01] behavior', () => {});"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyCatalog(catalog, browser)
	if err == nil || !strings.Contains(err.Error(), "unknown journey ID FAKE-01") {
		t.Fatalf("error = %v, want unknown journey evidence", err)
	}
}

func TestVerifyCatalogRequiresDatedOfficialSource(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog")
	browser := filepath.Join(root, "browser")
	if err := os.MkdirAll(catalog, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(browser, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalog, "00-area.md"), []byte("# Area\n\n## REAL-01 — Real journey\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browser, "area.spec.mjs"), []byte("test('[REAL-01] behavior', () => {});"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyCatalog(catalog, browser)
	if err == nil || !strings.Contains(err.Error(), "Sources checked") {
		t.Fatalf("error = %v, want missing source date", err)
	}
}
