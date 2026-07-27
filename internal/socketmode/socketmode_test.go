package socketmode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

type testEventSource struct {
	record events.Record
}

type testResponseSink struct {
	appID      domain.AppID
	envelopeID string
	payload    []byte
}

func (s *testResponseSink) HandleSocketModeResponse(_ context.Context, appID domain.AppID, envelopeID string, payload []byte) error {
	s.appID = appID
	s.envelopeID = envelopeID
	s.payload = append([]byte(nil), payload...)
	return nil
}

func (s testEventSource) ListAppEventsAfter(_ context.Context, _ domain.AppID, after uint64, _ int) ([]events.Record, error) {
	if s.record.Sequence <= after {
		return nil, nil
	}
	return []events.Record{s.record}, nil
}

// journalSource answers like the durable journal: the oldest record after the
// cursor, whatever that record is. The journal carries internal worker records
// and records written before the typed payload contract as well as events.
type journalSource struct {
	records []events.Record
}

func (s journalSource) ListAppEventsAfter(_ context.Context, _ domain.AppID, after uint64, _ int) ([]events.Record, error) {
	for _, record := range s.records {
		if record.Sequence > after {
			return []events.Record{record}, nil
		}
	}
	return nil, nil
}

func producedRecord(t *testing.T, sequence uint64, id domain.EventID, topic string, fields ...events.Field) events.Record {
	t.Helper()
	event, err := events.New(id, "T1", "U1", events.NewPayload(topic, fields...), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return events.Record{Sequence: sequence, Event: event}
}

func dialHandler(t *testing.T, handler Handler, connections *memory.Store) *websocket.Conn {
	t.Helper()
	service := Service{Store: connections, Host: "example.test"}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	parsed.Scheme = "ws"
	parsed.Host = strings.TrimPrefix(server.URL, "http://")
	client, _, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var hello map[string]any
	if err := client.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if hello["type"] != "hello" {
		t.Fatalf("hello=%v", hello)
	}
	return client
}

func TestOpenRequiresExplicitAppAndHost(t *testing.T) {
	service := Service{Store: memory.New(), Host: "example.test"}
	if _, err := service.Open(context.Background(), ""); err != ErrInvalidAppID {
		t.Fatalf("Open empty app ID error=%v, want %v", err, ErrInvalidAppID)
	}
	service.Host = ""
	if _, err := service.Open(context.Background(), "A123"); err == nil {
		t.Fatal("Open without public host succeeded")
	}
}

func TestConnectionIsSingleUseAndExpires(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test", TLS: true}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.URL, "wss://example.test/socket-mode?") {
		t.Fatalf("URL=%q", result.URL)
	}
	id, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := connections.ConsumeSocketModeConnection(context.Background(), id.Query().Get("connection_id"))
	if err != nil || connection.AppID != "A123" {
		t.Fatalf("connection=%+v error=%v", connection, err)
	}
	if _, err := connections.ConsumeSocketModeConnection(context.Background(), connection.ID); err != store.ErrNotFound {
		t.Fatalf("replay error=%v, want %v", err, store.ErrNotFound)
	}
}

func TestConnectionLimitAllowsReplacementAfterRelease(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test"}
	ids := make([]string, 0, domain.SocketModeConnectionLimit)
	for range domain.SocketModeConnectionLimit {
		result, err := service.Open(context.Background(), "A123")
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(result.URL)
		if err != nil {
			t.Fatal(err)
		}
		connection, err := connections.ConsumeSocketModeConnection(context.Background(), parsed.Query().Get("connection_id"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, connection.ID)
	}
	if count, err := connections.CountSocketModeConnections(context.Background(), "A123"); err != nil || count != domain.SocketModeConnectionLimit {
		t.Fatalf("active connection count=%d error=%v", count, err)
	}
	if _, err := service.Open(context.Background(), "A123"); err != ErrConnectionLimit {
		t.Fatalf("connection beyond limit error=%v, want %v", err, ErrConnectionLimit)
	}
	if err := connections.RenewSocketModeConnection(context.Background(), ids[0], time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := connections.ReleaseSocketModeConnection(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	if count, err := connections.CountSocketModeConnections(context.Background(), "A123"); err != nil || count != domain.SocketModeConnectionLimit-1 {
		t.Fatalf("active count after release=%d error=%v", count, err)
	}
	if _, err := service.Open(context.Background(), "A123"); err != nil {
		t.Fatalf("replacement connection error=%v", err)
	}
}

func TestHandlerSendsHelloAndAcknowledgesEnvelope(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test"}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Store: connections})
	defer server.Close()
	parsed.Scheme = "ws"
	parsed.Host = strings.TrimPrefix(server.URL, "http://")
	client, _, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var hello map[string]any
	if err := client.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if hello["type"] != "hello" {
		t.Fatalf("hello=%v", hello)
	}
	debug, ok := hello["debug_info"].(map[string]any)
	if !ok || debug["host"] == "" || debug["host"] == "A123" {
		t.Fatalf("debug_info=%v, want the serving host rather than the app ID", hello["debug_info"])
	}
	if err := client.WriteJSON(map[string]string{"envelope_id": "env-1", "payload": "{}"}); err != nil {
		t.Fatal(err)
	}
	var acknowledgement map[string]string
	if err := client.ReadJSON(&acknowledgement); err != nil {
		t.Fatal(err)
	}
	if acknowledgement["envelope_id"] != "env-1" {
		t.Fatalf("acknowledgement=%v", acknowledgement)
	}
	if _, err := json.Marshal(hello); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerRejectsEnvelopeWithoutID(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test"}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Store: connections})
	defer server.Close()
	parsed.Scheme = "ws"
	parsed.Host = strings.TrimPrefix(server.URL, "http://")
	client, _, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, _, err := client.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if err := client.WriteJSON(map[string]string{"payload": "{}"}); err != nil {
		t.Fatal(err)
	}
	_, _, err = client.ReadMessage()
	if err == nil {
		t.Fatal("malformed envelope did not close the connection")
	}
}

func TestHandlerDeliversEventAndAdvancesOnlyAfterAcknowledgement(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test"}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	responses := new(testResponseSink)
	// The record is built by the same typed constructor the service uses, so
	// this asserts delivery of a payload production actually emits.
	record := producedRecord(t, 4, "event-4", "message.created", events.String("message_id", "M1"), events.String("channel_id", "C1"))
	server := httptest.NewServer(Handler{Store: connections, Events: testEventSource{record: record}, Cursors: connections, Responses: responses})
	defer server.Close()
	parsed.Scheme = "ws"
	parsed.Host = strings.TrimPrefix(server.URL, "http://")
	client, _, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var hello map[string]any
	if err := client.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		EnvelopeID string `json:"envelope_id"`
		Type       string `json:"type"`
		Payload    struct {
			Type      string `json:"type"`
			MessageID string `json:"message_id"`
			EventTS   string `json:"event_ts"`
		} `json:"payload"`
	}
	if err := client.ReadJSON(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.EnvelopeID != "event-4" || envelope.Type != "events_api" {
		t.Fatalf("event envelope=%+v", envelope)
	}
	if envelope.Payload.Type != "message.created" || envelope.Payload.MessageID != "M1" || envelope.Payload.EventTS == "" {
		t.Fatalf("event payload=%+v", envelope.Payload)
	}
	if err := client.WriteJSON(map[string]any{"envelope_id": envelope.EnvelopeID, "payload": map[string]string{"ok": "true"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		cursor, cursorErr := connections.GetSocketModeCursor(context.Background(), "A123")
		if cursorErr != nil {
			t.Fatal(cursorErr)
		}
		if cursor == 4 && responses.appID == "A123" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cursor=%d, want 4", cursor)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if responses.envelopeID != "event-4" || string(responses.payload) != `{"ok":"true"}` {
		t.Fatalf("response=%+v", responses)
	}
}

// A record the app can never receive must not end the connection: the cursor
// would stay where it is, the SDK would reconnect, and the same record would
// close the connection again forever. Delivery has to step over it.
func TestHandlerSkipsUndeliverableRecordsAndKeepsDelivering(t *testing.T) {
	connections := memory.New()
	source := journalSource{records: []events.Record{
		// An internal blob-cleanup record: its payload is an object-storage key.
		{Sequence: 4, Event: events.Event{ID: "event-4", WorkspaceID: "T1", Topic: events.UserPhotoBlobDeleteTopic, Payload: "T1/users/U1/photo_secret", CreatedAt: time.Unix(1700000000, 0)}},
		// A record written before the typed payload contract: a bare message ID.
		{Sequence: 5, Event: events.Event{ID: "event-5", WorkspaceID: "T1", Topic: "message.created", Payload: "M0123", CreatedAt: time.Unix(1700000000, 0)}},
		// A record addressed to one user, carrying that user's message text.
		producedRecord(t, 6, "event-6", events.EphemeralMessageTopic, events.String("user_id", "U1"), events.String("text", "only for U1")),
		producedRecord(t, 7, "event-7", "message.created", events.String("message_id", "M6")),
	}}
	client := dialHandler(t, Handler{Store: connections, Events: source, Cursors: connections, Responses: new(testResponseSink)}, connections)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	messageType, raw, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("delivery stopped at an undeliverable record: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("message type=%d", messageType)
	}
	if strings.Contains(string(raw), "photo_secret") {
		t.Fatalf("an internal object-storage key was delivered to the app: %s", raw)
	}
	var envelope struct {
		EnvelopeID string         `json:"envelope_id"`
		Payload    map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "only for U1") {
		t.Fatalf("a message addressed to one user was delivered to an app: %s", raw)
	}
	if envelope.EnvelopeID != "event-7" || envelope.Payload["message_id"] != "M6" {
		t.Fatalf("envelope=%s", raw)
	}
	cursor, err := connections.GetSocketModeCursor(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 6 {
		t.Fatalf("cursor=%d, want the undeliverable records skipped durably", cursor)
	}
}

// Delivery from the shared per-app cursor gives every open connection of an
// application its own copy of every envelope, because the position is keyed on
// the application and every connection reads and writes the same row. Slack's
// Socket Mode contract delivers each envelope to exactly one connection, and
// domain.SocketModeConnectionLimit deliberately permits ten, so an app that
// holds connections open across a redeploy runs its handler ten times per
// event, side effects included.
//
// No store in this repository implements per-envelope ownership, so the defect
// is live. What must not be true is that it is also silent: an operator has no
// other signal that the events their app is handling are duplicates. The
// warning is emitted exactly when duplication is happening — when a second
// connection of the same application starts delivering.
func TestSharedCursorDeliveryReportsThatEveryConnectionReceivesEveryEnvelope(t *testing.T) {
	connections := memory.New()
	var lines lockedBuffer
	record := producedRecord(t, 4, "event-4", "message.created", events.String("message_id", "M1"))
	handler := Handler{Store: connections, Events: testEventSource{record: record}, Cursors: connections,
		Responses: new(testResponseSink), Logger: slog.New(slog.NewTextHandler(&lines, nil))}
	first := dialHandler(t, handler, connections)
	defer first.Close()
	second := dialHandler(t, handler, connections)
	defer second.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		logged := lines.String()
		if strings.Contains(logged, "every open connection of this app receives every envelope") {
			if !strings.Contains(logged, "A123") {
				t.Fatalf("the warning does not name the application: %s", logged)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a second connection began duplicating every envelope with no operator-visible report: %s", logged)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type lockedBuffer struct {
	mu      sync.Mutex
	written bytes.Buffer
}

func (b *lockedBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written.Write(payload)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written.String()
}

// Losing a race for an envelope is not a server fault. Closing with an internal
// error sends an SDK looking for a failure that did not happen, and #86 made
// exactly that reachable by writing the shared position from every connection.
func TestLosingAnEnvelopeRaceClosesBenignly(t *testing.T) {
	if code, _ := deliveryCloseCode(store.ErrConflict); code != websocket.CloseTryAgainLater {
		t.Fatalf("a conflicting position write closed with %d, want %d", code, websocket.CloseTryAgainLater)
	}
	if code, _ := deliveryCloseCode(store.ErrLeaseConflict); code != websocket.CloseTryAgainLater {
		t.Fatalf("a lost envelope lease closed with %d, want %d", code, websocket.CloseTryAgainLater)
	}
	if code, _ := deliveryCloseCode(errors.New("connection refused")); code != websocket.CloseInternalServerErr {
		t.Fatalf("an unavailable store closed with %d, want %d", code, websocket.CloseInternalServerErr)
	}
}

// A record with no identifier has nothing wrong with its payload: the envelope
// ID is derived from the event ID, and that is what is missing. Reporting it as
// a malformed payload made it indistinguishable from a record that must be
// dropped silently, in this handler and in the outbox worker alike.
func TestARecordWithoutAnIdentifierIsReportedAsAnIncompleteRecord(t *testing.T) {
	record := events.Record{Sequence: 1, Event: events.Event{ID: " ", WorkspaceID: "T1", Topic: "message.created", Payload: `{"type":"message.created","event_ts":"1.0"}`}}
	_, err := encodeEvent(record)
	if !errors.Is(err, events.ErrEventIncomplete) {
		t.Fatalf("error=%v, want %v", err, events.ErrEventIncomplete)
	}
	if errors.Is(err, events.ErrPayloadMalformed) {
		t.Fatalf("error=%v is still classified as a payload defect", err)
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A client that pipelines frames used to leak the reader goroutine, the
// connection and its buffers for the process lifetime: the goroutine blocked
// handing a frame to a loop that had already returned.
func TestHandlerDoesNotLeakTheReaderOnPipelinedFrames(t *testing.T) {
	connections := memory.New()
	service := Service{Store: connections, Host: "example.test"}
	result, err := service.Open(context.Background(), "A123")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	record := producedRecord(t, 4, "event-4", "message.created", events.String("message_id", "M1"))
	baseline := runtime.NumGoroutine()
	server := httptest.NewServer(Handler{Store: connections, Events: testEventSource{record: record}, Cursors: connections, Responses: new(testResponseSink)})
	parsed.Scheme = "ws"
	parsed.Host = strings.TrimPrefix(server.URL, "http://")
	client, _, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The frames name an envelope the handler never sent, so the first one ends
	// the connection while the rest are still queued behind it.
	for range 8 {
		if err := client.WriteJSON(map[string]string{"envelope_id": "unknown"}); err != nil {
			break
		}
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := client.ReadMessage(); err != nil {
			break
		}
	}
	_ = client.Close()
	server.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		count := runtime.NumGoroutine()
		if count <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines=%d, want no more than %d: the reader goroutine leaked", count, baseline)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestResponseRecorderRequiresDurableStore(t *testing.T) {
	recorder := ResponseRecorder{}
	if err := recorder.HandleSocketModeResponse(context.Background(), "A123", "env-1", []byte(`{"ok":true}`)); err == nil {
		t.Fatal("response recorder without a store succeeded")
	}
}

// The Socket Mode envelope encoder must accept exactly the records the shared
// deliverability rule accepts, and must never produce an envelope that cannot
// be marshalled. Restating the JSON rules here would only assert that the
// encoder is implemented the way it is implemented.
func FuzzEncodeEventMatchesDeliverability(f *testing.F) {
	f.Add(`{"type":"message.created","event_ts":"1700000000.000000"}`, "message.created")
	f.Add("not json", "message.created")
	f.Add("M0123", "message.created")
	f.Add("T1/users/U1/photo", events.UserPhotoBlobDeleteTopic)
	f.Fuzz(func(t *testing.T, payload, topic string) {
		record := events.Record{Event: events.Event{ID: "event", WorkspaceID: "T1", Topic: topic, Payload: payload}}
		encoded, err := encodeEvent(record)
		if _, deliverableErr := events.Deliverable(record.Event); deliverableErr != nil {
			if err == nil {
				t.Fatalf("an undeliverable record was encoded: topic=%q payload=%q", topic, payload)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := json.Marshal(encoded); err != nil {
			t.Fatal(err)
		}
	})
}

var _ http.Handler = Handler{}
