package grpc

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
)

// Slackbot's custom responses and their dispatch across the chat boundary.

func (r Remote) SlackbotResponses(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.SlackbotResponse, error) {
	out, err := r.slackbot.ListSlackbotResponses(ctx, &chatv1.SlackbotResponsesRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return nil, err
	}
	values := make([]domain.SlackbotResponse, 0, len(out.GetResponses()))
	for _, value := range out.GetResponses() {
		values = append(values, decodeProtoSlackbotResponse(value))
	}
	return values, nil
}

func (r Remote) AddSlackbotResponse(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, triggers, replies []string) (domain.SlackbotResponse, error) {
	out, err := r.slackbot.AddSlackbotResponse(ctx, &chatv1.AddSlackbotResponseRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Triggers: triggers, Replies: replies,
	})
	if err != nil {
		return domain.SlackbotResponse{}, err
	}
	return decodeProtoSlackbotResponse(out), nil
}

func (r Remote) DeleteSlackbotResponse(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SlackbotResponseID) error {
	_, err := r.slackbot.DeleteSlackbotResponse(ctx, &chatv1.DeleteSlackbotResponseRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Id: string(id)})
	return err
}

func (r Remote) DispatchSlackbotResponses(ctx context.Context, workspaceID domain.WorkspaceID, limit int) (int, error) {
	out, err := r.slackbot.DispatchSlackbotResponses(ctx, &chatv1.DispatchSlackbotResponsesRequest{WorkspaceId: string(workspaceID), Limit: int32(limit)})
	if err != nil {
		return 0, err
	}
	return int(out.GetAnswered()), nil
}

func (s *Server) ListSlackbotResponses(ctx context.Context, input *chatv1.SlackbotResponsesRequest) (*chatv1.SlackbotResponsesResponse, error) {
	values, err := s.implementation.SlackbotResponses(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	response := &chatv1.SlackbotResponsesResponse{Responses: make([]*chatv1.SlackbotResponse, 0, len(values))}
	for _, value := range values {
		response.Responses = append(response.Responses, encodeProtoSlackbotResponse(value))
	}
	return response, nil
}

func (s *Server) AddSlackbotResponse(ctx context.Context, input *chatv1.AddSlackbotResponseRequest) (*chatv1.SlackbotResponse, error) {
	value, err := s.implementation.AddSlackbotResponse(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), input.GetTriggers(), input.GetReplies())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSlackbotResponse(value), nil
}

func (s *Server) DeleteSlackbotResponse(ctx context.Context, input *chatv1.DeleteSlackbotResponseRequest) (*chatv1.DeleteSlackbotResponseResponse, error) {
	if err := s.implementation.DeleteSlackbotResponse(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SlackbotResponseID(input.GetId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.DeleteSlackbotResponseResponse{Ok: true}, nil
}

func (s *Server) DispatchSlackbotResponses(ctx context.Context, input *chatv1.DispatchSlackbotResponsesRequest) (*chatv1.DispatchSlackbotResponsesResponse, error) {
	answered, err := s.implementation.DispatchSlackbotResponses(ctx, domain.WorkspaceID(input.GetWorkspaceId()), int(input.GetLimit()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.DispatchSlackbotResponsesResponse{Answered: int32(answered)}, nil
}

func encodeProtoSlackbotResponse(value domain.SlackbotResponse) *chatv1.SlackbotResponse {
	return &chatv1.SlackbotResponse{
		WorkspaceId: string(value.WorkspaceID), Id: string(value.ID), Triggers: value.Triggers, Replies: value.Replies,
		CreatedBy: string(value.CreatedBy), CreatedAtUnixNano: unixNanoOrZero(value.CreatedAt),
	}
}

func decodeProtoSlackbotResponse(value *chatv1.SlackbotResponse) domain.SlackbotResponse {
	return domain.SlackbotResponse{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), ID: domain.SlackbotResponseID(value.GetId()),
		Triggers: nonEmptyStrings(value.GetTriggers()), Replies: nonEmptyStrings(value.GetReplies()),
		CreatedBy: domain.UserID(value.GetCreatedBy()), CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()),
	}
}
