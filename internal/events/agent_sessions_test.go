package events

import (
	"errors"
	"testing"
	"time"
)

func agentSessionDelivered(t *testing.T, topic string, fields ...Field) Delivered {
	t.Helper()
	event, err := New("EVAS", "T1", "U1", NewPayload(topic, fields...), time.Unix(1_783_536_983, 783_769_000).UTC())
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := Broadcastable(event)
	if err != nil {
		t.Fatal(err)
	}
	return delivered
}

// TestAgentSessionEventsMatchTheReferencePayloads renders both events as the
// reference pages show them, field for field, and only on the surfaces the
// pages list: the Events API (over HTTP or Socket Mode), never RTM.
func TestAgentSessionEventsMatchTheReferencePayloads(t *testing.T) {
	for _, test := range []struct {
		name   string
		topic  string
		fields []Field
		want   string
	}{
		{
			name:  "agent_session_stopped",
			topic: AgentSessionStoppedTopic,
			fields: []Field{
				String("target_app_id", "A1"), String("channel_id", "C0123ABC456"), String("thread_ts", "1782234671.392669"),
				String("user_id", "U123ABC456"), Strings("streaming_message_ts", []string{"1782234987.693923"}),
			},
			want: `{"channel":"C0123ABC456","event_ts":"1783536983.783769","streaming_message_ts":["1782234987.693923"],"thread_ts":"1782234671.392669","type":"agent_session_stopped","user":"U123ABC456"}`,
		},
		{
			name:  "agent_session_stopped with no stream",
			topic: AgentSessionStoppedTopic,
			fields: []Field{
				String("target_app_id", "A1"), String("channel_id", "C1"), String("thread_ts", "1782234671.392669"),
				String("user_id", "U1"), Strings("streaming_message_ts", nil),
			},
			want: `{"channel":"C1","event_ts":"1783536983.783769","streaming_message_ts":[],"thread_ts":"1782234671.392669","type":"agent_session_stopped","user":"U1"}`,
		},
		{
			name:  "agent_session_title_changed",
			topic: AgentSessionTitleChangedTopic,
			fields: []Field{
				String("target_app_id", "A1"), String("team_id", "T0123ABC456"), String("channel_id", "C0123ABC456"),
				String("thread_ts", "1782234671.392669"), String("user_id", "U123ABC456"),
				String("title", "Bora Bora trip prep"), String("previous_title", "Scuba diving research"),
			},
			want: `{"channel":"C0123ABC456","event_ts":"1783536983.783769","previous_title":"Scuba diving research","team_id":"T0123ABC456","thread_ts":"1782234671.392669","title":"Bora Bora trip prep","type":"agent_session_title_changed","user":"U123ABC456"}`,
		},
		{
			name:  "agent_session_title_changed from no title",
			topic: AgentSessionTitleChangedTopic,
			fields: []Field{
				String("target_app_id", "A1"), String("team_id", "T1"), String("channel_id", "C1"),
				String("thread_ts", "1782234671.392669"), String("user_id", "U1"), String("title", "First"),
			},
			want: `{"channel":"C1","event_ts":"1783536983.783769","team_id":"T1","thread_ts":"1782234671.392669","title":"First","type":"agent_session_title_changed","user":"U1"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			delivered := agentSessionDelivered(t, test.topic, test.fields...)
			for _, surface := range []Surface{SurfaceEventsAPI, SurfaceSocketMode} {
				inners, err := SlackInner(test.topic, delivered, surface)
				if err != nil || len(inners) != 1 {
					t.Fatalf("surface %d: %d inners, err %v", surface, len(inners), err)
				}
				encoded, err := inners[0].Encode()
				if err != nil {
					t.Fatal(err)
				}
				// target_app_id is routing, not event content; the app-facing
				// paths strip it before building (broadcastableToApp), so the
				// builder never reads it.
				if encoded != test.want {
					t.Fatalf("surface %d:\n got  %s\n want %s", surface, encoded, test.want)
				}
			}
			if inners, err := SlackInner(test.topic, delivered, SurfaceRTM); err != nil || len(inners) != 0 {
				t.Fatalf("RTM carried %d inners (err %v); the pages list the Events API only", len(inners), err)
			}
		})
	}
}

// TestAgentSessionEventsRefuseAnIncompleteRecord holds the producers to the
// fields the pages require: a record missing one is a producer defect the
// outbox classifies as permanent, not an event with a hole in it.
func TestAgentSessionEventsRefuseAnIncompleteRecord(t *testing.T) {
	stopped := agentSessionDelivered(t, AgentSessionStoppedTopic, String("channel_id", "C1"), String("thread_ts", "1.2"), String("user_id", "U1"))
	if _, err := SlackInner(AgentSessionStoppedTopic, stopped, SurfaceEventsAPI); !errors.Is(err, ErrSlackEventIncomplete) {
		t.Fatalf("a stop without streaming_message_ts: %v", err)
	}
	retitled := agentSessionDelivered(t, AgentSessionTitleChangedTopic, String("channel_id", "C1"), String("thread_ts", "1.2"), String("user_id", "U1"), String("title", "x"))
	if _, err := SlackInner(AgentSessionTitleChangedTopic, retitled, SurfaceEventsAPI); !errors.Is(err, ErrSlackEventIncomplete) {
		t.Fatalf("a retitle without team_id: %v", err)
	}
	// The app-caused records are not Slack events at all.
	for _, topic := range []string{AgentSessionStatusSetTopic, AgentSessionRenamedTopic} {
		if name, mapped := SlackEventType(topic); mapped {
			t.Fatalf("%s maps to %s", topic, name)
		}
	}
	if AutomaticSlackEvent("agent_session_stopped") || AutomaticSlackEvent("agent_session_title_changed") {
		t.Fatal("both events need a subscription; neither is dispatched automatically")
	}
}
