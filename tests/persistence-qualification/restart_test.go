package qualification

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The restart contracts. Every other contract in this package opens its target
// once and reads back through the same handle, which proves a write was
// committed but not that it outlived the process. These drop the handle and
// open a new one.
//
// What each asserts is chosen from what specs/persistence.md promises:
// committed state survives, unfinished outbox work is replayable, and stale
// writers are fenced after recovery.

// restartFixture is newFixture plus the reopen. It returns the seeded world and
// a function that closes the current handle and hands back a fresh one.
type restartFixture struct {
	fixture
	restart restarter
}

func newRestartFixture(t *testing.T, ctx context.Context, open restartOpener) (restartFixture, func()) {
	t.Helper()
	repository, restart, closeRepository := open(t, ctx)
	if restart == nil {
		t.Skip("this profile does not qualify restart survival here: the in-memory store cannot outlive its process, and dqlite is qualified by real node failure in tests/dqlite-qualification")
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	value := restartFixture{
		fixture: fixture{
			repository:  repository,
			workspaceID: domain.WorkspaceID("T-restart-" + suffix),
			userID:      domain.UserID("U-restart-" + suffix),
			channelID:   domain.ConversationID("C-restart-" + suffix),
			suffix:      suffix,
		},
		restart: restart,
	}
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: value.workspaceID, Name: "Restart"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: value.userID, WorkspaceID: value.workspaceID, Email: "restart-" + suffix + "@example.com", Name: "restart"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversation(ctx, domain.Conversation{ID: value.channelID, WorkspaceID: value.workspaceID, Name: "restart-" + suffix}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversationMember(ctx, value.channelID, value.userID); err != nil {
		t.Fatal(err)
	}
	return value, closeRepository
}

// reopen drops the handle everything so far was written through and returns a
// new one. The old handle must not be used afterwards.
func (f *restartFixture) reopen(t *testing.T, ctx context.Context) {
	t.Helper()
	f.repository = f.restart(t, ctx)
}

func committedStateSurvivesARestart(t *testing.T, open restartOpener) {
	ctx := context.Background()
	f, closeRepository := newRestartFixture(t, ctx, open)
	defer closeRepository()

	posted := f.message(t, ctx, "restart-message", time.Unix(1_700_000_000, 0).UTC())
	timestamp := domain.NewMessageTimestamp(posted.CreatedAt)
	if err := f.repository.SetReadCursor(ctx, domain.ReadCursor{
		WorkspaceID: f.workspaceID, UserID: f.userID, Conversation: f.channelID,
		LastRead: timestamp, UpdatedAt: time.Unix(1_700_000_050, 0).UTC(),
	}, f.event("restart-cursor", "read_cursor.set", string(f.channelID))); err != nil {
		t.Fatal(err)
	}

	f.reopen(t, ctx)

	page, err := f.repository.ListMessages(ctx, f.channelID, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != posted.ID {
		t.Fatalf("messages=%+v err=%v, want the committed message to have survived", page.Messages, err)
	}
	if page.Messages[0].Text != posted.Text {
		t.Fatalf("text=%q, want %q — the row survived but its content did not", page.Messages[0].Text, posted.Text)
	}
	cursor, err := f.repository.GetReadCursor(ctx, f.workspaceID, f.userID, f.channelID)
	if err != nil || cursor.LastRead != timestamp {
		t.Fatalf("cursor=%+v err=%v, want the read position to have survived", cursor, err)
	}
	// A membership the seed wrote is still a membership: the whole world, not
	// just the last write, has to come back.
	members, err := f.repository.ListConversationMembers(ctx, f.channelID, domain.PageRequest{Limit: 10})
	if err != nil || len(members.Users) != 1 || members.Users[0].ID != f.userID {
		t.Fatalf("members=%+v err=%v", members.Users, err)
	}
}

// specs/persistence.md: "unfinished outbox work MUST be replayable". Every
// existing replay test models the crash as a lease expiring on a live store.
// This one drops the store instead.
func unfinishedOutboxWorkSurvivesARestart(t *testing.T, open restartOpener) {
	ctx := context.Background()
	f, closeRepository := newRestartFixture(t, ctx, open)
	defer closeRepository()

	f.message(t, ctx, "restart-outbox", time.Unix(1_700_000_100, 0).UTC())
	claimed, err := f.repository.ClaimEvents(ctx, f.workspaceID, "worker-before-restart", 10, time.Minute)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("claimed=%d err=%v, want the message's event to be claimable", len(claimed), err)
	}
	sequences := make([]uint64, 0, len(claimed))
	for _, record := range claimed {
		sequences = append(sequences, record.Sequence)
	}

	// The process that held this lease is gone. The claim is durable, so the
	// restart must change nothing about who owns the work.
	f.reopen(t, ctx)

	// Nobody else may take it while the lease stands — otherwise two workers
	// deliver the same event after every restart.
	if again, err := f.repository.ClaimEvents(ctx, f.workspaceID, "another-worker", 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("claimed=%d err=%v, want the surviving lease to fence a different worker", len(again), err)
	}
	// And the owner still owns it. Worker identities are explicit configuration
	// (cmd/worker's required -owner), not process-random, precisely so a
	// restarted worker resumes its own in-flight batch instead of orphaning it
	// under a new name until the lease lapses.
	if err := f.repository.RenewEvents(ctx, "worker-before-restart", sequences, time.Minute); err != nil {
		t.Fatalf("the owner could not renew its own lease after a restart: %v", err)
	}
	if err := f.repository.AckEvents(ctx, "worker-before-restart", sequences); err != nil {
		t.Fatalf("the owner could not finish its own work after a restart: %v", err)
	}
	// Acknowledged work is not replayed: at-least-once must not become
	// always-again just because the process bounced.
	if replayed, err := f.repository.ClaimEvents(ctx, f.workspaceID, "another-worker", 10, time.Minute); err != nil || len(replayed) != 0 {
		t.Fatalf("claimed=%d err=%v, want acknowledged work to stay acknowledged", len(replayed), err)
	}
}

// Policy is the state whose loss is silent: nothing fails, the workspace just
// quietly stops enforcing what an administrator set. Retention is the sharpest
// case — a lost policy means content that should be deleted is kept forever,
// and nothing anywhere reports it.
func policyStateSurvivesARestart(t *testing.T, open restartOpener) {
	ctx := context.Background()
	f, closeRepository := newRestartFixture(t, ctx, open)
	defer closeRepository()

	if err := f.repository.SetRetentionPolicy(ctx, f.workspaceID, domain.RetentionPolicy{MessageDays: 90, FileDays: 30},
		f.event("restart-retention", "retention.policy_changed", string(f.workspaceID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SetConversationRetention(ctx, f.workspaceID, f.channelID, 7, time.Unix(1_700_000_200, 0).UTC(),
		f.event("restart-override", "retention.policy_changed", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SetWorkspaceNotificationPreferences(ctx, domain.WorkspaceNotificationPreferences{
		WorkspaceID: f.workspaceID, UserID: f.userID, Level: domain.NotificationAll,
		ActivityChannels: true, ActivityReminders: true, BrowserNotifications: true,
	}, f.event("restart-notifications", "notification.preferences_changed", string(f.userID))); err != nil {
		t.Fatal(err)
	}

	f.reopen(t, ctx)

	policy, err := f.repository.GetRetentionPolicy(ctx, f.workspaceID)
	if err != nil || policy.MessageDays != 90 || policy.FileDays != 30 {
		t.Fatalf("policy=%+v err=%v, want the retention policy to have survived", policy, err)
	}
	override, err := f.repository.GetConversationRetention(ctx, f.workspaceID, f.channelID)
	if err != nil || override.DurationDays != 7 {
		t.Fatalf("override=%+v err=%v, want the channel's own limit to have survived", override, err)
	}
	preferences, err := f.repository.GetWorkspaceNotificationPreferences(ctx, f.workspaceID, f.userID)
	if err != nil || preferences.Level != domain.NotificationAll || !preferences.BrowserNotifications {
		t.Fatalf("preferences=%+v err=%v", preferences, err)
	}
}
