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
	botSubscriptions := make(map[string]bool, len(botEvents))
	for _, name := range botEvents {
		botSubscriptions[name] = true
	}
	userSubscriptions := make(map[string]bool, len(userEvents))
	for _, name := range userEvents {
		userSubscriptions[name] = true
	}
	filtered := make([][]byte, 0, len(bodies))
	for _, body := range bodies {
		var envelope struct {
			Event struct {
				Type    string                `json:"type"`
				Channel domain.ConversationID `json:"channel"`
			} `json:"event"`
			Authorizations []struct {
				EnterpriseID        string             `json:"enterprise_id"`
				TeamID              domain.WorkspaceID `json:"team_id"`
				UserID              domain.UserID      `json:"user_id"`
				IsBot               bool               `json:"is_bot"`
				IsEnterpriseInstall bool               `json:"is_enterprise_install"`
			} `json:"authorizations"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, err
		}
		eventName := envelope.Event.Type
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
			eventName = messageSubscription
		}
		botSubscribed := botSubscriptions[eventName]
		userSubscribed := userSubscriptions[eventName]
		automatic := AutomaticSlackEvent(eventName)
		if !automatic && !botSubscribed && !userSubscribed {
			continue
		}
		if len(envelope.Authorizations) == 0 {
			// Compatibility fixtures written before authorization-aware
			// delivery still exercise subscription naming. Production records
			// carry explicit perspectives and take the branch below.
			filtered = append(filtered, body)
			continue
		}
		kept := envelope.Authorizations[:0]
		for _, authorization := range envelope.Authorizations {
			if automatic || authorization.IsBot && botSubscribed || !authorization.IsBot && userSubscribed {
				kept = append(kept, authorization)
			}
		}
		if len(kept) == 0 {
			continue
		}
		// Slack's current callback carries one representative installation.
		// apps.event.authorizations.list is the API for the complete set.
		kept = kept[:1]
		var object map[string]json.RawMessage
		if err := json.Unmarshal(body, &object); err != nil {
			return nil, err
		}
		encodedAuthorizations, err := json.Marshal(kept)
		if err != nil {
			return nil, err
		}
		object["authorizations"] = encodedAuthorizations
		encoded, err := encodeObject(object)
		if err != nil {
			return nil, err
		}
		filtered = append(filtered, []byte(encoded))
	}
	return filtered, nil
}

// SlackEventBodyAuthorizations returns the already subscription-filtered
// authorization perspective from a callback. Socket Mode rebuilds the same
// callback after the service claim crosses gRPC, so the selected perspective
// must travel on the record rather than being chosen again without a manifest.
func SlackEventBodyAuthorizations(body []byte) ([]Authorization, error) {
	var envelope struct {
		Authorizations []struct {
			EnterpriseID        string             `json:"enterprise_id"`
			TeamID              domain.WorkspaceID `json:"team_id"`
			UserID              domain.UserID      `json:"user_id"`
			IsBot               bool               `json:"is_bot"`
			IsEnterpriseInstall bool               `json:"is_enterprise_install"`
		} `json:"authorizations"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	result := make([]Authorization, 0, len(envelope.Authorizations))
	for _, value := range envelope.Authorizations {
		result = append(result, Authorization{
			EnterpriseID: value.EnterpriseID, TeamID: value.TeamID, UserID: value.UserID,
			IsBot: value.IsBot, IsEnterpriseInstall: value.IsEnterpriseInstall,
		})
	}
	return result, nil
}
