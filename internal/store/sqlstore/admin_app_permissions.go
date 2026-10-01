package sqlstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The entity and channel lists of an access control list are JSON because a
// list is read and written whole: no query asks which apps name one user.

func (s *Store) GetAppPermission(ctx context.Context, workspace domain.WorkspaceID, app domain.AppID) (domain.AppPermission, error) {
	var value domain.AppPermission
	var permissionType, mode, users, groups, channels string
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT app_id, workspace_id, permission_type, user_ids, usergroup_ids, channel_restriction_mode, channel_ids, updated_at
		FROM app_permissions WHERE workspace_id = ? AND app_id = ?`, workspace, app).
		Scan(&value.AppID, &value.WorkspaceID, &permissionType, &users, &groups, &mode, &channels, &updated)
	if err != nil {
		return domain.AppPermission{}, classify(err)
	}
	value.PermissionType = domain.AppPermissionType(permissionType)
	value.ChannelRestrictionMode = domain.ChannelRestrictionMode(mode)
	if err := decodeJSONList(users, &value.UserIDs); err != nil {
		return domain.AppPermission{}, err
	}
	if err := decodeJSONList(groups, &value.UserGroupIDs); err != nil {
		return domain.AppPermission{}, err
	}
	if err := decodeJSONList(channels, &value.ChannelIDs); err != nil {
		return domain.AppPermission{}, err
	}
	value.UpdatedAt = time.Unix(0, updated).UTC()
	return value, nil
}

func (s *Store) SetAppPermission(ctx context.Context, value domain.AppPermission, event events.Event) error {
	if err := value.Check(); err != nil {
		return store.InvalidArgument("invalid app permission")
	}
	users, err := encodeJSONList(value.UserIDs)
	if err != nil {
		return err
	}
	groups, err := encodeJSONList(value.UserGroupIDs)
	if err != nil {
		return err
	}
	channels, err := encodeJSONList(value.ChannelIDs)
	if err != nil {
		return err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_permissions(app_id, workspace_id, permission_type, user_ids, usergroup_ids, channel_restriction_mode, channel_ids, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, app_id) DO UPDATE SET permission_type = excluded.permission_type,
			user_ids = excluded.user_ids, usergroup_ids = excluded.usergroup_ids,
			channel_restriction_mode = excluded.channel_restriction_mode, channel_ids = excluded.channel_ids,
			updated_at = excluded.updated_at`,
		value.AppID, value.WorkspaceID, string(value.PermissionType), users, groups,
		string(value.ChannelRestrictionMode), channels, value.UpdatedAt.UTC().UnixNano()); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListMCPServerPermissions(ctx context.Context, workspace domain.WorkspaceID, app domain.AppID) ([]domain.MCPServerPermission, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace_id, app_id, server_id, permission_type, user_ids, usergroup_ids, updated_at
		FROM mcp_server_permissions WHERE workspace_id = ? AND app_id = ? ORDER BY server_id`, workspace, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.MCPServerPermission, 0)
	for rows.Next() {
		var value domain.MCPServerPermission
		var permissionType, users, groups string
		var updated int64
		if err := rows.Scan(&value.WorkspaceID, &value.AppID, &value.ServerID, &permissionType, &users, &groups, &updated); err != nil {
			return nil, err
		}
		value.PermissionType = domain.MCPServerPermissionType(permissionType)
		if err := decodeJSONList(users, &value.UserIDs); err != nil {
			return nil, err
		}
		if err := decodeJSONList(groups, &value.UserGroupIDs); err != nil {
			return nil, err
		}
		value.UpdatedAt = time.Unix(0, updated).UTC()
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) SetMCPServerPermission(ctx context.Context, value domain.MCPServerPermission, event events.Event) error {
	if value.WorkspaceID == "" || value.AppID == "" || value.ServerID == "" || !value.PermissionType.Valid() {
		return store.InvalidArgument("invalid MCP server permission")
	}
	users, err := encodeJSONList(value.UserIDs)
	if err != nil {
		return err
	}
	groups, err := encodeJSONList(value.UserGroupIDs)
	if err != nil {
		return err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO mcp_server_permissions(workspace_id, app_id, server_id, permission_type, user_ids, usergroup_ids, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, app_id, server_id) DO UPDATE SET permission_type = excluded.permission_type,
			user_ids = excluded.user_ids, usergroup_ids = excluded.usergroup_ids, updated_at = excluded.updated_at`,
		value.WorkspaceID, value.AppID, value.ServerID, string(value.PermissionType), users, groups,
		value.UpdatedAt.UTC().UnixNano()); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// encodeJSONList writes a nil list as [] so the column never holds null.
func encodeJSONList[T ~string](values []T) (string, error) {
	if values == nil {
		values = []T{}
	}
	body, err := json.Marshal(values)
	return string(body), err
}

// decodeJSONList reads an empty list back as empty rather than nil, as the
// memory profile does.
func decodeJSONList[T ~string](raw string, target *[]T) error {
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return err
	}
	if *target == nil {
		*target = []T{}
	}
	return nil
}
