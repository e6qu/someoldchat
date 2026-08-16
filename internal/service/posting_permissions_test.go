package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// postingWorld builds a channel every tier of member belongs to: an admin, a
// full member, a second full member used as an allowlist target, and a guest.
func postingWorld(t *testing.T) (context.Context, Messages, *memory.Store) {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "Uadmin", WorkspaceID: "T1", Name: "admin"})
	seedWorkspaceAdmin(t, s, "T1", "Uadmin")
	s.SeedUser(domain.User{ID: "Umember", WorkspaceID: "T1", Name: "member"})
	s.SeedUser(domain.User{ID: "Uallow", WorkspaceID: "T1", Name: "allowed"})
	guest := domain.User{ID: "Uguest", WorkspaceID: "T1", Name: "guest", Email: "guest@example.com"}
	if err := s.CreateUser(ctx, guest, domain.WorkspaceMembership{
		WorkspaceID: "T1", UserID: guest.ID, Role: domain.WorkspaceRoleMember, Active: true, Restricted: true,
	}, events.Event{ID: "E-guest", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []domain.UserID{"Uadmin", "Umember", "Uallow", "Uguest"} {
		if err := s.SeedConversationMember("C1", u); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, Messages{Store: s}, s
}

func setWhoCanPost(t *testing.T, ctx context.Context, m Messages, list domain.ConversationPreferenceList) {
	t.Helper()
	if _, err := m.AdminSetConversationPrefs(ctx, "T1", "Uadmin", "C1", domain.ConversationPrefs{WhoCanPost: list}); err != nil {
		t.Fatalf("set who_can_post %+v: %v", list, err)
	}
}

func mustPost(t *testing.T, ctx context.Context, m Messages, author domain.UserID) {
	t.Helper()
	if _, err := m.Post(ctx, "T1", author, "C1", "hi", "", ""); err != nil {
		t.Fatalf("%s was refused a post they should be allowed: %v", author, err)
	}
}

func mustNotPost(t *testing.T, ctx context.Context, m Messages, author domain.UserID) {
	t.Helper()
	if _, err := m.Post(ctx, "T1", author, "C1", "hi", "", ""); !errors.Is(err, ErrConversationPostingRestricted) {
		t.Fatalf("%s post error = %v, want ErrConversationPostingRestricted", author, err)
	}
}

// TestWhoCanPostEnforcesEveryMemberClass walks the four Slack posting policies:
// everyone (the default), everyone-except-guests, admins-only, and a named
// allowlist. In every restricted case the workspace admin still posts, which is
// Slack's channel-management invariant.
func TestWhoCanPostEnforcesEveryMemberClass(t *testing.T) {
	ctx, m, _ := postingWorld(t)

	// Default: no preference set — everyone posts, guest included.
	mustPost(t, ctx, m, "Uguest")
	mustPost(t, ctx, m, "Umember")

	// Everyone except guests.
	setWhoCanPost(t, ctx, m, domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{domain.ConversationPosterRegularMembers}})
	mustPost(t, ctx, m, "Umember")
	mustPost(t, ctx, m, "Uadmin")
	mustNotPost(t, ctx, m, "Uguest")

	// Admins only.
	setWhoCanPost(t, ctx, m, domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{domain.ConversationPosterAdmins}})
	mustPost(t, ctx, m, "Uadmin")
	mustNotPost(t, ctx, m, "Umember")
	mustNotPost(t, ctx, m, "Uguest")

	// A named allowlist: only Uallow plus admins.
	setWhoCanPost(t, ctx, m, domain.ConversationPreferenceList{Users: []domain.UserID{"Uallow"}})
	mustPost(t, ctx, m, "Uallow")
	mustPost(t, ctx, m, "Uadmin")
	mustNotPost(t, ctx, m, "Umember")

	// The explicit "everyone" token is Slack's default spelled out and admits
	// every member, guests included, exactly as an empty list does.
	setWhoCanPost(t, ctx, m, domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{domain.ConversationPosterEveryone}})
	mustPost(t, ctx, m, "Uguest")
	mustPost(t, ctx, m, "Umember")

	// Returning to an empty list reopens it for the guest too.
	setWhoCanPost(t, ctx, m, domain.ConversationPreferenceList{})
	mustPost(t, ctx, m, "Uguest")
}

// TestWhoCanPostBlockedMessageDoesNotPersist proves the refusal happens before
// the write: a blocked member leaves no message behind.
func TestWhoCanPostBlockedMessageDoesNotPersist(t *testing.T) {
	ctx, m, s := postingWorld(t)
	setWhoCanPost(t, ctx, m, domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{domain.ConversationPosterAdmins}})
	mustNotPost(t, ctx, m, "Umember")
	page, err := s.ListMessages(ctx, "C1", domain.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("a refused post persisted %d messages", len(page.Messages))
	}
}

// TestCanThreadGovernsRepliesSeparately shows a channel that restricts new
// top-level messages to admins while leaving threads open: a member cannot start
// a message but can reply under an existing one.
func TestCanThreadGovernsRepliesSeparately(t *testing.T) {
	ctx, m, _ := postingWorld(t)
	// Admin opens a thread root; posting is not yet restricted.
	root, err := m.Post(ctx, "T1", "Uadmin", "C1", "root", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Restrict who may post, but leave who may reply open (empty CanThread).
	if _, err := m.AdminSetConversationPrefs(ctx, "T1", "Uadmin", "C1", domain.ConversationPrefs{
		WhoCanPost: domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{domain.ConversationPosterAdmins}},
	}); err != nil {
		t.Fatal(err)
	}
	rootTS := domain.NewMessageTimestamp(root.CreatedAt)
	if _, err := m.Post(ctx, "T1", "Umember", "C1", "reply", rootTS, ""); err != nil {
		t.Fatalf("member reply refused though threads are open: %v", err)
	}
	mustNotPost(t, ctx, m, "Umember")

	// Now restrict replies to admins too: the member loses the reply as well.
	if _, err := m.AdminSetConversationPrefs(ctx, "T1", "Uadmin", "C1", domain.ConversationPrefs{
		WhoCanPost: domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{domain.ConversationPosterAdmins}},
		CanThread:  domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{domain.ConversationPosterAdmins}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Post(ctx, "T1", "Umember", "C1", "reply2", rootTS, ""); !errors.Is(err, ErrConversationPostingRestricted) {
		t.Fatalf("restricted reply error = %v, want ErrConversationPostingRestricted", err)
	}
}

// TestWhoCanPostRejectsUnknownVocabulary makes the write path validate the
// member-class vocabulary rather than store any string a caller invents.
func TestWhoCanPostRejectsUnknownVocabulary(t *testing.T) {
	ctx, m, _ := postingWorld(t)
	if _, err := m.AdminSetConversationPrefs(ctx, "T1", "Uadmin", "C1", domain.ConversationPrefs{
		WhoCanPost: domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{"root_only"}},
	}); !errors.Is(err, ErrInvalidConversationPrefs) {
		t.Fatalf("unknown poster class error = %v, want ErrInvalidConversationPrefs", err)
	}
	// The same guard rejects an unknown class in the threading list.
	if _, err := m.AdminSetConversationPrefs(ctx, "T1", "Uadmin", "C1", domain.ConversationPrefs{
		CanThread: domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{"nobody"}},
	}); !errors.Is(err, ErrInvalidConversationPrefs) {
		t.Fatalf("unknown thread class error = %v, want ErrInvalidConversationPrefs", err)
	}
}
