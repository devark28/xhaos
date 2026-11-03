import test from 'node:test';
import assert from 'node:assert/strict';
import { connectBackend, createSync } from './server-client.ts';

test('returns null when the Wails runtime is unavailable', async () => {
  assert.equal(await connectBackend(() => Promise.reject(new Error('404')), {}), null);
});

test('calls the health service through the Wails runtime', async () => {
  const calls = [];
  const scope = { wails: { Events: {}, Call: { ByName: async (name, ...args) => { calls.push([name, args]); return { version: 'v' }; } } } };
  const backend = await connectBackend(async () => {}, scope);
  assert.deepEqual(backend.status, { version: 'v' });
  assert.deepEqual(calls, [['xhaos/app/health.Service.Status', []]]);
});

const ev = seq => ({ workspaceId: 'w', seq, kind: 'agent.message' });

function fakeBackend(log, snapshotSeq) {
  return async (service, method, workspaceId, after) => {
    if (method === 'Snapshot') return { seq: snapshotSeq, agents: [] };
    if (method === 'EventsAfter') return log.filter(e => e.seq > after);
    throw new Error(method);
  };
}

test('applies live events in order and drops duplicates', async () => {
  const seen = [];
  const sync = createSync({ call: fakeBackend([], 2), workspaceId: 'w', onSnapshot() {}, onEvent: e => seen.push(e.seq) });
  await sync.start();
  sync.receive({ data: ev(3) });
  sync.receive({ data: ev(3) });
  sync.receive({ data: ev(2) });
  sync.receive({ data: { ...ev(4), workspaceId: 'other' } });
  assert.deepEqual(seen, [3]);
  assert.equal(sync.seq, 3);
});

test('a gap fetches the missed events before continuing', async () => {
  const log = [ev(1), ev(2), ev(3), ev(4)];
  const seen = [];
  const sync = createSync({ call: fakeBackend(log, 1), workspaceId: 'w', onSnapshot() {}, onEvent: e => seen.push(e.seq) });
  await sync.start();
  // start() already caught up 2..4; simulate a later reconnect gap.
  log.push(ev(5), ev(6));
  sync.receive({ data: ev(6) });
  await sync.catchUp();
  assert.deepEqual(seen, [2, 3, 4, 5, 6]);
  assert.equal(sync.seq, 6);
});

test('tool events become one entry per call and track pending calls', async () => {
  const { viewFromSnapshot, applyEvent, pendingCalls } = await import('./backend-view.ts');
  const view = viewFromSnapshot({ workspace: { id: 'default', name: 'Default' }, agents: [{ id: 1, title: 't', status: 'attention' }], transcript: [
    { seq: 2, agentId: 1, kind: 'agent.message', data: { from: 'agent', body: '', calls: [{ id: 'c1' }] } },
    { seq: 3, agentId: 1, kind: 'agent.tool', data: { callId: 'c1', name: 'bash', effect: 'exec', summary: 'ls', state: 'pending', reason: 'changes need review' } },
  ] });
  const agent = view.agents[0];
  assert.equal(agent.activity.length, 1);
  assert.equal(agent.activity[0].kind, 'approval');
  assert.deepEqual(pendingCalls(agent), ['c1']);
  applyEvent(view, { seq: 4, agentId: 1, kind: 'agent.tool', data: { callId: 'c1', name: 'bash', effect: 'exec', summary: 'ls', state: 'done', output: 'a.txt' } });
  assert.equal(agent.activity.length, 1);
  assert.equal(agent.activity[0].kind, 'tool');
  assert.equal(agent.activity[0].output, 'a.txt');
  assert.deepEqual(pendingCalls(agent), []);
  applyEvent(view, { seq: 5, agentId: 1, kind: 'agent.message', data: { from: 'tool', results: [] } });
  assert.equal(agent.activity.length, 1);
});
