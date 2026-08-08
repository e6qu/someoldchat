package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
// So evidence is now typed, and every kind resolves to something on disk. A Web
// API test cited as operation evidence must also name the method it is cited
// for, because existence alone let a row point at any function that happened to
// compile — and two rows did, both naming the scope-enforcement table, which
// proves scope handling and says nothing about the method.
//
// What this still does NOT claim is that the target proves the method: no gate
// can read a test and decide whether it establishes a Slack contract. It closes
// the mechanical half — deletions, renames, moves, mislabelled kinds, citations
// that were never about the method — and leaves the judgement half where it
// belongs, with the reviewer. Saying which half is covered is the point; a
// checker that implied more would be the same overstated claim in a new place.

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
	testBodies     map[string]map[string]string
	browserID      map[string]bool
	browserScanned bool
}

func newEvidenceResolver(root string) *evidenceResolver {
	return &evidenceResolver{root: root, testFuncs: map[string]map[string]bool{}, testBodies: map[string]map[string]string{}, browserID: map[string]bool{}}
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
		exercises, err := r.testMentionsMethod(pkg, name, method, auditing)
		if err != nil {
			return fmt.Errorf("compatibility evidence %q for %q: %w", entry, method, err)
		}
		if !exercises {
			return fmt.Errorf("compatibility evidence %q for %q names a Web API test that never mentions %q; cite the test that calls the method", entry, method, method)
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

// testMentionsMethod requires a Web API test to name the method it is cited for.
//
// The existence check above proves a function is there; it cannot prove the
// function has anything to do with the method. With 219 rows to evidence at
// once that gap is the whole risk: pointing every row at some test that happens
// to compile would pass the gate and mean nothing. Requiring the named
// function's own body to mention the method - as a literal or as its /api/
// route - closes the cheapest way to be wrong.
//
// The rule applies only to tests under internal/api/slack, because only those
// speak in method names. A cross-profile contract exercises store methods and a
// service test exercises service methods; neither would mention chat.postMessage
// and neither should have to. This still does not decide whether the test
// establishes the contract - no gate can read a test and judge that - so the
// reviewer's half is unchanged. What it removes is the citation that was never
// about the method at all.
func (r *evidenceResolver) testMentionsMethod(pkg, name, method string, auditing bool) (bool, error) {
	// A downgrade audit cites what showed the claim was overstated, which is
	// often a test about the behaviour rather than about the method. That is
	// the looser rule this resolver already draws, and this check keeps to the
	// same side of it.
	if auditing {
		return true, nil
	}
	if !strings.HasPrefix(filepath.ToSlash(pkg), "internal/api/slack") {
		return true, nil
	}
	if !strings.Contains(method, ".") {
		return true, nil
	}
	bodies, cached := r.testBodies[pkg]
	if !cached {
		bodies = map[string]string{}
		directory := filepath.Join(r.root, pkg)
		entries, err := os.ReadDir(directory)
		if err != nil {
			return false, fmt.Errorf("package %s cannot be read", pkg)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			body, err := os.ReadFile(path)
			if err != nil {
				return false, err
			}
			fileSet := token.NewFileSet()
			parsed, err := parser.ParseFile(fileSet, path, body, 0)
			if err != nil {
				return false, fmt.Errorf("%s cannot be parsed: %w", path, err)
			}
			for _, declaration := range parsed.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				start := fileSet.Position(function.Body.Pos()).Offset
				end := fileSet.Position(function.Body.End()).Offset
				if start < 0 || end > len(body) || start >= end {
					continue
				}
				bodies[function.Name.Name] = string(body[start:end])
			}
		}
		r.testBodies[pkg] = bodies
	}
	source, ok := bodies[name]
	if !ok {
		return false, nil
	}
	return strings.Contains(source, `"`+method+`"`) || strings.Contains(source, "/api/"+method), nil
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
