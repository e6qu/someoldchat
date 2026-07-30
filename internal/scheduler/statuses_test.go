package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestStatusWorkerExpiresOnceAcrossCompetingWorkers(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	if err := source.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	profile := domain.UserProfile{StatusText: "In a meeting", StatusEmoji: ":calendar:", StatusExpiration: due}
	if _, err := source.UpdateUserProfile(ctx, "T1", "U1", profile, events.Event{ID: "profile-set", WorkspaceID: "T1", Topic: "user.profile_changed", CreatedAt: due.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	first, err := NewStatusWorker(source, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStatusWorker(source, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := first.RunOnceAt(ctx, "T1", due); err != nil || count != 1 {
		t.Fatalf("first expiration count=%d err=%v", count, err)
	}
	if count, err := second.RunOnceAt(ctx, "T1", due); err != nil || count != 0 {
		t.Fatalf("competing expiration count=%d err=%v", count, err)
	}
	user, err := source.GetUser(ctx, "U1")
	if err != nil {
		t.Fatal(err)
	}
	if user.Profile.StatusText != "" || user.Profile.StatusEmoji != "" || !user.Profile.StatusExpiration.IsZero() {
		t.Fatalf("expired profile=%+v", user.Profile)
	}
	records, err := source.ListEventsAfter(ctx, "T1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].Event.Topic != "user.profile_changed" {
		t.Fatalf("expiration records=%+v", records)
	}
}

func TestStatusWorkerLeavesReplacementStatusAlone(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	_ = source.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"})
	_ = source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	due := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	old := domain.UserProfile{StatusText: "Old", StatusExpiration: due}
	if _, err := source.UpdateUserProfile(ctx, "T1", "U1", old, events.Event{ID: "old", WorkspaceID: "T1", Topic: "user.profile_changed"}); err != nil {
		t.Fatal(err)
	}
	observed, err := source.DueUserStatuses(ctx, "T1", due, 10)
	if err != nil || len(observed) != 1 {
		t.Fatalf("due profiles=%+v err=%v", observed, err)
	}
	replacement := domain.UserProfile{StatusText: "Replacement", StatusExpiration: due.Add(time.Hour)}
	if _, err := source.UpdateUserProfile(ctx, "T1", "U1", replacement, events.Event{ID: "replacement", WorkspaceID: "T1", Topic: "user.profile_changed"}); err != nil {
		t.Fatal(err)
	}
	expirationEvent, err := statusExpiredEvent(observed[0], due)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := source.ExpireUserStatus(ctx, "T1", "U1", observed[0].Profile.StatusExpiration, observed[0].Profile.ActiveScheduledStatusID, due, expirationEvent)
	if err != nil || changed {
		t.Fatalf("stale expiration changed=%t err=%v", changed, err)
	}
	user, _ := source.GetUser(ctx, "U1")
	if user.Profile.StatusText != "Replacement" || !user.Profile.StatusExpiration.Equal(due.Add(time.Hour)) {
		t.Fatalf("replacement profile=%+v", user.Profile)
	}
}

func TestProductWakeDeadlineIncludesStatusExpiration(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	if err := source.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	profile := domain.UserProfile{StatusText: "Until noon", StatusExpiration: expires}
	event := events.Event{ID: "status-wake", WorkspaceID: "T1", Topic: "user.profile_changed", CreatedAt: expires.Add(-time.Hour)}
	if _, err := source.UpdateUserProfile(ctx, "T1", "U1", profile, event); err != nil {
		t.Fatal(err)
	}
	publisher := &recordingProductDeadline{fence: 11}
	if err := PublishEarliestProductWakeDeadlineWithStatuses(ctx, source, source, source, publisher); err != nil {
		t.Fatal(err)
	}
	if publisher.publishedFence != 11 || !publisher.deadline.Equal(expires) {
		t.Fatalf("published fence=%d deadline=%s, want fence 11 and %s", publisher.publishedFence, publisher.deadline, expires)
	}
}
