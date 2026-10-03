package grpc

import (
	"context"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
)

func (r Remote) AdminBulkSetConversationProperties(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, ids []domain.ConversationID, property domain.ConversationProperty) error {
	out, err := r.mutations.AdminBulkSetConversationProperties(ctx, &chatv1.ConversationPropertiesRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ConversationIds: conversationStrings(ids),
		ExcludeFromSlackAi: property.ExcludeFromSlackAI,
	})
	if err != nil {
		return err
	}
	if !out.GetOk() {
		return errors.New("typed channel property change was not acknowledged")
	}
	return nil
}

func (s *Server) AdminBulkSetConversationProperties(ctx context.Context, input *chatv1.ConversationPropertiesRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AdminBulkSetConversationProperties(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		conversationIDs(input.GetConversationIds()), domain.ConversationProperty{ExcludeFromSlackAI: input.GetExcludeFromSlackAi()}); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}
