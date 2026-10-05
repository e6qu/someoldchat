package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Threads and Unreads are the two views Slack leads its sidebar with, and
// neither existed. Their absence was not only a missing page: it is why the two
// navigation shortcuts Slack publishes for them could not be bound in the
// keyboard layer, because a shortcut that goes nowhere is worse than one that
// is missing.
//
// Both are read-only projections over state the product already keeps. Threads
// reads the follow records that `thread_follows` has stored since threads were
// built, and which nothing has ever listed. Unreads reads the per-conversation
// read cursor that the sidebar badge already reports.

// unreadConversationWindow bounds how many conversations the Unreads view
// opens, and unreadMessageWindow how many messages it shows from each. Slack's
// Unreads view is a triage surface, not an archive: a member with four hundred
// unread conversations needs the page to render, and the count in the heading
// tells them what the page is not showing.
const (
	unreadConversationWindow = 30
	unreadMessageWindow      = 20
)

type threadsData struct {
	// Shell is the workspace frame the page renders inside.
	Shell     shellView
	Channel   string
	CSRFToken string
	Threads   []followedThreadView
	Notice    string
	Empty     bool
	// Directories are the mention suggestions, one per conversation the
	// cards come from, so a card suggests that conversation's members; Dialogs
	// are the composer's page-level dialogs, rendered once for every card.
	Directories []composerDirectory
	Dialogs     composerDialogsView
	Composers   bool
}

// followedThreadView is one card of the Threads view as Slack draws it: the
// conversation and who is talking, the root message, a count of the replies
// not shown, the latest replies, and a place to reply.
type followedThreadView struct {
	Conversation string
	ChannelName  string
	Root         string
	Participants string
	RootMessage  threadCardMessage
	Replies      []threadCardMessage
	// HiddenReplies is how many earlier replies the card leaves out, shown
	// as "N more replies" linking into the thread.
	HiddenReplies      int
	HiddenRepliesLabel string
	ReplyCountLabel    string
	Unread             int
	LastReplyMachine   string
	LastReplyRelative  string
	URL                string
	// Anchor names the card so a reply sent from it comes back to it.
	Anchor string
	// ReplyURL is where the card's reply form posts, and ReplyReturn the
	// Threads view the post answers with.
	ReplyURL    string
	ReplyReturn string
	// Composer is the card's reply composer: the thread composer, with
	// mention suggestions from the card's conversation, formatting, its own
	// saved draft and attachments. HasComposer is false where the member
	// cannot post there, and the card links into the thread instead.
	Composer    composerView
	HasComposer bool
}

// threadCardMessage is a message as a Threads card shows it: author, time and
// the body rendered the way the timeline renders it.
type threadCardMessage struct {
	AuthorName    string
	AuthorInitial string
	MachineTime   string
	ClockTime     string
	FullTime      string
	Text          template.HTML
	Edited        bool
	URL           string
}

// threadCardReplies is how many of a thread's latest replies a Threads card
// shows under its root.
const threadCardReplies = 2

type unreadsData struct {
	// Shell is the workspace frame the page renders inside.
	Shell         shellView
	Channel       string
	CSRFToken     string
	Conversations []unreadConversationView
	Total         int
	Shown         int
	Truncated     bool
	Notice        string
}

type unreadConversationView struct {
	ID          string
	Name        string
	Prefix      string
	Count       int
	More        int
	URL         string
	MarkReadURL string
	Messages    []unreadMessageView
}

// unreadMessageView is one unread message as the Unreads view shows it: the
// author's face and name (which open their profile), the message rendered
// by the same mrkdwn renderer as the timeline, a machine time the page shows
// in the reader's own zone, and the permalink the row opens.
type unreadMessageView struct {
	AuthorID    string
	AuthorName  string
	AvatarURL   string
	Initial     string
	Text        template.HTML
	MachineTime string
	Time        string
	URL         string
}

// threadsPage lists the threads the member follows, newest reply first.
func (h Handler) threadsPage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
	if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	page, err := h.Messages.FollowedThreads(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 50})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) || errors.Is(err, store.ErrInvalidArgument) {
			h.writePageError(w, http.StatusBadRequest, "That Threads link is not valid", "Open Threads from the workspace and try again.")
			return
		}
		h.writeStoreError(w, err, "Threads is temporarily unavailable.")
		return
	}
	names := h.newUserNames(r.Context(), principal)
	data := threadsData{
		Channel:   channel,
		CSRFToken: auth.CSRFToken(sessionCookie.Value),
		Threads:   make([]followedThreadView, 0, len(page.Threads)),
	}
	emojiImages := map[string]string{}
	if custom, emojiErr := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID); emojiErr == nil {
		emojiImages = customEmojiImages(custom)
	}
	location, now := readerLocation(r), time.Now()
	for _, thread := range page.Threads {
		// Following starts when a member posts, so the follow records include
		// every message they wrote; a thread is a message with replies, and
		// Slack's Threads view lists only those.
		if thread.ReplyCount == 0 {
			continue
		}
		view, ok := h.followedThreadCard(r.Context(), principal, thread, names, emojiImages, location, now)
		if !ok {
			data.Notice = "Some threads could not be shown right now."
			continue
		}
		view.ReplyReturn = "/app/threads?" + url.Values{"channel": {channel}}.Encode() + "#" + view.Anchor
		data.Threads = append(data.Threads, view)
	}
	data.Empty = len(data.Threads) == 0
	if principal.HasScope(auth.ScopeChatWrite) && !data.Empty {
		h.threadCardComposers(r.Context(), principal, &data)
	}
	data.Shell = h.newShell(r, principal, shellRequest{Destination: destinationHome})
	h.writeHTML(w, threadsTemplate, data, http.StatusOK, "Threads rendering unavailable")
}

// threadCardComposers gives each card the thread composer, as Slack's Threads
// view does. Cards come from many conversations, so each conversation gets its
// own suggestion directory, read once however many of its threads are listed:
// a card suggests that conversation's members and labels everyone else as
// outside it. A card whose conversation cannot be read, or is archived, keeps
// no composer and links into the thread instead.
func (h Handler) threadCardComposers(ctx context.Context, principal auth.Principal, data *threadsData) {
	conversations := map[string]*domain.Conversation{}
	canUpload := principal.HasScope(auth.ScopeFilesWrite)
	for index := range data.Threads {
		card := &data.Threads[index]
		conversation, seen := conversations[card.Conversation]
		if !seen {
			conversations[card.Conversation] = nil
			info, err := h.Messages.ConversationInfo(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationID(card.Conversation))
			if err != nil || info.Archived {
				continue
			}
			conversation = &info
			conversations[card.Conversation] = conversation
			directory, notices := h.composerDirectoryFor(ctx, principal, info)
			directory.ID = "composer-directory-" + card.Conversation
			data.Directories = append(data.Directories, directory)
			if len(notices) > 0 && data.Notice == "" {
				data.Notice = "Some reply suggestions are temporarily unavailable."
			}
		}
		if conversation == nil {
			continue
		}
		prefix := "#"
		if conversation.IsDirectOrGroup() {
			prefix = ""
		}
		_, composer, notices := h.composerViews(ctx, composerPageRequest{
			Principal: principal, Conversation: *conversation, ThreadTimestamp: card.Root,
			CSRFToken: data.CSRFToken, ChannelName: card.ChannelName, ChannelPrefix: prefix,
			CanUpload: canUpload, Member: true, AtLatest: true,
		})
		if len(notices) > 0 && data.Notice == "" {
			data.Notice = "Some saved drafts are temporarily unavailable."
		}
		composer.IDPrefix = card.Anchor + "-"
		composer.ReturnTo = card.ReplyReturn
		composer.DirectoryID = "composer-directory-" + card.Conversation
		composer.AccessibleLabel = "Reply to the thread in " + prefix + card.ChannelName
		composer.Quiet, composer.Autofocus = true, false
		composer.NoEmojiPicker = true
		// The shortcut browser and recent files are page-level and name one
		// conversation; a page of cards from several has no single one to
		// name. Typing / still offers the conversation's commands.
		composer.HasShortcuts, composer.RecentFiles = false, nil
		composer.HXTarget = ""
		card.Composer, card.HasComposer = composer, true
		data.Composers = true
	}
	data.Dialogs = composerDialogsView{CSRFToken: data.CSRFToken, CanUpload: canUpload}
}

// followedThreadCard reads a thread's root and latest replies for its card.
// A thread longer than one window shows its root and the count alone, rather
// than replies that are not its latest.
func (h Handler) followedThreadCard(ctx context.Context, principal auth.Principal, thread domain.FollowedThread, names *userNames, emojiImages map[string]string, location *time.Location, now time.Time) (followedThreadView, bool) {
	view := followedThreadView{
		Conversation:    string(thread.Conversation),
		ChannelName:     thread.ConversationName,
		Root:            string(thread.Root),
		ReplyCountLabel: replyCountLabel(thread.ReplyCount),
		Unread:          thread.UnreadReplies,
		URL:             appURL(string(thread.Conversation), string(thread.Root), "", "", ""),
		Anchor:          "thread-" + string(thread.Conversation) + "-" + string(thread.Root),
		ReplyURL:        mutationURL("/app/message", string(thread.Conversation), "", string(thread.Root), ""),
	}
	if !thread.LastReplyAt.IsZero() {
		view.LastReplyMachine = thread.LastReplyAt.UTC().Format(time.RFC3339Nano)
		view.LastReplyRelative = relativeTime(thread.LastReplyAt, now)
	}
	page, err := h.Messages.Replies(ctx, principal.WorkspaceID, principal.UserID, thread.Conversation, thread.Root, domain.ThreadRequest{Page: domain.PageRequest{Limit: timelineWindow}})
	if err != nil || len(page.Messages) == 0 {
		return view, false
	}
	card := func(message domain.Message) threadCardMessage {
		if names.hidden(message.AuthorID) {
			return threadCardMessage{
				AuthorName: hiddenPersonName, AuthorInitial: "?",
				MachineTime: message.CreatedAt.UTC().Format(time.RFC3339Nano),
				ClockTime:   clockTime(message.CreatedAt, location), FullTime: fullTime(message.CreatedAt, location),
				Text: template.HTML(hiddenPreviewText),
				URL:  domain.MessagePermalinkPath(message.Conversation, domain.NewMessageTimestamp(message.CreatedAt), message.ThreadTimestamp),
			}
		}
		author := names.name(message.AuthorID)
		if presentation := decodeMessageStreamPresentation(message.StreamState); presentation.Username != "" {
			author = presentation.Username
		}
		display := message
		display.Text = resolveSlackReferences(message.Text, names)
		display.Blocks = resolveSlackReferenceJSON(message.Blocks, names)
		content := newRichMessageContent(display, emojiImages)
		text := content.Text
		if text == "" {
			text = template.HTML(template.HTMLEscapeString(plainPreview(display.Text, 280))) // #nosec G203 -- escaped immediately.
		}
		return threadCardMessage{
			AuthorName: author, AuthorInitial: initial(author),
			MachineTime: message.CreatedAt.UTC().Format(time.RFC3339Nano),
			ClockTime:   clockTime(message.CreatedAt, location), FullTime: fullTime(message.CreatedAt, location),
			Text:   markSelfMentions(text, string(principal.UserID)),
			Edited: !message.EditedAt.IsZero(),
			URL:    domain.MessagePermalinkPath(message.Conversation, domain.NewMessageTimestamp(message.CreatedAt), message.ThreadTimestamp),
		}
	}
	root := page.Messages[0]
	view.RootMessage = card(root)
	replies := make([]domain.Message, 0, len(page.Messages)-1)
	for _, message := range page.Messages[1:] {
		if !message.Deleted {
			replies = append(replies, message)
		}
	}
	people := []string{}
	seen := map[domain.UserID]bool{}
	for _, message := range append([]domain.Message{root}, replies...) {
		if seen[message.AuthorID] || message.AuthorID == "" {
			continue
		}
		seen[message.AuthorID] = true
		name := names.name(message.AuthorID)
		if message.AuthorID == principal.UserID {
			name = "you"
		}
		people = append(people, name)
	}
	view.Participants = participantSentence(people)
	if page.HasMore {
		// The window ends before the latest reply; show none rather than
		// replies from the middle of the thread.
		replies = nil
	}
	if len(replies) > threadCardReplies {
		replies = replies[len(replies)-threadCardReplies:]
	}
	for _, message := range replies {
		view.Replies = append(view.Replies, card(message))
	}
	if hidden := thread.ReplyCount - len(view.Replies); hidden > 0 {
		view.HiddenReplies = hidden
		view.HiddenRepliesLabel = strings.Replace(replyCountLabel(hidden), "repl", "more repl", 1)
	}
	return view, true
}

// participantSentence names a thread's people as Slack's Threads view does:
// "Ada", "Ada and you", "Ada, Grace and you".
func participantSentence(people []string) string {
	for index, name := range people {
		if name == "you" && index != len(people)-1 {
			people = append(append(people[:index:index], people[index+1:]...), "you")
			break
		}
	}
	switch len(people) {
	case 0:
		return ""
	case 1:
		if people[0] == "you" {
			return "You"
		}
		return people[0]
	}
	return strings.Join(people[:len(people)-1], ", ") + " and " + people[len(people)-1]
}

// unreadsPage groups every unread message by conversation, so a member can
// clear a backlog without opening each conversation in turn.
func (h Handler) unreadsPage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
	if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	conversations, err := h.Messages.Conversations(r.Context(), principal.WorkspaceID, principal.UserID,
		domain.ConversationListRequest{Limit: 200, IncludeClosedDirects: true})
	if err != nil {
		h.writeStoreError(w, err, "Unreads is temporarily unavailable.")
		return
	}
	names := h.newUserNames(r.Context(), principal)
	data := unreadsData{Channel: channel, CSRFToken: auth.CSRFToken(sessionCookie.Value)}
	for _, conversation := range conversations.Conversations {
		if conversation.UnreadCount <= 0 {
			continue
		}
		data.Total++
		if len(data.Conversations) >= unreadConversationWindow {
			data.Truncated = true
			continue
		}
		messages, ok := h.unreadMessages(r, principal, conversation, names)
		if !ok {
			// One unreadable conversation must not take the page down; it is
			// simply not listed, and Total still counts it so the heading and
			// the list disagreeing is visible rather than silent.
			continue
		}
		name := conversationName(conversation)
		prefix := unreadPrefix(conversation)
		// A DM is named for the people in it, as the sidebar names it; its
		// stored name is an internal key ("direct"), not something to show.
		if conversation.IsDirectOrGroup() {
			if participants := h.participantNames(r.Context(), principal, conversation.ID); participants != "" {
				name, prefix = participants, ""
			}
		}
		view := unreadConversationView{
			ID:          string(conversation.ID),
			Name:        name,
			Prefix:      prefix,
			Count:       conversation.UnreadCount,
			URL:         appURL(string(conversation.ID), "", "", "", ""),
			MarkReadURL: "/app/read?channel=" + string(conversation.ID),
			Messages:    messages,
		}
		if conversation.UnreadCount > len(messages) {
			view.More = conversation.UnreadCount - len(messages)
		}
		data.Conversations = append(data.Conversations, view)
	}
	data.Shown = len(data.Conversations)
	data.Shell = h.newShell(r, principal, shellRequest{Destination: destinationHome})
	h.writeHTML(w, unreadsTemplate, data, http.StatusOK, "Unreads rendering unavailable")
}

// unreadMessages reads the newest window of a conversation and keeps what falls
// after the member's read position. It reads newest-first and filters in Go
// because the read position is a MessageTimestamp and the page cursor is not:
// there is no cursor that means "the message after the one I have read".
func (h Handler) unreadMessages(r *http.Request, principal auth.Principal, conversation domain.Conversation, names *userNames) ([]unreadMessageView, bool) {
	history, err := h.Messages.History(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID,
		domain.HistoryRequest{Page: domain.PageRequest{Limit: unreadMessageWindow, Descending: true}})
	if err != nil {
		return nil, false
	}
	cursor, err := h.Messages.ReadCursor(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, false
	}
	// Compared as instants, not as timestamp text: the textual form's seconds
	// field is unpadded, so string order is only accidentally time order.
	var lastRead time.Time
	if cursor.LastRead != "" {
		if parsed, parseErr := domain.ParseMessageTimestamp(cursor.LastRead); parseErr == nil {
			lastRead = parsed
		}
	}
	unread := make([]domain.Message, 0, len(history.Messages))
	// History answered newest-first; the view reads oldest-first, which is the
	// order a member catches up in. A member's own messages are never unread
	// to them, so they are not listed even when the cursor is behind them.
	for index := len(history.Messages) - 1; index >= 0; index-- {
		message := history.Messages[index]
		if message.AuthorID == principal.UserID {
			continue
		}
		if !lastRead.IsZero() && !message.CreatedAt.Truncate(time.Microsecond).After(lastRead.Truncate(time.Microsecond)) {
			continue
		}
		unread = append(unread, message)
	}
	rendered := h.newResultViews(r.Context(), principal, unread, names)
	views := make([]unreadMessageView, 0, len(rendered))
	for index, message := range rendered {
		author := unread[index].AuthorID
		view := unreadMessageView{
			AuthorName: message.AuthorName, AvatarURL: names.avatarURL(author), Initial: message.AuthorInitial,
			Text: message.DisplayText, MachineTime: message.MachineTime, Time: message.DisplayTime, URL: message.Permalink,
		}
		if unread[index].AppID == "" && author != "" {
			view.AuthorID = string(author)
		}
		views = append(views, view)
	}
	return views, true
}

// unreadPrefix names the conversation the way the sidebar does, so the two
// surfaces do not disagree about what a conversation is called.
func unreadPrefix(conversation domain.Conversation) string {
	if conversation.IsDirectOrGroup() {
		return "@"
	}
	return "#"
}

var threadsTemplate = mustPage(threadsMarkup)

var threadsMarkup = `{{define "title"}}Threads · SameOldChat{{end}}
{{define "styles"}}` + shellStyle + shellPageStyle + composerStyle + `<style>
.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(900px,calc(100% - 32px));margin:28px auto 48px}.heading{display:grid;gap:5px;margin-bottom:17px}.heading h2,.heading p{margin:0}.heading p{color:var(--muted)}
` + threadsViewStyle + `
@media(max-width:600px){.layout{width:min(100% - 20px,900px);margin-top:18px}}
</style>{{end}}
{{define "scripts"}}` + shellScript + searchSuggestionsScript + localTimeScript + composerScript + `{{end}}
{{define "content"}}{{template "shell-open" .Shell}}<main class="layout">
<div class="heading"><h1>Threads</h1><p>Threads you follow, most recently replied first.</p></div>
{{template "threads-list" .}}
{{if .Composers}}{{range .Directories}}{{template "composer-directory" .}}{{end}}{{template "composer-dialogs" .Dialogs}}{{end}}
<p class="visually-hidden" id="live-status" role="status" aria-live="polite"></p>
</main>{{template "shell-close" .Shell}}{{end}}
` + threadsListPartial + composerPartial + typingPartial

// threadsListPartial is the Threads view's list of cards.
//
// Each card ends in the thread composer, as Slack's Threads view does: mention
// suggestions from the card's conversation, formatting, its own saved draft
// and attachments. A reply posts into the thread and the page comes back to
// the card. Refused, it is shown next to the composer with the draft kept;
// without script, the thread opens with the draft kept and the reason shown.
const threadsListPartial = `{{define "threads-list"}}
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .Empty}}<p class="empty">No threads yet. When you reply to a message, or are mentioned in a thread, it shows up here.</p>
{{else}}<ul class="thread-list" aria-label="Threads">{{range .Threads}}
  <li class="thread-card" id="{{.Anchor}}">
    <header class="thread-card-head">
      <a class="thread-card-channel" href="{{.URL}}">#{{.ChannelName}}</a>
      {{if .Participants}}<span class="thread-card-people">{{.Participants}}</span>{{end}}
      {{if .Unread}}<span class="thread-unread">{{.Unread}} new</span>{{end}}
    </header>
    <article class="thread-card-message">
      <span class="thread-card-avatar" aria-hidden="true">{{.RootMessage.AuthorInitial}}</span>
      <div class="thread-card-body"><p class="thread-card-meta"><span class="author">{{.RootMessage.AuthorName}}</span> <a class="time" href="{{.RootMessage.URL}}"><time datetime="{{.RootMessage.MachineTime}}" title="{{.RootMessage.FullTime}}" data-format="time">{{.RootMessage.ClockTime}}</time></a></p><div class="message-text">{{.RootMessage.Text}}{{if .RootMessage.Edited}}<span class="edited-label"> (edited)</span>{{end}}</div></div>
    </article>
    {{if .HiddenReplies}}<a class="thread-card-more" href="{{.URL}}">{{.HiddenRepliesLabel}}</a>{{end}}
    {{range .Replies}}<article class="thread-card-message reply">
      <span class="thread-card-avatar" aria-hidden="true">{{.AuthorInitial}}</span>
      <div class="thread-card-body"><p class="thread-card-meta"><span class="author">{{.AuthorName}}</span> <a class="time" href="{{.URL}}"><time datetime="{{.MachineTime}}" title="{{.FullTime}}" data-format="time">{{.ClockTime}}</time></a></p><div class="message-text">{{.Text}}{{if .Edited}}<span class="edited-label"> (edited)</span>{{end}}</div></div>
    </article>{{end}}
    <footer class="thread-card-foot">
      {{if .HasComposer}}<div class="thread-card-composer" data-thread-reply-slot data-channel="{{.Conversation}}" data-thread-ts="{{.Root}}">{{template "composer" .Composer}}</div>{{else}}<a class="thread-card-open" href="{{.URL}}">Open the thread to reply</a>{{end}}
      <span class="thread-card-summary">{{.ReplyCountLabel}}{{if .LastReplyRelative}} · Last reply <time datetime="{{.LastReplyMachine}}" data-format="relative">{{.LastReplyRelative}}</time>{{end}}</span>
    </footer>
  </li>{{end}}
</ul>{{end}}
{{end}}`

// threadsViewStyle styles the Threads cards, including the formatted message
// text they share with the timeline.
const threadsViewStyle = `:root{` + messageLightTokens + `}
html[data-theme=dark]{` + messageDarkTokens + `}
@media(prefers-color-scheme:dark){html[data-theme=light]:not([data-theme-explicit]){` + messageDarkTokens + `}}
.thread-list{display:grid;gap:16px;margin:0;padding:0;list-style:none}
.thread-card{border:1px solid var(--line);border-radius:10px;background:var(--panel-strong);overflow:hidden}
.thread-card-head{display:flex;align-items:baseline;flex-wrap:wrap;gap:4px 10px;padding:12px 16px 4px}
.thread-card-channel{font-weight:800;color:var(--text);text-decoration:none}.thread-card-channel:hover{text-decoration:underline}
.thread-card-people{color:var(--muted);font-size:13px}
.thread-unread{border-radius:9px;background:var(--action);color:var(--on-strong);font-size:11px;font-weight:800;padding:2px 8px}
.thread-card-message{display:grid;grid-template-columns:36px minmax(0,1fr);gap:8px;padding:8px 16px}
.thread-card-avatar{width:36px;height:36px;border-radius:6px;display:grid;place-items:center;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;font-weight:800}
.thread-card-body{min-width:0}
.thread-card-meta{margin:0}.thread-card-meta .author{font-weight:800}.thread-card-meta .time{color:var(--muted);font-size:12px;text-decoration:none;margin-left:6px}
.thread-card .message-text{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.46}
.thread-card .message-text blockquote{margin:4px 0;padding:0 0 0 12px;border-left:4px solid var(--quote-bar)}
.thread-card .message-text pre{margin:4px 0;padding:8px 10px;border:1px solid var(--line);border-radius:4px;background:var(--code-bg);font:12px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;white-space:pre-wrap}
.thread-card .message-text code{padding:1px 3px;border:1px solid var(--line);border-radius:3px;background:var(--code-bg);color:var(--code-text);font:12px ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.thread-card .message-text pre code{border:0;padding:0;background:transparent;color:inherit}
.thread-card .message-text ul,.thread-card .message-text ol{margin:2px 0;padding-left:26px;white-space:normal}
.thread-card .slack-mention{padding:0 2px;border-radius:3px;background:var(--mention-link-bg);color:var(--mention-link);font-weight:600;text-decoration:none}
.thread-card .slack-mention[data-self],.thread-card .mention-broadcast{background:var(--mention-pill);color:var(--mention-pill-text)}
.thread-card .edited-label{color:var(--muted);font-size:12px}
.thread-card-more{display:block;margin:0 16px 0 60px;padding:4px 0;color:var(--mention-link);font-weight:700;font-size:13px;text-decoration:none}
.thread-card-more:hover{text-decoration:underline}
.thread-card-message.reply{padding-left:16px}
.thread-card-foot{display:flex;align-items:center;flex-wrap:wrap;gap:8px 16px;padding:8px 16px 14px}
.thread-card-composer{flex:1 1 100%;min-width:0}
.thread-card-composer .composer{margin:0}
.thread-card-open{color:var(--mention-link);font-weight:700;font-size:13px}
.thread-card-summary{color:var(--muted);font-size:12px}
.empty{padding:30px;border:1px dashed var(--line);border-radius:10px;color:var(--muted);text-align:center}`

var unreadsTemplate = mustPage(unreadsMarkup)

const unreadsMarkup = `{{define "title"}}Unreads · SameOldChat{{end}}
{{define "styles"}}` + shellStyle + shellPageStyle + viewStyle + `<style>
.unread-group{margin:0 0 16px}
.unread-head{display:flex;align-items:center;gap:10px;flex-wrap:wrap;padding:10px 14px;border-bottom:1px solid var(--line);background:var(--panel)}
.unread-head h3{margin:0;font-size:16px}
.unread-head h3 a{color:var(--text);text-decoration:none}.unread-head h3 a:hover{text-decoration:underline}
.unread-head form{margin:0 0 0 auto}
.unread-messages{margin:0;padding:0;list-style:none}
.unread-meta{display:flex;align-items:baseline;gap:8px;flex-wrap:wrap}
.unread-author{color:var(--text);font-weight:800;text-decoration:none}.unread-author:hover{text-decoration:underline}
.unread-time{color:var(--muted);font-size:12px;text-decoration:none}.unread-time:hover{text-decoration:underline}
.unread-more{margin:0;padding:10px 14px;border-top:1px solid var(--line);color:var(--muted);font-size:13px}
.unread-more a{font-weight:700}
@media(max-width:600px){.unread-head form{margin-left:0}}
</style>{{end}}
{{define "scripts"}}` + shellScript + searchSuggestionsScript + localTimeScript + rowLinkScript + profilePanelScript + `{{end}}
{{define "content"}}{{template "shell-open" .Shell}}{{template "unreads-view" .}}{{template "shell-close" .Shell}}{{end}}
{{define "unreads-view"}}<main class="v-page unreads-page">
<div class="v-head"><h1>Unreads</h1>{{if .Total}}<form method="post" action="/app/read/all?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="v-btn" type="submit" {{ariaKeyshortcuts "Mark every conversation read"}}><span aria-hidden="true">✓</span> Mark all as read</button></form>{{end}}</div>
<p class="v-sub">{{if eq .Total 1}}1 conversation has unread messages.{{else if .Total}}{{.Total}} conversations have unread messages.{{else}}Everything is read.{{end}}</p>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .Conversations}}{{range .Conversations}}
<section class="unread-group v-list" aria-labelledby="unread-heading-{{.ID}}">
  <div class="unread-head">
    <h3 id="unread-heading-{{.ID}}"><a href="{{.URL}}">{{.Prefix}}{{.Name}}</a></h3>
    <span class="v-count">{{.Count}} new</span>
    <form method="post" action="{{.MarkReadURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="v-btn quiet" type="submit" aria-label="Mark {{.Prefix}}{{.Name}} as read">Mark as read</button></form>
  </div>
  <ul class="unread-messages">{{range .Messages}}
    <li class="v-row" data-row-href="{{.URL}}"><span class="v-avatar" aria-hidden="true">{{if .AvatarURL}}<img src="{{.AvatarURL}}" alt="">{{else}}{{.Initial}}{{end}}</span>
      <div class="v-row-main"><div class="unread-meta">{{if .AuthorID}}<a class="unread-author" href="/app/members?user={{.AuthorID}}" data-profile-user="{{.AuthorID}}">{{.AuthorName}}</a>{{else}}<span class="unread-author">{{.AuthorName}}</span>{{end}}<a class="unread-time" href="{{.URL}}" aria-label="Open message from {{.AuthorName}}"><time datetime="{{.MachineTime}}">{{.Time}}</time></a></div>
      <div class="v-row-text v-text">{{.Text}}</div></div></li>{{end}}
  </ul>
  {{if .More}}<p class="unread-more">{{.More}} older unread messages are not shown here. <a href="{{.URL}}">Open the conversation</a> to read them.</p>{{end}}
</section>{{end}}
{{else}}<p class="v-empty"><strong>You’re all caught up</strong>Everything in this workspace has been read.</p>{{end}}
{{if .Truncated}}<p class="pager">Showing the first {{.Shown}} of {{.Total}} conversations with unread messages.</p>{{end}}
</main>{{end}}`
