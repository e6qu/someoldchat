package web

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type Handler struct {
	Messages       chatapi.Service
	Authenticator  auth.Authenticator
	SessionRevoker auth.SessionRevoker
	Channel        domain.ConversationID
	CookieDomain   string
	Login          *LoginHandler
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

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en" data-theme="light"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Channel}} · SameOldChat</title><style>
:root{color-scheme:light;--bg:#fff;--panel:#f8f8fa;--panel-strong:#fff;--text:#1d1c1d;--muted:#696969;--line:#dedede;--accent:#611f69;--accent-2:#36c5f0;--hover:#f1edf2;--shadow:0 8px 24px #1d1c1d18}*{box-sizing:border-box}html[data-theme=dark]{color-scheme:dark;--bg:#1a1d21;--panel:#222529;--panel-strong:#1e2125;--text:#e8e8e8;--muted:#a7a7a7;--line:#3b3f45;--hover:#2c3035;--shadow:0 8px 24px #0006}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}button,input{font:inherit}button{cursor:pointer}.shell{min-height:100vh;display:grid;grid-template-rows:52px 1fr}.topbar{background:var(--accent);color:#fff;display:flex;align-items:center;gap:16px;padding:0 20px;box-shadow:var(--shadow)}.brand{font-weight:800;letter-spacing:.2px}.search{flex:1;max-width:560px;margin:auto;display:flex;align-items:center;gap:8px;background:#ffffff2b;border:1px solid #fff5;border-radius:7px;padding:6px 12px;color:#fff}.search input{width:100%;border:0;outline:0;background:transparent;color:#fff}.search input::placeholder{color:#fff}.top-actions{display:flex;gap:8px;margin-left:auto}.icon-button{border:0;background:transparent;color:inherit;border-radius:6px;padding:7px 9px}.icon-button:hover{background:#fff2}.workspace{display:grid;grid-template-columns:256px minmax(0,1fr);min-height:0}.sidebar{background:var(--accent);color:#fff;padding:18px 10px;display:flex;flex-direction:column;gap:18px}.workspace-name{font-weight:800;padding:0 10px}.workspace-sub{color:#e8cbe9;font-size:12px;padding:2px 10px}.side-section{display:grid;gap:2px}.side-label{color:#e8cbe9;font-size:12px;font-weight:700;padding:6px 10px;text-transform:uppercase;letter-spacing:.06em}.side-link{display:flex;align-items:center;gap:9px;padding:7px 10px;border-radius:5px;color:#fff;text-decoration:none}.side-link:hover,.side-link[aria-current=page]{background:#ffffff26}.side-link[aria-current=page]{font-weight:700}.badge{margin-left:auto;background:#fff;color:var(--accent);border-radius:12px;min-width:20px;text-align:center;padding:1px 5px;font-size:12px;font-weight:700}.sidebar-bottom{margin-top:auto;border-top:1px solid #ffffff38;padding-top:12px}.content{min-width:0;display:grid;grid-template-columns:minmax(0,1fr) {{if .ThreadTimestamp}}360px{{end}};background:var(--bg)}.chat{min-width:0;display:grid;grid-template-rows:64px minmax(0,1fr) auto}.channel-header{display:flex;align-items:center;gap:12px;border-bottom:1px solid var(--line);padding:0 26px}.channel-title{font-size:18px;font-weight:800}.channel-meta{color:var(--muted);font-size:13px}.timeline{overflow:auto;padding:24px 26px 12px}.message{display:grid;grid-template-columns:38px minmax(0,1fr);gap:10px;padding:10px 8px;border-radius:7px}.message:hover{background:var(--hover)}.avatar{height:36px;width:36px;border-radius:6px;background:linear-gradient(135deg,#36c5f0,#2eb67d);color:#fff;display:grid;place-items:center;font-weight:800}.message-head{display:flex;align-items:baseline;gap:8px}.author{font-weight:800}.time{color:var(--muted);font-size:12px}.message-text{margin:2px 0 8px;white-space:pre-wrap;overflow-wrap:anywhere}.message-actions{display:flex;gap:8px;align-items:center}.message-actions a,.message-actions button{color:var(--muted);background:transparent;border:0;padding:2px 0;text-decoration:none;font-size:12px}.message-actions a:hover,.message-actions button:hover{color:var(--accent-2)}.inline-form{display:inline-flex;gap:6px}.inline-form input{width:120px;border:1px solid var(--line);border-radius:4px;background:var(--panel-strong);color:var(--text);padding:3px 6px}.composer-wrap{padding:12px 26px 20px;background:linear-gradient(transparent,var(--bg) 15%)}.composer{border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow);padding:10px}.composer textarea{width:100%;min-height:44px;resize:vertical;border:0;outline:0;background:transparent;color:var(--text)}.composer-footer{display:flex;justify-content:space-between;align-items:center}.composer-tools{color:var(--muted);font-size:13px}.send{border:0;border-radius:5px;background:#007a5a;color:#fff;font-weight:700;padding:7px 14px}.thread{border-left:1px solid var(--line);background:var(--panel);padding:20px;overflow:auto}.thread h2{margin:0 0 18px;font-size:18px}.empty{color:var(--muted);padding:30px;text-align:center}.theme-toggle{border:1px solid #fff6;border-radius:5px;color:#fff;background:transparent;padding:6px 9px}.theme-toggle:hover{background:#fff2}@media(max-width:800px){.workspace{grid-template-columns:68px minmax(0,1fr)}.sidebar{padding:18px 6px}.workspace-name,.workspace-sub,.side-label,.side-link span:not(.badge),.sidebar-bottom form{display:none}.side-link{justify-content:center}.content{grid-template-columns:minmax(0,1fr)}.thread{display:none}.search{max-width:none}.topbar{padding:0 10px}.timeline,.composer-wrap{padding-left:14px;padding-right:14px}}
</style></head><body><div class="shell"><header class="topbar"><div class="brand">SameOldChat</div><label class="search" aria-label="Search"><span>⌕</span><input placeholder="Search the workspace" aria-label="Search the workspace"></label><div class="top-actions"><button class="theme-toggle" id="theme-toggle" type="button" aria-label="Toggle dark mode">☾</button><a class="icon-button" href="/app/members" aria-label="Members">◉</a></div></header><div class="workspace"><aside class="sidebar"><div><div class="workspace-name">SameOldChat</div><div class="workspace-sub">Workspace</div></div><nav class="side-section" aria-label="Workspace navigation"><div class="side-label">Workspace</div><a class="side-link" href="/app/members"><span>♙</span><span>Members</span></a></nav><nav class="side-section" aria-label="Channels"><div class="side-label">Channels</div>{{range .Conversations}}<a class="side-link" href="/app?channel={{.ID}}"{{if .Current}} aria-current="page"{{end}}><span>#</span><span>{{.Name}}</span>{{if .UnreadCount}}<span class="badge" aria-label="unread messages">{{.UnreadCount}}</span>{{end}}</a>{{else}}<span class="side-link">No channels available.</span>{{end}}</nav><div class="sidebar-bottom"><form method="post" action="/app/session/revoke"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="side-link" type="submit"><span>↪</span><span>Sign out</span></button></form></div></aside><div class="content"><section class="chat"><header class="channel-header"><div><div class="channel-title"># {{.Channel}}</div><div class="channel-meta">Messages and conversations</div></div></header><section id="timeline" class="timeline" aria-live="polite">{{template "messages" .}}</section><div class="composer-wrap"><form class="composer" method="post" action="/app/message?channel={{.Channel}}" hx-post="/app/message?channel={{.Channel}}" hx-target="#timeline" hx-swap="beforeend"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><textarea id="text" name="text" required autofocus placeholder="Message #{{.Channel}}" aria-label="Message"></textarea>{{if .ThreadTimestamp}}<input type="hidden" name="thread_ts" value="{{.ThreadTimestamp}}"><div class="composer-tools">Replying in thread</div>{{end}}<div class="composer-footer"><span class="composer-tools">Enter to send · Shift+Enter for a new line</span><button class="send" type="submit">Send</button></div></form></div></section>{{if .ThreadTimestamp}}<aside class="thread" aria-label="Thread"><h2>Thread</h2>{{template "messages" .Thread}}</aside>{{end}}</div></div></div><script>(function(){const root=document.documentElement;const saved=localStorage.getItem('sameoldchat-theme');if(saved==='dark')root.dataset.theme='dark';document.getElementById('theme-toggle').addEventListener('click',function(){const dark=root.dataset.theme==='dark';root.dataset.theme=dark?'light':'dark';localStorage.setItem('sameoldchat-theme',dark?'light':'dark')});if(window.EventSource){const events=new EventSource('/events');const refresh=()=>window.location.reload();['message.created','message.updated','message.deleted'].forEach(type=>events.addEventListener(type,refresh))}})();</script></body></html>
{{define "messages"}}{{range .Messages}}<article class="message" data-message-id="{{.ID}}"><div class="avatar" aria-hidden="true">{{.AuthorID}}</div><div><div class="message-head"><span class="author">{{.AuthorID}}</span><time class="time" datetime="{{.CreatedAt}}">{{.CreatedAt}}</time></div><p class="message-text">{{.Text}}</p><div class="message-actions"><a href="/app?channel={{$.Channel}}&thread={{.Timestamp}}">Reply in thread</a><form class="inline-form" aria-label="Add reaction" method="post" action="/app/reaction?channel={{$.Channel}}&ts={{.Timestamp}}" hx-post="/app/reaction?channel={{$.Channel}}&ts={{.Timestamp}}" hx-target="#timeline" hx-swap="outerHTML"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><label for="reaction-{{.ID}}" hidden>Reaction</label><input id="reaction-{{.ID}}" name="name" maxlength="64" placeholder="Add reaction" required><button type="submit">Add</button></form><form method="post" action="/app/pin?channel={{$.Channel}}&ts={{.Timestamp}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Pin</button></form></div></div></article>{{else}}<p class="empty">No messages yet. Start the conversation.</p>{{end}}{{end}}`))

var membersTemplate = template.Must(template.New("members").Parse(`<!doctype html>
<html lang="en" data-theme="light"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Members · SameOldChat</title><style>:root{--bg:#fff;--panel:#f8f8fa;--text:#1d1c1d;--muted:#696969;--line:#dedede;--accent:#611f69;--green:#007a5a}html[data-theme=dark]{--bg:#1a1d21;--panel:#222529;--text:#e8e8e8;--muted:#a7a7a7;--line:#3b3f45}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.bar{height:52px;background:var(--accent);color:#fff;display:flex;align-items:center;padding:0 22px;gap:18px}.bar a{color:#fff;text-decoration:none;font-weight:700}.bar button{margin-left:auto;border:1px solid #fff6;background:transparent;color:#fff;border-radius:5px;padding:6px 10px}.layout{max-width:1100px;margin:0 auto;padding:32px 24px}.heading{border-bottom:1px solid var(--line);padding-bottom:20px;margin-bottom:24px}.heading h1{margin:0 0 4px;font-size:28px}.muted{color:var(--muted)}.grid{display:grid;grid-template-columns:minmax(280px,380px) 1fr;gap:24px}.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:22px}.card h2{margin-top:0}.field{display:grid;gap:5px;margin:14px 0}.field input{width:100%;border:1px solid var(--line);border-radius:5px;background:var(--bg);color:var(--text);padding:9px}.save{background:var(--green);color:#fff;border:0;border-radius:5px;padding:9px 14px;font-weight:700}.members{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px}.person{background:var(--bg);border:1px solid var(--line);border-radius:8px;padding:15px}.person h2{font-size:16px;margin:0}.person p{margin:5px 0;color:var(--muted)}@media(max-width:720px){.grid{grid-template-columns:1fr}.layout{padding:22px 14px}}</style></head><body><header class="bar"><a href="/app">← Back to chat</a><span>Members</span><button id="theme-toggle" type="button">☾ Theme</button></header><main class="layout"><div class="heading"><h1>Workspace members</h1><div class="muted">Manage your profile and see who is here.</div></div><div class="grid"><section class="card" aria-label="Your profile"><h2>Edit profile</h2><form method="post" action="/app/profile"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label class="field" for="display_name">Display name<input id="display_name" name="display_name" value="{{.Current.Profile.DisplayName}}" required></label><label class="field" for="status_text">Status<input id="status_text" name="status_text" value="{{.Current.Profile.StatusText}}"></label><label class="field" for="status_emoji">Status emoji<input id="status_emoji" name="status_emoji" value="{{.Current.Profile.StatusEmoji}}"></label><label class="field" for="image_24">Image 24 URL<input id="image_24" name="image_24" value="{{.Current.Profile.Image24}}"></label><label class="field" for="image_32">Image 32 URL<input id="image_32" name="image_32" value="{{.Current.Profile.Image32}}"></label><label class="field" for="image_48">Image 48 URL<input id="image_48" name="image_48" value="{{.Current.Profile.Image48}}"></label><label class="field" for="image_72">Image 72 URL<input id="image_72" name="image_72" value="{{.Current.Profile.Image72}}"></label><label class="field" for="image_192">Image 192 URL<input id="image_192" name="image_192" value="{{.Current.Profile.Image192}}"></label><label class="field" for="image_512">Image 512 URL<input id="image_512" name="image_512" value="{{.Current.Profile.Image512}}"></label><label class="field" for="image_1024">Image 1024 URL<input id="image_1024" name="image_1024" value="{{.Current.Profile.Image1024}}"></label><button class="save" type="submit">Save profile</button></form></section><section class="card" aria-label="Workspace members"><h2>People</h2><div class="members">{{range .Members}}<article class="person"><h2>{{.Name}}</h2><p>{{.RealName}}</p>{{if .Profile.DisplayName}}<p>{{.Profile.DisplayName}}</p>{{end}}{{if .Profile.StatusText}}<p>{{.Profile.StatusEmoji}} {{.Profile.StatusText}}</p>{{end}}</article>{{else}}<p class="muted">No members available.</p>{{end}}</div></section></div></main><script>(function(){const root=document.documentElement;const saved=localStorage.getItem('sameoldchat-theme');if(saved==='dark')root.dataset.theme='dark';document.getElementById('theme-toggle').addEventListener('click',function(){const dark=root.dataset.theme==='dark';root.dataset.theme=dark?'light':'dark';localStorage.setItem('sameoldchat-theme',dark?'light':'dark')})})();</script></body></html>`))

var searchTemplate = template.Must(template.New("search").Parse(`<!doctype html>
<html lang="en" data-theme="light"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Search · SameOldChat</title><style>:root{--bg:#fff;--panel:#f8f8fa;--text:#1d1c1d;--muted:#696969;--line:#dedede;--accent:#611f69}html[data-theme=dark]{--bg:#1a1d21;--panel:#222529;--text:#e8e8e8;--muted:#a7a7a7;--line:#3b3f45}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.bar{height:52px;background:var(--accent);color:#fff;display:flex;align-items:center;padding:0 22px;gap:18px}.bar a{color:#fff;text-decoration:none;font-weight:700}.bar form{display:flex;flex:1;max-width:600px;margin:auto;gap:8px}.bar input{width:100%;border:1px solid #fff6;border-radius:5px;padding:8px 10px;background:#ffffff2b;color:#fff}.bar input::placeholder{color:#fff}.bar button{border:1px solid #fff6;background:transparent;color:#fff;border-radius:5px;padding:6px 10px}.layout{max-width:900px;margin:0 auto;padding:32px 24px}.heading{border-bottom:1px solid var(--line);padding-bottom:20px;margin-bottom:24px}.heading h1{margin:0 0 4px;font-size:28px}.muted{color:var(--muted)}.results{display:grid;gap:8px}.result{display:block;padding:16px;background:var(--panel);border:1px solid var(--line);border-radius:8px;color:inherit;text-decoration:none}.result:hover{border-color:var(--accent)}.author{font-weight:700}.time{color:var(--muted);font-size:12px;margin-left:8px}.text{margin:6px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}.empty{color:var(--muted);padding:24px;text-align:center}@media(max-width:720px){.layout{padding:22px 14px}.bar{padding:0 12px}.bar>a{font-size:13px}}
</style></head><body><header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><form method="get" action="/app/search" aria-label="Search the workspace"><input name="q" value="{{.Query}}" placeholder="Search messages" aria-label="Search messages" required><button type="submit">Search</button></form><button id="theme-toggle" type="button">☾ Theme</button></header><main class="layout"><div class="heading"><h1>Search results</h1>{{if .Query}}<div class="muted">Messages matching “{{.Query}}”</div>{{else}}<div class="muted">Enter a search term to find messages.</div>{{end}}</div><section class="results" aria-live="polite">{{range .Messages}}<a class="result" href="/app?channel={{.Channel}}&thread={{.Timestamp}}"><span class="author">{{.AuthorID}}</span><time class="time" datetime="{{.CreatedAt}}">{{.CreatedAt}}</time><p class="text">{{.Text}}</p></a>{{else}}<p class="empty">No matching messages.</p>{{end}}</section></main><script>(function(){const root=document.documentElement;const saved=localStorage.getItem('sameoldchat-theme');if(saved==='dark')root.dataset.theme='dark';document.getElementById('theme-toggle').addEventListener('click',function(){const dark=root.dataset.theme==='dark';root.dataset.theme=dark?'light':'dark';localStorage.setItem('sameoldchat-theme',dark?'light':'dark')})})();</script></body></html>`))

type pageData struct {
	Messages        []messageView
	Conversations   []conversationView
	Channel         string
	Thread          messagePage
	ThreadTimestamp string
	CSRFToken       string
}
type membersData struct {
	Members   []memberView
	Current   memberView
	CSRFToken string
}
type messageView struct{ ID, AuthorID, Text, CreatedAt, Timestamp, Channel string }
type memberView struct {
	Name     string
	RealName string
	Profile  domain.UserProfile
}
type conversationView struct {
	ID          string
	Name        string
	Current     bool
	UnreadCount int
}

type searchData struct {
	Query    string
	Channel  string
	Messages []messageView
}

const progressiveEnhancementScript = `<script>(function(){var suppressRefresh=false;var events=null;document.addEventListener('submit',function(event){var form=event.target.closest('form');if(!form)return;if(!form.hasAttribute('hx-post')){suppressRefresh=true;if(events)events.close();return}event.preventDefault();suppressRefresh=true;if(events)events.close();form.classList.remove('is-error');fetch(form.getAttribute('hx-post'),{method:'POST',body:new FormData(form),headers:{'HX-Request':'true'}}).then(function(response){if(!response.ok)throw new Error('request failed');var redirect=response.headers.get('HX-Redirect');if(redirect){window.location.assign(redirect);return null}if(response.status===204)return null;return response.text()}).then(function(html){if(html===null)return;var target=document.querySelector(form.getAttribute('hx-target'));if(!target)throw new Error('update target missing');if(form.getAttribute('hx-swap')==='outerHTML')target.outerHTML=html;else target.insertAdjacentHTML('beforeend',html)}).catch(function(){form.classList.add('is-error')}).finally(function(){suppressRefresh=false})});if(window.EventSource){var cursor=sessionStorage.getItem('sameoldchat-last-event')||'';events=new EventSource('/events'+(cursor?'?last_event_id='+encodeURIComponent(cursor):''));var refresh=function(event){if(event.lastEventId)sessionStorage.setItem('sameoldchat-last-event',event.lastEventId);if(suppressRefresh||(document.activeElement&&document.activeElement.form))return;events.close();window.location.reload()};['message.created','message.updated','message.deleted'].forEach(function(type){events.addEventListener(type,refresh)})}})();</script>`

type messagePage struct {
	Messages []messageView
	Channel  string
}

func (h Handler) Register(mux *http.ServeMux) {
	if h.Login != nil {
		h.Login.Register(mux)
		mux.HandleFunc("GET /app/admin/auth", h.authAdminPage)
		mux.HandleFunc("GET /api/admin.auth.methods.list", h.authMethodsList)
		mux.HandleFunc("POST /api/admin.auth.methods.set", h.authMethodSet)
		mux.HandleFunc("POST /api/admin.auth.users.invite", h.authUserInvite)
		mux.HandleFunc("POST /api/admin.auth.users.create", h.authUserCreate)
		mux.HandleFunc("GET /api/admin.auth.users.list", h.authUsersList)
		mux.HandleFunc("POST /api/admin.auth.users.set", h.authUserSet)
	}
	mux.HandleFunc("GET /app", h.index)
	mux.HandleFunc("GET /app/search", h.search)
	mux.HandleFunc("GET /app/members", h.members)
	mux.HandleFunc("POST /app/profile", h.setProfile)
	mux.HandleFunc("POST /app/message", h.postMessage)
	mux.HandleFunc("POST /app/conversation/open", h.openConversation)
	mux.HandleFunc("POST /app/reaction", h.addReaction)
	mux.HandleFunc("POST /app/pin", h.addPin)
	mux.HandleFunc("POST /app/session/revoke", h.revokeSession)
	mux.HandleFunc("POST /logout", h.revokeSession)
}

func (h Handler) setCSRFCookie(w http.ResponseWriter, r *http.Request) {
	session, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(session.Value) == "" {
		return
	}
	http.SetCookie(w, auth.CSRFCookie(auth.CSRFToken(session.Value), 86400, h.CookieDomain))
}

func (h Handler) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	if err := auth.ValidateCSRF(r); err != nil {
		http.Error(w, "CSRF token is invalid", http.StatusForbidden)
		return false
	}
	return true
}

func (h Handler) search(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeSearchRead)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	h.setCSRFCookie(w, r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	results := domain.MessagePage{}
	if query != "" {
		results, err = h.Messages.Search(r.Context(), principal.WorkspaceID, principal.UserID, query, domain.PageRequest{Limit: 100})
		if err != nil {
			http.Error(w, "search unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	var output bytes.Buffer
	if err := searchTemplate.Execute(&output, searchData{Query: query, Channel: string(h.Channel), Messages: toViews(results.Messages)}); err != nil {
		http.Error(w, "search rendering unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(output.Bytes())
}

func (h Handler) members(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	h.setCSRFCookie(w, r)
	page, err := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 100})
	if err != nil {
		http.Error(w, "member store unavailable", http.StatusServiceUnavailable)
		return
	}
	members := make([]memberView, 0, len(page.Users))
	for _, user := range page.Users {
		members = append(members, memberView{Name: user.Name, RealName: user.RealName, Profile: user.Profile})
	}
	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		http.Error(w, "profile unavailable", http.StatusServiceUnavailable)
		return
	}
	var output bytes.Buffer
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		http.Error(w, "session unavailable", http.StatusUnauthorized)
		return
	}
	if err := membersTemplate.Execute(&output, membersData{Members: members, Current: memberView{Name: current.Name, RealName: current.RealName, Profile: current.Profile}, CSRFToken: auth.CSRFToken(sessionCookie.Value)}); err != nil {
		http.Error(w, "member rendering unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(output.Bytes())
}

func (h Handler) setProfile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		http.Error(w, "invalid profile form", http.StatusBadRequest)
		return
	}
	profile := domain.UserProfile{DisplayName: fields["display_name"], StatusText: fields["status_text"], StatusEmoji: fields["status_emoji"], Image24: fields["image_24"], Image32: fields["image_32"], Image48: fields["image_48"], Image72: fields["image_72"], Image192: fields["image_192"], Image512: fields["image_512"], Image1024: fields["image_1024"]}
	if _, err := h.Messages.SetUserProfile(r.Context(), principal.WorkspaceID, principal.UserID, profile); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, service.ErrInvalidProfile) {
			status = http.StatusBadRequest
		}
		http.Error(w, "profile update unavailable", status)
		return
	}
	http.Redirect(w, r, "/app/members", http.StatusSeeOther)
}

func (h Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	if _, err := h.Authenticator.Authenticate(r); err != nil {
		h.writeAuthError(w, err)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, auth.ErrNotAuthenticated)
		return
	}
	if err := h.SessionRevoker.RevokeSession(r.Context(), sessionCookie.Value); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "session revocation unavailable", status)
		return
	}
	cookie := auth.SessionCookie("", -1, h.CookieDomain)
	cookie.Expires = time.Unix(1, 0).UTC()
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h Handler) index(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		if errors.Is(err, auth.ErrNotAuthenticated) && h.Login != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h.writeAuthError(w, err)
		return
	}
	h.setCSRFCookie(w, r)
	channel := h.requestChannel(r)
	conversations, err := h.Messages.Conversations(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{Limit: 100})
	if err != nil {
		http.Error(w, "conversation store unavailable", http.StatusServiceUnavailable)
		return
	}
	page, err := h.Messages.History(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.PageRequest{Limit: 100})
	if err != nil {
		http.Error(w, "message store unavailable", http.StatusServiceUnavailable)
		return
	}
	views := make([]conversationView, 0, len(conversations.Conversations))
	for _, conversation := range conversations.Conversations {
		views = append(views, conversationView{ID: string(conversation.ID), Name: conversation.Name, Current: conversation.ID == channel, UnreadCount: conversation.UnreadCount})
	}
	if len(page.Messages) > 0 {
		last := page.Messages[len(page.Messages)-1]
		if _, err := h.Messages.MarkRead(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.NewMessageTimestamp(last.CreatedAt)); err != nil {
			http.Error(w, "read cursor unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	threadTimestamp := strings.TrimSpace(r.URL.Query().Get("thread"))
	var thread messagePage
	if threadTimestamp != "" {
		if _, err := domain.ParseMessageTimestamp(domain.MessageTimestamp(threadTimestamp)); err != nil {
			http.Error(w, "invalid thread", http.StatusBadRequest)
			return
		}
		replies, err := h.Messages.Replies(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(threadTimestamp), domain.PageRequest{Limit: 100})
		if err != nil {
			http.Error(w, "thread unavailable", http.StatusServiceUnavailable)
			return
		}
		thread = messagePage{Messages: toViews(replies.Messages), Channel: string(channel)}
	}
	var output bytes.Buffer
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		http.Error(w, "session unavailable", http.StatusUnauthorized)
		return
	}
	if err := pageTemplate.Execute(&output, pageData{Messages: toViews(page.Messages), Conversations: views, Channel: string(channel), Thread: thread, ThreadTimestamp: threadTimestamp, CSRFToken: auth.CSRFToken(sessionCookie.Value)}); err != nil {
		http.Error(w, "page rendering unavailable", http.StatusServiceUnavailable)
		return
	}
	rendered := output.String()
	if !strings.Contains(rendered, "</body>") {
		http.Error(w, "page rendering unavailable", http.StatusServiceUnavailable)
		return
	}
	rendered, err = normalizeSearchControl(rendered)
	if err != nil {
		http.Error(w, "page rendering unavailable", http.StatusServiceUnavailable)
		return
	}
	rendered, err = removeLegacyEventStream(rendered)
	if err != nil {
		http.Error(w, "page rendering unavailable", http.StatusServiceUnavailable)
		return
	}
	rendered = strings.Replace(rendered, "</body>", progressiveEnhancementScript+"</body>", 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(rendered))
}

func normalizeSearchControl(rendered string) (string, error) {
	const start = `<label class="search" aria-label="Search">`
	const end = `</label>`
	if strings.Count(rendered, start) != 1 {
		return "", errors.New("page search control is missing or duplicated")
	}
	startIndex := strings.Index(rendered, start)
	endOffset := strings.Index(rendered[startIndex+len(start):], end)
	if endOffset < 0 {
		return "", errors.New("page search control is not closed")
	}
	endIndex := startIndex + len(start) + endOffset
	content := rendered[startIndex+len(start) : endIndex]
	const input = `<input placeholder="Search the workspace" aria-label="Search the workspace">`
	if strings.Count(content, input) != 1 {
		return "", errors.New("page search input is missing or duplicated")
	}
	content = strings.Replace(content, input, `<input name="q" placeholder="Search the workspace" aria-label="Search the workspace">`, 1)
	control := `<form class="search" method="get" action="/app/search" aria-label="Search the workspace">` + content + `<button type="submit" hidden>Search</button></form>`
	return rendered[:startIndex] + control + rendered[endIndex+len(end):], nil
}

const legacyEventStream = `if(window.EventSource){const events=new EventSource('/events');const refresh=()=>window.location.reload();['message.created','message.updated','message.deleted'].forEach(type=>events.addEventListener(type,refresh))}`

func removeLegacyEventStream(rendered string) (string, error) {
	if strings.Count(rendered, legacyEventStream) != 1 {
		return "", errors.New("page event stream is missing or duplicated")
	}
	return strings.Replace(rendered, legacyEventStream, "", 1), nil
}

func (h Handler) postMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}
	message, err := h.Messages.Post(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), fields["text"], domain.MessageTimestamp(fields["thread_ts"]), "")
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, service.ErrInvalidMessage) || errors.Is(err, service.ErrInvalidTimestamp) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "unable to post message: "+err.Error(), status)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var output bytes.Buffer
		sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
		if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
			http.Error(w, "session unavailable", http.StatusUnauthorized)
			return
		}
		if err := pageTemplate.ExecuteTemplate(&output, "messages", pageData{Messages: toViews([]domain.Message{message}), Channel: string(h.requestChannel(r)), CSRFToken: auth.CSRFToken(sessionCookie.Value)}); err != nil {
			http.Error(w, "fragment rendering unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(output.Bytes())
		return
	}
	query := url.Values{"channel": {string(h.requestChannel(r))}}
	if thread := strings.TrimSpace(fields["thread_ts"]); thread != "" {
		query.Set("thread", thread)
	}
	http.Redirect(w, r, "/app?"+query.Encode(), http.StatusSeeOther)
}

func (h Handler) addReaction(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeReactionsWrite)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil || strings.TrimSpace(fields["name"]) == "" {
		http.Error(w, "invalid reaction", http.StatusBadRequest)
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		http.Error(w, "invalid message timestamp", http.StatusBadRequest)
		return
	}
	if err := h.Messages.AddReaction(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp, strings.TrimSpace(fields["name"])); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, service.ErrInvalidReaction) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "unable to add reaction", status)
		return
	}
	h.redirectToChannel(w, r)
}

func (h Handler) openConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		http.Error(w, "invalid conversation form", http.StatusBadRequest)
		return
	}
	users, err := normalizeUserIDs(fields["users"])
	if err != nil {
		http.Error(w, "invalid conversation users", http.StatusBadRequest)
		return
	}
	conversation, err := h.Messages.OpenConversation(r.Context(), principal.WorkspaceID, principal.UserID, users)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, service.ErrInvalidConversation) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "unable to open conversation", status)
		return
	}
	http.Redirect(w, r, "/app?"+url.Values{"channel": {string(conversation.ID)}}.Encode(), http.StatusSeeOther)
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

func (h Handler) addPin(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopePinsWrite)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		http.Error(w, "invalid message timestamp", http.StatusBadRequest)
		return
	}
	if err := h.Messages.AddPin(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "unable to pin message", status)
		return
	}
	h.redirectToChannel(w, r)
}

func (h Handler) redirectToChannel(w http.ResponseWriter, r *http.Request) {
	query := url.Values{"channel": {string(h.requestChannel(r))}}
	hx := r.Header.Get("HX-Request") == "true"
	if hx {
		w.Header().Set("HX-Redirect", "/app?"+query.Encode())
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/app?"+query.Encode(), http.StatusSeeOther)
}

func (h Handler) requestChannel(r *http.Request) domain.ConversationID {
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		return domain.ConversationID(channel)
	}
	return h.Channel
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
	if errors.Is(err, auth.ErrMissingScope) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.Error(w, "not authenticated", http.StatusUnauthorized)
}

func toViews(values []domain.Message) []messageView {
	result := make([]messageView, 0, len(values))
	for _, value := range values {
		result = append(result, messageView{ID: string(value.ID), AuthorID: string(value.AuthorID), Text: value.Text, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), Timestamp: string(domain.NewMessageTimestamp(value.CreatedAt)), Channel: string(value.Conversation)})
	}
	return result
}

const maxFormBody = 4 << 20

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
