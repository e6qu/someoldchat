package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestScheduledStatusWorkerActivatesOnceAcrossCompetingWorkers(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	_ = source.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"})
	_ = source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	start := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	value := domain.ScheduledStatus{
		ID: "scheduled_status_1", WorkspaceID: "T1", UserID: "U1", StatusText: "Lunch", StatusEmoji: ":sandwich:",
		StartsAt: start, EndsAt: start.Add(time.Hour), CreatedAt: start.Add(-time.Hour), UpdatedAt: start.Add(-time.Hour),
	}
	if err := source.CreateScheduledStatus(ctx, value); err != nil {
		t.Fatal(err)
	}
	first, _ := NewScheduledStatusWorker(source, 10)
	second, _ := NewScheduledStatusWorker(source, 10)
	if count, err := first.RunOnceAt(ctx, "T1", start); err != nil || count != 1 {
		t.Fatalf("first count=%d err=%v", count, err)
	}
	if count, err := second.RunOnceAt(ctx, "T1", start); err != nil || count != 0 {
		t.Fatalf("second count=%d err=%v", count, err)
	}
	user, err := source.GetUser(ctx, "U1")
	if err != nil || user.Profile.StatusText != "Lunch" || user.Profile.StatusEmoji != ":sandwich:" || !user.Profile.StatusExpiration.Equal(start.Add(time.Hour)) {
		t.Fatalf("activated user=%+v err=%v", user, err)
	}
	if values, err := source.ListScheduledStatuses(ctx, "T1", "U1"); err != nil || len(values) != 0 {
		t.Fatalf("remaining=%+v err=%v", values, err)
	}
}

func TestScheduledStatusCompareAndSetRejectsAnEditedRevision(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	_ = source.SeedWorkspace(domain.Workspace{ID: "T1"})
	_ = source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	start := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	original := domain.ScheduledStatus{ID: "scheduled_status_1", WorkspaceID: "T1", UserID: "U1", StatusText: "Old", StartsAt: start, EndsAt: start.Add(time.Hour), CreatedAt: start.Add(-time.Hour), UpdatedAt: start.Add(-time.Hour)}
	if err := source.CreateScheduledStatus(ctx, original); err != nil {
		t.Fatal(err)
	}
	observed, err := source.DueScheduledStatuses(ctx, "T1", start, 10)
	if err != nil || len(observed) != 1 {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	edited := original
	edited.StatusText = "Edited"
	edited.StartsAt = start.Add(time.Hour)
	edited.EndsAt = start.Add(2 * time.Hour)
	edited.UpdatedAt = start
	if err := source.UpdateScheduledStatus(ctx, edited); err != nil {
		t.Fatal(err)
	}
	staleUser, _ := source.GetUser(ctx, "U1")
	event, _ := scheduledStatusEvent(observed[0], staleUser, start)
	changed, err := source.ActivateScheduledStatus(ctx, "T1", "U1", original.ID, observed[0].UpdatedAt, start, event)
	if err != nil || changed {
		t.Fatalf("stale revision changed=%t err=%v", changed, err)
	}
	user, _ := source.GetUser(ctx, "U1")
	if user.Profile.StatusText != "" {
		t.Fatalf("stale status activated: %+v", user.Profile)
	}
}

func TestScheduledStatusExpiryCannotClearAManualReplacementWithTheSameDeadline(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	_ = source.SeedWorkspace(domain.Workspace{ID: "T1"})
	_ = source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	start := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	value := domain.ScheduledStatus{ID: "scheduled_status_1", WorkspaceID: "T1", UserID: "U1", StatusText: "Scheduled", StartsAt: start, EndsAt: end, CreatedAt: start.Add(-time.Hour), UpdatedAt: start.Add(-time.Hour)}
	if err := source.CreateScheduledStatus(ctx, value); err != nil {
		t.Fatal(err)
	}
	worker, _ := NewScheduledStatusWorker(source, 10)
	if count, err := worker.RunOnceAt(ctx, "T1", start); err != nil || count != 1 {
		t.Fatalf("activation count=%d err=%v", count, err)
	}
	observed, err := source.DueUserStatuses(ctx, "T1", end, 10)
	if err != nil || len(observed) != 1 || observed[0].Profile.ActiveScheduledStatusID != value.ID {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	replacement := observed[0].Profile
	replacement.StatusText = "Manual replacement"
	if _, err := source.UpdateUserProfile(ctx, "T1", "U1", replacement, events.Event{ID: "replacement", WorkspaceID: "T1", Topic: "user.profile_changed"}); err != nil {
		t.Fatal(err)
	}
	expirationEvent, _ := statusExpiredEvent(observed[0], end)
	changed, err := source.ExpireUserStatus(ctx, "T1", "U1", end, value.ID, end, expirationEvent)
	if err != nil || changed {
		t.Fatalf("stale scheduled expiry changed=%t err=%v", changed, err)
	}
	user, _ := source.GetUser(ctx, "U1")
	if user.Profile.StatusText != "Manual replacement" || user.Profile.ActiveScheduledStatusID != "" {
		t.Fatalf("replacement profile=%+v", user.Profile)
	}
}

func TestScheduledStatusWorkerConsumesMissedStatusWithoutApplyingIt(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	_ = source.SeedWorkspace(domain.Workspace{ID: "T1"})
	_ = source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	start := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	value := domain.ScheduledStatus{ID: "scheduled_status_1", WorkspaceID: "T1", UserID: "U1", StatusText: "Brief", StartsAt: start, EndsAt: start.Add(time.Minute), CreatedAt: start.Add(-time.Hour), UpdatedAt: start.Add(-time.Hour)}
	if err := source.CreateScheduledStatus(ctx, value); err != nil {
		t.Fatal(err)
	}
	worker, _ := NewScheduledStatusWorker(source, 10)
	if count, err := worker.RunOnceAt(ctx, "T1", start.Add(2*time.Minute)); err != nil || count != 1 {
		t.Fatalf("missed count=%d err=%v", count, err)
	}
	user, _ := source.GetUser(ctx, "U1")
	if user.Profile.StatusText != "" {
		t.Fatalf("expired scheduled status activated: %+v", user.Profile)
	}
}

func TestCompleteProductWakeDeadlineIncludesScheduledStatusStart(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	_ = source.SeedWorkspace(domain.Workspace{ID: "T1"})
	_ = source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	start := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	value := domain.ScheduledStatus{ID: "scheduled_status_1", WorkspaceID: "T1", UserID: "U1", StatusText: "Lunch", StartsAt: start, EndsAt: start.Add(time.Hour), CreatedAt: start.Add(-time.Hour), UpdatedAt: start.Add(-time.Hour)}
	if err := source.CreateScheduledStatus(ctx, value); err != nil {
		t.Fatal(err)
	}
	publisher := &recordingProductDeadline{fence: 12}
	if err := PublishEarliestProductWakeDeadlineComplete(ctx, source, source, source, source, source, publisher); err != nil {
		t.Fatal(err)
	}
	if publisher.publishedFence != 12 || !publisher.deadline.Equal(start) {
		t.Fatalf("published fence=%d deadline=%s", publisher.publishedFence, publisher.deadline)
	}
}
