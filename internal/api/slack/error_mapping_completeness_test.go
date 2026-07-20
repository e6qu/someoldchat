package slack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serviceValidationErrors reports every exported service.ErrInvalid* sentinel
// declared in the service package. The list is derived from source rather than
// hand-maintained so that a newly declared validation error cannot silently
// bypass the transport mapping below.
func serviceValidationErrors(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "service")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read service package: %v", err)
	}
	names := make([]string, 0)
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || (general.Tok != token.VAR && general.Tok != token.CONST) {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					if strings.HasPrefix(name.Name, "ErrInvalid") && name.IsExported() {
						names = append(names, name.Name)
					}
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("no service validation errors discovered; the source scan is broken")
	}
	return names
}

// A validation error describes a caller mistake. Routing one to a 5xx tells the
// caller to retry a request that can never succeed, so every sentinel must be
// named by mapServiceError. Reading handler.go keeps the check honest even when
// a sentinel is reachable only through a route this package does not exercise.
func TestMapServiceErrorNamesEveryServiceValidationError(t *testing.T) {
	source, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	body := string(source)
	missing := make([]string, 0)
	for _, name := range serviceValidationErrors(t) {
		if !strings.Contains(body, "service."+name+")") {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("mapServiceError does not name %v; unnamed validation errors fall through to %d service_unavailable", missing, http.StatusServiceUnavailable)
	}
}
