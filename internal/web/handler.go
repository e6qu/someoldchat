package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type Handler struct {
	Messages        chatapi.Service
	Authenticator   auth.Authenticator
	SessionRevoker  auth.SessionRevoker
	Channel         domain.ConversationID
	CookieDomain    string
	Login           *LoginHandler
	ReleaseRevision string
}

var immutableReleaseRevision = regexp.MustCompile(`^[0-9a-f]{12,64}$|^sha256:[0-9a-f]{64}$`)

// ValidateReleaseRevision decides, without constructing anything, whether a
// release identity is acceptable. It is exported so a process can settle the
// question inside its configuration contract — cmd/server's -check-config
// accepted `-release-revision not-a-commit` and the real start then exited 2 on
// it, which is precisely the drift scripts/check-terraform-module-startup.sh
// treats -check-config as authoritative against.
func ValidateReleaseRevision(revision string) error {
	if !immutableReleaseRevision.MatchString(strings.TrimSpace(revision)) {
		return errors.New("web release revision must be an immutable commit or image digest")
	}
	return nil
}

func (h *Handler) SetReleaseRevision(revision string) error {
	if err := ValidateReleaseRevision(revision); err != nil {
		return err
	}
	h.ReleaseRevision = strings.TrimSpace(revision)
	return nil
}

func NewHandler(messages chatapi.Service, authenticator auth.Authenticator, sessionRevoker auth.SessionRevoker, channel, cookieDomain string) (Handler, error) {
	if messages == nil {
		return Handler{}, errors.New("web requires a chat service")
	}
	if authenticator == nil {
		return Handler{}, errors.New("web requires an authenticator")
	}
	if sessionRevoker == nil {
		return Handler{}, errors.New("web requires a session revoker")
	}
	if channel == "" {
		return Handler{}, errors.New("web requires a channel")
	}
	if err := auth.ValidateSessionCookieDomain(cookieDomain); err != nil {
		return Handler{}, err
	}
	return Handler{Messages: messages, Authenticator: authenticator, SessionRevoker: sessionRevoker, Channel: domain.ConversationID(channel), CookieDomain: strings.TrimSpace(cookieDomain)}, nil
}

const (
	// timelineWindow is how many messages one timeline view renders.
	timelineWindow = 50
	// conversationWindow bounds the sidebar; the remainder is reachable through
	// the sidebar pager.
	conversationWindow = 50
	// directNameWindow bounds how many direct conversations resolve participant
	// names, so a large sidebar cannot turn into an unbounded member lookup.
	directNameWindow = 20
	searchWindow     = 25
	memberWindow     = 100
	// reactionWindow bounds the reactions rendered for one message.
	reactionWindow = 50
	// pinWindow bounds the pins loaded for one conversation view.
	pinWindow = 100
)

// liveEventTopics are the durable event topics the workspace page reacts to.
// They have to match the topics the chat service actually publishes
// (internal/service/messages.go): an edit is published as "message.changed",
// and no component publishes "message.updated".
var liveEventTopics = []string{
	"message.created",
	"message.changed",
	"message.deleted",
	"message.unfurled",
	"reaction.added",
	"reaction.removed",
	"pin.added",
	"pin.removed",
}

// ---------------------------------------------------------------------------
// View models
// ---------------------------------------------------------------------------

// messageList is the only type the "messages" partial is ever invoked with. The
// main timeline, the thread pane and both mutation fragments render through it,
// so the partial cannot be reached with a value that is missing a field it
// needs: that mismatch is what made every thread view fail to render.
type messageList struct {
	Messages    []messageView
	ChannelName string
	CSRFToken   string
	CanReact    bool
	CanPin      bool
}

type messageView struct {
	ID            string
	Anchor        string
	AuthorName    string
	AuthorInitial string
	Text          string
	MachineTime   string
	DisplayTime   string
	Pinned        bool
	Reactions     []reactionView
	ReplyURL      string
	ReactionURL   string
	UnreactURL    string
	PinURL        string
	UnpinURL      string
	Permalink     string
	Channel       string
	ChannelName   string
}

type reactionView struct {
	Name  string
	Count int
	Mine  bool
}

type conversationView struct {
	ID          string
	Name        string
	Current     bool
	UnreadCount int
}

type pageData struct {
	Timeline          messageList
	Thread            messageList
	ThreadTimestamp   string
	Channels          []conversationView
	Directs           []conversationView
	MoreChannelsURL   string
	Channel           string
	ChannelName       string
	ChannelMeta       string
	CSRFToken         string
	ShowProfile       bool
	ShowAdmin         bool
	Username          string
	UserInitial       string
	OlderURL          string
	LatestURL         string
	MarkReadURL       string
	MarkReadTimestamp string
	// NewestURL is set when the rendered window is not the newest one, so a
	// post made while reading older history can take the reader to where the
	// message actually landed instead of refreshing a window that cannot hold it.
	NewestURL   string
	AtLatest    bool
	Notice      string
	Error       string
	Draft       string
	ComposeURL  string
	TimelineURL string
	ThreadURL   string
}

type memberView struct {
	ID       string
	Name     string
	RealName string
	Profile  domain.UserProfile
	IsSelf   bool
}

type membersData struct {
	Members        []memberView
	Profile        domain.UserProfile
	CSRFToken      string
	Error          string
	CanMessage     bool
	MoreMembersURL string
}

type searchData struct {
	Query    string
	Channel  string
	Messages []messageView
	Error    string
	MoreURL  string
	Searched bool
}

type identityData struct {
	Heading   string
	Username  string
	Email     string
	Role      string
	Release   string
	CSRFToken string
	AvatarURL string
	Avatar    string
}

type errorData struct {
	Heading string
	Message string
}

// ---------------------------------------------------------------------------
// Templates
//
// Every page is rendered from one layout with one palette and one theme
// bootstrap. The layout owns the document, the shared stylesheet and the theme
// script; each page contributes a "title", a "content" and optionally "styles"
// and "scripts". Nothing is patched into the rendered HTML afterwards, so a
// template edit can no longer take a page out of service.
// ---------------------------------------------------------------------------

// The palette carries three tokens that exist because a colour that reads well
// as text does not read well as a background:
//
//   - --on-strong is the text colour used on a --ok or --danger *background*.
//     The dark palette tunes --ok and --danger as foreground colours, so white
//     on them measured 2.31:1 (the Send button) and 1.96:1 (Sign out) — both
//     below SC 1.4.3. The dark value of --on-strong takes them to 7.8:1 and
//     9.2:1.
//   - --field-line is the border of a control, which SC 1.4.11 holds to 3:1.
//     --line is a decorative separator at 1.35:1 and cannot serve both.
//   - --focus-chrome is the focus ring over the purple topbar and sidebar,
//     where --focus measures 1.65:1 — and where every primary control lives.
const lightTokens = `color-scheme:light;--bg:#fff;--panel:#f7f5f8;--panel-strong:#fff;--text:#1d1c1d;--muted:#5b565c;--line:#d9d4da;--field-line:#6b6570;--accent:#611f69;--on-accent:#fff;--on-strong:#fff;--action:#5c1a64;--hover:#f1edf2;--focus:#0b5cad;--focus-chrome:#fff;--danger:#a01133;--danger-bg:#fdeef1;--ok:#0a6b4f;--shadow:0 8px 24px #1d1c1d1f`

const darkTokens = `color-scheme:dark;--bg:#1a1d21;--panel:#222529;--panel-strong:#1e2125;--text:#e9e7ea;--muted:#aca7ae;--line:#3b3f45;--field-line:#8a8f96;--accent:#4a1750;--on-accent:#fff;--on-strong:#141719;--action:#8fd7f4;--hover:#2c3035;--focus:#7cc4ff;--focus-chrome:#fff;--danger:#ff9db4;--danger-bg:#3a1622;--ok:#3fbf95;--shadow:0 8px 24px #0006`

const sharedStyle = `*{box-sizing:border-box}
:root{` + lightTokens + `}
html[data-theme=dark]{` + darkTokens + `}
@media(prefers-color-scheme:dark){html[data-theme=light]:not([data-theme-explicit]){` + darkTokens + `}}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
button,input,textarea{font:inherit}
button{cursor:pointer}
a{color:var(--action)}
:focus-visible{outline:3px solid var(--focus);outline-offset:2px}
.topbar :focus-visible,.sidebar :focus-visible,.bar :focus-visible{outline-color:var(--focus-chrome)}
.visually-hidden{position:absolute;width:1px;height:1px;margin:-1px;padding:0;overflow:hidden;clip:rect(0 0 0 0);clip-path:inset(50%);white-space:nowrap;border:0}
.skip-link{position:absolute;left:8px;top:-48px;z-index:9;background:var(--panel-strong);color:var(--action);border:1px solid var(--line);border-radius:0 0 6px 6px;padding:8px 12px;text-decoration:none}
.skip-link:focus{top:0}
.notice{margin:0;padding:8px 12px;background:var(--panel);border:1px solid var(--line);border-radius:6px;color:var(--text);font-size:13px}
.form-error{margin:0 0 10px;padding:10px 12px;background:var(--danger-bg);border:1px solid var(--danger);border-radius:6px;color:var(--danger);font-weight:700}
.theme-toggle{border:1px solid #ffffffb8;border-radius:5px;color:inherit;background:transparent;padding:6px 9px}
.theme-toggle:hover{background:#ffffff2b}
.theme-toggle[aria-pressed=true]{background:#ffffff42}
.pager{margin:0;padding:6px 0;text-align:center;font-size:13px}
@media(prefers-reduced-motion:reduce){*{animation-duration:.01ms !important;animation-iteration-count:1 !important;transition-duration:.01ms !important;scroll-behavior:auto !important}}`

// themeBootstrap resolves the theme before the first paint, so a stored or
// operating-system dark preference never flashes the light palette.
const themeBootstrap = `<script>(function(){var root=document.documentElement;var dark=false;try{var stored=localStorage.getItem('sameoldchat-theme');dark=stored?stored==='dark':!!(window.matchMedia&&window.matchMedia('(prefers-color-scheme: dark)').matches)}catch(error){dark=false}root.setAttribute('data-theme',dark?'dark':'light');root.setAttribute('data-theme-explicit','')})();</script>`

const themeToggleScript = `<script>(function(){var root=document.documentElement;var toggle=document.getElementById('theme-toggle');function apply(theme){root.setAttribute('data-theme',theme);root.setAttribute('data-theme-explicit','');if(toggle)toggle.setAttribute('aria-pressed',theme==='dark'?'true':'false')}apply(root.getAttribute('data-theme')==='dark'?'dark':'light');if(!toggle)return;toggle.addEventListener('click',function(){var next=root.getAttribute('data-theme')==='dark'?'light':'dark';apply(next);try{localStorage.setItem('sameoldchat-theme',next)}catch(error){}})})();</script>`

const layoutMarkup = `<!doctype html>
<html lang="en" data-theme="light"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{template "title" .}}</title><style>` + sharedStyle + `</style>{{block "styles" .}}{{end}}` + themeBootstrap + `</head><body>{{template "content" .}}` + themeToggleScript + `{{block "scripts" .}}{{end}}</body></html>`

var layoutTemplate = template.Must(template.New("layout").Parse(layoutMarkup))

func mustPage(markup string) *template.Template {
	return template.Must(template.Must(layoutTemplate.Clone()).Parse(markup))
}

const pageStyle = `<style>
.shell{height:100vh;display:grid;grid-template-rows:52px minmax(0,1fr)}
.topbar{background:var(--accent);color:var(--on-accent);display:flex;align-items:center;gap:12px;padding:0 16px;box-shadow:var(--shadow)}
.brand{font-weight:800;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.search{flex:1 1 auto;min-width:0;max-width:560px;margin:auto;display:flex;align-items:center;gap:8px;background:#ffffff2b;border:1px solid #ffffff8a;border-radius:7px;padding:4px 10px}
.search input[name=q]{flex:1 1 auto;min-width:0;border:0;outline:0;background:transparent;color:var(--on-accent)}
.search input[name=q]::placeholder{color:#ffffffd6}
.search-submit{border:0;background:transparent;color:var(--on-accent);font-weight:700;padding:2px 2px}
.top-actions{display:flex;align-items:center;gap:8px;margin-left:auto;flex:0 0 auto}
.icon-button{border:0;background:transparent;color:var(--on-accent);border-radius:6px;padding:7px 9px;text-decoration:none}
.icon-button:hover{background:#ffffff2b}
.workspace{display:grid;grid-template-columns:256px minmax(0,1fr);min-height:0}
.sidebar{background:var(--accent);color:var(--on-accent);padding:16px 10px;display:flex;flex-direction:column;gap:14px;overflow:auto}
.workspace-name{font-weight:800;padding:0 10px}
.workspace-sub{color:#e8cbe9;font-size:12px;padding:2px 10px}
.side-section{display:grid;gap:2px}
.side-label{color:#e8cbe9;font-size:12px;font-weight:700;padding:6px 10px;text-transform:uppercase;letter-spacing:.06em}
.side-link{display:flex;align-items:center;gap:9px;width:100%;padding:7px 10px;border:0;border-radius:5px;background:transparent;color:var(--on-accent);font:inherit;text-align:left;text-decoration:none}
.side-link:hover,.side-link[aria-current=page]{background:#ffffff2b}
.side-link[aria-current=page]{font-weight:700}
.side-icon{flex:0 0 auto;display:inline-block;min-width:1em;text-align:center}
.side-text{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.side-empty{margin:0;padding:6px 10px;color:#e8cbe9;font-size:13px}
.side-more{padding:6px 10px;color:var(--on-accent);font-size:13px}
.badge{margin-left:auto;background:var(--on-accent);color:var(--accent);border-radius:12px;min-width:20px;text-align:center;padding:1px 6px;font-size:12px;font-weight:800}
.sidebar-bottom{margin-top:auto;border-top:1px solid #ffffff5c;padding-top:12px}
.signed-in{display:flex;align-items:center;gap:9px;padding:4px 10px 10px;min-width:0}
.signed-in-avatar{flex:0 0 auto;width:24px;height:24px;border-radius:5px;display:grid;place-items:center;background:#ffffff42;font-size:11px;font-weight:800;text-transform:uppercase;overflow:hidden}
.signed-in-name{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:700}
.content{min-width:0;min-height:0;display:grid;grid-template-rows:auto minmax(0,1fr) auto;{{if .ThreadTimestamp}}grid-template-columns:minmax(0,1fr) minmax(0,360px);grid-template-areas:"head thread" "timeline thread" "composer thread"{{else}}grid-template-columns:minmax(0,1fr);grid-template-areas:"head" "timeline" "composer"{{end}}}
.channel-header{grid-area:head;display:flex;align-items:center;gap:16px;flex-wrap:wrap;border-bottom:1px solid var(--line);padding:12px 26px}
.channel-title{margin:0;font-size:18px;font-weight:800}
.channel-meta{margin:2px 0 0;color:var(--muted);font-size:13px}
.channel-actions{margin-left:auto;display:flex;align-items:center;gap:12px;font-size:13px}
.timeline-wrap{grid-area:timeline;min-height:0;display:grid;grid-template-rows:auto minmax(0,1fr) auto}
.pager-older{grid-row:1}
.timeline{grid-row:2;overflow:auto;padding:18px 26px 12px;scroll-behavior:smooth}
.pager-newer{grid-row:3}
.message{display:grid;grid-template-columns:38px minmax(0,1fr);gap:10px;padding:10px 8px;border-radius:7px}
.message:hover{background:var(--hover)}
.message:target{background:var(--hover);outline:2px solid var(--focus)}
.avatar{height:36px;width:36px;border-radius:6px;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;display:grid;place-items:center;font-weight:800;font-size:15px;text-transform:uppercase;overflow:hidden}
.message-body{min-width:0}
.message-head{display:flex;align-items:baseline;gap:8px;flex-wrap:wrap}
.author{font-weight:800}
.time{color:var(--muted);font-size:12px}
.pinned{color:var(--muted);font-size:12px;font-weight:700}
.message-text{margin:2px 0 6px;white-space:pre-wrap;overflow-wrap:anywhere}
.reactions{display:flex;flex-wrap:wrap;gap:6px;margin:0 0 6px;padding:0;list-style:none}
.chip{display:inline-flex;gap:5px;align-items:center;border:1px solid var(--field-line);border-radius:12px;background:var(--panel);color:var(--text);padding:1px 9px;font-size:12px}
.chip[aria-pressed=true]{border-color:var(--action);font-weight:800}
.chip-count{font-variant-numeric:tabular-nums;font-weight:700}
.message-actions{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.message-actions a,.message-actions button{color:var(--muted);background:transparent;border:0;padding:2px 0;text-decoration:none;font-size:12px}
.message-actions a:hover,.message-actions button:hover{color:var(--action);text-decoration:underline}
.inline-form{display:inline-flex;gap:6px;align-items:center}
.inline-form input[type=text]{width:130px;border:1px solid var(--field-line);border-radius:4px;background:var(--panel-strong);color:var(--text);padding:3px 6px}
.empty{color:var(--muted);padding:26px;text-align:center}
.composer-wrap{grid-area:composer;padding:8px 26px 18px}
.live-status{margin:0 0 6px;min-height:18px;color:var(--muted);font-size:12px}
.composer{border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow);padding:10px}
.composer.is-error{border-color:var(--danger)}
.composer textarea{width:100%;min-height:44px;resize:vertical;border:0;outline:0;background:transparent;color:var(--text)}
.composer-footer{display:flex;justify-content:space-between;align-items:center;gap:12px}
.composer-tools{margin:0;color:var(--muted);font-size:13px}
.send{border:0;border-radius:5px;background:var(--ok);color:var(--on-strong);font-weight:700;padding:7px 14px}
.thread{grid-area:thread;min-height:0;border-left:1px solid var(--line);background:var(--panel);padding:16px 18px;overflow:auto}
.thread h2{margin:0 0 12px;font-size:16px}
@media(max-width:800px){
.workspace{grid-template-columns:64px minmax(0,1fr)}
.sidebar{padding:16px 6px}
.workspace-name,.workspace-sub,.side-label,.side-text,.signed-in-name{display:none}
.side-link{justify-content:center;padding:9px 4px}
.side-more{padding:6px 2px;text-align:center;font-size:11px}
.side-icon{font-size:18px}
.search{max-width:none}
.topbar{padding:0 8px;gap:8px}
.timeline,.composer-wrap,.channel-header{padding-left:12px;padding-right:12px}
{{if .ThreadTimestamp}}.content{grid-template-columns:minmax(0,1fr);grid-template-areas:"head" "thread" "composer"}
.timeline-wrap{display:none}
.thread{border-left:0;border-top:1px solid var(--line)}{{end}}
}
</style>`

const messagesPartial = `{{define "messages"}}{{range $message := .Messages}}<article class="message" id="{{$message.Anchor}}" data-message-id="{{$message.ID}}"><div class="avatar" aria-hidden="true">{{$message.AuthorInitial}}</div><div class="message-body"><div class="message-head"><span class="author">{{$message.AuthorName}}</span><time class="time" datetime="{{$message.MachineTime}}">{{$message.DisplayTime}}</time>{{if $message.Pinned}}<span class="pinned">Pinned</span>{{end}}</div><p class="message-text">{{$message.Text}}</p>{{if $message.Reactions}}<ul class="reactions">{{range $reaction := $message.Reactions}}<li>{{if $.CanReact}}<form class="inline-form" method="post" action="{{if $reaction.Mine}}{{$message.UnreactURL}}{{else}}{{$message.ReactionURL}}{{end}}" hx-post="{{if $reaction.Mine}}{{$message.UnreactURL}}{{else}}{{$message.ReactionURL}}{{end}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="name" value="{{$reaction.Name}}"><button class="chip" type="submit" aria-pressed="{{if $reaction.Mine}}true{{else}}false{{end}}" aria-label="{{if $reaction.Mine}}Remove your {{$reaction.Name}} reaction{{else}}React with {{$reaction.Name}}{{end}}, {{$reaction.Count}} so far">{{$reaction.Name}} <span class="chip-count">{{$reaction.Count}}</span></button></form>{{else}}<span class="chip" role="img" aria-label="{{$reaction.Name}}, {{$reaction.Count}} reactions">{{$reaction.Name}} <span class="chip-count">{{$reaction.Count}}</span></span>{{end}}</li>{{end}}</ul>{{end}}<div class="message-actions"><a href="{{$message.ReplyURL}}">Reply in thread</a>{{if $.CanReact}}<form class="inline-form" aria-label="Add reaction" method="post" action="{{$message.ReactionURL}}" hx-post="{{$message.ReactionURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><label class="visually-hidden" for="reaction-{{$message.ID}}">Add a reaction to the message from {{$message.AuthorName}}</label><input id="reaction-{{$message.ID}}" type="text" name="name" maxlength="255" placeholder=":wave:" required><button type="submit">Add</button></form>{{end}}{{if $.CanPin}}<form method="post" action="{{if $message.Pinned}}{{$message.UnpinURL}}{{else}}{{$message.PinURL}}{{end}}" hx-post="{{if $message.Pinned}}{{$message.UnpinURL}}{{else}}{{$message.PinURL}}{{end}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">{{if $message.Pinned}}Unpin{{else}}Pin{{end}}</button></form>{{end}}</div></div></article>{{else}}<p class="empty">No messages yet. Start the conversation.</p>{{end}}{{end}}`

var pageMarkup = `{{define "title"}}#{{.ChannelName}} · SameOldChat{{end}}
{{define "styles"}}` + pageStyle + `{{end}}
{{define "scripts"}}` + progressiveEnhancementScript + `{{end}}
{{define "content"}}<a class="skip-link" href="#timeline">Skip to the messages</a><div class="shell"><header class="topbar"><span class="brand">SameOldChat</span><form class="search" method="get" action="/app/search" role="search" aria-label="Search the workspace"><span aria-hidden="true">⌕</span><label class="visually-hidden" for="workspace-search">Search the workspace</label><input id="workspace-search" type="search" name="q" maxlength="500" placeholder="Search the workspace" required><button class="search-submit" type="submit">Search</button><input type="hidden" name="channel" value="{{.Channel}}"></form><div class="top-actions"><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button>{{if .ShowProfile}}<a class="icon-button" href="/me" aria-label="My profile">●</a>{{end}}</div></header><div class="workspace"><aside class="sidebar"><div><div class="workspace-name">SameOldChat</div><div class="workspace-sub">Workspace</div></div><nav class="side-section" aria-label="Workspace navigation"><div class="side-label">Workspace</div><a class="side-link" href="/app/members" aria-label="Members"><span class="side-icon" aria-hidden="true">☰</span><span class="side-text">Members</span></a>{{if .ShowAdmin}}<a class="side-link" href="/app/admin/auth" aria-label="Authorization"><span class="side-icon" aria-hidden="true">⚙</span><span class="side-text">Authorization</span></a>{{end}}</nav><nav class="side-section" aria-label="Channels"><div class="side-label">Channels</div>{{range .Channels}}<a class="side-link" href="/app?channel={{.ID}}"{{if .Current}} aria-current="page"{{end}} aria-label="{{.Name}}{{if .UnreadCount}}, {{.UnreadCount}} unread messages{{end}}"><span class="side-icon" aria-hidden="true">#</span><span class="side-text">{{.Name}}</span>{{if .UnreadCount}}<span class="badge" aria-hidden="true">{{.UnreadCount}}</span>{{end}}</a>{{else}}<p class="side-empty">No channels available.</p>{{end}}</nav>{{if .Directs}}<nav class="side-section" aria-label="Direct messages"><div class="side-label">Direct messages</div>{{range .Directs}}<a class="side-link" href="/app?channel={{.ID}}"{{if .Current}} aria-current="page"{{end}} aria-label="{{.Name}}{{if .UnreadCount}}, {{.UnreadCount}} unread messages{{end}}"><span class="side-icon" aria-hidden="true">◍</span><span class="side-text">{{.Name}}</span>{{if .UnreadCount}}<span class="badge" aria-hidden="true">{{.UnreadCount}}</span>{{end}}</a>{{end}}</nav>{{end}}{{if .MoreChannelsURL}}<a class="side-more" href="{{.MoreChannelsURL}}">More conversations</a>{{end}}<div class="sidebar-bottom"><div class="signed-in" data-shauth-user="{{.Username}}"><span class="signed-in-avatar" aria-hidden="true">{{.UserInitial}}</span><span class="signed-in-name">{{.Username}}</span></div><form method="post" action="/app/session/revoke"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="side-link" type="submit" data-shauth-sign-out aria-label="Sign out"><span class="side-icon" aria-hidden="true">↪</span><span class="side-text">Sign out</span></button></form></div></aside><main class="content" id="content"><header class="channel-header"><div><h1 class="channel-title"># {{.ChannelName}}</h1><p class="channel-meta">{{.ChannelMeta}}</p></div><div class="channel-actions">{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}{{if .MarkReadURL}}<form class="inline-form" id="mark-read" method="post" action="{{.MarkReadURL}}" hx-post="{{.MarkReadURL}}" data-quiet="true"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="ts" value="{{.MarkReadTimestamp}}"><button type="submit">Mark as read</button></form>{{end}}{{if .ThreadTimestamp}}<a href="/app?channel={{.Channel}}">Back to the channel</a>{{end}}</div></header><div class="timeline-wrap">{{if .OlderURL}}<p class="pager pager-older"><a href="{{.OlderURL}}">Show older messages</a></p>{{end}}<section id="timeline" class="timeline" tabindex="-1" aria-label="Messages" data-fragment="{{.TimelineURL}}" data-live="{{if .AtLatest}}true{{else}}false{{end}}">{{template "messages" .Timeline}}</section>{{if .LatestURL}}<p class="pager pager-newer"><a href="{{.LatestURL}}">Jump to the latest messages</a></p>{{end}}</div>{{if .ThreadTimestamp}}<aside class="thread" aria-labelledby="thread-heading"><h2 id="thread-heading">Thread</h2><div id="thread-messages" tabindex="-1" data-fragment="{{.ThreadURL}}" data-live="true">{{template "messages" .Thread}}</div></aside>{{end}}<div class="composer-wrap"><p class="live-status" id="live-status" role="status" aria-live="polite"></p><form class="composer{{if .Error}} is-error{{end}}" id="composer" method="post" action="{{.ComposeURL}}" hx-post="{{.ComposeURL}}" hx-target="{{if .ThreadTimestamp}}#thread-messages{{else}}#timeline{{end}}" data-newest="{{.NewestURL}}"><p class="form-error" id="composer-error" role="alert" tabindex="-1"{{if .Error}} autofocus{{end}}{{if not .Error}} hidden{{end}}>{{.Error}}</p><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label class="visually-hidden" for="text">{{if .ThreadTimestamp}}Reply in the thread{{else}}Message #{{.ChannelName}}{{end}}</label><textarea id="text" name="text" required{{if not .Error}} autofocus{{end}} aria-describedby="composer-hint" placeholder="{{if .ThreadTimestamp}}Reply in the thread{{else}}Message #{{.ChannelName}}{{end}}">{{.Draft}}</textarea>{{if .ThreadTimestamp}}<input type="hidden" name="thread_ts" value="{{.ThreadTimestamp}}"><p class="composer-tools">Replying in thread</p>{{end}}<div class="composer-footer"><span class="composer-tools" id="composer-hint">Enter to send · Shift+Enter for a new line</span><button class="send" type="submit">Send</button></div></form></div></main></div></div>{{end}}
` + messagesPartial

var pageTemplate = mustPage(pageMarkup)

const membersMarkup = `{{define "title"}}Members · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}
.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}
.bar .theme-toggle{margin-left:auto}
.layout{max-width:1100px;margin:0 auto;padding:28px 22px}
.heading{border-bottom:1px solid var(--line);padding-bottom:18px;margin-bottom:22px}
.heading h1{margin:0 0 4px;font-size:26px}
.muted{color:var(--muted)}
.grid{display:grid;grid-template-columns:minmax(280px,380px) minmax(0,1fr);gap:22px;align-items:start}
.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:20px}
.card h2{margin-top:0}
.field{display:grid;gap:5px;margin:12px 0}
.field input{width:100%;border:1px solid var(--field-line);border-radius:5px;background:var(--bg);color:var(--text);padding:9px}
.save{background:var(--ok);color:var(--on-strong);border:0;border-radius:5px;padding:9px 14px;font-weight:700}
.members{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px}
.person{background:var(--bg);border:1px solid var(--line);border-radius:8px;padding:14px}
.person h3{font-size:16px;margin:0}
.person p{margin:5px 0;color:var(--muted)}
.person button{border:1px solid var(--line);border-radius:5px;background:var(--panel);color:var(--text);padding:5px 10px}
@media(max-width:720px){.grid{grid-template-columns:minmax(0,1fr)}.layout{padding:20px 14px}}
</style>{{end}}
{{define "content"}}<header class="bar"><a href="/app">← Back to chat</a><span>Members</span><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout"><div class="heading"><h1>Workspace members</h1><p class="muted">Manage your profile and see who is here.</p></div><div class="grid"><section class="card" aria-labelledby="profile-heading"><h2 id="profile-heading">Edit profile</h2>{{if .Error}}<p class="form-error" role="alert">{{.Error}}</p>{{end}}<form method="post" action="/app/profile"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label class="field" for="display_name">Display name<input id="display_name" name="display_name" maxlength="80" value="{{.Profile.DisplayName}}" required></label><label class="field" for="status_text">Status<input id="status_text" name="status_text" maxlength="100" value="{{.Profile.StatusText}}"></label><label class="field" for="status_emoji">Status emoji<input id="status_emoji" name="status_emoji" maxlength="64" value="{{.Profile.StatusEmoji}}"></label><label class="field" for="image_24">Image 24 URL<input id="image_24" type="url" maxlength="2048" name="image_24" value="{{.Profile.Image24}}"></label><label class="field" for="image_32">Image 32 URL<input id="image_32" type="url" maxlength="2048" name="image_32" value="{{.Profile.Image32}}"></label><label class="field" for="image_48">Image 48 URL<input id="image_48" type="url" maxlength="2048" name="image_48" value="{{.Profile.Image48}}"></label><label class="field" for="image_72">Image 72 URL<input id="image_72" type="url" maxlength="2048" name="image_72" value="{{.Profile.Image72}}"></label><label class="field" for="image_192">Image 192 URL<input id="image_192" type="url" maxlength="2048" name="image_192" value="{{.Profile.Image192}}"></label><label class="field" for="image_512">Image 512 URL<input id="image_512" type="url" maxlength="2048" name="image_512" value="{{.Profile.Image512}}"></label><label class="field" for="image_1024">Image 1024 URL<input id="image_1024" type="url" maxlength="2048" name="image_1024" value="{{.Profile.Image1024}}"></label><button class="save" type="submit">Save profile</button></form></section><section class="card" aria-labelledby="people-heading"><h2 id="people-heading">People</h2><div class="members">{{range .Members}}<article class="person"><h3>{{.Name}}</h3><p>{{.RealName}}</p>{{if .Profile.DisplayName}}<p>{{.Profile.DisplayName}}</p>{{end}}{{if .Profile.StatusText}}<p>{{.Profile.StatusEmoji}} {{.Profile.StatusText}}</p>{{end}}{{if and $.CanMessage (not .IsSelf)}}<form method="post" action="/app/conversation/open"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="users" value="{{.ID}}"><button type="submit">Message {{.Name}}</button></form>{{end}}</article>{{else}}<p class="muted">No members available.</p>{{end}}</div>{{if .MoreMembersURL}}<p class="pager"><a href="{{.MoreMembersURL}}">Show more members</a></p>{{end}}</section></div></main>{{end}}`

var membersTemplate = mustPage(membersMarkup)

const searchMarkup = `{{define "title"}}Search · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}
.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}
.bar form{display:flex;flex:1 1 auto;min-width:0;max-width:600px;margin:auto;gap:8px}
.bar input{flex:1 1 auto;min-width:0;border:1px solid #ffffff8a;border-radius:5px;padding:8px 10px;background:#ffffff2b;color:var(--on-accent)}
.bar input::placeholder{color:#ffffffd6}
.bar button{border:1px solid #ffffff6b;background:transparent;color:var(--on-accent);border-radius:5px;padding:6px 10px}
.layout{max-width:900px;margin:0 auto;padding:28px 22px}
.heading{border-bottom:1px solid var(--line);padding-bottom:18px;margin-bottom:22px}
.heading h1{margin:0 0 4px;font-size:26px}
.muted{color:var(--muted)}
.results{display:grid;gap:8px}
.result{display:block;padding:14px;background:var(--panel);border:1px solid var(--line);border-radius:8px;color:inherit;text-decoration:none}
.result:hover{border-color:var(--action)}
.author{font-weight:700}
.time{color:var(--muted);font-size:12px;margin-left:8px}
.channel{color:var(--muted);font-size:12px;margin-left:8px}
.text{margin:6px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}
.empty{color:var(--muted);padding:22px;text-align:center}
@media(max-width:720px){.layout{padding:20px 14px}.bar{padding:0 12px;gap:10px}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><form method="get" action="/app/search" role="search" aria-label="Search the workspace"><label class="visually-hidden" for="search-query">Search messages</label><input id="search-query" type="search" name="q" maxlength="500" value="{{.Query}}" placeholder="Search messages" required><button type="submit">Search</button><input type="hidden" name="channel" value="{{.Channel}}"></form><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout"><div class="heading"><h1>Search results</h1>{{if .Error}}<p class="form-error" role="alert">{{.Error}}</p>{{else if .Searched}}<p class="muted">Messages matching “{{.Query}}”</p>{{else}}<p class="muted">Enter a search term to find messages.</p>{{end}}</div><section class="results" aria-label="Results">{{range .Messages}}<a class="result" href="{{.Permalink}}"><span class="author">{{.AuthorName}}</span><time class="time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time><span class="channel">#{{.ChannelName}}</span><p class="text">{{.Text}}</p></a>{{else}}{{if .Searched}}<p class="empty">No matching messages.</p>{{end}}{{end}}</section>{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Show more results</a></p>{{end}}</main>{{end}}`

var searchTemplate = mustPage(searchMarkup)

const identityMarkup = `{{define "title"}}{{.Heading}} · SameOldChat{{end}}
{{define "styles"}}<style>
body{min-height:100vh}
.bar{display:flex;align-items:center;gap:16px;padding:14px 22px;background:var(--panel);border-bottom:1px solid var(--line)}
.bar a{color:var(--action);font-weight:700;text-decoration:none}
.bar .theme-toggle{margin-left:auto;border-color:var(--line);color:var(--text)}
.bar .theme-toggle:hover{background:var(--hover)}
.layout{width:min(760px,calc(100% - 32px));margin:40px auto}
.card{padding:28px;background:var(--panel);border:1px solid var(--line);border-radius:16px;box-shadow:var(--shadow)}
.identity-heading{display:grid;grid-template-columns:72px minmax(0,1fr);align-items:center;gap:18px;margin-bottom:8px}
.avatar{width:72px;height:72px;border-radius:16px;display:grid;place-items:center;background:linear-gradient(135deg,var(--accent),#2f7f9c);color:#fff;font-size:1.8rem;font-weight:800;object-fit:cover;overflow:hidden;text-transform:uppercase}
h1{margin:0;font-size:clamp(1.8rem,5vw,2.8rem);line-height:1.1}
.lede{margin:0 0 24px;color:var(--muted)}
dl{display:grid;grid-template-columns:minmax(120px,180px) minmax(0,1fr);margin:0 0 24px;border-top:1px solid var(--line)}
dt,dd{margin:0;padding:12px 0;border-bottom:1px solid var(--line)}
dt{color:var(--muted);font-weight:700}
dd{overflow-wrap:anywhere}
.button{border:0;border-radius:8px;padding:11px 17px;background:var(--danger);color:var(--on-strong);font-weight:800}
@media(max-width:540px){.layout{margin:22px auto}.card{padding:20px}.identity-heading{grid-template-columns:56px minmax(0,1fr)}.avatar{width:56px;height:56px;border-radius:12px}dl{grid-template-columns:minmax(0,1fr)}dt{padding-bottom:0;border-bottom:0}dd{padding-top:3px}}
</style>{{end}}
{{define "content"}}<header class="bar"><strong>SameOldChat</strong><a href="/app">Back to chat</a><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout"><section class="card" aria-labelledby="identity-heading"><div class="identity-heading">{{if .AvatarURL}}<img class="avatar" src="{{.AvatarURL}}" alt="Avatar for {{.Username}}">{{else}}<span class="avatar" role="img" aria-label="Avatar for {{.Username}}">{{.Avatar}}</span>{{end}}<h1 id="identity-heading">{{.Heading}}</h1></div><p class="lede">Your verified Shauth identity and this immutable application release.</p><dl><dt>Username</dt><dd data-testid="validation-username">{{.Username}}</dd><dt>Email</dt><dd data-testid="validation-email">{{.Email}}</dd><dt>Role</dt><dd data-testid="validation-role">{{.Role}}</dd><dt>Release</dt><dd data-testid="validation-release">{{.Release}}</dd></dl><form method="post" action="/logout"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="button" type="submit">Sign out</button></form></section></main>{{end}}`

var identityTemplate = mustPage(identityMarkup)

const errorMarkup = `{{define "title"}}{{.Heading}} · SameOldChat{{end}}
{{define "styles"}}<style>
.layout{width:min(640px,calc(100% - 32px));margin:64px auto;padding:26px;background:var(--panel);border:1px solid var(--line);border-radius:12px}
h1{margin:0 0 10px;font-size:24px}
p{margin:0 0 18px;color:var(--muted)}
a{font-weight:700}
</style>{{end}}
{{define "content"}}<main class="layout"><h1>{{.Heading}}</h1><p>{{.Message}}</p><a href="/app">Back to chat</a></main>{{end}}`

var errorTemplate = mustPage(errorMarkup)

// localTimeScript renders machine timestamps in the reader's own locale and
// zone. The server keeps the machine value in datetime= so the page is still
// readable without JavaScript.
const localTimeScript = `<script>(function(){window.sameoldchatLocalTimes=function(root){if(!root||!window.Intl)return;var nodes=root.querySelectorAll('time[datetime]');for(var index=0;index<nodes.length;index++){var value=new Date(nodes[index].getAttribute('datetime'));if(isNaN(value.getTime()))continue;nodes[index].textContent=value.toLocaleString(undefined,{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'})}};window.sameoldchatLocalTimes(document)})();</script>`

// progressiveEnhancementScript is the whole client budget for the workspace
// page: submit forms without losing the page, keep the composer usable, keep
// the live stream open, and re-render the message regions the server owns.
//
// Four properties are load-bearing and each replaced a defect:
//
//   - bursts collapse into one refresh, and a refresh aborts the one before it
//     and drops any response that lands after a newer one started. Ten events
//     used to issue ten concurrent full-conversation scans per open tab, whose
//     responses could land out of order and visibly revert the timeline.
//   - a submit takes a lock and disables its button, and success clears only
//     the exact text that was sent. Holding Enter used to post twice, and the
//     response used to reset the form over whatever was typed while it was in
//     flight.
//   - the stream reports its own failure. A 401 closes an EventSource
//     permanently, and the page used to keep looking live forever.
//   - every URL the client fetches must be a path on this origin. The values
//     come from mutationURL/fragmentURL today, but nothing else in the document
//     enforced it, and these fetches carry credentials.
//
// Two behaviours in it are not obvious from the code. `refresh(force)` is
// forced only for a change the reader themselves made: it then re-renders every
// message region, including one that is not following the live conversation,
// and does not step aside for focus, because the reader is waiting. A refresh
// caused by somebody else's event leaves a focused region alone. And a
// successful post from a window that is not the newest one navigates to the
// newest window instead of appending, because the message it just stored cannot
// appear in the window on screen — which is how a sent message used to flash up
// and then vanish.
//
// The script carries no JavaScript comments on purpose: html/template elides
// them when it renders a script context, so the bytes the browser receives
// would no longer match the Content-Security-Policy hash computed from this
// constant, and the whole client would be silently blocked.
var progressiveEnhancementScript = localTimeScript + `<script>(function(){
var topics=` + liveEventTopicsLiteral() + `;
var composer=document.getElementById('composer');
var text=document.getElementById('text');
var errorBox=document.getElementById('composer-error');
var status=document.getElementById('live-status');
var generation=0;
var inFlight=null;
var scheduled=null;
var sending=false;
var streamState='';
function localize(root){if(window.sameoldchatLocalTimes)window.sameoldchatLocalTimes(root)}
function announce(message){if(status)status.textContent=message}
function showError(message){if(!errorBox){window.alert(message);return}errorBox.textContent=message;errorBox.hidden=false;if(composer)composer.classList.add('is-error');errorBox.scrollIntoView({block:'nearest'})}
function clearError(){if(!errorBox)return;errorBox.textContent='';errorBox.hidden=true;if(composer)composer.classList.remove('is-error')}
function failure(error){var message=error&&error.message?String(error.message).trim():'';if(message.charAt(0)==='<')message='';if(message.length>200)message=message.slice(0,200);return message||'The request could not be completed. Your message was kept in the composer.'}
function ownPath(value){return typeof value==='string'&&value.charAt(0)==='/'&&value.charAt(1)!=='/'}
function shown(region){return !!(region.offsetParent||region.getClientRects().length)}
function atBottom(region){return region.scrollHeight-region.scrollTop-region.clientHeight<48}
function toBottom(region){if(region)region.scrollTop=region.scrollHeight}
function regions(force){return document.querySelectorAll(force?'[data-fragment]':'[data-fragment][data-live="true"]')}
function messageCount(){return document.querySelectorAll('[data-fragment] .message').length}
function refresh(force){
generation++;
var token=generation;
if(inFlight){inFlight.abort();inFlight=null}
var controller=window.AbortController?new AbortController():null;
inFlight=controller;
var pending=[];
var live=regions(force);
for(var index=0;index<live.length;index++){(function(region){
var target=region.getAttribute('data-fragment');
if(!ownPath(target))return;
if(!shown(region))return;
var focused=!!(document.activeElement&&region.contains(document.activeElement));
if(focused&&!force)return;
var stick=atBottom(region);
var options={headers:{'HX-Request':'true'},credentials:'same-origin'};
if(controller)options.signal=controller.signal;
pending.push(fetch(target,options).then(function(response){if(!response.ok)throw new Error('The conversation could not be refreshed.');return response.text()}).then(function(html){if(token!==generation)return;region.innerHTML=html;localize(region);if(stick)toBottom(region);if(focused&&region.hasAttribute('tabindex'))region.focus()}));
})(live[index])}
return Promise.all(pending).then(function(){if(inFlight===controller)inFlight=null});
}
function scheduleRefresh(){
if(scheduled)return;
scheduled=window.setTimeout(function(){
scheduled=null;
var behind=document.querySelectorAll('[data-fragment]:not([data-live="true"])').length>0;
var before=messageCount();
refresh(false).then(function(){
var arrived=messageCount()-before;
if(arrived>0){announce(arrived===1?'1 new message.':arrived+' new messages.');return}
if(behind)announce('New activity is available in this conversation.');
}).catch(function(error){if(error&&error.name==='AbortError')return;announce('New activity could not be loaded. Reload the page.')});
},250);
}
function submitQuietly(form){
var action=form.getAttribute('hx-post');
if(!ownPath(action))return;
fetch(action,{method:'POST',body:new FormData(form),headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){if(response.ok)form.hidden=true}).catch(function(){});
}
document.addEventListener('submit',function(event){
var form=event.target.closest('form');
if(!form||!form.hasAttribute('hx-post'))return;
var action=form.getAttribute('hx-post');
if(!ownPath(action))return;
event.preventDefault();
if(form===composer){if(sending)return;sending=true}
var quiet=form.getAttribute('data-quiet')==='true';
var body=new FormData(form);
var sent=text?text.value:'';
var button=form.querySelector('button[type=submit]');
if(button)button.disabled=true;
var release=function(){if(button)button.disabled=false;if(form===composer)sending=false};
clearError();
fetch(action,{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){
if(!response.ok)return response.text().then(function(body){throw new Error(body)});
var redirect=response.headers.get('HX-Redirect');
if(redirect){if(ownPath(redirect))window.location.assign(redirect);return null}
if(response.status===204)return '';
return response.text();
}).then(function(html){
if(html===null)return null;
if(quiet){form.hidden=true;return null}
if(html===''){return refresh(true).then(function(){announce('The conversation was updated.')})}
var newest=form===composer?form.getAttribute('data-newest'):'';
if(newest&&ownPath(newest)){
window.location.assign(newest);
return null;
}
var target=document.querySelector(form.getAttribute('hx-target'));
if(!target)throw new Error('The page could not be updated. Reload to see the message.');
target.insertAdjacentHTML('beforeend',html);
localize(target);
if(form===composer&&text){if(text.value===sent)text.value='';text.focus()}else{form.reset()}
toBottom(target);
toBottom(document.getElementById('timeline'));
return refresh(true);
}).catch(function(error){showError(failure(error))}).then(release,release);
});
if(text&&composer){text.addEventListener('keydown',function(event){
if(event.key!=='Enter'||event.shiftKey||event.ctrlKey||event.metaKey||event.altKey||event.isComposing)return;
event.preventDefault();
if(sending)return;
if(typeof composer.requestSubmit==='function'){composer.requestSubmit();return}
composer.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));
})}
if(window.EventSource){
var cursor='';
try{cursor=sessionStorage.getItem('sameoldchat-last-event')||''}catch(error){cursor=''}
var stream=new EventSource('/events'+(cursor?'?last_event_id='+encodeURIComponent(cursor):''));
var deliver=function(event){
if(event.lastEventId){try{sessionStorage.setItem('sameoldchat-last-event',event.lastEventId)}catch(error){}}
var live=regions(false);
if(!live.length){announce('New activity is available in this conversation.');return}
scheduleRefresh();
};
stream.onopen=function(){if(streamState){streamState='';announce('Live updates resumed.')}};
stream.onerror=function(){
if(stream.readyState===EventSource.CLOSED){streamState='closed';announce('Live updates stopped. Reload the page to reconnect.');return}
streamState='connecting';
announce('Reconnecting to live updates…');
};
topics.forEach(function(topic){stream.addEventListener(topic,deliver)});
}
var markRead=document.getElementById('mark-read');
if(markRead)submitQuietly(markRead);
toBottom(document.getElementById('timeline'));
if(text)text.focus();
})();</script>`

func liveEventTopicsLiteral() string {
	quoted := make([]string, 0, len(liveEventTopics))
	for _, topic := range liveEventTopics {
		if strings.ContainsAny(topic, `'"\<>`) {
			panic("live event topic must be a plain identifier: " + topic)
		}
		quoted = append(quoted, "'"+topic+"'")
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /signed-out", signedOut)
	if h.Login != nil {
		h.Login.Register(mux)
		mux.HandleFunc("GET /auth/validation", h.validation)
		mux.HandleFunc("GET /me", h.me)
		mux.HandleFunc("GET /app/admin/auth", h.authAdminPage)
		mux.HandleFunc("GET /api/admin.auth.methods.list", h.authMethodsList)
		mux.HandleFunc("POST /api/admin.auth.methods.set", h.authMethodSet)
		mux.HandleFunc("POST /api/admin.auth.users.invite", h.authUserInvite)
		mux.HandleFunc("POST /api/admin.auth.users.create", h.authUserCreate)
		mux.HandleFunc("GET /api/admin.auth.users.list", h.authUsersList)
		mux.HandleFunc("POST /api/admin.auth.users.set", h.authUserSet)
	}
	mux.HandleFunc("GET /app", h.index)
	mux.HandleFunc("GET /app/timeline", h.timeline)
	mux.HandleFunc("POST /app/read", h.markRead)
	mux.HandleFunc("GET /app/search", h.search)
	mux.HandleFunc("GET /app/members", h.members)
	mux.HandleFunc("POST /app/profile", h.setProfile)
	mux.HandleFunc("POST /app/message", h.postMessage)
	mux.HandleFunc("POST /app/conversation/open", h.openConversation)
	mux.HandleFunc("POST /app/reaction", h.addReaction)
	mux.HandleFunc("POST /app/reaction/remove", h.removeReaction)
	mux.HandleFunc("POST /app/pin", h.addPin)
	mux.HandleFunc("POST /app/pin/remove", h.removePin)
	mux.HandleFunc("POST /app/session/revoke", h.revokeSession)
	mux.HandleFunc("POST /logout", h.revokeSession)
}

// requireCSRF is the mutation precondition, and it answers a person rather than
// a log line. The three failures are distinguishable and only one of them means
// "reload": a token that no longer matches the session is a page that has been
// open too long, a foreign origin is an attack, and no session is a sign-in.
func (h Handler) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	err := auth.ValidateCSRF(r)
	switch {
	case err == nil:
		return true
	case errors.Is(err, auth.ErrCSRFCrossSite):
		h.writeMutationError(w, r, http.StatusForbidden, "That request came from another site",
			"This request was not made from SameOldChat, so nothing was changed. Open the workspace and try again.")
	case errors.Is(err, auth.ErrNoToken):
		h.writeMutationError(w, r, http.StatusUnauthorized, "Sign in required",
			"Your session has ended, so nothing was changed. Sign in and try again.")
	default:
		h.writeMutationError(w, r, http.StatusForbidden, "This page has been open too long",
			"Reload the page and send it again. Nothing was changed.")
	}
	return false
}

// writeMutationError answers a refused mutation in the shape the caller can
// use. The enhanced client puts the sentence next to the control that failed;
// a browser without JavaScript has navigated to this response, so it gets the
// styled page with a way back instead of a bare line of text on a white page.
func (h Handler) writeMutationError(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		secureHeaders(w, workspaceContentSecurityPolicy)
		http.Error(w, message, status)
		return
	}
	h.writePageError(w, status, heading, message)
}

// ---------------------------------------------------------------------------
// Workspace page
// ---------------------------------------------------------------------------

// composerState carries a rejected submission back into the page so a failed
// post keeps the text the user typed and says what went wrong.
type composerState struct {
	Draft   string
	Message string
	Status  int
}

// historyReader is a principal that has been checked for
// auth.ScopeChannelsHistory. renderApp takes one instead of a bare principal,
// so the workspace render — the newest 50 messages, every author, the whole
// sidebar and a live CSRF token — cannot be reached from a handler that
// authenticated for a different scope. POST /app/message did exactly that: it
// gated on chat:write and then answered a rejected post with the full page, so
// a token that got 403 from GET /app read the conversation out of a 400 body.
type historyReader struct{ principal auth.Principal }

func requireHistoryReader(principal auth.Principal) (historyReader, error) {
	if !principal.HasScope(auth.ScopeChannelsHistory) {
		return historyReader{}, auth.ErrMissingScope
	}
	return historyReader{principal: principal}, nil
}

func (h Handler) index(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		if errors.Is(err, auth.ErrNotAuthenticated) && h.Login != nil {
			http.Redirect(w, r, h.signInTarget(r), http.StatusSeeOther)
			return
		}
		h.writeAuthError(w, err)
		return
	}
	reader, err := requireHistoryReader(principal)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	h.renderApp(w, r, reader, composerState{Status: http.StatusOK})
}

// markRead advances the reader's unread cursor.
//
// This is a durable write and therefore a POST with a CSRF check. GET /app used
// to perform it inline, on a channel named in the query string, with no token
// at all: `<img src="/app?channel=C…">` or a link in any message silently wiped
// a victim's unread state, because SameSite=Lax sends the session cookie on a
// top-level cross-site navigation. A safe method must not write.
func (h Handler) markRead(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The read marker could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(fields["ts"]))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The unread marker could not be moved because the link does not identify a message in this conversation.")
		return
	}
	if _, err := h.Messages.MarkRead(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp); err != nil {
		// Unread bookkeeping is not worth an error page: the reader has read the
		// conversation either way.
		if errors.Is(err, store.ErrNotFound) {
			h.writeMutationError(w, r, http.StatusNotFound, "That conversation is not available", "The unread marker could not be moved because that conversation is no longer available.")
			return
		}
		if errors.Is(err, service.ErrNotInConversation) {
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this conversation", "The unread marker is only kept for conversations you have joined.")
			return
		}
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Unread counts are temporarily unavailable", "The unread marker could not be moved. Nothing else was changed.")
		return
	}
	h.completeMutation(w, r)
}

// signInTarget starts the exact provider the deployment can complete. A
// configured provider that the workspace has disabled would answer the bare
// "authorization method is disabled" page, so entry falls back to the provider
// list in that case.
func (h Handler) signInTarget(r *http.Request) string {
	if h.Login == nil || !h.Login.hasOpenIDConnectProvider() {
		return "/login"
	}
	if method, err := h.Messages.GetAuthMethod(r.Context(), h.Login.workspace, "oidc"); err == nil && !method.Enabled {
		return "/login"
	}
	return "/auth/oidc"
}

func (h Handler) renderApp(w http.ResponseWriter, r *http.Request, reader historyReader, state composerState) {
	principal := reader.principal
	channel := h.requestChannel(r)
	before := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("before")))
	threadTimestamp := strings.TrimSpace(r.URL.Query().Get("thread"))
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, auth.ErrNotAuthenticated)
		return
	}
	csrfToken := auth.CSRFToken(sessionCookie.Value)

	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeStoreError(w, err, "This conversation is temporarily unavailable.")
		return
	}
	history, err := h.historyWindow(r.Context(), principal, channel, before)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) {
			h.writePageError(w, http.StatusBadRequest, "That history link is not valid", "The link you followed does not point at a place in this conversation. Open the channel to read the latest messages.")
			return
		}
		h.writeStoreError(w, err, "The message store is temporarily unavailable.")
		return
	}
	names := h.newUserNames(r.Context(), principal)
	notices := make([]string, 0, 3)
	timeline, timelineNotice := h.newMessageList(r.Context(), principal, messageListRequest{Conversation: conversation, CSRFToken: csrfToken, Messages: history.Messages, Thread: threadTimestamp, Before: string(before), Names: names})
	if timelineNotice != "" {
		notices = append(notices, timelineNotice)
	}

	var thread messageList
	if threadTimestamp != "" {
		if _, parseErr := domain.ParseMessageTimestamp(domain.MessageTimestamp(threadTimestamp)); parseErr != nil {
			h.writePageError(w, http.StatusBadRequest, "That thread link is not valid", "The link you followed does not identify a message in this conversation.")
			return
		}
		replies, repliesErr := h.Messages.Replies(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(threadTimestamp), domain.PageRequest{Limit: timelineWindow})
		if repliesErr != nil {
			h.writeStoreError(w, repliesErr, "The thread is temporarily unavailable.")
			return
		}
		var threadNotice string
		thread, threadNotice = h.newMessageList(r.Context(), principal, messageListRequest{Conversation: conversation, CSRFToken: csrfToken, Messages: replies.Messages, Thread: threadTimestamp, Before: string(before), ThreadPane: true, Names: names})
		if threadNotice != "" && timelineNotice == "" {
			notices = append(notices, threadNotice)
		}
	}

	conversations := h.sidebar(r.Context(), principal, channel, history.AtLatest, domain.Cursor(strings.TrimSpace(r.URL.Query().Get("conversations"))))
	if conversations.Notice != "" {
		notices = append(notices, conversations.Notice)
	}

	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your identity is temporarily unavailable.")
		return
	}
	username := displayName(current)

	data := pageData{
		Timeline:        timeline,
		Thread:          thread,
		ThreadTimestamp: threadTimestamp,
		Channels:        conversations.Channels,
		Directs:         conversations.Directs,
		Channel:         string(channel),
		ChannelName:     conversationName(conversation),
		ChannelMeta:     conversationMeta(conversation),
		CSRFToken:       csrfToken,
		ShowProfile:     h.canShowIdentity(),
		ShowAdmin:       h.canShowAuthorizationAdmin(principal),
		Username:        username,
		UserInitial:     initial(username),
		AtLatest:        history.AtLatest,
		Notice:          strings.Join(notices, " "),
		Error:           state.Message,
		Draft:           state.Draft,
		ComposeURL:      mutationURL("/app/message", string(channel), "", threadTimestamp, ""),
		TimelineURL:     fragmentURL(string(channel), "", string(before)),
		ThreadURL:       fragmentURL(string(channel), threadTimestamp, ""),
	}
	if conversations.More != "" {
		data.MoreChannelsURL = appURL(string(channel), threadTimestamp, string(before), "", string(conversations.More))
	}
	// Marking the conversation read is a durable write, so the page carries a
	// form for it rather than performing it while rendering. The client submits
	// the form once the timeline has settled; a reader without JavaScript sees
	// the control and decides for themselves.
	if history.AtLatest && conversations.CurrentUnread > 0 && len(history.Messages) > 0 {
		last := history.Messages[len(history.Messages)-1]
		data.MarkReadURL = mutationURL("/app/read", string(channel), "", threadTimestamp, "")
		data.MarkReadTimestamp = string(domain.NewMessageTimestamp(last.CreatedAt))
	}
	if history.OlderCursor != "" {
		data.OlderURL = appURL(string(channel), threadTimestamp, string(history.OlderCursor), "", "")
	}
	if !history.AtLatest {
		data.NewestURL = appURL(string(channel), threadTimestamp, "", "", "")
		data.LatestURL = appURL(string(channel), threadTimestamp, "", "", "")
	}
	status := state.Status
	if status == 0 {
		status = http.StatusOK
	}
	h.writeHTML(w, pageTemplate, data, status, "page rendering unavailable")
}

// timeline renders the message region on its own so live updates and mutations
// re-render exactly what the server owns, instead of reloading the page and
// discarding the composer draft, the scroll position and the focus ring.
func (h Handler) timeline(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
	if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, auth.ErrNotAuthenticated)
		return
	}
	channel := h.requestChannel(r)
	threadTimestamp := strings.TrimSpace(r.URL.Query().Get("thread"))
	before := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("before")))
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeFragmentError(w, err, "the conversation is temporarily unavailable")
		return
	}
	var messages []domain.Message
	if threadTimestamp != "" {
		if _, parseErr := domain.ParseMessageTimestamp(domain.MessageTimestamp(threadTimestamp)); parseErr != nil {
			secureHeaders(w, workspaceContentSecurityPolicy)
			http.Error(w, "that thread link is not valid", http.StatusBadRequest)
			return
		}
		replies, repliesErr := h.Messages.Replies(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(threadTimestamp), domain.PageRequest{Limit: timelineWindow})
		if repliesErr != nil {
			h.writeFragmentError(w, repliesErr, "the thread is temporarily unavailable")
			return
		}
		messages = replies.Messages
	} else {
		history, historyErr := h.historyWindow(r.Context(), principal, channel, before)
		if historyErr != nil {
			if errors.Is(historyErr, domain.ErrInvalidCursor) {
				secureHeaders(w, workspaceContentSecurityPolicy)
				http.Error(w, "that history link is not valid", http.StatusBadRequest)
				return
			}
			h.writeFragmentError(w, historyErr, "the message store is temporarily unavailable")
			return
		}
		messages = history.Messages
	}
	list, _ := h.newMessageList(r.Context(), principal, messageListRequest{Conversation: conversation, CSRFToken: auth.CSRFToken(sessionCookie.Value), Messages: messages, Thread: threadTimestamp, Before: string(before), ThreadPane: threadTimestamp != "", Names: h.newUserNames(r.Context(), principal)})
	h.writeFragment(w, list)
}

// historyWindow is the newest window of a conversation, or the window ending at
// end when the reader has navigated into older history.
//
// History is read newest-first in one bounded keyset page, then reversed for
// chronological rendering. The cursor names the oldest displayed row, so the
// next descending request starts strictly before it and adjacent windows cannot
// overlap or skip a message.
type historyView struct {
	Messages    []domain.Message
	OlderCursor domain.Cursor
	AtLatest    bool
}

func (h Handler) historyWindow(ctx context.Context, principal auth.Principal, channel domain.ConversationID, end domain.Cursor) (historyView, error) {
	page, err := h.Messages.History(ctx, principal.WorkspaceID, principal.UserID, channel, domain.PageRequest{
		Limit:      timelineWindow,
		Cursor:     end,
		Descending: true,
	})
	if err != nil {
		return historyView{}, err
	}
	newestFirst := make([]domain.Message, 0, timelineWindow)
	for _, message := range page.Messages {
		if len(newestFirst) == timelineWindow {
			break
		}
		newestFirst = append(newestFirst, message)
	}
	view := historyView{AtLatest: end == ""}
	if page.HasMore && len(newestFirst) > 0 {
		cursor, cursorErr := domain.NewMessageCursor(newestFirst[len(newestFirst)-1])
		if cursorErr != nil {
			return historyView{}, cursorErr
		}
		view.OlderCursor = cursor
	}
	slices.Reverse(newestFirst)
	view.Messages = newestFirst
	return view, nil
}

// messageListRequest is everything a message region needs to render: the
// conversation it belongs to, the messages in it, and the view state that its
// controls have to return to.
type messageListRequest struct {
	Conversation domain.Conversation
	CSRFToken    string
	Messages     []domain.Message
	Thread       string
	Before       string
	ThreadPane   bool
	Names        *userNames
}

// newMessageList builds the single type the message partial renders. It also
// reports a user-facing notice when an adjacent read (reactions, pins) is
// degraded, instead of failing the whole conversation view.
func (h Handler) newMessageList(ctx context.Context, principal auth.Principal, request messageListRequest) (messageList, string) {
	conversation := request.Conversation
	csrfToken := request.CSRFToken
	messages := request.Messages
	threadTimestamp := request.Thread
	before := request.Before
	names := request.Names
	// The thread pane repeats messages that the timeline already shows, so
	// every identifier it generates is namespaced: one document cannot carry the
	// same id twice. The prefix used to be applied to the anchor only, while
	// messageView.ID — which builds `id="reaction-<id>"` and its `for=` label —
	// kept the bare message identifier, so opening a thread produced duplicate
	// ids and a label that pointed at the wrong control.
	anchorPrefix := ""
	if request.ThreadPane {
		anchorPrefix = "thread-"
	}
	channel := string(conversation.ID)
	list := messageList{
		ChannelName: conversationName(conversation),
		CSRFToken:   csrfToken,
		CanReact:    principal.HasScope(auth.ScopeReactionsWrite),
		CanPin:      principal.HasScope(auth.ScopePinsWrite),
		Messages:    make([]messageView, 0, len(messages)),
	}
	notice := ""
	pinned := map[domain.MessageID]struct{}{}
	if principal.HasScope(auth.ScopePinsRead) || principal.HasScope(auth.ScopePinsWrite) {
		pins, _, _, err := h.Messages.Pins(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: pinWindow})
		if err != nil {
			notice = "Pinned messages are temporarily unavailable."
		}
		for _, pin := range pins {
			pinned[pin.Message] = struct{}{}
		}
	}
	readReactions := principal.HasScope(auth.ScopeReactionsRead) || principal.HasScope(auth.ScopeReactionsWrite)
	for _, message := range messages {
		// Deletion is soft in the store and leaves the text in place, so every
		// region has to drop the row rather than trust the read to have done it.
		if message.Deleted {
			continue
		}
		timestamp := string(domain.NewMessageTimestamp(message.CreatedAt))
		author := names.name(message.AuthorID)
		view := messageView{
			ID:            anchorPrefix + string(message.ID),
			Anchor:        anchorPrefix + messageAnchor(message.ID),
			AuthorName:    author,
			AuthorInitial: initial(author),
			Text:          message.Text,
			MachineTime:   message.CreatedAt.UTC().Format(time.RFC3339Nano),
			DisplayTime:   formatTime(message.CreatedAt),
			Channel:       channel,
			ChannelName:   list.ChannelName,
			ReplyURL:      appURL(channel, timestamp, before, "", ""),
			ReactionURL:   mutationURL("/app/reaction", channel, timestamp, threadTimestamp, before),
			UnreactURL:    mutationURL("/app/reaction/remove", channel, timestamp, threadTimestamp, before),
			PinURL:        mutationURL("/app/pin", channel, timestamp, threadTimestamp, before),
			UnpinURL:      mutationURL("/app/pin/remove", channel, timestamp, threadTimestamp, before),
		}
		if _, ok := pinned[message.ID]; ok {
			view.Pinned = true
		}
		if readReactions {
			reactions, _, _, err := h.Messages.Reactions(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.MessageTimestamp(timestamp), domain.PageRequest{Limit: reactionWindow})
			if err != nil && notice == "" {
				notice = "Reactions are temporarily unavailable."
			}
			view.Reactions = summarizeReactions(reactions, principal.UserID)
		}
		list.Messages = append(list.Messages, view)
	}
	return list, notice
}

func summarizeReactions(reactions []domain.Reaction, viewer domain.UserID) []reactionView {
	if len(reactions) == 0 {
		return nil
	}
	order := make([]string, 0, len(reactions))
	counts := map[string]int{}
	mine := map[string]bool{}
	for _, reaction := range reactions {
		if _, seen := counts[reaction.Name]; !seen {
			order = append(order, reaction.Name)
		}
		counts[reaction.Name]++
		if reaction.UserID == viewer {
			mine[reaction.Name] = true
		}
	}
	sort.Strings(order)
	views := make([]reactionView, 0, len(order))
	for _, name := range order {
		views = append(views, reactionView{Name: name, Count: counts[name], Mine: mine[name]})
	}
	return views
}

// sidebarView is the conversation list plus the one fact the page outside it
// needs: whether the conversation being read still has unread messages, which
// decides whether the page offers to mark it read.
type sidebarView struct {
	Channels      []conversationView
	Directs       []conversationView
	More          domain.Cursor
	Notice        string
	CurrentUnread int
}

func (h Handler) sidebar(ctx context.Context, principal auth.Principal, channel domain.ConversationID, atLatest bool, cursor domain.Cursor) sidebarView {
	page, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{Limit: conversationWindow, Cursor: cursor})
	if err != nil {
		return sidebarView{Notice: "The conversation list is temporarily out of date."}
	}
	view := sidebarView{
		Channels: make([]conversationView, 0, len(page.Conversations)),
		Directs:  make([]conversationView, 0, len(page.Conversations)),
	}
	resolved := 0
	for _, conversation := range page.Conversations {
		if conversation.ID == channel {
			view.CurrentUnread = conversation.UnreadCount
		}
		item := conversationView{ID: string(conversation.ID), Name: conversationName(conversation), Current: conversation.ID == channel, UnreadCount: conversation.UnreadCount}
		if item.Current && atLatest {
			// The page just rendered every message in this conversation; showing
			// its own unread badge tells the reader to read what they are reading.
			item.UnreadCount = 0
		}
		if !conversation.IsDirect && !conversation.IsGroupDirect {
			view.Channels = append(view.Channels, item)
			continue
		}
		if resolved < directNameWindow {
			resolved++
			if participants := h.participantNames(ctx, principal, conversation.ID); participants != "" {
				item.Name = participants
			}
		}
		view.Directs = append(view.Directs, item)
	}
	if page.HasMore {
		view.More = page.NextCursor
	}
	return view
}

// participantNames names a direct conversation after the people in it: the
// stored name of every direct conversation is "direct", which is neither
// distinguishable nor useful in a sidebar.
func (h Handler) participantNames(ctx context.Context, principal auth.Principal, conversation domain.ConversationID) string {
	page, err := h.Messages.ConversationMembers(ctx, principal.WorkspaceID, principal.UserID, conversation, domain.PageRequest{Limit: 10})
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(page.Users))
	for _, user := range page.Users {
		if user.ID == principal.UserID {
			continue
		}
		names = append(names, displayName(user))
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func (h Handler) search(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeSearchRead)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	data := searchData{Query: query, Channel: channel, Searched: query != ""}
	if query == "" {
		h.writeHTML(w, searchTemplate, data, http.StatusOK, "search rendering unavailable")
		return
	}
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	results, err := h.Messages.Search(r.Context(), principal.WorkspaceID, principal.UserID, query, domain.PageRequest{Limit: searchWindow, Cursor: cursor})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidSearch):
			data.Error = "Enter between one and 500 characters to search."
			data.Searched = false
			h.writeHTML(w, searchTemplate, data, http.StatusBadRequest, "search rendering unavailable")
		case errors.Is(err, domain.ErrInvalidCursor):
			data.Error = "That results link is no longer valid. Search again to see matches."
			data.Searched = false
			h.writeHTML(w, searchTemplate, data, http.StatusBadRequest, "search rendering unavailable")
		default:
			h.writeStoreError(w, err, "Search is temporarily unavailable.")
		}
		return
	}
	names := h.newUserNames(r.Context(), principal)
	data.Messages = h.newResultViews(r.Context(), principal, results.Messages, names)
	if results.HasMore && results.NextCursor != "" {
		values := url.Values{"q": {query}, "channel": {channel}, "cursor": {string(results.NextCursor)}}
		data.MoreURL = "/app/search?" + values.Encode()
	}
	h.writeHTML(w, searchTemplate, data, http.StatusOK, "search rendering unavailable")
}

func (h Handler) newResultViews(ctx context.Context, principal auth.Principal, messages []domain.Message, names *userNames) []messageView {
	views := make([]messageView, 0, len(messages))
	for _, message := range messages {
		author := names.name(message.AuthorID)
		channelName := string(message.Conversation)
		if conversation, err := h.Messages.ConversationInfo(ctx, principal.WorkspaceID, principal.UserID, message.Conversation); err == nil {
			channelName = conversationName(conversation)
		}
		// A search hit is opened where it lives: the window that ends at the
		// message, the message anchored, and the thread pane only when the hit
		// really is a threaded reply.
		before := ""
		if cursor, err := domain.NewMessageCursor(message); err == nil {
			before = string(cursor)
		}
		views = append(views, messageView{
			ID:            string(message.ID),
			Anchor:        messageAnchor(message.ID),
			AuthorName:    author,
			AuthorInitial: initial(author),
			Text:          message.Text,
			MachineTime:   message.CreatedAt.UTC().Format(time.RFC3339Nano),
			DisplayTime:   formatTime(message.CreatedAt),
			Channel:       string(message.Conversation),
			ChannelName:   channelName,
			Permalink:     appURL(string(message.Conversation), string(message.ThreadTimestamp), before, messageAnchor(message.ID), ""),
		})
	}
	return views
}

// ---------------------------------------------------------------------------
// Members and profile
// ---------------------------------------------------------------------------

func (h Handler) members(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	h.renderMembers(w, r, principal, nil, "", http.StatusOK)
}

func (h Handler) renderMembers(w http.ResponseWriter, r *http.Request, principal auth.Principal, submitted *domain.UserProfile, message string, status int) {
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	page, err := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: memberWindow, Cursor: cursor})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) {
			h.writePageError(w, http.StatusBadRequest, "That members link is not valid", "Open the member directory again to see who is here.")
			return
		}
		h.writeStoreError(w, err, "The member directory is temporarily unavailable.")
		return
	}
	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your profile is temporarily unavailable.")
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, auth.ErrNotAuthenticated)
		return
	}
	members := make([]memberView, 0, len(page.Users))
	for _, user := range page.Users {
		// A deactivated account is not a person to message: UserInfo already
		// treats it as absent, and offering "Message <them>" here opened a dead
		// conversation or answered with a bare 404.
		if user.Deleted {
			continue
		}
		members = append(members, memberView{ID: string(user.ID), Name: displayName(user), RealName: user.RealName, Profile: user.Profile, IsSelf: user.ID == principal.UserID})
	}
	profile := current.Profile
	if submitted != nil {
		profile = *submitted
	}
	data := membersData{
		Members:    members,
		Profile:    profile,
		CSRFToken:  auth.CSRFToken(sessionCookie.Value),
		Error:      message,
		CanMessage: principal.HasScope(auth.ScopeChannelsManage),
	}
	if page.HasMore && page.NextCursor != "" {
		data.MoreMembersURL = "/app/members?" + url.Values{"cursor": {string(page.NextCursor)}}.Encode()
	}
	h.writeHTML(w, membersTemplate, data, status, "member rendering unavailable")
}

func (h Handler) setProfile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your profile could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	profile := domain.UserProfile{DisplayName: fields["display_name"], StatusText: fields["status_text"], StatusEmoji: fields["status_emoji"], Image24: fields["image_24"], Image32: fields["image_32"], Image48: fields["image_48"], Image72: fields["image_72"], Image192: fields["image_192"], Image512: fields["image_512"], Image1024: fields["image_1024"]}
	if _, err := h.Messages.SetUserProfile(r.Context(), principal.WorkspaceID, principal.UserID, profile); err != nil {
		// A rejected save keeps every submitted value and says which limit it
		// crossed, instead of answering with a bare status line.
		if errors.Is(err, service.ErrInvalidProfile) {
			h.renderMembers(w, r, principal, &profile, "Your profile was not saved. A display name is at most 80 characters, a status at most 100, a status emoji at most 64, and each image URL at most 2048.", http.StatusBadRequest)
			return
		}
		h.renderMembers(w, r, principal, &profile, "Your profile could not be saved because the workspace store is temporarily unavailable. Try again.", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/app/members", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

func (h Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	if _, err := h.Authenticator.Authenticate(r); err != nil {
		h.writeAuthError(w, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The sign-out request could not be read. Reload the page and try again."); !ok {
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, auth.ErrNotAuthenticated)
		return
	}
	redirectURL := "/signed-out"
	var logoutErr error
	if h.Login != nil {
		redirectURL, logoutErr = h.Login.logoutRedirectURL(r.Context(), sessionCookie.Value)
	}
	// The browser stops being signed in here, whatever the durable record does
	// next: leaving the cookie in place on a failed revocation strands the user
	// with a session they asked to end.
	cookie := auth.SessionCookie("", -1, h.CookieDomain)
	cookie.Expires = time.Unix(1, 0).UTC()
	http.SetCookie(w, cookie)
	w.Header().Set("Cache-Control", "no-store")
	if err := h.SessionRevoker.RevokeSession(r.Context(), sessionCookie.Value); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.writePageError(w, http.StatusServiceUnavailable, "Sign-out is incomplete", "You are signed out of this browser, but the session record could not be revoked. Tell an administrator if this keeps happening.")
		return
	}
	if h.Login != nil && logoutErr != nil {
		redirectURL = "/signed-out?global=failed"
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

func (h Handler) validation(w http.ResponseWriter, r *http.Request) {
	h.identity(w, r, "SameOldChat is authenticated")
}

func (h Handler) me(w http.ResponseWriter, r *http.Request) {
	h.identity(w, r, "My profile")
}

// canShowIdentity is the one predicate behind both the identity page and the
// control that leads to it, so the workspace shell cannot advertise a page that
// answers "unavailable".
func (h Handler) canShowIdentity() bool {
	return h.Login != nil && h.Login.hasOpenIDConnectProvider() && immutableReleaseRevision.MatchString(h.ReleaseRevision)
}

func (h Handler) canShowAuthorizationAdmin(principal auth.Principal) bool {
	if h.Login == nil {
		return false
	}
	for _, scope := range []auth.Scope{auth.ScopeAdminAppsRead, auth.ScopeAdminAppsWrite, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite} {
		if principal.HasScope(scope) {
			return true
		}
	}
	return false
}

func (h Handler) identity(w http.ResponseWriter, r *http.Request, heading string) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		if errors.Is(err, auth.ErrNotAuthenticated) && h.Login != nil {
			http.Redirect(w, r, "/signed-out", http.StatusSeeOther)
			return
		}
		h.writeAuthError(w, err)
		return
	}
	if !h.canShowIdentity() {
		h.writePageError(w, http.StatusServiceUnavailable, "Identity validation is unavailable", "This deployment does not have a Shauth provider and an immutable release revision, so a verified identity cannot be shown.")
		return
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your identity is temporarily unavailable.")
		return
	}
	role, err := h.currentRole(r, principal)
	if err != nil {
		h.writeStoreError(w, err, "Your workspace role is temporarily unavailable.")
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, auth.ErrNotAuthenticated)
		return
	}
	avatarURL := strings.TrimSpace(user.Profile.Image72)
	if avatarURL == "" {
		avatarURL = strings.TrimSpace(user.Profile.Image48)
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeHTML(w, identityTemplate, identityData{Heading: heading, Username: user.Name, Email: user.Email, Role: role, Release: h.ReleaseRevision, CSRFToken: auth.CSRFToken(sessionCookie.Value), AvatarURL: avatarURL, Avatar: initial(user.Name)}, http.StatusOK, "identity rendering unavailable")
}

// currentRole reports the signed-in user's own workspace role in the vocabulary
// the identity page renders.
//
// It reads one membership as the user it is about, which is authority the user
// always holds over themselves. It used to page the whole workspace through
// AdminListUsers as the signed-in member, so /me and /auth/validation depended on
// an administrative read that a member must not be allowed to perform.
func (h Handler) currentRole(r *http.Request, principal auth.Principal) (string, error) {
	membership, err := h.Messages.WorkspaceMembership(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		return "", err
	}
	switch membership.Role {
	case domain.WorkspaceRoleMember:
		return "developer", nil
	case domain.WorkspaceRoleAdmin, domain.WorkspaceRoleOwner:
		return "admin", nil
	default:
		return "", errors.New("identity has an unsupported workspace role")
	}
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

func (h Handler) postMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your message could not be read from the form. Reload the page and send it again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	message, err := h.Messages.Post(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["text"], domain.MessageTimestamp(fields["thread_ts"]), "")
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The message could not be sent because the workspace store is temporarily unavailable."
		if errors.Is(err, service.ErrInvalidMessage) {
			status = http.StatusBadRequest
			reason = "A message needs some text before it can be sent."
		}
		if errors.Is(err, service.ErrInvalidTimestamp) {
			status = http.StatusBadRequest
			reason = "That thread is not a message in this conversation."
		}
		// Posting into a channel now requires membership of it, which is a
		// refusal the reader can act on and not an outage.
		if errors.Is(err, service.ErrNotInConversation) {
			status = http.StatusForbidden
			reason = "You are not a member of this conversation, so the message was not sent."
		}
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			reason = "That conversation is no longer available."
		}
		w.Header().Set("Vary", "HX-Request")
		if r.Header.Get("HX-Request") == "true" {
			// The composer renders this text next to the field and keeps the
			// draft, so a failure is never silent and never loses the message.
			secureHeaders(w, workspaceContentSecurityPolicy)
			http.Error(w, reason, status)
			return
		}
		// Re-rendering the workspace requires the scope that reads the
		// workspace. Without this the page — the whole conversation, the
		// sidebar and a live CSRF token — was the failure body for a principal
		// that GET /app answers with 403.
		reader, readerErr := requireHistoryReader(principal)
		if readerErr != nil {
			h.writePageError(w, status, "That message was not sent", reason)
			return
		}
		h.renderApp(w, r, reader, composerState{Draft: fields["text"], Message: reason, Status: status})
		return
	}
	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
		if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
			h.writeAuthError(w, auth.ErrNotAuthenticated)
			return
		}
		conversation, conversationErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
		if conversationErr != nil {
			h.writeFragmentError(w, conversationErr, "the conversation is temporarily unavailable")
			return
		}
		thread := strings.TrimSpace(fields["thread_ts"])
		list, _ := h.newMessageList(r.Context(), principal, messageListRequest{Conversation: conversation, CSRFToken: auth.CSRFToken(sessionCookie.Value), Messages: []domain.Message{message}, Thread: thread, ThreadPane: thread != "", Names: h.newUserNames(r.Context(), principal)})
		h.writeFragment(w, list)
		return
	}
	http.Redirect(w, r, h.viewURL(r, strings.TrimSpace(fields["thread_ts"])), http.StatusSeeOther)
}

func (h Handler) addReaction(w http.ResponseWriter, r *http.Request) {
	h.mutateReaction(w, r, true)
}

func (h Handler) removeReaction(w http.ResponseWriter, r *http.Request) {
	h.mutateReaction(w, r, false)
}

func (h Handler) mutateReaction(w http.ResponseWriter, r *http.Request, add bool) {
	principal, err := h.authenticate(r, auth.ScopeReactionsWrite)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The reaction could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	name := strings.TrimSpace(fields["name"])
	if name == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reaction has no name", "A reaction needs a name, such as :wave:. Nothing was changed.")
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The reaction could not be applied because the link does not identify a message in this conversation.")
		return
	}
	if add {
		err = h.Messages.AddReaction(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp, name)
	} else {
		err = h.Messages.RemoveReaction(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp, name)
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The reaction could not be saved because the workspace store is temporarily unavailable."
		heading := "The reaction was not saved"
		if errors.Is(err, service.ErrInvalidReaction) {
			status = http.StatusBadRequest
			reason = "That reaction name is not valid."
		}
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			heading = "That message is no longer available"
			reason = "That message or reaction is no longer available."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) addPin(w http.ResponseWriter, r *http.Request) {
	h.mutatePin(w, r, true)
}

func (h Handler) removePin(w http.ResponseWriter, r *http.Request) {
	h.mutatePin(w, r, false)
}

func (h Handler) mutatePin(w http.ResponseWriter, r *http.Request, add bool) {
	principal, err := h.authenticate(r, auth.ScopePinsWrite)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The pin could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The pin could not be applied because the link does not identify a message in this conversation.")
		return
	}
	if add {
		err = h.Messages.AddPin(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp)
	} else {
		err = h.Messages.RemovePin(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp)
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The pin could not be saved because the workspace store is temporarily unavailable."
		heading := "The pin was not saved"
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			heading = "That message is no longer available"
			reason = "That message or pin is no longer available."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) openConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The conversation could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	users, err := normalizeUserIDs(fields["users"])
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That conversation has no members", "A direct conversation needs at least one member. Nothing was changed.")
		return
	}
	conversation, err := h.Messages.OpenConversation(r.Context(), principal.WorkspaceID, principal.UserID, users)
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The conversation could not be opened because the workspace store is temporarily unavailable."
		if errors.Is(err, service.ErrInvalidConversation) {
			status = http.StatusBadRequest
			reason = "That set of members cannot be opened as a conversation."
		}
		heading := "The conversation was not opened"
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			heading = "That member is no longer here"
			reason = "One of those members is no longer in the workspace."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	http.Redirect(w, r, appURL(string(conversation.ID), "", "", "", ""), http.StatusSeeOther)
}

func normalizeUserIDs(raw string) ([]domain.UserID, error) {
	parts := strings.Split(raw, ",")
	users := make([]domain.UserID, 0, len(parts))
	seen := make(map[domain.UserID]struct{}, len(parts))
	for _, part := range parts {
		user := domain.UserID(strings.TrimSpace(part))
		if user == "" {
			return nil, errors.New("conversation user is empty")
		}
		if _, exists := seen[user]; exists {
			continue
		}
		seen[user] = struct{}{}
		users = append(users, user)
	}
	if len(users) == 0 {
		return nil, errors.New("conversation requires a user")
	}
	return users, nil
}

// completeMutation answers a state change that has no fragment of its own. The
// enhanced client re-renders the live message regions on 204, so the reader
// keeps their draft, their scroll position and the thread they had open; a
// browser without JavaScript returns to exactly the view it submitted from.
func (h Handler) completeMutation(w http.ResponseWriter, r *http.Request) {
	// The response shape is chosen by a request header, so a cache that keeps
	// one must not serve it to the other.
	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, h.viewURL(r, strings.TrimSpace(r.URL.Query().Get("thread"))), http.StatusSeeOther)
}

func (h Handler) viewURL(r *http.Request, thread string) string {
	return appURL(string(h.requestChannel(r)), thread, strings.TrimSpace(r.URL.Query().Get("before")), "", "")
}

func (h Handler) requestChannel(r *http.Request) domain.ConversationID {
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		return domain.ConversationID(channel)
	}
	return h.Channel
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

// workspaceContentSecurityPolicy is the workspace equivalent of the policy the
// administration page already carried. It differs in exactly four places, each
// of which the workspace page actually needs: the hashes of its own four inline
// scripts (so an injected script still cannot run), `connect-src 'self'` for
// the fragment fetches and the event stream, `img-src` for profile images, and
// no `form-action`.
//
// form-action is omitted deliberately. Sign-out posts to this origin and is
// answered with a redirect to the identity provider's end-session endpoint, and
// browsers disagree about whether form-action applies across that redirect: the
// directive would leave global sign-out working in one browser and broken in
// another. The administration page keeps it, because every form there redirects
// to itself.
var workspaceContentSecurityPolicy = "default-src 'none'; script-src " +
	strings.Join(inlineScriptHashes(themeBootstrap, themeToggleScript, progressiveEnhancementScript), " ") +
	"; style-src 'unsafe-inline'; img-src 'self' https: data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

// entryContentSecurityPolicy covers the two pages a signed-out visitor reaches:
// /login and /signed-out. Both are static documents with one inline stylesheet
// and no script, so the policy can be stricter than the workspace's. They used
// to carry no policy and no X-Frame-Options at all, which made both framable —
// and /signed-out carries the "Sign in with Shauth" link, so framing it is
// enough to make a victim start an authorization flow they did not choose.
const entryContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

// inlineScriptHashes is the CSP source list for a set of documents' inline
// scripts. The scripts are package constants with no template action inside
// them, so their bytes are known at initialisation and a hash is exact.
func inlineScriptHashes(documents ...string) []string {
	hashes := make([]string, 0, len(documents))
	seen := map[string]struct{}{}
	for _, document := range documents {
		for _, body := range inlineScriptBodies(document) {
			digest := sha256.Sum256([]byte(body))
			hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
			if _, repeated := seen[hash]; repeated {
				continue
			}
			seen[hash] = struct{}{}
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

// inlineScriptBodies returns the body of every inline script in a document.
//
// It refuses any element it cannot hash rather than skipping it. Searching for
// the literal "<script>" made `<script type="module">` or `<script defer>`
// invisible to the policy builder *and* to the test that checks the policy
// against the document, so the browser would block a script both agreed was
// covered. A tag this cannot hash is a build-time panic, which is the same
// failure the package already takes for an unclosed script.
func inlineScriptBodies(document string) []string {
	const marker, open, close = "<script", "<script>", "</script>"
	bodies := make([]string, 0, 2)
	for {
		start := strings.Index(document, marker)
		if start < 0 {
			return bodies
		}
		if !strings.HasPrefix(document[start:], open) {
			panic("inline script carries attributes, so its hash cannot be computed: " + document[start:min(start+40, len(document))])
		}
		document = document[start+len(open):]
		end := strings.Index(document, close)
		if end < 0 {
			panic("inline script is not closed")
		}
		if strings.Contains(document[:end], marker) {
			panic("inline script body contains a nested script tag")
		}
		bodies = append(bodies, document[:end])
		document = document[end+len(close):]
	}
}

// secureHeaders is the header set every authenticated response in this package
// carries. Without it the workspace was framable — and framing is enough to
// turn one click into Sign out, Pin, or "Message <attacker>", because the
// framed page supplies its own valid token — and an authenticated page holding
// a conversation and a live CSRF token was stored by the browser, so Back after
// sign-out replayed it.
func secureHeaders(w http.ResponseWriter, policy string) {
	header := w.Header()
	header.Set("Content-Security-Policy", policy)
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
}

func (h Handler) writeHTML(w http.ResponseWriter, page *template.Template, data any, status int, unavailable string) {
	var output bytes.Buffer
	if err := page.Execute(&output, data); err != nil {
		secureHeaders(w, workspaceContentSecurityPolicy)
		http.Error(w, unavailable, http.StatusServiceUnavailable)
		return
	}
	secureHeaders(w, workspaceContentSecurityPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(output.Bytes())
}

func (h Handler) writeFragment(w http.ResponseWriter, list messageList) {
	var output bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&output, "messages", list); err != nil {
		secureHeaders(w, workspaceContentSecurityPolicy)
		http.Error(w, "the conversation could not be rendered", http.StatusServiceUnavailable)
		return
	}
	secureHeaders(w, workspaceContentSecurityPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(output.Bytes())
}

func (h Handler) writePageError(w http.ResponseWriter, status int, heading, message string) {
	var output bytes.Buffer
	if err := errorTemplate.Execute(&output, errorData{Heading: heading, Message: message}); err != nil {
		secureHeaders(w, workspaceContentSecurityPolicy)
		http.Error(w, heading, status)
		return
	}
	secureHeaders(w, workspaceContentSecurityPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(output.Bytes())
}

// writeStoreError separates a missing or forbidden conversation from an outage:
// a stale bookmark is a 404 page, not a 503 that blames the store.
func (h Handler) writeStoreError(w http.ResponseWriter, err error, unavailable string) {
	if errors.Is(err, store.ErrNotFound) {
		h.writePageError(w, http.StatusNotFound, "That conversation is not available", "It may have been deleted, or you may not be a member of it. Pick a conversation from the sidebar to keep reading.")
		return
	}
	h.writePageError(w, http.StatusServiceUnavailable, "Temporarily unavailable", unavailable)
}

// writeFragmentError and writeAuthError answer with a bare line of text, which
// is what the enhanced client can render next to the control that failed. The
// bytes are still an authenticated response on this origin, so they carry the
// same five headers as every rendered page: without them a fragment failure and
// every insufficient-scope answer were framable, sniffable and stored by the
// browser, while the test that checks the header set only ever visited a
// successful page and a 404.
func (h Handler) writeFragmentError(w http.ResponseWriter, err error, unavailable string) {
	secureHeaders(w, workspaceContentSecurityPolicy)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "that conversation is not available", http.StatusNotFound)
		return
	}
	http.Error(w, unavailable, http.StatusServiceUnavailable)
}

func (h Handler) authenticate(r *http.Request, scope auth.Scope) (auth.Principal, error) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		return auth.Principal{}, err
	}
	if !principal.HasScope(scope) {
		return auth.Principal{}, auth.ErrMissingScope
	}
	return principal, nil
}

func (h Handler) writeAuthError(w http.ResponseWriter, err error) {
	secureHeaders(w, workspaceContentSecurityPolicy)
	if errors.Is(err, auth.ErrMissingScope) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.Error(w, "not authenticated", http.StatusUnauthorized)
}

// userNames resolves author display names once per request. Message authors are
// people, and the timeline shows their names; a raw user identifier is only the
// last resort for a record that carries no name at all.
type userNames struct {
	handler   Handler
	ctx       context.Context
	principal auth.Principal
	cache     map[domain.UserID]string
}

func (h Handler) newUserNames(ctx context.Context, principal auth.Principal) *userNames {
	return &userNames{handler: h, ctx: ctx, principal: principal, cache: map[domain.UserID]string{}}
}

func (n *userNames) name(id domain.UserID) string {
	if id == "" {
		return "Unknown member"
	}
	if cached, ok := n.cache[id]; ok {
		return cached
	}
	resolved := string(id)
	if user, err := n.handler.Messages.UserInfo(n.ctx, n.principal.WorkspaceID, n.principal.UserID, id); err == nil {
		resolved = displayName(user)
	}
	n.cache[id] = resolved
	return resolved
}

func displayName(user domain.User) string {
	for _, candidate := range []string{user.Profile.DisplayName, user.RealName, user.Name} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return string(user.ID)
}

func conversationName(conversation domain.Conversation) string {
	if name := strings.TrimSpace(conversation.Name); name != "" {
		return name
	}
	return string(conversation.ID)
}

func conversationMeta(conversation domain.Conversation) string {
	if topic := strings.TrimSpace(conversation.Topic); topic != "" {
		return topic
	}
	if purpose := strings.TrimSpace(conversation.Purpose); purpose != "" {
		return purpose
	}
	if conversation.IsDirect || conversation.IsGroupDirect {
		return "Direct message"
	}
	if conversation.IsPrivate {
		return "Private channel"
	}
	return "Channel"
}

// initial is the single uppercase letter an avatar tile can hold. The tile is
// 24 to 72 pixels wide; a name or an identifier does not fit in it.
func initial(name string) string {
	for _, character := range strings.TrimSpace(name) {
		return strings.ToUpper(string(character))
	}
	return "?"
}

func messageAnchor(id domain.MessageID) string {
	return "message-" + string(id)
}

func formatTime(value time.Time) string {
	return value.UTC().Format("Jan 2, 15:04 UTC")
}

func appURL(channel, thread, before, anchor, conversations string) string {
	query := url.Values{"channel": {channel}}
	if thread != "" {
		query.Set("thread", thread)
	}
	if before != "" {
		query.Set("before", before)
	}
	if conversations != "" {
		query.Set("conversations", conversations)
	}
	result := "/app?" + query.Encode()
	if anchor != "" {
		result += "#" + anchor
	}
	return result
}

func mutationURL(path, channel, timestamp, thread, before string) string {
	query := url.Values{"channel": {channel}}
	if timestamp != "" {
		query.Set("ts", timestamp)
	}
	if thread != "" {
		query.Set("thread", thread)
	}
	if before != "" {
		query.Set("before", before)
	}
	return path + "?" + query.Encode()
}

func fragmentURL(channel, thread, before string) string {
	query := url.Values{"channel": {channel}}
	if thread != "" {
		query.Set("thread", thread)
	}
	if before != "" {
		query.Set("before", before)
	}
	return "/app/timeline?" + query.Encode()
}

// ---------------------------------------------------------------------------
// Form decoding
// ---------------------------------------------------------------------------

const maxFormBody = 4 << 20

// decodeMutation bounds and parses the body before anything reads a form value.
// CSRF validation falls back to the token form field, and reading that field
// parses the whole body: validating first would let net/http buffer its own 32
// MB default and leave maxFormBody unreachable.
func (h Handler) decodeMutation(w http.ResponseWriter, r *http.Request, invalid string) (map[string]string, bool) {
	fields, err := decodeFormFields(w, r)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That form could not be read", invalid)
		return nil, false
	}
	if !h.requireCSRF(w, r) {
		return nil, false
	}
	return fields, true
}

func decodeFormFields(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	var parseErr error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		parseErr = r.ParseMultipartForm(maxFormBody)
	} else {
		parseErr = r.ParseForm()
	}
	if parseErr != nil {
		return nil, parseErr
	}
	fields := make(map[string]string, len(r.Form))
	for name, values := range r.Form {
		if len(values) != 1 {
			return nil, errors.New("form fields must occur once")
		}
		fields[name] = values[0]
	}
	return fields, nil
}
