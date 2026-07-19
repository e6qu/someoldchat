package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSlackEventBodyWrapsInnerEvent(t *testing.T) {
	body, err := SlackEventBody(Record{Sequence: 9, Event: Event{
		ID: "Ev1", WorkspaceID: "T1", Topic: "message.created", CreatedAt: time.Unix(1700000000, 0).UTC(),
		Payload: `{"type":"message","event_ts":"1700000000.000000","channel":"C1","text":"hello"}`,
	}}, "A1")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(envelope["event"], &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "message" || string(envelope["api_app_id"]) != `"A1"` || string(envelope["event_id"]) != `"Ev1"` {
		t.Fatalf("envelope=%s", body)
	}
}

func TestSlackEventBodyPreservesAndValidatesEnvelope(t *testing.T) {
	payload := `{"type":"event_callback","team_id":"T1","api_app_id":"A1","event_id":"Ev1","event_time":1700000000,"event":{"type":"message","event_ts":"1700000000.000000"}}`
	body, err := SlackEventBody(Record{Event: Event{ID: "Ev1", WorkspaceID: "T1", CreatedAt: time.Unix(1700000000, 0).UTC(), Payload: payload}}, "A1")
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(payload), &want); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got, want) {
		t.Fatalf("body=%s want=%s", body, payload)
	}
	if _, err := SlackEventBody(Record{Event: Event{ID: "Ev1", WorkspaceID: "T1", CreatedAt: time.Now().UTC(), Payload: payload}}, "A2"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched app ID error=%v", err)
	}
}

func jsonEqual(left, right map[string]any) bool {
	leftBody, _ := json.Marshal(left)
	rightBody, _ := json.Marshal(right)
	return string(leftBody) == string(rightBody)
}

func TestSlackEventBodyRejectsIdentifierOnlyPayload(t *testing.T) {
	_, err := SlackEventBody(Record{Event: Event{ID: "Ev1", WorkspaceID: "T1", CreatedAt: time.Now().UTC(), Payload: "M1"}}, "A1")
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("identifier-only payload error=%v", err)
	}
}

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
