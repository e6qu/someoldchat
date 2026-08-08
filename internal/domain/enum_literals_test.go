package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoPackageComparesTheseEnumsAgainstALiteral fails when a distinctive enum
// value is written as a bare string in a comparison outside this package.
//
// The types below are string kinds, so the compiler happily compares one with
// an untyped constant: after WorkflowTriggerType became a type, four sites went
// on comparing trigger.Type with "link", "shortcut" and "webhook", and one
// compared a canvas section with "markdown". Naming the type closed the class
// only where callers were changed, and nothing would have said so.
//
// The rule is deliberately narrow. It covers values distinctive enough to be
// unambiguous, and skips ones like "user" or "read" that any string in the
// product might legitimately be. It is a smoke alarm for one shape of drift,
// not a type checker: catching every case needs type information the toolchain
// here does not have without a new dependency.
func TestNoPackageComparesTheseEnumsAgainstALiteral(t *testing.T) {
	guarded := map[string]string{
		// WorkflowTriggerType
		`"link"`: "domain.WorkflowTriggerLink", `"shortcut"`: "domain.WorkflowTriggerShortcut",
		`"webhook"`: "domain.WorkflowTriggerWebhook", `"scheduled"`: "domain.WorkflowTriggerScheduled",
		// WorkflowStepType
		`"create_canvas"`: "domain.WorkflowStepCanvas", `"add_people"`: "domain.WorkflowStepAddPeople",
		`"wait_until"`: "domain.WorkflowStepWaitUntil",
		// PermissionType
		`"app_collaborators"`: "domain.PermissionAppCollaborators", `"named_entities"`: "domain.PermissionNamedEntities",
		// AnalyticsKind and WorkflowAuthStrategy
		`"public_channel"`: "domain.AnalyticsPublicChannel", `"builder_choice"`: "domain.WorkflowAuthBuilderChoice",
		`"end_user_only"`: "domain.WorkflowAuthEndUserOnly",
	}
	// Two spellings are shared by concepts that have nothing to do with these
	// enums. Recording them is a decision; leaving the alarm to be ignored
	// would make every later alarm ignorable too.
	exempt := map[string]string{
		"../service/app_interactions.go:\"shortcut\"": "a Slack interaction payload type, not a workflow trigger type",
		"../web/handler.go:\"scheduled\"":             "the name of a Later tab, not a workflow trigger type",
	}
	root := ".."
	failures := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// The domain declares these values; the web templates carry them as
			// HTML option values, which is markup rather than a comparison.
			if name := info.Name(); name == "domain" || name == "gen" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, source, 0)
		if parseErr != nil {
			return nil
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			comparison, ok := node.(*ast.BinaryExpr)
			if !ok || (comparison.Op != token.EQL && comparison.Op != token.NEQ) {
				return true
			}
			for _, side := range []ast.Expr{comparison.X, comparison.Y} {
				literal, isLiteral := side.(*ast.BasicLit)
				if !isLiteral || literal.Kind != token.STRING {
					continue
				}
				constant, guardedValue := guarded[literal.Value]
				if !guardedValue {
					continue
				}
				if _, recorded := exempt[path+":"+literal.Value]; recorded {
					continue
				}
				failures++
				t.Errorf("%s:%d compares against %s; use %s so the value has one spelling",
					path, fileSet.Position(literal.Pos()).Line, literal.Value, constant)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = failures
}
