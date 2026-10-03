package qualification

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// recentReactionsAreTheMembersLatest holds the read behind the toolbar's
// one-click reactions: the distinct emoji a member reacted with, ordered by
// each one's latest use (by name on a tie), only their own and only in the
// workspace asked about, bounded by the limit.
func recentReactionsAreTheMembersLatest(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Unix(1_700_000_000, 0).UTC()
	first := f.message(t, ctx, "recent-first", base)
	second := f.message(t, ctx, "recent-second", base.Add(time.Second))
	other := f.secondMember(t, ctx)
	for index, reaction := range []domain.Reaction{
		{Message: first.ID, Name: "tada", UserID: f.userID, CreatedAt: base.Add(10 * time.Second)},
		{Message: second.ID, Name: "eyes", UserID: f.userID, CreatedAt: base.Add(20 * time.Second)},
		// tada again, later: its latest use is what counts.
		{Message: second.ID, Name: "tada", UserID: f.userID, CreatedAt: base.Add(30 * time.Second)},
		// a tie with eyes's latest use is ordered by name.
		{Message: first.ID, Name: "apple", UserID: f.userID, CreatedAt: base.Add(20 * time.Second)},
		{Message: first.ID, Name: "rocket", UserID: other, CreatedAt: base.Add(40 * time.Second)},
	} {
		event := f.event("recent-reaction-"+string(rune('a'+index)), "reaction.added", string(reaction.Message))
		if err := f.repository.AddReaction(ctx, reaction, event); err != nil {
			t.Fatal(err)
		}
	}
	names, err := f.repository.RecentReactionNames(ctx, f.workspaceID, f.userID, 10)
	if err != nil || !reflect.DeepEqual(names, []string{"tada", "apple", "eyes"}) {
		t.Fatalf("recent=%v err=%v, want [tada apple eyes]", names, err)
	}
	if names, err := f.repository.RecentReactionNames(ctx, f.workspaceID, f.userID, 2); err != nil || !reflect.DeepEqual(names, []string{"tada", "apple"}) {
		t.Fatalf("limited=%v err=%v", names, err)
	}
	if names, err := f.repository.RecentReactionNames(ctx, domain.WorkspaceID("T-elsewhere-"+f.suffix), f.userID, 10); err != nil || len(names) != 0 {
		t.Fatalf("another workspace=%v err=%v, want none", names, err)
	}
}
