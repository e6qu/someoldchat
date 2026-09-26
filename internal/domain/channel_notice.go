package domain

import "strings"

// ChannelNotice is what a workspace-generated channel message reports beyond
// its text. Slack puts these on the message object itself — a channel_topic
// message carries `topic`, a channel_purpose message `purpose`, a channel_name
// message `name` and `old_name` — and clients and apps read them there rather
// than parsing the sentence.
type ChannelNotice struct {
	Name    string
	OldName string
	Topic   string
	Purpose string
}

// The sentence each notice is written as. ChannelNoticeText writes it and
// ChannelNoticeFields reads it back, so the stored text is the one record of the
// fields and the two cannot drift: a second stored copy would be one more thing
// for the two storage profiles to disagree about.
const (
	noticeJoined         = " has joined the channel"
	noticeLeft           = " has left the channel"
	noticeTopicSet       = " set the channel topic: "
	noticeTopicCleared   = " cleared the channel topic"
	noticePurposeSet     = " set the channel purpose: "
	noticePurposeCleared = " cleared the channel purpose"
	noticeRenamedFrom    = " renamed the channel from \""
	noticeRenamedBetween = "\" to \""
	// noticeRenamedTo is the form a rename was written in before the old name
	// was recorded. It is still read, because those messages are still stored.
	noticeRenamedTo = " renamed the channel to "
)

// ChannelNoticeText is the text of the notice subtype posts for actor. It
// reports false for a subtype that is not a channel notice.
func ChannelNoticeText(subtype MessageSubtype, actor UserID, notice ChannelNotice) (string, bool) {
	mention := "<@" + string(actor) + ">"
	switch subtype {
	case MessageSubtypeChannelJoin:
		return mention + noticeJoined, true
	case MessageSubtypeChannelLeave:
		return mention + noticeLeft, true
	case MessageSubtypeChannelTopic:
		if strings.TrimSpace(notice.Topic) == "" {
			return mention + noticeTopicCleared, true
		}
		return mention + noticeTopicSet + notice.Topic, true
	case MessageSubtypeChannelPurpose:
		if strings.TrimSpace(notice.Purpose) == "" {
			return mention + noticePurposeCleared, true
		}
		return mention + noticePurposeSet + notice.Purpose, true
	case MessageSubtypeChannelName:
		return mention + noticeRenamedFrom + notice.OldName + noticeRenamedBetween + notice.Name + "\"", true
	}
	return "", false
}

// ChannelNoticeFields is the subtype-specific fields of a channel notice, keyed
// by their Slack message-object names. It is empty for any other message, and
// for a notice whose text is not one ChannelNoticeText wrote.
func ChannelNoticeFields(message Message) map[string]string {
	rest, ok := strings.CutPrefix(message.Text, "<@"+string(message.AuthorID)+">")
	if !ok {
		return nil
	}
	switch message.Subtype {
	case MessageSubtypeChannelTopic:
		if rest == noticeTopicCleared {
			return map[string]string{"topic": ""}
		}
		if topic, ok := strings.CutPrefix(rest, noticeTopicSet); ok {
			return map[string]string{"topic": topic}
		}
	case MessageSubtypeChannelPurpose:
		if rest == noticePurposeCleared {
			return map[string]string{"purpose": ""}
		}
		if purpose, ok := strings.CutPrefix(rest, noticePurposeSet); ok {
			return map[string]string{"purpose": purpose}
		}
	case MessageSubtypeChannelName:
		if names, ok := strings.CutPrefix(rest, noticeRenamedFrom); ok {
			// A channel name cannot contain a double quote, so for a channel
			// the first separator is the only one. A group DM's label can;
			// such a label is split at its first separator.
			if oldName, name, ok := strings.Cut(strings.TrimSuffix(names, "\""), noticeRenamedBetween); ok {
				return map[string]string{"old_name": oldName, "name": name}
			}
		}
		if name, ok := strings.CutPrefix(rest, noticeRenamedTo); ok {
			return map[string]string{"name": name}
		}
	}
	return nil
}
