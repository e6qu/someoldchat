package web

// messageTokens are the message surface's own colours, layered on the shared
// light and dark palettes: the highlight a mention of the reader gets, the tint
// of a pinned message, and the code colours.
const messageLightTokens = `--mention-row:#fdf4d3;--mention-bar:#c99300;--mention-pill:#f7e39a;--mention-pill-text:#3d2e00;--mention-link:#0b5cad;--mention-link-bg:#e3eefb;--pinned-row:#fff9eb;--pinned-text:#7a5a00;--code-text:#a3123f;--code-bg:#f6f4f7;--quote-bar:#c9c3cc;--row-hover:#f6f3f7`
const messageDarkTokens = `--mention-row:#3b3217;--mention-bar:#e0b53a;--mention-pill:#6b5616;--mention-pill-text:#fff4cf;--mention-link:#8fd7f4;--mention-link-bg:#1d3445;--pinned-row:#2f2a1b;--pinned-text:#f0d27a;--code-text:#ff9db4;--code-bg:#2a2d32;--quote-bar:#5b6068;--row-hover:#24272b`

// messageStyle styles the message row, its toolbar and menus, and the
// dialogs its actions open. It is loaded after the workspace page's own
// sheets and supersedes their older message rules.
const messageStyle = `<style>
:root{` + messageLightTokens + `}
html[data-theme=dark]{` + messageDarkTokens + `}
@media(prefers-color-scheme:dark){html[data-theme=light]:not([data-theme-explicit]){` + messageDarkTokens + `}}
.timeline{padding:8px 0 12px}
#thread-messages{padding:0 0 8px}
.message{position:relative;display:grid;grid-template-columns:36px minmax(0,1fr);column-gap:8px;padding:6px 20px 6px 20px;border-radius:0;margin:0}
.message.is-continuation{padding-top:1px;padding-bottom:1px}
.message:hover,.message:focus-within{background:var(--row-hover)}
.message:focus{background:var(--row-hover);outline:2px solid var(--focus);outline-offset:-2px}
.message.mentions-me{background:var(--mention-row);box-shadow:inset 3px 0 0 var(--mention-bar)}
.message.is-pinned{background:var(--pinned-row)}
.message.is-arrival{background:var(--mark-bg)}
.message-context{grid-column:2;display:flex;align-items:center;gap:6px;margin:0 0 2px;color:var(--muted);font-size:12px}
.message-context a{color:var(--muted);font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:60ch}
.pinned-label{color:var(--pinned-text);font-weight:600}
.context-icon{width:14px;height:14px;flex:0 0 auto}
.message-gutter{grid-column:1;position:relative;min-height:1px}
.message-body{grid-column:2;min-width:0}
.message-gutter>.avatar{width:36px;height:36px;border-radius:6px;font-size:14px}
.message-gutter>.avatar.avatar-emoji{background:transparent;font-size:26px;line-height:36px}
.message-gutter>.avatar.avatar-emoji .standard-emoji{font-size:26px;line-height:36px;width:auto;height:auto}
.message-gutter>.avatar.avatar-emoji .custom-emoji{width:36px;height:36px}
.message.is-continuation>.message-gutter>.avatar,.message.is-continuation .message-head{display:none}
.gutter-time{display:block;visibility:hidden;width:44px;margin-left:-6px;padding-top:3px;text-align:right;color:var(--muted);font-size:11px;line-height:18px;white-space:nowrap}
.message.is-continuation:hover .gutter-time,.message.is-continuation:focus-within .gutter-time,.message.is-continuation:focus .gutter-time{visibility:visible}
.message-head{display:flex;align-items:center;flex-wrap:wrap;gap:0 8px;line-height:1.35}
.message-head .author{font-weight:800}
/* 24px tall: the timestamp is a link, and WCAG 2.2 target-size measures it
   against the mention or link that can sit on the line below. */
.message-head a.time{display:inline-flex;align-items:center;min-height:24px;padding:0;margin:0;border-radius:3px;color:var(--muted);text-decoration:none;font-size:12px}
.message-head a.time:hover{text-decoration:underline;background:transparent}
.system-message{padding-top:3px;padding-bottom:3px}
.system-message .avatar-small{width:20px;height:20px;margin:1px 0 0 16px;border-radius:4px;font-size:10px}
.system-message>.message-body{grid-column:2}
.system-text{margin:0;color:var(--muted);font-size:14px}
.system-text .author{color:var(--text);font-weight:700}
.system-text .time{margin-left:4px;font-size:12px}
.broadcast-label{margin:0 0 2px;color:var(--muted);font-size:12px}
.edited-label{color:var(--muted);font-size:12px;white-space:nowrap}
.message-text{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.46}
.message-text.jumbo .standard-emoji{font-size:32px;line-height:40px;width:auto;height:auto;vertical-align:middle}
.message-text.jumbo .custom-emoji{width:32px;height:32px;vertical-align:middle}
.emoji-code,.message-image-fallback{display:none}
html[data-pref-emoji-as-text=true] .message-text .custom-emoji{display:none}
html[data-pref-emoji-as-text=true] .message-text .emoji-code,html[data-pref-inline-media=false] .message-image-fallback{display:inline}
html[data-pref-emoji-as-text=true] .message-text .standard-emoji{font-size:0}
html[data-pref-emoji-as-text=true] .message-text .standard-emoji::after{content:attr(aria-label);font-size:15px;line-height:22px}
html[data-pref-jumbomoji=false] .message-text.jumbo .standard-emoji{font-size:18px;line-height:20px}
html[data-pref-jumbomoji=false] .message-text.jumbo .custom-emoji{width:20px;height:20px;vertical-align:-4px}
html[data-pref-inline-media=false] .message-image{display:none}
html[data-pref-link-previews=false] .message-attachment.is-unfurl{display:none}
html[data-pref-underline-links=true] .message-text a,html[data-pref-underline-links=true] .message-attachment a{text-decoration:underline}
html[data-pref-reduce-motion=true] *,html[data-pref-reduce-motion=true] *::before,html[data-pref-reduce-motion=true] *::after{animation:none!important;transition:none!important;scroll-behavior:auto!important}
.message-text blockquote,.formatted-text blockquote{margin:4px 0;padding:0 0 0 12px;border-left:4px solid var(--quote-bar);white-space:pre-wrap}
.message-text pre,.formatted-text pre,.dialog-preview pre{margin:4px 0;padding:8px 10px;border:1px solid var(--line);border-radius:4px;background:var(--code-bg);font:12px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;overflow-x:auto}
.message-text pre code,.formatted-text pre code{border:0;padding:0;background:transparent;color:inherit;font:inherit}
.message-text code,.formatted-text code{padding:1px 3px;border:1px solid var(--line);border-radius:3px;background:var(--code-bg);color:var(--code-text);font:12px ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.message-text ul,.message-text ol,.formatted-text ul,.formatted-text ol{margin:2px 0;padding-left:26px;white-space:normal}
.message-text li,.formatted-text li{white-space:pre-wrap}
.slack-mention{display:inline;padding:0 2px;border-radius:3px;background:var(--mention-link-bg);color:var(--mention-link);font-weight:600;text-decoration:none}
a.slack-mention:hover{text-decoration:underline}
.slack-mention[data-self],.slack-mention.mention-broadcast{background:var(--mention-pill);color:var(--mention-pill-text)}
.message-content[hidden],.message-editor[hidden]{display:none}
.message-editor{margin:4px 0 2px;padding:8px;border:1px solid var(--field-line);border-radius:8px;background:var(--panel-strong)}
.message-editor textarea{display:block;width:100%;min-height:44px;max-height:40vh;resize:vertical;border:0;outline:0;background:transparent;color:var(--text);font:inherit;line-height:1.46}
.message-editor:focus-within{border-color:var(--focus);box-shadow:0 0 0 1px var(--focus)}
.editor-foot{display:flex;align-items:center;justify-content:flex-end;gap:8px;margin-top:6px}
.editor-hint{margin-right:auto;color:var(--muted);font-size:12px}
.editor-hint kbd{font:inherit;font-weight:700}
.editor-button,.dialog-button{display:inline-flex;align-items:center;min-height:32px;padding:0 14px;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);font-weight:700;font-size:14px;text-decoration:none;cursor:pointer}
.editor-button:hover,.dialog-button:hover{background:var(--hover)}
.editor-button.primary,.dialog-button.primary{border-color:var(--ok);background:var(--ok);color:var(--on-strong)}
.dialog-button.danger{border-color:var(--danger);background:var(--danger);color:var(--on-strong)}
.reaction-picker-form{display:none}
.reactions{display:flex;flex-wrap:wrap;align-items:center;gap:4px;margin:4px 0 2px}
.reaction-pill{display:contents}
.reactions .chip{display:inline-flex;align-items:center;gap:4px;height:24px;padding:0 7px;border:1px solid transparent;border-radius:12px;background:var(--hover);color:var(--text);font-size:12px;cursor:pointer}
.reactions .chip:hover{border-color:var(--field-line);background:var(--panel-strong)}
.reactions .chip[aria-pressed=true]{border-color:var(--focus);background:var(--mention-link-bg);color:var(--mention-link)}
.reactions .chip .standard-emoji{font-size:15px;line-height:18px;width:auto;height:auto}
.reactions .chip .custom-emoji{width:16px;height:16px}
.reactions .chip-count{font-weight:700;font-variant-numeric:tabular-nums}
.add-reaction-chip{visibility:hidden;color:var(--muted)}
.add-reaction-chip .action-icon{width:16px;height:16px}
.message:hover .add-reaction-chip,.message:focus-within .add-reaction-chip{visibility:visible}
.thread-summary{display:inline-flex;align-items:center;gap:8px;max-width:100%;margin:4px 0 0;padding:4px 8px 4px 4px;border:1px solid transparent;border-radius:6px;color:var(--text);font-size:13px;text-decoration:none}
.thread-summary:hover,.thread-summary:focus-visible{border-color:var(--line);background:var(--panel-strong)}
.thread-avatars{display:inline-flex;gap:4px}
.avatar.avatar-tiny{width:24px;height:24px;border-radius:4px;font-size:11px}
.thread-count{color:var(--mention-link);font-weight:700}
.thread-last-reply{color:var(--muted)}
.thread-view{display:none;color:var(--muted)}
.thread-summary:hover .thread-last-reply,.thread-summary:focus-visible .thread-last-reply{display:none}
.thread-summary:hover .thread-view,.thread-summary:focus-visible .thread-view{display:inline}
.day-separator{position:sticky;top:0;z-index:4;display:flex;align-items:center;justify-content:center;height:28px;margin:10px 0 2px;pointer-events:none}
.day-separator::before{content:"";position:absolute;left:0;right:0;top:50%;height:1px;background:var(--line)}
.day-separator::after{display:none}
.day-pill{position:relative;display:inline-flex;align-items:center;height:26px;padding:0 14px;border:1px solid var(--line);border-radius:13px;background:var(--bg);color:var(--text);font-size:13px;font-weight:700;pointer-events:auto}
.unread-divider{display:flex;align-items:center;gap:0;margin:6px 0;color:var(--danger);font-size:12px;font-weight:700}
.unread-divider::before{content:"";flex:1;height:1px;background:var(--danger)}
.unread-divider::after{display:none}
.unread-divider span{padding:0 6px;background:var(--bg)}
.thread-replies-divider{display:flex;align-items:center;gap:10px;margin:4px 20px 6px;color:var(--muted);font-size:13px}
.thread-replies-divider::after{content:"";flex:1;height:1px;background:var(--line)}
.message-actions{position:absolute;z-index:5;top:-16px;right:18px;display:none;align-items:center;gap:1px;padding:2px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:0 2px 6px #0000001f}
.message:hover>.message-actions,.message:focus>.message-actions,.message:focus-within>.message-actions,.message-actions:has(details[open]){display:flex}
.message.is-editing>.message-actions{display:none}
.message-actions form{display:contents}
.message-action,.message-actions a.message-action,.message-actions button.message-action,.message-actions summary.message-action{display:inline-flex;align-items:center;justify-content:center;width:32px;height:32px;min-height:0;margin:0;padding:0;border:0;border-radius:6px;background:transparent;color:var(--muted);font-size:12px;text-decoration:none;cursor:pointer;list-style:none}
.message-action::-webkit-details-marker{display:none}
.message-action:hover,.message-actions a.message-action:hover,.message-actions button.message-action:hover,.message-actions summary.message-action:hover{background:var(--hover);color:var(--text);text-decoration:none}
.message-action[aria-pressed=true]{color:var(--action)}
.quick-reaction .standard-emoji{font-size:17px;line-height:20px;width:auto;height:auto}
.quick-reaction .custom-emoji{width:18px;height:18px}
.message-more{position:relative;display:inline-block}
.message-more>summary{list-style:none}
.message-menu,.message-submenu,.file-menu{display:grid;min-width:260px;max-width:min(340px,calc(100vw - 16px));padding:6px 0;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);text-align:left}
.message-more>.message-menu{position:absolute;z-index:40;top:36px;right:0;max-height:min(70vh,520px);overflow:auto}
.message-menu.is-fixed,.message-submenu.is-fixed{position:fixed;top:auto;right:auto}
.message-menu form{display:block}
.message-menu [role=menuitem],.message-submenu [role=menuitem],.file-menu summary{display:flex;align-items:center;justify-content:space-between;gap:16px;width:100%;min-height:30px;margin:0;padding:4px 20px;border:0;border-radius:0;background:transparent;color:var(--text);font:inherit;font-size:14px;font-weight:400;text-align:left;text-decoration:none;cursor:pointer;list-style:none;white-space:nowrap}
.message-menu [role=menuitem]::-webkit-details-marker,.file-menu summary::-webkit-details-marker{display:none}
.message-menu [role=menuitem]:hover,.message-menu [role=menuitem]:focus,.message-submenu [role=menuitem]:hover,.message-submenu [role=menuitem]:focus,.file-menu summary:hover{background:var(--focus);color:var(--on-strong);outline:0}
.message-menu [role=menuitem] small{display:block;color:inherit;opacity:.8;font-size:12px}
.message-menu [role=menuitem] span{min-width:0;overflow:hidden;text-overflow:ellipsis}
.message-menu kbd{color:inherit;opacity:.7;font:12px inherit;font-family:inherit}
.message-menu .danger{color:var(--danger)}
.message-menu .danger:hover,.message-menu .danger:focus{background:var(--danger);color:var(--on-strong)}
.menu-separator{height:1px;margin:6px 0;background:var(--line)}
.menu-submenu{position:relative}
.menu-submenu>.message-submenu{position:absolute;z-index:41;top:-6px;right:calc(100% - 4px)}
.menu-submenu>.message-submenu.is-fixed{position:fixed;right:auto}
.message-menu form.message-submenu{display:grid}
.submenu-arrow{font-size:16px;line-height:1}
.reminder-custom>summary{list-style:none}
.reminder-custom-fields{display:grid;gap:6px;padding:6px 20px 8px}
.reminder-custom-fields label{display:grid;gap:2px;font-size:12px;color:var(--muted)}
.reminder-custom-fields input{border:1px solid var(--field-line);border-radius:5px;padding:4px 6px;background:var(--panel-strong);color:var(--text)}
.reminder-custom-fields button{justify-self:start;border:1px solid var(--ok);border-radius:6px;background:var(--ok);color:var(--on-strong);padding:4px 10px;font-weight:700}
.message-files{display:grid;gap:6px;margin:4px 0;max-width:480px}
.message-file{position:relative;display:flex;align-items:center;gap:10px;padding:10px 12px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong)}
.message-file.is-image{display:grid;justify-items:start;align-content:start;gap:4px;min-height:64px;padding:0;border:0;background:transparent}
.file-image-name{color:var(--muted);font-size:12px}
.message-image-link{display:block;border-radius:8px;overflow:hidden;border:1px solid var(--line)}
.message-image{display:block;max-width:min(360px,100%);max-height:280px;height:auto;border-radius:0}
.file-actions{position:absolute;z-index:6;top:6px;right:6px;display:none;align-items:center;gap:1px;padding:2px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:0 2px 6px #0000001f}
.message-file.is-image .file-actions{top:24px}
.message-file:hover .file-actions,.message-file:focus-within .file-actions,.file-actions:has(details[open]){display:flex}
.file-more{position:relative}
.file-more>summary{list-style:none}
.file-more>.file-menu{position:absolute;z-index:40;top:36px;right:0;min-width:280px}
.file-menu details>form{display:grid;gap:6px;padding:6px 20px 10px}
.file-menu textarea{width:100%;border:1px solid var(--field-line);border-radius:5px;background:var(--panel-strong);color:var(--text)}
.file-menu button{justify-self:start;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:4px 10px;font-weight:700}
.file-menu button.danger{border-color:var(--danger);background:var(--danger);color:var(--on-strong)}
.file-menu p{margin:0;font-size:13px}
.message-block{white-space:normal;overflow-wrap:anywhere}
.block-text,.message-attachment .attachment-text{white-space:pre-wrap}
.message-block.header{font-size:16px;font-weight:800;line-height:1.35}
.message-block.context{display:flex;flex-wrap:wrap;gap:4px;color:var(--muted);font-size:12px}
.message-blocks{gap:6px;margin:4px 0}
.message-block-fields{grid-template-columns:repeat(2,minmax(0,1fr));gap:8px 16px;margin:8px 0 0;max-width:600px}
.message-block-fields li{white-space:normal}
.block-action.style-primary{border-color:var(--ok);background:var(--ok);color:var(--on-strong)}
.block-action.style-primary:hover{filter:brightness(.92);background:var(--ok)}
.block-action.style-danger{border-color:var(--danger);background:var(--panel-strong);color:var(--danger)}
.block-action.style-danger:hover{background:var(--danger);color:var(--on-strong)}
.emoji-picker{position:fixed;z-index:60;display:flex;flex-direction:column;width:min(360px,calc(100vw - 16px));height:min(440px,calc(100vh - 16px));border:1px solid var(--line);border-radius:10px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);overflow:hidden}
.emoji-picker[hidden]{display:none}
.emoji-picker>*{flex:0 0 auto}
.emoji-picker-head{display:flex;gap:6px;align-items:center;padding:10px 10px 6px}
.emoji-search{flex:1 1 auto}
.emoji-search input{width:100%;height:32px;padding:0 10px;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text)}
.emoji-tone-button{width:32px;height:32px;border:1px solid var(--line);border-radius:6px;background:transparent;font-size:18px}
.emoji-tone-options{display:flex;gap:2px;justify-content:flex-end;padding:0 10px 6px}
.emoji-tone-options[hidden]{display:none}
.emoji-tone-options button{width:30px;height:30px;border:1px solid transparent;border-radius:6px;background:transparent;font-size:18px}
.emoji-tone-options button[aria-checked=true]{border-color:var(--focus)}
.emoji-categories{display:flex;gap:2px;padding:0 8px 4px;border-bottom:1px solid var(--line);overflow-x:auto}
.emoji-categories button{flex:0 0 auto;width:30px;height:30px;border:0;border-bottom:2px solid transparent;border-radius:4px 4px 0 0;background:transparent;font-size:16px;opacity:.75}
.emoji-categories button[aria-selected=true]{border-bottom-color:var(--focus);opacity:1}
.emoji-section-title{margin:0;padding:6px 12px 2px;color:var(--muted);font-size:12px;font-weight:700}
.emoji-grid{flex:1 1 auto;min-height:0;display:grid;grid-template-columns:repeat(auto-fill,36px);grid-auto-rows:36px;align-content:start;gap:0;padding:2px 8px 8px;overflow-y:auto}
.emoji-grid button{display:grid;place-items:center;width:36px;height:36px;padding:0;border:0;border-radius:6px;background:transparent;font-size:22px;line-height:1;cursor:pointer}
.emoji-grid button:hover,.emoji-grid button[aria-selected=true]{background:var(--hover)}
.emoji-grid button:focus-visible{outline:2px solid var(--focus);outline-offset:-2px}
.emoji-grid .custom-emoji{width:24px;height:24px}
.emoji-picker-status{margin:0;padding:0 12px;color:var(--muted);font-size:12px;min-height:0}
.emoji-picker-status:empty{display:none}
.emoji-picker-foot{display:flex;align-items:center;gap:8px;min-height:44px;padding:6px 12px;border-top:1px solid var(--line);background:var(--panel)}
.emoji-preview{display:flex;align-items:center;gap:8px;min-width:0;flex:1 1 auto}
.emoji-preview-glyph{font-size:26px;line-height:1}
.emoji-preview-glyph .custom-emoji{width:28px;height:28px}
.emoji-preview-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:700;font-size:13px}
.emoji-add{flex:0 0 auto;padding:5px 10px;border:1px solid var(--field-line);border-radius:6px;color:var(--text);font-size:13px;font-weight:700;text-decoration:none}
.message-dialog{width:min(520px,calc(100vw - 24px));max-height:calc(100vh - 32px);padding:0;border:1px solid var(--line);border-radius:10px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow)}
.message-dialog::backdrop{background:#0000008c}
.message-dialog[open]:not(:modal){position:fixed;inset:0;z-index:70;margin:auto}
.message-dialog form{display:grid;gap:10px;padding:20px 24px}
.dialog-head{display:flex;align-items:center;justify-content:space-between;gap:12px}
.dialog-head h2{margin:0;font-size:20px}
.dialog-close{display:grid;place-items:center;width:32px;height:32px;border-radius:6px;color:var(--muted);font-size:22px;text-decoration:none}
.dialog-close:hover{background:var(--hover);color:var(--text)}
.message-dialog p{margin:0}
.dialog-preview{max-height:180px;overflow:auto;padding:8px 12px;border:1px solid var(--line);border-left:4px solid var(--quote-bar);border-radius:6px;background:var(--panel)}
.dialog-preview:empty{display:none}
.dialog-preview-author{font-weight:800}
.dialog-preview-author .time{font-weight:400;margin-left:6px}
.dialog-note{color:var(--muted);font-size:13px}
.dialog-label{font-weight:700;font-size:14px}
.dialog-label .optional{color:var(--muted);font-weight:400}
.dialog-field{width:100%;border:1px solid var(--field-line);border-radius:6px;padding:7px 10px;background:var(--panel-strong);color:var(--text);font:inherit}
.forward-destinations{padding:4px}
.forward-destinations option{padding:5px 8px;border-radius:4px}
.dialog-actions{display:flex;align-items:center;justify-content:flex-end;gap:8px;margin-top:6px}
.dialog-spacer{flex:1 1 auto}
.lightbox{width:100vw;height:100vh;max-width:none;max-height:none;margin:0;padding:0;border:0;background:#111214f2;color:var(--on-strong)}
.lightbox::backdrop{background:#000c}
.lightbox[open]{display:grid;grid-template-rows:auto minmax(0,1fr)}
.lightbox-head{display:flex;align-items:center;gap:12px;padding:10px 16px;background:#000000a6}
.lightbox-copy{flex:1 1 auto;min-width:0}
.lightbox-copy h2{margin:0;font-size:15px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.lightbox-copy p{margin:0;color:#c9c9cc;font-size:12px}
.lightbox-actions{display:flex;gap:4px}
.lightbox-button{display:grid;place-items:center;width:36px;height:36px;border:0;border-radius:6px;background:transparent;color:#fff;font-size:20px;text-decoration:none;cursor:pointer}
.lightbox-button:hover{background:#ffffff26}
.lightbox-button .action-icon{width:20px;height:20px}
.lightbox-stage{display:grid;place-items:center;min-height:0;padding:16px}
.lightbox-stage img{max-width:100%;max-height:100%;object-fit:contain}
.toast{position:fixed;z-index:80;left:50%;bottom:24px;transform:translateX(-50%);max-width:calc(100vw - 32px);padding:10px 16px;border-radius:8px;background:#1d1c1d;color:#fff;box-shadow:var(--shadow);font-size:14px}
html[data-theme=dark] .toast{background:#e9e7ea;color:#1a1d21}
.toast[hidden]{display:none}
.content:has(>.thread){grid-template-columns:minmax(0,1fr) minmax(0,400px);grid-template-areas:"head thread" "timeline thread" "composer thread"}
.thread{padding:0;display:flex;flex-direction:column}
.thread-heading{position:sticky;top:0;z-index:6;display:flex;align-items:center;gap:8px;margin:0;padding:12px 16px;border-bottom:1px solid var(--line);background:var(--panel)}
.thread-heading h2{margin:0;font-size:18px}
.thread-channel{color:var(--muted);font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.thread-close{display:grid;place-items:center;width:32px;height:32px;margin-left:auto;border-radius:6px;color:var(--muted);font-size:22px;text-decoration:none}
.thread-close:hover{background:var(--hover);color:var(--text)}
.thread .message{padding-left:16px;padding-right:16px}
/* The thread pane is 400px wide. With the one-click reactions the toolbar lay
   over the author's timestamp, a link WCAG 2.2 target-size then reports as
   obscured; Add reaction stays, one click further. */
.thread .message-actions .quick-reaction{display:none}
@media(max-width:800px){
.message{padding-left:12px;padding-right:12px}
.message-actions,.message-actions:has(details[open]){position:absolute;top:auto;bottom:calc(100% - 8px);right:8px;display:none;padding:2px;border:1px solid var(--line);background:var(--panel-strong);box-shadow:0 2px 6px #0000001f}
.message:focus-within>.message-actions,.message:focus>.message-actions,.message-actions:has(details[open]){display:flex}
.message:hover:not(:focus-within)>.message-actions{display:none}
.message-more>.message-menu{position:fixed;left:8px;right:8px;top:auto;bottom:8px;min-width:0;max-width:none;max-height:70vh}
.menu-submenu>.message-submenu{position:static;box-shadow:none;border:0;border-top:1px solid var(--line);border-radius:0;min-width:0}
.message-block-fields{grid-template-columns:minmax(0,1fr)}
.content:has(>.thread){grid-template-columns:minmax(0,1fr);grid-template-areas:"head" "thread" "composer"}
.content:has(>.thread) .timeline-wrap{display:none}
.emoji-picker{left:8px !important;right:8px;width:auto;bottom:8px;top:auto !important;height:min(420px,70vh)}
}
/* A closed disclosure hides its content in every engine. The menus' own
   display rules (a grid submenu, flex items) otherwise win over WebKit's
   closed-details hiding and leave a closed submenu's items rendered, so this
   comes last. */
.menu-submenu:not([open])>:not(summary),.reminder-custom:not([open])>:not(summary),details[data-message-menu]:not([open])>:not(summary),.file-more:not([open])>:not(summary){display:none}
</style>`
