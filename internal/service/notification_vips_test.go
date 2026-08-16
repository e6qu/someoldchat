package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func vipWorld(t *testing.T) (context.Context, Messages, *memory.Store) {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "author"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "reader"})
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "bystander"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	s.SeedConversationMember("C1", "U3")
	return ctx, Messages{Store: s}, s
}

func channelActivityCount(t *testing.T, s *memory.Store, user domain.UserID) int {
	t.Helper()
	page, err := s.ListActivity(context.Background(), "T1", user, domain.ActivityQuery{
		Kinds: []domain.ActivityKind{domain.ActivityChannel}, Page: domain.PageRequest{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Items)
}

// A VIP's ordinary channel message reaches a member even though their level
// (the default mentions-only, or an explicit mute) would otherwise drop it —
// and only the member who marked the author a VIP, not every recipient.
func TestVIPChannelMessageReachesAMemberWhoWouldOtherwiseNotBeNotified(t *testing.T) {
	ctx, messages, s := vipWorld(t)
	// U2 marks U1 a VIP and even mutes the channel; U3 does neither.
	if err := messages.SetNotificationVIP(ctx, "T1", "U2", "U1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetConversationNotificationPreferences(ctx, "T1", "U2", "C1", domain.NotificationMute, false); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "morning all", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := channelActivityCount(t, s, "U2"); got != 1 {
		t.Fatalf("VIP reader channel activity = %d, want 1 (a VIP pierces even a mute)", got)
	}
	if got := channelActivityCount(t, s, "U3"); got != 0 {
		t.Fatalf("non-VIP reader channel activity = %d, want 0 (default mentions-only suppresses it)", got)
	}
	// The author does not notify themselves.
	if got := channelActivityCount(t, s, "U1"); got != 0 {
		t.Fatalf("author channel activity = %d, want 0", got)
	}
}

// The override upgrades an item for a member already receiving the message; it
// never turns a VIP into a recipient of a conversation they are not in.
func TestVIPOverrideNeverNotifiesANonMember(t *testing.T) {
	ctx, messages, s := vipWorld(t)
	// U2 marks U3 a VIP, but U3 posts in a channel U2 is not a member of.
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "secret"})
	s.SeedConversationMember("C2", "U3")
	if err := messages.SetNotificationVIP(ctx, "T1", "U2", "U3", true); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U3", "C2", "you can't see this", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := channelActivityCount(t, s, "U2"); got != 0 {
		t.Fatalf("non-member VIP activity = %d, want 0 (a VIP override never adds a recipient)", got)
	}
}

// A section's notification level layers between a channel's own override and the
// workspace default: a muted section suppresses a channel that the workspace
// default would otherwise deliver, and a channel's own override still wins.
func TestSectionNotificationLevelLayersBetweenConversationAndWorkspace(t *testing.T) {
	ctx, messages, s := vipWorld(t)
	// U2's workspace default is All, so a plain channel message reaches them.
	if _, err := messages.SetWorkspaceNotificationPreferences(ctx, "T1", "U2", domain.NotificationAll, nil, true, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "hello one", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := channelActivityCount(t, s, "U2"); got != 1 {
		t.Fatalf("baseline U2 channel activity = %d, want 1 (workspace All)", got)
	}
	// Placing C1 in a muted section suppresses what the workspace default delivered.
	section, err := messages.CreateSidebarSection(ctx, "T1", "U2", "Muted")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.AssignConversationToSidebarSection(ctx, "T1", "U2", "C1", section.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetSidebarSectionNotificationLevel(ctx, "T1", "U2", section.ID, domain.NotificationMute); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "hello two", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := channelActivityCount(t, s, "U2"); got != 1 {
		t.Fatalf("after section mute, U2 channel activity = %d, want still 1 (the muted section suppressed the new message)", got)
	}
	// A channel's own override beats the section: U2 sets C1 to All.
	if _, err := messages.SetConversationNotificationPreferences(ctx, "T1", "U2", "C1", domain.NotificationAll, false); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "hello three", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := channelActivityCount(t, s, "U2"); got != 2 {
		t.Fatalf("with a channel override of All over a muted section, U2 = %d, want 2", got)
	}
}

// SetNotificationVIP manages the member's own list: toggling adds then removes,
// marking yourself or a stranger is refused, and it is read back on the prefs.
func TestSetNotificationVIPManagesTheMembersOwnList(t *testing.T) {
	ctx, messages, _ := vipWorld(t)
	if err := messages.SetNotificationVIP(ctx, "T1", "U1", "U1", true); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("marking yourself a VIP = %v, want invalid argument", err)
	}
	if err := messages.SetNotificationVIP(ctx, "T1", "U1", "U-missing", true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("marking a stranger a VIP = %v, want not found", err)
	}
	if err := messages.SetNotificationVIP(ctx, "T1", "U1", "U2", true); err != nil {
		t.Fatal(err)
	}
	// Marking twice is idempotent — no duplicate.
	if err := messages.SetNotificationVIP(ctx, "T1", "U1", "U2", true); err != nil {
		t.Fatal(err)
	}
	prefs, err := messages.WorkspaceNotificationPreferences(ctx, "T1", "U1")
	if err != nil || len(prefs.VIPs) != 1 || prefs.VIPs[0] != "U2" {
		t.Fatalf("VIPs = %+v err=%v, want [U2]", prefs.VIPs, err)
	}
	// A layout/keyword write must not clobber the VIP list.
	if _, err := messages.SetWorkspaceNotificationPreferences(ctx, "T1", "U1", domain.NotificationAll, []string{"deploy"}, true, true, false); err != nil {
		t.Fatal(err)
	}
	if after, err := messages.WorkspaceNotificationPreferences(ctx, "T1", "U1"); err != nil || len(after.VIPs) != 1 {
		t.Fatalf("VIPs after a preferences write = %+v err=%v, want kept", after.VIPs, err)
	}
	if err := messages.SetNotificationVIP(ctx, "T1", "U1", "U2", false); err != nil {
		t.Fatal(err)
	}
	if gone, err := messages.WorkspaceNotificationPreferences(ctx, "T1", "U1"); err != nil || len(gone.VIPs) != 0 {
		t.Fatalf("VIPs after removal = %+v err=%v, want none", gone.VIPs, err)
	}
}

// TestDeactivatedMembersLoseTheirVIPGuard makes the workspace-membership guard
// on SetNotificationVIP load-bearing: the store scopes the list to the member
// but records nothing about their standing.
func TestDeactivatedMembersLoseTheirVIPGuard(t *testing.T) {
	ctx, messages, s := vipWorld(t)
	if err := s.SetUserDeleted(ctx, "T1", "U1", true, events.Event{ID: "gone", WorkspaceID: "T1", Topic: "user.removed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetNotificationVIP(ctx, "T1", "U1", "U2", true); err == nil {
		t.Fatal("a deactivated member set a VIP")
	}
}
