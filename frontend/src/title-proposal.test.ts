import test from 'node:test';
import assert from 'node:assert/strict';
import { viewFromSnapshot, applyEvent } from './backend-view.ts';

// A title the agent proposed shows on its card until it is applied or ends.
test('title proposals come and go with their events', () => {
  const view = viewFromSnapshot({
    seq: 1, workspace: { id: 'default', name: 'Default' },
    agents: [{ id: 1, title: 'Old task', status: 'running', titleProposal: { title: 'From the snapshot', at: '' } }],
    transcript: [],
  } as any);
  const a = view.agents[0];
  assert.equal(a.titleProposal?.title, 'From the snapshot');
  applyEvent(view, { seq: 2, agentId: 1, kind: 'agent.title_proposal', data: { title: 'New task', reason: 'focus moved', at: '' } } as any);
  assert.deepEqual(a.titleProposal, { title: 'New task', reason: 'focus moved', at: '' });
  // The model's first title (no by) leaves a proposal alone.
  applyEvent(view, { seq: 3, agentId: 1, kind: 'agent.title', data: { title: 'Model title' } } as any);
  assert.equal(a.titleProposal?.title, 'New task');
  // An applied rename clears it.
  applyEvent(view, { seq: 4, agentId: 1, kind: 'agent.title', data: { title: 'New task', from: 'Model title', by: 'owner' } } as any);
  assert.equal(a.title, 'New task');
  assert.equal(a.titleProposal, undefined);
  applyEvent(view, { seq: 5, agentId: 1, kind: 'agent.title_proposal', data: { title: 'Another', at: '' } } as any);
  applyEvent(view, { seq: 6, agentId: 1, kind: 'agent.title_proposal', data: null } as any);
  assert.equal(a.titleProposal, undefined);
});
