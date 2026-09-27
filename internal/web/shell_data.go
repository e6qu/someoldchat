package web

import (
	"context"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// homeSectionView is one section of the Home pane as it is drawn. Built-in and
// custom sections share it, so every section has the same header, menu,
// keyboard behaviour and drop target, and differs only in what it declares.
type homeSectionView struct {
	// Key names the section in the member's per-browser sort and filter
	// choices: "starred", "channels", "directs", "apps" or the custom
	// section's identifier.
	Key               string
	Label             string
	ID                string
	Custom            bool
	Collapsed         bool
	CanUp             bool
	CanDown           bool
	NotificationLevel string
	Rows              []conversationView
	Empty             string
	// Draggable rows can be dropped on a section whose DropTarget is set: a
	// custom section's identifier, or "channels" for the default group.
	Draggable    bool
	DropTarget   string
	AddChannels  bool
	AddCoworkers bool
	Apps         bool
	// HasRecency offers "By most recent activity": only DMs carry the time of
	// their newest message (see sidebar).
	HasRecency bool
}

// homeSections lays the Home pane out in Slack's order: Starred, the member's
// own sections, Channels, Direct messages, then Apps.
func (s sidebarView) homeSections(canInvite, hasApps bool) []homeSectionView {
	sections := make([]homeSectionView, 0, len(s.Sections)+4)
	if len(s.Starred) > 0 {
		sections = append(sections, homeSectionView{Key: "starred", Label: "Starred", Rows: s.Starred})
	}
	for _, section := range s.Sections {
		sections = append(sections, homeSectionView{
			Key: "section-" + section.ID, Label: section.Name, ID: section.ID, Custom: true,
			Collapsed: section.Collapsed, CanUp: section.CanUp, CanDown: section.CanDown,
			NotificationLevel: section.NotificationLevel, Rows: section.Channels,
			Empty: "Drag channels here, or use a channel’s Move to menu.", Draggable: true, DropTarget: section.ID,
		})
	}
	sections = append(sections,
		homeSectionView{Key: "channels", Label: "Channels", Rows: s.Channels, Empty: "You are not in any channels yet.", Draggable: len(s.Sections) > 0, DropTarget: "channels", AddChannels: true},
		homeSectionView{Key: "directs", Label: "Direct messages", Rows: s.Directs, Empty: "No direct messages yet.", AddCoworkers: canInvite, HasRecency: true},
	)
	if hasApps {
		sections = append(sections, homeSectionView{Key: "apps", Label: "Apps", Apps: true})
	}
	return sections
}

// SortName is what the client sorts a row by alphabetically.
func (c conversationView) SortName() string { return strings.ToLower(c.Name) }

// RecentUnix is the newest message's time, which "By most recent activity"
// sorts by; zero when the conversation has no messages.
func (c conversationView) RecentUnix() int64 {
	if c.RecentAt.IsZero() {
		return 0
	}
	return c.RecentAt.Unix()
}

func (c conversationView) RecentISO() string {
	if c.RecentAt.IsZero() {
		return ""
	}
	return c.RecentAt.UTC().Format(time.RFC3339)
}

// IsChannelKind reports a channel, public or private, as opposed to a DM.
func (c conversationView) IsChannelKind() bool {
	return c.Kind == "channel" || c.Kind == "private" || c.Kind == ""
}

// KindNoun is the word Slack's menus use: "Star channel", "Mute conversation".
func (c conversationView) KindNoun() string {
	if c.IsChannelKind() {
		return "channel"
	}
	return "conversation"
}

// OthersCount is how many people besides the member are in a group DM, which
// Slack draws in place of an avatar.
func (c conversationView) OthersCount() int {
	if c.MemberCount > 1 {
		return c.MemberCount - 1
	}
	return c.MemberCount
}

// AccessibleName is the row's name as a screen reader announces it: its name,
// then every state the row shows only visually — privacy, unread, mentions,
// draft, muted.
func (c conversationView) AccessibleName() string {
	parts := []string{c.Name}
	if c.IsPrivate {
		parts = append(parts, "private channel")
	}
	if c.IsGroupDirect {
		parts = append(parts, "group direct message")
	}
	if c.UnreadCount > 0 {
		parts = append(parts, pluralCount(c.UnreadCount, "unread message", "unread messages"))
	}
	if c.MentionCount > 0 && c.IsChannelKind() {
		parts = append(parts, pluralCount(c.MentionCount, "mention", "mentions"))
	}
	if c.HasDraft {
		parts = append(parts, "has a draft")
	}
	if c.Muted {
		parts = append(parts, "muted")
	}
	return strings.Join(parts, ", ")
}

// faceInitials is up to three members' initials for the header's face pile. A
// DM's header is the person, so it has none; a failed read shows no faces
// rather than failing the page for a decoration.
func (h Handler) faceInitials(ctx context.Context, principal auth.Principal, conversation domain.Conversation) []string {
	if conversation.Kind == domain.ConversationTypeIM {
		return nil
	}
	page, err := h.Messages.ConversationMembers(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: 3})
	if err != nil {
		return nil
	}
	initials := make([]string, 0, len(page.Users))
	for _, user := range page.Users {
		if len(initials) == 3 {
			break
		}
		initials = append(initials, initial(displayName(user)))
	}
	return initials
}

// kindTextFor is the header's spoken type for a conversation kind.
func kindNounFor(kind string) string {
	return conversationView{Kind: kind}.KindNoun()
}

// markDrafts flags every row that has an unsent draft, wherever it is drawn.
func (s *sidebarView) markDrafts(withDraft map[domain.ConversationID]bool) {
	mark := func(rows []conversationView) {
		for index := range rows {
			rows[index].HasDraft = withDraft[domain.ConversationID(rows[index].ID)]
		}
	}
	mark(s.Starred)
	mark(s.Channels)
	mark(s.Directs)
	mark(s.All)
	for index := range s.Sections {
		mark(s.Sections[index].Channels)
	}
}
