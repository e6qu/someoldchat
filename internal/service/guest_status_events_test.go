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

// TestEveryGuestStatusChangeJournalsUserGuestStatusChanged drives the product
// paths that change a guest status — admin.users.setRegular and setAdmin
// (SetUserRole, which the first-party admin page also calls) and deactivation
// (RemoveUser, which admin.users.remove and the admin page's Disable call) —
// and requires each to journal a record that translates to the reference's
// user_guest_status_changed, while the identity provider's role claim, which
// must not make a guest a member, journals none. The record reaches an app
// holding users:read and no app without it.
func TestEveryGuestStatusChangeJournalsUserGuestStatusChanged(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"}),
		repository.SeedUser(domain.User{ID: "UB", WorkspaceID: "T1", Name: "bot"}),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	if err := repository.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleOwner); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_758_000_000, 0).UTC()
	for index, guest := range []struct {
		id    domain.UserID
		ultra bool
	}{{"UG1", false}, {"UG2", true}, {"UG3", false}, {"UG4", false}} {
		created, err := events.New(domain.EventID("Ev-seed-"+string(rune('a'+index))), "T1", "U1", events.NewPayload("user.created", events.String("user_id", string(guest.id))), at)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.CreateUser(ctx, domain.User{ID: guest.id, WorkspaceID: "T1", Email: string(guest.id) + "@example.test", Name: strings.ToLower(string(guest.id))},
			domain.WorkspaceMembership{WorkspaceID: "T1", UserID: guest.id, Role: domain.WorkspaceRoleMember, Active: true, Restricted: !guest.ultra, UltraRestricted: guest.ultra}, created); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: repository}
	guestRecords := func() []events.Event {
		t.Helper()
		records, err := repository.ListEventsAfter(ctx, "T1", 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		var found []events.Event
		for _, record := range records {
			if record.Event.Topic == events.GuestStatusChangedTopic {
				found = append(found, record.Event)
			}
		}
		return found
	}
	inner := func(event events.Event) string {
		t.Helper()
		delivered, err := events.Broadcastable(event)
		if err != nil {
			t.Fatal(err)
		}
		inners, err := events.SlackInner(event.Topic, delivered, events.SurfaceEventsAPI)
		if err != nil || len(inners) != 1 || inners[0].Type() != "user_guest_status_changed" {
			t.Fatalf("inners=%v err=%v", inners, err)
		}
		encoded, err := inners[0].Encode()
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	// The identity provider describes UG3 as a member: it stays a guest and
	// nothing is journalled.
	if err := messages.SynchronizeExternalUserRole(ctx, "T1", "UG3", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	if membership, err := repository.GetWorkspaceMembership(ctx, "T1", "UG3"); err != nil || !membership.Restricted {
		t.Fatalf("a role claim ended a guest tier: %+v err=%v", membership, err)
	}
	if found := guestRecords(); len(found) != 0 {
		t.Fatalf("a role claim journalled %d guest status records", len(found))
	}

	// admin.users.setRegular on a multi-channel guest.
	if err := messages.SetUserRole(ctx, "T1", "U1", "UG1", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	found := guestRecords()
	if len(found) != 1 || found[0].ActorID != "U1" {
		t.Fatalf("records after setRegular = %+v", found)
	}
	if encoded := inner(found[0]); !strings.Contains(encoded, `"id":"UG1"`) || !strings.Contains(encoded, `"is_restricted":false`) || !strings.Contains(encoded, `"cache_ts":`) {
		t.Fatalf("setRegular inner = %s", encoded)
	}
	if user, err := messages.UserInfo(ctx, "T1", "U1", "UG1"); err != nil || user.Restricted || user.UltraRestricted {
		t.Fatalf("users.info after setRegular = %+v err=%v", user, err)
	}

	// admin.users.setAdmin on a guest.
	if err := messages.SetUserRole(ctx, "T1", "U1", "UG4", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if found := guestRecords(); len(found) != 2 || !strings.Contains(inner(found[1]), `"is_admin":true`) {
		t.Fatalf("records after setAdmin = %+v", found)
	}

	// Deactivating a single-channel guest.
	if err := messages.RemoveUser(ctx, "T1", "U1", "UG2"); err != nil {
		t.Fatal(err)
	}
	found = guestRecords()
	if len(found) != 3 {
		t.Fatalf("records after deactivation = %+v", found)
	}
	if encoded := inner(found[2]); !strings.Contains(encoded, `"deleted":true`) || !strings.Contains(encoded, `"is_ultra_restricted":true`) {
		t.Fatalf("deactivation inner = %s", encoded)
	}
	// user_change for the same deactivation now reports the guest tier too.
	records, err := repository.ListEventsAfter(ctx, "T1", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Event.Topic == "user.removed" && !strings.Contains(record.Event.Payload, `"is_ultra_restricted":true`) {
			t.Fatalf("user.removed snapshot lost the guest tier: %s", record.Event.Payload)
		}
	}

	// Delivery: users:read admits it, any other grant does not.
	if err := repository.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "UB", Name: "bot", UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-A1", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A1", BotID: "B1", TokenType: domain.TokenBot, Scopes: []string{"users:read"}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedToken(ctx, "xoxb-A2", domain.TokenRecord{WorkspaceID: "T1", UserID: "UB", AppID: "A2", BotID: "B2", TokenType: domain.TokenBot, Scopes: []string{"channels:read", "team:read"}}); err != nil {
		t.Fatal(err)
	}
	if _, visible, err := PrepareAppEvent(ctx, repository, appEventTestKey, "", "A1", events.Record{Sequence: 1, Event: found[2]}); err != nil || !visible {
		t.Fatalf("users:read app visible=%v err=%v", visible, err)
	}
	if _, visible, err := PrepareAppEvent(ctx, repository, appEventTestKey, "", "A2", events.Record{Sequence: 1, Event: found[2]}); err != nil || visible {
		t.Fatalf("an app without users:read received user_guest_status_changed: visible=%v err=%v", visible, err)
	}
}
