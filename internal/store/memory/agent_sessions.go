package memory

import (
	"context"
	"sort"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The agent session repository mirrors the SQL profile: one session row per
// thread, one agent row per app, and every mutation's events appended under
// the same lock as its rows.

func agentSessionKey(workspace domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp) string {
	return string(workspace) + "\x00" + string(conversation) + "\x00" + string(thread)
}

func cloneAgentSession(value domain.AgentSession) domain.AgentSession {
	value.Agents = append([]domain.AgentSessionAgent(nil), value.Agents...)
	return value
}

func (s *Store) GetAgentSession(_ context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AgentSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.agentSessions[agentSessionKey(workspace, conversation, thread)]
	if !ok {
		return domain.AgentSession{}, store.ErrNotFound
	}
	return cloneAgentSession(value), nil
}

func (s *Store) SetAgentSessionStatus(_ context.Context, write domain.AgentSessionStatusWrite, event events.Event) (domain.AgentSession, error) {
	if !write.Valid() {
		return domain.AgentSession{}, store.InvalidArgument("an agent session status write requires a workspace, conversation, thread, app, valid status and time")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := agentSessionKey(write.WorkspaceID, write.Conversation, write.ThreadTimestamp)
	session, exists := s.agentSessions[key]
	if !exists {
		session = domain.AgentSession{
			WorkspaceID: write.WorkspaceID, Conversation: write.Conversation, ThreadTimestamp: write.ThreadTimestamp,
			Title: write.Title, InitiatorUserID: write.InitiatorUserID, CreatedAt: write.At.UTC(),
		}
	}
	session.Agents = append([]domain.AgentSessionAgent(nil), session.Agents...)
	index := -1
	for position, agent := range session.Agents {
		if agent.AppID == write.AppID {
			index = position
		}
	}
	if index < 0 {
		session.Agents = append(session.Agents, domain.AgentSessionAgent{AppID: write.AppID})
		index = len(session.Agents) - 1
	}
	session.Agents[index].Status = write.Status
	if write.ReplaceIdentity {
		session.Agents[index].Identity = write.Identity
	}
	session.Agents[index].UpdatedAt = write.At.UTC()
	domain.SortAgentSessionAgents(session.Agents)
	session.UpdatedAt = write.At.UTC()
	s.agentSessions[key] = session
	s.outbox = append(s.outbox, event)
	return cloneAgentSession(session), nil
}

func (s *Store) RenameAgentSession(_ context.Context, rename domain.AgentSessionRename, produced []events.Event) (domain.AgentSession, error) {
	if !rename.Valid() {
		return domain.AgentSession{}, store.InvalidArgument("an agent session rename requires a workspace, conversation, thread, title and time")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := agentSessionKey(rename.WorkspaceID, rename.Conversation, rename.ThreadTimestamp)
	session, exists := s.agentSessions[key]
	if !exists {
		return domain.AgentSession{}, store.ErrNotFound
	}
	if session.Title != rename.ExpectedTitle {
		return domain.AgentSession{}, store.ErrConflict
	}
	session.Title = rename.Title
	session.UpdatedAt = rename.At.UTC()
	s.agentSessions[key] = session
	s.outbox = append(s.outbox, produced...)
	return cloneAgentSession(session), nil
}

func (s *Store) StopAgentSession(_ context.Context, stop domain.AgentSessionStop, produced []events.Event) error {
	if !stop.Valid() {
		return store.InvalidArgument("an agent session stop requires a workspace, conversation, thread and at least one agent")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, exists := s.agentSessions[agentSessionKey(stop.WorkspaceID, stop.Conversation, stop.ThreadTimestamp)]
	if !exists {
		return store.ErrNotFound
	}
	for _, app := range stop.Agents {
		agent, ok := session.Agent(app)
		if !ok || agent.Status != domain.AgentSessionProcessing {
			return store.ErrConflict
		}
	}
	// Every precondition is checked before anything is written, so a conflict
	// leaves no stream half-stopped.
	values := s.messages[stop.Conversation]
	positions := make([]int, len(stop.Streams))
	for streamIndex, stream := range stop.Streams {
		positions[streamIndex] = -1
		for index := range values {
			if values[index].ID == stream.Message.ID {
				positions[streamIndex] = index
			}
		}
		if positions[streamIndex] < 0 || values[positions[streamIndex]].StreamState != stream.PreviousStreamState {
			return store.ErrConflict
		}
	}
	for streamIndex, stream := range stop.Streams {
		values[positions[streamIndex]].StreamState = stream.Message.StreamState
	}
	s.messages[stop.Conversation] = values
	s.outbox = append(s.outbox, produced...)
	return nil
}

func (s *Store) ListActiveMessageStreams(_ context.Context, conversation domain.ConversationID, thread domain.MessageTimestamp, app domain.AppID) ([]domain.MessageTimestamp, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found []domain.MessageTimestamp
	for _, message := range s.messages[conversation] {
		if message.Deleted || message.AppID != app || message.ThreadTimestamp != thread || !strings.HasPrefix(message.StreamState, domain.ActiveMessageStreamPrefix) {
			continue
		}
		found = append(found, domain.NewMessageTimestamp(message.CreatedAt))
	}
	sort.Slice(found, func(left, right int) bool { return found[left] < found[right] })
	return found, nil
}

// deleteConversationAgentSessionsLocked removes every session a deleted
// conversation owned, as the SQL profile's conversation deletion does.
func (s *Store) deleteConversationAgentSessionsLocked(conversation domain.ConversationID) {
	for key, session := range s.agentSessions {
		if session.Conversation == conversation {
			delete(s.agentSessions, key)
		}
	}
}
