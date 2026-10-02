package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The inner event is the reference page's: type, the user object as the change
// leaves it, cache_ts, and event_ts. The page lists Events as the only
// compatible API, so RTM does not carry it.
func TestGuestStatusChangedTranslatesToTheReferenceShape(t *testing.T) {
	at := time.Unix(1_758_000_000, 100_000).UTC()
	event, err := GuestStatusChangedEvent(domain.User{
		ID: "U1234567", WorkspaceID: "T1234567", Name: "sandraslacksalot", RealName: "Sandra Slacksalot",
		Role: domain.WorkspaceRoleMember, Restricted: true,
	}, "UADMIN", at)
	if err != nil {
		t.Fatal(err)
	}
	if event.Topic != GuestStatusChangedTopic || event.ActorID != "UADMIN" || event.WorkspaceID != "T1234567" {
		t.Fatalf("event = %+v", event)
	}
	delivered, err := Broadcastable(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range []Surface{SurfaceEventsAPI, SurfaceSocketMode} {
		inners, err := SlackInner(event.Topic, delivered, surface)
		if err != nil || len(inners) != 1 || inners[0].Type() != "user_guest_status_changed" {
			t.Fatalf("surface %d: inners=%v err=%v", surface, inners, err)
		}
		encoded, err := inners[0].Encode()
		if err != nil {
			t.Fatal(err)
		}
		var inner struct {
			Type    string `json:"type"`
			EventTS string `json:"event_ts"`
			CacheTS int64  `json:"cache_ts"`
			User    struct {
				ID                string `json:"id"`
				TeamID            string `json:"team_id"`
				Deleted           bool   `json:"deleted"`
				IsRestricted      bool   `json:"is_restricted"`
				IsUltraRestricted bool   `json:"is_ultra_restricted"`
				Updated           int64  `json:"updated"`
			} `json:"user"`
		}
		if err := json.Unmarshal([]byte(encoded), &inner); err != nil {
			t.Fatal(err)
		}
		if inner.Type != "user_guest_status_changed" || inner.CacheTS != 1_758_000_000 || inner.EventTS != "1758000000.000100" ||
			inner.User.ID != "U1234567" || inner.User.TeamID != "T1234567" || inner.User.Deleted || !inner.User.IsRestricted ||
			inner.User.IsUltraRestricted || inner.User.Updated != 1_758_000_000 {
			t.Fatalf("inner = %s", encoded)
		}
	}
	if inners, err := SlackInner(event.Topic, delivered, SurfaceRTM); err != nil || len(inners) != 0 {
		t.Fatalf("RTM carried user_guest_status_changed: inners=%v err=%v", inners, err)
	}
}

// A deactivated guest is reported as one: the user object says deleted and
// keeps the guest tier it had.
func TestGuestStatusChangedReportsADeactivatedGuest(t *testing.T) {
	at := time.Unix(1_758_000_000, 0).UTC()
	event, err := GuestStatusChangedEvent(domain.User{ID: "U1", WorkspaceID: "T1", Name: "guest", Deleted: true, UltraRestricted: true}, "U2", at)
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := Broadcastable(event)
	if err != nil {
		t.Fatal(err)
	}
	inners, err := SlackInner(event.Topic, delivered, SurfaceEventsAPI)
	if err != nil || len(inners) != 1 {
		t.Fatalf("inners=%v err=%v", inners, err)
	}
	encoded, err := inners[0].Encode()
	if err != nil {
		t.Fatal(err)
	}
	var inner struct {
		User struct {
			Deleted           bool `json:"deleted"`
			IsUltraRestricted bool `json:"is_ultra_restricted"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(encoded), &inner); err != nil || !inner.User.Deleted || !inner.User.IsUltraRestricted {
		t.Fatalf("inner = %s err=%v", encoded, err)
	}
}
