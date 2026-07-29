package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (s *Store) MergeAppDatastoreItems(ctx context.Context, patches []domain.AppDatastoreItem) ([]domain.AppDatastoreItem, error) {
	if err := validateAppDatastoreValues(patches); err != nil {
		return nil, err
	}
	var values []domain.AppDatastoreItem
	err := underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		values = make([]domain.AppDatastoreItem, len(patches))
		for index, patch := range patches {
			var existing string
			err := tx.QueryRowContext(ctx, `SELECT item FROM app_datastore_items
				WHERE app_id = ? AND workspace_id = ? AND datastore = ? AND item_id = ?`,
				patch.AppID, patch.WorkspaceID, patch.Datastore, patch.ID).Scan(&existing)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return classify(err)
			}
			merged, err := store.MergeJSONObjects(existing, patch.Item)
			if err != nil {
				return err
			}
			patch.Item = merged
			values[index] = patch
		}
		for _, value := range values {
			if _, err := tx.ExecContext(ctx, `INSERT INTO app_datastore_items(app_id, workspace_id, datastore, item_id, item, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(app_id, workspace_id, datastore, item_id) DO UPDATE SET item = excluded.item, updated_at = excluded.updated_at`,
				value.AppID, value.WorkspaceID, value.Datastore, value.ID, value.Item, domain.NewStoredTime(value.UpdatedAt)); err != nil {
				return classify(err)
			}
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	return values, nil
}

func (s *Store) PutAppDatastoreItems(ctx context.Context, values []domain.AppDatastoreItem) error {
	if err := validateAppDatastoreValues(values); err != nil {
		return err
	}
	return underContention(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, value := range values {
			if _, err := tx.ExecContext(ctx, `INSERT INTO app_datastore_items(app_id, workspace_id, datastore, item_id, item, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(app_id, workspace_id, datastore, item_id) DO UPDATE SET item = excluded.item, updated_at = excluded.updated_at`,
				value.AppID, value.WorkspaceID, value.Datastore, value.ID, value.Item, domain.NewStoredTime(value.UpdatedAt)); err != nil {
				return classify(err)
			}
		}
		return tx.Commit()
	})
}

func validateAppDatastoreValues(values []domain.AppDatastoreItem) error {
	if len(values) == 0 || len(values) > 25 {
		return store.InvalidArgument("app datastore write must contain 1 to 25 items")
	}
	for _, value := range values {
		if value.AppID == "" || value.WorkspaceID == "" || strings.TrimSpace(value.Datastore) == "" ||
			strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.Item) == "" || value.UpdatedAt.IsZero() {
			return store.InvalidArgument("app datastore item is incomplete")
		}
	}
	return nil
}

func (s *Store) GetAppDatastoreItems(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID, datastore string, ids []string) ([]domain.AppDatastoreItem, error) {
	if appID == "" || workspaceID == "" || strings.TrimSpace(datastore) == "" || len(ids) == 0 || len(ids) > 25 {
		return nil, store.InvalidArgument("invalid app datastore read")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	arguments := make([]any, 0, 3+len(ids))
	arguments = append(arguments, appID, workspaceID, datastore)
	for _, id := range ids {
		arguments = append(arguments, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, item, updated_at FROM app_datastore_items
		WHERE app_id = ? AND workspace_id = ? AND datastore = ? AND item_id IN (`+placeholders+`)`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]domain.AppDatastoreItem, len(ids))
	for rows.Next() {
		value := domain.AppDatastoreItem{AppID: appID, WorkspaceID: workspaceID, Datastore: datastore}
		var updated string
		if err := rows.Scan(&value.ID, &value.Item, &updated); err != nil {
			return nil, err
		}
		value.UpdatedAt, err = domain.ParseStoredTime(updated)
		if err != nil {
			return nil, err
		}
		byID[value.ID] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]domain.AppDatastoreItem, 0, len(byID))
	for _, id := range ids {
		if value, exists := byID[id]; exists {
			values = append(values, value)
		}
	}
	return values, nil
}

func (s *Store) DeleteAppDatastoreItems(ctx context.Context, appID domain.AppID, workspaceID domain.WorkspaceID, datastore string, ids []string) error {
	if appID == "" || workspaceID == "" || strings.TrimSpace(datastore) == "" || len(ids) == 0 || len(ids) > 25 {
		return store.InvalidArgument("invalid app datastore delete")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	arguments := make([]any, 0, 3+len(ids))
	arguments = append(arguments, appID, workspaceID, datastore)
	for _, id := range ids {
		arguments = append(arguments, id)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_datastore_items WHERE app_id = ? AND workspace_id = ? AND datastore = ? AND item_id IN (`+placeholders+`)`, arguments...)
	return classify(err)
}
