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

// packageSourceFiles is every non-test source file of this package.
//
// This used to be the single name "handler.go". The package happens to hold one
// implementation file today, so both gates were correct by accident: moving one
// handler into a second file, or adding one, would have removed it from the error
// gate and the scope table silently.
func packageSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no package source files discovered; the scan is broken")
	}
	return names
}

// handlerSource is the concatenated text of every file the gates read, for the
// checks that work on text rather than syntax.
func handlerSource(t *testing.T) string {
	t.Helper()
	var body strings.Builder
	for _, name := range packageSourceFiles(t) {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body.Write(source)
	}
	return body.String()
}

func parseHandlerSource(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fileSet := token.NewFileSet()
	files := make([]*ast.File, 0, 2)
	for _, name := range packageSourceFiles(t) {
		parsed, err := parser.ParseFile(fileSet, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, parsed)
	}
	return fileSet, files
}

// registeredRoutes reads every mux.HandleFunc call in Register.
func registeredRoutes(t *testing.T) []registeredRoute {
	t.Helper()
	fileSet, parsed := parseHandlerSource(t)
	routes := make([]registeredRoute, 0, 400)
	for _, file := range parsed {
		for _, declaration := range file.Decls {
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
					t.Errorf("%s: a route is registered under a pattern this scan cannot read, so it is tested by nothing", fileSet.Position(call.Pos()))
					return true
				}
				target, ok := call.Args[1].(*ast.SelectorExpr)
				if !ok {
					// A closure or a wrapped handler used to be skipped in
					// silence: the route vanished from the error gate and the
					// scope table with nothing to say it had.
					t.Errorf("%s: %q is registered with a handler this scan cannot resolve; give it a method so both gates can see it", fileSet.Position(call.Pos()), pattern)
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

// codeWriters names the primitives that turn a string into an error code on the
// wire, and which of their arguments that string is. Everything else that carries
// a code carries it *to* one of these, and is derived rather than listed: see
// codeArguments.
var codeWriters = map[string][]int{
	"writeError":                {1},
	"decodeFailure":             {0},
	"mapServiceError":           {1},
	"mapAdminError":             {1},
	"mapServiceErrorExists":     {1, 2},
	"mapServiceErrorNamed":      {1, 2, 3},
	"writeIncomingWebhookError": {2},
}

// codeArguments names, for each code-carrying call, which of its arguments carry
// an error code.
//
// This used to be codeWriters alone, hand-maintained, and a code that reached a
// primitive through a *parameter* was recorded nowhere: the literal at the call
// site was not a known code position, and inside the helper the argument is an
// identifier rather than a literal. Three live codes on five routes were invisible
// to both gates that way — `invalid_ts_oldest`/`invalid_ts_latest` passed to
// normalizeHistoryRequest by /conversations.history, and `invalid_cursor` passed
// to decodeListRequestFields by /admin.conversations.search,
// /admin.conversations.getTeams, /users.list and /conversations.members — so any
// of them could have been changed to a code the operation does not declare with
// both gates staying green.
//
// So the positions are derived instead of remembered: a parameter that a function
// hands to a known code position is itself a code position, and that rule is
// applied to a fixpoint. A new helper written the same way is discovered on the
// run that introduces it, and
// TestEveryPinnedCodeLiteralInThePackageIsVisibleToTheScan fails if a code ever
// reaches the wire by a route this derivation still cannot follow.
func codeArguments(t *testing.T) map[string][]int {
	t.Helper()
	_, parsed := parseHandlerSource(t)
	positions := make(map[string]map[int]struct{}, len(codeWriters))
	for name, indexes := range codeWriters {
		positions[name] = make(map[int]struct{}, len(indexes))
		for _, index := range indexes {
			positions[name][index] = struct{}{}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, file := range parsed {
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				parameters := make(map[string]int)
				index := 0
				for _, field := range function.Type.Params.List {
					for _, name := range field.Names {
						parameters[name.Name] = index
						index++
					}
					if len(field.Names) == 0 {
						index++
					}
				}
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					known, ok := positions[calleeName(call.Fun)]
					if !ok {
						return true
					}
					for argument := range known {
						if argument >= len(call.Args) {
							continue
						}
						identifier, ok := call.Args[argument].(*ast.Ident)
						if !ok {
							continue
						}
						parameter, ok := parameters[identifier.Name]
						if !ok {
							continue
						}
						if positions[function.Name.Name] == nil {
							positions[function.Name.Name] = make(map[int]struct{})
						}
						if _, recorded := positions[function.Name.Name][parameter]; !recorded {
							positions[function.Name.Name][parameter] = struct{}{}
							changed = true
						}
					}
					return true
				})
			}
		}
	}
	derived := make(map[string][]int, len(positions))
	for name, indexes := range positions {
		for index := range indexes {
			derived[name] = append(derived[name], index)
		}
		sort.Ints(derived[name])
	}
	return derived
}

// scopeArguments names the calls that *enforce* a scope, and which argument
// carries it.
//
// The collection used to record every `auth.Scope…` selector appearing anywhere in
// a handler or its callees, so it could not tell `h.authenticate(r, scope)` from a
// scope merely mentioned in a log line or read back out of a principal after
// authenticating on a weaker one. A handler that authenticated weakly and named a
// strong scope anywhere satisfied the table.
var scopeArguments = map[string][]int{
	"authenticate":             {1},
	"listEmoji":                {2},
	"deleteListItemsWithScope": {2},
}

// codeReturningFunctions return a code rather than writing one, so every string
// literal they return is a code they can emit.
var codeReturningFunctions = map[string]struct{}{
	"mapServiceErrorNamed": {}, "mapAdminError": {}, "postMessageError": {},
	"decodeErrorCode": {},
}

// handlerFacts reads every function declared in this package's source.
func handlerFacts(t *testing.T) map[string]functionFacts {
	t.Helper()
	_, parsed := parseHandlerSource(t)
	arguments := codeArguments(t)
	facts := make(map[string]functionFacts)
	for _, file := range parsed {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			collectFunctionFacts(function, arguments, facts)
		}
	}
	return facts
}

func collectFunctionFacts(function *ast.FuncDecl, arguments map[string][]int, facts map[string]functionFacts) {
	{
		name := function.Name.Name
		entry, exists := facts[name]
		if !exists {
			entry = functionFacts{codes: make(map[string]struct{}), scopes: make(map[string]struct{}), calls: make(map[string]struct{})}
		}
		_, returnsCode := codeReturningFunctions[name]
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CallExpr:
				callee := calleeName(value.Fun)
				if callee != "" {
					entry.calls[callee] = struct{}{}
				}
				for _, index := range arguments[callee] {
					if index < len(value.Args) {
						if code, ok := stringLiteral(value.Args[index]); ok && code != "" {
							entry.codes[code] = struct{}{}
						}
					}
				}
				// A scope is recorded where it is *enforced*: the argument
				// position of a call that refuses the request without it.
				for _, index := range scopeArguments[callee] {
					if index < len(value.Args) {
						if scope, ok := scopeSelector(value.Args[index]); ok {
							entry.scopes[scope] = struct{}{}
						}
					}
				}

			case *ast.CompositeLit:
				// A `{"ok": false, "error": "…"}` envelope, whether it is written
				// inline or assembled in a local first.
				for _, code := range envelopeCodes(value) {
					entry.codes[code] = struct{}{}
				}
				// A route may also enforce a scope without authenticate: reading
				// the principal from another authenticator and refusing with
				// missingScopeError is how /apps.connections.open does it. The
				// refusal is the enforcement, so the scope is recorded from it.
				if scope, ok := missingScopeLiteral(value); ok {
					entry.scopes[scope] = struct{}{}
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
}

// missingScopeLiteral reads `missingScopeError{needed: auth.Scope…}`.
func missingScopeLiteral(composite *ast.CompositeLit) (string, bool) {
	identifier, ok := composite.Type.(*ast.Ident)
	if !ok || identifier.Name != "missingScopeError" {
		return "", false
	}
	for _, element := range composite.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok || key.Name != "needed" {
			continue
		}
		return scopeSelector(pair.Value)
	}
	return "", false
}

// scopeSelector reads an `auth.Scope…` argument.
func scopeSelector(expression ast.Expr) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "auth" || !strings.HasPrefix(selector.Sel.Name, "Scope") {
		return "", false
	}
	return selector.Sel.Name, true
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
		"restricted_too_many":       "store.ErrScheduledMessageLimit / service.ErrScheduledTooMany, classified by sentinel",
		"socket_mode_unavailable":   "store.ErrSocketModeConnectionLimit, classified by sentinel",
		"token_expired":             "auth.ErrTokenExpired, classified by sentinel",
		"internal_error":            "store.ErrTransient / store.ErrMessageTimestampTaken, classified by sentinel: a storage-engine failure whose retry was exhausted, named apart from the unclassified fatal_error",
		"invalid_arg_name":          "the invalidReason default and decodeErrorCode's argument fallback",
		"invalid_form_data":         "decodeErrorCode's fallback for an unreadable request",
		"invalid_json":              "decodeErrorCode's fallback for a malformed JSON document",
		"request_timeout":           "decodeErrorCode's name for a request body past the size ceiling",
		"channel_not_found":         "postMessageError's name for store.ErrNotFound on a message write",
		"is_archived":               "postMessageError's name for service.ErrConversationAlreadyArchived on a message write",
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

// The scope collection must record what a route *enforces*, not what it mentions.
// It used to record every auth.Scope… selector anywhere in a handler or its
// callees, so a handler that authenticated on a weak scope and merely named a
// strong one — reading it back off the principal to decide whether to include an
// extra field, for instance — satisfied the scoped-route table as if it had
// enforced it.
func TestTheScopeCollectionRecordsEnforcementRatherThanMention(t *testing.T) {
	const source = `package slack

func (h Handler) mentionsWithoutEnforcing(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		return
	}
	if principal.HasScope(auth.ScopeAdminUsersWrite) {
		writeError(w, "no_permission")
	}
}
`
	parsed, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", source, 0)
	if err != nil {
		t.Fatalf("parse the synthetic handler: %v", err)
	}
	facts := make(map[string]functionFacts)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		collectFunctionFacts(function, codeWriters, facts)
	}
	scopes := sortedKeys(facts["mentionsWithoutEnforcing"].scopes)
	if len(scopes) != 1 || scopes[0] != "ScopeUsersRead" {
		t.Fatalf("the collection recorded %v; the handler enforces only ScopeUsersRead and mentions ScopeAdminUsersWrite", scopes)
	}
}

// nonCodePinnedLiterals are the string literals in this package that happen to
// equal a pinned Slack error code while not being one. Each entry states what the
// literal is instead, so the exemption cannot become a place to hide a code the
// scan cannot see.
func nonCodePinnedLiterals() map[string]string {
	return map[string]string{}
}

// The gates can only judge what the fact collection sees, and what it sees is a
// matter of shape: a code written as a literal at a known argument position. A
// code that reaches the wire any other way is invisible to *both* gates, which is
// silent — the tests stay green while an operation answers a code its pinned enum
// does not declare.
//
// So the collection is checked against the source directly: every string literal
// in this package that is a known error code — a member of the pinned union, or
// one of the recorded deviations — must either be visible to handlerFacts or be
// recorded below as something other than a code. This is what proves
// codeArguments' derivation reaches the codes passed through helper parameters,
// and it fails if a future helper, or a renamed local such as the `reason` the
// OAuth endpoints assemble, puts one somewhere the collection cannot follow.
func TestEveryPinnedCodeLiteralInThePackageIsVisibleToTheScan(t *testing.T) {
	pinned := pinnedErrorCodes(t)
	for code := range recordedNonPinnedCodes() {
		pinned[code] = struct{}{}
	}
	recorded := nonCodePinnedLiterals()
	visible := make(map[string]struct{})
	for _, entry := range handlerFacts(t) {
		for code := range entry.codes {
			visible[code] = struct{}{}
		}
	}
	fileSet, parsed := parseHandlerSource(t)
	invisible := make(map[string][]string)
	for _, file := range parsed {
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, ok := stringLiteral(literal)
			if !ok {
				return true
			}
			if _, isPinned := pinned[value]; !isPinned {
				return true
			}
			if _, seen := visible[value]; seen {
				return true
			}
			if _, excused := recorded[value]; excused {
				return true
			}
			invisible[value] = append(invisible[value], fileSet.Position(literal.Pos()).String())
			return true
		})
	}
	for _, code := range sortedKeys(func() map[string]struct{} {
		names := make(map[string]struct{}, len(invisible))
		for name := range invisible {
			names[name] = struct{}{}
		}
		return names
	}()) {
		t.Errorf("the pinned error code %q appears at %v but no gate can see it emitted; give it a shape the collection reads, or record it as something other than a code", code, invisible[code])
	}
	// An exemption whose literal is gone is stale, and one that has become
	// visible is simply wrong.
	for value, reason := range recorded {
		if _, seen := visible[value]; seen {
			t.Errorf("%q is recorded as %q but the scan now sees it emitted; delete the entry", value, reason)
		}
		if !strings.Contains(handlerSource(t), strconv.Quote(value)) {
			t.Errorf("%q is recorded as %q but no longer appears in the package; delete the entry", value, reason)
		}
	}
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
