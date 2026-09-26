package socketmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

const (
	connectionLifetime = 30 * time.Second
	// connectionRefreshAge is how long one WebSocket serves before the handler
	// asks the client to reconnect with Slack's disconnect frame
	// ({"type":"disconnect","reason":"refresh_requested"}). Slack refreshes
	// Socket Mode connections on the order of half an hour; official SDKs
	// treat the frame as an orderly handoff and reconnect without backoff,
	// which is what lets a deployment drain a replica without dropping
	// deliveries on the floor.
	connectionRefreshAge = 30 * time.Minute
	maxEnvelopeBytes     = 1 << 20
	// Slack's Socket Mode contract keeps a connection alive with ping/pong. A
	// peer that disappears without a TCP FIN is otherwise undetectable, and the
	// connection plus its reader goroutine and its connection-limit slot stay
	// pinned until the operating system gives up on the socket.
	pingPeriod = 10 * time.Second
	// readTimeout allows one missed ping before the peer is declared gone.
	readTimeout = 2 * pingPeriod
	// writeTimeout bounds every write, so a peer that stops reading cannot
	// block the handler once the send buffer fills.
	writeTimeout = 10 * time.Second
	// envelopeTimeout bounds how long delivery waits for an app to acknowledge
	// an envelope before the envelope is returned to the queue for a retry.
	// Waiting forever stalls the app's whole event stream with no error
	// anywhere.
	envelopeTimeout = 30 * time.Second
	// pollInterval is how often an idle connection looks for new work. It is a
	// latency bound for work that arrives while the connection is idle, not a
	// throughput bound: after every acknowledgement the next envelope is
	// claimed at once.
	pollInterval = 100 * time.Millisecond
	// maxInFlightInteractions bounds how many interaction envelopes one
	// connection holds unacknowledged at once. Slack delivers envelopes
	// concurrently; one slow interaction must not hold up the next.
	maxInFlightInteractions = 10
	// maxDeliveryRetries is how many times an envelope is re-sent after its
	// first delivery went unacknowledged. Slack retries an event three times
	// and then drops it; retrying for ever pinned an app's whole stream behind
	// one envelope it would never acknowledge.
	maxDeliveryRetries = 3
	// buildNumber identifies this server's Socket Mode protocol revision in the
	// hello frame's debug_info, where Slack reports its own build.
	buildNumber = 1
	// ConnectionLimitRetryAfter is the Retry-After apps.connections.open
	// answers while an app holds all of its connections. A slot held by a
	// vanished peer frees when its lease lapses, within one lifetime.
	ConnectionLimitRetryAfter = connectionLifetime
)

// retryDelays is Slack's published Events API retry schedule — immediately,
// after one minute, after five minutes — indexed by retry number minus one.
var retryDelays = [maxDeliveryRetries]time.Duration{0, time.Minute, 5 * time.Minute}

var ErrInvalidAppID = errors.New("Socket Mode app ID is required")

// ErrConnectionLimit is the store's sentinel, so a limit reached in the store
// and one found by the pre-check are the same error to every caller. They used
// to be two sentinels, and the transport, which classified only the store's,
// answered the pre-check's with fatal_error.
var ErrConnectionLimit = store.ErrSocketModeConnectionLimit

type ConnectionStore interface {
	CreateSocketModeConnection(context.Context, domain.SocketModeConnection) error
	ConsumeSocketModeConnection(context.Context, string) (domain.SocketModeConnection, error)
	RenewSocketModeConnection(context.Context, string, time.Time) error
	ReleaseSocketModeConnection(context.Context, string) error
	CountSocketModeConnections(context.Context, domain.AppID) (int, error)
}

type EventQueue interface {
	ClaimAppEvent(context.Context, domain.AppID, string, string, time.Duration) (events.Record, int, string, bool, error)
	AckAppEvent(context.Context, domain.AppID, string, string, uint64) error
	ReleaseAppEvent(context.Context, domain.AppID, string, string, uint64, string, time.Time) error
}

type InteractionQueue interface {
	ClaimSocketModeInteraction(context.Context, domain.AppID, string, time.Duration) (domain.SocketModeInteraction, bool, error)
	AckSocketModeInteraction(context.Context, domain.AppID, string, string) error
	ReleaseSocketModeInteraction(context.Context, domain.AppID, string, string, string, time.Time) error
}

type ResponseSink interface {
	HandleSocketModeResponse(context.Context, domain.AppID, string, []byte) error
}

// Service issues Socket Mode connection tickets.
type Service struct {
	Store ConnectionStore
	// Host, when set, is the public host every connection URL names, and TLS
	// then selects wss:// over ws://. When Host is empty the URL follows the
	// origin the client reached apps.connections.open on, and TLS only forces
	// wss:// for a proxy that terminates TLS without saying so.
	Host string
	TLS  bool
}

type OpenResult struct {
	URL string
}

// Open issues a single-use connection ticket for appID. origin is the
// scheme://host the client called apps.connections.open on.
func (s Service) Open(ctx context.Context, appID domain.AppID, origin string) (OpenResult, error) {
	if s.Store == nil {
		return OpenResult{}, errors.New("Socket Mode requires a connection store")
	}
	if strings.TrimSpace(string(appID)) == "" {
		return OpenResult{}, ErrInvalidAppID
	}
	base, err := s.connectionOrigin(origin)
	if err != nil {
		return OpenResult{}, err
	}
	active, err := s.Store.CountSocketModeConnections(ctx, appID)
	if err != nil {
		return OpenResult{}, err
	}
	if active >= domain.SocketModeConnectionLimit {
		return OpenResult{}, ErrConnectionLimit
	}
	id, err := domain.NewSocketModeConnectionID()
	if err != nil {
		return OpenResult{}, err
	}
	connection := domain.SocketModeConnection{ID: id, AppID: appID, ExpiresAt: time.Now().UTC().Add(connectionLifetime)}
	if err := s.Store.CreateSocketModeConnection(ctx, connection); err != nil {
		return OpenResult{}, err
	}
	address, err := WebSocketURL(base, "/socket-mode", url.Values{"connection_id": []string{id}})
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{URL: address}, nil
}

func (s Service) connectionOrigin(origin string) (string, error) {
	if host := strings.TrimSpace(s.Host); host != "" {
		if s.TLS {
			return "https://" + host, nil
		}
		return "http://" + host, nil
	}
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("Socket Mode requires a public host or the request origin")
	}
	if s.TLS {
		parsed.Scheme = "https"
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// WebSocketURL maps an HTTP origin onto the WebSocket URL served at path on
// the same host: http becomes ws and https becomes wss. Both Socket Mode and
// RTM hand a client such a URL, and deriving it from the transport's own TLS
// state instead of the origin gave a client behind a TLS-terminating proxy a
// ws:// URL the proxy does not serve.
func WebSocketURL(origin, path string, query url.Values) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("WebSocket origin %q is not an absolute URL", origin)
	}
	scheme := ""
	switch parsed.Scheme {
	case "http":
		scheme = "ws"
	case "https":
		scheme = "wss"
	default:
		return "", fmt.Errorf("WebSocket origin %q is not http or https", origin)
	}
	return (&url.URL{Scheme: scheme, Host: parsed.Host, Path: path, RawQuery: query.Encode()}).String(), nil
}

type Handler struct {
	Store ConnectionStore
	// Queue leases each durable record to one connection. An application may
	// keep up to ten connections open, but Slack delivers each Socket Mode
	// envelope to only one of them.
	Queue        EventQueue
	Interactions InteractionQueue
	Responses    ResponseSink
	// Upgrader upgrades the connection. Its CheckOrigin, when nil, accepts
	// every origin: the single-use ticket is the credential, and a browser's
	// same-origin rule has nothing to protect on a socket no cookie opens. The
	// default same-origin check refused official SDK clients behind any proxy
	// that rewrites the Host header, with 403.
	Upgrader websocket.Upgrader
	// RefreshAge overrides connectionRefreshAge, the horizon at which one
	// WebSocket is handed off with a refresh_requested disconnect frame. Zero
	// selects the default; only tests shorten it.
	RefreshAge time.Duration
	// EnvelopeTimeout overrides envelopeTimeout. Zero selects the default;
	// only tests shorten it.
	EnvelopeTimeout time.Duration
	// Logger records connection-level failures that are handled rather than
	// returned to a caller: a released connection slot that could not be
	// released, and a durable record that can never be delivered to an app.
	// Both are invisible without it.
	Logger *slog.Logger
}

func (h Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func (h Handler) envelopeTimeout() time.Duration {
	if h.EnvelopeTimeout > 0 {
		return h.EnvelopeTimeout
	}
	return envelopeTimeout
}

// upgradable reports whether the upgrader can accept r. It runs before the
// ticket is consumed: a ticket spent on a request that then fails the
// handshake leaves the client nothing to connect with but a fresh
// apps.connections.open.
func upgradable(r *http.Request) bool {
	return r.Method == http.MethodGet && websocket.IsWebSocketUpgrade(r) &&
		r.Header.Get("Sec-Websocket-Version") == "13" && strings.TrimSpace(r.Header.Get("Sec-Websocket-Key")) != ""
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("connection_id"))
	if id == "" {
		http.Error(w, "connection_id is required", http.StatusBadRequest)
		return
	}
	if h.Store == nil {
		http.Error(w, "Socket Mode is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !upgradable(r) {
		http.Error(w, "Socket Mode requires a WebSocket upgrade", http.StatusBadRequest)
		return
	}
	connection, err := h.Store.ConsumeSocketModeConnection(r.Context(), id)
	if err != nil {
		// At the limit the ticket is perfectly valid; the app is simply holding
		// as many connections as it may. Answering 401 would send a client off
		// to re-authenticate instead of releasing a connection and retrying.
		if errors.Is(err, store.ErrSocketModeConnectionLimit) {
			http.Error(w, "Socket Mode connection limit reached", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "connection is invalid or expired", http.StatusUnauthorized)
		return
	}
	defer func() {
		if releaseErr := h.Store.ReleaseSocketModeConnection(context.Background(), connection.ID); releaseErr != nil {
			h.logger().Error("Socket Mode connection slot was not released", "connection", connection.ID, "app", connection.AppID, "error", releaseErr)
		}
	}()
	upgrader := h.Upgrader
	if upgrader.CheckOrigin == nil {
		upgrader.CheckOrigin = func(*http.Request) bool { return true }
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(maxEnvelopeBytes)
	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})
	writeJSON := func(value any) error {
		if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			return err
		}
		return conn.WriteJSON(value)
	}
	closeWith := func(code int, reason string) {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(writeTimeout))
	}
	// disconnectWith announces the close in Slack's own vocabulary before the
	// WebSocket close frame: {"type":"disconnect","reason":…}. A bare close
	// reads as a failure to an official SDK; the frame makes it an instruction.
	// "warning" is server trouble the client should reconnect through, and
	// "refresh_requested" is the routine connection handoff.
	disconnectWith := func(reason string, code int, detail string) {
		_ = writeJSON(map[string]any{"type": "disconnect", "reason": reason, "debug_info": map[string]string{"host": servingHost(r)}})
		closeWith(code, detail)
	}
	logger := h.logger().With("app", connection.AppID, "connection", connection.ID)
	timeout := h.envelopeTimeout()
	delivery := h.newDelivery(connection, logger, timeout)
	if delivery != nil {
		defer func() {
			if releaseErr := delivery.settle(context.Background(), "connection_closed", time.Now().UTC()); releaseErr != nil {
				logger.Error("Socket Mode event lease was not released", "error", releaseErr)
			}
		}()
	}
	interactionDelivery := h.newInteractionDelivery(connection, logger, timeout)
	if interactionDelivery != nil {
		defer func() {
			if releaseErr := interactionDelivery.settleAll(context.Background(), "connection_closed", time.Now().UTC()); releaseErr != nil {
				logger.Error("Socket Mode interaction lease was not released", "error", releaseErr)
			}
		}()
	}
	connectionCount, err := h.Store.CountSocketModeConnections(r.Context(), connection.AppID)
	if err != nil {
		disconnectWith("warning", websocket.CloseInternalServerErr, "connection state unavailable")
		return
	}
	refreshAge := h.RefreshAge
	if refreshAge <= 0 {
		refreshAge = connectionRefreshAge
	}
	// debug_info.host identifies the host serving the connection. Reporting the
	// app ID there makes SDK diagnostics and reconnect logs describe the client
	// instead of the replica the client is talking to. connection_info.app_id
	// and the build and connection-time hints complete Slack's hello shape.
	if err := writeJSON(map[string]any{
		"type":            "hello",
		"num_connections": connectionCount,
		"connection_info": map[string]string{"app_id": string(connection.AppID)},
		"debug_info": map[string]any{
			"host":                        servingHost(r),
			"build_number":                buildNumber,
			"approximate_connection_time": int(refreshAge / time.Second),
		},
	}); err != nil {
		return
	}
	// done is closed before the connection is closed, so the reader goroutine
	// is woken whether it is blocked in ReadMessage or blocked handing a
	// pipelined frame to this loop. Without it a client that pipelines frames
	// leaks the goroutine, the connection and its buffers for the process
	// lifetime.
	done := make(chan struct{})
	defer close(done)
	readErrors := make(chan error, 1)
	readMessages := make(chan []byte, 1)
	go func() {
		for {
			messageType, payload, readErr := conn.ReadMessage()
			if readErr != nil {
				select {
				case readErrors <- readErr:
				case <-done:
				}
				return
			}
			if messageType != websocket.TextMessage {
				select {
				case readErrors <- errors.New("Socket Mode requires text messages"):
				case <-done:
				}
				return
			}
			select {
			case readMessages <- payload:
			case <-done:
				return
			}
		}
	}()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	leaseTicker := time.NewTicker(connectionLifetime / 3)
	defer leaseTicker.Stop()
	pingTicker := time.NewTicker(pingPeriod)
	defer pingTicker.Stop()
	refreshTimer := time.NewTimer(refreshAge)
	defer refreshTimer.Stop()
	// fill sends as much work as this connection may hold: interaction
	// envelopes up to the in-flight bound, and the app's next event. It runs
	// on every poll and again right after every acknowledgement, so a
	// responsive app is never paced by the poll interval. It reports false
	// once the connection is closed.
	fill := func() bool {
		if interactionDelivery != nil {
			for interactionDelivery.count() < maxInFlightInteractions {
				interaction, ok, err := interactionDelivery.next(r.Context())
				if err != nil {
					disconnectWith("warning", websocket.CloseInternalServerErr, "interaction source unavailable")
					return false
				}
				if !ok {
					break
				}
				var payload json.RawMessage = []byte(interaction.Payload)
				if err := writeJSON(map[string]any{
					"envelope_id": interaction.EnvelopeID, "type": interaction.Type,
					"payload": payload, "accepts_response_payload": true,
				}); err != nil {
					return false
				}
			}
		}
		for delivery != nil && !delivery.busy() {
			record, ok, err := delivery.next(r.Context())
			if err != nil {
				disconnectWith("warning", websocket.CloseInternalServerErr, "event source unavailable")
				return false
			}
			if !ok {
				break
			}
			envelopes, err := encodeEvent(record, connection.AppID)
			if err != nil || len(envelopes) == 0 {
				if !skippable(logger, record, err) {
					disconnectWith("warning", websocket.CloseInternalServerErr, "event payload is invalid")
					return false
				}
				if consumeErr := delivery.skip(r.Context()); consumeErr != nil {
					if !leaseLost(consumeErr) {
						disconnectWith("warning", websocket.CloseInternalServerErr, "event delivery state unavailable")
						return false
					}
					logger.Warn("Socket Mode lost the lease on a skipped event", "sequence", record.Sequence, "error", consumeErr)
				}
				continue
			}
			ids := make([]string, 0, len(envelopes))
			for _, envelope := range envelopes {
				// retry_attempt and retry_reason are what official SDKs hand a
				// listener to recognise a redelivery; Bolt exposes them as
				// retryNum and retryReason. A first delivery carries 0 and "".
				envelope.Frame["retry_attempt"] = delivery.attempt
				envelope.Frame["retry_reason"] = slackRetryReason(delivery.attempt, delivery.reason)
				if err := writeJSON(envelope.Frame); err != nil {
					return false
				}
				ids = append(ids, envelope.ID)
			}
			delivery.sent(ids, time.Now())
		}
		return true
	}
	if !fill() {
		return
	}
	for {
		select {
		case err := <-readErrors:
			closeWith(websocket.CloseProtocolError, err.Error())
			return
		case payload := <-readMessages:
			var envelope struct {
				EnvelopeID string          `json:"envelope_id"`
				Payload    json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil || strings.TrimSpace(envelope.EnvelopeID) == "" {
				closeWith(websocket.CloseProtocolError, "envelope_id is required")
				return
			}
			if delivery == nil && interactionDelivery == nil {
				if err := writeJSON(map[string]string{"envelope_id": envelope.EnvelopeID}); err != nil {
					return
				}
				continue
			}
			interaction := interactionDelivery != nil && interactionDelivery.holds(envelope.EnvelopeID)
			if !interaction && (delivery == nil || !delivery.holds(envelope.EnvelopeID)) {
				// A late acknowledgement for an envelope that timed out and was
				// returned to the queue, or a duplicate one, is not a protocol
				// violation: Slack ignores it, and official SDKs acknowledge
				// again on reconnect. Closing the connection over it turned one
				// slow listener into a reconnect storm.
				logger.Debug("Socket Mode ignored an acknowledgement for an envelope this connection does not hold", "envelope", envelope.EnvelopeID)
				continue
			}
			if !emptyPayload(envelope.Payload) {
				var responsePayload map[string]json.RawMessage
				if json.Unmarshal(envelope.Payload, &responsePayload) != nil || responsePayload == nil {
					closeWith(websocket.CloseProtocolError, "response payload must be a JSON object")
					return
				}
				if h.Responses == nil {
					closeWith(websocket.ClosePolicyViolation, "response payload routing is unavailable")
					return
				}
				if err := h.Responses.HandleSocketModeResponse(r.Context(), connection.AppID, envelope.EnvelopeID, envelope.Payload); err != nil {
					disconnectWith("warning", websocket.CloseInternalServerErr, "response payload routing failed")
					return
				}
			}
			var ackErr error
			if interaction {
				ackErr = interactionDelivery.consume(r.Context(), envelope.EnvelopeID)
			} else {
				// One durable record can be several envelopes — a single
				// conversation.members_invited record is one
				// member_joined_channel per invited user — and the delivery
				// position names the record, so it advances only once every
				// envelope of the record is acknowledged.
				ackErr = delivery.acknowledge(r.Context(), envelope.EnvelopeID)
			}
			if ackErr != nil {
				if !leaseLost(ackErr) {
					disconnectWith("warning", websocket.CloseInternalServerErr, "event delivery state unavailable")
					return
				}
				// Another connection owns the envelope now; this one simply
				// lost a race it cannot win, which is not a server fault.
				logger.Warn("Socket Mode acknowledgement arrived after the envelope lease was lost", "envelope", envelope.EnvelopeID, "error", ackErr)
			}
			if !fill() {
				return
			}
		case <-ticker.C:
			now := time.Now()
			if interactionDelivery != nil {
				if err := interactionDelivery.expire(r.Context(), now); err != nil {
					disconnectWith("warning", websocket.CloseInternalServerErr, "interaction delivery state unavailable")
					return
				}
			}
			if delivery != nil && delivery.expired(now) {
				// The envelope goes back to the queue with backoff, and this
				// connection stays up: an unacknowledged envelope is a listener
				// problem, and tearing the socket down every envelope timeout
				// punished every other envelope the app was handling.
				if err := delivery.settle(r.Context(), "ack_timeout", now.UTC()); err != nil {
					disconnectWith("warning", websocket.CloseInternalServerErr, "event delivery state unavailable")
					return
				}
			}
			if !fill() {
				return
			}
		case <-pingTicker.C:
			if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				return
			}
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				return
			}
		case <-leaseTicker.C:
			if err := h.Store.RenewSocketModeConnection(r.Context(), connection.ID, time.Now().UTC().Add(connectionLifetime)); err != nil {
				disconnectWith("warning", websocket.CloseInternalServerErr, "connection lease unavailable")
				return
			}
		case <-refreshTimer.C:
			disconnectWith("refresh_requested", websocket.CloseNormalClosure, "refresh_requested")
			return
		}
	}
}

// emptyPayload reports whether an acknowledgement carries no response at all:
// the payload member is absent or null. An empty object is still a response —
// for a block_suggestion it is an empty option list — and whether it means
// anything for an event is decided where responses are interpreted.
func emptyPayload(payload json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(payload))
	return trimmed == "" || trimmed == "null"
}

// skippable classifies a record that yields no envelope. It reports true when
// the record is to be consumed durably and delivery continues, after logging
// why, and false when the record points at a defect no reconnect can hide.
func skippable(logger *slog.Logger, record events.Record, err error) bool {
	switch {
	case err == nil:
		// The record's topic maps to no Slack event, or to one whose inner
		// shape this repository has not modelled. Shipping the durable record
		// instead is what made every official client unable to parse this
		// stream, so the record is stepped over durably. Debug rather than
		// Warn: for most topics this is the table's deliberate answer, not an
		// incident.
		logger.Debug("Socket Mode has no Slack event for this topic", "sequence", record.Sequence, "topic", record.Event.Topic)
	case errors.Is(err, events.ErrEventIncomplete):
		// A record with no identifier is reported at Error: the payload may be
		// a perfectly deliverable event, and what is wrong is the record's own
		// identity, which no reconnect recovers.
		logger.Error("Socket Mode dropped a record with no event ID", "sequence", record.Sequence, "topic", record.Event.Topic, "error", err)
	case errors.Is(err, events.ErrSlackEventIncomplete) || errors.Is(err, events.ErrPayloadFieldInvalid):
		// A payload that names a Slack event this system knows but cannot fill
		// in is a defect in the producer: not an app problem, and no reconnect
		// fixes it.
		logger.Error("Socket Mode dropped a record whose payload cannot fill its Slack event", "sequence", record.Sequence, "topic", record.Event.Topic, "error", err)
	case errors.Is(err, events.ErrPayloadInternal) || errors.Is(err, events.ErrPayloadMalformed) || errors.Is(err, events.ErrPayloadRecipientScoped):
		// An internal worker record, a record addressed to a single user, or a
		// payload written before the typed payload contract can never be
		// delivered to an app. Closing here would leave the record in place and
		// reconnect into it forever.
		logger.Warn("Socket Mode skipped an undeliverable event", "sequence", record.Sequence, "topic", record.Event.Topic, "error", err)
	default:
		return false
	}
	return true
}

// slackRetryReason names why an envelope is being re-sent, in Slack's
// vocabulary. The store records this server's own reason for the failed
// attempt; every one of them means the app did not acknowledge in time, which
// Slack reports as "timeout".
func slackRetryReason(attempt int, stored string) string {
	if attempt == 0 {
		return ""
	}
	switch strings.TrimSpace(stored) {
	case "ack_timeout", "connection_closed", "":
		return "timeout"
	}
	return strings.TrimSpace(stored)
}

// retryAt is when the retry numbered retry (1-based) becomes deliverable.
func retryAt(now time.Time, retry int) time.Time {
	if retry < 1 {
		retry = 1
	}
	if retry > len(retryDelays) {
		retry = len(retryDelays)
	}
	return now.Add(retryDelays[retry-1])
}

// leaseLost reports whether a lease operation failed because another
// connection now owns the envelope, as opposed to the store being unable to
// answer. Losing that race is not a server fault.
func leaseLost(err error) bool {
	return errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrLeaseConflict) || errors.Is(err, store.ErrNotFound)
}

func servingHost(r *http.Request) string {
	if name, err := os.Hostname(); err == nil && strings.TrimSpace(name) != "" {
		return name
	}
	return r.Host
}

// newDelivery prepares this connection's lease owner. A handler with no queue
// delivers nothing and answers acknowledgements.
func (h Handler) newDelivery(connection domain.SocketModeConnection, logger *slog.Logger, timeout time.Duration) *leasedDelivery {
	if h.Queue == nil {
		return nil
	}
	return &leasedDelivery{queue: h.Queue, appID: connection.AppID, owner: connection.ID, logger: logger, timeout: timeout}
}

func (h Handler) newInteractionDelivery(connection domain.SocketModeConnection, logger *slog.Logger, timeout time.Duration) *leasedInteractionDelivery {
	if h.Interactions == nil {
		return nil
	}
	return &leasedInteractionDelivery{queue: h.Interactions, appID: connection.AppID, owner: connection.ID, logger: logger, timeout: timeout, inFlight: make(map[string]interactionLease)}
}

type interactionLease struct {
	sentAt time.Time
	// retries is how many times this interaction had already been re-sent
	// when this connection claimed it.
	retries int
}

// leasedInteractionDelivery holds the interaction envelopes this connection
// has sent and not had acknowledged. Interactions are leased one row each, so
// several may be in flight at once and one the app never answers holds up
// none of the others.
type leasedInteractionDelivery struct {
	queue    InteractionQueue
	appID    domain.AppID
	owner    string
	logger   *slog.Logger
	timeout  time.Duration
	inFlight map[string]interactionLease
}

func (d *leasedInteractionDelivery) count() int { return len(d.inFlight) }

func (d *leasedInteractionDelivery) holds(envelopeID string) bool {
	_, ok := d.inFlight[envelopeID]
	return ok
}

func (d *leasedInteractionDelivery) next(ctx context.Context) (domain.SocketModeInteraction, bool, error) {
	value, found, err := d.queue.ClaimSocketModeInteraction(ctx, d.appID, d.owner, d.timeout+writeTimeout)
	if errors.Is(err, store.ErrNotFound) {
		return domain.SocketModeInteraction{}, false, nil
	}
	if err != nil || !found {
		return domain.SocketModeInteraction{}, false, err
	}
	d.inFlight[value.EnvelopeID] = interactionLease{sentAt: time.Now(), retries: value.RetryCount}
	return value, true, nil
}

func (d *leasedInteractionDelivery) consume(ctx context.Context, envelopeID string) error {
	if !d.holds(envelopeID) {
		return store.ErrLeaseConflict
	}
	delete(d.inFlight, envelopeID)
	return d.queue.AckSocketModeInteraction(ctx, d.appID, envelopeID, d.owner)
}

// expire returns every interaction whose acknowledgement is overdue to the
// queue, or drops it once its retries are spent.
func (d *leasedInteractionDelivery) expire(ctx context.Context, now time.Time) error {
	for envelopeID, lease := range d.inFlight {
		if now.Sub(lease.sentAt) < d.timeout {
			continue
		}
		d.logger.Warn("Socket Mode envelope was not acknowledged", "envelope", envelopeID, "timeout", d.timeout, "retries", lease.retries)
		if err := d.settle(ctx, envelopeID, lease, "ack_timeout", now.UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (d *leasedInteractionDelivery) settleAll(ctx context.Context, reason string, now time.Time) error {
	var result error
	for envelopeID, lease := range d.inFlight {
		result = errors.Join(result, d.settle(ctx, envelopeID, lease, reason, now))
	}
	return result
}

func (d *leasedInteractionDelivery) settle(ctx context.Context, envelopeID string, lease interactionLease, reason string, now time.Time) error {
	delete(d.inFlight, envelopeID)
	var err error
	if lease.retries >= maxDeliveryRetries {
		d.logger.Error("Socket Mode dropped an interaction that was never acknowledged", "envelope", envelopeID, "attempts", lease.retries+1)
		err = d.queue.AckSocketModeInteraction(ctx, d.appID, envelopeID, d.owner)
	} else {
		err = d.queue.ReleaseSocketModeInteraction(ctx, d.appID, envelopeID, d.owner, reason, retryAt(now, lease.retries+1))
	}
	if leaseLost(err) {
		return nil
	}
	return err
}

// leasedDelivery holds the one durable event record this connection has
// leased. The store keeps one delivery position per app, so an app's events
// are delivered in order, one record at a time across all of its
// connections.
type leasedDelivery struct {
	queue   EventQueue
	appID   domain.AppID
	owner   string
	logger  *slog.Logger
	timeout time.Duration
	// sequence is the leased record, zero when none is.
	sequence uint64
	// attempt is how many times the leased record had been delivered before,
	// and reason why the last of those failed.
	attempt int
	reason  string
	// remaining are the record's envelopes not yet acknowledged.
	remaining map[string]struct{}
	sentAt    time.Time
}

func (d *leasedDelivery) busy() bool { return d.sequence != 0 }

func (d *leasedDelivery) holds(envelopeID string) bool {
	_, ok := d.remaining[envelopeID]
	return ok
}

func (d *leasedDelivery) expired(now time.Time) bool {
	return d.sequence != 0 && len(d.remaining) != 0 && now.Sub(d.sentAt) >= d.timeout
}

func (d *leasedDelivery) next(ctx context.Context) (events.Record, bool, error) {
	if d.sequence != 0 {
		return events.Record{}, false, errors.New("Socket Mode delivery already owns an event")
	}
	record, attempt, reason, found, err := d.queue.ClaimAppEvent(ctx, d.appID, "socket", d.owner, d.timeout+writeTimeout)
	if errors.Is(err, store.ErrNotFound) {
		return events.Record{}, false, nil
	}
	if err != nil || !found {
		return events.Record{}, false, err
	}
	d.sequence, d.attempt, d.reason = record.Sequence, attempt, reason
	return record, true, nil
}

func (d *leasedDelivery) sent(ids []string, at time.Time) {
	d.remaining = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		d.remaining[id] = struct{}{}
	}
	d.sentAt = at
}

// acknowledge records one envelope's acknowledgement and consumes the record
// once every envelope of it is acknowledged.
func (d *leasedDelivery) acknowledge(ctx context.Context, envelopeID string) error {
	if !d.holds(envelopeID) {
		return store.ErrLeaseConflict
	}
	delete(d.remaining, envelopeID)
	if len(d.remaining) != 0 {
		return nil
	}
	return d.consume(ctx)
}

// skip consumes a leased record that yields no envelope.
func (d *leasedDelivery) skip(ctx context.Context) error {
	return d.consume(ctx)
}

func (d *leasedDelivery) consume(ctx context.Context) error {
	sequence := d.sequence
	d.clear()
	if sequence == 0 {
		return nil
	}
	return d.queue.AckAppEvent(ctx, d.appID, "socket", d.owner, sequence)
}

func (d *leasedDelivery) clear() {
	d.sequence, d.attempt, d.reason, d.remaining, d.sentAt = 0, 0, "", nil, time.Time{}
}

// settle gives the leased record up: back to the queue with backoff, or,
// once its retries are spent, consumed and reported. A lease another
// connection has taken over is not an error.
func (d *leasedDelivery) settle(ctx context.Context, reason string, now time.Time) error {
	if d.sequence == 0 {
		return nil
	}
	sequence, attempt := d.sequence, d.attempt
	var err error
	if attempt >= maxDeliveryRetries {
		d.logger.Error("Socket Mode dropped an event that was never acknowledged", "sequence", sequence, "attempts", attempt+1)
		err = d.consume(ctx)
	} else {
		if reason == "ack_timeout" {
			d.logger.Warn("Socket Mode envelope was not acknowledged", "sequence", sequence, "timeout", d.timeout, "retries", attempt)
		}
		d.clear()
		err = d.queue.ReleaseAppEvent(ctx, d.appID, "socket", d.owner, sequence, reason, retryAt(now, attempt+1))
	}
	if leaseLost(err) {
		return nil
	}
	return err
}

// encodeEvent renders a durable record as the Socket Mode envelopes it becomes.
//
// The shape is built by events.SocketModeEnvelopes, not here: an official
// Socket Mode client indexes payload.event and dispatches on the inner event's
// type, and this transport used to ship the durable payload verbatim, so every
// official client received a payload with no "event" member and a type that is
// not a Slack event name. A transport must not decide what a Slack event looks
// like, so the whole shape — envelope, wrapper and inner event — comes from the
// one translation site.
//
// An empty result is not an error. It means the record's topic maps to no Slack
// event, or to one whose inner shape is not modelled yet; the record is
// consumed and reported rather than delivered in a shape no client can parse.
func encodeEvent(record events.Record, appID domain.AppID) ([]events.SocketModeEnvelope, error) {
	// A missing identifier is a defect in the record's identity, not in its
	// payload: the payload may be a perfectly deliverable event. Classifying it
	// as a malformed payload made it indistinguishable from an undeliverable one
	// everywhere the two are handled together, including the outbox worker's
	// drop-and-acknowledge path.
	if strings.TrimSpace(string(record.Event.ID)) == "" {
		return nil, fmt.Errorf("%w: Socket Mode envelope ID comes from the event ID, which is empty", events.ErrEventIncomplete)
	}
	return events.SocketModeEnvelopes(record, string(appID))
}
