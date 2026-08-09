// Package mutation asks the question a green suite cannot answer about itself:
// if the guard were gone, would anything notice?
//
// Seven functional holes reached main in one stretch of this project, and they
// were not caught by a missing test — they were passed by tests that asserted
// the defect. A test that calls an operation as an administrator and checks it
// succeeds keeps passing when the authorization in front of it is deleted. The
// authorization matrix in tests/authorization was built to close that, and this
// gate is what proves the matrix has teeth: it removes each guard in turn and
// requires the suite to fail.
//
// It is not part of `make check`. Each mutant is a separate compile-and-run of
// the suites below, and there are hundreds. `make test-mutation` runs it,
// check-full includes that, and CI gives it a job of its own — putting it in the
// job that already spends most of its twenty minutes on the race suite would
// cancel a healthy gate rather than report one.
package mutation

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// killers are the packages a removed guard must be noticed by. The matrix is
// the one built for this, and the service and transport suites are included
// because a guard those cover and the matrix does not is still a guard under
// test — the question is whether anything notices, not whether one chosen
// suite does.
var killers = []string{"./tests/authorization", "./internal/service", "./internal/api/slack"}

// guardPrefixes name the authorization helpers. A guard is recognised by the
// shape of its name, which is a real limit: an authorization check written
// under some other name is invisible here. It is stated rather than pretended
// away, and the prefix set widens when one arrives that it misses.
var guardPrefixes = []string{"require", "authorize"}

type site struct {
	file   string
	line   int
	guard  string
	inside string
	start  int
	end    int
}

func (s site) String() string {
	return fmt.Sprintf("%s:%d %s in %s", s.file, s.line, s.guard, s.inside)
}

// operation is every guard standing in front of one function. The mutant
// deletes all of them at once, and that is deliberate.
//
// Deleting guards one at a time answers a different and much weaker question.
// Where a function holds two — DispatchSlashCommand checks workspace
// membership and then conversation membership — removing either leaves the
// other to refuse, so each survives on its own while the operation is in fact
// guarded and asserted. A per-guard sweep reported 127 such survivors and
// called every one of them "an operation whose authorization no test asserts",
// which was measuring redundancy and reporting it as absence.
//
// Deleting the whole set asks the question worth asking: with nothing left in
// front of this operation, does any suite notice?
type operation struct {
	file   string
	name   string
	guards []site
}

func (o operation) String() string {
	names := make([]string, 0, len(o.guards))
	for _, guard := range o.guards {
		names = append(names, fmt.Sprintf("%s:%d %s", o.file, guard.line, guard.guard))
	}
	return o.name + " [" + strings.Join(names, ", ") + "]"
}

// groupByOperation collects the guards of each function, in the order they
// appear, so a mutant can delete them from the bottom up without invalidating
// the offsets above it.
func groupByOperation(sites []site) []operation {
	order := make([]string, 0)
	seen := map[string]*operation{}
	for _, guard := range sites {
		key := guard.file + "\x00" + guard.inside
		existing, ok := seen[key]
		if !ok {
			existing = &operation{file: guard.file, name: guard.inside}
			seen[key] = existing
			order = append(order, key)
		}
		existing.guards = append(existing.guards, guard)
	}
	grouped := make([]operation, 0, len(order))
	for _, key := range order {
		grouped = append(grouped, *seen[key])
	}
	return grouped
}

// findGuardSites returns every `if err := m.<guard>(...); err != nil { ... }`
// in the service package.
//
// Only that form is mutated. A guard called as a plain assignment is counted
// separately and reported, because deleting one would leave its result
// unbound and the mutant would fail to compile — which proves nothing. The
// count is printed rather than dropped silently.
func findGuardSites(t *testing.T, root string) (sites []site, unmutatable int) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "internal", "service", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		var enclosing string
		ast.Inspect(parsed, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				enclosing = node.Name.Name
			case *ast.IfStmt:
				if node.Init == nil {
					return true
				}
				assign, ok := node.Init.(*ast.AssignStmt)
				if !ok {
					return true
				}
				name, ok := guardCallName(assign)
				if !ok {
					return true
				}
				start := fset.Position(node.Pos())
				end := fset.Position(node.End())
				sites = append(sites, site{
					file: relative, line: start.Line, guard: name, inside: enclosing,
					start: start.Offset, end: end.Offset,
				})
			}
			return true
		})
		unmutatable += countPlainGuardAssignments(fset, parsed)
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].file != sites[j].file {
			return sites[i].file < sites[j].file
		}
		return sites[i].line < sites[j].line
	})
	return sites, unmutatable
}

func guardCallName(assign *ast.AssignStmt) (string, bool) {
	if len(assign.Rhs) != 1 {
		return "", false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return "", false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	name := selector.Sel.Name
	for _, prefix := range guardPrefixes {
		if strings.HasPrefix(name, prefix) {
			return name, true
		}
	}
	return "", false
}

// countPlainGuardAssignments counts guard calls that are statements in their
// own right rather than an if-init, which this gate cannot delete.
func countPlainGuardAssignments(fset *token.FileSet, parsed *ast.File) int {
	inIfInit := map[token.Pos]bool{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		if stmt, ok := n.(*ast.IfStmt); ok && stmt.Init != nil {
			inIfInit[stmt.Init.Pos()] = true
		}
		return true
	})
	count := 0
	ast.Inspect(parsed, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || inIfInit[assign.Pos()] {
			return true
		}
		if _, ok := guardCallName(assign); ok {
			count++
		}
		return true
	})
	return count
}

type verdict int

const (
	killed verdict = iota
	survived
	didNotBuild
	harnessBroke
)

// TestEveryAuthorizationGuardIsLoadBearing deletes each guard and requires the
// suite to notice.
func TestEveryAuthorizationGuardIsLoadBearing(t *testing.T) {
	if os.Getenv("SAMEOLDCHAT_MUTATION") == "" {
		t.Skip("set SAMEOLDCHAT_MUTATION=1, or run `make test-mutation`: every guard is a separate compile and suite run")
	}
	root := repoRoot(t)
	found, unmutatable := findGuardSites(t, root)
	if len(found) == 0 {
		t.Fatal("no authorization guards were found at all, which means the scan is broken rather than that the service has none")
	}
	sites := groupByOperation(found)
	t.Logf("%d guards across %d operations; %d guard calls are not if-init form and are not mutated", len(found), len(sites), unmutatable)

	type outcome struct {
		site    operation
		verdict verdict
		detail  string
	}
	results := make([]outcome, len(sites))
	work := make(chan int)
	var wait sync.WaitGroup
	workers := runtime.NumCPU() / 2
	if workers < 1 {
		workers = 1
	}
	// Progress goes to stderr as it happens. Test logs are buffered until the
	// test ends, and a gate that prints nothing for a quarter of an hour is
	// indistinguishable from a hung one in a CI log.
	var done atomic.Int64
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range work {
				v, detail := runMutant(root, sites[index])
				results[index] = outcome{site: sites[index], verdict: v, detail: detail}
				if finished := done.Add(1); finished%25 == 0 || finished == int64(len(sites)) {
					fmt.Fprintf(os.Stderr, "mutation: %d/%d operations stripped and judged\n", finished, len(sites))
				}
			}
		}()
	}
	for index := range sites {
		work <- index
	}
	close(work)
	wait.Wait()

	var survivors, unbuildable, broken []string
	for _, result := range results {
		switch result.verdict {
		case survived:
			survivors = append(survivors, result.site.String())
		case didNotBuild:
			unbuildable = append(unbuildable, result.site.String()+": "+result.detail)
		case harnessBroke:
			broken = append(broken, result.site.String()+": "+result.detail)
		}
	}
	// Nothing below is believable if the machine gave out partway through.
	if len(broken) > 0 {
		t.Fatalf("%d mutants failed for reasons that are not about the mutant, so this run decides nothing:\n  %s",
			len(broken), strings.Join(broken, "\n  "))
	}
	// A mutant that does not compile proves nothing either way. It is reported
	// rather than counted as killed, because counting it as killed is how a
	// mutation score flatters itself.
	if len(unbuildable) > 0 {
		t.Logf("%d mutants did not compile and decide nothing:\n  %s", len(unbuildable), strings.Join(unbuildable, "\n  "))
	}
	if len(survivors) > survivingGuardCeiling {
		t.Fatalf("%d operations can lose every guard in front of them with the suite still green, above the ceiling of %d. Each is an operation whose authorization no test asserts:\n  %s",
			len(survivors), survivingGuardCeiling, strings.Join(survivors, "\n  "))
	}
	if len(survivors) < survivingGuardCeiling {
		t.Fatalf("only %d operations survive losing every guard now: lower survivingGuardCeiling to %d, so the ground gained is kept",
			len(survivors), len(survivors))
	}
}

// runMutant strips every guard from one operation and reports whether anything
// noticed.
func runMutant(root string, target operation) (verdict, string) {
	original, err := os.ReadFile(filepath.Join(root, target.file))
	if err != nil {
		return didNotBuild, err.Error()
	}
	mutated := append([]byte{}, original...)
	// Bottom-up, so each deletion leaves the offsets above it untouched.
	for index := len(target.guards) - 1; index >= 0; index-- {
		guard := target.guards[index]
		if guard.end > len(mutated) {
			return didNotBuild, "guard range is outside the file"
		}
		mutated = append(append([]byte{}, mutated[:guard.start]...), mutated[guard.end:]...)
	}
	dir, err := os.MkdirTemp("", "guard-mutant")
	if err != nil {
		return didNotBuild, err.Error()
	}
	defer os.RemoveAll(dir)
	replacement := filepath.Join(dir, filepath.Base(target.file))
	if err := os.WriteFile(replacement, mutated, 0o600); err != nil {
		return didNotBuild, err.Error()
	}
	overlay := filepath.Join(dir, "overlay.json")
	document := fmt.Sprintf("{%q:{%q:%q}}", "Replace", filepath.Join(root, target.file), replacement)
	if err := os.WriteFile(overlay, []byte(document), 0o600); err != nil {
		return didNotBuild, err.Error()
	}
	// Concurrent builds sharing one cache occasionally read an entry another
	// process has just evicted, which surfaces as "could not import ... no such
	// file or directory" and has nothing to do with the mutant. It is retried
	// rather than recorded, because recording it silently turns a killed mutant
	// into an undecided one — the second sweep reported 32 undecided and most
	// of them were this. Only a fault that survives every attempt stops the run.
	var text string
	var runErr error
	for attempt := 0; attempt < mutantAttempts; attempt++ {
		arguments := append([]string{"test", "-count=1", "-overlay=" + overlay}, killers...)
		command := exec.Command("go", arguments...)
		command.Dir = root
		// The repository's own cache, which is what the Makefile builds
		// through. Sharing the developer's default cache means the sweep
		// races whatever else is compiling on the machine.
		command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".cache", "go-build"))
		var output []byte
		output, runErr = command.CombinedOutput()
		text = string(output)
		if !looksLikeAToolchainFault(text) {
			break
		}
	}
	if fault, broken := toolchainFault(text); broken {
		return harnessBroke, fault
	}
	if strings.Contains(text, "[build failed]") {
		return didNotBuild, buildErrorLine(text)
	}
	if runErr == nil {
		return survived, ""
	}
	return killed, ""
}

// mutantAttempts is how many times a mutant is retried when the toolchain
// itself failed rather than the mutant.
const mutantAttempts = 3

// toolchainFaults are failures of the machine or the build cache. A run that
// still shows one after every attempt decides nothing and says so, rather than
// producing a finished-looking report: the first sweep here filled the disk and
// reported 157 undecided mutants, several of which had in fact been killed.
var toolchainFaults = []string{
	"no space left on device",
	"too many open files",
	"cannot allocate memory",
	"signal: killed",
	"could not import",
}

func looksLikeAToolchainFault(text string) bool {
	_, broken := toolchainFault(text)
	return broken
}

func toolchainFault(text string) (string, bool) {
	for _, fault := range toolchainFaults {
		if strings.Contains(text, fault) {
			return fault, true
		}
	}
	return "", false
}

// buildErrorLine returns the compiler's own first complaint, which is what
// says whether the mutant was unbuildable for a reason worth knowing.
func buildErrorLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, ".go:") && strings.Contains(trimmed, ": ") {
			return trimmed
		}
	}
	return "build failed with no compiler diagnostic"
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
