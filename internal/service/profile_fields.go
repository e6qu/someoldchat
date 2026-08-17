package service

import (
	"context"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// SetWorkspaceProfileField declares or replaces a custom profile field. Only a
// workspace administrator may define the shape of everyone's profile, so this is
// admin-gated like the other workspace-configuration operations. A definition
// with no id is created with a fresh one; a definition naming an existing id
// edits it.
func (m Messages) SetWorkspaceProfileField(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, definition domain.ProfileFieldDefinition) (domain.ProfileFieldDefinition, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.ProfileFieldDefinition{}, err
	}
	definition.WorkspaceID = workspaceID
	definition.Label = strings.TrimSpace(definition.Label)
	definition.Hint = strings.TrimSpace(definition.Hint)
	if !definition.Valid() {
		return domain.ProfileFieldDefinition{}, ErrInvalidProfileField
	}
	if strings.TrimSpace(string(definition.ID)) == "" {
		id, err := domain.NewProfileFieldID()
		if err != nil {
			return domain.ProfileFieldDefinition{}, err
		}
		definition.ID = id
		definition.CreatedAt = time.Now().UTC()
	} else if existing, err := m.Store.GetWorkspaceProfileField(ctx, workspaceID, definition.ID); err == nil {
		// Editing keeps the field's original creation time; the setter carries a
		// zero time on an edit and would otherwise overwrite it.
		definition.CreatedAt = existing.CreatedAt
	} else if definition.CreatedAt.IsZero() {
		definition.CreatedAt = time.Now().UTC()
	}
	if err := m.Store.SetWorkspaceProfileField(ctx, definition); err != nil {
		return domain.ProfileFieldDefinition{}, err
	}
	return definition, nil
}

// WorkspaceProfileFields lists a workspace's custom profile field definitions.
// Any member may read them: they are the labels a client needs to render the
// values users.profile.get returns, which is why team.profile.get is a
// profile-read rather than an administrative operation.
func (m Messages) WorkspaceProfileFields(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) ([]domain.ProfileFieldDefinition, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	return m.Store.ListWorkspaceProfileFields(ctx, workspaceID)
}

// DeleteWorkspaceProfileField removes a custom profile field and every member's
// value for it. Admin-gated, like defining one.
func (m Messages) DeleteWorkspaceProfileField(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.ProfileFieldID) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return err
	}
	return m.Store.DeleteWorkspaceProfileField(ctx, workspaceID, id)
}

// SetUserProfileFields sets a member's own custom profile field values. A member
// edits only their own profile fields here; Slack's admin editing of another
// member's profile is a separate, paid-plan surface this does not claim. Each
// value is validated against its field's declared type, so a date field never
// holds prose and an options_list never holds a value it does not offer, and an
// empty value clears the field.
func (m Messages) SetUserProfileFields(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID, values []domain.UserProfileFieldValue) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return err
	}
	if targetID == "" {
		targetID = actorID
	}
	if targetID != actorID {
		return ErrInvalidProfile
	}
	if len(values) == 0 {
		return nil
	}
	for _, value := range values {
		definition, err := m.Store.GetWorkspaceProfileField(ctx, workspaceID, value.FieldID)
		if err != nil {
			// A value for a field nobody defined is invisible to every reader and
			// would be orphaned the moment a field minted the same id, so it is
			// refused rather than stored.
			return ErrInvalidProfile
		}
		if !definition.Accepts(value.Value) {
			return ErrInvalidProfile
		}
	}
	return m.Store.SetUserProfileFieldValues(ctx, workspaceID, targetID, values)
}

// UserProfileFields reads a member's custom profile field values. A field marked
// hidden is visible only to the member it belongs to and to administrators, the
// way Slack keeps a sensitive field from the rest of the workspace; a reader who
// is neither sees it filtered out rather than blanked, so its very existence
// stays private.
func (m Messages) UserProfileFields(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, targetID domain.UserID) ([]domain.UserProfileFieldValue, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if targetID == "" {
		targetID = actorID
	}
	if _, err := m.Store.GetWorkspaceMembership(ctx, workspaceID, targetID); err != nil {
		return nil, store.ErrNotFound
	}
	values, err := m.Store.ListUserProfileFieldValues(ctx, workspaceID, targetID)
	if err != nil {
		return nil, err
	}
	if targetID == actorID || m.requireWorkspaceAdmin(ctx, workspaceID, actorID) == nil {
		return values, nil
	}
	definitions, err := m.Store.ListWorkspaceProfileFields(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	hidden := make(map[domain.ProfileFieldID]struct{})
	for _, definition := range definitions {
		if definition.IsHidden {
			hidden[definition.ID] = struct{}{}
		}
	}
	visible := make([]domain.UserProfileFieldValue, 0, len(values))
	for _, value := range values {
		if _, isHidden := hidden[value.FieldID]; isHidden {
			continue
		}
		visible = append(visible, value)
	}
	return visible, nil
}
