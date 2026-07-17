package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testHandler() http.Handler {
	handler, _ := testHandlerWithStore()
	return handler
}

func testHandlerWithStore() (http.Handler, *memory.Store) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:"}})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	if err := s.CreateBot(context.Background(), domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "U2", Name: "testbot", UpdatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := s.CreateUserMigration(context.Background(), domain.UserMigration{WorkspaceID: "T1", OldID: "U1", GlobalID: "W1"}, events.Event{ID: "EM1", WorkspaceID: "T1", Topic: "user.migration_created", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	if err := s.SetAppApproval(context.Background(), "T1", "A1", "R1", domain.AppApprovalApproved, time.Now().UTC(), events.Event{ID: "EAPP1", WorkspaceID: "T1", ActorID: "U1", Topic: "app.approved", Payload: "A1", CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "archived", Archived: true})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	s.SeedConversationMember("C2", "U1")
	s.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes()})
	if err := s.CreateOAuthClient(context.Background(), domain.OAuthClient{ID: "oauth-client", SecretHash: domain.HashToken("oauth-secret"), AppID: "A1"}); err != nil {
		panic(err)
	}
	if err := s.CreateOAuthCode(context.Background(), domain.OAuthCode{Code: "oauth-code", ClientID: "oauth-client", WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}, RedirectURI: "https://callback"}); err != nil {
		panic(err)
	}
	if err := s.CreateFile(context.Background(), domain.File{ID: "F1", WorkspaceID: "T1", Uploader: "U1", Name: "file.txt", BlobKey: "blob", CreatedAt: time.Now().UTC()}, events.Event{ID: "EF1", WorkspaceID: "T1", Topic: "file.created", Payload: "F1", CreatedAt: time.Now().UTC()}); err != nil {
		panic(err)
	}
	s.SeedFileComment(domain.FileComment{ID: "FC1", File: "F1", WorkspaceID: "T1", UserID: "U1", Text: "comment", CreatedAt: time.Now().UTC()})
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChatWrite: {}, auth.ScopeChannelsHistory: {}, auth.ScopeRTMStream: {}, auth.ScopeUsersRead: {}, auth.ScopeUsersReadEmail: {}, auth.ScopeUsersWrite: {}, auth.ScopeUsersProfileWrite: {}, auth.ScopeChannelsRead: {}, auth.ScopeChannelsManage: {}, auth.ScopeReactionsWrite: {}, auth.ScopeReactionsRead: {}, auth.ScopePinsWrite: {}, auth.ScopePinsRead: {}, auth.ScopeSearchRead: {}, auth.ScopeFilesRead: {}, auth.ScopeFilesWrite: {}, auth.ScopeRemoteFilesRead: {}, auth.ScopeRemoteFilesWrite: {}, auth.ScopeRemoteFilesShare: {}, auth.ScopeTeamRead: {}, auth.ScopeEmojiRead: {}, auth.ScopeIdentityBasic: {}, auth.ScopeDNDRead: {}, auth.ScopeDNDWrite: {}, auth.ScopeRemindersRead: {}, auth.ScopeRemindersWrite: {}, auth.ScopeUserGroupsRead: {}, auth.ScopeUserGroupsWrite: {}, auth.ScopeCallsRead: {}, auth.ScopeCallsWrite: {}, auth.ScopeWorkflowStepsExecute: {}, auth.ScopeTokensBasic: {}, auth.ScopeAdmin: {}, auth.ScopeAdminUsersRead: {}, auth.ScopeAdminUsersWrite: {}, auth.ScopeAdminInvitesRead: {}, auth.ScopeAdminInvitesWrite: {}, auth.ScopeAdminConversationsRead: {}, auth.ScopeAdminConversationsWrite: {}, auth.ScopeAdminEmojiWrite: {}, auth.ScopeAdminUserGroupsRead: {}, auth.ScopeAdminUserGroupsWrite: {}, auth.ScopeAdminTeamsRead: {}, auth.ScopeAdminTeamsWrite: {}, auth.ScopeAdminAppsRead: {}, auth.ScopeAdminAppsWrite: {}}})
	if err != nil {
		panic(err)
	}
	h, err := NewHandler(service.Messages{Store: s}, authenticator)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, s
}

func TestListInputsNormalizeAndRejectMalformedJSON(t *testing.T) {
	values, err := parseNormalizedStringList(` ["T1", " T2 ", "T1", ""] `)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{"T1", "T2"}; !equalStrings(values, got) {
		t.Fatalf("values=%v, want %v", values, got)
	}
	if values == nil {
		t.Fatal("normalized values are nil")
	}
	if _, err := parseNormalizedStringList(`{"id":"T1"}`); err == nil {
		t.Fatal("JSON object was accepted as a string list")
	}
	if _, err := normalizeListFieldValue("team_ids", `{"id":"T1"}`); err == nil {
		t.Fatal("form JSON object was accepted as a list field")
	}
	channels, err := parseConversationIDs(" C1, C2, C1, ")
	if err != nil {
		t.Fatal(err)
	}
	if got := []domain.ConversationID{"C1", "C2"}; !equalConversationIDs(channels, got) {
		t.Fatalf("channels=%v, want %v", channels, got)
	}
	if channels == nil {
		t.Fatal("normalized channels are nil")
	}
	if _, err := parseConversationIDs(`["C1"`); err == nil {
		t.Fatal("malformed channel JSON was accepted")
	}
	users, err := parseCallUsers(`[{"slack_id":"U1"},{"external_id":"U2"},{"slack_id":"U1"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if got := []domain.UserID{"U1", "U2"}; !equalUserIDs(users, got) {
		t.Fatalf("users=%v, want %v", users, got)
	}
	if users == nil {
		t.Fatal("normalized users are nil")
	}
	if _, err := parseCallUsers(`[{"slack_id":"U1"}`); err == nil {
		t.Fatal("malformed user JSON was accepted")
	}
	if _, err := parseCallUsers(`{"slack_id":"U1"}`); err == nil {
		t.Fatal("JSON object was accepted as a user identifier")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalConversationIDs(left, right []domain.ConversationID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalUserIDs(left, right []domain.UserID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
		AuthedUser  struct {
			ID          string `json:"id"`
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
		} `json:"authed_user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.AccessToken != "" || body.AppID != "A1" || body.AuthedUser.ID != "U1" || body.AuthedUser.AccessToken == "" || body.AuthedUser.TokenType != "user" {
		t.Fatalf("unexpected body: %s", response.Body)
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
	request := httptest.NewRequest(http.MethodGet, "/api/rtm.connect?token=token", nil)
	request.Host = "chat.example.test"
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
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
		t.Fatalf("unexpected body: %s", response.Body)
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
	handler := testHandler()
	form := func(path string, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer token")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	opened := form("/api/views.open", url.Values{"trigger_id": {"trigger-1"}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"First"}}`}})
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
	pushed := form("/api/views.push", url.Values{"trigger_id": {"trigger-2"}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"Second"}}`}})
	if pushed.Code != http.StatusOK || !strings.Contains(pushed.Body.String(), openedBody.View.ID) {
		t.Fatalf("push status=%d body=%s", pushed.Code, pushed.Body)
	}
	updated := form("/api/views.update", url.Values{"view_id": {openedBody.View.ID}, "hash": {openedBody.View.Hash}, "view": {`{"type":"modal","title":{"type":"plain_text","text":"Updated"}}`}})
	if updated.Code != http.StatusOK || strings.Contains(updated.Body.String(), openedBody.View.Hash) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	published := form("/api/views.publish", url.Values{"user_id": {"U2"}, "view": {`{"type":"home","blocks":[]}`}})
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"type":"home"`) {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body)
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
	handler := testHandler()
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
	if invalidResponse.Code != http.StatusBadRequest || !strings.Contains(invalidResponse.Body.String(), `"error":"invalid_arguments"`) {
		t.Fatalf("invalid status=%d body=%s", invalidResponse.Code, invalidResponse.Body)
	}
}

func TestFunctionsCompleteErrorHTTPValidatesAndCompletes(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/functions.completeError", strings.NewReader(url.Values{
		"function_execution_id": {"execution-error-http"},
		"error":                 {"function failed"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	testHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestDialogOpenHTTP(t *testing.T) {
	handler := testHandler()
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

func TestMapServiceErrorKeepsHandledFailuresNon500AndDistinct(t *testing.T) {
	if code, reason := mapServiceError(service.ErrMessageAlreadyDeleted, "message_not_found"); code != http.StatusBadRequest || reason != "message_not_found" {
		t.Fatalf("deleted message mapping = %d %q", code, reason)
	}
	if code, reason := mapServiceError(status.Error(codes.FailedPrecondition, "deleted"), "message_not_found"); code != http.StatusBadRequest || reason != "message_not_found" {
		t.Fatalf("remote deleted message mapping = %d %q", code, reason)
	}
	if code, reason := mapServiceError(service.ErrBlobUnavailable, "service_unavailable"); code != http.StatusServiceUnavailable || reason != "file_storage_unavailable" {
		t.Fatalf("blob mapping = %d %q", code, reason)
	}
	if code, reason := mapServiceError(status.Error(codes.Unavailable, "blob unavailable"), "service_unavailable"); code == http.StatusInternalServerError || reason == "" {
		t.Fatalf("remote unavailable mapping = %d %q", code, reason)
	}
	if code, reason := mapServiceError(service.ErrInvalidAppApproval, "app_not_found"); code != http.StatusBadRequest || reason != "invalid_arguments" {
		t.Fatalf("app approval mapping = %d %q", code, reason)
	}
	if code, reason := mapServiceError(errors.New("unexpected dependency failure"), "service_unavailable"); code != http.StatusServiceUnavailable || reason != "service_unavailable" {
		t.Fatalf("unknown handled failure mapping = %d %q, want 503 service_unavailable", code, reason)
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

func TestCallsAddAllowsOptionalUsers(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/calls.add", strings.NewReader("external_unique_id=external-without-users&join_url=https%3A%2F%2Fcall.example%2Fwithout-users"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		OK   bool `json:"ok"`
		Call struct {
			Users []string `json:"users"`
		} `json:"call"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK {
		t.Fatalf("body=%s", response.Body.String())
	}
	if body.Call.Users == nil {
		t.Fatalf("users must be a nonnil normalized list: %s", response.Body.String())
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
	if denied.Code != http.StatusBadRequest {
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
	if second.Code != http.StatusNotFound {
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
	authorizations := httptest.NewRequest(http.MethodGet, "/api/apps.event.authorizations.list?event_context=ctx-1", nil)
	authorizations.Header.Set("Authorization", "Bearer token")
	authorizationsResult := httptest.NewRecorder()
	handler.ServeHTTP(authorizationsResult, authorizations)
	if authorizationsResult.Code != http.StatusOK || !strings.Contains(authorizationsResult.Body.String(), `"team_id":"T1"`) {
		t.Fatalf("authorizations status=%d body=%s", authorizationsResult.Code, authorizationsResult.Body)
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

func TestAppsUninstallRevokesAuthenticatedToken(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/apps.uninstall", strings.NewReader(""))
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || result.Body.String() != `{"ok":true}`+"\n" {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
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
	if foreignResult.Code != http.StatusBadRequest {
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
	if foreignResult.Code != http.StatusBadRequest {
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
			ID string `json:"id"`
		} `json:"usergroup"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil || createdBody.UserGroup.ID == "" {
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
	handler := testHandler()
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
	if after.Code != http.StatusNotFound {
		t.Fatalf("removed user status=%d body=%s", after.Code, after.Body)
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

func TestAdminUsersSessionResetIsRegistered(t *testing.T) {
	handler := testHandler()
	request := httptest.NewRequest(http.MethodPost, "/api/admin.users.session.reset", strings.NewReader("team_id=T1&user_id=U2"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"error":"user_not_found"`) {
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
		{endpoint: "admin.conversations.unarchive", body: "channel_id=C1"},
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
	if after.Code != http.StatusNotFound {
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
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "sameoldchat.local/archives/C1/p") {
		t.Fatalf("permalink status=%d body=%s", result.Code, result.Body)
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
	handler := testHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/conversations.invite", strings.NewReader("channel=C1&users=U2,U2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"C1"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
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

func TestTeamInfo(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/team.info", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"T1"`) || !strings.Contains(res.Body.String(), `"name":"test"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
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
	if checkResult.Code != http.StatusUnauthorized {
		t.Fatalf("revoked auth status=%d body=%s", checkResult.Code, checkResult.Body)
	}
}

func TestJSONDuplicateFieldsAreRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", bytes.NewBufferString(`{"channel":"C1","channel":"C2","text":"duplicate"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "invalid_form_data") {
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
	handler := testHandler()
	post := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text=searchable hello"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Authorization", "Bearer token")
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, post)
	search := httptest.NewRequest(http.MethodGet, "/api/search.messages?query=hello", nil)
	search.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, search)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "searchable hello") {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
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
	request := httptest.NewRequest(http.MethodPost, "/api/users.profile.set", strings.NewReader(`profile={"display_name":"new-name","status_text":"Ready","status_emoji":":white_check_mark:","image_24":"","image_32":"","image_48":"","image_72":"","image_192":"","image_512":"","image_1024":""}`))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"display_name":"new-name"`) {
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
	req := httptest.NewRequest(http.MethodGet, "/api/emoji.list", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"emoji":{}`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
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
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="image"; filename="photo.png"`},
		"Content-Type":        {"image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("photo")); err != nil {
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
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
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

	publicClose := httptest.NewRequest(http.MethodPost, "/api/conversations.close", strings.NewReader("channel=C1"))
	publicClose.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	publicClose.Header.Set("Authorization", "Bearer token")
	publicResult := httptest.NewRecorder()
	handler.ServeHTTP(publicResult, publicClose)
	if publicResult.Code != http.StatusBadRequest || !strings.Contains(publicResult.Body.String(), `"error":"method_not_supported_for_channel_type"`) {
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
	if secondResult.Code != http.StatusOK || !strings.Contains(secondResult.Body.String(), "two") {
		t.Fatalf("second status=%d body=%s", secondResult.Code, secondResult.Body)
	}
}
