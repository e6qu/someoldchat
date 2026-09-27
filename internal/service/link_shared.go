package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// linkSharedTopic journals the links a message shares. Slack sends link_shared
// to each app whose registered unfurl domains claim one of them, and the app
// answers with chat.unfurl. The record is written once, whatever apps are
// installed, and each app's delivery projects it: the domains an app claims
// are read from its manifest when the event is delivered, so no producer has
// to know which apps exist.
const linkSharedTopic = "link.shared"

// linkSharedSource is the only source this product produces. Slack's other
// value, composer, names a link typed into an unsent draft; the first-party
// client previews no drafts, so every shared link is in a posted message.
const linkSharedSource = "conversations_history"

// linkSharedSnapshot is the message fact a link.shared record carries in its
// private payload, like messageEventSnapshot: the links are message content,
// so the workspace journal names only the conversation and the message.
type linkSharedSnapshot struct {
	Channel   domain.ConversationID   `json:"channel"`
	User      domain.UserID           `json:"user"`
	MessageTS domain.MessageTimestamp `json:"message_ts"`
	ThreadTS  domain.MessageTimestamp `json:"thread_ts,omitempty"`
	Links     []string                `json:"links"`
}

// linkSharedEvents returns the link.shared record for the links message
// shares that previous did not, or none when it shares no new link. previous
// is nil for a new message; an edit notifies only the links it adds, so an
// app is not asked again to unfurl a link it has already been handed.
func linkSharedEvents(workspaceID domain.WorkspaceID, message domain.Message, previous *domain.Message, createdAt time.Time) ([]events.Event, error) {
	links := domain.LinksInMessage(message.Text, message.Blocks)
	if previous != nil && len(links) != 0 {
		before := make(map[string]struct{})
		for _, link := range domain.LinksInMessage(previous.Text, previous.Blocks) {
			before[link] = struct{}{}
		}
		added := links[:0:0]
		for _, link := range links {
			if _, shared := before[link]; !shared {
				added = append(added, link)
			}
		}
		links = added
	}
	if len(links) == 0 {
		return nil, nil
	}
	event, err := newEvent(workspaceID, message.AuthorID, events.NewPayload(linkSharedTopic,
		events.String("channel_id", string(message.Conversation)),
		events.String("message_id", string(message.ID)),
	), createdAt)
	if err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(linkSharedSnapshot{
		Channel: message.Conversation, User: message.AuthorID,
		MessageTS: domain.NewMessageTimestamp(message.CreatedAt), ThreadTS: message.ThreadTimestamp,
		Links: links,
	})
	if err != nil {
		return nil, err
	}
	event.PrivatePayload = string(snapshot)
	return []events.Event{event}, nil
}

// prepareAppLinkSharedEvent projects a link.shared record for one app. The
// app receives link_shared when its manifest's unfurl domains claim at least
// one of the links, for a conversation it can see: any public channel - Slack
// sends link_shared for a public channel the bot has not joined, and reports
// that with is_bot_user_member - and otherwise a conversation one of its
// authorized users or its bot belongs to. Authorizations have already been
// narrowed to those holding links:read.
func prepareAppLinkSharedEvent(ctx context.Context, state AppEventProjectionStore, appID domain.AppID, authorizations []domain.AppAuthorization, record events.Record) (events.Record, bool, error) {
	if strings.TrimSpace(record.Event.PrivatePayload) == "" {
		return events.Record{}, false, events.ErrPayloadFieldInvalid
	}
	var snapshot linkSharedSnapshot
	if err := json.Unmarshal([]byte(record.Event.PrivatePayload), &snapshot); err != nil ||
		snapshot.Channel == "" || snapshot.MessageTS == "" || len(snapshot.Links) == 0 {
		return events.Record{}, false, events.ErrPayloadFieldInvalid
	}
	if len(authorizations) == 0 {
		return record, false, nil
	}
	_, revision, err := state.GetApp(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return events.Record{}, false, err
	}
	parsed, problems := appmanifest.Parse(revision.Manifest)
	if len(problems) != 0 {
		return events.Record{}, false, ErrAppInteractionUnavailable
	}
	links := domain.SharedLinks(parsed.UnfurlDomains, snapshot.Links)
	if len(links) == 0 {
		return record, false, nil
	}
	conversation, err := state.GetConversation(ctx, snapshot.Channel)
	if errors.Is(err, store.ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return events.Record{}, false, err
	}
	if conversation.WorkspaceID != record.Event.WorkspaceID {
		return record, false, nil
	}
	if conversation.Kind.OrPublic() != domain.ConversationTypePublic {
		authorizations, err = visibleAppAuthorizations(ctx, state, authorizations, snapshot.Channel)
		if err != nil {
			return events.Record{}, false, err
		}
	}
	record, visible, err := withEventAuthorizations(record, authorizations)
	if err != nil || !visible {
		return record, visible, err
	}
	botMember := false
	if bot, err := state.GetBotByApp(ctx, record.Event.WorkspaceID, appID); err == nil && bot.UserID != "" {
		botMember, err = state.IsConversationMember(ctx, snapshot.Channel, bot.UserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return events.Record{}, false, err
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return events.Record{}, false, err
	}
	body := map[string]any{
		"type":               "link_shared",
		"channel":            snapshot.Channel,
		"is_bot_user_member": botMember,
		"user":               snapshot.User,
		"message_ts":         snapshot.MessageTS,
		"unfurl_id":          domain.NewUnfurlID(snapshot.Channel, snapshot.MessageTS),
		"source":             linkSharedSource,
		"event_ts":           string(domain.NewMessageTimestamp(record.Event.CreatedAt)),
		"links":              links,
	}
	if snapshot.ThreadTS != "" {
		body["thread_ts"] = snapshot.ThreadTS
	}
	return encodeProjectedEvent(record, body)
}
