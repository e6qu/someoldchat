package web

// canvasTextScript is the canvas's collaborative text in the browser: the same
// replicated sequence as package crdt, so an op made here integrates on the
// server and on every other editor exactly as it does here. It defines
// window.sameoldchatCanvasText.create(), which returns an empty document with
// apply, insert, remove, replace and text, speaking the ops crdt.Op encodes as
// JSON.
//
// The two implementations are held together by internal/crdt's conformance
// vectors, which the browser suite replays against this script as the canvas
// page ships it. Change one and the vectors, regenerated from the Go side,
// show whether the other still agrees.
//
// Like every inline script it is hashed into the page's policy, so it carries
// no comments, backticks or template actions.
const canvasTextScript = `<script>(function(){
var replicaName=/^[A-Za-z0-9_.:-]{1,64}$/;var maxClock=9007199254740991;var missing={};
function key(id){return id.c+':'+id.r}
function idOf(value){return value?{r:value.r||'',c:value.c||0}:{r:'',c:0}}
function isZero(id){return id.r===''&&id.c===0}
function less(a,b){return a.c!==b.c?a.c<b.c:a.r<b.r}
function points(text){return Array.from(text)}
function isLone(point){var unit=point.charCodeAt(0);return point.length===1&&unit>=0xd800&&unit<=0xdfff}
function wellFormed(text){return points(text).map(function(point){return isLone(point)?'�':point}).join('')}
function isClock(value){return Number.isSafeInteger(value)&&value>0}
function check(op){
var inserting=typeof op.text==='string'&&op.text!=='';var deleting=Array.isArray(op.delete)&&op.delete.length>0;
if(inserting===deleting)throw new Error('an op is one insert or one delete');
if(inserting){var id=idOf(op.id);var after=idOf(op.after);
if(!replicaName.test(id.r)||!isClock(id.c))throw new Error('an insert names its writer and clock');
if(points(op.text).some(isLone))throw new Error('an insert is well-formed text');
if(!isZero(after)&&(!replicaName.test(after.r)||!isClock(after.c)))throw new Error('an insert goes after a character or the start');
if(id.c+points(op.text).length>maxClock)throw new Error('an insert clocks overflow');return}
op.delete.forEach(function(span){if(!span||!replicaName.test(span.r)||!isClock(span.s)||!isClock(span.e)||span.e<span.s)throw new Error('a delete names whole runs of characters')})}
function Sequence(){this.head={id:{r:'',c:0},value:'',deleted:false,next:null};this.nodes=new Map();this.clock=0;this.visible=0;this.pending=[]}
Sequence.prototype.text=function(){var out=[];for(var current=this.head.next;current;current=current.next){if(!current.deleted)out.push(current.value)}return out.join('')};
Sequence.prototype.length=function(){return this.visible};
Sequence.prototype.pendingCount=function(){return this.pending.length};
Sequence.prototype.apply=function(op){check(op);var changed=this.integrate(op);
if(changed===missing){this.pending.push(op);return false}
for(var progress=true;progress&&this.pending.length>0;){progress=false;var waiting=this.pending;this.pending=[];
for(var i=0;i<waiting.length;i++){var held=this.integrate(waiting[i]);if(held===missing){this.pending.push(waiting[i])}else{progress=true;changed=changed||held}}}
return changed};
Sequence.prototype.integrate=function(op){return typeof op.text==='string'&&op.text!==''?this.integrateInsert(op):this.integrateDelete(op)};
Sequence.prototype.integrateInsert=function(op){var id=idOf(op.id);var afterID=idOf(op.after);var after=this.head;
if(!isZero(afterID)){after=this.nodes.get(key(afterID));if(!after)return missing}
if(id.c<=after.id.c)throw new Error('an insert clock is not after the character it follows');
var changed=false;var clock=id.c;var values=points(op.text);
for(var i=0;i<values.length;i++){var next={r:id.r,c:clock};clock++;var existing=this.nodes.get(key(next));
if(existing){after=existing;continue}
var previous=after;while(previous.next&&less(next,previous.next.id))previous=previous.next;
var inserted={id:next,value:values[i],deleted:false,next:previous.next};previous.next=inserted;this.nodes.set(key(next),inserted);
this.visible++;this.clock=Math.max(this.clock,next.c);after=inserted;changed=true}
return changed};
Sequence.prototype.integrateDelete=function(op){var self=this;var i,clock,span;
for(i=0;i<op.delete.length;i++){span=op.delete[i];for(clock=span.s;clock<=span.e;clock++){if(!self.nodes.has(key({r:span.r,c:clock})))return missing}}
var changed=false;
for(i=0;i<op.delete.length;i++){span=op.delete[i];for(clock=span.s;clock<=span.e;clock++){var target=self.nodes.get(key({r:span.r,c:clock}));
if(!target.deleted){target.deleted=true;self.visible--;changed=true}self.clock=Math.max(self.clock,clock)}}
return changed};
Sequence.prototype.visibleBefore=function(position){if(position===0)return null;var index=0;
for(var current=this.head.next;current;current=current.next){if(current.deleted)continue;index++;if(index===position)return current.id}return null};
Sequence.prototype.insert=function(replica,position,text){
if(!replicaName.test(replica))throw new Error('an edit names its writer');
if(!Number.isInteger(position)||position<0||position>this.visible)throw new Error('the position is outside the document');
text=wellFormed(String(text));if(text==='')throw new Error('an insert is not empty');
var op={id:{r:replica,c:this.clock+1}};var after=this.visibleBefore(position);if(after)op.after={r:after.r,c:after.c};op.text=text;
this.apply(op);return op};
Sequence.prototype.remove=function(position,length){
if(!Number.isInteger(position)||!Number.isInteger(length)||position<0||length<=0||position+length>this.visible)throw new Error('the characters are outside the document');
var spans=[];var index=0;
for(var current=this.head.next;current&&index<position+length;current=current.next){if(current.deleted)continue;
if(index>=position){var last=spans[spans.length-1];if(last&&last.r===current.id.r&&last.e+1===current.id.c){last.e=current.id.c}else{spans.push({r:current.id.r,s:current.id.c,e:current.id.c})}}
index++}
var op={delete:spans};this.apply(op);return op};
Sequence.prototype.replace=function(replica,text){
var current=points(this.text());var next=points(wellFormed(String(text)));var prefix=0;var suffix=0;
while(prefix<current.length&&prefix<next.length&&current[prefix]===next[prefix])prefix++;
while(suffix<current.length-prefix&&suffix<next.length-prefix&&current[current.length-1-suffix]===next[next.length-1-suffix])suffix++;
var ops=[];var removed=current.length-prefix-suffix;if(removed>0)ops.push(this.remove(prefix,removed));
var inserted=next.slice(prefix,next.length-suffix).join('');if(inserted!=='')ops.push(this.insert(replica,prefix,inserted));
return ops};
window.sameoldchatCanvasText={create:function(){return new Sequence()}};
})();</script>`
