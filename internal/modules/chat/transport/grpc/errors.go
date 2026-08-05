package grpc

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
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
//   - at most one class per code sets restoresCode, and no class whose code
//     appears in libraryProducedCodes sets it at all. That class is the fallback
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

	// Validation. Every specific sentinel precedes store.ErrInvalidArgument,
	// which closes the block: it is the generic member of the class, and this
	// file's own ordering rule is that a specific sentinel comes first. It used
	// to lead the block, so errors.Join(service.ErrInvalidMessage,
	// store.ErrInvalidArgument) — the shape a compensating store failure
	// produces — classified as the generic one and lost the specific one.
	{key: "store.invalid_conversation_type", code: codes.InvalidArgument, sentinel: store.ErrInvalidConversationType},
	{key: "store.invalid_invite_request", code: codes.InvalidArgument, sentinel: store.ErrInvalidInviteRequest},
	{key: "store.invalid_app_approval", code: codes.InvalidArgument, sentinel: store.ErrInvalidAppApproval},
	{key: "domain.invalid_cursor", code: codes.InvalidArgument, sentinel: domain.ErrInvalidCursor},
	{key: "domain.invalid_message_timestamp", code: codes.InvalidArgument, sentinel: domain.ErrInvalidMessageTimestamp},
	{key: "service.invalid_message", code: codes.InvalidArgument, sentinel: service.ErrInvalidMessage},
	{key: "service.invalid_message_stream", code: codes.InvalidArgument, sentinel: service.ErrInvalidMessageStream},
	{key: "service.invalid_stream_chunks", code: codes.InvalidArgument, sentinel: service.ErrInvalidStreamChunks},
	{key: "service.missing_stream_recipient_team", code: codes.InvalidArgument, sentinel: service.ErrMissingStreamRecipientTeam},
	{key: "service.missing_stream_recipient_user", code: codes.InvalidArgument, sentinel: service.ErrMissingStreamRecipientUser},
	{key: "service.invalid_timestamp", code: codes.InvalidArgument, sentinel: service.ErrInvalidTimestamp},
	{key: "service.invalid_conversation", code: codes.InvalidArgument, sentinel: service.ErrInvalidConversation},
	{key: "service.invalid_workspace", code: codes.InvalidArgument, sentinel: service.ErrInvalidWorkspace},
	{key: "service.invalid_conversation_prefs", code: codes.InvalidArgument, sentinel: service.ErrInvalidConversationPrefs},
	{key: "service.invalid_reaction", code: codes.InvalidArgument, sentinel: service.ErrInvalidReaction},
	{key: "service.invalid_file", code: codes.InvalidArgument, sentinel: service.ErrInvalidFile},
	{key: "service.invalid_search", code: codes.InvalidArgument, sentinel: service.ErrInvalidSearch},
	{key: "service.invalid_profile", code: codes.InvalidArgument, sentinel: service.ErrInvalidProfile},
	{key: "service.invalid_scheduled_status", code: codes.InvalidArgument, sentinel: service.ErrInvalidScheduledStatus},
	{key: "service.invalid_presence", code: codes.InvalidArgument, sentinel: service.ErrInvalidPresence},
	{key: "service.invalid_snooze", code: codes.InvalidArgument, sentinel: service.ErrInvalidSnooze},
	{key: "service.invalid_reminder", code: codes.InvalidArgument, sentinel: service.ErrInvalidReminder},
	{key: "service.invalid_later_reminder", code: codes.InvalidArgument, sentinel: service.ErrInvalidLaterReminder},
	{key: "service.reminder_time_in_past", code: codes.InvalidArgument, sentinel: service.ErrReminderTimeInPast},
	{key: "service.scheduled_time_in_past", code: codes.InvalidArgument, sentinel: service.ErrScheduledTimeInPast},
	{key: "service.scheduled_time_too_far", code: codes.InvalidArgument, sentinel: service.ErrScheduledTimeTooFar},
	{key: "service.scheduled_too_many", code: codes.ResourceExhausted, sentinel: service.ErrScheduledTooMany},
	{key: "service.scheduled_status_limit", code: codes.ResourceExhausted, sentinel: service.ErrScheduledStatusLimit},
	{key: "service.invalid_call", code: codes.InvalidArgument, sentinel: service.ErrInvalidCall},
	{key: "service.invalid_user_group", code: codes.InvalidArgument, sentinel: service.ErrInvalidUserGroup},
	{key: "service.invalid_ephemeral", code: codes.InvalidArgument, sentinel: service.ErrInvalidEphemeral},
	{key: "service.invalid_access_log", code: codes.InvalidArgument, sentinel: service.ErrInvalidAccessLog},
	{key: "service.invalid_emoji", code: codes.InvalidArgument, sentinel: service.ErrInvalidEmoji},
	{key: "service.invalid_remote_file", code: codes.InvalidArgument, sentinel: service.ErrInvalidRemoteFile},
	{key: "service.invalid_invite_request", code: codes.InvalidArgument, sentinel: service.ErrInvalidInviteRequest},
	// FailedPrecondition, not InvalidArgument: the request is well formed and
	// the invitation is real; it is the state that refuses, and retrying with a
	// corrected argument cannot help.
	{key: "service.invitation_expired", code: codes.FailedPrecondition, sentinel: service.ErrInvitationExpired},
	{key: "service.huddle_not_owned", code: codes.PermissionDenied, sentinel: service.ErrHuddleNotOwned},
	{key: "service.invalid_shared_invite", code: codes.InvalidArgument, sentinel: service.ErrInvalidSharedInvite},
	// FailedPrecondition for both: the request is well formed and it is the
	// state — already decided, or already full — that refuses.
	{key: "service.shared_invite_settled", code: codes.FailedPrecondition, sentinel: service.ErrSharedInviteSettled},
	{key: "service.slack_connect_full", code: codes.FailedPrecondition, sentinel: service.ErrSlackConnectFull},
	{key: "service.invalid_retention_duration", code: codes.InvalidArgument, sentinel: service.ErrInvalidRetentionDuration},
	// FailedPrecondition: the request is well formed and the conversation is
	// real; it is the conversation's type that carries no retention policy.
	{key: "service.retention_not_supported", code: codes.FailedPrecondition, sentinel: service.ErrRetentionNotSupported},
	{key: "service.invalid_app_approval", code: codes.InvalidArgument, sentinel: service.ErrInvalidAppApproval},
	{key: "service.invalid_view", code: codes.InvalidArgument, sentinel: service.ErrInvalidView},
	{key: "service.invalid_assistant_thread", code: codes.InvalidArgument, sentinel: service.ErrInvalidAssistantThread},
	{key: "service.assistant_thread_not_found", code: codes.NotFound, sentinel: service.ErrAssistantThreadNotFound},
	{key: "service.invalid_workflow_step", code: codes.InvalidArgument, sentinel: service.ErrInvalidWorkflowStep},
	{key: "service.invalid_trigger_config", code: codes.InvalidArgument, sentinel: service.ErrInvalidTriggerConfig},
	{key: "service.automation_entities_empty", code: codes.InvalidArgument, sentinel: service.ErrAutomationEntitiesEmpty},
	{key: "service.invalid_dialog", code: codes.InvalidArgument, sentinel: service.ErrInvalidDialog},
	{key: "service.invalid_bot", code: codes.InvalidArgument, sentinel: service.ErrInvalidBot},
	{key: "service.invalid_migration", code: codes.InvalidArgument, sentinel: service.ErrInvalidMigration},
	{key: "service.invalid_oauth", code: codes.InvalidArgument, sentinel: service.ErrInvalidOAuth},
	{key: "service.invalid_oauth_client", code: codes.InvalidArgument, sentinel: service.ErrInvalidOAuthClient},
	{key: "service.oauth_app_mismatch", code: codes.PermissionDenied, sentinel: service.ErrOAuthAppMismatch},
	{key: "service.invalid_integration_logs", code: codes.InvalidArgument, sentinel: service.ErrInvalidIntegrationLogs},
	{key: "service.invalid_list", code: codes.InvalidArgument, sentinel: service.ErrInvalidList},
	{key: "service.invalid_entity", code: codes.InvalidArgument, sentinel: service.ErrInvalidEntity},
	{key: "service.invalid_bookmark", code: codes.InvalidArgument, sentinel: service.ErrInvalidBookmark},
	{key: "service.invalid_canvas", code: codes.InvalidArgument, sentinel: service.ErrInvalidCanvas},
	{key: "service.invalid_external_upload", code: codes.InvalidArgument, sentinel: service.ErrInvalidExternalUpload},
	{key: "service.invalid_app_manifest", code: codes.InvalidArgument, sentinel: service.ErrInvalidAppManifest},
	{key: "service.invalid_app_response", code: codes.InvalidArgument, sentinel: service.ErrInvalidAppResponse},
	{key: "service.invalid_datastore_item", code: codes.InvalidArgument, sentinel: service.ErrInvalidDatastoreItem},
	{key: "service.invalid_datastore_query", code: codes.InvalidArgument, sentinel: service.ErrInvalidDatastoreQuery},
	{key: "service.invalid_trigger", code: codes.InvalidArgument, sentinel: service.ErrInvalidTrigger},
	{key: "service.slash_command_in_thread", code: codes.InvalidArgument, sentinel: service.ErrSlashCommandInThread},
	// The generic member of the class closes it, and it restores a bare
	// InvalidArgument: a peer that sent no detail still yields an
	// invalid-argument classification (HTTP 400) rather than codes.Unavailable
	// (HTTP 503, which asks a caller to retry a request that can never succeed).
	{key: "store.invalid_argument", code: codes.InvalidArgument, sentinel: store.ErrInvalidArgument, restoresCode: true},

	// Configuration tokens authenticate a developer rather than an installed
	// app. Keeping this separate from OAuth validation lets the manifest API
	// return invalid_auth without pretending a client grant was malformed.
	{key: "service.app_configuration_authentication", code: codes.Unauthenticated, sentinel: service.ErrAppConfigurationAuthentication, restoresCode: true},
	{key: "service.app_credential_key_unavailable", code: codes.FailedPrecondition, sentinel: service.ErrAppCredentialKeyUnavailable},

	// Uniqueness. The two sentinels mean different things to the Slack API:
	// store.ErrAlreadyExists is "already_reacted" and
	// service.ErrEmojiAlreadyExists is "emoji_already_exists". Collapsing them
	// onto codes.AlreadyExists made reactions.add answer the emoji error in the
	// split deployment, so the specific sentinel is listed first and the generic
	// one restores the bare code.
	{key: "service.emoji_already_exists", code: codes.AlreadyExists, sentinel: service.ErrEmojiAlreadyExists},
	// A message's Slack-style timestamp is its public identifier, so a second
	// message on the same microsecond would be permanently unaddressable. The
	// repository refuses it and internal/service retries with the next
	// microsecond; the class exists because a refusal that escapes that retry is
	// a uniqueness failure, not an unavailable dependency.
	{key: "store.message_timestamp_taken", code: codes.AlreadyExists, sentinel: store.ErrMessageTimestampTaken},
	{key: "store.already_exists", code: codes.AlreadyExists, sentinel: store.ErrAlreadyExists, restoresCode: true},

	// Concurrency. The generic member closes the group, as everywhere else.
	{key: "store.lease_conflict", code: codes.Aborted, sentinel: store.ErrLeaseConflict},
	{key: "store.idempotency_conflict", code: codes.Aborted, sentinel: store.ErrIdempotencyConflict},
	{key: "store.conflict", code: codes.Aborted, sentinel: store.ErrConflict, restoresCode: true},

	// Quotas. Both are a caller holding too much of a bounded resource, and both
	// have a documented non-retryable HTTP answer (429 for Socket Mode,
	// 400 too_many_bookmarks for bookmarks). store.ErrBookmarkLimit had no case
	// at all before, so bookmarks.add past the limit answered 503 remotely.
	//
	// Neither restores the bare code: codes.ResourceExhausted is in
	// libraryProducedCodes because it is also what grpc-go itself answers when a
	// message exceeds MaxMessageBytes, with no detail attached. While
	// store.ErrSocketModeConnectionLimit was the fallback, every oversized page
	// on every RPC — conversations.history, files.list, search.messages — came
	// back as socket_mode_unavailable in the split deployment and as a page in
	// the monolith.
	{key: "store.socket_mode_connection_limit", code: codes.ResourceExhausted, sentinel: store.ErrSocketModeConnectionLimit},
	{key: "store.bookmark_limit", code: codes.ResourceExhausted, sentinel: store.ErrBookmarkLimit},
	{key: "store.scheduled_message_limit", code: codes.ResourceExhausted, sentinel: store.ErrScheduledMessageLimit},
	{key: "store.scheduled_status_limit", code: codes.ResourceExhausted, sentinel: store.ErrScheduledStatusLimit},

	// Authorisation and preconditions. service.ErrNotWorkspaceAdmin shares
	// codes.PermissionDenied with service.ErrMessageNotOwned and stays
	// distinguishable through its key; ErrMessageNotOwned keeps restoresCode
	// because renaming the fallback would change what a peer that sends no detail
	// means, and only one class per code may hold it.
	{key: "service.not_workspace_admin", code: codes.PermissionDenied, sentinel: service.ErrNotWorkspaceAdmin},
	{key: "service.workflow_permission_denied", code: codes.PermissionDenied, sentinel: service.ErrWorkflowPermissionDenied},
	{key: "service.function_access_denied", code: codes.PermissionDenied, sentinel: service.ErrFunctionAccessDenied},
	{key: "service.message_not_owned_by_app", code: codes.PermissionDenied, sentinel: service.ErrMessageNotOwnedByApp},
	{key: "service.message_not_owned", code: codes.PermissionDenied, sentinel: service.ErrMessageNotOwned, restoresCode: true},
	// Refusing to remove a workspace's last owner is a precondition failure, not
	// a permission failure: the actor has the authority, and the operation is
	// refused because the workspace would become unadministrable.
	{key: "service.last_workspace_owner", code: codes.FailedPrecondition, sentinel: service.ErrLastWorkspaceOwner},
	{key: "service.conversation_already_archived", code: codes.FailedPrecondition, sentinel: service.ErrConversationAlreadyArchived},
	{key: "service.conversation_not_archived", code: codes.FailedPrecondition, sentinel: service.ErrConversationNotArchived},
	{key: "service.cannot_archive_default", code: codes.FailedPrecondition, sentinel: service.ErrCannotArchiveDefault},
	{key: "service.cannot_leave_default", code: codes.FailedPrecondition, sentinel: service.ErrCannotLeaveDefault},
	// Not being in the conversation is the refusal behind not_in_channel: the
	// caller is a workspace member and the conversation exists, so it is neither
	// an absence nor a permission failure.
	{key: "service.not_in_conversation", code: codes.FailedPrecondition, sentinel: service.ErrNotInConversation},
	{key: "service.cannot_invite_self", code: codes.FailedPrecondition, sentinel: service.ErrCannotInviteSelf},
	{key: "service.app_interaction_unavailable", code: codes.FailedPrecondition, sentinel: service.ErrAppInteractionUnavailable},
	{key: "service.app_home_not_enabled", code: codes.FailedPrecondition, sentinel: service.ErrAppHomeNotEnabled},
	{key: "service.app_not_hosted", code: codes.FailedPrecondition, sentinel: service.ErrAppNotHosted},
	{key: "service.function_not_running", code: codes.FailedPrecondition, sentinel: service.ErrFunctionNotRunning},
	{key: "service.message_not_streaming", code: codes.FailedPrecondition, sentinel: service.ErrMessageNotStreaming},
	{key: "service.message_already_deleted", code: codes.FailedPrecondition, sentinel: service.ErrMessageAlreadyDeleted, restoresCode: true},

	// Absence. blob.ErrNotFound is distinct from store.ErrNotFound and reaches a
	// caller: service.Messages converts blob.ErrUnavailable to
	// service.ErrBlobUnavailable on every blob call but returns a blob absence
	// unchanged (OpenUserPhoto, internal/service/messages.go), so without a class
	// a missing object was store.ErrNotFound in one composition and
	// codes.Unavailable in the other.
	{key: "blob.not_found", code: codes.NotFound, sentinel: blob.ErrNotFound},
	{key: "service.automation_user_not_found", code: codes.NotFound, sentinel: service.ErrAutomationUserNotFound},
	{key: "service.automation_channel_not_found", code: codes.NotFound, sentinel: service.ErrAutomationChannelNotFound},
	{key: "service.automation_team_not_found", code: codes.NotFound, sentinel: service.ErrAutomationTeamNotFound},
	{key: "service.automation_org_not_found", code: codes.NotFound, sentinel: service.ErrAutomationOrgNotFound},
	{key: "service.workflow_function_not_found", code: codes.NotFound, sentinel: service.ErrWorkflowFunctionNotFound},
	{key: "service.webhook_trigger_secret", code: codes.NotFound, sentinel: service.ErrWebhookTriggerSecret},
	{key: "service.slash_command_not_found", code: codes.NotFound, sentinel: service.ErrSlashCommandNotFound},
	{key: "service.app_datastore_not_found", code: codes.NotFound, sentinel: service.ErrAppDatastoreNotFound},
	{key: "store.not_found", code: codes.NotFound, sentinel: store.ErrNotFound, restoresCode: true},

	// Corrupt stored data and events a producer could not build. codes.Internal
	// deliberately has no restoresCode class: it is also the code a recovered
	// panic carries, and a panic has no domain cause to restore.
	//
	// The events sentinels reach a caller through service.Messages.newEvent,
	// which every mutation funnels through: an event the service cannot build is
	// a defect in this system, not a caller mistake, so it must not read as a
	// retryable dependency failure.
	{key: "domain.invalid_stored_timestamp", code: codes.Internal, sentinel: domain.ErrInvalidStoredTimestamp},
	{key: "events.payload_required", code: codes.Internal, sentinel: events.ErrPayloadRequired},
	{key: "events.payload_field_invalid", code: codes.Internal, sentinel: events.ErrPayloadFieldInvalid},
	{key: "events.payload_malformed", code: codes.Internal, sentinel: events.ErrPayloadMalformed},
	{key: "events.slack_event_incomplete", code: codes.Internal, sentinel: events.ErrSlackEventIncomplete},
	{key: "events.event_incomplete", code: codes.Internal, sentinel: events.ErrEventIncomplete},

	// Dependency failure. codes.Unavailable deliberately has no restoresCode
	// class: it is also the code an unclassified internal failure returns, and
	// restoring service.ErrBlobUnavailable for every one of those would invent a
	// cause. A peer that sends the detail still restores it exactly.
	{key: "service.blob_unavailable", code: codes.Unavailable, sentinel: service.ErrBlobUnavailable},
	{key: "blob.unavailable", code: codes.Unavailable, sentinel: blob.ErrUnavailable},
	// A serialization failure, a deadlock victim, a lock timeout or a lost
	// leader is exactly what codes.Unavailable means: retry the same request.
	// Without a class it reached the caller as raw driver text under the
	// unclassified default, which asks for the same retry while telling the
	// caller nothing it can act on.
	{key: "store.transient", code: codes.Unavailable, sentinel: store.ErrTransient},
}

// libraryProducedCodes are the status codes grpc-go emits on its own behalf,
// with no DomainError detail attached, for a condition that has no domain cause.
//
// No class may restore one of them from the bare code: doing so hands the caller
// a sentinel for a failure the chat process never reported. The value is the
// condition that produces the code, so a future entry has to say why it belongs.
//
// codes.Canceled and codes.DeadlineExceeded are deliberately absent. grpc-go
// produces them too, but what it means by them *is* context.Canceled and
// context.DeadlineExceeded, so restoring those sentinels from the bare code is
// exact rather than invented.
var libraryProducedCodes = map[codes.Code]string{
	codes.ResourceExhausted: "a request or response message larger than MaxMessageBytes",
	codes.Internal:          "a recovered panic and every transport-level protocol failure",
	codes.Unavailable:       "a connection that could not be established, and this package's own unclassified-failure default",
	codes.Unimplemented:     "a method the peer does not serve, which is what a rolling deployment produces",
	codes.Unknown:           "an error that carries no status at all",
}

// maxStatusMessageBytes bounds the status message a classified failure carries.
//
// A status message travels in HTTP/2 trailers, which are bounded by
// MaxHeaderListSize rather than by MaxMessageBytes, and mapError copies the
// handler's err.Error() into it. A wrapped cause that embeds a caller-supplied
// value would otherwise decide whether the trailer fits, turning a domain error
// into a transport failure with a different class.
const maxStatusMessageBytes = 2048

// boundStatusMessage truncates a status message to maxStatusMessageBytes,
// keeping the truncation visible rather than silently losing the tail.
func boundStatusMessage(message string) string {
	if len(message) <= maxStatusMessageBytes {
		return message
	}
	const ellipsis = "…"
	// The bound is in bytes and the message is text, so the cut is moved back to
	// a rune boundary: truncating inside a multi-byte rune produces a status
	// message that is not valid UTF-8, which grpc-go silently repairs with
	// U+FFFD on the wire and internal/web renders into a browser.
	kept := message[:maxStatusMessageBytes-len(ellipsis)]
	for len(kept) > 0 && !utf8.ValidString(kept) {
		kept = kept[:len(kept)-1]
	}
	return kept + ellipsis
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
		if reason, produced := libraryProducedCodes[class.code]; produced {
			panic(fmt.Sprintf("chat gRPC class %q may not restore the bare code %s: grpc-go produces it for %s", class.key, class.code, reason))
		}
		errorClassesByCode[class.code] = class
	}
	// Attaching the detail must be infallible, because mapError has no honest
	// answer if it is not: a classified failure without its detail restores the
	// wrong sentinel for every code that several classes share. Proving it once
	// per process at startup turns a marshalling regression into an immediate,
	// attributable failure instead of a wrong error class in production.
	for _, class := range errorClasses {
		if _, err := status.New(class.code, "").WithDetails(&chatv1.DomainError{Key: class.key, Keys: []string{class.key}}); err != nil {
			panic(fmt.Sprintf("chat gRPC cannot attach the domain error detail for %q: %v", class.key, err))
		}
	}
}

// classifyErrors reports every class an error matches, in table order, so a
// specific sentinel precedes a general one it may wrap.
//
// An error can match more than one: errors.Join(store.ErrNotFound,
// service.ErrInvalidCanvas) is the literal shape internal/service returns when a
// compensating delete fails after a rejected create, and errors.Is is true for
// both sentinels in process. Returning only the first put one key on the wire
// and the caller lost the other, so canvases.create answered channel_not_found
// in the monolith and invalid_arg_name across the seam.
func classifyErrors(err error) []errorClass {
	if err == nil {
		return nil
	}
	var matched []errorClass
	for _, class := range errorClasses {
		if errors.Is(err, class.sentinel) {
			matched = append(matched, class)
		}
	}
	return matched
}

// codeSeverity ranks a status code by how restrictive the answer it carries is.
//
// An error can match several classes at once — errors.Join(store.ErrNotFound,
// service.ErrInvalidCanvas) is what internal/service returns when a
// compensating delete fails after a rejected create — and exactly one code and
// one key go on the wire. Taking them from the first matching row made the
// answer depend on the *order of the table*, whose first block is validation,
// so a refusal that was genuinely an absence or a denial was transported as
// HTTP 400: the caller was told to fix an argument for a channel that does not
// exist, or for an operation it is not allowed to perform.
//
// The rank is what the answer commits to. A denial and an absence each state
// something specific about the request that a validation failure does not, so
// they outrank it; a conflict states that the request was well formed and lost
// a race. Everything else keeps validation's rank, which leaves the table order
// deciding between equals exactly as before.
var codeSeverity = map[codes.Code]int{
	codes.PermissionDenied:   60,
	codes.Unauthenticated:    60,
	codes.NotFound:           50,
	codes.FailedPrecondition: 40,
	codes.Aborted:            40,
	codes.AlreadyExists:      40,
	codes.ResourceExhausted:  30,
}

const defaultCodeSeverity = 20

func severityOf(code codes.Code) int {
	if rank, known := codeSeverity[code]; known {
		return rank
	}
	return defaultCodeSeverity
}

// primaryClass is the class whose code and key the status carries: the most
// restrictive of the matched classes, with table order breaking a tie so a
// specific sentinel still precedes the generic member of its own block.
func primaryClass(matched []errorClass) errorClass {
	primary := matched[0]
	for _, class := range matched[1:] {
		if severityOf(class.code) > severityOf(primary.code) {
			primary = class
		}
	}
	return primary
}

// classifyError reports the class of a domain error that decides its status
// code. classifyErrors carries the rest.
func classifyError(err error) (errorClass, bool) {
	matched := classifyErrors(err)
	if len(matched) == 0 {
		return errorClass{}, false
	}
	return primaryClass(matched), true
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
	matched := classifyErrors(err)
	if len(matched) == 0 {
		return serverError{status: status.New(codes.Unavailable, unclassifiedMessage), cause: err}
	}
	// The code comes from the most restrictive matched class; keys carries every
	// class the error matched, in table order, so a caller that reads keys
	// restores the same set errors.Is answers in process. key repeats the class
	// that decided the code, for a peer that predates keys: a key that named a
	// different class than the code would leave that peer with the two halves
	// of one answer disagreeing.
	primary := primaryClass(matched)
	detail := &chatv1.DomainError{Key: primary.key, Keys: make([]string, 0, len(matched))}
	for _, class := range matched {
		detail.Keys = append(detail.Keys, class.key)
	}
	result := status.New(primary.code, boundStatusMessage(err.Error()))
	detailed, detailErr := result.WithDetails(detail)
	if detailErr != nil {
		// The detail is a fixed-shape message built from a table this package
		// owns, so a marshalling failure is a programming error. Degrading to
		// the bare code silently would restore the wrong sentinel for the 33
		// classes that share codes.InvalidArgument and none at all for the codes
		// with no fallback — the exact outcome the detail exists to prevent.
		panic(fmt.Sprintf("chat gRPC cannot attach the domain error detail for %q: %v", primary.key, detailErr))
	}
	return serverError{status: detailed, cause: err}
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

// restoreSentinel rebuilds the sentinels a chat handler failed with.
//
// The first DomainError detail decides, and no later detail is consulted: the
// scan used to continue, so with two details the last known key won and two
// peers that disagreed about which detail is authoritative disagreed about the
// sentinel.
//
// The bare status code is consulted only when the peer sent no DomainError
// detail at all. Absence of a detail means "this peer predates the detail and
// the code is all I have", and the code's fallback class is then the best
// answer available. A detail whose every key is unknown means something else
// entirely: the peer named a class and this build does not have it. Answering
// from the code there invents a *different specific* class — a newer peer's
// FailedPrecondition channel_is_archived came back as
// service.ErrMessageAlreadyDeleted and its PermissionDenied
// not_a_workspace_owner as service.ErrMessageNotOwned — and internal/api/slack
// maps by sentinel, so a caller was confidently told the wrong thing for the
// whole rolling window. Leaving it unclassified gives the generic path instead.
func restoreSentinel(remoteStatus *status.Status) (error, bool) {
	sawDetail := false
	for _, detail := range remoteStatus.Details() {
		domainError, ok := detail.(*chatv1.DomainError)
		if !ok {
			continue
		}
		sawDetail = true
		keys := domainError.GetKeys()
		if len(keys) == 0 {
			keys = []string{domainError.GetKey()}
		}
		restored := make([]error, 0, len(keys))
		for _, key := range keys {
			if class, known := errorClassesByKey[key]; known {
				restored = append(restored, class.sentinel)
			}
		}
		switch len(restored) {
		case 0:
			// Every key is unknown to this build.
		case 1:
			return restored[0], true
		default:
			return errors.Join(restored...), true
		}
		break
	}
	if sawDetail {
		return nil, false
	}
	class, ok := errorClassesByCode[remoteStatus.Code()]
	if !ok {
		return nil, false
	}
	return class.sentinel, true
}
