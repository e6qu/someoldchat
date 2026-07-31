package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	chatgrpc "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// This file is the differential harness for the Slack error contract.
//
// mapServiceErrorNamed classifies a failure from the chat service, and the chat
// service reaches this handler either as a direct Go call (monolith) or through
// the generated gRPC adapter (split deployment). It used to classify partly by
// gRPC status code, which is coarser than a sentinel: codes.AlreadyExists is both
// store.ErrAlreadyExists and service.ErrEmojiAlreadyExists, so reactions.add on a
// duplicate answered `emoji_already_exists` — a code /reactions.add does not
// declare — in the split deployment and `already_reacted` in the monolith. No
// test could see it, because every test in this package runs the monolith.
//
// A case here drives the same HTTP request against two Handlers built over two
// independently seeded but identical stores: one over service.Messages, one over
// the same implementation behind a real gRPC server and client. Both must answer
// with the same envelope.

// composition is one wiring of the chat module behind the Slack HTTP handler.
type composition struct {
	name    string
	handler http.Handler
	store   *memory.Store
}

func parityCompositions(t *testing.T, seed func(*testing.T, *memory.Store)) []composition {
	t.Helper()
	build := func() (*memory.Store, service.Messages) {
		target := memory.New()
		seed(t, target)
		return target, service.Messages{Store: target}
	}

	localStore, localService := build()
	remoteStore, remoteService := build()

	server, err := chatgrpc.NewChatServer(remoteService, remoteStore, remoteStore, remoteStore, chatgrpc.Observer{})
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
	}, chatgrpc.DialOptions(chatgrpc.Observer{})...)
	connection, err := grpclib.NewClient("passthrough:///bufnet", dial...)
	if err != nil {
		t.Fatalf("chat client: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	remote, err := chatgrpc.NewRemote(connection)
	if err != nil {
		t.Fatalf("chat remote: %v", err)
	}

	principal := auth.Principal{WorkspaceID: "T1", UserID: "U1", Scopes: map[auth.Scope]struct{}{}}
	for _, scope := range auth.AllScopes() {
		principal.Scopes[auth.Scope(scope)] = struct{}{}
	}
	mux := func(chat chatapi.Service) http.Handler {
		authenticator, err := auth.NewStatic("token", principal)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewHandler(chat, authenticator)
		if err != nil {
			t.Fatal(err)
		}
		router := http.NewServeMux()
		handler.Register(router)
		return router
	}
	return []composition{
		{name: "local", handler: mux(localService), store: localStore},
		{name: "distributed", handler: mux(remote), store: remoteStore},
	}
}

func seedParityWorkspace(t *testing.T, target *memory.Store) {
	t.Helper()
	if err := target.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := target.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := target.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := target.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
}

// TestBothCompositionsNameTheSameFailure drives one request per case against the
// monolith and the split deployment and requires an identical envelope.
func TestBothCompositionsNameTheSameFailure(t *testing.T) {
	cases := []struct {
		name string
		// provoke runs against one composition and returns the envelope of the
		// request under test. Setup requests go through the same handler, so both
		// compositions reach the failure by the same route.
		provoke func(t *testing.T, handler http.Handler) slackEnvelope
		want    string
	}{
		{
			// The headline defect: store.ErrAlreadyExists and
			// service.ErrEmojiAlreadyExists share codes.AlreadyExists, and the code
			// test came first, so a duplicate reaction was reported with the emoji
			// error. /reactions.add declares `already_reacted` and does not declare
			// `emoji_already_exists`.
			name: "duplicate reaction",
			want: "already_reacted",
			provoke: func(t *testing.T, handler http.Handler) slackEnvelope {
				timestamp := postParityMessage(t, handler, "reaction target")
				if envelope := parityCall(t, handler, http.MethodPost, "/api/reactions.add", "channel=C1&timestamp="+timestamp+"&name=tada"); !envelope.OK {
					t.Fatalf("first reactions.add: %+v", envelope)
				}
				return parityCall(t, handler, http.MethodPost, "/api/reactions.add", "channel=C1&timestamp="+timestamp+"&name=tada")
			},
		},
		{
			name: "message deleted twice",
			want: "message_not_found",
			provoke: func(t *testing.T, handler http.Handler) slackEnvelope {
				timestamp := postParityMessage(t, handler, "delete target")
				if envelope := parityCall(t, handler, http.MethodPost, "/api/chat.delete", "channel=C1&ts="+timestamp); !envelope.OK {
					t.Fatalf("first chat.delete: %+v", envelope)
				}
				return parityCall(t, handler, http.MethodPost, "/api/chat.delete", "channel=C1&ts="+timestamp)
			},
		},
		{
			name: "unknown channel",
			want: "channel_not_found",
			provoke: func(t *testing.T, handler http.Handler) slackEnvelope {
				return parityCall(t, handler, http.MethodGet, "/api/conversations.history?channel=C_MISSING", "")
			},
		},
		{
			name: "invalid argument",
			want: "invalid_arg_name",
			provoke: func(t *testing.T, handler http.Handler) slackEnvelope {
				timestamp := postParityMessage(t, handler, "bad reaction name")
				return parityCall(t, handler, http.MethodPost, "/api/reactions.add", "channel=C1&timestamp="+timestamp+"&name="+strings.Repeat("x", 300))
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			compositions := parityCompositions(t, seedParityWorkspace)
			answers := make(map[string]slackEnvelope, len(compositions))
			for _, composition := range compositions {
				answers[composition.name] = testCase.provoke(t, composition.handler)
			}
			for name, envelope := range answers {
				if envelope.OK || envelope.Error != testCase.want {
					t.Errorf("%s composition: body=%+v, want ok=false error=%s", name, envelope, testCase.want)
				}
			}
			if answers["local"] != answers["distributed"] {
				t.Errorf("compositions disagree: local=%+v distributed=%+v", answers["local"], answers["distributed"])
			}
		})
	}
}

func TestConversationInviteAtomicAndForcedResultsMatchAcrossCompositions(t *testing.T) {
	seed := func(t *testing.T, target *memory.Store) {
		seedParityWorkspace(t, target)
		for _, user := range []domain.User{
			{ID: "U2", WorkspaceID: "T1", Name: "bob"},
			{ID: "U3", WorkspaceID: "T1", Name: "carol"},
		} {
			if err := target.SeedUser(user); err != nil {
				t.Fatal(err)
			}
		}
		if err := target.SeedConversationMember("C1", "U2"); err != nil {
			t.Fatal(err)
		}
	}
	compositions := parityCompositions(t, seed)
	defaultBodies := make(map[string]string, len(compositions))
	for _, composition := range compositions {
		response := parityRawCall(t, composition.handler, "/api/conversations.invite", "channel=C1&users=U2,U-missing,U3")
		defaultBodies[composition.name] = response
		for _, fragment := range []string{`"ok":false`, `"error":"already_in_channel"`, `"error":"user_not_found"`} {
			if !strings.Contains(response, fragment) {
				t.Errorf("%s default body=%s missing %s", composition.name, response, fragment)
			}
		}
		if member, err := composition.store.IsConversationMember(context.Background(), "C1", "U3"); err != nil || member {
			t.Errorf("%s default U3 member=%v err=%v", composition.name, member, err)
		}
	}
	if defaultBodies["local"] != defaultBodies["distributed"] {
		t.Errorf("default invitation compositions disagree: local=%s distributed=%s", defaultBodies["local"], defaultBodies["distributed"])
	}

	forcedBodies := make(map[string]string, len(compositions))
	for _, composition := range compositions {
		response := parityRawCall(t, composition.handler, "/api/conversations.invite", "channel=C1&users=U-missing,U3&force=true")
		forcedBodies[composition.name] = response
		if !strings.Contains(response, `"ok":true`) {
			t.Errorf("%s forced body=%s", composition.name, response)
		}
		if member, err := composition.store.IsConversationMember(context.Background(), "C1", "U3"); err != nil || !member {
			t.Errorf("%s forced U3 member=%v err=%v", composition.name, member, err)
		}
	}
	if forcedBodies["local"] != forcedBodies["distributed"] {
		t.Errorf("forced invitation compositions disagree: local=%s distributed=%s", forcedBodies["local"], forcedBodies["distributed"])
	}
}

func parityRawCall(t *testing.T, handler http.Handler, path, body string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST %s: status=%d body=%s", path, response.Code, response.Body)
	}
	return response.Body.String()
}

func postParityMessage(t *testing.T, handler http.Handler, text string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/chat.postMessage", strings.NewReader("channel=C1&text="+text))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body struct {
		OK bool   `json:"ok"`
		TS string `json:"ts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.OK || body.TS == "" {
		t.Fatalf("chat.postMessage status=%d body=%s", response.Code, response.Body)
	}
	return body.TS
}

func parityCall(t *testing.T, handler http.Handler, method, path, body string) slackEnvelope {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s %s: status=%d body=%s; a handled failure must be HTTP 200", method, path, response.Code, response.Body)
	}
	var envelope slackEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("%s %s: decode %s: %v", method, path, response.Body, err)
	}
	return envelope
}

// restoredRemoteError is the shape internal/modules/chat/transport/grpc restores
// on the client side: the domain sentinel is available through Unwrap and the
// gRPC status is available through GRPCStatus. It exists so the table below can
// cover error classes that no HTTP request can currently provoke end to end —
// store.ErrIdempotencyConflict is recovered inside the service, and
// store.ErrBookmarkLimit needs a full bookmark quota. TestBothCompositionsNameTheSameFailure
// above proves the shape is the real one by driving an actual gRPC connection.
type restoredRemoteError struct {
	code     codes.Code
	sentinel error
}

func (e restoredRemoteError) Error() string { return e.sentinel.Error() }
func (e restoredRemoteError) Unwrap() error { return e.sentinel }
func (e restoredRemoteError) GRPCStatus() *status.Status {
	return status.New(e.code, e.sentinel.Error())
}

// A gRPC status code is coarser than a domain sentinel: several sentinels share
// one code, and the sentinel is what names the Slack error. Every class the
// transport can restore must therefore be named from the sentinel alone.
func TestEveryRestoredSentinelIsNamedFromTheSentinelNotTheStatusCode(t *testing.T) {
	cases := []struct {
		code     codes.Code
		sentinel error
		want     string
	}{
		{codes.NotFound, store.ErrNotFound, "channel_not_found"},
		{codes.InvalidArgument, store.ErrInvalidArgument, "invalid_name"},
		{codes.InvalidArgument, service.ErrInvalidReaction, "invalid_name"},
		// Two sentinels, one code. The emoji error is not in /reactions.add's enum
		// and `already_reacted` is, so the duplicate-reaction case must not borrow it.
		// A generic collision is now named by the calling operation, so the shared
		// mapper answers the caller's collision code and never another operation's.
		{codes.AlreadyExists, store.ErrAlreadyExists, "already_reacted"},
		{codes.AlreadyExists, service.ErrEmojiAlreadyExists, "emoji_already_exists"},
		// Three sentinels, one code. The code test answered `hash_conflict` for all
		// of them, which shadowed the idempotency contract.
		{codes.Aborted, store.ErrConflict, "hash_conflict"},
		// An Idempotency-Key replayed with a different body can never succeed, so
		// it must not be reported with the one code every SDK retries on.
		{codes.Aborted, store.ErrIdempotencyConflict, "invalid_arg_name"},
		{codes.FailedPrecondition, service.ErrNotInConversation, "not_in_channel"},
		{codes.PermissionDenied, service.ErrMessageNotOwned, "no_permission"},
		{codes.PermissionDenied, service.ErrNotWorkspaceAdmin, "no_permission"},
		{codes.FailedPrecondition, service.ErrMessageAlreadyDeleted, "message_not_found"},
		{codes.ResourceExhausted, store.ErrBookmarkLimit, "too_many_bookmarks"},
		{codes.ResourceExhausted, store.ErrSocketModeConnectionLimit, "socket_mode_unavailable"},
		{codes.Unavailable, service.ErrBlobUnavailable, "file_storage_unavailable"},
	}
	for _, testCase := range cases {
		restored := restoredRemoteError{code: testCase.code, sentinel: testCase.sentinel}
		if reason := mapServiceErrorNamed(restored, "channel_not_found", "invalid_name", "already_reacted"); reason != testCase.want {
			t.Errorf("%s carrying %v = %q, want %q", testCase.code, testCase.sentinel, reason, testCase.want)
		}
		// The same sentinel raised in process must name the same failure, or the two
		// compositions disagree.
		local := fmt.Errorf("wrapped: %w", testCase.sentinel)
		if reason := mapServiceErrorNamed(local, "channel_not_found", "invalid_name", "already_reacted"); reason != testCase.want {
			t.Errorf("in-process %v = %q, want %q", testCase.sentinel, reason, testCase.want)
		}
	}
	// A status with no restorable sentinel must not borrow one: an unclassified
	// remote failure is `fatal_error`, not a guess at a domain cause.
	unclassified := status.Error(codes.Unavailable, "chat service could not complete the request")
	if reason := mapServiceErrorNamed(unclassified, "channel_not_found", "invalid_name", "already_reacted"); reason != "fatal_error" {
		t.Errorf("unclassified remote failure = %q, want fatal_error", reason)
	}
	// An operation whose own enum declares no collision code must not be given
	// another operation's. `already_reacted` appears in exactly one of the 99
	// pinned enums, and it used to be the answer on all of them.
	for _, collision := range []error{store.ErrAlreadyExists, restoredRemoteError{code: codes.AlreadyExists, sentinel: store.ErrAlreadyExists}} {
		if reason := mapServiceError(collision, "channel_not_found"); reason == "already_reacted" {
			t.Errorf("an unnamed collision borrowed reactions.add's code")
		} else if reason != "invalid_arg_name" {
			t.Errorf("unnamed collision = %q, want invalid_arg_name", reason)
		}
	}
	if errors.Is(unclassified, store.ErrNotFound) {
		t.Fatal("the unclassified fixture accidentally carries a sentinel")
	}
}
