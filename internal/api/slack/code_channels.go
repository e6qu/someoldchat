package slack

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// agents.conversations.create, archive and setProperties: Slack Code's code
// channel methods. Each needs code_channels:manage on an app's bot token, and
// each takes its arguments by the names the official SDKs declare
// (@slack/web-api 8.2.0's AgentsConversations*Arguments).

// codeChannelRequest authenticates an agent's bot token holding
// code_channels:manage and reads the method's arguments.
func (h Handler) codeChannelRequest(w http.ResponseWriter, r *http.Request) (auth.Principal, map[string]string, bool) {
	principal, err := h.authenticate(r, auth.ScopeCodeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return auth.Principal{}, nil, false
	}
	if !principal.TokenType.IsBot() || principal.AppID == "" {
		writeError(w, "not_allowed_token_type")
		return auth.Principal{}, nil, false
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return auth.Principal{}, nil, false
	}
	return principal, fields, true
}

func (h Handler) createCodeChannel(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.codeChannelRequest(w, r)
	if !ok {
		return
	}
	// team_id names the workspace an organization token creates in; a
	// workspace token's own workspace is the only one there is.
	if team := strings.TrimSpace(fields["team_id"]); team != "" && domain.WorkspaceID(team) != principal.WorkspaceID {
		writeError(w, "invalid_arguments")
		return
	}
	private, err := parseBoolField(fields["is_private"])
	if err != nil {
		writeError(w, "invalid_arguments")
		return
	}
	value, err := h.Messages.CreateCodeChannel(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, domain.CodeChannelRequest{
		SessionID: fields["session_id"], Name: fields["name"], Private: private,
		Origin: domain.CodeChannelOrigin{
			Channel:   domain.ConversationID(strings.TrimSpace(fields["origin_channel_id"])),
			Timestamp: domain.MessageTimestamp(strings.TrimSpace(fields["origin_message_ts"])),
		},
	})
	if err != nil {
		writeCodeChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel_id": value.Conversation})
}

func (h Handler) archiveCodeChannel(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.codeChannelRequest(w, r)
	if !ok {
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arguments")
		return
	}
	if err := h.Messages.ArchiveCodeChannel(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, channel,
		domain.MessageTimestamp(strings.TrimSpace(fields["summary_message_ts"]))); err != nil {
		writeCodeChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// codeChannelPropertiesArgument is setProperties' code_channel object.
type codeChannelPropertiesArgument struct {
	ContextBarItems *[]struct {
		Key      string `json:"key"`
		Label    string `json:"label"`
		Icon     string `json:"icon"`
		URL      string `json:"url"`
		ItemType string `json:"item_type"`
	} `json:"context_bar_items"`
	SummaryMessage *struct {
		MessageTS string `json:"message_ts"`
		ThreadTS  string `json:"thread_ts"`
	} `json:"summary_message"`
}

// agentResourceArgument is setProperties' agent_resource object; each field
// given is written.
type agentResourceArgument struct {
	URL          *string `json:"url"`
	ResourceType *string `json:"resource_type"`
	Title        *string `json:"title"`
	Provider     *string `json:"provider"`
}

func (h Handler) setCodeChannelProperties(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.codeChannelRequest(w, r)
	if !ok {
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arguments")
		return
	}
	var properties domain.CodeChannelProperties
	if raw := strings.TrimSpace(fields["code_channel"]); raw != "" {
		var argument codeChannelPropertiesArgument
		if err := json.Unmarshal([]byte(raw), &argument); err != nil {
			writeError(w, "invalid_arguments")
			return
		}
		if argument.ContextBarItems != nil {
			items := make([]domain.CodeChannelContextItem, 0, len(*argument.ContextBarItems))
			for _, item := range *argument.ContextBarItems {
				items = append(items, domain.CodeChannelContextItem{Key: item.Key, Label: item.Label, Icon: item.Icon, URL: item.URL, ItemType: item.ItemType})
			}
			properties.ContextBar = &items
		}
		if argument.SummaryMessage != nil {
			if strings.TrimSpace(argument.SummaryMessage.MessageTS) == "" {
				writeError(w, "invalid_arguments")
				return
			}
			properties.Summary = &domain.CodeChannelSummary{
				MessageTimestamp: domain.MessageTimestamp(strings.TrimSpace(argument.SummaryMessage.MessageTS)),
				ThreadTimestamp:  domain.MessageTimestamp(strings.TrimSpace(argument.SummaryMessage.ThreadTS)),
			}
		}
	}
	if raw := strings.TrimSpace(fields["agent_resource"]); raw != "" {
		var argument agentResourceArgument
		if err := json.Unmarshal([]byte(raw), &argument); err != nil {
			writeError(w, "invalid_arguments")
			return
		}
		properties.AgentResource = domain.AgentResourcePatch{URL: argument.URL, ResourceType: argument.ResourceType, Title: argument.Title, Provider: argument.Provider}
	}
	if err := h.Messages.SetCodeChannelProperties(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, channel, properties); err != nil {
		writeCodeChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel_id": channel})
}

// writeCodeChannelError answers a code channel refusal. The method pages'
// own error lists were not available to this deployment, so each refusal is
// the general Web API code for its condition.
func writeCodeChannelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotInConversation):
		writeError(w, "not_in_channel")
	case errors.Is(err, domain.ErrNotCodeChannel):
		writeError(w, "channel_not_found")
	case errors.Is(err, domain.ErrAgentSessionNotAgent):
		writeError(w, "not_allowed_token_type")
	case errors.Is(err, domain.ErrInvalidCodeChannelName):
		writeError(w, "invalid_name")
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, "name_taken")
	case errors.Is(err, domain.ErrCodeChannelMessageNotFound):
		writeError(w, "message_not_found")
	case errors.Is(err, domain.ErrConversationAlreadyArchived):
		writeError(w, "already_archived")
	case errors.Is(err, domain.ErrInvalidCodeChannel), errors.Is(err, domain.ErrCodeChannelHasNoOrigin),
		errors.Is(err, domain.ErrOriginExternallyShared), errors.Is(err, domain.ErrInvalidTimestamp):
		writeError(w, "invalid_arguments")
	default:
		writeError(w, mapServiceError(err, "channel_not_found"))
	}
}

// describeCodeChannel adds a code channel's properties to its conversation
// object, as conversations.info reports them: code_channel carries the
// context bar, and agent_session the channel's own session — its status,
// agents, title and the message the work began from.
func (h Handler) describeCodeChannel(r *http.Request, principal auth.Principal, conversation domain.ConversationID, response map[string]any) error {
	record, err := h.Messages.CodeChannel(r.Context(), principal.WorkspaceID, principal.UserID, conversation)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, len(record.ContextBar))
	bots := []string{string(record.BotUserID)}
	for _, item := range record.ContextBar {
		entry := map[string]any{"key": item.Key, "label": item.Label, "item_type": item.ItemType, "bot_user_id": item.BotUserID}
		if item.Icon != "" {
			entry["icon"] = item.Icon
		}
		if item.URL != "" {
			entry["url"] = item.URL
		}
		items = append(items, entry)
		if !slices.Contains(bots, string(item.BotUserID)) {
			bots = append(bots, string(item.BotUserID))
		}
	}
	session := map[string]any{"status": domain.AgentSessionClosed, "title": "", "agent_bot_user_ids": bots, "encoded_agent_bot_user_ids": bots}
	if view, err := h.Messages.AgentSession(r.Context(), principal.WorkspaceID, principal.UserID, conversation, ""); err == nil {
		session["status"], session["title"] = view.Session.Status(), view.Session.Title
	} else if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, domain.ErrAgentSessionNotFound) {
		return err
	}
	if !record.Origin.Empty() {
		session["origin_link"] = map[string]any{"channel_id": record.Origin.Channel, "ts": record.Origin.Timestamp}
	}
	properties, _ := response["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties["code_channel"] = map[string]any{"context_bar_items": items}
	properties["agent_session"] = session
	response["properties"] = properties
	return nil
}
