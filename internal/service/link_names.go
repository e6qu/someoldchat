package service

import (
	"context"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// nameDirectory is what a plain-text @name or #name can resolve to for one
// member: the workspace's people and, when asked for, its user groups, and
// the channels that member can see. It is loaded only for text that names
// something, and only for the length of one request.
type nameDirectory struct {
	usernames    map[string]domain.UserID
	displayNames map[string]domain.UserID
	groups       map[string]domain.UserGroupID
	channels     map[string]domain.Conversation
}

// loadNameDirectory reads the directory for userID. A deleted member, a
// disabled user group and a direct conversation are never named.
func (m Messages) loadNameDirectory(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, includeGroups bool) (nameDirectory, error) {
	directory := nameDirectory{
		usernames: make(map[string]domain.UserID), displayNames: make(map[string]domain.UserID),
		groups: make(map[string]domain.UserGroupID), channels: make(map[string]domain.Conversation),
	}
	key := func(name string) (string, bool) {
		name = strings.ToLower(strings.TrimSpace(name))
		return name, name != "" && !strings.ContainsAny(name, " \t\r\n")
	}
	var cursor domain.Cursor
	for {
		page, err := m.Store.ListUsers(ctx, workspaceID, domain.PageRequest{Limit: 200, Cursor: cursor})
		if err != nil {
			return nameDirectory{}, err
		}
		for _, user := range page.Users {
			if user.Deleted {
				continue
			}
			if name, ok := key(user.Name); ok {
				directory.usernames[name] = user.ID
			}
			if name, ok := key(user.Profile.DisplayName); ok {
				directory.displayNames[name] = user.ID
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	if includeGroups {
		cursor = ""
		for {
			page, err := m.Store.ListUserGroups(ctx, workspaceID, false, domain.PageRequest{Limit: 200, Cursor: cursor})
			if err != nil {
				return nameDirectory{}, err
			}
			for _, group := range page.Groups {
				if handle, ok := key(group.Handle); ok && group.Enabled {
					directory.groups[handle] = group.ID
				}
			}
			if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
				break
			}
			cursor = page.NextCursor
		}
	}
	cursor = ""
	for {
		page, err := m.Store.ListConversations(ctx, workspaceID, userID, domain.ConversationListRequest{Limit: 200, Cursor: cursor})
		if err != nil {
			return nameDirectory{}, err
		}
		for _, conversation := range page.Conversations {
			if name, ok := key(conversation.Name); ok && !conversation.IsDirectOrGroup() {
				directory.channels[name] = conversation
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	return directory, nil
}

// member resolves an @name to a person: a username before a display name,
// since a username is unique and a display name need not be.
func (d nameDirectory) member(name string) (domain.UserID, bool) {
	name = strings.ToLower(name)
	if id, ok := d.usernames[name]; ok {
		return id, true
	}
	id, ok := d.displayNames[name]
	return id, ok
}

func (d nameDirectory) channel(name string) (domain.Conversation, bool) {
	conversation, ok := d.channels[strings.ToLower(name)]
	return conversation, ok
}

func (d nameDirectory) group(name string) (domain.UserGroupID, bool) {
	id, ok := d.groups[strings.ToLower(name)]
	return id, ok
}

// linkMessageNames is chat.postMessage's link_names (and chat.update's,
// chat.postEphemeral's and chat.scheduleMessage's): the pinned reference
// describes it as "Find and link channel names and usernames", and the
// current reference adds user groups. A plain-text @name becomes <@U…> when
// it names a member and <!subteam^S…> when it is a user group's handle, and
// #name becomes <#C…> for a channel the author can see. A name that resolves
// to nothing stays the text the author wrote.
func (m Messages) linkMessageNames(ctx context.Context, workspaceID domain.WorkspaceID, authorID domain.UserID, text string) (string, error) {
	if !strings.ContainsAny(text, "@#") {
		return text, nil
	}
	directory, err := m.loadNameDirectory(ctx, workspaceID, authorID, true)
	if err != nil {
		return "", err
	}
	return domain.LinkNameReferences(text, func(kind domain.NameReferenceKind, name string) (string, bool) {
		switch kind {
		case domain.MemberNameReference:
			if id, ok := directory.member(name); ok {
				return "<@" + string(id) + ">", true
			}
			if id, ok := directory.group(name); ok {
				return "<!subteam^" + string(id) + ">", true
			}
		case domain.ChannelNameReference:
			if conversation, ok := directory.channel(name); ok {
				return "<#" + string(conversation.ID) + ">", true
			}
		}
		return "", false
	}), nil
}
