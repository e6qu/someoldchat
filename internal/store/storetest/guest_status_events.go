package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// GuestStatusRepository is the part of store.Store the guest status check
// drives.
type GuestStatusRepository interface {
	CreateUser(context.Context, domain.User, domain.WorkspaceMembership, events.Event) error
	GetWorkspaceMembership(context.Context, domain.WorkspaceID, domain.UserID) (domain.WorkspaceMembership, error)
	AssignWorkspaceRole(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkspaceRole, events.Event) error
	SetWorkspaceRole(context.Context, domain.WorkspaceID, domain.UserID, domain.WorkspaceRole, events.Event) error
	SetUserDeleted(context.Context, domain.WorkspaceID, domain.UserID, bool, events.Event) error
	SetUserExpiration(context.Context, domain.WorkspaceID, domain.UserID, time.Time, events.Event) error
	ExpireUserAccount(context.Context, domain.WorkspaceID, domain.UserID, time.Time, events.Event) (bool, error)
	ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error)
}

// CheckGuestStatusEventsCommitWithTheChange requires every mutation that
// changes a member's guest status to journal user.guest_status_changed in the
// same transaction, describing the member as the change leaves them, and every
// other mutation not to: ending a guest tier by an administrative role
// assignment, deactivating a guest by hand, and a guest account lapsing all
// do; deactivating a full member, deactivating an account twice, and
// assigning a member a role do not. A guest still cannot be promoted through
// SetWorkspaceRole, the identity provider's path. The repository must hold
// workspace T1.
func CheckGuestStatusEventsCommitWithTheChange(t *testing.T, repository GuestStatusRepository) {
	t.Helper()
	ctx := context.Background()
	at := time.Unix(1_758_000_000, 0).UTC()
	sequence := 0
	event := func(topic string, user domain.UserID) events.Event {
		t.Helper()
		sequence++
		value, err := events.New(domain.EventID("Ev-guest-"+string(rune('a'+sequence))), "T1", "UADMIN",
			events.NewPayload(topic, events.String("user_id", string(user))), at)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, seed := range []struct {
		id                domain.UserID
		restricted, ultra bool
	}{{"UG1", true, false}, {"UG2", false, true}, {"UG3", true, false}, {"UG4", true, false}, {"UM1", false, false}, {"UM2", false, false}} {
		user := domain.User{ID: seed.id, WorkspaceID: "T1", Email: string(seed.id) + "@example.test", Name: string(seed.id)}
		membership := domain.WorkspaceMembership{WorkspaceID: "T1", UserID: seed.id, Role: domain.WorkspaceRoleMember, Active: true, Restricted: seed.restricted, UltraRestricted: seed.ultra}
		if err := repository.CreateUser(ctx, user, membership, event("user.created", seed.id)); err != nil {
			t.Fatal(err)
		}
	}
	guestEvents := func() []map[string]any {
		t.Helper()
		records, err := repository.ListEventsAfter(ctx, "T1", 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		var found []map[string]any
		for _, record := range records {
			if record.Event.Topic != events.GuestStatusChangedTopic {
				continue
			}
			var payload struct {
				CacheTS int64          `json:"cache_ts"`
				User    map[string]any `json:"user"`
			}
			if err := json.Unmarshal([]byte(record.Event.Payload), &payload); err != nil {
				t.Fatal(err)
			}
			if record.Event.ActorID != "UADMIN" || payload.CacheTS != at.Unix() {
				t.Fatalf("guest status record %+v carries the wrong actor or cache_ts", record.Event)
			}
			found = append(found, payload.User)
		}
		return found
	}

	// The identity provider's path still refuses a guest.
	if err := repository.SetWorkspaceRole(ctx, "T1", "UG1", domain.WorkspaceRoleAdmin, event("workspace.role_changed", "UG1")); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("SetWorkspaceRole promoted a guest: err=%v", err)
	}
	if found := guestEvents(); len(found) != 0 {
		t.Fatalf("a refused promotion journalled %v", found)
	}

	// setRegular on a multi-channel guest: a full member, and one record.
	if err := repository.AssignWorkspaceRole(ctx, "T1", "UG1", domain.WorkspaceRoleMember, event("workspace.role_changed", "UG1")); err != nil {
		t.Fatal(err)
	}
	membership, err := repository.GetWorkspaceMembership(ctx, "T1", "UG1")
	if err != nil || membership.Guest() || membership.Role != domain.WorkspaceRoleMember {
		t.Fatalf("membership after setRegular = %+v err=%v", membership, err)
	}
	found := guestEvents()
	if len(found) != 1 || found[0]["id"] != "UG1" || found[0]["is_restricted"] != false || found[0]["is_ultra_restricted"] != false || found[0]["deleted"] != false {
		t.Fatalf("records after setRegular = %v", found)
	}

	// A member assigned a role is not a guest status change.
	if err := repository.AssignWorkspaceRole(ctx, "T1", "UM2", domain.WorkspaceRoleAdmin, event("workspace.role_changed", "UM2")); err != nil {
		t.Fatal(err)
	}
	// setAdmin on a guest makes an administrator.
	if err := repository.AssignWorkspaceRole(ctx, "T1", "UG4", domain.WorkspaceRoleAdmin, event("workspace.role_changed", "UG4")); err != nil {
		t.Fatal(err)
	}
	if membership, err := repository.GetWorkspaceMembership(ctx, "T1", "UG4"); err != nil || membership.Guest() || membership.Role != domain.WorkspaceRoleAdmin {
		t.Fatalf("membership after setAdmin = %+v err=%v", membership, err)
	}
	if found := guestEvents(); len(found) != 2 || found[1]["id"] != "UG4" || found[1]["is_admin"] != true {
		t.Fatalf("records after the role assignments = %v", found)
	}

	// Deactivating a single-channel guest: a deactivated guest.
	if err := repository.SetUserDeleted(ctx, "T1", "UG2", true, event("user.removed", "UG2")); err != nil {
		t.Fatal(err)
	}
	found = guestEvents()
	if len(found) != 3 || found[2]["id"] != "UG2" || found[2]["deleted"] != true || found[2]["is_ultra_restricted"] != true {
		t.Fatalf("records after deactivating a guest = %v", found)
	}
	// Deactivating it again, and deactivating a member, change no guest status.
	if err := repository.SetUserDeleted(ctx, "T1", "UG2", true, event("user.removed", "UG2")); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetUserDeleted(ctx, "T1", "UM1", true, event("user.removed", "UM1")); err != nil {
		t.Fatal(err)
	}
	if found := guestEvents(); len(found) != 3 {
		t.Fatalf("records after deactivations that change no guest status = %v", found)
	}

	// A lapsed guest account is deactivated by the expiry, which journals it.
	lapse := at.Add(-time.Hour)
	if err := repository.SetUserExpiration(ctx, "T1", "UG3", lapse, event("user.expiration_changed", "UG3")); err != nil {
		t.Fatal(err)
	}
	expired, err := repository.ExpireUserAccount(ctx, "T1", "UG3", lapse, event("user.removed", "UG3"))
	if err != nil || !expired {
		t.Fatalf("expire guest = %t err=%v", expired, err)
	}
	found = guestEvents()
	if len(found) != 4 || found[3]["id"] != "UG3" || found[3]["deleted"] != true || found[3]["is_restricted"] != true {
		t.Fatalf("records after a guest lapsed = %v", found)
	}
}
