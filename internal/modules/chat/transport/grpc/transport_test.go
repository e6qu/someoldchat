package grpc

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
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
func serve(t *testing.T, implementation chatapi.Service, target *memory.Store, observer Observer) (Remote, grpclib.ClientConnInterface) {
	t.Helper()
	server, err := NewChatServer(implementation, target, target, target, observer)
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

func (p panickingChat) UploadFile(context.Context, domain.WorkspaceID, domain.UserID, string, string, string, int64, io.Reader) (domain.File, error) {
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
	_, err := remote.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", int64(len(content)), bytes.NewReader(content))
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
func TestHandlersDoNotBuildStatusErrorsDirectly(t *testing.T) {
	source, err := os.ReadFile("grpc.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"status.Error(", "status.Errorf(", "status.New("} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Errorf("grpc.go builds a status with %s; use invalidArgument, invalidArgumentFrom or mapError so the failure carries a domain class", forbidden)
		}
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
	if _, err := unavailable.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", 5, bytes.NewReader([]byte("hello"))); !errors.Is(err, service.ErrBlobUnavailable) {
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
