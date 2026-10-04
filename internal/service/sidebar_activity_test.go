package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// The sidebar's one read answers every conversation the member belongs to
// with its newest message and the member's own notification level, leaves out
// one they do not belong to, and refuses a batch larger than a sidebar.
func TestSidebarActivityAnswersOnlyTheMembersConversations(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	for _, conversation := range []domain.Conversation{
		{ID: "Cquiet", WorkspaceID: "T1", Name: "quiet", Kind: domain.ConversationTypePublic},
		{ID: "Cbusy", WorkspaceID: "T1", Name: "busy", Kind: domain.ConversationTypePublic},
		{ID: "Cother", WorkspaceID: "T1", Name: "other", Kind: domain.ConversationTypePrivate},
	} {
		if err := s.SeedConversation(conversation); err != nil {
			t.Fatal(err)
		}
	}
	for _, membership := range []struct {
		conversation domain.ConversationID
		user         domain.UserID
	}{{"Cquiet", "U1"}, {"Cbusy", "U1"}, {"Cother", "U2"}} {
		if err := s.SeedConversationMember(membership.conversation, membership.user); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: s}
	posted, err := messages.Post(ctx, "T1", "U1", "Cbusy", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetConversationNotificationPreferences(ctx, "T1", "U1", "Cbusy", domain.NotificationMute, false); err != nil {
		t.Fatal(err)
	}

	activity, err := messages.SidebarActivity(ctx, "T1", "U1", []domain.ConversationID{"Cquiet", "Cbusy", "Cother", "Cmissing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(activity) != 2 {
		t.Fatalf("activity = %v, want only Cquiet and Cbusy", activity)
	}
	if _, ok := activity["Cother"]; ok {
		t.Fatal("a conversation the member does not belong to was described")
	}
	busy := activity["Cbusy"]
	postedAt, err := domain.ParseMessageTimestamp(domain.NewMessageTimestamp(posted.CreatedAt))
	if err != nil {
		t.Fatal(err)
	}
	if !busy.LatestAt.Equal(postedAt) || busy.Notifications.Level != domain.NotificationMute {
		t.Fatalf("Cbusy = %+v, want its message at %v and muted", busy, postedAt)
	}
	quiet := activity["Cquiet"]
	if !quiet.LatestAt.IsZero() || quiet.Notifications != domain.DefaultConversationNotificationPreferences("T1", "U1", "Cquiet") {
		t.Fatalf("Cquiet = %+v, want no message and the default preferences", quiet)
	}

	oversized := make([]domain.ConversationID, sidebarActivityLimit+1)
	for index := range oversized {
		oversized[index] = domain.ConversationID(fmt.Sprintf("C%d", index))
	}
	if _, err := messages.SidebarActivity(ctx, "T1", "U1", oversized); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("oversized batch: %v, want invalid argument", err)
	}
}
