package slack

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testHandler() http.Handler {
	handler, _ := testHandlerWithStore()
	return handler
}

func TestBlocksValidateMatchesCurrentSlackResponseShapes(t *testing.T) {
	call := func(body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/blocks.validate", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		testHandler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	valid := call(`{"blocks":[{"type":"section","text":{"type":"plain_text","text":"Hello world"}}]}`)
	if valid["ok"] != true {
		t.Fatalf("valid=%v", valid)
	}
	invalid := call(`{"blocks":[{"type":"section","text":{"type":"invalid","text":"Hello world"}}]}`)
	errors, _ := invalid["errors"].([]any)
	problem, _ := errors[0].(map[string]any)
	if invalid["ok"] != false || invalid["error"] != "invalid_blocks" ||
		problem["pointer"] != "/0/text/type" || problem["code"] != "failed_constraint" {
		t.Fatalf("invalid=%v", invalid)
	}
	ambiguous := call(`{"blocks":[],"message":{"blocks":[]}}`)
	metadata, _ := ambiguous["response_metadata"].(map[string]any)
	if ambiguous["error"] != "invalid_arguments" || metadata["messages"] == nil {
		t.Fatalf("ambiguous=%v", ambiguous)
	}
}

func TestConversationsCanvasesCreateMatchesSlackSingularChannelCanvas(t *testing.T) {
	handler := testHandler()
	call := func(path string, form url.Values) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	created := call("/api/conversations.canvases.create", url.Values{
		"channel_id": {"C1"}, "title": {"Channel notes"},
		"document_content": {`{"type":"markdown","markdown":"hello"}`},
	})
	canvasID, _ := created["canvas_id"].(string)
	if created["ok"] != true || canvasID == "" {
		t.Fatalf("created=%v", created)
	}
	info := call("/api/conversations.info", url.Values{"channel": {"C1"}})
	channel, _ := info["channel"].(map[string]any)
	properties, _ := channel["properties"].(map[string]any)
	canvas, _ := properties["canvas"].(map[string]any)
	if canvas["file_id"] != canvasID || canvas["is_empty"] != false {
		t.Fatalf("conversation canvas projection=%v", info)
	}
	duplicate := call("/api/conversations.canvases.create", url.Values{"channel_id": {"C1"}})
	if duplicate["ok"] != false || duplicate["error"] != "channel_canvas_already_exists" {
		t.Fatalf("duplicate=%v", duplicate)
	}
}

func TestAppsConnectionsOpenUsesAppTokenAndCreatesSingleUseConnection(t *testing.T) {
	store := memory.New()
	store.SeedAppToken(context.Background(), "xapp-test", domain.AppTokenRecord{AppID: "A1", Scopes: []string{string(auth.ScopeConnectionsWrite)}})
	userAuth, err := auth.NewStatic("user-token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChatWrite: {}}})
	if err != nil {
		t.Fatal(err)
	}
	appAuth, err := auth.NewAppStored(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, userAuth)
	if err != nil {
		t.Fatal(err)
	}
	handler.ConfigureSocketMode(socketmode.Service{Store: store, Host: "example.test"}, appAuth)
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/apps.connections.open", nil)
	request.Header.Set("Authorization", "Bearer xapp-test")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var body struct {
		OK  bool   `json:"ok"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || !strings.Contains(body.URL, "connection_id=") {
		t.Fatalf("body=%+v", body)
	}
}

func TestOpenIDConnectMethodsExchangeAndReturnUserInfo(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test", Domain: "test.example"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com"})
	ctx := context.Background()
	if err := store.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOAuthCode(ctx, domain.OAuthCode{Code: "openid-code", ClientID: "client", WorkspaceID: "T1", UserID: "U1", Scopes: append(auth.AllScopes(), "openid"), RedirectURI: "https://callback"}); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/openid.connect.token", strings.NewReader("client_id=client&client_secret=secret&code=openid-code&redirect_uri=https%3A%2F%2Fcallback&grant_type=authorization_code"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("token status=%d body=%s", response.Code, response.Body)
	}
	var token struct {
		OK           bool   `json:"ok"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	if !token.OK || token.AccessToken == "" || token.RefreshToken == "" {
		t.Fatalf("token=%+v", token)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/openid.connect.userInfo", strings.NewReader("token="+url.QueryEscape(token.AccessToken)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("userinfo status=%d body=%s", response.Code, response.Body)
	}
	var info map[string]any
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info["ok"] != true || info["sub"] != "U1" || info["https://slack.com/team_id"] != "T1" {
		t.Fatalf("userinfo=%v", info)
	}
}

// defaultTestScopes is the broad grant most tests rely on. It exists as a named
// value so that a scope-enforcement test can subtract exactly one scope from it,
// and so testHandlerWithScopes can build a deliberately narrow token.
func defaultTestScopes() []auth.Scope {
	return []auth.Scope{auth.ScopeChatWrite, auth.ScopeChannelsHistory, auth.ScopeRTMStream, auth.ScopeUsersRead, auth.ScopeUsersReadEmail, auth.ScopeUsersWrite, auth.ScopeUsersProfileRead, auth.ScopeUsersProfileWrite, auth.ScopeChannelsRead, auth.ScopeChannelsJoin, auth.ScopeChannelsWrite, auth.ScopeChannelsManage, auth.ScopeChannelsWriteInvites, auth.ScopeGroupsWrite, auth.ScopeGroupsWriteInvites, auth.ScopeIMWrite, auth.ScopeMPIMWrite, auth.ScopeReactionsWrite, auth.ScopeReactionsRead, auth.ScopePinsWrite, auth.ScopePinsRead, auth.ScopeBookmarksRead, auth.ScopeBookmarksWrite, auth.ScopeSearchRead, auth.ScopeFilesRead, auth.ScopeFilesWrite, auth.ScopeRemoteFilesRead, auth.ScopeRemoteFilesWrite, auth.ScopeRemoteFilesShare, auth.ScopeTeamRead, auth.ScopeTeamPreferencesRead, auth.ScopeEmojiRead, auth.ScopeAuthorizationsRead, auth.ScopeLinksWrite, auth.ScopeIdentityBasic, auth.ScopeDNDRead, auth.ScopeDNDWrite, auth.ScopeStarsRead, auth.ScopeStarsWrite, auth.ScopeRemindersRead, auth.ScopeRemindersWrite, auth.ScopeUserGroupsRead, auth.ScopeUserGroupsWrite, auth.ScopeCallsRead, auth.ScopeCallsWrite, auth.ScopeWorkflowStepsExecute, auth.ScopeTriggersRead, auth.ScopeTriggersWrite, auth.ScopeTokensBasic, auth.ScopeDatastoreRead, auth.ScopeDatastoreWrite, auth.ScopeAdmin, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite, auth.ScopeAdminInvitesRead, auth.ScopeAdminInvitesWrite, auth.ScopeAdminConversationsRead, auth.ScopeAdminConversationsWrite, auth.ScopeAdminUserGroupsRead, auth.ScopeAdminUserGroupsWrite, auth.ScopeAdminTeamsRead, auth.ScopeAdminTeamsWrite, auth.ScopeAdminAppsRead, auth.ScopeAdminAppsWrite, auth.ScopeAdminWorkflowsRead, auth.ScopeAdminWorkflowsWrite, auth.ScopeAdminRolesRead, auth.ScopeAdminRolesWrite, auth.ScopeAdminBarriersRead, auth.ScopeAdminBarriersWrite, auth.ScopeAdminAnalyticsRead, auth.ScopeAuditLogsRead, auth.ScopeCanvasesRead, auth.ScopeCanvasesWrite, auth.ScopeListsRead, auth.ScopeListsWrite}
}

func testHandlerWithStore() (http.Handler, *memory.Store) {
	return testHandlerWithScopes(defaultTestScopes()...)
}

// testHandlerWithScopes seeds the shared fixture but grants the API token only
// the named scopes. Without it every request in this package arrived holding
// every scope the system knows about, so a handler that enforced no scope at all
// was indistinguishable from one that enforced the right one.
func testHandlerWithScopes(scopes ...auth.Scope) (http.Handler, *memory.Store) {
	return testFixture(false, scopes...)
}

// testHandlerWithStoredTokenAuth builds the fixture around auth.Stored, the
// authenticator that reads the legacy `token` form field. auth.Static ignores the
// token's placement, so it cannot exercise the multipart-body placement the pinned
// /files.upload and /users.setPhoto declare.
func testHandlerWithStoredTokenAuth(scopes ...auth.Scope) (http.Handler, *memory.Store) {
	return testFixture(true, scopes...)
}

func testFixture(stored bool, scopes ...auth.Scope) (http.Handler, *memory.Store) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:"}})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	// U1 is this fixture's workspace administrator: most tests here exercise
	// admin.* operations, which now require the actor's ROLE and not merely an
	// admin.* token scope. U2 stays a member so a denial can be observed.
	if err := s.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		panic(err)
	}
	now := time.Now().UTC()
	if err := s.CreateApp(context.Background(), domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Fixture app", ClientID: "fixture-app-client",
		SigningSecretHash: "fixture-signing-hash", SigningSecretCiphertext: "fixture-signing-ciphertext",
		VerificationTokenHash: "fixture-verification-hash", VerificationTokenCiphertext: "fixture-verification-ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1,
		Manifest:  `{"display_information":{"name":"Fixture app"},"features":{"app_home":{"home_tab_enabled":true,"messages_tab_enabled":true}},"settings":{"function_runtime":"remote"},"functions":{"triage":{"title":"Triage","description":"Triage an item","input_parameters":{"properties":{"item":{"type":"string","title":"Item"}},"required":["item"]},"output_parameters":{"properties":{"result":{"type":"string","title":"Result"}},"required":["result"]}}}}`,
		CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "fixture-app-client", SecretHash: "fixture-client-secret-hash", AppID: "A1"}); err != nil {
		panic(err)
	}
	if err := s.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		panic(err)
	}
	if err := s.CreateBot(context.Background(), domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "U2", Name: "testbot", UpdatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := s.CreateUserMigration(context.Background(), domain.UserMigration{WorkspaceID: "T1", OldID: "U1", GlobalID: "W1"}, events.Event{ID: "EM1", WorkspaceID: "T1", Topic: "user.migration_created", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	// Built through events.New so the fixture carries the same self-describing
	// payload the service actually emits. Hand-writing `Payload: "A1"` described
	// an event no producer has ever emitted, which is why integration-log
	// attribution could pass here while being unreadable in production.
	approval, err := events.New("EAPP1", "T1", "U1", events.NewPayload("app.approved", events.String("app_id", "A1"), events.String("app_request_id", "R1")), time.Now().UTC())
	if err != nil {
		panic(err)
	}
	if err := s.SetAppApproval(context.Background(), "T1", "A1", "R1", domain.AppApprovalApproved, time.Now().UTC(), approval); err != nil {
		panic(err)
	}
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "archived", Archived: true})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	s.SeedConversationMember("C2", "U1")
	s.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: auth.AllScopes()})
	if err := s.CreateOAuthClient(context.Background(), domain.OAuthClient{ID: "oauth-client", SecretHash: domain.HashToken("oauth-secret"), AppID: "A1"}); err != nil {
		panic(err)
	}
	if err := s.CreateOAuthCode(context.Background(), domain.OAuthCode{Code: "oauth-code", ClientID: "oauth-client", WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}, UserScopes: []string{"chat:write"}, BotID: "B1", BotUserID: "U2", BotScopes: []string{"chat:write"}, RedirectURI: "https://callback"}); err != nil {
		panic(err)
	}
	if err := s.CreateFile(context.Background(), domain.File{ID: "F1", WorkspaceID: "T1", Uploader: "U1", Name: "file.txt", BlobKey: "blob", CreatedAt: time.Now().UTC()}, events.Event{ID: "EF1", WorkspaceID: "T1", Topic: "file.created", Payload: "F1", CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	s.SeedFileComment(domain.FileComment{ID: "FC1", File: "F1", WorkspaceID: "T1", UserID: "U1", Text: "comment", CreatedAt: time.Now().UTC()})
	granted := make(map[auth.Scope]struct{}, len(scopes))
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		granted[scope] = struct{}{}
		names = append(names, string(scope))
	}
	var authenticator auth.Authenticator
	if stored {
		if err := s.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: names}); err != nil {
			panic(err)
		}
		value, err := auth.NewStored(s)
		if err != nil {
			panic(err)
		}
		authenticator = value
	} else {
		value, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", AppID: "A1", BotID: "B1", TokenType: "bot", Scopes: granted})
		if err != nil {
			panic(err)
		}
		authenticator = value
	}
	h, err := NewHandler(service.Messages{Store: s}, authenticator)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, s
}

func TestListDownloadStreamsCSVAndPreservesArchiveOption(t *testing.T) {
	handler := testHandler()
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	var created struct {
		OK   bool `json:"ok"`
		List struct {
			ID string `json:"id"`
		} `json:"list"`
	}
	if result := post("/api/slackLists.create", "name=csv-list"); result.Code != http.StatusOK || json.NewDecoder(result.Body).Decode(&created) != nil || !created.OK {
		t.Fatalf("create status=%d body=%s", result.Code, result.Body)
	}
	if result := post("/api/slackLists.items.create", "list_id="+url.QueryEscape(created.List.ID)+"&initial_fields=%5B%7B%22column_id%22%3A%22title%22%2C%22value%22%3A%22row%22%7D%5D"); result.Code != http.StatusOK {
		t.Fatalf("item status=%d body=%s", result.Code, result.Body)
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	if result := post("/api/slackLists.download.start", "list_id="+url.QueryEscape(created.List.ID)+"&include_archived=true"); result.Code != http.StatusOK || json.NewDecoder(result.Body).Decode(&started) != nil || started.JobID == "" {
		t.Fatalf("start status=%d body=%s", result.Code, result.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/internal/slack-lists/download.csv?list_id="+url.QueryEscape(created.List.ID)+"&job_id="+url.QueryEscape(started.JobID), nil)
	req.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != http.StatusOK || !strings.HasPrefix(result.Header().Get("Content-Type"), "text/csv") || !strings.Contains(result.Body.String(), "item_id,fields") || !strings.Contains(result.Body.String(), "row") {
		t.Fatalf("download status=%d headers=%v body=%s", result.Code, result.Header(), result.Body)
	}
}

func TestEntityMethodsAcceptStructuredWorkObjectPayloads(t *testing.T) {
	handler := testHandler()
	post := func(path string, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	if result := post("/api/entity.presentDetails", url.Values{
		"trigger_id":         {"details-trigger"},
		"metadata":           {`{"entity_type":"slack#/entities/file"}`},
		"user_auth_required": {"true"},
		"user_auth_url":      {"https://example.test/login"},
	}); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) {
		t.Fatalf("details status=%d body=%s", result.Code, result.Body)
	}
	if result := post("/api/entity.presentComments", url.Values{
		"trigger_id":       {"comments-trigger"},
		"comments":         {`[{"id":"comment-1","can_delete":true}]`},
		"delete_action_id": {"delete-comment"},
	}); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) {
		t.Fatalf("comments status=%d body=%s", result.Code, result.Body)
	}
	if result := post("/api/entity.acknowledgeCommentAction", url.Values{
		"trigger_id": {"ack-trigger"},
		"comment":    {`{"id":"comment-1","value":"saved"}`},
	}); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) {
		t.Fatalf("acknowledgement status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminInviteRequestLifecycle(t *testing.T) {
	handler, store := testHandlerWithStore()
	now := time.Now().UTC()
	for _, request := range []domain.InviteRequest{
		{ID: "IR-approve", WorkspaceID: "T1", Email: "approve@example.com", RequestedBy: "U1", Status: domain.InviteRequestPending, CreatedAt: now},
		{ID: "IR-deny", WorkspaceID: "T1", Email: "deny@example.com", RequestedBy: "U1", Status: domain.InviteRequestPending, CreatedAt: now},
	} {
		if err := store.CreateInviteRequest(context.Background(), request, events.Event{ID: domain.EventID("event-" + string(request.ID)), WorkspaceID: "T1", ActorID: "U1", Topic: "invite.requested", Payload: string(request.ID), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer token")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	if result := list("/api/admin.inviteRequests.list?team_id=T1&limit=10"); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "IR-approve") {
		t.Fatalf("pending status=%d body=%s", result.Code, result.Body)
	}
	change := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	if result := change("/api/admin.inviteRequests.approve", "team_id=T1&invite_request_id=IR-approve"); result.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", result.Code, result.Body)
	}
	if result := change("/api/admin.inviteRequests.deny", "team_id=T1&invite_request_id=IR-deny"); result.Code != http.StatusOK {
		t.Fatalf("deny status=%d body=%s", result.Code, result.Body)
	}
	if result := list("/api/admin.inviteRequests.approved.list?team_id=T1&limit=10"); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "IR-approve") {
		t.Fatalf("approved status=%d body=%s", result.Code, result.Body)
	}
	if result := list("/api/admin.inviteRequests.denied.list?team_id=T1&limit=10"); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "IR-deny") {
		t.Fatalf("denied status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminAppRequestsList(t *testing.T) {
	handler, store := testHandlerWithStore()
	now := time.Now().UTC()
	if err := store.SetAppApproval(context.Background(), "T1", "A2", "R2", domain.AppApprovalRequested, now, events.Event{ID: "event-app-request", WorkspaceID: "T1", ActorID: "U1", Topic: "app.requested", Payload: "A2", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin.apps.requests.list?team_id=T1&limit=10", nil)
	req.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "A2") {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminUsersSessionInvalidateRevokesSession(t *testing.T) {
	handler, store := testHandlerWithStore()
	if err := store.SeedSession(context.Background(), "session-1", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin.users.session.invalidate", strings.NewReader("team_id=T1&session_id=session-1"))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestDoNotDisturbEndClearsEnabledState(t *testing.T) {
	handler, store := testHandlerWithStore()
	now := time.Now().UTC()
	if err := store.SetDoNotDisturb(context.Background(), domain.DoNotDisturb{WorkspaceID: "T1", UserID: "U1", Enabled: true}, events.Event{ID: "event-dnd-enabled", WorkspaceID: "T1", ActorID: "U1", Topic: "user.dnd_enabled", Payload: "U1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/dnd.endDnd", nil)
	req.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/dnd.info", nil)
	info.Header.Set("Authorization", "Bearer token")
	infoResult := httptest.NewRecorder()
	handler.ServeHTTP(infoResult, info)
	if infoResult.Code != http.StatusOK || !strings.Contains(infoResult.Body.String(), `"dnd_enabled":false`) {
		t.Fatalf("info status=%d body=%s", infoResult.Code, infoResult.Body)
	}
}

func TestOAuthV2AccessHTTPExchangesCode(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/oauth.v2.access", strings.NewReader(url.Values{
		"client_id":     {"oauth-client"},
		"client_secret": {"oauth-secret"},
		"code":          {"oauth-code"},
		"redirect_uri":  {"https://callback"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var body struct {
		OK          bool   `json:"ok"`
		AccessToken string `json:"access_token"`
		AppID       string `json:"app_id"`
		BotUserID   string `json:"bot_user_id"`
		TokenType   string `json:"token_type"`
		AuthedUser  struct {
			ID          string `json:"id"`
			AccessToken string `json:"access_token"`
			Scope       string `json:"scope"`
			TokenType   string `json:"token_type"`
		} `json:"authed_user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || !strings.HasPrefix(body.AccessToken, "xoxb-") || body.AppID != "A1" || body.BotUserID != "U2" || body.TokenType != "bot" || body.AuthedUser.ID != "U1" || !strings.HasPrefix(body.AuthedUser.AccessToken, "xoxp-") || body.AuthedUser.Scope == "" || body.AuthedUser.TokenType != "user" {
		t.Fatalf("unexpected body: %s", response.Body)
	}
}

func TestOAuthV2UserAccessReturnsUserGrantOnly(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/oauth.v2.user.access", strings.NewReader(url.Values{
		"client_id":     {"oauth-client"},
		"client_secret": {"oauth-secret"},
		"code":          {"oauth-code"},
		"redirect_uri":  {"https://callback"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var body struct {
		OK          bool   `json:"ok"`
		AccessToken string `json:"access_token"`
		AuthedUser  struct {
			ID          string `json:"id"`
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
		} `json:"authed_user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.AccessToken != "" || body.AuthedUser.ID != "U1" || !strings.HasPrefix(body.AuthedUser.AccessToken, "xoxp-") || body.AuthedUser.TokenType != "user" {
		t.Fatalf("unexpected body: %s", response.Body)
	}
}

func TestBotIdentityAndEventAuthorizationsUseTheirRequiredTokenTypes(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "Ubot", WorkspaceID: "T1", Name: "app"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "Ubot")
	s.SeedConversationMember("C1", "U1")
	s.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()})
	s.SeedToken(ctx, "xoxb-test", domain.TokenRecord{
		WorkspaceID: "T1",
		UserID:      "Ubot",
		AppID:       "A1",
		BotID:       "B1",
		TokenType:   "bot",
		Scopes:      []string{"reactions:read"},
	})
	s.SeedToken(ctx, "xoxp-test", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user", Scopes: []string{"reactions:read"},
	})
	s.SeedAppToken(ctx, "xapp-test", domain.AppTokenRecord{AppID: "A1", Scopes: []string{string(auth.ScopeAuthorizationsRead)}})
	s.SeedAppToken(ctx, "xapp-narrow", domain.AppTokenRecord{AppID: "A1", Scopes: []string{string(auth.ScopeConnectionsWrite)}})
	event, err := events.New("EV1", "T1", "U1", events.NewPayload("reaction.added",
		events.String("channel_id", "C1"), events.String("user_id", "U1"),
		events.String("reaction", "wave"), events.String("ts", "1700000000.000001"),
	), time.Unix(1700000001, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStored(s)
	if err != nil {
		t.Fatal(err)
	}
	appAuthenticator, err := auth.NewAppStored(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler.ConfigureSocketMode(socketmode.Service{}, appAuthenticator)
	mux := http.NewServeMux()
	handler.Register(mux)

	eventContext, err := events.EventContext("A1", events.Record{Sequence: 1, Event: event})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path     string
		token    string
		expected []string
	}{
		{"/api/auth.test", "xoxb-test", []string{`"user_id":"Ubot"`, `"bot_id":"B1"`, `"is_enterprise_install":false`}},
		{"/api/apps.event.authorizations.list?event_context=" + url.QueryEscape(eventContext), "xapp-test", []string{`"user_id":"Ubot"`, `"is_bot":true`, `"user_id":"U1"`, `"is_bot":false`}},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("Authorization", "Bearer "+test.token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", test.path, response.Code, response.Body)
		}
		for _, fragment := range test.expected {
			if !strings.Contains(response.Body.String(), fragment) {
				t.Errorf("%s body=%s, want %s", test.path, response.Body, fragment)
			}
		}
	}

	missingScope := httptest.NewRequest(http.MethodGet, "/api/apps.event.authorizations.list?event_context="+url.QueryEscape(eventContext), nil)
	missingScope.Header.Set("Authorization", "Bearer xapp-narrow")
	missingScopeResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingScopeResponse, missingScope)
	if !strings.Contains(missingScopeResponse.Body.String(), `"error":"missing_scope"`) ||
		!strings.Contains(missingScopeResponse.Body.String(), `"needed":"authorizations:read"`) {
		t.Fatalf("app token missing authorizations:read body=%s", missingScopeResponse.Body)
	}
}

func TestChatStreamMethodsFollowOfficialBotTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "assistant"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "UBOT")
	repository.SeedConversationMember("C1", "U1")
	repository.SeedToken(ctx, "xoxb-stream", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "UBOT", AppID: "A1", BotID: "B1", TokenType: "bot",
		Scopes: []string{string(auth.ScopeChatWrite)},
	})
	repository.SeedToken(ctx, "xoxp-user", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user",
		Scopes: []string{string(auth.ScopeChatWrite)},
	})
	messages := service.Messages{Store: repository}
	parent, err := messages.Post(ctx, "T1", "U1", "C1", "Question", "", "")
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStored(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	call := func(token, path, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body)
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	refused := call("xoxp-user", "/api/chat.startStream", `{"channel":"C1","thread_ts":"`+string(domain.NewMessageTimestamp(parent.CreatedAt))+`"}`)
	if refused["ok"] != false || refused["error"] != "not_allowed_token_type" {
		t.Fatalf("user-token response=%v", refused)
	}
	started := call("xoxb-stream", "/api/chat.startStream", `{
		"channel":"C1","thread_ts":"`+string(domain.NewMessageTimestamp(parent.CreatedAt))+`",
		"recipient_team_id":"T1","recipient_user_id":"U1","markdown_text":"**Hello",
		"task_display_mode":"dense","username":"Answer bot","icon_emoji":":speech_balloon:"
	}`)
	if started["ok"] != true || started["channel"] != "C1" || started["ts"] == "" {
		t.Fatalf("start response=%v", started)
	}
	timestamp := started["ts"].(string)
	appended := call("xoxb-stream", "/api/chat.appendStream", `{
		"channel":"C1","ts":"`+timestamp+`","chunks":[
			{"type":"markdown_text","text":" world**"},
			{"type":"task_update","id":"answer","title":"Answer","status":"complete"}
		]
	}`)
	if appended["ok"] != true || appended["ts"] != timestamp {
		t.Fatalf("append response=%v", appended)
	}
	stopped := call("xoxb-stream", "/api/chat.stopStream", `{
		"channel":"C1","ts":"`+timestamp+`","markdown_text":" Done.",
		"blocks":[{"type":"context","elements":[{"type":"plain_text","text":"Final"}]}],
		"metadata":{"event_type":"answer","event_payload":{"id":"R1"}}
	}`)
	message, _ := stopped["message"].(map[string]any)
	metadata, _ := message["metadata"].(map[string]any)
	if stopped["ok"] != true || stopped["ts"] != timestamp ||
		message["text"] != "**Hello world** Done." || message["bot_id"] != "B1" ||
		message["username"] != "Answer bot" || metadata["event_type"] != "answer" {
		t.Fatalf("stop response=%v", stopped)
	}
	icons, _ := message["icons"].(map[string]any)
	if icons["emoji"] != ":speech_balloon:" {
		t.Fatalf("stop icons=%v", icons)
	}
	afterStop := call("xoxb-stream", "/api/chat.appendStream", `{"channel":"C1","ts":"`+timestamp+`","markdown_text":"late"}`)
	if afterStop["ok"] != false || afterStop["error"] != "message_not_in_streaming_state" {
		t.Fatalf("append after stop=%v", afterStop)
	}
}

func TestOAuthAccessAndTokenHTTPExchangeCodes(t *testing.T) {
	request := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{
			"client_id":     {"oauth-client"},
			"client_secret": {"oauth-secret"},
			"code":          {"oauth-code"},
			"redirect_uri":  {"https://callback"},
		}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		testHandler().ServeHTTP(response, request)
		return response
	}
	for _, path := range []string{"/api/oauth.access", "/api/oauth.token"} {
		response := request(path)
		if response.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body)
		}
		var body struct {
			OK          bool   `json:"ok"`
			AccessToken string `json:"access_token"`
			TeamID      string `json:"team_id"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("path=%s decode: %v", path, err)
		}
		if !body.OK || body.AccessToken == "" || body.TeamID != "T1" {
			t.Fatalf("path=%s body=%+v", path, body)
		}
	}
}

func TestRTMConnectReturnsDurableEventStreamURL(t *testing.T) {
	for _, method := range []string{"rtm.connect", "rtm.start"} {
		request := httptest.NewRequest(http.MethodGet, "/api/"+method+"?token=token", nil)
		request.Host = "chat.example.test"
		response := httptest.NewRecorder()
		testHandler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
		}
		var body struct {
			OK   bool   `json:"ok"`
			URL  string `json:"url"`
			Self struct {
				ID string `json:"id"`
			} `json:"self"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		streamURL, err := url.Parse(body.URL)
		if err != nil {
			t.Fatal(err)
		}
		if !body.OK || streamURL.Scheme != "ws" || streamURL.Host != "chat.example.test" || streamURL.Path != "/rtm" || streamURL.Query().Get("session_id") == "" || body.Self.ID != "U1" {
			t.Fatalf("%s unexpected body: %s", method, response.Body)
		}
	}
}

func TestTeamPreferencesListReturnsEnforcedWorkspacePolicies(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/team.preferences.list", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	for _, fragment := range []string{
		`"display_real_names":false`,
		`"disable_file_uploads":"allow_all"`,
		`"msg_edit_window_mins":0`,
		`"who_can_post_general":"everyone"`,
	} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("response does not contain %q: %s", fragment, response.Body)
		}
	}
}

func TestIntegrationLogsHTTPExposeActorAttribution(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/team.integrationLogs?token=token&app_id=A1&team_id=Tother", nil)
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var body struct {
		OK   bool `json:"ok"`
		Logs []struct {
			AppID  string `json:"app_id"`
			UserID string `json:"user_id"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || len(body.Logs) != 1 || body.Logs[0].AppID != "A1" || body.Logs[0].UserID != "U1" {
		t.Fatalf("unexpected body: %s", response.Body)
	}
}

func TestViewsHTTPExposeDurableOpenPushUpdateAndPublish(t *testing.T) {
	handler, store := testHandlerWithStore()
	seedHTTPInteractionTrigger(t, store, "trigger-1")
	seedHTTPInteractionTrigger(t, store, "trigger-2")
	form := func(path string, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer token")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	opened := form("/api/views.open", url.Values{"trigger_id": {"trigger-1"}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"First"},"blocks":[]}`}})
	if opened.Code != http.StatusOK {
		t.Fatalf("open status=%d body=%s", opened.Code, opened.Body)
	}
	var openedBody struct {
		View struct {
			ID   string `json:"id"`
			Hash string `json:"hash"`
		} `json:"view"`
	}
	if err := json.Unmarshal(opened.Body.Bytes(), &openedBody); err != nil || openedBody.View.ID == "" || openedBody.View.Hash == "" {
		t.Fatalf("open body=%s err=%v", opened.Body, err)
	}
	pushed := form("/api/views.push", url.Values{"trigger_id": {"trigger-2"}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"Second"},"blocks":[]}`}})
	if pushed.Code != http.StatusOK || !strings.Contains(pushed.Body.String(), openedBody.View.ID) {
		t.Fatalf("push status=%d body=%s", pushed.Code, pushed.Body)
	}
	updated := form("/api/views.update", url.Values{"view_id": {openedBody.View.ID}, "hash": {openedBody.View.Hash}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"Updated"},"blocks":[]}`}})
	if updated.Code != http.StatusOK || strings.Contains(updated.Body.String(), openedBody.View.Hash) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	published := form("/api/views.publish", url.Values{"user_id": {"U2"}, "view": {`{"type":"home","blocks":[]}`}})
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"type":"home"`) {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body)
	}
}

func seedHTTPInteractionTrigger(t *testing.T, target *memory.Store, plaintext string) {
	t.Helper()
	now := time.Now().UTC()
	err := target.CreateAppInteractionCapabilities(context.Background(),
		domain.AppTrigger{
			TokenHash: domain.HashToken(plaintext), AppID: "A1", WorkspaceID: "T1", UserID: "U1",
			CreatedAt: now, ExpiresAt: now.Add(3 * time.Second),
		},
		domain.AppResponseURL{
			TokenHash: domain.HashToken("response-" + plaintext), AppID: "A1", WorkspaceID: "T1", UserID: "U1",
			ConversationID: "C1", CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute), UsesRemaining: 5,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowMethodsHTTPPersistLifecycle(t *testing.T) {
	handler := testHandler()
	form := func(path string, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer token")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	configured := form("/api/workflows.updateStep", url.Values{"workflow_step_edit_id": {"edit-http"}, "inputs": {`{"name":"input"}`}, "outputs": {`[]`}, "step_name": {"Configured"}})
	if configured.Code != http.StatusOK {
		t.Fatalf("update step status=%d body=%s", configured.Code, configured.Body)
	}
	completed := form("/api/workflows.stepCompleted", url.Values{"workflow_step_execute_id": {"execute-http"}, "outputs": {`{"result":"ok"}`}})
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"ok":true`) {
		t.Fatalf("completed status=%d body=%s", completed.Code, completed.Body)
	}
	failed := form("/api/workflows.stepFailed", url.Values{"workflow_step_execute_id": {"execute-failed"}, "error": {`{"message":"nope"}`}})
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), `"ok":true`) {
		t.Fatalf("failed status=%d body=%s", failed.Code, failed.Body)
	}
}

func TestFunctionsCompleteSuccessHTTPValidatesAndCompletes(t *testing.T) {
	handler, repository := testHandlerWithStore()
	seedFunctionExecution(t, repository, "execution-http")
	request := httptest.NewRequest(http.MethodPost, "/api/functions.completeSuccess", strings.NewReader(url.Values{
		"function_execution_id": {"execution-http"},
		"outputs":               {`{"result":"ok"}`},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/api/functions.completeSuccess", strings.NewReader(url.Values{
		"function_execution_id": {"execution-http"},
		"outputs":               {`[]`},
	}.Encode()))
	invalid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalid.Header.Set("Authorization", "Bearer token")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	// `invalid_arguments` appears in only 4 of the 174 pinned enums, and
	// functions.completeSuccess is not one of them (it is absent from the snapshot
	// entirely). `invalid_arg_name` is the code the pinned snapshot declares for a
	// rejected argument on every operation that declares an enum at all.
	if invalidResponse.Code != http.StatusOK || !strings.Contains(invalidResponse.Body.String(), `"error":"invalid_arg_name"`) {
		t.Fatalf("invalid status=%d body=%s", invalidResponse.Code, invalidResponse.Body)
	}
}

func TestFunctionsCompleteErrorHTTPValidatesAndCompletes(t *testing.T) {
	handler, repository := testHandlerWithStore()
	seedFunctionExecution(t, repository, "execution-error-http")
	request := httptest.NewRequest(http.MethodPost, "/api/functions.completeError", strings.NewReader(url.Values{
		"function_execution_id": {"execution-error-http"},
		"error":                 {"function failed"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func seedFunctionExecution(t *testing.T, repository *memory.Store, executionID domain.WorkflowStepID) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	workflowID := domain.WorkflowID("workflow-" + string(executionID))
	runID := domain.WorkflowRunID("run-" + string(executionID))
	workflow := domain.WorkflowDefinition{
		ID: workflowID, WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", Title: "HTTP workflow",
		InputSchema: `{}`, Steps: `[{"function_id":"callback"}]`, Status: domain.WorkflowPublished,
		Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflow(ctx, workflow, events.Event{ID: domain.EventID("workflow-" + string(executionID)), WorkspaceID: "T1", Topic: "workflow.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := domain.WorkflowRun{
		ID: runID, WorkflowID: workflowID, WorkflowVersion: 1, WorkspaceID: "T1", AppID: "A1",
		ActorID: "U1", Status: domain.WorkflowRunRunning, Inputs: `{}`, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	step := domain.WorkflowStep{
		ID: executionID, WorkflowRunID: runID, WorkspaceID: "T1", AppID: "A1", UserID: "U1",
		FunctionID: "FnCallback", EditID: "callback", Status: domain.WorkflowStepExecuting,
		Inputs: `{}`, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflowRun(ctx, run, &step, []events.Event{{ID: domain.EventID("run-" + string(executionID)), WorkspaceID: "T1", Topic: "workflow.run_started", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowTriggerWebhookRunsOnlyWithThePathSecret(t *testing.T) {
	handler, repository := testHandlerWithStore()
	ctx := context.Background()
	now := time.Now().UTC()
	workflow := domain.WorkflowDefinition{
		ID: "WfHook", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", CallbackID: "hook-workflow",
		Title: "Hook workflow", InputSchema: `{}`, Steps: `[{"function_id":"triage"}]`,
		Status: domain.WorkflowPublished, Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflow(ctx, workflow, events.Event{ID: "workflow-hook", WorkspaceID: "T1", Topic: "workflow.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	trigger := domain.WorkflowTrigger{
		ID: "FtHook", WorkflowID: workflow.ID, WorkspaceID: "T1", AppID: "A1", Title: "Hook",
		Type: "webhook", Config: `{"webhook_secret_hash":"` + domain.HashToken("hook-secret") + `"}`,
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SetWorkflowTrigger(ctx, trigger, events.Event{ID: "trigger-hook", WorkspaceID: "T1", Topic: "workflow.trigger_created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	base := "/services/triggers/T1/FtHook/"
	for _, tc := range []struct{ name, path, body string }{
		{"wrong secret", base + "wrong-secret", `{"item":"a"}`},
		{"unknown trigger", "/services/triggers/T1/FtMissing/hook-secret", `{"item":"a"}`},
		{"unknown workspace", "/services/triggers/T9/FtHook/hook-secret", `{"item":"a"}`},
		{"bad payload", base + "hook-secret", `["not-an-object"]`},
	} {
		result := post(tc.path, tc.body)
		want := http.StatusNotFound
		if tc.name == "bad payload" {
			want = http.StatusBadRequest
		}
		if result.Code != want {
			t.Fatalf("%s status=%d body=%s, want %d", tc.name, result.Code, result.Body, want)
		}
	}
	empty := post(base+"hook-secret", "")
	if empty.Code != http.StatusBadRequest {
		// An empty body is a valid invocation with empty inputs, not an error.
		if empty.Code != http.StatusOK || empty.Body.String() != "ok" {
			t.Fatalf("empty body status=%d body=%s", empty.Code, empty.Body)
		}
	}
	invoked := post(base+"hook-secret", `{"item":"from-hook"}`)
	if invoked.Code != http.StatusOK || invoked.Body.String() != "ok" {
		t.Fatalf("invoke status=%d body=%s", invoked.Code, invoked.Body)
	}
	runs, _, _, err := repository.ListWorkflowRuns(ctx, "T1", workflow.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(runs) == 0 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	var hooked *domain.WorkflowRun
	for index := range runs {
		if runs[index].Inputs == `{"item":"from-hook"}` {
			hooked = &runs[index]
		}
	}
	if hooked == nil {
		t.Fatalf("no run carried the webhook inputs: %+v", runs)
	}
	if hooked.TriggerID != trigger.ID || hooked.ActorID != "U1" || hooked.Status != domain.WorkflowRunRunning {
		t.Fatalf("webhook run=%+v", *hooked)
	}
}

func TestCurrentWorkflowPermissionFeaturedAndStepMethodsAreDurable(t *testing.T) {
	handler, repository := testHandlerWithStore()
	ctx := context.Background()
	now := time.Now().UTC()
	workflow := domain.WorkflowDefinition{
		ID: "WfHTTP", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", CallbackID: "triage-workflow",
		Title: "Triage workflow", InputSchema: `{}`, Steps: `[{"function_id":"triage","title":"Triage step"}]`,
		Status: domain.WorkflowPublished, Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflow(ctx, workflow, events.Event{ID: "workflow-http", WorkspaceID: "T1", Topic: "workflow.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	trigger := domain.WorkflowTrigger{
		ID: "FtHTTP", WorkflowID: workflow.ID, WorkspaceID: "T1", AppID: "A1", Title: "Run triage",
		Type: "link", Config: `{}`, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SetWorkflowTrigger(ctx, trigger, events.Event{ID: "trigger-http", WorkspaceID: "T1", Topic: "workflow.trigger_created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	call := func(path string, values url.Values) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s status=%d body=%s err=%v", path, response.Code, response.Body, err)
		}
		return body
	}
	functionReference := url.Values{"function_callback_id": {"triage"}, "function_app_id": {"A1"}}
	defaultFunction := call("/api/functions.distributions.permissions.list", functionReference)
	if users, ok := defaultFunction["users"].([]any); defaultFunction["permission_type"] != "app_collaborators" || !ok || len(users) != 1 {
		t.Fatalf("default function collaborators=%v", defaultFunction)
	}
	functionReference.Set("permission_type", "named_entities")
	functionReference.Set("user_ids", `["U1","U2"]`)
	setFunction := call("/api/functions.distributions.permissions.set", functionReference)
	if setFunction["ok"] != true || setFunction["permission_type"] != "named_entities" {
		t.Fatalf("function permission set=%v", setFunction)
	}
	invalidFunction := call("/api/functions.distributions.permissions.set", url.Values{
		"function_callback_id": {"triage"}, "function_app_id": {"A1"}, "permission_type": {"workspace_admins"},
	})
	if invalidFunction["ok"] != false || invalidFunction["error"] != "invalid_permission_type" {
		t.Fatalf("invalid function permission=%v", invalidFunction)
	}
	missingFunctionType := call("/api/functions.distributions.permissions.set", url.Values{
		"function_callback_id": {"triage"}, "function_app_id": {"A1"},
	})
	if missingFunctionType["ok"] != false || missingFunctionType["error"] != "permission_type_required" {
		t.Fatalf("missing function permission type=%v", missingFunctionType)
	}
	missingFunctionUser := call("/api/functions.distributions.permissions.set", url.Values{
		"function_callback_id": {"triage"}, "function_app_id": {"A1"},
		"permission_type": {"named_entities"}, "user_ids": {`["U-missing"]`},
	})
	if missingFunctionUser["ok"] != false || missingFunctionUser["error"] != "user_not_found" {
		t.Fatalf("missing function permission user=%v", missingFunctionUser)
	}
	listFunction := call("/api/functions.distributions.permissions.list", url.Values{"function_callback_id": {"triage"}, "function_app_id": {"A1"}})
	if users, ok := listFunction["users"].([]any); listFunction["ok"] != true || !ok || len(users) != 2 {
		t.Fatalf("function permission list=%v", listFunction)
	}
	defaultTrigger := call("/api/workflows.triggers.permissions.list", url.Values{"trigger_id": {"FtHTTP"}})
	if users, ok := defaultTrigger["user_ids"].([]any); defaultTrigger["permission_type"] != "app_collaborators" || !ok || len(users) != 1 {
		t.Fatalf("default trigger collaborators=%v", defaultTrigger)
	}
	setTrigger := call("/api/workflows.triggers.permissions.set", url.Values{
		"trigger_id": {"FtHTTP"}, "permission_type": {"everyone"},
	})
	if setTrigger["ok"] != true || setTrigger["permission_type"] != "everyone" {
		t.Fatalf("trigger permission set=%v", setTrigger)
	}
	invalidTrigger := call("/api/workflows.triggers.permissions.set", url.Values{
		"trigger_id": {"FtHTTP"}, "permission_type": {"workspace_admins"},
	})
	if invalidTrigger["ok"] != false || invalidTrigger["error"] != "invalid_permission_type" {
		t.Fatalf("invalid trigger permission=%v", invalidTrigger)
	}
	emptyTriggerEntities := call("/api/workflows.triggers.permissions.set", url.Values{
		"trigger_id": {"FtHTTP"}, "permission_type": {"named_entities"},
	})
	if emptyTriggerEntities["ok"] != false || emptyTriggerEntities["error"] != "named_entities_cannot_be_empty" {
		t.Fatalf("empty trigger named entities=%v", emptyTriggerEntities)
	}
	missingTriggerChannel := call("/api/workflows.triggers.permissions.set", url.Values{
		"trigger_id": {"FtHTTP"}, "permission_type": {"named_entities"}, "channel_ids": {`["C-missing"]`},
	})
	if missingTriggerChannel["ok"] != false || missingTriggerChannel["error"] != "channel_not_found" {
		t.Fatalf("missing trigger permission channel=%v", missingTriggerChannel)
	}
	listTrigger := call("/api/workflows.triggers.permissions.list", url.Values{"trigger_id": {"FtHTTP"}})
	if listTrigger["ok"] != true || listTrigger["permission_type"] != "everyone" {
		t.Fatalf("trigger permission list=%v", listTrigger)
	}
	featured := call("/api/workflows.featured.set", url.Values{"channel_id": {"C1"}, "trigger_ids": {`["FtHTTP"]`}})
	if featured["ok"] != true {
		t.Fatalf("featured set=%v", featured)
	}
	listFeatured := call("/api/workflows.featured.list", url.Values{"channel_ids": {`["C1"]`}})
	groups, ok := listFeatured["featured_workflows"].([]any)
	if listFeatured["ok"] != true || !ok || len(groups) != 1 {
		t.Fatalf("featured list=%v", listFeatured)
	}
	sum := sha256.Sum256([]byte("A1\x00triage"))
	functionID := fmt.Sprintf("Fn%X", sum[:8])
	steps := call("/api/functions.workflows.steps.list", url.Values{"function_id": {functionID}, "workflow_id": {"WfHTTP"}})
	versions, ok := steps["steps_versions"].([]any)
	if steps["ok"] != true || !ok || len(versions) != 1 {
		t.Fatalf("workflow steps=%v", steps)
	}
	missingFunctionSteps := call("/api/functions.workflows.steps.list", url.Values{
		"function_id": {"FnMissing"}, "workflow_id": {"WfHTTP"},
	})
	if missingFunctionSteps["ok"] != false || missingFunctionSteps["error"] != "function_not_found" {
		t.Fatalf("missing function workflow steps=%v", missingFunctionSteps)
	}
}

func TestDialogOpenHTTP(t *testing.T) {
	handler, store := testHandlerWithStore()
	seedHTTPInteractionTrigger(t, store, "trigger-http")
	values := url.Values{"trigger_id": {"trigger-http"}, "dialog": {`{"callback_id":"callback","title":"Title","elements":[{"type":"text"}]}`}}
	req := httptest.NewRequest(http.MethodPost, "/api/dialog.open", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestBotsInfoHTTPUsesRegisteredBot(t *testing.T) {
	handler := testHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/bots.info?bot=B1", nil)
	req.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"id":"B1"`) || !strings.Contains(result.Body.String(), `"name":"testbot"`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestMigrationExchangeHTTP(t *testing.T) {
	handler := testHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/migration.exchange", strings.NewReader(url.Values{"users": {"U1,missing"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"U1":"W1"`) || !strings.Contains(result.Body.String(), "missing") {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

// mapServiceError no longer selects an HTTP status: a handled Slack failure is
// always HTTP 200 plus `{"ok":false,"error":…}`. The previous version of this
// test asserted `503 service_unavailable` for an unclassified handled error;
// `service_unavailable` appears in none of the 174 pinned error enums, and a 503
// makes every official SDK retry a request that can never succeed, which is the
// exact pattern AGENTS.md forbids. The names below are the pinned replacements.
func TestMapServiceErrorNamesHandledFailuresFromThePinnedEnums(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		notFound string
		want     string
	}{
		{"already deleted", service.ErrMessageAlreadyDeleted, "message_not_found", "message_not_found"},
		{"blob unavailable", service.ErrBlobUnavailable, "file_not_found", "file_storage_unavailable"},
		{"not found", store.ErrNotFound, "channel_not_found", "channel_not_found"},
		{"validation", service.ErrInvalidAppApproval, "app_not_found", "invalid_arg_name"},
		{"denied", service.ErrMessageNotOwned, "message_not_found", "no_permission"},
		{"role denied", service.ErrNotWorkspaceAdmin, "channel_not_found", "no_permission"},
		// A gRPC status this handler cannot recognise is a genuine transport
		// failure, not a handled domain error, so it takes the catch-all. Cases
		// asserting that a bare status code names a specific domain error were
		// removed with the `status.Code(err)` fallbacks they described: the
		// transport now restores the domain sentinel itself before the error
		// reaches this package, so a raw status no longer carries domain meaning
		// here. Local-versus-remote parity for every class is asserted end to end
		// over a real wire by TestEveryClassSurvivesTheWireInBothDirections and
		// the differential harness in internal/modules/chat/transport/grpc.
		{"unrecognised transport failure", status.Error(codes.Unavailable, "peer gone"), "channel_not_found", "fatal_error"},
		// A bare errors.New raised by a service or store validation path used to
		// become `503 service_unavailable`. `fatal_error` is declared by 55 pinned
		// operations; no pinned operation declares `service_unavailable`.
		{"unclassified", errors.New("unexpected dependency failure"), "channel_not_found", "fatal_error"},
	}
	for _, testCase := range cases {
		if reason := mapServiceError(testCase.err, testCase.notFound); reason != testCase.want {
			t.Errorf("%s: mapServiceError = %q, want %q", testCase.name, reason, testCase.want)
		}
	}
	if reason := mapServiceErrorNamed(service.ErrInvalidConversation, "channel_not_found", "restricted_action", ""); reason != "restricted_action" {
		t.Errorf("named validation reason = %q, want restricted_action", reason)
	}
}

func TestCallsLifecycle(t *testing.T) {
	handler := testHandler()
	add := httptest.NewRequest(http.MethodPost, "/api/calls.add", strings.NewReader("external_unique_id=external-1&join_url=https%3A%2F%2Fcall.example%2F1&users=U2"))
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	add.Header.Set("Authorization", "Bearer token")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, add)
	if created.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", created.Code, created.Body)
	}
	var response struct {
		Call struct {
			ID string `json:"id"`
		} `json:"call"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil || response.Call.ID == "" {
		t.Fatalf("add response=%s err=%v", created.Body, err)
	}
	update := httptest.NewRequest(http.MethodPost, "/api/calls.update", strings.NewReader("id="+response.Call.ID+"&title=Updated%20call"))
	update.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	update.Header.Set("Authorization", "Bearer token")
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"title":"Updated call"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	participantsAdd := httptest.NewRequest(http.MethodPost, "/api/calls.participants.add", strings.NewReader("id="+response.Call.ID+"&users=U2"))
	participantsAdd.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	participantsAdd.Header.Set("Authorization", "Bearer token")
	participantsAdded := httptest.NewRecorder()
	handler.ServeHTTP(participantsAdded, participantsAdd)
	if participantsAdded.Code != http.StatusOK {
		t.Fatalf("participants add status=%d body=%s", participantsAdded.Code, participantsAdded.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/calls.info?id="+response.Call.ID, nil)
	info.Header.Set("Authorization", "Bearer token")
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, info)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), response.Call.ID) {
		t.Fatalf("info status=%d body=%s", got.Code, got.Body)
	}
	participants := httptest.NewRequest(http.MethodPost, "/api/calls.participants.remove", strings.NewReader("id="+response.Call.ID+"&users=U2"))
	participants.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	participants.Header.Set("Authorization", "Bearer token")
	changed := httptest.NewRecorder()
	handler.ServeHTTP(changed, participants)
	if changed.Code != http.StatusOK {
		t.Fatalf("participants status=%d body=%s", changed.Code, changed.Body)
	}
	end := httptest.NewRequest(http.MethodPost, "/api/calls.end", strings.NewReader("id="+response.Call.ID+"&duration=42"))
	end.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	end.Header.Set("Authorization", "Bearer token")
	ended := httptest.NewRecorder()
	handler.ServeHTTP(ended, end)
	if ended.Code != http.StatusOK {
		t.Fatalf("end status=%d body=%s", ended.Code, ended.Body)
	}
}

func TestAdminAppsApprovalHTTPWorkflow(t *testing.T) {
	handler := testHandler()
	approve := httptest.NewRequest(http.MethodPost, "/api/admin.apps.approve", strings.NewReader("team_id=T1&app_id=A1&request_id=R1"))
	approve.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	approve.Header.Set("Authorization", "Bearer token")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, approve)
	if created.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", created.Code, created.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/admin.apps.approved.list?team_id=T1&limit=1", nil)
	list.Header.Set("Authorization", "Bearer token")
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, list)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "A1") {
		t.Fatalf("list status=%d body=%s", got.Code, got.Body)
	}
	restrict := httptest.NewRequest(http.MethodPost, "/api/admin.apps.restrict", strings.NewReader("team_id=T1&app_id=A1"))
	restrict.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	restrict.Header.Set("Authorization", "Bearer token")
	restricted := httptest.NewRecorder()
	handler.ServeHTTP(restricted, restrict)
	if restricted.Code != http.StatusOK {
		t.Fatalf("restrict status=%d body=%s", restricted.Code, restricted.Body)
	}
	restrictedList := httptest.NewRequest(http.MethodGet, "/api/admin.apps.restricted.list?team_id=T1&limit=1", nil)
	restrictedList.Header.Set("Authorization", "Bearer token")
	restrictedResult := httptest.NewRecorder()
	handler.ServeHTTP(restrictedResult, restrictedList)
	if restrictedResult.Code != http.StatusOK || !strings.Contains(restrictedResult.Body.String(), "A1") {
		t.Fatalf("restricted list status=%d body=%s", restrictedResult.Code, restrictedResult.Body)
	}
}

func TestTeamBillableInfoUsesDurableMembershipState(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodGet, "/api/team.billableInfo?user=U1", nil)
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"U1":{"billing_active":true}`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestAccessLogsRequireAdminAndExposeRecordedAccess(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodGet, "/api/users.info?user=U1", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("recorded request status=%d body=%s", response.Code, response.Body)
	}
	logs := httptest.NewRequest(http.MethodGet, "/api/team.accessLogs", nil)
	logs.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, logs)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"user_id":"U1"`) {
		t.Fatalf("logs status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminUsersListIsBoundedAndWorkspaceScoped(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodGet, "/api/admin.users.list?team_id=T1&limit=1", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"users"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	foreign := httptest.NewRequest(http.MethodGet, "/api/admin.users.list?team_id=T2", nil)
	foreign.Header.Set("Authorization", "Bearer token")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, foreign)
	// admin.users.list declares no error enum; `invalid_arguments` is in only 4 of the
	// 174 pinned enums, so the generic pinned argument rejection is used instead.
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), `"error":"invalid_arg_name"`) {
		t.Fatalf("foreign status=%d body=%s", denied.Code, denied.Body)
	}
}

func TestAdminUsersInvitePersistsRequiredInviteParameters(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/admin.users.invite", strings.NewReader("team_id=T1&email=Alice%40Example.com&channel_ids=C1%2CC1&custom_message=Welcome&real_name=Alice+Example&resend=true&is_restricted=true&guest_expiration_ts=4102444800"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"ok":true}`+"\n" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/admin.inviteRequests.list?team_id=T1&limit=1", nil)
	list.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, list)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"email":"alice@example.com"`) {
		t.Fatalf("list status=%d body=%s", result.Code, result.Body)
	}
}

func TestFileCommentDeleteIsDurable(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/files.comments.delete", strings.NewReader("file=F1&id=FC1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"ok":true}`+"\n" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	secondRequest := httptest.NewRequest(http.MethodPost, "/api/files.comments.delete", strings.NewReader("file=F1&id=FC1"))
	secondRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secondRequest.Header.Set("Authorization", "Bearer token")
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"error":"comment_not_found"`) {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body)
	}
}

func TestAppPermissionIntrospectionUsesAuthenticatedScopes(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodGet, "/api/apps.permissions.scopes.list", nil)
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"chat:write"`) {
		t.Fatalf("scopes status=%d body=%s", result.Code, result.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/apps.permissions.info", nil)
	info.Header.Set("Authorization", "Bearer token")
	infoResult := httptest.NewRecorder()
	handler.ServeHTTP(infoResult, info)
	if infoResult.Code != http.StatusOK || !strings.Contains(infoResult.Body.String(), `"info"`) {
		t.Fatalf("info status=%d body=%s", infoResult.Code, infoResult.Body)
	}
	resources := httptest.NewRequest(http.MethodGet, "/api/apps.permissions.resources.list?limit=1", nil)
	resources.Header.Set("Authorization", "Bearer token")
	resourcesResult := httptest.NewRecorder()
	handler.ServeHTTP(resourcesResult, resources)
	if resourcesResult.Code != http.StatusOK || !strings.Contains(resourcesResult.Body.String(), `"id":"T1"`) {
		t.Fatalf("resources status=%d body=%s", resourcesResult.Code, resourcesResult.Body)
	}
	users := httptest.NewRequest(http.MethodGet, "/api/apps.permissions.users.list?limit=1", nil)
	users.Header.Set("Authorization", "Bearer token")
	usersResult := httptest.NewRecorder()
	handler.ServeHTTP(usersResult, users)
	if usersResult.Code != http.StatusOK || !strings.Contains(usersResult.Body.String(), `"id":"U1"`) {
		t.Fatalf("users status=%d body=%s", usersResult.Code, usersResult.Body)
	}
	permissionRequest := httptest.NewRequest(http.MethodGet, "/api/apps.permissions.request?scopes=chat:write,users:read&trigger_id=trigger-1", nil)
	permissionRequest.Header.Set("Authorization", "Bearer token")
	permissionResult := httptest.NewRecorder()
	handler.ServeHTTP(permissionResult, permissionRequest)
	if permissionResult.Code != http.StatusOK {
		t.Fatalf("permission request status=%d body=%s", permissionResult.Code, permissionResult.Body)
	}
	userPermissionRequest := httptest.NewRequest(http.MethodGet, "/api/apps.permissions.users.request?user=U2&scopes=chat:write&trigger_id=trigger-2", nil)
	userPermissionRequest.Header.Set("Authorization", "Bearer token")
	userPermissionResult := httptest.NewRecorder()
	handler.ServeHTTP(userPermissionResult, userPermissionRequest)
	if userPermissionResult.Code != http.StatusOK {
		t.Fatalf("user permission request status=%d body=%s", userPermissionResult.Code, userPermissionResult.Body)
	}
}

func TestAppsUninstallRevokesTheWholeMatchingInstallation(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "app"})
	if err := s.CreateOAuthClient(context.Background(), domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthClient(context.Background(), domain.OAuthClient{ID: "other-client", SecretHash: domain.HashToken("secret"), AppID: "A2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(context.Background(), domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	s.SeedToken(context.Background(), "token-one", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user"})
	s.SeedToken(context.Background(), "token-two", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user"})
	s.SeedToken(context.Background(), "bot-token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "bot"})
	authenticator, err := auth.NewStored(s)
	if err != nil {
		t.Fatal(err)
	}
	slackHandler, err := NewHandler(service.Messages{Store: s}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.NewServeMux()
	slackHandler.Register(handler)
	uninstall := func(token, clientID string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/apps.uninstall", strings.NewReader(url.Values{"client_id": {clientID}, "client_secret": {"secret"}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer "+token)
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		return result
	}
	if result := uninstall("bot-token", "client"); !strings.Contains(result.Body.String(), `"error":"no_permission"`) {
		t.Fatalf("bot uninstall status=%d body=%s", result.Code, result.Body)
	}
	if result := uninstall("token-one", "other-client"); !strings.Contains(result.Body.String(), `"error":"client_id_token_mismatch"`) {
		t.Fatalf("mismatched uninstall status=%d body=%s", result.Code, result.Body)
	}
	result := uninstall("token-one", "client")
	if result.Code != http.StatusOK || result.Body.String() != `{"ok":true}`+"\n" {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	for _, token := range []string{"token-one", "token-two"} {
		record, err := s.LookupToken(context.Background(), token)
		if err != nil || !record.Revoked {
			t.Fatalf("%s record=%+v err=%v", token, record, err)
		}
	}
	if installations, err := s.ListAppInstallations(context.Background(), "A1"); err != nil || len(installations) != 0 {
		t.Fatalf("installations=%+v err=%v", installations, err)
	}
}

func TestAdminConversationTeamsAreExplicitlySingleWorkspace(t *testing.T) {
	handler := testHandler()
	get := httptest.NewRequest(http.MethodGet, "/api/admin.conversations.getTeams?channel_id=C1&limit=1", nil)
	get.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, get)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"team_ids":["T1"]`) {
		t.Fatalf("get status=%d body=%s", result.Code, result.Body)
	}
	set := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.setTeams", strings.NewReader("channel_id=C1&target_team_ids=T1"))
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	set.Header.Set("Authorization", "Bearer token")
	setResult := httptest.NewRecorder()
	handler.ServeHTTP(setResult, set)
	if setResult.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", setResult.Code, setResult.Body)
	}
	foreign := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.setTeams", strings.NewReader("channel_id=C1&target_team_ids=T2"))
	foreign.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	foreign.Header.Set("Authorization", "Bearer token")
	foreignResult := httptest.NewRecorder()
	handler.ServeHTTP(foreignResult, foreign)
	if foreignResult.Code != http.StatusOK || !strings.Contains(foreignResult.Body.String(), `"error":"invalid_team"`) {
		t.Fatalf("foreign status=%d body=%s", foreignResult.Code, foreignResult.Body)
	}
}

func TestAdminConversationSharedDisconnectAndEKMInfo(t *testing.T) {
	handler := testHandler()
	set := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.setTeams", strings.NewReader("channel_id=C1&target_team_ids=T1&org_channel=false"))
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	set.Header.Set("Authorization", "Bearer token")
	setResult := httptest.NewRecorder()
	handler.ServeHTTP(setResult, set)
	if setResult.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", setResult.Code, setResult.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/admin.conversations.ekm.listOriginalConnectedChannelInfo?limit=10&channel_ids=C1", nil)
	info.Header.Set("Authorization", "Bearer token")
	infoResult := httptest.NewRecorder()
	handler.ServeHTTP(infoResult, info)
	if infoResult.Code != http.StatusOK || !strings.Contains(infoResult.Body.String(), `"id":"C1"`) {
		t.Fatalf("info status=%d body=%s", infoResult.Code, infoResult.Body)
	}
	disconnect := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.disconnectShared", strings.NewReader("channel_id=C1&leaving_team_ids=T1"))
	disconnect.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	disconnect.Header.Set("Authorization", "Bearer token")
	disconnectResult := httptest.NewRecorder()
	handler.ServeHTTP(disconnectResult, disconnect)
	if disconnectResult.Code != http.StatusOK {
		t.Fatalf("disconnect status=%d body=%s", disconnectResult.Code, disconnectResult.Body)
	}
}

// An administrator could not find a workflow they did not own, and could stop
// one only through the owner's edit path — which revalidates the steps and
// requires the owning app to still be installed, so exactly the workflows most
// worth stopping were the ones that could not be.
func TestAdminWorkflowSearchAndUnpublishReachWhatMembersCannot(t *testing.T) {
	handler, repository := testHandlerWithStore()
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	// Workflows are authored in the builder rather than through the Web API —
	// Slack publishes no create method either — so the one under administration
	// here is created through the service.
	messages := service.Messages{Store: repository}
	created, err := messages.CreateWorkflow(context.Background(), "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", CallbackID: "nightly", Title: "Nightly triage", InputSchema: `{}`,
		Steps: `[{"function_id":"triage","title":"Triage"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := messages.UpdateWorkflow(context.Background(), "T1", "U1", created, created.Version, true)
	if err != nil {
		t.Fatal(err)
	}

	found := post("/api/admin.workflows.search", "query=nightly&limit=10")
	if found.Code != http.StatusOK || !strings.Contains(found.Body.String(), string(published.ID)) {
		t.Fatalf("search status=%d body=%s", found.Code, found.Body)
	}
	// The query means something: a term nobody matches answers with nothing
	// rather than with everything.
	if empty := post("/api/admin.workflows.search", "query=nothing-is-called-this&limit=10"); strings.Contains(empty.Body.String(), string(published.ID)) {
		t.Fatalf("an unmatched query returned the workflow: %s", empty.Body)
	}

	// A workflow that is not here stops the whole request.
	mixed := post("/api/admin.workflows.unpublish", "workflow_ids="+string(published.ID)+",Wf-absent")
	if !strings.Contains(mixed.Body.String(), `"ok":false`) {
		t.Fatalf("a mixed unpublish succeeded: %s", mixed.Body)
	}

	if stopped := post("/api/admin.workflows.unpublish", "workflow_ids="+string(published.ID)); stopped.Code != http.StatusOK || !strings.Contains(stopped.Body.String(), `"ok":true`) {
		t.Fatalf("unpublish status=%d body=%s", stopped.Code, stopped.Body)
	}
	after := post("/api/admin.workflows.search", "query=nightly&limit=10")
	if !strings.Contains(after.Body.String(), `"status":"disabled"`) {
		t.Fatalf("the workflow is not out of service: %s", after.Body)
	}
	// Stopping is not authoring: the version does not move, because runs pin
	// the published version and a stop is not a revision anybody wrote.
	if !strings.Contains(after.Body.String(), `"version":`+strconv.FormatUint(published.Version, 10)) {
		t.Fatalf("stopping the workflow moved its version: %s", after.Body)
	}
}

// Session administration could end sessions and never show them: an
// administrator had to decide whether to sign somebody out without being able
// to see what they would be ending.
func TestAdminSessionListShowsSessionsWithoutTheirTokens(t *testing.T) {
	handler, s := testHandlerWithStore()
	now := time.Now().UTC()
	if err := s.SeedSession(context.Background(), "member-token", domain.SessionRecord{
		WorkspaceID: "T1", UserID: "U2", Scopes: []string{string(auth.ScopeChatWrite)},
		CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	listed := post("/api/admin.users.session.list", "user_id=U2")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"user_id":"U2"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	// The credential must never appear: a review that handed out tokens would
	// be a way to become the member rather than a way to see them.
	if strings.Contains(listed.Body.String(), "member-token") {
		t.Fatalf("the session list carried the token: %s", listed.Body)
	}
	if !strings.Contains(listed.Body.String(), `"session_id":"`+domain.HashToken("member-token")+`"`) {
		t.Fatalf("the session is not identified by its stored hash: %s", listed.Body)
	}

	// A member named in a bulk reset who is not here stops the whole request.
	if mixed := post("/api/admin.users.session.resetBulk", "user_ids=U2,U-absent"); !strings.Contains(mixed.Body.String(), `"ok":false`) {
		t.Fatalf("a bulk reset naming a stranger succeeded: %s", mixed.Body)
	}
	if still := post("/api/admin.users.session.list", "user_id=U2"); !strings.Contains(still.Body.String(), `"session_id"`) {
		t.Fatalf("the refused bulk reset ended a session anyway: %s", still.Body)
	}

	if reset := post("/api/admin.users.session.resetBulk", "user_ids=U1,U2"); reset.Code != http.StatusOK || !strings.Contains(reset.Body.String(), `"ok":true`) {
		t.Fatalf("bulk reset status=%d body=%s", reset.Code, reset.Body)
	}
	after := post("/api/admin.users.session.list", "user_id=U2")
	if strings.Contains(after.Body.String(), `"session_id"`) {
		t.Fatalf("a revoked session is still listed: %s", after.Body)
	}
}

// team.externalTeams.* is the whole-organization half of Slack Connect. The
// per-channel form already existed; the question "who are we connected to, and
// end it everywhere" had no answer at all.
func TestExternalTeamsListAndDisconnectSpanEveryChannel(t *testing.T) {
	handler, s := testHandlerWithStore()
	s.SeedWorkspace(domain.Workspace{ID: "T2", Name: "Partner"})
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	// The connection is seeded rather than made through admin.conversations
	// .setTeams, which deliberately refuses a foreign workspace: a connection is
	// only ever created by accepting a shared invitation. What is under test
	// here is what an administrator can see and end afterwards.
	for _, channel := range []domain.ConversationID{"C1", "C2"} {
		if err := s.SetConversationTeams(context.Background(), "T1", channel, []domain.WorkspaceID{"T1", "T2"}, false, events.Event{ID: domain.EventID("E-connect-" + string(channel)), WorkspaceID: "T1", Topic: "conversation.connected", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatalf("connect %s: %v", channel, err)
		}
	}

	listed := post("/api/team.externalTeams.list", "limit=10")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"team_id":"T2"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	// The count is the reason to look: disconnecting ends access to every one.
	if !strings.Contains(listed.Body.String(), `"channel_count":2`) {
		t.Fatalf("list did not count both channels: %s", listed.Body)
	}

	// An organization nothing is shared with cannot be disconnected: reporting
	// success would say a connection had been ended that never existed.
	if absent := post("/api/team.externalTeams.disconnect", "target_team=T3"); !strings.Contains(absent.Body.String(), `"ok":false`) {
		t.Fatalf("absent disconnect: %s", absent.Body)
	}

	if result := post("/api/team.externalTeams.disconnect", "target_team=T2"); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) {
		t.Fatalf("disconnect status=%d body=%s", result.Code, result.Body)
	}
	after := post("/api/team.externalTeams.list", "limit=10")
	if strings.Contains(after.Body.String(), `"team_id":"T2"`) {
		t.Fatalf("the organization is still connected after disconnecting: %s", after.Body)
	}
}

func TestAdminUserGroupAddTeamsValidatesWorkspaceTopology(t *testing.T) {
	handler := testHandler()
	create := httptest.NewRequest(http.MethodPost, "/api/usergroups.create", strings.NewReader("name=Engineering&handle=engineering"))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	create.Header.Set("Authorization", "Bearer token")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	var payload struct {
		UserGroup struct {
			ID string `json:"id"`
		} `json:"usergroup"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/admin.usergroups.addTeams", strings.NewReader("usergroup_id="+payload.UserGroup.ID+"&team_ids=T1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("same-workspace status=%d body=%s", result.Code, result.Body)
	}
	foreign := httptest.NewRequest(http.MethodPost, "/api/admin.usergroups.addTeams", strings.NewReader("usergroup_id="+payload.UserGroup.ID+"&team_ids=T2"))
	foreign.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	foreign.Header.Set("Authorization", "Bearer token")
	foreignResult := httptest.NewRecorder()
	handler.ServeHTTP(foreignResult, foreign)
	if foreignResult.Code != http.StatusOK || !strings.Contains(foreignResult.Body.String(), `"error":"invalid_team"`) {
		t.Fatalf("foreign status=%d body=%s", foreignResult.Code, foreignResult.Body)
	}
}

func TestUserGroupLifecycle(t *testing.T) {
	handler := testHandler()
	form := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	created := form("/api/usergroups.create", "name=Engineering&handle=engineering")
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	var createdBody struct {
		UserGroup struct {
			ID        string `json:"id"`
			IsSubteam bool   `json:"is_subteam"`
		} `json:"usergroup"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil || createdBody.UserGroup.ID == "" || !createdBody.UserGroup.IsSubteam {
		t.Fatalf("create body=%s err=%v", created.Body, err)
	}
	id := createdBody.UserGroup.ID
	private := form("/api/admin.conversations.create", "name=Access%20controlled&is_private=true")
	if private.Code != http.StatusOK {
		t.Fatalf("private conversation status=%d body=%s", private.Code, private.Body)
	}
	var privateBody struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(private.Body.Bytes(), &privateBody); err != nil || privateBody.Channel.ID == "" {
		t.Fatalf("private conversation body=%s err=%v", private.Body, err)
	}
	accessAdd := form("/api/admin.conversations.restrictAccess.addGroup", "channel_id="+privateBody.Channel.ID+"&group_id="+id)
	if accessAdd.Code != http.StatusOK {
		t.Fatalf("access add status=%d body=%s", accessAdd.Code, accessAdd.Body)
	}
	accessList := httptest.NewRequest(http.MethodGet, "/api/admin.conversations.restrictAccess.listGroups?channel_id="+privateBody.Channel.ID, nil)
	accessList.Header.Set("Authorization", "Bearer token")
	accessListResult := httptest.NewRecorder()
	handler.ServeHTTP(accessListResult, accessList)
	if accessListResult.Code != http.StatusOK || !strings.Contains(accessListResult.Body.String(), id) {
		t.Fatalf("access list status=%d body=%s", accessListResult.Code, accessListResult.Body)
	}
	accessRemove := form("/api/admin.conversations.restrictAccess.removeGroup", "channel_id="+privateBody.Channel.ID+"&group_id="+id)
	if accessRemove.Code != http.StatusOK {
		t.Fatalf("access remove status=%d body=%s", accessRemove.Code, accessRemove.Body)
	}
	channelAdd := form("/api/admin.usergroups.addChannels", "usergroup="+id+"&channel_ids=C1")
	if channelAdd.Code != http.StatusOK {
		t.Fatalf("channel add status=%d body=%s", channelAdd.Code, channelAdd.Body)
	}
	channelRemove := form("/api/admin.usergroups.removeChannels", "usergroup="+id+"&channel_ids=C1")
	if channelRemove.Code != http.StatusOK {
		t.Fatalf("channel remove status=%d body=%s", channelRemove.Code, channelRemove.Body)
	}
	updated := form("/api/usergroups.update", "usergroup="+id+"&name=Engineering%20Updated&handle=engineering-updated")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "Engineering Updated") {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	setUsers := form("/api/usergroups.users.update", "usergroup="+id+"&users=U1,U2")
	if setUsers.Code != http.StatusOK {
		t.Fatalf("users update status=%d body=%s", setUsers.Code, setUsers.Body)
	}
	users := httptest.NewRequest(http.MethodGet, "/api/usergroups.users.list?usergroup="+id, nil)
	users.Header.Set("Authorization", "Bearer token")
	usersResult := httptest.NewRecorder()
	handler.ServeHTTP(usersResult, users)
	if usersResult.Code != http.StatusOK || !strings.Contains(usersResult.Body.String(), `"users":["U1","U2"]`) {
		t.Fatalf("users list status=%d body=%s", usersResult.Code, usersResult.Body)
	}
	listed := httptest.NewRecorder()
	list := httptest.NewRequest(http.MethodGet, "/api/usergroups.list?include_users=true", nil)
	list.Header.Set("Authorization", "Bearer token")
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), id) || !strings.Contains(listed.Body.String(), `"users":["U1","U2"]`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	disabled := form("/api/usergroups.disable", "usergroup="+id)
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"date_delete"`) {
		t.Fatalf("disable status=%d body=%s", disabled.Code, disabled.Body)
	}
	enabled := form("/api/usergroups.enable", "usergroup="+id)
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"date_delete":0`) {
		t.Fatalf("enable status=%d body=%s", enabled.Code, enabled.Body)
	}
}

func TestAdminTeamsCreatePersistsNewWorkspace(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/admin.teams.create", strings.NewReader("team_domain=second-workspace&team_name=Second%20Workspace&team_description=created"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"team":"T`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminUsersRemoveDeactivatesUser(t *testing.T) {
	handler, store := testHandlerWithStore()
	if err := store.SeedToken(context.Background(), "user-two-token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U2", Scopes: auth.AllScopes()}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedSession(context.Background(), "user-two-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/admin.users.remove", strings.NewReader("team_id=T1&user_id=U2"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", response.Code, response.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/users.info?user=U2", nil)
	info.Header.Set("Authorization", "Bearer token")
	after := httptest.NewRecorder()
	handler.ServeHTTP(after, info)
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"error":"user_not_found"`) {
		t.Fatalf("removed user status=%d body=%s", after.Code, after.Body)
	}
	token, err := store.LookupToken(context.Background(), "user-two-token")
	if err != nil || !token.Revoked {
		t.Fatalf("removed user token=%+v err=%v", token, err)
	}
	session, err := store.LookupSession(context.Background(), "user-two-session")
	if err != nil || !session.Revoked {
		t.Fatalf("removed user session=%+v err=%v", session, err)
	}
}

func TestAdminUsersAssignReactivatesAndJoinsChannels(t *testing.T) {
	handler := testHandler()
	remove := httptest.NewRequest(http.MethodPost, "/api/admin.users.remove", strings.NewReader("team_id=T1&user_id=U2"))
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	remove.Header.Set("Authorization", "Bearer token")
	removed := httptest.NewRecorder()
	handler.ServeHTTP(removed, remove)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", removed.Code, removed.Body)
	}
	assign := httptest.NewRequest(http.MethodPost, "/api/admin.users.assign", strings.NewReader("team_id=T1&user_id=U2&channel_ids=C1,C1"))
	assign.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	assign.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, assign)
	if response.Code != http.StatusOK || response.Body.String() != `{"ok":true}`+"\n" {
		t.Fatalf("assign status=%d body=%s", response.Code, response.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/users.info?user=U2", nil)
	info.Header.Set("Authorization", "Bearer token")
	active := httptest.NewRecorder()
	handler.ServeHTTP(active, info)
	if active.Code != http.StatusOK {
		t.Fatalf("assigned user status=%d body=%s", active.Code, active.Body)
	}
}

func TestAdminUsersRoleMutationsUseTypedRoles(t *testing.T) {
	for _, endpoint := range []string{"admin.users.setAdmin", "admin.users.setOwner", "admin.users.setRegular"} {
		handler := testHandler()
		request := httptest.NewRequest(http.MethodPost, "/api/"+endpoint, strings.NewReader("team_id=T1&user_id=U2"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
	}
}

func TestAdminUsersSetExpirationAcceptsEpochAndClear(t *testing.T) {
	handler := testHandler()
	for _, expiration := range []string{"1", "0"} {
		request := httptest.NewRequest(http.MethodPost, "/api/admin.users.setExpiration", strings.NewReader("team_id=T1&user_id=U2&expiration_ts="+expiration))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != `{"ok":true}`+"\n" {
			t.Fatalf("expiration=%s status=%d body=%s", expiration, response.Code, response.Body)
		}
	}
}

// TestAdminRoleAssignments holds the admin.roles.* contract: a role is written
// over named entities, read back in one order, and taken away again. A member
// outside the workspace is refused before any row lands, so a request that
// names one leaves nothing behind.
func TestAdminRoleAssignments(t *testing.T) {
	handler := testHandler()
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	if added := call(t, http.MethodPost, "admin.roles.addAssignments", "role_id=Rl0A&entity_ids=C2,C1&user_ids=U1,U2"); added["ok"] != true {
		t.Fatalf("added=%v", added)
	}
	listed := call(t, http.MethodPost, "admin.roles.listAssignments", "role_id=Rl0A")
	assignments, ok := listed["role_assignments"].([]any)
	if !ok || len(assignments) != 4 {
		t.Fatalf("listed=%v", listed)
	}
	order := make([]string, 0, len(assignments))
	for _, value := range assignments {
		assignment, isMap := value.(map[string]any)
		if !isMap {
			t.Fatalf("assignment=%v", value)
		}
		order = append(order, assignment["user_id"].(string)+"/"+assignment["entity_id"].(string))
	}
	if want := "U1/C1,U1/C2,U2/C1,U2/C2"; strings.Join(order, ",") != want {
		t.Fatalf("order=%v want=%v", order, want)
	}
	// A member the workspace does not hold is refused, and the refusal is not
	// a partial write: the rows the same request named must not appear.
	stranger := call(t, http.MethodPost, "admin.roles.addAssignments", "role_id=Rl0B&entity_ids=C1&user_ids=U1,U-nobody")
	if stranger["error"] != "user_not_found" {
		t.Fatalf("stranger=%v", stranger)
	}
	if empty := call(t, http.MethodPost, "admin.roles.listAssignments", "role_id=Rl0B"); len(empty["role_assignments"].([]any)) != 0 {
		t.Fatalf("partial write survived: %v", empty)
	}
	for _, body := range []string{"entity_ids=C1&user_ids=U1", "role_id=Rl0A&user_ids=U1", "role_id=Rl0A&entity_ids=C1"} {
		if refused := call(t, http.MethodPost, "admin.roles.addAssignments", body); refused["error"] != "invalid_arg_name" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if removed := call(t, http.MethodPost, "admin.roles.removeAssignments", "role_id=Rl0A&entity_ids=C1&user_ids=U1,U2"); removed["ok"] != true {
		t.Fatalf("removed=%v", removed)
	}
	left := call(t, http.MethodGet, "admin.roles.listAssignments?role_id=Rl0A", "")
	if remaining := left["role_assignments"].([]any); len(remaining) != 2 {
		t.Fatalf("left=%v", left)
	}
}

// TestAdminAuthPolicyEntities holds the admin.auth.policy.* contract. Slack
// names one policy and one entity type; anything else is refused rather than
// stored as a policy nothing enforces.
func TestAdminAuthPolicyEntities(t *testing.T) {
	handler := testHandler()
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	if assigned := call(t, http.MethodPost, "admin.auth.policy.assignEntities", "policy_name=email_password&entity_type=USER&entity_ids=U2,U1"); assigned["ok"] != true {
		t.Fatalf("assigned=%v", assigned)
	}
	listed := call(t, http.MethodPost, "admin.auth.policy.getEntities", "policy_name=email_password&entity_type=USER")
	entities, ok := listed["entities"].([]any)
	if !ok || len(entities) != 2 || listed["entity_total_count"].(float64) != 2 {
		t.Fatalf("listed=%v", listed)
	}
	if first := entities[0].(map[string]any); first["entity_id"] != "U1" || first["entity_type"] != "USER" {
		t.Fatalf("first=%v", first)
	}
	// A lower-case entity type means the member too: Slack documents the upper
	// case, and refusing the other spelling would be a difference nobody asked
	// for.
	if folded := call(t, http.MethodPost, "admin.auth.policy.getEntities", "policy_name=email_password&entity_type=user"); len(folded["entities"].([]any)) != 2 {
		t.Fatalf("folded=%v", folded)
	}
	for _, body := range []string{
		"policy_name=sso_only&entity_type=USER&entity_ids=U1",
		"policy_name=email_password&entity_type=CHANNEL&entity_ids=U1",
		"policy_name=email_password&entity_type=USER",
		"entity_type=USER&entity_ids=U1",
	} {
		if refused := call(t, http.MethodPost, "admin.auth.policy.assignEntities", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if stranger := call(t, http.MethodPost, "admin.auth.policy.assignEntities", "policy_name=email_password&entity_type=USER&entity_ids=U-nobody"); stranger["error"] != "user_not_found" {
		t.Fatalf("stranger=%v", stranger)
	}
	if removed := call(t, http.MethodPost, "admin.auth.policy.removeEntities", "policy_name=email_password&entity_type=USER&entity_ids=U1"); removed["ok"] != true {
		t.Fatalf("removed=%v", removed)
	}
	left := call(t, http.MethodGet, "admin.auth.policy.getEntities?policy_name=email_password&entity_type=USER", "")
	if len(left["entities"].([]any)) != 1 || left["entity_total_count"].(float64) != 1 {
		t.Fatalf("left=%v", left)
	}
}

// TestAdminUsersSessionSettings holds the admin.users.session.*Settings
// contract. A member on the workspace default is named in no_settings_applied
// and not reported with zeros, and a duration under eight hours is refused.
func TestAdminUsersSessionSettings(t *testing.T) {
	handler := testHandler()
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	if set := call(t, http.MethodPost, "admin.users.session.setSettings", "user_ids=U1&duration=43200&mobile_device_check=true"); set["ok"] != true {
		t.Fatalf("set=%v", set)
	}
	read := call(t, http.MethodPost, "admin.users.session.getSettings", "user_ids=U1,U2")
	applied, ok := read["session_settings"].([]any)
	if !ok || len(applied) != 1 {
		t.Fatalf("read=%v", read)
	}
	first := applied[0].(map[string]any)
	if first["user_id"] != "U1" || first["duration"].(float64) != 43200 || first["mobile_device_check"] != true || first["desktop_app_browser_quit"] != false {
		t.Fatalf("first=%v", first)
	}
	if none := read["no_settings_applied"].([]any); len(none) != 1 || none[0] != "U2" {
		t.Fatalf("no_settings_applied=%v", read["no_settings_applied"])
	}
	// Eight hours is the floor, so 28800 stands and 28799 is refused rather
	// than rounded up to it.
	if floor := call(t, http.MethodPost, "admin.users.session.setSettings", "user_ids=U1&duration=28800"); floor["ok"] != true {
		t.Fatalf("floor=%v", floor)
	}
	for _, body := range []string{"user_ids=U1&duration=28799", "user_ids=U1&duration=-1", "user_ids=U1&duration=soon", "duration=43200"} {
		if refused := call(t, http.MethodPost, "admin.users.session.setSettings", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if stranger := call(t, http.MethodPost, "admin.users.session.setSettings", "user_ids=U-nobody&duration=43200"); stranger["error"] != "user_not_found" {
		t.Fatalf("stranger=%v", stranger)
	}
	if cleared := call(t, http.MethodPost, "admin.users.session.clearSettings", "user_ids=U1"); cleared["ok"] != true {
		t.Fatalf("cleared=%v", cleared)
	}
	after := call(t, http.MethodGet, "admin.users.session.getSettings?user_ids=U1", "")
	if len(after["session_settings"].([]any)) != 0 || len(after["no_settings_applied"].([]any)) != 1 {
		t.Fatalf("after=%v", after)
	}
}

// TestAdminBarriers holds the admin.barriers.* contract. A barrier restricts
// every subject Slack declares or it is refused: one that stopped direct
// messages but not calls would read as a barrier and leave a way through.
func TestAdminBarriers(t *testing.T) {
	handler, target := testHandlerWithStore()
	now := time.Now().UTC()
	for _, group := range []domain.UserGroup{
		{ID: "S1", WorkspaceID: "T1", Name: "Traders", Handle: "traders", Creator: "U1", UpdatedBy: "U1", CreatedAt: now, UpdatedAt: now},
		{ID: "S2", WorkspaceID: "T1", Name: "Analysts", Handle: "analysts", Creator: "U1", UpdatedBy: "U1", CreatedAt: now, UpdatedAt: now},
	} {
		if err := target.CreateUserGroup(context.Background(), group, events.Event{ID: domain.EventID("evt-group-" + string(group.ID)), WorkspaceID: "T1", ActorID: "U1", Topic: "subteam.created", Payload: string(group.ID), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	created := call(t, http.MethodPost, "admin.barriers.create", "primary_usergroup_id=S1&barriered_from_usergroup_ids=S2&restricted_subjects=im,mpim,call")
	barrier, ok := created["barrier"].(map[string]any)
	if !ok || barrier["primary_usergroup"] != "S1" {
		t.Fatalf("created=%v", created)
	}
	id := barrier["id"].(string)
	if subjects := barrier["restricted_subjects"].([]any); len(subjects) != 3 {
		t.Fatalf("restricted_subjects=%v", subjects)
	}
	for _, body := range []string{
		"primary_usergroup_id=S1&barriered_from_usergroup_ids=S2&restricted_subjects=im",
		"primary_usergroup_id=S1&barriered_from_usergroup_ids=S2&restricted_subjects=im,mpim,call,email",
		"primary_usergroup_id=S1&restricted_subjects=im,mpim,call",
		"barriered_from_usergroup_ids=S2&restricted_subjects=im,mpim,call",
	} {
		if refused := call(t, http.MethodPost, "admin.barriers.create", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	// A group barriered from itself is not a barrier.
	if itself := call(t, http.MethodPost, "admin.barriers.create", "primary_usergroup_id=S1&barriered_from_usergroup_ids=S1&restricted_subjects=im,mpim,call"); itself["ok"] == true {
		t.Fatalf("a group was barriered from itself: %v", itself)
	}
	updated := call(t, http.MethodPost, "admin.barriers.update", "barrier_id="+id+"&primary_usergroup_id=S2&barriered_from_usergroup_ids=S1&restricted_subjects=im,mpim,call")
	if updated["barrier"].(map[string]any)["primary_usergroup"] != "S2" {
		t.Fatalf("updated=%v", updated)
	}
	if missing := call(t, http.MethodPost, "admin.barriers.update", "barrier_id=B-nobody&primary_usergroup_id=S2&barriered_from_usergroup_ids=S1&restricted_subjects=im,mpim,call"); missing["error"] != "barrier_not_found" {
		t.Fatalf("missing=%v", missing)
	}
	listed := call(t, http.MethodGet, "admin.barriers.list", "")
	if len(listed["barriers"].([]any)) != 1 {
		t.Fatalf("listed=%v", listed)
	}
	if deleted := call(t, http.MethodPost, "admin.barriers.delete", "barrier_id="+id); deleted["ok"] != true {
		t.Fatalf("deleted=%v", deleted)
	}
	if again := call(t, http.MethodPost, "admin.barriers.delete", "barrier_id="+id); again["error"] != "barrier_not_found" {
		t.Fatalf("again=%v", again)
	}
	if left := call(t, http.MethodPost, "admin.barriers.list", ""); len(left["barriers"].([]any)) != 0 {
		t.Fatalf("left=%v", left)
	}
}

// TestAdminAutomationPermissions holds the admin.*.permissions.* contract. A
// resource nobody has set a permission on answers the default rather than being
// left out of the reply, and named_entities naming nobody is refused: it would
// read as a narrowing and open the resource to no one.
func TestAdminAutomationPermissions(t *testing.T) {
	handler := testHandler()
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	defaults := call(t, http.MethodPost, "admin.functions.permissions.lookup", "function_ids=Fn1,Fn2")
	permissions, ok := defaults["permissions"].(map[string]any)
	if !ok || len(permissions) != 2 {
		t.Fatalf("defaults=%v", defaults)
	}
	if permissions["Fn1"].(map[string]any)["permission_type"] != "everyone" {
		t.Fatalf("Fn1 default=%v", permissions["Fn1"])
	}
	set := call(t, http.MethodPost, "admin.functions.permissions.set", "function_id=Fn1&visibility=named_entities&user_ids=U1")
	if set["permission_type"] != "named_entities" || len(set["user_ids"].([]any)) != 1 {
		t.Fatalf("set=%v", set)
	}
	after := call(t, http.MethodGet, "admin.functions.permissions.lookup?function_ids=Fn1,Fn2", "")
	stored := after["permissions"].(map[string]any)
	if stored["Fn1"].(map[string]any)["permission_type"] != "named_entities" ||
		stored["Fn2"].(map[string]any)["permission_type"] != "everyone" {
		t.Fatalf("after=%v", after)
	}
	for _, body := range []string{
		"function_id=Fn1&visibility=named_entities",
		"function_id=Fn1&visibility=system",
		"function_id=Fn1&visibility=nobody",
		"visibility=everyone",
	} {
		if refused := call(t, http.MethodPost, "admin.functions.permissions.set", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if workflows := call(t, http.MethodPost, "admin.workflows.permissions.lookup", "workflow_ids=Wf1"); len(workflows["permissions"].(map[string]any)) != 1 {
		t.Fatalf("workflows=%v", workflows)
	}
	if triggerSet := call(t, http.MethodPost, "admin.workflows.triggers.types.permissions.set", "trigger_type_id=scheduled&visibility=app_collaborators"); triggerSet["permission_type"] != "app_collaborators" {
		t.Fatalf("triggerSet=%v", triggerSet)
	}
	if triggerRead := call(t, http.MethodGet, "admin.workflows.triggers.types.permissions.lookup?trigger_type_id=scheduled", ""); triggerRead["permission_type"] != "app_collaborators" {
		t.Fatalf("triggerRead=%v", triggerRead)
	}
	// A trigger type the platform does not run cannot carry a permission.
	for _, endpoint := range []string{"admin.workflows.triggers.types.permissions.set", "admin.workflows.triggers.types.permissions.lookup"} {
		if refused := call(t, http.MethodPost, endpoint, "trigger_type_id=sundial&visibility=everyone"); refused["error"] != "invalid_arguments" {
			t.Fatalf("%s refused=%v", endpoint, refused)
		}
	}
}

// TestAdminAppConfigAndResolution holds the admin.apps.config.* and
// admin.apps.clearResolution contract. An app nobody has configured answers the
// defaults, and clearing a resolution leaves the app undecided rather than
// restricted.
func TestAdminAppConfigAndResolution(t *testing.T) {
	handler := testHandler()
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	defaults := call(t, http.MethodPost, "admin.apps.config.lookup", "app_ids=A1")
	configs, ok := defaults["configs"].([]any)
	if !ok || len(configs) != 1 {
		t.Fatalf("defaults=%v", defaults)
	}
	first := configs[0].(map[string]any)
	if first["workflow_auth_strategy"] != "builder_choice" {
		t.Fatalf("default strategy=%v", first)
	}
	if urls := first["domain_restrictions"].(map[string]any)["urls"].([]any); len(urls) != 0 {
		t.Fatalf("default urls=%v", urls)
	}
	set := call(t, http.MethodPost, "admin.apps.config.set",
		`app_id=A1&workflow_auth_strategy=end_user_only&domain_restrictions={"urls":["https://example.invalid"],"emails":["ops@example.invalid"]}`)
	config := set["config"].(map[string]any)
	if config["workflow_auth_strategy"] != "end_user_only" {
		t.Fatalf("set=%v", set)
	}
	after := call(t, http.MethodGet, "admin.apps.config.lookup?app_ids=A1", "")
	stored := after["configs"].([]any)[0].(map[string]any)
	restrictions := stored["domain_restrictions"].(map[string]any)
	if len(restrictions["urls"].([]any)) != 1 || len(restrictions["emails"].([]any)) != 1 {
		t.Fatalf("after=%v", after)
	}
	for _, body := range []string{
		"app_id=A1&workflow_auth_strategy=whoever",
		`app_id=A1&domain_restrictions={"urls":`,
		"workflow_auth_strategy=end_user_only",
	} {
		if refused := call(t, http.MethodPost, "admin.apps.config.set", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	// Clearing a resolution leaves the app undecided, and clearing an undecided
	// app is not found: undecided is not the same as restricted.
	if cleared := call(t, http.MethodPost, "admin.apps.clearResolution", "app_id=A1"); cleared["ok"] != true {
		t.Fatalf("cleared=%v", cleared)
	}
	if again := call(t, http.MethodPost, "admin.apps.clearResolution", "app_id=A1"); again["error"] != "app_not_found" {
		t.Fatalf("again=%v", again)
	}
	if approved := call(t, http.MethodPost, "admin.apps.approve", "app_id=A1"); approved["ok"] != true {
		t.Fatalf("approved=%v", approved)
	}
	if reCleared := call(t, http.MethodPost, "admin.apps.clearResolution", "app_id=A1"); reCleared["ok"] != true {
		t.Fatalf("reCleared=%v", reCleared)
	}
	if missing := call(t, http.MethodPost, "admin.apps.clearResolution", "app_id=A-nobody"); missing["error"] != "app_not_found" {
		t.Fatalf("missing=%v", missing)
	}
}

// TestAdminConversationAdministration holds the remaining
// admin.conversations.* contract: the lookup, the two bulk settings and the
// object links. A batch naming a channel that is not here changes nothing.
func TestAdminConversationAdministration(t *testing.T) {
	handler := testHandler()
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	everything := call(t, http.MethodPost, "admin.conversations.lookup", "")
	channels, ok := everything["channels"].([]any)
	if !ok || len(channels) == 0 {
		t.Fatalf("a lookup naming no filter answered nothing: %v", everything)
	}
	// A member-count ceiling of zero is not a filter; it is the absence of one.
	if unfiltered := call(t, http.MethodGet, "admin.conversations.lookup?max_member_count=0", ""); len(unfiltered["channels"].([]any)) != len(channels) {
		t.Fatalf("a zero ceiling filtered: %v", unfiltered)
	}
	for _, body := range []string{"max_member_count=-1", "last_message_activity_before=-1", "last_message_activity_before=soon"} {
		if refused := call(t, http.MethodPost, "admin.conversations.lookup", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if excluded := call(t, http.MethodPost, "admin.conversations.bulkSetExcludeFromSlackAi", "channel_ids=C1&exclude_from_slack_ai_value=true"); excluded["ok"] != true {
		t.Fatalf("excluded=%v", excluded)
	}
	if missing := call(t, http.MethodPost, "admin.conversations.bulkSetExcludeFromSlackAi", "channel_ids=C1,C-nobody&exclude_from_slack_ai_value=true"); missing["error"] != "channel_not_found" {
		t.Fatalf("missing=%v", missing)
	}
	if linked := call(t, http.MethodPost, "admin.conversations.linkObjects", "channel=C1&salesforce_org_id=00D000&record_id=a01,a02"); linked["ok"] != true {
		t.Fatalf("linked=%v", linked)
	}
	if unlinked := call(t, http.MethodPost, "admin.conversations.unlinkObjects", "channels=C1"); unlinked["ok"] != true {
		t.Fatalf("unlinked=%v", unlinked)
	}
	made := call(t, http.MethodPost, "admin.conversations.createForObjects", "channel_name=record-channel&salesforce_org_id=00D000&object_id=a03")
	if made["ok"] != true || made["channel_id"] == "" {
		t.Fatalf("made=%v", made)
	}
	for _, body := range []string{"channel_name=other&salesforce_org_id=00D000", "salesforce_org_id=00D000&object_id=a04", "channel_name=other&object_id=a04"} {
		if refused := call(t, http.MethodPost, "admin.conversations.createForObjects", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if noTarget := call(t, http.MethodPost, "admin.conversations.bulkMove", "channel_ids=C1"); noTarget["error"] != "invalid_arguments" {
		t.Fatalf("noTarget=%v", noTarget)
	}
	if badTarget := call(t, http.MethodPost, "admin.conversations.bulkMove", "channel_ids=C1&target_team_id=T-nobody"); badTarget["error"] != "channel_not_found" {
		t.Fatalf("badTarget=%v", badTarget)
	}
}

// TestAppActivityLog holds the apps.activities.list and
// admin.apps.activities.list contract. An app reads its own log from its
// credential rather than from an argument, and a level the platform does not
// emit is refused rather than ignored: ignoring it would answer every entry to
// a caller that asked for a narrow set.
func TestAppActivityLog(t *testing.T) {
	handler, target := testHandlerWithStore()
	now := time.Now().UTC()
	for index, level := range []domain.ActivityLevel{domain.ActivityInfo, domain.ActivityError} {
		if err := target.RecordAppActivity(context.Background(), domain.AppActivity{
			AppID: "A1", WorkspaceID: "T1", ComponentType: "function", ComponentID: "triage",
			Level: level, EventType: "function_execution", Source: "slack", Message: string(level),
			TraceID: fmt.Sprintf("trace-%d", index), CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	listed := call(t, http.MethodPost, "admin.apps.activities.list", "app_id=A1")
	activities, ok := listed["activities"].([]any)
	if !ok || len(activities) != 2 {
		t.Fatalf("listed=%v", listed)
	}
	first := activities[0].(map[string]any)
	if first["component_type"] != "function" || first["level"] != "info" || first["source"] != "slack" {
		t.Fatalf("first=%v", first)
	}
	// warn and above is the error entry alone, though "error" sorts before
	// "info" and "warn" by name.
	errors := call(t, http.MethodGet, "admin.apps.activities.list?app_id=A1&min_log_level=warn", "")
	if len(errors["activities"].([]any)) != 1 {
		t.Fatalf("errors=%v", errors)
	}
	for _, body := range []string{"app_id=A1&min_log_level=shouted", "app_id=A1&min_date_created=soon", "app_id=A1&max_date_created=-1"} {
		if refused := call(t, http.MethodPost, "admin.apps.activities.list", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if traced := call(t, http.MethodPost, "admin.apps.activities.list", "app_id=A1&trace_id=trace-1"); len(traced["activities"].([]any)) != 1 {
		t.Fatalf("traced=%v", traced)
	}
	// The app's own read takes the app from the credential. Naming another app
	// changes nothing, because an app that could name one could read its log.
	own := call(t, http.MethodPost, "apps.activities.list", "app_id=A-somebody-else")
	entries, ok := own["activities"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("own=%v", own)
	}
	for _, entry := range entries {
		if entry.(map[string]any)["app_id"] != "A1" {
			t.Fatalf("an app read another app's log: %v", entry)
		}
	}
}

// TestAdminAnalytics holds the admin.analytics.* contract. getFile answers a
// gzipped stream of JSON lines, which is what Slack answers: the file is meant
// to be piped to a store rather than parsed out of a JSON envelope.
func TestAdminAnalytics(t *testing.T) {
	handler := testHandler()
	get := func(t *testing.T, endpoint, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		return response
	}
	decode := func(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	file := get(t, "admin.analytics.getFile", "type=member&date=2023-11-14")
	if got := file.Header().Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("content type=%q", got)
	}
	reader, err := gzip.NewReader(bytes.NewReader(file.Body.Bytes()))
	if err != nil {
		t.Fatalf("the stream is not gzip: %v", err)
	}
	lines := 0
	decoder := json.NewDecoder(reader)
	for {
		var row map[string]any
		if err := decoder.Decode(&row); err != nil {
			break
		}
		if row["date"] != "2023-11-14" || row["user_id"] == nil {
			t.Fatalf("row=%v", row)
		}
		lines++
	}
	if lines == 0 {
		t.Fatal("a member file for a workspace with members held no lines")
	}
	// metadata_only describes the columns without reading anybody's day.
	metadata := decode(t, get(t, "admin.analytics.getFile", "type=public_channel&metadata_only=true"))
	fields, ok := metadata["fields"].([]any)
	if !ok || len(fields) == 0 {
		t.Fatalf("metadata=%v", metadata)
	}
	for _, body := range []string{"type=hourly", "type=member&date=14-11-2023", "date=2023-11-14"} {
		if refused := decode(t, get(t, "admin.analytics.getFile", body)); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	activity := decode(t, get(t, "admin.analytics.messages.activity", "date=2023-11-14"))
	if _, ok := activity["activity"].([]any); !ok || activity["ok"] != true {
		t.Fatalf("activity=%v", activity)
	}
	if described := decode(t, get(t, "admin.analytics.messages.metadata", "")); len(described["fields"].([]any)) == 0 {
		t.Fatalf("metadata=%v", described)
	}
}

// TestAdminAuditBillingAndExports holds the audit allow list, the billing plan
// and the two export requests. An empty allow list is the state a workspace
// starts in, not a missing one, and an address without a reason is refused: an
// exclusion nobody explained is one nobody can review later.
func TestAdminAuditBillingAndExports(t *testing.T) {
	handler := testHandler()
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	empty := call(t, http.MethodPost, "admin.audit.anomaly.allow.getItem", "")
	item, ok := empty["anomaly_allow_updated_item"].(map[string]any)
	if !ok || len(item["ips"].([]any)) != 0 {
		t.Fatalf("empty=%v", empty)
	}
	updated := call(t, http.MethodPost, "admin.audit.anomaly.allow.updateItem", "ip_addresses=198.51.100.7,203.0.113.0/24&reasons=office")
	stored := updated["anomaly_allow_updated_item"].(map[string]any)
	if len(stored["ips"].([]any)) != 2 || len(stored["reasons"].([]any)) != 1 {
		t.Fatalf("updated=%v", updated)
	}
	if read := call(t, http.MethodGet, "admin.audit.anomaly.allow.getItem", ""); len(read["anomaly_allow_updated_item"].(map[string]any)["ips"].([]any)) != 2 {
		t.Fatalf("read=%v", read)
	}
	for _, body := range []string{"ip_addresses=198.51.100.7", "ip_addresses=the-office&reasons=office"} {
		if refused := call(t, http.MethodPost, "admin.audit.anomaly.allow.updateItem", body); refused["ok"] == true {
			t.Fatalf("body=%q was accepted: %v", body, refused)
		}
	}
	if plan := call(t, http.MethodGet, "team.billing.info", ""); plan["ok"] != true {
		t.Fatalf("plan=%v", plan)
	}
	if exported := call(t, http.MethodPost, "admin.users.unsupportedVersions.export", "date_end_of_support=1700000000"); exported["ok"] != true {
		t.Fatalf("exported=%v", exported)
	}
	for _, body := range []string{"date_end_of_support=soon", "date_sessions_started=-1"} {
		if refused := call(t, http.MethodPost, "admin.users.unsupportedVersions.export", body); refused["error"] != "invalid_arguments" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
	if noStep := call(t, http.MethodPost, "functions.workflows.steps.responses.export", "workflow_id=Wf1"); noStep["error"] != "invalid_arguments" {
		t.Fatalf("noStep=%v", noStep)
	}
	if missing := call(t, http.MethodPost, "functions.workflows.steps.responses.export", "workflow_id=Wf-nobody&step_id=intake"); missing["ok"] == true {
		t.Fatalf("an export was accepted for a workflow that is not here: %v", missing)
	}
}

// TestAppCredentialsAndAssistantSearch holds apps.icon.set,
// apps.auth.external.*, apps.user.connection.update and assistant.search.*. An
// external credential's secret never reaches the caller, and the assistant's
// context is the member's own search rather than a wider one.
func TestAppCredentialsAndAssistantSearch(t *testing.T) {
	handler, target := testHandlerWithStore()
	now := time.Now().UTC()
	if err := target.SetExternalAuthToken(context.Background(), domain.ExternalAuthToken{
		ID: "Et1", AppID: "A1", WorkspaceID: "T1", UserID: "U1", Provider: "example",
		Ciphertext: "sealed", ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}, events.Event{ID: "evt-external", WorkspaceID: "T1", Topic: "app.external_token_set", Payload: "Et1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	call := func(t *testing.T, method, endpoint, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	if icon := call(t, http.MethodPost, "apps.icon.set", "app_id=A1&image_url=https://example.invalid/icon.png"); icon["ok"] != true {
		t.Fatalf("icon=%v", icon)
	}
	for _, body := range []string{"app_id=A1&image_url=an icon", "app_id=A1", "image_url=https://example.invalid/icon.png"} {
		if refused := call(t, http.MethodPost, "apps.icon.set", body); refused["ok"] == true {
			t.Fatalf("body=%q was accepted: %v", body, refused)
		}
	}
	external := call(t, http.MethodPost, "apps.auth.external.get", "external_token_id=Et1")
	token, ok := external["external_token"].(map[string]any)
	if !ok || token["external_token_id"] != "Et1" || token["provider_name"] != "example" {
		t.Fatalf("external=%v", external)
	}
	// The secret is not in the reply under any name.
	for name, value := range token {
		if text, isText := value.(string); isText && strings.Contains(text, "sealed") {
			t.Fatalf("the credential's secret reached the caller in %q", name)
		}
	}
	if missing := call(t, http.MethodPost, "apps.auth.external.get", "external_token_id=Et-nobody"); missing["error"] != "token_not_found" {
		t.Fatalf("missing=%v", missing)
	}
	if unnamed := call(t, http.MethodPost, "apps.auth.external.get", ""); unnamed["error"] != "invalid_arguments" {
		t.Fatalf("unnamed=%v", unnamed)
	}
	if revoked := call(t, http.MethodPost, "apps.auth.external.delete", "external_token_id=Et1"); revoked["ok"] != true {
		t.Fatalf("revoked=%v", revoked)
	}
	if again := call(t, http.MethodPost, "apps.auth.external.delete", "external_token_id=Et1"); again["error"] != "token_not_found" {
		t.Fatalf("again=%v", again)
	}
	if connection := call(t, http.MethodPost, "apps.user.connection.update", ""); connection["ok"] != true {
		t.Fatalf("connection=%v", connection)
	}
	info := call(t, http.MethodGet, "assistant.search.info", "")
	if info["enabled"] != true || len(info["searchable_sources"].([]any)) == 0 {
		t.Fatalf("info=%v", info)
	}
	found := call(t, http.MethodPost, "assistant.search.context", "query=hello")
	if _, ok := found["results"].(map[string]any)["messages"].([]any); !ok {
		t.Fatalf("found=%v", found)
	}
	if empty := call(t, http.MethodPost, "assistant.search.context", "query=%20"); empty["error"] != "invalid_arguments" {
		t.Fatalf("empty=%v", empty)
	}
}

func TestAdminUsersSessionResetIsRegistered(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/admin.users.session.reset", strings.NewReader("team_id=T1&user_id=U2"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"error":"user_not_found"`) {
		t.Fatalf("reset status=%d body=%s", response.Code, response.Body)
	}
}

func TestAdminConversationMutationsAreRegistered(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		body     string
	}{
		{endpoint: "admin.conversations.rename", body: "channel_id=C1&name=renamed"},
		{endpoint: "admin.conversations.archive", body: "channel_id=C1"},
		{endpoint: "admin.conversations.unarchive", body: "channel_id=C2"},
	} {
		handler := testHandler()
		request := httptest.NewRequest(http.MethodPost, "/api/"+test.endpoint, strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
			t.Fatalf("%s status=%d body=%s", test.endpoint, response.Code, response.Body)
		}
	}
}

func TestAdminConversationDeleteRemovesPublicChannel(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.delete", strings.NewReader("channel_id=C1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"ok":true}`+"\n" {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/conversations.info?channel=C1", nil)
	info.Header.Set("Authorization", "Bearer token")
	after := httptest.NewRecorder()
	handler.ServeHTTP(after, info)
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"error":"channel_not_found"`) {
		t.Fatalf("deleted channel status=%d body=%s", after.Code, after.Body)
	}
}

func TestAdminConversationCreateUsesDurableConversationBoundary(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.create", strings.NewReader("name=admin-created&is_private=true"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"admin-created"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestAdminConversationInviteIsRegistered(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.invite", strings.NewReader("channel_id=C1&users=U2"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestAdminConversationConvertToPrivateIsRegistered(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.convertToPrivate", strings.NewReader("channel_id=C1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"is_private":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestAdminConversationPrefsLifecycle(t *testing.T) {
	handler := testHandler()
	set := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.setConversationPrefs", strings.NewReader(`channel_id=C1&prefs={"can_thread":{"type":["everyone"],"user":["U1"]},"who_can_post":{"type":["admin"],"user":[]}}`))
	set.Header.Set("Authorization", "Bearer token")
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	changed := httptest.NewRecorder()
	handler.ServeHTTP(changed, set)
	if changed.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", changed.Code, changed.Body)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/admin.conversations.getConversationPrefs?channel_id=C1", nil)
	get.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, get)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"everyone"`) {
		t.Fatalf("get status=%d body=%s", result.Code, result.Body)
	}
}

func TestRemoteFileLifecycle(t *testing.T) {
	handler := testHandler()
	add := httptest.NewRequest(http.MethodPost, "/api/files.remote.add", strings.NewReader("external_id=remote-1&title=Remote%20file&external_url=https%3A%2F%2Ffiles.example%2Fdoc"))
	add.Header.Set("Authorization", "Bearer token")
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, add)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"external_id":"remote-1"`) {
		t.Fatalf("add status=%d body=%s", created.Code, created.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/files.remote.list?limit=10", nil)
	list.Header.Set("Authorization", "Bearer token")
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"external_id":"remote-1"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	share := httptest.NewRequest(http.MethodGet, "/api/files.remote.share?external_id=remote-1&channels=C1%2CC1", nil)
	share.Header.Set("Authorization", "Bearer token")
	shared := httptest.NewRecorder()
	handler.ServeHTTP(shared, share)
	if shared.Code != http.StatusOK || !strings.Contains(shared.Body.String(), `"channels":["C1"]`) {
		t.Fatalf("share status=%d body=%s", shared.Code, shared.Body)
	}
	update := httptest.NewRequest(http.MethodPost, "/api/files.remote.update", strings.NewReader("external_id=remote-1&title=Updated%20file"))
	update.Header.Set("Authorization", "Bearer token")
	update.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"title":"Updated file"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/files.remote.info?external_id=remote-1", nil)
	info.Header.Set("Authorization", "Bearer token")
	infoResult := httptest.NewRecorder()
	handler.ServeHTTP(infoResult, info)
	if infoResult.Code != http.StatusOK || !strings.Contains(infoResult.Body.String(), `"external_id":"remote-1"`) {
		t.Fatalf("info status=%d body=%s", infoResult.Code, infoResult.Body)
	}
	remove := httptest.NewRequest(http.MethodPost, "/api/files.remote.remove", strings.NewReader("external_id=remote-1"))
	remove.Header.Set("Authorization", "Bearer token")
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	removeResult := httptest.NewRecorder()
	handler.ServeHTTP(removeResult, remove)
	if removeResult.Code != http.StatusOK || !strings.Contains(removeResult.Body.String(), `"ok":true`) {
		t.Fatalf("remove status=%d body=%s", removeResult.Code, removeResult.Body)
	}
}

func TestAPITestDoesNotRequireAuthentication(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "success", path: "/api/api.test", want: `{"ok":true}`},
		{name: "artificial error", path: "/api/api.test?error=synthetic", want: `"error":"synthetic"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, strings.NewReader(test.body))
			res := httptest.NewRecorder()
			testHandler().ServeHTTP(res, req)
			if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), test.want) {
				t.Fatalf("status=%d body=%s", res.Code, res.Body)
			}
		})
	}
}

func TestAuthTestIncludesContractIdentityFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/auth.test", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	for _, field := range []string{`"team_id":"T1"`, `"user_id":"U1"`} {
		if !strings.Contains(res.Body.String(), field) {
			t.Fatalf("body=%s missing %s", res.Body, field)
		}
	}
}

func TestLookupUserByEmail(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/users.lookupByEmail?email=ALICE%40EXAMPLE.COM", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"U1"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestGetPermalink(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=permalink"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	if posted.Code != http.StatusOK {
		t.Fatalf("post status=%d body=%s", posted.Code, posted.Body)
	}
	var response struct {
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(posted.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	permalink := httptest.NewRequest(http.MethodGet, "/api/chat.getPermalink?channel=C1&message_ts="+response.TS, nil)
	permalink.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, permalink)
	// The permalink is Slack's shape on THIS deployment's origin. It used to
	// name sameoldchat.local, a host that exists nowhere, so every permalink
	// this product handed out was unfollowable; the path is now served by
	// internal/web's /archives route.
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"permalink":"/archives/C1/p`) {
		t.Fatalf("permalink status=%d body=%s", result.Code, result.Body)
	}
	if strings.Contains(result.Body.String(), "sameoldchat.local") {
		t.Fatalf("permalink still names a host that does not exist: %s", result.Body)
	}
}

func TestMeMessageUsesNarrowResponse(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.meMessage", strings.NewReader("channel=C1&text=action"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"ok":true`) || !strings.Contains(res.Body.String(), `"channel":"C1"`) || strings.Contains(res.Body.String(), `"message"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestPostEphemeralReturnsMessageTimestamp(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.postEphemeral", strings.NewReader("channel=C1&user=U2&text=temporary"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"message_ts"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestRenameConversationNormalizesAndPersists(t *testing.T) {
	handler := testHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/conversations.rename", strings.NewReader("channel=C1&name= New Room "))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"name":"new-room"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	topic := httptest.NewRequest(http.MethodPost, "/api/conversations.setTopic", strings.NewReader("channel=C1&topic=Project%20discussion"))
	topic.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	topic.Header.Set("Authorization", "Bearer token")
	topicResult := httptest.NewRecorder()
	handler.ServeHTTP(topicResult, topic)
	if topicResult.Code != http.StatusOK || !strings.Contains(topicResult.Body.String(), `"value":"Project discussion"`) {
		t.Fatalf("topic status=%d body=%s", topicResult.Code, topicResult.Body)
	}
	purpose := httptest.NewRequest(http.MethodPost, "/api/conversations.setPurpose", strings.NewReader("channel=C1&purpose=For%20planning"))
	purpose.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	purpose.Header.Set("Authorization", "Bearer token")
	purposeResult := httptest.NewRecorder()
	handler.ServeHTTP(purposeResult, purpose)
	if purposeResult.Code != http.StatusOK || !strings.Contains(purposeResult.Body.String(), `"value":"For planning"`) {
		t.Fatalf("purpose status=%d body=%s", purposeResult.Code, purposeResult.Body)
	}
	archive := httptest.NewRequest(http.MethodPost, "/api/conversations.archive", strings.NewReader("channel=C1"))
	archive.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	archive.Header.Set("Authorization", "Bearer token")
	archiveResult := httptest.NewRecorder()
	handler.ServeHTTP(archiveResult, archive)
	if archiveResult.Code != http.StatusOK || archiveResult.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("archive status=%d body=%s", archiveResult.Code, archiveResult.Body)
	}
	unarchive := httptest.NewRequest(http.MethodPost, "/api/conversations.unarchive", strings.NewReader("channel=C1"))
	unarchive.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unarchive.Header.Set("Authorization", "Bearer token")
	unarchiveResult := httptest.NewRecorder()
	handler.ServeHTTP(unarchiveResult, unarchive)
	if unarchiveResult.Code != http.StatusOK || unarchiveResult.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("unarchive status=%d body=%s", unarchiveResult.Code, unarchiveResult.Body)
	}
	kick := httptest.NewRequest(http.MethodPost, "/api/conversations.kick", strings.NewReader("channel=C1&user=U2"))
	kick.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	kick.Header.Set("Authorization", "Bearer token")
	kickResult := httptest.NewRecorder()
	handler.ServeHTTP(kickResult, kick)
	if kickResult.Code != http.StatusOK {
		t.Fatalf("kick status=%d body=%s", kickResult.Code, kickResult.Body)
	}
}

func TestInviteConversationNormalizesMembers(t *testing.T) {
	handler, repository := testHandlerWithStore()
	if err := repository.RemoveConversationMember(context.Background(), "C1", "U2", events.Event{ID: "E-remove-before-invite", WorkspaceID: "T1", ActorID: "U1", Topic: "conversation.member_removed", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversations.invite", strings.NewReader("channel=C1&users=U2,U2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"C1"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	duplicate := httptest.NewRequest(http.MethodPost, "/api/conversations.invite", strings.NewReader("channel=C1&users=U2"))
	duplicate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	duplicate.Header.Set("Authorization", "Bearer token")
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusOK || !strings.Contains(duplicateResponse.Body.String(), `"error":"already_in_channel"`) {
		t.Fatalf("duplicate status=%d body=%s", duplicateResponse.Code, duplicateResponse.Body)
	}
}

func TestInviteConversationReportsPerUserFailuresAndIsAtomicByDefault(t *testing.T) {
	handler, repository := testHandlerWithStore()
	repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"})

	response := callSlackForm(t, handler, "/api/conversations.invite", "channel=C1&users=U2,U-missing,U1,U3")
	body := response.Body.String()
	for _, fragment := range []string{
		`"ok":false`,
		`"error":"already_in_channel"`,
		`"user":"U2"`,
		`"user":"U-missing"`,
		`"error":"user_not_found"`,
		`"user":"U1"`,
		`"error":"cant_invite_self"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body=%s missing %s", body, fragment)
		}
	}
	member, err := repository.IsConversationMember(context.Background(), "C1", "U3")
	if err != nil || member {
		t.Fatalf("U3 member=%v err=%v; default invite must be atomic", member, err)
	}
}

func TestInviteConversationForceInvitesValidSubset(t *testing.T) {
	handler, repository := testHandlerWithStore()
	repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"})

	response := callSlackForm(t, handler, "/api/conversations.invite", "channel=C1&users=U-missing,U3&force=true")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	member, err := repository.IsConversationMember(context.Background(), "C1", "U3")
	if err != nil || !member {
		t.Fatalf("U3 member=%v err=%v", member, err)
	}
	member, err = repository.IsConversationMember(context.Background(), "C1", "U-missing")
	if err != nil || member {
		t.Fatalf("missing user member=%v err=%v", member, err)
	}
}

func TestInviteConversationValidatesForceAndCurrentHundredUserLimit(t *testing.T) {
	handler := testHandler()
	invalidForce := callSlackForm(t, handler, "/api/conversations.invite", "channel=C1&users=U3&force=occasionally")
	if invalidForce.Code != http.StatusOK || !strings.Contains(invalidForce.Body.String(), `"error":"invalid_arg_name"`) {
		t.Fatalf("invalid force status=%d body=%s", invalidForce.Code, invalidForce.Body)
	}

	users := make([]string, 101)
	for index := range users {
		users[index] = "U" + strconv.Itoa(index+100)
	}
	tooMany := callSlackForm(t, handler, "/api/conversations.invite", "channel=C1&users="+strings.Join(users, ","))
	if tooMany.Code != http.StatusOK || !strings.Contains(tooMany.Body.String(), `"error":"too_many_users"`) {
		t.Fatalf("too many status=%d body=%s", tooMany.Code, tooMany.Body)
	}
}

func TestInviteConversationSupportsPrivateChannels(t *testing.T) {
	handler, repository := testHandlerWithStore()
	repository.SeedConversation(domain.Conversation{ID: "C-private", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
	repository.SeedConversationMember("C-private", "U1")
	req := httptest.NewRequest(http.MethodPost, "/api/conversations.invite", strings.NewReader("channel=C-private&users=U2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"C-private"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	activity, err := repository.ListActivity(context.Background(), "T1", "U2", domain.ActivityQuery{
		Kinds: []domain.ActivityKind{domain.ActivityInvitation}, Page: domain.PageRequest{Limit: 10},
	})
	if err != nil || len(activity.Items) != 1 || !activity.Items[0].SourceAvailable {
		t.Fatalf("private invitation activity=%+v err=%v", activity, err)
	}
}

func TestInviteConversationUsesCurrentTokenAndChannelScopeMatrix(t *testing.T) {
	t.Run("bot public channels write invites", func(t *testing.T) {
		handler, repository := testHandlerWithScopes(auth.ScopeChannelsWriteInvites)
		if err := repository.RemoveConversationMember(context.Background(), "C1", "U2", events.Event{ID: "E-scope-remove", WorkspaceID: "T1", ActorID: "U1", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		response := callSlackForm(t, handler, "/api/conversations.invite", "channel=C1&users=U2")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	})

	t.Run("bot private groups write", func(t *testing.T) {
		handler, repository := testHandlerWithScopes(auth.ScopeGroupsWrite)
		repository.SeedConversation(domain.Conversation{ID: "C-private-scope", WorkspaceID: "T1", Name: "private-scope", Kind: domain.ConversationTypePrivate})
		repository.SeedConversationMember("C-private-scope", "U1")
		response := callSlackForm(t, handler, "/api/conversations.invite", "channel=C-private-scope&users=U2")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	})

	t.Run("public scope cannot invite to private channel", func(t *testing.T) {
		handler, repository := testHandlerWithScopes(auth.ScopeChannelsManage)
		repository.SeedConversation(domain.Conversation{ID: "C-private-scope", WorkspaceID: "T1", Name: "private-scope", Kind: domain.ConversationTypePrivate})
		repository.SeedConversationMember("C-private-scope", "U1")
		response := callSlackForm(t, handler, "/api/conversations.invite", "channel=C-private-scope&users=U2")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"error":"missing_scope"`) || !strings.Contains(response.Body.String(), `"needed":"groups:write"`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	})

	t.Run("user public channels write", func(t *testing.T) {
		repository := memory.New()
		repository.SeedWorkspace(domain.Workspace{ID: "T1"})
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
		repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
		repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
		repository.SeedConversationMember("C1", "U1")
		authenticator, err := auth.NewStatic("user-token", auth.Principal{
			WorkspaceID: "T1", UserID: "U1", TokenType: "user",
			Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsWrite: {}},
		})
		if err != nil {
			t.Fatal(err)
		}
		api, err := NewHandler(service.Messages{Store: repository}, authenticator)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		api.Register(mux)
		request := httptest.NewRequest(http.MethodPost, "/api/conversations.invite", strings.NewReader("channel=C1&users=U2"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer user-token")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	})
}

func callSlackForm(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestPostMessageForm(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=hello"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}
	var body struct {
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK {
		t.Fatal("response was not ok")
	}
}

func TestPostMessageRejectsArchivedChannelWithSlackCode(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C2&text=hello"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"error":"is_archived"`) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
}

func TestChatUnfurlPersistsMetadata(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=link"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	var body struct {
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(posted.Body).Decode(&body); err != nil || body.TS == "" {
		t.Fatalf("post body=%s err=%v", posted.Body, err)
	}
	unfurl := httptest.NewRequest(http.MethodPost, "/api/chat.unfurl", strings.NewReader("channel=C1&ts="+body.TS+"&unfurls=%7B%22https%3A%2F%2Fexample.com%22%3A%7B%22title%22%3A%22Example%22%7D%7D"))
	unfurl.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unfurl.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, unfurl)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"unfurls":{"https://example.com":{"title":"Example"}}`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestPostMessageJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", bytes.NewBufferString(`{"channel":"C1","text":"json hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "json hello") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestEmptyJSONBodyIsAnEmptyArgumentObject(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/api.test", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `{"ok":true}`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestGetUserProfile(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/users.profile.get?user=U1", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"display_name":"alice"`) || !strings.Contains(res.Body.String(), `"team":"T1"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestUserReadsOmitEmailWithoutEmailScope(t *testing.T) {
	handler, _ := testHandlerWithScopes(auth.ScopeUsersRead, auth.ScopeUsersProfileRead)
	for _, path := range []string{"/api/users.profile.get?user=U1", "/api/users.info?user=U1", "/api/users.list"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"display_name":"alice"`) {
			t.Fatalf("%s: status=%d body=%s", path, res.Code, res.Body)
		}
		if strings.Contains(res.Body.String(), `"email"`) || strings.Contains(res.Body.String(), "alice@example.com") {
			t.Fatalf("%s: email escaped users:read.email boundary: %s", path, res.Body)
		}
	}
}

func TestTeamInfo(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/team.info", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"T1"`) || !strings.Contains(res.Body.String(), `"name":"test"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestAuthTeamsListPagesEveryApprovedWorkspaceAndOptionalIcons(t *testing.T) {
	handler, repository := testHandlerWithStore()
	repository.SeedWorkspace(domain.Workspace{ID: "T2", Name: "second", IconURL: "https://cdn.example.test/team.png"})
	if err := repository.CreateAppInstallation(context.Background(), domain.AppInstallation{
		AppID: "A1", WorkspaceID: "T2", Enabled: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	call := func(path string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body)
		}
		var value map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := call("/api/auth.teams.list?limit=1&include_icon=true")
	teams, _ := first["teams"].([]any)
	if len(teams) != 1 || teams[0].(map[string]any)["id"] != "T1" {
		t.Fatalf("first=%v", first)
	}
	metadata, _ := first["response_metadata"].(map[string]any)
	cursor, _ := metadata["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("first page has no cursor: %v", first)
	}
	second := call("/api/auth.teams.list?limit=1&include_icon=true&cursor=" + url.QueryEscape(cursor))
	teams, _ = second["teams"].([]any)
	if len(teams) != 1 || teams[0].(map[string]any)["id"] != "T2" {
		t.Fatalf("second=%v", second)
	}
	icon, _ := teams[0].(map[string]any)["icon"].(map[string]any)
	if icon["image_132"] != "https://cdn.example.test/team.png" || icon["image_default"] != false {
		t.Fatalf("icon=%v", icon)
	}
}

func TestAuthRevokeDurablyInvalidatesToken(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes()})
	authenticator, err := auth.NewStored(s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	revoke := httptest.NewRequest(http.MethodGet, "/api/auth.revoke", nil)
	revoke.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, revoke)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"revoked":true`) {
		t.Fatalf("revoke status=%d body=%s", result.Code, result.Body)
	}
	check := httptest.NewRequest(http.MethodGet, "/api/auth.test", nil)
	check.Header.Set("Authorization", "Bearer token")
	checkResult := httptest.NewRecorder()
	mux.ServeHTTP(checkResult, check)
	// A revoked token is `token_revoked`, not `not_authed`. The two are distinct
	// members of the same pinned `error` enum, and this assertion used to accept
	// the code that means "you sent no credential" for a credential this
	// deployment issued and then withdrew — so nothing here could tell a revoked
	// token from an absent one.
	if checkResult.Code != http.StatusOK || !strings.Contains(checkResult.Body.String(), `"error":"token_revoked"`) {
		t.Fatalf("revoked auth status=%d body=%s", checkResult.Code, checkResult.Body)
	}
	// And an absent credential must still be `not_authed`, so the two cases stay
	// distinguishable in both directions.
	anonymous := httptest.NewRequest(http.MethodGet, "/api/auth.test", nil)
	anonymousResult := httptest.NewRecorder()
	mux.ServeHTTP(anonymousResult, anonymous)
	if anonymousResult.Code != http.StatusOK || !strings.Contains(anonymousResult.Body.String(), `"error":"not_authed"`) {
		t.Fatalf("anonymous auth status=%d body=%s", anonymousResult.Code, anonymousResult.Body)
	}
}

// A credential store that did not answer is server trouble, not an
// authentication outcome. Answering `invalid_auth` made official clients
// discard their token and re-authenticate during every store outage;
// `fatal_error` tells them to retry with the credential they already hold.
func TestCredentialStoreOutageAnswersFatalErrorNotInvalidAuth(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeAuthError(recorder, fmt.Errorf("%w: connection refused", auth.ErrCredentialStoreUnavailable))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"error":"fatal_error"`) {
		t.Fatalf("outage status=%d body=%s", recorder.Code, recorder.Body)
	}
	if strings.Contains(recorder.Body.String(), "invalid_auth") || strings.Contains(recorder.Body.String(), "not_authed") {
		t.Fatalf("outage answered an authentication outcome: %s", recorder.Body)
	}
}

func TestJSONDuplicateFieldsAreRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", bytes.NewBufferString(`{"channel":"C1","channel":"C2","text":"duplicate"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	// A duplicated JSON key is a malformed JSON document, not malformed form data:
	// `invalid_json` is the code the pinned enums declare for it, and the blanket
	// `invalid_form_data` told the caller to look at the wrong encoding.
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"error":"invalid_json"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestFormDuplicateFieldsAcceptIdenticalValuesAndRejectConflicts(t *testing.T) {
	identical := httptest.NewRequest(http.MethodPost, "/api/conversations.replies", strings.NewReader("channel=C1&ts=1.000000&ts=1.000000"))
	identical.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fields, err := decodeFields(httptest.NewRecorder(), identical)
	if err != nil || fields["ts"] != "1.000000" {
		t.Fatalf("identical fields=%v err=%v", fields, err)
	}

	conflicting := httptest.NewRequest(http.MethodPost, "/api/conversations.replies", strings.NewReader("channel=C1&ts=1.000000&ts=2.000000"))
	conflicting.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := decodeFields(httptest.NewRecorder(), conflicting); err == nil {
		t.Fatal("conflicting form fields were accepted")
	}
}

func TestUpdateAndDeleteMessage(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=before"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	postResult := httptest.NewRecorder()
	handler.ServeHTTP(postResult, post)
	var posted struct {
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(postResult.Body).Decode(&posted); err != nil {
		t.Fatal(err)
	}
	update := httptest.NewRequest(http.MethodPost, "/api/chat.update", strings.NewReader("channel=C1&ts="+posted.TS+"&text=after"))
	update.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	update.Header.Set("Authorization", "Bearer token")
	updateResult := httptest.NewRecorder()
	handler.ServeHTTP(updateResult, update)
	if updateResult.Code != http.StatusOK || !strings.Contains(updateResult.Body.String(), "after") {
		t.Fatalf("update status=%d body=%s", updateResult.Code, updateResult.Body)
	}
	remove := httptest.NewRequest(http.MethodPost, "/api/chat.delete", strings.NewReader("channel=C1&ts="+posted.TS))
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	remove.Header.Set("Authorization", "Bearer token")
	removeResult := httptest.NewRecorder()
	handler.ServeHTTP(removeResult, remove)
	if removeResult.Code != http.StatusOK || !strings.Contains(removeResult.Body.String(), `"ok":true`) {
		t.Fatalf("delete status=%d body=%s", removeResult.Code, removeResult.Body)
	}
}

func TestPostMessageIdempotencyKeyReturnsOriginalResponse(t *testing.T) {
	handler := testHandler()
	post := func(text string) string {
		req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text="+text))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Idempotency-Key", "api-request-1")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", res.Code, res.Body)
		}
		return res.Body.String()
	}
	first, second := post("first"), post("different retry")
	if first != second || !strings.Contains(second, "first") {
		t.Fatalf("first=%s second=%s", first, second)
	}
}

func TestHistoryPostForm(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=hello"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	postResult := httptest.NewRecorder()
	handler.ServeHTTP(postResult, post)
	history := httptest.NewRequest(http.MethodPost, "/api/conversations.history", strings.NewReader("channel=C1&limit=1"))
	history.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	history.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, history)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "hello") {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestSearchMessages(t *testing.T) {
	_, repository := testHandlerWithStore()
	handler := userSearchHandler(t, repository)
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=searchable hello"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer user-token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	search := httptest.NewRequest(http.MethodGet, "/api/search.messages?query=hello", nil)
	search.Header.Set("Authorization", "Bearer user-token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, search)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "searchable hello") {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	var payload struct {
		Messages struct {
			Total      int `json:"total"`
			Matches    []map[string]any
			Pagination struct {
				PerPage int `json:"per_page"`
			} `json:"pagination"`
			Paging struct {
				Count int `json:"count"`
			} `json:"paging"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Messages.Total != 1 || payload.Messages.Pagination.PerPage != 20 || payload.Messages.Paging.Count != 20 {
		t.Fatalf("search paging does not match Slack's default envelope: %s", result.Body)
	}
	if channel, ok := payload.Messages.Matches[0]["channel"].(map[string]any); !ok || channel["id"] != "C1" {
		t.Fatalf("search match omitted its channel object: %s", result.Body)
	}
}

func TestSearchAllAndFilesUseOfficialUserTokenAndPagingContracts(t *testing.T) {
	_, repository := testHandlerWithStore()
	handler := userSearchHandler(t, repository)
	file := domain.File{ID: "FSEARCH", WorkspaceID: "T1", Uploader: "U1", Name: "searchable.txt", Title: "Searchable notes", MIMEType: "text/plain", BlobKey: "searchable", CreatedAt: time.Unix(1_700_000_100, 0).UTC()}
	if err := repository.CreateFile(context.Background(), file, events.Event{ID: "EFSEARCH", WorkspaceID: "T1", Topic: "file.created", CreatedAt: file.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=searchable message"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer user-token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	if posted.Code != http.StatusOK {
		t.Fatalf("post status=%d body=%s", posted.Code, posted.Body)
	}
	for _, target := range []string{
		"/api/search.files?query=searchable&count=1&page=1&sort=timestamp&sort_dir=desc",
		"/api/search.all?query=searchable&count=1&page=1&sort=timestamp&sort_dir=desc",
	} {
		result := getAPIWithToken(handler, target, "user-token")
		if result.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, result.Code, result.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		files, ok := payload["files"].(map[string]any)
		if !ok || files["total"].(float64) != 1 || len(files["matches"].([]any)) != 1 {
			t.Fatalf("%s files envelope=%v", target, payload["files"])
		}
		if strings.Contains(target, "search.all") {
			messages, ok := payload["messages"].(map[string]any)
			if !ok || messages["total"].(float64) < 1 || len(messages["matches"].([]any)) != 1 {
				t.Fatalf("%s messages envelope=%v", target, payload["messages"])
			}
		}
	}
	if code := errorCode(t, getAPIWithToken(handler, "/api/search.files", "user-token")); code != "no_query" {
		t.Fatalf("missing query code=%q, want no_query", code)
	}
	if code := errorCode(t, getAPIWithToken(handler, "/api/search.all?query=x&sort=unknown", "user-token")); code != "invalid_arg_name" {
		t.Fatalf("invalid sort code=%q, want invalid_arg_name", code)
	}
	botHandler := testHandler()
	if code := errorCode(t, getAPI(botHandler, "/api/search.messages?query=x")); code != "not_allowed_token_type" {
		t.Fatalf("bot search code=%q, want not_allowed_token_type", code)
	}
}

func userSearchHandler(t *testing.T, repository *memory.Store) http.Handler {
	t.Helper()
	scopes := make(map[auth.Scope]struct{}, len(auth.AllScopes()))
	for _, scope := range auth.AllScopes() {
		scopes[auth.Scope(scope)] = struct{}{}
	}
	authenticator, err := auth.NewStatic("user-token", auth.Principal{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user", Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: repository}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux
}

func TestFileMessageResponseMatchesSlackFileShareShape(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	response := messageResponse(domain.Message{
		ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", CreatedAt: created,
		Files: []domain.File{{ID: "F1", WorkspaceID: "T1", Uploader: "U1", Name: "report.txt", Title: "Report", MIMEType: "text/plain", Size: 12, CreatedAt: created, SharedChannels: []domain.ConversationID{"C1"}}},
	})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, expected := range []string{`"subtype":"file_share"`, `"upload":true`, `"files":[`, `"id":"F1"`, `"mode":"hosted"`, `"url_private":"/api/files/F1"`, `"channels":["C1"]`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("file share response is missing %s: %s", expected, body)
		}
	}
}

func TestFileMetadataEndpoints(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: s, Blob: blobs}
	file, err := messages.UploadFile(context.Background(), "T1", "U1", "a.txt", "A", "text/plain", 3, strings.NewReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeFilesRead: {}, auth.ScopeFilesWrite: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	var uploadBody bytes.Buffer
	writer := multipart.NewWriter(&uploadBody)
	if err := writer.WriteField("title", "Uploaded"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "upload.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("uploaded")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRequest(http.MethodPost, "/api/files.upload", &uploadBody)
	upload.Header.Set("Authorization", "Bearer token")
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	uploadResult := httptest.NewRecorder()
	mux.ServeHTTP(uploadResult, upload)
	if uploadResult.Code != http.StatusOK || !strings.Contains(uploadResult.Body.String(), "upload.txt") {
		t.Fatalf("upload status=%d body=%s", uploadResult.Code, uploadResult.Body)
	}
	download := httptest.NewRequest(http.MethodGet, "/api/files/"+string(file.ID), nil)
	download.Header.Set("Authorization", "Bearer token")
	downloadResult := httptest.NewRecorder()
	mux.ServeHTTP(downloadResult, download)
	if downloadResult.Code != http.StatusOK || downloadResult.Body.String() != "abc" {
		t.Fatalf("download status=%d body=%q", downloadResult.Code, downloadResult.Body.String())
	}
	info := httptest.NewRequest(http.MethodGet, "/api/files.info?file="+string(file.ID), nil)
	info.Header.Set("Authorization", "Bearer token")
	infoResult := httptest.NewRecorder()
	mux.ServeHTTP(infoResult, info)
	if infoResult.Code != http.StatusOK || !strings.Contains(infoResult.Body.String(), string(file.ID)) {
		t.Fatalf("info status=%d body=%s", infoResult.Code, infoResult.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/files.list?limit=10", nil)
	list.Header.Set("Authorization", "Bearer token")
	listResult := httptest.NewRecorder()
	mux.ServeHTTP(listResult, list)
	if listResult.Code != http.StatusOK || !strings.Contains(listResult.Body.String(), "a.txt") {
		t.Fatalf("list status=%d body=%s", listResult.Code, listResult.Body)
	}
	shared := httptest.NewRequest(http.MethodPost, "/api/files.sharedPublicURL", strings.NewReader("file="+string(file.ID)))
	shared.Header.Set("Authorization", "Bearer token")
	shared.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sharedResult := httptest.NewRecorder()
	mux.ServeHTTP(sharedResult, shared)
	if sharedResult.Code != http.StatusOK || !strings.Contains(sharedResult.Body.String(), `"permalink_public"`) {
		t.Fatalf("share public status=%d body=%s", sharedResult.Code, sharedResult.Body)
	}
	revoke := httptest.NewRequest(http.MethodPost, "/api/files.revokePublicURL", strings.NewReader("file="+string(file.ID)))
	revoke.Header.Set("Authorization", "Bearer token")
	revoke.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	revokeResult := httptest.NewRecorder()
	mux.ServeHTTP(revokeResult, revoke)
	if revokeResult.Code != http.StatusOK || !strings.Contains(revokeResult.Body.String(), `"ok":true`) {
		t.Fatalf("revoke public status=%d body=%s", revokeResult.Code, revokeResult.Body)
	}
	remove := httptest.NewRequest(http.MethodPost, "/api/files.delete", strings.NewReader("file="+string(file.ID)))
	remove.Header.Set("Authorization", "Bearer token")
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	removeResult := httptest.NewRecorder()
	mux.ServeHTTP(removeResult, remove)
	if removeResult.Code != http.StatusOK || !strings.Contains(removeResult.Body.String(), `"ok":true`) {
		t.Fatalf("delete status=%d body=%s", removeResult.Code, removeResult.Body)
	}
}

func TestConversationAndUserInfo(t *testing.T) {
	handler := testHandler()
	conversation := httptest.NewRequest(http.MethodGet, "/api/conversations.info?channel=C1", nil)
	conversation.Header.Set("Authorization", "Bearer token")
	conversationResult := httptest.NewRecorder()
	handler.ServeHTTP(conversationResult, conversation)
	if conversationResult.Code != http.StatusOK || !strings.Contains(conversationResult.Body.String(), `"name":"general"`) {
		t.Fatalf("conversation status=%d body=%s", conversationResult.Code, conversationResult.Body)
	}
	user := httptest.NewRequest(http.MethodPost, "/api/users.info", strings.NewReader("user=U1"))
	user.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	user.Header.Set("Authorization", "Bearer token")
	userResult := httptest.NewRecorder()
	handler.ServeHTTP(userResult, user)
	if userResult.Code != http.StatusOK || !strings.Contains(userResult.Body.String(), `"id":"U1"`) || !strings.Contains(userResult.Body.String(), `"display_name":"alice"`) {
		t.Fatalf("user status=%d body=%s", userResult.Code, userResult.Body)
	}
}

func TestPresenceEndpointsPersistAndNormalize(t *testing.T) {
	handler := testHandler()
	get := httptest.NewRequest(http.MethodGet, "/api/users.getPresence", nil)
	get.Header.Set("Authorization", "Bearer token")
	getResult := httptest.NewRecorder()
	handler.ServeHTTP(getResult, get)
	if getResult.Code != http.StatusOK || !strings.Contains(getResult.Body.String(), `"presence":"active"`) {
		t.Fatalf("initial presence status=%d body=%s", getResult.Code, getResult.Body)
	}
	set := httptest.NewRequest(http.MethodPost, "/api/users.setPresence", strings.NewReader("presence=away"))
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	set.Header.Set("Authorization", "Bearer token")
	setResult := httptest.NewRecorder()
	handler.ServeHTTP(setResult, set)
	if setResult.Code != http.StatusOK || setResult.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("set presence status=%d body=%s", setResult.Code, setResult.Body)
	}
	get = httptest.NewRequest(http.MethodGet, "/api/users.getPresence?user=U1", nil)
	get.Header.Set("Authorization", "Bearer token")
	getResult = httptest.NewRecorder()
	handler.ServeHTTP(getResult, get)
	if getResult.Code != http.StatusOK || !strings.Contains(getResult.Body.String(), `"presence":"away"`) {
		t.Fatalf("updated presence status=%d body=%s", getResult.Code, getResult.Body)
	}
}

func TestUserProfileSet(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/users.profile.set", strings.NewReader(`profile={"display_name":"new-name","status_text":"Ready","status_emoji":":white_check_mark:","status_expiration":4102444800,"image_24":"","image_32":"","image_48":"","image_72":"","image_192":"","image_512":"","image_1024":""}`))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"display_name":"new-name"`) || !strings.Contains(result.Body.String(), `"status_expiration":4102444800`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	partial := httptest.NewRequest(http.MethodPost, "/api/users.profile.set", strings.NewReader(`profile={"status_text":"Still here"}`))
	partial.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	partial.Header.Set("Authorization", "Bearer token")
	partialResult := httptest.NewRecorder()
	handler.ServeHTTP(partialResult, partial)
	if partialResult.Code != http.StatusOK || !strings.Contains(partialResult.Body.String(), `"display_name":"new-name"`) || !strings.Contains(partialResult.Body.String(), `"status_text":"Still here"`) {
		t.Fatalf("partial status=%d body=%s", partialResult.Code, partialResult.Body)
	}
	past := httptest.NewRequest(http.MethodPost, "/api/users.profile.set", strings.NewReader(`profile={"status_text":"Already gone","status_expiration":1}`))
	past.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	past.Header.Set("Authorization", "Bearer token")
	pastResult := httptest.NewRecorder()
	handler.ServeHTTP(pastResult, past)
	if pastResult.Code != http.StatusOK || pastResult.Body.String() != "{\"error\":\"invalid_profile\",\"ok\":false}\n" {
		t.Fatalf("past expiration status=%d body=%s", pastResult.Code, pastResult.Body)
	}
}

func TestUserProfileSetOmitsEmailWithoutEmailScope(t *testing.T) {
	handler, _ := testHandlerWithScopes(auth.ScopeUsersProfileWrite)
	request := httptest.NewRequest(http.MethodPost, "/api/users.profile.set", strings.NewReader(`profile={"status_text":"Private response"}`))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"status_text":"Private response"`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	if strings.Contains(result.Body.String(), `"email"`) || strings.Contains(result.Body.String(), "alice@example.com") {
		t.Fatalf("email escaped users:read.email boundary: %s", result.Body)
	}
}

func TestUsersAndConversationsList(t *testing.T) {
	handler := testHandler()
	users := httptest.NewRequest(http.MethodGet, "/api/users.list?limit=1", nil)
	users.Header.Set("Authorization", "Bearer token")
	usersResult := httptest.NewRecorder()
	handler.ServeHTTP(usersResult, users)
	if usersResult.Code != http.StatusOK || !strings.Contains(usersResult.Body.String(), `"id":"U1"`) {
		t.Fatalf("users status=%d body=%s", usersResult.Code, usersResult.Body)
	}
	conversations := httptest.NewRequest(http.MethodPost, "/api/conversations.list", strings.NewReader("limit=1"))
	conversations.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	conversations.Header.Set("Authorization", "Bearer token")
	conversationsResult := httptest.NewRecorder()
	handler.ServeHTTP(conversationsResult, conversations)
	if conversationsResult.Code != http.StatusOK || !strings.Contains(conversationsResult.Body.String(), `"id":"C1"`) {
		t.Fatalf("conversations status=%d body=%s", conversationsResult.Code, conversationsResult.Body)
	}
	filtered := httptest.NewRequest(http.MethodGet, "/api/conversations.list?exclude_archived=true&types=public_channel", nil)
	filtered.Header.Set("Authorization", "Bearer token")
	filteredResult := httptest.NewRecorder()
	handler.ServeHTTP(filteredResult, filtered)
	if filteredResult.Code != http.StatusOK || !strings.Contains(filteredResult.Body.String(), `"id":"C1"`) || strings.Contains(filteredResult.Body.String(), `"id":"C2"`) {
		t.Fatalf("filtered conversations status=%d body=%s", filteredResult.Code, filteredResult.Body)
	}
	byUser := httptest.NewRequest(http.MethodGet, "/api/users.conversations?user=U2", nil)
	byUser.Header.Set("Authorization", "Bearer token")
	byUserResult := httptest.NewRecorder()
	handler.ServeHTTP(byUserResult, byUser)
	if byUserResult.Code != http.StatusOK || !strings.Contains(byUserResult.Body.String(), `"id":"C1"`) {
		t.Fatalf("users.conversations status=%d body=%s", byUserResult.Code, byUserResult.Body)
	}
}

func TestTeamProfileGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/team.profile.get", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"profile":{"fields":[]}`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestEmojiList(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/emoji.list?include_categories=true", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	body := res.Body.String()
	if res.Code != http.StatusOK || !strings.Contains(body, `"emoji":{}`) ||
		!strings.Contains(body, `"categories_version":"097705020bcf82331c9ef10df3425aad15f5043c"`) ||
		!strings.Contains(body, `"name":"Smileys \u0026 Emotion"`) ||
		!strings.Contains(body, `"emoji_names":["grinning"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	invalid := httptest.NewRequest(http.MethodGet, "/api/emoji.list?include_categories=sometimes", nil)
	invalid.Header.Set("Authorization", "Bearer token")
	invalidResult := httptest.NewRecorder()
	testHandler().ServeHTTP(invalidResult, invalid)
	if invalidResult.Code != http.StatusOK || !strings.Contains(invalidResult.Body.String(), `"error":"invalid_arguments"`) {
		t.Fatalf("invalid status=%d body=%s", invalidResult.Code, invalidResult.Body)
	}
}

func TestAdminEmojiLifecycle(t *testing.T) {
	handler := testHandler()
	call := func(endpoint, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/"+endpoint, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if res := call("admin.emoji.add", "name=wave&url=https%3A%2F%2Fcdn.example%2Fwave.png"); res.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", res.Code, res.Body)
	}
	if res := call("admin.emoji.addAlias", "name=hello&alias_for=wave"); res.Code != http.StatusOK {
		t.Fatalf("alias status=%d body=%s", res.Code, res.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/admin.emoji.list", nil)
	list.Header.Set("Authorization", "Bearer token")
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"hello":"alias:wave"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	if res := call("admin.emoji.rename", "name=hello&new_name=greeting"); res.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", res.Code, res.Body)
	}
	if res := call("admin.emoji.remove", "name=wave"); res.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", res.Code, res.Body)
	}
}

func TestAdminConversationSearchIsRegistered(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/admin.conversations.search?query=general&limit=10", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"general"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestAdminUserGroupChannelMembershipIsRegistered(t *testing.T) {
	handler := testHandler()
	create := httptest.NewRequest(http.MethodPost, "/api/usergroups.create", strings.NewReader("name=Engineering&handle=engineering"))
	create.Header.Set("Authorization", "Bearer token")
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	var body struct {
		UserGroup struct {
			ID string `json:"id"`
		} `json:"usergroup"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil || body.UserGroup.ID == "" {
		t.Fatalf("create status=%d body=%s err=%v", created.Code, created.Body, err)
	}
	add := httptest.NewRequest(http.MethodPost, "/api/admin.usergroups.addChannels", strings.NewReader("usergroup="+body.UserGroup.ID+"&channel_ids=C1"))
	add.Header.Set("Authorization", "Bearer token")
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	added := httptest.NewRecorder()
	handler.ServeHTTP(added, add)
	if added.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", added.Code, added.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/admin.usergroups.listChannels?usergroup="+body.UserGroup.ID, nil)
	list.Header.Set("Authorization", "Bearer token")
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"C1"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
}

func TestAdminTeamSettingsNameLifecycle(t *testing.T) {
	handler := testHandler()
	set := httptest.NewRequest(http.MethodPost, "/api/admin.teams.settings.setName", strings.NewReader("name=Renamed%20Team"))
	set.Header.Set("Authorization", "Bearer token")
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	changed := httptest.NewRecorder()
	handler.ServeHTTP(changed, set)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"name":"Renamed Team"`) {
		t.Fatalf("set status=%d body=%s", changed.Code, changed.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/admin.teams.settings.info", nil)
	info.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, info)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"name":"Renamed Team"`) {
		t.Fatalf("info status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminTeamSettingsDescriptionLifecycle(t *testing.T) {
	handler := testHandler()
	set := httptest.NewRequest(http.MethodPost, "/api/admin.teams.settings.setDescription", strings.NewReader("description=Workspace%20description"))
	set.Header.Set("Authorization", "Bearer token")
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	changed := httptest.NewRecorder()
	handler.ServeHTTP(changed, set)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"description":"Workspace description"`) {
		t.Fatalf("set status=%d body=%s", changed.Code, changed.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/admin.teams.settings.info", nil)
	info.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, info)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"description":"Workspace description"`) {
		t.Fatalf("info status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminTeamSettingsDiscoverabilityLifecycle(t *testing.T) {
	handler := testHandler()
	set := httptest.NewRequest(http.MethodPost, "/api/admin.teams.settings.setDiscoverability", strings.NewReader("discoverability=invite_only"))
	set.Header.Set("Authorization", "Bearer token")
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	changed := httptest.NewRecorder()
	handler.ServeHTTP(changed, set)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"discoverability":"invite_only"`) {
		t.Fatalf("set status=%d body=%s", changed.Code, changed.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/admin.teams.settings.info", nil)
	info.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, info)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"discoverability":"invite_only"`) {
		t.Fatalf("info status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminTeamSettingsIconLifecycle(t *testing.T) {
	handler := testHandler()
	set := httptest.NewRequest(http.MethodPost, "/api/admin.teams.settings.setIcon", strings.NewReader("image_url=https%3A%2F%2Fcdn.example%2Ficon.png"))
	set.Header.Set("Authorization", "Bearer token")
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	changed := httptest.NewRecorder()
	handler.ServeHTTP(changed, set)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"icon_url":"https://cdn.example/icon.png"`) {
		t.Fatalf("set status=%d body=%s", changed.Code, changed.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/admin.teams.settings.info", nil)
	info.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, info)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"icon_url":"https://cdn.example/icon.png"`) {
		t.Fatalf("info status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminTeamSettingsDefaultChannelsLifecycle(t *testing.T) {
	handler := testHandler()
	set := httptest.NewRequest(http.MethodPost, "/api/admin.teams.settings.setDefaultChannels", strings.NewReader("channel_ids=C1%2CC1"))
	set.Header.Set("Authorization", "Bearer token")
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	changed := httptest.NewRecorder()
	handler.ServeHTTP(changed, set)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"default_channels":["C1"]`) {
		t.Fatalf("set status=%d body=%s", changed.Code, changed.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/admin.teams.settings.info", nil)
	info.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, info)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"default_channels":["C1"]`) {
		t.Fatalf("info status=%d body=%s", result.Code, result.Body)
	}
}

func TestAdminTeamListIsRegistered(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/admin.teams.list", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"T1"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestUsersIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/users.identity", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"user":{"id":"U1","name":"alice"}`) || !strings.Contains(res.Body.String(), `"team":{"id":"T1"}`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestUsersDeletePhoto(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/users.deletePhoto", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestUsersSetPhotoAcceptsOfficialMultipartField(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeUsersProfileWrite: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store, Blob: blobs}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	for _, contentType := range []string{"image/png", "application/octet-stream"} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": {`form-data; name="image"; filename="photo.png"`},
			"Content-Type":        {contentType},
		})
		if err != nil {
			t.Fatal(err)
		}
		// A real PNG signature: the profile service sniffs the bytes and refuses a
		// stream whose content disagrees with the declared type, which is the upload
		// half of the stored-XSS repair. The octet-stream case is what Web API 8
		// emits for a Buffer.
		if _, err := part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/users.setPhoto", &body)
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", writer.FormDataContentType())
		result := httptest.NewRecorder()
		mux.ServeHTTP(result, req)
		if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) {
			t.Fatalf("content-type=%s status=%d body=%s", contentType, result.Code, result.Body)
		}
	}
}

func TestUsersSetActiveAndAdminTeamRoleLists(t *testing.T) {
	handler := testHandler()
	active := httptest.NewRequest(http.MethodPost, "/api/users.setActive", strings.NewReader(""))
	active.Header.Set("Authorization", "Bearer token")
	activeResult := httptest.NewRecorder()
	handler.ServeHTTP(activeResult, active)
	if activeResult.Code != http.StatusOK || !strings.Contains(activeResult.Body.String(), `"ok":true`) {
		t.Fatalf("set active status=%d body=%s", activeResult.Code, activeResult.Body)
	}
	for _, test := range []struct {
		endpoint string
		field    string
	}{
		{endpoint: "admin.teams.admins.list", field: "admin_ids"},
		{endpoint: "admin.teams.owners.list", field: "owner_ids"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/"+test.endpoint+"?team_id=T1&limit=10", nil)
		req.Header.Set("Authorization", "Bearer token")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"`+test.field+`"`) {
			t.Fatalf("%s status=%d body=%s", test.endpoint, result.Code, result.Body)
		}
	}
}

func TestDoNotDisturbLifecycle(t *testing.T) {
	handler := testHandler()
	info := httptest.NewRequest(http.MethodGet, "/api/dnd.info", nil)
	info.Header.Set("Authorization", "Bearer token")
	infoResult := httptest.NewRecorder()
	handler.ServeHTTP(infoResult, info)
	if infoResult.Code != http.StatusOK || !strings.Contains(infoResult.Body.String(), `"dnd_enabled":false`) || !strings.Contains(infoResult.Body.String(), `"snooze_enabled":false`) {
		t.Fatalf("initial dnd status=%d body=%s", infoResult.Code, infoResult.Body)
	}
	teamInfo := httptest.NewRequest(http.MethodGet, "/api/dnd.teamInfo?users=U1,U2", nil)
	teamInfo.Header.Set("Authorization", "Bearer token")
	teamInfoResult := httptest.NewRecorder()
	handler.ServeHTTP(teamInfoResult, teamInfo)
	if teamInfoResult.Code != http.StatusOK || !strings.Contains(teamInfoResult.Body.String(), `"users"`) {
		t.Fatalf("team info status=%d body=%s", teamInfoResult.Code, teamInfoResult.Body)
	}
	set := httptest.NewRequest(http.MethodPost, "/api/dnd.setSnooze", strings.NewReader("num_minutes=5"))
	set.Header.Set("Authorization", "Bearer token")
	set.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setResult := httptest.NewRecorder()
	handler.ServeHTTP(setResult, set)
	if setResult.Code != http.StatusOK || !strings.Contains(setResult.Body.String(), `"snooze_enabled":true`) {
		t.Fatalf("set dnd status=%d body=%s", setResult.Code, setResult.Body)
	}
	end := httptest.NewRequest(http.MethodPost, "/api/dnd.endSnooze", nil)
	end.Header.Set("Authorization", "Bearer token")
	endResult := httptest.NewRecorder()
	handler.ServeHTTP(endResult, end)
	if endResult.Code != http.StatusOK || !strings.Contains(endResult.Body.String(), `"snooze_enabled":false`) {
		t.Fatalf("end dnd status=%d body=%s", endResult.Code, endResult.Body)
	}
}

func TestStarsMessageLifecycle(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	created := time.Unix(200, 0).UTC()
	if err := s.CreateMessage(ctx, domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "star me", CreatedAt: created}, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeStarsRead: {}, auth.ScopeStarsWrite: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: s}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	add := httptest.NewRequest(http.MethodPost, "/api/stars.add", strings.NewReader("channel=C1&timestamp=200.000000"))
	add.Header.Set("Authorization", "Bearer token")
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, add)
	if result.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", result.Code, result.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/stars.list", nil)
	list.Header.Set("Authorization", "Bearer token")
	result = httptest.NewRecorder()
	mux.ServeHTTP(result, list)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"type":"message"`) || !strings.Contains(result.Body.String(), `"ts":"200.000000"`) {
		t.Fatalf("list status=%d body=%s", result.Code, result.Body)
	}
	remove := httptest.NewRequest(http.MethodPost, "/api/stars.remove", strings.NewReader("channel=C1&timestamp=200.000000"))
	remove.Header.Set("Authorization", "Bearer token")
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result = httptest.NewRecorder()
	mux.ServeHTTP(result, remove)
	if result.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", result.Code, result.Body)
	}
}

func TestRemindersLifecycle(t *testing.T) {
	handler := testHandler()
	due := time.Now().UTC().Add(time.Hour).Unix()
	add := httptest.NewRequest(http.MethodPost, "/api/reminders.add", strings.NewReader("text=check-in&time="+strconv.FormatInt(due, 10)))
	add.Header.Set("Authorization", "Bearer token")
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, add)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"reminder"`) || !strings.Contains(result.Body.String(), `"text":"check-in"`) {
		t.Fatalf("add status=%d body=%s", result.Code, result.Body)
	}
	var added struct {
		Reminder struct {
			ID string `json:"id"`
		} `json:"reminder"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &added); err != nil || added.Reminder.ID == "" {
		t.Fatalf("add body=%s err=%v", result.Body, err)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/reminders.list", nil)
	list.Header.Set("Authorization", "Bearer token")
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, list)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"text":"check-in"`) {
		t.Fatalf("list status=%d body=%s", result.Code, result.Body)
	}
	info := httptest.NewRequest(http.MethodGet, "/api/reminders.info?reminder="+added.Reminder.ID, nil)
	info.Header.Set("Authorization", "Bearer token")
	infoResult := httptest.NewRecorder()
	handler.ServeHTTP(infoResult, info)
	if infoResult.Code != http.StatusOK || !strings.Contains(infoResult.Body.String(), added.Reminder.ID) {
		t.Fatalf("info status=%d body=%s", infoResult.Code, infoResult.Body)
	}
	complete := httptest.NewRequest(http.MethodPost, "/api/reminders.complete", strings.NewReader("reminder="+added.Reminder.ID))
	complete.Header.Set("Authorization", "Bearer token")
	complete.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	completeResult := httptest.NewRecorder()
	handler.ServeHTTP(completeResult, complete)
	if completeResult.Code != http.StatusOK || !strings.Contains(completeResult.Body.String(), `"ok":true`) {
		t.Fatalf("complete status=%d body=%s", completeResult.Code, completeResult.Body)
	}
	remove := httptest.NewRequest(http.MethodPost, "/api/reminders.delete", strings.NewReader("reminder="+added.Reminder.ID))
	remove.Header.Set("Authorization", "Bearer token")
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	removeResult := httptest.NewRecorder()
	handler.ServeHTTP(removeResult, remove)
	if removeResult.Code != http.StatusOK || !strings.Contains(removeResult.Body.String(), `"ok":true`) {
		t.Fatalf("delete status=%d body=%s", removeResult.Code, removeResult.Body)
	}
}

func TestScheduledMessageLifecycle(t *testing.T) {
	handler := testHandler()
	postAt := time.Now().UTC().Add(time.Hour).Unix()
	add := httptest.NewRequest(http.MethodPost, "/api/chat.scheduleMessage", strings.NewReader("channel=C1&text=later&post_at="+strconv.FormatInt(postAt, 10)))
	add.Header.Set("Authorization", "Bearer token")
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, add)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"scheduled_message_id":"Q`) {
		t.Fatalf("add status=%d body=%s", result.Code, result.Body)
	}
	var added struct {
		ID string `json:"scheduled_message_id"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &added); err != nil || added.ID == "" {
		t.Fatalf("decode add body=%s err=%v", result.Body, err)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/chat.scheduledMessages.list?channel=C1", nil)
	list.Header.Set("Authorization", "Bearer token")
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, list)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), added.ID) {
		t.Fatalf("list status=%d body=%s", result.Code, result.Body)
	}
	remove := httptest.NewRequest(http.MethodPost, "/api/chat.deleteScheduledMessage", strings.NewReader("channel=C1&scheduled_message_id="+added.ID))
	remove.Header.Set("Authorization", "Bearer token")
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, remove)
	if result.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", result.Code, result.Body)
	}
}

func TestConversationMembers(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/conversations.members?channel=C1&limit=1", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"members":["U1"]`) || !strings.Contains(res.Body.String(), `"has_more":true`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestConversationsOpenReusesDirectConversation(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/conversations.open", strings.NewReader("users=U2"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"is_im":true`) {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body)
	}
	secondRequest := httptest.NewRequest(http.MethodPost, "/api/conversations.open", strings.NewReader("users=U2"))
	secondRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secondRequest.Header.Set("Authorization", "Bearer token")
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body)
	}
	var firstBody, secondBody struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if firstBody.Channel.ID == "" || firstBody.Channel.ID != secondBody.Channel.ID {
		t.Fatalf("direct conversation was not reused: first=%q second=%q", firstBody.Channel.ID, secondBody.Channel.ID)
	}
	closeRequest := httptest.NewRequest(http.MethodPost, "/api/conversations.close", strings.NewReader("channel="+firstBody.Channel.ID))
	closeRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	closeRequest.Header.Set("Authorization", "Bearer token")
	closed := httptest.NewRecorder()
	handler.ServeHTTP(closed, closeRequest)
	if closed.Code != http.StatusOK || closed.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("close status=%d body=%s", closed.Code, closed.Body)
	}
	alreadyClosedRequest := httptest.NewRequest(http.MethodPost, "/api/conversations.close", strings.NewReader("channel="+firstBody.Channel.ID))
	alreadyClosedRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	alreadyClosedRequest.Header.Set("Authorization", "Bearer token")
	alreadyClosed := httptest.NewRecorder()
	handler.ServeHTTP(alreadyClosed, alreadyClosedRequest)
	if alreadyClosed.Code != http.StatusOK || !strings.Contains(alreadyClosed.Body.String(), `"no_op":true`) || !strings.Contains(alreadyClosed.Body.String(), `"already_closed":true`) {
		t.Fatalf("second close status=%d body=%s", alreadyClosed.Code, alreadyClosed.Body)
	}
	reopenRequest := httptest.NewRequest(http.MethodPost, "/api/conversations.open", strings.NewReader("users=U2"))
	reopenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reopenRequest.Header.Set("Authorization", "Bearer token")
	reopened := httptest.NewRecorder()
	handler.ServeHTTP(reopened, reopenRequest)
	var reopenedBody struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(reopened.Body.Bytes(), &reopenedBody); err != nil {
		t.Fatal(err)
	}
	if reopened.Code != http.StatusOK || reopenedBody.Channel.ID != firstBody.Channel.ID {
		t.Fatalf("reopen status=%d id=%q body=%s", reopened.Code, reopenedBody.Channel.ID, reopened.Body)
	}
	directLeave := httptest.NewRequest(http.MethodPost, "/api/conversations.leave", strings.NewReader("channel="+firstBody.Channel.ID))
	directLeave.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	directLeave.Header.Set("Authorization", "Bearer token")
	directLeaveResult := httptest.NewRecorder()
	handler.ServeHTTP(directLeaveResult, directLeave)
	if directLeaveResult.Code != http.StatusOK || !strings.Contains(directLeaveResult.Body.String(), `"error":"method_not_supported_for_channel_type"`) {
		t.Fatalf("direct leave status=%d body=%s", directLeaveResult.Code, directLeaveResult.Body)
	}

	publicClose := httptest.NewRequest(http.MethodPost, "/api/conversations.close", strings.NewReader("channel=C1"))
	publicClose.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	publicClose.Header.Set("Authorization", "Bearer token")
	publicResult := httptest.NewRecorder()
	handler.ServeHTTP(publicResult, publicClose)
	if publicResult.Code != http.StatusOK || !strings.Contains(publicResult.Body.String(), `"error":"method_not_supported_for_channel_type"`) {
		t.Fatalf("public close status=%d body=%s", publicResult.Code, publicResult.Body)
	}
}

func TestCreatePrivateConversation(t *testing.T) {
	handler := testHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/conversations.create", bytes.NewBufferString(`{"name":"Private Room","is_private":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"is_private":true`) || !strings.Contains(res.Body.String(), `"name":"private-room"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	duplicate := httptest.NewRequest(http.MethodPost, "/api/conversations.create", strings.NewReader("name=Private+Room&is_private=false"))
	duplicate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	duplicate.Header.Set("Authorization", "Bearer token")
	duplicateResult := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResult, duplicate)
	if duplicateResult.Code != http.StatusOK || !strings.Contains(duplicateResult.Body.String(), `"error":"name_taken"`) {
		t.Fatalf("duplicate status=%d body=%s", duplicateResult.Code, duplicateResult.Body)
	}
	rename := httptest.NewRequest(http.MethodPost, "/api/conversations.rename", strings.NewReader("channel=C1&name=Private+Room"))
	rename.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rename.Header.Set("Authorization", "Bearer token")
	renameResult := httptest.NewRecorder()
	handler.ServeHTTP(renameResult, rename)
	if renameResult.Code != http.StatusOK || !strings.Contains(renameResult.Body.String(), `"error":"name_taken"`) {
		t.Fatalf("rename collision status=%d body=%s", renameResult.Code, renameResult.Body)
	}
}

func TestJoinPublicConversation(t *testing.T) {
	handler := testHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/conversations.join", strings.NewReader("channel=C1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"C1"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestJoinPublicConversationUsesCurrentTokenTypeScopes(t *testing.T) {
	_, repository := testHandlerWithStore()
	call := func(tokenType domain.TokenType, botID domain.BotID, scopes ...auth.Scope) string {
		granted := make(map[auth.Scope]struct{}, len(scopes))
		for _, scope := range scopes {
			granted[scope] = struct{}{}
		}
		authenticator, err := auth.NewStatic("join-token", auth.Principal{
			WorkspaceID: "T1", UserID: "U1", AppID: "A1", BotID: botID,
			TokenType: tokenType, Scopes: granted,
		})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewHandler(service.Messages{Store: repository}, authenticator)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		handler.Register(mux)
		request := httptest.NewRequest(http.MethodPost, "/api/conversations.join", strings.NewReader("channel=C1"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer join-token")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response.Body.String()
	}

	if body := call("bot", "B1", auth.ScopeChannelsJoin); !strings.Contains(body, `"ok":true`) {
		t.Fatalf("bot channels:join response=%s", body)
	}
	if body := call("bot", "B1", auth.ScopeChannelsManage); !strings.Contains(body, `"error":"missing_scope"`) || !strings.Contains(body, `"needed":"channels:join"`) {
		t.Fatalf("bot channels:manage response=%s", body)
	}
	if body := call("user", "", auth.ScopeChannelsWrite); !strings.Contains(body, `"ok":true`) {
		t.Fatalf("user channels:write response=%s", body)
	}
}

func TestLeavePublicConversation(t *testing.T) {
	handler := testHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/conversations.leave", strings.NewReader("channel=C1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"channel":"C1"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

// Public-channel history is visible to a workspace member before joining, but
// chat.postMessage is not. This exercises the complete Slack-facing recovery
// path instead of only checking join and post independently.
func TestJoinRecoversPostOutsidePublicConversation(t *testing.T) {
	handler := testHandler()
	call := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	left := call("/api/conversations.leave", "channel=C1")
	if left.Code != http.StatusOK || !strings.Contains(left.Body.String(), `"ok":true`) {
		t.Fatalf("leave status=%d body=%s", left.Code, left.Body)
	}
	history := call("/api/conversations.history", "channel=C1")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"ok":true`) {
		t.Fatalf("public history status=%d body=%s", history.Code, history.Body)
	}
	refused := call("/api/chat.postMessage", "channel=C1&text=before+joining")
	if refused.Code != http.StatusOK || !strings.Contains(refused.Body.String(), `"error":"not_in_channel"`) {
		t.Fatalf("post before join status=%d body=%s", refused.Code, refused.Body)
	}
	joined := call("/api/conversations.join", "channel=C1")
	if joined.Code != http.StatusOK || !strings.Contains(joined.Body.String(), `"ok":true`) {
		t.Fatalf("join status=%d body=%s", joined.Code, joined.Body)
	}
	posted := call("/api/chat.postMessage", "channel=C1&text=after+joining")
	if posted.Code != http.StatusOK || !strings.Contains(posted.Body.String(), `"ok":true`) || !strings.Contains(posted.Body.String(), `"after joining"`) {
		t.Fatalf("post after join status=%d body=%s", posted.Code, posted.Body)
	}
}

func TestMarkConversation(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=hello"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	var message struct {
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(posted.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	mark := httptest.NewRequest(http.MethodPost, "/api/conversations.mark", strings.NewReader("channel=C1&ts="+message.TS))
	mark.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mark.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, mark)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) || !strings.Contains(result.Body.String(), message.TS) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestAddGetAndRemoveReaction(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=hello"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	var message struct {
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(posted.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	add := httptest.NewRequest(http.MethodPost, "/api/reactions.add", strings.NewReader("channel=C1&timestamp="+message.TS+"&name=thumbsup"))
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	add.Header.Set("Authorization", "Bearer token")
	added := httptest.NewRecorder()
	handler.ServeHTTP(added, add)
	if added.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", added.Code, added.Body)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/reactions.get?channel=C1&timestamp="+message.TS, nil)
	get.Header.Set("Authorization", "Bearer token")
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, get)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "thumbsup") || !strings.Contains(got.Body.String(), "U1") {
		t.Fatalf("get status=%d body=%s", got.Code, got.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/reactions.list?limit=10", nil)
	list.Header.Set("Authorization", "Bearer token")
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"type":"message"`) || !strings.Contains(listed.Body.String(), "thumbsup") {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	remove := httptest.NewRequest(http.MethodPost, "/api/reactions.remove", strings.NewReader("channel=C1&timestamp="+message.TS+"&name=thumbsup"))
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	remove.Header.Set("Authorization", "Bearer token")
	removed := httptest.NewRecorder()
	handler.ServeHTTP(removed, remove)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", removed.Code, removed.Body)
	}
}

func TestAddListAndRemovePin(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=hello"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	var message struct {
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(posted.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	add := httptest.NewRequest(http.MethodPost, "/api/pins.add", strings.NewReader("channel=C1&timestamp="+message.TS))
	add.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	add.Header.Set("Authorization", "Bearer token")
	added := httptest.NewRecorder()
	handler.ServeHTTP(added, add)
	if added.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", added.Code, added.Body)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/pins.list?channel=C1", nil)
	list.Header.Set("Authorization", "Bearer token")
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "U1") {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	remove := httptest.NewRequest(http.MethodPost, "/api/pins.remove", strings.NewReader("channel=C1&timestamp="+message.TS))
	remove.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	remove.Header.Set("Authorization", "Bearer token")
	removed := httptest.NewRecorder()
	handler.ServeHTTP(removed, remove)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", removed.Code, removed.Body)
	}
}

func TestHistoryReturnsMessages(t *testing.T) {
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=hello"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	postResult := httptest.NewRecorder()
	handler := testHandler()
	handler.ServeHTTP(postResult, post)

	get := httptest.NewRequest(http.MethodGet, "/api/conversations.history?channel=C1", nil)
	get.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, get)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "hello") {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestConversationRepliesReturnsRootAndThread(t *testing.T) {
	handler := testHandler()
	post := func(text, thread string) string {
		body := "channel=C1&text=" + text
		if thread != "" {
			body += "&thread_ts=" + thread
		}
		req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("post status=%d body=%s", res.Code, res.Body)
		}
		var value struct {
			TS string `json:"ts"`
		}
		if err := json.NewDecoder(res.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value.TS
	}
	root := post("root", "")
	post("reply", root)
	req := httptest.NewRequest(http.MethodGet, "/api/conversations.replies?channel=C1&ts="+root, nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "root") || !strings.Contains(res.Body.String(), "reply") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestHistoryCursorAdvancesBoundedPage(t *testing.T) {
	handler := testHandler()
	for _, text := range []string{"one", "two"} {
		req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text="+text))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("post status = %d", res.Code)
		}
	}
	first := httptest.NewRequest(http.MethodGet, "/api/conversations.history?channel=C1&limit=1", nil)
	first.Header.Set("Authorization", "Bearer token")
	firstResult := httptest.NewRecorder()
	handler.ServeHTTP(firstResult, first)
	var firstBody struct {
		HasMore  bool `json:"has_more"`
		Metadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := json.NewDecoder(firstResult.Body).Decode(&firstBody); err != nil {
		t.Fatal(err)
	}
	if !firstBody.HasMore || firstBody.Metadata.NextCursor == "" {
		t.Fatalf("first page = %+v", firstBody)
	}
	second := httptest.NewRequest(http.MethodGet, "/api/conversations.history?channel=C1&limit=1&cursor="+firstBody.Metadata.NextCursor, nil)
	second.Header.Set("Authorization", "Bearer token")
	secondResult := httptest.NewRecorder()
	handler.ServeHTTP(secondResult, second)
	if secondResult.Code != http.StatusOK || !strings.Contains(secondResult.Body.String(), "one") {
		t.Fatalf("second status=%d body=%s", secondResult.Code, secondResult.Body)
	}
}

func TestScheduleMessageFormAcceptsBlocksWithoutFallbackText(t *testing.T) {
	form := url.Values{
		"channel": {"C1"},
		"post_at": {strconv.FormatInt(time.Now().UTC().Add(time.Hour).Unix(), 10)},
		"blocks":  {`[{"type":"divider"}]`},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/chat.scheduleMessage", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"blocks":[{"type":"divider"}]`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestScheduleMessagePreservesCurrentSlackMessageOptionsUntilDelivery(t *testing.T) {
	handler, backing := testHandlerWithStore()
	post := func(path string, form url.Values) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, res.Code, res.Body)
		}
		var response map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	root := post("/api/chat.postMessage", url.Values{"channel": {"C1"}, "text": {"root"}})
	rootTS, _ := root["ts"].(string)
	if rootTS == "" {
		t.Fatalf("root response=%v", root)
	}
	metadata := `{"event_type":"task_created","event_payload":{"id":"11223"}}`
	scheduled := post("/api/chat.scheduleMessage", url.Values{
		"channel":         {"C1"},
		"markdown_text":   {"**future**"},
		"metadata":        {metadata},
		"thread_ts":       {rootTS},
		"reply_broadcast": {"true"},
		"link_names":      {"true"},
		"parse":           {"none"},
		"unfurl_links":    {"false"},
		"unfurl_media":    {"true"},
		"post_at":         {strconv.FormatInt(time.Now().UTC().Add(time.Hour).Unix(), 10)},
	})
	id, _ := scheduled["scheduled_message_id"].(string)
	if scheduled["ok"] != true || id == "" {
		t.Fatalf("schedule response=%v", scheduled)
	}
	item, err := backing.ClaimScheduledMessageForCredential(context.Background(), "T1", domain.HashToken("token"), domain.ScheduledMessageID(id), "delivery", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request, err := service.ScheduledMessagePostRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	if request.Text != "**future**" || !request.MarkdownText || !request.ReplyBroadcast || !request.LinkNames ||
		request.Parse != "none" || request.Metadata == "" || request.UnfurlLinks == nil || *request.UnfurlLinks ||
		request.UnfurlMedia == nil || !*request.UnfurlMedia {
		t.Fatalf("scheduled delivery request lost message options: %+v", request)
	}
}

func TestScheduleMessageRejectsMarkdownConflictsAndInvalidBooleans(t *testing.T) {
	for name, form := range map[string]url.Values{
		"markdown conflict": {
			"channel": {"C1"}, "text": {"text"}, "markdown_text": {"**markdown**"},
			"post_at": {strconv.FormatInt(time.Now().UTC().Add(time.Hour).Unix(), 10)},
		},
		"invalid boolean": {
			"channel": {"C1"}, "text": {"text"}, "reply_broadcast": {"sometimes"},
			"post_at": {strconv.FormatInt(time.Now().UTC().Add(time.Hour).Unix(), 10)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/chat.scheduleMessage", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Authorization", "Bearer token")
			res := httptest.NewRecorder()
			testHandler().ServeHTTP(res, req)
			if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"ok":false`) {
				t.Fatalf("status=%d body=%s", res.Code, res.Body)
			}
		})
	}
}

func TestScheduledMessageAPIIsScopedToTheExactBearerToken(t *testing.T) {
	handler, store := testHandlerWithStoredTokenAuth(auth.ScopeChatWrite)
	store.SeedToken(context.Background(), "other-token", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "U1", AppID: "A1", BotID: "B1",
		TokenType: "bot", Scopes: []string{string(auth.ScopeChatWrite)},
	})
	call := func(token, path, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	postAt := time.Now().UTC().Add(time.Hour).Unix()
	scheduled := call("token", "/api/chat.scheduleMessage", url.Values{
		"channel": {"C1"}, "text": {"token-owned"}, "post_at": {strconv.FormatInt(postAt, 10)},
	}.Encode())
	id, _ := scheduled["scheduled_message_id"].(string)
	if scheduled["ok"] != true || id == "" {
		t.Fatalf("schedule response=%v", scheduled)
	}
	message, _ := scheduled["message"].(map[string]any)
	if scheduled["post_at"] != strconv.FormatInt(postAt, 10) || message["bot_id"] != "B1" || message["type"] != "delayed_message" || message["subtype"] != "bot_message" {
		t.Fatalf("schedule response lost Slack post_at or bot attribution: %v", scheduled)
	}
	otherPage := call("other-token", "/api/chat.scheduledMessages.list", "")
	if items, _ := otherPage["scheduled_messages"].([]any); otherPage["ok"] != true || len(items) != 0 {
		t.Fatalf("another token saw scheduled messages: %v", otherPage)
	}
	otherDelete := call("other-token", "/api/chat.deleteScheduledMessage", url.Values{
		"channel": {"C1"}, "scheduled_message_id": {id},
	}.Encode())
	if otherDelete["error"] != "invalid_scheduled_message_id" {
		t.Fatalf("another token deleted the schedule: %v", otherDelete)
	}
	ownerPage := call("token", "/api/chat.scheduledMessages.list", url.Values{
		"oldest": {strconv.FormatInt(postAt-1, 10)}, "latest": {strconv.FormatInt(postAt+1, 10)},
	}.Encode())
	if items, _ := ownerPage["scheduled_messages"].([]any); ownerPage["ok"] != true || len(items) != 1 {
		t.Fatalf("creating token could not list its schedule: %v", ownerPage)
	}
}

func TestPostEphemeralAcceptsBlocksWithoutFallbackText(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.postEphemeral", strings.NewReader("channel=C1&user=U2&blocks=%5B%7B%22type%22%3A%22divider%22%7D%5D"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"blocks":[{"type":"divider"}]`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestPostMessageAcceptsAttachmentsWithoutFallbackText(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&attachments=%5B%7B%22text%22%3A%22attachment%22%7D%5D"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"attachments":[{"text":"attachment"}]`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
}

func TestPostMessageJSONAcceptsStructuredArrays(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader(`{
		"channel":"C1",
		"blocks":[{"type":"section","text":{"type":"plain_text","text":"from blocks"}}],
		"attachments":[{"text":"from attachments"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	for _, want := range []string{`"blocks":[{"type":"section"`, `"attachments":[{"text":"from attachments"}]`} {
		if !strings.Contains(res.Body.String(), want) {
			t.Fatalf("response does not contain %q: %s", want, res.Body)
		}
	}
}

func TestDecodeJSONFieldsPreservesStructuredArrayArguments(t *testing.T) {
	fields, err := decodeJSONFields(strings.NewReader(`{
		"blocks":[{"type":"divider"}],
		"attachments":[{"text":"attachment"}],
		"files":[{"id":"F1","title":"report"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{
		"blocks":      `[{"type":"divider"}]`,
		"attachments": `[{"text":"attachment"}]`,
		"files":       `[{"id":"F1","title":"report"}]`,
	}
	for name, want := range wants {
		if fields[name] != want {
			t.Fatalf("%s=%q, want %q", name, fields[name], want)
		}
	}
}

func TestUpdateMessageAcceptsAttachmentsWithoutFallbackText(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=before"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	var created struct {
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(posted.Body).Decode(&created); err != nil || created.TS == "" {
		t.Fatalf("post body=%s err=%v", posted.Body, err)
	}
	update := httptest.NewRequest(http.MethodPost, "/api/chat.update", strings.NewReader("channel=C1&ts="+created.TS+"&attachments=%5B%7B%22text%22%3A%22updated%22%7D%5D"))
	update.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	update.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, update)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"attachments":[{"text":"updated"}]`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
}

func TestUpdateMessageDistinguishesOmittedFieldsFromEmptyArrays(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader(url.Values{
		"channel":     {"C1"},
		"text":        {"fallback"},
		"blocks":      {`[{"type":"section","text":{"type":"plain_text","text":"block"}}]`},
		"attachments": {`[{"text":"attachment"}]`},
	}.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	var created struct {
		TS string `json:"ts"`
	}
	if err := json.NewDecoder(posted.Body).Decode(&created); err != nil || created.TS == "" {
		t.Fatalf("post body=%s err=%v", posted.Body, err)
	}
	call := func(values url.Values) map[string]any {
		t.Helper()
		values.Set("channel", "C1")
		values.Set("ts", created.TS)
		request := httptest.NewRequest(http.MethodPost, "/api/chat.update", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var payload map[string]any
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil || payload["ok"] != true {
			t.Fatalf("status=%d body=%s err=%v", response.Code, response.Body, err)
		}
		return payload["message"].(map[string]any)
	}
	attachmentsOnly := call(url.Values{"attachments": {`[{"text":"changed"}]`}})
	if _, ok := attachmentsOnly["blocks"]; !ok {
		t.Fatalf("attachments-only update erased blocks: %#v", attachmentsOnly)
	}
	if attachmentsOnly["text"] != "fallback" {
		t.Fatalf("attachments-only update erased text: %#v", attachmentsOnly)
	}
	noBlocks := call(url.Values{"blocks": {"[]"}})
	if blocks, ok := noBlocks["blocks"].([]any); !ok || len(blocks) != 0 {
		t.Fatalf("explicit empty blocks did not produce an empty array: %#v", noBlocks)
	}
	if _, ok := noBlocks["attachments"]; !ok {
		t.Fatalf("empty blocks erased omitted attachments: %#v", noBlocks)
	}
	noAttachments := call(url.Values{"attachments": {"[]"}})
	if _, ok := noAttachments["attachments"]; ok {
		t.Fatalf("explicit empty attachments were retained: %#v", noAttachments)
	}
}

func TestExternalUploadHTTPBatchCompletion(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	store.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "archive", Archived: true})
	store.SeedConversationMember("C2", "U1")
	objects, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeFilesRead: {}, auth.ScopeFilesWrite: {}, auth.ScopeChannelsHistory: {}, auth.ScopeChatWrite: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store, Blob: objects}, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	create := func(name string, content string, sdkMultipart bool) string {
		request := httptest.NewRequest(http.MethodPost, "/api/files.getUploadURLExternal", strings.NewReader(url.Values{"filename": {name}, "length": {strconv.Itoa(len(content))}}.Encode()))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("create status=%d body=%s", response.Code, response.Body)
		}
		var body struct {
			UploadURL string `json:"upload_url"`
			FileID    string `json:"file_id"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var upload *http.Request
		if sdkMultipart {
			var encoded bytes.Buffer
			writer := multipart.NewWriter(&encoded)
			part, err := writer.CreateFormFile("body", "file")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(part, content); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			upload = httptest.NewRequest(http.MethodPost, body.UploadURL, &encoded)
			upload.Header.Set("Content-Type", writer.FormDataContentType())
		} else {
			upload = httptest.NewRequest(http.MethodPost, body.UploadURL, strings.NewReader(content))
			upload.Header.Set("Content-Length", strconv.Itoa(len(content)))
		}
		uploadResponse := httptest.NewRecorder()
		mux.ServeHTTP(uploadResponse, upload)
		if uploadResponse.Code != http.StatusOK {
			t.Fatalf("upload status=%d body=%s", uploadResponse.Code, uploadResponse.Body)
		}
		return body.FileID
	}
	first := create("first.txt", "first", true)
	second := create("second.txt", "second", false)
	filesJSON, err := json.Marshal([]map[string]string{{"id": first, "title": "First"}, {"id": second, "title": "Second"}})
	if err != nil {
		t.Fatal(err)
	}
	completeValues := url.Values{"files": {string(filesJSON)}, "channels": {"C1"}, "blocks": {`[{"type":"divider"}]`}}
	complete := httptest.NewRequest(http.MethodPost, "/api/files.completeUploadExternal", strings.NewReader(completeValues.Encode()))
	complete.Header.Set("Authorization", "Bearer token")
	complete.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	completed := httptest.NewRecorder()
	mux.ServeHTTP(completed, complete)
	if completed.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completed.Code, completed.Body)
	}
	var result struct {
		OK    bool             `json:"ok"`
		Files []map[string]any `json:"files"`
	}
	if err := json.NewDecoder(completed.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Files) != 2 || result.Files[0]["title"] != "First" || result.Files[1]["title"] != "Second" {
		t.Fatalf("result=%+v", result)
	}

	archivedFile := create("archived.txt", "archived", false)
	archivedFilesJSON, err := json.Marshal([]map[string]string{{"id": archivedFile, "title": "Archived"}})
	if err != nil {
		t.Fatal(err)
	}
	archived := httptest.NewRequest(http.MethodPost, "/api/files.completeUploadExternal", strings.NewReader(url.Values{"files": {string(archivedFilesJSON)}, "channel_id": {"C2"}}.Encode()))
	archived.Header.Set("Authorization", "Bearer token")
	archived.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	refused := httptest.NewRecorder()
	mux.ServeHTTP(refused, archived)
	if refused.Code != http.StatusOK || !strings.Contains(refused.Body.String(), `"error":"posting_to_channel_denied"`) {
		t.Fatalf("archived completion status=%d body=%s", refused.Code, refused.Body)
	}
}

// Slack's message object reports an edit through `edited` and a
// workspace-generated message through `subtype`. Neither appeared in any
// method that returns a message, so no client could render "(edited)" or tell
// a /me narration from an ordinary post.
func TestMessageResponseCarriesEditedAndSubtype(t *testing.T) {
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.meMessage", strings.NewReader("channel=C1&text=waves"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	var narration struct {
		OK bool   `json:"ok"`
		TS string `json:"ts"`
	}
	if err := json.Unmarshal(posted.Body.Bytes(), &narration); err != nil || !narration.OK {
		t.Fatalf("meMessage=%s", posted.Body)
	}
	update := httptest.NewRequest(http.MethodPost, "/api/chat.update", strings.NewReader("channel=C1&ts="+narration.TS+"&text=waves+again"))
	update.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	update.Header.Set("Authorization", "Bearer token")
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"ok":true`) {
		t.Fatalf("update=%s", updated.Body)
	}
	history := httptest.NewRequest(http.MethodGet, "/api/conversations.history?channel=C1", nil)
	history.Header.Set("Authorization", "Bearer token")
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, history)
	var page struct {
		Messages []struct {
			TS      string `json:"ts"`
			Subtype string `json:"subtype"`
			Edited  *struct {
				User string `json:"user"`
				TS   string `json:"ts"`
			} `json:"edited"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range page.Messages {
		if message.TS != narration.TS {
			continue
		}
		found = true
		if message.Subtype != "me_message" {
			t.Fatalf("subtype=%q, want me_message", message.Subtype)
		}
		if message.Edited == nil || message.Edited.User == "" || message.Edited.TS == "" {
			t.Fatalf("edited=%+v, want the editor and the instant", message.Edited)
		}
	}
	if !found {
		t.Fatalf("history did not contain the narrated message: %s", read.Body)
	}
}
