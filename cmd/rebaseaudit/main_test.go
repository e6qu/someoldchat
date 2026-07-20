package main

import (
	"strings"
	"testing"
)

const parentSource = `package sample

type Store struct {
	db int
}

func (s *Store) Save(value string) error {
	return nil
}

func helper() string {
	return "parent"
}
`

// The branch modifies Save, leaves helper alone, and adds Load.
const branchSource = `package sample

type Store struct {
	db int
}

func (s *Store) Save(value string) error {
	if value == "" {
		return nil
	}
	return nil
}

func helper() string {
	return "parent"
}

func (s *Store) Load() (string, error) {
	return "", nil
}
`

func declsOrFail(t *testing.T, source string) map[string]string {
	t.Helper()
	result, err := declarations("sample.go", source)
	if err != nil {
		t.Fatalf("declarations: %v", err)
	}
	return result
}

func TestDeclarationsKeyMethodsByReceiverSoSameNamedMethodsStayDistinct(t *testing.T) {
	const source = `package sample

type A struct{}
type B struct{}

func (a *A) Read() string { return "a" }
func (b *B) Read() string { return "b" }
`
	decls := declsOrFail(t, source)
	first, firstOK := decls["method (A).Read"]
	second, secondOK := decls["method (B).Read"]
	if !firstOK || !secondOK {
		t.Fatalf("receiver-qualified keys missing: %v", keys(decls))
	}
	if first == second {
		t.Fatal("methods on different receivers collapsed to one entry")
	}
}

func TestDeclarationsIgnoresFormattingSoReindentationIsNotAChange(t *testing.T) {
	spaced := "package sample\n\nfunc helper() string {\n\treturn \"x\"\n}\n"
	crowded := "package sample\n\nfunc helper() string {\n\n\treturn \"x\"\n\n}\n"
	if declsOrFail(t, spaced)["func helper"] != declsOrFail(t, crowded)["func helper"] {
		t.Fatal("whitespace-only difference reported as a change")
	}
}

func TestDeclarationsIndexesTypesConstsAndVars(t *testing.T) {
	const source = `package sample

type Thing struct{}

const Limit = 10

var Sentinel = "x"
`
	decls := declsOrFail(t, source)
	for _, want := range []string{"type Thing", "const Limit", "var Sentinel"} {
		if _, ok := decls[want]; !ok {
			t.Fatalf("missing %q; got %v", want, keys(decls))
		}
	}
}

// The regression this command exists to catch: a rebase that keeps the base
// side of a conflict silently reverts the branch's change.
func TestStaleIsReportedWhenTargetStillHoldsTheParentBody(t *testing.T) {
	parent := declsOrFail(t, parentSource)
	branch := declsOrFail(t, branchSource)
	target := parent // rebase discarded the branch entirely

	findings := compare(parent, branch, target)
	if got := findingFor(findings, "method (Store).Save"); got != "stale" {
		t.Fatalf("modified declaration reported as %q, want stale", got)
	}
	if got := findingFor(findings, "method (Store).Load"); got != "missing" {
		t.Fatalf("added declaration reported as %q, want missing", got)
	}
	if got := findingFor(findings, "func helper"); got != "" {
		t.Fatalf("untouched declaration reported as %q, want no finding", got)
	}
}

func TestNoFindingsWhenTargetCarriesTheBranchWork(t *testing.T) {
	parent := declsOrFail(t, parentSource)
	branch := declsOrFail(t, branchSource)
	if findings := compare(parent, branch, branch); len(findings) != 0 {
		t.Fatalf("clean rebase reported %v", findings)
	}
}

// A declaration the branch changed and the target changed differently is a real
// conflict a human resolved; reporting it as lost would be a false positive.
func TestConcurrentChangeIsNotReportedAsLost(t *testing.T) {
	parent := declsOrFail(t, parentSource)
	branch := declsOrFail(t, branchSource)
	target := declsOrFail(t, strings.Replace(branchSource, `return "", nil`, `return "merged", nil`, 1))
	for _, item := range compare(parent, branch, target) {
		if item.Decl == "method (Store).Save" && item.Kind == "stale" {
			t.Fatal("independently merged declaration reported as stale")
		}
	}
}

// compare mirrors auditFile's decision table without touching git, so the
// classification rules can be exercised directly.
func compare(parent, branch, target map[string]string) []finding {
	findings := make([]finding, 0)
	for key, branchBody := range branch {
		parentBody, inParent := parent[key]
		if inParent && parentBody == branchBody {
			continue
		}
		targetBody, exists := target[key]
		switch {
		case !exists:
			findings = append(findings, finding{Kind: "missing", Decl: key})
		case targetBody != branchBody && inParent && targetBody == parentBody:
			findings = append(findings, finding{Kind: "stale", Decl: key})
		}
	}
	return findings
}

func findingFor(findings []finding, decl string) string {
	for _, item := range findings {
		if item.Decl == decl {
			return item.Kind
		}
	}
	return ""
}

func keys(m map[string]string) []string {
	result := make([]string, 0, len(m))
	for key := range m {
		result = append(result, key)
	}
	return result
}
