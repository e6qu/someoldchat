package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
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
	Channel   string
	CSRFToken string
	Threads   []followedThreadView
	Notice    string
	Empty     bool
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

type unreadMessageView struct {
	AuthorName string
	Text       string
	Time       string
	URL        string
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
		data.Threads = append(data.Threads, view)
	}
	data.Empty = len(data.Threads) == 0
	h.writeHTML(w, threadsTemplate, data, http.StatusOK, "Threads rendering unavailable")
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
		view := unreadConversationView{
			ID:          string(conversation.ID),
			Name:        conversationName(conversation),
			Prefix:      unreadPrefix(conversation),
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
	h.writeHTML(w, unreadsTemplate, data, http.StatusOK, "Unreads rendering unavailable")
}

// unreadMessages reads the newest window of a conversation and keeps what falls
// after the member's read position. It reads newest-first and filters in Go
// because the read position is a MessageTimestamp and the page cursor is not:
// there is no cursor that means "the message after the one I have read".
func (h Handler) unreadMessages(r *http.Request, principal auth.Principal, conversation domain.Conversation, names *userNames) ([]unreadMessageView, bool) {
	limit := conversation.UnreadCount
	if limit > unreadMessageWindow {
		limit = unreadMessageWindow
	}
	history, err := h.Messages.History(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID,
		domain.HistoryRequest{Page: domain.PageRequest{Limit: limit, Descending: true}})
	if err != nil {
		return nil, false
	}
	cursor, err := h.Messages.ReadCursor(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, false
	}
	lastRead := cursor.LastRead
	views := make([]unreadMessageView, 0, len(history.Messages))
	// History answered newest-first; the view reads oldest-first, which is the
	// order a member catches up in.
	for index := len(history.Messages) - 1; index >= 0; index-- {
		message := history.Messages[index]
		timestamp := domain.NewMessageTimestamp(message.CreatedAt)
		if lastRead != "" && timestamp <= lastRead {
			continue
		}
		views = append(views, unreadMessageView{
			AuthorName: names.name(message.AuthorID),
			Text:       message.Text,
			Time:       message.CreatedAt.UTC().Format("Jan 2, 15:04"),
			URL:        appURL(string(conversation.ID), "", "", "", "") + "#" + messageAnchor(message.ID),
		})
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

const threadsMarkup = `{{define "title"}}Threads · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(900px,calc(100% - 32px));margin:28px auto 48px}.heading{display:grid;gap:5px;margin-bottom:17px}.heading h2,.heading p{margin:0}.heading p{color:var(--muted)}
` + threadsViewStyle + `
@media(max-width:600px){.bar{padding:0 12px}.layout{width:min(100% - 20px,900px);margin-top:18px}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><h1>Threads</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
<div class="heading"><h2>Threads</h2><p>Threads you follow, most recently replied first.</p></div>
{{template "threads-list" .}}
</main>{{end}}
` + threadsListPartial

// threadsListPartial is the Threads view's content, kept apart from its page
// chrome so the workspace shell can render the same list inside itself.
//
// Each card ends in a reply slot, [data-thread-reply-slot], naming the thread
// by data-channel and data-thread-ts. Without script it links into the thread,
// where the reply composer is; a composer that can post from here replaces it.
const threadsListPartial = `{{define "threads-list"}}
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .Empty}}<p class="empty">No threads yet. When you reply to a message, or are mentioned in a thread, it shows up here.</p>
{{else}}<ul class="thread-list" aria-label="Threads">{{range .Threads}}
  <li class="thread-card">
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
      <a class="thread-card-reply" href="{{.URL}}" data-thread-reply-slot data-channel="{{.Conversation}}" data-thread-ts="{{.Root}}">Reply…</a>
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
.thread-card-reply{flex:1 1 260px;display:block;padding:9px 12px;border:1px solid var(--field-line);border-radius:8px;color:var(--muted);text-decoration:none}
.thread-card-reply:hover{border-color:var(--focus);color:var(--text)}
.thread-card-summary{color:var(--muted);font-size:12px}
.empty{padding:30px;border:1px dashed var(--line);border-radius:10px;color:var(--muted);text-align:center}`

var unreadsTemplate = mustPage(unreadsMarkup)

const unreadsMarkup = `{{define "title"}}Unreads · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(900px,calc(100% - 32px));margin:28px auto 48px}.heading{display:grid;gap:5px;margin-bottom:17px}.heading h2,.heading p{margin:0}.heading p{color:var(--muted)}
.unread-group{margin:0 0 14px;border:1px solid var(--line);border-radius:10px;background:var(--panel);overflow:hidden}
.unread-head{display:flex;align-items:center;gap:10px;flex-wrap:wrap;padding:13px 16px;border-bottom:1px solid var(--line)}
.unread-head a{font-weight:800;color:var(--text);text-decoration:none;min-height:24px;display:inline-flex;align-items:center}.unread-head a:hover{color:var(--action)}
.unread-count{border-radius:9px;background:var(--action);color:var(--on-strong);font-size:11px;font-weight:800;padding:2px 8px;min-height:20px;display:inline-flex;align-items:center}
.unread-head form{margin:0 0 0 auto}.unread-head button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:6px 10px;font-weight:800;min-height:24px}
.unread-messages{margin:0;padding:0;list-style:none}
.unread-messages li{display:grid;gap:3px;padding:11px 16px;border-top:1px solid var(--line)}.unread-messages li:first-child{border-top:0}
.unread-author{font-weight:800}.unread-time{color:var(--muted);font-size:12px}
.unread-text{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}
.unread-more{padding:10px 16px;border-top:1px solid var(--line);color:var(--muted);font-size:12px}
.empty{padding:30px;border:1px dashed var(--line);border-radius:10px;color:var(--muted);text-align:center}
@media(max-width:600px){.bar{padding:0 12px}.layout{width:min(100% - 20px,900px);margin-top:18px}.unread-head form{margin-left:0}}
</style>{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><h1>Unreads</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
<div class="heading"><h2>Unreads</h2><p>{{if .Total}}{{.Total}} conversations have unread messages.{{else}}Everything is read.{{end}}</p></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .Total}}<form method="post" action="/app/read/all?channel={{.Channel}}" style="margin:0 0 16px">
  <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
  <button type="submit" {{ariaKeyshortcuts "Mark every conversation read"}}>Mark all as read</button>
</form>{{end}}
{{if .Conversations}}{{range .Conversations}}
<section class="unread-group" aria-label="{{.Prefix}}{{.Name}}">
  <div class="unread-head">
    <a href="{{.URL}}">{{.Prefix}}{{.Name}}</a>
    <span class="unread-count">{{.Count}} unread</span>
    <form method="post" action="{{.MarkReadURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Mark read</button></form>
  </div>
  <ul class="unread-messages">{{range .Messages}}
    <li><span class="unread-author">{{.AuthorName}}</span> <span class="unread-time">{{.Time}}</span><p class="unread-text">{{.Text}}</p></li>{{end}}
  </ul>
  {{if .More}}<p class="unread-more">{{.More}} older unread messages are not shown here. Open the conversation to read them.</p>{{end}}
</section>{{end}}
{{else}}<p class="empty">Nothing unread. Everything in this workspace has been read.</p>{{end}}
{{if .Truncated}}<p class="unread-more">Showing the first {{.Shown}} of {{.Total}} conversations with unread messages.</p>{{end}}
</main>{{end}}`
