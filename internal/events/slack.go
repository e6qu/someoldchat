package events

import (
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
// by Slack's HTTP Events API. The payload is read through Broadcastable, so an
// internal worker record, a record addressed to a single user, and a record that
// predates the typed payload contract all fail with a sentinel the caller can
// classify as permanent instead of being retried forever.
func SlackEventBody(record Record, appID string) ([]byte, error) {
	if record.Event.ID == "" || record.Event.WorkspaceID == "" || record.Event.CreatedAt.IsZero() {
		return nil, fmt.Errorf("%w: Slack event record is incomplete", ErrEventIncomplete)
	}
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, errors.New("Slack event app ID is required")
	}
	delivered, err := Broadcastable(record.Event)
	if err != nil {
		return nil, err
	}
	if _, ok := delivered.Object["event_ts"]; !ok {
		return nil, fmt.Errorf("%w: Slack inner event requires event_ts", ErrPayloadMalformed)
	}
	encoded, err := delivered.Encode()
	if err != nil {
		// Every failure reachable from here describes the record rather than the
		// destination, so every one of them carries a sentinel the worker
		// classifies as permanent. An unsentinelled error would be classified
		// retryable and re-claimed every lease period forever, which is the
		// unbounded retry loop the typed payload contract exists to remove.
		return nil, fmt.Errorf("%w: Slack event payload cannot be re-encoded: %v", ErrPayloadMalformed, err)
	}
	envelope := map[string]any{
		"type":       "event_callback",
		"team_id":    string(record.Event.WorkspaceID),
		"api_app_id": appID,
		"event_id":   string(record.Event.ID),
		"event_time": record.Event.CreatedAt.Unix(),
		"event":      json.RawMessage(encoded),
	}
	return json.Marshal(envelope)
}

// SlackSignature returns the request signature specified by Slack's signing
// secret protocol for body and timestamp.
//
// The signature covers the exact bytes of the request body. A caller must sign
// the body it sends rather than a re-encoding of it: a second json.Marshal of
// the same document can differ in key order or spacing, and the receiver
// recomputes the MAC over the bytes it received, so a re-encode fails at every
// real receiver while passing any test that re-encodes the same way.
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
