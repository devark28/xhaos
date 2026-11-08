import test from 'node:test';
import assert from 'node:assert/strict';
import { viewFromSnapshot, prependOlder } from './backend-view.ts';

const tool = (seq: number, callId: string, state: string) => ({ seq, agentId: 1, kind: 'agent.tool', data: { callId, name: 'bash', summary: `run ${callId}`, state } });
const msg = (seq: number, body: string) => ({ seq, agentId: 1, kind: 'agent.message', data: { from: 'agent', body } });

test('the snapshot marks agents with older history; older pages go in before', () => {
  const view = viewFromSnapshot({
    seq: 20, workspace: { id: 'default', name: 'Default' },
    agents: [{ id: 1, title: 'Long', status: 'attention' }, { id: 2, title: 'Short', status: 'idle' }],
    // A waiting call older than the page (seq 3), then the recent end.
    transcript: [tool(3, 'wait', 'pending'), msg(10, 'ten'), tool(11, 'b', 'allowed'), tool(12, 'b', 'done')],
    older: { 1: 10 },
  } as any);
  const long = view.agents.find(a => a.id === 1)!, short = view.agents.find(a => a.id === 2)!;
  assert.equal(long.older, 10);
  assert.equal(short.older, undefined);
  assert.deepEqual(long.activity!.map(e => e.seq), [3, 10, 12]);

  // The page before 10 holds the waiting call again, an earlier state of it,
  // and a call whose later state is already shown: none of those come back.
  assert.equal(prependOlder(view, 1, { events: [msg(2, 'two'), tool(3, 'wait', 'pending'), tool(5, 'c', 'pending'), tool(6, 'c', 'done'), msg(8, 'eight'), tool(9, 'b', 'pending')], more: true } as any), true);
  assert.deepEqual(long.activity!.map(e => e.callId || e.text), ['two', 'c', 'eight', 'wait', 'ten', 'b']);
  assert.equal(long.activity!.find(e => e.callId === 'c')!.state, 'done', 'within a page, the later state wins');
  assert.equal(long.activity!.find(e => e.callId === 'b')!.state, 'done', 'a shown call keeps its newer state');
  assert.equal(long.older, 2, 'next page: before the first event of this one');

  prependOlder(view, 1, { events: [msg(1, 'one')], more: false } as any);
  assert.equal(long.activity![0].text, 'one');
  assert.equal(long.older, undefined, 'nothing older left');
  assert.equal(prependOlder(view, 99, { events: [], more: false } as any), false);
});
