package web

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The message composer, as Slack draws it: a formatting bar above the text,
// the text itself, staged attachments as cards, and a bottom row of insert
// actions ending in Send and the schedule chevron.
//
// A conversation page renders up to two of them. The conversation composer
// sits under the timeline; when a thread is open, the thread pane carries its
// own "Reply…" composer. They are independent in every respect Slack keeps
// independent: each has its own draft coordinate (conversation, or
// conversation plus thread), its own staged files and its own error. They
// used to be one form that turned into the thread's reply box whenever a
// thread was open, so a member reading a thread could not post to the channel
// and a channel draft was replaced on screen by the thread's.
//
// Everything a composer suggests — people, user groups, channels, special
// mentions and slash commands — is rendered once per page as
// composerDirectory, because both composers draw on the same directory and
// rendering it twice doubled the page for nothing.

// composerView is one composer. IDPrefix keeps every element id unique when
// two composers share a page: "" for the conversation composer, whose ids the
// rest of the page and the browser suite already name (#composer, #text), and
// "thread-" for the thread pane's.
type composerView struct {
	IDPrefix string
	// ReturnTo is set for a composer that is not on its conversation's page,
	// a Threads card's: the send comes back to that page rather than
	// appending to a timeline that is not there.
	ReturnTo string
	// DirectoryID names the suggestion directory this composer draws on, for
	// a page holding composers from more than one conversation; empty means
	// the page's one composer-directory.
	DirectoryID string
	// AccessibleLabel names the field when Label, the placeholder, is not
	// enough on its own: "Reply…" says nothing about which thread on a page of
	// threads.
	AccessibleLabel string
	// Quiet composers never take focus when the page loads: a page of them
	// would otherwise put the caret in whichever happens to be first.
	Quiet bool
	// NoEmojiPicker leaves out the emoji button on a page that has no picker
	// for it to open.
	NoEmojiPicker   bool
	Thread          bool
	ThreadTimestamp string
	// Label is the accessible name and the placeholder: "Message #general",
	// or Slack's "Reply…" in a thread.
	Label string
	// BroadcastLabel is the thread composer's "Also send to #general" (or "Also
	// send as direct message" in a DM). Empty on the conversation composer.
	BroadcastLabel   string
	Broadcast        bool
	CSRFToken        string
	Channel          string
	ChannelLabel     string
	ComposeURL       string
	DraftURL         string
	ScheduleURL      string
	StageUploadURL   string
	ScheduledURL     string
	HXTarget         string
	Newest           string
	ViewThread       string
	Draft            string
	DraftAttachments []draftAttachmentView
	DraftJSON        string
	ScheduleAt       string
	Error            string
	Autofocus        bool
	CanUpload        bool
	CanSchedule      bool
	HasShortcuts     bool
	// MemberCount drives Slack's confirmation before @channel or @here reaches
	// a large audience; CanInvite offers "Add them" after a mention of someone
	// who is not in the channel. Both are zero/false in a DM, where neither
	// applies.
	MemberCount int
	// BroadcastWarningOff is the workspace's choice to send those mentions
	// without the confirmation, which an administrator may make.
	BroadcastWarningOff bool
	CanInvite           bool
	InviteURL           string
	IsDirect            bool
	// RecipientName and RecipientZone are, in a one-to-one DM, the other
	// person and the IANA zone their client reported, so scheduling can say
	// what the chosen time is for them, as Slack does. Empty when they never
	// reported one.
	RecipientName string
	RecipientZone string
	// RecentFiles are the member's own recent files, which the + menu
	// offers to share here; ShareFileURL is where choosing one posts.
	RecentFiles  []recentFileView
	ShareFileURL string
	// Typing is the conversation composer's "is typing" line, rendered empty
	// so the live region exists before the stream first fills it.
	Typing typingView
}

// composerPerson is one mention candidate. Member is false for a workspace
// member who is not in this conversation: Slack still suggests them, labelled
// "Not in channel", and offers to add them after the message is sent.
type composerPerson struct {
	ID          string
	Name        string
	RealName    string
	DisplayName string
	AvatarURL   string
	Initial     string
	Member      bool
	Bot         bool
	Self        bool
}

type composerChannelOption struct {
	ID      string
	Name    string
	Private bool
}

// composerSpecial is one of Slack's broadcast mentions. Name is the mention
// without its @ and Entity is what the composer stores.
type composerSpecial struct {
	Name        string
	Entity      string
	Description string
}

type composerDirectory struct {
	// ID is the template's id: empty for a page's one directory, and one per
	// conversation on a page of composers from several (composerView's
	// DirectoryID names it).
	ID        string
	People    []composerPerson
	Groups    []userGroupView
	Channels  []composerChannelOption
	Specials  []composerSpecial
	Commands  []domain.AppShortcut
	Shortcuts []domain.AppShortcut
	// NonMembers is true when People carries workspace members outside the
	// conversation, so the client can label them.
	NonMembers bool
	// SearchPeopleURL is set when People is not the whole directory: the
	// client then asks it for the people a typed mention matches, as Slack
	// searches the directory as the member types.
	SearchPeopleURL string
}

// composerPeopleLimit bounds the workspace directory rendered into the page,
// so a large workspace's page does not carry every member. Past it, the
// client searches the directory as the member types (/app/mentions), as
// Slack does.
const composerPeopleLimit = 500

// composerSpecialMentions are Slack's broadcast mentions with the
// descriptions Slack's autocomplete shows. @everyone reaches the whole
// workspace and Slack offers it only in the workspace's general channel.
func composerSpecialMentions(conversation domain.Conversation) []composerSpecial {
	if conversation.IsDirectOrGroup() {
		return nil
	}
	specials := []composerSpecial{
		{Name: "here", Entity: "<!here>", Description: "Notify everyone online in this channel"},
		{Name: "channel", Entity: "<!channel>", Description: "Notify everyone in this channel"},
	}
	if conversation.IsGeneral {
		specials = append(specials, composerSpecial{Name: "everyone", Entity: "<!everyone>", Description: "Notify everyone in your workspace"})
	}
	return specials
}

// composerPeople reads the conversation's members and then the workspace
// directory, marking who is in the conversation. A failure to read the
// workspace directory degrades to members only; a failure to read the
// members degrades to nothing, and both are reported as notices rather than
// taking the page down for a suggestion list.
func (h Handler) composerPeople(ctx context.Context, principal auth.Principal, conversation domain.Conversation) ([]composerPerson, bool, bool, []string) {
	var notices []string
	members := map[domain.UserID]bool{}
	people := make([]composerPerson, 0)
	seen := map[domain.UserID]bool{}
	add := func(user domain.User, member bool) {
		if user.Deleted || seen[user.ID] {
			return
		}
		seen[user.ID] = true
		name := displayName(user)
		people = append(people, composerPerson{
			ID: string(user.ID), Name: name, RealName: strings.TrimSpace(user.RealName),
			DisplayName: strings.TrimSpace(user.Profile.DisplayName), AvatarURL: profileImageURL(user.Profile),
			Initial: initial(name), Member: member, Bot: user.IsBot(), Self: user.ID == principal.UserID,
		})
	}
	memberPage, err := h.Messages.ConversationMembers(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: memberWindow})
	if err != nil {
		return nil, false, false, []string{"Mention suggestions are temporarily unavailable."}
	}
	for _, user := range memberPage.Users {
		members[user.ID] = true
		add(user, true)
	}
	truncated := memberPage.HasMore
	nonMembers := false
	cursor := domain.Cursor("")
	seenCursors := map[domain.Cursor]bool{}
	for len(people) < composerPeopleLimit && !seenCursors[cursor] {
		seenCursors[cursor] = true
		page, pageErr := h.Messages.Users(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 200, Cursor: cursor})
		if pageErr != nil {
			notices = append(notices, "Only conversation members can be suggested for mentions right now.")
			break
		}
		for _, user := range page.Users {
			if len(people) >= composerPeopleLimit {
				truncated = true
				break
			}
			// Slackbot cannot be added to a channel, so it is never offered
			// as someone to mention into one.
			if user.IsSlackbot() && !members[user.ID] {
				continue
			}
			if !members[user.ID] && !user.Deleted && !seen[user.ID] {
				nonMembers = true
			}
			add(user, members[user.ID])
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sort.SliceStable(people, func(left, right int) bool {
		if people[left].Member != people[right].Member {
			return people[left].Member
		}
		return strings.ToLower(people[left].Name) < strings.ToLower(people[right].Name)
	})
	return people, nonMembers && !conversation.IsDirectOrGroup(), truncated, notices
}

// composerChannels lists the channels the member can see, marking private
// ones so the suggestion can show a lock rather than a hash.
func (h Handler) composerChannels(ctx context.Context, principal auth.Principal) ([]composerChannelOption, error) {
	page, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{
		Limit: searchFilterOptionLimit, ExcludeArchived: true,
		Types: []domain.ConversationType{domain.ConversationTypePublic, domain.ConversationTypePrivate},
	})
	if err != nil {
		return nil, err
	}
	options := make([]composerChannelOption, 0, len(page.Conversations))
	for _, conversation := range page.Conversations {
		options = append(options, composerChannelOption{ID: string(conversation.ID), Name: conversationName(conversation), Private: conversation.PrivateFlag()})
	}
	sort.Slice(options, func(left, right int) bool { return options[left].Name < options[right].Name })
	return options, nil
}

// composerUserGroups lists the enabled user groups a member can mention.
func (h Handler) composerUserGroups(ctx context.Context, principal auth.Principal) ([]userGroupView, string) {
	var groups []userGroupView
	cursor := domain.Cursor("")
	seen := map[domain.Cursor]struct{}{}
	for {
		if _, repeated := seen[cursor]; repeated {
			return nil, "User group suggestions stopped at an invalid page boundary."
		}
		seen[cursor] = struct{}{}
		page, err := h.Messages.ListUserGroups(ctx, principal.WorkspaceID, principal.UserID, false, domain.PageRequest{Limit: memberWindow, Cursor: cursor})
		if err != nil {
			return nil, "User group suggestions are temporarily unavailable."
		}
		for _, group := range page.Groups {
			if !group.Enabled || !group.DeletedAt.IsZero() {
				continue
			}
			groups = append(groups, userGroupView{
				ID: string(group.ID), Name: group.Name, Handle: group.Handle,
				Description: group.Description, MemberCount: len(group.Users),
			})
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sort.Slice(groups, func(left, right int) bool {
		return strings.ToLower(groups[left].Handle) < strings.ToLower(groups[right].Handle)
	})
	return groups, ""
}

// composerCommands is the slash-command directory: Slack's built-ins first,
// then a code channel's agent commands, then every installed app command that
// does not collide with either, with the installed apps' global shortcuts for
// the shortcut browser. An agent command answers ahead of an app command of
// the same name in its channel, as DispatchSlashCommand delivers it.
func (h Handler) composerCommands(ctx context.Context, principal auth.Principal, conversation domain.Conversation) ([]domain.AppShortcut, []domain.AppShortcut, []string) {
	var notices []string
	commands := builtInSlashCommands()
	if !conversation.IsDirectOrGroup() && conversation.ID != "" {
		if record, err := h.Messages.CodeChannel(ctx, principal.WorkspaceID, principal.UserID, conversation.ID); err == nil {
			commands = append(commands, h.codeChannelCommands(ctx, principal, record)...)
		} else if !errors.Is(err, store.ErrNotFound) {
			notices = append(notices, "This channel's agent commands are temporarily unavailable.")
		}
	}
	shortcuts, err := h.Messages.ListAppShortcuts(ctx, principal.WorkspaceID, principal.UserID, "global")
	if err != nil {
		notices = append(notices, "App shortcuts are temporarily unavailable.")
	}
	appCommands, err := h.Messages.ListAppShortcuts(ctx, principal.WorkspaceID, principal.UserID, "slash")
	if err != nil {
		notices = append(notices, "App slash commands are temporarily unavailable.")
		return commands, shortcuts, notices
	}
	taken := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		taken[command.Command] = struct{}{}
	}
	for _, command := range appCommands {
		if _, reserved := taken[command.Command]; !reserved {
			commands = append(commands, command)
		}
	}
	sort.SliceStable(commands, func(left, right int) bool { return commands[left].Command < commands[right].Command })
	return commands, shortcuts, notices
}

// codeChannelCommands is a code channel's agent commands as the composer
// offers them, each under the name of the agent that registered it.
func (h Handler) codeChannelCommands(ctx context.Context, principal auth.Principal, record domain.CodeChannel) []domain.AppShortcut {
	agents := make(map[domain.UserID]string)
	commands := make([]domain.AppShortcut, 0, len(record.Commands))
	for _, command := range record.Commands {
		name, known := agents[command.BotUserID]
		if !known {
			name = "Agent"
			if bot, err := h.Messages.UserInfo(ctx, principal.WorkspaceID, principal.UserID, command.BotUserID); err == nil {
				name = displayName(bot)
			}
			agents[command.BotUserID] = name
		}
		commands = append(commands, domain.AppShortcut{
			AppID: command.AppID, AppName: name, Name: command.Slash(), Command: command.Slash(), Description: command.Description,
			UsageHint: command.ArgumentHint, ShouldEscape: command.ShouldEscape, Type: "slash",
		})
	}
	return commands
}

// composerBroadcastLabel is the text beside the thread composer's broadcast
// checkbox, which Slack words by conversation kind.
func composerBroadcastLabel(conversation domain.Conversation, channelName string) string {
	if conversation.IsDirectOrGroup() {
		return "Also send as direct message"
	}
	return "Also send to #" + channelName
}

// validClientMessageID accepts the composer's per-send identifier, which the
// server uses as the post's idempotency key so a retried send cannot post
// twice. It is the client's own opaque token; anything that is not a short
// token is ignored rather than refused, because refusing it would fail a
// message over a field the member never sees.
func validClientMessageID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '-':
		default:
			return false
		}
	}
	return true
}

// presetLocalTime resolves one of Slack's suggested times in the member's
// time zone. The schedule menu offers "Tomorrow at 9:00 AM" and "Monday at
// 9:00 AM"; a message reminder's "Tomorrow" and "Next week" are the same two
// times, and its relative presets are the same offsets, so both surfaces
// resolve a named time the same way.
func presetLocalTime(preset string, now time.Time, location *time.Location) (time.Time, bool) {
	return presetLocalTimeAt(preset, now, location, domain.DefaultReminderClock)
}

// presetLocalTimeAt resolves a named day at the given time of day. A reminder's
// "Tomorrow" and "Next week" land at the member's default reminder time
// (Preferences' "Set a default time for reminder notifications"); the schedule
// menu names 9:00 AM and keeps it.
func presetLocalTimeAt(preset string, now time.Time, location *time.Location, clock domain.ReminderClock) (time.Time, bool) {
	local := now.In(location)
	atClock := func(day time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), clock.Hour, clock.Minute, 0, 0, location)
	}
	switch preset {
	case "20m":
		return now.Add(20 * time.Minute), true
	case "1h":
		return now.Add(time.Hour), true
	case "3h":
		return now.Add(3 * time.Hour), true
	case "tomorrow":
		return atClock(local.AddDate(0, 0, 1)), true
	case "monday", "nextweek":
		// Slack's "Next week" is the coming Monday at 9:00 AM.
		days := (int(time.Monday) - int(local.Weekday()) + 7) % 7
		if days == 0 {
			days = 7
		}
		return atClock(local.AddDate(0, 0, days)), true
	}
	return time.Time{}, false
}

// scheduleTimeFromFields reads a schedule request that arrived without the
// client's computed post_at: a suggested time or a custom date and time in the
// named zone. This is the path a browser without JavaScript takes.
func scheduleTimeFromFields(fields map[string]string, now time.Time) (time.Time, bool) {
	location, err := time.LoadLocation(strings.TrimSpace(fields["timezone"]))
	if err != nil || strings.TrimSpace(fields["timezone"]) == "" {
		location = time.UTC
	}
	preset := strings.TrimSpace(fields["schedule_preset"])
	if preset == "tomorrow" || preset == "monday" {
		return presetLocalTime(preset, now, location)
	}
	value := strings.TrimSpace(fields["schedule_at"])
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation("2006-01-02T15:04", value, location)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
