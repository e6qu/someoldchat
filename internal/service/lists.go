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

// documentAccess ranks the access one grant confers on a list or a canvas.
//
// Lists and canvases are the same access model written twice: SetListAccess and
// SetCanvasAccess both persist the strings "read", "write" and "owner" against a
// user or a channel, and both surfaces authorized on workspace membership alone,
// so every member of the workspace could read, edit and delete every other
// member's document. Both are now authorized through one ordered scale, so the
// two surfaces cannot drift apart again.
// requireDocumentAccess is the single authorization primitive for a list or a
// canvas. resolve reads the effective grant from the store, which returns the
// highest-ranked grant that applies — ownership, a grant on the user, or a grant
// on a channel the user belongs to — and store.ErrNotFound when none does.
//
// An insufficient grant is reported as store.ErrNotFound, the same answer a
// document that does not exist gets. A caller who holds no grant learns neither
// that the document exists nor who owns it, and no new error class has to cross
// the transport boundary for the refusal to keep its meaning.
func (m Messages) requireDocumentAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, required domain.AccessLevel, resolve func(context.Context, domain.UserID) (domain.AccessLevel, error)) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	granted, err := resolve(ctx, userID)
	if err != nil {
		return err
	}
	if granted.Rank() < required.Rank() {
		return store.ErrNotFound
	}
	return nil
}

// authorizeDocumentChannels is the check a document grant addressed to a
// conversation must pass: you may only share a list or a canvas into a
// conversation you can reach yourself.
//
// Both grant surfaces used to test only that the conversation existed in the
// same workspace, so any member could address a private channel by identifier
// and plant an attacker-authored document — with write access — into a
// confidential space they are not in and cannot read. The refusal is
// authorizeConversation's own store.ErrNotFound, so naming a private channel the
// actor is not in is indistinguishable from naming one that does not exist.
func (m Messages) authorizeDocumentChannels(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channelIDs []domain.ConversationID) error {
	for _, channelID := range channelIDs {
		if err := m.authorizeConversation(ctx, workspaceID, userID, channelID); err != nil {
			return err
		}
	}
	return nil
}

// requireListAccess authorizes one operation on one list.
func (m Messages) requireListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, required domain.AccessLevel) error {
	return m.requireDocumentAccess(ctx, workspaceID, userID, required, func(ctx context.Context, actor domain.UserID) (domain.AccessLevel, error) {
		grant, err := m.Store.GetListAccess(ctx, listID, actor)
		return grant.Access, err
	})
}

// requireCanvasAccess authorizes one operation on one canvas.
func (m Messages) requireCanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, canvasID domain.CanvasID, required domain.AccessLevel) error {
	return m.requireDocumentAccess(ctx, workspaceID, userID, required, func(ctx context.Context, actor domain.UserID) (domain.AccessLevel, error) {
		grant, err := m.Store.GetCanvasAccess(ctx, canvasID, actor)
		return grant.Access, err
	})
}

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
	// A schema that cannot be read is refused at the door rather than stored
	// and discovered later by every reader of the list.
	if _, err := domain.ParseListSchema(schema); err != nil {
		return domain.List{}, ErrInvalidList
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
	value := domain.List{ID: id, WorkspaceID: workspaceID, OwnerID: userID, Name: name, DescriptionBlocks: descriptionBlocks, Schema: schema, TodoMode: todoMode, Version: 1, CreatedAt: now, UpdatedAt: now}
	if copyFrom != "" {
		// Copying reads the source list's description and schema, and optionally
		// every record in it, so it needs read access to the source.
		if err := m.requireListAccess(ctx, workspaceID, userID, copyFrom, domain.AccessRead); err != nil {
			return domain.List{}, err
		}
		copied, err := m.Store.GetList(ctx, workspaceID, copyFrom)
		if err != nil {
			return domain.List{}, err
		}
		value.DescriptionBlocks = copied.DescriptionBlocks
		value.Schema = copied.Schema
	}
	// The whole copy is read and every record built before the list is created, so
	// a source too large to copy is refused before anything is written and before
	// list.created is published, and the work one request can demand is bounded.
	// The loop used to page the source with no cap, drawing an identifier and
	// opening a transaction per record, for any member holding read access on any
	// list.
	var copiedItems []domain.ListItem
	var copiedEvents []events.Event
	if copyFrom != "" && includeCopiedRecords {
		copiedItems, copiedEvents, err = m.copyListRecords(ctx, workspaceID, userID, copyFrom, id, now)
		if err != nil {
			return domain.List{}, err
		}
	}
	event, err := listEvent(workspaceID, userID, "list.created", events.String("list_id", string(id)))
	if err != nil {
		return domain.List{}, err
	}
	// One store call, one transaction. Creating the list, publishing list.created
	// and then copying the records one call at a time left a half-copied list
	// that clients had already been told about whenever a copy failed partway
	// through, and the caller could neither finish it nor undo it.
	items := make([]store.ListItemCreation, len(copiedItems))
	for index, item := range copiedItems {
		items[index] = store.ListItemCreation{Item: item, Event: copiedEvents[index]}
	}
	if err := m.Store.CreateListWithItems(ctx, value, event, items); err != nil {
		return domain.List{}, err
	}
	return value, nil
}

func (m Messages) List(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID) (domain.List, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return domain.List{}, err
	}
	return m.Store.GetList(ctx, workspaceID, id)
}

// ListGrants reports who a list is shared with, by the same rule CanvasGrants
// follows: read access is enough to ask, because a member who can open a
// document can already see the work in it, and a member deciding whether to
// share it needs to know it is not already shared.
func (m Messages) ListGrants(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID) ([]domain.ListAccess, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return nil, err
	}
	return m.Store.ListListGrants(ctx, workspaceID, id)
}

func (m Messages) ListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID) (domain.ListAccess, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ListAccess{}, err
	}
	return m.Store.GetListAccess(ctx, id, userID)
}

func (m Messages) Lists(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, page domain.PageRequest) (domain.ListPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ListPage{}, err
	}
	return m.Store.ListLists(ctx, workspaceID, userID, page)
}

// maxCopiedListRecords bounds lists.create with copy_from. A copy larger than
// this is refused rather than ground through one write transaction per record.
const maxCopiedListRecords = 1000

// copyListRecords reads the source list's records and builds the records and
// journal entries the copy will write. It performs no write of its own, so the
// caller can refuse the whole request before it has created anything.
func (m Messages) copyListRecords(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, copyFrom, into domain.ListID, now time.Time) ([]domain.ListItem, []events.Event, error) {
	items := make([]domain.ListItem, 0, 100)
	records := make([]events.Event, 0, 100)
	cursor := domain.Cursor("")
	for {
		page, err := m.Store.ListItems(ctx, workspaceID, copyFrom, domain.PageRequest{Limit: 100, Cursor: cursor}, false)
		if err != nil {
			return nil, nil, err
		}
		for _, source := range page.Items {
			if len(items) >= maxCopiedListRecords {
				return nil, nil, ErrInvalidList
			}
			itemID, err := domain.NewListItemID()
			if err != nil {
				return nil, nil, err
			}
			created, err := listEvent(workspaceID, userID, "list.item.created", events.String("list_item_id", string(itemID)), events.String("list_id", string(into)))
			if err != nil {
				return nil, nil, err
			}
			items = append(items, domain.ListItem{ID: itemID, ListID: into, WorkspaceID: workspaceID, Fields: source.Fields, CreatedBy: userID, UpdatedBy: userID, Version: 1, CreatedAt: now, UpdatedAt: now})
			records = append(records, created)
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
			return items, records, nil
		}
		cursor = page.NextCursor
	}
}

func (m Messages) UpdateList(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID, name, descriptionBlocks string, todoMode, todoModeSet bool) (domain.List, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, id, domain.AccessWrite); err != nil {
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
	value.Version++
	value.UpdatedAt = time.Now().UTC()
	event, err := listEvent(workspaceID, userID, "list.updated", events.String("list_id", string(id)))
	if err != nil {
		return domain.List{}, err
	}
	if err := m.Store.UpdateList(ctx, value, event); err != nil {
		return domain.List{}, err
	}
	return value, nil
}

// AddListColumn declares a new column on an existing list.
//
// It appends rather than replacing the schema, because every item's cells
// reference the columns already there by key: rewriting the schema wholesale
// would let one edit silently orphan every value in the list. A new column is
// additive by construction — no item has a value under it yet, so no item stops
// conforming.
//
// The key is minted from the name and made unique, so two columns called
// "Status" are survivable rather than a collision.
func (m Messages) AddListColumn(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID, name string, columnType domain.ListColumnType, options []string) (domain.List, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, id, domain.AccessWrite); err != nil {
		return domain.List{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || !columnType.Valid() {
		return domain.List{}, ErrInvalidList
	}
	value, err := m.Store.GetList(ctx, workspaceID, id)
	if err != nil {
		return domain.List{}, err
	}
	columns, err := domain.ParseListSchema(value.Schema)
	if err != nil {
		return domain.List{}, ErrInvalidList
	}
	if len(columns) >= domain.ListColumnLimit {
		return domain.List{}, ErrInvalidList
	}
	cleaned := make([]string, 0, len(options))
	for _, option := range options {
		if trimmed := strings.TrimSpace(option); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	// A select with no options is a text column that refuses every value, so it
	// is refused here rather than created and discovered by whoever tries to
	// fill it in.
	if columnType == domain.ListColumnSelect && len(cleaned) == 0 {
		return domain.List{}, ErrInvalidList
	}
	added := domain.ListColumn{Key: domain.ListColumnKey(name, columns), Name: name, Type: columnType, Options: cleaned}
	// The first declared column is the primary one, so a list that had only the
	// substituted default keeps showing something in its first position.
	if len(columns) == 0 {
		added.Primary = true
	}
	schema, err := domain.EncodeListSchema(append(columns, added))
	if err != nil {
		return domain.List{}, err
	}
	value.Schema = schema
	value.Version++
	value.UpdatedAt = time.Now().UTC()
	event, err := listEvent(workspaceID, userID, "list.updated", events.String("list_id", string(id)), events.String("column_key", added.Key))
	if err != nil {
		return domain.List{}, err
	}
	if err := m.Store.UpdateList(ctx, value, event); err != nil {
		return domain.List{}, err
	}
	return value, nil
}

// RemoveListColumn drops a column and everything recorded under it.
//
// This is the counterpart of AddListColumn and is deliberately louder: adding a
// column cannot invalidate anything, while removing one deletes a value from
// every item that had it. The values go rather than being left orphaned,
// because a cell under no column is invisible to every reader, is carried by
// every later edit, and would come back the day a new column minted the same
// key.
//
// The primary column stays. It is what an item is called wherever a list is
// shown as a row, so a list without one renders as unlabelled cells.
func (m Messages) RemoveListColumn(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ListID, key string) (domain.List, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, id, domain.AccessWrite); err != nil {
		return domain.List{}, err
	}
	value, err := m.Store.GetList(ctx, workspaceID, id)
	if err != nil {
		return domain.List{}, err
	}
	columns, err := domain.ParseListSchema(value.Schema)
	if err != nil {
		return domain.List{}, ErrInvalidList
	}
	remaining, err := domain.RemoveListColumn(columns, key)
	if err != nil {
		return domain.List{}, ErrInvalidList
	}
	schema, err := domain.EncodeListSchema(remaining)
	if err != nil {
		return domain.List{}, err
	}
	value.Schema = schema
	value.Version++
	value.UpdatedAt = time.Now().UTC()
	event, err := listEvent(workspaceID, userID, "list.updated", events.String("list_id", string(id)), events.String("removed_column_key", strings.TrimSpace(key)))
	if err != nil {
		return domain.List{}, err
	}
	if err := m.Store.RemoveListColumn(ctx, value, strings.TrimSpace(key), event); err != nil {
		return domain.List{}, err
	}
	return value, nil
}

func (m Messages) CreateListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, parentItemID domain.ListItemID, fields string) (domain.ListItem, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessWrite); err != nil {
		return domain.ListItem{}, err
	}
	list, err := m.Store.GetList(ctx, workspaceID, listID)
	if err != nil {
		return domain.ListItem{}, err
	}
	fields, err = normalizeJSONArray(fields, "[]")
	if err != nil {
		return domain.ListItem{}, ErrInvalidList
	}
	// A cell under a column nobody declared is invisible to every reader of the
	// list, so accepting it silently would lose the member's work while looking
	// like it had been saved.
	if err := domain.ValidateListFields(list.Schema, fields, ""); err != nil {
		return domain.ListItem{}, ErrInvalidList
	}
	id, err := domain.NewListItemID()
	if err != nil {
		return domain.ListItem{}, err
	}
	now := time.Now().UTC()
	value := domain.ListItem{ID: id, ListID: listID, ParentItemID: parentItemID, WorkspaceID: workspaceID, Fields: fields, CreatedBy: userID, UpdatedBy: userID, Version: 1, CreatedAt: now, UpdatedAt: now}
	event, err := listEvent(workspaceID, userID, "list.item.created", events.String("list_item_id", string(id)), events.String("list_id", string(listID)))
	if err != nil {
		return domain.ListItem{}, err
	}
	if err := m.Store.CreateListItem(ctx, value, event); err != nil {
		return domain.ListItem{}, err
	}
	return value, nil
}

func (m Messages) GetListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID) (domain.ListItem, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessRead); err != nil {
		return domain.ListItem{}, err
	}
	return m.Store.GetListItem(ctx, workspaceID, listID, itemID)
}

func (m Messages) ListItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, request domain.PageRequest, archived bool) (domain.ListItemPage, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessRead); err != nil {
		return domain.ListItemPage{}, err
	}
	return m.Store.ListItems(ctx, workspaceID, listID, request, archived)
}

func (m Messages) UpdateListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, fields string, archived bool) (domain.ListItem, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessWrite); err != nil {
		return domain.ListItem{}, err
	}
	value, err := m.Store.GetListItem(ctx, workspaceID, listID, itemID)
	if err != nil {
		return domain.ListItem{}, err
	}
	previousFields := value.Fields
	value.Fields, err = normalizeJSONArray(fields, value.Fields)
	if err != nil {
		return domain.ListItem{}, ErrInvalidList
	}
	list, err := m.Store.GetList(ctx, workspaceID, listID)
	if err != nil {
		return domain.ListItem{}, err
	}
	if err := domain.ValidateListFields(list.Schema, value.Fields, previousFields); err != nil {
		return domain.ListItem{}, ErrInvalidList
	}
	value.Archived = archived
	value.UpdatedBy = userID
	value.Version++
	value.UpdatedAt = time.Now().UTC()
	event, err := listEvent(workspaceID, userID, "list.item.updated", events.String("list_item_id", string(itemID)), events.String("list_id", string(listID)))
	if err != nil {
		return domain.ListItem{}, err
	}
	if err := m.Store.UpdateListItem(ctx, value, event); err != nil {
		return domain.ListItem{}, err
	}
	return value, nil
}

// ListAccessFor reports whether a member may open a list, which is the question
// an assignment picker has to answer before offering someone. It is the same
// check AssignListItem enforces, so the control cannot offer a choice the write
// would refuse.
func (m Messages) ListAccessFor(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID) error {
	return m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessRead)
}

// AssignListItem records who an item is for and when it is wanted, and tells
// them. An assignment nobody is told about is the same defect as a canvas
// shared in silence: the work exists and the person it belongs to has no way to
// find out.
//
// The assignee must be able to open the list. Assigning work to someone who
// cannot see where it lives produces an item they are told about and cannot
// reach, which is worse than not being assigned it — so the check is the list's
// own read access rather than mere workspace membership.
//
// Clearing is accepted: an empty assignee and a zero due date are how a
// mistaken assignment is undone.
func (m Messages) AssignListItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemID domain.ListItemID, assignee domain.UserID, dueAt time.Time) (domain.ListItem, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessWrite); err != nil {
		return domain.ListItem{}, err
	}
	if assignee != "" {
		if err := m.requireListAccess(ctx, workspaceID, assignee, listID, domain.AccessRead); err != nil {
			return domain.ListItem{}, ErrInvalidList
		}
	}
	value, err := m.Store.GetListItem(ctx, workspaceID, listID, itemID)
	if err != nil {
		return domain.ListItem{}, err
	}
	previous := value.AssigneeID
	value.AssigneeID = assignee
	value.DueAt = dueAt.UTC()
	value.UpdatedBy = userID
	value.Version++
	value.UpdatedAt = time.Now().UTC()
	event, err := listEvent(workspaceID, userID, "list.item.assigned", events.String("list_item_id", string(itemID)), events.String("list_id", string(listID)), events.String("assignee_id", string(assignee)))
	if err != nil {
		return domain.ListItem{}, err
	}
	if err := m.Store.UpdateListItem(ctx, value, event); err != nil {
		return domain.ListItem{}, err
	}
	// Only a change of hands is news, and only to the person receiving them.
	// Re-saving a due date on an item someone already holds is not an
	// assignment, and assigning something to yourself is not being told.
	if assignee != "" && assignee != previous && assignee != userID {
		if err := m.Store.RecordListAssignment(ctx, value, userID, event.CreatedAt); err != nil {
			return domain.ListItem{}, err
		}
	}
	return value, nil
}

func (m Messages) UpdateListCells(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, cells string) ([]domain.ListItem, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessWrite); err != nil {
		return nil, err
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cells), &input); err != nil || len(input) == 0 {
		return nil, ErrInvalidList
	}
	// Rows are kept in the order the request names them. Iterating the grouping
	// map directly made the returned order — and the order the writes happened in
	// — depend on Go's map seed, so the same request produced a different answer
	// on every call and a mid-batch failure left a different prefix each time.
	order := make([]domain.ListItemID, 0, len(input))
	grouped := make(map[domain.ListItemID][]map[string]json.RawMessage, len(input))
	for _, cell := range input {
		var rowID string
		if err := json.Unmarshal(cell["row_id"], &rowID); err != nil || strings.TrimSpace(rowID) == "" {
			return nil, ErrInvalidList
		}
		itemID := domain.ListItemID(rowID)
		if _, seen := grouped[itemID]; !seen {
			order = append(order, itemID)
		}
		grouped[itemID] = append(grouped[itemID], cell)
	}
	// Every row is read and every edit computed before any of them is written, so
	// a batch naming a missing row or an unparseable cell is refused whole
	// instead of committing the rows that happened to come first. The writes
	// themselves are still one transaction per row; see the store signature
	// UpdateListItems reported alongside this change.
	result := make([]domain.ListItem, 0, len(order))
	pending := make([]events.Event, 0, len(order))
	for _, itemID := range order {
		cellsForItem := grouped[itemID]
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
		item.Version++
		item.UpdatedAt = time.Now().UTC()
		event, err := listEvent(workspaceID, userID, "list.item.updated", events.String("list_item_id", string(itemID)), events.String("list_id", string(listID)))
		if err != nil {
			return nil, err
		}
		result = append(result, item)
		pending = append(pending, event)
	}
	if err := m.Store.UpdateListItems(ctx, result, pending); err != nil {
		return nil, err
	}
	return result, nil
}

func (m Messages) DeleteListItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, itemIDs []domain.ListItemID) error {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessWrite); err != nil {
		return err
	}
	if len(itemIDs) == 0 {
		return ErrInvalidList
	}
	event, err := listEvent(workspaceID, userID, "list.items.deleted", events.String("list_id", string(listID)), events.Strings("list_item_ids", listItemIDStrings(itemIDs)))
	if err != nil {
		return err
	}
	return m.Store.DeleteListItems(ctx, workspaceID, listID, itemIDs, event)
}

func (m Messages) SetListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, access domain.AccessLevel, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	// Granting access is the strongest operation on a list: write access must not
	// be enough to hand the list to anyone else.
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessOwner); err != nil {
		return err
	}
	if _, err := m.Store.GetList(ctx, workspaceID, listID); err != nil {
		return err
	}
	if err := validateListAccess(access, channelIDs, userIDs); err != nil {
		return err
	}
	if err := m.authorizeDocumentChannels(ctx, workspaceID, userID, channelIDs); err != nil {
		return err
	}
	for _, target := range userIDs {
		user, err := m.Store.GetUser(ctx, target)
		if err != nil || user.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
	}
	for _, target := range channelIDs {
		event, err := listEvent(workspaceID, userID, "list.access.set", events.String("list_id", string(listID)), events.String("entity_type", "channel"), events.String("entity_id", string(target)), events.String("access", string(access)))
		if err != nil {
			return err
		}
		if err := m.Store.SetListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: domain.GrantChannel, EntityID: string(target), Access: access}, event); err != nil {
			return err
		}
	}
	for _, target := range userIDs {
		event, err := listEvent(workspaceID, userID, "list.access.set", events.String("list_id", string(listID)), events.String("entity_type", "user"), events.String("entity_id", string(target)), events.String("access", string(access)))
		if err != nil {
			return err
		}
		if err := m.Store.SetListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: domain.GrantUser, EntityID: string(target), Access: access}, event); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) DeleteListAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessOwner); err != nil {
		return err
	}
	if _, err := m.Store.GetList(ctx, workspaceID, listID); err != nil {
		return err
	}
	if (len(channelIDs) == 0) == (len(userIDs) == 0) {
		return ErrInvalidList
	}
	for _, target := range channelIDs {
		event, err := listEvent(workspaceID, userID, "list.access.deleted", events.String("list_id", string(listID)), events.String("entity_type", "channel"), events.String("entity_id", string(target)))
		if err != nil {
			return err
		}
		if err := m.Store.DeleteListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: domain.GrantChannel, EntityID: string(target)}, event); err != nil {
			return err
		}
	}
	for _, target := range userIDs {
		event, err := listEvent(workspaceID, userID, "list.access.deleted", events.String("list_id", string(listID)), events.String("entity_type", "user"), events.String("entity_id", string(target)))
		if err != nil {
			return err
		}
		if err := m.Store.DeleteListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: domain.GrantUser, EntityID: string(target)}, event); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) StartListDownload(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, listID domain.ListID, includeArchived bool) (domain.ListDownload, error) {
	if err := m.requireListAccess(ctx, workspaceID, userID, listID, domain.AccessRead); err != nil {
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
	event, err := listEvent(workspaceID, userID, "list.download.started", events.String("list_download_id", string(id)), events.String("list_id", string(listID)))
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
	value, err := m.Store.GetListDownload(ctx, workspaceID, id)
	if err != nil {
		return domain.ListDownload{}, err
	}
	// A download job carries the exported list's contents, so reading the job is
	// a read of the list. Checking the job's own identifier only would let a
	// second member collect an export of a list they cannot open.
	if err := m.requireListAccess(ctx, workspaceID, userID, value.ListID, domain.AccessRead); err != nil {
		return domain.ListDownload{}, err
	}
	return value, nil
}

func validateListAccess(access domain.AccessLevel, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if !access.Valid() || (len(channelIDs) == 0) == (len(userIDs) == 0) {
		return ErrInvalidList
	}
	if access == domain.AccessOwner && len(channelIDs) > 0 {
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

func listEvent(workspaceID domain.WorkspaceID, actorID domain.UserID, topic string, fields ...events.Field) (events.Event, error) {
	return newEvent(workspaceID, actorID, events.NewPayload(topic, fields...), time.Now().UTC())
}

func listItemIDStrings(values []domain.ListItemID) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}
