package domain

import (
	"strings"
	"unicode/utf8"
)

// A code channel's commands are the slash commands its agents register with
// agents.conversations.setCommands. Each agent replaces its own set; the
// channel holds at most CodeChannelCommandLimit across every agent. A member
// typing /name in the channel invokes the agent that registered it.

// CodeChannelCommandLimit is the most commands a code channel holds across all
// of its agents.
const CodeChannelCommandLimit = 10

// CodeChannelCommand is one agent command of a code channel. Name has no
// leading slash.
type CodeChannelCommand struct {
	Name         string
	Description  string
	ArgumentHint string
	ShouldEscape bool
	// AppID and BotUserID are the agent that registered the command.
	AppID     AppID
	BotUserID UserID
}

// CodeChannelCanvasCommentLimit is the most comments getCanvas answers; more
// are reported by has_more_comments.
const CodeChannelCanvasCommentLimit = 100

// Slash is the command as a member types it.
func (c CodeChannelCommand) Slash() string {
	return "/" + c.Name
}

// slackBuiltinSlashCommands are the commands Slack reserves for itself in every
// conversation; an agent command may not take one of their names, whether or
// not this deployment implements it.
var slackBuiltinSlashCommands = map[string]struct{}{
	"active": {}, "apps": {}, "archive": {}, "away": {}, "call": {}, "collapse": {}, "dm": {}, "dnd": {},
	"expand": {}, "feed": {}, "feedback": {}, "giphy": {}, "invite": {}, "invite_people": {}, "join": {},
	"kick": {}, "leave": {}, "me": {}, "mentions": {}, "msg": {}, "mute": {}, "open": {}, "people": {},
	"prefs": {}, "remind": {}, "remove": {}, "rename": {}, "search": {}, "shortcuts": {}, "shrug": {},
	"star": {}, "status": {}, "topic": {}, "who": {},
}

// IsSlackBuiltinSlashCommand reports a command name, with or without its
// slash, that Slack reserves.
func IsSlackBuiltinSlashCommand(name string) bool {
	_, reserved := slackBuiltinSlashCommands[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))]
	return reserved
}

// NormalizeCodeChannelCommands checks one agent's setCommands set: each name
// 1-31 characters with no slash or whitespace, lowercase, unique within the
// set, and none of Slack's own commands. Each command is stamped with the
// agent that sets it.
func NormalizeCodeChannelCommands(commands []CodeChannelCommand, app AppID, bot UserID) ([]CodeChannelCommand, error) {
	if len(commands) > CodeChannelCommandLimit {
		return nil, ErrInvalidCodeChannel
	}
	normalized := make([]CodeChannelCommand, 0, len(commands))
	seen := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		name := strings.ToLower(strings.TrimSpace(command.Name))
		if name == "" || utf8.RuneCountInString(name) > 31 || strings.ContainsAny(name, "/ \t\r\n") || IsSlackBuiltinSlashCommand(name) {
			return nil, ErrInvalidCodeChannel
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, ErrInvalidCodeChannel
		}
		seen[name] = struct{}{}
		description, hint := strings.TrimSpace(command.Description), strings.TrimSpace(command.ArgumentHint)
		if utf8.RuneCountInString(description) > 255 || utf8.RuneCountInString(hint) > 255 {
			return nil, ErrInvalidCodeChannel
		}
		normalized = append(normalized, CodeChannelCommand{
			Name: name, Description: description, ArgumentHint: hint, ShouldEscape: command.ShouldEscape, AppID: app, BotUserID: bot,
		})
	}
	return normalized, nil
}

// WithAgentCommands is the channel's commands with the agent's own replaced
// by commands. Another agent's command of the same name, or more than
// CodeChannelCommandLimit in all, is refused.
func (c CodeChannel) WithAgentCommands(bot UserID, commands []CodeChannelCommand) ([]CodeChannelCommand, error) {
	others := make([]CodeChannelCommand, 0, len(c.Commands))
	for _, existing := range c.Commands {
		if existing.BotUserID != bot {
			others = append(others, existing)
		}
	}
	if len(others)+len(commands) > CodeChannelCommandLimit {
		return nil, ErrInvalidCodeChannel
	}
	for _, command := range commands {
		for _, other := range others {
			if other.Name == command.Name {
				return nil, ErrInvalidCodeChannel
			}
		}
	}
	return append(others, commands...), nil
}

// Command finds the channel's command a member typed, with or without its
// slash.
func (c CodeChannel) Command(typed string) (CodeChannelCommand, bool) {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(typed), "/"))
	for _, command := range c.Commands {
		if command.Name == name {
			return command, true
		}
	}
	return CodeChannelCommand{}, false
}
