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
	source, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	body := string(source)
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
func emittedErrorCodes(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	body := string(source)
	seen := make(map[string]struct{})
	collect := func(text string, patterns ...*regexp.Regexp) {
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(text, -1) {
				for _, group := range match[1:] {
					if group != "" {
						seen[group] = struct{}{}
					}
				}
			}
		}
	}
	collect(body,
		regexp.MustCompile(`writeError\(w, "([a-z_0-9]+)"\)`),
		regexp.MustCompile(`mapServiceError\(\w+, "([a-z_0-9]+)"\)`),
		regexp.MustCompile(`mapAdminError\(\w+, "([a-z_0-9]+)"\)`),
		regexp.MustCompile(`mapServiceErrorNamed\(\w+, "([a-z_0-9]+)", "([a-z_0-9]+)"\)`),
		regexp.MustCompile(`decodeFailure\("([a-z_0-9]+)"`),
		regexp.MustCompile(`reason = "([a-z_0-9]+)"`),
		regexp.MustCompile(`reason := "([a-z_0-9]+)"`),
		regexp.MustCompile(`writeJSON\(w, http\.Status\w+, map\[string\]any\{"ok": false, "error": "([a-z_0-9]+)"`),
	)
	// The three error-naming functions return their codes directly, so their bodies
	// are scanned for bare returns.
	returns := regexp.MustCompile(`return "([a-z_0-9]+)"`)
	for _, name := range []string{"mapServiceErrorNamed", "mapAdminError", "postMessageError"} {
		start := strings.Index(body, "func "+name+"(")
		if start < 0 {
			t.Fatalf("%s is missing from handler.go", name)
		}
		length := strings.Index(body[start:], "\n}\n")
		if length < 0 {
			t.Fatalf("cannot delimit %s", name)
		}
		collect(body[start:start+length], returns)
	}
	codes := make([]string, 0, len(seen))
	for code := range seen {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// pinnedErrorCodes is the union of every `error` enum in the pinned contract.
func pinnedErrorCodes(t *testing.T) map[string]struct{} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "specs", "upstream", "slack-api-specs", "web-api", "slack_web_openapi_v2.json"))
	if err != nil {
		t.Skipf("pinned contract unavailable: %v", err)
	}
	var document struct {
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
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse pinned contract: %v", err)
	}
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
		"comment_not_found":          "files.comments.delete declares comment_not_found, but only under that operation's own enum name",
		// OAuth 2.0 / OpenID Connect codes, governed by RFC 6749 rather than the
		// Slack snapshot. oauth.* and openid.connect.* declare no error enum.
		"invalid_client":         "RFC 6749 §5.2 token-endpoint error",
		"invalid_code":           "oauth.access legacy code rejection; the operation declares no enum",
		"invalid_request":        "RFC 6749 §5.2 token-endpoint error",
		"invalid_grant":          "RFC 6749 §5.2 token-endpoint error",
		"invalid_grant_type":     "oauth.access legacy grant rejection; the operation declares no enum",
		"invalid_refresh_token":  "oauth.access legacy refresh rejection; the operation declares no enum",
		"unsupported_grant_type": "RFC 6749 §5.2 token-endpoint error",
		"invalid_client_id":      "oauth.access legacy client rejection; the operation declares no enum",
		"invalid_team":           "admin.conversations.create declares invalid_team; setTeams/addTeams/session.invalidate declare no enum and reuse it",
		// Incoming webhooks answer plain text on hooks.slack.com, not a Web API method.
		"no_team":         "incoming-webhook plain-text contract, not a Web API method",
		"invalid_payload": "incoming-webhook plain-text contract, not a Web API method",
		// Recorded deviation: Socket Mode is optional in this deployment.
		"socket_mode_unavailable": "recorded deviation, and the only remaining non-200 JSON error status",
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
}

// service_unavailable was the fallback for every unclassified handled failure and
// appears in none of the pinned enums. Nothing may reintroduce it, and no handled
// path may answer with a non-200 status other than the one recorded deviation.
func TestTheHandlerNeitherEmitsServiceUnavailableNorNon200Errors(t *testing.T) {
	source, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	body := string(source)
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
