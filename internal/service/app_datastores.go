package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

const maxAppDatastoreItemBytes = 400 << 10

var (
	ErrAppNotHosted         = errors.New("application is not Slack-hosted")
	ErrAppDatastoreNotFound = errors.New("application datastore was not found")
	ErrInvalidDatastoreItem = errors.New("application datastore item is invalid")
)

// PutAppDatastoreItems replaces or merges one to 25 items in a declared
// Slack-hosted app datastore. Items cross the process boundary as canonical
// JSON so local and gRPC-backed deployments apply exactly the same validation.
func (m Messages) PutAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, rawItems []string, merge bool) ([]string, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	snapshot, datastoreDefinition, err := m.appDatastore(ctx, workspaceID, appID, datastore)
	if err != nil {
		return nil, err
	}
	if len(rawItems) == 0 || len(rawItems) > 25 {
		return nil, fmt.Errorf("%w: a request must contain 1 to 25 items", ErrInvalidDatastoreItem)
	}

	items := make([]map[string]any, len(rawItems))
	ids := make([]string, len(rawItems))
	seen := make(map[string]struct{}, len(rawItems))
	for index, raw := range rawItems {
		item, id, err := decodeAppDatastoreItem(raw, datastoreDefinition)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate primary key %q", ErrInvalidDatastoreItem, id)
		}
		seen[id] = struct{}{}
		items[index], ids[index] = item, id
	}

	now := time.Now().UTC()
	values := make([]domain.AppDatastoreItem, len(items))
	canonical := make([]string, len(items))
	for index, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidDatastoreItem, err)
		}
		if len(encoded) > maxAppDatastoreItemBytes {
			return nil, fmt.Errorf("%w: item exceeds 400 KiB", ErrInvalidDatastoreItem)
		}
		canonical[index] = string(encoded)
		values[index] = domain.AppDatastoreItem{
			AppID: snapshot.App.ID, WorkspaceID: workspaceID, Datastore: datastoreDefinition.Name,
			ID: ids[index], Item: canonical[index], UpdatedAt: now,
		}
	}
	if merge {
		merged, err := m.Store.MergeAppDatastoreItems(ctx, values)
		if err != nil {
			return nil, err
		}
		for index, value := range merged {
			var item map[string]any
			decoder := json.NewDecoder(strings.NewReader(value.Item))
			decoder.UseNumber()
			if err := decoder.Decode(&item); err != nil {
				return nil, fmt.Errorf("read merged datastore item: %w", err)
			}
			if err := validateAppDatastoreObject(item, datastoreDefinition); err != nil {
				return nil, fmt.Errorf("merged item %d: %w", index, err)
			}
			canonical[index] = value.Item
		}
	} else if err := m.Store.PutAppDatastoreItems(ctx, values); err != nil {
		return nil, err
	}
	return canonical, nil
}

func (m Messages) GetAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, ids []string) ([]string, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	_, datastoreDefinition, err := m.appDatastore(ctx, workspaceID, appID, datastore)
	if err != nil {
		return nil, err
	}
	if err := validateDatastoreIDs(ids); err != nil {
		return nil, err
	}
	values, err := m.Store.GetAppDatastoreItems(ctx, appID, workspaceID, datastoreDefinition.Name, ids)
	if err != nil {
		return nil, err
	}
	items := make([]string, len(values))
	for index, value := range values {
		items[index] = value.Item
	}
	return items, nil
}

func (m Messages) DeleteAppDatastoreItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, datastore string, ids []string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	_, datastoreDefinition, err := m.appDatastore(ctx, workspaceID, appID, datastore)
	if err != nil {
		return err
	}
	if err := validateDatastoreIDs(ids); err != nil {
		return err
	}
	return m.Store.DeleteAppDatastoreItems(ctx, appID, workspaceID, datastoreDefinition.Name, ids)
}

func (m Messages) appDatastore(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID, name string) (domain.AppManifestSnapshot, appmanifest.Datastore, error) {
	name = strings.TrimSpace(name)
	if appID == "" || name == "" {
		return domain.AppManifestSnapshot{}, appmanifest.Datastore{}, ErrAppDatastoreNotFound
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, appID)
	if err != nil {
		return domain.AppManifestSnapshot{}, appmanifest.Datastore{}, err
	}
	if !parsed.IsHosted || parsed.FunctionRuntime != "slack" {
		return domain.AppManifestSnapshot{}, appmanifest.Datastore{}, ErrAppNotHosted
	}
	definition, exists := parsed.Datastores[name]
	if !exists {
		return domain.AppManifestSnapshot{}, appmanifest.Datastore{}, ErrAppDatastoreNotFound
	}
	return snapshot, definition, nil
}

func validateDatastoreIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > 25 {
		return fmt.Errorf("%w: a request must contain 1 to 25 ids", ErrInvalidDatastoreItem)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: id is required", ErrInvalidDatastoreItem)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate id %q", ErrInvalidDatastoreItem, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func decodeAppDatastoreItem(raw string, definition appmanifest.Datastore) (map[string]any, string, error) {
	if len(raw) == 0 || len(raw) > maxAppDatastoreItemBytes {
		return nil, "", fmt.Errorf("%w: item must be a JSON object no larger than 400 KiB", ErrInvalidDatastoreItem)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var item map[string]any
	if err := decoder.Decode(&item); err != nil || item == nil {
		return nil, "", fmt.Errorf("%w: item must be a JSON object", ErrInvalidDatastoreItem)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, "", fmt.Errorf("%w: item contains multiple JSON values", ErrInvalidDatastoreItem)
	} else if !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("%w: item contains invalid trailing JSON", ErrInvalidDatastoreItem)
	}
	if err := validateAppDatastoreObject(item, definition); err != nil {
		return nil, "", err
	}
	id, _ := item[definition.PrimaryKey].(string)
	return item, id, nil
}

func validateAppDatastoreObject(item map[string]any, definition appmanifest.Datastore) error {
	for name, value := range item {
		attribute, exists := definition.Attributes[name]
		if !exists {
			return fmt.Errorf("%w: attribute %q is not declared", ErrInvalidDatastoreItem, name)
		}
		if !validAppDatastoreValue(value, attribute.Type) {
			return fmt.Errorf("%w: attribute %q does not match %s", ErrInvalidDatastoreItem, name, attribute.Type)
		}
	}
	id, exists := item[definition.PrimaryKey].(string)
	if !exists || strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: primary key %q must be a non-empty string", ErrInvalidDatastoreItem, definition.PrimaryKey)
	}
	return nil
}

func validAppDatastoreValue(value any, declaredType string) bool {
	kind := strings.ToLower(strings.TrimSpace(declaredType))
	if slash := strings.LastIndex(kind, "/"); slash >= 0 {
		kind = kind[slash+1:]
	}
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean", "bool":
		_, ok := value.(bool)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "number":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := number.Float64()
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
	case "timestamp":
		switch timestamp := value.(type) {
		case string:
			return strings.TrimSpace(timestamp) != ""
		case json.Number:
			_, err := timestamp.Int64()
			return err == nil
		default:
			return false
		}
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		// Slack schema references cover richer platform types whose wire value is
		// defined by the referenced schema. Rejecting a structurally valid value
		// here would make the runtime less capable than the accepted manifest.
		return value != nil
	}
}
