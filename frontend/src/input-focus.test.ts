import test from 'node:test';
import assert from 'node:assert/strict';
import { shouldResumeTyping, installTypingFlow } from './input-focus.ts';
const event=(key,extra={})=>({key,defaultPrevented:false,isComposing:false,ctrlKey:false,metaKey:false,altKey:false,...extra});
test('typing resumes without hijacking controls, shortcuts, selection, or IME',()=>{
 assert.equal(shouldResumeTyping(event('n')),true);
 assert.equal(shouldResumeTyping(event('/')),true);
 assert.equal(shouldResumeTyping(event('a'),{editing:true}),false);
 assert.equal(shouldResumeTyping(event('a'),{menuOpen:true}),false);
 assert.equal(shouldResumeTyping(event('a'),{textSelected:true}),false);
 assert.equal(shouldResumeTyping(event(' '),{activationTarget:true}),false);
 assert.equal(shouldResumeTyping(event('Enter')),false);
 assert.equal(shouldResumeTyping(event('b',{ctrlKey:true})),false);
 assert.equal(shouldResumeTyping(event('a',{isComposing:true})),false);
});
test('a click saves the caret and subsequent typing targets the last editor',()=>{
 const listeners={}, inserted=[];let captured=false;
 const editor={id:'new-agent-prompt',focus(options){assert.equal(options.preventScroll,true);},insertText:text=>inserted.push(text),closest:selector=>selector==='form'?{inert:false}:null};
 const doc={activeElement:{captureSelection(){captured=true;}},addEventListener:(type,fn)=>listeners[type]=fn,getElementById:id=>id===editor.id?editor:null,querySelector:selector=>selector==='#command-menu'?{hidden:true}:null};
 const win={getSelection:()=>({isCollapsed:true}),addEventListener:(type,fn)=>listeners[type]=fn};
 installTypingFlow(doc,win);
 listeners.focusin({target:{closest:()=>editor}});
 listeners.pointerdown();assert.equal(captured,true);
 doc.activeElement={closest:()=>null};let prevented=false;
 listeners.keydown({...event('x'),preventDefault(){prevented=true;}});
 assert.equal(prevented,true);assert.deepEqual(inserted,['x']);
});
