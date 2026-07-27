package grpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"path"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	storepkg "github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// This file is the differential harness for the seam.
//
// The architecture claims that "the same module interfaces support direct Go
// calls in monolith mode and generated gRPC adapters in distributed mode". No
// test asserted it: every existing transport test drives the Remote alone and
// compares it against absolute expectations, so a behaviour that the adapter
// changed — an error class the client could not restore, a field a decoder
// dropped, a parameter the client ignored — was invisible until it reached a
// deployment. Ten defects of exactly that shape accumulated behind the gap.
//
// A case here runs one operation twice: once against service.Messages called
// directly, and once against the same implementation behind a real gRPC server
// and client over a bufconn listener, with two independently seeded stores that
// start in the same state. Both outcomes must agree on
//
//   - success or failure,
//   - the classification of a failure against *every* sentinel in the error table
//     (not only the sentinel a case expects, which is what makes a case reject a
//     wrong-but-plausible sentinel such as reactions.add answering
//     service.ErrEmojiAlreadyExists instead of store.ErrAlreadyExists),
//   - the gRPC code the remote failure carries, which must be the code the local
//     failure maps to, and
//   - the value the operation projects.

// chatCaller is everything a caller holds of the chat module.
//
// The auth reads are a separate handle because that is how the composition roots
// wire them: in the monolith they resolve against the store, and in the split
// deployment against the Remote (cmd/server binds tokens, sessions and the
// session revoker to the same value). A case that exercises a token or session
// record has to go through the handle the deployment actually uses.
type chatCaller struct {
	chatapi.Service
	Tokens auth.TokenStore
}

// chatWorld is one composition of the chat module.
type chatWorld struct {
	name    string
	store   *memory.Store
	service chatCaller
}

// parity is the pair of compositions under comparison.
type parity struct {
	local  chatWorld
	remote chatWorld
}

// parityCase is one operation that must behave identically in both
// compositions.
type parityCase struct {
	name string

	// blobs provisions blob storage. A case that leaves it false exercises the
	// service.ErrBlobUnavailable path.
	blobs bool

	// seed prepares a store. It runs once per composition with an empty store, so
	// both compositions start from the same state.
	seed func(t *testing.T, target *memory.Store)

	// operate runs the case against one composition and returns the value to
	// compare. A failure case returns a nil value; a success case must project
	// only values that do not depend on generated identifiers or on wall-clock
	// time, because the two compositions generate their own.
	operate func(ctx context.Context, chat chatCaller) (any, error)

	// wantSentinel is the sentinel both compositions must fail with, or nil when
	// both must succeed.
	wantSentinel error

	// wantAgreedFailure marks a case where both compositions must fail and the
	// class is whatever the implementation gives, which the sweep below then
	// requires them to agree on.
	//
	// It replaces a wantUnclassifiedFailure flag that asserted the *absence* of
	// a domain class. That assertion ratified a defect: a non-positive page
	// bound is refused by a store guard that returns a bare errors.New
	// (internal/store/memory/memory.go "event limit must be positive",
	// internal/store/sqlstore/sqlstore.go "invalid Socket Mode response lease"),
	// so classifyErrors matches nothing and mapError falls through to
	// codes.Unavailable — HTTP 503, which asks a caller to retry a request that
	// can never succeed, for a request that is simply malformed. Nine RPCs
	// answer that way. Locking it in with a test meant giving those store guards
	// a sentinel would turn the suite red for doing the right thing. This flag
	// keeps the parity requirement, which is what the harness is for, and stops
	// asserting the class is missing; it passes before and after the store is
	// fixed. The fix itself belongs to internal/store and is reported.
	wantAgreedFailure bool
}

func seedBaseline(t *testing.T, target *memory.Store) {
	t.Helper()
	requireSeed(t, target.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}))
	requireSeed(t, target.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com", RealName: "Alice Example", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:"}}))
	requireSeed(t, target.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob", Email: "bob@example.com"}))
	// UA is the workspace administrator. Administrative operations are refused to
	// U1 and U2, who are plain members, so a case that drives one has to name the
	// authority it is claiming instead of relying on membership alone.
	requireSeed(t, target.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin", Email: "admin@example.com"}))
	requireSeed(t, seedWorkspaceRole(target, "T1", "UA", domain.WorkspaceRoleAdmin))
	requireSeed(t, target.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}))
	requireSeed(t, target.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "second"}))
	requireSeed(t, target.SeedConversationMember("C1", "U1"))
	requireSeed(t, target.SeedConversationMember("C1", "U2"))
	requireSeed(t, target.SeedConversationMember("C2", "U1"))
}

// seedWorkspaceRole promotes a seeded user. memory.Store.SeedUser creates a
// member membership, which is deliberately not enough for any administrative
// operation.
func seedWorkspaceRole(target *memory.Store, workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole) error {
	event, err := events.New(domain.EventID("evt_seed_role_"+string(userID)), workspaceID, "", events.NewPayload("workspace.role_changed", events.String("user_id", string(userID)), events.String("role", string(role))), time.Now().UTC())
	if err != nil {
		return err
	}
	return target.SetWorkspaceRole(context.Background(), workspaceID, userID, role, event)
}

func requireSeed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func newParity(t *testing.T, testCase parityCase) parity {
	t.Helper()
	seed := testCase.seed
	if seed == nil {
		seed = seedBaseline
	}
	build := func(name string) chatWorld {
		target := memory.New()
		seed(t, target)
		implementation := service.Messages{Store: target}
		if testCase.blobs {
			blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
			if err != nil {
				t.Fatalf("blob storage: %v", err)
			}
			implementation.Blob = blobs
		}
		return chatWorld{name: name, store: target, service: chatCaller{Service: implementation, Tokens: target}}
	}

	local := build("local")
	remote := build("remote")

	server, err := NewChatServer(remote.service, remote.store, remote.store, remote.store, Observer{})
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
	connection, err := grpclib.NewClient("passthrough:///bufnet", dial...)
	if err != nil {
		t.Fatalf("chat client: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	adapter, err := NewRemote(connection)
	if err != nil {
		t.Fatalf("chat remote: %v", err)
	}
	remote.service = chatCaller{Service: adapter, Tokens: adapter}
	return parity{local: local, remote: remote}
}

func TestCompositionsAgreeOnEveryErrorClassAndValue(t *testing.T) {
	for _, testCase := range parityCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			pair := newParity(t, testCase)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			localValue, localErr := testCase.operate(ctx, pair.local.service)
			remoteValue, remoteErr := testCase.operate(ctx, pair.remote.service)

			if (localErr == nil) != (remoteErr == nil) {
				t.Fatalf("local error = %v, remote error = %v: one composition failed and the other did not", localErr, remoteErr)
			}
			switch {
			case testCase.wantAgreedFailure:
				if localErr == nil {
					t.Fatal("both compositions succeeded, want a failure")
				}
			case testCase.wantSentinel == nil:
				if localErr != nil {
					t.Fatalf("both compositions failed with %v, want success", localErr)
				}
			default:
				if localErr == nil {
					t.Fatalf("both compositions succeeded, want %v", testCase.wantSentinel)
				}
				if !errors.Is(localErr, testCase.wantSentinel) {
					t.Fatalf("local error %v is not %v; the case no longer provokes the class it documents", localErr, testCase.wantSentinel)
				}
				if !errors.Is(remoteErr, testCase.wantSentinel) {
					t.Fatalf("remote error %v is not %v", remoteErr, testCase.wantSentinel)
				}
			}

			// The sweep is the point of the harness: the two compositions must
			// agree about every sentinel, so restoring a plausible neighbour
			// (service.ErrEmojiAlreadyExists for store.ErrAlreadyExists) fails
			// here even though the case only names one sentinel.
			for _, class := range errorClasses {
				if errors.Is(localErr, class.sentinel) != errors.Is(remoteErr, class.sentinel) {
					t.Errorf("errors.Is(err, %s): local = %t, remote = %t (local %v, remote %v)",
						class.key, errors.Is(localErr, class.sentinel), errors.Is(remoteErr, class.sentinel), localErr, remoteErr)
				}
			}
			if localErr != nil {
				if class, classified := classifyError(localErr); classified {
					if got := status.Code(remoteErr); got != class.code {
						t.Errorf("remote status code = %s, want %s (the code the local error maps to)", got, class.code)
					}
				}
			}
			if !reflect.DeepEqual(localValue, remoteValue) {
				t.Fatalf("projected value differs:\n local  = %#v\n remote = %#v", localValue, remoteValue)
			}
		})
	}
}

func parityCases() []parityCase {
	timestampOf := func(message domain.Message) domain.MessageTimestamp {
		return domain.NewMessageTimestamp(message.CreatedAt)
	}
	return []parityCase{
		// The privilege-escalation class. A member calling an administrative
		// mutation must be refused with the same sentinel and the same gRPC code in
		// both compositions; a refusal that arrives remotely as codes.Unavailable
		// reads as "retry", which is how an escalation attempt would look like a
		// transient failure instead of a denial.
		{
			name:         "a member cannot promote themselves",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return nil, chat.SetUserRole(ctx, "T1", "U1", "U1", domain.WorkspaceRoleOwner)
			},
		},
		{
			name:         "a member cannot list the workspace directory administratively",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.AdminListUsers(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				return nil, err
			},
		},
		{
			name:         "a member cannot rename the workspace",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.AdminSetWorkspaceName(ctx, "T1", "U1", "Taken Over")
				return nil, err
			},
		},
		{
			name: "an administrator promotes a member",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.SetUserRole(ctx, "T1", "UA", "U1", domain.WorkspaceRoleAdmin); err != nil {
					return nil, err
				}
				membership, err := chat.WorkspaceMembership(ctx, "T1", "U1", "U1")
				return membership, err
			},
		},
		{
			name: "a member reads their own membership",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return chat.WorkspaceMembership(ctx, "T1", "U1", "U1")
			},
		},
		{
			name:         "a member cannot read another member's membership",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.WorkspaceMembership(ctx, "T1", "U1", "U2")
				return nil, err
			},
		},
		{
			name: "an administrator reads another member's membership",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return chat.WorkspaceMembership(ctx, "T1", "UA", "U2")
			},
		},
		{
			name:         "membership of an unknown user is absent, not forbidden",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.WorkspaceMembership(ctx, "T1", "UA", "U-missing")
				return nil, err
			},
		},
		{
			// The sign-in operations take no actor at all, so they must succeed in
			// both compositions without any member being able to reach them through
			// the HTTP surface. The projection omits the generated identifier.
			name: "external provisioning creates a user and synchronises its role",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				user, err := chat.ProvisionExternalUser(ctx, "T1", " Carol@Example.COM ", "Carol Example", domain.WorkspaceRoleMember)
				if err != nil {
					return nil, err
				}
				if err := chat.SynchronizeExternalUserRole(ctx, "T1", user.ID, domain.WorkspaceRoleAdmin); err != nil {
					return nil, err
				}
				membership, err := chat.WorkspaceMembership(ctx, "T1", user.ID, user.ID)
				if err != nil {
					return nil, err
				}
				return []any{user.Email, user.RealName, membership.Role, membership.Active}, nil
			},
		},
		{
			name:         "external provisioning rejects a duplicate address",
			wantSentinel: storepkg.ErrAlreadyExists,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.ProvisionExternalUser(ctx, "T1", "alice@example.com", "Alice Again", domain.WorkspaceRoleMember)
				return nil, err
			},
		},
		{
			name:         "external role synchronisation rejects an unknown user",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return nil, chat.SynchronizeExternalUserRole(ctx, "T1", "U-missing", domain.WorkspaceRoleAdmin)
			},
		},
		{
			name:         "post rejects empty text",
			wantSentinel: service.ErrInvalidMessage,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.Post(ctx, "T1", "U1", "C1", "", "", "")
				return nil, err
			},
		},
		{
			name:         "post to an unknown conversation",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.Post(ctx, "T1", "U1", "C-missing", "hello", "", "")
				return nil, err
			},
		},
		{
			name:         "search rejects an empty query",
			wantSentinel: service.ErrInvalidSearch,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.Search(ctx, "T1", "U1", "   ", domain.PageRequest{Limit: 10})
				return nil, err
			},
		},
		{
			name:         "history rejects an undecodable cursor",
			wantSentinel: domain.ErrInvalidCursor,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10, Cursor: "not-a-cursor"})
				return nil, err
			},
		},
		{
			name:         "reaction rejects an invalid name",
			wantSentinel: service.ErrInvalidReaction,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "reactable", "", "")
				if err != nil {
					return nil, err
				}
				return nil, chat.AddReaction(ctx, "T1", "U1", "C1", timestampOf(message), "  ")
			},
		},
		{
			// The duplicate reaction is store.ErrAlreadyExists, which the Slack
			// API answers with already_reacted. Collapsing it onto the code alone
			// made the split deployment answer emoji_already_exists, a code that
			// method does not define.
			name:         "duplicate reaction",
			wantSentinel: storepkg.ErrAlreadyExists,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "reactable", "", "")
				if err != nil {
					return nil, err
				}
				if err := chat.AddReaction(ctx, "T1", "U1", "C1", timestampOf(message), "thumbsup"); err != nil {
					return nil, err
				}
				return nil, chat.AddReaction(ctx, "T1", "U1", "C1", timestampOf(message), "thumbsup")
			},
		},
		{
			name:         "duplicate custom emoji",
			wantSentinel: service.ErrEmojiAlreadyExists,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if err := chat.AdminAddEmoji(ctx, "T1", "UA", "party", "https://example.test/party.png"); err != nil {
					return nil, err
				}
				return nil, chat.AdminAddEmoji(ctx, "T1", "UA", "party", "https://example.test/party.png")
			},
		},
		{
			name:         "custom emoji rejects an empty name",
			wantSentinel: service.ErrInvalidEmoji,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return nil, chat.AdminAddEmoji(ctx, "T1", "UA", "  ", "https://example.test/party.png")
			},
		},
		{
			name:         "presence rejects an unknown value",
			wantSentinel: service.ErrInvalidPresence,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.SetUserPresence(ctx, "T1", "U1", domain.Presence("sleepy"))
				return nil, err
			},
		},
		{
			name:         "profile rejects an oversized display name",
			wantSentinel: service.ErrInvalidProfile,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.SetUserProfile(ctx, "T1", "U1", domain.UserProfile{DisplayName: string(bytes.Repeat([]byte("a"), 81))})
				return nil, err
			},
		},
		{
			name:         "conversation rejects an empty name",
			wantSentinel: service.ErrInvalidConversation,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.CreateConversation(ctx, "T1", "U1", "   ", false)
				return nil, err
			},
		},
		{
			name:         "bookmark rejects an unsupported type",
			wantSentinel: service.ErrInvalidBookmark,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.AddBookmark(ctx, "T1", "U1", "C1", "Title", "video", "https://example.test", ":link:", "", "", "")
				return nil, err
			},
		},
		{
			// store.ErrBookmarkLimit had no case in the server mapping at all, so
			// the limit answered 503 remotely and 400 in process.
			name:         "bookmark limit",
			wantSentinel: storepkg.ErrBookmarkLimit,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				for index := 0; index < domain.MaxBookmarksPerConversation; index++ {
					if _, err := chat.AddBookmark(ctx, "T1", "U1", "C1", fmt.Sprintf("Bookmark %d", index), "link", "https://example.test", "", "", "", ""); err != nil {
						return nil, err
					}
				}
				_, err := chat.AddBookmark(ctx, "T1", "U1", "C1", "One too many", "link", "https://example.test", "", "", "", "")
				return nil, err
			},
		},
		{
			name:         "update rejects a message owned by another user",
			wantSentinel: service.ErrMessageNotOwned,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U2", "C1", "bob wrote this", "", "")
				if err != nil {
					return nil, err
				}
				_, err = chat.Update(ctx, "T1", "U1", "C1", timestampOf(message), "alice rewrites it")
				return nil, err
			},
		},
		{
			name:         "delete rejects an already deleted message",
			wantSentinel: service.ErrMessageAlreadyDeleted,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "delete me", "", "")
				if err != nil {
					return nil, err
				}
				if _, err := chat.Delete(ctx, "T1", "U1", "C1", timestampOf(message)); err != nil {
					return nil, err
				}
				_, err = chat.Delete(ctx, "T1", "U1", "C1", timestampOf(message))
				return nil, err
			},
		},
		{
			// The reason commit 1d2ac46 exists: at the limit the ticket is valid
			// and the caller must release a connection, so the HTTP layer answers
			// 429. Without the sentinel it answered 401 and sent the client off to
			// re-authenticate.
			name:         "socket mode connection limit",
			wantSentinel: storepkg.ErrSocketModeConnectionLimit,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// A ticket is inactive until it is dialled, so consumption is what
				// the limit counts.
				for index := 0; index <= domain.SocketModeConnectionLimit; index++ {
					identifier := fmt.Sprintf("socket-%d", index)
					if err := chat.CreateSocketModeConnection(ctx, domain.SocketModeConnection{
						ID: identifier, AppID: "A1", ExpiresAt: time.Now().UTC().Add(time.Minute),
					}); err != nil {
						return nil, err
					}
					if _, err := chat.ConsumeSocketModeConnection(ctx, identifier); err != nil {
						return nil, err
					}
				}
				return nil, nil
			},
		},
		{
			name:         "upload without blob storage",
			wantSentinel: service.ErrBlobUnavailable,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", 5, bytes.NewReader([]byte("hello")))
				return nil, err
			},
		},
		// The malformed-field-plus-unauthorised-caller class. The transport used
		// to reject a missing required field before the implementation ran, so a
		// caller learned that its *field* was wrong from a request the
		// implementation would have refused for a reason it is not allowed to
		// know. files.upload with an empty title answered channel_not_found in
		// the monolith and invalid_arg_name in the split deployment; with blob
		// storage down it answered file_storage_unavailable locally and
		// invalid_arg_name remotely. Every seam guard that duplicated an
		// implementation check is deleted, and these cases keep them deleted.
		{
			name:         "upload with an empty title by a caller outside the workspace",
			blobs:        true,
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.UploadFile(ctx, "T1", "U-missing", "notes.txt", "", "text/plain", 5, bytes.NewReader([]byte("hello")))
				return nil, err
			},
		},
		{
			name:         "upload with an empty title while blob storage is down",
			wantSentinel: service.ErrBlobUnavailable,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.UploadFile(ctx, "T1", "U1", "notes.txt", "", "text/plain", 5, bytes.NewReader([]byte("hello")))
				return nil, err
			},
		},
		{
			name:         "canvas edit with empty changes by a caller outside the workspace",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				return nil, chat.EditCanvas(ctx, "T1", "U-missing", "CV1", "")
			},
		},
		{
			name:         "an unknown workspace is absent, not malformed",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.WorkspaceInfo(ctx, "", "U1")
				return nil, err
			},
		},
		// The page-bound class. protoPageRequest rejected a limit outside 1..200
		// on the seam only, so a limit of 201 returned a page in the monolith and
		// invalid_arg_name in the split deployment, and a limit of 0 failed with
		// two different classes. No parity case varied a limit, which is why 24
		// call sites carried the divergence.
		{
			name: "a page limit above the old seam bound",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "paged", "", ""); err != nil {
					return nil, err
				}
				history, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 201})
				if err != nil {
					return nil, err
				}
				conversations, err := chat.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 500})
				if err != nil {
					return nil, err
				}
				users, err := chat.Users(ctx, "T1", "U1", domain.PageRequest{Limit: 1000})
				if err != nil {
					return nil, err
				}
				return []any{len(history.Messages), history.HasMore, len(conversations.Conversations), len(users.Users)}, nil
			},
		},
		{
			// Both compositions must refuse a non-positive limit the same way.
			// The store owns the rule, so the refusal is the store's and neither
			// composition invents one of its own.
			//
			// The refusal reaches a remote caller as codes.Unavailable today,
			// because the store guard carries no sentinel; that is a defect in
			// the guard, not a contract, and this case deliberately does not
			// assert it either way. See wantAgreedFailure.
			name:              "a page limit of zero",
			wantAgreedFailure: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 0})
				return nil, err
			},
		},
		{
			name:         "unknown external identity",
			wantSentinel: storepkg.ErrNotFound,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.GetExternalIdentity(ctx, "T1", "oidc", "missing-subject")
				return nil, err
			},
		},
		{
			name:         "replayed provider logout token",
			wantSentinel: storepkg.ErrConflict,
			seed: func(t *testing.T, target *memory.Store) {
				t.Helper()
				seedBaseline(t, target)
				requireSeed(t, target.SeedSession(context.Background(), "session-token", domain.SessionRecord{
					WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}, ExpiresAt: time.Now().UTC().Add(time.Hour),
					OIDCProvider: "oidc", OIDCSubject: "subject", OIDCSID: "provider-session",
				}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				expiry := time.Now().UTC().Add(time.Minute)
				if err := chat.RevokeOIDCSessions(ctx, "T1", "oidc", "", "provider-session", "logout-token", expiry); err != nil {
					return nil, err
				}
				return nil, chat.RevokeOIDCSessions(ctx, "T1", "oidc", "", "provider-session", "logout-token", expiry)
			},
		},
		{
			// The decoders used to require presence to be exactly "auto" or "away"
			// and the profile to be present, invariants the local path does not
			// have: a stored record without them was a value in process and a hard
			// error across the seam.
			name: "user record without presence or profile",
			seed: func(t *testing.T, target *memory.Store) {
				t.Helper()
				seedBaseline(t, target)
				requireSeed(t, target.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				user, err := chat.UserInfo(ctx, "T1", "U1", "U3")
				if err != nil {
					return nil, err
				}
				return []any{user.ID, user.Name, user.Presence, user.Profile}, nil
			},
		},
		{
			// decodeProtoToken and decodeProtoSession rejected a record with no
			// scopes, which the store returns.
			name: "token and session records without scopes",
			seed: func(t *testing.T, target *memory.Store) {
				t.Helper()
				seedBaseline(t, target)
				requireSeed(t, target.SeedToken(context.Background(), "api-token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1"}))
				requireSeed(t, target.SeedSession(context.Background(), "session-token", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Second)}))
			},
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				token, err := chat.Tokens.LookupToken(ctx, "api-token")
				if err != nil {
					return nil, err
				}
				session, err := chat.LookupSession(ctx, "session-token")
				if err != nil {
					return nil, err
				}
				return []any{token.WorkspaceID, token.UserID, len(token.Scopes), session.UserID, len(session.Scopes), session.ExpiresAt.UTC()}, nil
			},
		},
		{
			name: "post then read history",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "first", "", ""); err != nil {
					return nil, err
				}
				if _, err := chat.Post(ctx, "T1", "U1", "C1", "second", "", "key-1"); err != nil {
					return nil, err
				}
				page, err := chat.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				texts := make([]string, 0, len(page.Messages))
				for _, message := range page.Messages {
					texts = append(texts, message.Text)
				}
				return []any{texts, page.HasMore}, nil
			},
		},
		{
			name: "user lookups return the stored record",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				byID, err := chat.UserInfo(ctx, "T1", "U1", "U1")
				if err != nil {
					return nil, err
				}
				byEmail, err := chat.UserByEmail(ctx, "T1", "U1", "ALICE@EXAMPLE.COM")
				if err != nil {
					return nil, err
				}
				return []any{byID, byEmail}, nil
			},
		},
		{
			// The client used to drop the conversationID parameter and let the
			// server re-derive the target from the payload, so a call whose
			// parameter and payload disagree mutated C1 in process and C2 across
			// the seam.
			name: "conversation preferences target the parameter, not the payload",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				prefs := domain.ConversationPrefs{
					ConversationID: "C2",
					CanThread:      domain.ConversationPreferenceList{Types: []domain.ConversationPreferenceType{"admin"}},
				}
				if _, err := chat.AdminSetConversationPrefs(ctx, "T1", "UA", "C1", prefs); err != nil {
					return nil, err
				}
				first, err := chat.AdminGetConversationPrefs(ctx, "T1", "UA", "C1")
				if err != nil {
					return nil, err
				}
				second, err := chat.AdminGetConversationPrefs(ctx, "T1", "UA", "C2")
				if err != nil {
					return nil, err
				}
				return []any{first.CanThread.Types, second.CanThread.Types}, nil
			},
		},
		{
			// The server discarded the user the module resolved and the client
			// fabricated an identifier-only record, so every other field of the
			// returned user was empty across the seam.
			name:  "user photo download returns the stored user",
			blobs: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				// The bytes have to sniff as the declared type: internal/service
				// now reads the leading bytes and refuses a photo whose content
				// does not match what the caller declared, so a repeated
				// four-byte stand-in is no longer a PNG to either composition.
				photo := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 64)...)
				if _, err := chat.SetUserPhoto(ctx, "T1", "U1", "image/png", int64(len(photo)), bytes.NewReader(photo)); err != nil {
					return nil, err
				}
				user, err := chat.UserInfo(ctx, "T1", "U1", "U1")
				if err != nil {
					return nil, err
				}
				// The photo token is minted per composition, so it is an input
				// here and never part of the projection.
				opened, reader, err := chat.OpenUserPhoto(ctx, "T1", "U1", path.Base(user.Profile.Image512))
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				content, err := io.ReadAll(reader)
				if err != nil {
					return nil, err
				}
				return []any{
					opened.ID, opened.WorkspaceID, opened.Email, opened.Name, opened.RealName,
					opened.Presence, opened.Deleted, opened.Profile.DisplayName,
					opened.Profile.StatusText, opened.Profile.StatusEmoji,
					bytes.Equal(content, photo),
				}, nil
			},
		},
		{
			name:  "external upload ticket",
			blobs: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				upload, err := chat.CreateExternalUpload(ctx, "T1", "U1", "external.txt", "text/plain", 5, time.Minute)
				if err != nil {
					return nil, err
				}
				if err := chat.UploadExternalFile(ctx, upload.ID, 5, bytes.NewReader([]byte("bytes"))); err != nil {
					return nil, err
				}
				file, err := chat.CompleteExternalUpload(ctx, "T1", "U1", upload.ID, "External", []domain.ConversationID{"C1"}, "shared", "", "")
				if err != nil {
					return nil, err
				}
				return []any{
					upload.Name, upload.MIMEType, upload.Size, upload.Status,
					upload.UploadedAt.IsZero(), upload.CompletedAt.IsZero(),
					file.Name, file.Title, file.Size, file.SharedChannels,
				}, nil
			},
		},
		{
			name:  "file upload, download and listing",
			blobs: true,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				content := bytes.Repeat([]byte("chunked-"), 20000)
				file, err := chat.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", int64(len(content)), bytes.NewReader(content))
				if err != nil {
					return nil, err
				}
				opened, reader, err := chat.OpenFile(ctx, "T1", "U1", file.ID)
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				readBack, err := io.ReadAll(reader)
				if err != nil {
					return nil, err
				}
				page, err := chat.Files(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{file.Name, file.Title, file.Size, opened.Name, bytes.Equal(readBack, content), len(page.Files), page.HasMore}, nil
			},
		},
		{
			name: "bookmark lifecycle",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				bookmark, err := chat.AddBookmark(ctx, "T1", "U1", "C1", "Docs", "link", "https://example.test/docs", ":book:", "", "", "")
				if err != nil {
					return nil, err
				}
				edited, err := chat.EditBookmark(ctx, "T1", "U1", "C1", bookmark.ID, domain.BookmarkUpdate{Title: "Handbook", SetTitle: true})
				if err != nil {
					return nil, err
				}
				listed, err := chat.Bookmarks(ctx, "T1", "U1", "C1")
				if err != nil {
					return nil, err
				}
				titles := make([]string, 0, len(listed))
				for _, item := range listed {
					titles = append(titles, item.Title)
				}
				return []any{bookmark.Title, bookmark.Link, bookmark.Emoji, edited.Title, titles}, nil
			},
		},
		{
			name: "presence and profile mutations",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				away, err := chat.SetUserPresence(ctx, "T1", "U1", domain.PresenceAway)
				if err != nil {
					return nil, err
				}
				updated, err := chat.SetUserProfile(ctx, "T1", "U1", domain.UserProfile{DisplayName: "alice2", StatusText: "Focusing", StatusEmoji: ":dart:"})
				if err != nil {
					return nil, err
				}
				return []any{away.Presence, updated.Profile}, nil
			},
		},
		{
			name: "conversation membership and cursor",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "read me", "", "")
				if err != nil {
					return nil, err
				}
				cursor, err := chat.MarkRead(ctx, "T1", "U1", "C1", timestampOf(message))
				if err != nil {
					return nil, err
				}
				members, err := chat.ConversationMembers(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				identifiers := make([]domain.UserID, 0, len(members.Users))
				for _, member := range members.Users {
					identifiers = append(identifiers, member.ID)
				}
				return []any{cursor.Conversation, cursor.LastRead == timestampOf(message), identifiers, members.HasMore}, nil
			},
		},
		{
			name: "reminders and scheduled messages",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				reminder, err := chat.AddReminder(ctx, "T1", "U1", "", "water the plants", time.Now().UTC().Add(time.Hour))
				if err != nil {
					return nil, err
				}
				page, err := chat.Reminders(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				scheduled, err := chat.ScheduleMessage(ctx, "T1", "U1", "C1", "later", time.Now().UTC().Add(time.Hour))
				if err != nil {
					return nil, err
				}
				scheduledPage, err := chat.ScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{reminder.Text, len(page.Reminders), page.HasMore, scheduled.Text, len(scheduledPage.Items)}, nil
			},
		},
		{
			name: "lists",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				list, err := chat.CreateList(ctx, "T1", "U1", "Groceries", "", "[]", "", false, false)
				if err != nil {
					return nil, err
				}
				item, err := chat.CreateListItem(ctx, "T1", "U1", list.ID, "", `[{"column_id":"title","value":"milk"}]`)
				if err != nil {
					return nil, err
				}
				items, err := chat.ListItems(ctx, "T1", "U1", list.ID, domain.PageRequest{Limit: 10}, false)
				if err != nil {
					return nil, err
				}
				fields := make([]string, 0, len(items.Items))
				for _, entry := range items.Items {
					fields = append(fields, entry.Fields)
				}
				return []any{list.Name, list.Schema, item.Fields, fields, items.HasMore}, nil
			},
		},
		{
			// The user-group mutations are administrative: creating one and
			// setting its membership is workspace administration, so they run as
			// UA. A member is refused, which the case below asserts in both
			// compositions.
			name: "user groups",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				group, err := chat.CreateUserGroup(ctx, "T1", "UA", "Engineers", "engineers", "builds things")
				if err != nil {
					return nil, err
				}
				withUsers, err := chat.SetUserGroupUsers(ctx, "T1", "UA", group.ID, []domain.UserID{"U1", "U2"})
				if err != nil {
					return nil, err
				}
				page, err := chat.ListUserGroups(ctx, "T1", "U1", false, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				return []any{group.Name, group.Handle, group.Description, withUsers.Users, len(page.Groups)}, nil
			},
		},
		{
			name:         "a member cannot create a user group",
			wantSentinel: service.ErrNotWorkspaceAdmin,
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				_, err := chat.CreateUserGroup(ctx, "T1", "U1", "Engineers", "engineers", "builds things")
				return nil, err
			},
		},
		{
			name: "reactions, pins and stars",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				message, err := chat.Post(ctx, "T1", "U1", "C1", "decorate me", "", "")
				if err != nil {
					return nil, err
				}
				timestamp := timestampOf(message)
				if err := chat.AddReaction(ctx, "T1", "U1", "C1", timestamp, "thumbsup"); err != nil {
					return nil, err
				}
				if err := chat.AddPin(ctx, "T1", "U1", "C1", timestamp); err != nil {
					return nil, err
				}
				if err := chat.AddStar(ctx, "T1", "U1", "C1", timestamp); err != nil {
					return nil, err
				}
				reactions, _, reactionsMore, err := chat.Reactions(ctx, "T1", "U1", "C1", timestamp, domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				pins, _, pinsMore, err := chat.Pins(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				stars, _, starsMore, err := chat.Stars(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(reactions))
				for _, reaction := range reactions {
					names = append(names, reaction.Name)
				}
				return []any{names, reactionsMore, len(pins), pinsMore, len(stars), starsMore}, nil
			},
		},
		{
			name: "workspace and conversation listings",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				workspace, err := chat.WorkspaceInfo(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				conversations, err := chat.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				names := make([]string, 0, len(conversations.Conversations))
				for _, conversation := range conversations.Conversations {
					names = append(names, conversation.Name)
				}
				users, err := chat.Users(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
				if err != nil {
					return nil, err
				}
				identifiers := make([]domain.UserID, 0, len(users.Users))
				for _, user := range users.Users {
					identifiers = append(identifiers, user.ID)
				}
				return []any{workspace.ID, workspace.Name, names, identifiers, conversations.HasMore, users.HasMore}, nil
			},
		},
		{
			name: "do not disturb",
			operate: func(ctx context.Context, chat chatCaller) (any, error) {
				initial, err := chat.DoNotDisturbInfo(ctx, "T1", "U1", "")
				if err != nil {
					return nil, err
				}
				snoozed, err := chat.SetSnooze(ctx, "T1", "U1", 5)
				if err != nil {
					return nil, err
				}
				ended, err := chat.EndSnooze(ctx, "T1", "U1")
				if err != nil {
					return nil, err
				}
				reference := time.Now().UTC()
				return []any{initial.Enabled, snoozed.SnoozeEnabled(reference), ended.SnoozeEnabled(reference)}, nil
			},
		},
	}
}

// TestEveryChatMethodHasAParityCaseOrADocumentedGap derives the coverage of this
// harness instead of trusting the case list to be complete.
//
// parityCases is hand-written, and the same change that added it made the
// converter property derived (TestEveryConverterPairIsExercisedByTheProperty)
// precisely because a hand-written list "could not see" what was missing. The
// argument applies here verbatim and was not applied: 96 transport guards were
// deleted on the claim that "the implementation owns the answer, so both
// compositions agree", which is exactly what this harness proves, and not one
// deleted guard got a case. Nine RPCs turned a malformed request from HTTP 400
// into HTTP 503 and nothing here saw it.
//
// The method set is read by reflection, and the methods a case exercises are
// read from this file's own source, so a method added to chatapi.Service fails
// here until it is either exercised or listed with the others.
func TestEveryChatMethodHasAParityCaseOrADocumentedGap(t *testing.T) {
	exercised := methodsExercisedByParityCases(t)
	serviceType := reflect.TypeOf((*chatapi.Service)(nil)).Elem()
	for index := range serviceType.NumMethod() {
		name := serviceType.Method(index).Name
		_, isGap := parityGaps[name]
		switch {
		case exercised[name] && isGap:
			t.Errorf("chatapi.Service.%s is exercised by a parity case and also listed in parityGaps; remove the entry", name)
		case !exercised[name] && !isGap:
			t.Errorf("chatapi.Service.%s crosses the seam with no parity case. Add a case to parityCases, or add %q to parityGaps and say why it cannot have one.", name, name)
		}
	}
	for name := range parityGaps {
		if _, exists := serviceType.MethodByName(name); !exists {
			t.Errorf("parityGaps names %s, which chatapi.Service no longer declares", name)
		}
	}
}

// methodsExercisedByParityCases reads the calls parityCases makes on the caller
// handle. A case that calls chat.X or chat.Tokens.X exercises X.
func methodsExercisedByParityCases(t *testing.T) map[string]bool {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "differential_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cases *ast.FuncDecl
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "parityCases" {
			cases = function
		}
	}
	if cases == nil {
		t.Fatal("differential_test.go declares no parityCases")
	}
	exercised := map[string]bool{}
	ast.Inspect(cases, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch receiver := selector.X.(type) {
		case *ast.Ident:
			if receiver.Name == "chat" {
				exercised[selector.Sel.Name] = true
			}
		case *ast.SelectorExpr:
			if identifier, ok := receiver.X.(*ast.Ident); ok && identifier.Name == "chat" {
				exercised[selector.Sel.Name] = true
			}
		}
		return true
	})
	if len(exercised) == 0 {
		t.Fatal("no parity case calls the chat handle; the scan is reading the wrong thing")
	}
	return exercised
}

// parityGaps is the backlog this harness has not covered yet.
//
// It is not an exemption list: every entry is a method whose two compositions
// have never been compared, which is the state the whole seam was in before the
// harness existed. It exists so the gap is *enumerated and derived* rather than
// invisible — the set can only shrink, a method added to chatapi.Service cannot
// join it silently, and a method whose parity case is deleted reappears here as
// a failure.
//
// Priority for closing it, from the failures the deleted guards produced: the
// methods that take a page bound, a timestamp in nanoseconds, or an identifier
// the store treats as optional.
var parityGaps = map[string]struct{}{
	"AckSocketModeResponses":                  {},
	"AcknowledgeEntityCommentAction":          {},
	"AddCall":                                 {},
	"AddCallParticipants":                     {},
	"AddRemoteFile":                           {},
	"AddUserGroupChannels":                    {},
	"AdminAddConversationAccessGroup":         {},
	"AdminAddEmojiAlias":                      {},
	"AdminAddUserGroupTeams":                  {},
	"AdminApproveApp":                         {},
	"AdminApproveInviteRequest":               {},
	"AdminAssignUser":                         {},
	"AdminConnectedChannelInfo":               {},
	"AdminConversationTeams":                  {},
	"AdminConvertConversationToPrivate":       {},
	"AdminCreateIncomingWebhook":              {},
	"AdminCreateUser":                         {},
	"AdminCreateWorkspace":                    {},
	"AdminDeleteConversation":                 {},
	"AdminDenyInviteRequest":                  {},
	"AdminDisconnectSharedConversation":       {},
	"AdminInviteConversationMembers":          {},
	"AdminInviteUser":                         {},
	"AdminListApps":                           {},
	"AdminListConversationAccessGroups":       {},
	"AdminListInviteRequests":                 {},
	"AdminRemoveConversationAccessGroup":      {},
	"AdminRemoveEmoji":                        {},
	"AdminRenameConversation":                 {},
	"AdminRenameEmoji":                        {},
	"AdminRestrictApp":                        {},
	"AdminSearchConversations":                {},
	"AdminSetConversationArchived":            {},
	"AdminSetConversationTeams":               {},
	"AdminSetIncomingWebhookEnabled":          {},
	"AdminSetWorkspaceDefaultChannels":        {},
	"AdminSetWorkspaceDescription":            {},
	"AdminSetWorkspaceDiscoverability":        {},
	"AdminSetWorkspaceIcon":                   {},
	"AdminTeamUsers":                          {},
	"BotInfo":                                 {},
	"ClaimSocketModeResponses":                {},
	"CompleteExternalUploads":                 {},
	"CompleteReminder":                        {},
	"ConsumeRTMConnection":                    {},
	"ConversationInfo":                        {},
	"CountSocketModeConnections":              {},
	"CreateAppInstallation":                   {},
	"CreateCanvas":                            {},
	"CreateExternalIdentity":                  {},
	"CreateRTMConnection":                     {},
	"CreateSession":                           {},
	"DeleteCanvas":                            {},
	"DeleteCanvasAccess":                      {},
	"DeleteFile":                              {},
	"DeleteFileComment":                       {},
	"DeleteListAccess":                        {},
	"DeleteListItems":                         {},
	"DeleteReminder":                          {},
	"DeleteScheduledMessage":                  {},
	"DeleteUserPhoto":                         {},
	"Emojis":                                  {},
	"EndCall":                                 {},
	"EndDND":                                  {},
	"FileInfo":                                {},
	"GetAuthMethod":                           {},
	"GetCall":                                 {},
	"GetListDownload":                         {},
	"GetListItem":                             {},
	"GetSocketModeCursor":                     {},
	"IntegrationLogs":                         {},
	"InviteConversationMembers":               {},
	"JoinConversation":                        {},
	"KickConversationMember":                  {},
	"LeaveConversation":                       {},
	"ListAccessLogs":                          {},
	"ListAppEventsAfter":                      {},
	"ListAppInstallations":                    {},
	"ListEventsAfter":                         {},
	"LookupAppToken":                          {},
	"LookupCanvasSections":                    {},
	"MigrationExchange":                       {},
	"OAuthExchange":                           {},
	"OpenConversation":                        {},
	"OpenDialog":                              {},
	"OpenIDConnectToken":                      {},
	"OpenIDConnectUserInfo":                   {},
	"OpenPublicFile":                          {},
	"OpenView":                                {},
	"Permalink":                               {},
	"PostEphemeral":                           {},
	"PostEphemeralWithBlocks":                 {},
	"PostEphemeralWithBlocksAndAttachments":   {},
	"PostIncomingWebhook":                     {},
	"PostIncomingWebhookWithAttachments":      {},
	"PostWithBlocks":                          {},
	"PostWithBlocksAndAttachments":            {},
	"PresentEntityComments":                   {},
	"PresentEntityDetails":                    {},
	"PublishView":                             {},
	"PushView":                                {},
	"RecordAccess":                            {},
	"RecordSocketModeResponse":                {},
	"ReleaseSocketModeConnection":             {},
	"ReleaseSocketModeResponses":              {},
	"ReminderInfo":                            {},
	"RemoteFileInfo":                          {},
	"RemoteFiles":                             {},
	"RemoveBookmark":                          {},
	"RemoveCallParticipants":                  {},
	"RemovePin":                               {},
	"RemoveReaction":                          {},
	"RemoveRemoteFile":                        {},
	"RemoveStar":                              {},
	"RemoveUser":                              {},
	"RemoveUserGroupChannels":                 {},
	"RenameConversation":                      {},
	"RenewSocketModeConnection":               {},
	"RenewSocketModeResponses":                {},
	"Replies":                                 {},
	"RequestAppPermissions":                   {},
	"ResetUserSessions":                       {},
	"RevokeFilePublic":                        {},
	"RevokeSession":                           {},
	"RevokeToken":                             {},
	"ScheduleMessageWithBlocks":               {},
	"ScheduleMessageWithBlocksAndAttachments": {},
	"SetAuthMethod":                           {},
	"SetCanvasAccess":                         {},
	"SetConversationArchived":                 {},
	"SetConversationPurpose":                  {},
	"SetConversationTopic":                    {},
	"SetListAccess":                           {},
	"SetSocketModeCursor":                     {},
	"SetUserExpiration":                       {},
	"SetUserGroupEnabled":                     {},
	"ShareFilePublic":                         {},
	"ShareRemoteFile":                         {},
	"StartListDownload":                       {},
	"TeamBillableInfo":                        {},
	"Unfurl":                                  {},
	"UpdateCall":                              {},
	"UpdateList":                              {},
	"UpdateListCells":                         {},
	"UpdateListItem":                          {},
	"UpdateRemoteFile":                        {},
	"UpdateUserGroup":                         {},
	"UpdateView":                              {},
	"UpdateWithBlocks":                        {},
	"UpdateWithBlocksAndAttachments":          {},
	"UserGroupChannels":                       {},
	"UserGroupUsers":                          {},
	"UserReactions":                           {},
	"WorkflowStepCompleted":                   {},
	"WorkflowStepFailed":                      {},
	"WorkflowUpdateStep":                      {},
}
