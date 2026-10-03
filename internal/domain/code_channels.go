package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// A code channel is Slack Code's dedicated channel for one agent session: an
// agent creates it with agents.conversations.create, works in it with its
// members, describes the work in the channel's context bar, and archives it
// when the task is done, sharing a summary back to the message the work began
// from. The channel is also a "session channel": its agent session is the
// channel's own, so agents.sessions.* name it without a thread.
//
// The arguments and their bounds are those the official SDKs declare for the
// methods (@slack/web-api 8.2.0's AgentsConversations*Arguments).

// CodeChannel is a code channel's record beside its conversation.
type CodeChannel struct {
	WorkspaceID  WorkspaceID
	Conversation ConversationID
	// AppID and BotUserID are the agent that created the channel.
	AppID     AppID
	BotUserID UserID
	// SessionID is the agent's opaque key for its session; creating with a
	// SessionID the agent already used answers the existing channel.
	SessionID string
	// Origin is the message the work began from, when the channel was
	// created from one.
	Origin CodeChannelOrigin
	// ContextBar is every agent's items, ordered by agent and then as each
	// agent gave them.
	ContextBar []CodeChannelContextItem
	// Commands is every agent's slash commands, ordered by agent and then as
	// each agent gave them.
	Commands      []CodeChannelCommand
	Summary       CodeChannelSummary
	AgentResource AgentResource
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CodeChannelOrigin is the channel and message an agent session was started
// from. Both are set or neither is.
type CodeChannelOrigin struct {
	Channel   ConversationID
	Timestamp MessageTimestamp
}

// Empty reports a code channel created without an origin message.
func (o CodeChannelOrigin) Empty() bool {
	return o.Channel == "" && o.Timestamp == ""
}

// CodeChannelContextItem is one item of a code channel's context bar. Each
// belongs to the agent whose bot set it.
type CodeChannelContextItem struct {
	Key       string
	Label     string
	Icon      string
	URL       string
	ItemType  string
	BotUserID UserID
}

// CodeChannelSummary records which message represents the session's current
// summary, and its thread when it is a reply.
type CodeChannelSummary struct {
	MessageTimestamp MessageTimestamp
	ThreadTimestamp  MessageTimestamp
}

// AgentResource is the external resource an agent's work lives in, such as a
// pull request.
type AgentResource struct {
	URL          string
	ResourceType string
	Title        string
	Provider     string
}

// CodeChannelRequest is agents.conversations.create's arguments.
type CodeChannelRequest struct {
	SessionID string
	Name      string
	Private   bool
	Origin    CodeChannelOrigin
}

// CodeChannelProperties is agents.conversations.setProperties: each field that
// is set replaces what the channel holds, and a field left nil is unchanged.
type CodeChannelProperties struct {
	// ContextBar replaces the calling agent's items.
	ContextBar    *[]CodeChannelContextItem
	Summary       *CodeChannelSummary
	AgentResource AgentResourcePatch
}

// AgentResourcePatch updates the fields of the agent resource it names.
type AgentResourcePatch struct {
	URL          *string
	ResourceType *string
	Title        *string
	Provider     *string
}

// Empty reports a patch that names no field.
func (p AgentResourcePatch) Empty() bool {
	return p.URL == nil && p.ResourceType == nil && p.Title == nil && p.Provider == nil
}

// Apply returns the resource with the patch's fields written.
func (p AgentResourcePatch) Apply(resource AgentResource) AgentResource {
	for _, field := range []struct {
		value *string
		into  *string
	}{{p.URL, &resource.URL}, {p.ResourceType, &resource.ResourceType}, {p.Title, &resource.Title}, {p.Provider, &resource.Provider}} {
		if field.value != nil {
			*field.into = *field.value
		}
	}
	return resource
}

// Bounds the SDK declares for code channel properties.
const (
	CodeChannelContextBarLimit     = 5
	CodeChannelContextKeyLimit     = 64
	CodeChannelContextLabelLimit   = 128
	CodeChannelURLLimit            = 2048
	AgentResourceTypeLimit         = 64
	AgentResourceTitleLimit        = 255
	AgentResourceProviderLimit     = 64
	CodeChannelContextItemInfo     = "info"
	CodeChannelContextItemAction   = "action"
	codeChannelSessionIDLimit      = 255
	CodeChannelNameLimit           = 80
	codeChannelNameFromOriginLimit = 60
)

// codeChannelContextIcons are the icons a context bar item may name.
var codeChannelContextIcons = map[string]bool{
	"branch": true, "folder": true, "hierarchy": true, "life-ring": true, "link": true,
	"globe": true, "terminal": true, "code": true, "search": true, "lock": true,
}

// ErrInvalidCodeChannel is a code channel argument outside its declared shape
// or bounds.
var ErrInvalidCodeChannel = errors.New("invalid code channel argument")

// ValidCodeChannelSessionID reports whether an agent's session key is usable.
func ValidCodeChannelSessionID(value string) bool {
	return utf8.RuneCountInString(value) <= codeChannelSessionIDLimit && !strings.ContainsAny(value, "\r\n")
}

// NormalizeContextBar validates an agent's context bar items and stamps them
// with the agent's bot. Keys are unique within the set; an item_type left out
// is info.
func NormalizeContextBar(items []CodeChannelContextItem, bot UserID) ([]CodeChannelContextItem, error) {
	if len(items) > CodeChannelContextBarLimit {
		return nil, ErrInvalidCodeChannel
	}
	keys := make(map[string]bool, len(items))
	normalized := make([]CodeChannelContextItem, 0, len(items))
	for _, item := range items {
		item.Key, item.Label, item.Icon, item.URL, item.ItemType = strings.TrimSpace(item.Key), strings.TrimSpace(item.Label), strings.TrimSpace(item.Icon), strings.TrimSpace(item.URL), strings.TrimSpace(item.ItemType)
		if item.ItemType == "" {
			item.ItemType = CodeChannelContextItemInfo
		}
		if item.Key == "" || utf8.RuneCountInString(item.Key) > CodeChannelContextKeyLimit || keys[item.Key] ||
			item.Label == "" || utf8.RuneCountInString(item.Label) > CodeChannelContextLabelLimit ||
			(item.Icon != "" && !codeChannelContextIcons[item.Icon]) ||
			(item.URL != "" && !ValidCodeChannelURL(item.URL)) ||
			(item.ItemType != CodeChannelContextItemInfo && item.ItemType != CodeChannelContextItemAction) {
			return nil, ErrInvalidCodeChannel
		}
		keys[item.Key] = true
		item.BotUserID = bot
		normalized = append(normalized, item)
	}
	return normalized, nil
}

// ValidCodeChannelURL reports an absolute http or https URL within the bound.
func ValidCodeChannelURL(value string) bool {
	if len(value) > CodeChannelURLLimit {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

// ValidAgentResource reports a resource within the declared bounds.
func ValidAgentResource(resource AgentResource) bool {
	return (resource.URL == "" || ValidCodeChannelURL(resource.URL)) &&
		utf8.RuneCountInString(resource.ResourceType) <= AgentResourceTypeLimit &&
		utf8.RuneCountInString(resource.Title) <= AgentResourceTitleLimit &&
		utf8.RuneCountInString(resource.Provider) <= AgentResourceProviderLimit
}

// WithAgentContextBar is the channel's context bar with one agent's items
// replaced, every other agent's kept, and the whole bar within the limit.
func (c CodeChannel) WithAgentContextBar(bot UserID, items []CodeChannelContextItem) ([]CodeChannelContextItem, error) {
	merged := make([]CodeChannelContextItem, 0, len(c.ContextBar)+len(items))
	for _, item := range c.ContextBar {
		if item.BotUserID != bot {
			merged = append(merged, item)
		}
	}
	merged = append(merged, items...)
	if len(merged) > CodeChannelContextBarLimit {
		return nil, ErrInvalidCodeChannel
	}
	return merged, nil
}

// CodeChannelName is the channel name a code channel is given: the agent's
// friendly name folded to a channel name, or the start of its origin message
// when it gave none. Channel names are lowercase, without spaces or periods,
// and at most 80 characters.
func CodeChannelName(friendly, originText string) string {
	source := friendly
	limit := CodeChannelNameLimit
	if strings.TrimSpace(source) == "" {
		source, limit = originText, codeChannelNameFromOriginLimit
	}
	var name strings.Builder
	dash := false
	for _, r := range strings.ToLower(source) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			name.WriteRune(r)
			dash = false
		case !dash && name.Len() > 0:
			name.WriteRune('-')
			dash = true
		}
		if name.Len() >= limit {
			break
		}
	}
	return strings.Trim(name.String(), "-_")
}

// The agents.conversations.* refusals.
var (
	// ErrNotCodeChannel is a channel that is not a code channel, named to a
	// method that only acts on one (channel_not_found).
	ErrNotCodeChannel = errors.New("conversation is not a code channel")
	// ErrCodeChannelHasNoOrigin is a summary to share back from a channel
	// created without an origin message (invalid_arguments).
	ErrCodeChannelHasNoOrigin = errors.New("code channel has no origin message")
	// ErrInvalidCodeChannelName is a channel name that folds to nothing
	// (invalid_name).
	ErrInvalidCodeChannelName = errors.New("code channel name is invalid")
	// ErrOriginExternallyShared is an origin channel shared with another
	// organization, which a code channel may not start from.
	ErrOriginExternallyShared = errors.New("origin channel is externally shared")
	// ErrCodeChannelMessageNotFound is an origin, summary or shared-back
	// message that does not exist (message_not_found).
	ErrCodeChannelMessageNotFound = errors.New("code channel message not found")
)
