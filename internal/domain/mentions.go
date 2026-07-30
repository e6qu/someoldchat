package domain

import (
	"encoding/json"
	"sort"
	"strings"
)

const userGroupMentionPrefix = "<!subteam^"

// MessageMentions contains the recipients explicitly referenced by a message's
// top-level mrkdwn and Block Kit content.
type MessageMentions struct {
	Users      []UserID
	UserGroups []UserGroupID
}

// MentionsInMessage extracts only references that Slack treats as message
// content. Looking for "<@" in the serialized blocks object is not sufficient:
// action values, URLs and plain-text labels are opaque data and must never
// become notification recipients. Rich-text blocks carry user and user-group
// references as typed elements, while ordinary blocks carry them in mrkdwn
// text objects.
func MentionsInMessage(text, blocks string) MessageMentions {
	users := make(map[UserID]struct{})
	groups := make(map[UserGroupID]struct{})
	addText := func(value string) {
		for _, id := range MentionedUsers(value) {
			users[id] = struct{}{}
		}
		for _, id := range MentionedUserGroups(value) {
			groups[id] = struct{}{}
		}
	}
	addText(text)

	var value any
	if json.Unmarshal([]byte(blocks), &value) == nil {
		var visit func(any)
		visit = func(current any) {
			switch typed := current.(type) {
			case []any:
				for _, child := range typed {
					visit(child)
				}
			case map[string]any:
				switch typed["type"] {
				case "mrkdwn", "markdown":
					if text, ok := typed["text"].(string); ok {
						addText(text)
					}
				case "user":
					if id, ok := typed["user_id"].(string); ok && validUserMentionID(id) {
						users[UserID(id)] = struct{}{}
					}
				case "usergroup":
					if id, ok := typed["usergroup_id"].(string); ok && validUserGroupMentionID(id) {
						groups[UserGroupID(id)] = struct{}{}
					}
				}
				for _, child := range typed {
					visit(child)
				}
			}
		}
		visit(value)
	}

	result := MessageMentions{
		Users:      make([]UserID, 0, len(users)),
		UserGroups: make([]UserGroupID, 0, len(groups)),
	}
	for id := range users {
		result.Users = append(result.Users, id)
	}
	for id := range groups {
		result.UserGroups = append(result.UserGroups, id)
	}
	sort.Slice(result.Users, func(left, right int) bool { return result.Users[left] < result.Users[right] })
	sort.Slice(result.UserGroups, func(left, right int) bool { return result.UserGroups[left] < result.UserGroups[right] })
	return result
}

// MentionedUsers returns the distinct Slack user identifiers carried by
// mrkdwn. Slack accepts both U-prefixed workspace users and W-prefixed legacy
// workspace users; an expanded presentation label may follow after a pipe.
func MentionedUsers(values ...string) []UserID {
	found := make(map[UserID]struct{})
	for _, value := range values {
		for offset := 0; offset < len(value); {
			relative := strings.Index(value[offset:], "<@")
			if relative < 0 {
				break
			}
			start := offset + relative + 2
			relativeEnd := strings.IndexByte(value[start:], '>')
			if relativeEnd < 0 {
				break
			}
			end := start + relativeEnd
			raw := value[start:end]
			if separator := strings.IndexByte(raw, '|'); separator >= 0 {
				raw = raw[:separator]
			}
			if validUserMentionID(raw) {
				found[UserID(raw)] = struct{}{}
			}
			offset = end + 1
		}
	}
	result := make([]UserID, 0, len(found))
	for id := range found {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

// MentionedUserGroups returns the distinct Slack user-group identifiers carried
// by mrkdwn text. Slack transports a user-group mention as <!subteam^ID>; a
// display label may follow the identifier after a pipe in message payloads that
// have already been expanded for presentation.
//
// The scanner deliberately recognizes only Slack-shaped, ASCII identifiers.
// Treating arbitrary text after "!subteam^" as an identifier would let a
// malformed message accidentally notify a real group after truncation or
// normalization in another layer.
func MentionedUserGroups(values ...string) []UserGroupID {
	found := make(map[UserGroupID]struct{})
	for _, value := range values {
		for offset := 0; offset < len(value); {
			relative := strings.Index(value[offset:], userGroupMentionPrefix)
			if relative < 0 {
				break
			}
			start := offset + relative + len(userGroupMentionPrefix)
			relativeEnd := strings.IndexByte(value[start:], '>')
			if relativeEnd < 0 {
				break
			}
			end := start + relativeEnd
			raw := value[start:end]
			if separator := strings.IndexByte(raw, '|'); separator >= 0 {
				raw = raw[:separator]
			}
			if validUserGroupMentionID(raw) {
				found[UserGroupID(raw)] = struct{}{}
			}
			offset = end + 1
		}
	}
	result := make([]UserGroupID, 0, len(found))
	for id := range found {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func validUserGroupMentionID(value string) bool {
	if len(value) < 2 || value[0] != 'S' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func validUserMentionID(value string) bool {
	if len(value) < 2 || (value[0] != 'U' && value[0] != 'W') {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}
