package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestPostMessageRejectsForeignUser(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T2"})
	_, err := (Messages{Store: s}).Post(context.Background(), "T1", "U1", "C1", "hello", "", "")
	if err == nil {
		t.Fatal("Post returned nil error for foreign user")
	}
}

func TestOAuthExchangeConsumesAuthorizationCode(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	ctx := context.Background()
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthCode(ctx, domain.OAuthCode{Code: "code", ClientID: "client", WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}, RedirectURI: "https://callback"}); err != nil {
		t.Fatal(err)
	}
	token, err := (Messages{Store: s}).OAuthExchange(ctx, " client ", " secret ", " code ", "https://callback")
	if err != nil {
		t.Fatal(err)
	}
	if token.AppID != "A1" || token.WorkspaceID != "T1" || token.UserID != "U1" || token.TokenType != "user" || len(token.Scopes) != 1 {
		t.Fatalf("unexpected token: %+v", token)
	}
	issued, err := s.LookupToken(ctx, token.AccessToken)
	if err != nil || len(issued.Scopes) != 1 || issued.Scopes[0] != "chat:write" {
		t.Fatalf("issued token not usable: %v", err)
	}
	if _, err := (Messages{Store: s}).OAuthExchange(ctx, "client", "secret", "code", "https://callback"); !errors.Is(err, ErrInvalidOAuth) {
		t.Fatalf("second exchange error = %v, want %v", err, ErrInvalidOAuth)
	}
}

func TestOpenIDConnectTokenRotatesRefreshTokenAndUserInfoUsesIssuedScope(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test", Domain: "test.example"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com"})
	ctx := context.Background()
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthCode(ctx, domain.OAuthCode{Code: "code", ClientID: "client", WorkspaceID: "T1", UserID: "U1", Scopes: append(auth.AllScopes(), "openid"), RedirectURI: "https://callback", CodeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", CodeChallengeMethod: "S256"}); err != nil {
		t.Fatal(err)
	}
	service := Messages{Store: s}
	token, err := service.OpenIDConnectToken(ctx, "client", "secret", "code", "https://callback", "authorization_code", "", "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || len(strings.Split(token.IDToken, ".")) != 3 || token.TokenType != "Bearer" {
		t.Fatalf("token=%+v", token)
	}
	info, err := service.OpenIDConnectUserInfo(ctx, token.AccessToken)
	if err != nil || info.Subject != "U1" || info.WorkspaceID != "T1" || !info.EmailVerified {
		t.Fatalf("userinfo=%+v err=%v", info, err)
	}
	rotated, err := service.OpenIDConnectToken(ctx, "client", "secret", "", "", "refresh_token", token.RefreshToken, "")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.AccessToken == token.AccessToken || rotated.RefreshToken == token.RefreshToken || rotated.IDToken == "" {
		t.Fatalf("refresh did not rotate credentials: old=%+v new=%+v", token, rotated)
	}
	if _, err := service.OpenIDConnectToken(ctx, "client", "secret", "", "", "refresh_token", token.RefreshToken, ""); !errors.Is(err, ErrInvalidOAuth) {
		t.Fatalf("reused refresh token error=%v, want %v", err, ErrInvalidOAuth)
	}
}

func TestIntegrationLogsRequireAuthoritativeActorEvents(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	ctx := context.Background()
	if err := s.SetAppApproval(ctx, "T1", "A1", "R1", domain.AppApprovalApproved, time.Now().UTC(), events.Event{ID: "EAPP1", WorkspaceID: "T1", ActorID: "U1", Topic: "app.approved", Payload: "A1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	value, err := (Messages{Store: s}).IntegrationLogs(ctx, "T1", "U1", "A1", "added", "", "", 10, 1)
	if err != nil || len(value.Logs) != 1 || value.Logs[0].UserName != "alice" || value.Logs[0].ChangeType != "added" {
		t.Fatalf("logs=%+v err=%v", value, err)
	}
}

func TestRTMConnectionIsSingleUseAndExpires(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	ctx := context.Background()
	connection, err := (Messages{Store: s}).CreateRTMConnection(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Messages{Store: s}).ConsumeRTMConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := (Messages{Store: s}).ConsumeRTMConnection(ctx, connection.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second consume error=%v", err)
	}
	expired := domain.RTMConnection{ID: "rtm-expired", WorkspaceID: "T1", UserID: "U1", ExpiresAt: time.Now().UTC().Add(-time.Second)}
	if err := s.CreateRTMConnection(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeRTMConnection(ctx, expired.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired consume error=%v", err)
	}
}

func TestAuthMethodEnablementIsDurable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	ctx := context.Background()
	service := Messages{Store: s}
	method, err := service.GetAuthMethod(ctx, "T1", "Google")
	if err != nil || !method.Enabled {
		t.Fatalf("default auth method=%+v err=%v", method, err)
	}
	if err := service.SetAuthMethod(ctx, domain.AuthMethod{WorkspaceID: "T1", Provider: "GitHub", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	method, err = service.GetAuthMethod(ctx, "T1", "github")
	if err != nil || method.Enabled {
		t.Fatalf("disabled auth method=%+v err=%v", method, err)
	}
}

func TestAdminCreateUserNormalizesAndPersistsMembership(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"})
	ctx := context.Background()
	user, err := (Messages{Store: s}).AdminCreateUser(ctx, "T1", "U1", " Alice@Example.COM ", "Alice Example", domain.WorkspaceRoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == "" || user.Email != "alice@example.com" || user.RealName != "Alice Example" || user.Presence != domain.PresenceAuto {
		t.Fatalf("created user=%+v", user)
	}
	loaded, err := s.GetUser(ctx, user.ID)
	if err != nil || loaded.Email != user.Email {
		t.Fatalf("loaded user=%+v err=%v", loaded, err)
	}
	membership, err := s.GetWorkspaceMembership(ctx, "T1", user.ID)
	if err != nil || membership.Role != domain.WorkspaceRoleAdmin || !membership.Active {
		t.Fatalf("membership=%+v err=%v", membership, err)
	}
	if _, err := (Messages{Store: s}).AdminCreateUser(ctx, "T1", "U1", "alice@example.com", "Duplicate", domain.WorkspaceRoleMember); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate error=%v", err)
	}
	page, err := (Messages{Store: s}).AdminListUsers(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	foundAdmin := false
	for _, item := range page.Users {
		if item.User.Email == "alice@example.com" && item.Membership.Role == domain.WorkspaceRoleAdmin && item.Membership.Active {
			foundAdmin = true
			break
		}
	}
	if err != nil || len(page.Users) != 2 || !foundAdmin {
		t.Fatalf("administrator users=%+v err=%v", page, err)
	}
}

func TestListsLifecycleNormalizesCellsAndStreamsCopies(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}

	source, err := messages.CreateList(ctx, "T1", "U1", "Source", " [{\"type\":\"rich_text\"}] ", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		if _, err := messages.CreateListItem(ctx, "T1", "U1", source.ID, "", fmt.Sprintf(`[{"column_id":"title","value":"row-%03d"}]`, i)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := messages.ListItems(ctx, "T1", "U1", source.ID, domain.PageRequest{Limit: 100}, false)
	if err != nil || len(page.Items) != 100 || !page.HasMore {
		t.Fatalf("source page=%+v err=%v", page, err)
	}

	copy, err := messages.CreateList(ctx, "T1", "U1", "Copy", "", "", source.ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	copy, err = messages.UpdateList(ctx, "T1", "U1", copy.ID, "Renamed", "", false, false)
	if err != nil || copy.Name != "Renamed" || !copy.TodoMode {
		t.Fatalf("updated list=%+v err=%v", copy, err)
	}
	page, err = messages.ListItems(ctx, "T1", "U1", copy.ID, domain.PageRequest{Limit: 200}, false)
	if err != nil || len(page.Items) != 101 || page.HasMore {
		t.Fatalf("copy page=%+v err=%v", page, err)
	}

	item := page.Items[0]
	updated, err := messages.UpdateListCells(ctx, "T1", "U1", copy.ID, fmt.Sprintf(`[{"row_id":%q,"column_id":"title","value":"updated"},{"row_id":%q,"column_id":"status","value":"open"}]`, item.ID, item.ID))
	if err != nil || len(updated) != 1 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if !strings.Contains(updated[0].Fields, `"value":"updated"`) || !strings.Contains(updated[0].Fields, `"column_id":"status"`) {
		t.Fatalf("cells were not merged: %s", updated[0].Fields)
	}
	archivedItem, err := messages.UpdateListItem(ctx, "T1", "U1", copy.ID, item.ID, "", true)
	if err != nil || !archivedItem.Archived {
		t.Fatalf("archived item=%+v err=%v", archivedItem, err)
	}
	visible, err := messages.ListItems(ctx, "T1", "U1", copy.ID, domain.PageRequest{Limit: 200}, false)
	if err != nil || len(visible.Items) != 100 {
		t.Fatalf("visible items=%d err=%v", len(visible.Items), err)
	}
	allItems, err := messages.ListItems(ctx, "T1", "U1", copy.ID, domain.PageRequest{Limit: 200}, true)
	if err != nil || len(allItems.Items) != 101 {
		t.Fatalf("all items=%d err=%v", len(allItems.Items), err)
	}
	if _, err := messages.GetListItem(ctx, "T1", "U1", copy.ID, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetListAccess(ctx, "T1", "U1", copy.ID, "read", []domain.ConversationID{"C1"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetListAccess(ctx, "T1", "U1", copy.ID, "owner", nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteListAccess(ctx, "T1", "U1", copy.ID, []domain.ConversationID{"C1"}, nil); err != nil {
		t.Fatal(err)
	}
	download, err := messages.StartListDownload(ctx, "T1", "U1", copy.ID, true)
	if err != nil || download.Status != "COMPLETED" || download.URL == "" || !download.IncludeArchived {
		t.Fatalf("download=%+v err=%v", download, err)
	}
	if _, err := messages.GetListDownload(ctx, "T1", "U1", download.ID); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteListItems(ctx, "T1", "U1", copy.ID, []domain.ListItemID{item.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestExternalIdentityLinkIsUniqueAndDurable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
	ctx := context.Background()
	service := Messages{Store: s}
	identity := domain.ExternalIdentity{WorkspaceID: "T1", Provider: "google", Subject: "sub-1", UserID: "U1"}
	if err := service.CreateExternalIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if err := service.CreateExternalIdentity(ctx, identity); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate identity error=%v", err)
	}
	value, err := service.GetExternalIdentity(ctx, "T1", "GOOGLE", "sub-1")
	if err != nil || value.UserID != "U1" {
		t.Fatalf("identity=%+v err=%v", value, err)
	}
}

func TestViewsAreTypedDurableAndHashChecked(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	ctx := context.Background()
	opened, err := messages.OpenView(ctx, "T1", "U1", "trigger-1", `{"type":"modal","title":{"type":"plain_text","text":"First"}}`)
	if err != nil || opened.RootViewID != opened.ID || opened.Hash == "" {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
	pushed, err := messages.PushView(ctx, "T1", "U1", "trigger-2", `{"type":"modal","title":{"type":"plain_text","text":"Second"}}`)
	if err != nil || pushed.RootViewID != opened.RootViewID || pushed.PreviousViewID != opened.ID {
		t.Fatalf("pushed=%+v err=%v", pushed, err)
	}
	updated, err := messages.UpdateView(ctx, "T1", "U1", string(opened.ID), "", `{"type":"modal","title":{"type":"plain_text","text":"Updated"}}`, opened.Hash)
	if err != nil || updated.Hash == opened.Hash {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := messages.UpdateView(ctx, "T1", "U1", string(opened.ID), "", `{"type":"modal"}`, opened.Hash); err == nil {
		t.Fatal("stale view hash unexpectedly succeeded")
	}
}

func TestWorkflowStepLifecycleNormalizesJSONAndPersists(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	ctx := context.Background()
	if err := messages.WorkflowUpdateStep(ctx, "T1", "U1", "edit-1", "", `[{"name":"output"}]`, "Step", "https://example/image.png"); err != nil {
		t.Fatal(err)
	}
	configured, err := s.GetWorkflowStep(ctx, "T1", "edit-1")
	if err != nil || configured.Status != domain.WorkflowStepConfigured || configured.Inputs != "{}" || configured.Outputs == "" {
		t.Fatalf("configured=%+v err=%v", configured, err)
	}
	if err := messages.WorkflowStepCompleted(ctx, "T1", "U1", "execute-1", `{"result":"ok"}`); err != nil {
		t.Fatal(err)
	}
	completed, err := s.GetWorkflowStep(ctx, "T1", "execute-1")
	if err != nil || completed.Status != domain.WorkflowStepCompleted || completed.Outputs != `{"result":"ok"}` {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if err := messages.WorkflowStepFailed(ctx, "T1", "U1", "execute-2", `{"message":"failed"}`); err != nil {
		t.Fatal(err)
	}
	if err := messages.WorkflowStepFailed(ctx, "T1", "U1", "execute-3", `{"detail":"missing message"}`); err != ErrInvalidWorkflowStep {
		t.Fatalf("invalid failure err=%v", err)
	}
}

func TestDialogOpenValidatesAndPersistsPayload(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	if err := messages.OpenDialog(context.Background(), "T1", "U1", "trigger-1", `{"callback_id":"callback","title":"Title","elements":[{"type":"text"}]}`); err != nil {
		t.Fatal(err)
	}
	if err := messages.OpenDialog(context.Background(), "T1", "U1", "trigger-2", `{"callback_id":"callback","title":"Title"}`); err != ErrInvalidDialog {
		t.Fatalf("invalid dialog err=%v", err)
	}
}

func TestBotInfoUsesDurableBotRegistry(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	updated := time.Now().UTC()
	if err := s.CreateBot(context.Background(), domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "U1", Name: "bot", UpdatedAt: updated}); err != nil {
		t.Fatal(err)
	}
	value, err := (Messages{Store: s}).BotInfo(context.Background(), "T1", "U1", "B1")
	if err != nil || value.ID != "B1" || value.AppID != "A1" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	if _, err := (Messages{Store: s}).BotInfo(context.Background(), "T1", "U1", "B2"); err != store.ErrNotFound {
		t.Fatalf("missing bot err=%v", err)
	}
}

func TestMigrationExchangeUsesExplicitMappingsAndReportsInvalidIDs(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	now := time.Now().UTC()
	if err := s.CreateUserMigration(context.Background(), domain.UserMigration{WorkspaceID: "T1", OldID: "U1", GlobalID: "W1"}, events.Event{ID: "EM1", WorkspaceID: "T1", Topic: "migration.created", Payload: "U1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	value, err := (Messages{Store: s}).MigrationExchange(context.Background(), "T1", "U1", []domain.UserID{"U1", "missing", "U1"}, false)
	if err != nil || value.UserIDMap["U1"] != "W1" || len(value.InvalidUserIDs) != 1 || value.InvalidUserIDs[0] != "missing" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	value, err = (Messages{Store: s}).MigrationExchange(context.Background(), "T1", "U1", []domain.UserID{"W1"}, true)
	if err != nil || value.UserIDMap["W1"] != "U1" {
		t.Fatalf("to old value=%+v err=%v", value, err)
	}
}

func TestConversationTeamsAreDurableAndDisconnectable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "one"})
	s.SeedWorkspace(domain.Workspace{ID: "T2", Name: "two"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	messages := Messages{Store: s}
	if err := messages.AdminSetConversationTeams(context.Background(), "T1", "U1", "C1", []domain.WorkspaceID{"T1", "T2"}, false); err != nil {
		t.Fatal(err)
	}
	teams, _, err := s.ListConversationTeams(context.Background(), "T1", "C1")
	if err != nil || len(teams) != 2 {
		t.Fatalf("teams=%v err=%v", teams, err)
	}
	if err := messages.AdminDisconnectSharedConversation(context.Background(), "T1", "U1", "C1", []domain.WorkspaceID{"T2"}); err != nil {
		t.Fatal(err)
	}
	teams, _, err = s.ListConversationTeams(context.Background(), "T1", "C1")
	if err != nil || len(teams) != 1 || teams[0] != "T1" {
		t.Fatalf("after disconnect teams=%v err=%v", teams, err)
	}
}

func TestResetUserSessionsRevokesEveryTargetSession(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	ctx := context.Background()
	if err := s.SeedSession(ctx, "target-one", domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(ctx, "target-two", domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(ctx, "other", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := (Messages{Store: s}).ResetUserSessions(ctx, "T1", "U1", "U2"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"target-one", "target-two"} {
		record, err := s.LookupSession(ctx, token)
		if err != nil || !record.Revoked {
			t.Fatalf("target session %q = %+v, err=%v", token, record, err)
		}
	}
	other, err := s.LookupSession(ctx, "other")
	if err != nil || other.Revoked {
		t.Fatalf("other session = %+v, err=%v", other, err)
	}
}

func TestAdminConversationMutationsDoNotRequireConversationMembership(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "old"})
	messages := Messages{Store: s}
	value, err := messages.AdminRenameConversation(context.Background(), "T1", "U1", "C1", "new")
	if err != nil || value.Name != "new" {
		t.Fatalf("rename=%+v err=%v", value, err)
	}
	value, err = messages.AdminSetConversationArchived(context.Background(), "T1", "U1", "C1", true)
	if err != nil || !value.Archived {
		t.Fatalf("archive=%+v err=%v", value, err)
	}
}

func TestAdminConversationInviteDoesNotRequireActorMembership(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "channel"})
	if _, err := (Messages{Store: s}).AdminInviteConversationMembers(context.Background(), "T1", "U1", "C1", []domain.UserID{"U2", "U2"}); err != nil {
		t.Fatal(err)
	}
	member, err := s.IsConversationMember(context.Background(), "C1", "U2")
	if err != nil || !member {
		t.Fatalf("member=%v err=%v", member, err)
	}
}

func TestAdminConversationConversionEnforcesConversationType(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "public"})
	messages := Messages{Store: s}
	value, err := messages.AdminConvertConversationToPrivate(context.Background(), "T1", "U1", "C1")
	if err != nil || !value.IsPrivate {
		t.Fatalf("conversion=%+v err=%v", value, err)
	}
	if _, err := messages.AdminConvertConversationToPrivate(context.Background(), "T1", "U1", "C1"); err != ErrInvalidConversation {
		t.Fatalf("second conversion err=%v", err)
	}
}

func TestAdminConversationPrefsAreTypedNormalizedAndDurable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	value, err := messages.AdminSetConversationPrefs(context.Background(), "T1", "U1", "C1", domain.ConversationPrefs{
		CanThread:  domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{" everyone ", "everyone"}, Users: []domain.UserID{"U2"}},
		WhoCanPost: domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{"admin"}, Users: []domain.UserID{"U2", "U2"}},
	})
	if err != nil || len(value.CanThread.Types) != 1 || value.CanThread.Types[0] != "everyone" || len(value.WhoCanPost.Users) != 1 {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	loaded, err := messages.AdminGetConversationPrefs(context.Background(), "T1", "U1", "C1")
	if err != nil || loaded.CanThread.Users[0] != "U2" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestRemoteFileLifecycleIsDurableAndBounded(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	value, err := messages.AddRemoteFile(context.Background(), "T1", "U1", domain.RemoteFile{ExternalID: "external-1", Title: " Remote   document ", FileType: "pdf", ExternalURL: "https://files.example/doc.pdf"})
	if err != nil || value.Title != "Remote document" || value.ID == "" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	page, err := messages.RemoteFiles(context.Background(), "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Files) != 1 || page.Files[0].ExternalID != "external-1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	info, err := messages.RemoteFileInfo(context.Background(), "T1", "U1", domain.RemoteFileLookup{ExternalID: "external-1"})
	if err != nil || info.ID != value.ID {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	shared, err := messages.ShareRemoteFile(context.Background(), "T1", "U1", domain.RemoteFileLookup{ID: value.ID}, []domain.ConversationID{"C1", "C1"})
	if err != nil || len(shared.SharedChannels) != 1 || shared.SharedChannels[0] != "C1" {
		t.Fatalf("shared=%+v err=%v", shared, err)
	}
	updated, err := messages.UpdateRemoteFile(context.Background(), "T1", "U1", domain.RemoteFileUpdate{Lookup: domain.RemoteFileLookup{ID: value.ID}, SetTitle: true, Title: " Updated   title "})
	if err != nil || updated.Title != "Updated title" || len(updated.SharedChannels) != 1 || updated.SharedChannels[0] != "C1" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := messages.UpdateRemoteFile(context.Background(), "T1", "U1", domain.RemoteFileUpdate{Lookup: domain.RemoteFileLookup{ID: value.ID}}); !errors.Is(err, ErrInvalidRemoteFile) {
		t.Fatalf("empty update error=%v", err)
	}
	if err := messages.RemoveRemoteFile(context.Background(), "T1", "U1", domain.RemoteFileLookup{ID: value.ID}); err != nil {
		t.Fatal(err)
	}
	page, err = messages.RemoteFiles(context.Background(), "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Files) != 0 {
		t.Fatalf("after remove page=%+v err=%v", page, err)
	}
}

func TestConversationAccessGroupsNormalizeAndPersist(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", IsPrivate: true})
	messages := Messages{Store: s}
	group, err := messages.CreateUserGroup(context.Background(), "T1", "U1", "Engineering", "engineering", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminAddConversationAccessGroup(context.Background(), "T1", "U1", "C1", group.ID); err != nil {
		t.Fatal(err)
	}
	groups, err := messages.AdminListConversationAccessGroups(context.Background(), "T1", "U1", "C1")
	if err != nil || len(groups) != 1 || groups[0] != group.ID {
		t.Fatalf("groups=%v err=%v", groups, err)
	}
	if err := messages.AdminRemoveConversationAccessGroup(context.Background(), "T1", "U1", "C1", group.ID); err != nil {
		t.Fatal(err)
	}
	groups, err = messages.AdminListConversationAccessGroups(context.Background(), "T1", "U1", "C1")
	if err != nil || len(groups) != 0 {
		t.Fatalf("after remove groups=%v err=%v", groups, err)
	}
}

func TestInviteRequestApprovalIsDurableAndBounded(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	now := time.Now().UTC()
	if err := s.CreateInviteRequest(context.Background(), domain.InviteRequest{ID: "IR1", WorkspaceID: "T1", Email: "one@example.com", RequestedBy: "U1", Status: domain.InviteRequestPending, CreatedAt: now}, events.Event{ID: "EIR1", WorkspaceID: "T1", Topic: "invite_request.created", Payload: "IR1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}
	page, err := messages.AdminListInviteRequests(context.Background(), "T1", "U1", domain.InviteRequestPending, domain.PageRequest{Limit: 1})
	if err != nil || len(page.Requests) != 1 || page.Requests[0].ID != "IR1" {
		t.Fatalf("pending page=%+v err=%v", page, err)
	}
	if err := messages.AdminApproveInviteRequest(context.Background(), "T1", "U1", "IR1"); err != nil {
		t.Fatal(err)
	}
	page, err = messages.AdminListInviteRequests(context.Background(), "T1", "U1", domain.InviteRequestApproved, domain.PageRequest{Limit: 1})
	if err != nil || len(page.Requests) != 1 || page.Requests[0].Status != domain.InviteRequestApproved || page.Requests[0].ReviewedAt.IsZero() {
		t.Fatalf("approved page=%+v err=%v", page, err)
	}
}

func TestAdminInviteUserNormalizesAndPersistsAllInviteState(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	expiration := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	if err := (Messages{Store: s}).AdminInviteUser(context.Background(), "T1", "U1", " Alice@Example.COM ", []domain.ConversationID{"C1", "C1"}, "Welcome", "Alice Example", true, true, false, expiration); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListInviteRequests(context.Background(), "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 1})
	if err != nil || len(page.Requests) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	value := page.Requests[0]
	if value.Email != "alice@example.com" || len(value.ChannelIDs) != 1 || value.ChannelIDs[0] != "C1" || value.CustomMessage != "Welcome" || value.RealName != "Alice Example" || !value.Resend || !value.Restricted || value.UltraRestricted || !value.GuestExpirationAt.Equal(expiration) {
		t.Fatalf("invite=%+v", value)
	}
}

func TestAdminAssignUserReactivatesAtomicallyWithChannels(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := s.SetUserDeleted(context.Background(), "T1", "U2", true, events.Event{ID: "EDEL", WorkspaceID: "T1", Topic: "user.removed", Payload: "U2", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := (Messages{Store: s}).AdminAssignUser(context.Background(), "T1", "U1", "U2", []domain.ConversationID{"C1", "C1"}); err != nil {
		t.Fatal(err)
	}
	user, err := s.GetUser(context.Background(), "U2")
	if err != nil || user.Deleted {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	member, err := s.IsConversationMember(context.Background(), "C1", "U2")
	if err != nil || !member {
		t.Fatalf("member=%v err=%v", member, err)
	}
}

func TestUnfurlPersistsNormalizedMetadata(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}
	message, err := messages.Post(context.Background(), "T1", "U1", "C1", "https://example.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := messages.Unfurl(context.Background(), "T1", "U1", "C1", domain.NewMessageTimestamp(message.CreatedAt), map[string]string{"https://example.com": " {\"title\": \"Example\"} "})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Unfurls["https://example.com"] != `{"title":"Example"}` {
		t.Fatalf("unfurls=%v", updated.Unfurls)
	}
	loaded, err := s.GetMessage(context.Background(), message.ID)
	if err != nil || loaded.Unfurls["https://example.com"] != `{"title":"Example"}` {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestDeleteFileCommentIsDurableAndWorkspaceScoped(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedFileComment(domain.FileComment{ID: "FC1", File: "F1", WorkspaceID: "T1", UserID: "U1", Text: "comment", CreatedAt: time.Now().UTC()})
	if err := s.CreateFile(context.Background(), domain.File{ID: "F1", WorkspaceID: "T1", Uploader: "U1", Name: "file", BlobKey: "blob", CreatedAt: time.Now().UTC()}, events.Event{ID: "EF1", WorkspaceID: "T1", Topic: "file.created", Payload: "F1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}
	if err := messages.DeleteFileComment(context.Background(), "T1", "U1", "F1", "FC1"); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteFileComment(context.Background(), "T1", "U1", "F1", "FC1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second deletion err=%v", err)
	}
}

func TestAdminAppApprovalIsDurableAndBounded(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SetAppApproval(ctx, "T1", "A1", "R1", domain.AppApprovalRequested, now, events.Event{ID: "EAPP1", WorkspaceID: "T1", Topic: "app.requested", Payload: "A1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	page, err := messages.AdminListApps(ctx, "T1", "U1", domain.AppApprovalRequested, domain.PageRequest{Limit: 1})
	if err != nil || len(page.Apps) != 1 || page.Apps[0].ID != "A1" {
		t.Fatalf("requested page=%+v err=%v", page, err)
	}
	if err := messages.AdminApproveApp(ctx, "T1", "U1", "A1", "R1"); err != nil {
		t.Fatal(err)
	}
	page, err = messages.AdminListApps(ctx, "T1", "U1", domain.AppApprovalApproved, domain.PageRequest{Limit: 1})
	if err != nil || len(page.Apps) != 1 || page.Apps[0].Status != domain.AppApprovalApproved {
		t.Fatalf("approved page=%+v err=%v", page, err)
	}
	if _, err := messages.AdminListApps(ctx, "T1", "U1", domain.AppApprovalApproved, domain.PageRequest{Limit: 0}); !errors.Is(err, store.ErrInvalidAppApproval) {
		t.Fatalf("invalid page error=%v", err)
	}
}

func TestCustomEmojiLifecycleNormalizesAndPersists(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	ctx := context.Background()
	if err := messages.AdminAddEmoji(ctx, "T1", "U1", " Wave ", "https://cdn.example/wave.png"); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminAddEmojiAlias(ctx, "T1", "U1", "hello", "WAVE"); err != nil {
		t.Fatal(err)
	}
	values, err := messages.Emojis(ctx, "T1", "U1")
	if err != nil || len(values) != 2 {
		t.Fatalf("values=%+v err=%v", values, err)
	}
	if err := messages.AdminRenameEmoji(ctx, "T1", "U1", "hello", "greeting"); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminRemoveEmoji(ctx, "T1", "U1", "wave"); err != nil {
		t.Fatal(err)
	}
	values, err = messages.Emojis(ctx, "T1", "U1")
	if err != nil || len(values) != 1 || values[0].Name != "greeting" {
		t.Fatalf("final values=%+v err=%v", values, err)
	}
}

func TestAdminConversationSearchIsBoundedAndWorkspaceScoped(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "engineering"})
	page, err := (Messages{Store: s}).AdminSearchConversations(context.Background(), "T1", "U1", "gene", domain.PageRequest{Limit: 1})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].ID != "C1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestUserGroupChannelMembershipLifecycle(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	ctx := context.Background()
	group, err := messages.CreateUserGroup(ctx, "T1", "U1", "Engineering", "engineering", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.AddUserGroupChannels(ctx, "T1", "U1", group.ID, []domain.ConversationID{"C1", "C1"}); err != nil {
		t.Fatal(err)
	}
	channels, err := messages.UserGroupChannels(ctx, "T1", "U1", group.ID)
	if err != nil || len(channels) != 1 || channels[0] != "C1" {
		t.Fatalf("channels=%v err=%v", channels, err)
	}
	if err := messages.RemoveUserGroupChannels(ctx, "T1", "U1", group.ID, []domain.ConversationID{"C1"}); err != nil {
		t.Fatal(err)
	}
	channels, err = messages.UserGroupChannels(ctx, "T1", "U1", group.ID)
	if err != nil || len(channels) != 0 {
		t.Fatalf("final channels=%v err=%v", channels, err)
	}
}

func TestAdminWorkspaceNameMutationIsDurable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "old"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	value, err := (Messages{Store: s}).AdminSetWorkspaceName(context.Background(), "T1", "U1", " New   Name ")
	if err != nil || value.Name != "New Name" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	loaded, err := s.GetWorkspace(context.Background(), "T1")
	if err != nil || loaded.Name != "New Name" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestAdminWorkspaceDescriptionMutationIsDurable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "old"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	value, err := (Messages{Store: s}).AdminSetWorkspaceDescription(context.Background(), "T1", "U1", " A   useful workspace ")
	if err != nil || value.Description != "A useful workspace" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	loaded, err := s.GetWorkspace(context.Background(), "T1")
	if err != nil || loaded.Description != "A useful workspace" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestAdminWorkspaceDiscoverabilityIsTypedAndDurable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	value, err := messages.AdminSetWorkspaceDiscoverability(context.Background(), "T1", "U1", domain.WorkspaceDiscoverabilityInviteOnly)
	if err != nil || value.Discoverability != domain.WorkspaceDiscoverabilityInviteOnly {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	if _, err := messages.AdminSetWorkspaceDiscoverability(context.Background(), "T1", "U1", "invalid"); err != ErrInvalidWorkspace {
		t.Fatalf("invalid discoverability err=%v", err)
	}
	loaded, err := s.GetWorkspace(context.Background(), "T1")
	if err != nil || loaded.Discoverability != domain.WorkspaceDiscoverabilityInviteOnly {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestAdminWorkspaceIconRequiresAbsoluteHTTPURL(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	value, err := messages.AdminSetWorkspaceIcon(context.Background(), "T1", "U1", " https://cdn.example/icon.png ")
	if err != nil || value.IconURL != "https://cdn.example/icon.png" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	if _, err := messages.AdminSetWorkspaceIcon(context.Background(), "T1", "U1", "relative/icon.png"); err != ErrInvalidWorkspace {
		t.Fatalf("relative icon err=%v", err)
	}
}

func TestAdminWorkspaceDefaultChannelsNormalizeAndValidate(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	value, err := messages.AdminSetWorkspaceDefaultChannels(context.Background(), "T1", "U1", []domain.ConversationID{" C1 ", "C1"})
	if err != nil || len(value.DefaultChannelIDs) != 1 || value.DefaultChannelIDs[0] != "C1" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	if _, err := messages.AdminSetWorkspaceDefaultChannels(context.Background(), "T1", "U1", []domain.ConversationID{"private"}); err != store.ErrNotFound {
		t.Fatalf("invalid channel err=%v", err)
	}
}

func TestAdminTeamUsersFiltersRolesAndPaginates(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	if err := s.SetWorkspaceRole(context.Background(), "T1", "U1", domain.WorkspaceRoleAdmin, events.Event{ID: "evt_role", WorkspaceID: "T1", Topic: "test", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	page, err := (Messages{Store: s}).AdminTeamUsers(context.Background(), "T1", "U2", domain.WorkspaceRoleAdmin, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Users) != 1 || page.Users[0].ID != "U1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestCallLifecycleNormalizesParticipants(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	value, err := messages.AddCall(context.Background(), "T1", "U1", "external", "", "https://call.example", "", "demo", time.Time{}, []domain.UserID{"U2", "U1", "U2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Participants) != 2 || value.Participants[0] != "U1" || value.Participants[1] != "U2" {
		t.Fatalf("participants=%v", value.Participants)
	}
	if err := messages.RemoveCallParticipants(context.Background(), "T1", "U1", value.ID, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	value, err = messages.GetCall(context.Background(), "T1", "U1", value.ID)
	if err != nil || len(value.Participants) != 1 || value.Participants[0] != "U1" {
		t.Fatalf("call=%+v err=%v", value, err)
	}
	if err := messages.EndCall(context.Background(), "T1", "U1", value.ID, 42); err != nil {
		t.Fatal(err)
	}
	value, err = messages.GetCall(context.Background(), "T1", "U1", value.ID)
	if err != nil || value.DurationSeconds != 42 || value.EndedAt.IsZero() {
		t.Fatalf("ended call=%+v err=%v", value, err)
	}
}

func TestPublicFileSharingStreamsOnlyWhileTokenIsActive(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	file, err := messages.UploadFile(context.Background(), "T1", "U1", "a.txt", "A", "text/plain", 5, bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	file, err = messages.ShareFilePublic(context.Background(), "T1", "U1", file.ID)
	if err != nil || file.PublicToken == "" {
		t.Fatalf("shared file=%+v err=%v", file, err)
	}
	_, reader, err := messages.OpenPublicFile(context.Background(), file.PublicToken)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(content) != "hello" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if _, err := messages.RevokeFilePublic(context.Background(), "T1", "U1", file.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := messages.OpenPublicFile(context.Background(), file.PublicToken); err != store.ErrNotFound {
		t.Fatalf("revoked public file err=%v", err)
	}
}

func TestUserPhotoStagesBlobAndExposesOnlyCommittedToken(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "photos"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	user, err := messages.SetUserPhoto(context.Background(), "T1", "U1", "image/png", 5, bytes.NewReader([]byte("photo")))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/users/T1/U1/photo/"
	if !strings.HasPrefix(user.Profile.Image24, prefix) {
		t.Fatalf("photo url=%q", user.Profile.Image24)
	}
	token := strings.TrimPrefix(user.Profile.Image24, prefix)
	_, reader, err := messages.OpenUserPhoto(context.Background(), "T1", "U1", token)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(content) != "photo" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if _, _, err := messages.OpenUserPhoto(context.Background(), "T1", "U1", "wrong"); err != store.ErrNotFound {
		t.Fatalf("wrong token err=%v", err)
	}
}

type failingProfileStore struct {
	store.Store
	err error
}

func (s failingProfileStore) UpdateUserProfile(context.Context, domain.WorkspaceID, domain.UserID, domain.UserProfile, events.Event) (domain.User, error) {
	return domain.User{}, s.err
}

type failingPhotoCleanupBlob struct {
	deleteErr error
}

func (failingPhotoCleanupBlob) Put(context.Context, string, int64, io.Reader) (blob.Object, error) {
	return blob.Object{}, nil
}

func (failingPhotoCleanupBlob) Open(context.Context, string) (blob.Object, io.ReadCloser, error) {
	return blob.Object{}, nil, blob.ErrNotFound
}

func (b failingPhotoCleanupBlob) Delete(context.Context, string) error {
	return b.deleteErr
}

func TestUserPhotoReportsBlobCleanupFailureAfterProfileFailure(t *testing.T) {
	base := memory.New()
	base.SeedWorkspace(domain.Workspace{ID: "T1"})
	base.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	profileErr := errors.New("profile update failed")
	cleanupErr := errors.New("blob delete failed")
	messages := Messages{
		Store: failingProfileStore{Store: base, err: profileErr},
		Blob:  failingPhotoCleanupBlob{deleteErr: cleanupErr},
	}
	_, err := messages.SetUserPhoto(context.Background(), "T1", "U1", "image/png", 5, bytes.NewReader([]byte("photo")))
	if !errors.Is(err, profileErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("error=%v, want profile and cleanup errors", err)
	}
}

func TestEphemeralMessageIsDurableAndRecipientScoped(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	value, err := (Messages{Store: s}).PostEphemeral(context.Background(), "T1", "U1", "C1", "U2", "secret")
	if err != nil || value.RecipientID != "U2" || value.Text != "secret" {
		t.Fatalf("ephemeral=%+v err=%v", value, err)
	}
	if _, err := (Messages{Store: s}).PostEphemeral(context.Background(), "T1", "U1", "C1", "U3", "secret"); err != store.ErrNotFound {
		t.Fatalf("foreign recipient err=%v", err)
	}
	records, err := s.ListEventsAfter(context.Background(), "T1", 0, 10)
	if err != nil || len(records) != 1 || records[0].Event.Topic != events.EphemeralMessageTopic {
		t.Fatalf("events=%+v err=%v", records, err)
	}
}

func TestPostMessagePersistsMessage(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	message, err := (Messages{Store: s}).Post(context.Background(), "T1", "U1", "C1", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "hello" || message.ID == "" {
		t.Fatalf("unexpected message: %+v", message)
	}
	got, err := s.ListMessages(context.Background(), "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(got.Messages) != 1 {
		t.Fatalf("messages = %+v, err = %v", got, err)
	}
}

func TestPostWithBlocksPersistsNormalizedPayload(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	message, err := (Messages{Store: s}).PostWithBlocks(context.Background(), "T1", "U1", "C1", "", ` [ { "type": "section" } ] `, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "" || message.Blocks != `[{"type":"section"}]` {
		t.Fatalf("unexpected message: %+v", message)
	}
	updated, err := (Messages{Store: s}).UpdateWithBlocks(context.Background(), "T1", "U1", "C1", domain.NewMessageTimestamp(message.CreatedAt), "updated", `[{"type":"divider"}]`)
	if err != nil || updated.Text != "updated" || updated.Blocks != `[{"type":"divider"}]` {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestPrivateConversationRequiresMembership(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "Cprivate", WorkspaceID: "T1", Name: "private", IsPrivate: true})
	if _, err := (Messages{Store: s}).Post(context.Background(), "T1", "U1", "Cprivate", "secret", "", ""); err == nil {
		t.Fatal("private conversation allowed non-member")
	}
	s.SeedConversationMember("Cprivate", "U1")
	if _, err := (Messages{Store: s}).Post(context.Background(), "T1", "U1", "Cprivate", "secret", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateAndDeleteMessageUseTypedTimestampAndOutbox(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	created, err := messages.Post(context.Background(), "T1", "U1", "C1", "before", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(created.CreatedAt)
	updated, err := messages.Update(context.Background(), "T1", "U1", "C1", timestamp, "after")
	if err != nil || updated.Text != "after" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	deleted, err := messages.Delete(context.Background(), "T1", "U1", "C1", timestamp)
	if err != nil || !deleted.Deleted {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	if _, err := messages.Delete(context.Background(), "T1", "U1", "C1", timestamp); err != ErrMessageAlreadyDeleted {
		t.Fatalf("second delete err=%v", err)
	}
	if got := len(s.Outbox()); got != 3 {
		t.Fatalf("outbox events=%d, want 3", got)
	}
}

func TestReplyStoresSlackThreadTimestamp(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	root, err := messages.Post(context.Background(), "T1", "U1", "C1", "root", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.NewMessageTimestamp(root.CreatedAt)
	reply, err := messages.Post(context.Background(), "T1", "U1", "C1", "reply", thread, "")
	if err != nil {
		t.Fatal(err)
	}
	if reply.ThreadTimestamp != thread {
		t.Fatalf("thread timestamp=%q, want %q", reply.ThreadTimestamp, thread)
	}
	page, err := messages.Replies(context.Background(), "T1", "U1", "C1", thread, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 2 || page.Messages[0].ID != root.ID || page.Messages[1].ID != reply.ID {
		t.Fatalf("replies=%+v err=%v", page, err)
	}
}

func TestIdempotentPostReturnsOriginalCommittedMessage(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	first, err := messages.Post(context.Background(), "T1", "U1", "C1", "first", "", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := messages.Post(context.Background(), "T1", "U1", "C1", "different retry payload", "", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Text != "first" || len(s.Outbox()) != 1 {
		t.Fatalf("first=%+v second=%+v outbox=%d", first, second, len(s.Outbox()))
	}
}

func TestMarkReadPersistsCursorAndOutbox(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	cursor, err := messages.MarkRead(context.Background(), "T1", "U1", "C1", "1700000000.123456")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReadCursor(context.Background(), "T1", "U1", "C1")
	if err != nil || got.LastRead != cursor.LastRead || got.UpdatedAt.IsZero() {
		t.Fatalf("cursor=%+v got=%+v err=%v", cursor, got, err)
	}
	if len(s.Outbox()) != 1 || s.Outbox()[0].Topic != "conversation.read" {
		t.Fatalf("outbox=%+v", s.Outbox())
	}
}

func TestReactionsAreDurableAndIdempotentlyRejected(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	message, err := messages.Post(context.Background(), "T1", "U1", "C1", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	if err := messages.AddReaction(context.Background(), "T1", "U1", "C1", timestamp, "  THUMBSUP "); err != nil {
		t.Fatal(err)
	}
	if err := messages.AddReaction(context.Background(), "T1", "U1", "C1", timestamp, "thumbsup"); err != store.ErrAlreadyExists {
		t.Fatalf("duplicate reaction err=%v", err)
	}
	values, _, more, err := messages.Reactions(context.Background(), "T1", "U1", "C1", timestamp, domain.PageRequest{Limit: 10})
	if err != nil || more || len(values) != 1 || values[0].Name != "thumbsup" || values[0].UserID != "U1" {
		t.Fatalf("reactions=%+v more=%t err=%v", values, more, err)
	}
	userReactions, err := messages.UserReactions(context.Background(), "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || userReactions.HasMore || len(userReactions.Items) != 1 || userReactions.Items[0].Message.ID != message.ID {
		t.Fatalf("user reactions=%+v err=%v", userReactions, err)
	}
	if err := messages.RemoveReaction(context.Background(), "T1", "U1", "C1", timestamp, "thumbsup"); err != nil {
		t.Fatal(err)
	}
	if len(s.Outbox()) != 3 {
		t.Fatalf("outbox events=%d, want 3", len(s.Outbox()))
	}
}

func TestPinsAreDurableAndScopedToConversation(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	message, err := messages.Post(context.Background(), "T1", "U1", "C1", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	if err := messages.AddPin(context.Background(), "T1", "U1", "C1", timestamp); err != nil {
		t.Fatal(err)
	}
	if err := messages.AddPin(context.Background(), "T1", "U1", "C1", timestamp); err != store.ErrAlreadyExists {
		t.Fatalf("duplicate pin err=%v", err)
	}
	pins, _, more, err := messages.Pins(context.Background(), "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || more || len(pins) != 1 || pins[0].Message != message.ID {
		t.Fatalf("pins=%+v more=%t err=%v", pins, more, err)
	}
	if err := messages.RemovePin(context.Background(), "T1", "U1", "C1", timestamp); err != nil {
		t.Fatal(err)
	}
}

func TestUploadFileKeepsBytesExternalAndMetadataDurable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	file, err := messages.UploadFile(context.Background(), "T1", "U1", "notes.txt", "Notes", "text/plain", 7, bytes.NewReader([]byte("content")))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := messages.FileInfo(context.Background(), "T1", "U1", file.ID)
	if err != nil || metadata.BlobKey != file.BlobKey || metadata.Size != 7 {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	page, err := messages.Files(context.Background(), "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Files) != 1 {
		t.Fatalf("files=%+v err=%v", page, err)
	}
	if err := messages.DeleteFile(context.Background(), "T1", "U1", file.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := objects.Open(context.Background(), file.BlobKey); err != nil {
		t.Fatalf("blob before cleanup err=%v", err)
	}
	cleanup, err := blob.NewCleanupWorker(s, objects, "cleanup-1", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := cleanup.RunOnce(context.Background(), "T1"); err != nil || count != 1 {
		t.Fatalf("cleanup count=%d err=%v", count, err)
	}
	if _, _, err := objects.Open(context.Background(), file.BlobKey); err != blob.ErrNotFound {
		t.Fatalf("blob after cleanup err=%v", err)
	}
}

func TestSearchNormalizesTermsAndHidesPrivateConversations(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", IsPrivate: true})
	messages := Messages{Store: s}
	if _, err := messages.Post(context.Background(), "T1", "U1", "C1", "Hello durable search", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(context.Background(), "T1", "U1", "C2", "Hello secret", "", ""); err == nil {
		t.Fatal("private conversation allowed without membership")
	}
	page, err := messages.Search(context.Background(), "T1", "U1", "  HELLO   durable ", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Conversation != "C1" {
		t.Fatalf("search=%+v err=%v", page, err)
	}
}

func TestSetUserProfileNormalizesAndPersists(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	messages := Messages{Store: s}
	user, err := messages.SetUserProfile(context.Background(), "T1", "U1", domain.UserProfile{DisplayName: " alice ", StatusText: " Available ", StatusEmoji: " :wave: "})
	if err != nil || user.Profile.DisplayName != "alice" || user.Profile.StatusText != "Available" || user.Profile.StatusEmoji != ":wave:" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	stored, err := s.GetUser(context.Background(), "U1")
	if err != nil || stored.Profile.DisplayName != "alice" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if _, err := messages.SetUserProfile(context.Background(), "T1", "U1", domain.UserProfile{StatusText: string(make([]byte, 101))}); err != ErrInvalidProfile {
		t.Fatalf("oversized profile err=%v", err)
	}
}

func TestScheduleMessageWithBlocksPersistsNormalizedPayload(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	value, err := (Messages{Store: s}).ScheduleMessageWithBlocks(context.Background(), "T1", "U1", "C1", "", ` [{"type":"divider"}] `, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if value.Text != "" || value.Blocks != `[{"type":"divider"}]` {
		t.Fatalf("scheduled=%+v", value)
	}
	page, err := (Messages{Store: s}).ScheduledMessages(context.Background(), "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Blocks != value.Blocks {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestPostEphemeralWithBlocksPersistsNormalizedEvent(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	value, err := (Messages{Store: s}).PostEphemeralWithBlocks(context.Background(), "T1", "U1", "C1", "U2", "", ` [{"type":"divider"}] `)
	if err != nil {
		t.Fatal(err)
	}
	if value.Text != "" || value.Blocks != `[{"type":"divider"}]` {
		t.Fatalf("ephemeral=%+v", value)
	}
	records, err := s.ListEventsAfter(context.Background(), "T1", 0, 10)
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Event.Payload, `"blocks":"[{\"type\":\"divider\"}]"`) {
		t.Fatalf("events=%+v err=%v", records, err)
	}
}
