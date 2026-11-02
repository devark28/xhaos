import test from 'node:test';
import assert from 'node:assert/strict';
import { createAttentionTracker, agentPath } from './attention.js';
test('attention is announced once per transition, including nested requests', () => {
 const changed=createAttentionTracker();
 const agents=[{id:1,status:'running',children:[{id:2,status:'waiting'}]}];
 assert.deepEqual(changed('studio',agents),[2]);
 assert.deepEqual(changed('studio',agents),[]);
 agents[0].children[0].status='running';
 assert.deepEqual(changed('studio',agents),[]);
 agents[0].children[0].status='waiting';
 assert.deepEqual(changed('studio',agents),[2]);
 assert.deepEqual(changed('other',agents),[2]);
 assert.deepEqual(agentPath(agents,2),[1,2]);
 assert.deepEqual(agentPath(agents,99),[]);
});
