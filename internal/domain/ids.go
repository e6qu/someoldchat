package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type WorkspaceID string
type UserID string
type ConversationID string
type MessageID string
type EventID string
type FileID string
type CanvasID string
type ListID string
type ListItemID string
type ListTemplateID string
type ListDownloadID string
type ListItemCommentID string
type ListItemFileID string
type FileCommentID string
type CanvasCommentID string
type ExternalUploadID string
type ReminderID string
type LaterReminderID string
type ActivityID string
type ActivitySavedViewID string
type SidebarSectionID string
type SavedItemID string
type ScheduledMessageID string
type ScheduledStatusID string
type UserGroupID string
type InviteRequestID string
type SharedInviteID string
type AppID string
type AppRequestID string
type CallID string
type BookmarkID string
type ViewID string
type WorkflowStepID string
type WorkflowID string
type WorkflowTriggerID string
type WorkflowRunID string
type DialogID string
type BotID string
type IncomingWebhookID string
type MessageTimestamp string

func NewMessageTimestamp(value time.Time) MessageTimestamp {
	return MessageTimestamp(fmt.Sprintf("%d.%06d", value.Unix(), value.Nanosecond()/1000))
}

// MessageInstant is the creation instant a message may be stored with: the
// microsecond resolution its own Slack-style timestamp can express.
//
// A message's timestamp IS its public identifier, and read cursors, thread roots
// and pagination all key on it. Storing an instant finer than that identifier
// makes the two disagree: a message created at .123456789 stores greater than a
// read cursor built from the very same instant, which truncates to .123456, so
// the message can never be marked read. The service truncated at its own two
// creation sites, which hid this from every caller that went through it, but the
// repository accepted any resolution — and whether the defect appeared at all
// then depended on the host clock's granularity.
func MessageInstant(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

// ErrInvalidMessageTimestamp is the single sentinel for a malformed Slack-style
// message timestamp. The three ad-hoc errors it replaces were indistinguishable
// from a repository failure, so a caller could not tell a bad request from a
// broken database. ErrInvalidCursor is the precedent.
var ErrInvalidMessageTimestamp = errors.New("invalid message timestamp")

// ParseMessageTimestamp reads a message identifier: whole seconds, a dot and
// exactly six digits of microseconds, which is the only shape a `ts` this
// system mints can have.
//
// It and ParseTimestampBound share one reader. The Web API used to carry its
// own lenient parser beside this strict one, and the two disagreed about the
// same argument: reactions.add accepted `timestamp=1700000000` at the wire,
// and the service then refused it as a malformed identifier, so the caller was
// told `invalid_arg_name` instead of the `bad_timestamp` its enum declares.
func ParseMessageTimestamp(value MessageTimestamp) (time.Time, error) {
	return parseSlackTimestamp(string(value), true)
}

// MessagePermalinkPath is Slack's message link path,
// /archives/<channel>/p<ts without the dot>, with the thread coordinates Slack
// appends for a reply. It is a path: the origin belongs to whichever transport
// served the request, and every caller that hands a link out resolves it
// against that origin. The shape used to be spelled out at four sites.
func MessagePermalinkPath(conversation ConversationID, timestamp, threadTimestamp MessageTimestamp) string {
	path := "/archives/" + url.PathEscape(string(conversation)) + "/p" + strings.ReplaceAll(string(timestamp), ".", "")
	if threadTimestamp != "" && threadTimestamp != timestamp {
		path += "?" + url.Values{"cid": {string(conversation)}, "thread_ts": {string(threadTimestamp)}}.Encode()
	}
	return path
}

// ParseTimestampBound reads a Slack timestamp used as a bound rather than as an
// identifier — conversations.history's `oldest`/`latest`, a file filter's
// `ts_from`. Slack accepts whole seconds there, and up to six fractional
// digits.
func ParseTimestampBound(raw string) (time.Time, error) {
	return parseSlackTimestamp(strings.TrimSpace(raw), false)
}

// maxTimestampSeconds is the largest `ts` whose microsecond scaling fits in
// int64 WITH its fractional microseconds added. Bounding seconds*1e6 alone was
// not enough: `9223372036854.8` overflowed to a negative instant.
const maxTimestampSeconds = (math.MaxInt64 - 999999) / 1000000

func parseSlackTimestamp(raw string, identifier bool) (time.Time, error) {
	whole, fraction, dotted := strings.Cut(raw, ".")
	if !asciiDigits(whole) || len(fraction) > 6 || (fraction != "" && !asciiDigits(fraction)) ||
		(identifier && (!dotted || len(fraction) != 6)) {
		return time.Time{}, fmt.Errorf("%w %q", ErrInvalidMessageTimestamp, raw)
	}
	seconds, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || seconds > maxTimestampSeconds {
		return time.Time{}, fmt.Errorf("%w %q", ErrInvalidMessageTimestamp, raw)
	}
	micros := int64(0)
	for index := 0; index < 6; index++ {
		micros *= 10
		if index < len(fraction) {
			micros += int64(fraction[index] - '0')
		}
	}
	return time.Unix(seconds, micros*1000).UTC(), nil
}

// asciiDigits reports whether value is one or more ASCII digits. ParseInt alone
// also accepts a sign, which is how `-1.000000` used to parse as an identifier.
func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

// PublicID is deliberately opaque. The prefix is part of the wire contract;
// the random suffix is not used as an ordering key. It mints tokens, secrets
// and this deployment's own opaque handles; an identifier Slack defines is
// minted by SlackID instead.
func PublicID(prefix string) (string, error) {
	suffix, err := randomSuffix(prefix)
	if err != nil {
		return "", err
	}
	return prefix + suffix, nil
}

// SlackID mints an identifier in one of Slack's object namespaces: the kind
// prefix Slack uses (U, C, D, T, A, B, F, S, Rm, Q, V, Ev, …) followed by
// upper-case letters and digits only. Slack's identifiers never contain a
// lower-case letter, and apps rely on that: they parse `<@U[A-Z0-9]+>` and
// `<#C[A-Z0-9]+|…>` out of message text, and the pinned OpenAPI document
// validates ids against patterns such as ^[UW][A-Z0-9]{8,}$. The tail is the
// same 80 random bits PublicID uses, in upper-case hex.
//
// Identifiers minted before SlackID existed keep their lower-case hex tail
// and remain valid: every identifier is opaque and compared byte-for-byte, and
// the shape checks that recognise an identifier accept either case.
func SlackID(prefix string) (string, error) {
	suffix, err := randomSuffix(prefix)
	if err != nil {
		return "", err
	}
	return prefix + strings.ToUpper(suffix), nil
}

func randomSuffix(prefix string) (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return hex.EncodeToString(b[:]), nil
}

func NewMessageID() (MessageID, error) { value, err := PublicID("msg_"); return MessageID(value), err }
func NewEventID() (EventID, error)     { value, err := SlackID("Ev"); return EventID(value), err }
func NewFileID() (FileID, error)       { value, err := SlackID("F"); return FileID(value), err }
func NewCanvasID() (CanvasID, error)   { value, err := SlackID("F"); return CanvasID(value), err }
func NewUserID() (UserID, error)       { value, err := SlackID("U"); return UserID(value), err }
func NewListID() (ListID, error)       { value, err := SlackID("F"); return ListID(value), err }
func NewListTemplateID() (ListTemplateID, error) {
	value, err := SlackID("Ft")
	return ListTemplateID(value), err
}

// NewExternalUploadID mints an identifier in the file identifier space.
// files.getUploadURLExternal hands this value to the client as file_id, and the
// file that files.completeUploadExternal creates keeps it, so a client can
// reference the file by the identifier it was given before the bytes existed.
func NewExternalUploadID() (ExternalUploadID, error) {
	value, err := NewFileID()
	return ExternalUploadID(value), err
}
func NewListItemID() (ListItemID, error) {
	value, err := SlackID("Rec")
	return ListItemID(value), err
}
func NewListDownloadID() (ListDownloadID, error) {
	value, err := PublicID("export_")
	return ListDownloadID(value), err
}
func NewWorkflowID() (WorkflowID, error) {
	value, err := SlackID("Wf")
	return WorkflowID(value), err
}
func NewWorkflowTriggerID() (WorkflowTriggerID, error) {
	value, err := SlackID("Ft")
	return WorkflowTriggerID(value), err
}
func NewWorkflowRunID() (WorkflowRunID, error) {
	// Slack exposes workflow executions as Wx… identifiers in
	// function_executed payloads and functions.* completion calls. Keep the
	// durable run in that same public identifier space so transports never have
	// to invent a second execution identity.
	value, err := SlackID("Wx")
	return WorkflowRunID(value), err
}
func NewFunctionExecutionID() (WorkflowStepID, error) {
	value, err := SlackID("Fx")
	return WorkflowStepID(value), err
}
func NewReminderID() (ReminderID, error) {
	value, err := SlackID("Rm")
	return ReminderID(value), err
}

func NewLaterReminderID() (LaterReminderID, error) {
	value, err := PublicID("later_reminder_")
	return LaterReminderID(value), err
}

// NewProfileFieldID mints a custom-profile-field identifier. Slack names these
// Xf…; the prefix is what a client keys the value object by.
func NewProfileFieldID() (ProfileFieldID, error) {
	value, err := SlackID("Xf")
	return ProfileFieldID(value), err
}

// ActivityIDFor returns the stable identity of a notification-producing fact.
//
// Message delivery, reaction delivery, and reminder delivery are committed in
// the same transaction as their source mutation and may be retried. A random
// identifier would turn a successful retry into a duplicate notification. The
// source key includes the recipient and occurrence identity. The readable
// composition is deliberate: schema upgrades can derive the same key with
// portable SQL, so existing history does not disappear after an upgrade.
func ActivityIDFor(recipient UserID, sourceKey string) ActivityID {
	return ActivityID("activity:" + string(recipient) + ":" + sourceKey)
}

func NewSavedItemID() (SavedItemID, error) {
	value, err := PublicID("saved_")
	return SavedItemID(value), err
}

func NewConversationID() (ConversationID, error) {
	value, err := SlackID("C")
	return ConversationID(value), err
}

// NewDirectConversationID names a new direct conversation of the given kind; see
// DirectConversationIDPrefix. Existing conversations keep the identifier they
// were created with, which every stored reference already names.
func NewDirectConversationID(kind ConversationType) (ConversationID, error) {
	value, err := SlackID(DirectConversationIDPrefix(kind))
	return ConversationID(value), err
}

func NewBookmarkID() (BookmarkID, error) {
	value, err := SlackID("Bk")
	return BookmarkID(value), err
}

func NewScheduledMessageID() (ScheduledMessageID, error) {
	value, err := SlackID("Q")
	return ScheduledMessageID(value), err
}

func NewScheduledStatusID() (ScheduledStatusID, error) {
	value, err := PublicID("scheduled_status_")
	return ScheduledStatusID(value), err
}

func NewUserGroupID() (UserGroupID, error) {
	value, err := SlackID("S")
	return UserGroupID(value), err
}

// NewBarrierID mints an information barrier identifier. Slack prefixes one with
// B, as it does a bot, and the two never share a table.
func NewBarrierID() (BarrierID, error) {
	value, err := SlackID("B")
	return BarrierID(value), err
}

func NewCallID() (CallID, error) { value, err := SlackID("R"); return CallID(value), err }
func NewIncomingWebhookID() (IncomingWebhookID, error) {
	value, err := PublicID("wh_")
	return IncomingWebhookID(value), err
}
func NewWorkspaceID() (WorkspaceID, error) {
	value, err := SlackID("T")
	return WorkspaceID(value), err
}

func NewAppRequestID() (AppRequestID, error) {
	value, err := SlackID("R")
	return AppRequestID(value), err
}

func NewAppID() (AppID, error) {
	value, err := SlackID("A")
	return AppID(value), err
}

func NewViewID() (ViewID, error) {
	value, err := SlackID("V")
	return ViewID(value), err
}

func NewDialogID() (DialogID, error) {
	value, err := SlackID("D")
	return DialogID(value), err
}

func NewBotID() (BotID, error) {
	value, err := SlackID("B")
	return BotID(value), err
}

func NewOAuthToken() (string, error) { return NewUserToken() }

func NewUserToken() (string, error) { return PublicID("xoxp-") }

func NewBotToken() (string, error) { return PublicID("xoxb-") }

func NewRotatingUserToken() (string, error) { return PublicID("xoxe.xoxp-") }

func NewRotatingBotToken() (string, error) { return PublicID("xoxe.xoxb-") }

func NewRefreshToken() (string, error) { return PublicID("xoxe-") }

func NewAppToken() (string, error) { return PublicID("xapp-") }

func NewRTMConnectionID() (string, error) { return PublicID("rtm-") }

func NewSocketModeConnectionID() (string, error) { return PublicID("socket-") }
