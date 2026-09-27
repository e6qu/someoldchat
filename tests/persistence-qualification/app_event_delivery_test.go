package qualification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// appEventDeliveryStateIsPerRecord holds every profile to one contract: an app
// event's lease, retry schedule, attempt count and accepted callbacks belong
// to that record, so a record waiting for a retry never holds back the records
// after it. The state used to be one position per (app, surface), and one
// failing callback parked the app's whole stream until its retries ran out.
func appEventDeliveryStateIsPerRecord(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	appID := domain.AppID("A-records-" + f.suffix)
	now := time.Now().UTC()
	if err := f.repository.CreateApp(ctx, domain.App{
		ID: appID, DevelopmentWorkspaceID: f.workspaceID, OwnerID: f.userID, Name: "Records", ClientID: "client-records-" + f.suffix,
		SigningSecretHash: "signing", SigningSecretCiphertext: "cipher", VerificationTokenHash: "verify",
		VerificationTokenCiphertext: "cipher", ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: appID, Version: 1, CreatedBy: f.userID, CreatedAt: now,
		Manifest: `{"display_information":{"name":"Records"}}`,
	}, domain.OAuthClient{ID: "client-records-" + f.suffix, SecretHash: "secret", AppID: appID}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: appID, WorkspaceID: f.workspaceID, Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second", "third"} {
		if err := f.repository.AppendEvent(ctx, f.event("delivery-"+name, "reaction.added", `{"type":"reaction.added"}`)); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(owner string) (events.AppEventClaim, bool) {
		t.Helper()
		claimed, found, err := f.repository.ClaimAppEvent(ctx, appID, "http", owner, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return claimed, found
	}
	eventName := func(claimed events.AppEventClaim) domain.EventID {
		return claimed.Record.Event.ID
	}

	// Two workers hold two different records at once.
	first, found := claim("worker-a")
	if !found || eventName(first) != domain.EventID("delivery-first-"+f.suffix) || first.Attempt != 0 {
		t.Fatalf("first claim=%+v found=%v", first, found)
	}
	second, found := claim("worker-b")
	if !found || eventName(second) != domain.EventID("delivery-second-"+f.suffix) {
		t.Fatalf("a held lease blocked the next record: claim=%+v found=%v", second, found)
	}
	if err := f.repository.AckAppEvent(ctx, appID, "http", "worker-a", second.Record.Sequence); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("a worker acknowledged another worker's record: %v", err)
	}

	// The first record fails and waits for its retry; the rest flow past it.
	retryAt := time.Now().UTC().Add(1500 * time.Millisecond)
	if err := f.repository.ReleaseAppEvent(ctx, appID, "http", "worker-a", first.Record.Sequence, events.AppEventRelease{
		Reason: "http_error", RetryAt: retryAt, Delivered: []string{"cb-2", "cb-1", "cb-1", " "},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.AckAppEvent(ctx, appID, "http", "worker-b", second.Record.Sequence); err != nil {
		t.Fatal(err)
	}
	third, found := claim("worker-a")
	if !found || eventName(third) != domain.EventID("delivery-third-"+f.suffix) {
		t.Fatalf("a record waiting for its retry held back the next one: claim=%+v found=%v", third, found)
	}
	if err := f.repository.AckAppEvent(ctx, appID, "http", "worker-a", third.Record.Sequence); err != nil {
		t.Fatal(err)
	}
	if waiting, found := claim("worker-a"); found {
		t.Fatalf("a record was claimed before its retry was due: %+v", waiting)
	}
	cursor, err := f.repository.GetAppEventCursor(ctx, appID, "http")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Pending != 1 || cursor.AcknowledgedSequence != first.Record.Sequence-1 || cursor.RetryCount != 1 ||
		cursor.RetryReason != "http_error" || !cursor.RetryAt.After(now) || cursor.InFlightSequence != 0 {
		t.Fatalf("cursor=%+v, want one record waiting for its first retry below the settled position", cursor)
	}

	// Once due, the record comes back with its own attempt count, reason and
	// accepted callbacks. An internal failure returns it without spending an
	// attempt or replacing the Slack reason.
	time.Sleep(time.Until(retryAt))
	retried, found := claim("worker-c")
	if !found || retried.Record.Sequence != first.Record.Sequence || retried.Attempt != 1 || retried.RetryReason != "http_error" ||
		len(retried.Delivered) != 2 || retried.Delivered[0] != "cb-1" || retried.Delivered[1] != "cb-2" {
		t.Fatalf("retried claim=%+v found=%v", retried, found)
	}
	if err := f.repository.ReleaseAppEvent(ctx, appID, "http", "worker-c", retried.Record.Sequence, events.AppEventRelease{
		Reason: "event_projection_failed", RetryAt: now.Add(-time.Second), Internal: true, Delivered: retried.Delivered,
	}); err != nil {
		t.Fatal(err)
	}
	again, found := claim("worker-c")
	if !found || again.Attempt != 1 || again.RetryReason != "http_error" || len(again.Delivered) != 2 {
		t.Fatalf("an internal failure changed the record's Slack delivery state: %+v found=%v", again, found)
	}
	if err := f.repository.AckAppEvent(ctx, appID, "http", "worker-c", again.Record.Sequence); err != nil {
		t.Fatal(err)
	}
	cursor, err = f.repository.GetAppEventCursor(ctx, appID, "http")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Pending != 0 || cursor.AcknowledgedSequence < third.Record.Sequence {
		t.Fatalf("cursor after settling everything=%+v", cursor)
	}
	attempts, err := f.repository.ListAppDeliveryAttempts(ctx, appID, "http", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 5 || !attempts[0].Delivered || attempts[1].Reason != "event_projection_failed" {
		t.Fatalf("attempt history=%+v", attempts)
	}
}
