package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func (m Messages) PresentEntityDetails(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, metadata string, userAuthRequired bool, userAuthURL, errorPayload string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if strings.TrimSpace(triggerID) == "" || !validEntityObject(metadata) || !validEntityError(errorPayload) {
		return ErrInvalidEntity
	}
	if userAuthRequired && strings.TrimSpace(userAuthURL) == "" {
		return ErrInvalidEntity
	}
	return nil
}

func (m Messages) PresentEntityComments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, comments, cursor string, canPostComment bool, deleteActionID string, userAuthRequired bool, userAuthURL, errorPayload string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if strings.TrimSpace(triggerID) == "" || strings.TrimSpace(comments) == "" || !validEntityArray(comments) || !validEntityError(errorPayload) {
		return ErrInvalidEntity
	}
	if userAuthRequired && strings.TrimSpace(userAuthURL) == "" {
		return ErrInvalidEntity
	}
	if strings.TrimSpace(deleteActionID) == "" && entityCommentsAllowDelete(comments) {
		return ErrInvalidEntity
	}
	return nil
}

func (m Messages) AcknowledgeEntityCommentAction(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggerID, comment, errorPayload string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if strings.TrimSpace(triggerID) == "" || !validEntityObjectOrEmpty(comment) || !validEntityError(errorPayload) {
		return ErrInvalidEntity
	}
	return nil
}

func validEntityArray(value string) bool {
	var decoded []json.RawMessage
	return json.Unmarshal([]byte(defaultJSON(value, "[]")), &decoded) == nil && decoded != nil
}

func validEntityObject(value string) bool {
	var decoded map[string]json.RawMessage
	return json.Unmarshal([]byte(defaultJSON(value, "{}")), &decoded) == nil && decoded != nil
}

func validEntityObjectOrEmpty(value string) bool {
	return strings.TrimSpace(value) == "" || validEntityObject(value)
}

func validEntityError(value string) bool {
	return validEntityObjectOrEmpty(value)
}

func entityCommentsAllowDelete(value string) bool {
	var comments []map[string]json.RawMessage
	if json.Unmarshal([]byte(defaultJSON(value, "[]")), &comments) != nil {
		return false
	}
	for _, comment := range comments {
		var canDelete bool
		if json.Unmarshal(comment["can_delete"], &canDelete) == nil && canDelete {
			return true
		}
	}
	return false
}

func defaultJSON(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
