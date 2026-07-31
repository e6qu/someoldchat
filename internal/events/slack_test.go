package events

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
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

func TestPretranslatedEventCannotBypassKnownTopicSurface(t *testing.T) {
	event := Event{
		ID:          "Ev1",
		WorkspaceID: "T1",
		Topic:       "user.presence_changed",
		Payload:     `{"type":"presence_change","event_ts":"1700000000.000000","user":"U1","presence":"away"}`,
		CreatedAt:   time.Unix(1700000000, 0).UTC(),
	}
	delivered, err := Deliverable(event)
	if err != nil {
		t.Fatal(err)
	}
	if inners, err := SlackInner(event.Topic, delivered, SurfaceEventsAPI); err != nil || len(inners) != 0 {
		t.Fatalf("Events API inners=%v err=%v, want RTM-only event withheld", inners, err)
	}
	inners, err := SlackInner(event.Topic, delivered, SurfaceRTM)
	if err != nil || len(inners) != 1 || inners[0].Type() != "presence_change" {
		t.Fatalf("RTM inners=%v err=%v", inners, err)
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
