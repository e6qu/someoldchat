package events

import "fmt"

// The durable agent session topics. Only the two a member causes are Slack
// events; the two an app causes exist so an open client re-renders the
// session. The topic table records each decision.
const (
	AgentSessionStatusSetTopic    = "agent_session.status_set"
	AgentSessionRenamedTopic      = "agent_session.renamed"
	AgentSessionTitleChangedTopic = "agent_session.title_changed"
	AgentSessionStoppedTopic      = "agent_session.stopped"
)

// The two agent session events, built as the current Slack reference pages
// for agent_session_stopped and agent_session_title_changed show them. Both
// are app events (the pages list the Events API alone, so RTM never carries
// them), both need chat:write, and both are routed by target_app_id to one
// agent of the session per record.

// agentSessionStopped renders a member pressing stop:
//
//	{"type":"agent_session_stopped","channel":…,"thread_ts":…,"user":…,
//	 "streaming_message_ts":[…],"event_ts":…}
//
// streaming_message_ts is always present — an empty list when no stream was
// in progress — and team_id is on the event_callback envelope rather than in
// the event, as the page says.
func agentSessionStopped(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "channel_id", "thread_ts", "user_id")
	if err != nil {
		return nil, err
	}
	streams, ok := delivered.Strings("streaming_message_ts")
	if !ok {
		return nil, fmt.Errorf("%w: agent_session_stopped payload has no streaming_message_ts", ErrSlackEventIncomplete)
	}
	inner, err := newInner("agent_session_stopped", delivered,
		String("channel", values["channel_id"]),
		String("thread_ts", values["thread_ts"]),
		String("user", values["user_id"]),
		Strings("streaming_message_ts", streams),
	)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}

// agentSessionTitleChanged renders a member retitling a session:
//
//	{"type":"agent_session_title_changed","channel":…,"thread_ts":…,"user":…,
//	 "title":…,"previous_title":…,"team_id":…,"event_ts":…}
//
// previous_title is omitted when the session had no title before the change.
// enterprise_id, which the page includes for org-level installs, has no
// counterpart here: this product has no Enterprise organization installs.
func agentSessionTitleChanged(delivered Delivered, _ Surface) ([]Inner, error) {
	values, err := stringFields(delivered, "channel_id", "thread_ts", "user_id", "title", "team_id")
	if err != nil {
		return nil, err
	}
	fields := []Field{
		String("channel", values["channel_id"]),
		String("thread_ts", values["thread_ts"]),
		String("user", values["user_id"]),
		String("title", values["title"]),
		String("team_id", values["team_id"]),
	}
	if previous, ok := delivered.Field("previous_title"); ok && previous != "" {
		fields = append(fields, String("previous_title", previous))
	}
	inner, err := newInner("agent_session_title_changed", delivered, fields...)
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}
