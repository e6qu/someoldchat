package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// directoryWorld gives a public channel, a private channel the reader is not in,
// and two members, because what directory search has to get right is the fold
// and the visibility rule.
func directoryWorld(t *testing.T) (context.Context, Messages) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "ada", RealName: "Ada Lovelace"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "grace", RealName: "Grace Hopper"})
	repository.SeedConversation(domain.Conversation{ID: "Cpublic", WorkspaceID: "T1", Name: "deployment"})
	repository.SeedConversation(domain.Conversation{ID: "Csecret", WorkspaceID: "T1", Name: "deployment-leadership", Kind: domain.ConversationTypePrivate})
	repository.SeedConversationMember("Csecret", "U2")
	return ctx, Messages{Store: repository}
}

// The fold is the product's one fold, and it applies to the real name as well as
// the handle: a member searching for the name they see should find the person.
func TestPeopleSearchFoldsNameAndRealName(t *testing.T) {
	ctx, messages := directoryWorld(t)
	page := domain.PageRequest{Limit: 10}
	for name, query := range map[string]string{
		"handle in another case": "ADA",
		"real name":              "Lovelace",
		"partial real name":      "love",
	} {
		found, err := messages.SearchPeople(ctx, "T1", "U1", query, page)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(found.Users) != 1 || found.Users[0].ID != "U1" {
			t.Fatalf("%s found %+v, want Ada", name, found.Users)
		}
	}
	none, err := messages.SearchPeople(ctx, "T1", "U1", "nobody-by-that-name", page)
	if err != nil || len(none.Users) != 0 {
		t.Fatalf("absent member search = %+v err = %v, want nothing", none.Users, err)
	}
}

// Channel search is the member conversation listing with a query, so it inherits
// the sidebar's visibility rule. Both channels here match the term; only one is
// the reader's to see, which is the whole point of not writing a second query.
func TestChannelSearchCannotRevealAPrivateChannel(t *testing.T) {
	ctx, messages := directoryWorld(t)
	page := domain.PageRequest{Limit: 10}
	found, err := messages.SearchChannels(ctx, "T1", "U1", "DEPLOYMENT", page)
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Conversations) != 1 || found.Conversations[0].ID != "Cpublic" {
		t.Fatalf("channel search found %+v, want only the public channel", found.Conversations)
	}

	// The member of the private channel sees both, which proves the first
	// result was filtered by membership rather than by the term.
	member, err := messages.SearchChannels(ctx, "T1", "U2", "DEPLOYMENT", page)
	if err != nil {
		t.Fatal(err)
	}
	if len(member.Conversations) != 2 {
		t.Fatalf("the private channel's member found %+v, want both", member.Conversations)
	}
}

// A blank query is refused rather than treated as "everything". A search surface
// that answers an empty question with the whole directory is a directory.
func TestDirectorySearchRefusesABlankQuery(t *testing.T) {
	ctx, messages := directoryWorld(t)
	page := domain.PageRequest{Limit: 10}
	if _, err := messages.SearchPeople(ctx, "T1", "U1", "   ", page); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("blank people search = %v, want ErrInvalidSearch", err)
	}
	if _, err := messages.SearchChannels(ctx, "T1", "U1", "", page); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("blank channel search = %v, want ErrInvalidSearch", err)
	}
}
