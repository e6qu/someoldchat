// Package fuzzcoverage requires every fuzz target in the tree to be run by a
// gate.
//
// A fuzz target that no target runs is not a weaker test than one that runs; it
// is not a test. It sits in the tree looking like coverage, it is counted when
// somebody asks how much fuzzing there is, and it never executes. Eight of the
// twenty-three here were in that state, and one of them failed the moment it was
// run — with a saved crasher already committed beside it, so the failure had
// been found once and then left where nothing would look at it again.
package fuzzcoverage

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	targetPattern = regexp.MustCompile(`(?m)^func (Fuzz[A-Za-z0-9_]*)\(`)
	gatePattern   = regexp.MustCompile(`-fuzz (Fuzz[A-Za-z0-9_]*)\b`)
)

func TestEveryFuzzTargetIsRunByAGate(t *testing.T) {
	root := repoRoot(t)
	declared := fuzzTargetsInTree(t, root)
	if len(declared) == 0 {
		t.Fatal("no fuzz targets were found at all, which means the scan is broken rather than that the tree has none")
	}
	gated := fuzzTargetsInMakefile(t, root)

	var unrun []string
	for _, name := range declared {
		if !gated[name] {
			unrun = append(unrun, name)
		}
	}
	sort.Strings(unrun)
	if len(unrun) > 0 {
		t.Fatalf("%d fuzz targets exist and no gate runs them, so they are not tests:\n  %s\nAdd each to the test-fuzz target.", len(unrun), strings.Join(unrun, "\n  "))
	}

	inTree := map[string]bool{}
	for _, name := range declared {
		inTree[name] = true
	}
	var missing []string
	for name := range gated {
		if !inTree[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the fuzz gate names %d targets that no longer exist, so it is quietly running nothing for them:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

func fuzzTargetsInTree(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".cache", "bin", "dist", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range targetPattern.FindAllStringSubmatch(string(source), -1) {
			names = append(names, match[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func fuzzTargetsInMakefile(t *testing.T, root string) map[string]bool {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	gated := map[string]bool{}
	for _, match := range gatePattern.FindAllStringSubmatch(string(source), -1) {
		gated[match[1]] = true
	}
	return gated
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
