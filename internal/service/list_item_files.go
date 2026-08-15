package service

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// AttachFileToListItem records that an already-uploaded file belongs to a list
// item. Attaching is an edit of the row, so it takes write access to the list,
// the same authority CreateListItem and SetListItemValue take. The file is
// uploaded first through the ordinary UploadFile path — this only records the
// association; the bytes and their blob already exist.
func (m Messages) AttachFileToListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, fileID domain.FileID) (domain.ListItemFile, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessWrite); err != nil {
		return domain.ListItemFile{}, err
	}
	if fileID == "" || itemID == "" {
		return domain.ListItemFile{}, ErrInvalidList
	}
	identifier, err := domain.PublicID("temp:LIF:")
	if err != nil {
		return domain.ListItemFile{}, err
	}
	link := domain.ListItemFile{
		ID: domain.ListItemFileID(identifier), ListID: listID, ItemID: itemID,
		WorkspaceID: workspaceID, FileID: fileID, CreatedAt: time.Now().UTC(),
	}
	event, err := listEvent(workspaceID, userID, "list.item.file_attached",
		events.String("list_item_file_id", identifier), events.String("list_id", string(listID)),
		events.String("list_item_id", string(itemID)), events.String("file_id", string(fileID)))
	if err != nil {
		return domain.ListItemFile{}, err
	}
	if err := m.Store.AttachListItemFile(ctx, link, event); err != nil {
		return domain.ListItemFile{}, err
	}
	return link, nil
}

// ListItemFiles returns the files attached to one item under the same read
// access the list itself is read under.
func (m Messages) ListItemFiles(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID) ([]domain.File, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessRead); err != nil {
		return nil, err
	}
	return m.Store.ListItemFiles(ctx, workspaceID, userID, listID, itemID)
}

// DetachFileFromListItem removes a file's attachment from an item. Like
// attaching, detaching edits the row and takes write access; the file itself is
// untouched and stays available to its uploader and anywhere else it is shared.
func (m Messages) DetachFileFromListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, fileID domain.FileID) error {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessWrite); err != nil {
		return err
	}
	event, err := listEvent(workspaceID, userID, "list.item.file_detached",
		events.String("list_id", string(listID)), events.String("list_item_id", string(itemID)),
		events.String("file_id", string(fileID)))
	if err != nil {
		return err
	}
	return m.Store.DetachListItemFile(ctx, workspaceID, listID, itemID, fileID, event)
}
