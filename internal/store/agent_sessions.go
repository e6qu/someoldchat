package store

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// AgentSessionStore is the persistence of agents.sessions.* and the member's
// stop and retitle controls. Every mutation commits its events in the same
// transaction as its rows.
type AgentSessionStore interface {
	// GetAgentSession reads one session with all its agents, or ErrNotFound.
	GetAgentSession(context.Context, domain.WorkspaceID, domain.ConversationID, domain.MessageTimestamp) (domain.AgentSession, error)
	// SetAgentSessionStatus creates the session when it does not exist, writes
	// the app's agent row, and answers the session as it is after the write.
	SetAgentSessionStatus(context.Context, domain.AgentSessionStatusWrite, events.Event) (domain.AgentSession, error)
	// RenameAgentSession retitles a session whose title is still the
	// rename's ExpectedTitle, answering ErrConflict when it is not and
	// ErrNotFound when the session does not exist.
	RenameAgentSession(context.Context, domain.AgentSessionRename, []events.Event) (domain.AgentSession, error)
	// StopAgentSession ends the named streams while every named agent is
	// still processing and every stream still holds its previous state,
	// answering ErrConflict otherwise. Agent statuses are not changed.
	StopAgentSession(context.Context, domain.AgentSessionStop, []events.Event) error
	// ListActiveMessageStreams answers the timestamps of an app's messages in
	// a thread whose stream is still in progress, oldest first.
	ListActiveMessageStreams(context.Context, domain.ConversationID, domain.MessageTimestamp, domain.AppID) ([]domain.MessageTimestamp, error)
}
