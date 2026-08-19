package web

// huddleMediaScript connects one browser to a huddle through the in-process
// selective forwarding unit.
//
// Each browser holds ONE connection, to this server. It publishes its
// microphone, its camera and its screen — each on its own lane — once; the SFU
// forwards each participant's media to everyone else. That is one upload per
// participant however many people are in the huddle — not the mesh's one upload
// per other participant — and the media flows through a server the browser can
// always reach rather than a direct peer path that fails behind NAT. The screen
// lane carries a dedicated media stream whose id the browser declares to the SFU
// in its publish offer, so the SFU can forward a screen share on its own stream
// and a subscriber can play it beside the sharer's camera instead of in place.
//
// Negotiation has two halves. The browser publishes with a single initial offer
// POSTed to the SFU, which answers. Thereafter the SFU drives renegotiation:
// when the set of tracks the browser should receive changes, the SFU sends an
// offer as a recipient-scoped huddle.signal event stamped from the SFU, and the
// browser answers over the signal endpoint. Forwarded media arrives grouped by
// participant: a stream whose id is the participant carries their camera and
// microphone onto their tile, and a stream whose id is that participant plus a
// screen suffix opens a separate presenter tile, so a screen share plays beside
// the sharer's camera.
//
// The script carries no comments: html/template elides them inside a script
// element, so the served bytes would stop matching the Content-Security-Policy
// hash computed from this source.
const huddleMediaScript = `<script>(function(){
var start=function(session){
if(!session||session.getAttribute('data-huddle-started')==='true')return;
session.setAttribute('data-huddle-started','true');
var status=document.querySelector('[data-huddle-status]');
var tiles=session.querySelector('[data-huddle-tiles]');
var announce=function(text){if(status)status.textContent=text};
if(!window.RTCPeerConnection||!navigator.mediaDevices||!navigator.mediaDevices.getUserMedia){
announce('This browser cannot carry huddle audio. Everything else in the huddle still works.');
return;
}
var callID=session.getAttribute('data-huddle-call');
var selfID=session.getAttribute('data-huddle-self');
var csrf=session.getAttribute('data-huddle-csrf');
var offerURL=session.getAttribute('data-huddle-sfu');
var signalURL=session.getAttribute('data-huddle-sfu-signal');
var names={};
try{names=JSON.parse(session.getAttribute('data-huddle-names')||'{}')}catch(error){names={}}
var iceServers=[];
try{iceServers=JSON.parse(session.getAttribute('data-huddle-ice')||'[]')}catch(error){iceServers=[]}
var presenceURL=session.getAttribute('data-huddle-presence');
var pc=null;
var local=null;
var cameraTrack=null;
var screenStream=null;
var videoSender=null;
var screenSender=null;
var screenStreamOut=null;
var pending=[];
var haveRemote=false;
var selfMuted=false;
var selfCamera=false;
var selfPresenting=false;
var audioContext=null;
var meters={};
var broadcastPresence=function(){
if(!presenceURL)return;
post(presenceURL,{muted:selfMuted?'true':'false',camera:selfCamera?'true':'false',presenting:selfPresenting?'true':'false'}).catch(function(){});
};
var applyPresence=function(id,muted,camera,presenting){
var tile=tileFor(id);
tile.setAttribute('data-huddle-muted',muted?'true':'false');
tile.setAttribute('data-huddle-camera',camera?'true':'false');
};
var refreshPresenter=function(){
var presenter=tiles.querySelector('[data-huddle-tile][data-huddle-screen]');
if(presenter){tiles.setAttribute('data-huddle-presenter',presenter.getAttribute('data-huddle-tile'))}
else{tiles.removeAttribute('data-huddle-presenter')}
};
var ensureAudioContext=function(){
if(!audioContext&&(window.AudioContext||window.webkitAudioContext)){audioContext=new (window.AudioContext||window.webkitAudioContext)()}
return audioContext;
};
var meterFor=function(id,stream){
var ctx=ensureAudioContext();
if(!ctx||!stream||meters[id]||stream.getAudioTracks().length===0)return;
try{
var source=ctx.createMediaStreamSource(stream);
var analyser=ctx.createAnalyser();
analyser.fftSize=512;
source.connect(analyser);
meters[id]={analyser:analyser,data:new Uint8Array(analyser.frequencyBinCount)};
}catch(error){}
};
var speakingLoop=function(){
var loudest=null;
var loudestLevel=0.05;
Object.keys(meters).forEach(function(id){
if(id===selfID&&selfMuted)return;
var meter=meters[id];
meter.analyser.getByteFrequencyData(meter.data);
var sum=0;
for(var i=0;i<meter.data.length;i++){sum+=meter.data[i]*meter.data[i]}
var level=Math.sqrt(sum/meter.data.length)/255;
if(level>loudestLevel){loudestLevel=level;loudest=id}
});
Array.prototype.forEach.call(tiles.querySelectorAll('[data-huddle-tile]:not([data-huddle-screen])'),function(tile){
tile.setAttribute('data-huddle-speaking',tile.getAttribute('data-huddle-tile')===loudest?'true':'false');
});
if(window.requestAnimationFrame){window.requestAnimationFrame(speakingLoop)}else{window.setTimeout(speakingLoop,120)}
};
var nameOf=function(id){return id===selfID?'You':(names[id]||id)};
var tileFor=function(id){
var found=tiles.querySelector('[data-huddle-tile="'+id+'"]');
if(found)return found;
var item=document.createElement('li');
item.setAttribute('data-huddle-tile',id);
item.setAttribute('data-huddle-state','new');
var media=document.createElement('video');
media.autoplay=true;
media.playsInline=true;
if(id===selfID)media.muted=true;
item.appendChild(media);
var label=document.createElement('span');
label.className='huddle-tile-label';
label.textContent=nameOf(id);
item.appendChild(label);
var badge=document.createElement('span');
badge.className='huddle-tile-badge';
badge.setAttribute('data-huddle-badge','');
badge.setAttribute('aria-hidden','true');
item.appendChild(badge);
tiles.appendChild(item);
return item;
};
var screenKey=function(id){return 'screen:'+id};
var screenTileFor=function(id){
var key=screenKey(id);
var found=tiles.querySelector('[data-huddle-tile="'+key+'"]');
if(found)return found;
var item=document.createElement('li');
item.setAttribute('data-huddle-tile',key);
item.setAttribute('data-huddle-screen','');
item.setAttribute('data-huddle-presenting','true');
var media=document.createElement('video');
media.autoplay=true;
media.playsInline=true;
if(id===selfID)media.muted=true;
item.appendChild(media);
var label=document.createElement('span');
label.className='huddle-tile-label';
label.textContent=id===selfID?'Your screen':nameOf(id)+'’s screen';
item.appendChild(label);
tiles.appendChild(item);
return item;
};
var attachScreen=function(id,stream){
var tile=screenTileFor(id);
var media=tile.querySelector('video');
if(media.srcObject!==stream)media.srcObject=stream;
refreshPresenter();
};
var detachScreen=function(id){
var tile=tiles.querySelector('[data-huddle-tile="'+screenKey(id)+'"]');
if(tile)tile.remove();
refreshPresenter();
};
var dropTile=function(id){
var tile=tiles.querySelector('[data-huddle-tile="'+id+'"]');
if(tile)tile.remove();
detachScreen(id);
report();
};
var remoteCount=function(){
var total=tiles.querySelectorAll('[data-huddle-tile]:not([data-huddle-screen])').length;
if(tiles.querySelector('[data-huddle-tile="'+selfID+'"]'))total=total-1;
return total;
};
var report=function(){
var total=remoteCount();
session.setAttribute('data-huddle-connected',String(total));
if(total>0){announce(total===1?'Connected to 1 other person.':'Connected to '+total+' other people.');return}
announce('Your microphone is on. Waiting for somebody else to join.');
};
var post=function(url,params){
var body=new URLSearchParams();
body.set('_csrf',csrf);
body.set('call_id',callID);
Object.keys(params).forEach(function(key){body.set(key,params[key])});
return fetch(url,{method:'POST',credentials:'same-origin',headers:{'content-type':'application/x-www-form-urlencoded'},body:body.toString()});
};
var toServer=function(kind,extra){extra=extra||{};extra.kind=kind;return post(signalURL,extra).catch(function(){})};
var addCandidate=function(init){
if(!haveRemote){pending.push(init);return}
pc.addIceCandidate(init).catch(function(){});
};
var flush=function(){haveRemote=true;pending.forEach(function(init){pc.addIceCandidate(init).catch(function(){})});pending=[]};
var handleServerOffer=function(sdp){
pc.setRemoteDescription({type:'offer',sdp:sdp}).then(function(){return pc.createAnswer()}).then(function(answer){return pc.setLocalDescription(answer)}).then(function(){return toServer('answer',{sdp:pc.localDescription.sdp})}).catch(function(){});
};
document.addEventListener('sameoldchat:event',function(event){
var detail=event.detail;
if(!detail||detail.type!=='huddle.signal')return;
var decoded=null;
try{decoded=JSON.parse(detail.data)}catch(error){return}
if(!decoded||decoded.call_id!==callID||decoded.from_user_id!=='sfu')return;
var signal=null;
try{signal=JSON.parse(decoded.payload)}catch(error){return}
if(!signal||!pc)return;
if(signal.kind==='offer'){handleServerOffer(signal.sdp);return}
if(signal.kind==='candidate'){var init=null;try{init=JSON.parse(signal.candidate)}catch(error){return}addCandidate(init)}
});
var connect=function(){
pc=new RTCPeerConnection({iceServers:iceServers});
pc.onicecandidate=function(event){if(event.candidate)toServer('candidate',{candidate:JSON.stringify(event.candidate)})};
pc.ontrack=function(event){
var stream=event.streams&&event.streams[0];
if(!stream)return;
var screen=stream.id.slice(-7)==='.screen';
var owner=screen?stream.id.slice(0,-7):stream.id;
if(owner===selfID)return;
if(screen){
attachScreen(owner,stream);
stream.addEventListener('removetrack',function(){if(stream.getTracks().length===0)detachScreen(owner)});
report();
return;
}
var media=tileFor(owner).querySelector('video');
if(media.srcObject!==stream)media.srcObject=stream;
meterFor(owner,stream);
stream.addEventListener('removetrack',function(){if(stream.getTracks().length===0){delete meters[owner];dropTile(owner)}});
report();
};
pc.onconnectionstatechange=function(){
if(pc.connectionState==='failed'||pc.connectionState==='closed'){announce('The huddle connection dropped. Rejoin to reconnect.')}
report();
};
pc.addTrack(local.getAudioTracks()[0],local);
videoSender=pc.addTransceiver('video',{direction:'sendrecv'}).sender;
screenStreamOut=new MediaStream();
screenSender=pc.addTransceiver('video',{direction:'sendonly',streams:[screenStreamOut]}).sender;
pc.createOffer().then(function(offer){return pc.setLocalDescription(offer)}).then(function(){
return post(offerURL,{sdp:pc.localDescription.sdp,screen_stream:screenStreamOut.id});
}).then(function(response){if(!response.ok)throw new Error('offer');return response.json()}).then(function(answer){
return pc.setRemoteDescription({type:'answer',sdp:answer.sdp});
}).then(function(){flush();report()}).catch(function(){
announce('The huddle media connection could not be established. Everything else still works.');
});
};
var reactURL=session.getAttribute('data-huddle-react');
var reactionLayer=session.querySelector('[data-huddle-reactions]');
var glyphMap={};
var sendReaction=function(name){
if(!reactURL||!name)return;
post(reactURL,{reaction:name}).catch(function(){});
};
var floatReaction=function(glyph){
if(!reactionLayer||!glyph)return;
var bubble=document.createElement('span');
bubble.className='huddle-reaction-bubble';
bubble.textContent=glyph;
bubble.style.left=(10+Math.floor(Math.random()*80))+'%';
reactionLayer.appendChild(bubble);
window.setTimeout(function(){bubble.remove()},2600);
};
Array.prototype.forEach.call(session.querySelectorAll('[data-huddle-react-name]'),function(button){
var name=button.getAttribute('data-huddle-react-name');
glyphMap[name]=button.textContent;
button.addEventListener('click',function(){sendReaction(name)});
});
document.addEventListener('sameoldchat:event',function(event){
var detail=event.detail;
if(!detail||detail.type!=='huddle.reaction')return;
var decoded=null;
try{decoded=JSON.parse(detail.data)}catch(error){return}
if(!decoded||decoded.call_id!==callID||!decoded.reaction)return;
floatReaction(glyphMap[decoded.reaction]||(':'+decoded.reaction+':'));
});
document.addEventListener('sameoldchat:event',function(event){
var detail=event.detail;
if(!detail||detail.type!=='huddle.presence')return;
var decoded=null;
try{decoded=JSON.parse(detail.data)}catch(error){return}
if(!decoded||decoded.call_id!==callID||!decoded.user_id)return;
applyPresence(decoded.user_id,decoded.muted==='true',decoded.camera==='true',decoded.presenting==='true');
});
var control=function(name){return session.querySelector('[data-huddle-control="'+name+'"]')};
var microphone=control('microphone');
if(microphone)microphone.addEventListener('click',function(){
if(!local)return;
var track=local.getAudioTracks()[0];
if(!track)return;
track.enabled=!track.enabled;
selfMuted=!track.enabled;
microphone.setAttribute('aria-pressed',track.enabled?'false':'true');
microphone.textContent=track.enabled?'Mute microphone':'Unmute microphone';
session.setAttribute('data-huddle-microphone',track.enabled?'on':'off');
applyPresence(selfID,selfMuted,selfCamera,selfPresenting);
broadcastPresence();
});
var showSelf=function(track){
tileFor(selfID).querySelector('video').srcObject=track?new MediaStream([track]):local;
};
var camera=control('camera');
if(camera)camera.addEventListener('click',function(){
if(!videoSender)return;
if(cameraTrack){
cameraTrack.stop();cameraTrack=null;
videoSender.replaceTrack(null);
showSelf(null);
selfCamera=false;
camera.setAttribute('aria-pressed','false');
camera.textContent='Turn on camera';
session.setAttribute('data-huddle-camera','off');
applyPresence(selfID,selfMuted,selfCamera,selfPresenting);
broadcastPresence();
return;
}
navigator.mediaDevices.getUserMedia({video:true}).then(function(stream){
cameraTrack=stream.getVideoTracks()[0];
if(!cameraTrack)return;
videoSender.replaceTrack(cameraTrack);
showSelf(cameraTrack);
selfCamera=true;
camera.setAttribute('aria-pressed','true');
camera.textContent='Turn off camera';
session.setAttribute('data-huddle-camera','on');
applyPresence(selfID,selfMuted,selfCamera,selfPresenting);
broadcastPresence();
}).catch(function(){announce('Your camera could not be opened. The huddle continues without it.')});
});
var screen=control('screen');
if(screen)screen.addEventListener('click',function(){
if(!screenSender)return;
if(screenStream){
screenStream.getTracks().forEach(function(track){track.stop()});
screenStream=null;
screenSender.replaceTrack(null);
detachScreen(selfID);
selfPresenting=false;
screen.setAttribute('aria-pressed','false');
screen.textContent='Share screen';
session.setAttribute('data-huddle-screen','off');
applyPresence(selfID,selfMuted,selfCamera,selfPresenting);
broadcastPresence();
return;
}
if(!navigator.mediaDevices.getDisplayMedia){announce('This browser cannot share a screen.');return}
navigator.mediaDevices.getDisplayMedia({video:true}).then(function(stream){
screenStream=stream;
var track=stream.getVideoTracks()[0];
if(!track)return;
track.addEventListener('ended',function(){if(screenStream)screen.click()});
screenSender.replaceTrack(track);
attachScreen(selfID,stream);
selfPresenting=true;
screen.setAttribute('aria-pressed','true');
screen.textContent='Stop sharing';
session.setAttribute('data-huddle-screen','on');
applyPresence(selfID,selfMuted,selfCamera,selfPresenting);
broadcastPresence();
}).catch(function(){announce('The screen was not shared.')});
});
navigator.mediaDevices.getUserMedia({audio:true}).then(function(stream){
local=stream;
session.setAttribute('data-huddle-microphone','on');
tileFor(selfID).querySelector('video').srcObject=local;
applyPresence(selfID,selfMuted,selfCamera,selfPresenting);
broadcastPresence();
meterFor(selfID,local);
speakingLoop();
connect();
report();
}).catch(function(){
announce('Your microphone was not available, so this huddle has no sound for you. Everything else still works.');
session.setAttribute('data-huddle-microphone','denied');
});
window.addEventListener('pagehide',function(){
if(pc){try{pc.close()}catch(error){}}
if(local)local.getTracks().forEach(function(track){track.stop()});
if(screenStream)screenStream.getTracks().forEach(function(track){track.stop()});
if(audioContext){try{audioContext.close()}catch(error){}}
});
};
start(document.querySelector('[data-huddle-call]'));
if(window.MutationObserver){
new MutationObserver(function(){start(document.querySelector('[data-huddle-call]'))}).observe(document.body,{childList:true,subtree:true});
}
})();</script>`
