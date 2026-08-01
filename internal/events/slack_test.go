package events

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func TestSlackSignatureUsesPublishedV0Format(t *testing.T) {
	timestamp := time.Unix(1531420618, 0).UTC()
	body := []byte("token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&channel_name=foobar&user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&trigger_id=398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c")
	signature, err := SlackSignature("8f742231b10e8888abcd99yyyzzz85a5", timestamp, body)
	if err != nil {
		t.Fatal(err)
	}
	if signature != "v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503" {
		t.Fatalf("signature=%q", signature)
	}
	if _, err := SlackSignature("", timestamp, body); err == nil {
		t.Fatal("empty signing secret accepted")
	}
}

func TestSlackEventBodiesFanOutInvitedMembers(t *testing.T) {
	event, err := New("Ev1", "T1", "U1", NewPayload("conversation.members_invited",
		String("channel_id", "C1"),
		Strings("user_ids", []string{"U2", "U3"}),
	), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	bodies, err := SlackEventBodies(Record{Sequence: 1, Event: event}, "A1")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies=%d, want one member_joined_channel per invited user", len(bodies))
	}
	for index, wantUser := range []string{"U2", "U3"} {
		var envelope struct {
			EventContext string `json:"event_context"`
			Event        struct {
				Type    string `json:"type"`
				User    string `json:"user"`
				Channel string `json:"channel"`
				EventTS string `json:"event_ts"`
			} `json:"event"`
		}
		if err := json.Unmarshal(bodies[index], &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Event.Type != "member_joined_channel" || envelope.Event.User != wantUser || envelope.Event.Channel != "C1" || envelope.Event.EventTS == "" {
			t.Fatalf("body[%d]=%s", index, bodies[index])
		}
		appID, sequence, eventID, err := ParseEventContext(envelope.EventContext)
		if err != nil || appID != "A1" || sequence != 1 || eventID != "Ev1" {
			t.Fatalf("event_context=%q decoded app=%q sequence=%d event=%q err=%v", envelope.EventContext, appID, sequence, eventID, err)
		}
	}
	if _, _, _, err := ParseEventContext("qualification-event"); !errors.Is(err, ErrPayloadFieldInvalid) {
		t.Fatalf("malformed event context error=%v", err)
	}
}

func TestAppHomeOpenedIsDeliveredOnlyToItsOwningApp(t *testing.T) {
	event, err := New("Ev-home", "T1", "U1", NewPayload("app.home_opened",
		String("target_app_id", "A1"),
		String("user_id", "U1"),
		String("channel_id", "D1"),
		String("tab", "home"),
		JSON("view", `{"id":"V1","type":"home","app_id":"A1","blocks":[]}`),
	), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Sequence: 1, Event: event}
	bodies, err := SlackEventBodies(record, "A1")
	if err != nil || len(bodies) != 1 {
		t.Fatalf("owning app bodies=%d err=%v", len(bodies), err)
	}
	var envelope struct {
		AppID string `json:"api_app_id"`
		Event struct {
			Type    string         `json:"type"`
			User    string         `json:"user"`
			Channel string         `json:"channel"`
			Tab     string         `json:"tab"`
			View    map[string]any `json:"view"`
		} `json:"event"`
	}
	if err := json.Unmarshal(bodies[0], &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.AppID != "A1" || envelope.Event.Type != "app_home_opened" ||
		envelope.Event.User != "U1" || envelope.Event.Channel != "D1" || envelope.Event.Tab != "home" ||
		envelope.Event.View["id"] != "V1" {
		t.Fatalf("envelope=%s", bodies[0])
	}
	if string(bodies[0]) == "" || containsJSONField(bodies[0], "target_app_id") {
		t.Fatalf("routing metadata leaked into Slack payload: %s", bodies[0])
	}
	other, err := SlackEventBodies(record, "A2")
	if err != nil || len(other) != 0 {
		t.Fatalf("other app received %d bodies err=%v", len(other), err)
	}
	socket, err := SocketModeEnvelopes(record, "A2")
	if err != nil || len(socket) != 0 {
		t.Fatalf("other app received %d socket envelopes err=%v", len(socket), err)
	}
}

func TestFunctionExecutedBuildsCurrentSlackEventForOnlyItsOwningApp(t *testing.T) {
	event, err := New("Ev-function", "T1", "U1", NewPayload("function_executed",
		String("target_app_id", "A1"),
		JSON("function", `{"id":"Fn1","callback_id":"triage","title":"Triage","type":"app","input_parameters":[],"output_parameters":[],"app_id":"A1","date_created":1700000000,"date_updated":1700000000,"date_deleted":0}`),
		JSON("inputs", `{"incident":"INC-1"}`),
		String("function_execution_id", "Fx1"),
		String("workflow_execution_id", "Wx1"),
		String("bot_access_token", "xoxb-execution"),
	), time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	event.Authorizations = []Authorization{{TeamID: "T1", UserID: "UB", IsBot: true}}
	record := Record{Sequence: 1, Event: event}
	bodies, err := SlackEventBodies(record, "A1")
	if err != nil || len(bodies) != 1 {
		t.Fatalf("owning app bodies=%d err=%v", len(bodies), err)
	}
	var envelope struct {
		AppID string `json:"api_app_id"`
		Event struct {
			Type                string         `json:"type"`
			Function            map[string]any `json:"function"`
			Inputs              map[string]any `json:"inputs"`
			FunctionExecutionID string         `json:"function_execution_id"`
			WorkflowExecutionID string         `json:"workflow_execution_id"`
			BotAccessToken      string         `json:"bot_access_token"`
			EventTS             string         `json:"event_ts"`
		} `json:"event"`
	}
	if err := json.Unmarshal(bodies[0], &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.AppID != "A1" || envelope.Event.Type != "function_executed" ||
		envelope.Event.Function["id"] != "Fn1" || envelope.Event.Inputs["incident"] != "INC-1" ||
		envelope.Event.FunctionExecutionID != "Fx1" || envelope.Event.WorkflowExecutionID != "Wx1" ||
		envelope.Event.BotAccessToken != "xoxb-execution" || envelope.Event.EventTS == "" {
		t.Fatalf("envelope=%s", bodies[0])
	}
	if containsJSONField(bodies[0], "target_app_id") {
		t.Fatalf("routing metadata leaked into Slack payload: %s", bodies[0])
	}
	other, err := SlackEventBodies(record, "A2")
	if err != nil || len(other) != 0 {
		t.Fatalf("other app received %d bodies err=%v", len(other), err)
	}
	if inners, err := SlackInner(event.Topic, mustDeliverable(t, event), SurfaceRTM); err != nil || len(inners) != 0 {
		t.Fatalf("RTM received app-only function event: inners=%v err=%v", inners, err)
	}
}

func mustDeliverable(t *testing.T, event Event) Delivered {
	t.Helper()
	delivered, err := Deliverable(event)
	if err != nil {
		t.Fatal(err)
	}
	return delivered
}

func containsJSONField(encoded []byte, field string) bool {
	var value any
	if json.Unmarshal(encoded, &value) != nil {
		return false
	}
	return jsonTreeContainsField(value, field)
}

func jsonTreeContainsField(value any, field string) bool {
	switch value := value.(type) {
	case map[string]any:
		if _, ok := value[field]; ok {
			return true
		}
		for _, child := range value {
			if jsonTreeContainsField(child, field) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if jsonTreeContainsField(child, field) {
				return true
			}
		}
	}
	return false
}

// An RTM-only mapping must stay off the webhook and Socket Mode no matter how
// the record arrives: a produced payload is translated for RTM and withheld
// from every application surface, and a pre-translated presence body cannot
// ride an application surface either. The topic used to be a mapped row fed
// by pre-translated bodies; it is producer-translated now, and the surface
// restriction is checked before the builder runs, so both arrivals stay
// restricted.
func TestPresenceCannotBypassItsRTMOnlySurface(t *testing.T) {
	payload := NewPayload("user.presence_changed",
		String("user_id", "U1"),
		String("presence", "away"),
	)
	event, err := New("Ev1", "T1", "U1", payload, time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := Deliverable(event)
	if err != nil {
		t.Fatal(err)
	}
	if inners, err := SlackInner(event.Topic, delivered, SurfaceEventsAPI); err != nil || len(inners) != 0 {
		t.Fatalf("Events API inners=%v err=%v, want RTM-only event withheld", inners, err)
	}
	if inners, err := SlackInner(event.Topic, delivered, SurfaceSocketMode); err != nil || len(inners) != 0 {
		t.Fatalf("Socket Mode inners=%v err=%v, want RTM-only event withheld", inners, err)
	}
	inners, err := SlackInner(event.Topic, delivered, SurfaceRTM)
	if err != nil || len(inners) != 1 || inners[0].Type() != "presence_change" {
		t.Fatalf("RTM inners=%v err=%v", inners, err)
	}
	pretranslated := Event{
		ID:          "Ev2",
		WorkspaceID: "T1",
		Topic:       "user.presence_changed",
		Payload:     `{"type":"presence_change","event_ts":"1700000000.000000","user":"U1","presence":"away"}`,
		CreatedAt:   time.Unix(1700000000, 0).UTC(),
	}
	deliveredPretranslated, err := Deliverable(pretranslated)
	if err != nil {
		t.Fatal(err)
	}
	if inners, err := SlackInner(pretranslated.Topic, deliveredPretranslated, SurfaceEventsAPI); err != nil || len(inners) != 0 {
		t.Fatalf("pre-translated Events API inners=%v err=%v, want withheld", inners, err)
	}
}

func TestPretranslatedEventMustMatchKnownTopicMapping(t *testing.T) {
	event := Event{
		ID:          "Ev1",
		WorkspaceID: "T1",
		Topic:       "message.created",
		Payload:     `{"type":"reaction_added","event_ts":"1700000000.000000"}`,
		CreatedAt:   time.Unix(1700000000, 0).UTC(),
	}
	delivered, err := Deliverable(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SlackInner(event.Topic, delivered, SurfaceSocketMode); !errors.Is(err, ErrPayloadFieldInvalid) {
		t.Fatalf("mismatched translated event error=%v, want %v", err, ErrPayloadFieldInvalid)
	}
}

// TestPromotedTopicsTranslateFromTheirProducerPayloads drives every wave-one
// promotion through SlackInner with the exact payload shape its producer
// writes, and asserts the event name and the load-bearing fields. The
// payloads here are the contract the producers in internal/service and
// internal/scheduler must keep: a field dropped there surfaces here as
// ErrSlackEventIncomplete before it surfaces in an app's webhook.
func TestPromotedTopicsTranslateFromTheirProducerPayloads(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	user := domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", RealName: "Alice"}
	user.Profile.StatusText = "focussing"
	userPayload := func(topic string, statusChanged bool) Payload {
		payload, err := UserChangePayload(topic, user, topic == "user.removed", statusChanged, at)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	cases := []struct {
		name    string
		payload Payload
		surface Surface
		want    []string          // event type per emitted inner, in order
		fields  map[string]string // raw JSON fragment per field of the FIRST inner
	}{
		{
			name: "public channel created",
			payload: NewPayload("conversation.created",
				String("channel_id", "C1"), String("name", "general"),
				Bool("is_private", false), Int("created", 1700000000), String("user_id", "U1")),
			surface: SurfaceEventsAPI,
			want:    []string{"channel_created"},
			fields:  map[string]string{"channel": `"name":"general"`},
		},
		{
			name: "direct conversation created",
			payload: NewPayload("conversation.direct_created",
				String("channel_id", "D1"), String("name", "direct"),
				Bool("is_private", true), Int("created", 1700000000), String("user_id", "U1")),
			surface: SurfaceEventsAPI,
			want:    []string{"im_created"},
			fields:  map[string]string{"user": `"U1"`, "channel": `"is_im":true`},
		},
		{
			name: "public rename",
			payload: NewPayload("conversation.renamed",
				String("channel_id", "C1"), String("name", "renamed"), Bool("is_private", false), String("user_id", "U1")),
			surface: SurfaceEventsAPI,
			want:    []string{"channel_rename"},
		},
		{
			name: "private rename travels as group_rename",
			payload: NewPayload("conversation.renamed",
				String("channel_id", "C1"), String("name", "renamed"), Bool("is_private", true), String("user_id", "U1")),
			surface: SurfaceEventsAPI,
			want:    []string{"group_rename"},
		},
		{
			name: "public archive carries the acting user",
			payload: NewPayload("conversation.archived",
				String("channel_id", "C1"), String("name", "general"), Bool("is_private", false), String("user_id", "U2")),
			surface: SurfaceEventsAPI,
			want:    []string{"channel_archive"},
			fields:  map[string]string{"channel": `"C1"`, "user": `"U2"`},
		},
		{
			name: "private unarchive travels as group_unarchive",
			payload: NewPayload("conversation.unarchived",
				String("channel_id", "C1"), String("name", "general"), Bool("is_private", true), String("user_id", "U2")),
			surface: SurfaceEventsAPI,
			want:    []string{"group_unarchive"},
		},
		{
			name: "channel deleted",
			payload: NewPayload("conversation.deleted",
				String("channel_id", "C1"), String("name", "general"), Bool("is_private", false), String("user_id", "U2")),
			surface: SurfaceEventsAPI,
			want:    []string{"channel_deleted"},
		},
		{
			name:    "emoji added",
			payload: NewPayload("emoji.added", String("name", "party"), String("value", "https://emoji.test/party.png")),
			surface: SurfaceSocketMode,
			want:    []string{"emoji_changed"},
			fields:  map[string]string{"subtype": `"add"`, "name": `"party"`, "value": `"https://emoji.test/party.png"`},
		},
		{
			name:    "emoji alias added",
			payload: NewPayload("emoji.alias_added", String("name", "cheer"), String("alias_for", "party")),
			surface: SurfaceSocketMode,
			want:    []string{"emoji_changed"},
			fields:  map[string]string{"subtype": `"add"`, "value": `"alias:party"`},
		},
		{
			name:    "emoji removed",
			payload: NewPayload("emoji.removed", String("name", "party")),
			surface: SurfaceSocketMode,
			want:    []string{"emoji_changed"},
			fields:  map[string]string{"subtype": `"remove"`, "names": `["party"]`},
		},
		{
			name:    "emoji renamed",
			payload: NewPayload("emoji.renamed", String("old_name", "party"), String("new_name", "celebrate")),
			surface: SurfaceSocketMode,
			want:    []string{"emoji_changed"},
			fields:  map[string]string{"subtype": `"rename"`, "old_name": `"party"`, "new_name": `"celebrate"`},
		},
		{
			name: "usergroup created",
			payload: NewPayload("usergroup.created",
				String("usergroup_id", "S1"), String("team_id", "T1"), String("handle", "oncall"),
				JSON("subteam", `{"id":"S1","team_id":"T1","handle":"oncall","is_usergroup":true}`)),
			surface: SurfaceEventsAPI,
			want:    []string{"subteam_created"},
			fields:  map[string]string{"subteam": `"handle":"oncall"`},
		},
		{
			name: "usergroup members changed",
			payload: NewPayload("usergroup.users_changed",
				String("usergroup_id", "S1"), String("team_id", "T1"), String("handle", "oncall"),
				JSON("subteam", `{"id":"S1"}`),
				Strings("added_users", []string{"U2"}), Strings("removed_users", []string{})),
			surface: SurfaceEventsAPI,
			want:    []string{"subteam_members_changed"},
			fields:  map[string]string{"subteam_id": `"S1"`, "team_id": `"T1"`, "added_users": `["U2"]`, "removed_users": `[]`},
		},
		{
			name:    "team join",
			payload: userPayload("user.created", false),
			surface: SurfaceEventsAPI,
			want:    []string{"team_join"},
			fields:  map[string]string{"user": `"id":"U1"`},
		},
		{
			name:    "user removed reports deleted",
			payload: userPayload("user.removed", false),
			surface: SurfaceEventsAPI,
			want:    []string{"user_change"},
			fields:  map[string]string{"user": `"deleted":true`},
		},
		{
			name:    "profile change fans out to the current catalog names",
			payload: userPayload("user.profile_changed", false),
			surface: SurfaceEventsAPI,
			want:    []string{"user_change", "user_profile_changed"},
		},
		{
			name:    "status change adds user_status_changed",
			payload: userPayload("user.profile_changed", true),
			surface: SurfaceSocketMode,
			want:    []string{"user_change", "user_profile_changed", "user_status_changed"},
		},
		{
			name: "dnd snooze",
			payload: NewPayload("user.dnd_snoozed",
				String("user_id", "U1"), Bool("dnd_enabled", false), Bool("snooze_enabled", true),
				Int("snooze_endtime", 1700003600)),
			surface: SurfaceEventsAPI,
			want:    []string{"dnd_updated"},
			fields:  map[string]string{"user": `"U1"`, "dnd_status": `"snooze_enabled":true`},
		},
		{
			name:    "workspace rename",
			payload: NewPayload("workspace.name_changed", String("name", "New Name")),
			surface: SurfaceEventsAPI,
			want:    []string{"team_rename"},
			fields:  map[string]string{"name": `"New Name"`},
		},
		{
			name:    "file made public",
			payload: NewPayload("file.public_shared", String("file_id", "F1"), String("user_id", "U1")),
			surface: SurfaceEventsAPI,
			want:    []string{"file_public"},
			fields:  map[string]string{"file_id": `"F1"`, "file": `"id":"F1"`},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			event, err := New("Ev1", "T1", "U1", testCase.payload, at)
			if err != nil {
				t.Fatal(err)
			}
			delivered, err := Broadcastable(event)
			if err != nil {
				t.Fatal(err)
			}
			inners, err := SlackInner(event.Topic, delivered, testCase.surface)
			if err != nil {
				t.Fatal(err)
			}
			if len(inners) != len(testCase.want) {
				t.Fatalf("inners=%d, want %d", len(inners), len(testCase.want))
			}
			for index, want := range testCase.want {
				if inners[index].Type() != want {
					t.Fatalf("inner[%d].Type=%s, want %s", index, inners[index].Type(), want)
				}
			}
			encoded, err := inners[0].Encode()
			if err != nil {
				t.Fatal(err)
			}
			for field, fragment := range testCase.fields {
				if !strings.Contains(encoded, `"`+field+`":`) || !strings.Contains(encoded, fragment) {
					t.Fatalf("inner %s lacks %s carrying %s", encoded, field, fragment)
				}
			}
			if !strings.Contains(encoded, `"event_ts":`) {
				t.Fatalf("inner %s lacks event_ts", encoded)
			}
		})
	}
}
