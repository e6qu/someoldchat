package slack

import (
	"encoding/json"
	"net/http"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// absoluteURL resolves a server-relative path against the origin origin.go
// chooses.
func (h Handler) absoluteURL(r *http.Request, path string) string {
	return originURL(h.origin(r), path)
}

// permalink is Slack's message link on this server's origin.
func (h Handler) permalink(r *http.Request, message domain.Message) string {
	return messagePermalink(h.origin(r), message)
}

// messagePermalink is a message's absolute link on origin. A reply links into
// its thread the way Slack's does. It is a function of the message alone, so
// a page of results needs no call per row to build them.
func messagePermalink(origin string, message domain.Message) string {
	return origin + domain.MessagePermalinkPath(message.Conversation, domain.NewMessageTimestamp(message.CreatedAt), message.ThreadTimestamp)
}

// messageContext is what one conversation's messages carry beside their own
// content: thread summaries for the roots, reactions and pins for every
// message, and the bot profiles of the apps that posted. Each is one batched
// read for the whole page, so projecting a history page of two hundred
// messages costs a constant number of calls rather than several per row.
type messageContext struct {
	conversation domain.ConversationID
	summaries    map[domain.MessageTimestamp]domain.ThreadSummary
	annotations  map[domain.MessageID]domain.MessageAnnotation
	bots         map[domain.BotID]domain.Bot
	authors      map[domain.MessageTimestamp]domain.UserID
	userToken    bool
	origin       string
}

func (h Handler) loadMessageContext(r *http.Request, principal auth.Principal, conversation domain.ConversationID, messages []domain.Message) (messageContext, error) {
	ctx := r.Context()
	value := messageContext{
		origin:       h.origin(r),
		conversation: conversation,
		bots:         make(map[domain.BotID]domain.Bot),
		authors:      make(map[domain.MessageTimestamp]domain.UserID, len(messages)),
		userToken:    !principal.TokenType.IsBot(),
	}
	if len(messages) == 0 {
		return value, nil
	}
	roots := make([]domain.MessageTimestamp, 0, len(messages))
	ids := make([]domain.MessageID, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
		timestamp := domain.NewMessageTimestamp(message.CreatedAt)
		value.authors[timestamp] = message.AuthorID
		if message.ThreadTimestamp == "" {
			roots = append(roots, timestamp)
		}
		if bot := messageBotID(message); bot != "" {
			if _, seen := value.bots[bot]; !seen {
				// A bot that has since been removed is still named by its
				// messages; its profile is simply absent, which is what Slack
				// shows for a deleted app's old messages.
				if resolved, err := h.Messages.BotInfo(ctx, principal.WorkspaceID, principal.UserID, bot); err == nil {
					value.bots[bot] = resolved
				} else {
					value.bots[bot] = domain.Bot{}
				}
			}
		}
	}
	var err error
	if len(roots) > 0 {
		if value.summaries, err = h.Messages.ThreadSummaries(ctx, principal.WorkspaceID, principal.UserID, conversation, roots); err != nil {
			return messageContext{}, err
		}
	}
	if value.annotations, err = h.Messages.MessageAnnotations(ctx, principal.WorkspaceID, principal.UserID, conversation, ids); err != nil {
		return messageContext{}, err
	}
	return value, nil
}

// projectMessages renders one conversation's messages as Slack message
// objects with their full context.
func (h Handler) projectMessages(r *http.Request, principal auth.Principal, conversation domain.ConversationID, messages []domain.Message) ([]map[string]any, error) {
	loaded, err := h.loadMessageContext(r, principal, conversation, messages)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		result = append(result, loaded.project(message))
	}
	return result, nil
}

// projectMessage is projectMessages for a single message.
func (h Handler) projectMessage(r *http.Request, principal auth.Principal, message domain.Message) (map[string]any, error) {
	projected, err := h.projectMessages(r, principal, message.Conversation, []domain.Message{message})
	if err != nil {
		return nil, err
	}
	return projected[0], nil
}

func (c messageContext) project(message domain.Message) map[string]any {
	result := messageResponse(c.origin, message)
	result["team"] = message.WorkspaceID
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	if summary, ok := c.summaries[timestamp]; ok && summary.ReplyCount > 0 && message.ThreadTimestamp == "" {
		// A parent names its own ts as its thread_ts; that is how every
		// client tells a thread's root from an unthreaded message.
		result["thread_ts"] = timestamp
		result["reply_count"] = summary.ReplyCount
		users := summary.Participants
		if users == nil {
			users = []domain.UserID{}
		}
		result["reply_users"] = users
		result["reply_users_count"] = len(summary.Participants)
		if !summary.LastReplyAt.IsZero() {
			result["latest_reply"] = domain.NewMessageTimestamp(summary.LastReplyAt)
		}
		if c.userToken {
			result["subscribed"] = summary.Subscribed
		}
	}
	if message.ThreadTimestamp != "" {
		if parent, ok := c.authors[message.ThreadTimestamp]; ok {
			result["parent_user_id"] = parent
		}
	}
	if annotation, ok := c.annotations[message.ID]; ok {
		if len(annotation.Reactions) > 0 {
			reactions := make([]map[string]any, 0, len(annotation.Reactions))
			for _, reaction := range annotation.Reactions {
				reactions = append(reactions, reactionResponse(reaction))
			}
			result["reactions"] = reactions
		}
		if annotation.Pinned {
			result["pinned_to"] = []domain.ConversationID{c.conversation}
		}
	}
	if bot, ok := c.bots[messageBotID(message)]; ok && bot.ID != "" {
		result["bot_profile"] = botProfileResponse(bot)
	}
	return result
}

func reactionResponse(reaction domain.ReactionSummary) map[string]any {
	users := reaction.Users
	if users == nil {
		users = []domain.UserID{}
	}
	return map[string]any{"name": reaction.Name, "users": users, "count": reaction.Count}
}

// botProfileResponse is Slack's bot_profile object. The icons are the bot's
// own; a message that customised its icon carries that in `icons` beside it.
func botProfileResponse(bot domain.Bot) map[string]any {
	return map[string]any{
		"id": bot.ID, "app_id": bot.AppID, "name": bot.Name, "deleted": bot.Deleted,
		"updated": bot.UpdatedAt.Unix(), "team_id": bot.WorkspaceID,
		"icons": map[string]string{"image_36": bot.Image36, "image_48": bot.Image48, "image_72": bot.Image72},
	}
}

// messageBotID reads the posting bot's identity from the message's stream
// state, where the service records it for every bot-token post.
func messageBotID(message domain.Message) domain.BotID {
	if message.StreamState == "" {
		return ""
	}
	var state domain.MessageStreamState
	if json.Unmarshal([]byte(message.StreamState), &state) != nil {
		return ""
	}
	return state.BotID
}
