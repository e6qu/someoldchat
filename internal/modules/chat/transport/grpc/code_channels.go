package grpc

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
)

// The code channel surface of the chat boundary. Every field of every domain
// value crosses: the differential test compares the two compositions on the
// whole record, and the round-trip property on each converter.

func (r Remote) CreateCodeChannel(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, request domain.CodeChannelRequest) (domain.CodeChannel, error) {
	out, err := r.codeChannels.CreateCodeChannel(ctx, encodeProtoCodeChannelRequest(workspaceID, actor, app, request))
	if err != nil {
		return domain.CodeChannel{}, err
	}
	return decodeProtoCodeChannel(out), nil
}

func (r Remote) ArchiveCodeChannel(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, summary domain.MessageTimestamp) error {
	_, err := r.codeChannels.ArchiveCodeChannel(ctx, &chatv1.ArchiveCodeChannelRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app), Conversation: string(conversation), SummaryMessageTs: string(summary),
	})
	return err
}

func (r Remote) SetCodeChannelProperties(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, properties domain.CodeChannelProperties) error {
	_, err := r.codeChannels.SetCodeChannelProperties(ctx, encodeProtoCodeChannelProperties(workspaceID, actor, app, conversation, properties))
	return err
}

func (r Remote) CodeChannel(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID) (domain.CodeChannel, error) {
	out, err := r.codeChannels.GetCodeChannel(ctx, &chatv1.CodeChannelRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Conversation: string(conversation)})
	if err != nil {
		return domain.CodeChannel{}, err
	}
	return decodeProtoCodeChannel(out), nil
}

func (s *Server) CreateCodeChannel(ctx context.Context, input *chatv1.CreateCodeChannelRequest) (*chatv1.CodeChannel, error) {
	workspaceID, actor, app, request := decodeProtoCodeChannelRequest(input)
	value, err := s.implementation.CreateCodeChannel(ctx, workspaceID, actor, app, request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCodeChannel(value), nil
}

func (s *Server) ArchiveCodeChannel(ctx context.Context, input *chatv1.ArchiveCodeChannelRequest) (*chatv1.CodeChannelMutationResponse, error) {
	if err := s.implementation.ArchiveCodeChannel(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.ConversationID(input.GetConversation()), domain.MessageTimestamp(input.GetSummaryMessageTs())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.CodeChannelMutationResponse{Ok: true}, nil
}

func (s *Server) SetCodeChannelProperties(ctx context.Context, input *chatv1.SetCodeChannelPropertiesRequest) (*chatv1.CodeChannelMutationResponse, error) {
	workspaceID, actor, app, conversation, properties := decodeProtoCodeChannelProperties(input)
	if err := s.implementation.SetCodeChannelProperties(ctx, workspaceID, actor, app, conversation, properties); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.CodeChannelMutationResponse{Ok: true}, nil
}

func (s *Server) GetCodeChannel(ctx context.Context, input *chatv1.CodeChannelRequest) (*chatv1.CodeChannel, error) {
	value, err := s.implementation.CodeChannel(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversation()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCodeChannel(value), nil
}

func encodeProtoCodeChannelRequest(workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, request domain.CodeChannelRequest) *chatv1.CreateCodeChannelRequest {
	return &chatv1.CreateCodeChannelRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app),
		SessionId: request.SessionID, Name: request.Name, IsPrivate: request.Private,
		OriginChannel: string(request.Origin.Channel), OriginTs: string(request.Origin.Timestamp),
	}
}

func decodeProtoCodeChannelRequest(input *chatv1.CreateCodeChannelRequest) (domain.WorkspaceID, domain.UserID, domain.AppID, domain.CodeChannelRequest) {
	return domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), domain.CodeChannelRequest{
		SessionID: input.GetSessionId(), Name: input.GetName(), Private: input.GetIsPrivate(),
		Origin: domain.CodeChannelOrigin{Channel: domain.ConversationID(input.GetOriginChannel()), Timestamp: domain.MessageTimestamp(input.GetOriginTs())},
	}
}

func encodeProtoCodeChannelProperties(workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, properties domain.CodeChannelProperties) *chatv1.SetCodeChannelPropertiesRequest {
	request := &chatv1.SetCodeChannelPropertiesRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app), Conversation: string(conversation),
		ResourceUrl: properties.AgentResource.URL, ResourceType: properties.AgentResource.ResourceType,
		ResourceTitle: properties.AgentResource.Title, ResourceProvider: properties.AgentResource.Provider,
	}
	if properties.ContextBar != nil {
		request.SetContextBar = true
		request.ContextBar = encodeProtoCodeChannelContextBar(*properties.ContextBar)
	}
	if properties.Summary != nil {
		request.SetSummary = true
		request.SummaryMessageTs = string(properties.Summary.MessageTimestamp)
		request.SummaryThreadTs = string(properties.Summary.ThreadTimestamp)
	}
	return request
}

func decodeProtoCodeChannelProperties(input *chatv1.SetCodeChannelPropertiesRequest) (domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.CodeChannelProperties) {
	properties := domain.CodeChannelProperties{AgentResource: domain.AgentResourcePatch{
		URL: input.ResourceUrl, ResourceType: input.ResourceType, Title: input.ResourceTitle, Provider: input.ResourceProvider,
	}}
	if input.GetSetContextBar() {
		items := decodeProtoCodeChannelContextBar(input.GetContextBar())
		properties.ContextBar = &items
	}
	if input.GetSetSummary() {
		properties.Summary = &domain.CodeChannelSummary{
			MessageTimestamp: domain.MessageTimestamp(input.GetSummaryMessageTs()), ThreadTimestamp: domain.MessageTimestamp(input.GetSummaryThreadTs()),
		}
	}
	return domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), domain.ConversationID(input.GetConversation()), properties
}

func encodeProtoCodeChannelContextBar(items []domain.CodeChannelContextItem) []*chatv1.CodeChannelContextItem {
	encoded := make([]*chatv1.CodeChannelContextItem, 0, len(items))
	for _, item := range items {
		encoded = append(encoded, &chatv1.CodeChannelContextItem{Key: item.Key, Label: item.Label, Icon: item.Icon, Url: item.URL, ItemType: item.ItemType, BotUserId: string(item.BotUserID)})
	}
	return encoded
}

func decodeProtoCodeChannelContextBar(items []*chatv1.CodeChannelContextItem) []domain.CodeChannelContextItem {
	decoded := make([]domain.CodeChannelContextItem, 0, len(items))
	for _, item := range items {
		decoded = append(decoded, domain.CodeChannelContextItem{Key: item.GetKey(), Label: item.GetLabel(), Icon: item.GetIcon(), URL: item.GetUrl(), ItemType: item.GetItemType(), BotUserID: domain.UserID(item.GetBotUserId())})
	}
	return decoded
}

func encodeProtoCodeChannel(value domain.CodeChannel) *chatv1.CodeChannel {
	return &chatv1.CodeChannel{
		WorkspaceId: string(value.WorkspaceID), Conversation: string(value.Conversation), AppId: string(value.AppID), BotUserId: string(value.BotUserID),
		SessionId: value.SessionID, OriginChannel: string(value.Origin.Channel), OriginTs: string(value.Origin.Timestamp),
		ContextBar:       encodeProtoCodeChannelContextBar(value.ContextBar),
		SummaryMessageTs: string(value.Summary.MessageTimestamp), SummaryThreadTs: string(value.Summary.ThreadTimestamp),
		AgentResource: &chatv1.AgentResource{
			Url: value.AgentResource.URL, ResourceType: value.AgentResource.ResourceType, Title: value.AgentResource.Title, Provider: value.AgentResource.Provider,
		},
		CreatedAtUnixNano: unixNanoOrZero(value.CreatedAt), UpdatedAtUnixNano: unixNanoOrZero(value.UpdatedAt),
	}
}

func decodeProtoCodeChannel(value *chatv1.CodeChannel) domain.CodeChannel {
	resource := value.GetAgentResource()
	return domain.CodeChannel{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Conversation: domain.ConversationID(value.GetConversation()),
		AppID: domain.AppID(value.GetAppId()), BotUserID: domain.UserID(value.GetBotUserId()), SessionID: value.GetSessionId(),
		Origin:     domain.CodeChannelOrigin{Channel: domain.ConversationID(value.GetOriginChannel()), Timestamp: domain.MessageTimestamp(value.GetOriginTs())},
		ContextBar: decodeProtoCodeChannelContextBar(value.GetContextBar()),
		Summary: domain.CodeChannelSummary{
			MessageTimestamp: domain.MessageTimestamp(value.GetSummaryMessageTs()), ThreadTimestamp: domain.MessageTimestamp(value.GetSummaryThreadTs()),
		},
		AgentResource: domain.AgentResource{URL: resource.GetUrl(), ResourceType: resource.GetResourceType(), Title: resource.GetTitle(), Provider: resource.GetProvider()},
		CreatedAt:     optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
	}
}
