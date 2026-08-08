package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// typingWorld gives two members of one channel plus a second channel only one
// of them belongs to, because most of what a typing signal has to get right is
// who is allowed to see it.
func typingWorld(t *testing.T) (context.Context, *memory.Store, Messages) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{
		{ID: "U1", WorkspaceID: "T1", Name: "ada"},
		{ID: "U2", WorkspaceID: "T1", Name: "grace"},
		{ID: "U3", WorkspaceID: "T1", Name: "alan"},
	} {
		if err := repository.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
	repository.SeedConversationMember("C1", "U1")
	repository.SeedConversationMember("C1", "U2")
	repository.SeedConversationMember("C2", "U1")
	repository.SeedConversationMember("C2", "U3")
	return ctx, repository, Messages{Store: repository}
}

// The defining property: a typing signal writes no event. Everything else this
// service does commits an outbox record, and if a signal ever acquired one it
// would be replayed to a reconnecting client as news and delivered to apps that
// asked for messages. Counting the outbox is the only way to notice.
func TestTypingWritesNoEvent(t *testing.T) {
	ctx, repository, messages := typingWorld(t)
	before := len(repository.Outbox())
	if err := messages.SetTyping(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetTyping(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatal(err)
	}
	if after := len(repository.Outbox()); after != before {
		t.Fatalf("outbox grew from %d to %d; a typing signal must not be journalled", before, after)
	}
}

// A member sees who else is composing and never themselves. Slack's own client
// has no use for "you are typing", and rendering it would be a bug the user
// sees rather than a harmless extra.
func TestTypingReachesOtherMembersOnly(t *testing.T) {
	ctx, _, messages := typingWorld(t)
	if err := messages.SetTyping(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatal(err)
	}
	seen, err := messages.TypingIn(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].UserID != "U2" {
		t.Fatalf("U1 saw %+v, want exactly U2", seen)
	}
	own, err := messages.TypingIn(ctx, "T1", "U2", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 0 {
		t.Fatalf("U2 saw its own signal: %+v", own)
	}
}

// Membership is the visibility rule, and it is the reader's membership that
// decides. Without it a signal would tell someone outside a private channel
// that composition is happening there — a smaller leak than the message, and
// the same kind.
func TestTypingIsInvisibleOutsideTheConversation(t *testing.T) {
	ctx, _, messages := typingWorld(t)
	if err := messages.SetTyping(ctx, "T1", "U3", "C2"); err != nil {
		t.Fatal(err)
	}
	seen, err := messages.TypingSignals(ctx, "T1", "U2")
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("a non-member of C2 saw %+v", seen)
	}
	member, err := messages.TypingSignals(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if len(member) != 1 || member[0].Conversation != "C2" {
		t.Fatalf("the member of C2 saw %+v, want one signal in C2", member)
	}
}

// Someone who is not in the conversation cannot claim to be typing in it. The
// signal is displayed to everyone who can read the conversation, so accepting
// it from a stranger would let anyone put their name in a channel they cannot
// otherwise reach.
func TestTypingRequiresMembership(t *testing.T) {
	ctx, _, messages := typingWorld(t)
	if err := messages.SetTyping(ctx, "T1", "U2", "C2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetTyping by a non-member = %v, want ErrNotFound", err)
	}
}

// A signal stops on the clock rather than on a second call, which is what makes
// a client that closes mid-word stop appearing. This drives the store directly
// because the service always writes "now plus the TTL"; the expiry it produces
// is exactly what a reader compares against.
func TestTypingExpiresWithoutBeingRetracted(t *testing.T) {
	ctx, repository, messages := typingWorld(t)
	past := time.Now().UTC().Add(-time.Second)
	if err := repository.RecordTyping(ctx, domain.TypingSignal{
		WorkspaceID: "T1", Conversation: "C1", UserID: "U2", ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	seen, err := messages.TypingIn(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("an expired signal was still shown: %+v", seen)
	}

	// Renewing brings the same member back rather than adding a second row.
	if err := messages.SetTyping(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatal(err)
	}
	if err := messages.SetTyping(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatal(err)
	}
	renewed, err := messages.TypingIn(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if len(renewed) != 1 {
		t.Fatalf("renewal produced %d signals, want one", len(renewed))
	}
	if !renewed[0].Active(time.Now().UTC()) {
		t.Fatalf("a renewed signal is already expired: %+v", renewed[0])
	}
	if renewed[0].ExpiresAt.After(time.Now().UTC().Add(domain.TypingSignalTTL + time.Second)) {
		t.Fatalf("a signal outlives its TTL: %v", renewed[0].ExpiresAt)
	}
}
