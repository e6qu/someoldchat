package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// Every message write path refuses blocks Slack would refuse, with
// ErrInvalidBlocks (invalid_blocks), and accepts Slack's message limit of 50.
func TestMessageWritesRefuseInvalidBlockKit(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	messages := Messages{Store: s}
	ctx := context.Background()
	dividers := func(count int) string {
		return "[" + strings.TrimSuffix(strings.Repeat(`{"type":"divider"},`, count), ",") + "]"
	}
	for name, blocks := range map[string]string{
		"unknown type":         `[{"type":"bogus"}]`,
		"section without text": `[{"type":"section"}]`,
		"eleven fields":        `[{"type":"section","fields":[` + strings.TrimSuffix(strings.Repeat(`{"type":"mrkdwn","text":"f"},`, 11), ",") + `]}]`,
		"51 blocks":            dividers(51),
	} {
		if _, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "U1", "C1", "x", blocks, "", "", "", ""); !errors.Is(err, domain.ErrInvalidBlocks) {
			t.Fatalf("post %s err=%v", name, err)
		}
		if _, err := messages.PostEphemeralWithBlocks(ctx, "T1", "U1", "C1", "U2", "x", blocks); !errors.Is(err, domain.ErrInvalidBlocks) {
			t.Fatalf("ephemeral %s err=%v", name, err)
		}
		if _, err := messages.ScheduleMessageWithBlocks(ctx, "T1", "U1", "C1", "x", blocks, time.Now().UTC().Add(time.Hour)); !errors.Is(err, domain.ErrInvalidBlocks) {
			t.Fatalf("schedule %s err=%v", name, err)
		}
	}
	posted, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "U1", "C1", "x", dividers(50), "", "", "", "")
	if err != nil {
		t.Fatalf("50 blocks: %v", err)
	}
	if _, err := messages.UpdateWithBlocks(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(posted.CreatedAt), "x", `[{"type":"image","alt_text":"a"}]`); !errors.Is(err, domain.ErrInvalidBlocks) {
		t.Fatalf("update err=%v", err)
	}
	valid := `[{"type":"image","slack_file":{"id":"F1"},"alt_text":"a"},{"type":"call","call_id":"R1"},{"type":"section","fields":[{"type":"mrkdwn","text":"a"}],"accessory":{"type":"button","action_id":"b","text":{"type":"plain_text","text":"B"}}},{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"t"}]}]},{"type":"context","elements":[{"type":"mrkdwn","text":"c"}]},{"type":"header","text":{"type":"plain_text","text":"h"}},{"type":"markdown","text":"**m**"}]`
	if _, err := messages.UpdateWithBlocks(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(posted.CreatedAt), "x", valid); err != nil {
		t.Fatalf("valid blocks refused: %v", err)
	}
}
