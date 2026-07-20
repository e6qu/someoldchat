package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestSQLiteFindUserByEmailIsCaseInsensitiveAndMigrated(t *testing.T) {
	s, err := Open(context.Background(), "file:email_lookup?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(context.Background(), domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(context.Background(), domain.User{ID: "U1", WorkspaceID: "T1", Email: "Alice@example.com", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	user, err := s.FindUserByEmail(context.Background(), "T1", " ALICE@EXAMPLE.COM ")
	if err != nil || user.ID != "U1" || user.Email != "alice@example.com" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
}

func TestSQLiteCreateUserIsTransactionalAndWorkspaceScoped(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:create-user?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	value := domain.User{ID: "U2", WorkspaceID: "T1", Email: "alice@example.com", Name: "Alice", RealName: "Alice"}
	membership := domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "U2", Role: domain.WorkspaceRoleAdmin, Active: true}
	if err := s.CreateUser(ctx, value, membership, events.Event{ID: "E-user", WorkspaceID: "T1", Topic: "user.created", Payload: "U2", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetUser(ctx, "U2")
	if err != nil || loaded.Email != value.Email {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	role, err := s.GetWorkspaceMembership(ctx, "T1", "U2")
	if err != nil || role.Role != domain.WorkspaceRoleAdmin {
		t.Fatalf("membership=%+v err=%v", role, err)
	}
	adminUsers, err := s.ListAdminUsers(ctx, "T1", domain.PageRequest{Limit: 1})
	if err != nil || len(adminUsers.Users) != 1 || adminUsers.Users[0].User.ID != "U2" || adminUsers.Users[0].Membership.Role != domain.WorkspaceRoleAdmin || !adminUsers.Users[0].Membership.Active {
		t.Fatalf("administrator users=%+v err=%v", adminUsers, err)
	}
	if err := s.CreateUser(ctx, domain.User{ID: "U3", WorkspaceID: "T1", Email: "ALICE@EXAMPLE.COM", Name: "Other"}, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "U3", Role: domain.WorkspaceRoleMember, Active: true}, events.Event{ID: "E-duplicate", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != store.ErrAlreadyExists {
		t.Fatalf("duplicate error=%v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO users (id, workspace_id, email, name) VALUES (?, ?, ?, ?)`, "U4", "T1", "Alice@Example.com", "Other"); err == nil {
		t.Fatal("database accepted a case-insensitive duplicate user email")
	}
}

func TestSQLiteUserRemovalRevokesCredentialsAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:remove-user-credentials?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U2", WorkspaceID: "T1", Email: "u2@example.com", Name: "User Two"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedToken(ctx, "user-two-token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U2", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(ctx, "user-two-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", Scopes: []string{"chat:write"}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDeleted(ctx, "T1", "U2", true, events.Event{ID: "E-remove-user", WorkspaceID: "T1", Topic: "user.removed", Payload: "U2", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	token, err := s.LookupToken(ctx, "user-two-token")
	if err != nil || !token.Revoked {
		t.Fatalf("token=%+v err=%v", token, err)
	}
	session, err := s.LookupSession(ctx, "user-two-session")
	if err != nil || !session.Revoked {
		t.Fatalf("session=%+v err=%v", session, err)
	}
}

func TestSQLiteSessionPreservesOIDCLogoutMetadata(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:oidc-session-metadata?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	expected := domain.SessionRecord{
		WorkspaceID: "T1", UserID: "U1", Scopes: []string{"users:read", "openid"}, ExpiresAt: time.Now().UTC().Add(time.Hour),
		OIDCProvider: "oidc", OIDCIDToken: "signed.id.token", OIDCSubject: "subject", OIDCSID: "provider-session",
	}
	if err := s.CreateSession(ctx, "session", expected); err != nil {
		t.Fatal(err)
	}
	got, err := s.LookupSession(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != expected.WorkspaceID || got.UserID != expected.UserID || got.OIDCProvider != expected.OIDCProvider || got.OIDCIDToken != expected.OIDCIDToken || got.OIDCSubject != expected.OIDCSubject || got.OIDCSID != expected.OIDCSID {
		t.Fatalf("session metadata=%+v, want=%+v", got, expected)
	}
}

func TestSQLiteViewLifecycleIsDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:view-lifecycle?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domain.View{ID: "V1", WorkspaceID: "T1", UserID: "U1", Type: "home", ExternalID: "home-1", Payload: `{"type":"home","blocks":[]}`, Hash: "hash-1", RootViewID: "V1", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateView(ctx, value, events.Event{ID: "EV1", WorkspaceID: "T1", Topic: "view.created", Payload: "V1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetPublishedView(ctx, "T1", "U1")
	if err != nil || loaded.Payload != value.Payload || loaded.ExternalID != value.ExternalID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	value.Payload = `{"type":"home","blocks":[{"type":"section"}]}`
	value.Hash = "hash-2"
	value.UpdatedAt = now.Add(time.Second)
	updated, err := s.UpdateView(ctx, value, "hash-1", events.Event{ID: "EV2", WorkspaceID: "T1", Topic: "view.updated", Payload: "V1", CreatedAt: now.Add(time.Second)})
	if err != nil || updated.Hash != "hash-2" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := s.UpdateView(ctx, value, "hash-1", events.Event{ID: "EV3", WorkspaceID: "T1", Topic: "view.updated", Payload: "V1", CreatedAt: now.Add(2 * time.Second)}); err == nil {
		t.Fatal("stale hash unexpectedly succeeded")
	}
}

func TestSQLiteWorkflowStepLifecycleIsDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:workflow-step-lifecycle?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domain.WorkflowStep{ID: "execute-1", WorkspaceID: "T1", UserID: "U1", Status: domain.WorkflowStepCompleted, Outputs: `{"ok":true}`, CreatedAt: now, UpdatedAt: now}
	if err := s.SetWorkflowStep(ctx, value, events.Event{ID: "EW1", WorkspaceID: "T1", Topic: "workflow.step_completed", Payload: "execute-1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetWorkflowStep(ctx, "T1", "execute-1")
	if err != nil || loaded.Status != domain.WorkflowStepCompleted || loaded.Outputs != value.Outputs {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	value.Status = domain.WorkflowStepFailed
	value.Error = `{"message":"failed"}`
	value.UpdatedAt = now.Add(time.Second)
	if err := s.SetWorkflowStep(ctx, value, events.Event{ID: "EW2", WorkspaceID: "T1", Topic: "workflow.step_failed", Payload: "execute-1", CreatedAt: value.UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.GetWorkflowStep(ctx, "T1", "execute-1")
	if err != nil || loaded.Status != domain.WorkflowStepFailed || loaded.CreatedAt.IsZero() {
		t.Fatalf("updated=%+v err=%v", loaded, err)
	}
}

func TestSQLiteDialogIsDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:dialog-lifecycle?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domain.Dialog{ID: "D1", WorkspaceID: "T1", UserID: "U1", Payload: `{"callback_id":"callback","title":"Title","elements":[{"type":"text"}]}`, CreatedAt: now}
	if err := s.CreateDialog(ctx, value, events.Event{ID: "ED1", WorkspaceID: "T1", Topic: "dialog.opened", Payload: "D1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetDialog(ctx, "T1", "D1")
	if err != nil || loaded.Payload != value.Payload || loaded.UserID != value.UserID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestSQLiteBotRegistryIsDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:bot-registry?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	value := domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "U1", Name: "bot", UpdatedAt: time.Now().UTC()}
	if err := s.CreateBot(ctx, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetBot(ctx, "T1", "B1")
	if err != nil || loaded.Name != "bot" || loaded.AppID != "A1" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestSQLiteUserMigrationMappingIsDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:user-migration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domain.UserMigration{WorkspaceID: "T1", OldID: "U1", GlobalID: "W1"}
	if err := s.CreateUserMigration(ctx, value, events.Event{ID: "EM1", WorkspaceID: "T1", Topic: "migration.created", Payload: "U1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.FindUserMigration(ctx, "T1", "W1")
	if err != nil || loaded.OldID != "U1" || loaded.GlobalID != "W1" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestSQLiteRemoteFileLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:remote-file-lifecycle?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	created := time.Now().UTC()
	value := domain.RemoteFile{ID: "file_remote_1", WorkspaceID: "T1", ExternalID: "external-1", Title: "Remote", ExternalURL: "https://files.example/doc", CreatedAt: created}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRemoteFile(ctx, value, events.Event{ID: "evt_remote_1", WorkspaceID: "T1", Topic: "remote_file.created", Payload: string(value.ID), CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetRemoteFile(ctx, "T1", domain.RemoteFileLookup{ExternalID: "external-1"})
	if err != nil || loaded.ID != value.ID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	page, err := s.ListRemoteFiles(ctx, "T1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Files) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	updated, err := s.UpdateRemoteFile(ctx, "T1", domain.RemoteFile{ID: value.ID, Title: "Updated", ExternalURL: value.ExternalURL}, events.Event{ID: "evt_remote_1b", WorkspaceID: "T1", Topic: "remote_file.updated", Payload: string(value.ID), CreatedAt: created})
	if err != nil || updated.Title != "Updated" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if err := s.RemoveRemoteFile(ctx, "T1", domain.RemoteFileLookup{ID: value.ID}, events.Event{ID: "evt_remote_2", WorkspaceID: "T1", Topic: "remote_file.removed", Payload: string(value.ID), CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListRemoteFiles(ctx, "T1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Files) != 0 {
		t.Fatalf("after remove page=%+v err=%v", page, err)
	}
}

func TestSQLiteUserPresenceIsDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:user-presence?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	user, err := s.SetUserPresence(ctx, "T1", "U1", domain.PresenceAway, events.Event{ID: "presence-1", WorkspaceID: "T1", Topic: "user.presence_changed", Payload: "U1", CreatedAt: time.Now().UTC()})
	if err != nil || user.Presence != domain.PresenceAway {
		t.Fatalf("updated user=%+v err=%v", user, err)
	}
	user, err = s.GetUser(ctx, "U1")
	if err != nil || user.Presence != domain.PresenceAway {
		t.Fatalf("stored user=%+v err=%v", user, err)
	}
}

func TestSQLiteUserExpirationInvalidatesTokenAndCanBeCleared(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:user-expiration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedToken(ctx, "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupToken(ctx, "token"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserExpiration(ctx, "T1", "U1", time.Unix(1, 0), events.Event{ID: "expiration-1", WorkspaceID: "T1", Topic: "user.expiration_changed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupToken(ctx, "token"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired token error=%v", err)
	}
	if err := s.SetUserExpiration(ctx, "T1", "U1", time.Time{}, events.Event{ID: "expiration-2", WorkspaceID: "T1", Topic: "user.expiration_changed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupToken(ctx, "token"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteDeleteConversationRemovesChannelAndDependents(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:delete-conversation?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReadCursor(ctx, domain.ReadCursor{WorkspaceID: "T1", UserID: "U1", Conversation: "C1", LastRead: "1.000000", UpdatedAt: time.Now().UTC()}, events.Event{ID: "cursor-delete", WorkspaceID: "T1", Topic: "cursor.changed", Payload: "C1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConversation(ctx, "T1", "C1", events.Event{ID: "conversation-delete", WorkspaceID: "T1", Topic: "conversation.deleted", Payload: "C1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConversation(ctx, "C1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted conversation error=%v", err)
	}
	if _, err := s.GetReadCursor(ctx, "T1", "U1", "C1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted cursor error=%v", err)
	}
}

func TestSQLiteCallsAreDurable(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "calls.db")
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Truncate(time.Second)
	value := domain.Call{ID: "call_123", WorkspaceID: "T1", ExternalUniqueID: "external-1", JoinURL: "https://call.example", CreatedBy: "U1", StartedAt: created, Participants: []domain.UserID{"U1"}}
	if err := s.CreateCall(ctx, value, events.Event{ID: "call-event-1", WorkspaceID: "T1", Topic: "call.created", Payload: "call_123", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetCall(ctx, "T1", "call_123")
	if err != nil || got.ExternalUniqueID != "external-1" || len(got.Participants) != 1 || got.Participants[0] != "U1" {
		t.Fatalf("call=%+v err=%v", got, err)
	}
}

func TestSQLitePublicFileSharingIsDurable(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "public-file.db")
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1700000000, 0).UTC()
	file := domain.File{ID: "file_1", WorkspaceID: "T1", Uploader: "U1", Name: "a.txt", Title: "A", MIMEType: "text/plain", BlobKey: "T1/file_1", CreatedAt: created}
	if err := s.CreateFile(ctx, file, events.Event{ID: "file-event-1", WorkspaceID: "T1", Topic: "file.created", Payload: "file_1", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := s.ShareFilePublic(ctx, "T1", "file_1", "pub_test", events.Event{ID: "file-event-2", WorkspaceID: "T1", Topic: "file.public_shared", Payload: "file_1", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetPublicFile(ctx, "pub_test")
	if err != nil || got.PublicToken != "pub_test" {
		t.Fatalf("file=%+v err=%v", got, err)
	}
	if err := s.RevokeFilePublic(ctx, "T1", "file_1", events.Event{ID: "file-event-3", WorkspaceID: "T1", Topic: "file.public_revoked", Payload: "file_1", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPublicFile(ctx, "pub_test"); err != store.ErrNotFound {
		t.Fatalf("revoked lookup err=%v", err)
	}
}

func TestSQLiteAccessLogsAreBoundedAndDurable(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "access-logs.db")
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1700000000, 0).UTC()
	for index := 0; index < 3; index++ {
		value := domain.AccessLog{WorkspaceID: "T1", UserID: "U1", Username: "alice", CreatedAt: created.Add(time.Duration(index) * time.Second), IP: "127.0.0.1", UserAgent: "test"}
		if err := s.RecordAccess(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	values, hasMore, err := s.ListAccessLogs(ctx, "T1", time.Time{}, 2, 1)
	if err != nil || len(values) != 2 || !hasMore {
		t.Fatalf("values=%+v hasMore=%v err=%v", values, hasMore, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	values, _, err = s.ListAccessLogs(ctx, "T1", time.Time{}, 10, 1)
	if err != nil || len(values) != 3 {
		t.Fatalf("durable values=%+v err=%v", values, err)
	}
}

func TestSQLiteDoNotDisturbIsDurable(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "dnd.db")
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := s.SetDoNotDisturb(ctx, domain.DoNotDisturb{WorkspaceID: "T1", UserID: "U1", Enabled: true, SnoozeUntil: until}, events.Event{ID: "dnd-1", WorkspaceID: "T1", Topic: "user.dnd_changed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetDoNotDisturb(ctx, "T1", "U1")
	if err != nil || !got.Enabled || !got.SnoozeUntil.Equal(until) || !got.NextStartAt.IsZero() || !got.NextEndAt.IsZero() {
		t.Fatalf("dnd=%+v err=%v", got, err)
	}
}

func TestSQLiteStarsAreDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:stars?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember(ctx, "C1", "U1"); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(300, 0).UTC()
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "starred", CreatedAt: created}
	if err := s.CreateMessage(ctx, message, events.Event{ID: "message-1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddStar(ctx, domain.Star{Message: message, Conversation: "C1", UserID: "U1", CreatedAt: created}, events.Event{ID: "star-1", WorkspaceID: "T1", Topic: "star.added", Payload: "M1", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	stars, _, more, err := s.ListStars(ctx, "T1", "U1", domain.PageRequest{Limit: 1})
	if err != nil || len(stars) != 1 || stars[0].Message.ID != "M1" || more {
		t.Fatalf("stars=%+v more=%v err=%v", stars, more, err)
	}
}

func TestSQLiteRemindersAreDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:reminders?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	id, err := domain.NewReminderID()
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	reminder := domain.Reminder{WorkspaceID: "T1", ID: id, Creator: "U1", User: "U1", Text: "check-in", Time: due}
	if err := s.CreateReminder(ctx, reminder, events.Event{ID: "reminder-1", WorkspaceID: "T1", Topic: "reminder.created", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReminder(ctx, "T1", "U1", id)
	if err != nil || got.Text != "check-in" || !got.Time.Equal(due) {
		t.Fatalf("reminder=%+v err=%v", got, err)
	}
}

func TestSQLiteScheduledMessagesAreDurable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:scheduled-messages?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	id, err := domain.NewScheduledMessageID()
	if err != nil {
		t.Fatal(err)
	}
	value := domain.ScheduledMessage{WorkspaceID: "T1", ID: id, Channel: "C1", Author: "U1", Text: "later", PostAt: time.Now().UTC().Add(-time.Hour).Truncate(time.Second), CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := s.CreateScheduledMessage(ctx, value, events.Event{ID: "scheduled-1", WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != id {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	claimed, err := s.ClaimScheduledMessages(ctx, "T1", "worker-1", 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != id {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := s.MarkScheduledMessageDelivered(ctx, "worker-1", id); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("delivered page=%+v err=%v", page, err)
	}
}

func TestSQLiteScheduledMessageShortLeaseRemainsUsable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:scheduled-short-lease?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, seed := range []func() error{
		func() error { return s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}) },
		func() error { return s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}) },
		func() error { return s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1"}) },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	value := domain.ScheduledMessage{WorkspaceID: "T1", ID: "Q-short-lease", Channel: "C1", Author: "U1", Text: "short lease", PostAt: now.Add(-time.Minute), CreatedAt: now}
	if err := s.CreateScheduledMessage(ctx, value, events.Event{ID: "scheduled-short-lease", WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(value.ID), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimScheduledMessages(ctx, value.WorkspaceID, "worker-1", 1, 20*time.Millisecond)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := s.MarkScheduledMessageDelivered(ctx, "worker-1", value.ID); err != nil {
		t.Fatalf("short scheduled-message lease was not usable: %v", err)
	}
}

func TestSQLiteEarliestScheduledMessage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:scheduled-deadline?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, seed := range []func() error{
		func() error { return s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}) },
		func() error { return s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}) },
		func() error { return s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1"}) },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	first := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	second := first.Add(time.Hour)
	for _, value := range []domain.ScheduledMessage{
		{WorkspaceID: "T1", ID: "Q1", Channel: "C1", Author: "U1", Text: "first", PostAt: first, CreatedAt: first},
		{WorkspaceID: "T1", ID: "Q2", Channel: "C1", Author: "U1", Text: "second", PostAt: second, CreatedAt: second},
	} {
		if err := s.CreateScheduledMessage(ctx, value, events.Event{ID: domain.EventID("event-" + string(value.ID)), WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(value.ID), CreatedAt: value.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.EarliestScheduledMessage(ctx, "T1")
	if err != nil || !got.Equal(first) {
		t.Fatalf("deadline=%s err=%v, want %s", got, err, first)
	}
}

func TestSQLiteScheduledMessagePaginationContinues(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:scheduled-pagination?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []domain.ScheduledMessageID{"Q1", "Q2"} {
		value := domain.ScheduledMessage{WorkspaceID: "T1", ID: id, Channel: "C1", Author: "U1", Text: string(id), PostAt: now.Add(time.Hour), CreatedAt: now}
		if err := s.CreateScheduledMessage(ctx, value, events.Event{ID: domain.EventID("scheduled-" + string(id)), WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(id), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = s.ListScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(page.Items) != 1 || page.HasMore {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
}

func TestSQLiteConversationListFiltersBeforePagination(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:conversation-filters?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "public"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", IsPrivate: true, Archived: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember(ctx, "C2", "U1"); err != nil {
		t.Fatal(err)
	}
	public, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 1, Types: []domain.ConversationType{domain.ConversationTypePublic}})
	if err != nil || len(public.Conversations) != 1 || public.Conversations[0].ID != "C1" || public.HasMore {
		t.Fatalf("public filter=%+v err=%v", public, err)
	}
	active, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10, ExcludeArchived: true})
	if err != nil || len(active.Conversations) != 1 || active.Conversations[0].ID != "C1" {
		t.Fatalf("archived filter=%+v err=%v", active, err)
	}
}

func TestSQLiteRoundTrip(t *testing.T) {
	s, err := Open(context.Background(), "file:roundtrip?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var foreignKeys int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d", foreignKeys)
	}
	if err := s.IntegrityCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workspaces(id, name) VALUES ('T1', 'Test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO users(id, workspace_id, name, display_name, status_text, status_emoji) VALUES ('U1', 'T1', 'alice', 'Alice', 'Available', ':wave:')`); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(context.Background(), domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedToken(context.Background(), "secret-token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write", "channels:history"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(context.Background(), domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	membership, err := s.GetWorkspaceMembership(context.Background(), "T1", "U2")
	if err != nil || membership.Role != "member" || !membership.Active {
		t.Fatalf("membership=%+v err=%v", membership, err)
	}
	if err := s.SeedConversation(context.Background(), domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", IsPrivate: true}); err != nil {
		t.Fatal(err)
	}
	users, err := s.ListUsers(context.Background(), "T1", domain.PageRequest{Limit: 1})
	if err != nil || len(users.Users) != 1 || !users.HasMore || users.NextCursor == "" {
		t.Fatalf("first users=%+v err=%v", users, err)
	}
	users, err = s.ListUsers(context.Background(), "T1", domain.PageRequest{Limit: 1, Cursor: users.NextCursor})
	if err != nil || len(users.Users) != 1 || users.HasMore {
		t.Fatalf("second users=%+v err=%v", users, err)
	}
	conversations, err := s.ListConversations(context.Background(), "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(conversations.Conversations) != 1 {
		t.Fatalf("private conversation leaked=%+v err=%v", conversations, err)
	}
	if err := s.SeedConversationMember(context.Background(), "C2", "U1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember(context.Background(), "C2", "U2"); err != nil {
		t.Fatal(err)
	}
	members, err := s.ListConversationMembers(context.Background(), "C2", domain.PageRequest{Limit: 1})
	if err != nil || len(members.Users) != 1 || !members.HasMore || members.Users[0].ID != "U1" {
		t.Fatalf("conversation members=%+v err=%v", members, err)
	}
	conversations, err = s.ListConversations(context.Background(), "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(conversations.Conversations) != 2 {
		t.Fatalf("authorized conversations=%+v err=%v", conversations, err)
	}
	record, err := s.LookupToken(context.Background(), "secret-token")
	if err != nil || len(record.Scopes) != 2 || record.WorkspaceID != "T1" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if err := s.RevokeToken(context.Background(), "secret-token"); err != nil {
		t.Fatal(err)
	}
	record, err = s.LookupToken(context.Background(), "secret-token")
	if err != nil || !record.Revoked {
		t.Fatalf("revoked record=%+v err=%v", record, err)
	}
	user, err := s.GetUser(context.Background(), "U1")
	if err != nil || user.Profile.DisplayName != "Alice" || user.Profile.StatusText != "Available" || user.Profile.StatusEmoji != ":wave:" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	updated, err := s.UpdateUserProfile(context.Background(), "T1", "U2", domain.UserProfile{DisplayName: "bob", StatusText: "Ready"}, events.Event{ID: "evt_profile", WorkspaceID: "T1", Topic: "user.profile_changed", Payload: "U2", CreatedAt: time.Now().UTC()})
	if err != nil || updated.Profile.DisplayName != "bob" || updated.Profile.StatusText != "Ready" {
		t.Fatalf("updated user=%+v err=%v", updated, err)
	}
	direct := domain.Conversation{ID: "D1", WorkspaceID: "T1", Name: "direct", IsPrivate: true, IsDirect: true}
	if err := s.CreateDirectConversation(context.Background(), direct, []domain.UserID{"U1", "U2"}, events.Event{ID: "evt_direct", WorkspaceID: "T1", Topic: "conversation.direct_created", Payload: "D1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	found, err := s.FindDirectConversation(context.Background(), "T1", []domain.UserID{"U2", "U1"})
	if err != nil || found.ID != "D1" || !found.IsDirect {
		t.Fatalf("found direct=%+v err=%v", found, err)
	}
	var plaintextCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE token_hash = 'secret-token'`).Scan(&plaintextCount); err != nil {
		t.Fatal(err)
	}
	if plaintextCount != 0 {
		t.Fatal("token was stored in plaintext")
	}
	want := domain.Message{ID: "msg_1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: time.Unix(100, 123000000).UTC()}
	if err := s.CreateMessage(context.Background(), want, events.Event{ID: "evt_1", WorkspaceID: "T1", Topic: "message.created", Payload: string(want.ID), CreatedAt: want.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListMessages(context.Background(), "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(got.Messages) != 1 {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
	if got.Messages[0].Text != want.Text || !got.Messages[0].CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("got = %+v, want = %+v", got.Messages[0], want)
	}
	reply := domain.Message{ID: "msg_reply", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "reply", ThreadTimestamp: domain.NewMessageTimestamp(want.CreatedAt), CreatedAt: time.Unix(101, 0).UTC()}
	if err := s.CreateMessage(context.Background(), reply, events.Event{ID: "evt_reply", WorkspaceID: "T1", Topic: "message.created", Payload: string(reply.ID), CreatedAt: reply.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	replies, err := s.ListThreadMessages(context.Background(), "C1", domain.NewMessageTimestamp(want.CreatedAt), domain.PageRequest{Limit: 10})
	if err != nil || len(replies.Messages) != 2 || replies.Messages[0].ID != want.ID || replies.Messages[1].ID != reply.ID {
		t.Fatalf("replies = %+v, err = %v", replies, err)
	}
	reaction := domain.Reaction{Message: want.ID, Name: "thumbsup", UserID: "U1", CreatedAt: time.Unix(102, 0).UTC()}
	if err := s.AddReaction(context.Background(), reaction, events.Event{ID: "evt_reaction", WorkspaceID: "T1", Topic: "reaction.added", Payload: "msg_1|thumbsup|U1", CreatedAt: reaction.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	reactions, _, more, err := s.ListReactions(context.Background(), want.ID, domain.PageRequest{Limit: 10})
	if err != nil || more || len(reactions) != 1 || reactions[0].Name != "thumbsup" {
		t.Fatalf("reactions=%+v more=%t err=%v", reactions, more, err)
	}
	userReactions, err := s.ListUserReactions(context.Background(), "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || userReactions.HasMore || len(userReactions.Items) != 1 || userReactions.Items[0].Message.ID != want.ID {
		t.Fatalf("user reactions=%+v err=%v", userReactions, err)
	}
	pin := domain.Pin{Message: want.ID, UserID: "U1", CreatedAt: time.Unix(103, 0).UTC()}
	if err := s.AddPin(context.Background(), pin, events.Event{ID: "evt_pin", WorkspaceID: "T1", Topic: "pin.added", Payload: "msg_1|U1", CreatedAt: pin.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	pins, _, more, err := s.ListPins(context.Background(), "C1", domain.PageRequest{Limit: 10})
	if err != nil || more || len(pins) != 1 || pins[0].Message != want.ID {
		t.Fatalf("pins=%+v more=%t err=%v", pins, more, err)
	}
	file := domain.File{ID: "file_1", WorkspaceID: "T1", Uploader: "U1", Name: "notes.txt", Title: "Notes", MIMEType: "text/plain", BlobKey: "T1/file_1", Size: 7, CreatedAt: time.Unix(104, 0).UTC()}
	if err := s.CreateFile(context.Background(), file, events.Event{ID: "evt_file", WorkspaceID: "T1", Topic: "file.created", Payload: string(file.ID), CreatedAt: file.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	files, err := s.ListFiles(context.Background(), "T1", domain.PageRequest{Limit: 10})
	if err != nil || len(files.Files) != 1 || files.Files[0].BlobKey != file.BlobKey {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	if err := s.DeleteFile(context.Background(), file.ID, events.Event{ID: "evt_file_delete", WorkspaceID: "T1", Topic: "file.deleted", Payload: string(file.ID), CreatedAt: time.Unix(105, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFile(context.Background(), file.ID); err != store.ErrNotFound {
		t.Fatalf("deleted file err=%v", err)
	}
	search, err := s.SearchMessages(context.Background(), "T1", "U1", "hello", domain.PageRequest{Limit: 10})
	if err != nil || len(search.Messages) != 1 || search.Messages[0].ID != want.ID {
		t.Fatalf("search=%+v err=%v", search, err)
	}
}

func TestSQLiteConversationUnreadCountFollowsReadCursor(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:conversation-unread?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	if err := s.CreateMessage(ctx, domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "unread", CreatedAt: created}, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].UnreadCount != 1 {
		t.Fatalf("unread page=%+v err=%v", page, err)
	}
	if err := s.SetReadCursor(ctx, domain.ReadCursor{WorkspaceID: "T1", UserID: "U1", Conversation: "C1", LastRead: domain.NewMessageTimestamp(created), UpdatedAt: time.Now().UTC()}, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "conversation.read", Payload: "C1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || page.Conversations[0].UnreadCount != 0 {
		t.Fatalf("read page=%+v err=%v", page, err)
	}
}

func TestSQLiteSessionScopeMigrationPreservesLegacyAccess(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:legacy-session?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (10, '2026-01-01T00:00:00Z')`,
		`CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL, real_name TEXT NOT NULL DEFAULT '', deleted INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE sessions (session_hash TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, expires_at TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO workspaces(id, name) VALUES ('T1', 'Test')`,
		`INSERT INTO users(id, workspace_id, name) VALUES ('U1', 'T1', 'user')`,
		`INSERT INTO sessions(session_hash, workspace_id, user_id, expires_at, revoked) VALUES (?, 'T1', 'U1', ?, 0)`,
	} {
		if statement == `INSERT INTO sessions(session_hash, workspace_id, user_id, expires_at, revoked) VALUES (?, 'T1', 'U1', ?, 0)` {
			if _, err := db.ExecContext(ctx, statement, domain.HashToken("legacy"), time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := FromDB(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	record, err := selected.LookupSession(ctx, "legacy")
	if err != nil || len(record.Scopes) != 21 || !containsScope(record.Scopes, "chat:write") || !containsScope(record.Scopes, "bookmarks:read") || !containsScope(record.Scopes, "bookmarks:write") || !containsScope(record.Scopes, "canvases:read") || !containsScope(record.Scopes, "canvases:write") || !containsScope(record.Scopes, "lists:read") || !containsScope(record.Scopes, "lists:write") {
		t.Fatalf("legacy session=%+v err=%v", record, err)
	}
	membership, err := selected.GetWorkspaceMembership(ctx, "T1", "U1")
	if err != nil || membership.Role != "member" || !membership.Active {
		t.Fatalf("legacy membership=%+v err=%v", membership, err)
	}
}

func TestSQLiteConcurrentOpenSerializesMigration(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "concurrent-migration.db")
	const replicas = 4
	start := make(chan struct{})
	results := make(chan error, replicas)
	var group sync.WaitGroup
	for i := 0; i < replicas; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			store, err := Open(context.Background(), dsn)
			if err == nil {
				err = store.Close()
			}
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLiteLegacyConversationMigrationAddsDirectKeyAfterColumn(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:legacy-conversation?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (13, '2026-01-01T00:00:00Z')`,
		`CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL, real_name TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '', status_text TEXT NOT NULL DEFAULT '', status_emoji TEXT NOT NULL DEFAULT '', image_24 TEXT NOT NULL DEFAULT '', image_32 TEXT NOT NULL DEFAULT '', image_48 TEXT NOT NULL DEFAULT '', image_72 TEXT NOT NULL DEFAULT '', image_192 TEXT NOT NULL DEFAULT '', image_512 TEXT NOT NULL DEFAULT '', image_1024 TEXT NOT NULL DEFAULT '', deleted INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE conversations (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL, is_private INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO workspaces(id, name) VALUES ('T1', 'Test')`,
		`INSERT INTO users(id, workspace_id, name) VALUES ('U1', 'T1', 'alice')`,
		`INSERT INTO conversations(id, workspace_id, name, is_private) VALUES ('C1', 'T1', 'general', 0)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := FromDB(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := selected.GetConversation(ctx, "C1")
	if err != nil || conversation.IsDirect || conversation.IsGroupDirect {
		t.Fatalf("conversation=%+v err=%v", conversation, err)
	}
}

func containsScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func TestSQLiteInviteRequestRetainsInviteParameters(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:invite-parameters?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "Admin"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	want := domain.InviteRequest{ID: "IR1", WorkspaceID: "T1", Email: "alice@example.com", RequestedBy: "U1", ChannelIDs: []domain.ConversationID{"C1"}, CustomMessage: "Welcome", RealName: "Alice Example", Resend: true, Restricted: true, GuestExpirationAt: now.Add(time.Hour), Status: domain.InviteRequestPending, CreatedAt: now}
	if err := s.CreateInviteRequest(ctx, want, events.Event{ID: "EIR1", WorkspaceID: "T1", Topic: "invite_request.created", Payload: "IR1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInviteRequest(ctx, "T1", "IR1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != want.Email || len(got.ChannelIDs) != 1 || got.ChannelIDs[0] != "C1" || got.CustomMessage != want.CustomMessage || got.RealName != want.RealName || !got.Resend || !got.Restricted || !got.GuestExpirationAt.Equal(want.GuestExpirationAt) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestSQLiteMessageUnfurlsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:message-unfurls?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	value := domain.Message{ID: "msg_unfurl", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "link", CreatedAt: now, Unfurls: map[string]string{"https://example.com": `{"title":"Example"}`}}
	if err := s.CreateMessage(ctx, value, events.Event{ID: "evt_unfurl", WorkspaceID: "T1", Topic: "message.created", Payload: string(value.ID), CreatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetMessage(ctx, value.ID)
	if err != nil || loaded.Unfurls["https://example.com"] != value.Unfurls["https://example.com"] {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestSQLiteAppApprovalSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "app-approval.db")
	first, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	if err := first.SetAppApproval(ctx, "T1", "A1", "R1", domain.AppApprovalRequested, now, events.Event{ID: "evt_app_approval", WorkspaceID: "T1", ActorID: "U1", Topic: "app.requested", Payload: "A1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	page, err := second.ListAppApprovals(ctx, "T1", domain.AppApprovalRequested, domain.PageRequest{Limit: 1})
	if err != nil || len(page.Apps) != 1 || page.Apps[0].ID != "A1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if err := second.SetAppApproval(ctx, "T1", "A1", "R1", domain.AppApprovalApproved, now.Add(time.Second), events.Event{ID: "evt_app_approved", WorkspaceID: "T1", ActorID: "U1", Topic: "app.approved", Payload: "A1", CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	page, err = second.ListAppApprovals(ctx, "T1", domain.AppApprovalApproved, domain.PageRequest{Limit: 1})
	if err != nil || len(page.Apps) != 1 || page.Apps[0].Status != domain.AppApprovalApproved {
		t.Fatalf("approved page=%+v err=%v", page, err)
	}
	records, err := second.ListEventsAfter(ctx, "T1", 0, 10)
	if err != nil || len(records) != 2 || records[1].Event.ActorID != "U1" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestSQLiteRTMConnectionIsSingleUseAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "rtm.db")
	first, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	connection := domain.RTMConnection{ID: "rtm-1", WorkspaceID: "T1", UserID: "U1", ExpiresAt: time.Now().UTC().Add(time.Minute)}
	if err := first.CreateRTMConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	value, err := second.ConsumeRTMConnection(ctx, connection.ID)
	if err != nil || value.UserID != "U1" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	if _, err := second.ConsumeRTMConnection(ctx, connection.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second consume error=%v", err)
	}
}

func TestSQLiteCommittedStateSurvivesRestart(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "sameoldchat.db")
	ctx := context.Background()
	first, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: "msg_restart", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "durable", CreatedAt: time.Unix(200, 0).UTC()}
	event := events.Event{ID: "evt_restart", WorkspaceID: "T1", Topic: "message.created", Payload: string(message.ID), CreatedAt: message.CreatedAt}
	if err := first.CreateMessage(ctx, message, event, ""); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	page, err := second.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "durable" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	var outboxCount int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE id = 'evt_restart'`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox count=%d", outboxCount)
	}
}

func TestSQLiteCreatedWorkspaceAndDomainSurviveRestart(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "workspace-create.db")
	first, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Parent"}); err != nil {
		t.Fatal(err)
	}
	value := domain.Workspace{ID: "T2", Domain: "second-workspace", Name: "Second Workspace", Description: "created", Discoverability: domain.WorkspaceDiscoverabilityOpen}
	event := events.Event{ID: "evt_workspace_create", WorkspaceID: "T2", Topic: "workspace.created", Payload: "T2", CreatedAt: time.Now().UTC()}
	if err := first.CreateWorkspace(ctx, value, event); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	loaded, err := second.GetWorkspace(ctx, "T2")
	if err != nil || loaded.Domain != value.Domain || loaded.Name != value.Name {
		t.Fatalf("workspace=%+v err=%v", loaded, err)
	}
}

func TestSQLiteAppPermissionRequestIsCommittedWithEvent(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "permission-request.db")
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	value := domain.AppPermissionRequest{ID: "R1", WorkspaceID: "T1", RequesterID: "U1", TargetUserID: "U1", Scopes: []string{"users:read", "chat:write"}, TriggerID: "trigger-1", CreatedAt: time.Unix(100, 0).UTC()}
	event := events.Event{ID: "evt_permission_request", WorkspaceID: "T1", Topic: "app.permissions_requested", Payload: "R1", CreatedAt: value.CreatedAt}
	if err := s.CreateAppPermissionRequest(ctx, value, event); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM app_permission_requests WHERE id = 'R1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("request count=%d err=%v", count, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE id = 'evt_permission_request'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("event count=%d err=%v", count, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteReadCursorSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "read-cursor.db")
	first, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	cursor := domain.ReadCursor{WorkspaceID: "T1", UserID: "U1", Conversation: "C1", LastRead: "1700000000.123456", UpdatedAt: time.Unix(300, 0).UTC()}
	event := events.Event{ID: "evt_read", WorkspaceID: "T1", Topic: "conversation.read", Payload: "C1|1700000000.123456", CreatedAt: cursor.UpdatedAt}
	if err := first.SetReadCursor(ctx, cursor, event); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	got, err := second.GetReadCursor(ctx, "T1", "U1", "C1")
	if err != nil || got.LastRead != cursor.LastRead || !got.UpdatedAt.Equal(cursor.UpdatedAt) {
		t.Fatalf("cursor=%+v err=%v", got, err)
	}
	var count int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE id = 'evt_read'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbox count=%d", count)
	}
}

func TestSQLiteOutboxLeaseAndAck(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:outbox-lease?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	if err := s.CreateMessage(ctx, domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: created}, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimEvents(ctx, "T1", "worker-1", 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	if claimed[0].Event.ID != "E1" {
		t.Fatalf("claimed event=%+v", claimed[0])
	}
	if other, err := s.ClaimEvents(ctx, "T1", "worker-2", 10, time.Minute); err != nil || len(other) != 0 {
		t.Fatalf("other claim=%v err=%v", other, err)
	}
	if err := s.RenewEvents(ctx, "worker-1", []uint64{claimed[0].Sequence}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if renewed, err := s.ClaimEvents(ctx, "T1", "worker-2", 10, time.Minute); err != nil || len(renewed) != 0 {
		t.Fatalf("renewed event was reclaimed=%v err=%v", renewed, err)
	}
	if err := s.ReleaseEvents(ctx, "worker-1", []uint64{claimed[0].Sequence}, time.Now().UTC().Add(5*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if remaining, err := s.ClaimEvents(ctx, "T1", "worker-2", 10, time.Minute); err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
	time.Sleep(10 * time.Millisecond)
	reclaimed, err := s.ClaimEvents(ctx, "T1", "worker-2", 10, time.Minute)
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("reclaimed=%v err=%v", reclaimed, err)
	}
	if err := s.AckEvents(ctx, "worker-2", []uint64{reclaimed[0].Sequence}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteAuthSeedingDoesNotOverwriteDurableState(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:auth-seed?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedToken(ctx, "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedToken(ctx, "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Revoked: true}); err != nil {
		t.Fatal(err)
	}
	token, err := s.LookupToken(ctx, "token")
	if err != nil || token.WorkspaceID != "T1" || token.Revoked {
		t.Fatalf("token=%+v err=%v", token, err)
	}
	firstExpiry := time.Now().UTC().Add(time.Hour)
	if err := s.SeedSession(ctx, "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", ExpiresAt: firstExpiry}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(ctx, "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Revoked: true}); err != nil {
		t.Fatal(err)
	}
	session, err := s.LookupSession(ctx, "session")
	if err != nil || session.WorkspaceID != "T1" || session.Revoked {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	if err := s.RevokeSession(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	session, err = s.LookupSession(ctx, "session")
	if err != nil || !session.Revoked {
		t.Fatalf("revoked session=%+v err=%v", session, err)
	}
}

func TestSQLiteBlobCleanupTopicHasSeparateLeaseStream(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:blob-topic?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	file := domain.File{ID: "file_topic", WorkspaceID: "T1", Uploader: "U1", Name: "x", Title: "x", MIMEType: "text/plain", BlobKey: "T1/x", Size: 1, CreatedAt: created}
	if err := s.CreateFile(ctx, file, events.Event{ID: "Efile", WorkspaceID: "T1", Topic: "file.created", Payload: string(file.ID), CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	createdEvent, err := s.ClaimEvents(ctx, "T1", "delivery", 10, time.Minute)
	if err != nil || len(createdEvent) != 1 {
		t.Fatalf("created events=%v err=%v", createdEvent, err)
	}
	if err := s.AckEvents(ctx, "delivery", []uint64{createdEvent[0].Sequence}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFile(ctx, file.ID, events.Event{ID: "Edelete", WorkspaceID: "T1", Topic: events.FileBlobDeleteTopic, Payload: file.BlobKey, CreatedAt: created.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if visible, err := s.ClaimEvents(ctx, "T1", "delivery-2", 10, time.Minute); err != nil || len(visible) != 0 {
		t.Fatalf("internal event was externally visible: %v err=%v", visible, err)
	}
	cleanup, err := s.ClaimEventsForTopic(ctx, "T1", events.FileBlobDeleteTopic, "cleanup", 10, time.Minute)
	if err != nil || len(cleanup) != 1 || cleanup[0].Event.Payload != file.BlobKey {
		t.Fatalf("cleanup events=%v err=%v", cleanup, err)
	}
}

func TestSQLiteWalkBlobReferencesStreamsLiveFilesAndPhotos(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:blob-references?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Profile: domain.UserProfile{Image24: "/users/T1/U1/photo/photo_1"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U2", WorkspaceID: "T1", Name: "deleted", Deleted: true, Profile: domain.UserProfile{Image24: "/users/T1/U2/photo/photo_2"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.CreateFile(ctx, domain.File{ID: "file_live", WorkspaceID: "T1", Uploader: "U1", Name: "x", Title: "x", BlobKey: "T1/file_live", Size: 1, CreatedAt: now}, events.Event{ID: "E-live", WorkspaceID: "T1", Topic: "file.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFile(ctx, domain.File{ID: "file_deleted", WorkspaceID: "T1", Uploader: "U1", Name: "y", Title: "y", BlobKey: "T1/file_deleted", Size: 1, CreatedAt: now}, events.Event{ID: "E-deleted", WorkspaceID: "T1", Topic: "file.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFile(ctx, "file_deleted", events.Event{ID: "E-delete", WorkspaceID: "T1", Topic: events.FileBlobDeleteTopic, Payload: "T1/file_deleted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var references []string
	if err := s.WalkBlobReferences(ctx, "T1", func(reference string) error {
		references = append(references, reference)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(references)
	want := []string{"T1/file_live", "T1/users/U1/photo_1"}
	if !sort.StringsAreSorted(want) || len(references) != len(want) || references[0] != want[0] || references[1] != want[1] {
		t.Fatalf("references=%v want=%v", references, want)
	}
}

func TestSQLiteLifecycleStatePersistsAndFences(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "lifecycle.db")
	firstStore, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	first, err := lifecycle.NewPersistent(firstStore)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := first.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Date(2026, time.July, 17, 5, 0, 0, 123, time.UTC)
	if err := first.SetWakeDeadline(fence, deadline); err != nil {
		t.Fatal(err)
	}
	secondStore, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	defer secondStore.Close()
	second, err := lifecycle.NewPersistent(secondStore)
	if err != nil {
		t.Fatal(err)
	}
	state, generation := second.Snapshot()
	if state != lifecycle.StateWaking || generation != fence {
		t.Fatalf("state=%s generation=%d", state, generation)
	}
	metadata := second.Metadata()
	if !metadata.WakeDeadline.Equal(deadline) {
		t.Fatalf("wake deadline=%s, want %s", metadata.WakeDeadline, deadline)
	}
	if err := second.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if err := first.Activate(fence); !errors.Is(err, lifecycle.ErrStateConflict) {
		t.Fatalf("stale controller error=%v", err)
	}
}

func TestSQLiteStaleGenerationCannotBeginHibernate(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "stale-hibernate.db")
	firstStore, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	first, err := lifecycle.NewPersistent(firstStore)
	if err != nil {
		t.Fatal(err)
	}
	wakeFence, err := first.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Activate(wakeFence); err != nil {
		t.Fatal(err)
	}
	secondStore, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	second, err := lifecycle.NewPersistent(secondStore)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.BeginHibernate(wakeFence); err != nil {
		t.Fatal(err)
	}
	if _, err := second.BeginHibernate(wakeFence); !errors.Is(err, lifecycle.ErrStateConflict) {
		t.Fatalf("stale hibernate error=%v, want state conflict", err)
	}
}

func TestSQLiteLifecycleWakeDeadlineMigration(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "legacy-lifecycle.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); CREATE TABLE lifecycle_state (id INTEGER PRIMARY KEY CHECK(id = 1), state TEXT NOT NULL, generation INTEGER NOT NULL); INSERT INTO schema_migrations(version, applied_at) VALUES (56, 'legacy'); INSERT INTO lifecycle_state(id, state, generation) VALUES (1, 'hibernated', 7)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	controller, err := lifecycle.NewPersistent(store)
	if err != nil {
		t.Fatal(err)
	}
	metadata := controller.Metadata()
	if metadata.State != lifecycle.StateHibernated || metadata.Generation != 7 || !metadata.WakeDeadline.IsZero() {
		t.Fatalf("metadata=%+v, want legacy state without deadline", metadata)
	}
}

func TestSQLiteIdempotencyReturnsCommittedMessage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:idempotency?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, seed := range []func() error{
		func() error { return s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}) },
		func() error { return s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}) },
		func() error {
			return s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
		},
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Now().UTC().Truncate(time.Microsecond)
	message := domain.Message{ID: "M-idempotent", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "first", CreatedAt: created}
	event := events.Event{ID: "E-idempotent", WorkspaceID: "T1", Topic: "message.created", Payload: string(message.ID), CreatedAt: created}
	if err := s.CreateMessage(ctx, message, event, "request-1"); err != nil {
		t.Fatal(err)
	}
	duplicate := domain.Message{ID: "M-other", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "second", CreatedAt: created.Add(time.Microsecond)}
	if err := s.CreateMessage(ctx, duplicate, events.Event{ID: "E-other", WorkspaceID: "T1", Topic: "message.created", Payload: string(duplicate.ID), CreatedAt: duplicate.CreatedAt}, "request-1"); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("duplicate error=%v", err)
	}
	got, err := s.GetIdempotentMessage(ctx, "T1", "U1", "request-1")
	if err != nil || got.ID != message.ID || got.Text != "first" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestSQLiteIncomingWebhookSecretIsHashedAndRevocable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "file:incoming-webhook?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	value := domain.IncomingWebhook{ID: "wh_1", WorkspaceID: "T1", AppID: "A1", ConversationID: "C1", UserID: "U1", SecretHash: domain.HashToken("secret"), Enabled: true, CreatedAt: time.Unix(100, 0).UTC()}
	if err := s.CreateIncomingWebhook(ctx, value); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LookupIncomingWebhook(ctx, "T1", "A1", "secret"); err != nil || got.ID != value.ID || got.SecretHash == "secret" {
		t.Fatalf("lookup=%+v err=%v", got, err)
	}
	if err := s.SetIncomingWebhookEnabled(ctx, "T1", value.ID, false, events.Event{ID: "evt_1", WorkspaceID: "T1", Topic: "incoming_webhook.disabled"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupIncomingWebhook(ctx, "T1", "A1", "secret"); err != store.ErrNotFound {
		t.Fatalf("disabled webhook error=%v", err)
	}
}

func TestSQLiteExternalUploadCompletionPersistsFileShares(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "external-upload.db")
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Truncate(time.Second)
	upload := domain.ExternalUpload{ID: "upload_1", WorkspaceID: "T1", Uploader: "U1", Name: "notes.txt", Title: "Notes", MIMEType: "text/plain", BlobKey: "T1/upload_1", Size: 7, Status: domain.ExternalUploadUploaded, CreatedAt: created, ExpiresAt: created.Add(time.Hour)}
	if err := s.CreateExternalUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}
	file := domain.File{ID: "file_1", WorkspaceID: "T1", Uploader: "U1", Name: upload.Name, Title: upload.Title, MIMEType: upload.MIMEType, BlobKey: upload.BlobKey, Size: upload.Size, CreatedAt: created}
	if err := s.CompleteExternalUpload(ctx, upload.ID, file, []domain.ConversationID{"C1"}, events.Event{ID: "file-event-1", WorkspaceID: "T1", Topic: "file.created", Payload: string(file.ID), CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetFile(ctx, file.ID)
	if err != nil || len(got.SharedChannels) != 1 || got.SharedChannels[0] != "C1" {
		t.Fatalf("file=%+v err=%v", got, err)
	}
	completed, err := s.GetExternalUpload(ctx, upload.ID)
	if err != nil || completed.Status != domain.ExternalUploadCompleted || completed.FileID != file.ID {
		t.Fatalf("upload=%+v err=%v", completed, err)
	}
}

func TestSQLiteExternalUploadBatchCompletionIsAtomicAndDurable(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "external-upload-batch.db")
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Truncate(time.Second)
	uploads := []domain.ExternalUpload{
		{ID: "upload_1", WorkspaceID: "T1", Uploader: "U1", Name: "one.txt", Title: "One", MIMEType: "text/plain", BlobKey: "T1/upload_1", Size: 3, Status: domain.ExternalUploadUploaded, CreatedAt: created, ExpiresAt: created.Add(time.Hour)},
		{ID: "upload_2", WorkspaceID: "T1", Uploader: "U1", Name: "two.txt", Title: "Two", MIMEType: "text/plain", BlobKey: "T1/upload_2", Size: 3, Status: domain.ExternalUploadUploaded, CreatedAt: created, ExpiresAt: created.Add(time.Hour)},
	}
	for _, upload := range uploads {
		if err := s.CreateExternalUpload(ctx, upload); err != nil {
			t.Fatal(err)
		}
	}
	files := []domain.File{
		{ID: "file_1", WorkspaceID: "T1", Uploader: "U1", Name: "one.txt", Title: "First", MIMEType: "text/plain", BlobKey: uploads[0].BlobKey, Size: 3, CreatedAt: created, SharedChannels: []domain.ConversationID{"C1"}},
		{ID: "file_2", WorkspaceID: "T1", Uploader: "U1", Name: "two.txt", Title: "Second", MIMEType: "text/plain", BlobKey: uploads[1].BlobKey, Size: 3, CreatedAt: created, SharedChannels: []domain.ConversationID{"C1"}},
	}
	completions := []domain.ExternalUploadCompletion{{ID: uploads[0].ID, Title: files[0].Title}, {ID: uploads[1].ID, Title: files[1].Title}}
	emitted := []events.Event{{ID: "file-event-1", WorkspaceID: "T1", Topic: "file.created", Payload: "file_1", CreatedAt: created}, {ID: "file-event-2", WorkspaceID: "T1", Topic: "file.created", Payload: "file_2", CreatedAt: created}}
	if err := s.CompleteExternalUploads(ctx, completions, files, []domain.ConversationID{"C1"}, emitted); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for index, expected := range files {
		got, err := s.GetFile(ctx, expected.ID)
		if err != nil || got.Title != expected.Title || len(got.SharedChannels) != 1 || got.SharedChannels[0] != "C1" {
			t.Fatalf("file %d=%+v err=%v", index, got, err)
		}
		completed, err := s.GetExternalUpload(ctx, uploads[index].ID)
		if err != nil || completed.Status != domain.ExternalUploadCompleted || completed.FileID != expected.ID {
			t.Fatalf("upload %d=%+v err=%v", index, completed, err)
		}
	}
}
