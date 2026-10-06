package domain

import "strings"

// MaxChannelNameLength is the longest channel name Slack accepts.
const MaxChannelNameLength = 80

// NormalizeChannelName is the name a channel is stored under: lower case, with
// each run of whitespace turned into one hyphen. conversations.create,
// conversations.rename, admin.conversations.rename and
// admin.conversations.unlinkObjects's new_name all name a channel, so they share
// one rule rather than each deciding what a channel may be called.
func NormalizeChannelName(name string) (string, error) {
	name = strings.ToLower(strings.Join(strings.Fields(name), "-"))
	if name == "" || len(name) > MaxChannelNameLength {
		return "", ErrInvalidConversation
	}
	return name, nil
}
