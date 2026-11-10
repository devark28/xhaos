import test from 'node:test';
import assert from 'node:assert/strict';
import { liveSubagents, terminateFlow, terminatePrompt } from './terminate-confirm.ts';
import type { Agent } from './types.ts';

const agent = (id: number, status: Agent['status'] = 'running', children: Agent[] = []) => ({ id, status, children }) as unknown as Agent;

function flow() {
  let asked: number | null = null;
  const terminated: number[] = [];
  const f = terminateFlow(() => asked, id => { asked = id; }, id => { terminated.push(id); });
  return { f, terminated, asked: () => asked };
}

test('choosing Terminate agent only opens the dialog', () => {
  const { f, terminated, asked } = flow();
  f.ask(7);
  assert.equal(asked(), 7);
  assert.deepEqual(terminated, []);
});

test('Cancel (also Escape and a click outside) closes the dialog and terminates nothing', () => {
  const { f, terminated, asked } = flow();
  f.ask(7);
  f.cancel();
  assert.equal(asked(), null);
  assert.deepEqual(terminated, []);
  f.confirm(); // a late confirm after cancelling does nothing
  assert.deepEqual(terminated, []);
});

test('Terminate terminates the asked-about agent once and closes the dialog', () => {
  const { f, terminated, asked } = flow();
  f.ask(7);
  f.confirm();
  assert.deepEqual(terminated, [7]);
  assert.equal(asked(), null);
});

test('the prompt names the agent and counts the subagents it stops', () => {
  assert.deepEqual(terminatePrompt(agent(4)), { title: 'Terminate agent #4?', text: 'This stops #4. Its history is kept: you can bring it back from /history.' });
  assert.equal(terminatePrompt(agent(4, 'running', [agent(5)])).text, 'This stops #4 and its subagent. Its history is kept: you can bring it back from /history.');
  const tree = agent(4, 'running', [agent(5, 'idle', [agent(8, 'waiting')]), agent(6, 'terminated'), agent(7, 'complete')]);
  assert.equal(liveSubagents(tree), 3);
  assert.match(terminatePrompt(tree).text, /^This stops #4 and its 3 subagents\./);
});
