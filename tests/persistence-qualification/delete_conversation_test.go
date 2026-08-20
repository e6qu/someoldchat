package qualification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// deletingAConversationRemovesEverythingItOwns is the contract for a defect that
// lived on every SQL profile: DeleteConversation cleared a channel's messages and
// a handful of side tables but not the rest of what a channel owns. Because the
// SQL profiles enforce foreign keys, a channel that had a bookmark, an incoming
// webhook, a per-member notification preference, a linked record or a retention
// override could not be deleted at all — the final DELETE failed with a foreign
// key violation. The in-memory profile did not fail, but leaked the same rows, so
// after deleting a channel the two profiles disagreed on whether a member still
// had a saved item or a followed thread pointing into it.
//
// The contract seeds one row in each kind of thing a channel owns, deletes the
// channel, and requires the delete to succeed and every owned row to be gone —
// identically on whichever profile runs it.
func deletingAConversationRemovesEverythingItOwns(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	now := time.Unix(1700000000, 0).UTC()
	message := f.message(t, ctx, "owned-message", now)

	// One row in each table keyed to the conversation. Several of these carry an
	// enforced foreign key, so before the fix any one of them alone made the
	// delete below fail outright.
	if err := f.repository.CreateBookmark(ctx, domain.Bookmark{
		ID: domain.BookmarkID("BK-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
		Title: "Docs", Type: "link", Link: "https://example.com", CreatedAt: now, UpdatedAt: now, UpdatedBy: f.userID,
	}, f.event("bookmark", "bookmark.created", "BK")); err != nil {
		t.Fatalf("seed bookmark: %v", err)
	}
	if err := f.repository.CreateIncomingWebhook(ctx, domain.IncomingWebhook{
		ID: domain.IncomingWebhookID("WH-" + f.suffix), WorkspaceID: f.workspaceID, AppID: domain.AppID("A-" + f.suffix),
		ConversationID: f.channelID, UserID: f.userID, SecretHash: "secret-" + f.suffix, Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed incoming webhook: %v", err)
	}
	if err := f.repository.SetConversationNotificationPreferences(ctx, domain.ConversationNotificationPreferences{
		WorkspaceID: f.workspaceID, UserID: f.userID, Conversation: f.channelID, Level: domain.NotificationAll,
	}, f.event("notif-pref", "conversation.notification_prefs_set", string(f.channelID))); err != nil {
		t.Fatalf("seed notification preference: %v", err)
	}
	if err := f.repository.SetConversationRetention(ctx, f.workspaceID, f.channelID, 30, now, f.event("retention", "conversation.retention_set", string(f.channelID))); err != nil {
		t.Fatalf("seed retention override: %v", err)
	}
	if err := f.repository.LinkConversationObjects(ctx, []domain.LinkedObject{{
		ConversationID: f.channelID, WorkspaceID: f.workspaceID, OrgID: "O-" + f.suffix, RecordID: "R-" + f.suffix, CreatedAt: now,
	}}, f.event("link-object", "conversation.objects_linked", string(f.channelID))); err != nil {
		t.Fatalf("seed linked object: %v", err)
	}
	if err := f.repository.SetThreadFollowed(ctx, f.workspaceID, f.userID, f.channelID, domain.MessageTimestamp("1700000000.000100"), true, f.event("follow", "thread.followed", string(message.ID))); err != nil {
		t.Fatalf("seed thread follow: %v", err)
	}
	if _, _, err := f.repository.CreateSavedItem(ctx, domain.SavedItem{
		ID: domain.SavedItemID("SV-" + f.suffix), WorkspaceID: f.workspaceID, UserID: f.userID, MessageID: message.ID,
		Conversation: f.channelID, State: domain.SavedItemInProgress, CreatedAt: now, UpdatedAt: now,
	}, f.event("saved", "saved_item.created", string(message.ID))); err != nil {
		t.Fatalf("seed saved item: %v", err)
	}

	// The delete must now succeed rather than fail on a foreign key.
	if err := f.repository.DeleteConversation(ctx, f.workspaceID, f.channelID, f.event("delete", "conversation.deleted", string(f.channelID))); err != nil {
		t.Fatalf("delete a conversation that owns bookmarks, a webhook, preferences, a retention override, a linked record, a follow and a saved item: %v", err)
	}

	// And every owned row is gone, so the two profiles agree on the aftermath.
	if bookmarks, err := f.repository.ListBookmarks(ctx, f.workspaceID, f.channelID); err != nil || len(bookmarks) != 0 {
		t.Fatalf("bookmarks after delete: %d (err %v), want none", len(bookmarks), err)
	}
	saved, err := f.repository.ListSavedItems(ctx, f.workspaceID, f.userID, domain.SavedItemInProgress, domain.PageRequest{Limit: 50})
	if err != nil || len(saved.Items) != 0 {
		t.Fatalf("saved items after delete: %d (err %v), want none", len(saved.Items), err)
	}
	if objects, err := f.repository.ListConversationObjects(ctx, f.workspaceID, f.channelID); err != nil || len(objects) != 0 {
		t.Fatalf("linked objects after delete: %d (err %v), want none", len(objects), err)
	}
	retention, err := f.repository.GetConversationRetention(ctx, f.workspaceID, f.channelID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read retention after delete: %v", err)
	}
	if retention.DurationDays != 0 {
		t.Fatalf("retention override survived delete: %d days", retention.DurationDays)
	}
	preference, err := f.repository.GetConversationNotificationPreferences(ctx, f.workspaceID, f.userID, f.channelID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read notification preference after delete: %v", err)
	}
	if preference.Level != "" && preference.Level != domain.NotificationInherit {
		t.Fatalf("notification preference survived delete: %q", preference.Level)
	}
	followed, err := f.repository.IsThreadFollowed(ctx, f.workspaceID, f.userID, f.channelID, domain.MessageTimestamp("1700000000.000100"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read thread follow after delete: %v", err)
	}
	if followed {
		t.Fatal("thread follow survived delete")
	}
}
