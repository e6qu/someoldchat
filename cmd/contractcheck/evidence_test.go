package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evidenceRoot builds a miniature repository so the validator is tested against
// its own rules rather than against whatever the real ledger happens to contain
// today. A rule that only ever runs on data that already satisfies it is not a
// rule that has been tested.
func evidenceRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/example/thing_test.go", "package example\n\nfunc TestRealThing(t *testing.T) {}\n\nfunc helper(value string) {}\n")
	write("tests/example/contract_test.go", "package example\n\nfunc crossProfileContract(t *testing.T, open opener) {}\n")
	write("tests/browser/specs/workspace.spec.mjs", "test('[REAL-01 A11Y-01] a real journey', async () => {});\n")
	write("tests/official-sdk-qualification/node/qualification.mjs", "// walk\n")
	write("specs/journeys/00-real.md", "# Real\n")
	write("internal/example/thing.go", "package example\n")
	return root
}

func TestEvidenceResolvesEveryKindItAccepts(t *testing.T) {
	resolver := newEvidenceResolver(evidenceRoot(t))
	for _, entry := range []string{
		"go-test:internal/example:TestRealThing",
		"official-sdk:tests/official-sdk-qualification/node/qualification.mjs",
		"journey:specs/journeys/00-real.md",
		"https://docs.slack.dev/reference/methods/chat.postMessage/",
		"https://slack.com/help/articles/201457107-Send-and-read-messages",
	} {
		if err := resolver.validate("chat.postMessage", entry, false); err != nil {
			t.Fatalf("validate(%q) = %v, want accepted", entry, err)
		}
	}
}

// A cross-profile contract is a function taking *testing.T that is driven by one
// table-driven runner rather than a TestXxx the go tool finds on its own. Those
// contracts are the whole evidence for several storage claims, so a rule that
// only recognised TestXxx would have forced them back to naming a file.
func TestEvidenceAcceptsAContractFunctionThatIsNotNamedTest(t *testing.T) {
	resolver := newEvidenceResolver(evidenceRoot(t))
	if err := resolver.validate("chat.postMessage", "go-test:tests/example:crossProfileContract", false); err != nil {
		t.Fatalf("cross-profile contract rejected: %v", err)
	}
	// A function that takes no *testing.T is not a contract, whatever it is
	// named. Accepting one would let any helper stand in as evidence.
	if err := resolver.validate("chat.postMessage", "go-test:internal/example:helper", false); err == nil {
		t.Fatal("a non-test helper was accepted as evidence")
	}
}

// The whole point: an entry naming something that does not exist must fail,
// because that is the state the ledger silently reached before this existed.
func TestEvidenceRefusesTargetsThatDoNotExist(t *testing.T) {
	resolver := newEvidenceResolver(evidenceRoot(t))
	for name, entry := range map[string]string{
		"renamed test":        "go-test:internal/example:TestRenamedAway",
		"missing package":     "go-test:internal/absent:TestRealThing",
		"deleted spec":        "journey:specs/journeys/99-gone.md",
		"moved sdk walk":      "official-sdk:tests/official-sdk-qualification/node/moved.mjs",
		"uncited journey":     "browser-journey:NEVER-01",
		"untyped entry":       "internal/example/thing_test.go",
		"unknown kind":        "vibes:internal/example/thing_test.go",
		"malformed go-test":   "go-test:internal/example",
		"non-Slack reference": "https://example.test/reference/methods/chat.postMessage/",
	} {
		if err := resolver.validate("chat.postMessage", entry, false); err == nil {
			t.Fatalf("%s: validate(%q) was accepted", name, entry)
		}
	}
}

// A journey ID counts only where the browser suite actually cites it, which is
// the same place journeycheck reads its citations from.
func TestEvidenceReadsBrowserCitationsFromScenarioNames(t *testing.T) {
	resolver := newEvidenceResolver(evidenceRoot(t))
	if err := resolver.validate("chat.postMessage", "browser-journey:REAL-01", false); err != nil {
		t.Fatalf("a cited journey was rejected: %v", err)
	}
	// The bracket carries several IDs; every one of them is a citation.
	if err := resolver.validate("chat.postMessage", "browser-journey:A11Y-01", false); err != nil {
		t.Fatalf("a second ID in the same citation was rejected: %v", err)
	}
}

// Implementation is admissible only where it means something: a downgrade audit
// records that the code does not support the claim that was made. As operation
// evidence it would let a method prove itself with the thing being judged.
func TestEvidenceAdmitsImplementationOnlyInADowngradeAudit(t *testing.T) {
	resolver := newEvidenceResolver(evidenceRoot(t))
	entry := "source:internal/example/thing.go"
	if err := resolver.validate("users.setPresence", entry, true); err != nil {
		t.Fatalf("a downgrade audit could not cite implementation: %v", err)
	}
	err := resolver.validate("users.setPresence", entry, false)
	if err == nil {
		t.Fatal("operation evidence cited implementation")
	}
	if !strings.Contains(err.Error(), "evidence for itself") {
		t.Fatalf("error = %v, want it to name the reason", err)
	}
}

// A kind that points outside its own tree is mislabelled, and a mislabelled
// entry reads as stronger evidence than it is: "official-sdk" on a unit test
// claims a pinned client issued the call.
func TestEvidenceRefusesAKindPointingOutsideItsTree(t *testing.T) {
	resolver := newEvidenceResolver(evidenceRoot(t))
	if err := resolver.validate("chat.postMessage", "official-sdk:internal/example/thing_test.go", false); err == nil {
		t.Fatal("an official-sdk entry pointing at a unit test was accepted")
	}
	if err := resolver.validate("chat.postMessage", "journey:tests/official-sdk-qualification/node/qualification.mjs", false); err == nil {
		t.Fatal("a journey entry pointing outside specs/journeys was accepted")
	}
}
