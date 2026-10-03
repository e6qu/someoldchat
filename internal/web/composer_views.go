package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// composerDialogsView is what the page-level composer dialogs need: the
// shared suggestion directory and the shortcut browser's form fields.
type composerDialogsView struct {
	Directory composerDirectory
	CSRFToken string
	Channel   string
	CanUpload bool
}

// composerDirectoryFor builds the suggestion directory both composers share.
// Each part degrades on its own: a store failure for one leaves the others
// usable and says which suggestions are missing.
func (h Handler) composerDirectoryFor(ctx context.Context, principal auth.Principal, conversation domain.Conversation) (composerDirectory, []string) {
	var notices []string
	people, nonMembers, peopleNotices := h.composerPeople(ctx, principal, conversation)
	notices = append(notices, peopleNotices...)
	groups, groupNotice := h.composerUserGroups(ctx, principal)
	if groupNotice != "" {
		notices = append(notices, groupNotice)
	}
	channels, err := h.composerChannels(ctx, principal)
	if err != nil {
		notices = append(notices, "Channel suggestions are temporarily unavailable.")
		channels = nil
	}
	commands, shortcuts, commandNotices := h.composerCommands(ctx, principal, conversation)
	notices = append(notices, commandNotices...)
	return composerDirectory{
		People: people, Groups: groups, Channels: channels,
		Specials: composerSpecialMentions(conversation),
		Commands: commands, Shortcuts: shortcuts, NonMembers: nonMembers,
	}, notices
}

// composerPageRequest is what a conversation page knows that its composers
// need.
type composerPageRequest struct {
	Principal       auth.Principal
	Conversation    domain.Conversation
	ThreadTimestamp string
	CSRFToken       string
	ChannelName     string
	ChannelPrefix   string
	MemberCount     int
	CanUpload       bool
	Member          bool
	AtLatest        bool
	// State is a rejected submission being shown again. It belongs to the
	// thread composer when State.Thread is set and to the conversation
	// composer otherwise; the other composer shows its own saved draft.
	State composerState
}

// composerViews builds the conversation composer and, when a thread is open,
// the thread pane's reply composer. Each restores its own saved draft.
func (h Handler) composerViews(ctx context.Context, request composerPageRequest) (composerView, composerView, []string) {
	var notices []string
	principal, conversation := request.Principal, request.Conversation
	channel := string(conversation.ID)
	canInvite := !conversation.IsDirectOrGroup() && principal.HasScope(auth.ScopeChannelsManage)
	base := func(thread string) composerView {
		view := composerView{
			CSRFToken: request.CSRFToken, Channel: channel, ChannelLabel: request.ChannelName,
			ComposeURL:     mutationURL("/app/message", channel, "", request.ThreadTimestamp, ""),
			DraftURL:       mutationURL("/app/draft", channel, "", thread, ""),
			ScheduleURL:    mutationURL("/app/message/schedule", channel, "", request.ThreadTimestamp, ""),
			StageUploadURL: mutationURL("/app/file/stage", channel, "", thread, ""),
			ScheduledURL:   "/app/drafts?" + url.Values{"channel": {channel}, "tab": {"scheduled"}}.Encode(),
			CanUpload:      request.CanUpload, CanSchedule: principal.HasScope(auth.ScopeChatWrite),
			HasShortcuts: true, IsDirect: conversation.IsDirectOrGroup(),
		}
		if !view.IsDirect {
			view.MemberCount = request.MemberCount
		}
		if canInvite {
			view.CanInvite = true
			view.InviteURL = "/app/conversation/invite?" + url.Values{"channel": {channel}}.Encode()
		}
		return view
	}
	restore := func(view *composerView, thread string, state composerState, own bool) {
		var attachments []domain.DraftAttachment
		if own {
			view.Draft, view.Error, view.ScheduleAt, view.Broadcast = state.Draft, state.Message, state.ScheduleAt, state.Broadcast
			attachments = state.Attachments
		}
		if view.Draft == "" && len(attachments) == 0 && request.Member {
			draft, err := h.Messages.Draft(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.MessageTimestamp(thread))
			switch {
			case err == nil:
				view.Draft, attachments = draft.Text, draft.Attachments
			case errors.Is(err, store.ErrNotFound):
			default:
				notices = append(notices, "Your saved draft is temporarily unavailable.")
			}
		}
		view.DraftAttachments = newDraftAttachmentViews(attachments)
		encoded, _ := json.Marshal(view.DraftAttachments)
		view.DraftJSON = string(encoded)
	}
	main := base("")
	main.Label = "Message " + request.ChannelPrefix + request.ChannelName
	main.HXTarget = "#timeline"
	main.ViewThread = request.ThreadTimestamp
	main.Autofocus = request.ThreadTimestamp == ""
	if !request.AtLatest {
		main.Newest = appURL(channel, request.ThreadTimestamp, "", "", "")
	}
	restore(&main, "", request.State, !request.State.Thread)
	var thread composerView
	if request.ThreadTimestamp != "" {
		thread = base(request.ThreadTimestamp)
		thread.IDPrefix = "thread-"
		thread.Thread = true
		thread.ThreadTimestamp = request.ThreadTimestamp
		thread.Label = "Reply…"
		thread.BroadcastLabel = composerBroadcastLabel(conversation, request.ChannelName)
		thread.HXTarget = "#thread-messages"
		thread.Autofocus = true
		restore(&thread, request.ThreadTimestamp, request.State, request.State.Thread)
	}
	if main.Error != "" || thread.Error != "" {
		main.Autofocus, thread.Autofocus = false, false
	}
	return main, thread, notices
}
