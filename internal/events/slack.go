package events

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SlackEventBody converts a durable event record into the JSON envelope used
// by Slack's HTTP Events API. The event payload is deliberately required to
// contain the inner event object; an identifier-only domain event cannot be
// delivered as a compatible event and therefore fails loudly.
func SlackEventBody(record Record, appID string) ([]byte, error) {
	if record.Event.ID == "" || record.Event.WorkspaceID == "" || record.Event.CreatedAt.IsZero() {
		return nil, errors.New("Slack event record is incomplete")
	}
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, errors.New("Slack event app ID is required")
	}
	payload := bytes.TrimSpace([]byte(record.Event.Payload))
	if len(payload) == 0 || payload[0] != '{' {
		return nil, errors.New("Slack event payload must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, fmt.Errorf("decode Slack event payload: %w", err)
	}
	if object == nil {
		return nil, errors.New("Slack event payload must be a JSON object")
	}
	if rawType, ok := object["type"]; ok {
		var eventType string
		if err := json.Unmarshal(rawType, &eventType); err != nil {
			return nil, errors.New("Slack event type must be a string")
		}
		if eventType == "event_callback" {
			return validateSlackEventEnvelope(object, appID, record)
		}
	}
	if _, ok := object["event_ts"]; !ok {
		return nil, errors.New("Slack inner event requires event_ts")
	}
	if _, ok := object["type"]; !ok {
		return nil, errors.New("Slack inner event requires type")
	}
	envelope := map[string]any{
		"type":       "event_callback",
		"team_id":    string(record.Event.WorkspaceID),
		"api_app_id": appID,
		"event_id":   string(record.Event.ID),
		"event_time": record.Event.CreatedAt.Unix(),
		"event":      json.RawMessage(payload),
	}
	return json.Marshal(envelope)
}

func validateSlackEventEnvelope(object map[string]json.RawMessage, appID string, record Record) ([]byte, error) {
	for _, name := range []string{"team_id", "api_app_id", "event_id", "event_time", "event"} {
		if _, ok := object[name]; !ok {
			return nil, fmt.Errorf("Slack event envelope requires %s", name)
		}
	}
	var actualAppID string
	if err := json.Unmarshal(object["api_app_id"], &actualAppID); err != nil || strings.TrimSpace(actualAppID) == "" {
		return nil, errors.New("Slack event api_app_id must be a non-empty string")
	}
	if actualAppID != appID {
		return nil, fmt.Errorf("Slack event app ID %q does not match configured app ID %q", actualAppID, appID)
	}
	var teamID, eventID string
	if err := json.Unmarshal(object["team_id"], &teamID); err != nil || teamID != string(record.Event.WorkspaceID) {
		return nil, errors.New("Slack event team_id does not match the durable record workspace")
	}
	if err := json.Unmarshal(object["event_id"], &eventID); err != nil || eventID != string(record.Event.ID) {
		return nil, errors.New("Slack event event_id does not match the durable record")
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal(object["event"], &event); err != nil || event == nil {
		return nil, errors.New("Slack event envelope event must be an object")
	}
	if _, ok := event["type"]; !ok {
		return nil, errors.New("Slack inner event requires type")
	}
	if _, ok := event["event_ts"]; !ok {
		return nil, errors.New("Slack inner event requires event_ts")
	}
	return json.Marshal(object)
}

// SlackSignature returns the request signature specified by Slack's signing
// secret protocol for body and timestamp.
func SlackSignature(signingSecret string, timestamp time.Time, body []byte) (string, error) {
	signingSecret = strings.TrimSpace(signingSecret)
	if signingSecret == "" {
		return "", errors.New("Slack signing secret is required")
	}
	if timestamp.IsZero() {
		return "", errors.New("Slack signature timestamp is required")
	}
	base := "v0:" + strconv.FormatInt(timestamp.Unix(), 10) + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = mac.Write([]byte(base))
	return "v0=" + hex.EncodeToString(mac.Sum(nil)), nil
}
