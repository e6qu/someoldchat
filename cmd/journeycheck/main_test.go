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
	document := `# Area

## REAL-01 — Real journey

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| REAL-01 | [Real behavior](https://slack.com/help/example) | Slack exposes the real journey. |

Sources checked 2026-07-29:

- https://slack.com/help/example
`
	if err := os.WriteFile(filepath.Join(catalog, "00-area.md"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browser, "area.spec.mjs"), []byte("test('[FAKE-01] behavior', () => {});"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyCatalog(catalog, browser, -1)
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
	err := verifyCatalog(catalog, browser, -1)
	if err == nil || !strings.Contains(err.Error(), "Sources checked") {
		t.Fatalf("error = %v, want missing source date", err)
	}
}

func TestVerifyCatalogRequiresPerJourneyOfficialSourceMapping(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(browser, "area.spec.mjs"), []byte("test('[REAL-01] behavior', () => {});"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyCatalog(catalog, browser, -1)
	if err == nil || !strings.Contains(err.Error(), "no journey-source map row for REAL-01") {
		t.Fatalf("error = %v, want missing per-journey source mapping", err)
	}
}

func TestOfficialSlackURLRejectsLookalikeAndEmbeddedHosts(t *testing.T) {
	for _, raw := range []string{
		"https://slack.com/help/example",
		"https://docs.slack.dev/reference/methods/chat.postMessage/",
		"https://api.slack.com/methods/chat.postMessage",
	} {
		if !isOfficialSlackURL(raw) {
			t.Errorf("official URL rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		"http://slack.com/help/example",
		"https://slack.com.example.test/help/example",
		"https://customer-workspace.slack.com/archives/C123",
		"https://example.test/redirect?next=https://slack.com/help/example",
		"https://slack.com:444/help/example",
	} {
		if isOfficialSlackURL(raw) {
			t.Errorf("non-official URL accepted: %s", raw)
		}
	}
}

// The ceiling is the whole point of the change that added it, so it is checked
// in both directions rather than trusted: a gap that grows is a journey that
// lost its citation, and a gap that shrinks without the ceiling moving is
// coverage left as slack for whoever next needs room.
func TestBrowserGapCeilingHoldsInBothDirections(t *testing.T) {
	if err := checkBrowserGapCeiling(9, 9); err != nil {
		t.Fatalf("a backlog at its ceiling failed: %v", err)
	}
	if err := checkBrowserGapCeiling(10, 9); err == nil {
		t.Fatal("a backlog above its ceiling passed")
	}
	if err := checkBrowserGapCeiling(8, 9); err == nil {
		t.Fatal("a backlog below its ceiling passed, so ground gained can be given back")
	}
	// A negative ceiling is how the unit tests build small catalogs without
	// asserting a number that only means something for the real one.
	if err := checkBrowserGapCeiling(3, -1); err != nil {
		t.Fatalf("an unasserted ceiling failed: %v", err)
	}
}

func TestVerifyCatalogRejectsUnknownExternalEvidence(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog")
	browser := filepath.Join(root, "browser")
	if err := os.MkdirAll(catalog, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(browser, 0o755); err != nil {
		t.Fatal(err)
	}
	document := `# Area

## REAL-01 — Real journey

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| REAL-01 | [Real behavior](https://slack.com/help/example) | Slack exposes the real journey. |

Sources checked 2026-07-29:
`
	if err := os.WriteFile(filepath.Join(catalog, "00-area.md"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browser, "area.spec.mjs"), []byte("test('[REAL-01] behavior', () => {});"), 0o644); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external.sh")
	if err := os.WriteFile(external, []byte("assert '[FAKE-01] unsupported claim'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyCatalog(catalog, browser, -1, external)
	if err == nil || !strings.Contains(err.Error(), "unknown journey ID FAKE-01") {
		t.Fatalf("error = %v, want unknown external journey evidence", err)
	}
}
