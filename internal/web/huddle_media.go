package web

// huddleMediaScript connects one browser to the other people in a huddle.
//
// The connection is peer to peer: each browser opens an RTCPeerConnection to
// each other participant and sends its own audio and video directly. Slack uses
// a selective forwarding unit, which scales past the handful of people a mesh
// can carry — with a mesh, every participant uploads one copy of their media
// per other participant, so a six-person huddle asks each browser for five
// uploads. That ceiling is recorded in the journey document rather than hidden,
// and it is the honest shape for a deployment that hosts no media server.
//
// The server relays the handshake and nothing else. An SDP description and an
// ICE candidate mean something to the two browsers and nothing here, which is
// why the payload crosses the service as an opaque string.
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
var signalURL=session.getAttribute('data-huddle-signal');
var csrf=session.getAttribute('data-huddle-csrf');
var peerList=(session.getAttribute('data-huddle-peers')||'').split(',').filter(function(id){return id});
var connections={};
var local=null;
var screenStream=null;
var send=function(to,kind,payload){
var body=new URLSearchParams();
body.set('_csrf',csrf);
body.set('call_id',callID);
body.set('to',to);
body.set('signal',kind);
body.set('payload',payload);
return fetch(signalURL,{method:'POST',credentials:'same-origin',headers:{'content-type':'application/x-www-form-urlencoded'},body:body.toString()}).then(function(response){
if(response.status===409){drop(to);return false}
return response.ok;
}).catch(function(){return false});
};
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
label.textContent=id===selfID?'You':id;
item.appendChild(label);
tiles.appendChild(item);
return item;
};
var drop=function(id){
var connection=connections[id];
if(connection){try{connection.close()}catch(error){}delete connections[id]}
var tile=tiles.querySelector('[data-huddle-tile="'+id+'"]');
if(tile)tile.remove();
};
var connected=function(){
return Object.keys(connections).filter(function(id){
return connections[id].connectionState==='connected';
}).length;
};
var report=function(){
var total=connected();
session.setAttribute('data-huddle-connected',String(total));
if(total>0){announce(total===1?'Connected to 1 other person.':'Connected to '+total+' other people.');return}
announce('Your microphone is on. Waiting for somebody else to join.');
};
var connectionFor=function(id){
if(connections[id])return connections[id];
var connection=new RTCPeerConnection({iceServers:[]});
connections[id]=connection;
if(local)local.getTracks().forEach(function(track){connection.addTrack(track,local)});
connection.onicecandidate=function(event){if(event.candidate)send(id,'candidate',JSON.stringify(event.candidate))};
connection.ontrack=function(event){
var tile=tileFor(id);
var media=tile.querySelector('video');
if(event.streams&&event.streams[0]&&media.srcObject!==event.streams[0])media.srcObject=event.streams[0];
};
connection.onconnectionstatechange=function(){
var tile=tileFor(id);
tile.setAttribute('data-huddle-state',connection.connectionState);
if(connection.connectionState==='failed'||connection.connectionState==='closed')drop(id);
report();
};
return connection;
};
var offerTo=function(id){
var connection=connectionFor(id);
return connection.createOffer().then(function(offer){
return connection.setLocalDescription(offer).then(function(){
return send(id,'offer',JSON.stringify(connection.localDescription));
});
}).catch(function(){});
};
var receive=function(from,kind,payload){
var connection=connectionFor(from);
var described=null;
try{described=JSON.parse(payload)}catch(error){return}
if(kind==='offer'){
connection.setRemoteDescription(described).then(function(){
return connection.createAnswer();
}).then(function(answer){
return connection.setLocalDescription(answer).then(function(){
return send(from,'answer',JSON.stringify(connection.localDescription));
});
}).catch(function(){});
return;
}
if(kind==='answer'){connection.setRemoteDescription(described).catch(function(){});return}
connection.addIceCandidate(described).catch(function(){});
};
document.addEventListener('sameoldchat:event',function(event){
var detail=event.detail;
if(!detail||detail.type!=='huddle.signal')return;
var decoded=null;
try{decoded=JSON.parse(detail.data)}catch(error){return}
if(!decoded||decoded.call_id!==callID||!decoded.from_user_id)return;
receive(decoded.from_user_id,decoded.signal,decoded.payload);
});
var replaceOutgoing=function(track,kind){
Object.keys(connections).forEach(function(id){
var sender=connections[id].getSenders().filter(function(candidate){
return candidate.track&&candidate.track.kind===kind;
})[0];
if(sender)sender.replaceTrack(track);
});
};
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
var camera=control('camera');
if(camera)camera.addEventListener('click',function(){
if(!local)return;
var existing=local.getVideoTracks()[0];
if(existing&&existing.enabled){
existing.enabled=false;
replaceOutgoing(null,'video');
camera.setAttribute('aria-pressed','false');
camera.textContent='Turn on camera';
session.setAttribute('data-huddle-camera','off');
return;
}
if(existing){
existing.enabled=true;
replaceOutgoing(existing,'video');
camera.setAttribute('aria-pressed','true');
camera.textContent='Turn off camera';
session.setAttribute('data-huddle-camera','on');
return;
}
navigator.mediaDevices.getUserMedia({video:true}).then(function(stream){
var track=stream.getVideoTracks()[0];
if(!track)return;
local.addTrack(track);
Object.keys(connections).forEach(function(id){connections[id].addTrack(track,local)});
tileFor(selfID).querySelector('video').srcObject=local;
camera.setAttribute('aria-pressed','true');
camera.textContent='Turn off camera';
session.setAttribute('data-huddle-camera','on');
}).catch(function(){announce('Your camera could not be opened. The huddle continues without it.')});
});
var screen=control('screen');
if(screen)screen.addEventListener('click',function(){
if(screenStream){
screenStream.getTracks().forEach(function(track){track.stop()});
screenStream=null;
var restored=local?local.getVideoTracks()[0]:null;
replaceOutgoing(restored&&restored.enabled?restored:null,'video');
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
replaceOutgoing(track,'video');
screen.setAttribute('aria-pressed','true');
screen.textContent='Stop sharing';
session.setAttribute('data-huddle-screen','on');
}).catch(function(){announce('The screen was not shared.')});
});
navigator.mediaDevices.getUserMedia({audio:true}).then(function(stream){
local=stream;
session.setAttribute('data-huddle-microphone','on');
tileFor(selfID).querySelector('video').srcObject=local;
report();
peerList.forEach(function(id){
if(id===selfID)return;
if(selfID<id){offerTo(id);return}
connectionFor(id);
});
}).catch(function(){
announce('Your microphone was not available, so this huddle has no sound for you. Everything else still works.');
session.setAttribute('data-huddle-microphone','denied');
});
window.addEventListener('pagehide',function(){
Object.keys(connections).forEach(drop);
if(local)local.getTracks().forEach(function(track){track.stop()});
if(screenStream)screenStream.getTracks().forEach(function(track){track.stop()});
});
};
start(document.querySelector('[data-huddle-call]'));
if(window.MutationObserver){
new MutationObserver(function(){start(document.querySelector('[data-huddle-call]'))}).observe(document.body,{childList:true,subtree:true});
}
})();</script>`
