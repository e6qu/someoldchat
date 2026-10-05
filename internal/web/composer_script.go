package web

// composerScript is the whole client for the message composers. It is its own
// script, hashed for the Content-Security-Policy like every other inline
// script, and it owns composer submission outright: a submit on a composer
// form is handled here and does not reach the page's generic form handler.
//
// The script carries no JavaScript comments: html/template elides them in a
// script context, and the served bytes would stop matching the hash that
// permits them. The design notes live here instead.
//
//   - Rich editing. Each composer's textarea is the form field and the no-JS
//     editor. The script puts a contenteditable editor in front of it and
//     serialises the editor into the textarea on every change, so the server
//     receives exactly what the textarea would have carried: Slack mrkdwn,
//     with mentions as <@U…>, <#C…>, <!subteam^…> and <!here>, and &, < and >
//     escaped as Slack's own clients escape them. "Format messages with
//     markup" (a composer preference) keeps the textarea instead.
//   - Two composers. The conversation composer and the thread pane's reply
//     composer are separate instances with separate drafts, staged files,
//     suggestions and errors. A thread pane opened in place announces itself
//     with sameoldchat:thread-pane and its reply composer is initialised then;
//     a composer the swap removed flushes its pending draft save, because no
//     pagehide fires to do it and the server would keep a stale draft.
//     The emoji picker is the message layer's one shared popover; it hands a
//     choice back as sameoldchat:composer-emoji with the button that opened
//     it, so the emoji lands in that button's composer. The page-level
//     dialogs (shortcuts, clip recorder, link, custom schedule time, broadcast
//     confirmation) act on whichever composer the member was last in.
//   - Sending. A send carries a client_msg_id the server uses as the post's
//     idempotency key. A transport failure, rate limit or unavailable server
//     moves the message into the composer's outbox as "Not sent" with Retry
//     and Delete, and Retry reuses the same id, so a send that did commit
//     before its response was lost cannot post twice. A refusal the member
//     can act on (too long, not permitted, archived) keeps the text in the
//     composer and says why. An empty composer does nothing on Enter, as in
//     Slack, instead of raising the browser's "Please fill out this field".
//     A send is finished when the post answers: the composer is released
//     then, its message goes into the conversation through the page's
//     sameoldchatPage.append (never written to a region directly, which used
//     to leave a second copy when the live stream had already drawn it), and
//     only afterwards is the view caught up with a forced refresh. A refresh
//     that fails is the view being behind, announced as such; it used to be
//     reported as the send failing, putting a committed message in the outbox
//     as "Not sent". A committed send the server could not render answers 204
//     with X-SameOldChat-Sent-View: pending, the same success, and the
//     refresh draws the message.
//   - Suggestions are one listbox per composer, anchored above that composer,
//     for @ (people, including workspace members outside the conversation
//     labelled "Not in channel", apps, user groups and @here/@channel/
//     @everyone), # (channels), : (emoji, after two characters) and / (slash
//     commands at the start of an empty composer).
//   - After a message mentioning someone outside the channel commits, the
//     outbox offers Slack's "Add them" / "Do nothing". A broadcast mention in
//     a channel of at least six members asks first, the threshold Slack's
//     "Manage who can notify a channel or workspace" help article publishes.
//   - Up in an empty composer edits the member's own last message in that
//     composer's conversation or thread, in place, through the message
//     layer's window.sameoldchatEditLastMessage; when there is no message of
//     theirs it moves focus to the last message instead.
//   - Preferences are per browser (localStorage keys sameoldchat-composer-enter,
//     -markup and -formatting) and are announced with the
//     sameoldchat:composer-preferences event so a preferences surface and the
//     composers stay in step.
var composerScript = `<script>(function(){
var doc=document;
var composerForms=doc.querySelectorAll('form[data-composer]');
var composers=[];
var active=null;
var apple=/Mac|iPhone|iPad/.test(navigator.platform||'');
var BT='\x60';
var LIMIT=40000;
var liveStatus=doc.getElementById('live-status')||doc.querySelector('[data-composer-preference-status]');
function page(){return window.sameoldchatPage||{}}
function primary(event){return apple?event.metaKey&&!event.ctrlKey:event.ctrlKey&&!event.metaKey}
function ownPath(value){return typeof value==='string'&&value.charAt(0)==='/'&&value.charAt(1)!=='/'}
function announce(message){if(liveStatus)liveStatus.textContent=message}
function clip(value){var message=String(value||'').trim();if(message.charAt(0)==='<')message='';if(message.length>240)message=message.slice(0,240);return message}
function uuid(){try{if(window.crypto&&window.crypto.randomUUID)return window.crypto.randomUUID()}catch(error){}var value='';for(var index=0;index<32;index++)value+=Math.floor(Math.random()*16).toString(16);return value}
function runes(value){return Array.from?Array.from(value).length:value.length}
function escapeText(value){return value.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')}
function decodeText(value){return value.replace(/&lt;/g,'<').replace(/&gt;/g,'>').replace(/&amp;/g,'&')}
function cleanText(value){return value.replace(/\u200B/g,'').replace(/\u00A0/g,' ')}
function closest(node,selector,root){while(node&&node!==root){if(node.nodeType===1&&node.matches&&node.matches(selector))return node;node=node.parentNode}return null}
function safeHref(value){return /^(https?:\/\/|mailto:)/i.test(value||'')}
function normalizeHref(value){value=String(value||'').trim();if(!value)return '';if(safeHref(value))return value;if(/^[^\s:\/]+\.[^\s]+$/.test(value))return 'https://'+value;return ''}
function plural(count,one,many){return count===1?one:many}
function formatSize(value){var size=value||0;var unit='B';if(size>=1048576){size=size/1048576;unit='MiB'}else if(size>=1024){size=size/1024;unit='KiB'}return(unit==='B'?size:String(Math.round(size*10)/10))+' '+unit}
var prefs={enter:'send',markup:false,formatting:true};
function readPrefs(){try{prefs.enter=localStorage.getItem('sameoldchat-composer-enter')==='newline'?'newline':'send';prefs.markup=localStorage.getItem('sameoldchat-composer-markup')==='true';prefs.formatting=localStorage.getItem('sameoldchat-composer-formatting')!=='hidden'}catch(error){}}
readPrefs();
function syncPreferenceControls(){Array.prototype.forEach.call(doc.querySelectorAll('[data-composer-preference]'),function(input){var key=input.getAttribute('data-composer-preference');if(key==='enter')input.checked=input.value===prefs.enter;if(key==='markup')input.checked=prefs.markup});Array.prototype.forEach.call(doc.querySelectorAll('[data-keyboard-apple]'),function(node){if(node.closest('[data-composer-preferences]'))node.hidden=!apple});Array.prototype.forEach.call(doc.querySelectorAll('[data-keyboard-other]'),function(node){if(node.closest('[data-composer-preferences]'))node.hidden=apple})}
function setPref(key,value){try{localStorage.setItem('sameoldchat-composer-'+key,value)}catch(error){}if(window.sameoldchatKeepPreference)window.sameoldchatKeepPreference('composer-'+key,value);readPrefs();composers.forEach(function(composer){composer.applyPrefs()});syncPreferenceControls();try{doc.dispatchEvent(new CustomEvent('sameoldchat:composer-preferences',{detail:{enter:prefs.enter,markup:prefs.markup,formatting:prefs.formatting}}))}catch(error){}}
doc.addEventListener('change',function(event){var input=event.target;if(!input||!input.getAttribute)return;var key=input.getAttribute('data-composer-preference');if(!key)return;if(key==='enter'&&input.checked)setPref('enter',input.value==='newline'?'newline':'send');if(key==='markup')setPref('markup',input.checked?'true':'false');announce(key==='enter'?(prefs.enter==='send'?'Enter now sends a message.':'Enter now starts a new line.'):(prefs.markup?'Messages are now written with markup.':'Messages are now formatted as you type.'))});
window.addEventListener('storage',function(event){if(event.key&&event.key.indexOf('sameoldchat-composer-')===0){readPrefs();composers.forEach(function(composer){composer.applyPrefs()});syncPreferenceControls()}});
if(!composerForms.length){syncPreferenceControls();return}
var directories={};
function loadDirectory(node){var loaded={people:[],groups:[],specials:[],channels:[],commands:[],search:node?node.getAttribute('data-people-search')||'':'',searched:{}};
if(node&&node.content){Array.prototype.forEach.call(node.content.querySelectorAll('i'),function(node){
var entry={kind:node.getAttribute('data-kind'),id:node.getAttribute('data-id')||'',name:node.getAttribute('data-name')||'',real:node.getAttribute('data-real')||'',display:node.getAttribute('data-display')||'',avatar:node.getAttribute('data-avatar')||'',initial:node.getAttribute('data-initial')||'',member:node.hasAttribute('data-member'),bot:node.hasAttribute('data-bot'),self:node.hasAttribute('data-self'),description:node.getAttribute('data-description')||'',count:node.getAttribute('data-count')||'',hint:node.getAttribute('data-hint')||'',app:node.getAttribute('data-app')||'',isPrivate:node.hasAttribute('data-private')};
var list={person:loaded.people,group:loaded.groups,special:loaded.specials,channel:loaded.channels,command:loaded.commands}[entry.kind];
if(list)list.push(entry);
})}return loaded}
function directoryFor(form){var id=form.getAttribute('data-directory')||'composer-directory';if(!directories[id])directories[id]=loadDirectory(doc.getElementById(id));return directories[id]}
var directory=directoryFor(composerForms[0]);
function useDirectory(form){directory=directoryFor(form)}
function findEntry(list,id){for(var index=0;index<list.length;index++)if(list[index].id===id)return list[index];return null}
var peopleTimer=null;
function searchPeople(query,refresh){var asked=directory;if(!asked.search||!query||asked.searched[query])return;if(peopleTimer)window.clearTimeout(peopleTimer);peopleTimer=window.setTimeout(function(){asked.searched[query]=true;fetch(asked.search+'&q='+encodeURIComponent(query),{credentials:'same-origin',headers:{accept:'application/json'}}).then(function(response){return response.ok?response.json():[]}).then(function(found){var added=false;(Array.isArray(found)?found:[]).forEach(function(person){if(!person||!person.id||findEntry(asked.people,person.id))return;asked.people.push({kind:'person',id:person.id,name:person.name||'',real:person.real||'',display:person.display||'',avatar:person.avatar||'',initial:person.initial||'',member:!!person.member||!!person.membership_unknown,bot:!!person.bot,self:!!person.self});added=true});if(added&&refresh)refresh(query)}).catch(function(){asked.searched[query]=false})},150)}
function isBlock(node){return node&&node.nodeType===1&&/^(P|DIV|PRE|BLOCKQUOTE|UL|OL|LI|H[1-6])$/.test(node.nodeName)}
function wrapMark(mark,inner){var match=/^(\s*)([\s\S]*?)(\s*)$/.exec(inner);if(!match[2])return inner;return match[1]+mark+match[2]+mark+match[3]}
function preText(node){var value='';Array.prototype.forEach.call(node.childNodes,function(child,index){if(child.nodeType===3)value+=child.data;else if(child.nodeName==='BR'){if(index<node.childNodes.length-1)value+='\n'}else if(isBlock(child)){if(value&&value.charAt(value.length-1)!=='\n')value+='\n';value+=preText(child)}else value+=preText(child)});return value}
function inlineChildren(node){var value='';var children=node.childNodes;for(var index=0;index<children.length;index++)value+=inlineOf(children[index],index===children.length-1);return value}
function inlineOf(node,last){
if(node.nodeType===3)return escapeText(cleanText(node.data));
if(node.nodeType!==1)return '';
if(node.hasAttribute('data-entity'))return node.getAttribute('data-entity');
var name=node.nodeName;
if(name==='BR')return last?'':'\n';
if(isBlock(node))return blockLines(node).join('\n');
if(name==='CODE'){var code=cleanText(node.textContent);return code?wrapMark(BT,escapeText(code)):''}
if(name==='A'){var href=node.getAttribute('href')||'';var label=escapeText(cleanText(node.textContent));if(!safeHref(href))return label;return label&&label!==escapeText(href)?'<'+href+'|'+label+'>':'<'+href+'>'}
var inner=inlineChildren(node);
var style=node.style||{};
if(name==='B'||name==='STRONG'||/^(bold|[6-9]00)$/.test(style.fontWeight||''))inner=wrapMark('*',inner);
if(name==='I'||name==='EM'||style.fontStyle==='italic')inner=wrapMark('_',inner);
if(name==='S'||name==='STRIKE'||name==='DEL'||/line-through/.test((style.textDecoration||'')+(style.textDecorationLine||'')))inner=wrapMark('~',inner);
return inner;
}
function blocksOf(container){
var lines=[];var inline=null;
Array.prototype.forEach.call(container.childNodes,function(node){
if(isBlock(node)){if(inline!==null){lines.push(inline);inline=null}Array.prototype.push.apply(lines,blockLines(node));return}
if(node.nodeName==='BR'){lines.push(inline===null?'':inline);inline=null;return}
inline=(inline||'')+inlineOf(node,false);
});
if(inline!==null)lines.push(inline);
return lines;
}
function blockLines(node){
var name=node.nodeName;
if(name==='PRE'){var code=cleanText(preText(node)).replace(/\n+$/,'');return code.trim()?[BT+BT+BT+escapeText(code)+BT+BT+BT]:['']}
if(name==='BLOCKQUOTE'){var quoted=blocksOf(node);if(!quoted.length)quoted=[''];return quoted.join('\n').split('\n').map(function(line){return '&gt; '+line})}
if(name==='UL'||name==='OL'){var items=[];var count=0;Array.prototype.forEach.call(node.childNodes,function(item){if(item.nodeName!=='LI')return;count++;var body=blocksOf(item);var first=body.length?body.shift():'';items.push((name==='OL'?count+'. ':'\u2022 ')+first);body.forEach(function(line){items.push('    '+line)})});return items}
if(name==='LI')return blocksOf(node);
for(var index=0;index<node.childNodes.length;index++)if(isBlock(node.childNodes[index]))return blocksOf(node);
return [inlineChildren(node)];
}
function serialize(root){return blocksOf(root).join('\n').replace(/\n+$/,'')}
function makePill(entity,label,special){var pill=doc.createElement('span');pill.className='composer-pill'+(special?' is-special':'');pill.setAttribute('contenteditable','false');pill.setAttribute('data-entity',entity);pill.textContent=label;return pill}
function makeEmoji(name,glyph,image){if(!glyph&&!image)return doc.createTextNode(':'+name+':');var node=doc.createElement('span');node.className='composer-emoji';node.setAttribute('contenteditable','false');node.setAttribute('data-entity',':'+name+':');node.setAttribute('role','img');node.setAttribute('aria-label',':'+name+':');if(image){var img=doc.createElement('img');img.src=image;img.alt='';img.className='custom-emoji';node.appendChild(img)}else node.textContent=glyph;return node}
function referenceNode(raw){
var cut=raw.indexOf('|');var target=cut<0?raw:raw.slice(0,cut);var label=cut<0?'':raw.slice(cut+1);
if(target.charAt(0)==='@'){var person=findEntry(directory.people,target.slice(1));return makePill('<'+raw+'>',label?'@'+decodeText(label):'@'+(person?person.name:target.slice(1)))}
if(target.charAt(0)==='#'){var channel=findEntry(directory.channels,target.slice(1));return makePill('<'+raw+'>','#'+(label?decodeText(label):channel?channel.name:target.slice(1)))}
if(target.indexOf('!subteam^')===0){var group=findEntry(directory.groups,target.slice(9));return makePill('<'+raw+'>',label?decodeText(label):'@'+(group?group.name:'group'),false)}
if(target.charAt(0)==='!'){return makePill('<'+raw+'>',label?decodeText(label):'@'+target.slice(1).split('^')[0],true)}
if(safeHref(target)){var link=doc.createElement('a');link.href=target;link.textContent=decodeText(label||target);return link}
return null;
}
var inlinePattern=new RegExp('(\\x60[^\\x60\\n]+\\x60)|(<[^<>\\s][^<>]*>)|(\\*[^*\\s](?:[^*\\n]*[^*\\s])?\\*)|(_[^_\\s](?:[^_\\n]*[^_\\s])?_)|(~[^~\\s](?:[^~\\n]*[^~\\s])?~)','g');
function renderInline(parent,value){
var last=0;var match;inlinePattern.lastIndex=0;var pattern=new RegExp(inlinePattern.source,'g');
while((match=pattern.exec(value))){
if(match.index>last)parent.appendChild(doc.createTextNode(decodeText(value.slice(last,match.index))));
var token=match[0];var node=null;
if(match[1]){node=doc.createElement('code');node.textContent=decodeText(token.slice(1,-1))}
else if(match[2]){node=referenceNode(token.slice(1,-1));if(!node)node=doc.createTextNode(decodeText(token))}
else{node=doc.createElement(match[3]?'b':match[4]?'i':'s');renderInline(node,token.slice(1,-1))}
parent.appendChild(node);last=match.index+token.length;
}
if(last<value.length)parent.appendChild(doc.createTextNode(decodeText(value.slice(last))));
}
function emptyParagraph(){var paragraph=doc.createElement('p');paragraph.appendChild(doc.createElement('br'));return paragraph}
function renderMarkup(root,value){
root.textContent='';
var fence=BT+BT+BT;var parts=String(value||'').split(fence);
if(parts.length%2===0){parts[parts.length-2]=parts[parts.length-2]+fence+parts[parts.length-1];parts.pop()}
var group=null;var groupKind='';
function closeGroup(){group=null;groupKind=''}
function line(text){
var quote=/^&gt; ?(.*)$|^> ?(.*)$/.exec(text);
if(quote){if(groupKind!=='quote'){group=doc.createElement('blockquote');root.appendChild(group);groupKind='quote'}var paragraph=doc.createElement('p');var body=quote[1]!==undefined?quote[1]:quote[2]||'';if(body)renderInline(paragraph,body);else paragraph.appendChild(doc.createElement('br'));group.appendChild(paragraph);return}
var bullet=/^\u2022 (.*)$/.exec(text);
if(bullet){if(groupKind!=='ul'){group=doc.createElement('ul');root.appendChild(group);groupKind='ul'}var item=doc.createElement('li');renderInline(item,bullet[1]);group.appendChild(item);return}
var ordered=/^(\d+)\. (.*)$/.exec(text);
if(ordered&&(groupKind==='ol'||ordered[1]==='1')){if(groupKind!=='ol'){group=doc.createElement('ol');root.appendChild(group);groupKind='ol'}var entry=doc.createElement('li');renderInline(entry,ordered[2]);group.appendChild(entry);return}
closeGroup();
var paragraph=doc.createElement('p');if(text)renderInline(paragraph,text);else paragraph.appendChild(doc.createElement('br'));root.appendChild(paragraph);
}
parts.forEach(function(part,index){
if(index%2===1){closeGroup();var pre=doc.createElement('pre');pre.textContent=decodeText(part.replace(/^\n/,'').replace(/\n$/,''));root.appendChild(pre);return}
if(index>0&&part.charAt(0)==='\n')part=part.slice(1);
if(index<parts.length-1&&part.charAt(part.length-1)==='\n')part=part.slice(0,-1);
if(!part&&(index>0||parts.length>1))return;
part.split('\n').forEach(line);
});
if(!root.firstChild)root.appendChild(emptyParagraph());
}
function composerFor(node){for(var index=0;index<composers.length;index++)if(composers[index].form.contains(node))return composers[index];return null}
var typingRegion=doc.getElementById('typing');
var typingURL=typingRegion?typingRegion.getAttribute('data-typing')||'':'';
var typingSent=0;
var typingTiming=` + typingTimingLiteral() + `;
function sendTyping(csrf){var now=Date.now();if(!ownPath(typingURL)||now-typingSent<typingTiming.interval)return;typingSent=now;var body=new URLSearchParams();body.set('_csrf',csrf);fetch(typingURL,{method:'POST',credentials:'same-origin',headers:{'content-type':'application/x-www-form-urlencoded'},body:body.toString()}).catch(function(){})}
function recentEmoji(){try{var recent=JSON.parse(localStorage.getItem('sameoldchat-recent-emoji')||'[]');return Array.isArray(recent)?recent:[]}catch(error){return[]}}
function rememberEmoji(name){try{var recent=recentEmoji().filter(function(value){return value!==name});recent.unshift(name);localStorage.setItem('sameoldchat-recent-emoji',JSON.stringify(recent.slice(0,24)))}catch(error){}}
var emojiRequest=null;
function fetchEmoji(query){if(emojiRequest&&emojiRequest.abort)emojiRequest.abort();emojiRequest=window.AbortController?new AbortController():null;var parameters=new URLSearchParams({q:query});var recent=recentEmoji();if(recent.length)parameters.set('recent',recent.slice(0,24).join(','));var options={credentials:'same-origin'};if(emojiRequest)options.signal=emojiRequest.signal;return fetch('/app/emoji/options?'+parameters.toString(),options).then(function(response){if(!response.ok)throw new Error('emoji');return response.json()}).then(function(payload){return payload&&Array.isArray(payload.options)?payload.options:[]})}
function createComposer(form){
useDirectory(form);
var prefix=form.id==='composer'?'':form.id.slice(0,form.id.length-'composer'.length);
function byId(id){return doc.getElementById(prefix+id)}
var api={form:form,thread:form.getAttribute('data-composer')==='thread'};
var field=byId('text');
var list=byId('composer-suggestions');
var errorBox=byId('composer-error');
var hint=byId('composer-hint');
var counter=byId('composer-count');
var outbox=byId('composer-outbox');
var formatBar=byId('composer-format');
var preview=byId('upload-preview');
var uploadForm=byId('upload-form');
var uploadFile=byId('upload-file');
var draftInput=byId('draft-attachments');
var scheduleInput=byId('schedule-at');
var formatToggle=form.querySelector('[data-format-toggle]');
var mentionButton=form.querySelector('[data-composer-action="mention"]');
var sendButton=form.querySelector('button.send');
var csrfInput=form.querySelector('input[name=_csrf]');
var csrf=csrfInput?csrfInput.value:'';
var channelLabel=form.getAttribute('data-channel-label')||'this channel';
var memberCount=parseInt(form.getAttribute('data-member-count')||'0',10)||0;
var inviteURL=form.getAttribute('data-invite-url')||'';
var direct=form.hasAttribute('data-direct');
var draftKey='sameoldchat-draft:'+(form.getAttribute('data-draft-url')||form.getAttribute('action'));
var editor=null;var mode='plain';var sending=false;var staging=0;var draftTimer=null;var draftDirty=false;var suggestion=null;var suggestionTimer=null;
var attachments=[];try{attachments=JSON.parse(draftInput&&draftInput.value||'[]');if(!Array.isArray(attachments))attachments=[]}catch(error){attachments=[]}
var pendingUploads=[];var thumbnails={};
function surface(){return mode==='rich'?editor:field}
function value(){return field.value}
function sync(){if(mode==='rich'){field.value=serialize(editor)}refresh();persistDraft()}
function isEmpty(){return !field.value.replace(/[\s\u200B]+/g,'')&&!attachments.length&&!pendingUploads.length}
function refresh(){
var text=field.value;var empty=!text.replace(/[\s\u200B]+/g,'');
if(editor){var visible=editor.textContent.replace(/\u200B/g,'')!==''||!!editor.querySelector('[data-entity],li,pre,blockquote,img');editor.classList.toggle('is-empty',!visible)}
form.classList.toggle('is-empty',empty&&!attachments.length&&!pendingUploads.length);
if(hint)hint.classList.toggle('is-visible',!empty);
var over=runes(text)-LIMIT;
if(counter){if(over>0){counter.hidden=false;counter.textContent='-'+over.toLocaleString();counter.setAttribute('aria-label','Your message is '+over.toLocaleString()+' '+plural(over,'character','characters')+' too long.')}else{counter.hidden=true;counter.textContent=''}}
if(mode==='plain'&&field){field.style.height='auto';field.style.height=Math.min(field.scrollHeight+2,Math.round(window.innerHeight*0.4))+'px'}
}
function persistDraft(){
try{if(field.value)localStorage.setItem(draftKey,field.value);else localStorage.removeItem(draftKey)}catch(error){}
draftDirty=true;if(draftTimer)window.clearTimeout(draftTimer);
draftTimer=window.setTimeout(function(){saveDraftRemote(false)},450);
}
function saveDraftRemote(keepalive){
var action=form.getAttribute('data-draft-url');
if(!action||!ownPath(action)||runes(field.value)>LIMIT)return Promise.resolve();
if(draftTimer){window.clearTimeout(draftTimer);draftTimer=null}
var body=new URLSearchParams();body.set('_csrf',csrf);body.set('text',field.value);body.set('draft_attachments',JSON.stringify(attachments));
var threadInput=form.querySelector('input[name=thread_ts]');if(threadInput)body.set('thread_ts',threadInput.value);
return fetch(action,{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin',keepalive:!!keepalive&&body.toString().length<60000}).then(function(response){if(response.ok)draftDirty=false;if(!response.ok)announce('Your draft has not been saved yet. Keep this tab open and try typing again.')}).catch(function(){announce('Your draft has not been saved yet. Keep this tab open and try typing again.')});
}
api.flushDraft=function(){if(draftTimer)return saveDraftRemote(false);return Promise.resolve()};
function persistDraftNow(){try{if(field.value)localStorage.setItem(draftKey,field.value);else localStorage.removeItem(draftKey)}catch(error){}return saveDraftRemote(false)}
function setValue(text){field.value=text;if(mode==='rich')renderMarkup(editor,text);refresh()}
function showError(message){if(!errorBox){window.alert(message);return}errorBox.textContent=message;errorBox.hidden=false;form.classList.add('is-error');errorBox.scrollIntoView({block:'nearest'});errorBox.focus()}
function clearError(){if(!errorBox)return;errorBox.textContent='';errorBox.hidden=true;form.classList.remove('is-error')}
function focus(){var target=surface();if(!target)return;target.focus();if(mode==='rich'){var selection=window.getSelection();if(selection&&!editor.contains(selection.anchorNode)){var block=editor.lastChild;while(block&&block.nodeType===1&&isBlock(block.lastChild))block=block.lastChild;var range=doc.createRange();if(block&&block.nodeType===1&&isBlock(block)){if(block.lastChild&&block.lastChild.nodeName==='BR')range.setStartBefore(block.lastChild);else{range.selectNodeContents(block);range.collapse(false)}}else{range.selectNodeContents(editor);range.collapse(false)}range.collapse(true);selection.removeAllRanges();selection.addRange(range)}}}
api.focus=focus;
function caret(){
if(mode==='plain'){if(field.selectionStart!==field.selectionEnd)return null;return{before:field.value.slice(0,field.selectionStart),offset:field.selectionStart,node:null}}
var selection=window.getSelection();if(!selection||!selection.rangeCount||!selection.isCollapsed)return null;
var node=selection.anchorNode;var offset=selection.anchorOffset;if(!node||!editor.contains(node))return null;
if(node.nodeType!==3){var previous=node.childNodes[offset-1];if(previous&&previous.nodeType===3){node=previous;offset=previous.data.length}else return{before:'',offset:0,node:null}}
if(closest(node,'code,pre',editor))return null;
return{before:node.data.slice(0,offset),offset:offset,node:node};
}
function fullText(){return mode==='rich'?cleanText(editor.textContent):field.value}
function trigger(){
var info=caret();if(!info)return null;
var whole=fullText();
if(/^\/[^\s]*$/.test(whole)&&info.before===whole&&directory.commands.length)return{type:'/',query:whole.toLowerCase(),info:info};
var reference=/(^|[\s(])([@#])((?:[^\s@#<>][^@#<>\n]{0,40})?)$/.exec(info.before.replace(/\u00A0/g,' '));
if(reference)return{type:reference[2],query:reference[3].toLowerCase(),start:info.offset-reference[3].length-1,info:info};
var emoji=/(^|[\s(]):([a-zA-Z0-9_+\-]{2,})$/.exec(info.before);
if(emoji)return{type:':',query:emoji[2].toLowerCase(),start:info.offset-emoji[2].length-1,info:info};
return null;
}
function placeCaret(node,offset){var selection=window.getSelection();var range=doc.createRange();range.setStart(node,offset);range.collapse(true);selection.removeAllRanges();selection.addRange(range)}
function escapeFormatting(range){
var container=range.startContainer;var node=container.nodeType===3?container:container.childNodes[range.startOffset-1]||container;
var formatting=closest(node,'b,strong,i,em,s,strike,del,code,a',editor);
while(formatting){var atEnd=container.nodeType===3?range.startOffset===container.data.length&&!container.nextSibling:true;if(!atEnd)break;range.setStartAfter(formatting);range.collapse(true);container=range.startContainer;formatting=closest(formatting.parentNode,'b,strong,i,em,s,strike,del,code,a',editor)}
return range;
}
function insertNodes(nodes){
var selection=window.getSelection();var range;
if(selection&&selection.rangeCount&&editor.contains(selection.anchorNode)){range=selection.getRangeAt(0)}else{range=doc.createRange();range.selectNodeContents(editor);range.collapse(false)}
range.deleteContents();
if(range.startContainer===editor){var previousBlock=editor.childNodes[range.startOffset-1];var nextBlock=editor.childNodes[range.startOffset];var atEnd=!!(previousBlock&&isBlock(previousBlock));var near=atEnd?previousBlock:nextBlock;if(near&&isBlock(near)){var inside=near;while(atEnd&&inside.lastChild&&isBlock(inside.lastChild))inside=inside.lastChild;while(!atEnd&&inside.firstChild&&isBlock(inside.firstChild))inside=inside.firstChild;range.selectNodeContents(inside);range.collapse(!atEnd);if(atEnd&&inside.lastChild&&inside.lastChild.nodeName==='BR'){range.setStartBefore(inside.lastChild);range.collapse(true)}}}
if(nodes.length&&nodes[0].nodeType===1&&nodes[0].hasAttribute('data-entity'))range=escapeFormatting(range);
var last=null;nodes.forEach(function(node){range.insertNode(node);range.setStartAfter(node);range.collapse(true);last=node});
if(last&&last.nodeType===3)placeCaret(last,last.data.length);else if(last){range.setStartAfter(last);range.collapse(true);selection.removeAllRanges();selection.addRange(range)}
var trailing=editor.querySelector('p>br:last-child');if(trailing&&trailing.previousSibling)trailing.parentNode.removeChild(trailing);
}
function replaceTrigger(found,entity,label,kind,extra){
if(mode==='plain'){
var start=found.type==='/'?0:found.start;var end=field.selectionStart;
var text=kind==='emoji'?':'+entity+': ':(kind==='command'?entity+' ':entity+' ');
field.value=field.value.slice(0,start)+text+field.value.slice(end);var position=start+text.length;field.focus();field.setSelectionRange(position,position);sync();return;
}
if(found.type==='/'){renderMarkup(editor,'');var paragraph=editor.firstChild;paragraph.textContent=entity+' ';placeCaret(paragraph.firstChild,paragraph.firstChild.data.length);sync();return}
var info=found.info;if(!info.node){focus();return}
var range=doc.createRange();range.setStart(info.node,found.start);range.setEnd(info.node,info.offset);range.deleteContents();
var selection=window.getSelection();selection.removeAllRanges();selection.addRange(range);
var node=kind==='emoji'?makeEmoji(entity,extra&&extra.glyph,extra&&extra.image):makePill(entity,label,kind==='special');
insertNodes([node,doc.createTextNode(' ')]);
sync();
}
function hideSuggestions(){suggestion=null;if(list){list.hidden=true;list.textContent=''}var target=surface();if(target){target.setAttribute('aria-expanded','false');target.removeAttribute('aria-activedescendant')}}
api.hideSuggestions=hideSuggestions;
function suggestionOptions(){return list?Array.prototype.slice.call(list.querySelectorAll('[role=option]')):[]}
function activate(index){var options=suggestionOptions();if(!options.length)return;index=(index+options.length)%options.length;options.forEach(function(option,position){option.setAttribute('aria-selected',position===index?'true':'false')});options[index].scrollIntoView({block:'nearest'});surface().setAttribute('aria-activedescendant',options[index].id)}
function option(index,parts,accept){
var node=doc.createElement('div');node.setAttribute('role','option');node.id=prefix+'composer-option-'+index;node.setAttribute('aria-selected',index===0?'true':'false');
parts.forEach(function(part){if(!part)return;var child=doc.createElement(part.tag||'span');if(part.className)child.className=part.className;if(part.text!==undefined)child.textContent=part.text;if(part.image){var img=doc.createElement('img');img.src=part.image;img.alt='';child.appendChild(img)}if(part.hidden)child.setAttribute('aria-hidden','true');if(part.label)node.setAttribute('aria-label',part.label);node.appendChild(child)});
node.addEventListener('mousedown',function(event){event.preventDefault()});
node.addEventListener('click',function(){accept()});
node._accept=accept;
return node;
}
function showOptions(label,nodes){
if(!list)return;list.textContent='';
if(!nodes.length){hideSuggestions();return}
list.setAttribute('aria-label',label);nodes.forEach(function(node){list.appendChild(node)});list.hidden=false;
var target=surface();target.setAttribute('aria-expanded','true');target.setAttribute('aria-activedescendant',nodes[0].id);
}
function matches(values,query){if(!query)return true;for(var index=0;index<values.length;index++){var candidate=(values[index]||'').toLowerCase();if(candidate.indexOf(query)===0||candidate.indexOf(' '+query)!==-1)return true}return false}
function updateSuggestions(){
var found=trigger();
if(!found){hideSuggestions();return}
suggestion=found;var nodes=[];var index=0;
if(found.type==='@'){
var query=found.query;
searchPeople(query,function(asked){var current=trigger();if(current&&current.type==='@'&&current.query===asked)updateSuggestions()});
var people=directory.people.filter(function(person){return matches([person.name,person.real,person.display],query)});
var members=people.filter(function(person){return person.member||direct});var outsiders=direct?[]:people.filter(function(person){return !person.member});
members.slice(0,8).forEach(function(person){nodes.push(personOption(index++,person,found))});
directory.groups.filter(function(group){return matches([group.name,group.real],query)}).slice(0,4).forEach(function(group){nodes.push(option(index++,[{className:'suggestion-avatar',text:'@',hidden:true},{className:'suggestion-name',text:'@'+group.name},{className:'suggestion-meta',text:group.real+(group.count?' \u00b7 '+group.count+' '+plural(+group.count,'member','members'):'')}],function(){replaceTrigger(found,'<!subteam^'+group.id+'>','@'+group.name,'group');hideSuggestions()}))});
outsiders.slice(0,Math.max(0,10-nodes.length)).forEach(function(person){nodes.push(personOption(index++,person,found))});
directory.specials.filter(function(special){return special.name.indexOf(query)===0}).forEach(function(special){nodes.push(option(index++,[{className:'suggestion-avatar',text:'@',hidden:true},{className:'suggestion-name',text:'@'+special.name},{className:'suggestion-meta',text:special.description}],function(){replaceTrigger(found,special.id,'@'+special.name,'special');hideSuggestions()}))});
showOptions('Mention suggestions',nodes);return;
}
if(found.type==='#'){
directory.channels.filter(function(channel){return channel.name.toLowerCase().indexOf(found.query)!==-1}).slice(0,10).forEach(function(channel){nodes.push(option(index++,[{className:'suggestion-avatar',text:channel.isPrivate?'\u{1F512}':'#',hidden:true},{className:'suggestion-name',text:channel.name},channel.isPrivate?{className:'suggestion-badge',text:'Private'}:null],function(){replaceTrigger(found,'<#'+channel.id+'>','#'+channel.name,'channel');hideSuggestions()}))});
showOptions('Channel suggestions',nodes);return;
}
if(found.type==='/'){
directory.commands.filter(function(command){if(api.thread&&command.app&&command.app!=='Slack')return false;return command.name.toLowerCase().indexOf(found.query)===0||(found.query.length>1&&(command.description+' '+command.app).toLowerCase().indexOf(found.query.slice(1))!==-1)}).slice(0,12).forEach(function(command){nodes.push(option(index++,[{className:'suggestion-name',text:command.name},{className:'suggestion-meta',text:command.description+(command.hint?' '+command.hint:'')},command.app?{className:'suggestion-badge',text:command.app}:null],function(){replaceTrigger(found,command.name,command.name,'command');hideSuggestions()}))});
showOptions('Shortcuts and slash commands',nodes);return;
}
if(found.type===':'){
if(suggestionTimer)window.clearTimeout(suggestionTimer);
var asked=found.query;
suggestionTimer=window.setTimeout(function(){fetchEmoji(asked).then(function(values){var current=trigger();if(!current||current.type!==':'||current.query!==asked)return;var emojiNodes=values.slice(0,10).map(function(value,position){return option(position,[{className:'suggestion-avatar',text:value.image_url?undefined:(value.display||''),image:value.image_url||'',hidden:true},{className:'suggestion-name',text:':'+value.name+':',label:':'+value.name+':'}],function(){rememberEmoji(value.name);replaceTrigger(current,value.name,'','emoji',{glyph:value.display||'',image:value.image_url||''});hideSuggestions()})});suggestion=current;showOptions('Emoji suggestions',emojiNodes)}).catch(function(){})},90);
}
}
function personOption(index,person,found){
var badge=person.self?'(you)':[person.bot?'App':'',!person.member&&!direct?'Not in channel':''].filter(Boolean).join(' \u00b7 ');
var displayOnly=!!window.sameoldchatPreferences&&window.sameoldchatPreferences.get('name-display','full')==='display';
var secondary=person.real&&person.real!==person.name&&!displayOnly?person.real:(person.display&&person.display!==person.name?person.display:'');
return option(index,[{className:'suggestion-avatar',text:person.avatar?undefined:person.initial,image:person.avatar,hidden:true},{className:'suggestion-name',text:person.name},secondary?{className:'suggestion-meta',text:secondary}:null,badge?{className:'suggestion-badge',text:badge}:null],function(){replaceTrigger(found,'<@'+person.id+'>','@'+person.name,'person');hideSuggestions()});
}
function acceptSuggestion(){var options=suggestionOptions();var chosen=options.filter(function(node){return node.getAttribute('aria-selected')==='true'})[0]||options[0];if(chosen&&chosen._accept){chosen._accept();return true}return false}
function currentBlock(node){while(node&&node.parentNode!==editor){if(node===editor)return null;node=node.parentNode}return node}
function normalizeEditor(){keepSelection(normalizeRoot)}
function normalizeRoot(){var run=[];function flush(){if(!run.length)return;var paragraph=doc.createElement('p');editor.insertBefore(paragraph,run[0]);run.forEach(function(node){paragraph.appendChild(node)});run=[]}Array.prototype.slice.call(editor.childNodes).forEach(function(node){if(isBlock(node)&&node.nodeName!=='DIV'){flush();return}if(node.nodeName==='DIV'){flush();var paragraph=doc.createElement('p');while(node.firstChild)paragraph.appendChild(node.firstChild);editor.replaceChild(paragraph,node);return}if(node.nodeName==='BR'){run.push(node);flush();return}run.push(node)});flush()}
function selectedBlocks(){normalizeEditor();var selection=window.getSelection();if(!selection.rangeCount)return[];var range=selection.getRangeAt(0);var first=currentBlock(range.startContainer);var last=currentBlock(range.endContainer);if(!first){if(!editor.firstChild)editor.appendChild(emptyParagraph());first=editor.firstChild}if(!last)last=first;var blocks=[];var node=first;while(node){blocks.push(node);if(node===last)break;node=node.nextSibling}return blocks}
function unwrap(node){var parent=node.parentNode;while(node.firstChild)parent.insertBefore(node.firstChild,node);parent.removeChild(node)}
function toggleQuote(){var selection=window.getSelection();if(!selection.rangeCount)return;var quote=closest(selection.anchorNode,'blockquote',editor);if(quote){Array.prototype.slice.call(quote.childNodes).forEach(function(child){if(!isBlock(child)){var paragraph=doc.createElement('p');quote.insertBefore(paragraph,child);paragraph.appendChild(child)}});unwrap(quote);return}var blocks=selectedBlocks();if(!blocks.length)return;var block=doc.createElement('blockquote');editor.insertBefore(block,blocks[0]);blocks.forEach(function(node){block.appendChild(node)});placeCaret(block.lastChild,block.lastChild.childNodes.length)}
function toggleCodeBlock(){var selection=window.getSelection();if(!selection.rangeCount)return;var pre=closest(selection.anchorNode,'pre',editor);if(pre){var lines=preText(pre).split('\n');var lastParagraph=null;lines.forEach(function(text){var paragraph=doc.createElement('p');if(text)paragraph.textContent=text;else paragraph.appendChild(doc.createElement('br'));editor.insertBefore(paragraph,pre);lastParagraph=paragraph});editor.removeChild(pre);if(lastParagraph)placeCaret(lastParagraph,lastParagraph.childNodes.length);return}var blocks=selectedBlocks();if(!blocks.length)return;var text=blocks.map(function(node){return node.nodeName==='UL'||node.nodeName==='OL'?Array.prototype.map.call(node.children,function(item){return cleanText(item.textContent)}).join('\n'):cleanText(node.textContent)}).join('\n');var block=doc.createElement('pre');block.textContent=text||'';if(!text)block.appendChild(doc.createElement('br'));editor.insertBefore(block,blocks[0]);blocks.forEach(function(node){editor.removeChild(node)});placeCaret(block,block.childNodes.length)}
function toggleInlineCode(){var selection=window.getSelection();if(!selection.rangeCount)return;var range=selection.getRangeAt(0);var code=closest(range.commonAncestorContainer,'code',editor);if(code){if(range.collapsed&&range.startContainer.nodeType===3&&range.startOffset===range.startContainer.data.length&&!range.startContainer.nextSibling){var after=doc.createTextNode('\u200B');code.parentNode.insertBefore(after,code.nextSibling);placeCaret(after,1);return}unwrap(code);return}var node=doc.createElement('code');if(range.collapsed){node.textContent='\u200B';range.insertNode(node);placeCaret(node.firstChild,1);return}node.textContent=range.toString();range.deleteContents();range.insertNode(node);var selected=doc.createRange();selected.selectNodeContents(node);selection.removeAllRanges();selection.addRange(selected)}
function keepSelection(work){var selection=window.getSelection();var range=selection.rangeCount?selection.getRangeAt(0):null;var saved=range?{start:range.startContainer,startOffset:range.startOffset,end:range.endContainer,endOffset:range.endOffset}:null;work();if(saved&&editor.contains(saved.start)&&editor.contains(saved.end)){var restored=doc.createRange();try{restored.setStart(saved.start,saved.startOffset);restored.setEnd(saved.end,saved.endOffset);selection.removeAllRanges();selection.addRange(restored)}catch(error){}}}
function toggleList(tag){
var selection=window.getSelection();if(!selection.rangeCount)return;
var list=closest(selection.anchorNode,'ul,ol',editor);
if(list&&list.nodeName===tag){keepSelection(function(){Array.prototype.slice.call(list.children).forEach(function(item){var paragraph=doc.createElement('p');while(item.firstChild)paragraph.appendChild(item.firstChild);if(!paragraph.firstChild)paragraph.appendChild(doc.createElement('br'));list.parentNode.insertBefore(paragraph,list)});list.parentNode.removeChild(list)});return}
if(list){keepSelection(function(){var renamed=doc.createElement(tag);while(list.firstChild)renamed.appendChild(list.firstChild);list.parentNode.replaceChild(renamed,list)});return}
var blocks=selectedBlocks();if(!blocks.length)return;
keepSelection(function(){var created=doc.createElement(tag);blocks[0].parentNode.insertBefore(created,blocks[0]);blocks.forEach(function(block){var item=doc.createElement('li');if(block.nodeName==='P'||block.nodeName==='DIV'){while(block.firstChild)item.appendChild(block.firstChild);block.parentNode.removeChild(block)}else item.appendChild(block);if(!item.firstChild)item.appendChild(doc.createElement('br'));created.appendChild(item)})});
}
function exec(command,argument){try{return doc.execCommand(command,false,argument)}catch(error){return false}}
function listItem(){var selection=window.getSelection();return selection.rangeCount?closest(selection.anchorNode,'li',editor):null}
function richFormat(kind){
editor.focus();
if(kind==='bold')exec('bold');
else if(kind==='italic')exec('italic');
else if(kind==='strike')exec('strikeThrough');
else if(kind==='code')toggleInlineCode();
else if(kind==='codeblock')toggleCodeBlock();
else if(kind==='quote')keepSelection(toggleQuote);
else if(kind==='ordered')toggleList('OL');
else if(kind==='bulleted')toggleList('UL');
sync();updatePressed();
}
function plainWrap(mark){
var start=field.selectionStart;var end=field.selectionEnd;var text=field.value;var selected=text.slice(start,end);var size=mark.length;
if(start>=size&&text.slice(start-size,start)===mark&&text.slice(end,end+size)===mark){field.value=text.slice(0,start-size)+selected+text.slice(end+size);field.setSelectionRange(start-size,end-size);return}
if(selected.length>=size*2&&selected.slice(0,size)===mark&&selected.slice(-size)===mark){field.value=text.slice(0,start)+selected.slice(size,-size)+text.slice(end);field.setSelectionRange(start,end-size*2);return}
field.value=text.slice(0,start)+mark+selected+mark+text.slice(end);field.setSelectionRange(start+size,end+size);
}
function plainLines(transform){var text=field.value;var start=text.lastIndexOf('\n',field.selectionStart-1)+1;var endIndex=text.indexOf('\n',field.selectionEnd);var end=endIndex<0?text.length:endIndex;var lines=text.slice(start,end).split('\n');var next=transform(lines).join('\n');field.value=text.slice(0,start)+next+text.slice(end);field.setSelectionRange(start,start+next.length)}
function plainFormat(kind){
field.focus();
if(kind==='bold')plainWrap('*');
else if(kind==='italic')plainWrap('_');
else if(kind==='strike')plainWrap('~');
else if(kind==='code')plainWrap(BT);
else if(kind==='codeblock')plainWrap(BT+BT+BT);
else if(kind==='quote')plainLines(function(lines){var all=lines.every(function(line){return /^> /.test(line)});return lines.map(function(line){return all?line.slice(2):'> '+line})});
else if(kind==='bulleted')plainLines(function(lines){var all=lines.every(function(line){return /^\u2022 /.test(line)});return lines.map(function(line){return all?line.slice(2):'\u2022 '+line})});
else if(kind==='ordered')plainLines(function(lines){var all=lines.every(function(line){return /^\d+\. /.test(line)});return lines.map(function(line,index){return all?line.replace(/^\d+\. /,''):(index+1)+'. '+line})});
sync();
}
function format(kind){if(kind==='link'){openLinkDialog();return}if(mode==='rich')richFormat(kind);else plainFormat(kind)}
api.format=format;
function updatePressed(){
if(!formatBar)return;
var state={};
if(mode==='rich'){var selection=window.getSelection();var node=selection&&selection.rangeCount?selection.anchorNode:null;if(node&&editor.contains(node)){try{state.bold=doc.queryCommandState('bold');state.italic=doc.queryCommandState('italic');state.strike=doc.queryCommandState('strikeThrough')}catch(error){}state.code=!!closest(node,'code',editor);state.codeblock=!!closest(node,'pre',editor);state.quote=!!closest(node,'blockquote',editor);state.ordered=!!closest(node,'ol',editor);state.bulleted=!!closest(node,'ul',editor)}}
Array.prototype.forEach.call(formatBar.querySelectorAll('[data-format][aria-pressed]'),function(button){button.setAttribute('aria-pressed',state[button.getAttribute('data-format')]?'true':'false')});
}
api.updatePressed=updatePressed;
var linkRange=null;
function openLinkDialog(){
var dialog=doc.getElementById('composer-link-dialog');if(!dialog||typeof dialog.showModal!=='function')return;
var textInput=doc.getElementById('composer-link-text');var urlInput=doc.getElementById('composer-link-url');var error=doc.getElementById('composer-link-error');
var selectedText='';var existing='';
if(mode==='rich'){var selection=window.getSelection();if(selection.rangeCount&&editor.contains(selection.anchorNode)){linkRange=selection.getRangeAt(0).cloneRange();selectedText=cleanText(linkRange.toString());var anchor=closest(linkRange.commonAncestorContainer,'a',editor);if(anchor){existing=anchor.getAttribute('href')||'';selectedText=cleanText(anchor.textContent);linkRange=doc.createRange();linkRange.selectNode(anchor)}}else linkRange=null}
else{linkRange={start:field.selectionStart,end:field.selectionEnd};selectedText=field.value.slice(field.selectionStart,field.selectionEnd)}
textInput.value=selectedText;urlInput.value=existing;error.hidden=true;error.textContent='';urlInput.removeAttribute('aria-invalid');
dialog.setAttribute('data-owner',form.id);dialog.showModal();(selectedText?urlInput:textInput).focus();
}
api.applyLink=function(text,url){
var href=normalizeHref(url);if(!href)return false;var label=text||href;
if(mode==='rich'){editor.focus();var selection=window.getSelection();if(linkRange&&linkRange.startContainer){selection.removeAllRanges();selection.addRange(linkRange)}else focus();var link=doc.createElement('a');link.href=href;link.textContent=label;insertNodes([link,doc.createTextNode(' ')])}
else{var range=linkRange||{start:field.selectionStart,end:field.selectionEnd};var markup=label===href?'<'+href+'>':'<'+href+'|'+label+'>';field.value=field.value.slice(0,range.start)+markup+field.value.slice(range.end);field.focus();var position=range.start+markup.length;field.setSelectionRange(position,position)}
linkRange=null;sync();return true;
};
function insertText(text){
if(mode==='rich'){focus();exec('insertText',text)}
else{var start=field.selectionStart;var end=field.selectionEnd;field.value=field.value.slice(0,start)+text+field.value.slice(end);field.focus();field.setSelectionRange(start+text.length,start+text.length)}
sync();
}
var caretRange=null;
function restoreCaret(){var range=caretRange;caretRange=null;if(!range||!editor.contains(range.startContainer))return;var selection=window.getSelection();selection.removeAllRanges();selection.addRange(range)}
api.insertEmoji=function(name,glyph,image){
if(mode==='rich'){focus();restoreCaret();insertNodes([makeEmoji(name,glyph,image)])}else insertText(':'+name+':');
sync();
};
api.insertCommand=function(command){setValue(command+' ');focus();if(mode==='plain'){field.setSelectionRange(field.value.length,field.value.length)}else{var last=editor.lastChild;if(last&&last.lastChild)placeCaret(last.lastChild,last.lastChild.nodeType===3?last.lastChild.data.length:0)}sync()};
function startMention(){
var before=caret();var needsSpace=before&&before.before&&!/\s$/.test(before.before);
insertText((needsSpace?' ':'')+'@');updateSuggestions();
}
function autoformat(event){
if(mode!=='rich'||event.inputType!=='insertText'||!event.data)return;
var info=caret();if(!info||!info.node)return;var node=info.node;var before=info.before;
var block=currentBlock(node);
if(event.data===' '&&block&&block.nodeName==='P'&&node===firstText(block)&&info.offset===before.length){
var marker=before.replace(/\u00A0/g,' ');
if(marker==='> '||marker==='- '||marker==='* '||marker==='1. '){node.data=node.data.slice(before.length);placeCaret(node,0);if(marker==='> ')toggleQuote();else toggleList(marker==='1. '?'OL':'UL');return}
}
if(event.data===BT&&/^\x60\x60\x60$/.test(cleanText(block?block.textContent:''))&&block&&block.nodeName==='P'){block.textContent='';block.appendChild(doc.createElement('br'));placeCaret(block,0);toggleCodeBlock();return}
var mark=event.data;if('*_~\x60'.indexOf(mark)===-1)return;
var escaped='\\'+mark;
var pattern=new RegExp('(^|[\\s(])'+escaped+'([^\\s'+escaped+'](?:[^'+escaped+']*[^\\s'+escaped+'])?)'+escaped+'$');
var found=pattern.exec(before.replace(/\u00A0/g,' '));if(!found)return;
var length=found[2].length+2;var start=info.offset-length;
var range=doc.createRange();range.setStart(node,start);range.setEnd(node,info.offset);range.deleteContents();
var element=doc.createElement(mark==='*'?'b':mark==='_'?'i':mark==='~'?'s':'code');element.textContent=found[2];range.insertNode(element);
var after=doc.createTextNode('\u200B');element.parentNode.insertBefore(after,element.nextSibling);placeCaret(after,1);
}
function firstText(block){var walker=doc.createTreeWalker(block,NodeFilter.SHOW_TEXT,null);return walker.nextNode()}
function newline(){
if(mode!=='rich')return false;
var selection=window.getSelection();var node=selection.rangeCount?selection.anchorNode:null;
if(closest(node,'pre',editor)){exec('insertLineBreak');return true}
var item=listItem();
if(item&&!cleanText(item.textContent).trim()){var owner=item.parentNode;var paragraph=doc.createElement('p');paragraph.appendChild(doc.createElement('br'));owner.parentNode.insertBefore(paragraph,owner.nextSibling);var after=Array.prototype.slice.call(owner.children).slice(Array.prototype.indexOf.call(owner.children,item)+1);owner.removeChild(item);if(after.length){var rest=doc.createElement(owner.nodeName);after.forEach(function(entry){rest.appendChild(entry)});owner.parentNode.insertBefore(rest,paragraph.nextSibling)}if(!owner.children.length)owner.parentNode.removeChild(owner);placeCaret(paragraph,0);return true}
if(item&&selection.rangeCount){var range=selection.getRangeAt(0);range.deleteContents();var tail=doc.createRange();tail.setStart(range.startContainer,range.startOffset);tail.setEnd(item,item.childNodes.length);var next=doc.createElement('li');next.appendChild(tail.extractContents());[item,next].forEach(function(entry){if(!cleanText(entry.textContent).trim()&&!entry.querySelector('img,[data-entity]')){entry.textContent='';entry.appendChild(doc.createElement('br'))}});item.parentNode.insertBefore(next,item.nextSibling);var start=firstText(next);if(start)placeCaret(start,0);else placeCaret(next,0);return true}
if(closest(node,'blockquote',editor)&&!item){var quoteBlock=closest(node,'p',editor);if(quoteBlock&&!cleanText(quoteBlock.textContent).trim()&&!quoteBlock.nextSibling){toggleQuote();return true}}
exec('insertParagraph');return true;
}
function editLast(){
var region=api.thread?doc.getElementById('thread-messages'):doc.getElementById('timeline');
if(!region)return false;
if(typeof window.sameoldchatEditLastMessage==='function'&&window.sameoldchatEditLastMessage(region))return true;
var messages=region.querySelectorAll('.message');
if(!messages.length)return false;
var focusMessage=page().focusMessage;var last=messages[messages.length-1];
if(focusMessage)focusMessage(last);else{last.focus();last.scrollIntoView({block:'nearest'})}
return true;
}
function onKeydown(event){
if(list&&!list.hidden){
if(event.key==='ArrowDown'||event.key==='ArrowUp'){event.preventDefault();var options=suggestionOptions();var current=options.findIndex(function(node){return node.getAttribute('aria-selected')==='true'});activate(event.key==='ArrowDown'?current+1:current-1);return}
if(event.key==='Escape'){event.preventDefault();event.stopPropagation();hideSuggestions();return}
if((event.key==='Enter'||event.key==='Tab')&&!event.shiftKey&&!event.ctrlKey&&!event.metaKey&&!event.altKey){if(acceptSuggestion()){event.preventDefault();return}}
}
if(event.isComposing)return;
var key=typeof event.key==='string'?event.key.toLowerCase():'';var code=event.code||'';
if(primary(event)){
var chosen='';
if(!event.shiftKey&&!event.altKey&&key==='b')chosen='bold';
else if(!event.shiftKey&&!event.altKey&&key==='i')chosen='italic';
else if(event.shiftKey&&!event.altKey&&(key==='x'||code==='KeyX'))chosen='strike';
else if(event.shiftKey&&event.altKey&&code==='KeyC')chosen='codeblock';
else if(event.shiftKey&&!event.altKey&&(key==='c'||code==='KeyC'))chosen='code';
else if(event.shiftKey&&!event.altKey&&(key==='u'||code==='KeyU'))chosen='link';
else if(event.shiftKey&&!event.altKey&&code==='Digit7')chosen='ordered';
else if(event.shiftKey&&!event.altKey&&code==='Digit8')chosen='bulleted';
else if(event.shiftKey&&!event.altKey&&code==='Digit9')chosen='quote';
if(chosen){event.preventDefault();format(chosen);return}
}
if(event.key==='Enter'&&!event.altKey){
var sendChord=prefs.enter==='send'?(!event.shiftKey&&!event.ctrlKey&&!event.metaKey):(primary(event)&&!event.shiftKey);
if(sendChord){event.preventDefault();submitMessage();return}
if(mode==='rich'&&!event.ctrlKey&&!event.metaKey){event.preventDefault();newline();sync();return}
if(mode==='plain'&&prefs.enter==='newline'&&!event.shiftKey)return;
return;
}
if(event.key==='ArrowUp'&&!event.shiftKey&&!event.ctrlKey&&!event.metaKey&&!event.altKey&&!field.value&&!attachments.length){if(editLast())event.preventDefault()}
}
function resetEditor(){renderMarkup(editor,'');placeCaret(editor.firstChild,0);['bold','italic','strikeThrough'].forEach(function(command){try{if(doc.queryCommandState(command))exec(command)}catch(error){}});field.value='';refresh();persistDraft()}
function mountEditor(){
if(editor)return;
editor=doc.createElement('div');editor.className='composer-editor';editor.id=prefix+'text-editor';editor.setAttribute('contenteditable','true');editor.setAttribute('role','combobox');editor.setAttribute('aria-labelledby',prefix+'text-label');editor.setAttribute('aria-describedby',field.getAttribute('aria-describedby')||'');editor.setAttribute('aria-autocomplete','list');editor.setAttribute('aria-haspopup','listbox');editor.setAttribute('aria-controls',prefix+'composer-suggestions');editor.setAttribute('aria-expanded','false');editor.setAttribute('aria-keyshortcuts',field.getAttribute('aria-keyshortcuts')||'Enter');editor.setAttribute('spellcheck','true');editor.setAttribute('data-placeholder',field.getAttribute('placeholder')||'');editor.setAttribute('tabindex','0');
field.parentNode.insertBefore(editor,field.nextSibling);
editor.addEventListener('keydown',onKeydown);
editor.addEventListener('beforeinput',function(event){var type=event.inputType||'';if(!/^(insertText|insertReplacementText|insertFromPaste|delete)/.test(type))return;var selection=window.getSelection();if(!selection.rangeCount||selection.isCollapsed)return;var whole=cleanText(editor.textContent).replace(/\s+/g,'');if(!whole||cleanText(selection.toString()).replace(/\s+/g,'')!==whole)return;var text=type.indexOf('insert')===0?(typeof event.data==='string'?event.data:(event.dataTransfer?event.dataTransfer.getData('text/plain'):'')):'';event.preventDefault();resetEditor();if(text.indexOf('\n')!==-1)exec('insertText',text);else if(text){var line=editor.firstChild;var typed=doc.createTextNode(text);while(line.firstChild)line.removeChild(line.firstChild);line.appendChild(typed);placeCaret(typed,text.length)}sync();updateSuggestions();updatePressed()});
editor.addEventListener('input',function(event){if(Array.prototype.some.call(editor.childNodes,function(node){return !isBlock(node)&&!(node.nodeType===3&&!node.data)}))normalizeEditor();if(/^(delete|insertText|insertReplacementText)/.test(event.inputType||'')&&!cleanText(editor.textContent).trim()&&!editor.querySelector('[data-entity],img')&&!(editor.childNodes.length===1&&editor.firstChild.nodeName==='P')){resetEditor()}autoformat(event);sync();updateSuggestions();updatePressed();if(field.value)sendTyping(csrf)});
editor.addEventListener('click',function(){updateSuggestions();updatePressed()});
editor.addEventListener('keyup',function(event){if(event.key==='ArrowLeft'||event.key==='ArrowRight'||event.key==='Home'||event.key==='End'){updateSuggestions();updatePressed()}});
editor.addEventListener('blur',function(){var selection=window.getSelection();caretRange=selection&&selection.rangeCount&&editor.contains(selection.anchorNode)?selection.getRangeAt(0).cloneRange():null;window.setTimeout(function(){if(!form.contains(doc.activeElement))hideSuggestions()},150)});
editor.addEventListener('paste',function(event){var data=event.clipboardData;if(!data)return;if(data.files&&data.files.length&&uploadFile){event.preventDefault();stageFiles(data.files);return}var text=data.getData('text/plain');if(!text)return;event.preventDefault();var selection=window.getSelection();var inCode=selection.rangeCount&&closest(selection.anchorNode,'code,pre',editor);var trimmed=text.trim();if(!inCode&&selection.rangeCount&&!selection.isCollapsed&&safeHref(trimmed)&&!/\s/.test(trimmed)){exec('createLink',trimmed);sync();return}exec('insertText',text);sync()});
editor.addEventListener('drop',function(event){if(event.dataTransfer&&event.dataTransfer.files&&event.dataTransfer.files.length)event.preventDefault()});
}
api.applyPrefs=function(){
var wantRich=!prefs.markup&&!!(window.getSelection&&doc.execCommand);
if(wantRich&&mode!=='rich'){var hadFocus=doc.activeElement===field;mountEditor();renderMarkup(editor,field.value);field.hidden=true;editor.hidden=false;mode='rich';if(hadFocus)focus()}
else if(!wantRich&&mode==='rich'){var focused=editor.contains(doc.activeElement)||doc.activeElement===editor;field.value=serialize(editor);editor.hidden=true;field.hidden=false;mode='plain';if(focused)field.focus()}
if(formatBar){var shown=prefs.formatting;formatBar.hidden=!shown}
if(formatToggle){formatToggle.hidden=false;formatToggle.setAttribute('aria-pressed',prefs.formatting?'true':'false');var toggleLabel=prefs.formatting?'Hide formatting':'Show formatting';formatToggle.setAttribute('aria-label',toggleLabel);formatToggle.setAttribute('data-tip',toggleLabel)}
if(hint){var sendLabel=apple?'Return':'Enter';var modifier=apple?'\u2318':'Ctrl';var span=hint.querySelector('[data-hint-send]')||hint;span.textContent='';var parts=prefs.enter==='send'?[['kbd','Shift'],['',' + '],['kbd',sendLabel],['',' to add a new line']]:[['kbd',modifier],['',' + '],['kbd',sendLabel],['',' to send']];parts.forEach(function(part){if(part[0]){var kbd=doc.createElement('kbd');kbd.textContent=part[1];span.appendChild(kbd)}else span.appendChild(doc.createTextNode(part[1]))})}
var target=surface();target.setAttribute('aria-keyshortcuts',prefs.enter==='send'?'Enter Shift+Enter':(apple?'Meta+Enter Enter':'Control+Enter Enter'));
refresh();
};
field.addEventListener('keydown',onKeydown);
field.addEventListener('input',function(){sync();updateSuggestions();if(field.value)sendTyping(csrf)});
field.addEventListener('click',updateSuggestions);
field.addEventListener('blur',function(){window.setTimeout(function(){if(!form.contains(doc.activeElement))hideSuggestions()},150)});
field.addEventListener('paste',function(event){var data=event.clipboardData;if(!data)return;if(data.files&&data.files.length&&uploadFile){event.preventDefault();stageFiles(data.files);return}var text=(data.getData('text/plain')||'').trim();if(field.selectionStart!==field.selectionEnd&&safeHref(text)&&!/\s/.test(text)){event.preventDefault();var start=field.selectionStart;var end=field.selectionEnd;var label=field.value.slice(start,end);var markup='<'+text+'|'+label+'>';field.value=field.value.slice(0,start)+markup+field.value.slice(end);field.setSelectionRange(start+markup.length,start+markup.length);sync()}});
field.required=false;field.removeAttribute('required');field.removeAttribute('maxlength');
if(formatBar)formatBar.addEventListener('mousedown',function(event){if(event.target.closest('[data-format]'))event.preventDefault()});
if(formatBar)formatBar.addEventListener('click',function(event){var button=event.target.closest('[data-format]');if(button)format(button.getAttribute('data-format'))});
if(formatToggle)formatToggle.addEventListener('click',function(){setPref('formatting',prefs.formatting?'hidden':'shown');focus()});
if(mentionButton){mentionButton.hidden=false;mentionButton.addEventListener('click',function(){focus();startMention()})}
form.addEventListener('focusin',function(){active=api;useDirectory(form)});
form.addEventListener('click',function(event){var action=event.target.closest('[data-composer-action]');if(!action)return;var kind=action.getAttribute('data-composer-action');var menu=action.closest('details');if(menu)menu.open=false;active=api;
if(kind==='upload'&&uploadFile)uploadFile.click();
if(kind==='snippet')openSnippet();
if(kind==='shortcuts'||kind==='slash')openShortcuts(action);
});
var customToggle=form.querySelector('[data-schedule-custom]>summary');
if(customToggle)customToggle.addEventListener('click',function(event){event.preventDefault();var menu=customToggle.closest('.schedule-menu');if(menu)menu.open=false;openScheduleDialog(api)});
function renderPreview(){
if(!preview)return;preview.textContent='';
attachments.forEach(function(file,index){preview.appendChild(fileCard(file.name||'Staged file',file.size,file.mime_type,thumbnails[file.upload_id],index,null))});
pendingUploads.forEach(function(pending){preview.appendChild(fileCard(pending.file.name||'Pasted file',pending.file.size,pending.file.type,pending.thumb,-1,pending))});
if(draftInput)draftInput.value=JSON.stringify(attachments);
var hidden=byId('upload-draft-attachments');if(hidden)hidden.value=JSON.stringify(attachments);
refresh();
}
function fileCard(name,size,mime,thumb,index,pending){
var card=doc.createElement('span');card.className='staged-file';
var visual=doc.createElement('span');visual.className='staged-file-thumb';visual.setAttribute('aria-hidden','true');
if(thumb){var img=doc.createElement('img');img.src=thumb;img.alt='';visual.appendChild(img)}else visual.textContent=(name.split('.').length>1?name.split('.').pop():'file').slice(0,4);
var copy=doc.createElement('span');copy.className='staged-file-copy';
var title=doc.createElement('span');title.className='staged-file-name';title.textContent=name;
var meta=doc.createElement('span');meta.className='staged-file-meta';meta.textContent=formatSize(size||0)+(pending?' \u00b7 '+(pending.progress<100?'Uploading '+pending.progress+'%':'Saving'):'');
copy.appendChild(title);copy.appendChild(meta);
if(pending){var bar=doc.createElement('progress');bar.max=100;bar.value=pending.progress;bar.setAttribute('aria-label','Uploading '+name);copy.appendChild(bar)}
card.appendChild(visual);card.appendChild(copy);
if(index>=0){
if(attachments.length>1&&index>0){var earlier=doc.createElement('button');earlier.type='button';earlier.className='staged-file-move';earlier.setAttribute('aria-label','Move '+name+' earlier');earlier.textContent='\u2190';earlier.addEventListener('click',function(){moveAttachment(index,-1)});copy.appendChild(earlier)}
if(attachments.length>1&&index<attachments.length-1){var later=doc.createElement('button');later.type='button';later.className='staged-file-move';later.setAttribute('aria-label','Move '+name+' later');later.textContent='\u2192';later.addEventListener('click',function(){moveAttachment(index,1)});copy.appendChild(later)}
var remove=doc.createElement('button');remove.type='button';remove.className='staged-file-remove';remove.setAttribute('aria-label','Remove '+name);remove.textContent='\u00d7';remove.addEventListener('click',function(){removeAttachment(index)});card.appendChild(remove);
}
return card;
}
function moveAttachment(index,direction){var target=index+direction;if(target<0||target>=attachments.length)return;var moved=attachments[index];attachments[index]=attachments[target];attachments[target]=moved;renderPreview();persistDraftNow().then(function(){announce((moved.name||'Staged file')+' moved.')})}
function removeAttachment(index){if(index<0||index>=attachments.length)return;var name=attachments[index].name||'Staged file';attachments.splice(index,1);renderPreview();persistDraftNow().then(function(){announce(name+' removed from the draft.')});focus()}
function stageFiles(fileList){
if(!uploadForm||!fileList||!fileList.length)return false;
var files=Array.prototype.slice.call(fileList);
if(attachments.length+pendingUploads.length+files.length>10){showError('A message can include up to ten files.');return false}
clearError();
var pending=files.map(function(file){var entry={file:file,progress:0,thumb:''};if(/^image\//.test(file.type||'')&&file.size<8388608&&window.FileReader){var reader=new FileReader();reader.addEventListener('load',function(){entry.thumb=String(reader.result||'');attachments.forEach(function(staged){if(staged._entry===entry)thumbnails[staged.upload_id]=entry.thumb});renderPreview()});reader.readAsDataURL(file)}return entry});
Array.prototype.push.apply(pendingUploads,pending);staging++;renderPreview();
var body=new FormData();body.set('_csrf',csrf);body.set('text',field.value);body.set('draft_attachments',JSON.stringify(attachments));files.forEach(function(file){body.append('file',file,file.name||'pasted-file')});
var request=new XMLHttpRequest();
var done=function(){staging--;pendingUploads=pendingUploads.filter(function(entry){return pending.indexOf(entry)===-1});if(uploadFile)uploadFile.value=''};
request.upload.addEventListener('progress',function(event){if(!event.lengthComputable)return;var percent=Math.min(99,Math.round(event.loaded/event.total*100));pending.forEach(function(entry){entry.progress=percent});renderPreview()});
request.addEventListener('load',function(){
if(request.status>=200&&request.status<300){var result=null;try{result=JSON.parse(request.responseText)}catch(error){}
var before=attachments.map(function(entry){return entry.upload_id});
attachments=result&&Array.isArray(result.attachments)?result.attachments:attachments;
var added=attachments.filter(function(entry){return before.indexOf(entry.upload_id)===-1});added.forEach(function(entry,position){if(!pending[position])return;if(pending[position].thumb)thumbnails[entry.upload_id]=pending[position].thumb;else Object.defineProperty(entry,'_entry',{value:pending[position],enumerable:false})});
done();renderPreview();persistDraftNow().then(function(){announce(attachments.length===1?'One file is saved with this draft.':attachments.length+' files are saved with this draft.')});return}
done();renderPreview();showError(clip(request.responseText)||'Those files could not be added. Try again.');
});
request.addEventListener('error',function(){done();renderPreview();showError('Those files could not be uploaded. Check your connection and try again.')});
request.open('POST',uploadForm.getAttribute('action'));request.setRequestHeader('HX-Request','true');request.send(body);
return true;
}
api.stageFiles=stageFiles;
api.upload=function(){if(uploadFile){uploadFile.click();return true}return false};
if(uploadFile)uploadFile.addEventListener('change',function(){if(uploadFile.files&&uploadFile.files.length)stageFiles(uploadFile.files)});
if(uploadForm)uploadForm.addEventListener('submit',function(event){event.preventDefault();if(uploadFile&&uploadFile.files&&uploadFile.files.length)stageFiles(uploadFile.files)});
form.addEventListener('dragover',function(event){if(uploadFile&&event.dataTransfer&&event.dataTransfer.types&&Array.prototype.indexOf.call(event.dataTransfer.types,'Files')!==-1){event.preventDefault();form.classList.add('is-dragging')}});
form.addEventListener('dragleave',function(){form.classList.remove('is-dragging')});
form.addEventListener('drop',function(event){form.classList.remove('is-dragging');var files=event.dataTransfer&&event.dataTransfer.files;if(files&&files.length&&stageFiles(files))event.preventDefault()});
function needsConfirmation(text){var match=/<!(channel|here|everyone)>/.exec(text);if(!match||direct||memberCount<6)return null;return match[1]}
function outboxItem(className,state,text){var item=doc.createElement('div');item.className='composer-outbox-item '+className;item.setAttribute('role','group');var copy=doc.createElement('p');copy.className='composer-outbox-text';copy.textContent=text;var label=doc.createElement('p');label.className='composer-outbox-state';label.textContent=state;var actions=doc.createElement('div');actions.className='composer-outbox-actions';item.appendChild(copy);item.appendChild(label);item.appendChild(actions);return{item:item,copy:copy,state:label,actions:actions}}
function actionButton(actions,label,handler){var button=doc.createElement('button');button.type='button';button.textContent=label;button.addEventListener('click',handler);actions.appendChild(button);return button}
function previewText(text){var holder=doc.createElement('div');renderMarkup(holder,text);return cleanText(holder.textContent)||'(files)'}
function failedSend(body,text,reason){
if(!outbox)return;
var parts=outboxItem('is-failed','Not sent. '+reason,previewText(text));parts.item.setAttribute('aria-label','Message not sent');
actionButton(parts.actions,'Retry',function(){parts.state.textContent='Sending\u2026';post(body,text,parts.item)});
actionButton(parts.actions,'Delete',function(){parts.item.remove();announce('The unsent message was deleted.');focus()});
outbox.appendChild(parts.item);
announce('Your message was not sent. Use Retry to send it again.');
}
function mentionPrompt(text){
if(direct||!outbox)return;
var seen={};var outsiders=[];var pattern=/<@([A-Z0-9]+)(?:\|[^>]*)?>/g;var match;
while((match=pattern.exec(text))){var person=findEntry(directory.people,match[1]);if(person&&!person.member&&!person.self&&!seen[person.id]){seen[person.id]=true;outsiders.push(person)}}
if(!outsiders.length)return;
var names=outsiders.map(function(person){return '@'+person.name});
var who=names.length===1?names[0]:names.slice(0,-1).join(', ')+' and '+names[names.length-1];
var parts=outboxItem('is-prompt',inviteURL?'Would you like to add '+plural(outsiders.length,'them','them')+'? They won\u2019t be notified until they join.':'They won\u2019t be notified, because only channel members can be added by someone with permission.',who+' '+plural(outsiders.length,'isn\u2019t','aren\u2019t')+' in #'+channelLabel.replace(/^#/,'')+'.');
parts.item.setAttribute('aria-label','Mentioned people are not in this channel');
if(inviteURL){actionButton(parts.actions,'Add them',function(){parts.state.textContent='Adding\u2026';Promise.all(outsiders.map(function(person){var body=new FormData();body.set('_csrf',csrf);body.set('user',person.id);return fetch(inviteURL,{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){if(!response.ok)return response.text().then(function(message){throw new Error(clip(message)||'That person could not be added.')});person.member=true})})).then(function(){parts.item.remove();announce('Added '+who+' to #'+channelLabel.replace(/^#/,'')+'.');focus()}).catch(function(error){parts.state.textContent=error.message||'They could not be added.'})})}
actionButton(parts.actions,'Do nothing',function(){parts.item.remove();focus()});
outbox.appendChild(parts.item);
announce(who+' '+plural(outsiders.length,'isn\u2019t','aren\u2019t')+' in this channel.');
}
function clearSent(text){if(field.value===text){attachments=[];setValue('');if(mode==='rich')renderMarkup(editor,'');renderPreview();persistDraft()}}
function post(body,text,retrying){
sending=true;if(sendButton)sendButton.disabled=true;clearError();
var targetSelector=form.getAttribute('hx-target');var target=targetSelector?doc.querySelector(targetSelector):null;
var release=function(){sending=false;if(sendButton)sendButton.disabled=false};
var committed=false;
var catchUp=function(){if(!committed||!page().refresh)return;page().refresh(true).catch(function(){announce('Sent. The conversation will update when the connection recovers.')})};
return fetch(form.getAttribute('hx-post'),{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){
if(!response.ok)return response.text().then(function(message){var error=new Error(clip(message));error.status=response.status;throw error});
if(retrying)retrying.remove();
if(response.headers.get('X-SameOldChat-Draft-Cleanup')==='failed')announce('Your message was sent, but its old draft could not be cleared. Delete it from Drafts & sent.');
var redirect=response.headers.get('HX-Redirect');
if(redirect){clearSent(text);if(ownPath(redirect)){var next=new URL(redirect,window.location.href);if(next.pathname+next.search===window.location.pathname+window.location.search){window.location.hash=next.hash;window.location.reload()}else window.location.assign(redirect)}return null}
if(response.status===204){clearSent(text);committed=true;if(response.headers.get('X-SameOldChat-Sent-View')==='pending')announce('Sent. The conversation is refreshing to show it.');return null}
return response.text().then(function(html){
clearSent(text);
var newest=form.getAttribute('data-newest');if(!api.thread&&newest&&ownPath(newest)){window.location.assign(newest);return null}
if(target&&page().append){page().append(target,html);target.scrollTop=target.scrollHeight}
var timeline=doc.getElementById('timeline');if(timeline&&!api.thread)timeline.scrollTop=timeline.scrollHeight;
mentionPrompt(text);
committed=true;
});
}).catch(function(error){
if(error&&error.name==='AbortError')return;
var transient=!error||!error.status||error.status>=500||error.status===429;
if(error&&!error.status)error=new Error('Check your connection and try again.');else if(error&&error.status===429&&!error.message)error.message='Too many messages were sent at once. Wait a moment and retry.';
if(transient){if(!retrying){failedSend(body,text,error&&error.message||'Check your connection and try again.');clearSent(text)}else{var state=retrying.querySelector('.composer-outbox-state');if(state)state.textContent='Not sent. '+(error&&error.message||'Check your connection and try again.')}return}
if(retrying){var stateNode=retrying.querySelector('.composer-outbox-state');if(stateNode)stateNode.textContent='Not sent. '+(error.message||'The message was refused.');return}
showError(error.message||'Your message was not sent. It is still in the composer.');
}).then(release,release).then(catchUp);
}
function submitMessage(confirmed){
if(sending)return;
if(mode==='rich')field.value=serialize(editor);
var text=field.value;
if(!text.replace(/[\s\u200B]+/g,'')&&!attachments.length){if(staging)announce('Wait for the files to finish uploading, then send again.');return}
if(staging){announce('Wait for the files to finish uploading, then send again.');return}
var over=runes(text)-LIMIT;
if(over>0){showError('Your message is too long. Shorten it by '+over.toLocaleString()+' '+plural(over,'character','characters')+' to send it.');return}
var broadcast=confirmed?null:needsConfirmation(text);
if(broadcast){confirmBroadcast(broadcast,memberCount,channelLabel,function(){submitMessage(true)});return}
hideSuggestions();
if(draftTimer){window.clearTimeout(draftTimer);draftTimer=null}
var body=new FormData(form);body.set('text',text);body.set('draft_attachments',JSON.stringify(attachments));body.set('client_msg_id',uuid());['schedule_at','post_at','schedule_preset'].forEach(function(name){body.delete(name)});
post(body,text,null);
}
api.submit=submitMessage;
function scheduleAt(when){
if(mode==='rich')field.value=serialize(editor);
var text=field.value;
if(!text.replace(/[\s\u200B]+/g,'')&&!attachments.length){showError('Write a message or add a file before scheduling it.');return}
if(staging){announce('Wait for the files to finish uploading, then schedule again.');return}
var action=form.querySelector('[data-schedule-preset]');action=action?action.getAttribute('formaction'):'';if(!ownPath(action))return;
var body=new FormData(form);body.set('text',text);body.set('draft_attachments',JSON.stringify(attachments));body.set('post_at',String(Math.floor(when.getTime()/1000)));body.delete('client_msg_id');
sending=true;clearError();
fetch(action,{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){
if(!response.ok)return response.text().then(function(message){throw new Error(clip(message)||'The message could not be scheduled.')});
clearSent(text);var redirect=response.headers.get('HX-Redirect');if(redirect&&ownPath(redirect)){window.location.assign(redirect);return}
announce('Your message is scheduled.');
}).catch(function(error){showError(error.message||'The message could not be scheduled.')}).then(function(){sending=false},function(){sending=false});
}
api.scheduleAt=scheduleAt;
form.addEventListener('submit',function(event){
event.preventDefault();event.stopPropagation();
var submitter=event.submitter;
var preset=submitter&&submitter.getAttribute('data-schedule-preset');
if(submitter&&submitter.getAttribute('name')==='schedule_preset'){
var menu=submitter.closest('details.schedule-menu');if(menu)menu.open=false;
if(preset){scheduleAt(presetTime(preset));return}
var custom=scheduleInput&&scheduleInput.value?new Date(scheduleInput.value):null;
if(!custom||isNaN(custom.getTime())){showError('Choose a date and time for the scheduled message.');return}
scheduleAt(custom);return;
}
submitMessage(false);
},false);
if(window.sameoldchatLifecycle)window.sameoldchatLifecycle.leaving(function(){if(draftDirty||draftTimer)saveDraftRemote(true)});
window.addEventListener('beforeunload',function(event){if(staging){event.preventDefault();event.returnValue=''}});
if(!field.value){try{var saved=localStorage.getItem(draftKey);if(saved)field.value=saved}catch(error){}}
api.applyPrefs();
renderPreview();
refresh();
if(field.value)persistDraft();
return api;
}
function presetTime(preset){var now=new Date();var date=new Date(now.getFullYear(),now.getMonth(),now.getDate(),9,0,0,0);if(preset==='monday'){var days=(8-now.getDay())%7;if(days===0)days=7;date.setDate(date.getDate()+days)}else date.setDate(date.getDate()+1);return date}
function zoneName(){try{return Intl.DateTimeFormat().resolvedOptions().timeZone||''}catch(error){return ''}}
function recipientIn(root){var node=root&&root.querySelector?root.querySelector('[data-schedule-recipient]'):null;return node?{name:node.getAttribute('data-recipient-name'),zone:node.getAttribute('data-recipient-zone'),node:node}:null}
function clockIn(when,zone,withDay){var options={hour:'numeric',minute:'2-digit'};if(withDay)options.weekday='long';if(zone)options.timeZone=zone;try{return new Intl.DateTimeFormat([],options).format(when)}catch(error){return ''}}
function forRecipient(when,recipient){if(!recipient)return '';var theirs=clockIn(when,recipient.zone,true);if(!theirs||theirs===clockIn(when,'',true))return '';return theirs+' for '+recipient.name}
function labelSchedules(root){Array.prototype.forEach.call(root.querySelectorAll('[data-schedule-zone]'),function(node){var zone=zoneName();if(zone)node.textContent='Times are in your time zone ('+zone.replace(/_/g,' ')+').'});
Array.prototype.forEach.call(root.querySelectorAll('[data-schedule-preset]'),function(button){var when=presetTime(button.getAttribute('data-schedule-preset'));var label=(button.getAttribute('data-schedule-preset')==='monday'?'Monday':'Tomorrow')+' at '+when.toLocaleTimeString([],{hour:'numeric',minute:'2-digit'});var theirs=forRecipient(when,recipientIn(button.closest('form')));button.textContent=theirs?label+' ('+theirs+')':label});
Array.prototype.forEach.call(root.querySelectorAll('[data-schedule-recipient]'),function(node){var recipient=recipientIn(node.parentNode);var now=new Date();var theirs=clockIn(now,recipient.zone,false);if(!theirs)return;node.textContent=theirs===clockIn(now,'',false)?recipient.name+' is in your time zone.':'It\u2019s '+theirs+' for '+recipient.name+'.'})}
var linkDialog=doc.getElementById('composer-link-dialog');
if(linkDialog){var linkForm=linkDialog.querySelector('[data-link-form]');linkForm.addEventListener('submit',function(event){event.preventDefault();var owner=composers.filter(function(composer){return composer.form.id===linkDialog.getAttribute('data-owner')})[0]||active;var urlInput=doc.getElementById('composer-link-url');var error=doc.getElementById('composer-link-error');if(!normalizeHref(urlInput.value)){error.textContent='Enter a web address that starts with http:// or https://, or an email link.';error.hidden=false;urlInput.setAttribute('aria-invalid','true');urlInput.focus();return}linkDialog.close();if(owner)owner.applyLink(doc.getElementById('composer-link-text').value.trim(),urlInput.value)});
linkDialog.addEventListener('close',function(){var owner=composers.filter(function(composer){return composer.form.id===linkDialog.getAttribute('data-owner')})[0]||active;if(owner)owner.focus()});}
var scheduleDialog=doc.getElementById('composer-schedule-dialog');var scheduleOwner=null;var scheduleRecipient=null;
function describeScheduleRecipient(){var line=doc.getElementById('composer-schedule-recipient');if(!line)return;var date=doc.getElementById('composer-schedule-date').value;var time=doc.getElementById('composer-schedule-time').value;var when=new Date(date+'T'+time);var theirs=date&&time&&!isNaN(when.getTime())?forRecipient(when,scheduleRecipient):'';line.textContent=theirs?'That\u2019s '+theirs+'.':'';line.hidden=!theirs}
if(scheduleDialog){['composer-schedule-date','composer-schedule-time'].forEach(function(id){var input=doc.getElementById(id);if(input)input.addEventListener('input',describeScheduleRecipient)})}
function openScheduleDialog(composer){if(!scheduleDialog||typeof scheduleDialog.showModal!=='function')return;scheduleOwner=composer;var when=presetTime('tomorrow');var pad=function(value){return String(value).padStart(2,'0')};doc.getElementById('composer-schedule-date').value=when.getFullYear()+'-'+pad(when.getMonth()+1)+'-'+pad(when.getDate());doc.getElementById('composer-schedule-time').value='09:00';var zone=zoneName();var note=doc.getElementById('composer-schedule-zone');if(note&&zone)note.textContent='Times are in your time zone ('+zone.replace(/_/g,' ')+').';scheduleRecipient=recipientIn(composer&&composer.form);describeScheduleRecipient();var error=doc.getElementById('composer-schedule-error');error.hidden=true;scheduleDialog.showModal();doc.getElementById('composer-schedule-date').focus()}
if(scheduleDialog){scheduleDialog.querySelector('[data-schedule-form]').addEventListener('submit',function(event){event.preventDefault();var date=doc.getElementById('composer-schedule-date').value;var time=doc.getElementById('composer-schedule-time').value;var when=new Date(date+'T'+time);var error=doc.getElementById('composer-schedule-error');if(!date||!time||isNaN(when.getTime())){error.textContent='Choose a date and a time.';error.hidden=false;return}if(when.getTime()<=Date.now()){error.textContent='Choose a time in the future.';error.hidden=false;return}scheduleDialog.close();if(scheduleOwner)scheduleOwner.scheduleAt(when)});scheduleDialog.addEventListener('close',function(){if(scheduleOwner)scheduleOwner.focus()})}
var broadcastDialog=doc.getElementById('composer-broadcast-dialog');var broadcastThen=null;
function confirmBroadcast(kind,count,channel,then){if(!broadcastDialog||typeof broadcastDialog.showModal!=='function'){then();return}broadcastThen=then;doc.getElementById('composer-broadcast-title').textContent='Notify '+(kind==='here'?'everyone online in':'everyone in')+' #'+channel.replace(/^#/,'')+'?';doc.getElementById('composer-broadcast-body').textContent=kind==='here'?'Using @here will notify everyone who is active in this channel of '+count+' members.':kind==='everyone'?'Using @everyone will notify everyone in your workspace.':'Using @channel will notify all '+count+' members of this channel.';broadcastDialog.returnValue='';broadcastDialog.showModal()}
if(broadcastDialog)broadcastDialog.addEventListener('close',function(){var then=broadcastThen;broadcastThen=null;if(broadcastDialog.returnValue==='send'&&then)then();else if(active)active.focus()});
var snippetDialog=doc.getElementById('composer-snippet-dialog');var snippetOwner=null;
function openSnippet(){if(!snippetDialog||typeof snippetDialog.showModal!=='function')return;snippetOwner=active;doc.getElementById('composer-snippet-name').value='';doc.getElementById('composer-snippet-body').value='';snippetDialog.showModal();doc.getElementById('composer-snippet-body').focus()}
if(snippetDialog){snippetDialog.querySelector('[data-snippet-form]').addEventListener('submit',function(event){event.preventDefault();var body=doc.getElementById('composer-snippet-body').value;if(!body.trim()){doc.getElementById('composer-snippet-body').focus();return}var title=doc.getElementById('composer-snippet-name').value.trim()||'snippet';var name=/\.[a-z0-9]+$/i.test(title)?title:title+'.txt';snippetDialog.close();if(snippetOwner&&window.File)snippetOwner.stageFiles([new File([body],name,{type:'text/plain'})])});snippetDialog.addEventListener('close',function(){if(snippetOwner)snippetOwner.focus()})}
Array.prototype.forEach.call(doc.querySelectorAll('.composer-dialog [data-dialog-cancel]'),function(button){button.addEventListener('click',function(){var dialog=button.closest('dialog');if(dialog)dialog.close()})});
var shortcutBrowser=doc.getElementById('shortcut-browser');var shortcutQuery=doc.getElementById('shortcut-browser-query');var shortcutResults=doc.getElementById('shortcut-browser-results');var shortcutEmpty=doc.getElementById('shortcut-browser-empty');var shortcutClose=doc.getElementById('shortcut-browser-close');var shortcutTrigger=null;
function filterShortcuts(){if(!shortcutResults)return;var query=(shortcutQuery.value||'').trim().toLowerCase();var shown=0;Array.prototype.forEach.call(shortcutResults.children,function(item){var visible=!query||(item.getAttribute('data-shortcut-search')||'').toLowerCase().indexOf(query)!==-1;item.hidden=!visible;if(visible)shown++});if(shortcutEmpty)shortcutEmpty.hidden=shown!==0}
function openShortcuts(trigger){if(!shortcutBrowser||typeof shortcutBrowser.showModal!=='function')return;shortcutTrigger=trigger;shortcutBrowser.showModal();shortcutQuery.value='';filterShortcuts();shortcutQuery.focus()}
if(shortcutQuery)shortcutQuery.addEventListener('input',filterShortcuts);
if(shortcutClose)shortcutClose.addEventListener('click',function(){shortcutBrowser.close()});
if(shortcutResults)shortcutResults.addEventListener('click',function(event){var choice=event.target.closest('[data-browser-command]');if(!choice)return;var owner=active||composers[0];shortcutTrigger=null;shortcutBrowser.close();if(owner)owner.insertCommand(choice.getAttribute('data-browser-command'))});
if(shortcutBrowser){shortcutBrowser.addEventListener('click',function(event){if(event.target===shortcutBrowser)shortcutBrowser.close()});shortcutBrowser.addEventListener('close',function(){if(shortcutTrigger&&doc.contains(shortcutTrigger))shortcutTrigger.focus();shortcutTrigger=null})}
var clipDialog=doc.getElementById('clip-recorder');var clipTitle=doc.getElementById('clip-recorder-title');var clipStatus=doc.getElementById('clip-recorder-status');var clipPreview=doc.getElementById('clip-recorder-preview');var clipStop=doc.getElementById('clip-recorder-stop');var clipCancel=doc.getElementById('clip-recorder-cancel');
var clipRecorder=null,clipStream=null,clipChunks=[],clipCancelled=false,clipLimit=null,clipElapsed=null,clipStartedAt=0,clipTrigger=null,clipGeneration=0,clipOwner=null;
function clearClipTimers(){if(clipLimit)window.clearTimeout(clipLimit);if(clipElapsed)window.clearInterval(clipElapsed);clipLimit=null;clipElapsed=null}
function releaseClipStream(){if(clipStream){clipStream.getTracks().forEach(function(track){track.stop()});clipStream=null}if(clipPreview){clipPreview.pause();clipPreview.srcObject=null}}
function clipClock(seconds){return Math.floor(seconds/60)+':'+String(seconds%60).padStart(2,'0')}
function updateClipElapsed(kind){if(!clipStatus||!clipStartedAt)return;var elapsed=Math.min(300,Math.floor((Date.now()-clipStartedAt)/1000));clipStatus.textContent='Recording '+kind+' \u00b7 '+clipClock(elapsed)+' / 5:00'}
function supportedClipMime(kind){var choices=kind==='video'?['video/webm;codecs=vp9,opus','video/webm;codecs=vp8,opus','video/mp4']:['audio/webm;codecs=opus','audio/webm','audio/mp4','audio/ogg;codecs=opus'];for(var index=0;index<choices.length;index++)if(!MediaRecorder.isTypeSupported||MediaRecorder.isTypeSupported(choices[index]))return choices[index];return ''}
function clipExtension(type){if(type.indexOf('mp4')!==-1)return 'mp4';if(type.indexOf('ogg')!==-1)return 'ogg';return 'webm'}
function closeClipDialog(){if(clipDialog&&clipDialog.open)clipDialog.close()}
function cancelClip(){clipGeneration++;clipCancelled=true;clearClipTimers();if(clipRecorder&&clipRecorder.state!=='inactive'){clipRecorder.stop();return}releaseClipStream();closeClipDialog()}
function clipError(message){var owner=clipOwner||active;if(owner)owner.showError(message);else announce(message)}
function startClip(kind,trigger,owner){
if(!clipDialog)return;clipOwner=owner;
if(!window.MediaRecorder||!navigator.mediaDevices||!navigator.mediaDevices.getUserMedia){clipError('This browser cannot record clips. Attach an audio or video file instead.');return}
var generation=++clipGeneration;clipTrigger=trigger;clipCancelled=false;clipChunks=[];clipRecorder=null;
if(clipTitle)clipTitle.textContent=kind==='video'?'Record a video clip':'Record an audio clip';
if(clipStatus)clipStatus.textContent=kind==='video'?'Requesting camera and microphone access\u2026':'Requesting microphone access\u2026';
if(clipPreview){clipPreview.hidden=kind!=='video';clipPreview.srcObject=null}
if(clipStop)clipStop.disabled=true;
clipDialog.showModal();
navigator.mediaDevices.getUserMedia(kind==='video'?{audio:true,video:true}:{audio:true}).then(function(stream){
if(generation!==clipGeneration){stream.getTracks().forEach(function(track){track.stop()});return}
clipStream=stream;if(kind==='video'&&clipPreview){clipPreview.srcObject=stream;clipPreview.play().catch(function(){})}
var mime=supportedClipMime(kind);var options=mime?{mimeType:mime}:undefined;
try{clipRecorder=new MediaRecorder(stream,options)}catch(error){releaseClipStream();closeClipDialog();clipError('Recording could not start in this browser. Attach an audio or video file instead.');return}
clipRecorder.addEventListener('dataavailable',function(event){if(event.data&&event.data.size)clipChunks.push(event.data)});
clipRecorder.addEventListener('error',function(){clipCancelled=true;clipError('The clip recording failed. No attachment was added.')});
clipRecorder.addEventListener('stop',function(){clearClipTimers();releaseClipStream();var recorderType=clipRecorder&&clipRecorder.mimeType?clipRecorder.mimeType:mime;clipRecorder=null;closeClipDialog();if(clipCancelled||!clipChunks.length)return;var blob=new Blob(clipChunks,{type:recorderType});var stamp=new Date().toISOString().replace(/[:.]/g,'-');var file=new File([blob],kind+'-clip-'+stamp+'.'+clipExtension(recorderType),{type:recorderType,lastModified:Date.now()});if(clipOwner&&clipOwner.stageFiles([file]))announce((kind==='video'?'Video':'Audio')+' clip staged. Add a message or send when ready.');else clipError('The recorded clip could not be staged. Attach an audio or video file instead.')});
clipRecorder.start(1000);clipStartedAt=Date.now();if(clipStop)clipStop.disabled=false;updateClipElapsed(kind);
clipElapsed=window.setInterval(function(){updateClipElapsed(kind)},1000);
clipLimit=window.setTimeout(function(){if(clipRecorder&&clipRecorder.state!=='inactive'){announce('Five-minute clip limit reached.');clipRecorder.stop()}},300000);
}).catch(function(error){if(generation!==clipGeneration)return;releaseClipStream();closeClipDialog();var denied=error&&(error.name==='NotAllowedError'||error.name==='SecurityError');clipError(denied?'Microphone or camera permission was denied. Allow access or attach a file instead.':'The microphone or camera is unavailable. Attach an audio or video file instead.')});
}
if(clipStop)clipStop.addEventListener('click',function(){if(clipRecorder&&clipRecorder.state!=='inactive')clipRecorder.stop()});
if(clipCancel)clipCancel.addEventListener('click',cancelClip);
if(clipDialog){clipDialog.addEventListener('cancel',function(event){event.preventDefault();cancelClip()});clipDialog.addEventListener('click',function(event){if(event.target===clipDialog)cancelClip()});clipDialog.addEventListener('close',function(){if(clipTrigger&&doc.contains(clipTrigger))clipTrigger.focus();clipTrigger=null})}
function chordLabel(chord){var keys=chord.split('+');if(apple)return '\u2318'+keys.map(function(key){return key==='Shift'?'\u21e7':key==='Alt'?'\u2325':key}).join('');return 'Ctrl+'+chord}
function adopt(root){
labelSchedules(root);
Array.prototype.forEach.call(root.querySelectorAll('[data-tip-key]'),function(control){control.setAttribute('data-tip',control.getAttribute('data-tip')+'  '+chordLabel(control.getAttribute('data-tip-key')))});
Array.prototype.forEach.call(root.querySelectorAll('form[data-composer]'),function(form){var composer=createComposer(form);composer.showError=function(message){var box=form.querySelector('.form-error');if(box){box.textContent=message;box.hidden=false;form.classList.add('is-error');box.focus()}};composers.push(composer)});
Array.prototype.forEach.call(root.querySelectorAll('[data-record-clip]'),function(button){button.addEventListener('click',function(){startClip(button.getAttribute('data-record-clip'),button,composerFor(button)||active)})});
Array.prototype.forEach.call(root.querySelectorAll('details.composer-menu,details.schedule-menu'),function(menu){menu.addEventListener('toggle',function(){var toggle=menu.querySelector('summary');if(toggle)toggle.setAttribute('aria-expanded',menu.open?'true':'false');if(menu.open){var first=menu.querySelector('[role=menuitem]');if(first)window.setTimeout(function(){first.focus()},0)}});menu.addEventListener('keydown',function(event){if(event.key!=='ArrowDown'&&event.key!=='ArrowUp')return;var items=Array.prototype.slice.call(menu.querySelectorAll('[role=menuitem]')).filter(function(item){return item.offsetParent!==null});if(!items.length)return;event.preventDefault();var index=items.indexOf(doc.activeElement);index=event.key==='ArrowDown'?(index+1)%items.length:(index-1+items.length)%items.length;items[index].focus()})});
}
adopt(doc);
active=composers.filter(function(composer){return composer.thread})[0]||composers[0];
doc.addEventListener('selectionchange',function(){if(active)active.updatePressed()});
doc.addEventListener('sameoldchat:composer-emoji',function(event){var detail=event.detail||{};var owner=(detail.trigger&&composerFor(detail.trigger))||active||composers[0];if(!owner||!detail.name)return;active=owner;owner.insertEmoji(detail.name,detail.glyph||'',detail.image||'')});
doc.addEventListener('sameoldchat:thread-pane',function(event){for(var index=composers.length-1;index>=0;index--){if(!doc.contains(composers[index].form)){if(composers[index].flushDraft)composers[index].flushDraft();composers.splice(index,1)}}var pane=event.detail&&event.detail.pane;if(pane&&doc.contains(pane))adopt(pane);if(!active||!doc.contains(active.form))active=composers.filter(function(composer){return composer.thread})[0]||composers[0]||null});
function openMenus(){return Array.prototype.slice.call(doc.querySelectorAll('details.composer-menu[open],details.schedule-menu[open]'))}
doc.addEventListener('keydown',function(event){
if(event.key==='Escape'){var menus=openMenus();if(menus.length){event.preventDefault();event.stopPropagation();menus.forEach(function(menu){menu.open=false});var summary=menus[0].querySelector('summary');if(summary)summary.focus();return}}
if(primary(event)&&!event.shiftKey&&!event.altKey&&typeof event.key==='string'&&event.key.toLowerCase()==='u'){var owner=composerFor(event.target)||active||composers[0];if(owner&&owner.upload()){event.preventDefault();event.stopPropagation()}}
},true);
doc.addEventListener('click',function(event){openMenus().forEach(function(menu){if(!menu.contains(event.target))menu.open=false})});
syncPreferenceControls();
window.sameoldchatComposer={focus:function(){var owner=active||composers[0];if(owner)owner.focus()},composers:composers,preferences:function(){return{enter:prefs.enter,markup:prefs.markup,formatting:prefs.formatting}},setPreference:setPref};
var focused=doc.activeElement;
var arrival=window.location.hash&&doc.querySelector('.message.is-arrival');
if(!arrival&&!doc.querySelector('dialog[open],[aria-modal="true"]')&&(!focused||focused===doc.body||focused.classList.contains('composer-input'))&&!doc.querySelector('.composer .form-error:not([hidden])')){var eager=composers.filter(function(composer){return !composer.form.hasAttribute('data-composer-quiet')});var initial=eager.filter(function(composer){return composer.thread})[0]||eager[0];if(initial)initial.focus()}
})();</script>`
