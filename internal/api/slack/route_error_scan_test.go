package slack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file reads handler.go as source and answers two questions the package
// could not answer before:
//
//   - which registered route does each handler function serve, and
//   - which error codes can that function put on the wire, including the codes
//     written by every helper it calls?
//
// Both gates in this package used to work on flattened sets — the union of every
// pinned enum, and a hand-maintained table of scoped routes — so a code declared
// by *some* operation passed on *every* operation, and a route absent from the
// scope table was tested by nothing.

// registeredRoute is one line of Handler.Register.
type registeredRoute struct {
	method  string
	path    string
	handler string
}

// operation is the pinned contract path a route corresponds to: "/api/files.list"
// is operation "/files.list".
func (r registeredRoute) operation() string {
	return strings.TrimPrefix(r.path, "/api")
}

func parseHandlerSource(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	source, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "handler.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse handler.go: %v", err)
	}
	return fileSet, parsed
}

// registeredRoutes reads every mux.HandleFunc call in Register.
func registeredRoutes(t *testing.T) []registeredRoute {
	t.Helper()
	_, parsed := parseHandlerSource(t)
	routes := make([]registeredRoute, 0, 400)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Register" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "HandleFunc" {
				return true
			}
			pattern, ok := stringLiteral(call.Args[0])
			if !ok {
				return true
			}
			target, ok := call.Args[1].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			method, path := "", pattern
			if verb, rest, found := strings.Cut(pattern, " "); found {
				method, path = verb, rest
			}
			routes = append(routes, registeredRoute{method: method, path: path, handler: target.Sel.Name})
			return true
		})
	}
	if len(routes) < 100 {
		t.Fatalf("only %d routes discovered; the Register scan is broken", len(routes))
	}
	return routes
}

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// functionFacts is what one function in this package contributes: the codes its
// own body writes and the package functions it calls.
type functionFacts struct {
	codes  map[string]struct{}
	scopes map[string]struct{}
	calls  map[string]struct{}
}

// codeArguments names, for each code-emitting call, which of its arguments carry
// an error code.
var codeArguments = map[string][]int{
	"writeError":            {1},
	"decodeFailure":         {0},
	"mapServiceError":       {1},
	"mapAdminError":         {1},
	"mapServiceErrorExists": {1, 2},
	"mapServiceErrorNamed":  {1, 2, 3},
}

// codeReturningFunctions return a code rather than writing one, so every string
// literal they return is a code they can emit.
var codeReturningFunctions = map[string]struct{}{
	"mapServiceErrorNamed": {}, "mapAdminError": {}, "postMessageError": {},
	"decodeErrorCode": {},
}

// handlerFacts reads every function declared in handler.go.
func handlerFacts(t *testing.T) map[string]functionFacts {
	t.Helper()
	_, parsed := parseHandlerSource(t)
	facts := make(map[string]functionFacts)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		name := function.Name.Name
		entry, exists := facts[name]
		if !exists {
			entry = functionFacts{codes: make(map[string]struct{}), scopes: make(map[string]struct{}), calls: make(map[string]struct{})}
		}
		_, returnsCode := codeReturningFunctions[name]
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.SelectorExpr:
				// Any auth.Scope… named in a handler is a scope that handler can
				// require: authenticate takes it directly, and the three
				// scope-parameterised helpers receive it from their caller.
				if pkg, ok := value.X.(*ast.Ident); ok && pkg.Name == "auth" && strings.HasPrefix(value.Sel.Name, "Scope") {
					entry.scopes[value.Sel.Name] = struct{}{}
				}
			case *ast.CallExpr:
				callee := calleeName(value.Fun)
				if callee != "" {
					entry.calls[callee] = struct{}{}
				}
				for _, index := range codeArguments[callee] {
					if index < len(value.Args) {
						if code, ok := stringLiteral(value.Args[index]); ok && code != "" {
							entry.codes[code] = struct{}{}
						}
					}
				}

			case *ast.CompositeLit:
				// A `{"ok": false, "error": "…"}` envelope, whether it is written
				// inline or assembled in a local first.
				for _, code := range envelopeCodes(value) {
					entry.codes[code] = struct{}{}
				}
			case *ast.ReturnStmt:
				if !returnsCode {
					return true
				}
				for _, result := range value.Results {
					if code, ok := stringLiteral(result); ok && code != "" {
						entry.codes[code] = struct{}{}
					}
				}
			case *ast.AssignStmt:
				// The OAuth token endpoints build their RFC 6749 code in a local
				// named `reason` before writing it.
				for index, target := range value.Lhs {
					identifier, ok := target.(*ast.Ident)
					if !ok || identifier.Name != "reason" || index >= len(value.Rhs) {
						continue
					}
					if code, ok := stringLiteral(value.Rhs[index]); ok && code != "" {
						entry.codes[code] = struct{}{}
					}
				}
			}
			return true
		})
		facts[name] = entry
	}
	return facts
}

// envelopeCodes reads `map[string]any{"ok": false, "error": "…"}`.
func envelopeCodes(composite *ast.CompositeLit) []string {
	failure := false
	codes := make([]string, 0, 1)
	for _, element := range composite.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := stringLiteral(pair.Key)
		if !ok {
			continue
		}
		if key == "ok" {
			if identifier, ok := pair.Value.(*ast.Ident); ok && identifier.Name == "false" {
				failure = true
			}
		}
		if key == "error" {
			if code, ok := stringLiteral(pair.Value); ok {
				codes = append(codes, code)
			}
		}
	}
	if !failure {
		return nil
	}
	return codes
}

func calleeName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		if receiver, ok := value.X.(*ast.Ident); ok && receiver.Name == "h" {
			return value.Sel.Name
		}
	}
	return ""
}

// sharedNamers are the functions that name a failure from a *sentinel* or from
// the transport, not from the calling operation: mapServiceErrorNamed classifies
// a service error, writeAuthError classifies a credential failure, and
// decodeErrorCode classifies a malformed request. Every route reaches all three,
// so the codes they return on their own account are the same everywhere and
// cannot be attributed to one operation's enum. They are recorded once, in
// sentinelDrivenCodes, and asserted to be exactly that set.
//
// The arguments a route passes *to* these functions — notFoundReason,
// invalidReason, existsReason — are the route's own choice and are attributed to
// the route, because they are recorded where the call appears.
func sharedNamers() map[string]struct{} {
	return map[string]struct{}{
		"mapServiceErrorNamed": {},
		"mapAdminError":        {},
		"postMessageError":     {},
		"decodeErrorCode":      {},
		"writeAuthError":       {},
	}
}

// reachableCodes is the transitive closure of the codes a function can emit,
// stopping at the shared namers.
func reachableCodes(facts map[string]functionFacts, root string) map[string]struct{} {
	shared := sharedNamers()
	codes := make(map[string]struct{})
	seen := map[string]struct{}{root: {}}
	queue := []string{root}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		entry, ok := facts[name]
		if !ok {
			continue
		}
		if _, isShared := shared[name]; isShared && name != root {
			continue
		}
		for code := range entry.codes {
			codes[code] = struct{}{}
		}
		for call := range entry.calls {
			if _, visited := seen[call]; visited {
				continue
			}
			seen[call] = struct{}{}
			queue = append(queue, call)
		}
	}
	return codes
}

// sentinelDrivenCodes is the residue: the codes a shared namer returns on its own
// account, with the reason each one cannot be checked against a per-operation
// enum. Keeping the list here rather than leaving it implicit means a new
// unattributable code added to the mapper has to be justified.
func sentinelDrivenCodes() map[string]string {
	return map[string]string{
		"not_authed":                "authentication outcome; writeAuthError, reached by every scoped route",
		"invalid_auth":              "authentication outcome; writeAuthError, reached by every scoped route",
		"account_inactive":          "authentication outcome; writeAuthError, reached by every scoped route",
		"token_revoked":             "authentication outcome; the snapshot omits it from 122 operations that do declare invalid_auth and account_inactive for the same credential check",
		"missing_scope":             "authorization outcome; the snapshot omits it from ~60 operations that nonetheless declare a token scope",
		"fatal_error":               "the unclassified fallback of mapServiceErrorNamed and writeAuthError; no operation chooses it",
		"no_permission":             "service.ErrMessageNotOwned / ErrNotWorkspaceAdmin, classified by sentinel",
		"not_an_admin":              "mapAdminError's role denial, classified by sentinel",
		"not_in_channel":            "service.ErrNotInConversation, classified by sentinel",
		"cant_delete_primary_owner": "service.ErrLastWorkspaceOwner, classified by sentinel",
		"message_not_found":         "service.ErrMessageAlreadyDeleted, classified by sentinel",
		"invalid_presence":          "service.ErrInvalidPresence, classified by sentinel",
		"emoji_already_exists":      "service.ErrEmojiAlreadyExists, classified by sentinel",
		"file_storage_unavailable":  "service.ErrBlobUnavailable, classified by sentinel",
		"hash_conflict":             "store.ErrConflict, classified by sentinel",
		"too_many_bookmarks":        "store.ErrBookmarkLimit, classified by sentinel",
		"socket_mode_unavailable":   "store.ErrSocketModeConnectionLimit, classified by sentinel",
		"invalid_arg_name":          "the invalidReason default and decodeErrorCode's argument fallback",
		"invalid_form_data":         "decodeErrorCode's fallback for an unreadable request",
		"invalid_json":              "decodeErrorCode's fallback for a malformed JSON document",
		"request_timeout":           "decodeErrorCode's name for a request body past the size ceiling",
		"channel_not_found":         "postMessageError's name for store.ErrNotFound on a message write",
		"no_text":                   "postMessageError's name for service.ErrInvalidMessage",
	}
}

// A shared namer must not grow a code without a recorded reason, and a recorded
// reason must not outlive the code.
func TestSentinelDrivenCodesAreExactlyWhatTheSharedNamersReturn(t *testing.T) {
	facts := handlerFacts(t)
	recorded := sentinelDrivenCodes()
	emitted := make(map[string]struct{})
	for name := range sharedNamers() {
		entry, ok := facts[name]
		if !ok {
			t.Fatalf("%s is missing from handler.go", name)
		}
		for code := range entry.codes {
			emitted[code] = struct{}{}
		}
	}
	for _, code := range sortedKeys(emitted) {
		if _, ok := recorded[code]; !ok {
			t.Errorf("shared namer returns %q with no recorded reason", code)
		}
	}
	for code := range recorded {
		if _, ok := emitted[code]; !ok {
			t.Errorf("recorded sentinel-driven code %q is no longer returned by a shared namer", code)
		}
	}
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// reachableScopes is the transitive closure of the auth scopes a route enforces.
func reachableScopes(facts map[string]functionFacts, root string) map[string]struct{} {
	scopes := make(map[string]struct{})
	seen := map[string]struct{}{root: {}}
	queue := []string{root}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		entry, ok := facts[name]
		if !ok {
			continue
		}
		for scope := range entry.scopes {
			scopes[scope] = struct{}{}
		}
		for call := range entry.calls {
			if _, visited := seen[call]; visited {
				continue
			}
			seen[call] = struct{}{}
			queue = append(queue, call)
		}
	}
	return scopes
}
