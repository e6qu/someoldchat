package web

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The workspace shell: the frame every signed-in workspace page shares.
//
// Slack's client is one frame — a rail of destinations on the left (the
// workspace, Home, DMs, Activity, Later, More, Create and the member's own
// avatar), a top bar holding search and help, and the destination's own panes
// beside the rail. SameOldChat used to draw that frame only on the conversation
// page; every other destination was a separate document with a "← Back to chat"
// link that dropped the conversation the member came from. The frame is now a
// set of partials (shellPartials) that every workspace page renders around its
// own content, fed by one view (shellView) that one builder (newShell) fills.

// Rail destinations. They are the values of shellView.Destination and decide
// which rail item carries aria-current.
const (
	destinationHome     = "home"
	destinationDMs      = "dms"
	destinationActivity = "activity"
	destinationLater    = "later"
	destinationMore     = "more"
)

// shellView is everything the frame renders. It is deliberately independent of
// the page inside it: a page embeds it as .Shell and knows nothing else about
// the rail, the top bar or the shared dialogs.
type shellView struct {
	WorkspaceName    string
	WorkspaceInitial string
	Workspaces       []workspaceChoice
	// Channel is the conversation the member was reading. Every link back into
	// a conversation carries it, so leaving Home for Activity and returning does
	// not lose the member's place.
	Channel     string
	Destination string
	CSRFToken   string
	// Preferences is the member's own preferences as JSON, which the page
	// seeds this browser from so they follow the member to every client;
	// empty when they could not be read, so a failed read never overwrites
	// what this browser keeps.
	Preferences string
	UserID      string
	Username    string
	UserInitial string
	AvatarURL   string
	Away        bool
	// Status is the member's own status, shown in the avatar menu and edited by
	// the status dialog.
	StatusDisplay    template.HTML
	StatusEmoji      string
	StatusText       string
	StatusExpiration string
	// NotificationsPaused is the member's own pause (snooze). PausedUntil is
	// the machine-readable end, localised on the client.
	NotificationsPaused bool
	PausedUntil         string
	ShowAdmin           bool
	ShowAuthAdmin       bool
	// CanRequestInvite offers a member who cannot invite directly the request
	// Slack's Add coworkers sends to the workspace administrators. A guest
	// may do neither.
	CanRequestInvite bool
	// Timezone is the member's profile zone, which Language & region shows
	// and lets them set by hand.
	Timezone       string
	ShowIdentity   bool
	CanCreate      bool
	CanSchedule    bool
	CanSetStatus   bool
	CanMessage     bool
	ReminderUnread bool
	Keyboard       []keyboardSectionView
	Switcher       []switcherEntry
	// Directs is the member's DMs, newest activity first, for the DMs pane.
	Directs []conversationView
	// SearchQuery pre-fills the top bar's search field on the search page.
	SearchQuery string
	// ReturnTo is this page's own address, so a form in the frame (status,
	// away, pause) can bring the member back to where they were instead of to
	// the page that happens to own the mutation.
	ReturnTo string
}

// HomeURL is where the Home rail item goes: the conversation the member was
// reading, or the default one.
func (s shellView) HomeURL() string {
	if s.Channel == "" {
		return "/app"
	}
	return "/app?channel=" + url.QueryEscape(s.Channel)
}

// With appends the current conversation to a destination URL.
func (s shellView) With(path string) string {
	if s.Channel == "" {
		return path
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "channel=" + url.QueryEscape(s.Channel)
}

// switcherEntry is one option in the Ctrl/Cmd+K switcher. Kind drives the icon
// and the accessible type ("Channel", "Private channel", "Direct message",
// "Group DM", "App"); Context is the second line Slack shows under a result.
type switcherEntry struct {
	ID       string
	Name     string
	Href     string
	Kind     string
	KindText string
	Context  string
	Unread   bool
}

// conversationKind is the single word the shell uses for what a conversation
// is. The icon, the switcher's type column and the details dialog all read it,
// so a private channel cannot be drawn with a # in one place and a lock in
// another.
func conversationKind(conversation domain.Conversation) string {
	switch conversation.Kind {
	case domain.ConversationTypeIM:
		return "dm"
	case domain.ConversationTypeMPIM:
		return "group"
	case domain.ConversationTypePrivate:
		return "private"
	}
	return "channel"
}

func conversationKindText(kind string) string {
	switch kind {
	case "dm":
		return "Direct message"
	case "group":
		return "Group DM"
	case "private":
		return "Private channel"
	case "app":
		return "App"
	}
	return "Channel"
}

// shellRequest names the page a shell is drawn around.
type shellRequest struct {
	Destination string
	Channel     domain.ConversationID
	CSRFToken   string
	// Conversations is the sidebar the page already built, when it built one;
	// otherwise the shell reads it for the switcher.
	Conversations *sidebarView
	SearchQuery   string
}

// newShell fills the frame. Every read here is decoration around a page that
// has its own content, so a failed read degrades the frame (a missing status,
// an empty switcher) instead of failing the page — and each such failure is a
// visible absence, never a fabricated value.
func (h Handler) newShell(r *http.Request, principal auth.Principal, request shellRequest) shellView {
	ctx := r.Context()
	csrf := request.CSRFToken
	if csrf == "" {
		csrf, _ = pageCSRFToken(r)
	}
	channel := request.Channel
	if channel == "" {
		channel = h.requestChannelParameter(r)
	}
	view := shellView{
		Channel:       string(channel),
		Destination:   request.Destination,
		CSRFToken:     csrf,
		UserID:        string(principal.UserID),
		Keyboard:      keyboardHelp(),
		Workspaces:    h.workspaceChoices(r, principal),
		ShowIdentity:  h.canShowIdentity(),
		ShowAdmin:     h.canShowWorkspaceAdmin(ctx, principal),
		CanCreate:     principal.HasScope(auth.ScopeChannelsManage),
		CanMessage:    principal.HasScope(auth.ScopeChannelsManage),
		CanSchedule:   principal.HasScope(auth.ScopeChatWrite),
		CanSetStatus:  principal.HasScope(auth.ScopeUsersWrite),
		SearchQuery:   request.SearchQuery,
		WorkspaceName: "SameOldChat",
	}
	view.ShowAuthAdmin = h.Login != nil && view.ShowAdmin
	if preferences, err := h.Messages.MemberPreferences(ctx, principal.WorkspaceID, principal.UserID); err == nil {
		if encoded, err := json.Marshal(preferences); err == nil {
			view.Preferences = string(encoded)
		}
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/app") {
		view.ReturnTo = r.URL.RequestURI()
	}
	if workspace, err := h.Messages.WorkspaceInfo(ctx, principal.WorkspaceID, principal.UserID); err == nil && strings.TrimSpace(workspace.Name) != "" {
		view.WorkspaceName = strings.TrimSpace(workspace.Name)
	}
	view.WorkspaceInitial = initial(view.WorkspaceName)
	if user, err := h.Messages.UserInfo(ctx, principal.WorkspaceID, principal.UserID, principal.UserID); err == nil {
		view.Username = displayName(user)
		view.Timezone = user.Profile.Timezone
		view.CanRequestInvite = !view.ShowAuthAdmin && view.CanCreate && !user.Restricted && !user.UltraRestricted
		view.AvatarURL = profileImageURL(user.Profile)
		view.Away = user.Presence == domain.PresenceAway
		view.StatusEmoji = user.Profile.StatusEmoji
		view.StatusText = user.Profile.StatusText
		if !user.Profile.StatusExpiration.IsZero() {
			view.StatusExpiration = user.Profile.StatusExpiration.UTC().Format(time.RFC3339)
		}
		if strings.TrimSpace(user.Profile.StatusEmoji) != "" {
			emojiImages := map[string]string{}
			if custom, err := h.Messages.Emojis(ctx, principal.WorkspaceID, principal.UserID); err == nil {
				emojiImages = customEmojiImages(custom)
			}
			view.StatusDisplay = statusEmojiDisplay(user.Profile.StatusEmoji, emojiImages)
		}
	} else {
		view.Username = string(principal.UserID)
	}
	view.UserInitial = initial(view.Username)
	if dnd, err := h.Messages.DoNotDisturbInfo(ctx, principal.WorkspaceID, principal.UserID, principal.UserID); err == nil && dnd.SnoozeUntil.After(time.Now()) {
		view.NotificationsPaused = true
		view.PausedUntil = dnd.SnoozeUntil.UTC().Format(time.RFC3339)
	}
	if unread, err := h.hasUnacknowledgedReminder(ctx, principal); err == nil {
		view.ReminderUnread = unread
	}
	conversations := request.Conversations
	if conversations == nil {
		built := h.sidebar(ctx, principal, channel, false)
		conversations = &built
	}
	view.Switcher = conversations.switcherEntries()
	view.Directs = append([]conversationView(nil), conversations.Directs...)
	sort.SliceStable(view.Directs, func(left, right int) bool { return view.Directs[left].RecentAt.After(view.Directs[right].RecentAt) })
	if apps, err := h.Messages.ListWorkspaceApps(ctx, principal.WorkspaceID, principal.UserID); err == nil {
		for _, app := range apps {
			view.Switcher = append(view.Switcher, switcherEntry{
				ID: string(app.ID), Name: app.Name, Href: view.With("/app/apps/" + url.PathEscape(string(app.ID))),
				Kind: "app", KindText: "App", Context: strings.TrimSpace(app.Description),
			})
		}
	}
	return view
}

// requestChannelParameter is the conversation named in the query string, if
// any. Unlike requestChannel it does not fall back to the default channel: a
// page that was not opened from a conversation has none to return to, and
// inventing one would send Home somewhere the member never was.
func (h Handler) requestChannelParameter(r *http.Request) domain.ConversationID {
	value := strings.TrimSpace(r.URL.Query().Get("channel"))
	if value == "" || len(value) > 64 || strings.ContainsAny(value, "/?#&") {
		return ""
	}
	return domain.ConversationID(value)
}

// switcherEntries lists every conversation the switcher offers, members' first
// in sidebar order, then public channels the member could preview.
func (s sidebarView) switcherEntries() []switcherEntry {
	entries := make([]switcherEntry, 0, len(s.All)+len(s.Browsable))
	add := func(item conversationView) {
		context := item.Topic
		switch {
		case !item.IsMember:
			context = "Not a member"
			if item.MemberCount > 0 {
				context += " · " + pluralCount(item.MemberCount, "member", "members")
			}
		case item.Kind == "dm" && item.IsSelfDirect:
			context = "This is your space"
		case context == "" && item.Kind == "group":
			context = pluralCount(item.MemberCount, "member", "members")
		}
		entries = append(entries, switcherEntry{
			ID: item.ID, Name: item.Name, Href: "/app?channel=" + url.QueryEscape(item.ID),
			Kind: item.Kind, KindText: conversationKindText(item.Kind), Context: context, Unread: item.IsUnread,
		})
	}
	for _, item := range s.All {
		add(item)
	}
	for _, item := range s.Browsable {
		add(item)
	}
	return entries
}

// sidebarSectionOption is a section a conversation can be moved to.
type sidebarSectionOption struct {
	ID   string
	Name string
}

// sidebarSectionView is one custom section in the sidebar: its conversations
// in order, whether it is collapsed, and whether it can move up or down among
// the member's sections.
type sidebarSectionView struct {
	ID                string
	Name              string
	Collapsed         bool
	NotificationLevel string
	CanUp             bool
	CanDown           bool
	Channels          []conversationView
}

// sidebarView is the Home pane. Only conversations the member belongs to are
// in it, as in Slack; a public channel the member has not joined is reached
// through Browse channels or the switcher and opens in preview.
type sidebarView struct {
	Starred        []conversationView
	Channels       []conversationView
	Sections       []sidebarSectionView
	SectionOptions []sidebarSectionOption
	Directs        []conversationView
	// All is every member conversation in on-screen order; Browsable is the
	// public channels the member could join. The switcher reads both.
	All       []conversationView
	Browsable []conversationView
	// Truncated reports that the member belongs to more conversations than the
	// sidebar reads, so the pane can say so instead of pretending to be whole.
	Truncated     bool
	Notice        string
	CurrentUnread int
}

// sidebarPages bounds how many conversation pages one render reads. A member
// in more than sidebarPages×conversationWindow conversations sees a notice and
// reaches the rest through the switcher's search and Browse channels.
const sidebarPages = 6

func (h Handler) sidebar(ctx context.Context, principal auth.Principal, channel domain.ConversationID, atLatest bool) sidebarView {
	var view sidebarView
	var conversations []domain.Conversation
	cursor := domain.Cursor("")
	for page := 0; ; page++ {
		result, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{Limit: conversationWindow, Cursor: cursor})
		if err != nil {
			return sidebarView{Notice: "The conversation list is temporarily out of date."}
		}
		conversations = append(conversations, result.Conversations...)
		if !result.HasMore || result.NextCursor == "" {
			break
		}
		if page+1 >= sidebarPages {
			view.Truncated = true
			break
		}
		cursor = result.NextCursor
	}

	starred := map[domain.ConversationID]bool{}
	if stars, err := h.Messages.Stars(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 200}); err == nil {
		for _, star := range stars.Stars {
			if star.IsChannel() {
				starred[star.Conversation] = true
			}
		}
	}
	// Mention and keyword counts come from the member's unread Activity. They
	// are bounded by the conversation's own unread count, so reading a
	// conversation — which clears its unread count — clears its badge too.
	mentions := map[domain.ConversationID]int{}
	if activity, err := h.Messages.Activity(ctx, principal.WorkspaceID, principal.UserID, domain.ActivityQuery{
		Kinds: []domain.ActivityKind{domain.ActivityMention, domain.ActivityKeyword}, UnreadOnly: true, Page: domain.PageRequest{Limit: 200},
	}); err == nil {
		for _, item := range activity.Items {
			mentions[item.Conversation]++
		}
	}
	names := h.newUserNames(ctx, principal)

	byID := map[domain.ConversationID]conversationView{}
	var channelOrder, directOrder []domain.ConversationID
	resolved := 0
	for _, conversation := range conversations {
		item := conversationView{
			ID: string(conversation.ID), Name: conversationName(conversation), Current: conversation.ID == channel,
			Kind: conversationKind(conversation), IsMember: conversation.IsMember, IsPrivate: conversation.Kind == domain.ConversationTypePrivate,
			IsGroupDirect: conversation.Kind == domain.ConversationTypeMPIM, UnreadCount: conversation.UnreadCount,
			MemberCount: conversation.NumMembers, Topic: strings.TrimSpace(conversation.Topic), IsStarred: starred[conversation.ID],
			Archived: conversation.Archived, NotificationLevel: string(domain.NotificationInherit),
			CanLeave: conversation.IsDirectOrGroup() || (!conversation.IsGeneral && !conversation.Archived),
		}
		if item.Topic == "" {
			item.Topic = strings.TrimSpace(conversation.Purpose)
		}
		if conversation.ID == channel {
			view.CurrentUnread = conversation.UnreadCount
		}
		if !conversation.IsMember && !conversation.IsDirectOrGroup() {
			if !conversation.Archived && conversation.Kind.OrPublic() == domain.ConversationTypePublic {
				view.Browsable = append(view.Browsable, item)
			}
			continue
		}
		if item.Current && atLatest {
			// The page just rendered every message in this conversation;
			// marking it unread would tell the reader to read what they are
			// reading.
			item.UnreadCount = 0
		}
		item.IsUnread = item.UnreadCount > 0
		if item.IsUnread {
			item.MentionCount = min(mentions[conversation.ID], item.UnreadCount)
		}
		if conversation.IsDirectOrGroup() {
			item.IsMember = true
			switch {
			case conversation.Kind == domain.ConversationTypeIM && conversation.DirectUserID != "":
				entry := names.entry(conversation.DirectUserID)
				item.Name = entry.name
				item.Initial = initial(entry.name)
				item.IsSelfDirect = conversation.DirectUserID == principal.UserID
				if item.IsSelfDirect {
					item.Name += " (you)"
					item.Presence = "active"
				}
				if user, err := h.Messages.UserInfo(ctx, principal.WorkspaceID, principal.UserID, conversation.DirectUserID); err == nil {
					item.AvatarURL = profileImageURL(user.Profile)
					item.Presence = viewPresence(user, item.IsSelfDirect, time.Now().UTC())
					item.Topic = strings.TrimSpace(user.Profile.StatusText)
				}
			case resolved < directNameWindow && (conversation.Kind != domain.ConversationTypeMPIM || conversation.Name == "" || conversation.Name == "direct"):
				resolved++
				if participants := h.participantNames(ctx, principal, conversation.ID); participants != "" {
					item.Name = participants
				}
				item.Initial = initial(item.Name)
			default:
				item.Initial = initial(item.Name)
			}
			// A DM's badge counts every unread message: all of them are for
			// the member.
			item.MentionCount = item.UnreadCount
			directOrder = append(directOrder, conversation.ID)
		} else {
			channelOrder = append(channelOrder, conversation.ID)
		}
		byID[conversation.ID] = item
	}

	// The newest message's time orders the DM pane and a DM section sorted by
	// recent activity. It costs one read per conversation, so it is read for at
	// most directNameWindow DMs, the bound the DM names above already use; a
	// channel's recency would need a batched read the service does not offer
	// (specs/product-gap-audit.md), so channel sections sort alphabetically or
	// by priority only.
	for index, id := range directOrder {
		if index >= directNameWindow {
			break
		}
		if history, err := h.Messages.History(ctx, principal.WorkspaceID, principal.UserID, id, domain.HistoryRequest{Page: domain.PageRequest{Limit: 1, Descending: true}}); err == nil && len(history.Messages) == 1 {
			item := byID[id]
			item.RecentAt = history.Messages[0].CreatedAt
			byID[id] = item
		}
	}
	// Muted state is the member's own per-conversation level; a section's
	// level applies to the conversations in it below.
	for id, item := range byID {
		if preferences, err := h.Messages.ConversationNotificationPreferences(ctx, principal.WorkspaceID, principal.UserID, id); err == nil {
			item.NotificationLevel = string(preferences.Level)
			item.FollowEveryThread = preferences.FollowEveryThread
			item.Muted = preferences.Level == domain.NotificationMute
			byID[id] = item
		}
	}

	placed := map[domain.ConversationID]bool{}
	take := func(id domain.ConversationID, section string) (conversationView, bool) {
		item, ok := byID[id]
		if !ok || placed[id] {
			return conversationView{}, false
		}
		placed[id] = true
		item.SectionID = section
		return item, true
	}
	// Starred comes first, as in Slack: starring a conversation moves it there.
	for _, id := range append(append([]domain.ConversationID(nil), channelOrder...), directOrder...) {
		if starred[id] {
			if item, ok := take(id, "starred"); ok {
				view.Starred = append(view.Starred, item)
			}
		}
	}
	if sections, err := h.Messages.SidebarSections(ctx, principal.WorkspaceID, principal.UserID); err == nil {
		for index, section := range sections {
			sectionView := sidebarSectionView{
				ID: string(section.ID), Name: section.Name, Collapsed: section.Collapsed,
				NotificationLevel: string(section.NotificationLevel),
				CanUp:             index > 0, CanDown: index < len(sections)-1,
			}
			for _, id := range section.Conversations {
				if item, ok := take(id, string(section.ID)); ok {
					if section.NotificationLevel == domain.NotificationMute && item.NotificationLevel == string(domain.NotificationInherit) {
						item.Muted = true
					}
					sectionView.Channels = append(sectionView.Channels, item)
				}
			}
			sortConversations(sectionView.Channels)
			view.Sections = append(view.Sections, sectionView)
			view.SectionOptions = append(view.SectionOptions, sidebarSectionOption{ID: string(section.ID), Name: section.Name})
		}
	}
	for _, id := range channelOrder {
		if item, ok := take(id, "channels"); ok {
			view.Channels = append(view.Channels, item)
		}
	}
	for _, id := range directOrder {
		if item, ok := take(id, "directs"); ok {
			view.Directs = append(view.Directs, item)
		}
	}
	sortConversations(view.Starred)
	sortConversations(view.Channels)
	sortConversations(view.Directs)
	sortConversations(view.Browsable)

	// All is the on-screen order, which previous/next conversation follows.
	view.All = append(view.All, view.Starred...)
	for _, section := range view.Sections {
		view.All = append(view.All, section.Channels...)
	}
	view.All = append(view.All, view.Channels...)
	view.All = append(view.All, view.Directs...)
	return view
}

// sortConversations is the sidebar's default order: alphabetical, the way
// Slack sorts a section until the member picks another order. The member's
// choice of order is applied on top of this by the client.
func sortConversations(items []conversationView) {
	sort.SliceStable(items, func(left, right int) bool {
		return strings.ToLower(items[left].Name) < strings.ToLower(items[right].Name)
	})
}

// sidebarStateAttributes is what the client needs to re-sort a row: its name,
// and its priority (mentions, then unread, then everything else).
func (c conversationView) Priority() int {
	switch {
	case c.MentionCount > 0:
		return 2
	case c.IsUnread:
		return 1
	}
	return 0
}
