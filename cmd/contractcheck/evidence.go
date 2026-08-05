package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Compatibility evidence has to name something that exists.
//
// Until now an `evidence:` entry was any non-empty string, and the report
// printed how many operations carried one as though that number meant a method
// had been individually qualified. It measured how many rows had prose in a
// field. Two vocabularies had grown side by side, several entries pointed at
// files that had moved, and nothing would have noticed if a named test were
// renamed or deleted — which is the same silent-staleness failure that made the
// audit's method count and journey count wrong for several releases.
//
// So evidence is now typed, and every kind resolves to something on disk. What
// this does NOT claim is that the target proves the method: no gate can read a
// test and decide whether it establishes a Slack contract. It closes the
// mechanical half — deletions, renames, moves, mislabelled kinds — and leaves
// the judgement half where it belongs, with the reviewer. Saying which half is
// covered is the point; a checker that implied more would be the same overstated
// claim in a new place.

// evidenceKinds maps each kind to the directory its target must live under. An
// empty prefix means the kind is resolved by lookup rather than by path.
var evidenceKinds = map[string]string{
	// A Go test or cross-profile contract function. The most precise kind: it
	// survives a file being split or renamed and fails when the function it
	// names stops existing.
	"go-test": "",
	// A journey ID cited by the Playwright suite.
	"browser-journey": "",
	// A walk in one of the pinned official SDKs. That the SDK really issued
	// the method is separately fail-closed: `sdkcoverage -require-claimed`
	// refuses a row claiming sdk-compatible or above that no SDK requested.
	"official-sdk": "tests/official-sdk-qualification",
	// A normative journey document.
	"journey": "specs/journeys",
	// The pinned third-party contract walk.
	"external-contract": "tests/external-contract-qualification",
	// A cross-profile persistence contract file, for evidence that is about a
	// storage profile agreeing rather than about one function.
	"persistence": "tests/persistence-qualification",
	// Implementation, admissible only in a downgrade audit. "Here is the code
	// that shows the claim was overstated" is a real citation for a
	// correction; it is never a qualification, and allowing it as operation
	// evidence would let a row prove itself with the thing being judged.
	"source": "",
}

var (
	goFunc        = regexp.MustCompile(`(?m)^func\s+(\w+)\s*\(([^)]*)\)`)
	journeyIDPart = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)+$`)
)

// evidenceResolver answers whether a target exists. It caches what it reads
// because the ledger names the same few packages and spec files hundreds of
// times, and re-reading the browser suite for each of them turns a gate that
// runs in milliseconds into one that does not.
type evidenceResolver struct {
	root           string
	testFuncs      map[string]map[string]bool
	browserID      map[string]bool
	browserScanned bool
}

func newEvidenceResolver(root string) *evidenceResolver {
	return &evidenceResolver{root: root, testFuncs: map[string]map[string]bool{}, browserID: map[string]bool{}}
}

// validate checks one entry. auditing selects the looser rule that a downgrade
// audit needs: it may cite implementation, because the correction it records is
// a statement about the code rather than about a test.
func (r *evidenceResolver) validate(method, entry string, auditing bool) error {
	// An upstream citation stays a bare URL. Prefixing it with a kind would
	// read worse and say nothing the scheme does not already say; what matters
	// is that it names an official Slack host, which is the same rule
	// journeycheck applies to journey sources. A citation of some other site is
	// not upstream evidence no matter how it is labelled.
	if strings.HasPrefix(entry, "https://") {
		if !officialSlackSource(entry) {
			return fmt.Errorf("compatibility evidence %q for %q is not an official Slack source", entry, method)
		}
		return nil
	}
	kind, target, found := strings.Cut(entry, ":")
	if !found {
		return fmt.Errorf("compatibility evidence %q for %q is untyped: use one of %s", entry, method, knownEvidenceKinds())
	}
	prefix, known := evidenceKinds[kind]
	if !known {
		return fmt.Errorf("compatibility evidence %q for %q has unknown kind %q: use one of %s", entry, method, kind, knownEvidenceKinds())
	}
	if kind == "source" && !auditing {
		return fmt.Errorf("compatibility evidence %q for %q cites implementation: a method cannot be evidence for itself", entry, method)
	}
	switch kind {
	case "source":
		if info, err := os.Stat(filepath.Join(r.root, filepath.Clean(target))); err != nil || info.IsDir() {
			return fmt.Errorf("compatibility evidence %q for %q names no file", entry, method)
		}
	case "go-test":
		pkg, name, ok := strings.Cut(target, ":")
		if !ok || strings.TrimSpace(pkg) == "" || strings.TrimSpace(name) == "" {
			return fmt.Errorf("compatibility evidence %q for %q must name go-test:<package>:<FuncName>", entry, method)
		}
		present, err := r.hasTestFunc(pkg, name)
		if err != nil {
			return fmt.Errorf("compatibility evidence %q for %q: %w", entry, method, err)
		}
		if !present {
			return fmt.Errorf("compatibility evidence %q for %q names no test function in %s", entry, method, pkg)
		}
	case "browser-journey":
		if !journeyIDPart.MatchString(target) {
			return fmt.Errorf("compatibility evidence %q for %q is not a journey ID", entry, method)
		}
		cited, err := r.browserCites(target)
		if err != nil {
			return fmt.Errorf("compatibility evidence %q for %q: %w", entry, method, err)
		}
		if !cited {
			return fmt.Errorf("compatibility evidence %q for %q names a journey no browser scenario cites", entry, method)
		}
	default:
		clean := filepath.Clean(target)
		if !strings.HasPrefix(clean, prefix+string(filepath.Separator)) {
			return fmt.Errorf("compatibility evidence %q for %q must point inside %s", entry, method, prefix)
		}
		info, err := os.Stat(filepath.Join(r.root, clean))
		if err != nil || info.IsDir() {
			return fmt.Errorf("compatibility evidence %q for %q names no file", entry, method)
		}
	}
	return nil
}

// hasTestFunc accepts any top-level function in the package that takes a
// *testing.T. Cross-profile contracts are written as helpers driven by one
// table-driven runner rather than as TestXxx functions, and they are the whole
// evidence for several storage claims; a rule that only recognised TestXxx
// would have pushed those back to naming a file.
func (r *evidenceResolver) hasTestFunc(pkg, name string) (bool, error) {
	funcs, cached := r.testFuncs[pkg]
	if !cached {
		funcs = map[string]bool{}
		entries, err := os.ReadDir(filepath.Join(r.root, pkg))
		if err != nil {
			return false, fmt.Errorf("package %s cannot be read", pkg)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(r.root, pkg, entry.Name()))
			if err != nil {
				return false, err
			}
			for _, match := range goFunc.FindAllStringSubmatch(string(body), -1) {
				if strings.Contains(match[2], "*testing.T") {
					funcs[match[1]] = true
				}
			}
		}
		r.testFuncs[pkg] = funcs
	}
	return funcs[name], nil
}

func (r *evidenceResolver) browserCites(id string) (bool, error) {
	if !r.browserScanned {
		entries, err := os.ReadDir(filepath.Join(r.root, "tests/browser/specs"))
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".mjs") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(r.root, "tests/browser/specs", entry.Name()))
			if err != nil {
				return false, err
			}
			// Journey IDs are cited inside the bracketed prefix of a scenario
			// name, which is the same place journeycheck reads them from.
			for _, match := range regexp.MustCompile(`\[([A-Z0-9 \-]+)\]`).FindAllStringSubmatch(string(body), -1) {
				for _, cited := range strings.Fields(match[1]) {
					r.browserID[cited] = true
				}
			}
		}
		r.browserScanned = true
	}
	return r.browserID[id], nil
}

// officialSlackSource mirrors journeycheck's host rule. Host equality, not a
// suffix test: "slack.com.example.test" ends with neither, and a redirect
// parameter carrying an official URL is not an official URL.
func officialSlackSource(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	switch parsed.Host {
	case "slack.com", "www.slack.com", "api.slack.com", "docs.slack.dev":
		return true
	}
	return false
}

func knownEvidenceKinds() string {
	kinds := make([]string, 0, len(evidenceKinds))
	for kind := range evidenceKinds {
		kinds = append(kinds, kind)
	}
	sortStrings(kinds)
	return strings.Join(kinds, ", ")
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
