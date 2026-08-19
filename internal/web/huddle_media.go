package web

// huddleMediaScript connects one browser to a huddle through the in-process
// selective forwarding unit.
//
// Each browser holds ONE connection, to this server. It publishes its microphone
// and one video lane (its camera, or its screen while sharing) once; the SFU
// forwards each participant's media to everyone else. That is one upload per
// participant however many people are in the huddle — not the mesh's one upload
// per other participant — and the media flows through a server the browser can
// always reach rather than a direct peer path that fails behind NAT.
//
// Negotiation has two halves. The browser publishes with a single initial offer
// POSTed to the SFU, which answers. Thereafter the SFU drives renegotiation:
// when the set of tracks the browser should receive changes, the SFU sends an
// offer as a recipient-scoped huddle.signal event stamped from the SFU, and the
// browser answers over the signal endpoint. Forwarded media arrives grouped into
// one stream per participant, whose id is that participant, so each track lands
// on the right tile.
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
var pc=null;
var local=null;
var cameraTrack=null;
var screenStream=null;
var videoSender=null;
var pending=[];
var haveRemote=false;
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
tiles.appendChild(item);
return item;
};
var dropTile=function(id){
var tile=tiles.querySelector('[data-huddle-tile="'+id+'"]');
if(tile)tile.remove();
report();
};
var remoteCount=function(){
var total=tiles.querySelectorAll('[data-huddle-tile]').length;
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
var owner=stream.id;
if(owner===selfID)return;
var media=tileFor(owner).querySelector('video');
if(media.srcObject!==stream)media.srcObject=stream;
stream.addEventListener('removetrack',function(){if(stream.getTracks().length===0)dropTile(owner)});
report();
};
pc.onconnectionstatechange=function(){
if(pc.connectionState==='failed'||pc.connectionState==='closed'){announce('The huddle connection dropped. Rejoin to reconnect.')}
report();
};
pc.addTrack(local.getAudioTracks()[0],local);
videoSender=pc.addTransceiver('video',{direction:'sendrecv'}).sender;
pc.createOffer().then(function(offer){return pc.setLocalDescription(offer)}).then(function(){
return post(offerURL,{sdp:pc.localDescription.sdp});
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
var control=function(name){return session.querySelector('[data-huddle-control="'+name+'"]')};
var microphone=control('microphone');
if(microphone)microphone.addEventListener('click',function(){
if(!local)return;
var track=local.getAudioTracks()[0];
if(!track)return;
track.enabled=!track.enabled;
microphone.setAttribute('aria-pressed',track.enabled?'false':'true');
microphone.textContent=track.enabled?'Mute microphone':'Unmute microphone';
session.setAttribute('data-huddle-microphone',track.enabled?'on':'off');
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
camera.setAttribute('aria-pressed','false');
camera.textContent='Turn on camera';
session.setAttribute('data-huddle-camera','off');
return;
}
navigator.mediaDevices.getUserMedia({video:true}).then(function(stream){
cameraTrack=stream.getVideoTracks()[0];
if(!cameraTrack)return;
if(!screenStream){videoSender.replaceTrack(cameraTrack);showSelf(cameraTrack)}
camera.setAttribute('aria-pressed','true');
camera.textContent='Turn off camera';
session.setAttribute('data-huddle-camera','on');
}).catch(function(){announce('Your camera could not be opened. The huddle continues without it.')});
});
var screen=control('screen');
if(screen)screen.addEventListener('click',function(){
if(!videoSender)return;
if(screenStream){
screenStream.getTracks().forEach(function(track){track.stop()});
screenStream=null;
videoSender.replaceTrack(cameraTrack||null);
showSelf(cameraTrack);
screen.setAttribute('aria-pressed','false');
screen.textContent='Share screen';
session.setAttribute('data-huddle-screen','off');
return;
}
if(!navigator.mediaDevices.getDisplayMedia){announce('This browser cannot share a screen.');return}
navigator.mediaDevices.getDisplayMedia({video:true}).then(function(stream){
screenStream=stream;
var track=stream.getVideoTracks()[0];
if(!track)return;
track.addEventListener('ended',function(){if(screenStream)screen.click()});
videoSender.replaceTrack(track);
showSelf(track);
screen.setAttribute('aria-pressed','true');
screen.textContent='Stop sharing';
session.setAttribute('data-huddle-screen','on');
}).catch(function(){announce('The screen was not shared.')});
});
navigator.mediaDevices.getUserMedia({audio:true}).then(function(stream){
local=stream;
session.setAttribute('data-huddle-microphone','on');
tileFor(selfID).querySelector('video').srcObject=local;
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
});
};
start(document.querySelector('[data-huddle-call]'));
if(window.MutationObserver){
new MutationObserver(function(){start(document.querySelector('[data-huddle-call]'))}).observe(document.body,{childList:true,subtree:true});
}
})();</script>`
