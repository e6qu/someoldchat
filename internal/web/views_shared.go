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

	"github.com/sameoldchat/sameoldchat/internal/store"
)

// This file holds what the secondary views (Activity, Later, Unreads, Search,
// People, Files, Canvases, Lists, Notifications) share, so each view is built
// from the same parts rather than from a private copy of them:
//
//   - viewStyle, one visual vocabulary for rows, tabs, buttons, chips, menus
//     and avatars, in SameOldChat's own palette;
//   - rowLinkScript, which makes a whole row open what its primary link
//     opens, the way Slack's list rows do, without nesting interactive
//     content inside an anchor;
//   - the member profile panel (PROFILE-01): one server-rendered fragment and
//     one script, opened from People, search, Activity, messages and anything
//     else that marks a member with data-profile-user.
//
// Each view keeps its body in a named template ("<name>-view") that its own
// page renders inside the standalone bar, so the workspace shell can render
// the same body as a list pane.

// viewStyle is the shared stylesheet of the secondary views. Class names are
// prefixed v- so they cannot collide with the workspace page's own.
const viewStyle = `<style>
.v-page{width:min(980px,calc(100% - 32px));margin:18px auto 48px}
.v-head{display:flex;align-items:center;gap:8px;min-height:44px;margin:0 0 6px;flex-wrap:wrap}
.v-head h1,.v-head h2{margin:0 auto 0 0;font-size:22px;line-height:1.2}
.v-sub{margin:0 0 12px;color:var(--muted);font-size:14px}
.v-tabs{display:flex;gap:2px;margin:0 0 12px;border-bottom:1px solid var(--line);overflow-x:auto;scrollbar-width:none}
.v-tabs::-webkit-scrollbar{display:none}
.v-tabs a{flex:0 0 auto;display:inline-flex;align-items:center;gap:6px;padding:9px 11px;color:var(--muted);font-weight:700;font-size:14px;text-decoration:none;border-bottom:2px solid transparent;white-space:nowrap}
.v-tabs a:hover{color:var(--text)}
.v-tabs a[aria-current=page]{color:var(--text);border-bottom-color:var(--action)}
.v-count{display:inline-grid;place-items:center;min-width:20px;height:18px;padding:0 6px;border-radius:9px;background:var(--hover);color:var(--muted);font-size:11px;font-weight:800}
` + viewControlRules + `
.v-chips{display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin:0 0 12px}
.v-chip{display:inline-flex;align-items:center;gap:6px;min-height:30px;padding:0 11px;border:1px solid var(--field-line);border-radius:15px;background:var(--panel-strong);color:var(--text);font:inherit;font-size:13px;font-weight:700;text-decoration:none;cursor:pointer;white-space:nowrap}
.v-chip:hover{background:var(--hover)}
.v-chip[aria-pressed=true],.v-chip[aria-current=page],.v-chip.on{border-color:var(--action);background:var(--hover);color:var(--text);box-shadow:inset 0 0 0 1px var(--action)}
.v-chip select{border:0;background:transparent;color:inherit;font:inherit;font-weight:700;padding:0;max-width:180px}
.v-toggle{display:inline-flex;align-items:center;gap:8px;font-size:13px;font-weight:700;color:var(--text);cursor:pointer;text-decoration:none}
.v-toggle .v-switch{position:relative;width:30px;height:18px;border-radius:9px;background:var(--field-line);flex:0 0 auto}
.v-toggle .v-switch::after{content:"";position:absolute;top:2px;left:2px;width:14px;height:14px;border-radius:50%;background:#fff;transition:left .12s}
.v-toggle[aria-checked=true] .v-switch{background:var(--ok)}
.v-toggle[aria-checked=true] .v-switch::after{left:14px}
.v-list{margin:0;padding:0;list-style:none;border:1px solid var(--line);border-radius:10px;background:var(--panel-strong);overflow:hidden}
.v-row{position:relative;display:grid;grid-template-columns:auto minmax(0,1fr) auto;gap:4px 12px;align-items:start;padding:12px 14px;border-top:1px solid var(--line)}
.v-row:first-child{border-top:0}
.v-row[data-row-href]{cursor:pointer}
.v-row:hover,.v-row:focus-within{background:var(--hover)}
.v-row.unread .v-row-title{font-weight:800}
.v-row.unread::before{content:"";position:absolute;left:0;top:10px;bottom:10px;width:3px;border-radius:0 3px 3px 0;background:var(--action)}
.v-row-main{min-width:0}
.v-row-meta{display:flex;align-items:baseline;gap:6px;flex-wrap:wrap;color:var(--muted);font-size:12px;line-height:1.4}
.v-row-meta a{color:inherit;display:inline-block;min-height:24px;line-height:24px}
.v-row-title{margin:1px 0 0;color:var(--text);font-size:15px;font-weight:600;overflow-wrap:anywhere}
.v-row-title a{color:inherit;text-decoration:none}
.v-row-title a:hover{text-decoration:underline}
.v-row-text{margin:3px 0 0;color:var(--text);overflow-wrap:anywhere}
.v-row-text.clamp{display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden}
.v-row-time{color:var(--muted);font-size:12px;white-space:nowrap}
.v-row-side{display:flex;flex-direction:column;align-items:flex-end;gap:4px;min-height:32px}
.v-hover-actions{display:flex;gap:2px;padding:2px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow);opacity:0}
.v-row:hover .v-hover-actions,.v-row:focus-within .v-hover-actions,.v-hover-actions:has(details[open]){opacity:1}
@media(hover:none){.v-hover-actions{opacity:1;box-shadow:none}}
.v-hover-actions form{margin:0}
.v-avatar{position:relative;display:grid;place-items:center;width:36px;height:36px;flex:0 0 auto;overflow:hidden;border-radius:6px;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;font-size:15px;font-weight:800;text-transform:uppercase}
.v-avatar img{width:100%;height:100%;object-fit:cover}
.v-avatar.sm{width:24px;height:24px;border-radius:4px;font-size:11px}
.v-avatar.lg{width:72px;height:72px;border-radius:10px;font-size:28px}
.v-avatar.glyph{background:var(--hover);color:var(--text);font-size:18px;text-transform:none}
.v-avatar-badge{position:absolute;right:-4px;bottom:-4px;display:grid;place-items:center;width:18px;height:18px;border:2px solid var(--panel-strong);border-radius:50%;background:var(--panel-strong);font-size:11px}
.v-avatar-wrap{position:relative;width:36px;height:36px}
.v-empty{margin:0;padding:40px 20px;border:1px dashed var(--line);border-radius:10px;color:var(--muted);text-align:center}
.v-empty strong{display:block;margin-bottom:4px;color:var(--text);font-size:16px}
.v-field{display:grid;gap:5px;color:var(--muted);font-size:12px;font-weight:700}
.v-field input,.v-field select,.v-field textarea,.v-input{min-width:0;min-height:34px;padding:6px 10px;border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);font:inherit;font-size:14px}
.v-search{display:flex;align-items:center;gap:8px;min-width:0;padding:0 10px;border:1px solid var(--field-line);border-radius:8px;background:var(--bg)}
.v-search>span[aria-hidden]{color:var(--muted);font-size:18px;line-height:1}
.v-search input{flex:1 1 auto;min-width:0;min-height:36px;border:0;outline:0;background:transparent;color:var(--text);font:inherit}
.v-search:focus-within{box-shadow:0 0 0 3px color-mix(in srgb,var(--focus) 35%,transparent);border-color:var(--focus)}
.v-text p{margin:0}.v-text p+p{margin-top:4px}.v-text code{padding:1px 4px;border-radius:4px;background:var(--hover);font-size:13px}.v-text pre{margin:4px 0;padding:8px;border-radius:6px;background:var(--hover);white-space:pre-wrap}.v-text blockquote{margin:2px 0;padding-left:10px;border-left:3px solid var(--line)}.v-text mark{background:var(--mark-bg);color:inherit}
@media(max-width:650px){.v-page{width:calc(100% - 20px);margin-top:12px}.v-row{grid-template-columns:auto minmax(0,1fr);padding:11px 12px}.v-row-side{grid-column:2;flex-direction:row;align-items:center;justify-content:space-between}.v-head h2{font-size:20px}}
` + profilePanelStyle + `</style>`

// searchSuggestionStyle is the search dropdown's stylesheet. It is part of
// shellStyle, so the top bar's search and the search page's own query box on
// every page in the frame draw their suggestions the same way. Its colours
// are declared on the dropdown's own classes with enough specificity that a
// header's link colour (white on the purple bar) cannot bleed into it, which
// is how the search page's suggestions became white on white.
const searchSuggestionStyle = `.search-suggestions{position:absolute;z-index:30;top:calc(100% + 6px);left:0;right:0;max-height:min(420px,calc(70vh / var(--zoom, 1)));overflow:auto;padding:6px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow)}
.search-suggestions[hidden]{display:none}
.search-suggestions a.search-suggestion{display:grid;grid-template-columns:24px minmax(0,1fr) auto;align-items:center;gap:4px 10px;padding:7px 10px;border-radius:6px;color:var(--text);font-weight:400;text-decoration:none}
.search-suggestions a.search-suggestion:hover,.search-suggestions a.search-suggestion[aria-selected=true]{background:var(--hover);color:var(--text)}
.search-suggestion-icon{display:grid;place-items:center;width:24px;height:24px;overflow:hidden;border-radius:4px;background:var(--hover);color:var(--muted);font-size:13px;font-weight:800;text-transform:uppercase}
.search-suggestion-icon img{width:100%;height:100%;object-fit:cover}
.search-suggestion-label{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:700}
.search-suggestion-kind{color:var(--muted);font-size:12px}
.search-suggestion.query .search-suggestion-label{font-weight:400}`

// rowLinkScript lets a click anywhere on a [data-row-href] row open that
// address, which is how Slack's list rows behave. The row's own primary link
// stays the keyboard and assistive-technology path; a click on any control
// inside the row, a modified click, or a text selection is left alone.
//
// It also gives every .v-menu (a details disclosure) the behaviour of a
// menu: Escape closes the open one and returns focus to its button, a click
// elsewhere closes it, and opening one closes any other.
const rowLinkScript = `<script>(function(){
function openMenus(){return Array.prototype.slice.call(document.querySelectorAll('details.v-menu[open]'))}
document.addEventListener('keydown',function(event){if(event.key!=='Escape')return;var menus=openMenus();if(!menus.length)return;var menu=menus[menus.length-1];menu.open=false;var summary=menu.querySelector('summary');if(summary)summary.focus();event.stopPropagation()},true);
document.addEventListener('click',function(event){openMenus().forEach(function(menu){if(!menu.contains(event.target))menu.open=false})});
document.addEventListener('toggle',function(event){var menu=event.target;if(!menu.matches||!menu.matches('details.v-menu')||!menu.open)return;openMenus().forEach(function(other){if(other!==menu&&!other.contains(menu))other.open=false});var first=menu.querySelector('.v-menu-list a,.v-menu-list button,.v-menu-list input');if(first&&menu.querySelector('summary')===document.activeElement&&!first.matches('input'))first.focus()},true);
document.addEventListener('click',function(event){
if(event.defaultPrevented||event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
var row=event.target.closest('[data-row-href]');
if(!row||event.target.closest('a,button,input,select,textarea,summary,label,details,[contenteditable]'))return;
var selection=window.getSelection?String(window.getSelection()):'';
if(selection)return;
window.location.assign(row.getAttribute('data-row-href'));
});
})();</script>`

// liveFilterScript applies a filter form the moment it changes, which is how
// Slack's search chips, People filters and Files filters behave. A form marked
// data-live-filter="#region" is fetched with its current values (typing is
// debounced) and only that region is replaced; the address is updated so a
// refresh or a shared link shows the same filtered view. Without script, or
// if the fetch fails, the form is an ordinary GET form and submits normally.
const liveFilterScript = `<script>(function(){
var timers={};var pending=null;var serial=0;
function address(form){var params=new URLSearchParams();Array.prototype.forEach.call(form.elements,function(field){if(!field.name||field.disabled)return;if((field.type==='checkbox'||field.type==='radio')&&!field.checked)return;if(field.value==='')return;params.append(field.name,field.value)});var query=params.toString();return(form.getAttribute('action')||window.location.pathname)+(query?'?'+query:'')}
function run(form){var selector=form.getAttribute('data-live-filter');var target=document.querySelector(selector);if(!target||!window.fetch||!window.DOMParser){form.submit();return}var url=address(form);var mine=++serial;if(pending&&pending.abort)pending.abort();pending=window.AbortController?new AbortController():null;target.setAttribute('aria-busy','true');
fetch(url,{credentials:'same-origin',signal:pending?pending.signal:undefined}).then(function(response){if(!response.ok)throw new Error('status');return response.text()}).then(function(html){if(mine!==serial)return;var next=new DOMParser().parseFromString(html,'text/html').querySelector(selector);if(!next)throw new Error('region');target.innerHTML=next.innerHTML;Array.prototype.forEach.call(next.attributes,function(attribute){if(attribute.name.indexOf('data-')===0)target.setAttribute(attribute.name,attribute.value)});target.removeAttribute('aria-busy');if(window.history&&window.history.replaceState)window.history.replaceState(window.history.state,'',url);if(window.sameoldchatLocalTimes)window.sameoldchatLocalTimes(target);var status=document.getElementById('view-status');if(status)status.textContent=next.getAttribute('data-live-summary')||'Results updated.'}).catch(function(error){if(error&&error.name==='AbortError')return;target.removeAttribute('aria-busy');form.submit()})}
function typed(field){return field.type==='search'||field.type==='text'}
document.addEventListener('input',function(event){var form=event.target.form;if(!form||!form.hasAttribute('data-live-filter')||!typed(event.target))return;var key=form.getAttribute('data-live-filter');window.clearTimeout(timers[key]);timers[key]=window.setTimeout(function(){run(form)},250)});
document.addEventListener('change',function(event){var form=event.target.form;if(!form||!form.hasAttribute('data-live-filter')||typed(event.target))return;run(form)});
document.addEventListener('submit',function(event){var form=event.target;if(!form.hasAttribute||!form.hasAttribute('data-live-filter'))return;event.preventDefault();run(form)});
})();</script>`

// viewControlRules are the buttons, icon buttons, badges, presence dots,
// menus and screen-reader text every view and the profile panel draw with.
// The panel also opens over the conversation page, which does not carry
// viewStyle, so they are one block both stylesheets include rather than rules
// the panel silently lacked there.
const viewControlRules = `.v-btn{display:inline-flex;align-items:center;justify-content:center;gap:6px;min-height:32px;padding:0 12px;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);font:inherit;font-size:14px;font-weight:700;text-decoration:none;cursor:pointer;white-space:nowrap}
.v-btn:hover{background:var(--hover)}
.v-btn.primary{border-color:var(--ok);background:var(--ok);color:var(--on-strong)}
.v-btn.primary:hover{filter:brightness(1.08)}
.v-btn.danger{border-color:var(--danger);color:var(--danger)}
.v-btn.quiet{border-color:transparent;background:transparent;color:var(--muted)}
.v-btn.quiet:hover{background:var(--hover);color:var(--text)}
.v-icon{display:inline-grid;place-items:center;width:32px;height:32px;padding:0;border:0;border-radius:6px;background:transparent;color:var(--muted);font:inherit;font-size:16px;line-height:1;text-decoration:none;cursor:pointer}
.v-icon:hover,.v-icon[aria-expanded=true]{background:var(--hover);color:var(--text)}
.v-badge{display:inline-flex;align-items:center;padding:0 6px;border-radius:4px;background:var(--hover);color:var(--muted);font-size:11px;font-weight:800;letter-spacing:.02em;text-transform:uppercase;vertical-align:middle}
.v-presence{display:inline-block;width:9px;height:9px;border:2px solid var(--muted);border-radius:50%;vertical-align:middle}
.v-presence.active{border-color:var(--ok);background:var(--ok)}
.v-menu{position:relative;display:inline-block}
.v-menu>summary{list-style:none}
.v-menu>summary::-webkit-details-marker{display:none}
.v-menu-list{position:absolute;z-index:35;right:0;top:calc(100% + 4px);display:grid;min-width:200px;padding:6px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow)}
.v-menu-list.up{top:auto;bottom:calc(100% + 4px)}
.v-menu-list>*,.v-menu-list form>button{display:flex;align-items:center;gap:8px;width:100%;min-height:32px;padding:0 10px;border:0;border-radius:5px;background:transparent;color:var(--text);font:inherit;font-size:14px;text-align:left;text-decoration:none;cursor:pointer}
.v-menu-list form{margin:0;padding:0}
.v-menu-list>*:hover,.v-menu-list form>button:hover,.v-menu-list>*:focus-visible{background:var(--action);color:var(--on-strong)}
.v-menu-list hr{height:1px;min-height:0;margin:4px 0;padding:0;background:var(--line)}
.v-menu-list .danger{color:var(--danger)}
.v-menu-label{padding:4px 10px;color:var(--muted);font-size:12px;font-weight:800}
.v-sr{position:absolute;width:1px;height:1px;margin:-1px;padding:0;overflow:hidden;clip:rect(0 0 0 0);clip-path:inset(50%);white-space:nowrap;border:0}`

// profilePanelStyle is the member profile panel's own stylesheet. It is part
// of viewStyle, and the workspace page includes it on its own, because the
// panel opens over the timeline too.
const profilePanelStyle = `.profile-panel{position:fixed;z-index:60;top:0;right:0;bottom:0;display:flex;flex-direction:column;width:min(400px,calc(100vw / var(--zoom, 1)));border-left:1px solid var(--line);background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);overflow:hidden}
.profile-panel[hidden]{display:none}
.pp-head{display:flex;align-items:center;gap:8px;min-height:52px;padding:0 10px 0 18px;border-bottom:1px solid var(--line)}
.pp-head h2{margin:0 auto 0 0;font-size:18px}
.pp-head h2:focus{outline:0}
.pp-body{flex:1 1 auto;overflow:auto;padding:18px}
.pp-photo{display:grid;place-items:center;width:min(100%,240px);aspect-ratio:1;margin:0 auto 16px;overflow:hidden;border-radius:12px;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;font-size:88px;font-weight:800;text-transform:uppercase}
.pp-photo img{width:100%;height:100%;object-fit:cover}
.pp-name{margin:0;font-size:22px;font-weight:800;line-height:1.25;overflow-wrap:anywhere}
.pp-pronouns{color:var(--muted);font-size:15px;font-weight:600}
.pp-line{margin:4px 0 0;color:var(--text);font-size:15px;overflow-wrap:anywhere}
.pp-muted{color:var(--muted)}
.pp-facts{display:grid;gap:6px;margin:12px 0 0;padding:0;list-style:none;font-size:14px}
.pp-facts li{display:flex;align-items:center;gap:8px}
.pp-facts .fact-icon{width:18px;text-align:center;color:var(--muted)}
.pp-actions{display:flex;flex-wrap:wrap;gap:8px;margin:16px 0 4px}
.pp-actions form{margin:0}
.pp-section{margin-top:18px;padding-top:14px;border-top:1px solid var(--line)}
.pp-section h3{margin:0 0 8px;font-size:15px}
.pp-section dl{display:grid;gap:10px;margin:0}
.pp-section dt{color:var(--muted);font-size:12px;font-weight:700}
.pp-section dd{margin:2px 0 0;overflow-wrap:anywhere}
.pp-error{margin:0;padding:18px;color:var(--danger)}
@media(max-width:650px){.profile-panel{width:calc(100vw / var(--zoom, 1));border-left:0}}`

// profilePanelScript opens a member's profile in the side panel. It is the
// single entry point every surface uses: window.sameoldchatOpenProfile(id,
// opener) from script, or any element carrying data-profile-user (an author
// name, a People card, a search hit) from a click, as well as a rendered
// member mention (a.slack-mention with data-user-id). The element's own href
// stays a full-page fallback to People, so the profile is reachable without
// script and from a new tab.
//
// The panel is a complementary landmark, not a modal: Slack keeps the
// conversation usable while a profile is open. Focus moves to the panel's
// heading on open and back to the opener on close (Escape or the close
// button). It also answers data-copy-text buttons anywhere on the page, which
// is how "Copy member ID" and "Copy link" work.
const profilePanelScript = `<script>(function(){
var host=null,returnFocus=null,request=null,clock=0;
function ensure(){host=document.getElementById('profile-panel');if(!host){host=document.createElement('aside');host.id='profile-panel';host.className='profile-panel';host.hidden=true;document.body.appendChild(host)}host.setAttribute('aria-labelledby',host.id+'-heading');return host}
function tick(){if(!host)return;var node=host.querySelector('[data-profile-tz]');if(!node||!window.Intl)return;var out=node.querySelector('[data-profile-local-time]');try{out.textContent=new Intl.DateTimeFormat(undefined,{timeZone:node.getAttribute('data-profile-tz'),hour:'numeric',minute:'2-digit'}).format(new Date())}catch(error){}}
function settle(){window.clearInterval(clock);tick();clock=window.setInterval(tick,30000);if(window.sameoldchatLocalTimes)window.sameoldchatLocalTimes(host);var heading=host.querySelector('h2');if(heading)heading.focus()}
function close(){if(!host||host.hidden)return;if(request&&request.abort)request.abort();window.clearInterval(clock);host.hidden=true;host.textContent='';if(window.history&&window.history.replaceState&&/[?&]user=/.test(window.location.search)){var params=new URLSearchParams(window.location.search);params.delete('user');var query=params.toString();window.history.replaceState(window.history.state,'',window.location.pathname+(query?'?'+query:'')+window.location.hash)}if(returnFocus&&document.contains(returnFocus))returnFocus.focus();returnFocus=null}
function frame(message,failed){host.textContent='';var head=document.createElement('div');head.className='pp-head';var heading=document.createElement('h2');heading.id=host.getAttribute('aria-labelledby');heading.tabIndex=-1;heading.textContent='Profile';head.appendChild(heading);var shut=document.createElement('button');shut.type='button';shut.className='v-icon';shut.setAttribute('data-profile-close','');shut.setAttribute('aria-label','Close profile');shut.textContent='×';head.appendChild(shut);var note=document.createElement('p');note.className=failed?'pp-error':'pp-body pp-muted';note.setAttribute('role',failed?'alert':'status');note.textContent=message;host.appendChild(head);host.appendChild(note)}
function open(id,opener){if(!id)return;ensure();returnFocus=opener||document.activeElement;if(request&&request.abort)request.abort();request=window.AbortController?new AbortController():null;host.hidden=false;host.setAttribute('aria-busy','true');frame('Loading profile…',false);
fetch('/app/members/profile?user='+encodeURIComponent(id),{credentials:'same-origin',headers:{'X-SameOldChat-Fragment':'profile'},signal:request?request.signal:undefined}).then(function(response){return response.text().then(function(html){if(!response.ok&&!html)throw new Error();return html})}).then(function(html){host.innerHTML=html;host.removeAttribute('aria-busy');settle()}).catch(function(error){if(error&&error.name==='AbortError')return;host.removeAttribute('aria-busy');frame('The profile could not be loaded. Try again.',true);settle()})}
window.sameoldchatOpenProfile=open;
document.addEventListener('click',function(event){
var copy=event.target.closest('[data-copy-text]');
if(copy){event.preventDefault();var text=copy.getAttribute('data-copy-text');if(copy.hasAttribute('data-copy-url'))text=new URL(text,window.location.origin).toString();var done=copy.getAttribute('data-copy-done')||copy.getAttribute('data-copied')||'Copied.';var status=(copy.closest('#profile-panel')&&document.querySelector('[data-profile-status]'))||document.getElementById('shell-status')||document.getElementById('view-status');var report=function(message){if(status)status.textContent=message};var menu=copy.closest('details');if(menu)menu.open=false;if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(text).then(function(){report(done)},function(){report('Copy it by hand: '+text)})}else{report('Copy it by hand: '+text)}return}
if(event.target.closest('[data-profile-close]')){event.preventDefault();close();return}
if(event.defaultPrevented||event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
var trigger=event.target.closest('[data-profile-user],a.slack-mention[data-user-id]');
if(!trigger)return;
event.preventDefault();event.stopPropagation();var who=trigger.getAttribute('data-profile-user')||trigger.getAttribute('data-user-id');var menu=trigger.closest('details[open]');if(menu){menu.open=false;var toggle=menu.querySelector('summary');if(toggle){toggle.setAttribute('aria-expanded','false');trigger=toggle}}open(who,trigger);
},true);
document.addEventListener('keydown',function(event){if(event.key==='Escape'&&host&&!host.hidden&&(host.contains(document.activeElement)||document.activeElement===document.body)){var menu=host.querySelector('details[open]');if(menu){menu.open=false;menu.querySelector('summary').focus();return}event.preventDefault();close()}});
var initial=document.getElementById('profile-panel');if(initial&&!initial.hidden){host=initial;tick();clock=window.setInterval(tick,30000)}
})();</script>`

// faviconSVG is SameOldChat's own mark: a speech bubble on its purple.
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#611f69"/><path d="M17 15h30a7 7 0 0 1 7 7v15a7 7 0 0 1-7 7H31l-11 9v-9h-3a7 7 0 0 1-7-7V22a7 7 0 0 1 7-7z" fill="#fff"/><circle cx="23" cy="29.5" r="3.5" fill="#611f69"/><circle cx="32" cy="29.5" r="3.5" fill="#611f69"/><circle cx="41" cy="29.5" r="3.5" fill="#611f69"/></svg>`

// favicon answers the browser's automatic /favicon.ico request, which used to
// be a 404 logged on every workspace page. SVG is served under both names:
// every current browser renders an SVG favicon whatever the path says.
func (h Handler) favicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(faviconSVG))
}

// profileView is everything the profile panel shows about one member. It is
// built only from what the viewer is authorized to read: the e-mail address
// is present only for a viewer holding users:read.email, so a hidden field is
// absent from the HTML rather than hidden by CSS (PROFILE-01).
type profileView struct {
	ID            string
	Name          string
	RealName      string
	Title         string
	Pronouns      string
	Initial       string
	AvatarURL     string
	StatusDisplay template.HTML
	StatusText    string
	StatusUntil   string
	Presence      string
	PresenceLabel string
	Timezone      string
	LocalTime     string
	Email         string
	Phone         string
	RoleLabel     string
	IsBot         bool
	IsSelf        bool
	IsVIP         bool
	IsHidden      bool
	CanMessage    bool
	CanEdit       bool
	CSRFToken     string
	FilesURL      string
	SearchURL     string
	Fields        []profileFactView
}

type profileFactView struct {
	Label string
	Value string
	Link  string
}

// profilePanelPartial is the panel's markup. It is its own template so the
// fragment endpoint and the People page (which renders the panel open for
// /app/members?user=<id>) produce the same document.
const profilePanelPartial = `{{define "profile-panel"}}<div class="pp-head"><h2 id="profile-panel-heading" tabindex="-1">Profile</h2><button type="button" class="v-icon" data-profile-close aria-label="Close profile">×</button></div>
<div class="pp-body" data-profile-panel="{{.ID}}">
<div class="pp-photo" aria-hidden="true">{{if .AvatarURL}}<img src="{{.AvatarURL}}" alt="">{{else}}{{.Initial}}{{end}}</div>
<p class="pp-name">{{.Name}}{{if .Pronouns}} <span class="pp-pronouns">({{.Pronouns}})</span>{{end}}{{if .IsBot}} <span class="v-badge">App</span>{{end}}{{if .RoleLabel}} <span class="v-badge">{{.RoleLabel}}</span>{{end}}</p>
{{if .Title}}<p class="pp-line">{{.Title}}</p>{{end}}
{{if and .RealName (ne .RealName .Name)}}<p class="pp-line pp-muted">{{.RealName}}</p>{{end}}
<ul class="pp-facts">
{{if .StatusText}}<li><span class="fact-icon" aria-hidden="true">{{if .StatusDisplay}}{{.StatusDisplay}}{{else}}💬{{end}}</span><span>{{.StatusText}}{{if .StatusUntil}} <span class="pp-muted">· until <time datetime="{{.StatusUntil}}">{{.StatusUntil}}</time></span>{{end}}</span></li>{{else if .StatusDisplay}}<li><span class="fact-icon">{{.StatusDisplay}}</span></li>{{end}}
<li><span class="fact-icon"><span class="v-presence {{.Presence}}" aria-hidden="true"></span></span><span>{{.PresenceLabel}}</span></li>
{{if .Timezone}}<li data-profile-tz="{{.Timezone}}"><span class="fact-icon" aria-hidden="true">🕒</span><span><span data-profile-local-time>{{.LocalTime}}</span> local time</span></li>{{end}}
</ul>
<div class="pp-actions">
{{if .IsSelf}}{{if .CanEdit}}<a class="v-btn" href="/app/members#profile-heading">Edit profile</a>{{end}}
{{else}}{{if .CanMessage}}<form method="post" action="/app/conversation/open"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="users" value="{{.ID}}"><button class="v-btn primary" type="submit" aria-label="Message {{.Name}}">Message</button></form>
{{if not .IsBot}}<form method="post" action="/app/conversation/open"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="users" value="{{.ID}}"><input type="hidden" name="huddle" value="1"><button class="v-btn" type="submit" aria-label="Start a huddle with {{.Name}}"><span aria-hidden="true">🎧</span> Huddle</button></form>{{end}}{{end}}{{end}}
<details class="v-menu"><summary class="v-btn" role="button" aria-label="More actions for {{.Name}}"><span aria-hidden="true">⋮</span></summary><div class="v-menu-list">
<button type="button" data-copy-text="{{.ID}}" data-copy-done="Member ID copied.">Copy member ID</button>
<a href="{{.FilesURL}}">View files</a>
<a href="{{.SearchURL}}">Search messages from {{.Name}}</a>
{{if not .IsSelf}}<form method="post" action="/app/notifications/vips"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="target" value="{{.ID}}"><input type="hidden" name="add" value="{{if .IsVIP}}false{{else}}true{{end}}"><button type="submit">{{if .IsVIP}}Remove from VIPs{{else}}Add to VIPs{{end}}</button></form>
<form method="post" action="/app/people/hidden"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="target" value="{{.ID}}"><input type="hidden" name="hidden" value="{{if .IsHidden}}false{{else}}true{{end}}"><button type="submit">{{if .IsHidden}}Unhide {{.Name}}{{else}}Hide {{.Name}}{{end}}</button></form>{{end}}
</div></details>
</div>
<p class="v-sr" role="status" data-profile-status></p>
<section class="pp-section" aria-labelledby="profile-contact-heading"><h3 id="profile-contact-heading">Contact information</h3>{{if or .Email .Phone}}<dl>{{if .Email}}<div><dt>Email address</dt><dd><a href="mailto:{{.Email}}">{{.Email}}</a></dd></div>{{end}}{{if .Phone}}<div><dt>Phone</dt><dd><a href="tel:{{.Phone}}">{{.Phone}}</a></dd></div>{{end}}</dl>{{else}}<p class="pp-muted">No contact details are shared with you.</p>{{end}}</section>
{{if .Fields}}<section class="pp-section" aria-labelledby="profile-about-heading"><h3 id="profile-about-heading">About</h3><dl>{{range .Fields}}<div><dt>{{.Label}}</dt><dd>{{if .Link}}<a href="{{.Link}}" rel="noopener noreferrer" target="_blank">{{.Value}}</a>{{else}}{{.Value}}{{end}}</dd></div>{{end}}</dl></section>{{end}}
</div>{{end}}`

var profilePanelTemplate = template.Must(template.New("profile-panel-fragment").Funcs(templateFunctions).Parse(profilePanelPartial))

// memberProfile answers the profile panel fragment. A navigation (no
// fragment header) is sent to the People page with the panel open, so a
// copied or middle-clicked link still shows the profile.
func (h Handler) memberProfile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	user := strings.TrimSpace(r.URL.Query().Get("user"))
	if r.Header.Get("X-SameOldChat-Fragment") != "profile" {
		http.Redirect(w, r, "/app/members?"+url.Values{"user": {user}}.Encode(), http.StatusSeeOther)
		return
	}
	view, status, message := h.buildProfileView(r, principal, domain.UserID(user))
	secureHeaders(w, workspaceContentSecurityPolicy())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`<div class="pp-head"><h2 id="profile-panel-heading" tabindex="-1">Profile</h2><button type="button" class="v-icon" data-profile-close aria-label="Close profile">×</button></div><p class="pp-error" role="alert">` + template.HTMLEscapeString(message) + `</p>`))
		return
	}
	var output bytes.Buffer
	if err := profilePanelTemplate.ExecuteTemplate(&output, "profile-panel", view); err != nil {
		http.Error(w, "The profile could not be rendered.", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write(output.Bytes())
}

// buildProfileView resolves one member for the panel. It answers a status and
// a sentence instead of an error so both callers (the fragment and the People
// page) report a missing or unreadable member the same way, and neither turns
// a handled refusal into a 500.
func (h Handler) buildProfileView(r *http.Request, principal auth.Principal, id domain.UserID) (profileView, int, string) {
	if id == "" {
		return profileView{}, http.StatusBadRequest, "Choose a member to see their profile."
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, domain.ErrUserNotFound) {
			return profileView{}, http.StatusNotFound, "That member is not in this workspace."
		}
		return profileView{}, http.StatusServiceUnavailable, "The profile is temporarily unavailable. Try again."
	}
	if user.Deleted {
		return profileView{}, http.StatusNotFound, "That account has been deactivated."
	}
	emojiImages := map[string]string{}
	if customEmoji, emojiErr := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID); emojiErr == nil {
		emojiImages = customEmojiImages(customEmoji)
	}
	now := time.Now().UTC()
	name := displayName(user)
	view := profileView{
		ID: string(user.ID), Name: name, RealName: strings.TrimSpace(user.RealName),
		Title: user.Profile.Title, Pronouns: user.Profile.Pronouns, Phone: user.Profile.Phone,
		Initial: initial(name), AvatarURL: profileImageURL(user.Profile),
		StatusDisplay: statusEmojiDisplay(user.Profile.StatusEmoji, emojiImages), StatusText: user.Profile.StatusText,
		IsBot: user.IsBot(), IsSelf: user.ID == principal.UserID,
		CanMessage: principal.HasScope(auth.ScopeChannelsManage), CanEdit: principal.HasScope(auth.ScopeUsersWrite),
		FilesURL: "/app/files?" + url.Values{"from": {string(user.ID)}}.Encode(),
	}
	// Search by the handle a member would type; an ID reference is the
	// fallback for a handle search's own syntax cannot carry.
	if handle := strings.TrimSpace(user.Name); handle != "" && !strings.ContainsAny(handle, " \t") {
		view.SearchURL = "/app/search?" + url.Values{"q": {"from:@" + handle + " "}}.Encode()
	} else {
		view.SearchURL = "/app/search?" + url.Values{"q": {"from:<@" + string(user.ID) + "> "}}.Encode()
	}
	if !user.Profile.StatusExpiration.IsZero() && user.Profile.StatusText != "" {
		view.StatusUntil = user.Profile.StatusExpiration.UTC().Format(time.RFC3339)
	}
	switch {
	case user.IsBot():
		view.Presence, view.PresenceLabel = "active", "App"
	case viewPresence(user, view.IsSelf, now) == "away":
		view.Presence, view.PresenceLabel = "away", "Away"
	default:
		view.Presence, view.PresenceLabel = "active", "Active"
	}
	switch {
	case user.UltraRestricted:
		view.RoleLabel = "Single-channel guest"
	case user.Restricted:
		view.RoleLabel = "Guest"
	case user.PrimaryOwner:
		view.RoleLabel = "Workspace primary owner"
	case user.Role == domain.WorkspaceRoleOwner:
		view.RoleLabel = "Workspace owner"
	case user.Role == domain.WorkspaceRoleAdmin:
		view.RoleLabel = "Workspace admin"
	}
	if zone := strings.TrimSpace(user.Profile.Timezone); zone != "" {
		if location, zoneErr := time.LoadLocation(zone); zoneErr == nil {
			view.Timezone = zone
			view.LocalTime = now.In(location).Format("3:04 PM")
		}
	}
	if principal.HasScope(auth.ScopeUsersReadEmail) {
		view.Email = user.Email
	}
	if sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName); cookieErr == nil && strings.TrimSpace(sessionCookie.Value) != "" {
		view.CSRFToken = auth.CSRFToken(sessionCookie.Value)
	}
	if !view.IsSelf {
		if preferences, prefErr := h.Messages.MemberPreferences(r.Context(), principal.WorkspaceID, principal.UserID); prefErr == nil {
			view.IsHidden = domain.HiddenPeople(preferences)[user.ID]
		}
		if preferences, prefErr := h.Messages.WorkspaceNotificationPreferences(r.Context(), principal.WorkspaceID, principal.UserID); prefErr == nil {
			for _, vip := range preferences.VIPs {
				if vip == user.ID {
					view.IsVIP = true
				}
			}
		}
	}
	if definitions, fieldsErr := h.Messages.WorkspaceProfileFields(r.Context(), principal.WorkspaceID, principal.UserID); fieldsErr == nil && len(definitions) > 0 {
		if stored, valuesErr := h.Messages.UserProfileFields(r.Context(), principal.WorkspaceID, principal.UserID, user.ID); valuesErr == nil {
			values := make(map[domain.ProfileFieldID]string, len(stored))
			for _, value := range stored {
				values[value.FieldID] = value.Value
			}
			for _, definition := range definitions {
				value := strings.TrimSpace(values[definition.ID])
				if value == "" {
					continue
				}
				field := profileFactView{Label: definition.Label, Value: value}
				if definition.Type == domain.ProfileFieldLink && (strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://")) {
					field.Link = value
				}
				view.Fields = append(view.Fields, field)
			}
		}
	}
	return view, http.StatusOK, ""
}

// avatarURL and isBot come from the same cached lookup as the name, so a view
// that shows a member's face beside their name costs no extra read.
func (n *userNames) avatarURL(id domain.UserID) string { return n.entry(id).avatarURL }
func (n *userNames) isBot(id domain.UserID) bool       { return n.entry(id).bot }
