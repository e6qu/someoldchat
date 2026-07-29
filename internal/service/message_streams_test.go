package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestMessageStreamLifecyclePersistsMarkdownChunksBlocksAndMetadata(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"})
	repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "assistant"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	repository.SeedConversationMember("C1", "UBOT")
	repository.SeedConversationMember("C1", "U1")
	messages := Messages{Store: repository}
	parent, err := messages.Post(ctx, "T1", "U1", "C1", "What changed?", "", "")
	if err != nil {
		t.Fatal(err)
	}

	stream, err := messages.StartMessageStream(ctx, "T1", "UBOT", domain.MessageStreamStart{
		Conversation: "C1", ThreadTimestamp: domain.NewMessageTimestamp(parent.CreatedAt), AppID: "A1", BotID: "B1",
		RecipientTeamID: "T1", RecipientUserID: "U1", MarkdownText: "**Deploy",
		TaskDisplayMode: "plan", Username: "Release assistant", IconEmoji: ":rocket:", IconURL: "https://ignored.example/icon.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	startState := messageStreamState(t, stream)
	if !startState.Active || startState.TaskDisplayMode != "plan" || startState.BotID != "B1" ||
		startState.Username != "Release assistant" || startState.IconEmoji != ":rocket:" || startState.IconURL != "" ||
		stream.Text != "**Deploy" || stream.AppID != "A1" {
		t.Fatalf("started stream=%+v", stream)
	}

	stream, err = messages.AppendMessageStream(ctx, "T1", "UBOT", domain.MessageStreamMutation{
		Conversation: "C1", Timestamp: domain.NewMessageTimestamp(stream.CreatedAt), AppID: "A1",
		MarkdownText: " complete**",
		Chunks: `[
			{"type":"plan_update","title":"Release plan"},
			{"type":"task_update","id":"deploy","title":"Deploy API","status":"in_progress","details":"Rolling out"}
		]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := messageStreamState(t, stream)
	if stream.Text != "**Deploy complete**" || state.PlanTitle != "Release plan" || len(state.Tasks) != 1 {
		t.Fatalf("appended stream=%+v state=%+v", stream, state)
	}

	stream, err = messages.AppendMessageStream(ctx, "T1", "UBOT", domain.MessageStreamMutation{
		Conversation: "C1", Timestamp: domain.NewMessageTimestamp(stream.CreatedAt), AppID: "A1",
		Chunks: `[
			{"type":"task_update","id":"deploy","title":"Deploy API","status":"complete","output":"Healthy"},
			{"type":"blocks","blocks":[{"type":"context","elements":[{"type":"plain_text","text":"SDK chunk"}]}]}
		]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	state = messageStreamState(t, stream)
	if len(state.Tasks) != 1 || len(state.ChunkBlocks) != 1 || !strings.Contains(string(state.Tasks[0]), `"complete"`) {
		t.Fatalf("updated state=%+v", state)
	}
	excessBlocks := make([]map[string]any, 51)
	for index := range excessBlocks {
		excessBlocks[index] = map[string]any{"type": "divider"}
	}
	excessChunks, err := json.Marshal([]map[string]any{{"type": "blocks", "blocks": excessBlocks}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err = messages.AppendMessageStream(ctx, "T1", "UBOT", domain.MessageStreamMutation{
		Conversation: "C1", Timestamp: domain.NewMessageTimestamp(stream.CreatedAt), AppID: "A1", Chunks: string(excessChunks),
	})
	if err != nil {
		t.Fatal(err)
	}
	state = messageStreamState(t, stream)
	if len(state.ChunkBlocks) != 50 || len(state.Warnings) != 1 || state.Warnings[0] != "too_many_blocks" {
		t.Fatalf("truncated block state=%+v", state)
	}

	if _, err := messages.StopMessageStream(ctx, "T1", "UBOT", domain.MessageStreamMutation{
		Conversation: "C1", Timestamp: domain.NewMessageTimestamp(stream.CreatedAt), AppID: "A2",
	}); !errors.Is(err, ErrMessageNotOwnedByApp) {
		t.Fatalf("foreign app stop error=%v", err)
	}
	stream, err = messages.StopMessageStream(ctx, "T1", "UBOT", domain.MessageStreamMutation{
		Conversation: "C1", Timestamp: domain.NewMessageTimestamp(stream.CreatedAt), AppID: "A1",
		MarkdownText: "\nDone.", Blocks: `[{"type":"section","text":{"type":"plain_text","text":"Final actions"}}]`,
		Metadata: `{"event_type":"answer","event_payload":{"request_id":"R1"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if messageStreamState(t, stream).Active || !strings.Contains(stream.Text, "Done.") ||
		!strings.Contains(stream.Blocks, "Final actions") || !strings.Contains(stream.Metadata, `"request_id":"R1"`) {
		t.Fatalf("stopped stream=%+v", stream)
	}
	if _, err := messages.AppendMessageStream(ctx, "T1", "UBOT", domain.MessageStreamMutation{
		Conversation: "C1", Timestamp: domain.NewMessageTimestamp(stream.CreatedAt), AppID: "A1", MarkdownText: "late",
	}); !errors.Is(err, ErrMessageNotStreaming) {
		t.Fatalf("append after stop error=%v", err)
	}
}

func TestMessageStreamRequiresChannelRecipientsAndValidChunks(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1"})
	repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	repository.SeedConversationMember("C1", "UBOT")
	repository.SeedConversationMember("C1", "U1")
	messages := Messages{Store: repository}
	parent, err := messages.Post(ctx, "T1", "U1", "C1", "question", "", "")
	if err != nil {
		t.Fatal(err)
	}
	request := domain.MessageStreamStart{
		Conversation: "C1", ThreadTimestamp: domain.NewMessageTimestamp(parent.CreatedAt), AppID: "A1",
	}
	if _, err := messages.StartMessageStream(ctx, "T1", "UBOT", request); !errors.Is(err, ErrMissingStreamRecipientTeam) {
		t.Fatalf("missing team error=%v", err)
	}
	request.RecipientTeamID = "T1"
	if _, err := messages.StartMessageStream(ctx, "T1", "UBOT", request); !errors.Is(err, ErrMissingStreamRecipientUser) {
		t.Fatalf("missing user error=%v", err)
	}
	request.RecipientUserID = "U1"
	request.TaskDisplayMode = "invented"
	if _, err := messages.StartMessageStream(ctx, "T1", "UBOT", request); !errors.Is(err, ErrInvalidMessageStream) {
		t.Fatalf("invalid task mode error=%v", err)
	}
	request.TaskDisplayMode = "dense"
	request.IconURL = "javascript:alert(1)"
	if _, err := messages.StartMessageStream(ctx, "T1", "UBOT", request); !errors.Is(err, ErrInvalidMessageStream) {
		t.Fatalf("invalid icon URL error=%v", err)
	}
	request.IconURL = ""
	request.Chunks = `[{"type":"task_update","id":"task","title":"Task","status":"invented"}]`
	if _, err := messages.StartMessageStream(ctx, "T1", "UBOT", request); !errors.Is(err, ErrInvalidStreamChunks) {
		t.Fatalf("invalid chunks error=%v", err)
	}
}

func messageStreamState(t *testing.T, message domain.Message) domain.MessageStreamState {
	t.Helper()
	var state domain.MessageStreamState
	if err := json.Unmarshal([]byte(message.StreamState), &state); err != nil {
		t.Fatalf("stream state %q: %v", message.StreamState, err)
	}
	return state
}
