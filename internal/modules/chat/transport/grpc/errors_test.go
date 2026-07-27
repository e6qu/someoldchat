package grpc

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestErrorClassTableIsConsistent(t *testing.T) {
	keys := make(map[string]struct{}, len(errorClasses))
	sentinels := make(map[error]string, len(errorClasses))
	fallbacks := make(map[codes.Code]string, len(errorClasses))
	for _, class := range errorClasses {
		if class.sentinel == nil {
			t.Fatalf("class %q has no sentinel", class.key)
		}
		if !strings.Contains(class.key, ".") {
			t.Errorf("class key %q must be qualified by the package that declares the sentinel", class.key)
		}
		if _, exists := keys[class.key]; exists {
			t.Errorf("class key %q is declared twice; keys are wire contract and must be unique", class.key)
		}
		keys[class.key] = struct{}{}
		if existing, exists := sentinels[class.sentinel]; exists {
			t.Errorf("sentinel %v is classified twice (%q and %q)", class.sentinel, existing, class.key)
		}
		sentinels[class.sentinel] = class.key
		if !class.restoresCode {
			continue
		}
		if existing, exists := fallbacks[class.code]; exists {
			t.Errorf("code %s has two fallback classes (%q and %q); a bare code has one meaning", class.code, existing, class.key)
		}
		fallbacks[class.code] = class.key
	}
	if len(errorClassesByKey) != len(errorClasses) {
		t.Fatalf("errorClassesByKey has %d entries for %d classes", len(errorClassesByKey), len(errorClasses))
	}
	// codes.Unavailable must not restore a sentinel from the bare code: it is also
	// the code an unclassified internal failure carries, so a fallback there would
	// invent service.ErrBlobUnavailable for every storage failure.
	if class, exists := errorClassesByCode[codes.Unavailable]; exists {
		t.Errorf("codes.Unavailable must have no fallback class, found %q", class.key)
	}
}

// TestEveryClassSurvivesTheWireInBothDirections is the exhaustiveness gate: for
// every class in the table, the server's status must carry it and the client must
// restore that exact sentinel and no other. A sentinel that were mapped in one
// direction only fails here, which is what a hand-maintained pair of switch
// statements could not guarantee.
func TestEveryClassSurvivesTheWireInBothDirections(t *testing.T) {
	for _, class := range errorClasses {
		t.Run(class.key, func(t *testing.T) {
			mapped := mapError(fmt.Errorf("handler failed: %w", class.sentinel))
			if got := status.Code(mapped); got != class.code {
				t.Fatalf("mapError code = %s, want %s", got, class.code)
			}
			sent, ok := status.FromError(mapped)
			if !ok {
				t.Fatal("mapError did not produce a gRPC status")
			}
			// status.Err/FromError is exactly what grpc-go does with the handler's
			// error, so this round trip is the wire behaviour.
			restored := mapRemoteError(sent.Err())
			if !errors.Is(restored, class.sentinel) {
				t.Fatalf("mapRemoteError did not restore %s: %v", class.key, restored)
			}
			if got := status.Code(restored); got != class.code {
				t.Fatalf("restored code = %s, want %s", got, class.code)
			}
			for _, other := range errorClasses {
				if other.key == class.key {
					continue
				}
				if errors.Is(restored, other.sentinel) {
					t.Fatalf("restored error for %s also matches %s", class.key, other.key)
				}
			}
		})
	}
}

// TestBareStatusCodeRestoresOnlyTheFallbackClass covers the rolling deployment: a
// chat process that predates the DomainError detail sends the code alone, and the
// client must fall back to the class that owns the code without inventing one for
// a code that has no fallback.
func TestBareStatusCodeRestoresOnlyTheFallbackClass(t *testing.T) {
	for _, class := range errorClasses {
		t.Run(class.key, func(t *testing.T) {
			restored := mapRemoteError(status.Error(class.code, "peer without details"))
			fallback, hasFallback := errorClassesByCode[class.code]
			switch {
			case !hasFallback:
				if _, classified := classifyError(restored); classified {
					t.Fatalf("code %s has no fallback class but restored %v", class.code, restored)
				}
			case class.key == fallback.key:
				if !errors.Is(restored, class.sentinel) {
					t.Fatalf("bare %s did not restore %s: %v", class.code, class.key, restored)
				}
			default:
				if errors.Is(restored, class.sentinel) {
					t.Fatalf("bare %s restored the specific sentinel %s instead of the fallback %s", class.code, class.key, fallback.key)
				}
			}
		})
	}
}

// TestUnclassifiedFailuresDoNotLeakStorageText covers the information disclosure:
// internal/web renders err.Error() to the browser, and the catch-all used to copy
// the storage layer's message into the status.
func TestUnclassifiedFailuresDoNotLeakStorageText(t *testing.T) {
	secret := `dial postgres://chat:s3cret@db.internal:5432/chat: connection refused`
	mapped := mapError(errors.New(secret))
	if status.Code(mapped) != codes.Unavailable {
		t.Fatalf("unclassified code = %s, want %s", status.Code(mapped), codes.Unavailable)
	}
	sent, _ := status.FromError(mapped)
	if strings.Contains(sent.Message(), "s3cret") || sent.Message() != unclassifiedMessage {
		t.Fatalf("status message = %q, want the fixed unclassified message", sent.Message())
	}
	if !strings.Contains(mapped.Error(), "s3cret") {
		t.Fatal("the cause must stay available in process for the log record")
	}
	restored := mapRemoteError(sent.Err())
	if strings.Contains(restored.Error(), "s3cret") {
		t.Fatalf("client-visible error %q leaks the storage message", restored.Error())
	}
}

func TestRemoteErrorTextOmitsTheTransportPreamble(t *testing.T) {
	mapped := mapError(fmt.Errorf("post message: %w", service.ErrInvalidMessage))
	sent, _ := status.FromError(mapped)
	restored := mapRemoteError(sent.Err())
	if got := restored.Error(); got != "post message: "+service.ErrInvalidMessage.Error() {
		t.Fatalf("remote error text = %q, want the domain text a caller renders in process", got)
	}
}

func TestInvalidArgumentHelpersCarryTheGenericClass(t *testing.T) {
	direct := invalidArgument("workspace_id is required")
	if status.Code(direct) != codes.InvalidArgument || !errors.Is(direct, store.ErrInvalidArgument) {
		t.Fatalf("invalidArgument produced %v (code %s)", direct, status.Code(direct))
	}
	sent, _ := status.FromError(direct)
	if !strings.Contains(sent.Message(), "workspace_id is required") {
		t.Fatalf("status message %q lost the reason", sent.Message())
	}
	// A value that already carries a sentinel keeps it: losing it would answer a
	// caller with a less specific class than the monolith does.
	preserved := invalidArgumentFrom(fmt.Errorf("decode cursor: %w", domain.ErrInvalidCursor))
	if !errors.Is(preserved, domain.ErrInvalidCursor) {
		t.Fatalf("invalidArgumentFrom discarded the sentinel: %v", preserved)
	}
	generic := invalidArgumentFrom(errors.New("limit must be between 1 and 200"))
	if !errors.Is(generic, store.ErrInvalidArgument) {
		t.Fatalf("invalidArgumentFrom did not classify a bare error: %v", generic)
	}
}

func TestMappingIsIdempotentAndKeepsAPanicInternal(t *testing.T) {
	recovered := panicError("nil map write")
	if status.Code(recovered) != codes.Internal {
		t.Fatalf("panic code = %s, want %s", status.Code(recovered), codes.Internal)
	}
	if status.Code(mapError(recovered)) != codes.Internal {
		t.Fatal("mapError must not reclassify an error that already carries a status")
	}
	sent, _ := status.FromError(recovered)
	if strings.Contains(sent.Message(), "nil map write") {
		t.Fatalf("panic status message %q must not carry the panic value", sent.Message())
	}
}

func TestContextErrorsRestoreWithoutDetails(t *testing.T) {
	for sentinel, code := range map[error]codes.Code{
		context.Canceled:         codes.Canceled,
		context.DeadlineExceeded: codes.DeadlineExceeded,
	} {
		// grpc-go produces these itself, without a DomainError detail.
		restored := mapRemoteError(status.Error(code, "produced by the transport"))
		if !errors.Is(restored, sentinel) {
			t.Fatalf("bare %s did not restore %v: %v", code, sentinel, restored)
		}
	}
}

func TestUnknownDetailKeyFallsBackToTheCode(t *testing.T) {
	// A newer chat process may send a class this client does not know. The client
	// must fall back to the code rather than dropping the classification.
	result := status.New(codes.NotFound, "unknown class")
	detailed, err := result.WithDetails(&chatv1.DomainError{Key: "store.some_future_sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	restored := mapRemoteError(detailed.Err())
	if !errors.Is(restored, store.ErrNotFound) {
		t.Fatalf("unknown key did not fall back to the code: %v", restored)
	}
}

// TestEveryDomainSentinelIsClassified reads the sentinel declarations of
// internal/store, internal/service and internal/domain from source. A sentinel
// declared there and absent from the table fails here, which is the gate that
// stops the seam from silently degrading a new failure mode to codes.Unavailable.
//
// The expected key is derived from the declaration, so the table cannot drift
// from the sentinel it claims to carry either.
func TestEveryDomainSentinelIsClassified(t *testing.T) {
	for _, pkg := range []struct {
		name string
		dir  string
	}{
		{name: "store", dir: filepath.Join("..", "..", "..", "..", "store")},
		{name: "service", dir: filepath.Join("..", "..", "..", "..", "service")},
		{name: "domain", dir: filepath.Join("..", "..", "..", "..", "domain")},
	} {
		names := exportedSentinelNames(t, pkg.dir)
		if len(names) == 0 {
			t.Fatalf("no sentinels discovered in %s; the source scan is broken", pkg.dir)
		}
		for _, name := range names {
			key := pkg.name + "." + sentinelKey(name)
			if reason, excluded := unclassifiedSentinels[pkg.name+"."+name]; excluded {
				if _, classified := errorClassesByKey[key]; classified {
					t.Errorf("%s.%s is both classified and excluded (%s)", pkg.name, name, reason)
				}
				continue
			}
			if _, classified := errorClassesByKey[key]; !classified {
				t.Errorf("%s.%s crosses the seam unclassified: add {key: %q, code: ..., sentinel: %s.%s} to errorClasses, or document it in unclassifiedSentinels",
					pkg.name, name, key, pkg.name, name)
			}
		}
	}
}

// unclassifiedSentinels is the documented escape hatch: a sentinel listed here
// deliberately has no class, with the reason. It is empty because every sentinel
// these three packages declare today can reach a chat handler, and a sentinel
// that can reach a handler belongs in the table. An entry must name a sentinel
// that actually exists, which TestExclusionsNameRealSentinels asserts.
var unclassifiedSentinels = map[string]string{}

func TestExclusionsNameRealSentinels(t *testing.T) {
	discovered := make(map[string]struct{})
	for _, pkg := range []struct {
		name string
		dir  string
	}{
		{name: "store", dir: filepath.Join("..", "..", "..", "..", "store")},
		{name: "service", dir: filepath.Join("..", "..", "..", "..", "service")},
		{name: "domain", dir: filepath.Join("..", "..", "..", "..", "domain")},
	} {
		for _, name := range exportedSentinelNames(t, pkg.dir) {
			discovered[pkg.name+"."+name] = struct{}{}
		}
	}
	for excluded, reason := range unclassifiedSentinels {
		if _, exists := discovered[excluded]; !exists {
			t.Errorf("unclassifiedSentinels names %s (%q), which no longer exists", excluded, reason)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is excluded without a reason", excluded)
		}
	}
}

func exportedSentinelNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fileSet := token.NewFileSet()
	names := make([]string, 0)
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
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					if strings.HasPrefix(name.Name, "Err") && name.IsExported() {
						names = append(names, name.Name)
					}
				}
			}
		}
	}
	return names
}

// sentinelKey derives the wire key from the Go identifier: ErrInvalidMessage
// becomes invalid_message. Acronyms are listed rather than guessed, because a
// mechanical split renders OAuth as o_auth.
func sentinelKey(name string) string {
	trimmed := strings.TrimPrefix(name, "Err")
	for acronym, replacement := range map[string]string{"OAuth": "Oauth", "OIDC": "Oidc", "URL": "Url", "ID": "Id", "IP": "Ip"} {
		trimmed = strings.ReplaceAll(trimmed, acronym, replacement)
	}
	var builder strings.Builder
	for index, character := range trimmed {
		if character >= 'A' && character <= 'Z' {
			if index > 0 {
				builder.WriteByte('_')
			}
			builder.WriteRune(character - 'A' + 'a')
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}
