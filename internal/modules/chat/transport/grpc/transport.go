package grpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/observability"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MaxMessageBytes bounds a single request or response message on the chat seam,
// on both peers and in both directions.
//
// grpc-go defaults both receive limits to 4 MiB and neither peer set them, so a
// payload the monolith accepted failed with codes.ResourceExhausted once the same
// module ran behind gRPC. Several fields on this contract are unbounded opaque
// JSON — canvas content, list schema, list item fields, message blocks and
// attachments, view payloads — and each of them arrives in an HTTP request body
// bounded to httpRequestBodyBytes. A request carrying a full body therefore does
// not fit in 4 MiB at all once proto framing is added, which is why the default
// is the wrong bound rather than a merely conservative one.
//
// 16x the HTTP body bound admits every single-record request with room for
// framing and for a response page that aggregates several body-sized records,
// while still capping how much one in-flight message may buffer.
// TestMessageBoundExceedsTheHTTPBodyBound asserts the relationship and
// TestHTTPRequestBodyBoundMatchesTheHTTPLayer asserts that httpRequestBodyBytes
// still equals the constant the HTTP handlers use, so the two cannot drift.
const MaxMessageBytes = 16 * httpRequestBodyBytes

// httpRequestBodyBytes mirrors maxRequestBody in internal/api/slack and
// maxFormBody in internal/web. Those constants are unexported there, so the value
// is restated here and gated by a test that reads their declarations from source.
const httpRequestBodyBytes = 4 << 20

// Observer receives the measurements and the log records of the chat seam.
//
// Both fields are optional: a nil Registry or Logger disables that half rather
// than panicking, because the seam must not depend on a process having chosen to
// observe it. No field of a measurement or a log record carries tenant, user,
// message or token data, and metric names never embed a method name, so the
// series count is bounded by the number of gRPC status codes.
type Observer struct {
	Metrics *observability.Registry
	Logger  *slog.Logger
}

func (o Observer) count(name string) {
	if o.Metrics == nil {
		return
	}
	o.Metrics.AddCounter(name, 1)
}

func (o Observer) observe(name string, value time.Duration) {
	if o.Metrics == nil {
		return
	}
	if value < 0 {
		value = 0
	}
	o.Metrics.ObserveDuration(name, value)
}

func (o Observer) log(ctx context.Context, level slog.Level, message string, attributes ...any) {
	if o.Logger == nil {
		return
	}
	o.Logger.Log(ctx, level, message, attributes...)
}

// ServerOptions returns every grpc.ServerOption the chat process needs: matching
// message bounds, panic recovery, seam instrumentation, and the connection limits
// that let a peer notice a dead session.
//
// It exists so the composition root cannot install a subset. A chatd built with
// grpc.NewServer(grpc.Creds(...)) alone had no message bound (so it disagreed
// with the monolith about which payloads are acceptable), no panic recovery (so
// one nil map write in a handler terminated every in-flight request on the
// replica, where net/http recovers the same panic and loses one request), no
// keepalive (so a half-open connection was never detected) and no observability.
func ServerOptions(observer Observer) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxMessageBytes),
		grpc.MaxSendMsgSize(MaxMessageBytes),
		grpc.ChainUnaryInterceptor(UnaryServerInterceptor(observer)),
		grpc.ChainStreamInterceptor(StreamServerInterceptor(observer)),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
	}
}

// NewChatServer builds the gRPC server for a chat process: it applies every
// option the transport requires and registers the module on it.
//
// It exists so a composition root cannot assemble a partial server. cmd/chatd
// built one with grpc.NewServer(grpc.Creds(...)) and registered the module
// separately, which is why the process ran with no panic recovery, no message
// bound, no keepalive and no instrumentation for as long as it did: nothing in
// the transport package was in a position to notice. Extra options — transport
// credentials, in particular — are appended, so a caller can add what only it
// knows without being able to drop what the seam needs.
func NewChatServer(implementation chatapi.Service, tokens auth.TokenStore, sessions auth.SessionStore, revoker auth.SessionRevoker, observer Observer, extra ...grpc.ServerOption) (*grpc.Server, error) {
	server := grpc.NewServer(append(ServerOptions(observer), extra...)...)
	if err := RegisterServer(server, implementation, tokens, sessions, revoker); err != nil {
		return nil, err
	}
	return server, nil
}

// DialOptions returns every grpc.DialOption the HTTP process needs so that the
// two peers agree on message bounds, keep the session verified, and report the
// same measurements from the caller's side.
func DialOptions(observer Observer) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxMessageBytes),
			grpc.MaxCallSendMsgSize(MaxMessageBytes),
		),
		grpc.WithChainUnaryInterceptor(UnaryClientInterceptor(observer)),
		grpc.WithChainStreamInterceptor(StreamClientInterceptor(observer)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
	}
}

const (
	serverMetricPrefix = "sameoldchat_chat_server_"
	clientMetricPrefix = "sameoldchat_chat_client_"
)

// UnaryServerInterceptor recovers handler panics and records the call.
func UnaryServerInterceptor(observer Observer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
		ctx = traceServerContext(ctx)
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				observer.count(serverMetricPrefix + "rpc_panics_total")
				observer.log(ctx, slog.LevelError, "chat rpc handler panicked",
					"method", info.FullMethod, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()), "trace_id", traceID(ctx))
				response, err = nil, panicError(recovered)
			}
			recordCall(ctx, observer, serverMetricPrefix, info.FullMethod, started, err)
		}()
		return handler(ctx, request)
	}
}

// StreamServerInterceptor recovers streaming handler panics and records the
// stream. The two upload handlers additionally recover inside the goroutine they
// start, because a panic there unwinds a stack this interceptor does not own.
func StreamServerInterceptor(observer Observer) grpc.StreamServerInterceptor {
	return func(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		ctx := traceServerContext(stream.Context())
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				observer.count(serverMetricPrefix + "stream_panics_total")
				observer.log(ctx, slog.LevelError, "chat stream handler panicked",
					"method", info.FullMethod, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()), "trace_id", traceID(ctx))
				err = panicError(recovered)
			}
			recordStream(ctx, observer, serverMetricPrefix, info.FullMethod, started, err)
		}()
		return handler(service, tracedServerStream{ServerStream: stream, ctx: ctx})
	}
}

// UnaryClientInterceptor records the caller's view of a call and propagates the
// trace identity, so a trace does not break at the seam.
func UnaryClientInterceptor(observer Observer) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, request, response any, connection *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
		ctx = traceClientContext(ctx)
		started := time.Now()
		err := invoker(ctx, method, request, response, connection, options...)
		recordCall(ctx, observer, clientMetricPrefix, method, started, err)
		return err
	}
}

// StreamClientInterceptor records the caller's view of a stream and propagates
// the trace identity.
func StreamClientInterceptor(observer Observer) grpc.StreamClientInterceptor {
	return func(ctx context.Context, descriptor *grpc.StreamDesc, connection *grpc.ClientConn, method string, streamer grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx = traceClientContext(ctx)
		started := time.Now()
		stream, err := streamer(ctx, descriptor, connection, method, options...)
		recordStream(ctx, observer, clientMetricPrefix, method, started, err)
		return stream, err
	}
}

func recordCall(ctx context.Context, observer Observer, prefix, method string, started time.Time, err error) {
	code := status.Code(err)
	observer.count(prefix + "rpc_requests_total")
	observer.count(prefix + "rpc_code_" + codeMetricName(code) + "_total")
	observer.observe(prefix+"rpc_duration_seconds", time.Since(started))
	logOutcome(ctx, observer, "chat rpc", method, started, code, err)
}

func recordStream(ctx context.Context, observer Observer, prefix, method string, started time.Time, err error) {
	code := status.Code(err)
	observer.count(prefix + "stream_requests_total")
	observer.count(prefix + "stream_code_" + codeMetricName(code) + "_total")
	observer.observe(prefix+"stream_duration_seconds", time.Since(started))
	logOutcome(ctx, observer, "chat stream", method, started, code, err)
}

// logOutcome records one line per failed call on the seam.
//
// A successful call is not logged: the counters and the duration summary already
// describe the healthy path, and a log line per request would cost more than it
// tells. codes.NotFound is expected traffic (a lookup that misses) and is logged
// at debug. Everything else is a warning, and the error text is attached only
// when the failure carries no domain class, because that is the only case where
// the text is the sole description of the cause; a classified failure logs its
// sentinel key instead, which keeps storage-internal text out of the record.
func logOutcome(ctx context.Context, observer Observer, kind, method string, started time.Time, code codes.Code, err error) {
	if observer.Logger == nil || err == nil {
		return
	}
	attributes := []any{
		"method", method,
		"code", code.String(),
		"duration_ms", time.Since(started).Milliseconds(),
	}
	if identity := traceID(ctx); identity != "" {
		attributes = append(attributes, "trace_id", identity)
	}
	level := slog.LevelWarn
	if code == codes.NotFound {
		level = slog.LevelDebug
	}
	if class, classified := classifyError(err); classified {
		attributes = append(attributes, "domain_error", class.key)
	} else {
		attributes = append(attributes, "error", err.Error())
	}
	observer.log(ctx, level, kind+" failed", attributes...)
}

// codeMetricName is the metric-name fragment for a status code. The set is the
// closed set of gRPC codes, so the series count is bounded; an unrecognised code
// is bucketed rather than embedded, because codes.Code.String() renders an
// unknown value as "Code(23)", which is not a valid Prometheus metric name.
func codeMetricName(code codes.Code) string {
	if name, ok := codeMetricNames[code]; ok {
		return name
	}
	return "unrecognized"
}

var codeMetricNames = map[codes.Code]string{
	codes.OK:                 "ok",
	codes.Canceled:           "canceled",
	codes.Unknown:            "unknown",
	codes.InvalidArgument:    "invalid_argument",
	codes.DeadlineExceeded:   "deadline_exceeded",
	codes.NotFound:           "not_found",
	codes.AlreadyExists:      "already_exists",
	codes.PermissionDenied:   "permission_denied",
	codes.ResourceExhausted:  "resource_exhausted",
	codes.FailedPrecondition: "failed_precondition",
	codes.Aborted:            "aborted",
	codes.OutOfRange:         "out_of_range",
	codes.Unimplemented:      "unimplemented",
	codes.Internal:           "internal",
	codes.Unavailable:        "unavailable",
	codes.DataLoss:           "data_loss",
	codes.Unauthenticated:    "unauthenticated",
}

// panicError is the error a recovered panic becomes.
//
// A panic is an unhandled exception, so it is codes.Internal rather than one of
// the domain classes, and the message is fixed: the panic value can name
// anything that happened to be on the stack and must not reach a caller. The
// value stays available in process through Unwrap for the log record.
func panicError(recovered any) error {
	cause := fmt.Errorf("chat handler panicked: %v", recovered)
	if err, ok := recovered.(error); ok {
		cause = fmt.Errorf("chat handler panicked: %w", err)
	}
	return serverError{status: status.New(codes.Internal, "chat service failed to handle the request"), cause: cause}
}

// traceparentHeader is the W3C Trace Context header. gRPC lowercases metadata
// keys, and the header name is already lowercase.
const traceparentHeader = "traceparent"

// traceServerContext joins the caller's trace, or starts one when the caller sent
// none, so every record on this side of the seam carries a correlation identity.
func traceServerContext(ctx context.Context) context.Context {
	if _, ok := observability.TraceFromContext(ctx); ok {
		return ctx
	}
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		for _, value := range incoming.Get(traceparentHeader) {
			parsed, err := observability.ParseTraceParent(value)
			if err != nil {
				continue
			}
			child, err := parsed.Child()
			if err != nil {
				continue
			}
			return observability.ContextWithTrace(ctx, child)
		}
	}
	trace, err := observability.NewTrace()
	if err != nil {
		return ctx
	}
	return observability.ContextWithTrace(ctx, trace)
}

// traceClientContext sends the caller's trace to the chat process, starting one
// when the caller has none so that the two sides of a call are still correlated.
func traceClientContext(ctx context.Context) context.Context {
	trace, ok := observability.TraceFromContext(ctx)
	if !ok {
		started, err := observability.NewTrace()
		if err != nil {
			return ctx
		}
		trace = started
		ctx = observability.ContextWithTrace(ctx, trace)
	}
	child, err := trace.Child()
	if err != nil {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, traceparentHeader, child.TraceParent())
}

func traceID(ctx context.Context) string {
	if trace, ok := observability.TraceFromContext(ctx); ok {
		return trace.TraceID
	}
	return ""
}

// tracedServerStream carries the trace-bearing context into a streaming handler,
// which reads its context from the stream rather than from a parameter.
type tracedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s tracedServerStream) Context() context.Context { return s.ctx }

// chunkBytes bounds one stream frame. Uploads and downloads on this contract are
// chunked rather than buffered so a large file never occupies its own size in
// memory on either peer, and io.Pipe supplies the back-pressure.
const chunkBytes = 64 << 10

// sendChunks copies source into send in bounded chunks.
//
// It replaces six near-identical loops (three uploads on the client, three
// downloads on the server) that differed only in the message they built. One of
// them, Remote.UploadExternalFile, was missing the zero-read guard the other five
// had, so an io.Reader that keeps returning (0, nil) spun that loop forever —
// a busy loop the monolith does not have, because there the same reader reaches
// the blob store directly. Sharing the loop removes that divergence by
// construction rather than by adding a sixth copy of the guard.
//
// The chunk handed to send is only valid for the duration of the call; a caller
// that hands it to a proto message must copy it.
func sendChunks(source io.Reader, what string, send func(chunk []byte) error) error {
	if source == nil {
		return fmt.Errorf("%s is required", what)
	}
	buffer := make([]byte, chunkBytes)
	for {
		read, readErr := source.Read(buffer)
		if read > 0 {
			if err := send(buffer[:read]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if read == 0 {
			return fmt.Errorf("%s returned no data or error", what)
		}
	}
}

// uploadFailure reports why an upload stopped.
//
// grpc-go answers Send with io.EOF once the server has ended the stream, and the
// reason is only available on the receive side. Returning the send error alone
// surfaced "EOF" to a caller for a failure the monolith reports as a domain error
// — service.ErrBlobUnavailable, for instance — and which of the two a caller saw
// depended on how fast the server tore the stream down.
func uploadFailure(sendErr error, receive func() error) error {
	if recvErr := receive(); recvErr != nil && !errors.Is(recvErr, io.EOF) {
		return recvErr
	}
	return sendErr
}
