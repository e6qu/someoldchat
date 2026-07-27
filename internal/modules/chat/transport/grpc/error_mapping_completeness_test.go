package grpc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// serviceValidationErrorNames reports every exported service.ErrInvalid*
// sentinel declared in the service package, read from source so a newly
// declared validation error cannot silently bypass mapError.
func serviceValidationErrorNames(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "..", "service")
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

// A validation error describes a caller mistake. codes.Unavailable tells the
// caller to retry, which a malformed request will never survive, so every
// sentinel must map to codes.InvalidArgument.
//
// The sentinel is resolved through errorClasses, not through a second
// hand-written name→sentinel map. That map had 33 entries that had to be kept in
// step with the table by hand — the exact duplication the single table was
// introduced to delete — and a sentinel missing from it failed the test for
// bookkeeping reasons rather than for a defect.
func TestMapErrorClassifiesEveryServiceValidationErrorAsInvalidArgument(t *testing.T) {
	for _, name := range serviceValidationErrorNames(t) {
		t.Run(name, func(t *testing.T) {
			key := "service." + sentinelKey(name)
			class, classified := errorClassesByKey[key]
			if !classified {
				t.Fatalf("service.%s has no class; TestEveryDomainSentinelIsClassified says which table entry is missing", name)
			}
			got := status.Code(mapError(fmt.Errorf("wrapped: %w", class.sentinel)))
			if got != codes.InvalidArgument {
				t.Fatalf("mapError(service.%s) = %s, want %s", name, got, codes.InvalidArgument)
			}
		})
	}
}
