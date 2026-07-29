package events

import (
	"context"
	"encoding/json"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// ConversationResolver supplies the channel kind needed to map Slack's generic
// message event onto one of the four manifest subscription names.
type ConversationResolver func(context.Context, domain.ConversationID) (domain.Conversation, error)

// FilterSubscribedSlackEventBodies returns only callbacks selected by an app's
// manifest. HTTP Events API delivery and Socket Mode must use this same rule:
// otherwise enabling Socket Mode silently turns a narrow subscription into a
// workspace-wide event tap.
func FilterSubscribedSlackEventBodies(ctx context.Context, bodies [][]byte, botEvents, userEvents []string, resolve ConversationResolver) ([][]byte, error) {
	subscriptions := make(map[string]bool, len(botEvents)+len(userEvents))
	for _, name := range botEvents {
		subscriptions[name] = true
	}
	for _, name := range userEvents {
		subscriptions[name] = true
	}
	filtered := make([][]byte, 0, len(bodies))
	for _, body := range bodies {
		var envelope struct {
			Event struct {
				Type    string                `json:"type"`
				Channel domain.ConversationID `json:"channel"`
			} `json:"event"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, err
		}
		subscribed := subscriptions[envelope.Event.Type]
		if envelope.Event.Type == "message" && envelope.Event.Channel != "" {
			if resolve == nil {
				return nil, ErrSlackEventIncomplete
			}
			conversation, err := resolve(ctx, envelope.Event.Channel)
			if err != nil {
				return nil, err
			}
			messageSubscription := "message.channels"
			switch {
			case conversation.IsDirect:
				messageSubscription = "message.im"
			case conversation.IsGroupDirect:
				messageSubscription = "message.mpim"
			case conversation.IsPrivate:
				messageSubscription = "message.groups"
			}
			subscribed = subscriptions[messageSubscription]
		}
		if subscribed {
			filtered = append(filtered, body)
		}
	}
	return filtered, nil
}
