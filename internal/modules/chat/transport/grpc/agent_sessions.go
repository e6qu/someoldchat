package grpc

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
)

// The agent session surface of the chat boundary. Every field of every domain
// value crosses: the differential test compares the two compositions on the
// whole session, its agents and the warnings.

func (r Remote) SetAgentSessionStatus(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, thread domain.MessageTimestamp, request domain.AgentSessionStatusRequest) (domain.AgentSessionStatusResult, error) {
	out, err := r.agentSessions.SetAgentSessionStatus(ctx, &chatv1.SetAgentSessionStatusRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app),
		Conversation: string(conversation), ThreadTs: string(thread),
		Status: string(request.Status), Title: request.Title, InitiatorUserId: string(request.InitiatorUserID),
		Username: request.Identity.Username, IconEmoji: request.Identity.IconEmoji, IconUrl: request.Identity.IconURL,
	})
	if err != nil {
		return domain.AgentSessionStatusResult{}, err
	}
	return domain.AgentSessionStatusResult{
		Session:     decodeProtoAgentSession(out.GetSession()),
		AgentStatus: domain.AgentSessionStatus(out.GetAgentStatus()),
		Warnings:    append([]string(nil), out.GetWarnings()...),
	}, nil
}

func (r Remote) RenameAgentSession(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, thread domain.MessageTimestamp, title string) (domain.AgentSession, error) {
	out, err := r.agentSessions.RenameAgentSession(ctx, &chatv1.RenameAgentSessionRequest{
		WorkspaceId: string(workspaceID), UserId: string(actor), AppId: string(app),
		Conversation: string(conversation), ThreadTs: string(thread), Title: title,
	})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return decodeProtoAgentSession(out), nil
}

func (r Remote) ChangeAgentSessionTitle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, title string) (domain.AgentSession, error) {
	out, err := r.agentSessions.ChangeAgentSessionTitle(ctx, &chatv1.RenameAgentSessionRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID),
		Conversation: string(conversation), ThreadTs: string(thread), Title: title,
	})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return decodeProtoAgentSession(out), nil
}

func (r Remote) StopAgentSession(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AgentSession, error) {
	out, err := r.agentSessions.StopAgentSession(ctx, agentSessionRequest(workspaceID, userID, conversation, thread))
	if err != nil {
		return domain.AgentSession{}, err
	}
	return decodeProtoAgentSession(out), nil
}

func (r Remote) AgentSession(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AgentSessionView, error) {
	out, err := r.agentSessions.GetAgentSession(ctx, agentSessionRequest(workspaceID, userID, conversation, thread))
	if err != nil {
		return domain.AgentSessionView{}, err
	}
	return domain.AgentSessionView{Session: decodeProtoAgentSession(out.GetSession()), Stoppable: out.GetStoppable()}, nil
}

func agentSessionRequest(workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) *chatv1.AgentSessionRequest {
	return &chatv1.AgentSessionRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Conversation: string(conversation), ThreadTs: string(thread)}
}

func (s *Server) SetAgentSessionStatus(ctx context.Context, input *chatv1.SetAgentSessionStatusRequest) (*chatv1.SetAgentSessionStatusResponse, error) {
	result, err := s.implementation.SetAgentSessionStatus(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.ConversationID(input.GetConversation()), domain.MessageTimestamp(input.GetThreadTs()), domain.AgentSessionStatusRequest{
			Status: domain.AgentSessionStatus(input.GetStatus()), Title: input.GetTitle(), InitiatorUserID: domain.UserID(input.GetInitiatorUserId()),
			Identity: domain.AgentIdentity{Username: input.GetUsername(), IconEmoji: input.GetIconEmoji(), IconURL: input.GetIconUrl()},
		})
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.SetAgentSessionStatusResponse{
		Session: encodeProtoAgentSession(result.Session), AgentStatus: string(result.AgentStatus),
		Warnings: append([]string(nil), result.Warnings...),
	}, nil
}

func (s *Server) RenameAgentSession(ctx context.Context, input *chatv1.RenameAgentSessionRequest) (*chatv1.AgentSession, error) {
	session, err := s.implementation.RenameAgentSession(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.AppID(input.GetAppId()),
		domain.ConversationID(input.GetConversation()), domain.MessageTimestamp(input.GetThreadTs()), input.GetTitle())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAgentSession(session), nil
}

func (s *Server) ChangeAgentSessionTitle(ctx context.Context, input *chatv1.RenameAgentSessionRequest) (*chatv1.AgentSession, error) {
	session, err := s.implementation.ChangeAgentSessionTitle(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversation()), domain.MessageTimestamp(input.GetThreadTs()), input.GetTitle())
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAgentSession(session), nil
}

func (s *Server) StopAgentSession(ctx context.Context, input *chatv1.AgentSessionRequest) (*chatv1.AgentSession, error) {
	session, err := s.implementation.StopAgentSession(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversation()), domain.MessageTimestamp(input.GetThreadTs()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoAgentSession(session), nil
}

func (s *Server) GetAgentSession(ctx context.Context, input *chatv1.AgentSessionRequest) (*chatv1.AgentSessionView, error) {
	view, err := s.implementation.AgentSession(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()),
		domain.ConversationID(input.GetConversation()), domain.MessageTimestamp(input.GetThreadTs()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.AgentSessionView{Session: encodeProtoAgentSession(view.Session), Stoppable: view.Stoppable}, nil
}

func encodeProtoAgentSession(value domain.AgentSession) *chatv1.AgentSession {
	agents := make([]*chatv1.AgentSessionAgent, 0, len(value.Agents))
	for _, agent := range value.Agents {
		agents = append(agents, &chatv1.AgentSessionAgent{
			AppId: string(agent.AppID), Status: string(agent.Status),
			Username: agent.Identity.Username, IconEmoji: agent.Identity.IconEmoji, IconUrl: agent.Identity.IconURL,
			UpdatedAtUnixNano: optionalUnixNano(agent.UpdatedAt),
		})
	}
	return &chatv1.AgentSession{
		WorkspaceId: string(value.WorkspaceID), Conversation: string(value.Conversation), ThreadTs: string(value.ThreadTimestamp),
		Title: value.Title, InitiatorUserId: string(value.InitiatorUserID),
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt),
		Agents: agents,
	}
}

func decodeProtoAgentSession(value *chatv1.AgentSession) domain.AgentSession {
	session := domain.AgentSession{
		WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), Conversation: domain.ConversationID(value.GetConversation()),
		ThreadTimestamp: domain.MessageTimestamp(value.GetThreadTs()), Title: value.GetTitle(),
		InitiatorUserID: domain.UserID(value.GetInitiatorUserId()),
		CreatedAt:       optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
	}
	for _, agent := range value.GetAgents() {
		session.Agents = append(session.Agents, domain.AgentSessionAgent{
			AppID: domain.AppID(agent.GetAppId()), Status: domain.AgentSessionStatus(agent.GetStatus()),
			Identity:  domain.AgentIdentity{Username: agent.GetUsername(), IconEmoji: agent.GetIconEmoji(), IconURL: agent.GetIconUrl()},
			UpdatedAt: optionalTimeFromUnixNano(agent.GetUpdatedAtUnixNano()),
		})
	}
	return session
}
