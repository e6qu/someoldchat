package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// builtInSlashCommands are the Slack-owned commands this product implements.
// Only a command with a real first-party effect is listed: /collapse and
// /expand have no collapsible inline media to act on here, and /giphy needs a
// third-party service, so they remain recorded gaps rather than menu entries
// that post a success-looking no-op.
func builtInSlashCommands() []domain.AppShortcut {
	return []domain.AppShortcut{
		{AppName: "Slack", Name: "/away", Command: "/away", Description: "Toggle your away status", Type: "slash"},
		{AppName: "Slack", Name: "/dm", Command: "/dm", Description: "Send a direct message to someone", UsageHint: "@person [your message]", Type: "slash"},
		{AppName: "Slack", Name: "/dnd", Command: "/dnd", Description: "Pause notifications", UsageHint: "[30 minutes, 2 hours] or off", Type: "slash"},
		{AppName: "Slack", Name: "/invite", Command: "/invite", Description: "Add someone to this channel", UsageHint: "@person [#channel]", Type: "slash"},
		{AppName: "Slack", Name: "/join", Command: "/join", Description: "Join a channel", UsageHint: "#channel", Type: "slash"},
		{AppName: "Slack", Name: "/leave", Command: "/leave", Description: "Leave this channel", Type: "slash"},
		{AppName: "Slack", Name: "/mentions", Command: "/mentions", Description: "Open your mentions", Type: "slash"},
		{AppName: "Slack", Name: "/msg", Command: "/msg", Description: "Send a direct message to someone", UsageHint: "@person [your message]", Type: "slash"},
		{AppName: "Slack", Name: "/people", Command: "/people", Description: "Open the people directory", Type: "slash"},
		{AppName: "Slack", Name: "/remind", Command: "/remind", Description: "Set a channel reminder", UsageHint: "[#channel] [what] [when] or list", Type: "slash"},
		{AppName: "Slack", Name: "/search", Command: "/search", Description: "Search messages", UsageHint: "[search terms]", Type: "slash"},
		{AppName: "Slack", Name: "/shrug", Command: "/shrug", Description: "Add ¯\\_(ツ)_/¯ to your message", UsageHint: "[message]", Type: "slash"},
		{AppName: "Slack", Name: "/status", Command: "/status", Description: "Set your status", UsageHint: "[:emoji:] [status text] or clear", Type: "slash"},
		{AppName: "Slack", Name: "/topic", Command: "/topic", Description: "Set the channel topic", UsageHint: "[text]", Type: "slash"},
	}
}

// slashCommandRefusal is a built-in command that could not do what it was
// asked, for a reason the member can act on. postMessage renders its text
// with its status rather than reporting an outage.
type slashCommandRefusal struct {
	Status int
	Reason string
}

func (refusal slashCommandRefusal) Error() string { return refusal.Reason }

func refuse(status int, reason string) error {
	return slashCommandRefusal{Status: status, Reason: reason}
}

var (
	slashUserReference    = regexp.MustCompile(`^<@([A-Z0-9]+)(?:\|[^>]*)?>$`)
	slashChannelReference = regexp.MustCompile(`^<#([A-Z0-9]+)(?:\|[^>]*)?>$`)
)

// commandNoticeURL returns to the conversation with a notice, which is how
// the page confirms a command that changes something other than the timeline.
func commandNoticeURL(channel domain.ConversationID, thread domain.MessageTimestamp, notice string) string {
	query := url.Values{"channel": {string(channel)}, "notice": {notice}}
	if thread != "" {
		query.Set("thread", string(thread))
	}
	return "/app?" + query.Encode()
}

// dispatchBuiltInSlashCommand keeps Slack-owned commands ahead of installed
// app commands. It reports handled=false for a command it does not own, so
// the caller can dispatch it to the installing app.
func (h Handler) dispatchBuiltInSlashCommand(ctx context.Context, principal auth.Principal, channel domain.ConversationID, thread domain.MessageTimestamp, command, text, timeZone string) (domain.Message, string, bool, error) {
	text = strings.TrimSpace(text)
	switch strings.ToLower(command) {
	case "/shrug":
		body := text
		if body != "" {
			body += " "
		}
		body += `¯\\\_(ツ)\_/¯`
		message, err := h.Messages.Post(ctx, principal.WorkspaceID, principal.UserID, channel, body, thread, "")
		return message, "", true, err
	case "/search":
		if text == "" {
			return domain.Message{}, "", true, service.ErrInvalidSearch
		}
		values := url.Values{"q": {text}, "channel": {string(channel)}}
		return domain.Message{}, "/app/search?" + values.Encode(), true, nil
	case "/people":
		return domain.Message{}, "/app/members", true, nil
	case "/mentions":
		return domain.Message{}, "/app/activity?channel=" + url.QueryEscape(string(channel)), true, nil
	case "/remind":
		if thread != "" {
			return domain.Message{}, "", true, service.ErrSlashCommandInThread
		}
		if strings.EqualFold(text, "list") {
			values := url.Values{"channel": {string(channel)}, "filter": {"channel-reminders"}}
			return domain.Message{}, "/app/later?" + values.Encode(), true, nil
		}
		// A channel chosen from the composer's suggestions arrives as <#C…>;
		// the reminder grammar names it as #name.
		if fields := strings.Fields(text); len(fields) > 0 && slashChannelReference.MatchString(fields[0]) {
			if target, err := h.commandChannel(ctx, principal, fields[0]); err == nil {
				text = "#" + conversationName(target) + strings.TrimPrefix(text, fields[0])
			}
		}
		request, parseErr := h.channelReminderRequest(ctx, principal, channel, text, timeZone, time.Now().UTC())
		if parseErr != nil {
			return domain.Message{}, "", true, parseErr
		}
		if _, createErr := h.Messages.CreateLaterReminder(ctx, principal.WorkspaceID, principal.UserID, request); createErr != nil {
			return domain.Message{}, "", true, createErr
		}
		values := url.Values{"channel": {string(channel)}, "filter": {"channel-reminders"}, "changed": {"reminder"}}
		return domain.Message{}, "/app/later?" + values.Encode(), true, nil
	case "/away":
		redirect, err := h.toggleAway(ctx, principal, channel, thread)
		return domain.Message{}, redirect, true, err
	case "/dnd":
		redirect, err := h.commandDoNotDisturb(ctx, principal, channel, thread, text)
		return domain.Message{}, redirect, true, err
	case "/status":
		redirect, err := h.commandStatus(ctx, principal, channel, thread, text)
		return domain.Message{}, redirect, true, err
	case "/topic":
		redirect, err := h.commandTopic(ctx, principal, channel, thread, text)
		return domain.Message{}, redirect, true, err
	case "/invite":
		redirect, err := h.commandInvite(ctx, principal, channel, thread, text)
		return domain.Message{}, redirect, true, err
	case "/join":
		redirect, err := h.commandJoin(ctx, principal, text)
		return domain.Message{}, redirect, true, err
	case "/leave", "/part":
		redirect, err := h.commandLeave(ctx, principal, channel)
		return domain.Message{}, redirect, true, err
	case "/msg", "/dm":
		redirect, err := h.commandDirectMessage(ctx, principal, text)
		return domain.Message{}, redirect, true, err
	default:
		return domain.Message{}, "", false, nil
	}
}

func (h Handler) toggleAway(ctx context.Context, principal auth.Principal, channel domain.ConversationID, thread domain.MessageTimestamp) (string, error) {
	user, err := h.Messages.UserInfo(ctx, principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		return "", err
	}
	next, notice := domain.PresenceAway, "You are now set to away."
	if user.Presence == domain.PresenceAway {
		next, notice = domain.PresenceAuto, "You are no longer set to away."
	}
	if _, err := h.Messages.SetUserPresence(ctx, principal.WorkspaceID, principal.UserID, next); err != nil {
		return "", err
	}
	return commandNoticeURL(channel, thread, notice), nil
}

// commandDuration reads Slack's "/dnd 30 minutes" and "/dnd 2 hours" forms.
func commandDuration(text string) (int64, bool) {
	fields := strings.Fields(strings.ToLower(text))
	if len(fields) == 0 || len(fields) > 2 {
		return 0, false
	}
	number, unit := fields[0], ""
	if len(fields) == 2 {
		unit = fields[1]
	} else if trimmed := strings.TrimRight(number, "abcdefghijklmnopqrstuvwxyz"); trimmed != number {
		number, unit = trimmed, number[len(trimmed):]
	}
	value, err := strconv.ParseInt(number, 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	switch unit {
	case "", "m", "min", "mins", "minute", "minutes":
	case "h", "hr", "hrs", "hour", "hours":
		value *= 60
	default:
		return 0, false
	}
	if value > 24*60 {
		return 0, false
	}
	return value, true
}

func (h Handler) commandDoNotDisturb(ctx context.Context, principal auth.Principal, channel domain.ConversationID, thread domain.MessageTimestamp, text string) (string, error) {
	if strings.EqualFold(text, "off") {
		if _, err := h.Messages.EndSnooze(ctx, principal.WorkspaceID, principal.UserID); err != nil {
			return "", err
		}
		return commandNoticeURL(channel, thread, "Notifications are resumed."), nil
	}
	minutes, ok := commandDuration(text)
	if !ok {
		return "", refuse(http.StatusBadRequest, "Use /dnd with a duration of up to 24 hours, for example /dnd 30 minutes or /dnd 2 hours, or /dnd off.")
	}
	if _, err := h.Messages.SetSnooze(ctx, principal.WorkspaceID, principal.UserID, minutes); err != nil {
		return "", err
	}
	return commandNoticeURL(channel, thread, "Notifications are paused for "+strconv.FormatInt(minutes, 10)+" minutes."), nil
}

func (h Handler) commandStatus(ctx context.Context, principal auth.Principal, channel domain.ConversationID, thread domain.MessageTimestamp, text string) (string, error) {
	user, err := h.Messages.UserInfo(ctx, principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		return "", err
	}
	profile := user.Profile
	notice := "Your status is cleared."
	if text == "" || strings.EqualFold(text, "clear") {
		profile.StatusText, profile.StatusEmoji = "", ""
	} else {
		emoji := ""
		if fields := strings.Fields(text); len(fields) > 0 && len(fields[0]) > 2 && strings.HasPrefix(fields[0], ":") && strings.HasSuffix(fields[0], ":") {
			emoji = fields[0]
			text = strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		}
		if emoji == "" {
			emoji = ":speech_balloon:"
		}
		profile.StatusEmoji, profile.StatusText = emoji, text
		notice = "Your status is set."
	}
	profile.StatusExpiration = time.Time{}
	if _, err := h.Messages.SetUserProfile(ctx, principal.WorkspaceID, principal.UserID, profile); err != nil {
		if errors.Is(err, service.ErrInvalidProfile) {
			return "", refuse(http.StatusBadRequest, "Use /status with a known emoji and up to 100 characters, for example /status :palm_tree: On holiday, or /status clear.")
		}
		return "", err
	}
	return commandNoticeURL(channel, thread, notice), nil
}

func (h Handler) commandTopic(ctx context.Context, principal auth.Principal, channel domain.ConversationID, thread domain.MessageTimestamp, text string) (string, error) {
	if _, err := h.Messages.SetConversationTopic(ctx, principal.WorkspaceID, principal.UserID, channel, text); err != nil {
		if errors.Is(err, service.ErrInvalidConversation) || errors.Is(err, service.ErrInvalidMessage) {
			return "", refuse(http.StatusBadRequest, "That topic cannot be set here. A topic is up to 250 characters and belongs to a channel.")
		}
		return "", err
	}
	notice := "The channel topic is updated."
	if text == "" {
		notice = "The channel topic is cleared."
	}
	return commandNoticeURL(channel, thread, notice), nil
}

// commandUser resolves "<@U…>" (what the composer inserts) or "@name".
func (h Handler) commandUser(ctx context.Context, principal auth.Principal, token string) (domain.User, error) {
	if match := slashUserReference.FindStringSubmatch(token); match != nil {
		return h.Messages.UserInfo(ctx, principal.WorkspaceID, principal.UserID, domain.UserID(match[1]))
	}
	name := strings.ToLower(strings.TrimPrefix(token, "@"))
	if name == "" {
		return domain.User{}, store.ErrNotFound
	}
	cursor := domain.Cursor("")
	for page := 0; page < 10; page++ {
		users, err := h.Messages.Users(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 200, Cursor: cursor})
		if err != nil {
			return domain.User{}, err
		}
		for _, user := range users.Users {
			if user.Deleted {
				continue
			}
			for _, candidate := range []string{user.Name, user.Profile.DisplayName, user.RealName} {
				if strings.EqualFold(strings.TrimSpace(candidate), name) {
					return user, nil
				}
			}
		}
		if !users.HasMore || users.NextCursor == "" {
			break
		}
		cursor = users.NextCursor
	}
	return domain.User{}, store.ErrNotFound
}

// commandChannel resolves "<#C…>" or "#name" among the channels the member can
// see.
func (h Handler) commandChannel(ctx context.Context, principal auth.Principal, token string) (domain.Conversation, error) {
	if match := slashChannelReference.FindStringSubmatch(token); match != nil {
		return h.Messages.ConversationInfo(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationID(match[1]))
	}
	name := strings.TrimPrefix(token, "#")
	if name == "" || name == token {
		return domain.Conversation{}, store.ErrNotFound
	}
	page, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{
		Limit: searchFilterOptionLimit, ExcludeArchived: true,
		Types: []domain.ConversationType{domain.ConversationTypePublic, domain.ConversationTypePrivate},
	})
	if err != nil {
		return domain.Conversation{}, err
	}
	for _, conversation := range page.Conversations {
		if strings.EqualFold(conversation.Name, name) {
			return conversation, nil
		}
	}
	return domain.Conversation{}, store.ErrNotFound
}

func (h Handler) commandInvite(ctx context.Context, principal auth.Principal, channel domain.ConversationID, thread domain.MessageTimestamp, text string) (string, error) {
	if !principal.HasScope(auth.ScopeChannelsManage) {
		return "", auth.ErrMissingScope
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", refuse(http.StatusBadRequest, "Use /invite @person, or /invite @person #channel.")
	}
	var people []domain.User
	target := channel
	for _, field := range fields {
		if strings.HasPrefix(field, "#") || slashChannelReference.MatchString(field) {
			conversation, err := h.commandChannel(ctx, principal, field)
			if err != nil {
				return "", refuse(http.StatusNotFound, "That channel was not found, or you cannot see it.")
			}
			target = conversation.ID
			continue
		}
		user, err := h.commandUser(ctx, principal, field)
		if err != nil {
			return "", refuse(http.StatusNotFound, "No one in this workspace matches "+field+".")
		}
		people = append(people, user)
	}
	if len(people) == 0 {
		return "", refuse(http.StatusBadRequest, "Name at least one person to add, for example /invite @alex.")
	}
	ids := make([]domain.UserID, 0, len(people))
	names := make([]string, 0, len(people))
	for _, person := range people {
		ids = append(ids, person.ID)
		names = append(names, "@"+displayName(person))
	}
	conversation, err := h.Messages.InviteConversationMembers(ctx, principal.WorkspaceID, principal.UserID, target, ids)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAlreadyExists):
			return "", refuse(http.StatusConflict, strings.Join(names, ", ")+" is already in that channel.")
		case errors.Is(err, service.ErrInvalidConversation):
			return "", refuse(http.StatusBadRequest, "People can be added to channels, not to direct messages.")
		}
		return "", err
	}
	return commandNoticeURL(channel, thread, "Added "+strings.Join(names, ", ")+" to #"+conversationName(conversation)+"."), nil
}

func (h Handler) commandJoin(ctx context.Context, principal auth.Principal, text string) (string, error) {
	if !principal.HasScope(auth.ScopeChannelsManage) {
		return "", auth.ErrMissingScope
	}
	if text == "" {
		return "", refuse(http.StatusBadRequest, "Use /join #channel.")
	}
	token := strings.Fields(text)[0]
	if !strings.HasPrefix(token, "#") && !slashChannelReference.MatchString(token) {
		token = "#" + token
	}
	target, err := h.commandChannel(ctx, principal, token)
	if err != nil {
		return "", refuse(http.StatusNotFound, "That channel was not found, or it is private.")
	}
	if _, err := h.Messages.JoinConversation(ctx, principal.WorkspaceID, principal.UserID, target.ID); err != nil {
		return "", err
	}
	return appURL(string(target.ID), "", "", "", ""), nil
}

func (h Handler) commandLeave(ctx context.Context, principal auth.Principal, channel domain.ConversationID) (string, error) {
	conversation, err := h.Messages.ConversationInfo(ctx, principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		return "", err
	}
	if err := h.Messages.LeaveConversation(ctx, principal.WorkspaceID, principal.UserID, channel); err != nil {
		if errors.Is(err, service.ErrCannotLeaveDefault) {
			return "", refuse(http.StatusForbidden, "Everyone in the workspace stays in #"+conversationName(conversation)+", so it cannot be left.")
		}
		return "", err
	}
	query := url.Values{"channel": {string(h.Channel)}, "notice": {"You left #" + conversationName(conversation) + "."}}
	return "/app?" + query.Encode(), nil
}

func (h Handler) commandDirectMessage(ctx context.Context, principal auth.Principal, text string) (string, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", refuse(http.StatusBadRequest, "Use /msg @person followed by your message.")
	}
	person, err := h.commandUser(ctx, principal, fields[0])
	if err != nil {
		return "", refuse(http.StatusNotFound, "No one in this workspace matches "+fields[0]+".")
	}
	opened, err := h.Messages.OpenConversation(ctx, principal.WorkspaceID, principal.UserID, []domain.UserID{person.ID})
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	if body != "" {
		if _, err := h.Messages.Post(ctx, principal.WorkspaceID, principal.UserID, opened.Conversation.ID, body, "", ""); err != nil {
			return "", err
		}
	}
	return appURL(string(opened.Conversation.ID), "", "", "", ""), nil
}
