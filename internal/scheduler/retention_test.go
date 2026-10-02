package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func retentionStore(t *testing.T) *memory.Store {
	t.Helper()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	return store
}

func seedRetentionMessage(t *testing.T, store *memory.Store, id domain.MessageID, at time.Time) {
	t.Helper()
	message := domain.Message{
		ID: id, WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1",
		Text: string(id), Attachments: "[]", CreatedAt: domain.MessageInstant(at),
	}
	event := events.Event{
		ID: domain.EventID("E-" + id), WorkspaceID: "T1", Topic: "message.created",
		Payload: `{"type":"message.created","message_id":"` + string(id) + `"}`, CreatedAt: at,
	}
	if err := store.CreateMessage(context.Background(), message, event, ""); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// A workspace nobody has configured keeps everything. Retention that deleted by
// default would destroy content on upgrade, which is the one outcome a
// retention feature must never have.
func TestRetentionWorkerDeletesNothingWithoutAPolicy(t *testing.T) {
	store := retentionStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	seedRetentionMessage(t, store, "M-ancient", now.Add(-5000*24*time.Hour))

	worker, err := NewRetentionWorker(store, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnceAt(ctx, "T1", now); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListMessages(ctx, "C1", domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("messages=%d err=%v, want the unconfigured workspace to keep everything", len(page.Messages), err)
	}
}

// The whole point: with a policy, expired content is actually gone, and the
// sweep announces what it did once rather than once per message.
func TestRetentionWorkerDeletesExpiredContentAndAnnouncesItOnce(t *testing.T) {
	store := retentionStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	for index, at := range []time.Time{
		now.Add(-200 * 24 * time.Hour),
		now.Add(-150 * 24 * time.Hour),
		now.Add(-10 * 24 * time.Hour),
	} {
		seedRetentionMessage(t, store, domain.MessageID("M-"+string(rune('a'+index))), at)
	}
	if err := store.SetRetentionPolicy(ctx, "T1", domain.RetentionPolicy{MessageDays: 90}, events.Event{
		ID: "E-policy", WorkspaceID: "T1", Topic: "retention.policy_changed",
		Payload: `{"type":"retention.policy_changed"}`, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	worker, err := NewRetentionWorker(store, 50)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := worker.RunOnceAt(ctx, "T1", now)
	if err != nil || completed != 1 {
		t.Fatalf("completed=%d err=%v, want the one conversation swept", completed, err)
	}
	page, err := store.ListMessages(ctx, "C1", domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("messages=%+v err=%v, want only the one inside the horizon", page.Messages, err)
	}

	records, err := store.ListEventsAfter(ctx, "T1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	swept := 0
	for _, record := range records {
		if record.Event.Topic == "retention.swept" {
			swept++
			// The sweep is the workspace's own policy applying itself, not
			// something a person did.
			if record.Event.ActorID != "" {
				t.Fatalf("a retention sweep was attributed to %q", record.Event.ActorID)
			}
		}
	}
	if swept != 1 {
		t.Fatalf("retention.swept announced %d times for one sweep of one conversation", swept)
	}
}

// The watermark is the claim, so a second pass inside the interval does
// nothing — that is what makes the daily cadence and what stops two replicas
// doing the same deletion.
func TestRetentionWorkerSweepsAConversationOncePerInterval(t *testing.T) {
	store := retentionStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	if err := store.SetRetentionPolicy(ctx, "T1", domain.RetentionPolicy{MessageDays: 90}, events.Event{
		ID: "E-policy", WorkspaceID: "T1", Topic: "retention.policy_changed",
		Payload: `{"type":"retention.policy_changed"}`, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := NewRetentionWorker(store, 50)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRetentionWorker(store, 50)
	if err != nil {
		t.Fatal(err)
	}
	firstCount, err := first.RunOnceAt(ctx, "T1", now)
	if err != nil || firstCount != 1 {
		t.Fatalf("first=%d err=%v", firstCount, err)
	}
	secondCount, err := second.RunOnceAt(ctx, "T1", now.Add(time.Hour))
	if err != nil || secondCount != 0 {
		t.Fatalf("second=%d err=%v, want the watermark to hold it off", secondCount, err)
	}
	// A day later it is due again.
	thirdCount, err := second.RunOnceAt(ctx, "T1", now.Add(RetentionInterval+time.Minute))
	if err != nil || thirdCount != 1 {
		t.Fatalf("third=%d err=%v, want the conversation due again", thirdCount, err)
	}
}

// A per-channel override beats the workspace default, which is the whole
// purpose of admin.conversations.setCustomRetention.
func TestRetentionWorkerHonoursAConversationOverride(t *testing.T) {
	store := retentionStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	seedRetentionMessage(t, store, "M-thirty", now.Add(-30*24*time.Hour))
	policyEvent := events.Event{
		ID: "E-policy", WorkspaceID: "T1", Topic: "retention.policy_changed",
		Payload: `{"type":"retention.policy_changed"}`, CreatedAt: now,
	}
	// The workspace would keep this message; the channel would not.
	if err := store.SetRetentionPolicy(ctx, "T1", domain.RetentionPolicy{MessageDays: 365}, policyEvent); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConversationRetention(ctx, "T1", "C1", 7, now, events.Event{
		ID: "E-override", WorkspaceID: "T1", Topic: "retention.policy_changed",
		Payload: `{"type":"retention.policy_changed"}`, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewRetentionWorker(store, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnceAt(ctx, "T1", now); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListMessages(ctx, "C1", domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("messages=%+v err=%v, want the channel's stricter override to have applied", page.Messages, err)
	}
}

// Canvas and list retention is the workspace's own daily pass: a canvas left
// unedited past the duration is deleted and announced once, a pass across all
// workspaces reaches it, and a second run inside the interval does nothing.
func TestRetentionWorkerDeletesUneditedCanvasesOncePerInterval(t *testing.T) {
	ctx := context.Background()
	store := retentionStore(t)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for id, edited := range map[domain.CanvasID]time.Time{"Fstale": now.AddDate(0, 0, -91), "Ffresh": now.AddDate(0, 0, -89)} {
		if err := store.CreateCanvas(ctx, domain.Canvas{ID: id, WorkspaceID: "T1", OwnerID: "U1", Title: string(id), DocumentContent: "{}", Version: 1, CreatedAt: edited, UpdatedAt: edited},
			events.Event{ID: domain.EventID("E-" + id), WorkspaceID: "T1", Topic: "canvas.created", Payload: "{}", CreatedAt: edited}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetRetentionPolicy(ctx, "T1", domain.RetentionPolicy{CanvasListDays: 90},
		events.Event{ID: "E-policy", WorkspaceID: "T1", Topic: "retention.policy_changed", Payload: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewRetentionWorker(store, 10)
	if err != nil {
		t.Fatal(err)
	}
	announced := func() int {
		count := 0
		for _, event := range store.Outbox() {
			if event.Topic == "retention.documents_swept" {
				count++
			}
		}
		return count
	}
	if _, err := worker.RunOnceAt(ctx, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCanvas(ctx, "T1", "Fstale"); err == nil {
		t.Fatal("a canvas unedited past the duration survived the sweep")
	}
	if _, err := store.GetCanvas(ctx, "T1", "Ffresh"); err != nil {
		t.Fatalf("a canvas edited within the duration was deleted: %v", err)
	}
	if announced() != 1 {
		t.Fatalf("the sweep was announced %d times, want once", announced())
	}
	if err := store.CreateCanvas(ctx, domain.Canvas{ID: "Flater", WorkspaceID: "T1", OwnerID: "U1", Title: "later", DocumentContent: "{}", Version: 1, CreatedAt: now.AddDate(-1, 0, 0), UpdatedAt: now.AddDate(-1, 0, 0)},
		events.Event{ID: "E-later", WorkspaceID: "T1", Topic: "canvas.created", Payload: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnceAt(ctx, "", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCanvas(ctx, "T1", "Flater"); err != nil || announced() != 1 {
		t.Fatalf("a second run inside the interval swept again: %v, %d announcements", err, announced())
	}
	if _, err := worker.RunOnceAt(ctx, "", now.Add(RetentionInterval+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCanvas(ctx, "T1", "Flater"); err == nil || announced() != 2 {
		t.Fatalf("the next day's pass did not sweep: %v, %d announcements", err, announced())
	}
}
