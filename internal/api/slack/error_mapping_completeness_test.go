package slack

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// exportedSentinels reports every exported error sentinel declared in a package
// whose name matches one of the prefixes. The list is derived from source rather
// than hand-maintained so that a newly declared sentinel cannot silently bypass
// the transport mapping below.
func exportedSentinels(t *testing.T, dir string, prefixes ...string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
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
					if !name.IsExported() {
						continue
					}
					for _, prefix := range prefixes {
						if strings.HasPrefix(name.Name, prefix) {
							names = append(names, name.Name)
							break
						}
					}
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatalf("no sentinels discovered under %s; the source scan is broken", dir)
	}
	sort.Strings(names)
	return names
}

// A handled error must be named. Routing one to the unclassified fallback tells the
// caller nothing about what to fix, so every transport-relevant sentinel has to be
// named by mapServiceErrorNamed. Reading handler.go keeps the check honest even
// when a sentinel is reachable only through a route this package does not
// exercise.
//
// This used to scan service.ErrInvalid* only, so every store.Err* sentinel — and
// every non-ErrInvalid service sentinel such as ErrMessageNotOwned,
// ErrEmojiAlreadyExists and ErrBlobUnavailable — escaped the net entirely.
func TestMapServiceErrorNamesEveryTransportRelevantSentinel(t *testing.T) {
	body := handlerSource(t)
	// Sentinels that describe storage-engine internals rather than a client-visible
	// outcome. Each entry states why the transport cannot name it.
	exempt := map[string]string{
		"ErrLeaseHeld":     "outbox lease contention is retried by the worker, never returned to an API caller",
		"ErrLeaseExpired":  "outbox lease expiry is retried by the worker, never returned to an API caller",
		"ErrLeaseConflict": "outbox lease conflict is retried by the worker, never returned to an API caller",
	}
	missing := make([]string, 0)
	for _, pkg := range []struct {
		name string
		dir  string
	}{
		{"service", filepath.Join("..", "..", "service")},
		{"store", filepath.Join("..", "..", "store")},
	} {
		for _, name := range exportedSentinels(t, pkg.dir, "Err") {
			if _, ok := exempt[name]; ok {
				continue
			}
			if !strings.Contains(body, pkg.name+"."+name+")") {
				missing = append(missing, pkg.name+"."+name)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("mapServiceError does not name %v; an unnamed sentinel falls through to the unclassified fallback", missing)
	}
}

// emittedErrorCodes collects every error code handler.go can put on the wire.
//
// This used to be eight regular expressions over the file text, which had to be
// edited every time a naming function changed shape — and silently stopped
// matching when one did. It reads the same parsed source the per-route gate
// reads, so the two cannot disagree about what the handler emits.
func emittedErrorCodes(t *testing.T) []string {
	t.Helper()
	seen := make(map[string]struct{})
	for _, entry := range handlerFacts(t) {
		for code := range entry.codes {
			seen[code] = struct{}{}
		}
	}
	if len(seen) < 50 {
		t.Fatalf("only %d codes discovered; the source scan is broken", len(seen))
	}
	return sortedKeys(seen)
}

// pinnedContract is the shape of the pinned snapshot this package reads.
type pinnedContract struct {
	Paths map[string]map[string]struct {
		Responses map[string]struct {
			Schema struct {
				Properties struct {
					Error struct {
						Enum []string `json:"enum"`
					} `json:"error"`
				} `json:"properties"`
			} `json:"schema"`
		} `json:"responses"`
	} `json:"paths"`
}

func pinnedDocument(t *testing.T) pinnedContract {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "specs", "upstream", "slack-api-specs", "web-api", "slack_web_openapi_v2.json"))
	if err != nil {
		t.Skipf("pinned contract unavailable: %v", err)
	}
	var document pinnedContract
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse pinned contract: %v", err)
	}
	return document
}

// pinnedOperationCodes is the `error` enum of every operation in the pinned
// contract, keyed by path. An operation absent from the map is absent from the
// snapshot; an operation present with an empty set declares no enum.
func pinnedOperationCodes(t *testing.T) map[string]map[string]struct{} {
	t.Helper()
	document := pinnedDocument(t)
	operations := make(map[string]map[string]struct{}, len(document.Paths))
	for path, verbs := range document.Paths {
		codes := make(map[string]struct{})
		for _, operation := range verbs {
			for _, value := range operation.Responses["default"].Schema.Properties.Error.Enum {
				codes[value] = struct{}{}
			}
		}
		operations[path] = codes
	}
	if len(operations) == 0 {
		t.Fatal("no operations found in the pinned contract; the scan is broken")
	}
	return operations
}

// pinnedErrorCodes is the union of every `error` enum in the pinned contract.
func pinnedErrorCodes(t *testing.T) map[string]struct{} {
	t.Helper()
	document := pinnedDocument(t)
	codes := make(map[string]struct{})
	for _, operations := range document.Paths {
		for _, operation := range operations {
			for _, value := range operation.Responses["default"].Schema.Properties.Error.Enum {
				codes[value] = struct{}{}
			}
		}
	}
	if len(codes) == 0 {
		t.Fatal("no error codes found in the pinned contract; the scan is broken")
	}
	return codes
}

// recordedNonPinnedCodes lists every emitted code that the pinned Slack snapshot
// does not declare, together with the reason. Before this check existed the handler
// emitted names such as `service_unavailable`, `stars_not_found`,
// `usergroups_unavailable`, `access_logs_unavailable` and `upload_failed`, none of
// which appears in any of the 174 pinned error enums, so no SDK could match them.
func recordedNonPinnedCodes() map[string]string {
	return map[string]string{
		// Surfaces the pinned snapshot does not describe, or that declare no enum.
		"cant_delete_primary_owner":  "the pinned snapshot declares no owner-protection code for admin.users.*; this names the real cause rather than reporting a permission failure the actor does not have",
		"canvas_not_found":           "canvases.* is absent from the pinned snapshot",
		"list_not_found":             "slackLists.* is absent from the pinned snapshot",
		"call_not_found":             "calls.* declares no error enum",
		"bookmark_not_found":         "bookmarks.* is absent from the pinned snapshot",
		"too_many_bookmarks":         "bookmarks.* is absent from the pinned snapshot",
		"remote_file_not_found":      "files.remote.* declares no error enum",
		"remote_files_unavailable":   "files.remote.list declares no error enum",
		"remote_file_already_exists": "files.remote.add declares no error enum",
		"emoji_not_found":            "admin.emoji.* declares no error enum",
		"emoji_already_exists":       "admin.emoji.add declares no error enum",
		"invite_request_not_found":   "admin.inviteRequests.* declares no error enum",
		"app_not_found":              "admin.apps.* declares no error enum",
		"usergroup_not_found":        "no pinned enum declares a subteam-not-found code, not even no_such_subteam",
		"view_not_found":             "views.* declares no error enum",
		"invalid_view":               "views.* declares no error enum",
		"hash_conflict":              "views.* declares no error enum",
		"file_storage_unavailable":   "blob-store outage; no pinned enum declares a storage-outage code",
		// OAuth 2.0 / OpenID Connect codes, governed by RFC 6749 rather than the
		// Slack snapshot. oauth.* and openid.connect.* declare no error enum.
		"invalid_client":         "RFC 6749 §5.2 token-endpoint error",
		"invalid_code":           "oauth.access legacy code rejection; the operation declares no enum",
		"invalid_request":        "RFC 6749 §5.2 token-endpoint error",
		"invalid_grant":          "RFC 6749 §5.2 token-endpoint error",
		"invalid_grant_type":     "oauth.access legacy grant rejection; the operation declares no enum",
		"invalid_refresh_token":  "oauth.access legacy refresh rejection; the operation declares no enum",
		"unsupported_grant_type": "RFC 6749 §5.2 token-endpoint error",
		"token_expired":          "Slack authentication error for an access token whose explicit lifetime has elapsed",
		// The immutable legacy OpenAPI snapshot predates the App Manifest APIs.
		// These codes are declared by the current first-party method references
		// pinned in specs/compatibility.yaml.
		"invalid_app_id":   "current apps.manifest.* method references; absent from the legacy OpenAPI snapshot",
		"invalid_manifest": "current apps.manifest.* method references; absent from the legacy OpenAPI snapshot",
		"invalid_team_id":  "current apps.manifest.create method reference; absent from the legacy OpenAPI snapshot",
		"not_enabled":      "current views.publish method reference; returned when the app's Home tab is not enabled and absent from the legacy OpenAPI snapshot",
		"app_not_hosted":   "current apps.datastore.* method references; absent from the legacy OpenAPI snapshot",
		"datastore_error":  "current apps.datastore.* structured validation error; absent from the legacy OpenAPI snapshot",
		// Incoming webhooks answer plain text on hooks.slack.com, not a Web API method.
		"no_team":             "incoming-webhook plain-text contract, not a Web API method",
		"invalid_payload":     "incoming-webhook plain-text contract, not a Web API method",
		"channel_is_archived": "incoming-webhook plain-text contract; current Slack documentation requires HTTP 410",
		// files.completeUploadExternal is a current-reference method absent from
		// the immutable legacy OpenAPI snapshot.
		"posting_to_channel_denied": "current files.completeUploadExternal method reference; archived channels cannot accept the generated file-share message",
		"restricted_too_many":       "current chat.scheduleMessage method reference; the immutable legacy OpenAPI snapshot predates the documented 30-messages-per-five-minute restriction",
		"no_query":                  "current search.* method references require query; the immutable legacy OpenAPI snapshot omits the Web API error enum",
		// Recorded deviation: Socket Mode is optional in this deployment.
		"socket_mode_unavailable": "recorded deviation, and the only remaining non-200 JSON error status",
		// Recorded deviation: the snapshot describes no routing failure at all, so
		// there is no pinned code for an unknown method name or a verb no route
		// declares. net/http.ServeMux answered both with text/plain at a non-200
		// status, which no SDK can parse. `unknown_method` is the name Slack itself
		// uses for the case.
		"unknown_method": "the pinned snapshot declares no routing error code; this is the name Slack uses for an unrecognised method",
	}
}

// Every error code this transport emits must either appear in the pinned contract
// or be recorded above with the reason it cannot.
func TestEveryEmittedErrorCodeIsPinnedOrRecorded(t *testing.T) {
	pinned := pinnedErrorCodes(t)
	recorded := recordedNonPinnedCodes()
	emitted := emittedErrorCodes(t)
	unknown := make([]string, 0)
	for _, code := range emitted {
		if _, ok := pinned[code]; ok {
			continue
		}
		if _, ok := recorded[code]; ok {
			continue
		}
		unknown = append(unknown, code)
	}
	if len(unknown) > 0 {
		t.Errorf("these error codes are in no pinned enum and are not recorded: %v", unknown)
	}
	// A recorded entry that is no longer emitted is stale and must be removed, so the
	// list cannot drift into a permanent excuse.
	live := make(map[string]struct{}, len(emitted))
	for _, code := range emitted {
		live[code] = struct{}{}
	}
	stale := make([]string, 0)
	for code := range recorded {
		if _, ok := live[code]; !ok {
			stale = append(stale, code)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("recorded deviations no longer emitted: %v", stale)
	}
	// A recorded entry claiming a code is absent from the snapshot, when the
	// snapshot declares it, is simply false. The staleness check above cannot see
	// such an entry because it only looks at what is still emitted, which is how
	// `comment_not_found`, `invalid_client_id` and `invalid_team` sat here while
	// /files.comments.delete, /apps.uninstall and /admin.conversations.create
	// declared all three.
	false_ := make([]string, 0)
	for code := range recorded {
		if _, ok := pinned[code]; ok {
			false_ = append(false_, code)
		}
	}
	sort.Strings(false_)
	if len(false_) > 0 {
		t.Errorf("these codes are recorded as unpinned but the snapshot declares them; delete the entries: %v", false_)
	}
}

// service_unavailable was the fallback for every unclassified handled failure and
// appears in none of the pinned enums. Nothing may reintroduce it, and no handled
// path may answer with a non-200 status other than the one recorded deviation.
// The text is every source file of the package, not handler.go alone, so moving a
// writer into a new file does not move it out of this check.
func TestTheHandlerNeitherEmitsServiceUnavailableNorNon200Errors(t *testing.T) {
	body := handlerSource(t)
	if strings.Contains(body, `"service_unavailable"`) {
		t.Error(`handler.go emits "service_unavailable", which is in none of the 174 pinned error enums`)
	}
	pattern := regexp.MustCompile(`writeJSON\(w, http\.Status(?:BadRequest|NotFound|Forbidden|Conflict|ServiceUnavailable|Unauthorized|InternalServerError), map\[string\]any\{"ok": false`)
	for _, match := range pattern.FindAllString(body, -1) {
		if strings.Contains(match, "ServiceUnavailable") && strings.Contains(body, "socket_mode_unavailable") {
			continue
		}
		t.Errorf("handled error written with a non-200 status: %s", match)
	}
	// Exactly one non-200 JSON error remains, and it is the recorded Socket Mode
	// deviation.
	if count := strings.Count(body, `writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false`); count != 1 {
		t.Errorf("found %d non-200 JSON error writers, want exactly the recorded socket_mode_unavailable one", count)
	}
}

// TestEveryRouteEmitsOnlyCodesItsOwnOperationDeclares is the per-operation gate.
//
// TestEveryEmittedErrorCodeIsPinnedOrRecorded above checks the *union* of all 99
// pinned enums, so a code declared anywhere passed everywhere. That is why a
// duplicate pin answered `already_reacted`, chat.unfurl answered
// `message_not_found`, dnd.setSnooze answered `invalid_arguments` and
// conversations.replies answered `invalid_ts_latest` — every one of those codes
// is a member of the union and none is a member of the operation's own enum.
//
// This walks Register, resolves each route to its handler function, follows the
// helpers that function calls, and compares the resulting code set with that one
// operation's enum.
func TestEveryRouteEmitsOnlyCodesItsOwnOperationDeclares(t *testing.T) {
	operations := pinnedOperationCodes(t)
	facts := handlerFacts(t)
	recorded := recordedNonPinnedCodes()
	global := globallyExemptCodes()
	perRoute := perOperationExemptions()
	covered := make(map[string]map[string]struct{})
	for _, route := range registeredRoutes(t) {
		declared, described := operations[route.operation()]
		if !described || len(declared) == 0 {
			// The operation is absent from the snapshot or declares no enum, so
			// there is nothing to compare against.
			continue
		}
		exempt := perRoute[route.operation()]
		for _, code := range sortedKeys(reachableCodes(facts, route.handler)) {
			if _, ok := declared[code]; ok {
				continue
			}
			if _, ok := global[code]; ok {
				continue
			}
			if _, ok := recorded[code]; ok {
				continue
			}
			if _, ok := exempt[code]; ok {
				if covered[route.operation()] == nil {
					covered[route.operation()] = make(map[string]struct{})
				}
				covered[route.operation()][code] = struct{}{}
				continue
			}
			t.Errorf("%s emits %q, which its own pinned enum does not declare", route.operation(), code)
		}
	}
	// An exemption that is no longer reachable is stale and must be deleted, so
	// the table cannot become a permanent excuse the way the union check was.
	for operation, codes := range perRoute {
		for code := range codes {
			if _, ok := covered[operation][code]; !ok {
				t.Errorf("exemption %s/%s is no longer emitted; delete it", operation, code)
			}
		}
	}
}

// globallyExemptCodes are the request-decoding codes. One decoder reads every
// request before any operation-specific code runs — charset, content type, JSON
// shape, duplicate form field, list argument, limit, cursor — so these codes are
// reachable from every route by construction. The snapshot omits them from a
// minority of operations while declaring their immediate siblings, and in three
// places misspells them outright (`/pins.remove` declares `invalid_post_typ`,
// `missing_post_typ` and `request_timeou`), which is a snapshot defect rather
// than a transport one. Exempting them here keeps the gate pointed at the codes a
// route actually chooses.
func globallyExemptCodes() map[string]struct{} {
	return map[string]struct{}{
		"invalid_charset":   {},
		"invalid_form_data": {},
		"invalid_post_type": {},
		"missing_post_type": {},
		"invalid_json":      {},
		"json_not_object":   {},
		"invalid_array_arg": {},
		"invalid_arg_name":  {},
		"request_timeout":   {},
	}
}

// perOperationExemptions records, per pinned operation path, a code that route
// emits which the operation's own enum omits, together with the reason. Each one
// is a place where the snapshot declares no code for a failure the transport can
// genuinely reach; the alternative is answering with a declared code that names
// the wrong cause.
func perOperationExemptions() map[string]map[string]string {
	workspaceUnreadable := "the operation's enum declares no code for `the workspace behind an authenticated principal could not be read`; team_not_found names the real cause"
	return map[string]map[string]string{
		"/auth.test":                  {"team_not_found": workspaceUnreadable},
		"/team.info":                  {"team_not_found": workspaceUnreadable},
		"/rtm.connect":                {"team_not_found": workspaceUnreadable, "user_not_found": "rtm.connect resolves the connecting user and its enum declares no missing-user code"},
		"/conversations.list":         {"team_not_found": workspaceUnreadable},
		"/users.conversations":        {"team_not_found": workspaceUnreadable},
		"/users.deletePhoto":          {"user_not_found": "the enum declares no missing-user code, though the operation is defined entirely in terms of a user"},
		"/users.profile.set":          {"user_not_found": "the enum declares reserved_name, invalid_profile and profile_set_failed but no missing-user code"},
		"/files.upload":               {"fatal_error": "the enum declares no server-side failure code; /users.setPhoto, which shares this spool, declares fatal_error", "file_not_found": "the enum declares no not-found code for the workspace the upload is written to"},
		"/admin.conversations.search": {"fatal_error": "this enum is one of the short admin.* lists and declares no server-side failure code"},
	}
}
