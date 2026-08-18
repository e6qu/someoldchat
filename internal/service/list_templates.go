package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// SaveListAsTemplate captures a list's shape — its schema, whether it is a
// to-do list, and optionally its current rows as starter rows — into a reusable
// template the whole workspace can create new lists from. It needs read access
// to the source list, the same access copying one does.
func (m Messages) SaveListAsTemplate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, name, descriptionBlocks string, includeRecords bool) (domain.ListTemplate, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessRead); err != nil {
		return domain.ListTemplate{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return domain.ListTemplate{}, ErrInvalidListTemplate
	}
	list, err := m.Store.GetList(ctx, workspaceID, listID)
	if err != nil {
		return domain.ListTemplate{}, err
	}
	if strings.TrimSpace(descriptionBlocks) == "" {
		descriptionBlocks = list.DescriptionBlocks
	}
	descriptionBlocks, err = normalizeJSONArray(descriptionBlocks, "[]")
	if err != nil {
		return domain.ListTemplate{}, ErrInvalidListTemplate
	}
	seed := "[]"
	if includeRecords {
		seed, err = m.serializeListSeed(ctx, workspaceID, listID)
		if err != nil {
			return domain.ListTemplate{}, err
		}
	}
	return m.createListTemplate(ctx, workspaceID, userID, name, descriptionBlocks, list.Schema, list.TodoMode, seed)
}

// createListTemplate is the shared write path: it validates the schema and seed
// and records the template. A template with a schema no reader can parse, or
// seed rows that are not a JSON array, is refused at the door rather than stored
// and discovered later by whoever creates a list from it.
func (m Messages) createListTemplate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, descriptionBlocks, schema string, todoMode bool, seed string) (domain.ListTemplate, error) {
	if _, err := domain.ParseListSchema(schema); err != nil {
		return domain.ListTemplate{}, ErrInvalidListTemplate
	}
	schema, err := normalizeJSONArray(schema, "[]")
	if err != nil {
		return domain.ListTemplate{}, ErrInvalidListTemplate
	}
	seed, err = normalizeJSONArray(seed, "[]")
	if err != nil {
		return domain.ListTemplate{}, ErrInvalidListTemplate
	}
	id, err := domain.NewListTemplateID()
	if err != nil {
		return domain.ListTemplate{}, err
	}
	now := time.Now().UTC()
	template := domain.ListTemplate{ID: id, WorkspaceID: workspaceID, Creator: userID, Name: name, DescriptionBlocks: descriptionBlocks, Schema: schema, TodoMode: todoMode, SeedItems: seed, CreatedAt: now, UpdatedAt: now}
	if err := m.Store.CreateListTemplate(ctx, template); err != nil {
		return domain.ListTemplate{}, err
	}
	return template, nil
}

// serializeListSeed reads a list's rows into a JSON array of their field cells,
// the starter rows a template carries. It is bounded by the same record cap
// copying a list is, so a list too large to template is refused before anything
// is written.
func (m Messages) serializeListSeed(ctx context.Context, workspaceID domain.WorkspaceID, listID domain.ListID) (string, error) {
	rows := make([]json.RawMessage, 0, 100)
	cursor := domain.Cursor("")
	for {
		page, err := m.Store.ListItems(ctx, workspaceID, listID, domain.PageRequest{Limit: 100, Cursor: cursor}, false)
		if err != nil {
			return "", err
		}
		for _, item := range page.Items {
			if len(rows) >= maxCopiedListRecords {
				return "", ErrInvalidList
			}
			fields := strings.TrimSpace(item.Fields)
			if fields == "" {
				fields = "[]"
			}
			rows = append(rows, json.RawMessage(fields))
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return "", ErrInvalidList
	}
	return string(encoded), nil
}

// WorkspaceListTemplates lists a workspace's saved list templates. Any member
// may read them: a template is the workspace's shared shape, offered on the
// create-a-list surface.
func (m Messages) WorkspaceListTemplates(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.ListTemplate, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	return m.Store.ListListTemplates(ctx, workspaceID)
}

// CreateListFromTemplate instantiates a template into a new list owned by the
// member, copying the template's schema and starter rows. The rows are copied,
// not referenced, so deleting the template later leaves the list untouched.
func (m Messages) CreateListFromTemplate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, templateID domain.ListTemplateID, name string) (domain.List, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.List{}, err
	}
	template, err := m.Store.GetListTemplate(ctx, workspaceID, templateID)
	if err != nil {
		return domain.List{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = template.Name
	}
	if name == "" || len(name) > 200 {
		return domain.List{}, ErrInvalidList
	}
	listID, err := domain.NewListID()
	if err != nil {
		return domain.List{}, err
	}
	now := time.Now().UTC()
	value := domain.List{ID: listID, WorkspaceID: workspaceID, OwnerID: userID, Name: name, DescriptionBlocks: template.DescriptionBlocks, Schema: template.Schema, TodoMode: template.TodoMode, Version: 1, CreatedAt: now, UpdatedAt: now}
	items, records, err := m.seedItemsFromTemplate(workspaceID, userID, listID, template.SeedItems, now)
	if err != nil {
		return domain.List{}, err
	}
	event, err := listEvent(workspaceID, userID, "list.created", events.String("list_id", string(listID)))
	if err != nil {
		return domain.List{}, err
	}
	creations := make([]store.ListItemCreation, len(items))
	for index := range items {
		creations[index] = store.ListItemCreation{Item: items[index], Event: records[index]}
	}
	if err := m.Store.CreateListWithItems(ctx, value, event, creations); err != nil {
		return domain.List{}, err
	}
	return value, nil
}

// seedItemsFromTemplate turns a template's stored starter rows into items for a
// new list. A template whose seed is not a JSON array of cell arrays is refused
// rather than creating a list with rows nobody can read.
func (m Messages) seedItemsFromTemplate(workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, seed string, now time.Time) ([]domain.ListItem, []events.Event, error) {
	if strings.TrimSpace(seed) == "" {
		return nil, nil, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(seed), &rows); err != nil {
		return nil, nil, ErrInvalidListTemplate
	}
	items := make([]domain.ListItem, 0, len(rows))
	records := make([]events.Event, 0, len(rows))
	for _, row := range rows {
		if len(items) >= maxCopiedListRecords {
			return nil, nil, ErrInvalidListTemplate
		}
		itemID, err := domain.NewListItemID()
		if err != nil {
			return nil, nil, err
		}
		created, err := listEvent(workspaceID, userID, "list.item.created", events.String("list_item_id", string(itemID)), events.String("list_id", string(listID)))
		if err != nil {
			return nil, nil, err
		}
		items = append(items, domain.ListItem{ID: itemID, ListID: listID, WorkspaceID: workspaceID, Fields: string(row), CreatedBy: userID, UpdatedBy: userID, Version: 1, CreatedAt: now, UpdatedAt: now})
		records = append(records, created)
	}
	return items, records, nil
}

// DeleteListTemplate removes a saved template. Its creator may remove it, as may
// a workspace administrator; the lists already made from it are untouched.
func (m Messages) DeleteListTemplate(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, templateID domain.ListTemplateID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	template, err := m.Store.GetListTemplate(ctx, workspaceID, templateID)
	if err != nil {
		return err
	}
	if template.Creator != userID {
		if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
			return err
		}
	}
	return m.Store.DeleteListTemplate(ctx, workspaceID, templateID)
}
