import test from 'node:test';
import assert from 'node:assert/strict';
import { initialWorkspaces, flatten, transition } from './model.js';
test('approving a subagent changes only that request', () => {
 const workspaces=initialWorkspaces(); const all=flatten(workspaces[0].agents); const a=all.find(a=>a.id===15);
 transition(a,'approve'); assert.equal(a.status,'running');assert.equal(all.find(a=>a.id===18).status,'waiting');assert.equal(workspaces[1].agents[0].status,'paused');
});
test('pause and resume preserve task identity; completed agents stay completed',()=>{
 const all=flatten(initialWorkspaces()[0].agents), a=all[0];transition(a,'pause');assert.equal(a.status,'paused');transition(a,'pause');assert.equal(a.status,'running');assert.equal(a.id,11);
 const done=all.find(a=>a.status==='complete');transition(done,'pause');assert.equal(done.status,'complete');
});
test('declined requests pause rather than executing',()=>{const a={status:'waiting'};transition(a,'reject');assert.equal(a.status,'paused');transition(a,'approve');assert.equal(a.status,'paused');});
