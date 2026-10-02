package api

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// AgentSessions is the agent session surface of the chat boundary: the two
// agents.sessions.* methods an app calls, and the read, stop and retitle a
// member's client uses. Service embeds it.
type AgentSessions interface {
	// SetAgentSessionStatus is agents.sessions.setStatus for the calling
	// app's bot user and app.
	SetAgentSessionStatus(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.MessageTimestamp, domain.AgentSessionStatusRequest) (domain.AgentSessionStatusResult, error)
	// RenameAgentSession is agents.sessions.rename for the calling app.
	RenameAgentSession(context.Context, domain.WorkspaceID, domain.UserID, domain.AppID, domain.ConversationID, domain.MessageTimestamp, string) (domain.AgentSession, error)
	// ChangeAgentSessionTitle is a member retitling a session, which every
	// agent of the session hears as agent_session_title_changed.
	ChangeAgentSessionTitle(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, string) (domain.AgentSession, error)
	// StopAgentSession is a member pressing stop on a processing session,
	// which each stopped agent hears as agent_session_stopped.
	StopAgentSession(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (domain.AgentSession, error)
	// AgentSession reads a thread's session as a member's client shows it.
	AgentSession(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp) (domain.AgentSessionView, error)
}
