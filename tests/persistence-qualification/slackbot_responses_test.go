package qualification

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// slackbotResponsesAreKept holds the storage behind Slackbot's custom
// responses: they read back whole and oldest first, are removed by their own
// workspace only, and the cursor starts at the journal's head the first time
// it is read and only moves forward.
func slackbotResponsesAreKept(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Unix(1_700_000_000, 0).UTC()
	first := domain.SlackbotResponse{
		WorkspaceID: f.workspaceID, ID: domain.SlackbotResponseID("Sr-first-" + f.suffix), Triggers: []string{"lunch", "what's for lunch"},
		Replies: []string{"Tacos at noon!", "Ask Ada"}, CreatedBy: f.userID, CreatedAt: base,
	}
	second := domain.SlackbotResponse{
		WorkspaceID: f.workspaceID, ID: domain.SlackbotResponseID("Sr-second-" + f.suffix), Triggers: []string{"wifi"},
		Replies: []string{"It is on the fridge."}, CreatedBy: f.userID, CreatedAt: base.Add(time.Second),
	}
	for _, value := range []domain.SlackbotResponse{second, first} {
		if err := f.repository.CreateSlackbotResponse(ctx, value, f.event("slackbot-"+string(value.ID), "slackbot_response.created", string(value.ID))); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := f.repository.ListSlackbotResponses(ctx, f.workspaceID)
	if err != nil || !reflect.DeepEqual(listed, []domain.SlackbotResponse{first, second}) {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	if err := f.repository.DeleteSlackbotResponse(ctx, domain.WorkspaceID("T-other-"+f.suffix), first.ID, f.event("slackbot-cross", "slackbot_response.deleted", string(first.ID))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a removal from another workspace: %v", err)
	}
	if err := f.repository.DeleteSlackbotResponse(ctx, f.workspaceID, first.ID, f.event("slackbot-delete", "slackbot_response.deleted", string(first.ID))); err != nil {
		t.Fatal(err)
	}
	if listed, err := f.repository.ListSlackbotResponses(ctx, f.workspaceID); err != nil || len(listed) != 1 || listed[0].ID != second.ID {
		t.Fatalf("after removal=%+v err=%v", listed, err)
	}

	head, err := f.repository.LatestEventSequence(ctx, f.workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := f.repository.SlackbotResponseCursor(ctx, f.workspaceID)
	if err != nil || cursor < head {
		t.Fatalf("a new cursor=%d err=%v, want it at the journal's head %d", cursor, err, head)
	}
	if err := f.repository.AdvanceSlackbotResponseCursor(ctx, f.workspaceID, cursor+5); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.AdvanceSlackbotResponseCursor(ctx, f.workspaceID, cursor+2); err != nil {
		t.Fatal(err)
	}
	if moved, err := f.repository.SlackbotResponseCursor(ctx, f.workspaceID); err != nil || moved != cursor+5 {
		t.Fatalf("cursor=%d err=%v, want %d: it only moves forward", moved, err, cursor+5)
	}
}
