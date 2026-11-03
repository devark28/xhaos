import test from 'node:test';
import assert from 'node:assert/strict';
import { resolveSubmit, composerPlaceholder, takeMention, projectConflict } from './composer-target.ts';
import { viewFromSnapshot, applyEvent, applyDelta, pickerChoices } from './backend-view.ts';
import { findModel, loadModelChoice, saveModelChoice, modelChoices } from './models.ts';

test('no recipients: plain text creates a new agent', () => {
  assert.deepEqual(resolveSubmit('  Fix the composer  '), { kind: 'create', prompt: 'Fix the composer', parentId: 0 });
  assert.deepEqual(resolveSubmit('/new Write docs'), { kind: 'create', prompt: 'Write docs', parentId: 0 });
  assert.deepEqual(resolveSubmit('/new'), { kind: 'focus' });
  assert.deepEqual(resolveSubmit(''), { kind: 'none' });
});

test('recipients: plain text messages them, /new makes a subagent of one', () => {
  assert.deepEqual(resolveSubmit('status?', [3, 7]), { kind: 'message', agentIds: [3, 7], body: 'status?' });
  assert.deepEqual(resolveSubmit('/new Review it', [3]), { kind: 'create', prompt: 'Review it', parentId: 3 });
  assert.equal(resolveSubmit('/new Review it', [3, 7]).kind, 'error');
});

test('tell and other commands', () => {
  assert.deepEqual(resolveSubmit('tell agent 11: hi there'), { kind: 'message', agentIds: [11], body: 'hi there' });
  assert.deepEqual(resolveSubmit('/layout 2', [1]), { kind: 'command', text: '/layout 2' });
});

test('placeholder says what Enter will do', () => {
  assert.match(composerPlaceholder([]), /^New agent/);
  assert.match(composerPlaceholder([3, 7]), /^Message #3, #7/);
});

test('model choice falls back to the default and persists', () => {
  const store = new Map();
  const storage = { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
  assert.equal(loadModelChoice(storage), modelChoices[0].id);
  saveModelChoice('anthropic/claude-haiku-5-5', storage);
  assert.equal(loadModelChoice(storage), 'anthropic/claude-haiku-5-5');
  assert.equal(findModel('nope').id, modelChoices[0].id);
});

const snapshot = {
  seq: 3,
  workspace: { id: 'default', name: 'Default' },
  agents: [
    { id: 1, title: 'Parent', status: 'idle', provider: 'anthropic', model: 'claude-opus-5-5' },
    { id: 2, parentId: 1, title: 'Child', status: 'attention' },
  ],
  transcript: [{ seq: 2, agentId: 1, kind: 'agent.message', data: { from: 'owner', body: 'do it' } }],
};

test('snapshot becomes a card tree with transcripts', () => {
  const view = viewFromSnapshot(snapshot);
  assert.equal(view.agents.length, 1);
  const [parent] = view.agents;
  assert.equal(parent.modelLabel, 'Claude-opus-5.5');
  assert.deepEqual(parent.activity, [{ kind: 'user', text: 'do it', seq: 2 }]);
  assert.equal(parent.children[0].id, 2);
  assert.equal(parent.children[0].status, 'waiting');
});

test('live events add cards and transcript lines, and ignore duplicates', () => {
  const view = viewFromSnapshot(snapshot);
  assert.deepEqual(applyEvent(view, { kind: 'agent.created', data: { id: 3, title: 'New', status: 'idle' } }), { tree: true, created: true, agentId: 3 });
  assert.equal(applyEvent(view, { kind: 'agent.created', data: { id: 3, title: 'New', status: 'idle' } }), null);
  assert.deepEqual(applyEvent(view, { seq: 5, agentId: 2, kind: 'agent.message', data: { from: 'owner', body: 'hi' } }), { agentId: 2 });
  assert.equal(view.agents[0].id, 3);
  assert.equal(view.agents[1].children[0].activity.length, 1);
  assert.equal(applyEvent(view, { seq: 6, agentId: 99, kind: 'agent.message', data: {} }), null);
});

test('status events and live deltas', () => {
  const view = viewFromSnapshot(snapshot);
  const parent = view.agents[0];
  assert.deepEqual(applyEvent(view, { agentId: 1, kind: 'agent.status', data: { status: 'running' } }), { tree: true, agentId: 1 });
  assert.equal(parent.status, 'running');
  assert.equal(applyDelta(view, { workspaceId: 'default', agentId: 1, text: 'Hel' }), 1);
  applyDelta(view, { workspaceId: 'default', agentId: 1, text: 'lo' });
  assert.equal(parent.live, 'Hello');
  assert.equal(applyDelta(view, { workspaceId: 'other', agentId: 1, text: 'x' }), null);
  applyEvent(view, { seq: 9, agentId: 1, kind: 'agent.message', data: { from: 'agent', body: 'Hello' } });
  assert.equal(parent.live, undefined);
  assert.deepEqual(parent.activity.at(-1), { kind: 'assistant', text: 'Hello', seq: 9 });
  applyEvent(view, { seq: 10, agentId: 1, kind: 'agent.message', data: { from: 'system', body: 'No key' } });
  assert.equal(parent.activity.at(-1).kind, 'system');
});

test('picker lists models of providers that have a key', () => {
  const choices = pickerChoices([
    { id: 'anthropic', kind: 'anthropic', name: 'Anthropic', hasKey: true, models: ['claude-opus-5-5', 'claude-haiku-5-5'] },
    { id: 'spare', kind: 'anthropic', name: 'Spare', hasKey: false, models: ['claude-opus-5-5'] },
  ]);
  assert.deepEqual(choices.map(c => c.id), ['anthropic/claude-opus-5-5', 'anthropic/claude-haiku-5-5']);
  assert.equal(choices[0].label, 'Claude-opus-5.5 · Anthropic');
  assert.deepEqual(pickerChoices([]), []);
});

test('#N then a space attaches an existing agent; \\#N stays text', () => {
  const exists = id => [2, 12].includes(id);
  assert.deepEqual(takeMention('#2 ', exists), { id: 2, text: '' });
  assert.deepEqual(takeMention('ask #12\u00a0', exists), { id: 12, text: 'ask ' });
  assert.equal(takeMention('#7 ', exists), null);
  assert.equal(takeMention('\\#2 ', exists), null);
  assert.equal(takeMention('issue#2 ', exists), null);
  assert.equal(takeMention('#2', exists), null);
  assert.equal(takeMention('/terminate #2 ', exists), null);
  assert.deepEqual(resolveSubmit('see \\#2 and \\#3', [5]), { kind: 'message', agentIds: [5], body: 'see #2 and #3' });
  assert.equal(resolveSubmit('fix \\#4').prompt, 'fix #4');
});

test('an attached project names where new agents go and must match messaged agents', () => {
  assert.match(composerPlaceholder([], 'api'), /^New agent in api/);
  assert.match(composerPlaceholder([4], 'api'), /^Message #4/);
  const agents = [{ id: 1, projectId: 2 }, { id: 3, projectId: 5 }];
  assert.equal(projectConflict(agents, null), null);
  assert.equal(projectConflict(agents.slice(0, 1), 2), null);
  assert.equal(projectConflict(agents, 2).id, 3);
  assert.equal(projectConflict([{ id: 9 }], 2).id, 9); // no project is not "the same project"
});
