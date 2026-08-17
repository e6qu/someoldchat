package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
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

// seedWorkspaceAdmin promotes a seeded user to workspace administrator.
//
// memory.Store.SeedUser creates a MEMBER membership, and every administrative
// method now requires requireWorkspaceAdmin, so a fixture that drives an
// administrative operation has to state the authority it claims. These fixtures
// previously passed a plain member and succeeded, which documented the defect
// rather than the contract: a member could rename the workspace, approve an app
// or set any user's role.
func seedWorkspaceAdmin(t *testing.T, s *memory.Store, workspaceID domain.WorkspaceID, userID domain.UserID) {
	t.Helper()
	event, err := events.New(domain.EventID("evt_seed_admin_"+string(userID)), workspaceID, "", events.NewPayload("workspace.role_changed", events.String("user_id", string(userID)), events.String("role", string(domain.WorkspaceRoleAdmin))), time.Now().UTC())
	if err != nil {
		t.Fatalf("build role event: %v", err)
	}
	if err := s.SetWorkspaceRole(context.Background(), workspaceID, userID, domain.WorkspaceRoleAdmin, event); err != nil {
		t.Fatalf("promote %s to workspace administrator: %v", userID, err)
	}
}

func TestPostMessageRejectsForeignUser(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T2"})
	_, err := (Messages{Store: s}).Post(context.Background(), "T1", "U1", "C1", "hello", "", "")
	if err == nil {
		t.Fatal("Post returned nil error for foreign user")
	}
}

func TestGuestCanCreatePersonalButNotChannelReminder(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	guest := domain.User{ID: "UG", WorkspaceID: "T1", Email: "guest@example.com", Name: "guest"}
	membership := domain.WorkspaceMembership{
		WorkspaceID: "T1", UserID: guest.ID, Role: domain.WorkspaceRoleMember,
		Active: true, Restricted: true,
	}
	if err := s.CreateUser(ctx, guest, membership, events.Event{ID: "E-guest", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("C1", guest.ID); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}
	due := time.Now().UTC().Add(time.Hour)
	if _, err := messages.CreateLaterReminder(ctx, "T1", guest.ID, domain.LaterReminderRequest{
		Target: domain.LaterReminderPersonal, Text: "private", DueAt: due, TimeZone: "UTC",
	}); err != nil {
		t.Fatalf("personal reminder: %v", err)
	}
	if _, err := messages.CreateLaterReminder(ctx, "T1", guest.ID, domain.LaterReminderRequest{
		Target: domain.LaterReminderChannel, Channel: "C1", Text: "public", DueAt: due, TimeZone: "UTC",
	}); !errors.Is(err, ErrInvalidLaterReminder) {
		t.Fatalf("channel reminder error=%v, want %v", err, ErrInvalidLaterReminder)
	}
}

// TestCompleteReminderDistinguishesOthersAndRecurring covers reminders.complete's
// three refusals, which a user-scoped store lookup alone collapses into one
// not_found: another member's reminder is cannot_complete_others, a recurring
// reminder is cannot_complete_recurring, and only a genuinely absent reminder is
// not_found. A member's own one-off still completes.
func TestCompleteReminderDistinguishesOthersAndRecurring(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}
	due := time.Now().UTC().Add(time.Hour)

	// A member's own one-off reminder completes.
	own, err := messages.AddReminder(ctx, "T1", "U1", "U1", "water the plants", due)
	if err != nil {
		t.Fatalf("add own reminder: %v", err)
	}
	if err := messages.CompleteReminder(ctx, "T1", "U1", own.ID); err != nil {
		t.Fatalf("complete own reminder: %v", err)
	}

	// A reminder U1 sets for U2 belongs to U2; U1 cannot complete it.
	forOther, err := messages.AddReminder(ctx, "T1", "U1", "U2", "call the vet", due)
	if err != nil {
		t.Fatalf("add reminder for U2: %v", err)
	}
	if err := messages.CompleteReminder(ctx, "T1", "U1", forOther.ID); !errors.Is(err, ErrReminderOwnedByOther) {
		t.Fatalf("complete other's reminder error = %v, want %v", err, ErrReminderOwnedByOther)
	}
	// The refusal did not complete it: U2 can still see it as outstanding.
	if outstanding, infoErr := messages.ReminderInfo(ctx, "T1", "U2", forOther.ID); infoErr != nil || !outstanding.CompleteAt.IsZero() {
		t.Fatalf("U2 reminder after refusal: complete=%v err=%v", outstanding.CompleteAt, infoErr)
	}

	// A recurring reminder cannot be marked complete. AddReminder never mints one,
	// so it is written directly — the store carries the recurring flag and column.
	recurring := domain.Reminder{WorkspaceID: "T1", ID: "Rm-standup", Creator: "U1", User: "U1", Text: "daily standup", Time: due, Recurring: true}
	if err := s.CreateReminder(ctx, recurring, events.Event{ID: "E-recur", WorkspaceID: "T1", Topic: "reminder.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("seed recurring reminder: %v", err)
	}
	if err := messages.CompleteReminder(ctx, "T1", "U1", recurring.ID); !errors.Is(err, ErrReminderRecurring) {
		t.Fatalf("complete recurring reminder error = %v, want %v", err, ErrReminderRecurring)
	}

	// A reminder that does not exist is still not_found, not one of the above.
	if err := messages.CompleteReminder(ctx, "T1", "U1", "Rm-nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("complete missing reminder error = %v, want %v", err, store.ErrNotFound)
	}
}

func TestPostMessageRejectsArchivedConversation(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "archive", Archived: true})
	s.SeedConversationMember("C1", "U1")

	if _, err := (Messages{Store: s}).Post(context.Background(), "T1", "U1", "C1", "hello", "", ""); !errors.Is(err, ErrConversationAlreadyArchived) {
		t.Fatalf("Post error = %v, want %v", err, ErrConversationAlreadyArchived)
	}
	messages, err := s.ListMessages(context.Background(), "C1", domain.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Messages) != 0 {
		t.Fatalf("archived conversation persisted %d messages", len(messages.Messages))
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

func TestOAuthV2ExchangeIssuesBotIdentityAndScopes(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "Uinstaller", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "Ubot", WorkspaceID: "T1"})
	ctx := context.Background()
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "Ubot", Name: "app", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthCode(ctx, domain.OAuthCode{
		Code:        "code",
		ClientID:    "client",
		WorkspaceID: "T1",
		UserID:      "Uinstaller",
		UserScopes:  []string{"users:read"},
		BotID:       "B1",
		BotUserID:   "Ubot",
		BotScopes:   []string{"chat:write"},
		RedirectURI: "https://callback",
	}); err != nil {
		t.Fatal(err)
	}
	token, err := (Messages{Store: s, AppCredentialKey: []byte("0123456789abcdef0123456789abcdef")}).OAuthV2Exchange(ctx, "client", "secret", "code", "https://callback", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token.AccessToken, "xoxb-") || !strings.HasPrefix(token.AuthedUserAccessToken, "xoxp-") || token.TokenType != "bot" || token.UserID != "Ubot" || token.InstallerID != "Uinstaller" || token.BotID != "B1" || strings.Join(token.Scopes, " ") != "chat:write" || strings.Join(token.AuthedUserScopes, " ") != "users:read" {
		t.Fatalf("unexpected token: %+v", token)
	}
	issued, err := s.LookupToken(ctx, token.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if issued.AppID != "A1" || issued.BotID != "B1" || issued.UserID != "Ubot" || issued.TokenType != "bot" || strings.Join(issued.Scopes, " ") != "chat:write" {
		t.Fatalf("unexpected durable token: %+v", issued)
	}
	installerToken, err := s.LookupToken(ctx, token.AuthedUserAccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if installerToken.AppID != "A1" || installerToken.BotID != "" || installerToken.UserID != "Uinstaller" || installerToken.TokenType != "user" || strings.Join(installerToken.Scopes, " ") != "users:read" {
		t.Fatalf("unexpected durable installer token: %+v", installerToken)
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

// Every event this package emits needs a typed payload that journal consumers
// can inspect safely. Audience consumers must reject recipient-scoped records;
// Slack delivery may also deliberately withhold a typed record when no safe,
// complete Slack shape exists for its topic.
func TestEmittedEventsAreTypedAndRespectAudienceBoundaries(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	ctx := context.Background()
	messages := Messages{Store: s}
	message, err := messages.Post(ctx, "T1", "U1", "C1", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	if _, err := messages.Update(ctx, "T1", "U1", "C1", timestamp, "hello again"); err != nil {
		t.Fatal(err)
	}
	if err := messages.AddReaction(ctx, "T1", "U1", "C1", timestamp, "tada"); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.MarkRead(ctx, "T1", "U1", "C1", timestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.PostEphemeral(ctx, "T1", "U1", "C1", "U1", "just for you"); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListEventsAfter(ctx, "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 5 {
		t.Fatalf("records=%d, want the events of every call above", len(records))
	}
	sawMessageCreated := false
	for _, record := range records {
		delivered, err := events.Deliverable(record.Event)
		if err != nil {
			t.Fatalf("topic %q payload %q is not deliverable: %v", record.Event.Topic, record.Event.Payload, err)
		}
		if delivered.Type != record.Event.Topic {
			t.Fatalf("topic %q payload type %q", record.Event.Topic, delivered.Type)
		}
		if events.RecipientScoped(record.Event.Topic) {
			// The record is addressed to one user, so no audience consumer may
			// receive it: its payload carries that user's message text.
			if _, err := events.SlackEventBodies(record, "A1"); !errors.Is(err, events.ErrPayloadRecipientScoped) {
				t.Fatalf("topic %q was offered to an audience consumer: %v", record.Event.Topic, err)
			}
			if recipient, ok := delivered.Field("user_id"); !ok || recipient != "U1" {
				t.Fatalf("topic %q recipient=%q ok=%v", record.Event.Topic, recipient, ok)
			}
			continue
		}
		if _, err := events.SlackEventBodies(record, "A1"); err != nil {
			t.Fatalf("topic %q cannot be evaluated for Slack delivery: %v", record.Event.Topic, err)
		}
		if strings.Contains(record.Event.Payload, "just for you") {
			t.Fatalf("topic %q carries ephemeral message text: %s", record.Event.Topic, record.Event.Payload)
		}
		if record.Event.Topic != "message.created" {
			continue
		}
		sawMessageCreated = true
		if value, ok := delivered.Field("message_id"); !ok || value != string(message.ID) {
			t.Fatalf("message.created message_id=%q ok=%v", value, ok)
		}
		if value, ok := delivered.Field("channel_id"); !ok || value != "C1" {
			t.Fatalf("message.created channel_id=%q ok=%v", value, ok)
		}
		if strings.Contains(record.Event.Payload, "hello") {
			t.Fatalf("message.created payload carries the message text: %s", record.Event.Payload)
		}
	}
	if !sawMessageCreated {
		t.Fatal("no message.created record was emitted")
	}
}

func TestIntegrationLogsRequireAuthoritativeActorEvents(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	// team.integrationLogs is an administrative read of who installed and
	// approved every app, so the fixture has to state the authority it claims.
	seedWorkspaceAdmin(t, s, "T1", "U1")
	ctx := context.Background()
	// The approval event is built the way the service builds it: an app
	// identifier is a payload field, never the whole payload.
	approval, err := events.New("EAPP1", "T1", "U1", events.NewPayload("app.approved", events.String("app_id", "A1")), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAppApproval(ctx, "T1", "A1", "R1", domain.AppApprovalApproved, time.Now().UTC(), approval); err != nil {
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	seedInteractionTrigger(t, s, "trigger-1")
	seedInteractionTrigger(t, s, "trigger-2")
	messages := Messages{Store: s}
	ctx := context.Background()
	opened, err := messages.OpenView(ctx, "T1", "U1", "A1", "trigger-1", `{"type":"modal","title":{"type":"plain_text","text":"First"},"blocks":[]}`)
	if err != nil || opened.RootViewID != opened.ID || opened.Hash == "" {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
	pushed, err := messages.PushView(ctx, "T1", "U1", "A1", "trigger-2", `{"type":"modal","title":{"type":"plain_text","text":"Second"},"blocks":[]}`)
	if err != nil || pushed.RootViewID != opened.RootViewID || pushed.PreviousViewID != opened.ID {
		t.Fatalf("pushed=%+v err=%v", pushed, err)
	}
	updated, err := messages.UpdateView(ctx, "T1", "U1", "A1", string(opened.ID), "", `{"type":"modal","title":{"type":"plain_text","text":"Updated"},"blocks":[]}`, opened.Hash)
	if err != nil || updated.Hash == opened.Hash {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := messages.UpdateView(ctx, "T1", "U1", "A1", string(opened.ID), "", `{"type":"modal"}`, opened.Hash); err == nil {
		t.Fatal("stale view hash unexpectedly succeeded")
	}
	if _, err := messages.OpenView(ctx, "T1", "U1", "A1", "trigger-1", `{"type":"modal"}`); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("replayed trigger error=%v, want %v", err, ErrInvalidTrigger)
	}
}

func TestViewsAreOwnedByTheirApp(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	seedHomeApp(t, s, "A1")
	seedHomeApp(t, s, "A2")
	messages := Messages{Store: s}
	ctx := context.Background()

	first, err := messages.PublishView(ctx, "T1", "U1", "A1", "U1", `{"type":"home","external_id":"home-a1","blocks":[]}`, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := messages.PublishView(ctx, "T1", "U1", "A2", "U1", `{"type":"home","external_id":"home-a2","blocks":[]}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.AppID != "A1" || second.AppID != "A2" || first.ID == second.ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if _, err := messages.UpdateView(ctx, "T1", "U1", "A2", string(first.ID), "", `{"type":"home","blocks":[]}`, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-app update error=%v, want not found", err)
	}
	if _, err := messages.UpdateView(ctx, "T1", "U1", "A2", "", "home-a1", `{"type":"home","blocks":[]}`, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-app external-id update error=%v, want not found", err)
	}
	if _, err := messages.PublishView(ctx, "T1", "U1", "A2", "U1", `{"type":"home","external_id":"home-a1","blocks":[]}`, ""); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("workspace-duplicate external_id error=%v, want already exists", err)
	}
}

func seedHomeApp(t *testing.T, s *memory.Store, appID domain.AppID) {
	t.Helper()
	now := time.Now().UTC()
	clientID := "client-" + string(appID)
	if err := s.CreateApp(context.Background(), domain.App{
		ID: appID, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: string(appID), ClientID: clientID,
		SigningSecretHash: "signing-" + string(appID), SigningSecretCiphertext: "signing-cipher-" + string(appID),
		VerificationTokenHash: "verification-" + string(appID), VerificationTokenCiphertext: "verification-cipher-" + string(appID),
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: appID, Version: 1,
		Manifest:  `{"display_information":{"name":"` + string(appID) + `"},"features":{"app_home":{"home_tab_enabled":true}}}`,
		CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: clientID, SecretHash: "client-secret-" + string(appID), AppID: appID}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAppInstallation(context.Background(), domain.AppInstallation{
		AppID: appID, WorkspaceID: "T1", Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
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
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	seedInteractionTrigger(t, s, "trigger-1")
	seedInteractionTrigger(t, s, "trigger-2")
	messages := Messages{Store: s}
	if err := messages.OpenDialog(context.Background(), "T1", "U1", "A1", "trigger-1", `{"callback_id":"callback","title":"Title","elements":[{"type":"text"}]}`); err != nil {
		t.Fatal(err)
	}
	if err := messages.OpenDialog(context.Background(), "T1", "U1", "A1", "trigger-2", `{"callback_id":"callback","title":"Title"}`); err != ErrInvalidDialog {
		t.Fatalf("invalid dialog err=%v", err)
	}
}

func seedInteractionTrigger(t *testing.T, s *memory.Store, plaintext string) {
	t.Helper()
	now := time.Now().UTC()
	err := s.CreateAppInteractionCapabilities(context.Background(),
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

// This fixture used to associate a T1 channel with the unrelated workspace T2
// and assert that it worked, which recorded the defect as the contract: the
// operation tested only that the named workspace EXISTED, so it wrote a
// cross-tenant row and answered an absent id differently from a foreign one.
// The association is now the actor's own workspace, and the foreign id is
// refused.
func TestConversationTeamsAreDurableAndDisconnectable(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "one"})
	s.SeedWorkspace(domain.Workspace{ID: "T2", Name: "two"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	seedWorkspaceAdmin(t, s, "T1", "U1")
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	messages := Messages{Store: s}
	if err := messages.AdminSetConversationTeams(context.Background(), "T1", "U1", "C1", []domain.WorkspaceID{"T1", "T2"}, false); !errors.Is(err, ErrInvalidConversation) {
		t.Fatalf("association with the unrelated workspace T2: err=%v, want ErrInvalidConversation", err)
	}
	if err := messages.AdminSetConversationTeams(context.Background(), "T1", "U1", "C1", []domain.WorkspaceID{"T1"}, false); err != nil {
		t.Fatal(err)
	}
	teams, _, err := s.ListConversationTeams(context.Background(), "T1", "C1")
	if err != nil || len(teams) != 1 || teams[0] != "T1" {
		t.Fatalf("teams=%v err=%v", teams, err)
	}
	if err := messages.AdminDisconnectSharedConversation(context.Background(), "T1", "U1", "C1", []domain.WorkspaceID{"T1"}); err != nil {
		t.Fatal(err)
	}
	teams, _, err = s.ListConversationTeams(context.Background(), "T1", "C1")
	if err != nil || len(teams) != 0 {
		t.Fatalf("after disconnect teams=%v err=%v", teams, err)
	}
}

func TestResetUserSessionsRevokesEveryTargetSession(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "channel"})
	if _, err := (Messages{Store: s}).AdminInviteConversationMembers(context.Background(), "T1", "U1", "C1", []domain.UserID{"U2", "U2"}); err != nil {
		t.Fatal(err)
	}
	member, err := s.IsConversationMember(context.Background(), "C1", "U2")
	if err != nil || !member {
		t.Fatalf("member=%v err=%v", member, err)
	}
}

func TestConversationInviteSupportsPrivateChannelsAndCreatesActivity(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
	s.SeedConversationMember("C1", "U1")
	if _, err := (Messages{Store: s}).InviteConversationMembers(context.Background(), "T1", "U1", "C1", []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListActivity(context.Background(), "T1", "U2", domain.ActivityQuery{
		Kinds: []domain.ActivityKind{domain.ActivityInvitation}, Page: domain.PageRequest{Limit: 10},
	})
	if err != nil || len(page.Items) != 1 || !page.Items[0].SourceAvailable || page.Items[0].ActorID != "U1" {
		t.Fatalf("private invitation activity=%+v err=%v", page, err)
	}
}

func TestConversationInviteIsAtomicUnlessForced(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "channel"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	messages := Messages{Store: s}

	result, err := messages.inviteConversationMembersWithOptions(context.Background(), "T1", "U1", "C1", []domain.UserID{"U2", "U-missing", "U1", "U3"}, false)
	if err != nil || result.InvitedCount != 0 || len(result.Failures) != 3 {
		t.Fatalf("default result=%+v err=%v", result, err)
	}
	if member, err := s.IsConversationMember(context.Background(), "C1", "U3"); err != nil || member {
		t.Fatalf("default U3 member=%v err=%v", member, err)
	}

	result, err = messages.inviteConversationMembersWithOptions(context.Background(), "T1", "U1", "C1", []domain.UserID{"U-missing", "U3"}, true)
	if err != nil || result.InvitedCount != 1 || len(result.Failures) != 1 || result.Failures[0].Reason != conversationInviteUserNotFound {
		t.Fatalf("forced result=%+v err=%v", result, err)
	}
	if member, err := s.IsConversationMember(context.Background(), "C1", "U3"); err != nil || !member {
		t.Fatalf("forced U3 member=%v err=%v", member, err)
	}
}

func TestAdminConversationConversionEnforcesConversationType(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	seedWorkspaceAdmin(t, s, "T1", "U1")
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "public"})
	messages := Messages{Store: s}
	value, err := messages.AdminConvertConversationToPrivate(context.Background(), "T1", "U1", "C1")
	if err != nil || !value.PrivateFlag() {
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	messages := Messages{Store: s}
	value, err := messages.AdminSetConversationPrefs(context.Background(), "T1", "U1", "C1", domain.ConversationPrefs{
		CanThread:  domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{" regular_members ", "regular_members"}, Users: []domain.UserID{"U2"}},
		WhoCanPost: domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{"admins"}, Users: []domain.UserID{"U2", "U2"}},
	})
	if err != nil || len(value.CanThread.Types) != 1 || value.CanThread.Types[0] != domain.ConversationPosterRegularMembers || len(value.WhoCanPost.Users) != 1 {
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	if _, err := messages.AdminListApps(ctx, "T1", "U1", domain.AppApprovalApproved, domain.PageRequest{Limit: 0}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("invalid page error=%v", err)
	}
}

func TestCustomEmojiLifecycleNormalizesAndPersists(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	seedWorkspaceAdmin(t, s, "T1", "U1")
	messages := Messages{Store: s}
	ctx := context.Background()
	for _, invalid := range []struct {
		name string
		url  string
	}{
		{name: "bad name", url: "https://cdn.example/bad.png"},
		{name: "javascript", url: "javascript:alert(1)"},
		{name: "relative", url: "/emoji.png"},
	} {
		if err := messages.AdminAddEmoji(ctx, "T1", "U1", invalid.name, invalid.url); !errors.Is(err, ErrInvalidEmoji) {
			t.Fatalf("AdminAddEmoji(%q, %q) error=%v", invalid.name, invalid.url, err)
		}
	}
	// A custom emoji may not shadow a built-in one: Slack refuses a custom
	// ":smile:", and the built-in set is the catalog this product already ships.
	// The refusal reports the name as taken, on every write path — creation, an
	// alias, and a rename onto the shadowed name.
	if err := messages.AdminAddEmoji(ctx, "T1", "U1", "smile", "https://cdn.example/smile.png"); !errors.Is(err, ErrEmojiAlreadyExists) {
		t.Fatalf("AdminAddEmoji(smile) error=%v, want ErrEmojiAlreadyExists", err)
	}
	if err := messages.AdminAddEmoji(ctx, "T1", "U1", " Shipit ", "https://cdn.example/shipit.png"); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminAddEmojiAlias(ctx, "T1", "U1", "joy", "SHIPIT"); !errors.Is(err, ErrEmojiAlreadyExists) {
		t.Fatalf("AdminAddEmojiAlias(joy) error=%v, want ErrEmojiAlreadyExists", err)
	}
	if err := messages.AdminAddEmojiAlias(ctx, "T1", "U1", "hello", "SHIPIT"); err != nil {
		t.Fatal(err)
	}
	values, err := messages.Emojis(ctx, "T1", "U1")
	if err != nil || len(values) != 2 {
		t.Fatalf("values=%+v err=%v", values, err)
	}
	if err := messages.AdminRenameEmoji(ctx, "T1", "U1", "hello", "grin"); !errors.Is(err, ErrEmojiAlreadyExists) {
		t.Fatalf("AdminRenameEmoji(hello->grin) error=%v, want ErrEmojiAlreadyExists", err)
	}
	if err := messages.AdminRenameEmoji(ctx, "T1", "U1", "hello", "greeting"); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminRemoveEmoji(ctx, "T1", "U1", "shipit"); err != nil {
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	// A user group decides who may open a private channel restricted to it, so
	// rewriting one is an administrative operation.
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	seedWorkspaceAdmin(t, s, "T1", "U1")
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
	// The actor is the administrator the listing is expected to return: reading the
	// administrators of a workspace is itself an administrative read, and promoting
	// a second user to supply the actor would change the result being asserted.
	if err := s.SetWorkspaceRole(context.Background(), "T1", "U1", domain.WorkspaceRoleAdmin, events.Event{ID: "evt_role", WorkspaceID: "T1", Topic: "test", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	page, err := (Messages{Store: s}).AdminTeamUsers(context.Background(), "T1", "U1", domain.WorkspaceRoleAdmin, domain.PageRequest{Limit: 10})
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

// testImageBytes is a byte stream http.DetectContentType identifies as a PNG:
// the eight-byte signature the format itself defines, plus a marker so two
// fixtures can be told apart. A profile photo fixture has to be a real image
// now, because SetUserPhoto refuses a stream that is not the type it claims.
func testImageBytes(marker string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), marker...)
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
	photo := testImageBytes("photo")
	user, err := messages.SetUserPhoto(context.Background(), "T1", "U1", "image/png", int64(len(photo)), bytes.NewReader(photo))
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
	if err != nil || string(content) != string(photo) {
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

func (s failingProfileStore) UpdateUserProfile(context.Context, domain.WorkspaceID, domain.UserID, domain.UserProfile, ...events.Event) (domain.User, error) {
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
	photo := testImageBytes("photo")
	_, err := messages.SetUserPhoto(context.Background(), "T1", "U1", "image/png", int64(len(photo)), bytes.NewReader(photo))
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
	if value.ID == "" || value.CreatedAt.IsZero() {
		t.Fatalf("ephemeral identity=%+v", value)
	}
	visible, err := (Messages{Store: s}).ListEphemeralMessages(context.Background(), "T1", "U2", "C1", 10)
	if err != nil || len(visible) != 1 || visible[0].ID != value.ID {
		t.Fatalf("recipient ephemerals=%+v err=%v", visible, err)
	}
	hidden, err := (Messages{Store: s}).ListEphemeralMessages(context.Background(), "T1", "U1", "C1", 10)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("non-recipient ephemerals=%+v err=%v", hidden, err)
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
	s.SeedConversationMember("C1", "U1")
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
	s.SeedConversationMember("C1", "U1")
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
	s.SeedConversation(domain.Conversation{ID: "Cprivate", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
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
	s.SeedConversationMember("C1", "U1")
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
	s.SeedConversationMember("C1", "U1")
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
	s.SeedConversationMember("C1", "U1")
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
	s.SeedConversationMember("C1", "U1")
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
	s.SeedConversationMember("C1", "U1")
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
	if err := messages.AddReaction(context.Background(), "T1", "U1", "C1", timestamp, "not_a_real_emoji"); !errors.Is(err, ErrInvalidReaction) {
		t.Fatalf("unknown reaction err=%v", err)
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
	s.SeedConversationMember("C1", "U1")
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
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
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
	s.SeedConversationMember("C1", "U1")
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
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

func TestRecentSearchesRequireMembershipAndRemainPrivate(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}
	if err := messages.RecordSearch(ctx, "T1", "U1", "  deployment plan  "); err != nil {
		t.Fatal(err)
	}
	if err := messages.RecordSearch(ctx, "T1", "U2", "private query"); err != nil {
		t.Fatal(err)
	}
	values, err := messages.RecentSearches(ctx, "T1", "U1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Query != "deployment plan" {
		t.Fatalf("recent searches = %+v", values)
	}
	if err := messages.RecordSearch(ctx, "T1", "missing", "query"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign user error = %v, want not found", err)
	}
	if err := messages.RecordSearch(ctx, "T1", "U1", " "); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("blank query error = %v, want invalid search", err)
	}
	if _, err := messages.RecentSearches(ctx, "T1", "U1", 0); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("invalid limit error = %v, want invalid search", err)
	}
}

func TestSearchResolvesSlackModifiersAndPreservesDeterministicOrder(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", RealName: "Bob Example"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	for _, message := range []domain.Message{
		{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U2", Text: "Release candidate ready", CreatedAt: time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC)},
		{ID: "M2", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U2", Text: "Release draft", CreatedAt: time.Date(2025, 1, 6, 12, 0, 0, 0, time.UTC)},
		{ID: "M3", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "Release candidate ready", CreatedAt: time.Date(2025, 1, 7, 12, 0, 0, 0, time.UTC)},
	} {
		if err := s.CreateMessage(ctx, message, events.Event{ID: domain.EventID("E" + string(message.ID)), WorkspaceID: "T1", Topic: "message.created", CreatedAt: message.CreatedAt}, ""); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: s}
	page, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{
		Query: `"release candidate" -draft from:@bob in:#general after:2025-01-01 before:2025-02-01`,
		Sort:  domain.SearchSortTimestamp, Direction: domain.SearchDirectionDescending,
		Page: domain.PageRequest{Limit: 10},
	})
	if err != nil || page.Total != 1 || len(page.Messages) != 1 || page.Messages[0].ID != "M1" {
		t.Fatalf("search=%+v err=%v", page, err)
	}
	if _, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{Query: "release before:not-a-date", Page: domain.PageRequest{Limit: 10}}); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("malformed modifier err=%v, want ErrInvalidSearch", err)
	}
}

// TestSearchFromMeResolvesToTheSearcher covers Slack's from:me, which names
// whoever is searching. It used to resolve to no member and return nothing.
func TestSearchFromMeResolvesToTheSearcher(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	for _, message := range []domain.Message{
		{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "shared note", CreatedAt: time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC)},
		{ID: "M2", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U2", Text: "shared note", CreatedAt: time.Date(2025, 1, 6, 12, 0, 0, 0, time.UTC)},
	} {
		if err := s.CreateMessage(ctx, message, events.Event{ID: domain.EventID("E" + string(message.ID)), WorkspaceID: "T1", Topic: "message.created", CreatedAt: message.CreatedAt}, ""); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: s}
	mine, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{Query: "note from:me", Page: domain.PageRequest{Limit: 10}})
	if err != nil || mine.Total != 1 || mine.Messages[0].ID != "M1" {
		t.Fatalf("from:me for U1 = %+v err=%v, want M1", mine, err)
	}
	excluded, err := messages.SearchMessages(ctx, "T1", "U1", domain.MessageSearchRequest{Query: "note -from:me", Page: domain.PageRequest{Limit: 10}})
	if err != nil || excluded.Total != 1 || excluded.Messages[0].ID != "M2" {
		t.Fatalf("-from:me for U1 = %+v err=%v, want M2", excluded, err)
	}
	theirs, err := messages.SearchMessages(ctx, "T1", "U2", domain.MessageSearchRequest{Query: "note from:me", Page: domain.PageRequest{Limit: 10}})
	if err != nil || theirs.Total != 1 || theirs.Messages[0].ID != "M2" {
		t.Fatalf("from:me for U2 = %+v err=%v, want M2", theirs, err)
	}
}

func TestFileListingAndSearchApplyViewerVisibilityBeforePagination(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	s.SeedConversation(domain.Conversation{ID: "Cpublic", WorkspaceID: "T1", Name: "general"})
	s.SeedConversation(domain.Conversation{ID: "Cprivate", WorkspaceID: "T1", Name: "secret", Kind: domain.ConversationTypePrivate})
	s.SeedConversationMember("Cprivate", "U2")
	files := []domain.File{
		{ID: "FOWN", WorkspaceID: "T1", Uploader: "U1", Name: "release-notes.txt", Title: "Release notes", MIMEType: "text/plain", BlobKey: "own", CreatedAt: time.Unix(10, 0).UTC()},
		{ID: "FPUBLIC", WorkspaceID: "T1", Uploader: "U2", Name: "release-plan.pdf", Title: "Release plan", MIMEType: "application/pdf", BlobKey: "public", SharedChannels: []domain.ConversationID{"Cpublic"}, CreatedAt: time.Unix(20, 0).UTC()},
		{ID: "FPRIVATE", WorkspaceID: "T1", Uploader: "U2", Name: "release-secret.pdf", Title: "Release secret", MIMEType: "application/pdf", BlobKey: "private", SharedChannels: []domain.ConversationID{"Cprivate"}, CreatedAt: time.Unix(30, 0).UTC()},
	}
	for _, file := range files {
		if err := s.CreateFile(ctx, file, events.Event{ID: domain.EventID("E" + string(file.ID)), WorkspaceID: "T1", Topic: "file.created", CreatedAt: file.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: s}
	listed, err := messages.Files(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(listed.Files) != 2 {
		t.Fatalf("visible files=%+v err=%v", listed, err)
	}
	public, err := messages.FileInfo(ctx, "T1", "U1", "FPUBLIC")
	if err != nil || public.ID != "FPUBLIC" {
		t.Fatalf("public file info=%+v err=%v", public, err)
	}
	if _, err := messages.FileInfo(ctx, "T1", "U1", "FPRIVATE"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("private file info err=%v, want not found", err)
	}
	found, err := messages.SearchFiles(ctx, "T1", "U1", domain.FileSearchRequest{
		Query: "release type:pdf -from:@alice -in:#secret", Sort: domain.SearchSortTimestamp, Direction: domain.SearchDirectionDescending, Count: 1, Page: 1,
	})
	if err != nil || found.Total != 1 || len(found.Files) != 1 || found.Files[0].ID != "FPUBLIC" || found.HasMore {
		t.Fatalf("file search=%+v err=%v", found, err)
	}
	scoped, err := messages.SearchFiles(ctx, "T1", "U1", domain.FileSearchRequest{
		Query: "release", Conversation: "Cpublic", Count: 10, Page: 1,
	})
	if err != nil || scoped.Total != 1 || len(scoped.Files) != 1 || scoped.Files[0].ID != "FPUBLIC" {
		t.Fatalf("conversation file search=%+v err=%v", scoped, err)
	}
	excluded, err := messages.SearchFiles(ctx, "T1", "U1", domain.FileSearchRequest{
		Query: "release -from:@bob -in:#general", Count: 10, Page: 1,
	})
	if err != nil || excluded.Total != 1 || len(excluded.Files) != 1 || excluded.Files[0].ID != "FOWN" {
		t.Fatalf("excluded file search=%+v err=%v", excluded, err)
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
	if _, err := messages.SetUserProfile(context.Background(), "T1", "U1", domain.UserProfile{StatusText: "Unknown emoji", StatusEmoji: ":not_a_workspace_emoji:"}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("unknown status emoji err=%v", err)
	}
}

func TestScheduledStatusesFollowSlackCreateEditCancelAndFiveItemContracts(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	messages := Messages{Store: s}
	start := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	first, err := messages.ScheduleUserStatus(ctx, "T1", "U1", " Focus time ", " :dart: ", start, start.Add(time.Hour))
	if err != nil || first.StatusText != "Focus time" || first.StatusEmoji != ":dart:" {
		t.Fatalf("scheduled=%+v err=%v", first, err)
	}
	for index := 1; index < 5; index++ {
		at := start.Add(time.Duration(index) * time.Hour)
		if _, err := messages.ScheduleUserStatus(ctx, "T1", "U1", "Future", ":calendar:", at, at.Add(30*time.Minute)); err != nil {
			t.Fatalf("schedule %d: %v", index+1, err)
		}
	}
	if _, err := messages.ScheduleUserStatus(ctx, "T1", "U1", "Sixth", ":six:", start.Add(10*time.Hour), start.Add(11*time.Hour)); !errors.Is(err, ErrScheduledStatusLimit) {
		t.Fatalf("sixth scheduled status err=%v", err)
	}
	updated, err := messages.UpdateScheduledUserStatus(ctx, "T1", "U1", first.ID, "Deep work", ":headphones:", start.Add(15*time.Minute), start.Add(2*time.Hour))
	if err != nil || updated.StatusText != "Deep work" || !updated.StartsAt.Equal(start.Add(15*time.Minute)) {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if err := messages.DeleteScheduledUserStatus(ctx, "T1", "U1", first.ID); err != nil {
		t.Fatal(err)
	}
	values, err := messages.ScheduledUserStatuses(ctx, "T1", "U1")
	if err != nil || len(values) != 4 {
		t.Fatalf("scheduled statuses=%+v err=%v", values, err)
	}
	if _, err := messages.ScheduleUserStatus(ctx, "T1", "U1", "Past", ":clock:", time.Now().Add(-time.Minute), time.Now().Add(time.Hour)); !errors.Is(err, ErrInvalidScheduledStatus) {
		t.Fatalf("past scheduled status err=%v", err)
	}
	if _, err := messages.ScheduleUserStatus(ctx, "T1", "U1", "Unknown emoji", ":not_a_workspace_emoji:", start.Add(20*time.Hour), start.Add(21*time.Hour)); !errors.Is(err, ErrInvalidScheduledStatus) {
		t.Fatalf("unknown scheduled status emoji err=%v", err)
	}
}

func TestScheduleMessageWithBlocksPersistsNormalizedPayload(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
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

func TestScheduledMessagesFollowSlackTokenRangeThreadAndQuotaContracts(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}

	parent, err := messages.Post(ctx, "T1", "U1", "C1", "parent", "", "")
	if err != nil {
		t.Fatal(err)
	}
	postAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	first, err := messages.ScheduleMessageAs(ctx, "T1", "U1", domain.ScheduledMessageRequest{
		Channel: "C1", Text: "threaded", ThreadTimestamp: domain.NewMessageTimestamp(parent.CreatedAt),
		PostAt: postAt, AppID: "A1", BotID: "B1", CredentialHash: "token-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.AppID != "A1" || first.BotID != "B1" || first.ThreadTimestamp == "" {
		t.Fatalf("scheduled attribution/thread lost: %+v", first)
	}
	if _, err := messages.ScheduleMessageAs(ctx, "T1", "U1", domain.ScheduledMessageRequest{Channel: "C1", Text: "other token", PostAt: postAt.Add(time.Second), CredentialHash: "token-two"}); err != nil {
		t.Fatal(err)
	}

	page, err := messages.ScheduledMessagesForCredential(ctx, "T1", "U1", domain.ScheduledMessageQuery{
		CredentialHash: "token-one", Oldest: postAt.Add(-time.Second), Latest: postAt.Add(time.Second),
		Page: domain.PageRequest{Limit: 100},
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("token/range page=%+v err=%v", page, err)
	}
	if err := messages.DeleteScheduledMessageForCredential(ctx, "T1", "U1", "token-two", "C1", first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another token deleted the schedule: %v", err)
	}

	now := time.Now().UTC()
	for _, testCase := range []struct {
		name   string
		postAt time.Time
		want   error
	}{
		{name: "past", postAt: now.Add(-time.Second), want: ErrScheduledTimeInPast},
		{name: "too far", postAt: now.Add(120*24*time.Hour + time.Minute), want: ErrScheduledTimeTooFar},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := messages.ScheduleMessageAs(ctx, "T1", "U1", domain.ScheduledMessageRequest{Channel: "C1", Text: testCase.name, PostAt: testCase.postAt, CredentialHash: "token-one"})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error=%v, want %v", err, testCase.want)
			}
		})
	}

	window := postAt.Add(10 * time.Minute).Truncate(5 * time.Minute)
	for index := 0; index < 30; index++ {
		_, err := messages.ScheduleMessageAs(ctx, "T1", "U1", domain.ScheduledMessageRequest{
			Channel: "C1", Text: fmt.Sprintf("quota-%d", index), PostAt: window.Add(time.Duration(index) * time.Second), CredentialHash: "token-one",
		})
		if err != nil {
			t.Fatalf("schedule %d: %v", index, err)
		}
	}
	if _, err := messages.ScheduleMessageAs(ctx, "T1", "U1", domain.ScheduledMessageRequest{Channel: "C1", Text: "over quota", PostAt: window.Add(31 * time.Second), CredentialHash: "token-one"}); !errors.Is(err, ErrScheduledTooMany) {
		t.Fatalf("31st schedule error=%v, want %v", err, ErrScheduledTooMany)
	}
}

func TestFirstPartyDraftAndScheduledManagementLifecycle(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}

	root, err := messages.Post(ctx, "T1", "U1", "C1", "root", "", "")
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.NewMessageTimestamp(root.CreatedAt)
	now := time.Now().UTC()
	upload := domain.ExternalUpload{
		ID: "draft-upload", WorkspaceID: "T1", Uploader: "U1", Name: "draft.txt", Title: "draft.txt",
		MIMEType: "text/plain", BlobKey: "T1/external/draft-upload", Size: 5, Status: domain.ExternalUploadUploaded,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), UploadedAt: now,
	}
	if err := s.CreateExternalUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}
	draft, err := messages.SaveDraftWithAttachments(ctx, "T1", "U1", "C1", thread, "  exact draft text  ", []domain.DraftAttachment{{UploadID: upload.ID, Title: "Evidence"}})
	if err != nil || draft.Text != "  exact draft text  " || len(draft.Attachments) != 1 || draft.Attachments[0].Name != "draft.txt" {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	loaded, err := messages.Draft(ctx, "T1", "U1", "C1", thread)
	if err != nil || loaded.Text != draft.Text || len(loaded.Attachments) != 1 || loaded.Attachments[0].Title != "Evidence" {
		t.Fatalf("loaded draft=%+v err=%v", loaded, err)
	}
	drafts, err := messages.Drafts(ctx, "T1", "U1", domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(drafts.Items) != 1 || drafts.Items[0].ThreadTimestamp != thread {
		t.Fatalf("draft page=%+v err=%v", drafts, err)
	}
	if err := messages.DeleteDraft(ctx, "T1", "U1", "C1", thread); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteDraft(ctx, "T1", "U1", "C1", thread); err != nil {
		t.Fatalf("draft delete retry was not idempotent: %v", err)
	}

	scheduled, err := messages.ScheduleMessage(ctx, "T1", "U1", "C1", "before edit", time.Now().UTC().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	replacementTime := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second)
	updated, err := messages.UpdateScheduledMessage(ctx, "T1", "U1", scheduled.ID, "C1", "after edit", replacementTime)
	if err != nil || updated.Text != "after edit" || !updated.PostAt.Equal(replacementTime) {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	history, err := messages.ScheduledMessageHistory(ctx, "T1", "U1", false, domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(history.Items) != 1 || history.Items[0].ID != scheduled.ID {
		t.Fatalf("scheduled history=%+v err=%v", history, err)
	}
	if _, err := s.ClaimScheduledMessageForCredential(ctx, "T1", InternalScheduledCredential("T1", "U1"), scheduled.ID, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	failedAt := time.Now().UTC()
	if err := s.MarkScheduledMessageFailed(ctx, "worker", scheduled.ID, "not_in_channel", failedAt, events.Event{ID: "failed-event", WorkspaceID: "T1", Topic: "message.schedule_failed"}); err != nil {
		t.Fatal(err)
	}
	sent, err := messages.SendScheduledMessageNow(ctx, "T1", "U1", scheduled.ID)
	if err != nil || sent.Text != "after edit" {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
	if _, err := messages.SendScheduledMessageNow(ctx, "T1", "U1", scheduled.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second send-now error=%v, want not found", err)
	}
	pendingHistory, err := messages.ScheduledMessageHistory(ctx, "T1", "U1", false, domain.PageRequest{Limit: 10})
	if err != nil || len(pendingHistory.Items) != 0 {
		t.Fatalf("pending history retained a delivered item: page=%+v err=%v", pendingHistory, err)
	}
	sentPage, err := messages.SentMessages(ctx, "T1", "U1", domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(sentPage.Messages) != 2 || sentPage.Messages[0].Text != "after edit" {
		t.Fatalf("sent page=%+v err=%v", sentPage, err)
	}
	history, err = messages.ScheduledMessageHistory(ctx, "T1", "U1", true, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Items) != 1 || history.Items[0].DeliveredAt.IsZero() || !history.Items[0].FailedAt.IsZero() || history.Items[0].FailureCode != "" {
		t.Fatalf("delivered history=%+v err=%v", history, err)
	}
}

func TestScheduledComposerFilesSurviveTicketExpiryAndDeliverIdempotently(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}
	now := time.Now().UTC()
	upload := domain.ExternalUpload{
		ID: "scheduled-upload", WorkspaceID: "T1", Uploader: "U1", Name: "evidence.txt", Title: "evidence.txt",
		MIMEType: "text/plain", BlobKey: "T1/external/scheduled-upload", Size: 8,
		Status: domain.ExternalUploadUploaded, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute), UploadedAt: now.Add(-time.Hour),
	}
	if err := s.CreateExternalUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}
	attachment := domain.DraftAttachment{
		UploadID: upload.ID, Name: upload.Name, Title: "Evidence", MIMEType: upload.MIMEType, Size: upload.Size,
	}
	// Establish the durable draft ownership that lets an already-open composer
	// survive the short-lived upload ticket.
	if _, err := s.UpsertDraft(ctx, domain.Draft{
		WorkspaceID: "T1", UserID: "U1", ConversationID: "C1", Text: "send later",
		Attachments: []domain.DraftAttachment{attachment}, UpdatedAt: now,
	}, events.Event{ID: "draft-event", WorkspaceID: "T1", Topic: "draft.saved", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	draft, err := messages.SaveDraftWithAttachments(ctx, "T1", "U1", "C1", "", "send later", []domain.DraftAttachment{{UploadID: upload.ID, Title: "Evidence"}})
	if err != nil || len(draft.Attachments) != 1 {
		t.Fatalf("expired durable draft could not be saved: draft=%+v err=%v", draft, err)
	}
	scheduled, err := messages.ScheduleMessageAs(ctx, "T1", "U1", domain.ScheduledMessageRequest{
		Channel: "C1", PostAt: now.Add(time.Hour), CredentialHash: InternalScheduledCredential("T1", "U1"),
		FileAttachments: draft.Attachments,
	})
	if err != nil || scheduled.Text != "" || len(scheduled.FileAttachments) != 1 {
		t.Fatalf("file-only schedule=%+v err=%v", scheduled, err)
	}
	if err := messages.DeleteDraft(ctx, "T1", "U1", "C1", ""); err != nil {
		t.Fatal(err)
	}
	referenced, err := s.PendingUploadReferenceExists(ctx, "T1", "U1", upload.ID)
	if err != nil || !referenced {
		t.Fatalf("scheduled upload lost durable ownership: referenced=%v err=%v", referenced, err)
	}
	var references []string
	if err := s.WalkBlobReferences(ctx, "T1", func(value string) error {
		references = append(references, value)
		return nil
	}); err != nil || !slices.Contains(references, upload.BlobKey) {
		t.Fatalf("scheduled blob was collectable: references=%v err=%v", references, err)
	}
	first, err := messages.PostScheduledMessage(ctx, scheduled.WorkspaceID, scheduled.ID)
	if err != nil || len(first.Files) != 1 || first.Files[0].ID != domain.FileID(upload.ID) {
		t.Fatalf("first delivery=%+v err=%v", first, err)
	}
	if err := messages.DeleteScheduledMessage(ctx, "T1", "U1", "C1", scheduled.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("committed schedule was cancelled before acknowledgement: %v", err)
	}
	second, err := messages.PostScheduledMessage(ctx, scheduled.WorkspaceID, scheduled.ID)
	if err != nil || second.ID != first.ID {
		t.Fatalf("retry delivery=%+v err=%v, want message %s", second, err, first.ID)
	}
	history, err := s.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].ID != first.ID {
		t.Fatalf("delivery duplicated message: history=%+v err=%v", history, err)
	}
}

func TestSentMessagesHidesPrivateHistoryAfterLeaving(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}

	if _, err := messages.Post(ctx, "T1", "U1", "C1", "private history", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := messages.LeaveConversation(ctx, "T1", "U1", "C1"); err != nil {
		t.Fatal(err)
	}
	page, err := messages.SentMessages(ctx, "T1", "U1", domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("private history remained visible after leaving: page=%+v err=%v", page, err)
	}
}

func TestDirectConversationCloseKeepsMembershipHistoryAndCanonicalReopen(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	for index := 1; index <= 10; index++ {
		s.SeedUser(domain.User{ID: domain.UserID(fmt.Sprintf("U%d", index)), WorkspaceID: "T1"})
	}
	messages := Messages{Store: s}
	direct, err := messages.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil {
		t.Fatal(err)
	}
	posted, err := messages.Post(ctx, "T1", "U1", direct.ID, "history survives close", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.LeaveConversation(ctx, "T1", "U1", direct.ID); err != nil {
		t.Fatal(err)
	}
	if member, err := s.IsConversationMember(ctx, direct.ID, "U1"); err != nil || !member {
		t.Fatalf("close removed membership: member=%v err=%v", member, err)
	}
	page, err := messages.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, conversation := range page.Conversations {
		if conversation.ID == direct.ID {
			t.Fatalf("closed DM remained in current navigation: %+v", page)
		}
	}
	history, err := messages.History(ctx, "T1", "U1", direct.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].ID != posted.ID {
		t.Fatalf("closed history=%+v err=%v", history, err)
	}
	if err := messages.LeaveConversation(ctx, "T1", "U1", direct.ID); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("second close error=%v, want already closed", err)
	}
	reopened, err := messages.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil || reopened.ID != direct.ID {
		t.Fatalf("reopened=%+v err=%v, want %s", reopened, err, direct.ID)
	}
	page, err = messages.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].ID != direct.ID {
		t.Fatalf("reopened navigation=%+v err=%v", page, err)
	}
	if err := messages.LeaveConversation(ctx, "T1", "U1", direct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U2", direct.ID, "participant reopens it", "", ""); err != nil {
		t.Fatal(err)
	}
	page, err = messages.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].ID != direct.ID {
		t.Fatalf("participant message did not reopen navigation: page=%+v err=%v", page, err)
	}

	eightOthers := []domain.UserID{"U2", "U3", "U4", "U5", "U6", "U7", "U8", "U9"}
	if _, err := messages.OpenConversation(ctx, "T1", "U1", eightOthers); err != nil {
		t.Fatalf("nine-person DM rejected: %v", err)
	}
	if _, err := messages.OpenConversation(ctx, "T1", "U1", append(eightOthers, "U10")); !errors.Is(err, ErrInvalidConversation) {
		t.Fatalf("ten-person DM error=%v, want invalid conversation", err)
	}
}

func TestAddPeopleToDirectConversationCopiesChosenHistoryAndConversionPreservesIdentity(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	for _, id := range []domain.UserID{"U1", "U2", "U3", "U4"} {
		s.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)})
	}
	messages := Messages{Store: s}
	source, err := messages.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := messages.Post(ctx, "T1", "U1", source.ID, "history selected for the new DM", "", "")
	if err != nil {
		t.Fatal(err)
	}

	expanded, err := messages.AddPeopleToDirectConversation(ctx, "T1", "U1", source.ID, []domain.UserID{"U3", "U3"}, domain.DirectHistoryAll)
	if err != nil {
		t.Fatal(err)
	}
	if expanded.ID == source.ID || expanded.Kind != domain.ConversationTypeMPIM || !expanded.PrivateFlag() {
		t.Fatalf("expanded conversation = %+v", expanded)
	}
	sourceMembers, err := messages.ConversationMembers(ctx, "T1", "U1", source.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(sourceMembers.Users) != 2 {
		t.Fatalf("source membership mutated: %+v err=%v", sourceMembers, err)
	}
	targetMembers, err := messages.ConversationMembers(ctx, "T1", "U1", expanded.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(targetMembers.Users) != 3 {
		t.Fatalf("target members = %+v err=%v", targetMembers, err)
	}
	sourceHistory, err := messages.History(ctx, "T1", "U1", source.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(sourceHistory.Messages) != 2 {
		t.Fatalf("source history = %+v err=%v", sourceHistory, err)
	}
	targetHistory, err := messages.History(ctx, "T1", "U1", expanded.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(targetHistory.Messages) != 2 {
		t.Fatalf("target history = %+v err=%v", targetHistory, err)
	}
	if targetHistory.Messages[0].Text != original.Text || targetHistory.Messages[0].ID == original.ID {
		t.Fatalf("copied history = %+v, original = %+v", targetHistory.Messages[0], original)
	}

	converted, err := messages.ConvertGroupDirectToPrivate(ctx, "T1", "U1", expanded.ID, "Project Room")
	if err != nil {
		t.Fatal(err)
	}
	if converted.ID != expanded.ID || converted.Kind != domain.ConversationTypePrivate || converted.Name != "project-room" {
		t.Fatalf("converted conversation = %+v", converted)
	}
	convertedHistory, err := messages.History(ctx, "T1", "U1", converted.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(convertedHistory.Messages) != 3 {
		t.Fatalf("converted history = %+v err=%v", convertedHistory, err)
	}
	convertedMembers, err := messages.ConversationMembers(ctx, "T1", "U1", converted.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(convertedMembers.Users) != 3 {
		t.Fatalf("converted members = %+v err=%v", convertedMembers, err)
	}
	if _, err := messages.ConvertGroupDirectToPrivate(ctx, "T1", "U1", converted.ID, "another-name"); !errors.Is(err, ErrInvalidConversation) {
		t.Fatalf("second conversion error = %v, want invalid conversation", err)
	}

	noHistory, err := messages.AddPeopleToDirectConversation(ctx, "T1", "U1", source.ID, []domain.UserID{"U4"}, domain.DirectHistoryNone)
	if err != nil {
		t.Fatal(err)
	}
	emptyHistory, err := messages.History(ctx, "T1", "U1", noHistory.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(emptyHistory.Messages) != 1 || !strings.Contains(emptyHistory.Messages[0].Text, "added <@U4>") {
		t.Fatalf("history-free expansion = %+v err=%v", emptyHistory, err)
	}
}

func TestScheduledMessageOwnerCanCancelAfterLeavingPrivateConversation(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}
	scheduled, err := messages.ScheduleMessage(ctx, "T1", "U1", "C1", "cancel me", time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.LeaveConversation(ctx, "T1", "U1", "C1"); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteScheduledMessage(ctx, "T1", "U1", "C1", scheduled.ID); err != nil {
		t.Fatalf("owner could not cancel after leaving: %v", err)
	}
	page, err := messages.ScheduledMessages(ctx, "T1", "U1", "", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("scheduled page=%+v err=%v", page, err)
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

func TestRichMessagesPersistNormalizedAttachments(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	attachments := ` [{"text":"attachment"}] `
	message, err := (Messages{Store: s}).PostWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", "", "", attachments, "", "", "")
	if err != nil || message.Attachments != `[{"text":"attachment"}]` {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	updated, err := (Messages{Store: s}).UpdateWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", domain.NewMessageTimestamp(message.CreatedAt), "", "", `[{"text":"updated"}]`)
	if err != nil || updated.Attachments != `[{"text":"updated"}]` {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	ephemeral, err := (Messages{Store: s}).PostEphemeralWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", "U2", "", "", attachments, "")
	if err != nil || ephemeral.Attachments != `[{"text":"attachment"}]` {
		t.Fatalf("ephemeral=%+v err=%v", ephemeral, err)
	}
	scheduled, err := (Messages{Store: s}).ScheduleMessageWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", "", "", attachments, time.Now().UTC().Add(time.Hour))
	if err != nil || scheduled.Attachments != `[{"text":"attachment"}]` {
		t.Fatalf("scheduled=%+v err=%v", scheduled, err)
	}
}

func TestMessagePatchPreservesOmittedRichContentAndRemovesExplicitEmptyArrays(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}
	value, err := messages.PostWithBlocksAndAttachments(
		context.Background(), "T1", "U1", "C1", "fallback",
		`[{"type":"section","text":{"type":"plain_text","text":"block"}}]`,
		`[{"text":"attachment"}]`, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(value.CreatedAt)
	changedAttachments := `[{"text":"changed"}]`
	updated, err := messages.UpdateMessage(context.Background(), "T1", "U1", "C1", timestamp, domain.MessagePatch{Attachments: &changedAttachments})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Text != value.Text || updated.Blocks != value.Blocks || updated.Attachments != changedAttachments {
		t.Fatalf("attachments-only patch erased omitted fields: %+v", updated)
	}
	empty := "[]"
	updated, err = messages.UpdateMessage(context.Background(), "T1", "U1", "C1", timestamp, domain.MessagePatch{Blocks: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Blocks != "[]" || updated.Attachments != changedAttachments {
		t.Fatalf("explicit empty blocks did not remove only blocks: %+v", updated)
	}
	updated, err = messages.UpdateMessage(context.Background(), "T1", "U1", "C1", timestamp, domain.MessagePatch{Attachments: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Attachments != "[]" || updated.Text != value.Text {
		t.Fatalf("explicit empty attachments did not remove only attachments: %+v", updated)
	}
}

// The Slack HTTP decoder already enforced this ceiling, but the shared service
// did not. Browser, gRPC and webhook callers could therefore persist an
// oversized post or edit, while scheduled and ephemeral messages counted bytes
// and rejected non-ASCII text earlier than ASCII.
func TestEveryMessageWriteUsesOneUnicodeCharacterLimit(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	messages := Messages{Store: s}
	ctx := context.Background()
	atLimit := strings.Repeat("界", MaxMessageTextRunes)
	overLimit := atLimit + "界"

	message, err := messages.Post(ctx, "T1", "U1", "C1", atLimit, "", "")
	if err != nil {
		t.Fatalf("post at the Unicode character limit: %v", err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", overLimit, "", ""); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("oversized post error=%v, want %v", err, ErrInvalidMessage)
	}
	if _, err := messages.Update(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(message.CreatedAt), overLimit); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("oversized edit error=%v, want %v", err, ErrInvalidMessage)
	}
	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil || stored.Text != atLimit {
		t.Fatalf("rejected edit changed message: len=%d err=%v", len([]rune(stored.Text)), err)
	}
	if _, err := messages.ScheduleMessageWithBlocks(ctx, "T1", "U1", "C1", atLimit, "", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("schedule at the Unicode character limit: %v", err)
	}
	if _, err := messages.ScheduleMessageWithBlocks(ctx, "T1", "U1", "C1", overLimit, "", time.Now().UTC().Add(time.Hour)); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("oversized schedule error=%v, want %v", err, ErrInvalidMessage)
	}
	if _, err := messages.PostEphemeralWithBlocks(ctx, "T1", "U1", "C1", "U2", atLimit, ""); err != nil {
		t.Fatalf("ephemeral post at the Unicode character limit: %v", err)
	}
	if _, err := messages.PostEphemeralWithBlocks(ctx, "T1", "U1", "C1", "U2", overLimit, ""); !errors.Is(err, ErrInvalidEphemeral) {
		t.Fatalf("oversized ephemeral error=%v, want %v", err, ErrInvalidEphemeral)
	}
}

func TestEveryMessageWriteUsesOneStructuredBodyLimit(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	messages := Messages{Store: s}
	oversized := `[{"type":"section","text":{"type":"plain_text","text":"` + strings.Repeat("x", MaxMessageBodyBytes) + `"}}]`

	if _, err := messages.PostWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", "", oversized, "", "", "", ""); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("post oversized body err=%v", err)
	}
	plain, err := messages.Post(context.Background(), "T1", "U1", "C1", "before", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.UpdateWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", domain.NewMessageTimestamp(plain.CreatedAt), "", oversized, ""); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("update oversized body err=%v", err)
	}
	if _, err := messages.ScheduleMessageWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", "", oversized, "", time.Now().UTC().Add(time.Hour)); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("schedule oversized body err=%v", err)
	}
	if _, err := messages.PostEphemeralWithBlocksAndAttachments(context.Background(), "T1", "U1", "C1", "U2", "", oversized, "", ""); !errors.Is(err, ErrInvalidEphemeral) {
		t.Fatalf("ephemeral oversized body err=%v", err)
	}
	if _, err := messages.Unfurl(context.Background(), "T1", "U1", "C1", domain.NewMessageTimestamp(plain.CreatedAt), map[string]string{
		"https://example.test": `{"text":"` + strings.Repeat("x", MaxMessageBodyBytes) + `"}`,
	}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("unfurl oversized body err=%v", err)
	}

	stored, err := s.GetMessage(context.Background(), plain.ID)
	if err != nil || stored.Text != "before" || hasStructuredPayload(stored.Blocks) || hasStructuredPayload(stored.Attachments) || len(stored.Unfurls) != 0 {
		t.Fatalf("rejected update changed stored message: %+v err=%v", stored, err)
	}
}

func hasStructuredPayload(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw != "" && raw != "[]"
}

func TestExternalUploadSurvivesUploadRetryAndCompletesOnce(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	ctx := context.Background()
	upload, err := messages.CreateExternalUpload(ctx, "T1", "U1", "notes.txt", "text/plain", 7, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.UploadExternalFile(ctx, upload.ID, 7, bytes.NewReader([]byte("content"))); err != nil {
		t.Fatal(err)
	}
	file, err := messages.CompleteExternalUpload(ctx, "T1", "U1", upload.ID, "Notes", []domain.ConversationID{"C1", "C1"}, "Uploaded", `[{"type":"divider"}]`, "")
	if err != nil || file.BlobKey != upload.BlobKey {
		t.Fatalf("file=%+v err=%v", file, err)
	}
	second, err := messages.CompleteExternalUpload(ctx, "T1", "U1", upload.ID, "Notes", []domain.ConversationID{"C1"}, "Uploaded", `[{"type":"divider"}]`, "")
	if err != nil || second.ID != file.ID {
		t.Fatalf("second completion file=%+v err=%v", second, err)
	}
	metadata, err := messages.FileInfo(ctx, "T1", "U1", file.ID)
	if err != nil || len(metadata.SharedChannels) != 1 || metadata.SharedChannels[0] != "C1" {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	page, err := messages.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "Uploaded" || page.Messages[0].Blocks != "" || len(page.Messages[0].Files) != 1 || page.Messages[0].Files[0].ID != file.ID {
		t.Fatalf("published messages=%+v err=%v", page.Messages, err)
	}
	stored, err := s.GetExternalUpload(ctx, upload.ID)
	if err != nil || stored.Status != domain.ExternalUploadCompleted {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	records, err := s.ListEventsAfter(ctx, "T1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	topics := make([]string, 0, len(records))
	for _, record := range records {
		topics = append(topics, record.Event.Topic)
		if (record.Event.Topic == "file.created" || record.Event.Topic == "file.shared" || record.Event.Topic == "message.created") && record.Event.PrivatePayload == "" {
			t.Fatalf("%s has no immutable delivery snapshot", record.Event.Topic)
		}
	}
	if strings.Join(topics, ",") != "file.created,file.shared,message.created" {
		t.Fatalf("upload events=%v", topics)
	}
}

// A file is readable by whoever can read a conversation it is shared into, so
// deleting the only message that shared it has to end the share. Before this
// was wired, the share row outlived its message: a member who never saw the
// file posted could still open it, and files.list still listed it, with
// nothing in the channel to show where it came from.
func TestDeletingTheSharingMessageEndsTheShareAndAnnouncesIt(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general", Kind: domain.ConversationTypePrivate})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	ctx := context.Background()
	upload, err := messages.CreateExternalUpload(ctx, "T1", "U1", "notes.txt", "text/plain", 7, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.UploadExternalFile(ctx, upload.ID, 7, bytes.NewReader([]byte("content"))); err != nil {
		t.Fatal(err)
	}
	file, err := messages.CompleteExternalUpload(ctx, "T1", "U1", upload.ID, "Notes", []domain.ConversationID{"C1"}, "Uploaded", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.FileInfo(ctx, "T1", "U2", file.ID); err != nil {
		t.Fatalf("a member of the channel it was shared into cannot read the file: %v", err)
	}
	page, err := messages.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("messages=%+v err=%v", page.Messages, err)
	}
	if _, err := messages.Delete(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(page.Messages[0].CreatedAt)); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.FileInfo(ctx, "T1", "U2", file.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the file is still readable in a channel that no longer shares it: %v", err)
	}
	if info, err := messages.FileInfo(ctx, "T1", "U1", file.ID); err != nil || len(info.SharedChannels) != 0 {
		t.Fatalf("uploader sees shares=%+v err=%v, want the share retracted", info.SharedChannels, err)
	}
	records, err := s.ListEventsAfter(ctx, "T1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	unshared := 0
	for _, record := range records {
		if record.Event.Topic != "file.unshared" {
			continue
		}
		unshared++
		if record.Event.PrivatePayload == "" {
			t.Fatal("file.unshared has no immutable delivery snapshot, so no app can be authorized for it")
		}
	}
	if unshared != 1 {
		t.Fatalf("file.unshared announced %d times for one retracted share", unshared)
	}
}

func TestExternalUploadUsesTicketSizeForMultipartParts(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	upload, err := messages.CreateExternalUpload(context.Background(), "T1", "U1", "notes.txt", "text/plain", 7, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.UploadExternalFile(context.Background(), upload.ID, -1, bytes.NewReader([]byte("content"))); err != nil {
		t.Fatalf("multipart upload: %v", err)
	}
	if value, err := s.GetExternalUpload(context.Background(), upload.ID); err != nil || value.Status != domain.ExternalUploadUploaded {
		t.Fatalf("upload=%+v err=%v", value, err)
	}
}

func TestExternalUploadCompletionHandlesMultipleFilesAtomically(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "random"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C2", "U1")
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	ctx := context.Background()
	first, err := messages.CreateExternalUpload(ctx, "T1", "U1", "first.txt", "text/plain", 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := messages.CreateExternalUpload(ctx, "T1", "U1", "second.txt", "text/plain", 6, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.UploadExternalFile(ctx, first.ID, first.Size, bytes.NewReader([]byte("first"))); err != nil {
		t.Fatal(err)
	}
	if err := messages.UploadExternalFile(ctx, second.ID, second.Size, bytes.NewReader([]byte("second"))); err != nil {
		t.Fatal(err)
	}
	files, err := messages.CompleteExternalUploads(ctx, "T1", "U1", []domain.ExternalUploadCompletion{{ID: first.ID, Title: "First"}, {ID: second.ID, Title: "Second"}}, []domain.ConversationID{"C1"}, "", `[ {"type":"section","text":{"type":"plain_text","text":"Uploaded"}} ]`, "")
	if err != nil || len(files) != 2 || files[0].Title != "First" || files[1].Title != "Second" {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	page, err := messages.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Blocks == "" || len(page.Messages[0].Files) != 2 || page.Messages[0].Files[0].ID != files[0].ID || page.Messages[0].Files[1].ID != files[1].ID {
		t.Fatalf("messages=%+v err=%v", page.Messages, err)
	}
	retry, err := messages.CompleteExternalUploads(ctx, "T1", "U1", []domain.ExternalUploadCompletion{{ID: second.ID}, {ID: first.ID}}, []domain.ConversationID{"C1"}, "", `[ {"type":"section","text":{"type":"plain_text","text":"Uploaded"}} ]`, "")
	if err != nil || len(retry) != 2 || retry[0].ID != files[1].ID || retry[1].ID != files[0].ID {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	if _, err := messages.CompleteExternalUploads(ctx, "T1", "U1", []domain.ExternalUploadCompletion{{ID: first.ID}, {ID: second.ID}}, []domain.ConversationID{"C2"}, "wrong destination", "", ""); !errors.Is(err, ErrInvalidExternalUpload) {
		t.Fatalf("completed tickets reused in another channel: %v", err)
	}
	page, err = messages.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("duplicate messages=%+v err=%v", page.Messages, err)
	}
}

func TestDraftOwnedUploadRemainsCompletableAfterTicketWindow(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	now := time.Now().UTC()
	upload := domain.ExternalUpload{
		ID: "draft-expired", WorkspaceID: "T1", Uploader: "U1", Name: "old.txt", Title: "old.txt",
		MIMEType: "text/plain", BlobKey: "T1/external/draft-expired", Size: 3, Status: domain.ExternalUploadUploaded,
		CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute), UploadedAt: now.Add(-time.Hour),
	}
	if err := s.CreateExternalUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertDraft(ctx, domain.Draft{
		WorkspaceID: "T1", UserID: "U1", ConversationID: "C1", Text: "still working",
		Attachments: []domain.DraftAttachment{{UploadID: upload.ID, Name: upload.Name, Title: upload.Title, MIMEType: upload.MIMEType, Size: upload.Size}},
		UpdatedAt:   now,
	}, events.Event{ID: "draft-event", WorkspaceID: "T1", Topic: "draft.saved"}); err != nil {
		t.Fatal(err)
	}
	files, err := (Messages{Store: s}).CompleteExternalUploads(
		ctx, "T1", "U1", []domain.ExternalUploadCompletion{{ID: upload.ID}},
		[]domain.ConversationID{"C1"}, "finished", "", "",
	)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	history, err := s.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Text != "finished" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

// files.getUploadURLExternal hands the caller a file_id before any bytes exist,
// and Slack's documented flow uses that same identifier to reference the file
// once files.completeUploadExternal returns. Minting a fresh identifier at
// completion strands every caller that recorded the first one.
func TestExternalUploadKeepsItsIdentifierThroughCompletion(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	ctx := context.Background()

	upload, err := messages.CreateExternalUpload(ctx, "T1", "U1", "notes.txt", "text/plain", 7, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.UploadExternalFile(ctx, upload.ID, 7, bytes.NewReader([]byte("content"))); err != nil {
		t.Fatal(err)
	}
	file, err := messages.CompleteExternalUpload(ctx, "T1", "U1", upload.ID, "Notes", []domain.ConversationID{"C1"}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(file.ID) != string(upload.ID) {
		t.Fatalf("completed file id %q, want the upload id %q handed to the caller", file.ID, upload.ID)
	}
	// The identifier the caller was given must resolve without it having to
	// read a new one out of the completion response.
	metadata, err := messages.FileInfo(ctx, "T1", "U1", domain.FileID(upload.ID))
	if err != nil {
		t.Fatalf("FileInfo(%q) after completion: %v", upload.ID, err)
	}
	if metadata.Name != "notes.txt" {
		t.Fatalf("metadata=%+v", metadata)
	}
}

// A batch completion must preserve each upload's identifier, not just the first.
func TestExternalUploadBatchKeepsEveryIdentifier(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	if err := s.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	ctx := context.Background()

	completions := make([]domain.ExternalUploadCompletion, 0, 3)
	for index := 0; index < 3; index++ {
		upload, err := messages.CreateExternalUpload(ctx, "T1", "U1", "batch.txt", "text/plain", 7, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := messages.UploadExternalFile(ctx, upload.ID, 7, bytes.NewReader([]byte("content"))); err != nil {
			t.Fatal(err)
		}
		completions = append(completions, domain.ExternalUploadCompletion{ID: upload.ID, Title: "Batch"})
	}
	files, err := messages.CompleteExternalUploads(ctx, "T1", "U1", completions, []domain.ConversationID{"C1"}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(completions) {
		t.Fatalf("completed %d files, want %d", len(files), len(completions))
	}
	for index, file := range files {
		if string(file.ID) != string(completions[index].ID) {
			t.Fatalf("file %d has id %q, want %q", index, file.ID, completions[index].ID)
		}
	}
}

// An edit is a durable fact about the message, not only about the event it
// emitted. Slack's message object carries `edited`, and every reader needs
// it; deriving it from the outbox meant a replayed event reported its replay
// instant as the edit time, and a reader of the message could not tell an
// edited message from an untouched one at all.
func TestEditingAMessageRecordsWhoEditedItAndWhen(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}
	posted, err := messages.Post(ctx, "T1", "U1", "C1", "before", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !posted.EditedAt.IsZero() || posted.EditedBy != "" {
		t.Fatalf("a freshly posted message reports an edit: %+v", posted)
	}
	after := "after"
	edited, err := messages.UpdateMessage(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(posted.CreatedAt), domain.MessagePatch{Text: &after})
	if err != nil {
		t.Fatal(err)
	}
	if edited.EditedAt.IsZero() || edited.EditedBy != "U1" {
		t.Fatalf("edit=%+v, want an instant and the editor", edited)
	}
	// And the fact survives a read, which is the half the outbox event could
	// never supply.
	reread, err := s.GetMessage(ctx, posted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.EditedAt.IsZero() || reread.EditedBy != "U1" {
		t.Fatalf("stored message lost its edit: %+v", reread)
	}
	if !reread.CreatedAt.Equal(posted.CreatedAt) {
		t.Fatalf("an edit moved the message's identity from %s to %s", posted.CreatedAt, reread.CreatedAt)
	}
}

// A subtype is vocabulary, not free text: a caller cannot invent one, and
// chat.meMessage's narration is durably distinguishable from something a
// person composed.
func TestMessageSubtypeIsRefusedUnlessItIsVocabulary(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	messages := Messages{Store: s}
	narrated, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{
		Conversation: "C1", Text: "waves", Subtype: domain.MessageSubtypeMeMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if narrated.Subtype != domain.MessageSubtypeMeMessage {
		t.Fatalf("subtype=%q, want me_message", narrated.Subtype)
	}
	if _, err := messages.PostMessageAs(ctx, "T1", "U1", domain.MessagePostRequest{
		Conversation: "C1", Text: "hello", Subtype: domain.MessageSubtype("not_a_slack_subtype"),
	}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("invented subtype error=%v, want %v", err, ErrInvalidMessage)
	}
}

// The comment on InviteRequest.ExpiresAt says the deadline is set when the
// request is recorded rather than when it is approved, "because it is the
// promise made to the invited person that ages — an invitation that sat in the
// queue for a month is not fresh because someone finally clicked approve".
// Nothing enforced that. Approval succeeded and issued an invitation that
// AcceptInvitationForEmail refuses on the same deadline, so the address was
// invited to nothing and the queue said otherwise.
func TestALapsedInviteRequestCannotBeApprovedButCanBeDenied(t *testing.T) {
	ctx := context.Background()
	newFixture := func(t *testing.T, id domain.InviteRequestID) (*memory.Store, Messages) {
		t.Helper()
		s := memory.New()
		s.SeedWorkspace(domain.Workspace{ID: "T1"})
		s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
		seedWorkspaceAdmin(t, s, "T1", "U1")
		// The deadline is written directly: a request recorded through the
		// service is always dated fourteen days out, and waiting is not a test.
		past := time.Now().UTC().Add(-time.Hour)
		request := domain.InviteRequest{
			ID: id, WorkspaceID: "T1", Email: "late@example.com", RequestedBy: "U1",
			Status: domain.InviteRequestPending, CreatedAt: past.Add(-InvitationLifetime), ExpiresAt: past,
		}
		event := events.Event{ID: domain.EventID("E" + id), WorkspaceID: "T1", Topic: "invite_request.created", Payload: string(id), CreatedAt: past}
		if err := s.CreateInviteRequest(ctx, request, event); err != nil {
			t.Fatal(err)
		}
		return s, Messages{Store: s}
	}

	t.Run("approval is refused", func(t *testing.T) {
		s, messages := newFixture(t, "IR-lapsed")
		if err := messages.AdminApproveInviteRequest(ctx, "T1", "U1", "IR-lapsed"); !errors.Is(err, ErrInvitationExpired) {
			t.Fatalf("approving a lapsed request err=%v, want ErrInvitationExpired", err)
		}
		// The refusal changed nothing: the request is still pending.
		page, err := messages.AdminListInviteRequests(ctx, "T1", "U1", domain.InviteRequestPending, domain.PageRequest{Limit: 5})
		if err != nil || len(page.Requests) != 1 {
			t.Fatalf("pending page=%+v err=%v, want the request untouched", page, err)
		}
		_ = s
	})

	t.Run("denial is allowed", func(t *testing.T) {
		_, messages := newFixture(t, "IR-denied")
		if err := messages.AdminDenyInviteRequest(ctx, "T1", "U1", "IR-denied"); err != nil {
			t.Fatalf("denying a lapsed request: %v", err)
		}
		page, err := messages.AdminListInviteRequests(ctx, "T1", "U1", domain.InviteRequestDenied, domain.PageRequest{Limit: 5})
		if err != nil || len(page.Requests) != 1 {
			t.Fatalf("denied page=%+v err=%v", page, err)
		}
	})
}

// A guest reaches channels by being added to them, never by naming one. Without
// this rule a single-channel guest — someone invited to exactly one channel —
// could walk into every public channel in the workspace, one identifier at a
// time, and could create channels of their own.
func TestAGuestCannotReachAChannelNobodyAddedThemTo(t *testing.T) {
	ctx := context.Background()
	for _, guest := range []struct {
		name    string
		tier    domain.WorkspaceMembership
		refusal error
	}{
		{
			name:    "single-channel guest",
			tier:    domain.WorkspaceMembership{Role: domain.WorkspaceRoleMember, Active: true, UltraRestricted: true},
			refusal: ErrUserIsUltraRestricted,
		},
		{
			name:    "multi-channel guest",
			tier:    domain.WorkspaceMembership{Role: domain.WorkspaceRoleMember, Active: true, Restricted: true},
			refusal: ErrUserIsRestricted,
		},
	} {
		t.Run(guest.name, func(t *testing.T) {
			s := memory.New()
			if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
				t.Fatal(err)
			}
			user := domain.User{ID: "UG", WorkspaceID: "T1", Email: "guest@example.com", Name: "guest"}
			membership := guest.tier
			membership.WorkspaceID, membership.UserID = "T1", user.ID
			if err := s.CreateUser(ctx, user, membership, events.Event{ID: "E-guest", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			// The channel they were invited to, and one they were not.
			for _, id := range []domain.ConversationID{"C-invited", "C-elsewhere"} {
				if err := s.SeedConversation(domain.Conversation{ID: id, WorkspaceID: "T1", Name: string(id)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SeedConversationMember("C-invited", user.ID); err != nil {
				t.Fatal(err)
			}
			messages := Messages{Store: s}

			if _, err := messages.JoinConversation(ctx, "T1", user.ID, "C-elsewhere"); !errors.Is(err, guest.refusal) {
				t.Fatalf("joining err=%v, want %v", err, guest.refusal)
			}
			if _, err := messages.CreateConversation(ctx, "T1", user.ID, "guest-made", false); !errors.Is(err, guest.refusal) {
				t.Fatalf("creating err=%v, want %v", err, guest.refusal)
			}
			// The refusal is about reaching, not about being there: the channel
			// they were added to stays fully usable.
			if _, err := messages.Post(ctx, "T1", user.ID, "C-invited", "hello", "", ""); err != nil {
				t.Fatalf("the guest could not post in their own channel: %v", err)
			}
			// And it left no membership behind: a refusal that still wrote the
			// row would be the same breach one step later.
			member, err := s.IsConversationMember(ctx, "C-elsewhere", user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if member {
				t.Fatal("a refused join still added the guest to the channel")
			}
		})
	}
}

// The rule is about guests, so it must not touch an ordinary member.
func TestAnOrdinaryMemberStillJoinsAndCreatesChannels(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "member@example.com", Name: "member"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}
	if _, err := messages.JoinConversation(ctx, "T1", "U1", "C1"); err != nil {
		t.Fatalf("a member could not join a public channel: %v", err)
	}
	if _, err := messages.CreateConversation(ctx, "T1", "U1", "member-made", false); err != nil {
		t.Fatalf("a member could not create a channel: %v", err)
	}
}

// Ending a member's sessions is idempotent: the asked-for state is that they
// hold none, and a member who already holds none is not a missing member. The
// store used to report ErrNotFound when there was nothing to revoke, which the
// single-member path passed straight through — so an administrator signing out
// somebody who was not signed in was told the user did not exist. The bulk
// path had already noticed and worked around it, which is what proved the
// single path wrong rather than the store right.
func TestResettingSessionsSucceedsWhenThereAreNone(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.com", Name: "admin"}); err != nil {
		t.Fatal(err)
	}
	seedWorkspaceAdmin(t, s, "T1", "U1")
	if err := s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "quiet@example.com", Name: "quiet"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}

	if err := messages.ResetUserSessions(ctx, "T1", "U1", "U2"); err != nil {
		t.Fatalf("resetting the sessions of a member who has none: %v", err)
	}
	if err := messages.ResetUserSessionsBulk(ctx, "T1", "U1", []domain.UserID{"U2"}); err != nil {
		t.Fatalf("the bulk path disagreed with the single one: %v", err)
	}
	// A member who really is not there is still not found, so the fix did not
	// swallow the answer that matters.
	if err := messages.ResetUserSessions(ctx, "T1", "U1", "U-absent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("resetting sessions for a missing member err=%v, want ErrNotFound", err)
	}
}

// admin.teams.admins.list and admin.teams.owners.list are the only routes that
// reach this listing, so any other role is refused — but it used to be refused
// with ErrInvalidUserGroup, "user group name, handle, and members are invalid",
// which sent a caller asking about workspace roles to look at user groups.
func TestListingTeamUsersRefusesAnUnsupportedRoleAsAWorkspaceError(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.com", Name: "admin"}); err != nil {
		t.Fatal(err)
	}
	seedWorkspaceAdmin(t, s, "T1", "U1")
	messages := Messages{Store: s}

	if _, err := messages.AdminTeamUsers(ctx, "T1", "U1", domain.WorkspaceRoleMember, domain.PageRequest{Limit: 10}); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("listing members err=%v, want ErrInvalidWorkspace", err)
	}
	// The two roles the routes do pass still work.
	for _, role := range []domain.WorkspaceRole{domain.WorkspaceRoleAdmin, domain.WorkspaceRoleOwner} {
		if _, err := messages.AdminTeamUsers(ctx, "T1", "U1", role, domain.PageRequest{Limit: 10}); err != nil {
			t.Fatalf("listing %s: %v", role, err)
		}
	}
}
