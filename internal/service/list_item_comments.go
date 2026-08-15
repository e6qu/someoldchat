package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// CommentOnListItem records a remark on a list item. It mirrors CommentOnCanvas:
// read access to the list is enough to comment, and the author is the actor. The
// item is not checked for existence — like a canvas comment's section, a comment
// on a row that later goes away simply stops being shown.
func (m Messages) CommentOnListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, text string) (domain.ListItemComment, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessRead); err != nil {
		return domain.ListItemComment{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > domain.ListItemCommentLimit {
		return domain.ListItemComment{}, ErrInvalidList
	}
	identifier, err := domain.PublicID("temp:LIC:")
	if err != nil {
		return domain.ListItemComment{}, err
	}
	comment := domain.ListItemComment{
		ID: domain.ListItemCommentID(identifier), ListID: listID, ItemID: itemID, WorkspaceID: workspaceID,
		UserID: userID, Text: text, CreatedAt: time.Now().UTC(),
	}
	event, err := listEvent(workspaceID, userID, "list.item.commented", events.String("list_item_comment_id", identifier), events.String("list_id", string(listID)), events.String("list_item_id", string(itemID)))
	if err != nil {
		return domain.ListItemComment{}, err
	}
	if err := m.Store.CreateListItemComment(ctx, comment, event); err != nil {
		return domain.ListItemComment{}, err
	}
	return comment, nil
}

// ListItemComments reads one item's comments under the same read access the list
// itself is read under.
func (m Messages) ListItemComments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, request domain.PageRequest) (domain.ListItemCommentPage, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessRead); err != nil {
		return domain.ListItemCommentPage{}, err
	}
	return m.Store.ListListItemComments(ctx, workspaceID, userID, listID, itemID, request)
}

// DeleteListItemComment removes a remark. Like DeleteCanvasComment it verifies
// only workspace membership here; the author-only rule lives in the store's
// write, since the caller holds the comment id but not its list.
func (m Messages) DeleteListItemComment(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListItemCommentID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	event, err := listEvent(workspaceID, userID, "list.item.comment_deleted", events.String("list_item_comment_id", string(id)))
	if err != nil {
		return err
	}
	return m.Store.DeleteListItemComment(ctx, workspaceID, id, userID, event)
}
