package web

// shellStyle lays out the frame every workspace page shares: the top bar, the
// rail, the pane beside it and the shared menus and dialogs. Its selectors are
// deliberately specific to the frame's own classes, because pages render
// inside it with style sheets of their own — several of which style bare
// `button` or `.search` — and neither may restyle the other.
const shellStyle = `<style>
` + searchSuggestionStyle + `
.icon-sprite{position:absolute;width:0;height:0;overflow:hidden}
.icon{width:18px;height:18px;flex:0 0 auto;display:inline-block;fill:none;stroke:currentColor;stroke-width:1.6;stroke-linecap:round;stroke-linejoin:round;vertical-align:middle}
.icon .fill,.icon-sprite .fill{fill:currentColor;stroke:none}
.shell{height:calc(100vh / var(--zoom, 1));height:calc(100dvh / var(--zoom, 1));display:grid;grid-template-columns:minmax(0,1fr);grid-template-rows:44px minmax(0,1fr);background:var(--chrome-top);overflow:hidden}
.shell .topbar{grid-row:1;display:flex;align-items:center;gap:10px;height:44px;padding:0 12px 0 78px;background:var(--chrome-top);color:var(--on-accent);box-shadow:none;border:0}
.shell .top-search{position:relative;flex:1 1 auto;min-width:0;max-width:720px;margin:0 auto;display:flex;align-items:center;gap:8px;height:30px;padding:0 10px;border:1px solid #ffffff5c;border-radius:8px;background:#ffffff1f;color:var(--on-accent)}
.shell .top-search:focus-within{background:var(--panel-strong);color:var(--text);border-color:var(--focus)}
.shell .top-search input[name=q]{flex:1 1 auto;min-width:0;height:100%;border:0;outline:0;background:transparent;color:inherit;font:inherit;font-size:14px}
.shell .top-search input[name=q]::placeholder{color:currentColor;opacity:.85}
.shell .top-search .search-suggestions{top:calc(100% + 6px)}
.top-actions{display:flex;align-items:center;gap:4px;flex:0 0 auto}
.top-button{display:grid;place-items:center;width:32px;height:30px;border-radius:7px;color:var(--on-accent);cursor:pointer;list-style:none}
.top-button::-webkit-details-marker{display:none}
.top-button:hover,.menu[open]>.top-button{background:#ffffff2b}
.shell .workspace{display:grid;grid-template-columns:70px 280px minmax(0,1fr);min-height:0;padding:0 6px 6px 0;background:var(--chrome-top)}
.shell .workspace.without-pane{grid-template-columns:70px minmax(0,1fr)}
.rail{grid-column:1;grid-row:1;display:flex;flex-direction:column;align-items:center;gap:6px;min-height:0;padding:4px 0 10px;color:var(--on-accent)}
.rail-team{display:grid;place-items:center;width:40px;height:40px;margin-bottom:6px;border-radius:9px;cursor:pointer;list-style:none}
.rail summary::-webkit-details-marker{display:none}
.team-icon{display:grid;place-items:center;width:36px;height:36px;border-radius:9px;background:var(--panel-strong);color:var(--chrome);font-weight:800;font-size:17px}
.rail-item{display:flex;flex-direction:column;align-items:center;gap:3px;width:62px;padding:0;border:0;background:transparent;color:var(--on-accent);text-decoration:none;font-size:11px;font-weight:700;cursor:pointer;list-style:none;position:relative}
html .rail-item[data-rail=files],html .menu-list a[data-more-tab]{display:none}
html[data-pref-name-display=display] .full-name{display:none}
.hidden-people{list-style:none;margin:0;padding:0;display:grid;gap:6px}.hidden-people li{display:flex;align-items:center;justify-content:space-between;gap:12px}.hidden-people form{margin:0}.hidden-people button{min-height:28px}
html[data-pref-nav-dms=false] .rail-item[data-rail=dms],html[data-pref-nav-activity=false] .rail-item[data-rail=activity],html[data-pref-nav-later=false] .rail-item[data-rail=later]{display:none}
html[data-pref-nav-files=true] .rail-item[data-rail=files]{display:flex}
html[data-pref-nav-dms=false] .menu-list a[data-more-tab=dms],html[data-pref-nav-activity=false] .menu-list a[data-more-tab=activity],html[data-pref-nav-later=false] .menu-list a[data-more-tab=later],html:not([data-pref-nav-files=true]) .menu-list a[data-more-tab=files]{display:flex}
html[data-pref-nav-labels=false] .rail-label{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
.rail-item>.icon{box-sizing:content-box;width:20px;height:20px;padding:8px;border-radius:9px}
.rail-item:hover>.icon,.menu[open]>.rail-item>.icon{background:#ffffff26}
.rail-item[aria-current=page]>.icon{background:#ffffff3d}
.rail-item:focus-visible{outline:0}.rail-item:focus-visible>.icon{outline:3px solid var(--focus-chrome);outline-offset:1px}
.rail-dot{position:absolute;top:2px;right:14px;width:9px;height:9px;border-radius:50%;background:#e8912d;box-shadow:0 0 0 2px var(--chrome-top)}
.rail-end{margin-top:auto;display:flex;flex-direction:column;align-items:center;gap:12px}
.rail-create-button{display:grid;place-items:center;width:36px;height:36px;border-radius:50%;background:#ffffff26;color:var(--on-accent);cursor:pointer;list-style:none}
.rail-create-button:hover,.menu[open]>.rail-create-button{background:#ffffff40}
.rail-me{display:block;cursor:pointer;list-style:none;border-radius:8px}
.self-avatar{position:relative;display:inline-grid;place-items:center;width:34px;height:34px;border-radius:8px;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;font-weight:800;text-transform:uppercase;font-size:14px;flex:0 0 auto}
.self-avatar img{width:100%;height:100%;border-radius:inherit;object-fit:cover}
.presence-dot{position:absolute;right:-3px;bottom:-3px;width:11px;height:11px;border-radius:50%;background:#2bac76;border:2px solid var(--chrome-top)}
.presence-dot.away{background:var(--chrome-top);border-color:#cfc3d0;box-shadow:inset 0 0 0 1px #cfc3d0}
.menu{position:relative}
.menu>summary{list-style:none}
.menu-list{position:absolute;z-index:60;display:none;min-width:250px;max-width:min(340px,calc(100vw / var(--zoom, 1) - 16px));max-height:min(560px,calc(100vh / var(--zoom, 1) - 60px));overflow:auto;padding:6px 0;border:1px solid var(--line);border-radius:9px;background:var(--panel-strong);color:var(--text);box-shadow:0 10px 32px #0000004d;font-size:14px;font-weight:400;text-align:left}
details[open]>.menu-list{display:grid}
.rail .menu-list{left:calc(100% + 8px);top:0}
.rail .rail-end .menu-list{top:auto;bottom:0}
.menu-end{right:0;top:calc(100% + 6px)}
.menu-list [role=menuitem],.menu-list [role=menuitemradio],.menu-list [role=menuitemcheckbox]{display:flex;align-items:center;gap:10px;width:100%;min-height:32px;padding:5px 16px;border:0;border-radius:0;background:transparent;color:var(--text);font:inherit;font-weight:400;text-align:left;text-decoration:none;cursor:pointer;list-style:none;box-sizing:border-box}
.menu-list [role=menuitem]::-webkit-details-marker{display:none}
.menu-list [role=menuitem]>span{min-width:0;overflow:hidden;text-overflow:ellipsis}
.menu-list [role=menuitem] small{margin-left:auto;color:inherit;opacity:.8;white-space:nowrap}
.menu-list [role=menuitem]:hover,.menu-list [role=menuitem]:focus,.menu-list [role=menuitemradio]:hover,.menu-list [role=menuitemradio]:focus,.submenu[open]>[role=menuitem]{background:var(--action);color:var(--on-strong);outline:0}
.menu-list [aria-checked=true]::after{content:"✓";margin-left:auto;font-weight:800}
.menu-list hr{margin:6px 0;border:0;border-top:1px solid var(--line)}
.menu-list .menu-form{display:contents}
.menu-list [role=group]{display:grid}
.menu-heading{margin:6px 16px 2px;color:var(--muted);font-size:12px;font-weight:700}
.menu-note{display:block;padding:5px 16px;color:var(--muted);font-size:13px}
.menu-identity{display:flex;align-items:center;gap:10px;padding:8px 16px 10px;margin-bottom:4px;border-bottom:1px solid var(--line)}
.menu-identity strong{display:block;font-size:15px}.menu-identity small{display:block;color:var(--muted)}
.menu-identity .team-icon{background:var(--chrome);color:var(--on-accent);width:32px;height:32px;font-size:15px}
.menu-identity .presence-dot{border-color:var(--panel-strong)}
.status-now{display:inline-grid;place-items:center;width:18px}
.submenu{position:relative}
.submenu>[role=menuitem]::after{content:"›";margin-left:auto;padding-left:8px;font-size:16px;line-height:1}
.submenu[open]>.menu-list{left:calc(100% - 4px);top:-6px}
.rail .rail-end .submenu[open]>.menu-list{top:auto;bottom:-6px}
.sidebar .menu-list{left:8px;top:calc(100% + 4px)}
.shell .sidebar{grid-column:2;min-height:0;display:flex;flex-direction:column;gap:0;overflow:hidden;padding:0;border-radius:9px 0 0 9px;background:var(--chrome);color:var(--chrome-muted)}
.sidebar-head{display:flex;align-items:center;gap:6px;min-height:52px;padding:6px 10px 6px 12px;border-bottom:1px solid var(--chrome-line)}
.sidebar-head .menu{flex:1 1 auto;min-width:0}
.workspace-button{display:flex;align-items:center;gap:4px;max-width:100%;padding:4px 6px;border-radius:6px;color:var(--on-accent);font-size:18px;font-weight:800;cursor:pointer;list-style:none}
.workspace-button>span{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.workspace-button:hover,.menu[open]>.workspace-button{background:#ffffff1f}
.compose-button{display:grid;place-items:center;flex:0 0 auto;width:34px;height:34px;border-radius:8px;background:var(--panel-strong);color:var(--chrome);text-decoration:none}
.compose-button:hover{filter:brightness(.92)}
.sidebar-scroll{flex:1 1 auto;min-height:0;overflow:auto;padding:8px 0 16px}
.sidebar-top{display:grid;gap:1px;padding:0 0 10px}
.side-section{display:grid;gap:1px;margin:0 0 10px}
.side-section-head{position:relative;display:flex;align-items:center;gap:2px;min-height:28px;padding:0 8px 0 6px}
.section-toggle{flex:1 1 auto;min-width:0;display:flex;align-items:center;gap:4px;border:0;border-radius:6px;background:transparent;color:var(--chrome-muted);font:inherit;font-size:14px;font-weight:600;text-align:left;padding:3px 6px;cursor:pointer}
.section-toggle:hover{background:#ffffff14}
.section-toggle .icon{width:14px;height:14px;transition:transform .12s}
.section-toggle[aria-expanded=true] .icon{transform:rotate(90deg)}
.section-toggle span{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.section-collapse{flex:1 1 auto;min-width:0;display:flex;margin:0}
.side-section-head .menu>summary,.side-row .menu>summary{display:grid;place-items:center;width:26px;height:26px;border-radius:6px;color:var(--chrome-muted);cursor:pointer;opacity:0}
.side-section-head:hover .menu>summary,.side-section-head:focus-within .menu>summary,.side-section-head .menu[open]>summary,.side-row:hover .menu>summary,.side-row:focus-within .menu>summary,.side-row .menu[open]>summary{opacity:1}
@media(hover:none){.side-section-head .menu>summary,.side-row .menu>summary{opacity:1}}
.side-section-head .menu>summary:hover,.side-row .menu>summary:hover{background:#ffffff26;color:var(--on-accent)}
.side-section-head .menu-list,.side-row .menu-list{left:auto;right:4px;top:calc(100% + 2px)}
.side-row{position:relative;display:flex;align-items:center;margin:0 8px;border-radius:6px}
.side-row:hover{background:#ffffff14}
.side-row[data-dragging]{opacity:.5}
.side-row.drop-target,.side-section.drop-target>.side-section-head{box-shadow:inset 0 0 0 2px var(--focus-chrome)}
.shell .side-link{display:flex;align-items:center;gap:8px;flex:1 1 auto;min-width:0;min-height:28px;padding:3px 10px;border:0;border-radius:6px;background:transparent;color:var(--chrome-muted);font:inherit;font-size:15px;line-height:1.3;text-align:left;text-decoration:none}
.sidebar-top .side-link{margin:0 8px;width:auto}
.shell .side-link:hover{background:transparent;color:var(--on-accent)}
.sidebar-top .side-link:hover{background:#ffffff14}
.shell .side-link[aria-current=page]{background:var(--panel-strong);color:var(--chrome);font-weight:700}
.side-row:has(.side-link[aria-current=page]){background:var(--panel-strong)}
.side-row:has(.side-link[aria-current=page]) .menu>summary{color:var(--chrome)}
.shell .side-link.is-unread{color:var(--on-accent);font-weight:800}
.shell .side-link.is-muted{color:#b3a2b5}
.shell .side-link.is-muted[aria-current=page]{color:var(--chrome)}
.shell .side-link.is-muted .side-text{font-style:normal}
.side-icon{display:grid;place-items:center;flex:0 0 auto;width:20px;height:20px}
.side-icon .icon{width:17px;height:17px}
.side-avatar{position:relative;display:grid;place-items:center;flex:0 0 auto;width:20px;height:20px;border-radius:5px;background:#ffffff3b;color:var(--on-accent);font-size:11px;font-weight:800;text-transform:uppercase}
.side-avatar img{width:100%;height:100%;border-radius:inherit;object-fit:cover}
.side-avatar .presence-dot{width:9px;height:9px;right:-3px;bottom:-3px;border-color:var(--chrome)}

.shell .badge{margin-left:auto;display:inline-grid;place-items:center;min-width:20px;height:18px;padding:0 6px;border-radius:9px;background:#c81e4f;color:#fff;font-size:12px;font-weight:800}
.shell .draft-badge{margin-left:auto;display:inline-grid;place-items:center;color:inherit}
.shell .draft-badge .icon{width:15px;height:15px}
.shell .draft-badge+.badge{margin-left:4px}
.side-text{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.shell .side-link[hidden],.shell .side-row[hidden],.shell .side-add[hidden],.shell .sidebar[hidden]{display:none}
.side-add{display:flex;align-items:center;gap:8px;width:calc(100% - 16px);margin:0 8px;min-height:28px;padding:3px 10px;border:0;border-radius:6px;background:transparent;color:var(--chrome-muted);font:inherit;font-size:15px;text-align:left;text-decoration:none;cursor:pointer;list-style:none}
.side-add:hover{background:#ffffff14;color:var(--on-accent)}
.side-add .side-icon{border-radius:5px;background:#ffffff26}
.side-empty{margin:0 8px;padding:4px 10px;color:var(--chrome-muted);font-size:13px}
.side-note{margin:8px 16px;color:var(--chrome-muted);font-size:12px}
.side-section[data-filter=unreads] .side-row:not([data-unread]):not(:has([aria-current=page])),.side-section[data-filter=mentions] .side-row:not([data-mention]):not(:has([aria-current=page])){display:none}
.shell .content{grid-column:3;min-width:0;min-height:0;border-radius:0 9px 9px 0;overflow:hidden;background:var(--panel-strong)}
.shell .workspace.without-pane .content,.shell .workspace.without-pane .shell-main{grid-column:2;border-radius:9px}
.shell .workspace:not(.without-pane) .shell-main{grid-column:3;border-radius:0 9px 9px 0}
.shell .shell-main{min-width:0;min-height:0;overflow:auto;background:var(--bg);color:var(--text)}
.shell .shell-main>.layout,.shell .shell-main>main{margin-top:0}
.shell-dialog{width:min(560px,calc(100vw / var(--zoom, 1) - 32px));max-height:min(720px,calc(100vh / var(--zoom, 1) - 32px));padding:0;border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);color:var(--text);box-shadow:0 24px 80px #0007;overflow:auto}
.shell-dialog::backdrop{background:#0009}
.dialog-head{display:flex;align-items:center;gap:12px;padding:18px 20px 10px}
.dialog-head h2{margin:0;font-size:20px;flex:1 1 auto}
.shell-dialog h2[tabindex="-1"]:focus{outline:0}
.dialog-close{display:grid;place-items:center;width:32px;height:32px;margin-left:auto;border:0;border-radius:7px;background:transparent;color:var(--muted);cursor:pointer}
.dialog-close:hover{background:var(--hover);color:var(--text)}
.dialog-body{display:grid;gap:10px;padding:8px 20px 12px}
.dialog-body label{font-weight:700}
.dialog-foot{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:8px;padding:12px 20px 18px}
.dialog-note{margin:0;color:var(--muted);font-size:13px;font-weight:400}
.dialog-step{margin:0;color:var(--muted);font-size:12px}
.shell-dialog input[type=text],.shell-dialog input[type=search],.shell-dialog input[type=datetime-local],.shell-dialog select,.shell-dialog textarea,.shell-page-form input[type=text],.shell-page-form select{width:100%;min-width:0;padding:8px 10px;border:1px solid var(--field-line);border-radius:7px;background:var(--bg);color:var(--text);font:inherit;font-weight:400}
.button,.shell-dialog .button{display:inline-flex;align-items:center;justify-content:center;gap:6px;min-height:36px;padding:0 16px;border:1px solid var(--field-line);border-radius:7px;background:var(--panel-strong);color:var(--text);font:inherit;font-weight:700;text-decoration:none;cursor:pointer}
.button:hover{background:var(--hover)}
.button.primary{border-color:var(--ok);background:var(--ok);color:var(--on-strong)}
.button.primary:hover{filter:brightness(1.08)}
.button-quiet{margin-right:auto;border:0;background:transparent;color:var(--action);font:inherit;font-weight:700;cursor:pointer;padding:0 6px}
.shell-dialog [hidden]{display:none !important}
.conversation-switcher{width:min(640px,calc(100vw / var(--zoom, 1) - 32px));overflow:hidden;display:none;flex-direction:column}
.conversation-switcher[open]{display:flex}
.switcher-head{display:flex;align-items:center;gap:10px;padding:12px 14px;border-bottom:1px solid var(--line)}
.switcher-head input{flex:1 1 auto;min-width:0;border:0;outline:0;background:transparent;color:var(--text);font:inherit;font-size:17px}
.switcher-head .dialog-close{margin:0}
.switcher-group{margin:10px 16px 4px;color:var(--muted);font-size:12px;font-weight:700}
.switcher-results{list-style:none;margin:0;padding:0 6px 6px;overflow:auto;max-height:min(420px,calc(60vh / var(--zoom, 1)))}
.switcher-results li[hidden]{display:none}
.switcher-option{display:grid;cursor:pointer;grid-template-columns:22px minmax(0,auto) auto minmax(0,1fr);align-items:center;gap:4px 10px;padding:7px 10px;border-radius:7px;color:var(--text);text-decoration:none}
.switcher-results li[aria-selected=true] .switcher-option{background:var(--action);color:var(--on-strong)}
.switcher-results li[aria-selected=true] .switcher-type,.switcher-results li[aria-selected=true] .switcher-context{color:inherit}
.switcher-icon{display:grid;place-items:center}
.switcher-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.switcher-name.unread{font-weight:800}
.switcher-type{color:var(--muted);font-size:12px;white-space:nowrap}
.switcher-context{color:var(--muted);font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.switcher-empty{margin:0;padding:0 16px}
.switcher-empty:not(:empty){padding:22px 16px;text-align:center;color:var(--muted)}
.switcher-hint{margin:0;padding:8px 16px;border-top:1px solid var(--line);color:var(--muted);font-size:12px}
.switcher-hint kbd,.keyboard-help kbd{border:1px solid var(--field-line);border-bottom-width:2px;border-radius:4px;padding:0 5px;background:var(--panel);font:600 11px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace}
.keyboard-help{width:min(720px,calc(100vw / var(--zoom, 1) - 28px))}
.keyboard-help-head{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,220px) auto;align-items:center;gap:10px;padding:14px 16px;border-bottom:1px solid var(--line)}
.keyboard-help-head h2{margin:0;font-size:1.1rem}
.keyboard-help-head input{width:100%;border:1px solid var(--field-line);border-radius:7px;background:var(--panel);color:var(--text);padding:7px 10px}
.keyboard-help-body{padding:6px 16px 16px}
.keyboard-help-body h3{margin:16px 0 6px;font-size:.82rem;text-transform:uppercase;letter-spacing:.06em;color:var(--muted)}
.keyboard-help-body dl{display:grid;gap:2px;margin:0}
.keyboard-help-body dl>div{display:grid;grid-template-columns:minmax(0,1fr) auto;align-items:baseline;gap:14px;padding:7px 8px;border-radius:6px}
.keyboard-help-body dl>div[hidden],.keyboard-help-body section[hidden]{display:none}
.keyboard-help-body dl>div:nth-child(odd){background:var(--hover)}
.keyboard-help-body dt{margin:0;min-width:0}.keyboard-help-body dt small{display:block;color:var(--muted);font-size:.78rem}
.keyboard-help-body dd{margin:0;white-space:nowrap}
.keyboard-help-empty{padding:26px;text-align:center;color:var(--muted)}
.preferences-dialog{width:min(820px,calc(100vw / var(--zoom, 1) - 32px));height:min(620px,calc(100vh / var(--zoom, 1) - 32px))}
.preferences-dialog[open]{display:flex;flex-direction:column}
.preferences-layout{display:grid;grid-template-columns:200px minmax(0,1fr);flex:1 1 auto;min-height:0}
.preferences-tabs{display:flex;flex-direction:column;gap:2px;padding:6px 10px 16px;border-right:1px solid var(--line)}
.preferences-tabs [role=tab]{display:flex;align-items:center;gap:10px;padding:7px 10px;border:0;border-radius:7px;background:transparent;color:var(--text);font:inherit;text-align:left;cursor:pointer}
.preferences-tabs [role=tab]:hover{background:var(--hover)}
.preferences-tabs [role=tab][aria-selected=true]{background:var(--action);color:var(--on-strong);font-weight:700}
.preferences-panels{min-height:0;overflow:auto;padding:0 24px 24px}
.preferences-panel h3{margin:6px 0 12px;font-size:18px}
.preferences-panel fieldset{margin:0 0 18px;padding:0;border:0;display:grid;gap:8px}
.preferences-panel legend{margin-bottom:8px;font-weight:800}
.preferences-panel label{display:flex;align-items:flex-start;gap:8px}
.preferences-panel input[type=radio],.preferences-panel input[type=checkbox]{margin-top:4px}
.preferences-panel .preference-select{min-height:36px;max-width:100%;padding:6px 8px;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text)}
.preferences-links{margin:0;padding-left:18px;display:grid;gap:6px}
.status-dialog{width:min(520px,calc(100vw / var(--zoom, 1) - 32px))}
.status-inputs{display:grid;grid-template-columns:150px minmax(0,1fr);gap:10px}
.status-inputs label,.status-clear,.status-custom{display:grid;gap:4px;font-weight:700}
.status-presets{margin:0;padding:0;border:0;display:grid;gap:2px}
.status-presets legend{margin-bottom:6px;color:var(--muted);font-size:13px;font-weight:700}
.status-presets button{display:flex;align-items:center;gap:10px;padding:6px 8px;border:0;border-radius:7px;background:transparent;color:var(--text);font:inherit;text-align:left;cursor:pointer}
.status-presets button:hover,.status-presets button:focus-visible{background:var(--hover)}
.status-presets small{color:var(--muted)}
.prefixed-input{display:flex;align-items:center;gap:6px;border:1px solid var(--field-line);border-radius:7px;background:var(--bg);padding:0 10px}
.prefixed-input:focus-within{outline:3px solid var(--focus);outline-offset:1px}
.prefixed-input input[type=text]{border:0 !important;outline:0;padding-left:0 !important;background:transparent !important}
.char-count{color:var(--muted);font-size:13px;font-weight:400}
.choice-list{margin:0;padding:0;border:0;display:grid;gap:10px}
.choice-list legend{margin-bottom:8px;font-weight:800}
.choice-list label{display:flex;align-items:flex-start;gap:10px;padding:10px 12px;border:1px solid var(--line);border-radius:8px;font-weight:400}
.choice-list label:has(input:checked){border-color:var(--action)}
.choice-list small{display:block;color:var(--muted)}
.people-results{list-style:none;margin:0;padding:4px;border:1px solid var(--line);border-radius:8px;max-height:200px;overflow:auto}
.people-results li{padding:6px 10px;border-radius:6px;cursor:pointer}
.people-results li[aria-selected=true],.people-results li:hover{background:var(--action);color:var(--on-strong)}
.people-chosen{list-style:none;margin:0;padding:0;display:flex;flex-wrap:wrap;gap:6px}
.people-chosen li{display:inline-flex;align-items:center;gap:4px;padding:2px 4px 2px 10px;border:1px solid var(--line);border-radius:14px;background:var(--panel)}
.people-chosen button{border:0;background:transparent;color:var(--muted);cursor:pointer;padding:2px 6px;border-radius:10px}
html:not(.js) .create-channel-form [data-step-next],html:not(.js) .create-channel-form [data-step-back],html:not(.js) .create-channel-form [data-step-skip],html:not(.js) [data-people-picker],html:not(.js) .preferences-tabs{display:none}
html:not(.js) .preferences-panel[hidden]{display:block}
.shell-page-form{max-width:640px;margin:24px auto;padding:0 20px}
.dialog-error{margin:0 20px 4px}
.shell-page-form [data-dialog-close]{display:none}
.nav-scrim{display:none}
@media(max-width:800px){
.shell{grid-template-rows:44px minmax(0,1fr);padding-bottom:56px}
.shell .topbar{padding:0 8px}
.shell .top-search{margin:0}
.shell .workspace,.shell .workspace.without-pane{grid-row:2;grid-template-columns:minmax(0,1fr);padding:0}
.shell .content,.shell .shell-main,.shell .workspace.without-pane .content{grid-column:1;grid-row:1;border-radius:0}
.rail{position:fixed;left:0;right:0;bottom:0;z-index:32;height:56px;flex-direction:row;justify-content:space-around;align-items:center;gap:0;padding:4px 2px;background:var(--chrome-top);border-top:1px solid var(--chrome-line)}
html:not(.js) .shell{height:auto;min-height:calc(100vh / var(--zoom, 1));overflow:visible}
html:not(.js) .shell .workspace{display:block}
html:not(.js) .shell .sidebar{max-height:none;border-radius:0}
.rail-team{display:none}
.rail-end{margin:0;flex-direction:row;gap:6px}
.rail-item{width:auto;min-width:44px;font-size:10px}
.rail-item>.icon{padding:4px 10px}
.rail .menu-list,.rail .rail-end .menu-list{position:fixed;left:8px;right:8px;top:auto;bottom:62px;max-width:none;max-height:calc(100vh / var(--zoom, 1) - 120px)}
.submenu[open]>.menu-list,.rail .rail-end .submenu[open]>.menu-list{position:static;margin:0 8px;box-shadow:none;max-width:none}
.self-avatar{width:30px;height:30px}
html.js .nav-toggle{display:grid;place-items:center;flex:0 0 auto;width:34px;height:34px;border:0;border-radius:7px;background:transparent;color:var(--on-accent)}
html.js .nav-toggle:hover{background:#ffffff2b}
html.js .nav-scrim{position:fixed;inset:44px 0 56px;z-index:30;border:0;background:#0008}
html.js .nav-scrim.is-open{display:block}
html.js .shell .sidebar{position:fixed;inset:44px auto 56px 0;z-index:31;width:min(320px,calc(100vw / var(--zoom, 1) - 48px));border-radius:0;transform:translateX(-105%);transition:transform .18s ease;box-shadow:var(--shadow)}
html.js .shell .sidebar.is-open{transform:translateX(0)}
.preferences-layout{grid-template-columns:minmax(0,1fr);grid-template-rows:auto minmax(0,1fr)}
.preferences-tabs{flex-direction:row;flex-wrap:wrap;border-right:0;border-bottom:1px solid var(--line);padding:6px 10px}
.status-inputs{grid-template-columns:minmax(0,1fr)}
}
@media(max-height:520px) and (max-width:800px){
.shell{grid-template-rows:40px minmax(0,1fr);padding-bottom:44px}
.shell .topbar{height:40px}
.rail{height:44px}
.rail-label{display:none}
html.js .shell .sidebar{inset:40px auto 44px 0}
html.js .nav-scrim{inset:40px 0 44px}
.rail .menu-list,.rail .rail-end .menu-list{bottom:50px}
}
@media(prefers-reduced-motion:reduce){.section-toggle .icon{transition:none}}
</style>`
