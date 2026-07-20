package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
type ListDownloadID string
type FileCommentID string
type ExternalUploadID string
type ReminderID string
type ScheduledMessageID string
type UserGroupID string
type InviteRequestID string
type AppID string
type AppRequestID string
type CallID string
type BookmarkID string
type ViewID string
type WorkflowStepID string
type DialogID string
type BotID string
type IncomingWebhookID string
type MessageTimestamp string

func NewMessageTimestamp(value time.Time) MessageTimestamp {
	return MessageTimestamp(fmt.Sprintf("%d.%06d", value.Unix(), value.Nanosecond()/1000))
}

func ParseMessageTimestamp(value MessageTimestamp) (time.Time, error) {
	parts := strings.Split(string(value), ".")
	if len(parts) != 2 || len(parts[1]) != 6 {
		return time.Time{}, fmt.Errorf("invalid message timestamp")
	}
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid message timestamp: %w", err)
	}
	micros, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || micros < 0 || micros > 999999 {
		return time.Time{}, fmt.Errorf("invalid message timestamp")
	}
	return time.Unix(seconds, micros*1000).UTC(), nil
}

// PublicID is deliberately opaque. The prefix is part of the wire contract;
// the random suffix is not used as an ordering key.
func PublicID(prefix string) (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func IsPublicID(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) == len(prefix)+20
}

func NewMessageID() (MessageID, error) { value, err := PublicID("msg_"); return MessageID(value), err }
func NewEventID() (EventID, error)     { value, err := PublicID("evt_"); return EventID(value), err }
func NewFileID() (FileID, error)       { value, err := PublicID("file_"); return FileID(value), err }
func NewCanvasID() (CanvasID, error)   { value, err := PublicID("F"); return CanvasID(value), err }
func NewUserID() (UserID, error)       { value, err := PublicID("U"); return UserID(value), err }
func NewListID() (ListID, error)       { value, err := PublicID("F"); return ListID(value), err }
func NewExternalUploadID() (ExternalUploadID, error) {
	value, err := PublicID("upload_")
	return ExternalUploadID(value), err
}
func NewListItemID() (ListItemID, error) {
	value, err := PublicID("Rec")
	return ListItemID(value), err
}
func NewListDownloadID() (ListDownloadID, error) {
	value, err := PublicID("export_")
	return ListDownloadID(value), err
}
func NewReminderID() (ReminderID, error) {
	value, err := PublicID("Rm")
	if err != nil {
		return "", err
	}
	return ReminderID("Rm" + strings.ToUpper(value[2:])), nil
}

func NewConversationID() (ConversationID, error) {
	value, err := PublicID("C")
	return ConversationID(value), err
}

func NewBookmarkID() (BookmarkID, error) {
	value, err := PublicID("Bk")
	return BookmarkID(value), err
}

func NewScheduledMessageID() (ScheduledMessageID, error) {
	value, err := PublicID("Q")
	if err != nil {
		return "", err
	}
	return ScheduledMessageID("Q" + strings.ToUpper(value[1:])), nil
}

func NewUserGroupID() (UserGroupID, error) {
	value, err := PublicID("S")
	if err != nil {
		return "", err
	}
	return UserGroupID("S" + strings.ToUpper(value[1:])), nil
}

func NewCallID() (CallID, error) { value, err := PublicID("call_"); return CallID(value), err }
func NewIncomingWebhookID() (IncomingWebhookID, error) {
	value, err := PublicID("wh_")
	return IncomingWebhookID(value), err
}
func NewWorkspaceID() (WorkspaceID, error) {
	value, err := PublicID("T")
	return WorkspaceID(value), err
}

func NewAppRequestID() (AppRequestID, error) {
	value, err := PublicID("R")
	return AppRequestID(value), err
}

func NewViewID() (ViewID, error) {
	value, err := PublicID("V")
	return ViewID(value), err
}

func NewDialogID() (DialogID, error) {
	value, err := PublicID("D")
	return DialogID(value), err
}

func NewOAuthToken() (string, error) { return PublicID("xoxp-") }

func NewRTMConnectionID() (string, error) { return PublicID("rtm-") }

func NewSocketModeConnectionID() (string, error) { return PublicID("socket-") }
