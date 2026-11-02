import test from 'node:test';
import assert from 'node:assert/strict';
import { answerQuestions, demoQuestions } from './questions.js';
import { terminateAgent, transition } from './model.js';
const agent=()=>({id:1,status:'waiting',questionBatch:demoQuestions(),activity:[]});
test('batched answers accept choices and custom prose without requiring both',()=>{
 const a=agent();
 const answers=answerQuestions(a,a.questionBatch.id,{runner:{choices:['cli']},scope:{text:'Read the docs first'},notes:{}});
 assert.deepEqual(answers.runner,{choices:['cli'],text:''});
 assert.equal(answers.scope.text,'Read the docs first');
 assert.equal(a.questionBatch.status,'answered');
 assert.equal(a.status,'running');
 assert.ok(a.activity[0].text.includes('Existing CLI runner'));
 assert.throws(()=>answerQuestions(a,a.questionBatch.id,{}),/no longer pending/);
});
test('missing, invalid, and stale answers do not partially resolve a batch',()=>{
 const a=agent();
 assert.throws(()=>answerQuestions(a,'stale',{}),/no longer pending/);
 assert.throws(()=>answerQuestions(a,a.questionBatch.id,{runner:{choices:['cli']}}),/Answer/);
 assert.equal(a.questionBatch.status,'pending');
 assert.equal(a.questionBatch.answers,undefined);
 assert.throws(()=>answerQuestions(a,a.questionBatch.id,{runner:{choices:['unknown']}}),/valid/);
 transition(a,'approve');assert.equal(a.status,'waiting');
 terminateAgent([a],1);
 assert.equal(a.questionBatch.status,'cancelled');
 assert.throws(()=>answerQuestions(a,a.questionBatch.id,{}),/no longer pending/);
});
test('multiple selection is accepted only where the question permits it',()=>{
 const a=agent();a.questionBatch.questions[0].multiple=true;
 answerQuestions(a,a.questionBatch.id,{runner:{choices:['cli','api']},scope:{choices:['docs']}});
 assert.deepEqual(a.questionBatch.answers.runner.choices,['cli','api']);
});
test('the composer simulation is created once and never resurrects after answering',async()=>{
 const { ensureQuestionDemo }=await import('./questions.js');
 const workspaces=[{agents:[{id:40,children:[{id:41}]}]}];
 const first=ensureQuestionDemo(workspaces,workspaces[0]);
 assert.equal(first.id,42);
 answerQuestions(first,first.questionBatch.id,{runner:{choices:['cli']},scope:{text:'Only docs'}});
 const second=ensureQuestionDemo(workspaces,workspaces[0]);
 assert.equal(second,first);
 assert.equal(second.questionBatch.status,'answered');
 assert.equal(workspaces[0].agents.length,2);
});
