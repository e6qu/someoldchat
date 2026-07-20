package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (m Messages) CreateList(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, descriptionBlocks, schema string, copyFrom domain.ListID, includeCopiedRecords, todoMode bool) (domain.List, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.List{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.List{}, ErrInvalidList
	}
	descriptionBlocks, err := normalizeJSONArray(descriptionBlocks, "[]")
	if err != nil {
		return domain.List{}, ErrInvalidList
	}
	if strings.TrimSpace(schema) == "" && copyFrom == "" {
		schema = `[{"key":"title","name":"Title","type":"text","is_primary_column":true}]`
	}
	schema, err = normalizeJSONArray(schema, "[]")
	if err != nil {
		return domain.List{}, ErrInvalidList
	}
	id, err := domain.NewListID()
	if err != nil {
		return domain.List{}, err
	}
	now := time.Now().UTC()
	value := domain.List{ID: id, WorkspaceID: workspaceID, OwnerID: userID, Name: name, DescriptionBlocks: descriptionBlocks, Schema: schema, TodoMode: todoMode, CreatedAt: now, UpdatedAt: now}
	if copyFrom != "" {
		copied, err := m.Store.GetList(ctx, workspaceID, copyFrom)
		if err != nil {
			return domain.List{}, err
		}
		value.DescriptionBlocks = copied.DescriptionBlocks
		value.Schema = copied.Schema
	}
	event, err := listEvent(workspaceID, userID, "list.created", string(id))
	if err != nil {
		return domain.List{}, err
	}
	if err := m.Store.CreateList(ctx, value, event); err != nil {
		return domain.List{}, err
	}
	if copyFrom != "" && includeCopiedRecords {
		cursor := domain.Cursor("")
		for {
			page, err := m.Store.ListItems(ctx, workspaceID, copyFrom, domain.PageRequest{Limit: 100, Cursor: cursor}, false)
			if err != nil {
				return domain.List{}, err
			}
			for _, source := range page.Items {
				itemID, err := domain.NewListItemID()
				if err != nil {
					return domain.List{}, err
				}
				created, err := listEvent(workspaceID, userID, "list.item.created", string(itemID))
				if err != nil {
					return domain.List{}, err
				}
				if err := m.Store.CreateListItem(ctx, domain.ListItem{ID: itemID, ListID: id, WorkspaceID: workspaceID, Fields: source.Fields, CreatedBy: userID, UpdatedBy: userID, CreatedAt: now, UpdatedAt: now}, created); err != nil {
					return domain.List{}, err
				}
			}
			if !page.HasMore {
				break
			}
			cursor = page.NextCursor
		}
	}
	return value, nil
}

func (m Messages) UpdateList(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID, name, descriptionBlocks string, todoMode, todoModeSet bool) (domain.List, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.List{}, err
	}
	value, err := m.Store.GetList(ctx, workspaceID, id)
	if err != nil {
		return domain.List{}, err
	}
	if strings.TrimSpace(name) != "" {
		value.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(descriptionBlocks) != "" {
		value.DescriptionBlocks, err = normalizeJSONArray(descriptionBlocks, "[]")
		if err != nil {
			return domain.List{}, ErrInvalidList
		}
	}
	if todoModeSet {
		value.TodoMode = todoMode
	}
	value.UpdatedAt = time.Now().UTC()
	event, err := listEvent(workspaceID, userID, "list.updated", string(id))
	if err != nil {
		return domain.List{}, err
	}
	if err := m.Store.UpdateList(ctx, value, event); err != nil {
		return domain.List{}, err
	}
	return value, nil
}

func (m Messages) CreateListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, parentItemID domain.ListItemID, fields string) (domain.ListItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ListItem{}, err
	}
	if _, err := m.Store.GetList(ctx, workspaceID, listID); err != nil {
		return domain.ListItem{}, err
	}
	fields, err := normalizeJSONArray(fields, "[]")
	if err != nil {
		return domain.ListItem{}, ErrInvalidList
	}
	id, err := domain.NewListItemID()
	if err != nil {
		return domain.ListItem{}, err
	}
	now := time.Now().UTC()
	value := domain.ListItem{ID: id, ListID: listID, ParentItemID: parentItemID, WorkspaceID: workspaceID, Fields: fields, CreatedBy: userID, UpdatedBy: userID, CreatedAt: now, UpdatedAt: now}
	event, err := listEvent(workspaceID, userID, "list.item.created", string(id))
	if err != nil {
		return domain.ListItem{}, err
	}
	if err := m.Store.CreateListItem(ctx, value, event); err != nil {
		return domain.ListItem{}, err
	}
	return value, nil
}

func (m Messages) GetListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID) (domain.ListItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ListItem{}, err
	}
	return m.Store.GetListItem(ctx, workspaceID, listID, itemID)
}

func (m Messages) ListItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, request domain.PageRequest, archived bool) (domain.ListItemPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ListItemPage{}, err
	}
	return m.Store.ListItems(ctx, workspaceID, listID, request, archived)
}

func (m Messages) UpdateListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, fields string, archived bool) (domain.ListItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ListItem{}, err
	}
	value, err := m.Store.GetListItem(ctx, workspaceID, listID, itemID)
	if err != nil {
		return domain.ListItem{}, err
	}
	value.Fields, err = normalizeJSONArray(fields, value.Fields)
	if err != nil {
		return domain.ListItem{}, ErrInvalidList
	}
	value.Archived = archived
	value.UpdatedBy = userID
	value.UpdatedAt = time.Now().UTC()
	event, err := listEvent(workspaceID, userID, "list.item.updated", string(itemID))
	if err != nil {
		return domain.ListItem{}, err
	}
	if err := m.Store.UpdateListItem(ctx, value, event); err != nil {
		return domain.ListItem{}, err
	}
	return value, nil
}

func (m Messages) UpdateListCells(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, cells string) ([]domain.ListItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cells), &input); err != nil || len(input) == 0 {
		return nil, ErrInvalidList
	}
	grouped := make(map[domain.ListItemID][]map[string]json.RawMessage)
	for _, cell := range input {
		var rowID string
		if err := json.Unmarshal(cell["row_id"], &rowID); err != nil || strings.TrimSpace(rowID) == "" {
			return nil, ErrInvalidList
		}
		grouped[domain.ListItemID(rowID)] = append(grouped[domain.ListItemID(rowID)], cell)
	}
	result := make([]domain.ListItem, 0, len(grouped))
	for itemID, cellsForItem := range grouped {
		item, err := m.Store.GetListItem(ctx, workspaceID, listID, itemID)
		if err != nil {
			return nil, err
		}
		var fields []map[string]any
		if err := json.Unmarshal([]byte(item.Fields), &fields); err != nil {
			return nil, ErrInvalidList
		}
		for _, cell := range cellsForItem {
			columnID := ""
			if err := json.Unmarshal(cell["column_id"], &columnID); err != nil || columnID == "" {
				return nil, ErrInvalidList
			}
			updated := false
			for index := range fields {
				if value, ok := fields[index]["column_id"].(string); ok && value == columnID {
					for key, raw := range cell {
						if key != "row_id" {
							var decoded any
							if err := json.Unmarshal(raw, &decoded); err != nil {
								return nil, ErrInvalidList
							}
							fields[index][key] = decoded
						}
					}
					updated = true
					break
				}
			}
			if !updated {
				newField := make(map[string]any, len(cell))
				for key, raw := range cell {
					if key == "row_id" {
						continue
					}
					var decoded any
					if err := json.Unmarshal(raw, &decoded); err != nil {
						return nil, ErrInvalidList
					}
					newField[key] = decoded
				}
				fields = append(fields, newField)
			}
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		item.Fields = string(encoded)
		item.UpdatedBy = userID
		item.UpdatedAt = time.Now().UTC()
		event, err := listEvent(workspaceID, userID, "list.item.updated", string(itemID))
		if err != nil {
			return nil, err
		}
		if err := m.Store.UpdateListItem(ctx, item, event); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (m Messages) DeleteListItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemIDs []domain.ListItemID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if len(itemIDs) == 0 {
		return ErrInvalidList
	}
	event, err := listEvent(workspaceID, userID, "list.items.deleted", string(listID))
	if err != nil {
		return err
	}
	return m.Store.DeleteListItems(ctx, workspaceID, listID, itemIDs, event)
}

func (m Messages) SetListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, access string, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if _, err := m.Store.GetList(ctx, workspaceID, listID); err != nil {
		return err
	}
	if err := validateListAccess(access, channelIDs, userIDs); err != nil {
		return err
	}
	if access == "owner" {
		for _, target := range userIDs {
			user, err := m.Store.GetUser(ctx, target)
			if err != nil || user.WorkspaceID != workspaceID {
				return store.ErrNotFound
			}
		}
	}
	for _, target := range channelIDs {
		conversation, err := m.Store.GetConversation(ctx, target)
		if err != nil || conversation.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
	}
	for _, target := range userIDs {
		user, err := m.Store.GetUser(ctx, target)
		if err != nil || user.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
	}
	for _, target := range channelIDs {
		event, err := listEvent(workspaceID, userID, "list.access.set", string(listID)+"|channel|"+string(target))
		if err != nil {
			return err
		}
		if err := m.Store.SetListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: "channel", EntityID: string(target), Access: access}, event); err != nil {
			return err
		}
	}
	for _, target := range userIDs {
		event, err := listEvent(workspaceID, userID, "list.access.set", string(listID)+"|user|"+string(target))
		if err != nil {
			return err
		}
		if err := m.Store.SetListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: "user", EntityID: string(target), Access: access}, event); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) DeleteListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if _, err := m.Store.GetList(ctx, workspaceID, listID); err != nil {
		return err
	}
	if (len(channelIDs) == 0) == (len(userIDs) == 0) {
		return ErrInvalidList
	}
	for _, target := range channelIDs {
		event, err := listEvent(workspaceID, userID, "list.access.deleted", string(listID)+"|channel|"+string(target))
		if err != nil {
			return err
		}
		if err := m.Store.DeleteListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: "channel", EntityID: string(target)}, event); err != nil {
			return err
		}
	}
	for _, target := range userIDs {
		event, err := listEvent(workspaceID, userID, "list.access.deleted", string(listID)+"|user|"+string(target))
		if err != nil {
			return err
		}
		if err := m.Store.DeleteListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: "user", EntityID: string(target)}, event); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) StartListDownload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, includeArchived bool) (domain.ListDownload, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ListDownload{}, err
	}
	if _, err := m.Store.GetList(ctx, workspaceID, listID); err != nil {
		return domain.ListDownload{}, err
	}
	id, err := domain.NewListDownloadID()
	if err != nil {
		return domain.ListDownload{}, err
	}
	now := time.Now().UTC()
	value := domain.ListDownload{ID: id, ListID: listID, WorkspaceID: workspaceID, Status: "COMPLETED", URL: fmt.Sprintf("/internal/slack-lists/download.csv?list_id=%s&job_id=%s", listID, id), IncludeArchived: includeArchived, CreatedAt: now}
	event, err := listEvent(workspaceID, userID, "list.download.started", string(id))
	if err != nil {
		return domain.ListDownload{}, err
	}
	if err := m.Store.CreateListDownload(ctx, value, event); err != nil {
		return domain.ListDownload{}, err
	}
	return value, nil
}

func (m Messages) GetListDownload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListDownloadID) (domain.ListDownload, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ListDownload{}, err
	}
	return m.Store.GetListDownload(ctx, workspaceID, id)
}

func validateListAccess(access string, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if access != "read" && access != "write" && access != "owner" || (len(channelIDs) == 0) == (len(userIDs) == 0) {
		return ErrInvalidList
	}
	if access == "owner" && len(channelIDs) > 0 {
		return ErrInvalidList
	}
	return nil
}

func normalizeJSONArray(value, defaultValue string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return defaultValue, nil
	}
	var decoded []json.RawMessage
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(decoded)
	return string(encoded), err
}

func listEvent(workspaceID domain.WorkspaceID, actorID domain.UserID, topic, payload string) (events.Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return events.Event{}, err
	}
	return events.Event{ID: id, WorkspaceID: workspaceID, ActorID: actorID, Topic: topic, Payload: payload, CreatedAt: time.Now().UTC()}, nil
}
