package domain

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// An agent session is Slack's lifecycle record for an app working in one
// thread: agents.sessions.setStatus creates it and moves each agent through
// active, processing, suspended and closed, agents.sessions.rename titles it,
// and a member may stop a processing session or retitle it from the client.
//
// The reference pages scope a session to a thread root in a regular channel or
// a DM, or to a "session channel". This product has no session channels, so
// every session here is a thread session and is keyed by its conversation and
// thread root.

// AgentSessionStatus is one agent's lifecycle status, as
// agents.sessions.setStatus writes it.
type AgentSessionStatus string

const (
	// AgentSessionActive: the agent is alive and ready for the next prompt.
	AgentSessionActive AgentSessionStatus = "active"
	// AgentSessionProcessing: the agent is working on a member's task. The
	// client shows a loading state, with a stop control when the app
	// subscribes to agent_session_stopped.
	AgentSessionProcessing AgentSessionStatus = "processing"
	// AgentSessionSuspended: the agent cannot progress until a member
	// intervenes — a clarification or a tool approval.
	AgentSessionSuspended AgentSessionStatus = "suspended"
	// AgentSessionClosed: the agent has closed the session and will no longer
	// respond on it.
	AgentSessionClosed AgentSessionStatus = "closed"
)

// Valid reports whether the status is one of the four the reference accepts.
func (s AgentSessionStatus) Valid() bool {
	return s.precedence() > 0
}

// precedence orders the statuses for the session-level status the reference
// derives from all of a session's agents: suspended > processing > active >
// closed. Zero is an invalid status.
func (s AgentSessionStatus) precedence() int {
	switch s {
	case AgentSessionClosed:
		return 1
	case AgentSessionActive:
		return 2
	case AgentSessionProcessing:
		return 3
	case AgentSessionSuspended:
		return 4
	}
	return 0
}

// AgentSessionTitleLimit is the reference's title bound for both methods: a
// rename takes 1-200 characters and a creating setStatus at most 200.
const AgentSessionTitleLimit = 200

// AgentSessionUsernameLimit bounds setStatus's username override.
const AgentSessionUsernameLimit = 200

// AgentIdentity is the display override an agent may set with setStatus's
// icon_emoji, icon_url and username. The three travel as one set: a call that
// sets any of them replaces all three, so one left out of that call is
// cleared, and a call that sets none leaves the set as it was.
type AgentIdentity struct {
	Username  string
	IconEmoji string
	IconURL   string
}

// Empty reports an identity that overrides nothing.
func (i AgentIdentity) Empty() bool {
	return i.Username == "" && i.IconEmoji == "" && i.IconURL == ""
}

// AgentSessionAgent is one app's part in a session.
type AgentSessionAgent struct {
	AppID     AppID
	Status    AgentSessionStatus
	Identity  AgentIdentity
	UpdatedAt time.Time
}

// AgentSession is a session and every agent that has written to it.
type AgentSession struct {
	WorkspaceID     WorkspaceID
	Conversation    ConversationID
	ThreadTimestamp MessageTimestamp
	Title           string
	// InitiatorUserID is the member setStatus named when it created the
	// session, or empty when it named nobody.
	InitiatorUserID UserID
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// Agents is ordered by app ID.
	Agents []AgentSessionAgent
}

// Status is the session-level status the reference derives from the statuses
// of all its agents: the most demanding one wins, in the order suspended,
// processing, active, closed. A session nobody has written to is closed.
func (s AgentSession) Status() AgentSessionStatus {
	result := AgentSessionClosed
	for _, agent := range s.Agents {
		if agent.Status.precedence() > result.precedence() {
			result = agent.Status
		}
	}
	return result
}

// Agent returns one app's part in the session.
func (s AgentSession) Agent(app AppID) (AgentSessionAgent, bool) {
	for _, agent := range s.Agents {
		if agent.AppID == app {
			return agent, true
		}
	}
	return AgentSessionAgent{}, false
}

// AgentsIn lists the apps whose agent holds the given status, in app order.
func (s AgentSession) AgentsIn(status AgentSessionStatus) []AppID {
	var apps []AppID
	for _, agent := range s.Agents {
		if agent.Status == status {
			apps = append(apps, agent.AppID)
		}
	}
	return apps
}

// SortAgentSessionAgents puts agents in the order AgentSession.Agents
// promises, so every store answers the same sequence.
func SortAgentSessionAgents(agents []AgentSessionAgent) {
	sort.Slice(agents, func(left, right int) bool { return agents[left].AppID < agents[right].AppID })
}

// ValidAgentSessionTitle reports whether a title is one rename accepts: one to
// AgentSessionTitleLimit characters once surrounding space is removed.
func ValidAgentSessionTitle(title string) bool {
	title = strings.TrimSpace(title)
	return title != "" && utf8.RuneCountInString(title) <= AgentSessionTitleLimit
}

// AgentSessionStatusRequest is what agents.sessions.setStatus carries beyond
// the session's key and the calling app.
type AgentSessionStatusRequest struct {
	Status AgentSessionStatus
	// Title and InitiatorUserID apply only when this call creates the session;
	// they are ignored when it already exists.
	Title           string
	InitiatorUserID UserID
	// Identity replaces the agent's display override when it sets anything,
	// and leaves the override alone when it is empty.
	Identity AgentIdentity
}

// AgentSessionStatusResult is setStatus's answer: the session after the write
// (whose Status is the session-level status), the calling agent's own status,
// and the warnings Slack names in the response.
type AgentSessionStatusResult struct {
	Session     AgentSession
	AgentStatus AgentSessionStatus
	Warnings    []string
}

// MissingAgentSessionStoppedSubscription is the warning setStatus returns
// while the calling app does not subscribe to agent_session_stopped: the
// member then sees a loading indicator with no stop control.
const MissingAgentSessionStoppedSubscription = "missing_agent_session_stopped_event_subscription"

// AgentSessionStoppedEvent is the event name an app subscribes to so a member
// can stop it.
const AgentSessionStoppedEvent = "agent_session_stopped"

// ActiveMessageStreamPrefix is how every stored stream state of a message
// still streaming begins. MessageStreamState declares Active first, and
// encoding/json writes fields in declaration order, so a store can find the
// streams in progress without decoding every message's state; the domain test
// TestActiveMessageStreamPrefixMatchesTheEncoding holds the two together.
const ActiveMessageStreamPrefix = `{"active":true`

// AgentSessionStatusWrite is one setStatus mutation as a store applies it: the
// session row is created with Title and InitiatorUserID when it does not exist,
// and the app's agent row is written with Status (and Identity, when
// ReplaceIdentity is set).
type AgentSessionStatusWrite struct {
	WorkspaceID     WorkspaceID
	Conversation    ConversationID
	ThreadTimestamp MessageTimestamp
	AppID           AppID
	Status          AgentSessionStatus
	Title           string
	InitiatorUserID UserID
	ReplaceIdentity bool
	Identity        AgentIdentity
	At              time.Time
}

// Valid reports whether the write names everything a store needs.
func (w AgentSessionStatusWrite) Valid() bool {
	return w.WorkspaceID != "" && w.Conversation != "" && w.ThreadTimestamp != "" && w.AppID != "" && w.Status.Valid() && !w.At.IsZero()
}

// AgentSessionRename retitles a session, but only while its title is still
// ExpectedTitle: the title the rename's events were built from. A store
// answers store.ErrConflict when the title moved underneath, so a concurrent
// rename cannot be reported with the wrong previous_title.
type AgentSessionRename struct {
	WorkspaceID     WorkspaceID
	Conversation    ConversationID
	ThreadTimestamp MessageTimestamp
	ExpectedTitle   string
	Title           string
	At              time.Time
}

// Valid reports whether the rename names everything a store needs.
func (r AgentSessionRename) Valid() bool {
	return r.WorkspaceID != "" && r.Conversation != "" && r.ThreadTimestamp != "" && strings.TrimSpace(r.Title) != "" && !r.At.IsZero()
}

// AgentSessionStoppedStream is one in-progress streaming message a stop ends:
// the message as it is after the stop, and the stream state it must still
// hold for the stop to apply.
type AgentSessionStoppedStream struct {
	Message             Message
	PreviousStreamState string
}

// AgentSessionStop is a member pressing stop on a processing session. It
// applies only while every app in Agents is still processing and every stream
// still holds its PreviousStreamState; otherwise a store answers
// store.ErrConflict and the caller re-reads. The session's status itself is
// not changed: the reference leaves that to the app.
type AgentSessionStop struct {
	WorkspaceID     WorkspaceID
	Conversation    ConversationID
	ThreadTimestamp MessageTimestamp
	Agents          []AppID
	Streams         []AgentSessionStoppedStream
}

// Valid reports whether the stop names everything a store needs.
func (s AgentSessionStop) Valid() bool {
	return s.WorkspaceID != "" && s.Conversation != "" && s.ThreadTimestamp != "" && len(s.Agents) > 0
}

// AgentSessionView is a session as a member's client shows it.
type AgentSessionView struct {
	Session AgentSession
	// Stoppable reports that the client shows a stop control: an agent is
	// processing and its app subscribes to agent_session_stopped. Without the
	// subscription the client shows a loading indicator with no control.
	Stoppable bool
}
