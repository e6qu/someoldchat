package memory

import (
	"context"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func appDatastoreKey(appID domain.AppID, workspaceID domain.WorkspaceID, datastore, id string) string {
	return string(appID) + "\x00" + string(workspaceID) + "\x00" + datastore + "\x00" + id
}

func (s *Store) MergeAppDatastoreItems(_ context.Context, patches []domain.AppDatastoreItem) ([]domain.AppDatastoreItem, error) {
	if err := validateAppDatastoreValues(patches); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]domain.AppDatastoreItem, len(patches))
	for index, patch := range patches {
		key := appDatastoreKey(patch.AppID, patch.WorkspaceID, patch.Datastore, patch.ID)
		merged, err := store.MergeJSONObjects(s.appDatastoreItems[key].Item, patch.Item)
		if err != nil {
			return nil, err
		}
		patch.Item = merged
		values[index] = patch
	}
	for index, value := range values {
		s.appDatastoreItems[appDatastoreKey(value.AppID, value.WorkspaceID, value.Datastore, value.ID)] = value
		values[index] = value
	}
	return values, nil
}

func (s *Store) PutAppDatastoreItems(_ context.Context, values []domain.AppDatastoreItem) error {
	if err := validateAppDatastoreValues(values); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range values {
		s.appDatastoreItems[appDatastoreKey(value.AppID, value.WorkspaceID, value.Datastore, value.ID)] = value
	}
	return nil
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

func (s *Store) GetAppDatastoreItems(_ context.Context, appID domain.AppID, workspaceID domain.WorkspaceID, datastore string, ids []string) ([]domain.AppDatastoreItem, error) {
	if appID == "" || workspaceID == "" || strings.TrimSpace(datastore) == "" || len(ids) == 0 || len(ids) > 25 {
		return nil, store.InvalidArgument("invalid app datastore read")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.AppDatastoreItem, 0, len(ids))
	for _, id := range ids {
		if value, exists := s.appDatastoreItems[appDatastoreKey(appID, workspaceID, datastore, id)]; exists {
			values = append(values, value)
		}
	}
	return values, nil
}

func (s *Store) DeleteAppDatastoreItems(_ context.Context, appID domain.AppID, workspaceID domain.WorkspaceID, datastore string, ids []string) error {
	if appID == "" || workspaceID == "" || strings.TrimSpace(datastore) == "" || len(ids) == 0 || len(ids) > 25 {
		return store.InvalidArgument("invalid app datastore delete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.appDatastoreItems, appDatastoreKey(appID, workspaceID, datastore, id))
	}
	return nil
}
