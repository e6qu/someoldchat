package web

import (
	"errors"
	"net/http"
	"strings"

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

type followedThreadView struct {
	Conversation string
	ChannelName  string
	Root         string
	RootText     string
	AuthorName   string
	ReplyCount   int
	Unread       int
	LastReply    string
	URL          string
}

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
	for _, thread := range page.Threads {
		data.Threads = append(data.Threads, followedThreadView{
			Conversation: string(thread.Conversation),
			ChannelName:  thread.ConversationName,
			Root:         string(thread.Root),
			RootText:     thread.RootText,
			AuthorName:   names.name(thread.RootAuthorID),
			ReplyCount:   thread.ReplyCount,
			Unread:       thread.UnreadReplies,
			LastReply:    thread.LastReplyAt.UTC().Format("Jan 2, 15:04"),
			URL:          appURL(string(thread.Conversation), string(thread.Root), "", "", ""),
		})
	}
	data.Empty = len(data.Threads) == 0
	h.writeHTML(w, threadsTemplate, data, http.StatusOK, "Threads rendering unavailable")
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
		domain.PageRequest{Limit: limit, Descending: true})
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
	if conversation.IsDirect || conversation.IsGroupDirect {
		return "@"
	}
	return "#"
}

var threadsTemplate = mustPage(threadsMarkup)

const threadsMarkup = `{{define "title"}}Threads · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(900px,calc(100% - 32px));margin:28px auto 48px}.heading{display:grid;gap:5px;margin-bottom:17px}.heading h2,.heading p{margin:0}.heading p{color:var(--muted)}
.thread-list{display:grid;gap:10px;margin:0;padding:0;list-style:none}
.thread-item{display:grid;gap:9px;padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}
.thread-source{display:flex;align-items:center;gap:9px;flex-wrap:wrap}.thread-source a{font-weight:800;color:var(--text);text-decoration:none;min-height:24px;display:inline-flex;align-items:center}.thread-source a:hover{color:var(--action)}
.thread-meta{color:var(--muted);font-size:12px}
.thread-unread{border-radius:9px;background:var(--action);color:var(--on-strong);font-size:11px;font-weight:800;padding:2px 8px;min-height:20px;display:inline-flex;align-items:center}
.thread-text{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}
.empty{padding:30px;border:1px dashed var(--line);border-radius:10px;color:var(--muted);text-align:center}
@media(max-width:600px){.bar{padding:0 12px}.layout{width:min(100% - 20px,900px);margin-top:18px}}
</style>{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><h1>Threads</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
<div class="heading"><h2>Threads</h2><p>Threads you follow, most recently replied first.</p></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .Empty}}<p class="empty">You are not following any threads yet. Replying in a thread, or being mentioned in one, starts following it.</p>
{{else}}<ul class="thread-list" aria-label="Followed threads">{{range .Threads}}
  <li class="thread-item">
    <div class="thread-source">
      <a href="{{.URL}}">#{{.ChannelName}}</a>
      <span class="thread-meta">{{.AuthorName}} · {{.ReplyCount}} replies · last reply {{.LastReply}}</span>
      {{if .Unread}}<span class="thread-unread">{{.Unread}} unread</span>{{end}}
    </div>
    <p class="thread-text">{{.RootText}}</p>
  </li>{{end}}
</ul>{{end}}
</main>{{end}}`

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
