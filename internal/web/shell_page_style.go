package web

// shellPageStyle is what a page that renders its own content inside the frame
// needs beyond shellStyle: the heading row and the Browse channels list.
const shellPageStyle = `<style>
.shell-main{padding:0 0 32px}
.shell-main .page-head{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:18px 24px 8px}
.shell-main .page-head h1{margin:0;font-size:22px}
.browse-filters{display:flex;flex-wrap:wrap;align-items:end;gap:10px;padding:8px 24px 12px;border-bottom:1px solid var(--line)}
.browse-filters label{display:grid;gap:4px;font-size:13px;font-weight:700}
.browse-filters select{padding:7px 9px;border:1px solid var(--field-line);border-radius:7px;background:var(--bg);color:var(--text);font:inherit}
.browse-search{flex:1 1 280px;display:flex !important;align-items:center;gap:8px;padding:0 10px;border:1px solid var(--field-line);border-radius:8px;background:var(--bg)}
.browse-search input{flex:1 1 auto;min-width:0;height:38px;border:0;outline:0;background:transparent;color:var(--text);font:inherit;font-weight:400}
.browse-search:focus-within{outline:3px solid var(--focus);outline-offset:1px}
.browse-check{display:flex !important;align-items:center;gap:6px;min-height:36px}
.browse-count{margin:0;padding:10px 24px;color:var(--muted);font-size:13px}
.browse-list{list-style:none;margin:0;padding:0 12px}
.browse-row{display:grid;grid-template-columns:minmax(0,1fr) auto;align-items:center;gap:2px 16px;padding:12px;border-bottom:1px solid var(--line);border-radius:6px}
.browse-row:hover{background:var(--hover)}
.browse-name{display:inline-flex;align-items:center;gap:6px;color:var(--text);font-weight:800;text-decoration:none}
.browse-name:hover{text-decoration:underline}
.browse-meta{grid-column:1;margin:0;color:var(--muted);font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.browse-joined{color:var(--ok);font-weight:700}
.browse-joined .icon{width:14px;height:14px}
.browse-actions{grid-column:2;grid-row:1/3;display:flex;gap:8px}
.browse-actions form{margin:0}
.browse-empty{padding:32px 12px;color:var(--muted);text-align:center}
@media(max-width:620px){.browse-row{grid-template-columns:minmax(0,1fr)}.browse-actions{grid-column:1;grid-row:auto}.browse-meta{white-space:normal}}
</style>`

// conversationShellStyle is the conversation page's part of the frame: the
// channel header with its tabs and bookmarks, the DM pane, the details dialog
// and preview mode.
const conversationShellStyle = `<style>
.shell .channel-header{display:block;min-height:0;padding:0;border-bottom:1px solid var(--line);background:var(--panel-strong)}
.channel-header-row{display:flex;align-items:center;gap:12px;min-height:48px;padding:6px 16px 4px 20px}
.shell .channel-identity{display:flex;align-items:center;gap:12px;flex:1 1 auto;min-width:0}
.shell .channel-title{flex:0 1 auto;margin:0 0 0 -8px;min-width:0;font-size:18px;font-weight:800;display:flex;white-space:nowrap}
.channel-name-button{display:inline-flex;align-items:center;gap:4px;min-width:0;max-width:100%;padding:3px 8px;border-radius:7px;color:var(--text);text-decoration:none}
.channel-name-button:hover{background:var(--hover)}
.channel-name-text{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.channel-kind-icon{display:inline-grid;place-items:center}
.channel-name-button>.icon:last-child{width:14px;height:14px;opacity:.75}
.shell .channel-meta{flex:1 1 0;min-width:0;margin:0;color:var(--muted);font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.shell .channel-actions{margin-left:auto;display:flex;align-items:center;gap:6px;flex:0 0 auto}
.facepile{display:inline-flex;align-items:center;gap:6px;height:30px;padding:0 8px 0 4px;border:1px solid var(--line);border-radius:7px;color:var(--text);font-weight:700;font-size:13px;text-decoration:none}
.facepile:hover{background:var(--hover)}
.faces{display:inline-flex}
.faces span{display:grid;place-items:center;width:22px;height:22px;margin-left:-4px;border:2px solid var(--panel-strong);border-radius:6px;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;font-size:10px;font-weight:800;text-transform:uppercase}
.faces span:first-child{margin-left:0}
.header-button{display:inline-flex;align-items:center;gap:2px;height:30px;padding:0 6px;border:1px solid var(--line);border-radius:7px;background:transparent;color:var(--text);cursor:pointer;list-style:none}
.header-button:hover,.menu[open]>.header-button{background:var(--hover)}
.header-button>.icon+.icon{width:12px;height:12px}
.header-button.is-live{border-color:var(--ok);color:var(--ok)}
.channel-overflow>.header-button{padding:0 5px}
.channel-tabs{display:flex;flex-wrap:wrap;align-items:center;gap:2px;padding:0 16px}
.channel-tabs>a{display:inline-flex;align-items:center;gap:6px;padding:6px 10px 8px;border-bottom:2px solid transparent;color:var(--muted);font-size:13px;font-weight:700;text-decoration:none;white-space:nowrap}
.channel-tabs>a .icon{width:15px;height:15px}
.channel-tabs>a:hover{color:var(--text)}
.channel-tabs>a[aria-current=page]{border-bottom-color:var(--action);color:var(--text)}
.bookmark-add{position:relative}
.bookmark-add>summary{display:grid;place-items:center;width:26px;height:26px;border-radius:6px;color:var(--muted);cursor:pointer;list-style:none}
.bookmark-add>summary::-webkit-details-marker{display:none}
.bookmark-add>summary:hover{background:var(--hover);color:var(--text)}
.bookmark-form{position:absolute;z-index:40;top:30px;left:0;display:grid;gap:6px;width:min(320px,calc(100vw - 32px));padding:12px;border:1px solid var(--line);border-radius:9px;background:var(--panel-strong);box-shadow:var(--shadow)}
.bookmark-form label{font-size:13px;font-weight:700}
.bookmark-form input{padding:7px 9px;border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);font:inherit}
.bookmarks-bar{display:flex;align-items:center;gap:4px;margin:0;padding:2px 16px 6px;list-style:none;overflow-x:auto;scrollbar-width:thin}
.bookmarks-bar li{display:inline-flex;align-items:center;flex:0 0 auto;border-radius:6px}
.bookmarks-bar li:hover{background:var(--hover)}
.bookmarks-bar a{display:inline-flex;align-items:center;gap:5px;max-width:220px;padding:3px 6px;color:var(--text);font-size:13px;text-decoration:none}
.bookmarks-bar a span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.bookmarks-bar a .icon{width:14px;height:14px;color:var(--muted)}
.bookmarks-bar form{display:flex;margin:0}
.bookmarks-bar button{display:grid;place-items:center;width:20px;height:20px;padding:0;border:0;border-radius:4px;background:transparent;color:var(--muted);cursor:pointer;opacity:0}
.bookmarks-bar li:hover button,.bookmarks-bar li:focus-within button{opacity:1}
.bookmarks-bar button .icon{width:12px;height:12px}
.channel-notices{display:grid;gap:6px;padding:0 20px}
.channel-notices:has(.notice,.action-feedback:not([hidden])){padding:0 20px 8px}
.shell .content{grid-template-rows:auto minmax(0,1fr) auto}
.pins-view .empty strong{display:block;margin-bottom:6px;color:var(--text)}
.pane-title{margin:0;flex:1 1 auto;color:var(--on-accent);font-size:18px;font-weight:800;padding:4px 6px}
.dm-list .side-row{margin:0 6px}
.dm-row .side-link{min-height:48px;align-items:center}
.dm-row .side-avatar{width:32px;height:32px;border-radius:8px;font-size:13px}
.dm-copy{display:grid;min-width:0;flex:1 1 auto}
.dm-copy small{color:var(--chrome-muted);font-size:12px;font-weight:400;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.dm-time{flex:0 0 auto;color:var(--chrome-muted);font-size:11px;font-weight:400}
.side-link[aria-current=page] .dm-copy small,.side-link[aria-current=page] .dm-time{color:var(--muted)}
.conversation-gate-actions{display:flex;flex-wrap:wrap;align-items:center;gap:8px}
.conversation-gate-actions form{margin:0}
.conversation-details{width:min(640px,calc(100vw - 32px));height:min(720px,calc(100vh - 32px))}
.conversation-details[open]{display:flex;flex-direction:column}
.conversation-details-head h2{display:flex;align-items:center;gap:6px;font-size:22px}
.details-type{padding:0 20px 8px}
.details-tabs{display:flex;flex-wrap:wrap;gap:4px;padding:0 20px;border-bottom:1px solid var(--line)}
.details-tabs [role=tab]{padding:8px 10px;text-decoration:none;border:0;border-bottom:2px solid transparent;background:transparent;color:var(--muted);font:inherit;font-weight:700;cursor:pointer}
.details-tabs [role=tab][aria-selected=true]{border-bottom-color:var(--action);color:var(--text)}
.details-tabs [role=tab]:hover{color:var(--text)}
.details-panel{flex:1 1 auto;display:grid;align-content:start;gap:14px;padding:16px 20px 20px;overflow:auto;background:var(--panel)}
.details-panel[hidden]{display:none}
.details-card{display:grid;gap:0;border:1px solid var(--line);border-radius:10px;background:var(--panel-strong);overflow:hidden}
.details-card>.conversation-setting,.details-card>form,.details-card>h3,.details-card>p{margin:0;padding:12px 16px}
.details-row{display:flex;align-items:flex-start;gap:12px;padding:12px 16px;border-bottom:1px solid var(--line)}
.details-row:last-child{border-bottom:0}
.details-row>div{flex:1 1 auto;min-width:0}
.details-row h3{margin:0 0 2px;font-size:14px}
.details-row p{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}
.details-row .icon{width:15px;height:15px;vertical-align:-2px}
.details-edit{border:0;background:transparent;color:var(--action);font:inherit;font-weight:700;cursor:pointer;padding:2px 4px;border-radius:5px}
.details-edit:hover{text-decoration:underline}
.muted-text{color:var(--muted)}
.details-notify{gap:10px;padding:12px 16px}
.details-notify>*{padding:0 !important}
.choice-inline{margin:0;padding:0;border:0;display:flex;flex-wrap:wrap;gap:6px 16px}
.choice-inline legend{width:100%;margin-bottom:6px;font-weight:800}
.choice-inline label,.toggle-row{display:inline-flex;align-items:center;gap:6px}
.details-leave{padding:4px 16px}
.details-leave form{padding:8px 0 !important}
.danger-link{border:0;background:transparent;color:var(--danger);font:inherit;font-weight:700;cursor:pointer;padding:0}
.danger-link:hover{text-decoration:underline}
.details-members-tools{display:flex;gap:8px}
.details-members-tools input{flex:1 1 auto;padding:8px 10px;border:1px solid var(--field-line);border-radius:7px;background:var(--bg);color:var(--text);font:inherit}
.conversation-details .conversation-members{display:grid;grid-template-columns:minmax(0,1fr);gap:2px;margin:0;padding:0;list-style:none}
.conversation-details .conversation-member{position:relative;display:flex;align-items:center;gap:10px;padding:6px 8px;border:0;border-radius:7px}
.conversation-details .conversation-member:hover{background:var(--hover)}
.conversation-details .conversation-member[hidden]{display:none}
.conversation-member-avatar{position:relative;display:grid;place-items:center;flex:0 0 auto;width:32px;height:32px;border-radius:7px;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;font-weight:800}
.conversation-member-avatar .presence-dot{border-color:var(--panel)}
.conversation-member-avatar .presence-dot.auto{display:none}
.conversation-member-name{flex:1 1 auto;min-width:0}
.member-menu{margin-left:auto}
.member-menu>summary{display:grid;place-items:center;width:28px;height:28px;border-radius:6px;color:var(--muted);cursor:pointer;opacity:0}
.conversation-member:hover .member-menu>summary,.conversation-member:focus-within .member-menu>summary,.member-menu[open]>summary{opacity:1}
@media(hover:none){.bookmarks-bar button,.member-menu>summary{opacity:1}}
.details-apps{list-style:none;margin:6px 0 0;padding:0;display:grid;gap:4px}
.details-edit textarea{min-height:90px;resize:vertical}
@media(max-width:800px){
.channel-header-row{padding:4px 8px 2px 12px;min-height:44px}
.shell .channel-meta,.facepile .faces{display:none}
.channel-tabs{padding:0 8px}
.bookmarks-bar{padding:0 8px 4px}
.bookmark-form{position:fixed;top:auto;left:16px;right:16px;width:auto;margin-top:6px}
.conversation-details{width:100vw;max-width:100vw;height:100dvh;max-height:100dvh;border-radius:0}
.details-row{flex-wrap:wrap}
}
@media(max-height:520px){
.channel-tabs,.bookmarks-bar{display:none}
.channel-header-row{min-height:40px}
}
</style>`
