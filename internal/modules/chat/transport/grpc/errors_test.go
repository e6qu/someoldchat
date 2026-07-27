package grpc

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
}

// TestNoCodeGRPCProducesItselfHasAFallbackClass is the general form of a defect
// that was fixed once for codes.Unavailable and reintroduced for
// codes.ResourceExhausted.
//
// grpc-go emits several codes on its own behalf, with no DomainError detail, for
// conditions that have no domain cause: ResourceExhausted for a message over
// MaxMessageBytes, Internal for a recovered panic, Unimplemented for a method a
// peer at another version does not serve. A fallback class on any of them hands
// the caller a sentinel the chat process never reported —
// store.ErrSocketModeConnectionLimit for a conversations.history page that was
// merely too large, which internal/api/slack renders as socket_mode_unavailable.
//
// Asserting the whole set rather than one code is the point: the previous test
// named codes.Unavailable only, so the same mistake on a sibling code passed.
func TestNoCodeGRPCProducesItselfHasAFallbackClass(t *testing.T) {
	if len(libraryProducedCodes) == 0 {
		t.Fatal("libraryProducedCodes is empty; the rule it states is not being checked")
	}
	for code, reason := range libraryProducedCodes {
		if class, exists := errorClassesByCode[code]; exists {
			t.Errorf("%s must have no fallback class, found %q: grpc-go produces %s for %s", code, class.key, code, reason)
		}
	}
}

// TestTheFallbackClassIsTheLastOfItsCode holds the ordering rule the table
// documents above errorClasses: a specific sentinel precedes the general one it
// might wrap. The general member of a code group is exactly the one that
// restores the bare code, so the rule is checkable rather than aspirational.
//
// store.ErrInvalidArgument used to lead its block of 34, so classifyError
// answered the generic class for every error that carried both, and
// errors.Join(service.ErrInvalidMessage, store.ErrInvalidArgument) lost the
// specific sentinel on the wire.
func TestTheFallbackClassIsTheLastOfItsCode(t *testing.T) {
	lastIndexOfCode := make(map[codes.Code]int, len(errorClasses))
	for index, class := range errorClasses {
		lastIndexOfCode[class.code] = index
	}
	for index, class := range errorClasses {
		if !class.restoresCode {
			continue
		}
		if last := lastIndexOfCode[class.code]; last != index {
			t.Errorf("%q restores a bare %s but %q is declared after it; the general member of a code closes its group",
				class.key, class.code, errorClasses[last].key)
		}
	}
}

// TestAnErrorCarryingTwoSentinelsRestoresBoth covers the joined error.
//
// internal/service/canvases.go returns errors.Join(err, cleanupErr) when a
// compensating delete fails after a rejected create, so one error satisfies
// errors.Is for two sentinels in process. While the wire carried one key the
// caller restored one of them, and internal/api/slack — which tests
// store.ErrNotFound first — answered channel_not_found in the monolith and
// invalid_arg_name across the seam.
func TestAnErrorCarryingTwoSentinelsRestoresBoth(t *testing.T) {
	joined := errors.Join(store.ErrNotFound, service.ErrInvalidCanvas)
	mapped := mapError(joined)
	sent, ok := status.FromError(mapped)
	if !ok {
		t.Fatal("mapError did not produce a gRPC status")
	}
	restored := mapRemoteError(sent.Err())
	for _, sentinel := range []error{store.ErrNotFound, service.ErrInvalidCanvas} {
		if !errors.Is(joined, sentinel) {
			t.Fatalf("the local error no longer carries %v; the case no longer provokes the class it documents", sentinel)
		}
		if !errors.Is(restored, sentinel) {
			t.Errorf("the restored error lost %v: %v", sentinel, restored)
		}
	}
	// A sentinel the error never carried must not appear.
	if errors.Is(restored, service.ErrInvalidMessage) {
		t.Errorf("the restored error invented service.ErrInvalidMessage: %v", restored)
	}
}

// TestTheStatusMessageIsBounded keeps a classified failure inside the header
// list bound. mapError copies err.Error() into the status message, which travels
// in HTTP/2 trailers; an unbounded one turns a domain error into a transport
// failure with a different class.
func TestTheStatusMessageIsBounded(t *testing.T) {
	mapped := mapError(fmt.Errorf("%w: %s", store.ErrInvalidArgument, strings.Repeat("x", 4*maxStatusMessageBytes)))
	sent, _ := status.FromError(mapped)
	if len(sent.Message()) > maxStatusMessageBytes {
		t.Fatalf("status message is %d bytes, want at most %d", len(sent.Message()), maxStatusMessageBytes)
	}
	if !errors.Is(mapRemoteError(sent.Err()), store.ErrInvalidArgument) {
		t.Fatal("bounding the message lost the classification")
	}
}

// TestTheWireKeySetMatchesTheRecordedContract gates the key set against a
// checked-in file.
//
// errors.proto states that the keys must never be renamed, and
// TestEveryDomainSentinelIsClassified derives the expected key from the Go
// identifier — so renaming store.ErrNotFound to store.ErrMissing made the suite
// *demand* the key "store.missing", and a rolling deployment would then lose the
// classification in both directions with nothing red. The golden file is what
// makes a rename show up as a reviewed change to the contract rather than as a
// mechanical consequence of a Go rename.
func TestTheWireKeySetMatchesTheRecordedContract(t *testing.T) {
	keys := make([]string, 0, len(errorClasses))
	for _, class := range errorClasses {
		keys = append(keys, class.key)
	}
	sort.Strings(keys)
	recorded, err := os.ReadFile(filepath.Join("testdata", "error-keys.txt"))
	if err != nil {
		t.Fatalf("read the recorded key set: %v", err)
	}
	want := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	for index := range want {
		want[index] = strings.TrimSpace(want[index])
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("the wire key set changed.\n got: %v\nwant: %v\n\nAdding a key: append it to testdata/error-keys.txt.\nRemoving or renaming one: it is wire contract, and a rolling deployment runs both versions, so it needs a deprecation across a release rather than an edit here.", keys, want)
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
	for qualified, name := range discoveredSentinels(t) {
		key := packageOf(qualified) + "." + sentinelKey(name)
		if reason, excluded := unclassifiedSentinels[qualified]; excluded {
			if _, classified := errorClassesByKey[key]; classified {
				t.Errorf("%s is both classified and excluded (%s)", qualified, reason)
			}
			continue
		}
		if _, classified := errorClassesByKey[key]; !classified {
			t.Errorf("%s crosses the seam unclassified: add {key: %q, code: ..., sentinel: %s} to errorClasses, or document it in unclassifiedSentinels",
				qualified, key, qualified)
		}
	}
}

// unclassifiedSentinels is the documented escape hatch: a sentinel listed here
// deliberately has no class, with the reason it cannot reach a chat handler. An
// entry must name a sentinel that actually exists, which
// TestExclusionsNameRealSentinels asserts.
var unclassifiedSentinels = map[string]string{
	"sqlstore.ErrIntegrityCheckUnsupported": "IntegrityCheck is storage maintenance, not part of chatapi.Service, so it never crosses the chat seam; classifying it would make the transport package import a storage backend",
	"events.ErrPayloadInternal":             "a delivery filter the consumer evaluates on records it already holds (internal/socketmode), never returned by a chat RPC",
	"events.ErrPayloadRecipientScoped":      "a delivery filter, as above",
}

func TestExclusionsNameRealSentinels(t *testing.T) {
	discovered := discoveredSentinels(t)
	for excluded, reason := range unclassifiedSentinels {
		if _, exists := discovered[excluded]; !exists {
			t.Errorf("unclassifiedSentinels names %s (%q), which no longer exists", excluded, reason)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is excluded without a reason", excluded)
		}
	}
}

// sentinelRoots are the package trees whose exported sentinels can reach a chat
// handler and therefore have to be classified or excluded with a reason.
//
// internal/events and internal/blob are here because they do reach one:
// service.Messages.newEvent funnels every mutation through events.New, and
// service.Messages returns a blob absence unchanged. The scan used to cover
// store, service and domain only, so those sentinels crossed the seam as
// codes.Unavailable with the fixed unclassified message — a caller told to retry
// a request that can never succeed.
var sentinelRoots = []string{"store", "service", "domain", "events", "blob"}

// discoveredSentinels reads every exported Err* declaration under sentinelRoots
// from source and returns them keyed by "package.Name".
//
// It descends into subdirectories, because a subpackage's sentinel crosses the
// same seam: the scan used to skip directories, so
// store/sqlstore.ErrIntegrityCheckUnsupported was invisible to it. It accepts
// const as well as var, because a sentinel declared as a constant error type is
// still a sentinel and the sibling scan in this package already accepted both —
// two scans in one package that disagreed about what a sentinel is.
func discoveredSentinels(t *testing.T) map[string]string {
	t.Helper()
	discovered := make(map[string]string)
	for _, root := range sentinelRoots {
		before := len(discovered)
		dir := filepath.Join("..", "..", "..", "..", root)
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", path, parseErr)
			}
			for _, name := range exportedSentinelNames(parsed) {
				discovered[parsed.Name.Name+"."+name] = name
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
		if len(discovered) == before {
			t.Fatalf("no sentinels discovered under %s; the source scan is broken", dir)
		}
	}
	return discovered
}

func packageOf(qualified string) string {
	return qualified[:strings.Index(qualified, ".")]
}

func exportedSentinelNames(parsed *ast.File) []string {
	names := make([]string, 0)
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
				if strings.HasPrefix(name.Name, "Err") && name.IsExported() {
					names = append(names, name.Name)
				}
			}
		}
	}
	return names
}

// sentinelKey derives the wire key from the Go identifier: ErrInvalidMessage
// becomes invalid_message. Acronyms are listed rather than guessed, because a
// mechanical split renders OAuth as o_auth.
//
// The list is an ordered slice, longest first, not a map: map iteration order is
// random, "OIDC" contains "ID", and with ID→Id applied first OIDC became OIdC and
// the OIDC rule stopped matching — so the key for a sentinel containing OIDC
// would have alternated between "oidc" and "o_id_c" between runs. No sentinel
// contains OIDC today, which is the only reason the map was deterministic.
var sentinelAcronyms = []struct{ from, to string }{
	{from: "OAuth", to: "Oauth"},
	{from: "OIDC", to: "Oidc"},
	{from: "URL", to: "Url"},
	{from: "ID", to: "Id"},
	{from: "IP", to: "Ip"},
}

func sentinelKey(name string) string {
	trimmed := strings.TrimPrefix(name, "Err")
	for _, acronym := range sentinelAcronyms {
		trimmed = strings.ReplaceAll(trimmed, acronym.from, acronym.to)
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

// TestSentinelKeyResolvesAcronymsInAStableOrder pins the ordering the slice
// above exists for: a longer acronym has to be replaced before a shorter one it
// contains, or the result depends on which rule ran first.
func TestSentinelKeyResolvesAcronymsInAStableOrder(t *testing.T) {
	for name, want := range map[string]string{
		"ErrInvalidOIDCSession": "invalid_oidc_session",
		"ErrInvalidOAuthClient": "invalid_oauth_client",
		"ErrInvalidURLTarget":   "invalid_url_target",
		"ErrInvalidIDToken":     "invalid_id_token",
		"ErrInvalidMessage":     "invalid_message",
	} {
		if got := sentinelKey(name); got != want {
			t.Errorf("sentinelKey(%q) = %q, want %q", name, got, want)
		}
	}
}
