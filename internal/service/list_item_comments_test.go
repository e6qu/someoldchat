package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func listCommentWorld(t *testing.T) (context.Context, Messages, domain.ListID, domain.ListItemID) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"})
	messages := Messages{Store: repository}
	list, err := messages.CreateList(ctx, "T1", "U1", "Incidents", "", "[]", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "Title", domain.ListColumnText, nil); err != nil {
		t.Fatal(err)
	}
	item, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"the outage"}]`)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, messages, list.ID, item.ID
}

// Commenting on a list item needs read access, not write — the same rule a
// canvas follows, so a list shared for review can be discussed by its readers.
func TestAListReaderMayCommentWithoutBeingAbleToWrite(t *testing.T) {
	ctx, messages, listID, itemID := listCommentWorld(t)
	if err := messages.SetListAccess(ctx, "T1", "U1", listID, domain.AccessRead, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	comment, err := messages.CommentOnListItem(ctx, "T1", "U2", listID, itemID, "who owns this")
	if err != nil {
		t.Fatal(err)
	}
	page, err := messages.ListItemComments(ctx, "T1", "U1", listID, itemID, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Comments) != 1 || page.Comments[0].ID != comment.ID || page.Comments[0].ItemID != itemID || page.Comments[0].UserID != "U2" {
		t.Fatalf("comments = %+v, want the reader's comment on the item", page.Comments)
	}
}

// A comment belongs to whoever wrote it — the list owner cannot delete it.
func TestOnlyTheAuthorDeletesTheirListItemComment(t *testing.T) {
	ctx, messages, listID, itemID := listCommentWorld(t)
	if err := messages.SetListAccess(ctx, "T1", "U1", listID, domain.AccessRead, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	comment, err := messages.CommentOnListItem(ctx, "T1", "U2", listID, itemID, "a remark")
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteListItemComment(ctx, "T1", "U1", comment.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the list owner deleted someone else's comment: %v", err)
	}
	if err := messages.DeleteListItemComment(ctx, "T1", "U2", comment.ID); err != nil {
		t.Fatal(err)
	}
	page, err := messages.ListItemComments(ctx, "T1", "U1", listID, itemID, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Comments) != 0 {
		t.Fatalf("comments after deletion = %+v err = %v, want none", page.Comments, err)
	}
}

// A member with no grant on the list can neither read its item comments nor add
// one.
func TestListItemCommentsAreInvisibleWithoutListAccess(t *testing.T) {
	ctx, messages, listID, itemID := listCommentWorld(t)
	if _, err := messages.ListItemComments(ctx, "T1", "U3", listID, itemID, domain.PageRequest{Limit: 10}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stranger read the comments: %v", err)
	}
	if _, err := messages.CommentOnListItem(ctx, "T1", "U3", listID, itemID, "hello"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stranger commented: %v", err)
	}
}

// TestDeactivatedListMembersLoseTheirCommentGuards makes the workspace-membership
// guards on reading and deleting comments load-bearing rather than shadowed by
// the store. A deactivated member keeps their read grant and their authorship, so
// the store's grant check and the delete's author check both still match them —
// only the service's active-membership guard stands between a removed member and
// the comments, so removing it must be caught here.
func TestDeactivatedListMembersLoseTheirCommentGuards(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	messages := Messages{Store: repository}
	list, err := messages.CreateList(ctx, "T1", "U1", "Incidents", "", "[]", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AddListColumn(ctx, "T1", "U1", list.ID, "Title", domain.ListColumnText, nil); err != nil {
		t.Fatal(err)
	}
	item, err := messages.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"the outage"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SetListAccess(ctx, "T1", "U1", list.ID, domain.AccessRead, nil, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	comment, err := messages.CommentOnListItem(ctx, "T1", "U2", list.ID, item.ID, "a remark")
	if err != nil {
		t.Fatal(err)
	}
	// Deactivate the owner and the author. The store still recognises U1 as the
	// list's owner and U2 as the comment's author regardless of their standing, so
	// the read guard (which U1's ownership would otherwise carry through the store)
	// and the delete guard (which U2's authorship would) are the only thing left
	// refusing a removed member.
	for _, gone := range []domain.UserID{"U1", "U2"} {
		if err := repository.SetUserDeleted(ctx, "T1", gone, true, events.Event{ID: domain.EventID("gone-" + string(gone)), WorkspaceID: "T1", Topic: "user.removed", Payload: string(gone), CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := messages.ListItemComments(ctx, "T1", "U1", list.ID, item.ID, domain.PageRequest{Limit: 10}); err == nil {
		t.Fatal("a deactivated owner read the item's comments")
	}
	if err := messages.DeleteListItemComment(ctx, "T1", "U2", comment.ID); err == nil {
		t.Fatal("a deactivated author deleted their comment")
	}
}

func TestAListItemCommentMustSaySomethingAndNotTooMuch(t *testing.T) {
	ctx, messages, listID, itemID := listCommentWorld(t)
	if _, err := messages.CommentOnListItem(ctx, "T1", "U1", listID, itemID, "   "); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("an empty comment = %v, want ErrInvalidList", err)
	}
	tooLong := strings.Repeat("x", domain.ListItemCommentLimit+1)
	if _, err := messages.CommentOnListItem(ctx, "T1", "U1", listID, itemID, tooLong); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("an over-long comment = %v, want ErrInvalidList", err)
	}
}
