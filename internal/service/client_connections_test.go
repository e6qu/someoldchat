package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// The presence_change an RTM client receives names what others see, active
// or away: a member's first client brings them online, their last takes them
// offline, and choosing automatic presence while connected reads active,
// never Slack's setting name auto. A change that changes nothing is not
// announced.
func TestPresenceChangesAnnounceWhatOthersSee(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "ada"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s}
	head, err := s.LatestEventSequence(ctx, "T1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := messages.OpenClientConnection(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := messages.OpenClientConnection(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetUserPresence(ctx, "T1", "U1", domain.PresenceAway); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetUserPresence(ctx, "T1", "U1", domain.PresenceAuto); err != nil {
		t.Fatal(err)
	}
	if err := messages.CloseClientConnection(ctx, "T1", "U1", first.ID); err != nil {
		t.Fatal(err)
	}
	if err := messages.CloseClientConnection(ctx, "T1", "U1", second.ID); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListEventsAfter(ctx, "T1", head, 100)
	if err != nil {
		t.Fatal(err)
	}
	var frames []string
	for _, record := range records {
		delivered, err := events.Deliverable(record.Event)
		if err != nil {
			t.Fatal(err)
		}
		inners, err := events.RTMEvents(record.Event.Topic, delivered)
		if err != nil {
			t.Fatal(err)
		}
		for _, inner := range inners {
			frame, err := inner.Encode()
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, frame)
		}
	}
	want := []string{`"presence":"active"`, `"presence":"away"`, `"presence":"active"`, `"presence":"away"`}
	if len(frames) != len(want) {
		t.Fatalf("frames=%v, want %d presence changes: online, away, back, offline", frames, len(want))
	}
	for index, fragment := range want {
		if !strings.Contains(frames[index], fragment) || !strings.Contains(frames[index], `"type":"presence_change"`) {
			t.Fatalf("frame %d=%s, want %s", index, frames[index], fragment)
		}
	}
	if user, err := s.GetUser(ctx, "U1"); err != nil || user.PresenceAt(time.Now()) != "away" {
		t.Fatalf("presence after every client closed=%q err=%v", user.PresenceAt(time.Now()), err)
	}
}

// A member whose account has been deactivated cannot hold a connection: they
// cannot open one, and one opened before is neither renewed nor closed on
// their word, so it lapses with its lease. Their record is still there, so the
// store alone does not refuse them.
func TestAFormerMemberHoldsNoConnection(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "UF", WorkspaceID: "T1", Email: "former@example.com", Name: "former"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.OpenClientConnection(ctx, domain.ClientConnection{ID: "cc-before", WorkspaceID: "T1", UserID: "UF", ExpiresAt: now.Add(time.Minute)}, now, events.Event{}); err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(-time.Hour).Truncate(time.Second)
	if err := s.SetUserExpiration(ctx, "T1", "UF", expiry, events.Event{ID: "E-expiry", WorkspaceID: "T1", Topic: "user.expiration_set", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if expired, err := s.ExpireUserAccount(ctx, "T1", "UF", expiry, events.Event{ID: "E-expired", WorkspaceID: "T1", Topic: "user.deactivated", CreatedAt: now}); err != nil || !expired {
		t.Fatalf("expired=%v err=%v", expired, err)
	}
	messages := Messages{Store: s}
	if _, err := messages.OpenClientConnection(ctx, "T1", "UF"); err == nil {
		t.Fatal("a former member opened a connection")
	}
	if _, err := messages.RenewClientConnection(ctx, "T1", "UF", "cc-before"); err == nil {
		t.Fatal("a former member renewed a connection")
	}
	if err := messages.CloseClientConnection(ctx, "T1", "UF", "cc-before"); err == nil {
		t.Fatal("a former member closed a connection")
	}
	if _, err := messages.ClientConnectionCount(ctx, "T1", "UF"); err == nil {
		t.Fatal("a former member read their connections")
	}
	if count, err := s.CountClientConnections(ctx, "T1", "UF", now); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v, want the earlier lease untouched", count, err)
	}
}
