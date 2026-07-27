package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"golang.org/x/net/websocket"
)

type testSource struct {
	cancel context.CancelFunc
	calls  int
	after  []uint64
}

// producedRecord builds a record the way the service builds one, so the tests
// assert delivery of payloads production actually emits.
func producedRecord(sequence uint64, id domain.EventID, topic string, fields ...events.Field) events.Record {
	event, err := events.New(id, "T1", "U1", events.NewPayload(topic, fields...), time.Unix(1700000000, 0))
	if err != nil {
		panic(err)
	}
	return events.Record{Sequence: sequence, Event: event}
}

type emptyEventSource struct{}

func (emptyEventSource) ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error) {
	return nil, nil
}

type testRTMMessageService struct {
	workspace domain.WorkspaceID
	user      domain.UserID
	channel   domain.ConversationID
	text      string
	thread    domain.MessageTimestamp
	err       error
}

func (s *testRTMMessageService) Post(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, channel domain.ConversationID, text string, thread domain.MessageTimestamp, _ string) (domain.Message, error) {
	s.workspace, s.user, s.channel, s.text, s.thread = workspace, user, channel, text, thread
	if s.err != nil {
		return domain.Message{}, s.err
	}
	return domain.Message{Conversation: channel, Text: text, CreatedAt: time.Unix(12, 0)}, nil
}

type testRTMConnectionSource struct {
	connection domain.RTMConnection
}

func (s testRTMConnectionSource) ConsumeRTMConnection(context.Context, string) (domain.RTMConnection, error) {
	return s.connection, nil
}

func (s *testSource) ListEventsAfter(_ context.Context, _ domain.WorkspaceID, after uint64, _ int) ([]events.Record, error) {
	s.calls++
	s.after = append(s.after, after)
	if s.calls == 1 {
		return []events.Record{producedRecord(7, "evt_7", "message.created", events.String("message_id", "M7"), events.String("channel_id", "C1"))}, nil
	}
	s.cancel()
	return nil, nil
}

func TestSSEReplaysFromDurableSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &testSource{cancel: cancel}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(source, "T1", authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	if !strings.Contains(body, "id: 7\n") || !strings.Contains(body, "event: message.created\n") || !strings.Contains(body, `"message_id":"M7"`) {
		t.Fatalf("body=%q", body)
	}
	if !strings.Contains(body, "retry: ") {
		t.Fatalf("the stream advertised no reconnect interval: body=%q", body)
	}
	if len(source.after) == 0 || source.after[0] != 0 {
		t.Fatalf("first poll cursor=%v, want 0 without a Last-Event-ID", source.after)
	}
}

// The durable-replay contract is that a reconnecting client resumes after the
// cursor it reports, not from the head and not from zero.
func TestSSEResumesAfterTheReportedCursor(t *testing.T) {
	for name, prepare := range map[string]func(*http.Request){
		"Last-Event-ID header": func(r *http.Request) { r.Header.Set("Last-Event-ID", "41") },
		"query parameter":      func(r *http.Request) { r.URL.RawQuery = "last_event_id=41" },
	} {
		ctx, cancel := context.WithCancel(context.Background())
		source := &testSource{cancel: cancel}
		authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewHandler(source, "T1", authenticator)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
		request.Header.Set("Authorization", "Bearer token")
		prepare(request)
		handler.events(httptest.NewRecorder(), request)
		if len(source.after) == 0 || source.after[0] != 41 {
			t.Fatalf("%s: first poll cursor=%v, want 41", name, source.after)
		}
	}
}

// One record that cannot be delivered must not end a durable stream: the client
// reconnects with the same cursor, lands on the same record and breaks the
// stream for every user in the workspace. Undeliverable records are also never
// forwarded verbatim, which is how an internal object-storage key used to reach
// a browser.
func TestSSESkipsUndeliverableRecordsAndKeepsStreaming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &scriptedSource{cancel: cancel, records: []events.Record{
		{Sequence: 1, Event: events.Event{ID: "evt_1", WorkspaceID: "T1", Topic: events.UserPhotoBlobDeleteTopic, Payload: "T1/users/U1/photo_secret", CreatedAt: time.Unix(1, 0)}},
		{Sequence: 2, Event: events.Event{ID: "evt_2", WorkspaceID: "T1", Topic: "message.created", Payload: "M0123", CreatedAt: time.Unix(1, 0)}},
		producedRecord(3, "evt_3", events.EphemeralMessageTopic, events.String("channel_id", "C1"), events.String("text", "for somebody else")),
		producedRecord(4, "evt_4", events.EphemeralMessageTopic, events.String("channel_id", "C1"), events.String("user_id", "U1"), events.String("text", "for you")),
		producedRecord(5, "evt_5", "message.created", events.String("message_id", "M5")),
	}}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(source, "T1", authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.events(response, request)
	body := response.Body.String()
	if strings.Contains(body, "photo_secret") {
		t.Fatalf("an internal object-storage key was streamed to a browser: %q", body)
	}
	if strings.Contains(body, "data: M0123") {
		t.Fatalf("a payload that is not a self-describing object was forwarded: %q", body)
	}
	if strings.Contains(body, "for somebody else") {
		t.Fatalf("an ephemeral record with no recipient was broadcast: %q", body)
	}
	if !strings.Contains(body, "for you") {
		t.Fatalf("the recipient's own ephemeral record was withheld: %q", body)
	}
	if !strings.Contains(body, "id: 5") || !strings.Contains(body, `"message_id":"M5"`) {
		t.Fatalf("the stream stopped at an undeliverable record: %q", body)
	}
}

// A quiet stream has to keep producing bytes: nginx, AWS ALB and Cloud Run all
// sever an idle response at 60 s by default.
func TestSSESendsHeartbeatWhileIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &countingSource{}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(source, "T1", authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler.Heartbeat = time.Millisecond
	handler.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	response := &cancellingRecorder{ResponseRecorder: httptest.NewRecorder(), stop: cancel, after: ": keepalive"}
	handler.events(response, request)
	if !strings.Contains(response.Body.String(), ": keepalive") {
		t.Fatalf("an idle stream produced no heartbeat: %q", response.Body.String())
	}
}

// Sign-out, back-channel logout and administrative revocation must reach an
// open stream. Authenticating once and never re-checking leaves a revoked
// session receiving every workspace event indefinitely.
func TestSSEEndsWhenTheSessionIsRevoked(t *testing.T) {
	for name, withdrawal := range map[string]error{
		"revoked":          auth.ErrTokenRevoked,
		"account inactive": auth.ErrAccountInactive,
		"credential gone":  auth.ErrNoToken,
	} {
		source := &countingSource{}
		authenticator := &scriptedAuthenticator{principal: auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}}, allowed: 1, err: withdrawal}
		handler, err := NewHandler(source, "T1", authenticator)
		if err != nil {
			t.Fatal(err)
		}
		handler.Reauthorize = time.Nanosecond
		handler.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		request := httptest.NewRequest(http.MethodGet, "/events", nil)
		finished := make(chan struct{})
		go func() {
			handler.events(httptest.NewRecorder(), request)
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the stream outlived its own session", name)
		}
		if calls := authenticator.attempts(); calls != 2 {
			t.Fatalf("%s: authenticate calls=%d, want the stream to end on the first withdrawn credential", name, calls)
		}
	}
}

// The authenticator maps every session-store failure to the same error it uses
// for an unusable credential, and says so itself. Ending a stream on that error
// turns a one-second database failover into a full live-delivery outage: every
// open stream in the workspace ends within one re-authorization interval, logs
// that the user's session is no longer authorized, and reconnects together at
// the advertised retry delay.
func TestSSESurvivesASessionStoreThatCannotAnswer(t *testing.T) {
	source := &countingSource{}
	// Two inconclusive checks — a failover — then the store answers again.
	authenticator := &scriptedAuthenticator{
		principal:    auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}},
		allowed:      1,
		err:          auth.ErrInvalidToken,
		failures:     2,
		recoverAfter: true,
	}
	handler, err := NewHandler(source, "T1", authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler.Reauthorize = time.Nanosecond
	handler.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	finished := make(chan struct{})
	go func() {
		handler.events(httptest.NewRecorder(), request)
		close(finished)
	}()
	deadline := time.After(5 * time.Second)
	for {
		if authenticator.attempts() >= 6 {
			break
		}
		select {
		case <-finished:
			cancel()
			t.Fatalf("a session-store outage disconnected the stream after %d checks", authenticator.attempts())
		case <-deadline:
			cancel()
			t.Fatalf("the stream stopped re-checking after %d attempts", authenticator.attempts())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-finished
}

// A session that can never be confirmed must not stream forever either. The
// bound is on consecutive inconclusive checks, so a store that stays down closes
// the stream instead of leaving an unverifiable session live indefinitely.
func TestSSEEndsAfterRepeatedInconclusiveReauthorization(t *testing.T) {
	source := &countingSource{}
	authenticator := &scriptedAuthenticator{principal: auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}}, allowed: 1, err: auth.ErrInvalidToken}
	handler, err := NewHandler(source, "T1", authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler.Reauthorize = time.Nanosecond
	handler.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	finished := make(chan struct{})
	go func() {
		handler.events(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/events", nil))
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a permanently unconfirmable session streamed forever")
	}
	if calls := authenticator.attempts(); calls != 1+unresolvedReauthorizations {
		t.Fatalf("authenticate calls=%d, want %d inconclusive checks before giving up", calls, unresolvedReauthorizations)
	}
}

// The listener sets no WriteTimeout because it also serves this stream, and
// delegates the per-response deadline to this handler. Without one, a suspended
// tab that stops reading fills the socket buffer and parks the handler inside
// Write for as long as the kernel keeps the connection — one goroutine, one
// connection and one poll buffer per stalled reader, unbounded in their number —
// and the heartbeat guarantees the handler keeps writing even in a silent
// workspace, so the stall is reached with no traffic at all.
func TestSSEBoundsEveryWriteAndDropsAConsumerThatStopsReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &scriptedSource{cancel: cancel, records: []events.Record{
		producedRecord(1, "evt_1", "message.created", events.String("message_id", "M1")),
	}}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(source, "T1", authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	handler.WriteTimeout = 20 * time.Millisecond
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	// The preamble is accepted; the first record write stalls the way a full
	// socket buffer does, and the deadline is what turns that into a return.
	response := &stallingRecorder{ResponseRecorder: httptest.NewRecorder(), stallAfter: 1}
	finished := make(chan struct{})
	go func() {
		handler.events(response, request)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a consumer that stopped reading pinned the handler indefinitely")
	}
	deadlines := response.armed()
	if len(deadlines) == 0 {
		t.Fatal("the stream wrote to a client without ever arming a write deadline")
	}
	for _, deadline := range deadlines {
		if deadline.IsZero() {
			t.Fatal("a write deadline was cleared rather than set")
		}
	}
}

// http.Server.Shutdown waits for in-flight non-hijacked requests and does not
// cancel their contexts, so a stream that only ends when its client goes away
// makes every deploy take the full drain deadline and exit non-zero. The
// handler's half of that contract is this: when the request context ends, the
// stream returns promptly. The server's half is a BaseContext derived from the
// signal context, which is what makes the request context end at all.
func TestSSEReturnsPromptlyWhenItsRequestContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &countingSource{}
	authenticator, err := auth.NewStatic("token", auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{auth.ScopeChannelsHistory: {}}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(source, "T1", authenticator)
	if err != nil {
		t.Fatal(err)
	}
	handler.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer token")
	finished := make(chan struct{})
	go func() {
		handler.events(httptest.NewRecorder(), request)
		close(finished)
	}()
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * streamPollInterval):
		t.Fatal("the stream outlived its request context, so a drain would miss its deadline")
	}
}

// One filter, one set. Naming a single recipient-scoped topic here instead of
// asking the shared predicate is how a second such topic reaches every member of
// a workspace: Socket Mode and the event webhook refuse it through
// Broadcastable while this stream and RTM broadcast the recipient's own text.
func TestAddressedToWithholdsEveryRecipientScopedTopic(t *testing.T) {
	scoped := events.RecipientScopedTopics()
	if len(scoped) == 0 {
		t.Fatal("the recipient-scoped topic set is empty")
	}
	for _, topic := range scoped {
		record := producedRecord(1, "evt_1", topic, events.String("user_id", "U2"), events.String("text", "only for U2"))
		delivered, err := events.Deliverable(record.Event)
		if err != nil {
			t.Fatal(err)
		}
		if addressedTo(topic, delivered, "U1") {
			t.Fatalf("%s: a record addressed to U2 was shown to U1", topic)
		}
		if !addressedTo(topic, delivered, "U2") {
			t.Fatalf("%s: a record addressed to U2 was withheld from U2", topic)
		}
	}
	ordinary := producedRecord(2, "evt_2", "message.created", events.String("message_id", "M1"))
	delivered, err := events.Deliverable(ordinary.Event)
	if err != nil {
		t.Fatal(err)
	}
	if !addressedTo("message.created", delivered, "U1") {
		t.Fatal("a broadcast record was withheld")
	}
}

// scriptedSource returns every record once, then ends the stream.
type scriptedSource struct {
	records []events.Record
	cancel  context.CancelFunc
	served  bool
}

func (s *scriptedSource) ListEventsAfter(_ context.Context, _ domain.WorkspaceID, after uint64, _ int) ([]events.Record, error) {
	if s.served {
		s.cancel()
		return nil, nil
	}
	s.served = true
	result := make([]events.Record, 0, len(s.records))
	for _, record := range s.records {
		if record.Sequence > after {
			result = append(result, record)
		}
	}
	return result, nil
}

type countingSource struct {
	calls int
}

func (s *countingSource) ListEventsAfter(context.Context, domain.WorkspaceID, uint64, int) ([]events.Record, error) {
	s.calls++
	return nil, nil
}

// cancellingRecorder ends the request as soon as the stream writes the text the
// test is waiting for, so an unbounded loop cannot hang the suite.
type cancellingRecorder struct {
	*httptest.ResponseRecorder
	stop  context.CancelFunc
	after string
}

func (r *cancellingRecorder) Write(payload []byte) (int, error) {
	count, err := r.ResponseRecorder.Write(payload)
	if strings.Contains(r.ResponseRecorder.Body.String(), r.after) {
		r.stop()
	}
	return count, err
}

// scriptedAuthenticator answers with a principal for the first allowed calls,
// then with err. failures bounds how many times err is returned before the
// principal comes back, which is what a store failover looks like from here.
type scriptedAuthenticator struct {
	principal    auth.Principal
	allowed      int
	err          error
	failures     int
	recoverAfter bool

	mu    sync.Mutex
	calls int
}

func (a *scriptedAuthenticator) Authenticate(*http.Request) (auth.Principal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.calls <= a.allowed {
		return a.principal, nil
	}
	if a.recoverAfter && a.calls > a.allowed+a.failures {
		return a.principal, nil
	}
	return auth.Principal{}, a.err
}

func (a *scriptedAuthenticator) attempts() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// stallingRecorder accepts stallAfter writes and then behaves like a socket
// whose send buffer is full and whose write deadline has passed.
type stallingRecorder struct {
	*httptest.ResponseRecorder
	stallAfter int

	mu        sync.Mutex
	writes    int
	deadlines []time.Time
}

func (r *stallingRecorder) SetWriteDeadline(deadline time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

func (r *stallingRecorder) Write(payload []byte) (int, error) {
	r.mu.Lock()
	r.writes++
	stalled := r.writes > r.stallAfter
	r.mu.Unlock()
	if stalled {
		return 0, os.ErrDeadlineExceeded
	}
	return r.ResponseRecorder.Write(payload)
}

func (r *stallingRecorder) armed() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.deadlines...)
}

func TestRTMPongPreservesReplyIDAndScalarFields(t *testing.T) {
	payload, err := encodeRTMPong(`{"id":1234,"type":"ping","time":1403299273342,"label":"probe","enabled":true}`)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	if value["type"] != "pong" || value["reply_to"] != float64(1234) || value["time"] != float64(1403299273342) || value["label"] != "probe" || value["enabled"] != true {
		t.Fatalf("pong=%s", payload)
	}
}

func TestRTMPongRejectsInvalidIDsAndNestedFields(t *testing.T) {
	for _, message := range []string{
		`{"id":0,"type":"ping"}`,
		`{"id":-1,"type":"ping"}`,
		`{"id":1.5,"type":"ping"}`,
		`{"id":1,"type":"ping","nested":{"value":true}}`,
		`{"id":1,"type":"ping","list":[1,2]}`,
	} {
		if _, err := encodeRTMPong(message); err == nil {
			t.Fatalf("encodeRTMPong(%s) succeeded, want error", message)
		}
	}
}

func TestRTMMessagePostsThroughServiceAndCorrelatesReply(t *testing.T) {
	service := &testRTMMessageService{}
	connection := domain.RTMConnection{WorkspaceID: "T1", UserID: "U1"}
	payload, err := encodeRTMMessage(context.Background(), connection, service, `{"id":7,"type":"message","channel":"C1","text":" hello ","thread_ts":"12.3"}`)
	if err != nil {
		t.Fatal(err)
	}
	if service.workspace != "T1" || service.user != "U1" || service.channel != "C1" || service.text != " hello " || service.thread != "12.3" {
		t.Fatalf("service call=%+v", service)
	}
	var response map[string]any
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if response["ok"] != true || response["reply_to"] != float64(7) || response["text"] != " hello " || response["ts"] != "12.000000" {
		t.Fatalf("response=%s", payload)
	}
}

func TestRTMMessageRejectsInvalidCommands(t *testing.T) {
	service := &testRTMMessageService{}
	connection := domain.RTMConnection{WorkspaceID: "T1", UserID: "U1"}
	for _, raw := range []string{
		`{"type":"message"}`,
		`{"id":1,"type":"message","channel":"","text":"hello"}`,
		`{"id":1,"type":"message","channel":"C1","text":""}`,
	} {
		if _, err := encodeRTMMessage(context.Background(), connection, service, raw); err == nil {
			t.Fatalf("encodeRTMMessage(%s) succeeded, want error", raw)
		}
	}
}

func TestRTMWebSocketDispatchesMessageAndCorrelatesReply(t *testing.T) {
	service := &testRTMMessageService{}
	handler, err := NewRTMHandler(emptyEventSource{}, "T1", testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, service)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.RegisterRTM(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/rtm?session_id=session-1"
	config, err := websocket.NewConfig(websocketURL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	var hello map[string]any
	if err := websocket.JSON.Receive(connection, &hello); err != nil {
		t.Fatal(err)
	}
	if hello["type"] != "hello" {
		t.Fatalf("hello=%v", hello)
	}
	if err := websocket.Message.Send(connection, `{"id":9,"type":"message","channel":"C1","text":"hello"}`); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := websocket.JSON.Receive(connection, &response); err != nil {
		t.Fatal(err)
	}
	if response["ok"] != true || response["reply_to"] != float64(9) || response["ts"] != "12.000000" || response["text"] != "hello" {
		t.Fatalf("response=%v", response)
	}
	if service.workspace != "T1" || service.user != "U1" || service.channel != "C1" || service.text != "hello" {
		t.Fatalf("service=%+v", service)
	}
}

func TestRTMWebSocketAcceptsNonBrowserClientHandshake(t *testing.T) {
	handler, err := NewRTMHandler(emptyEventSource{}, "T1", testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, &testRTMMessageService{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.RegisterRTM(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/rtm?session_id=session-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	config.Origin = &url.URL{Scheme: "http", Host: "different.example"}
	conn, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var hello map[string]string
	if err := websocket.JSON.Receive(conn, &hello); err != nil {
		t.Fatal(err)
	}
	if hello["type"] != "hello" {
		t.Fatalf("hello=%v", hello)
	}
}

func TestRTMWebSocketCorrelatesMessageFailure(t *testing.T) {
	service := &testRTMMessageService{err: errors.New("store unavailable")}
	handler, err := NewRTMHandler(emptyEventSource{}, "T1", testRTMConnectionSource{connection: domain.RTMConnection{ID: "session-1", WorkspaceID: "T1", UserID: "U1"}}, service)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.RegisterRTM(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/rtm?session_id=session-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var hello map[string]any
	if err := websocket.JSON.Receive(connection, &hello); err != nil {
		t.Fatal(err)
	}
	if err := websocket.Message.Send(connection, `{"id":11,"type":"message","channel":"C1","text":"hello"}`); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := websocket.JSON.Receive(connection, &response); err != nil {
		t.Fatal(err)
	}
	errorValue, ok := response["error"].(map[string]any)
	if !ok || response["ok"] != false || response["reply_to"] != float64(11) || errorValue["code"] != float64(2) {
		t.Fatalf("response=%v", response)
	}
}

func TestRTMMessageLimitMatchesSlackProtocol(t *testing.T) {
	if maxRTMMessageBytes != 16<<10 {
		t.Fatalf("RTM message limit=%d, want %d", maxRTMMessageBytes, 16<<10)
	}
}
