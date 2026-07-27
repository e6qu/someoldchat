package grpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errorClass binds one domain sentinel to the gRPC status code that carries it
// and to the stable key that identifies it exactly.
//
// errorClasses below is the only place the mapping exists. Both directions read
// it: the server classifies an implementation error with classifyError and the
// client restores the sentinel with restoreSentinel, so a sentinel cannot be
// added to one direction and forgotten in the other. The previous design was a
// pair of hand-maintained switch statements — the server produced ten status
// classes and the client reversed five — and every sentinel outside that
// intersection was a one-way trip: errors.Is(err, service.ErrInvalidMessage) was
// always false in -chat-mode grpc, which turned an HTTP 400 in the monolith into
// an HTTP 503 in the split deployment.
//
// Invariants, all asserted by TestErrorClassTableIsConsistent and enforced at
// package initialisation:
//
//   - key is unique, and it is wire contract: renaming one breaks a rolling
//     deployment where the two processes run different versions.
//   - sentinel is unique. Two sentinels that share a code (store.ErrAlreadyExists
//     and service.ErrEmojiAlreadyExists) stay distinguishable because the key,
//     not the code, restores them.
//   - at most one class per code sets restoresCode. That class is the fallback
//     for a peer old enough to send no DomainError detail; a code with no such
//     class stays unmapped rather than restoring a sentinel that may be wrong.
//   - every exported sentinel of internal/store, internal/service and
//     internal/domain appears here or in unclassifiedSentinels with a reason
//     (TestEveryDomainSentinelIsClassified), so a newly declared sentinel fails
//     the suite instead of silently degrading to codes.Unavailable.
type errorClass struct {
	key      string
	code     codes.Code
	sentinel error

	// restoresCode marks this class as the meaning of a bare status code, used
	// when the peer sent no DomainError detail.
	restoresCode bool
}

// errorClasses is ordered: classifyError returns the first class whose sentinel
// the error matches, so a specific sentinel must precede a general one that it
// might wrap. The order reproduces the precedence of the original if-chain.
var errorClasses = []errorClass{
	// Context errors are produced by grpc-go itself as well as by handlers, and
	// grpc-go sends them without details, so both restore from the code.
	{key: "context.canceled", code: codes.Canceled, sentinel: context.Canceled, restoresCode: true},
	{key: "context.deadline_exceeded", code: codes.DeadlineExceeded, sentinel: context.DeadlineExceeded, restoresCode: true},

	// Validation. store.ErrInvalidArgument restores a bare InvalidArgument
	// because it is the generic member of the class: a peer that sent no detail
	// still yields an invalid-argument classification (HTTP 400) rather than
	// codes.Unavailable (HTTP 503, which asks a caller to retry a request that
	// can never succeed).
	{key: "store.invalid_argument", code: codes.InvalidArgument, sentinel: store.ErrInvalidArgument, restoresCode: true},
	{key: "store.invalid_conversation_type", code: codes.InvalidArgument, sentinel: store.ErrInvalidConversationType},
	{key: "store.invalid_invite_request", code: codes.InvalidArgument, sentinel: store.ErrInvalidInviteRequest},
	{key: "store.invalid_app_approval", code: codes.InvalidArgument, sentinel: store.ErrInvalidAppApproval},
	{key: "domain.invalid_cursor", code: codes.InvalidArgument, sentinel: domain.ErrInvalidCursor},
	{key: "domain.invalid_message_timestamp", code: codes.InvalidArgument, sentinel: domain.ErrInvalidMessageTimestamp},
	{key: "service.invalid_message", code: codes.InvalidArgument, sentinel: service.ErrInvalidMessage},
	{key: "service.invalid_timestamp", code: codes.InvalidArgument, sentinel: service.ErrInvalidTimestamp},
	{key: "service.invalid_conversation", code: codes.InvalidArgument, sentinel: service.ErrInvalidConversation},
	{key: "service.invalid_workspace", code: codes.InvalidArgument, sentinel: service.ErrInvalidWorkspace},
	{key: "service.invalid_conversation_prefs", code: codes.InvalidArgument, sentinel: service.ErrInvalidConversationPrefs},
	{key: "service.invalid_reaction", code: codes.InvalidArgument, sentinel: service.ErrInvalidReaction},
	{key: "service.invalid_file", code: codes.InvalidArgument, sentinel: service.ErrInvalidFile},
	{key: "service.invalid_search", code: codes.InvalidArgument, sentinel: service.ErrInvalidSearch},
	{key: "service.invalid_profile", code: codes.InvalidArgument, sentinel: service.ErrInvalidProfile},
	{key: "service.invalid_presence", code: codes.InvalidArgument, sentinel: service.ErrInvalidPresence},
	{key: "service.invalid_snooze", code: codes.InvalidArgument, sentinel: service.ErrInvalidSnooze},
	{key: "service.invalid_reminder", code: codes.InvalidArgument, sentinel: service.ErrInvalidReminder},
	{key: "service.invalid_call", code: codes.InvalidArgument, sentinel: service.ErrInvalidCall},
	{key: "service.invalid_user_group", code: codes.InvalidArgument, sentinel: service.ErrInvalidUserGroup},
	{key: "service.invalid_ephemeral", code: codes.InvalidArgument, sentinel: service.ErrInvalidEphemeral},
	{key: "service.invalid_access_log", code: codes.InvalidArgument, sentinel: service.ErrInvalidAccessLog},
	{key: "service.invalid_emoji", code: codes.InvalidArgument, sentinel: service.ErrInvalidEmoji},
	{key: "service.invalid_remote_file", code: codes.InvalidArgument, sentinel: service.ErrInvalidRemoteFile},
	{key: "service.invalid_invite_request", code: codes.InvalidArgument, sentinel: service.ErrInvalidInviteRequest},
	{key: "service.invalid_app_approval", code: codes.InvalidArgument, sentinel: service.ErrInvalidAppApproval},
	{key: "service.invalid_view", code: codes.InvalidArgument, sentinel: service.ErrInvalidView},
	{key: "service.invalid_workflow_step", code: codes.InvalidArgument, sentinel: service.ErrInvalidWorkflowStep},
	{key: "service.invalid_dialog", code: codes.InvalidArgument, sentinel: service.ErrInvalidDialog},
	{key: "service.invalid_bot", code: codes.InvalidArgument, sentinel: service.ErrInvalidBot},
	{key: "service.invalid_migration", code: codes.InvalidArgument, sentinel: service.ErrInvalidMigration},
	{key: "service.invalid_oauth", code: codes.InvalidArgument, sentinel: service.ErrInvalidOAuth},
	{key: "service.invalid_oauth_client", code: codes.InvalidArgument, sentinel: service.ErrInvalidOAuthClient},
	{key: "service.invalid_integration_logs", code: codes.InvalidArgument, sentinel: service.ErrInvalidIntegrationLogs},
	{key: "service.invalid_list", code: codes.InvalidArgument, sentinel: service.ErrInvalidList},
	{key: "service.invalid_entity", code: codes.InvalidArgument, sentinel: service.ErrInvalidEntity},
	{key: "service.invalid_bookmark", code: codes.InvalidArgument, sentinel: service.ErrInvalidBookmark},
	{key: "service.invalid_canvas", code: codes.InvalidArgument, sentinel: service.ErrInvalidCanvas},
	{key: "service.invalid_external_upload", code: codes.InvalidArgument, sentinel: service.ErrInvalidExternalUpload},

	// Uniqueness. The two sentinels mean different things to the Slack API:
	// store.ErrAlreadyExists is "already_reacted" and
	// service.ErrEmojiAlreadyExists is "emoji_already_exists". Collapsing them
	// onto codes.AlreadyExists made reactions.add answer the emoji error in the
	// split deployment, so the specific sentinel is listed first and the generic
	// one restores the bare code.
	{key: "service.emoji_already_exists", code: codes.AlreadyExists, sentinel: service.ErrEmojiAlreadyExists},
	{key: "store.already_exists", code: codes.AlreadyExists, sentinel: store.ErrAlreadyExists, restoresCode: true},

	// Concurrency.
	{key: "store.conflict", code: codes.Aborted, sentinel: store.ErrConflict, restoresCode: true},
	{key: "store.lease_conflict", code: codes.Aborted, sentinel: store.ErrLeaseConflict},
	{key: "store.idempotency_conflict", code: codes.Aborted, sentinel: store.ErrIdempotencyConflict},

	// Quotas. Both are a caller holding too much of a bounded resource, and both
	// have a documented non-retryable HTTP answer (429 for Socket Mode,
	// 400 too_many_bookmarks for bookmarks). store.ErrBookmarkLimit had no case
	// at all before, so bookmarks.add past the limit answered 503 remotely.
	{key: "store.socket_mode_connection_limit", code: codes.ResourceExhausted, sentinel: store.ErrSocketModeConnectionLimit, restoresCode: true},
	{key: "store.bookmark_limit", code: codes.ResourceExhausted, sentinel: store.ErrBookmarkLimit},

	// Authorisation and preconditions. service.ErrNotWorkspaceAdmin shares
	// codes.PermissionDenied with service.ErrMessageNotOwned and stays
	// distinguishable through its key; ErrMessageNotOwned keeps restoresCode
	// because renaming the fallback would change what a peer that sends no detail
	// means, and only one class per code may hold it.
	{key: "service.not_workspace_admin", code: codes.PermissionDenied, sentinel: service.ErrNotWorkspaceAdmin},
	{key: "service.message_not_owned", code: codes.PermissionDenied, sentinel: service.ErrMessageNotOwned, restoresCode: true},
	{key: "service.message_already_deleted", code: codes.FailedPrecondition, sentinel: service.ErrMessageAlreadyDeleted, restoresCode: true},

	// Absence.
	{key: "store.not_found", code: codes.NotFound, sentinel: store.ErrNotFound, restoresCode: true},

	// Corrupt stored data. codes.Internal deliberately has no restoresCode class:
	// it is also the code a recovered panic carries, and a panic has no domain
	// cause to restore.
	{key: "domain.invalid_stored_timestamp", code: codes.Internal, sentinel: domain.ErrInvalidStoredTimestamp},

	// Dependency failure. codes.Unavailable deliberately has no restoresCode
	// class: it is also the code an unclassified internal failure returns, and
	// restoring service.ErrBlobUnavailable for every one of those would invent a
	// cause. A peer that sends the detail still restores it exactly.
	{key: "service.blob_unavailable", code: codes.Unavailable, sentinel: service.ErrBlobUnavailable},
}

// unclassifiedMessage is the status message for an error with no domain class.
//
// The text is fixed on purpose. mapError used to copy err.Error() into the
// status for the catch-all, so a storage failure put whatever the driver said
// (DSN fragments, SQL constraint names, blob keys) into a message that
// internal/web renders straight into the browser. The cause stays available in
// process through serverError.Unwrap, which is what the logging interceptor
// records.
const unclassifiedMessage = "chat service could not complete the request"

var (
	errorClassesByKey  = make(map[string]errorClass, len(errorClasses))
	errorClassesByCode = make(map[codes.Code]errorClass, len(errorClasses))
)

func init() {
	sentinels := make(map[error]string, len(errorClasses))
	for _, class := range errorClasses {
		if class.key == "" || class.sentinel == nil {
			panic("chat gRPC error class requires a key and a sentinel")
		}
		if existing, exists := errorClassesByKey[class.key]; exists {
			panic(fmt.Sprintf("chat gRPC error key %q is declared twice (%v and %v)", class.key, existing.sentinel, class.sentinel))
		}
		if existing, exists := sentinels[class.sentinel]; exists {
			panic(fmt.Sprintf("chat gRPC sentinel %v is declared twice (%q and %q)", class.sentinel, existing, class.key))
		}
		errorClassesByKey[class.key] = class
		sentinels[class.sentinel] = class.key
		if !class.restoresCode {
			continue
		}
		if existing, exists := errorClassesByCode[class.code]; exists {
			panic(fmt.Sprintf("chat gRPC code %s has two fallback classes (%q and %q)", class.code, existing.key, class.key))
		}
		errorClassesByCode[class.code] = class
	}
}

// classifyError reports the class of a domain error, matching the table in
// order so a specific sentinel wins over a general one it may wrap.
func classifyError(err error) (errorClass, bool) {
	if err == nil {
		return errorClass{}, false
	}
	for _, class := range errorClasses {
		if errors.Is(err, class.sentinel) {
			return class, true
		}
	}
	return errorClass{}, false
}

// serverError is the error a handler returns. grpc-go sends its GRPCStatus, so
// the wire carries the classified code plus the sentinel key, while Unwrap keeps
// the original error available to interceptors for logging.
type serverError struct {
	status *status.Status
	cause  error
}

func (e serverError) Error() string              { return e.cause.Error() }
func (e serverError) Unwrap() error              { return e.cause }
func (e serverError) GRPCStatus() *status.Status { return e.status }

// mapError converts a domain error into the status a chat handler returns.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	// Mapping is idempotent, and an error that already carries a gRPC status keeps
	// it. A recovered panic is codes.Internal and a failed stream send is
	// codes.Canceled or codes.Unavailable from the transport itself;
	// re-classifying either would replace a precise code with the unclassified
	// default.
	if existing, ok := status.FromError(err); ok {
		return serverError{status: existing, cause: err}
	}
	class, ok := classifyError(err)
	if !ok {
		return serverError{status: status.New(codes.Unavailable, unclassifiedMessage), cause: err}
	}
	result := status.New(class.code, err.Error())
	if detailed, detailErr := result.WithDetails(&chatv1.DomainError{Key: class.key}); detailErr == nil {
		result = detailed
	}
	return serverError{status: result, cause: err}
}

// invalidArgument rejects a request the transport itself cannot accept: a
// missing required field, a malformed page bound, a stream that does not begin
// with metadata. It carries store.ErrInvalidArgument so the caller classifies it
// as a caller mistake (HTTP 400) rather than as an unavailable dependency.
func invalidArgument(message string) error {
	return mapError(fmt.Errorf("%w: %s", store.ErrInvalidArgument, message))
}

// invalidArgumentFrom rejects a request the transport cannot accept, preserving
// a domain sentinel the value already carries (a cursor that fails to decode
// carries domain.ErrInvalidCursor, and losing it would answer the caller with a
// less specific error remotely than in process).
func invalidArgumentFrom(err error) error {
	if _, ok := classifyError(err); ok {
		return mapError(err)
	}
	return invalidArgument(err.Error())
}

// remoteDomainError is the error a Remote method returns. Callers can inspect it
// with errors.Is against the sentinel the chat process failed with, and with
// status.Code against the gRPC code, so the same inspection works in both
// compositions.
type remoteDomainError struct {
	status *status.Status
	cause  error
}

// Error reports the status message without the "rpc error: code = ..." preamble
// grpc-go prefixes. The monolith surfaces the plain domain text, several
// handlers render err.Error() to a user, and the preamble is transport detail
// the caller already has through GRPCStatus.
func (e remoteDomainError) Error() string              { return e.status.Message() }
func (e remoteDomainError) Unwrap() error              { return e.cause }
func (e remoteDomainError) GRPCStatus() *status.Status { return e.status }

// mapRemoteError is the exact inverse of mapError: it restores the sentinel the
// chat process failed with from the DomainError detail, falling back to the
// class that owns the bare status code when the peer is an older build that
// sends no detail.
func mapRemoteError(err error) error {
	if err == nil {
		return nil
	}
	remoteStatus, ok := status.FromError(err)
	if !ok {
		return err
	}
	cause, ok := restoreSentinel(remoteStatus)
	if !ok {
		return err
	}
	return remoteDomainError{status: remoteStatus, cause: cause}
}

func restoreSentinel(remoteStatus *status.Status) (error, bool) {
	for _, detail := range remoteStatus.Details() {
		domainError, ok := detail.(*chatv1.DomainError)
		if !ok {
			continue
		}
		if class, known := errorClassesByKey[domainError.GetKey()]; known {
			return class.sentinel, true
		}
	}
	class, ok := errorClassesByCode[remoteStatus.Code()]
	if !ok {
		return nil, false
	}
	return class.sentinel, true
}
