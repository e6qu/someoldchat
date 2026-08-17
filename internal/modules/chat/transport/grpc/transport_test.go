package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
	"github.com/sameoldchat/sameoldchat/internal/observability"
	"github.com/sameoldchat/sameoldchat/internal/service"
	storepkg "github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// serve starts a chat server for one implementation and returns the Remote and
// the raw connection, wired with the options both binaries are expected to use.
func serve(t *testing.T, implementation chatapi.Service, target *memory.Store, observer Observer, extra ...grpclib.ServerOption) (Remote, grpclib.ClientConnInterface) {
	t.Helper()
	server, err := NewChatServer(implementation, target, target, target, observer, extra...)
	if err != nil {
		t.Fatalf("chat server: %v", err)
	}
	listener := bufconn.Listen(1 << 20)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		<-served
	})
	dial := append([]grpclib.DialOption{
		grpclib.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpclib.WithTransportCredentials(insecure.NewCredentials()),
	}, DialOptions(observer)...)
	connection, err := grpclib.NewClient("passthrough:///bufnet", dial...)
	if err != nil {
		t.Fatalf("chat client: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	remote, err := NewRemote(connection)
	if err != nil {
		t.Fatalf("chat remote: %v", err)
	}
	return remote, connection
}

// serveWithDialOptions is serve with extra client options, so a test can make a
// bound reachable without building a payload the size of the shipped one.
func serveWithDialOptions(t *testing.T, implementation chatapi.Service, target *memory.Store, extra ...grpclib.DialOption) Remote {
	t.Helper()
	server, err := NewChatServer(implementation, target, target, target, Observer{})
	if err != nil {
		t.Fatalf("chat server: %v", err)
	}
	listener := bufconn.Listen(1 << 20)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		<-served
	})
	dial := append([]grpclib.DialOption{
		grpclib.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpclib.WithTransportCredentials(insecure.NewCredentials()),
	}, DialOptions(Observer{})...)
	connection, err := grpclib.NewClient("passthrough:///bufnet", append(dial, extra...)...)
	if err != nil {
		t.Fatalf("chat client: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	remote, err := NewRemote(connection)
	if err != nil {
		t.Fatalf("chat remote: %v", err)
	}
	return remote
}

// TestAnOversizedResponseIsNotRestoredAsADomainSentinel covers the
// mis-restoration: codes.ResourceExhausted is what grpc-go itself answers when a
// message exceeds the receive bound, and it answers it with no DomainError
// detail. While store.ErrSocketModeConnectionLimit was the fallback for that
// code, every page too large for the seam came back carrying a sentinel the chat
// process never reported — which internal/api/slack renders as
// socket_mode_unavailable, for an operation that has nothing to do with Socket
// Mode.
//
// The bound is lowered on the client rather than the payload raised to 64 MiB:
// the defect is in what the code means, not in where the bound sits.
func TestAnOversizedResponseIsNotRestoredAsADomainSentinel(t *testing.T) {
	target := seededStore(t)
	remote := serveWithDialOptions(t, service.Messages{Store: target}, target,
		grpclib.WithDefaultCallOptions(grpclib.MaxCallRecvMsgSize(2048)))
	ctx := context.Background()

	for index := 0; index < 8; index++ {
		if _, err := remote.Post(ctx, "T1", "U1", "C1", strings.Repeat("block", 200), "", ""); err != nil {
			t.Fatalf("post %d: %v", index, err)
		}
	}
	_, err := remote.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 200})
	if err == nil {
		t.Fatal("a page larger than the receive bound was accepted")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Fatalf("history error = %v (code %s), want the transport's own size rejection", err, got)
	}
	if errors.Is(err, storepkg.ErrSocketModeConnectionLimit) {
		t.Fatalf("a message-size failure was restored as store.ErrSocketModeConnectionLimit: %v", err)
	}
	for _, class := range errorClasses {
		if errors.Is(err, class.sentinel) {
			t.Errorf("a message-size failure was restored as %s, a sentinel the chat process never reported", class.key)
		}
	}
}

// TestTheHeaderListIsBoundedOnBothPeers covers the second bound. The status, its
// message and the DomainError detail travel in trailers, which grpc-go defaults
// to 16 MiB on both peers — four times looser than the message bound the file
// comment presents as the bound on the seam.
func TestTheHeaderListIsBoundedOnBothPeers(t *testing.T) {
	if MaxHeaderListBytes >= MaxMessageBytes {
		t.Fatalf("MaxHeaderListBytes = %d must be well below MaxMessageBytes = %d", MaxHeaderListBytes, MaxMessageBytes)
	}
	if MaxHeaderListBytes <= maxStatusMessageBytes {
		t.Fatalf("MaxHeaderListBytes = %d must leave room for a bounded status message of %d bytes plus the rest of the list", MaxHeaderListBytes, maxStatusMessageBytes)
	}
	var serverBound, clientBound bool
	for _, option := range ServerOptions(Observer{}) {
		if fmt.Sprintf("%T", option) == fmt.Sprintf("%T", grpclib.MaxHeaderListSize(MaxHeaderListBytes)) {
			serverBound = true
		}
	}
	for _, option := range DialOptions(Observer{}) {
		if fmt.Sprintf("%T", option) == fmt.Sprintf("%T", grpclib.WithMaxHeaderListSize(MaxHeaderListBytes)) {
			clientBound = true
		}
	}
	if !serverBound {
		t.Error("ServerOptions does not bound the header list; the trailers a failure travels in are bounded only by grpc-go's 16 MiB default")
	}
	if !clientBound {
		t.Error("DialOptions does not bound the header list")
	}
}

func seededStore(t *testing.T) *memory.Store {
	t.Helper()
	target := memory.New()
	seedBaseline(t, target)
	return target
}

// panickingChat fails the way a defect in the implementation fails: with a panic
// on the handler's stack, or on the stack of the goroutine an upload handler
// starts. Everything it does not override is answered by the real module, so the
// test can check that the process is still serving afterwards.
type panickingChat struct {
	chatapi.Service
}

func (p panickingChat) Post(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, string, domain.MessageTimestamp, string) (domain.Message, error) {
	var broken map[string]string
	broken["written to a nil map"] = "boom"
	return domain.Message{}, nil
}

func (p panickingChat) UploadFile(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, string, int64, io.Reader) (domain.File, error) {
	panic("upload handler panicked")
}

// TestAPanicInAUnaryHandlerBecomesAnErrorAndTheProcessKeepsServing covers the
// divergence in the panic path: net/http recovers a handler panic and loses one
// request, while grpc-go does not recover and the whole chat replica aborts,
// taking every other in-flight request with it.
func TestAPanicInAUnaryHandlerBecomesAnErrorAndTheProcessKeepsServing(t *testing.T) {
	target := seededStore(t)
	remote, _ := serve(t, panickingChat{Service: service.Messages{Store: target}}, target, Observer{})
	ctx := context.Background()

	_, err := remote.Post(ctx, "T1", "U1", "C1", "provokes a panic", "", "")
	if err == nil {
		t.Fatal("a panicking handler answered without an error")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("panic code = %s, want %s", got, codes.Internal)
	}
	if strings.Contains(err.Error(), "nil map") {
		t.Fatalf("client-visible error %q carries the panic value", err.Error())
	}
	// The process must still be serving: this is the assertion that fails by
	// killing the test binary when no recovery interceptor is installed.
	if user, err := remote.UserInfo(ctx, "T1", "U1", "U1"); err != nil || user.ID != "U1" {
		t.Fatalf("the chat process stopped serving after a handler panic: user=%+v err=%v", user, err)
	}
}

// TestAPanicInAnUploadGoroutineBecomesAnErrorAndTheProcessKeepsServing covers the
// two upload handlers, which call the implementation from a goroutine that no
// interceptor can unwind.
func TestAPanicInAnUploadGoroutineBecomesAnErrorAndTheProcessKeepsServing(t *testing.T) {
	target := seededStore(t)
	remote, _ := serve(t, panickingChat{Service: service.Messages{Store: target}}, target, Observer{})
	ctx := context.Background()

	content := bytes.Repeat([]byte("upload-"), 32<<10)
	_, err := remote.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", "", int64(len(content)), bytes.NewReader(content))
	if err == nil {
		t.Fatal("a panicking upload answered without an error")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("panic code = %s, want %s", got, codes.Internal)
	}
	if strings.Contains(err.Error(), "upload handler panicked") {
		t.Fatalf("client-visible error %q carries the panic value", err.Error())
	}
	if user, err := remote.UserInfo(ctx, "T1", "U1", "U1"); err != nil || user.ID != "U1" {
		t.Fatalf("the chat process stopped serving after an upload panic: user=%+v err=%v", user, err)
	}
}

// recordingHandler collects slog records so a test can assert what the seam logs
// and, just as importantly, what it does not.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) attributes(t *testing.T, message string) map[string]string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range h.records {
		if record.Message != message {
			continue
		}
		result := make(map[string]string, record.NumAttrs())
		record.Attrs(func(attribute slog.Attr) bool {
			result[attribute.Key] = attribute.Value.String()
			return true
		})
		return result
	}
	t.Fatalf("no log record with message %q; got %d records", message, len(h.records))
	return nil
}

// byPeer returns the records with one message, keyed by the peer that produced
// them. recordCall runs on both interceptors, so one failed request produces two.
func (h *recordingHandler) byPeer(t *testing.T, message string) map[string]slog.Record {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make(map[string]slog.Record, 2)
	for _, record := range h.records {
		if record.Message != message {
			continue
		}
		peer := ""
		record.Attrs(func(attribute slog.Attr) bool {
			if attribute.Key == "peer" {
				peer = attribute.Value.String()
			}
			return true
		})
		if peer == "" {
			t.Fatalf("a %q record carries no peer attribute, so the two sides of the seam are indistinguishable", message)
		}
		result[peer] = record
	}
	return result
}

// TestOnlyAnOperatorFailureIsAWarning covers the log volume.
//
// codes.InvalidArgument is the ordinary outcome of a malformed Slack API call —
// the seam produces it for thirty-odd validation sentinels — and it was logged at
// warning on both peers, so one bad request from a browser wrote two
// unrate-limited WARN lines to a chatd whose logger is at Info level.
func TestOnlyAnOperatorFailureIsAWarning(t *testing.T) {
	ctx := context.Background()

	target := seededStore(t)
	handler := &recordingHandler{}
	remote, _ := serve(t, service.Messages{Store: target}, target, Observer{Logger: slog.New(handler)})
	if _, err := remote.Post(ctx, "T1", "U1", "C1", "", "", ""); !errors.Is(err, service.ErrInvalidMessage) {
		t.Fatalf("invalid post error = %v", err)
	}
	callerCaused := handler.byPeer(t, "chat rpc failed")
	if len(callerCaused) != 2 {
		t.Fatalf("a failed call produced records from %d peers, want both sides of the seam: %v", len(callerCaused), callerCaused)
	}
	for peer, record := range callerCaused {
		if record.Level != slog.LevelDebug {
			t.Errorf("a caller's malformed request logged at %s on the %s, want %s", record.Level, peer, slog.LevelDebug)
		}
	}

	// A panic is this system failing, and stays a warning.
	panicking := seededStore(t)
	panicHandler := &recordingHandler{}
	broken, _ := serve(t, panickingChat{Service: service.Messages{Store: panicking}}, panicking, Observer{Logger: slog.New(panicHandler)})
	if _, err := broken.Post(ctx, "T1", "U1", "C1", "provokes a panic", "", ""); err == nil {
		t.Fatal("a panicking handler answered without an error")
	}
	operatorCaused := panicHandler.byPeer(t, "chat rpc failed")
	if record, served := operatorCaused[peerServer]; !served || record.Level != slog.LevelWarn {
		t.Errorf("a recovered panic logged at %s on the server, want %s", record.Level, slog.LevelWarn)
	}
}

// TestWorkspaceMembershipSurvivesAPeerThatPredatesTheMethod covers the rolling
// deployment. GetWorkspaceMembership was added by the change that introduced it
// and is called on every sign-in and on /me; a chat replica that predates it
// answers codes.Unimplemented, which has no fallback class and cannot have one,
// so sign-in failed for the whole skew window with a raw status.
//
// The interceptor is what an older peer looks like from here: the method is not
// served, everything else is.
func TestWorkspaceMembershipSurvivesAPeerThatPredatesTheMethod(t *testing.T) {
	target := seededStore(t)
	olderPeer := grpclib.ChainUnaryInterceptor(func(ctx context.Context, request any, info *grpclib.UnaryServerInfo, handler grpclib.UnaryHandler) (any, error) {
		if strings.HasSuffix(info.FullMethod, "/GetWorkspaceMembership") {
			return nil, status.Error(codes.Unimplemented, "unknown method GetWorkspaceMembership")
		}
		return handler(ctx, request)
	})
	remote, _ := serve(t, service.Messages{Store: target}, target, Observer{}, olderPeer)
	ctx := context.Background()

	membership, err := remote.WorkspaceMembership(ctx, "T1", "UA", "U1")
	if err != nil {
		t.Fatalf("membership against a peer without the method: %v", err)
	}
	if membership.UserID != "U1" || membership.Role != domain.WorkspaceRoleMember || !membership.Active {
		t.Fatalf("membership = %+v, want U1's active member row", membership)
	}
	if _, err := remote.WorkspaceMembership(ctx, "T1", "UA", "U-missing"); !errors.Is(err, storepkg.ErrNotFound) {
		t.Fatalf("membership of an unknown user = %v, want store.ErrNotFound", err)
	}

	// The two RPCs that carry no actor have no equivalent to fall back to, so
	// they must at least say what is wrong instead of surfacing a bare status.
	_, err = remote.ProvisionExternalUser(ctx, "T1", "carol@example.com", "Carol", domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatalf("provisioning against a peer that serves it: %v", err)
	}
	skewed := skewFailure("ProvisionExternalUser", status.Error(codes.Unimplemented, "unknown method"))
	if !strings.Contains(skewed.Error(), "older than this build") || !strings.Contains(skewed.Error(), "ProvisionExternalUser") {
		t.Fatalf("skew failure text = %q, want the cause and the remedy", skewed.Error())
	}
	if got := skewFailure("ProvisionExternalUser", storepkg.ErrNotFound); !errors.Is(got, storepkg.ErrNotFound) {
		t.Fatalf("skewFailure rewrote an error that is not a version skew: %v", got)
	}
}

// TestTheSeamIsObservable covers the operational gap: the boundary reported no
// metrics, no logs and no trace, so a chat replica failing every call was
// invisible except as HTTP 503s on the other side of it.
func TestTheSeamIsObservable(t *testing.T) {
	target := seededStore(t)
	handler := &recordingHandler{}
	metrics := observability.NewRegistry()
	observer := Observer{Metrics: metrics, Logger: slog.New(handler)}
	remote, _ := serve(t, service.Messages{Store: target}, target, observer)

	trace, err := observability.NewTrace()
	if err != nil {
		t.Fatal(err)
	}
	ctx := observability.ContextWithTrace(context.Background(), trace)

	if _, err := remote.Post(ctx, "T1", "U1", "C1", "observed", "", ""); err != nil {
		t.Fatalf("post: %v", err)
	}
	if _, err := remote.Post(ctx, "T1", "U1", "C1", "", "", ""); !errors.Is(err, service.ErrInvalidMessage) {
		t.Fatalf("invalid post error = %v", err)
	}

	snapshot := metrics.Snapshot()
	for _, name := range []string{
		"sameoldchat_chat_server_rpc_requests_total",
		"sameoldchat_chat_client_rpc_requests_total",
		"sameoldchat_chat_server_rpc_code_ok_total",
		"sameoldchat_chat_server_rpc_code_invalid_argument_total",
		"sameoldchat_chat_client_rpc_code_invalid_argument_total",
	} {
		if snapshot.Counters[name] == 0 {
			t.Errorf("counter %s was not recorded; counters = %v", name, snapshot.Counters)
		}
	}
	for _, name := range []string{"sameoldchat_chat_server_rpc_duration_seconds", "sameoldchat_chat_client_rpc_duration_seconds"} {
		if snapshot.Durations[name].Count == 0 {
			t.Errorf("duration %s was not recorded", name)
		}
	}
	// Metric names must not embed the method, the workspace or the user: the
	// registry is unlabelled precisely so the series count stays bounded.
	for name := range snapshot.Counters {
		for _, forbidden := range []string{"T1", "U1", "Post", "post"} {
			if strings.Contains(name, forbidden) {
				t.Errorf("metric name %q embeds %q, which is unbounded or identifying", name, forbidden)
			}
		}
	}

	attributes := handler.attributes(t, "chat rpc failed")
	if attributes["code"] != codes.InvalidArgument.String() {
		t.Errorf("log code = %q", attributes["code"])
	}
	if !strings.Contains(attributes["method"], "MessagesService/Post") {
		t.Errorf("log method = %q", attributes["method"])
	}
	if attributes["domain_error"] != "service.invalid_message" {
		t.Errorf("log domain_error = %q, want the sentinel key", attributes["domain_error"])
	}
	// The trace identity crosses the boundary, so the two processes report the
	// same trace for one request.
	if attributes["trace_id"] != trace.TraceID {
		t.Errorf("log trace_id = %q, want the caller's trace %q", attributes["trace_id"], trace.TraceID)
	}
	for key, value := range attributes {
		if strings.Contains(value, "T1") || strings.Contains(value, "alice") {
			t.Errorf("log attribute %s = %q carries tenant or user data", key, value)
		}
	}
}

func TestAnObserverWithoutCollaboratorsIsInert(t *testing.T) {
	target := seededStore(t)
	remote, _ := serve(t, service.Messages{Store: target}, target, Observer{})
	if _, err := remote.Post(context.Background(), "T1", "U1", "C1", "unobserved", "", ""); err != nil {
		t.Fatalf("post with a zero observer: %v", err)
	}
}

// TestAMessageLargerThanTheDefaultBoundCrossesTheSeam covers the bound defect: the
// grpc-go default is 4 MiB on both receive paths, which is smaller than a single
// HTTP request body this system accepts, so a payload the monolith took failed
// remotely with codes.ResourceExhausted.
func TestAMessageLargerThanTheDefaultBoundCrossesTheSeam(t *testing.T) {
	target := seededStore(t)
	_, connection := serve(t, service.Messages{Store: target}, target, Observer{})
	client := chatv1.NewMessagesServiceClient(connection)

	// Larger than the grpc-go default and larger than the HTTP body bound, so the
	// call can only reach the handler if both peers were configured.
	query := strings.Repeat("q", httpRequestBodyBytes+(1<<20))
	_, err := client.Search(context.Background(), &chatv1.SearchRequest{WorkspaceId: "T1", UserId: "U1", Query: query, Limit: 10})
	if err == nil {
		t.Fatal("a 5 MiB query was accepted as a search")
	}
	if got := status.Code(err); got == codes.ResourceExhausted {
		t.Fatalf("the message bound rejected a payload the monolith accepts: %v", err)
	} else if got != codes.InvalidArgument {
		t.Fatalf("search error = %v (code %s), want the handler's own rejection", err, got)
	}
}

func TestMessageBoundExceedsTheHTTPBodyBound(t *testing.T) {
	if MaxMessageBytes <= httpRequestBodyBytes {
		t.Fatalf("MaxMessageBytes = %d must exceed the HTTP body bound %d, or a request carrying a full body cannot be sent at all", MaxMessageBytes, httpRequestBodyBytes)
	}
	if MaxMessageBytes%httpRequestBodyBytes != 0 {
		t.Fatalf("MaxMessageBytes = %d is not a multiple of the HTTP body bound %d; the relationship is the point of the constant", MaxMessageBytes, httpRequestBodyBytes)
	}
}

// TestHTTPRequestBodyBoundMatchesTheHTTPLayer reads the two HTTP body bounds from
// source. httpRequestBodyBytes restates them because they are unexported, and this
// is what stops the restatement from drifting.
func TestHTTPRequestBodyBoundMatchesTheHTTPLayer(t *testing.T) {
	for _, declaration := range []struct {
		file string
		name string
	}{
		{file: filepath.Join("..", "..", "..", "..", "api", "slack", "handler.go"), name: "maxRequestBody"},
		{file: filepath.Join("..", "..", "..", "..", "web", "handler.go"), name: "maxFormBody"},
	} {
		value, err := constantValue(declaration.file, declaration.name)
		if err != nil {
			t.Errorf("%s: %v (if the constant moved or was renamed, reconcile it with httpRequestBodyBytes here)", declaration.file, err)
			continue
		}
		if value != int64(httpRequestBodyBytes) {
			t.Errorf("%s %s = %d, but the transport bound is derived from %d; reconcile them", declaration.file, declaration.name, value, httpRequestBodyBytes)
		}
	}
}

func constantValue(path, name string) (int64, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, source, 0)
	if err != nil {
		return 0, err
	}
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != name || len(value.Values) != 1 {
				continue
			}
			expression := string(source[fileSet.Position(value.Values[0].Pos()).Offset:fileSet.Position(value.Values[0].End()).Offset])
			result, err := types.Eval(fileSet, nil, token.NoPos, expression)
			if err != nil {
				return 0, err
			}
			return strconvInt(result.Value.String())
		}
	}
	return 0, errors.New("constant " + name + " not found")
}

func strconvInt(value string) (int64, error) {
	var result int64
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("constant " + value + " is not an integer")
		}
		result = result*10 + int64(character-'0')
	}
	return result, nil
}

// TestHandlersDoNotBuildStatusErrorsDirectly keeps every failure that leaves the
// server classified. A bare status.Error in a handler carries a code and no domain
// class, so the client cannot restore a sentinel for it — which is how 139
// transport rejections came to answer differently from the same rejection in
// process.
// It reads every non-test file in the package except the two that legitimately
// construct a status. It used to read "grpc.go" by name, and grpc.go is 6,000
// lines whose obvious next refactor is a split, so any handler moved to a new
// file left the gate silently.
func TestHandlersDoNotBuildStatusErrorsDirectly(t *testing.T) {
	// errors.go owns the mapping and transport.go owns the panic conversion, so
	// both build statuses on purpose.
	allowed := map[string]bool{"errors.go": true, "transport.go": true}
	for _, name := range packageSourceFiles(t) {
		if allowed[name] {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"status.Error(", "status.Errorf(", "status.New("} {
			if bytes.Contains(source, []byte(forbidden)) {
				t.Errorf("%s builds a status with %s; use invalidArgument, invalidArgumentFrom or mapError so the failure carries a domain class", name, forbidden)
			}
		}
	}
}

func packageSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		t.Fatal("no package source files found; the scan is broken")
	}
	return names
}

// TestNoServerHandlerRejectsARequestField holds the ordering this seam settled
// on: the implementation authorises first and validates second, and the
// transport validates only what the implementation cannot see.
//
// A Server handler that rejected a missing field ran *before*
// service.Messages.authorizeWorkspace, so a caller outside the workspace learned
// which field of its request was wrong — a fact the monolith never tells it,
// because there the authorisation failure comes first. files.upload with an
// empty title answered channel_not_found in the monolith and invalid_arg_name in
// the split deployment; canvases.edit did the same. There were 105 such guards.
//
// What remains legitimate is what the transport alone can decide: a stream whose
// first message is not metadata, a stream part that is not a chunk, a timestamp
// that does not parse, a oneof arm this build cannot map. None of those is a
// field-presence test, so the gate is exactly "no rejection may read a request
// field's value".
func TestNoServerHandlerRejectsARequestField(t *testing.T) {
	for _, name := range packageSourceFiles(t) {
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || receiverTypeName(function.Recv) != "Server" {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				branch, ok := node.(*ast.IfStmt)
				if !ok || !rejectsTheRequest(branch.Body) {
					return true
				}
				if field := requestFieldRead(branch.Cond); field != "" {
					t.Errorf("%s: Server.%s rejects a request because %s is unset or out of range. The implementation decides that, and it decides it after authorising the caller, so this rejection makes the two compositions answer differently.",
						name, function.Name.Name, field)
				}
				return true
			})
		}
	}
}

// rejectsTheRequest reports whether a branch answers with invalidArgument.
func rejectsTheRequest(body *ast.BlockStmt) bool {
	rejects := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if identifier, ok := call.Fun.(*ast.Ident); ok && (identifier.Name == "invalidArgument" || identifier.Name == "invalidArgumentFrom") {
			rejects = true
		}
		return true
	})
	return rejects
}

// requestFieldRead names the first generated getter a condition reads, which is
// how a condition tests a request field rather than a transport fact.
func requestFieldRead(condition ast.Expr) string {
	field := ""
	ast.Inspect(condition, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(selector.Sel.Name, "Get") || field != "" {
			return true
		}
		field = selector.Sel.Name
		return false
	})
	return field
}

// TestEveryChatServerIsBuiltWithTransportCredentials reads the composition roots.
//
// NewChatServer makes the message bounds, the panic recovery, the keepalive and
// the instrumentation non-optional, and leaves transport credentials in a
// variadic grpc.ServerOption — the one requirement whose absence is a workspace
// takeover, because ProvisionExternalUser takes no actor and is reachable by
// anyone who can open the port. grpc.ServerOption is opaque, so the constructor
// cannot detect it; this reads the call sites instead.
func TestEveryChatServerIsBuiltWithTransportCredentials(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..", "cmd")
	found := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		// The check is per file rather than per function because a composition
		// root legitimately splits the two: cmd/chatd builds the credentials in
		// run() and calls the constructor from a chatServer helper that forwards
		// the option.
		used := make(map[string]struct{})
		ast.Inspect(parsed, func(node ast.Node) bool {
			if identifier, ok := node.(*ast.Ident); ok {
				used[identifier.Name] = struct{}{}
			}
			return true
		})
		if _, builds := used["NewChatServer"]; !builds {
			return nil
		}
		found++
		if _, credentials := used["Creds"]; !credentials {
			t.Errorf("%s builds a chat server without naming transport credentials; the three actorless DirectoryService RPCs would then be served in plaintext to anyone who reaches the port", path)
		}
		if _, plaintext := used["NewCredentials"]; plaintext {
			t.Errorf("%s builds a chat server with insecure transport credentials", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	if found == 0 {
		t.Fatalf("no chat server construction found under %s; the scan is broken and the requirement is unchecked", root)
	}
}

// TestNoRemoteMethodIgnoresAParameter makes the AdminSetConversationPrefs defect
// impossible rather than fixed: the client dropped its conversationID parameter and
// the server re-derived the target from the payload, so a call whose parameter and
// payload disagreed acted on different conversations in the two compositions. Any
// parameter a Remote method does not use is the same defect.
func TestNoRemoteMethodIgnoresAParameter(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "grpc.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || function.Body == nil {
			continue
		}
		if receiverTypeName(function.Recv) != "Remote" {
			continue
		}
		used := identifiersUsed(function.Body)
		for _, parameter := range function.Type.Params.List {
			for _, name := range parameter.Names {
				if name.Name == "_" || name.Name == "ctx" {
					continue
				}
				if _, ok := used[name.Name]; !ok {
					t.Errorf("Remote.%s ignores its %s parameter: the server cannot be acting on it, so the two compositions can disagree",
						function.Name.Name, name.Name)
				}
			}
		}
	}
}

func receiverTypeName(receiver *ast.FieldList) string {
	if receiver == nil || len(receiver.List) != 1 {
		return ""
	}
	switch value := receiver.List[0].Type.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		if identifier, ok := value.X.(*ast.Ident); ok {
			return identifier.Name
		}
	}
	return ""
}

func identifiersUsed(body *ast.BlockStmt) map[string]struct{} {
	used := make(map[string]struct{})
	ast.Inspect(body, func(node ast.Node) bool {
		if identifier, ok := node.(*ast.Ident); ok {
			used[identifier.Name] = struct{}{}
		}
		return true
	})
	return used
}

// TestTheServerReportsTheDomainClassOnEveryStreamingPath keeps the streaming
// handlers inside the error contract: a stream reports its failure through
// mappedClientStream, which is a different path from a unary call.
func TestTheServerReportsTheDomainClassOnEveryStreamingPath(t *testing.T) {
	ctx := context.Background()

	// No blob storage is configured, so the upload fails inside the module and the
	// failure has to travel back through the client stream.
	withoutBlobs := seededStore(t)
	unavailable, _ := serve(t, service.Messages{Store: withoutBlobs}, withoutBlobs, Observer{})
	if _, err := unavailable.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", "", 5, bytes.NewReader([]byte("hello"))); !errors.Is(err, service.ErrBlobUnavailable) {
		t.Fatalf("upload error = %v, want service.ErrBlobUnavailable", err)
	}

	// With blob storage configured, a download of a file that does not exist must
	// carry store.ErrNotFound, which is reported before the first stream message.
	withBlobs := seededStore(t)
	blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	available, _ := serve(t, service.Messages{Store: withBlobs, Blob: blobs}, withBlobs, Observer{})
	if _, _, err := available.OpenFile(ctx, "T1", "U1", "F-missing"); !errors.Is(err, storepkg.ErrNotFound) {
		t.Fatalf("download error = %v, want store.ErrNotFound", err)
	}
}

func TestChunkingRefusesAStalledSource(t *testing.T) {
	// A reader that returns (0, nil) forever must be rejected rather than spun
	// on: Remote.UploadExternalFile had no such guard, so it busy-looped where the
	// monolith handed the same reader to the blob store.
	done := make(chan error, 1)
	go func() {
		done <- sendChunks(stalledReader{}, "external upload source", func([]byte) error { return nil })
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no data or error") {
			t.Fatalf("stalled source error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sendChunks spun on a reader that returns no data and no error")
	}
}

type stalledReader struct{}

func (stalledReader) Read([]byte) (int, error) { return 0, nil }

// pageRecordingChat records the page bound the implementation is asked for, so
// the test can assert what crosses the seam rather than what the caller sent.
type pageRecordingChat struct {
	chatapi.Service

	mu      sync.Mutex
	records []int
}

func (c *pageRecordingChat) record(limit int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, limit)
}

func (c *pageRecordingChat) observed() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.records...)
}

func (c *pageRecordingChat) ListEventsAfter(_ context.Context, _ domain.WorkspaceID, _ uint64, limit int) ([]events.Record, error) {
	c.record(limit)
	return nil, nil
}

func (c *pageRecordingChat) History(_ context.Context, _ domain.WorkspaceID, _ domain.UserID, _ domain.ConversationID, page domain.PageRequest) (domain.MessagePage, error) {
	c.record(page.Limit)
	return domain.MessagePage{}, nil
}

// A page limit is not a question about the caller's domain — it is a bound on
// what the server allocates on the caller's behalf. Every store preallocates
// from it (make([]T, 0, limit) in both backends), and with the seam bound
// deleted one request for limit=2147483647 took the process from 18 MB to
// 320 MB of resident memory for a two-record answer; thirty concurrent requests
// is about nine gigabytes. Nothing below the transport re-bounds it:
// domain.PageRequest is a plain struct and the service passes it through.
//
// The bound is a clamp rather than a rejection, so it cannot make the two
// compositions answer differently: no caller is refused, and no real page is
// changed.
func TestAPageLimitIsBoundedByATransportResourceLimit(t *testing.T) {
	target := seededStore(t)
	recorder := &pageRecordingChat{Service: service.Messages{Store: target}}
	remote, _ := serve(t, recorder, target, Observer{})
	ctx := context.Background()
	if _, err := remote.ListEventsAfter(ctx, "T1", 0, math.MaxInt32); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: math.MaxInt32}); err != nil {
		t.Fatal(err)
	}
	for _, observed := range recorder.observed() {
		if observed > maxSeamPage {
			t.Fatalf("the implementation was asked to allocate a page of %d, which nothing below the transport bounds", observed)
		}
	}
	// A page a caller can really ask for crosses unchanged, so the clamp is not
	// a second, invisible product limit.
	before := len(recorder.observed())
	if _, err := remote.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 201}); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.ListEventsAfter(ctx, "T1", 0, 1000); err != nil {
		t.Fatal(err)
	}
	ordinary := recorder.observed()[before:]
	if len(ordinary) != 2 || ordinary[0] != 201 || ordinary[1] != 1000 {
		t.Fatalf("ordinary page bounds were rewritten: %v", ordinary)
	}
}

// The seam rebuilds a durable record from independent proto fields: Topic and
// Payload arrive as separate values and nothing on the wire says they describe
// the same event. That is the one producer in the tree that can create a record
// whose topic understates what its payload carries, and the payload rules used
// to be applied to the topic alone — so a payload that self-describes as
// message.ephemeral or as internal blob work crossed the seam under
// message.created and was handed to every app, every webhook and every browser
// in the workspace.
//
// The refusal is not duplicated here. events applies it to both names a record
// carries, from one shared site, so anything this codec rebuilds is judged by
// what it actually contains; this test is what proves that the codec's output
// reaches that rule.
func TestARecordRebuiltFromProtoFieldsIsJudgedByItsPayload(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	restricted := map[string]struct {
		payloadType string
		sentinel    error
	}{}
	for _, topic := range events.RecipientScopedTopics() {
		restricted["a payload addressed to one recipient: "+topic] = struct {
			payloadType string
			sentinel    error
		}{topic, events.ErrPayloadRecipientScoped}
	}
	for _, topic := range events.InternalTopics() {
		restricted["an internal worker payload: "+topic] = struct {
			payloadType string
			sentinel    error
		}{topic, events.ErrPayloadInternal}
	}
	for name, testCase := range restricted {
		t.Run(name, func(t *testing.T) {
			// Topic says one thing, payload says another. Only a rebuild can
			// produce this: events.New derives the payload type from the topic.
			original := []events.Record{{Sequence: 9, Event: events.Event{
				ID: "evt_9", WorkspaceID: "T1", ActorID: "U1", Topic: "message.created", CreatedAt: at,
				Payload: fmt.Sprintf(`{"type":%q,"event_ts":"1700000000.000000","user_id":"U2","text":"SECRET-ONLY-FOR-U2"}`, testCase.payloadType),
			}}}
			rebuilt, err := decodeProtoEvents(encodeProtoEvents(original))
			if err != nil {
				t.Fatal(err)
			}
			if len(rebuilt) != 1 || rebuilt[0].Event.Topic != "message.created" {
				t.Fatalf("rebuilt=%+v", rebuilt)
			}
			if _, err := events.Broadcastable(rebuilt[0].Event); !errors.Is(err, testCase.sentinel) {
				t.Fatalf("a rebuilt record was broadcastable: error=%v, want %v", err, testCase.sentinel)
			}
			if _, err := json.Marshal(rebuilt[0]); !errors.Is(err, testCase.sentinel) {
				t.Fatalf("a rebuilt record was serialized for a third party: error=%v, want %v", err, testCase.sentinel)
			}
		})
	}
	// A payload carrying a Slack event type still crosses: that is the shape
	// every official client parses, and refusing it here is what broke them.
	compatible := []events.Record{{Sequence: 10, Event: events.Event{
		ID: "evt_10", WorkspaceID: "T1", Topic: "message.created", CreatedAt: at,
		Payload: `{"type":"message","event_ts":"1700000000.000000","channel":"C1","text":"hello"}`,
	}}}
	rebuilt, err := decodeProtoEvents(encodeProtoEvents(compatible))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := events.Broadcastable(rebuilt[0].Event); err != nil {
		t.Fatalf("a Slack-shaped payload was refused after crossing the seam: %v", err)
	}
}

// An upload that keeps arriving and never delivers a byte is a peer that has
// stopped making progress, and the handler used to wait for it forever: the
// stream, the receive loop, the io.Pipe and the implementation goroutine stay
// pinned, and keepalive never fires because frames are arriving. The client
// half of this package already refuses the same shape (transport.go, "an
// io.Reader that keeps returning (0, nil) spun that loop forever").
func TestAnUploadStreamThatNeverDeliversAByteIsBounded(t *testing.T) {
	target := seededStore(t)
	_, connection := serve(t, service.Messages{Store: target}, target, Observer{})
	client := chatv1.NewChatServiceClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := client.UploadFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&chatv1.UploadFilePart{Part: &chatv1.UploadFilePart_Metadata{Metadata: &chatv1.UploadFileRequest{
		WorkspaceId: "T1", UserId: "U1", Name: "notes.txt", Title: "Notes", MimeType: "text/plain", Size: 8,
	}}}); err != nil {
		t.Fatal(err)
	}
	for range maxEmptyUploadFrames + 64 {
		if err := stream.Send(&chatv1.UploadFilePart{Part: &chatv1.UploadFilePart_Chunk{Chunk: nil}}); err != nil {
			// The server has already refused the stream, which is the point.
			break
		}
	}
	if _, err := stream.CloseAndRecv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a stream that delivered no bytes in %d frames ended with %v (code %s), want a refusal",
			maxEmptyUploadFrames+64, err, status.Code(err))
	}
}
