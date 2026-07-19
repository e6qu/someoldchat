package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestFindUserByEmailIsCaseInsensitiveAndWorkspaceScoped(t *testing.T) {
	s := New()
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "Alice@example.com"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T2", Email: "alice@example.com"})
	user, err := s.FindUserByEmail(context.Background(), "T1", " ALICE@EXAMPLE.COM ")
	if err != nil || user.ID != "U1" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	if _, err := s.FindUserByEmail(context.Background(), "T3", "alice@example.com"); err != store.ErrNotFound {
		t.Fatalf("cross-workspace lookup error=%v", err)
	}
}

func TestCreateUserRequiresWorkspaceAndRejectsDuplicateEmail(t *testing.T) {
	s := New()
	ctx := context.Background()
	if err := s.CreateUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Email: "a@example.com", Name: "Alice"}, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "U1", Role: domain.WorkspaceRoleMember, Active: true}, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "user.created"}); err != store.ErrNotFound {
		t.Fatalf("missing workspace error=%v", err)
	}
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	if err := s.CreateUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Email: "a@example.com", Name: "Alice"}, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "U1", Role: domain.WorkspaceRoleMember, Active: true}, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "user.created"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, domain.User{ID: "U2", WorkspaceID: "T1", Email: "A@EXAMPLE.COM", Name: "Other"}, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "U2", Role: domain.WorkspaceRoleMember, Active: true}, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "user.created"}); err != store.ErrAlreadyExists {
		t.Fatalf("duplicate email error=%v", err)
	}
}

func TestSocketModeResponseQueueLeasesAndRecovers(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	first := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "b", Payload: `{}`, ReceivedAt: now}
	second := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "a", Payload: `{}`, ReceivedAt: now}
	if err := s.RecordSocketModeResponse(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSocketModeResponse(ctx, second); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimSocketModeResponses(ctx, "A1", "worker-1", 2, time.Minute)
	if err != nil || len(claimed) != 2 || claimed[0].EnvelopeID != "a" || claimed[1].EnvelopeID != "b" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if claimedAgain, err := s.ClaimSocketModeResponses(ctx, "A1", "worker-2", 2, time.Minute); err != nil || len(claimedAgain) != 0 {
		t.Fatalf("claimed again=%+v err=%v", claimedAgain, err)
	}
	if err := s.ReleaseSocketModeResponses(ctx, "worker-1", claimed[1:], now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.AckSocketModeResponses(ctx, "worker-1", claimed[:1]); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := s.ClaimSocketModeResponses(ctx, "A1", "worker-2", 2, now.Add(time.Minute).Sub(now))
	if err != nil || len(reclaimed) != 1 || reclaimed[0].EnvelopeID != "b" {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
	if err := s.AckSocketModeResponses(ctx, "worker-2", reclaimed); err != nil {
		t.Fatal(err)
	}
}

func TestSocketModeResponseRenewalKeepsSlowLeaseOwned(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now().UTC()
	response := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "slow", Payload: `{}`, ReceivedAt: now}
	if err := s.RecordSocketModeResponse(ctx, response); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimSocketModeResponses(ctx, response.AppID, "worker-1", 1, 30*time.Millisecond)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := s.RenewSocketModeResponses(ctx, "worker-1", claimed, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if replacement, err := s.ClaimSocketModeResponses(ctx, response.AppID, "worker-2", 1, time.Minute); err != nil || len(replacement) != 0 {
		t.Fatalf("renewed response was reclaimed=%+v err=%v", replacement, err)
	}
}

func TestScheduledMessageClaimsSortBeforeApplyingLimit(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	created := time.Now().UTC().Add(-time.Minute)
	for _, id := range []domain.ScheduledMessageID{"scheduled-a", "scheduled-b"} {
		value := domain.ScheduledMessage{WorkspaceID: "T1", ID: id, Channel: "C1", Author: "U1", Text: string(id), PostAt: created, CreatedAt: created}
		if err := s.CreateScheduledMessage(ctx, value, events.Event{ID: domain.EventID("event-" + string(id)), WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(id), CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ClaimScheduledMessages(ctx, "T1", "worker-1", 1, time.Minute)
	if err != nil || len(first) != 1 || first[0].ID != "scheduled-a" {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	second, err := s.ClaimScheduledMessages(ctx, "T1", "worker-2", 1, time.Minute)
	if err != nil || len(second) != 1 || second[0].ID != "scheduled-b" {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
}

func TestListUsersAndConversationsAreBoundedAndAuthorized(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", IsPrivate: true})
	membership, err := s.GetWorkspaceMembership(ctx, "T1", "U1")
	if err != nil || membership.Role != "member" || !membership.Active {
		t.Fatalf("membership=%+v err=%v", membership, err)
	}

	users, err := s.ListUsers(ctx, "T1", domain.PageRequest{Limit: 1})
	if err != nil || len(users.Users) != 1 || !users.HasMore || users.NextCursor == "" {
		t.Fatalf("first users=%+v err=%v", users, err)
	}
	users, err = s.ListUsers(ctx, "T1", domain.PageRequest{Limit: 1, Cursor: users.NextCursor})
	if err != nil || len(users.Users) != 1 || users.HasMore {
		t.Fatalf("second users=%+v err=%v", users, err)
	}
	adminUsers, err := s.ListAdminUsers(ctx, "T1", domain.PageRequest{Limit: 1})
	if err != nil || len(adminUsers.Users) != 1 || !adminUsers.HasMore || adminUsers.NextCursor == "" {
		t.Fatalf("first administrator users=%+v err=%v", adminUsers, err)
	}

	conversations, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(conversations.Conversations) != 1 || conversations.Conversations[0].ID != "C1" {
		t.Fatalf("unauthorized private conversations=%+v err=%v", conversations, err)
	}
	s.SeedConversationMember("C2", "U1")
	conversations, err = s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(conversations.Conversations) != 2 {
		t.Fatalf("authorized conversations=%+v err=%v", conversations, err)
	}
}

func TestConversationUnreadCountFollowsReadCursor(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	created := time.Unix(1700000000, 123456789).UTC()
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

func TestConversationListFiltersBeforePagination(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "public"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", IsPrivate: true, Archived: true})
	s.SeedConversationMember("C2", "U1")
	public, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 1, Types: []domain.ConversationType{domain.ConversationTypePublic}})
	if err != nil || len(public.Conversations) != 1 || public.Conversations[0].ID != "C1" || public.HasMore {
		t.Fatalf("public filter=%+v err=%v", public, err)
	}
	active, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10, ExcludeArchived: true})
	if err != nil || len(active.Conversations) != 1 || active.Conversations[0].ID != "C1" {
		t.Fatalf("archived filter=%+v err=%v", active, err)
	}
}

func TestAuthSeedingDoesNotOverwriteDurableState(t *testing.T) {
	s := New()
	s.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}})
	s.SeedToken(context.Background(), "token", domain.TokenRecord{WorkspaceID: "T2", UserID: "U2", Revoked: true})
	token, err := s.LookupToken(context.Background(), "token")
	if err != nil || token.WorkspaceID != "T1" || token.Revoked {
		t.Fatalf("token=%+v err=%v", token, err)
	}
	firstExpiry := time.Now().UTC().Add(time.Hour)
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", ExpiresAt: firstExpiry}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedSession(context.Background(), "session", domain.SessionRecord{WorkspaceID: "T2", UserID: "U2", Revoked: true}); err != nil {
		t.Fatal(err)
	}
	session, err := s.LookupSession(context.Background(), "session")
	if err != nil || session.WorkspaceID != "T1" || session.Revoked || !session.ExpiresAt.Equal(firstExpiry) {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	if err := s.RevokeSession(context.Background(), "session"); err != nil {
		t.Fatal(err)
	}
	session, err = s.LookupSession(context.Background(), "session")
	if err != nil || !session.Revoked {
		t.Fatalf("revoked session=%+v err=%v", session, err)
	}
}

func TestFileMetadataIsDurableAndSoftDeleted(t *testing.T) {
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	file := domain.File{ID: "file_1", WorkspaceID: "T1", Uploader: "U1", Name: "notes.txt", BlobKey: "T1/file_1", Size: 7, CreatedAt: time.Now().UTC()}
	ctx := context.Background()
	if err := s.CreateFile(ctx, file, events.Event{ID: "evt_file", WorkspaceID: "T1", Topic: "file.created", Payload: string(file.ID), CreatedAt: file.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListFiles(ctx, "T1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Files) != 1 || page.Files[0].ID != file.ID {
		t.Fatalf("files=%+v err=%v", page, err)
	}
	if err := s.DeleteFile(ctx, file.ID, events.Event{ID: "evt_file_delete", WorkspaceID: "T1", Topic: "file.deleted", Payload: string(file.ID), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFile(ctx, file.ID); err != store.ErrNotFound {
		t.Fatalf("deleted file err=%v", err)
	}
}

func TestDoNotDisturbStateIsDurableInMemoryStore(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	value := domain.DoNotDisturb{WorkspaceID: "T1", UserID: "U1", Enabled: true, SnoozeUntil: until}
	if err := s.SetDoNotDisturb(ctx, value, events.Event{ID: "dnd-1", WorkspaceID: "T1", Topic: "user.dnd_changed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDoNotDisturb(ctx, "T1", "U1")
	if err != nil || !got.Enabled || !got.SnoozeUntil.Equal(until) {
		t.Fatalf("dnd=%+v err=%v", got, err)
	}
}

func TestStarsAreDurableAndPaged(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	s.SeedConversationMember("C1", "U1")
	created := time.Unix(300, 0).UTC()
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "starred", CreatedAt: created}
	if err := s.CreateMessage(ctx, message, events.Event{ID: "message-1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: created}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddStar(ctx, domain.Star{Message: message, Conversation: "C1", UserID: "U1", CreatedAt: created}, events.Event{ID: "star-1", WorkspaceID: "T1", Topic: "star.added", Payload: "M1", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	stars, next, more, err := s.ListStars(ctx, "T1", "U1", domain.PageRequest{Limit: 1})
	if err != nil || len(stars) != 1 || stars[0].Message.ID != "M1" || more || next != "" {
		t.Fatalf("stars=%+v next=%q more=%v err=%v", stars, next, more, err)
	}
}

func TestRemindersAreDurableAndCompletable(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	id, err := domain.NewReminderID()
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	reminder := domain.Reminder{WorkspaceID: "T1", ID: id, Creator: "U1", User: "U1", Text: "check-in", Time: due}
	if err := s.CreateReminder(ctx, reminder, events.Event{ID: "reminder-1", WorkspaceID: "T1", Topic: "reminder.created", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	second := domain.Reminder{WorkspaceID: "T1", ID: "RmZZ", Creator: "U1", User: "U1", Text: "second", Time: due}
	if err := s.CreateReminder(ctx, second, events.Event{ID: "reminder-3", WorkspaceID: "T1", Topic: "reminder.created", Payload: string(second.ID), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListReminders(ctx, "T1", "U1", domain.PageRequest{Limit: 1})
	if err != nil || len(page.Reminders) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first reminder page=%+v err=%v", page, err)
	}
	page, err = s.ListReminders(ctx, "T1", "U1", domain.PageRequest{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(page.Reminders) != 1 || page.HasMore {
		t.Fatalf("second reminder page=%+v err=%v", page, err)
	}
	if err := s.CompleteReminder(ctx, "T1", "U1", id, time.Now().UTC(), events.Event{ID: "reminder-2", WorkspaceID: "T1", Topic: "reminder.completed", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReminder(ctx, "T1", "U1", id)
	if err != nil || got.CompleteAt.IsZero() || got.Text != "check-in" {
		t.Fatalf("reminder=%+v err=%v", got, err)
	}
}

func TestAccessLogsPaginationDoesNotMaterializeHistory(t *testing.T) {
	ctx := context.Background()
	s := New()
	for index := 0; index < 4; index++ {
		if err := s.RecordAccess(ctx, domain.AccessLog{WorkspaceID: "T1", UserID: "U1", CreatedAt: time.Unix(int64(index+1), 0).UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	values, more, err := s.ListAccessLogs(ctx, "T1", time.Time{}, 2, 1)
	if err != nil || len(values) != 2 || !more || values[0].CreatedAt.Unix() != 4 || values[1].CreatedAt.Unix() != 3 {
		t.Fatalf("first page=%+v more=%v err=%v", values, more, err)
	}
	values, more, err = s.ListAccessLogs(ctx, "T1", time.Time{}, 2, 2)
	if err != nil || len(values) != 2 || more || values[0].CreatedAt.Unix() != 2 || values[1].CreatedAt.Unix() != 1 {
		t.Fatalf("second page=%+v more=%v err=%v", values, more, err)
	}
}

func TestUserGroupListingIsBoundedAndCursorPaginated(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	now := time.Now().UTC()
	for _, id := range []domain.UserGroupID{"S1", "S2"} {
		if err := s.CreateUserGroup(ctx, domain.UserGroup{ID: id, WorkspaceID: "T1", Name: string(id), Handle: strings.ToLower(string(id)), Creator: "U1", UpdatedBy: "U1", CreatedAt: now, UpdatedAt: now, Enabled: true}, events.Event{ID: domain.EventID("event-" + string(id)), WorkspaceID: "T1", Topic: "usergroup.created", Payload: string(id), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListUserGroups(ctx, "T1", false, domain.PageRequest{Limit: 1})
	if err != nil || len(page.Groups) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = s.ListUserGroups(ctx, "T1", false, domain.PageRequest{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(page.Groups) != 1 || page.HasMore {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
}

func TestScheduledMessagesAreDurableAndRemovable(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	id, err := domain.NewScheduledMessageID()
	if err != nil {
		t.Fatal(err)
	}
	value := domain.ScheduledMessage{WorkspaceID: "T1", ID: id, Channel: "C1", Author: "U1", Text: "later", PostAt: time.Now().UTC().Add(time.Hour), CreatedAt: time.Now().UTC()}
	if err := s.CreateScheduledMessage(ctx, value, events.Event{ID: "scheduled-1", WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != id {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if err := s.DeleteScheduledMessage(ctx, "T1", "U1", "C1", id, events.Event{ID: "scheduled-2", WorkspaceID: "T1", Topic: "message.schedule_deleted", Payload: string(id), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}
