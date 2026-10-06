package qualification

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// sdkMessageArgumentsAreDurable holds the storage behind arguments the pinned
// SDKs send to chat.update, chat.postEphemeral and files.getUploadURLExternal:
// an edit's reply_broadcast, file_ids, metadata and markdown presentation, an
// ephemeral message's presentation, and an upload ticket's alt_txt and
// snippet_type all read back as written on every profile.
func sdkMessageArgumentsAreDurable(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()
	messages := service.Messages{Store: f.repository}
	other := f.secondMember(t, ctx)

	created := time.Unix(1_700_000_600, 0).UTC()
	files := make([]domain.FileID, 2)
	for index, name := range []string{"first", "second"} {
		files[index] = domain.FileID("F-update-" + name + "-" + f.suffix)
		file := domain.File{ID: files[index], WorkspaceID: f.workspaceID, Uploader: f.userID, Name: name + ".txt", Title: name, MIMEType: "text/plain", BlobKey: string(f.workspaceID) + "/" + name, Size: 1, CreatedAt: created}
		if err := f.repository.CreateFile(ctx, file, f.event("update-file-"+name, "file.created", string(file.ID))); err != nil {
			t.Fatal(err)
		}
	}
	root, err := messages.PostMessageAs(ctx, f.workspaceID, f.userID, domain.MessagePostRequest{Conversation: f.channelID, Text: "root", AppID: "A-update"})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := messages.PostMessageAs(ctx, f.workspaceID, f.userID, domain.MessagePostRequest{Conversation: f.channelID, Text: "reply", AppID: "A-update", ThreadTimestamp: domain.NewMessageTimestamp(root.CreatedAt)})
	if err != nil {
		t.Fatal(err)
	}
	markdown, metadata := "**edited**", `{"event_type":"edited","event_payload":{"n":1}}`
	edit := func(patch domain.MessagePatch) domain.Message {
		t.Helper()
		patch.AppID = "A-update"
		if _, err := messages.UpdateMessage(ctx, f.workspaceID, f.userID, f.channelID, domain.NewMessageTimestamp(reply.CreatedAt), patch); err != nil {
			t.Fatal(err)
		}
		stored, err := f.repository.GetMessageByCreatedAt(ctx, f.channelID, reply.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	sharedInChannel := func(id domain.FileID) bool {
		t.Helper()
		file, err := f.repository.GetFile(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, channel := range file.SharedChannels {
			if channel == f.channelID {
				return true
			}
		}
		return false
	}
	first := []domain.FileID{files[0]}
	stored := edit(domain.MessagePatch{Text: &markdown, MarkdownText: true, Parse: "full", Metadata: &metadata, ReplyBroadcast: true, FileIDs: &first})
	var state domain.MessageStreamState
	if err := json.Unmarshal([]byte(stored.StreamState), &state); err != nil {
		t.Fatal(err)
	}
	if !stored.ReplyBroadcast || stored.Text != markdown || !state.MarkdownText || state.Parse != "full" ||
		stored.Metadata == "" || len(stored.Files) != 1 || stored.Files[0].ID != files[0] || !sharedInChannel(files[0]) {
		t.Fatalf("edited reply=%+v state=%+v", stored, state)
	}
	second := []domain.FileID{files[1]}
	stored = edit(domain.MessagePatch{FileIDs: &second})
	if len(stored.Files) != 1 || stored.Files[0].ID != files[1] || sharedInChannel(files[0]) || !sharedInChannel(files[1]) || !stored.ReplyBroadcast {
		t.Fatalf("re-filed reply=%+v; first shared=%v second shared=%v", stored, sharedInChannel(files[0]), sharedInChannel(files[1]))
	}

	if _, err := messages.PostEphemeralWithBlocksAndAttachments(ctx, f.workspaceID, f.userID, f.channelID, other, "**private**", "", "", "A-update", "",
		domain.EphemeralPresentation{MarkdownText: true, Username: "Helper", IconEmoji: ":robot_face:"}); err != nil {
		t.Fatal(err)
	}
	ephemeral, err := f.repository.ListEphemeralMessages(ctx, f.workspaceID, other, f.channelID, 10)
	if err != nil || len(ephemeral) != 1 {
		t.Fatalf("ephemeral=%+v err=%v", ephemeral, err)
	}
	state = domain.MessageStreamState{}
	if err := json.Unmarshal([]byte(ephemeral[0].StreamState), &state); err != nil || !state.MarkdownText || state.Username != "Helper" || state.IconEmoji != ":robot_face:" {
		t.Fatalf("ephemeral presentation=%q err=%v", ephemeral[0].StreamState, err)
	}

	ticket, err := messages.CreateExternalUpload(ctx, f.workspaceID, f.userID, domain.ExternalUploadRequest{Name: "chart.py", MIMEType: "text/x-python", Size: 4, TTL: time.Minute, Description: "A chart script", FileType: "python"})
	if err != nil {
		t.Fatal(err)
	}
	read, err := f.repository.GetExternalUpload(ctx, ticket.ID)
	if err != nil || read.Description != "A chart script" || read.FileType != "python" {
		t.Fatalf("upload ticket=%+v err=%v", read, err)
	}
}
