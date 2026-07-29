package socketmode

import (
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

type testResponseSink struct {
	mu         sync.Mutex
	appID      domain.AppID
	envelopeID string
	payload    []byte
}

func (s *testResponseSink) HandleSocketModeResponse(_ context.Context, appID domain.AppID, envelopeID string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appID = appID
	s.envelopeID = envelopeID
	s.payload = append([]byte(nil), payload...)
	return nil
}

func (s *testResponseSink) snapshot() (domain.AppID, string, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appID, s.envelopeID, append([]byte(nil), s.payload...)
}

type observedQueue struct {
	*memory.Store
	mu    sync.Mutex
	acked []uint64
}

func (q *observedQueue) AckAppEvent(ctx context.Context, appID domain.AppID, surface, owner string, sequence uint64) error {
	if err := q.Store.AckAppEvent(ctx, appID, surface, owner, sequence); err != nil {
		return err
	}
	q.mu.Lock()
	q.acked = append(q.acked, sequence)
	q.mu.Unlock()
	return nil
}

func (q *observedQueue) acknowledged(sequence uint64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.acked) != 0 && q.acked[len(q.acked)-1] >= sequence
}

func installAppEvents(t *testing.T, s *memory.Store, records ...events.Record) {
	t.Helper()
	if err := s.CreateAppInstallation(context.Background(), domain.AppInstallation{
		AppID: "A123", WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := s.AppendEvent(context.Background(), record.Event); err != nil {
			t.Fatal(err)
		}
	}
}

func producedRecord(t *testing.T, sequence uint64, id domain.EventID, topic string, fields ...events.Field) events.Record {
	t.Helper()
	event, err := events.New(id, "T1", "U1", events.NewPayload(topic, fields...), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return events.Record{Sequence: sequence, Event: event}
}

func translatedRecord(t *testing.T, sequence uint64, id domain.EventID) events.Record {
	t.Helper()
	return producedRecord(t, sequence, id, "reaction.added",
		events.String("message_id", "M1"),
		events.String("channel_id", "C1"),
		events.String("ts", "1700000000.000000"),
		events.String("reaction", "wave"),
		events.String("user_id", "U1"),
	)
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
	// The record is built by the same typed constructor the service uses and is
	// one whose complete Slack inner shape can be derived from durable fields.
	record := translatedRecord(t, 4, "event-4")
	installAppEvents(t, connections, record)
	queue := &observedQueue{Store: connections}
	server := httptest.NewServer(Handler{Store: connections, Queue: queue, Responses: responses})
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
			Type  string `json:"type"`
			Event struct {
				Type     string `json:"type"`
				Reaction string `json:"reaction"`
				EventTS  string `json:"event_ts"`
			} `json:"event"`
		} `json:"payload"`
	}
	if err := client.ReadJSON(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.EnvelopeID != "event-4" || envelope.Type != "events_api" {
		t.Fatalf("event envelope=%+v", envelope)
	}
	if envelope.Payload.Type != "event_callback" || envelope.Payload.Event.Type != "reaction_added" || envelope.Payload.Event.Reaction != "wave" || envelope.Payload.Event.EventTS == "" {
		t.Fatalf("event payload=%+v", envelope.Payload)
	}
	if err := client.WriteJSON(map[string]any{"envelope_id": envelope.EnvelopeID, "payload": map[string]string{"ok": "true"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		appID, _, _ := responses.snapshot()
		if queue.acknowledged(1) && appID == "A123" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event lease was not acknowledged")
		}
		time.Sleep(10 * time.Millisecond)
	}
	appID, envelopeID, payload := responses.snapshot()
	if appID != "A123" || envelopeID != "event-4" || string(payload) != `{"ok":"true"}` {
		t.Fatalf("response app=%q envelope=%q payload=%s", appID, envelopeID, payload)
	}
}

func TestHandlerDeliversAndAcknowledgesAppTargetedInteraction(t *testing.T) {
	connections := memory.New()
	now := time.Now().UTC()
	response := domain.AppResponseURL{
		TokenHash: "response-hash", AppID: "A123", WorkspaceID: "T1", UserID: "U1",
		ConversationID: "C1", CreatedAt: now, ExpiresAt: now.Add(time.Minute), UsesRemaining: 5,
	}
	if err := connections.CreateAppInteractionCapabilities(context.Background(), domain.AppTrigger{
		TokenHash: "trigger-hash", AppID: "A123", WorkspaceID: "T1", UserID: "U1",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}, response); err != nil {
		t.Fatal(err)
	}
	if err := connections.CreateSocketModeInteraction(context.Background(), domain.SocketModeInteraction{
		EnvelopeID: "interaction-1", AppID: "A123", WorkspaceID: "T1", UserID: "U1",
		Type: "slash_commands", Payload: `{"command":"/deploy","text":"production"}`,
		Response: response, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	responses := new(testResponseSink)
	client := dialHandler(t, Handler{
		Store: connections, Interactions: connections, Responses: responses,
	}, connections)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	var envelope struct {
		EnvelopeID             string `json:"envelope_id"`
		Type                   string `json:"type"`
		AcceptsResponsePayload bool   `json:"accepts_response_payload"`
		Payload                struct {
			Command string `json:"command"`
			Text    string `json:"text"`
		} `json:"payload"`
	}
	if err := client.ReadJSON(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.EnvelopeID != "interaction-1" || envelope.Type != "slash_commands" ||
		!envelope.AcceptsResponsePayload || envelope.Payload.Command != "/deploy" || envelope.Payload.Text != "production" {
		t.Fatalf("interaction envelope=%+v", envelope)
	}
	if err := client.WriteJSON(map[string]any{
		"envelope_id": envelope.EnvelopeID,
		"payload":     map[string]any{"response_type": "ephemeral", "text": "deployment queued"},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		stored, err := connections.GetSocketModeInteraction(context.Background(), "A123", envelope.EnvelopeID)
		_, responseEnvelopeID, _ := responses.snapshot()
		if err == nil && !stored.AcknowledgedAt.IsZero() && responseEnvelopeID == envelope.EnvelopeID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("interaction was not durably acknowledged: stored=%+v response_envelope=%q err=%v", stored, responseEnvelopeID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	appID, responseEnvelopeID, responsePayload := responses.snapshot()
	if appID != "A123" || responseEnvelopeID != envelope.EnvelopeID || string(responsePayload) != `{"response_type":"ephemeral","text":"deployment queued"}` {
		t.Fatalf("response app=%q envelope=%q payload=%s", appID, responseEnvelopeID, responsePayload)
	}
}

func TestHandlerAdvancesFanOutOnlyAfterEveryAcknowledgement(t *testing.T) {
	connections := memory.New()
	record := producedRecord(t, 4, "event-4", "conversation.members_invited",
		events.String("channel_id", "C1"),
		events.Strings("user_ids", []string{"U2", "U3"}),
	)
	installAppEvents(t, connections, record)
	queue := &observedQueue{Store: connections}
	client := dialHandler(t, Handler{Store: connections, Queue: queue, Responses: new(testResponseSink)}, connections)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	ids := make([]string, 0, 2)
	for range 2 {
		var envelope struct {
			EnvelopeID string `json:"envelope_id"`
		}
		if err := client.ReadJSON(&envelope); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, envelope.EnvelopeID)
	}
	if ids[0] != "event-4#0" || ids[1] != "event-4#1" {
		t.Fatalf("fan-out envelope IDs=%v", ids)
	}
	if err := client.WriteJSON(map[string]string{"envelope_id": ids[0]}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if queue.acknowledged(1) {
		t.Fatal("event was acknowledged after only one fan-out envelope")
	}
	if err := client.WriteJSON(map[string]string{"envelope_id": ids[1]}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if queue.acknowledged(1) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event was not acknowledged after every fan-out envelope")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A record the app can never receive must not end the connection: the cursor
// would stay where it is, the SDK would reconnect, and the same record would
// close the connection again forever. Delivery has to step over it.
func TestHandlerSkipsUndeliverableRecordsAndKeepsDelivering(t *testing.T) {
	connections := memory.New()
	records := []events.Record{
		// An internal blob-cleanup record: its payload is an object-storage key.
		{Sequence: 4, Event: events.Event{ID: "event-4", WorkspaceID: "T1", Topic: events.UserPhotoBlobDeleteTopic, Payload: "T1/users/U1/photo_secret", CreatedAt: time.Unix(1700000000, 0)}},
		// A record written before the typed payload contract: a bare message ID.
		{Sequence: 5, Event: events.Event{ID: "event-5", WorkspaceID: "T1", Topic: "message.created", Payload: "M0123", CreatedAt: time.Unix(1700000000, 0)}},
		// A record addressed to one user, carrying that user's message text.
		producedRecord(t, 6, "event-6", events.EphemeralMessageTopic, events.String("user_id", "U1"), events.String("text", "only for U1")),
		// A pre-translated body filed under a topic with a different Slack
		// mapping. Reconnecting cannot repair the durable mismatch.
		{Sequence: 7, Event: events.Event{
			ID: "event-7", WorkspaceID: "T1", Topic: "message.created",
			Payload: `{"type":"reaction_added","event_ts":"1700000000.000000"}`, CreatedAt: time.Unix(1700000000, 0),
		}},
		translatedRecord(t, 8, "event-8"),
	}
	installAppEvents(t, connections, records...)
	queue := &observedQueue{Store: connections}
	client := dialHandler(t, Handler{Store: connections, Queue: queue, Responses: new(testResponseSink)}, connections)
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
	payload, _ := envelope.Payload["event"].(map[string]any)
	if envelope.EnvelopeID != "event-8" || envelope.Payload["type"] != "event_callback" || payload["type"] != "reaction_added" {
		t.Fatalf("envelope=%s", raw)
	}
	if !queue.acknowledged(4) {
		t.Fatal("undeliverable records were not skipped durably")
	}
}

func TestConcurrentConnectionsDeliverAnEnvelopeExactlyOnce(t *testing.T) {
	connections := memory.New()
	record := translatedRecord(t, 4, "event-4")
	installAppEvents(t, connections, record)
	queue := &observedQueue{Store: connections}
	handler := Handler{Store: connections, Queue: queue, Responses: new(testResponseSink), Logger: quietLogger()}
	first := dialHandler(t, handler, connections)
	defer first.Close()
	second := dialHandler(t, handler, connections)
	defer second.Close()

	type delivery struct {
		connection *websocket.Conn
		envelopeID string
		err        error
	}
	deliveries := make(chan delivery, 2)
	for _, client := range []*websocket.Conn{first, second} {
		go func(client *websocket.Conn) {
			_ = client.SetReadDeadline(time.Now().Add(750 * time.Millisecond))
			var envelope struct {
				EnvelopeID string `json:"envelope_id"`
			}
			err := client.ReadJSON(&envelope)
			deliveries <- delivery{connection: client, envelopeID: envelope.EnvelopeID, err: err}
		}(client)
	}
	results := []delivery{<-deliveries, <-deliveries}
	var winner delivery
	delivered := 0
	for _, result := range results {
		if result.err == nil {
			delivered++
			winner = result
		}
	}
	if delivered != 1 || winner.envelopeID != "event-4" {
		t.Fatalf("successful deliveries=%d results=%+v, want one event-4 envelope", delivered, results)
	}
	if err := winner.connection.WriteJSON(map[string]string{"envelope_id": winner.envelopeID}); err != nil {
		t.Fatal(err)
	}
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
	_, err := encodeEvent(record, "A1")
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
	record := translatedRecord(t, 4, "event-4")
	installAppEvents(t, connections, record)
	baseline := runtime.NumGoroutine()
	server := httptest.NewServer(Handler{Store: connections, Queue: connections, Responses: new(testResponseSink)})
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
		record := events.Record{Event: events.Event{ID: "event", WorkspaceID: "T1", Topic: topic, Payload: payload, CreatedAt: time.Unix(1700000000, 0).UTC()}}
		encoded, err := encodeEvent(record, "A1")
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
