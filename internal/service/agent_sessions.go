package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Agent sessions: the lifecycle Slack's agents.sessions.* methods give an app
// working in a thread, and the two controls a member has over it — stopping a
// processing session and retitling it.

// agentSessionWriteAttempts bounds the optimistic retries of a rename or a
// stop that lost a race with another writer. Each retry re-reads the state
// the events are built from, so a lost race is never reported with stale
// values; a session contended past the bound reports the conflict.
const agentSessionWriteAttempts = 3

// SetAgentSessionStatus is agents.sessions.setStatus: it writes the calling
// app's status on the thread's session, creating the session first when none
// exists.
func (m Messages) SetAgentSessionStatus(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, thread domain.MessageTimestamp, request domain.AgentSessionStatusRequest) (domain.AgentSessionStatusResult, error) {
	// Standing first: the reference requires the app to be a member of the
	// conversation, and a caller who is not learns nothing about its
	// arguments.
	if err := m.requireConversationMembership(ctx, workspaceID, actor, conversation); err != nil {
		return domain.AgentSessionStatusResult{}, err
	}
	if app == "" {
		return domain.AgentSessionStatusResult{}, domain.ErrInvalidAgentSession
	}
	if _, err := m.agentSessionScope(ctx, workspaceID, conversation, thread); err != nil {
		return domain.AgentSessionStatusResult{}, err
	}
	if !request.Status.Valid() {
		return domain.AgentSessionStatusResult{}, domain.ErrInvalidAgentSessionStatus
	}
	identity, ok := normalizeAgentIdentity(request.Identity)
	if !ok {
		return domain.AgentSessionStatusResult{}, domain.ErrInvalidAgentSession
	}
	write := domain.AgentSessionStatusWrite{
		WorkspaceID: workspaceID, Conversation: conversation, ThreadTimestamp: thread, AppID: app,
		Status: request.Status, ReplaceIdentity: !identity.Empty(), Identity: identity, At: time.Now().UTC(),
	}
	_, err := m.Store.GetAgentSession(ctx, workspaceID, conversation, thread)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// The title and initiator are creation arguments: validated, and
		// used, only when this call creates the session.
		write.Title = strings.TrimSpace(request.Title)
		write.InitiatorUserID = request.InitiatorUserID
		if err := m.validateAgentSessionCreation(ctx, workspaceID, conversation, thread, write.Title, write.InitiatorUserID); err != nil {
			return domain.AgentSessionStatusResult{}, err
		}
	case err != nil:
		return domain.AgentSessionStatusResult{}, err
	}
	// The subscription is read before the write so a manifest that cannot be
	// read fails the call instead of following a committed write.
	subscribed, err := m.appSubscribesToAgentSessionStopped(ctx, workspaceID, app)
	if err != nil {
		return domain.AgentSessionStatusResult{}, err
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload(events.AgentSessionStatusSetTopic,
		events.String("channel_id", string(conversation)),
		events.String("thread_ts", string(thread)),
		events.String("app_id", string(app)),
		events.String("status", string(request.Status)),
	), write.At)
	if err != nil {
		return domain.AgentSessionStatusResult{}, err
	}
	session, err := m.Store.SetAgentSessionStatus(ctx, write, event)
	if err != nil {
		return domain.AgentSessionStatusResult{}, err
	}
	result := domain.AgentSessionStatusResult{Session: session, AgentStatus: request.Status}
	if !subscribed {
		result.Warnings = []string{domain.MissingAgentSessionStoppedSubscription}
	}
	return result, nil
}

// agentSessionScope decides how a session is named in a conversation. A code
// channel is a session channel, whose session is the channel's own and is
// named without a thread (thread_ts_not_allowed otherwise); in any other
// conversation a session is a thread's (thread_ts_required without one). It
// answers whether the conversation is a session channel.
func (m Messages) agentSessionScope(ctx context.Context, workspaceID domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp) (bool, error) {
	_, err := m.Store.GetCodeChannel(ctx, workspaceID, conversation)
	switch {
	case err == nil && thread != "":
		return true, domain.ErrAgentSessionThreadNotAllowed
	case err == nil:
		return true, nil
	case !errors.Is(err, store.ErrNotFound):
		return false, err
	case thread == "":
		return false, domain.ErrAgentSessionThreadRequired
	}
	return false, nil
}

// renameSessionChannel gives a session channel the name its session's new
// title folds to, renamed by the actor retitling the session.
func (m Messages) renameSessionChannel(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversation domain.ConversationID, title string) error {
	channel, err := m.Store.GetConversation(ctx, conversation)
	if err != nil {
		return err
	}
	if channel.Archived {
		return domain.ErrConversationAlreadyArchived
	}
	name := domain.CodeChannelName(title, "")
	if name == "" {
		return domain.ErrInvalidCodeChannelName
	}
	if name == channel.Name {
		return nil
	}
	_, err = m.RenameConversation(ctx, workspaceID, actor, conversation, name)
	return err
}

// validateAgentSessionCreation checks the arguments that only matter when a
// call creates a session: the thread root it is scoped to, its title, and the
// member named as its initiator.
func (m Messages) validateAgentSessionCreation(ctx context.Context, workspaceID domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp, title string, initiator domain.UserID) error {
	if utf8.RuneCountInString(title) > domain.AgentSessionTitleLimit {
		return domain.ErrInvalidAgentSession
	}
	if thread == "" {
		// A session channel's session is the channel's own; there is no
		// thread root to check.
		return m.validateAgentSessionInitiator(ctx, workspaceID, conversation, initiator)
	}
	at, err := domain.ParseMessageTimestamp(thread)
	if err != nil {
		return domain.ErrInvalidAgentSession
	}
	root, err := m.Store.GetMessageByCreatedAt(ctx, conversation, at)
	if errors.Is(err, store.ErrNotFound) {
		return domain.ErrInvalidAgentSession
	}
	if err != nil {
		return err
	}
	// A session is scoped to a thread root: a live top-level message of this
	// conversation, not a reply inside somebody else's thread.
	if root.WorkspaceID != workspaceID || root.Deleted || (root.ThreadTimestamp != "" && root.ThreadTimestamp != thread) {
		return domain.ErrInvalidAgentSession
	}
	return m.validateAgentSessionInitiator(ctx, workspaceID, conversation, initiator)
}

// validateAgentSessionInitiator checks the member a creating setStatus names
// as the session's initiator.
func (m Messages) validateAgentSessionInitiator(ctx context.Context, workspaceID domain.WorkspaceID, conversation domain.ConversationID, initiator domain.UserID) error {
	if initiator == "" {
		return nil
	}
	user, err := m.Store.GetUser(ctx, initiator)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (user.WorkspaceID != workspaceID || user.Deleted)) {
		return domain.ErrUserNotFound
	}
	if err != nil {
		return err
	}
	member, err := m.Store.IsConversationMember(ctx, conversation, initiator)
	if err != nil {
		return err
	}
	if !member {
		// The reference requires the initiator to be a member of the channel
		// and names no code of its own for one who is not.
		return domain.ErrInvalidAgentSession
	}
	return nil
}

// RenameAgentSession is agents.sessions.rename: an agent of the session
// retitles it. It is not agent_session_title_changed, which Slack sends when a
// member changes the title; an app is not told about its own rename.
func (m Messages) RenameAgentSession(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, app domain.AppID, conversation domain.ConversationID, thread domain.MessageTimestamp, title string) (domain.AgentSession, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, actor, conversation); err != nil {
		return domain.AgentSession{}, err
	}
	if app == "" {
		return domain.AgentSession{}, domain.ErrAgentSessionNotAgent
	}
	sessionChannel, err := m.agentSessionScope(ctx, workspaceID, conversation, thread)
	if err != nil {
		return domain.AgentSession{}, err
	}
	title = strings.TrimSpace(title)
	if !domain.ValidAgentSessionTitle(title) {
		return domain.AgentSession{}, domain.ErrInvalidAgentSession
	}
	if sessionChannel {
		// Renaming a session channel's session renames the channel, as the
		// reference says: invalid_name, name_taken and is_archived are the
		// channel's refusals. Only an agent of the session may.
		session, err := m.agentSession(ctx, workspaceID, conversation, thread)
		if err != nil {
			return domain.AgentSession{}, err
		}
		if _, agent := session.Agent(app); !agent {
			return domain.AgentSession{}, domain.ErrAgentSessionNotAgent
		}
		if err := m.renameSessionChannel(ctx, workspaceID, actor, conversation, title); err != nil {
			return domain.AgentSession{}, err
		}
	}
	return m.retitleAgentSession(ctx, workspaceID, conversation, thread, title, func(session domain.AgentSession, at time.Time) ([]events.Event, error) {
		if _, agent := session.Agent(app); !agent {
			return nil, domain.ErrAgentSessionNotAgent
		}
		event, err := newEvent(workspaceID, actor, events.NewPayload(events.AgentSessionRenamedTopic,
			events.String("channel_id", string(conversation)),
			events.String("thread_ts", string(thread)),
			events.String("app_id", string(app)),
		), at)
		return []events.Event{event}, err
	})
}

// ChangeAgentSessionTitle is a member retitling a session from the client.
// Every agent of the session is told with agent_session_title_changed, even
// when the agent set the title being replaced.
func (m Messages) ChangeAgentSessionTitle(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp, title string) (domain.AgentSession, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversation); err != nil {
		return domain.AgentSession{}, err
	}
	sessionChannel, err := m.agentSessionScope(ctx, workspaceID, conversation, thread)
	if err != nil {
		return domain.AgentSession{}, err
	}
	title = strings.TrimSpace(title)
	if sessionChannel && domain.ValidAgentSessionTitle(title) {
		// A member retitling a session channel's session renames the
		// channel, as an agent's rename does.
		if _, err := m.agentSession(ctx, workspaceID, conversation, thread); err != nil {
			return domain.AgentSession{}, err
		}
		if err := m.renameSessionChannel(ctx, workspaceID, userID, conversation, title); err != nil {
			return domain.AgentSession{}, err
		}
	}
	if !domain.ValidAgentSessionTitle(title) {
		return domain.AgentSession{}, domain.ErrInvalidAgentSession
	}
	return m.retitleAgentSession(ctx, workspaceID, conversation, thread, title, func(session domain.AgentSession, at time.Time) ([]events.Event, error) {
		produced := make([]events.Event, 0, len(session.Agents))
		for _, agent := range session.Agents {
			fields := []events.Field{
				events.String("target_app_id", string(agent.AppID)),
				events.String("team_id", string(workspaceID)),
				events.String("channel_id", string(conversation)),
				events.String("thread_ts", string(thread)),
				events.String("user_id", string(userID)),
				events.String("title", title),
			}
			// previous_title is omitted, not empty, when the session had no
			// title before the change.
			if session.Title != "" {
				fields = append(fields, events.String("previous_title", session.Title))
			}
			event, err := newEvent(workspaceID, userID, events.NewPayload(events.AgentSessionTitleChangedTopic, fields...), at)
			if err != nil {
				return nil, err
			}
			produced = append(produced, event)
		}
		return produced, nil
	})
}

// retitleAgentSession is the shared optimistic write: read the session, build
// the events from what was read, and write only while the title is still the
// one read. A title equal to the current one changes nothing and is not
// reported to anyone.
func (m Messages) retitleAgentSession(ctx context.Context, workspaceID domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp, title string, build func(domain.AgentSession, time.Time) ([]events.Event, error)) (domain.AgentSession, error) {
	var err error
	for attempt := 0; attempt < agentSessionWriteAttempts; attempt++ {
		session, readErr := m.agentSession(ctx, workspaceID, conversation, thread)
		if readErr != nil {
			return domain.AgentSession{}, readErr
		}
		at := time.Now().UTC()
		produced, buildErr := build(session, at)
		if buildErr != nil {
			return domain.AgentSession{}, buildErr
		}
		if session.Title == title {
			return session, nil
		}
		var renamed domain.AgentSession
		renamed, err = m.Store.RenameAgentSession(ctx, domain.AgentSessionRename{
			WorkspaceID: workspaceID, Conversation: conversation, ThreadTimestamp: thread,
			ExpectedTitle: session.Title, Title: title, At: at,
		}, produced)
		switch {
		case err == nil:
			return renamed, nil
		case errors.Is(err, store.ErrNotFound):
			return domain.AgentSession{}, domain.ErrAgentSessionNotFound
		case !errors.Is(err, store.ErrConflict):
			return domain.AgentSession{}, err
		}
	}
	return domain.AgentSession{}, err
}

// StopAgentSession is a member pressing the stop control on a processing
// session. Each processing agent whose app subscribes to
// agent_session_stopped is sent the event, and that app's streams in progress
// in the thread are stopped and listed in it. The status is left as it is:
// the reference makes moving it the app's job.
func (m Messages) StopAgentSession(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AgentSession, error) {
	if err := m.requireConversationMembership(ctx, workspaceID, userID, conversation); err != nil {
		return domain.AgentSession{}, err
	}
	if _, err := m.agentSessionScope(ctx, workspaceID, conversation, thread); err != nil {
		return domain.AgentSession{}, err
	}
	var err error
	for attempt := 0; attempt < agentSessionWriteAttempts; attempt++ {
		session, readErr := m.agentSession(ctx, workspaceID, conversation, thread)
		if readErr != nil {
			return domain.AgentSession{}, readErr
		}
		apps, readErr := m.stoppableAgents(ctx, session)
		if readErr != nil {
			return domain.AgentSession{}, readErr
		}
		if len(apps) == 0 {
			return domain.AgentSession{}, domain.ErrAgentSessionNotStoppable
		}
		stop, produced, buildErr := m.agentSessionStop(ctx, session, userID, apps)
		if buildErr != nil {
			return domain.AgentSession{}, buildErr
		}
		err = m.Store.StopAgentSession(ctx, stop, produced)
		switch {
		case err == nil:
			return session, nil
		case errors.Is(err, store.ErrNotFound):
			return domain.AgentSession{}, domain.ErrAgentSessionNotFound
		case !errors.Is(err, store.ErrConflict):
			return domain.AgentSession{}, err
		}
	}
	return domain.AgentSession{}, err
}

// agentSessionStop builds one stop: every in-progress stream of each stopped
// app in the thread, ended exactly as chat.stopStream ends one, and one
// agent_session_stopped record per app listing its streams.
func (m Messages) agentSessionStop(ctx context.Context, session domain.AgentSession, userID domain.UserID, apps []domain.AppID) (domain.AgentSessionStop, []events.Event, error) {
	stop := domain.AgentSessionStop{WorkspaceID: session.WorkspaceID, Conversation: session.Conversation, ThreadTimestamp: session.ThreadTimestamp, Agents: apps}
	var produced []events.Event
	at := time.Now().UTC()
	for _, app := range apps {
		timestamps, err := m.Store.ListActiveMessageStreams(ctx, session.Conversation, session.ThreadTimestamp, app)
		if err != nil {
			return domain.AgentSessionStop{}, nil, err
		}
		stopped := make([]string, 0, len(timestamps))
		for _, timestamp := range timestamps {
			createdAt, err := domain.ParseMessageTimestamp(timestamp)
			if err != nil {
				return domain.AgentSessionStop{}, nil, err
			}
			previous, err := m.Store.GetMessageByCreatedAt(ctx, session.Conversation, createdAt)
			if err != nil {
				return domain.AgentSessionStop{}, nil, err
			}
			var state domain.MessageStreamState
			if json.Unmarshal([]byte(previous.StreamState), &state) != nil || !state.Active {
				continue
			}
			state.Active = false
			encoded, err := json.Marshal(state)
			if err != nil {
				return domain.AgentSessionStop{}, nil, err
			}
			current := previous
			current.StreamState = string(encoded)
			changed, err := messageMutationEvent(session.WorkspaceID, "message.changed", current, previous)
			if err != nil {
				return domain.AgentSessionStop{}, nil, err
			}
			stop.Streams = append(stop.Streams, domain.AgentSessionStoppedStream{Message: current, PreviousStreamState: previous.StreamState})
			produced = append(produced, changed)
			stopped = append(stopped, string(timestamp))
		}
		event, err := newEvent(session.WorkspaceID, userID, events.NewPayload(events.AgentSessionStoppedTopic,
			events.String("target_app_id", string(app)),
			events.String("channel_id", string(session.Conversation)),
			events.String("thread_ts", string(session.ThreadTimestamp)),
			events.String("user_id", string(userID)),
			events.Strings("streaming_message_ts", stopped),
		), at)
		if err != nil {
			return domain.AgentSessionStop{}, nil, err
		}
		produced = append(produced, event)
	}
	return stop, produced, nil
}

// AgentSession reads a thread's session as a member's client shows it.
func (m Messages) AgentSession(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AgentSessionView, error) {
	if err := m.authorizeConversation(ctx, workspaceID, userID, conversation); err != nil {
		return domain.AgentSessionView{}, err
	}
	if _, err := m.agentSessionScope(ctx, workspaceID, conversation, thread); err != nil {
		return domain.AgentSessionView{}, err
	}
	session, err := m.agentSession(ctx, workspaceID, conversation, thread)
	if err != nil {
		return domain.AgentSessionView{}, err
	}
	apps, err := m.stoppableAgents(ctx, session)
	if err != nil {
		return domain.AgentSessionView{}, err
	}
	return domain.AgentSessionView{Session: session, Stoppable: len(apps) > 0}, nil
}

func (m Messages) agentSession(ctx context.Context, workspaceID domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AgentSession, error) {
	session, err := m.Store.GetAgentSession(ctx, workspaceID, conversation, thread)
	if errors.Is(err, store.ErrNotFound) {
		return domain.AgentSession{}, domain.ErrAgentSessionNotFound
	}
	return session, err
}

// stoppableAgents are the processing agents a stop reaches: those whose app
// subscribes to agent_session_stopped. An agent whose app does not is shown
// a loading indicator with no control, so a stop is not its to receive.
func (m Messages) stoppableAgents(ctx context.Context, session domain.AgentSession) ([]domain.AppID, error) {
	var apps []domain.AppID
	for _, app := range session.AgentsIn(domain.AgentSessionProcessing) {
		subscribed, err := m.appSubscribesToAgentSessionStopped(ctx, session.WorkspaceID, app)
		if err != nil {
			return nil, err
		}
		if subscribed {
			apps = append(apps, app)
		}
	}
	return apps, nil
}

// appSubscribesToAgentSessionStopped reads the installed app's manifest. An
// app that is no longer installed here subscribes to nothing.
func (m Messages) appSubscribesToAgentSessionStopped(ctx context.Context, workspaceID domain.WorkspaceID, app domain.AppID) (bool, error) {
	_, parsed, err := m.installedApp(ctx, workspaceID, app)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, domain.ErrAppInteractionUnavailable) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return slices.Contains(parsed.BotEvents, domain.AgentSessionStoppedEvent) || slices.Contains(parsed.UserEvents, domain.AgentSessionStoppedEvent), nil
}

// normalizeAgentIdentity trims a setStatus identity override and reports
// whether it may be stored: a username within the bound and an icon_url that
// is an absolute http(s) address, as a message's own icon_url must be.
func normalizeAgentIdentity(identity domain.AgentIdentity) (domain.AgentIdentity, bool) {
	identity = domain.AgentIdentity{
		Username:  strings.TrimSpace(identity.Username),
		IconEmoji: strings.TrimSpace(identity.IconEmoji),
		IconURL:   strings.TrimSpace(identity.IconURL),
	}
	valid := utf8.RuneCountInString(identity.Username) <= domain.AgentIdentityUsernameLimit &&
		(identity.IconURL == "" || validMessageIconURL(identity.IconURL))
	return identity, valid
}
