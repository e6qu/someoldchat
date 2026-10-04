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
		Commands:          encodeProtoCodeChannelCommands(value.Commands),
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt),
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
		Commands:      decodeProtoCodeChannelCommands(value.GetCommands()),
		CreatedAt:     optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
	}
}

func (r Remote) SetCodeChannelView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, request domain.CodeChannelViewRequest) (domain.CodeChannelView, error) {
	out, err := r.codeChannels.SetCodeChannelView(ctx, encodeProtoCodeChannelViewRequest(workspaceID, actor, app, conversation, request))
	if err != nil {
		return domain.CodeChannelView{}, err
	}
	return decodeProtoCodeChannelView(out), nil
}

func (r Remote) CodeChannelViews(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID) ([]domain.CodeChannelView, error) {
	out, err := r.codeChannels.ListCodeChannelViews(ctx, &chatv1.CodeChannelRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Conversation: string(conversation)})
	if err != nil {
		return nil, err
	}
	views := make([]domain.CodeChannelView, 0, len(out.GetViews()))
	for _, view := range out.GetViews() {
		views = append(views, decodeProtoCodeChannelView(view))
	}
	return views, nil
}

func (r Remote) RemoveCodeChannelView(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, id domain.CodeChannelViewID, key string) (domain.CodeChannelViewID, error) {
	out, err := r.codeChannels.RemoveCodeChannelView(ctx, &chatv1.RemoveCodeChannelViewRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app), Conversation: string(conversation), ViewId: string(id), ViewKey: key,
	})
	if err != nil {
		return "", err
	}
	return domain.CodeChannelViewID(out.GetViewId()), nil
}

func (s *Server) SetCodeChannelView(ctx context.Context, input *chatv1.SetCodeChannelViewRequest) (*chatv1.CodeChannelView, error) {
	workspaceID, actor, app, conversation, request := decodeProtoCodeChannelViewRequest(input)
	view, err := s.implementation.SetCodeChannelView(ctx, workspaceID, actor, app, conversation, request)
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoCodeChannelView(view), nil
}

func (s *Server) ListCodeChannelViews(ctx context.Context, input *chatv1.CodeChannelRequest) (*chatv1.CodeChannelViewsResponse, error) {
	views, err := s.implementation.CodeChannelViews(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversation()))
	if err != nil {
		return nil, mapError(err)
	}
	response := &chatv1.CodeChannelViewsResponse{Views: make([]*chatv1.CodeChannelView, 0, len(views))}
	for _, view := range views {
		response.Views = append(response.Views, encodeProtoCodeChannelView(view))
	}
	return response, nil
}

func (s *Server) RemoveCodeChannelView(ctx context.Context, input *chatv1.RemoveCodeChannelViewRequest) (*chatv1.RemoveCodeChannelViewResponse, error) {
	id, err := s.implementation.RemoveCodeChannelView(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.ConversationID(input.GetConversation()), domain.CodeChannelViewID(input.GetViewId()), input.GetViewKey())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.RemoveCodeChannelViewResponse{ViewId: string(id)}, nil
}

func encodeProtoCodeChannelViewRequest(workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, request domain.CodeChannelViewRequest) *chatv1.SetCodeChannelViewRequest {
	return &chatv1.SetCodeChannelViewRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app), Conversation: string(conversation),
		Type: string(request.Type), Key: request.Key, Name: request.Name, Content: request.Content, Blocks: request.Blocks,
		CanvasId: string(request.CanvasID), AccessLevel: string(request.AccessLevel), AgentContentHash: request.AgentContentHash,
		PrUrl: request.PRURL, BaseBranch: request.BaseBranch, HeadBranch: request.HeadBranch,
		CspConnectDomains: request.CSP.ConnectDomains, CspResourceDomains: request.CSP.ResourceDomains,
	}
}

func decodeProtoCodeChannelViewRequest(input *chatv1.SetCodeChannelViewRequest) (domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.CodeChannelViewRequest) {
	return domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()), domain.ConversationID(input.GetConversation()), domain.CodeChannelViewRequest{
		Type: domain.CodeChannelViewType(input.GetType()), Key: input.GetKey(), Name: input.GetName(), Content: input.GetContent(), Blocks: input.GetBlocks(),
		CanvasID: domain.CanvasID(input.GetCanvasId()), AccessLevel: domain.CodeChannelCanvasAccess(input.GetAccessLevel()), AgentContentHash: input.GetAgentContentHash(),
		PRURL: input.GetPrUrl(), BaseBranch: input.GetBaseBranch(), HeadBranch: input.GetHeadBranch(),
		CSP: domain.CodeChannelViewCSP{ConnectDomains: nonEmptyStrings(input.GetCspConnectDomains()), ResourceDomains: nonEmptyStrings(input.GetCspResourceDomains())},
	}
}

func encodeProtoCodeChannelView(view domain.CodeChannelView) *chatv1.CodeChannelView {
	return &chatv1.CodeChannelView{
		WorkspaceId: string(view.WorkspaceID), Conversation: string(view.Conversation), Id: string(view.ID), FileId: string(view.FileID),
		Key: view.Key, Type: string(view.Type), Label: view.Label, AppId: string(view.AppID), BotUserId: string(view.BotUserID),
		Content: view.Content, Blocks: view.Blocks, CanvasId: string(view.CanvasID), AccessLevel: string(view.AccessLevel),
		AgentContentHash: view.AgentContentHash, PrUrl: view.PRURL, BaseBranch: view.BaseBranch, HeadBranch: view.HeadBranch,
		CspConnectDomains: view.CSP.ConnectDomains, CspResourceDomains: view.CSP.ResourceDomains, Version: view.Version,
		CreatedAtUnixNano: optionalUnixNano(view.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(view.UpdatedAt),
	}
}

func decodeProtoCodeChannelView(view *chatv1.CodeChannelView) domain.CodeChannelView {
	return domain.CodeChannelView{
		WorkspaceID: domain.WorkspaceID(view.GetWorkspaceId()), Conversation: domain.ConversationID(view.GetConversation()),
		ID: domain.CodeChannelViewID(view.GetId()), FileID: domain.FileID(view.GetFileId()), Key: view.GetKey(),
		Type: domain.CodeChannelViewType(view.GetType()), Label: view.GetLabel(), AppID: domain.AppID(view.GetAppId()), BotUserID: domain.UserID(view.GetBotUserId()),
		Content: view.GetContent(), Blocks: view.GetBlocks(), CanvasID: domain.CanvasID(view.GetCanvasId()),
		AccessLevel: domain.CodeChannelCanvasAccess(view.GetAccessLevel()), AgentContentHash: view.GetAgentContentHash(),
		PRURL: view.GetPrUrl(), BaseBranch: view.GetBaseBranch(), HeadBranch: view.GetHeadBranch(),
		CSP:     domain.CodeChannelViewCSP{ConnectDomains: nonEmptyStrings(view.GetCspConnectDomains()), ResourceDomains: nonEmptyStrings(view.GetCspResourceDomains())},
		Version: view.GetVersion(), CreatedAt: optionalTimeFromUnixNano(view.GetCreatedAtUnixNano()), UpdatedAt: optionalTimeFromUnixNano(view.GetUpdatedAtUnixNano()),
	}
}

// nonEmptyStrings is a repeated field as a domain list: nil when empty, as the
// domain keeps an absent CSP list.
func nonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func encodeProtoCodeChannelCommands(commands []domain.CodeChannelCommand) []*chatv1.CodeChannelCommand {
	encoded := make([]*chatv1.CodeChannelCommand, 0, len(commands))
	for _, command := range commands {
		encoded = append(encoded, &chatv1.CodeChannelCommand{
			Name: command.Name, Description: command.Description, ArgumentHint: command.ArgumentHint, ShouldEscape: command.ShouldEscape,
			AppId: string(command.AppID), BotUserId: string(command.BotUserID),
		})
	}
	return encoded
}

func decodeProtoCodeChannelCommands(commands []*chatv1.CodeChannelCommand) []domain.CodeChannelCommand {
	decoded := make([]domain.CodeChannelCommand, 0, len(commands))
	for _, command := range commands {
		decoded = append(decoded, domain.CodeChannelCommand{
			Name: command.GetName(), Description: command.GetDescription(), ArgumentHint: command.GetArgumentHint(), ShouldEscape: command.GetShouldEscape(),
			AppID: domain.AppID(command.GetAppId()), BotUserID: domain.UserID(command.GetBotUserId()),
		})
	}
	return decoded
}

func (r Remote) SetCodeChannelCommands(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, commands []domain.CodeChannelCommand) (int, error) {
	out, err := r.codeChannels.SetCodeChannelCommands(ctx, &chatv1.SetCodeChannelCommandsRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app), Conversation: string(conversation), Commands: encodeProtoCodeChannelCommands(commands),
	})
	if err != nil {
		return 0, err
	}
	return int(out.GetCommandCount()), nil
}

func (r Remote) CodeChannelCanvas(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, id domain.CanvasID) (domain.Canvas, domain.CanvasCommentPage, error) {
	out, err := r.codeChannels.GetCodeChannelCanvas(ctx, &chatv1.CodeChannelCanvasRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app), Conversation: string(conversation), CanvasId: string(id),
	})
	if err != nil {
		return domain.Canvas{}, domain.CanvasCommentPage{}, err
	}
	canvas, err := decodeProtoCanvas(out.GetCanvas())
	if err != nil {
		return domain.Canvas{}, domain.CanvasCommentPage{}, err
	}
	comments, err := decodeProtoCanvasCommentPage(out.GetComments())
	if err != nil {
		return domain.Canvas{}, domain.CanvasCommentPage{}, err
	}
	return canvas, comments, nil
}

func (r Remote) SetCodeChannelCanvasContent(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, id domain.CanvasID, content string) (int, error) {
	out, err := r.codeChannels.SetCodeChannelCanvasContent(ctx, &chatv1.SetCodeChannelCanvasContentRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app), Conversation: string(conversation), CanvasId: string(id), Content: content,
	})
	if err != nil {
		return 0, err
	}
	return int(out.GetSectionsChangedCount()), nil
}

func (s *Server) SetCodeChannelCommands(ctx context.Context, input *chatv1.SetCodeChannelCommandsRequest) (*chatv1.SetCodeChannelCommandsResponse, error) {
	count, err := s.implementation.SetCodeChannelCommands(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.ConversationID(input.GetConversation()), decodeProtoCodeChannelCommands(input.GetCommands()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SetCodeChannelCommandsResponse{CommandCount: int32(count)}, nil
}

func (s *Server) GetCodeChannelCanvas(ctx context.Context, input *chatv1.CodeChannelCanvasRequest) (*chatv1.CodeChannelCanvasResponse, error) {
	canvas, comments, err := s.implementation.CodeChannelCanvas(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.ConversationID(input.GetConversation()), domain.CanvasID(input.GetCanvasId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.CodeChannelCanvasResponse{Canvas: encodeProtoCanvas(canvas), Comments: encodeProtoCanvasCommentPage(comments)}, nil
}

func (s *Server) SetCodeChannelCanvasContent(ctx context.Context, input *chatv1.SetCodeChannelCanvasContentRequest) (*chatv1.SetCodeChannelCanvasContentResponse, error) {
	count, err := s.implementation.SetCodeChannelCanvasContent(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.ConversationID(input.GetConversation()), domain.CanvasID(input.GetCanvasId()), input.GetContent())
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SetCodeChannelCanvasContentResponse{SectionsChangedCount: int32(count)}, nil
}
