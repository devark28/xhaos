import test from 'node:test';
import assert from 'node:assert/strict';
import { applyAgentCommand } from './agent-actions.ts';
import { demoQuestions } from './questions.ts';
test('pause and resume apply to eligible attached agents without toggling ineligible states',()=>{
 const agents=[{id:1,status:'running'},{id:2,status:'paused'},{id:3,status:'complete'}];
 assert.deepEqual(applyAgentCommand(agents,new Set([1,2,3]),'pause').applied,[1]);
 assert.equal(agents[1].status,'paused');
 assert.deepEqual(applyAgentCommand(agents,new Set([1,2,3]),'resume').applied,[1,2]);
 assert.equal(agents[2].status,'complete');
});
test('explicit targets override attached recipients and aliases behave consistently',()=>{
 const agents=[{id:1,status:'waiting'},{id:2,status:'waiting'}];
 applyAgentCommand(agents,new Set([1]),'allow','#2');
 assert.equal(agents[0].status,'waiting');assert.equal(agents[1].status,'running');
 applyAgentCommand(agents,new Set([1]),'decline');assert.equal(agents[0].status,'paused');
 assert.throws(()=>applyAgentCommand(agents,new Set(),'resume'),/Attach/);
});
test('bulk approval skips questionnaires and terminated or archived agents',()=>{
 const agents=[{id:1,status:'waiting'},{id:2,status:'waiting',questionBatch:demoQuestions()},{id:3,status:'terminated'},{id:4,status:'waiting',archivedAt:'now'}];
 const result=applyAgentCommand(agents,new Set([1,2,3,4]),'allow');
 assert.deepEqual(result.applied,[1]);assert.deepEqual(result.skipped,[2,3]);
 assert.equal(agents[1].status,'waiting');assert.equal(agents[3].status,'waiting');
 assert.throws(()=>applyAgentCommand(agents,new Set([2]),'deny'),/questions/);
});
