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

	"github.com/sameoldchat/sameoldchat/internal/service"
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

// lookupServiceError resolves a sentinel by name. A sentinel declared in the
// service package but absent from the table fails loudly rather than silently
// skipping coverage.
func lookupServiceError(t *testing.T, name string) error {
	t.Helper()
	err, ok := serviceErrorsByName()[name]
	if !ok {
		t.Fatalf("service.%s is declared but not listed in serviceErrorsByName; add it", name)
	}
	return err
}

// A validation error describes a caller mistake. codes.Unavailable tells the
// caller to retry, which a malformed request will never survive, so every
// sentinel must map to codes.InvalidArgument.
func TestMapErrorClassifiesEveryServiceValidationErrorAsInvalidArgument(t *testing.T) {
	for _, name := range serviceValidationErrorNames(t) {
		err := lookupServiceError(t, name)
		t.Run(name, func(t *testing.T) {
			got := status.Code(mapError(fmt.Errorf("wrapped: %w", err)))
			if got != codes.InvalidArgument {
				t.Fatalf("mapError(service.%s) = %s, want %s", name, got, codes.InvalidArgument)
			}
		})
	}
}

func serviceErrorsByName() map[string]error {
	return map[string]error{
		"ErrInvalidMessage":           service.ErrInvalidMessage,
		"ErrInvalidTimestamp":         service.ErrInvalidTimestamp,
		"ErrInvalidConversation":      service.ErrInvalidConversation,
		"ErrInvalidWorkspace":         service.ErrInvalidWorkspace,
		"ErrInvalidConversationPrefs": service.ErrInvalidConversationPrefs,
		"ErrInvalidReaction":          service.ErrInvalidReaction,
		"ErrInvalidFile":              service.ErrInvalidFile,
		"ErrInvalidSearch":            service.ErrInvalidSearch,
		"ErrInvalidProfile":           service.ErrInvalidProfile,
		"ErrInvalidPresence":          service.ErrInvalidPresence,
		"ErrInvalidSnooze":            service.ErrInvalidSnooze,
		"ErrInvalidReminder":          service.ErrInvalidReminder,
		"ErrInvalidCall":              service.ErrInvalidCall,
		"ErrInvalidUserGroup":         service.ErrInvalidUserGroup,
		"ErrInvalidEphemeral":         service.ErrInvalidEphemeral,
		"ErrInvalidAccessLog":         service.ErrInvalidAccessLog,
		"ErrInvalidEmoji":             service.ErrInvalidEmoji,
		"ErrInvalidRemoteFile":        service.ErrInvalidRemoteFile,
		"ErrInvalidInviteRequest":     service.ErrInvalidInviteRequest,
		"ErrInvalidAppApproval":       service.ErrInvalidAppApproval,
		"ErrInvalidView":              service.ErrInvalidView,
		"ErrInvalidWorkflowStep":      service.ErrInvalidWorkflowStep,
		"ErrInvalidDialog":            service.ErrInvalidDialog,
		"ErrInvalidBot":               service.ErrInvalidBot,
		"ErrInvalidMigration":         service.ErrInvalidMigration,
		"ErrInvalidOAuth":             service.ErrInvalidOAuth,
		"ErrInvalidOAuthClient":       service.ErrInvalidOAuthClient,
		"ErrInvalidIntegrationLogs":   service.ErrInvalidIntegrationLogs,
		"ErrInvalidBookmark":          service.ErrInvalidBookmark,
		"ErrInvalidCanvas":            service.ErrInvalidCanvas,
		"ErrInvalidList":              service.ErrInvalidList,
		"ErrInvalidEntity":            service.ErrInvalidEntity,
		"ErrInvalidExternalUpload":    service.ErrInvalidExternalUpload,
	}
}
