package web

// messageScript is the behaviour of the message surface: the toolbar's menus
// and their keyboard, the right-click menu, in-place editing, the Delete and
// Forward dialogs, the shared emoji picker, the image viewer, opening a thread
// in place, and the one-key shortcuts on a focused message. Every action is
// still a server form or link; the script opens them in place and falls back
// to the link when it cannot.
//
// Two events tie it to the composer script. A composer's emoji button opens
// this picker and hands the choice back as sameoldchat:composer-emoji, with
// the button as its trigger, so the emoji lands in the composer that asked
// (the conversation's or the thread's). Opening or closing the thread pane in
// place fires sameoldchat:thread-pane, and the composer script initialises the
// reply composer the fetched pane carries.
//
// It listens in the capture phase on window, so a key it handles — Escape in
// a menu, say — is not also read by the page script's conversation-wide keys.
//
// A reaction's form is looked up by id when the emoji is chosen, not held from
// when the picker opened: a live timeline update can replace the message while
// the picker is open, and a detached form submits nothing.
//
// The script carries no comments: html/template strips JavaScript comments
// from the page it renders, so the served bytes would differ from these and
// miss their Content-Security-Policy hash.
const messageScript = `<script>(function(){
'use strict';
var doc=document;
var narrow=window.matchMedia?window.matchMedia('(max-width:800px)'):null;
function visible(node){return !!(node&&node.getClientRects().length)}
function messageOf(node){return node&&node.closest?node.closest('.message'):null}
function messagesIn(region){return region?Array.prototype.slice.call(region.querySelectorAll('.message')).filter(visible):[]}
function focusMessage(message){if(!message)return false;try{message.focus({preventScroll:true})}catch(error){message.focus()}message.scrollIntoView({block:'nearest'});return true}
function submit(form,submitter){if(!form)return;if(typeof form.requestSubmit==='function'){if(submitter)form.requestSubmit(submitter);else form.requestSubmit()}else form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}))}
function ownPath(value){try{var parsed=new URL(value,window.location.origin);return parsed.origin===window.location.origin}catch(error){return false}}
function editing(node){return !!(node&&(node.tagName==='INPUT'||node.tagName==='TEXTAREA'||node.tagName==='SELECT'||node.isContentEditable))}

var toastNode=doc.getElementById('message-toast');var toastTimer=null;
function toast(text){if(!toastNode||!text)return;toastNode.textContent=text;toastNode.hidden=false;if(toastTimer)window.clearTimeout(toastTimer);toastTimer=window.setTimeout(function(){toastNode.hidden=true},4500)}
doc.addEventListener('sameoldchat:notice',function(event){toast(String(event.detail||''))});

try{var zone=Intl.DateTimeFormat().resolvedOptions().timeZone;if(zone&&/^[A-Za-z0-9_+\-\/]{1,64}$/.test(zone)&&(';'+doc.cookie).indexOf(' sameoldchat_tz='+zone)<0&&(';'+doc.cookie).indexOf(';sameoldchat_tz='+zone)<0){doc.cookie='sameoldchat_tz='+zone+';path=/;max-age=31536000;samesite=lax'}}catch(error){}

var menuPoint=null;var menuByKeyboard=false;
function openMenus(){return Array.prototype.slice.call(doc.querySelectorAll('details[data-message-menu][open],details.file-more[open]'))}
function closeMenus(except){openMenus().forEach(function(details){if(details!==except&&!details.contains(except)){details.open=false;var panel=details.querySelector('.message-menu');if(panel){panel.classList.remove('is-fixed');panel.style.left='';panel.style.top=''}}})}
function itemsOf(menu){return Array.prototype.slice.call(menu.querySelectorAll('[role=menuitem]')).filter(function(item){return item.closest('[role=menu]')===menu&&visible(item)})}
function place(panel,anchor,point){
if(narrow&&narrow.matches){panel.classList.remove('is-fixed');panel.style.left='';panel.style.top='';return}
panel.classList.add('is-fixed');panel.style.left='0px';panel.style.top='0px';
var width=panel.offsetWidth,height=panel.offsetHeight,margin=8;
var left,top;
if(point){left=point.x;top=point.y}else{var rect=anchor.getBoundingClientRect();left=rect.right-width;top=rect.bottom+4;if(top+height>window.innerHeight-margin)top=rect.top-height-4}
if(left+width>window.innerWidth-margin)left=window.innerWidth-width-margin;
if(top+height>window.innerHeight-margin)top=window.innerHeight-height-margin;
panel.style.left=Math.max(margin,left)+'px';panel.style.top=Math.max(margin,top)+'px';
}
function placeSubmenu(details){
var panel=details.querySelector('.message-submenu');if(!panel)return;
if(narrow&&narrow.matches){panel.classList.remove('is-fixed');panel.style.left='';panel.style.top='';return}
var rect=details.getBoundingClientRect();panel.classList.add('is-fixed');panel.style.left='0px';panel.style.top='0px';
var width=panel.offsetWidth,height=panel.offsetHeight;
var left=rect.left-width+4;if(left<8)left=Math.min(rect.right-4,window.innerWidth-width-8);
var top=rect.top-6;if(top+height>window.innerHeight-8)top=window.innerHeight-height-8;
panel.style.left=Math.max(8,left)+'px';panel.style.top=Math.max(8,top)+'px';
}
doc.addEventListener('toggle',function(event){
var details=event.target;
if(!details||details.tagName!=='DETAILS')return;
if(details.matches('[data-message-menu],.file-more')){
if(!details.open)return;
closeMenus(details);closePicker(false);
var panel=details.querySelector('.message-menu,.file-menu');
if(panel&&details.matches('[data-message-menu]'))place(panel,details.querySelector('summary'),menuPoint);
menuPoint=null;
if(menuByKeyboard&&panel){var first=itemsOf(panel)[0];if(first)first.focus()}
menuByKeyboard=false;
return;
}
if(details.matches('.menu-submenu')){
if(details.open){placeSubmenu(details);var sub=details.querySelector('.message-submenu');var first=sub?itemsOf(sub)[0]:null;if(first&&details.hasAttribute('data-keyboard-open'))first.focus();details.removeAttribute('data-keyboard-open')}
}
},true);
doc.addEventListener('pointerdown',function(event){
var target=event.target;
openMenus().forEach(function(details){if(!details.contains(target))details.open=false});
if(picker&&!picker.hidden&&!picker.contains(target)&&!(pickerTrigger&&pickerTrigger.contains(target)))closePicker(false);
},true);
window.addEventListener('resize',function(){closeMenus();closePicker(false)});
doc.addEventListener('contextmenu',function(event){
var message=messageOf(event.target);
if(!message||event.shiftKey||message.classList.contains('system-message'))return;
if(event.target.closest('a[href],img,input,textarea,select,.message-editor,.message-actions'))return;
if(window.getSelection&&String(window.getSelection()))return;
var details=message.querySelector('details[data-message-menu]');
if(!details)return;
event.preventDefault();
closeMenus();menuPoint={x:event.clientX,y:event.clientY};
if(details.open){place(details.querySelector('.message-menu'),null,menuPoint);menuPoint=null}else details.open=true;
});
function openMenuFromKeyboard(message){var details=message&&message.querySelector('details[data-message-menu]');if(!details)return false;menuByKeyboard=true;if(details.open){var first=itemsOf(details.querySelector('.message-menu'))[0];if(first)first.focus();menuByKeyboard=false}else details.open=true;return true}
function menuKey(event,menu){
var items=itemsOf(menu);var index=items.indexOf(doc.activeElement);var key=event.key;
if(key==='ArrowDown'||key==='ArrowUp'||key==='Home'||key==='End'){
event.preventDefault();if(!items.length)return true;
var next=key==='Home'?0:key==='End'?items.length-1:key==='ArrowDown'?(index+1)%items.length:(index-1+items.length)%items.length;
items[next].focus();return true}
if(key==='ArrowRight'&&doc.activeElement&&doc.activeElement.tagName==='SUMMARY'&&doc.activeElement.parentNode.classList.contains('menu-submenu')){event.preventDefault();var submenu=doc.activeElement.parentNode;submenu.setAttribute('data-keyboard-open','');if(submenu.open){submenu.removeAttribute('data-keyboard-open');var subFirst=itemsOf(submenu.querySelector('.message-submenu'))[0];if(subFirst)subFirst.focus()}else submenu.open=true;return true}
if((key==='ArrowLeft'||key==='Escape')&&menu.classList.contains('message-submenu')){event.preventDefault();var owner=menu.closest('.menu-submenu');owner.open=false;owner.querySelector('summary').focus();return true}
if(key==='Escape'){event.preventDefault();var details=menu.closest('details');if(details){details.open=false;var summary=details.querySelector('summary');if(summary)summary.focus()}return true}
if(key==='Tab'){closeMenus();return false}
if(key===' '&&doc.activeElement&&doc.activeElement.tagName==='A'){event.preventDefault();doc.activeElement.click();return true}
if(!event.ctrlKey&&!event.metaKey&&!event.altKey){var wanted=key==='Delete'||key==='Backspace'?'delete':key.toLowerCase();var match=items.find(function(item){return item.getAttribute('data-menu-key')===wanted});if(match){event.preventDefault();match.click();return true}}
return false;
}

function editorOf(message){return message?message.querySelector('form[data-message-editor]'):null}
function fit(area){area.style.height='auto';area.style.height=Math.min(area.scrollHeight+2,Math.floor(window.innerHeight*0.4))+'px'}
function openEditor(message){
var form=editorOf(message);if(!form)return false;
closeMenus();closePicker(false);
var content=message.querySelector('.message-content');if(content)content.hidden=true;
form.hidden=false;message.classList.add('is-editing');
var area=form.querySelector('textarea');area.focus();var end=area.value.length;area.setSelectionRange(end,end);fit(area);
return true;
}
function closeEditor(message,restore){
var form=editorOf(message);if(!form)return;
var area=form.querySelector('textarea');area.value=area.defaultValue;area.style.height='';
form.hidden=true;message.classList.remove('is-editing');
var content=message.querySelector('.message-content');if(content)content.hidden=false;
if(restore!==false)focusMessage(message);
}
function findMessage(target){
if(!target)return null;
if(target.nodeType===1)return target.closest('.message');
var wanted=String(target);var all=doc.querySelectorAll('.message');
for(var index=0;index<all.length;index++){if(all[index].getAttribute('data-message-id')===wanted||all[index].getAttribute('data-ts')===wanted)return all[index]}
return null;
}
window.sameoldchatEditMessage=function(target){return openEditor(findMessage(target))};
window.sameoldchatEditLastMessage=function(region){var list=messagesIn(region||doc.getElementById('timeline')).filter(function(message){return !!editorOf(message)});return list.length?openEditor(list[list.length-1]):false};

var deleteDialog=doc.getElementById('delete-message-dialog');
var forwardDialog=doc.getElementById('forward-message-dialog');
var dialogReturn=null;
function preview(dialog,message){
var slot=dialog.querySelector('[data-dialog-preview]');if(!slot)return;slot.textContent='';
var head=doc.createElement('p');head.className='dialog-preview-author';
var author=message.querySelector('.message-head .author');head.textContent=author?author.textContent:'';
var time=message.querySelector('.message-head time');if(time){var stamp=doc.createElement('span');stamp.className='time';stamp.textContent=time.textContent;head.appendChild(stamp)}
slot.appendChild(head);
var body=message.querySelector('.message-text')||message.querySelector('.message-content');
if(body){var copy=body.cloneNode(true);copy.removeAttribute('hidden');Array.prototype.forEach.call(copy.querySelectorAll('.file-actions,form,details,.edited-label'),function(node){node.remove()});slot.appendChild(copy)}
}
function showDialog(dialog,message){if(!dialog||typeof dialog.showModal!=='function')return false;closeMenus();closePicker(false);dialogReturn=message;if(dialog.open)dialog.close();dialog.showModal();return true}
function openDelete(message,link){
link=link||(message&&message.querySelector('[data-delete-message]'));if(!deleteDialog||!link)return false;
var form=deleteDialog.querySelector('form');var url=link.getAttribute('data-delete-message');if(!url||!ownPath(url))return false;
form.setAttribute('action',url);form.setAttribute('hx-post',url);
preview(deleteDialog,message);
var note=deleteDialog.querySelector('[data-delete-files-note]');if(note)note.hidden=link.getAttribute('data-delete-files')!=='true';
if(!showDialog(deleteDialog,message))return false;
var confirm=form.querySelector('button[type=submit]');if(confirm)confirm.focus();
return true;
}
function openForward(message,link){
link=link||(message&&message.querySelector('[data-forward-message]'));if(!forwardDialog||!link)return false;
var form=forwardDialog.querySelector('form');var url=link.getAttribute('data-forward-message');if(!url||!ownPath(url))return false;
form.reset();form.setAttribute('action',url);form.setAttribute('hx-post',url);
filterDestinations('');
var copy=forwardDialog.querySelector('[data-forward-copy-link]');var source=message.querySelector('[data-copy-link]');
if(copy){if(source)copy.setAttribute('data-copy-link',source.getAttribute('data-copy-link'));else copy.removeAttribute('data-copy-link');copy.hidden=!source}
preview(forwardDialog,message);
if(!showDialog(forwardDialog,message))return false;
var query=forwardDialog.querySelector('[data-forward-filter]');if(query)query.focus();
return true;
}
function filterDestinations(term){
if(!forwardDialog)return;var select=forwardDialog.querySelector('select[name=destination]');if(!select)return;
term=term.trim().toLowerCase().replace(/^[#@]/,'');var first=null;
Array.prototype.forEach.call(select.querySelectorAll('optgroup'),function(group){var shown=0;Array.prototype.forEach.call(group.querySelectorAll('option'),function(option){var match=!term||option.textContent.toLowerCase().replace(/^#/,'').indexOf(term)!==-1;option.hidden=!match;option.disabled=!match;if(match){shown++;if(!first)first=option}});group.hidden=shown===0});
if(term&&first)select.value=first.value;
}
[deleteDialog,forwardDialog].forEach(function(dialog){
if(!dialog)return;
dialog.addEventListener('close',function(){var message=dialogReturn;dialogReturn=null;if(message&&doc.contains(message))focusMessage(message)});
dialog.addEventListener('submit',function(event){
var form=event.target;
if(form.hasAttribute('data-forward-form')){var select=form.querySelector('select[name=destination]');if(select&&!select.value){event.preventDefault();event.stopPropagation();select.focus();return}}
var message=dialogReturn;
if(dialog===deleteDialog&&message){var items=messagesIn(message.closest('[data-fragment]'));var at=items.indexOf(message);dialogReturn=items[at+1]||items[at-1]||null}
dialog.close();
});
});
if(forwardDialog){var filter=forwardDialog.querySelector('[data-forward-filter]');if(filter){filter.addEventListener('input',function(){filterDestinations(filter.value)});filter.addEventListener('keydown',function(event){if(event.key==='ArrowDown'){event.preventDefault();var select=forwardDialog.querySelector('select[name=destination]');if(select){if(!select.value){var option=select.querySelector('option:not([hidden])');if(option)select.value=option.value}select.focus()}}else if(event.key==='Enter'){event.preventDefault();var form=forwardDialog.querySelector('form');submit(form)}})}}
Array.prototype.forEach.call(doc.querySelectorAll('dialog.message-dialog[open]'),function(dialog){if(typeof dialog.showModal==='function'){dialog.close();dialog.showModal();var first=dialog.querySelector('[data-forward-filter],button[type=submit]');if(first)first.focus()}});

var picker=doc.getElementById('emoji-picker');
var pickerQuery=doc.getElementById('emoji-picker-query');
var pickerGrid=doc.getElementById('emoji-picker-grid');
var pickerStatus=doc.getElementById('emoji-picker-status');
var pickerTitle=doc.getElementById('emoji-section-title');
var toneButton=doc.getElementById('emoji-tone-button');
var toneOptions=doc.getElementById('emoji-tone-options');
var pickerPreview=doc.getElementById('emoji-preview');
var pickerTrigger=null,pickerSelect=null,pickerCategory='Recent',pickerRequest=null,pickerTimer=null,pickerActive=-1;
var toneGlyphs=['','','\u{1F3FB}','\u{1F3FC}','\u{1F3FD}','\u{1F3FE}','\u{1F3FF}'];
function storedList(key){try{var value=JSON.parse(localStorage.getItem(key)||'[]');return Array.isArray(value)?value:[]}catch(error){return[]}}
function tone(){try{return localStorage.getItem('sameoldchat-emoji-tone')||''}catch(error){return''}}
function remember(name){
var base=name.split('::')[0];
try{var recent=storedList('sameoldchat-recent-emoji').filter(function(value){return value!==base});recent.unshift(base);recent=recent.slice(0,24);localStorage.setItem('sameoldchat-recent-emoji',JSON.stringify(recent));doc.cookie='sameoldchat_recent_emoji='+recent.slice(0,3).map(encodeURIComponent).join('.')+';path=/;max-age=31536000;samesite=lax'}catch(error){}
}
function pickerOptions(){return pickerGrid?Array.prototype.slice.call(pickerGrid.querySelectorAll('[data-picker-emoji]')):[]}
function showPreview(option){
if(!pickerPreview)return;var glyph=pickerPreview.querySelector('.emoji-preview-glyph');var label=pickerPreview.querySelector('.emoji-preview-name');
glyph.textContent='';label.textContent='';if(!option)return;
var visual=option.firstChild;if(visual)glyph.appendChild(visual.cloneNode(true));label.textContent=option.getAttribute('aria-label')||'';
}
function activate(index){
var options=pickerOptions();if(!options.length){pickerActive=-1;if(pickerQuery)pickerQuery.removeAttribute('aria-activedescendant');return}
index=Math.max(0,Math.min(options.length-1,index));
options.forEach(function(option,position){option.setAttribute('aria-selected',position===index?'true':'false')});
pickerActive=index;var option=options[index];if(pickerQuery)pickerQuery.setAttribute('aria-activedescendant',option.id);
option.scrollIntoView({block:'nearest'});showPreview(option);
}
function columns(){var options=pickerOptions();if(options.length<2)return 1;var top=options[0].offsetTop;for(var index=1;index<options.length;index++){if(options[index].offsetTop!==top)return index}return options.length}
var categoryLabels={};
Array.prototype.forEach.call(doc.querySelectorAll('[data-emoji-category]'),function(tab){categoryLabels[tab.getAttribute('data-emoji-category')]=tab.getAttribute('aria-label')});
function selectCategory(name){pickerCategory=name;Array.prototype.forEach.call(doc.querySelectorAll('[data-emoji-category]'),function(tab){tab.setAttribute('aria-selected',tab.getAttribute('data-emoji-category')===name?'true':'false')})}
function render(values){
pickerGrid.textContent='';var chosenTone=tone();
values.forEach(function(value,index){
var button=doc.createElement('button');button.type='button';button.id='emoji-choice-'+index;button.tabIndex=-1;
button.setAttribute('role','option');button.setAttribute('aria-selected','false');
button.setAttribute('data-picker-emoji',value.name||'');if(value.skin_tones)button.setAttribute('data-skin-tones','true');
button.setAttribute('aria-label',':'+(value.label||value.name||'')+':');button.title=':'+(value.label||value.name||'')+':';
var visual;
if(value.image_url){visual=doc.createElement('img');visual.className='custom-emoji';visual.src=value.image_url;visual.alt='';visual.loading='lazy'}
else{visual=doc.createElement('span');visual.setAttribute('aria-hidden','true');visual.textContent=(value.display||'')+(value.skin_tones&&chosenTone?toneGlyphs[parseInt(chosenTone,10)]||'':'')}
button.appendChild(visual);pickerGrid.appendChild(button);
});
activate(0);
}
function load(){
if(!picker)return;
var query=pickerQuery?pickerQuery.value.trim():'';
var category=query?'':pickerCategory;
var parameters=new URLSearchParams({q:query});
if(category)parameters.set('category',category);
var recent=storedList('sameoldchat-recent-emoji');if(recent.length)parameters.set('recent',recent.slice(0,24).join(','));
parameters.set('limit',query?'200':'2000');
if(pickerRequest)pickerRequest.abort();pickerRequest=window.AbortController?new AbortController():null;
if(pickerTitle)pickerTitle.textContent=query?'Search results':(categoryLabels[category]||category);
if(pickerStatus)pickerStatus.textContent='Loading emoji…';
return fetch('/app/emoji/options?'+parameters.toString(),{credentials:'same-origin',signal:pickerRequest?pickerRequest.signal:undefined}).then(function(response){if(!response.ok)throw new Error('unavailable');return response.json()}).then(function(payload){
var values=payload&&Array.isArray(payload.options)?payload.options:[];
if(!values.length&&category==='Recent'&&!query){selectCategory('Smileys & Emotion');return load()}
render(values);
if(pickerStatus)pickerStatus.textContent=values.length?'':(query?'No emoji match “'+query+'”.':'No emoji here yet.');
}).catch(function(error){if(error&&error.name==='AbortError')return;pickerGrid.textContent='';if(pickerStatus)pickerStatus.textContent='Emoji could not be loaded. Try again.'});
}
function positionPicker(trigger){
if(narrow&&narrow.matches){picker.style.left='';picker.style.top='';return}
var rect=trigger.getBoundingClientRect();var width=picker.offsetWidth,height=picker.offsetHeight;
var left=Math.min(Math.max(8,rect.right-width),window.innerWidth-width-8);
var top=rect.bottom+6;if(top+height>window.innerHeight-8)top=Math.max(8,rect.top-height-6);
picker.style.left=left+'px';picker.style.top=top+'px';
}
function openPicker(trigger,onSelect){
if(!picker)return false;
closeMenus();
pickerTrigger=trigger;pickerSelect=onSelect;
picker.hidden=false;positionPicker(trigger);
if(pickerQuery){pickerQuery.value='';pickerQuery.focus()}
if(toneOptions)toneOptions.hidden=true;
selectCategory('Recent');load();
return true;
}
function closePicker(restore){
if(!picker||picker.hidden)return;
picker.hidden=true;var trigger=pickerTrigger;pickerTrigger=null;pickerSelect=null;
if(restore!==false&&trigger&&doc.contains(trigger)){var owner=messageOf(trigger);if(!visible(trigger)&&owner)focusMessage(owner);if(visible(trigger))trigger.focus()}
}
function choose(option){
if(!option)return;var name=option.getAttribute('data-picker-emoji');if(!name)return;
var chosenTone=option.hasAttribute('data-skin-tones')?tone():'';
var full=name+(chosenTone?'::skin-tone-'+chosenTone:'');
remember(name);var callback=pickerSelect;closePicker(true);if(callback)callback(full,option);
}
window.sameoldchatEmojiPicker={open:openPicker,close:closePicker};
function reactWith(formId,name){
var form=doc.getElementById(formId);
if(!form){toast('That message is no longer here, so the reaction was not added.');return}
var input=doc.createElement('input');input.type='hidden';input.name='name';input.value=name;form.appendChild(input);submit(form);input.remove();
}
if(picker){
if(pickerQuery){
pickerQuery.addEventListener('input',function(){if(pickerTimer)window.clearTimeout(pickerTimer);pickerTimer=window.setTimeout(load,90)});
pickerQuery.addEventListener('keydown',function(event){
var options=pickerOptions();var across=columns();
if(event.key==='ArrowDown'){event.preventDefault();activate(pickerActive+across)}
else if(event.key==='ArrowUp'){event.preventDefault();activate(pickerActive-across)}
else if(event.key==='ArrowRight'&&pickerQuery.selectionStart===pickerQuery.value.length){event.preventDefault();activate(pickerActive+1)}
else if(event.key==='ArrowLeft'&&pickerQuery.selectionStart===0&&pickerQuery.selectionEnd===0){event.preventDefault();activate(pickerActive-1)}
else if(event.key==='Enter'){event.preventDefault();choose(options[pickerActive])}
});
}
pickerGrid.addEventListener('click',function(event){var option=event.target.closest('[data-picker-emoji]');if(option)choose(option)});
pickerGrid.addEventListener('mouseover',function(event){var option=event.target.closest('[data-picker-emoji]');if(option)showPreview(option)});
Array.prototype.forEach.call(picker.querySelectorAll('[data-emoji-category]'),function(tab){tab.addEventListener('click',function(){if(pickerQuery)pickerQuery.value='';selectCategory(tab.getAttribute('data-emoji-category'));load();if(pickerQuery)pickerQuery.focus()})});
if(toneButton&&toneOptions){
var syncTone=function(){var current=tone();Array.prototype.forEach.call(toneOptions.querySelectorAll('[data-tone]'),function(option){option.setAttribute('aria-checked',option.getAttribute('data-tone')===current?'true':'false')});var shown=toneOptions.querySelector('[data-tone="'+current+'"]');toneButton.firstElementChild.textContent=shown?shown.textContent:'✋'};
syncTone();
toneButton.addEventListener('click',function(){toneOptions.hidden=!toneOptions.hidden;toneButton.setAttribute('aria-expanded',toneOptions.hidden?'false':'true');if(!toneOptions.hidden){var checked=toneOptions.querySelector('[aria-checked=true]')||toneOptions.querySelector('[data-tone]');if(checked)checked.focus()}});
toneOptions.addEventListener('click',function(event){var option=event.target.closest('[data-tone]');if(!option)return;try{localStorage.setItem('sameoldchat-emoji-tone',option.getAttribute('data-tone'))}catch(error){}if(window.sameoldchatKeepPreference)window.sameoldchatKeepPreference('emoji-tone',option.getAttribute('data-tone'));syncTone();toneOptions.hidden=true;toneButton.setAttribute('aria-expanded','false');load();if(pickerQuery)pickerQuery.focus()});
}
}

var lightbox=doc.getElementById('image-lightbox');var lightboxReturn=null;
function openLightbox(link){
if(!lightbox||typeof lightbox.showModal!=='function')return false;
var href=link.getAttribute('href');if(!ownPath(href))return false;
lightbox.querySelector('[data-lightbox-image]').src=href;
var thumb=link.querySelector('img');lightbox.querySelector('[data-lightbox-image]').alt=thumb?thumb.alt:'';
lightbox.querySelector('[data-lightbox-title]').textContent=link.getAttribute('data-file-title')||'';
lightbox.querySelector('[data-lightbox-meta]').textContent=link.getAttribute('data-file-meta')||'';
lightbox.querySelector('[data-lightbox-download]').href=href;lightbox.querySelector('[data-lightbox-open]').href=href;
lightboxReturn=link;lightbox.showModal();lightbox.querySelector('[data-lightbox-close]').focus();return true;
}
if(lightbox){
lightbox.querySelector('[data-lightbox-close]').addEventListener('click',function(){lightbox.close()});
lightbox.addEventListener('click',function(event){if(event.target===lightbox||event.target.classList.contains('lightbox-stage'))lightbox.close()});
lightbox.addEventListener('close',function(){var back=lightboxReturn;lightboxReturn=null;lightbox.querySelector('[data-lightbox-image]').removeAttribute('src');if(back&&doc.contains(back))back.focus()});
}

function openThread(href){
if(!ownPath(href)){window.location.assign(href);return}
closeMenus();
fetch(href,{credentials:'same-origin'}).then(function(response){if(!response.ok)throw new Error('thread');return response.text()}).then(function(html){
var parsed=new DOMParser().parseFromString(html,'text/html');
var pane=parsed.querySelector('aside.thread');var content=doc.querySelector('main.content');
if(!pane||!content){window.location.assign(href);return}
var adopted=doc.importNode(pane,true);var current=content.querySelector('aside.thread');
if(current)current.replaceWith(adopted);else content.insertBefore(adopted,content.querySelector('.channel-composer-wrap'));
history.pushState({sameoldchatThread:true},'',href);
if(window.sameoldchatLocalTimes)window.sameoldchatLocalTimes(adopted);
doc.dispatchEvent(new CustomEvent('sameoldchat:thread-pane',{detail:{pane:adopted}}));
var heading=adopted.querySelector('#thread-heading');if(heading){heading.tabIndex=-1;heading.focus()}
}).catch(function(){window.location.assign(href)});
}
function closeThread(link){
var pane=doc.querySelector('aside.thread');if(!pane)return false;
var root=pane.querySelector('[data-thread-root]');var ts=root?root.getAttribute('data-ts'):'';
pane.remove();history.pushState({},'',link.getAttribute('href'));
doc.dispatchEvent(new CustomEvent('sameoldchat:thread-pane',{detail:{pane:null}}));
var back=ts?doc.querySelector('#timeline .message[data-ts="'+ts+'"]'):null;
if(!focusMessage(back)){var timeline=doc.getElementById('timeline');if(timeline)timeline.focus()}
return true;
}
window.addEventListener('popstate',function(){window.location.reload()});

doc.addEventListener('click',function(event){
if(event.defaultPrevented||event.button!==0||event.ctrlKey||event.metaKey||event.shiftKey||event.altKey)return;
var target=event.target;
var menuItem=target.closest('details[data-message-menu] [role=menuitem]');
if(menuItem&&menuItem.tagName!=='SUMMARY'){var menuOwner=messageOf(menuItem);window.setTimeout(function(){closeMenus();var now=doc.activeElement;if(menuOwner&&doc.contains(menuOwner)&&(!now||now===doc.body||!visible(now)))focusMessage(menuOwner)},0)}
var copy=target.closest('[data-copy-link]');
if(copy&&copy.getAttribute('data-copy-link')){
event.preventDefault();event.stopImmediatePropagation();closeMenus();
var absolute=new URL(copy.getAttribute('data-copy-link'),window.location.origin).toString();
if(navigator.clipboard&&navigator.clipboard.writeText)navigator.clipboard.writeText(absolute).then(function(){toast('Link copied to clipboard')}).catch(function(){toast('The link could not be copied: '+absolute)});
else toast(absolute);
return;
}
var opener=target.closest('[data-open-emoji-picker]');
if(opener){
event.preventDefault();event.stopImmediatePropagation();
if(picker&&!picker.hidden&&pickerTrigger===opener){closePicker(true);return}
if(opener.getAttribute('data-emoji-target')==='reaction'){var formId=opener.getAttribute('data-reaction-form')||'';if(doc.getElementById(formId))openPicker(opener,function(name){reactWith(formId,name)})}
else openPicker(opener,function(name,option){var visual=option&&option.firstChild;doc.dispatchEvent(new CustomEvent('sameoldchat:composer-emoji',{detail:{name:name,glyph:visual&&visual.tagName==='SPAN'?visual.textContent:'',image:visual&&visual.tagName==='IMG'?visual.getAttribute('src'):'',trigger:opener}}))});
return;
}
var edit=target.closest('[data-edit-message]');
if(edit&&openEditor(messageOf(edit))){event.preventDefault();return}
var cancel=target.closest('[data-edit-cancel]');
if(cancel){event.preventDefault();closeEditor(messageOf(cancel));return}
var del=target.closest('[data-delete-message]');
if(del&&openDelete(messageOf(del),del)){event.preventDefault();return}
var forward=target.closest('[data-forward-message]');
if(forward&&openForward(messageOf(forward),forward)){event.preventDefault();return}
var dismiss=target.closest('[data-dialog-cancel]');
if(dismiss){var dialog=dismiss.closest('dialog');if(dialog&&dialog.open){event.preventDefault();dialog.close()}return}
var image=target.closest('a[data-lightbox]');
if(image&&openLightbox(image)){event.preventDefault();return}
var thread=target.closest('a[data-thread-link]');
if(thread){event.preventDefault();openThread(thread.getAttribute('href'));return}
var closer=target.closest('[data-thread-close]');
if(closer&&closeThread(closer)){event.preventDefault();return}
},true);
doc.addEventListener('input',function(event){if(event.target.matches&&event.target.matches('form[data-message-editor] textarea'))fit(event.target)});

function messageKey(event,message){
var key=event.key,lower=key.length===1?key.toLowerCase():key;
var region=message.closest('[data-fragment]');var inThread=!!message.closest('aside.thread');
if(key==='ArrowUp'||key==='ArrowDown'||key==='Home'||key==='End'){
var items=messagesIn(region);var at=items.indexOf(message);
var next=key==='Home'?0:key==='End'?items.length-1:key==='ArrowUp'?Math.max(0,at-1):Math.min(items.length-1,at+1);
event.preventDefault();focusMessage(items[next]);return true}
if(key==='ContextMenu'||(key==='F10'&&event.shiftKey)){event.preventDefault();return openMenuFromKeyboard(message)}
if(event.shiftKey)return false;
if((lower==='t'||key==='ArrowRight')&&!inThread){var link=message.querySelector('.message-actions a[data-thread-link]');if(link){event.preventDefault();openThread(link.getAttribute('href'));return true}}
if(key==='ArrowLeft'&&inThread){var closer=doc.querySelector('[data-thread-close]');if(closer){event.preventDefault();if(!closeThread(closer))window.location.assign(closer.getAttribute('href'));return true}}
if(lower==='e'&&openEditor(message)){event.preventDefault();return true}
if((key==='Delete'||key==='Backspace')&&openDelete(message)){event.preventDefault();return true}
if(lower==='f'&&openForward(message)){event.preventDefault();return true}
if(lower==='r'){var react=message.querySelector('.message-actions [data-open-emoji-picker]');var formId=react?react.getAttribute('data-reaction-form')||'':'';if(react&&doc.getElementById(formId)){event.preventDefault();openPicker(react,function(name){reactWith(formId,name)});return true}}
if(lower==='u'||lower==='p'){var item=message.querySelector('[data-menu-key="'+lower+'"]');if(item){event.preventDefault();item.click();return true}}
if(lower==='a'){var save=message.querySelector('[data-message-save] button');if(save){event.preventDefault();submit(save.form,save);return true}}
if(lower==='m'){var remind=message.querySelector('[data-reminder-menu]');if(remind&&openMenuFromKeyboard(message)){event.preventDefault();remind.setAttribute('data-keyboard-open','');remind.open=true;return true}}
return false;
}
window.addEventListener('keydown',function(event){
var target=event.target;if(!target||!target.closest)return;
if(picker&&!picker.hidden&&picker.contains(target)&&event.key==='Escape'){event.preventDefault();event.stopPropagation();if(toneOptions&&!toneOptions.hidden){toneOptions.hidden=true;if(pickerQuery)pickerQuery.focus();return}closePicker(true);return}
if(picker&&!picker.hidden&&event.key==='Escape'){event.preventDefault();event.stopPropagation();closePicker(true);return}
var area=target.matches('form[data-message-editor] textarea')?target:null;
if(area){
var message=messageOf(area);
if(event.key==='Escape'){event.preventDefault();event.stopPropagation();closeEditor(message);return}
if(event.key==='Enter'&&!event.shiftKey&&!event.isComposing&&!event.altKey&&!event.ctrlKey&&!event.metaKey){
event.preventDefault();event.stopPropagation();
if(!area.value.trim()){closeEditor(message,false);openDelete(message);return}
if(area.value===area.defaultValue){closeEditor(message);return}
submit(area.form);return}
event.stopPropagation();return;
}
var menu=target.closest('[role=menu]');
if(menu&&menuKey(event,menu)){event.stopPropagation();return}
if(target.matches('details[data-message-menu]>summary')&&(event.key==='Enter'||event.key===' '||event.key==='ArrowDown')){
var details=target.parentNode;if(!details.open){menuByKeyboard=true;if(event.key==='ArrowDown'){event.preventDefault();details.open=true}}else if(event.key==='ArrowDown'){event.preventDefault();var first=itemsOf(details.querySelector('.message-menu'))[0];if(first)first.focus()}
event.stopPropagation();return;
}
if(event.key==='Escape'&&openMenus().length){event.preventDefault();event.stopPropagation();var open=openMenus()[0];open.open=false;var summary=open.querySelector('summary');if(summary&&open.contains(target))summary.focus();return}
if(editing(target)||event.ctrlKey||event.metaKey||event.altKey)return;
if((target.id==='timeline'||target.id==='thread-messages')&&(event.key==='ArrowUp'||event.key==='ArrowDown'||event.key==='Home'||event.key==='End')){
var items=messagesIn(target);if(!items.length)return;event.preventDefault();event.stopPropagation();
focusMessage(event.key==='ArrowUp'||event.key==='End'?items[items.length-1]:items[0]);return}
if(target.classList.contains('message')&&!target.classList.contains('is-editing')){if(messageKey(event,target))event.stopPropagation();return}
if(event.key==='Escape'&&target.closest('aside.thread')&&!doc.querySelector('dialog[open]')){var closer=doc.querySelector('[data-thread-close]');if(closer){event.preventDefault();event.stopPropagation();if(!closeThread(closer))window.location.assign(closer.getAttribute('href'))}}
},true);
Array.prototype.forEach.call(doc.querySelectorAll('.message.is-editing form[data-message-editor] textarea'),function(area){fit(area);area.focus();var end=area.value.length;area.setSelectionRange(end,end)});
})();</script>`
