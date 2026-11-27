import test from 'node:test';
import assert from 'node:assert/strict';
import { approvalOf, basePolicy, countsText, isPolicy, optionsFor, pickReview, policyLabel, policyMenu, policyText, reviewChoices, reviewCounts, suggestAuto, withField, within } from './policy.ts';
import { applyEvent, viewFromSnapshot } from './backend-view.ts';
import type { Agent, Policy } from './types.ts';

const auto: Policy = { access: 'rw', review: 'auto', commit: 'review' };
const ro: Policy = { access: 'ro', review: 'manual', commit: 'blocked' };

test('the card shows one label: manual, auto, or danger with the time left', () => {
  assert.equal(policyLabel(basePolicy), 'manual');
  assert.equal(policyLabel(auto), 'auto');
  assert.equal(policyLabel(ro), 'read-only');
  assert.equal(policyLabel(auto, 42 * 60_000 - 5_000), 'danger 42m');
  assert.equal(policyLabel(ro, 90_000, () => '1m 30s'), 'danger 1m 30s');
  assert.equal(policyLabel(auto, -1), 'auto', 'danger that ran out falls back to the stored mode');
  assert.equal(policyText(basePolicy), 'rw · manual · commits review');
  assert.equal(policyText(ro), 'ro · manual · commits blocked');
});

test('the ladder: danger is a timed override on top of the stored mode', () => {
  assert.equal(approvalOf(basePolicy, 0), 'manual');
  assert.equal(approvalOf(auto, 0), 'auto');
  assert.equal(approvalOf(basePolicy, 1000), 'danger');
});

test("the menu's main choices are manual then auto, in the owner's words", () => {
  assert.deepEqual(reviewChoices.map(c => c.value), ['manual', 'auto']);
  assert.equal(reviewChoices[0].hint, 'everything except reads asks you');
  assert.match(reviewChoices[1].hint, /the classifier decides other commands \(asks if the classifier is off\)/);
});

test('picking manual or auto ends danger, and stores the mode only when it changes', () => {
  assert.deepEqual(pickReview(basePolicy, true, 'manual'), { endDanger: true, policy: null });
  assert.deepEqual(pickReview(basePolicy, true, 'auto'), { endDanger: true, policy: auto });
  assert.deepEqual(pickReview(auto, false, 'manual'), { endDanger: false, policy: basePolicy });
  assert.deepEqual(pickReview(auto, false, 'auto'), { endDanger: false, policy: null });
});

test('isPolicy accepts known values only', () => {
  assert.ok(isPolicy(auto));
  assert.ok(!isPolicy({ access: 'rw', review: 'sometimes', commit: 'review' }));
  assert.ok(!isPolicy(undefined));
});

test('the menu marks the chosen value of each field', () => {
  const menu = policyMenu(auto);
  assert.deepEqual(menu.map(g => g.field), ['access', 'review', 'commit']);
  const chosen = menu.flatMap(g => g.items.filter(i => i.chosen).map(i => `${i.field}=${i.value}`));
  assert.deepEqual(chosen, ['access=rw', 'review=auto', 'commit=review']);
  assert.ok(menu.flatMap(g => g.items).every(i => !i.aboveParent), 'a top-level agent has no parent to exceed');
});

test("a subagent's menu marks choices above its parent, without hiding them", () => {
  const menu = policyMenu(ro, basePolicy);
  const above = menu.flatMap(g => g.items.filter(i => i.aboveParent).map(i => `${i.field}=${i.value}`));
  assert.deepEqual(above, ['review=auto', 'commit=allowed']);
  assert.ok(within(ro, basePolicy));
  assert.ok(!within(withField(ro, 'review', 'auto'), basePolicy));
});

test('the workspace default never offers commits allowed', () => {
  assert.deepEqual(optionsFor('commit', true).map(o => o.value), ['blocked', 'review']);
  assert.deepEqual(optionsFor('commit').map(o => o.value), ['blocked', 'review', 'allowed']);
});

test('auto is suggested only when the classifier is on and the default is read-write manual', () => {
  assert.ok(suggestAuto(basePolicy, true));
  assert.ok(!suggestAuto(basePolicy, false));
  assert.ok(!suggestAuto(auto, true));
  assert.ok(!suggestAuto(ro, true));
});

test('review counts cover active agents and subagents, not terminated or sample ones', () => {
  const agents = [
    { id: 1, title: 'a', status: 'running', policy: auto, children: [{ id: 2, title: 'b', status: 'idle', policy: basePolicy }] },
    { id: 3, title: 'c', status: 'idle', policy: ro },
    { id: 4, title: 'd', status: 'terminated', archivedAt: 'x', policy: auto },
    { id: 5, title: 'e', status: 'idle', sample: true, policy: auto },
  ] as Agent[];
  const c = reviewCounts(agents);
  assert.deepEqual(c, { auto: 1, manual: 1, readOnly: 1 });
  assert.equal(countsText(c), 'Active agents: 1 on auto review, 1 on manual review, 1 read-only.');
  assert.equal(countsText({ auto: 0, manual: 0, readOnly: 0 }), 'No active agents.');
});

test('an agent.policy event updates the card', () => {
  const view = viewFromSnapshot({ workspace: { id: 'default', name: 'Default' }, seq: 1, agents: [
    { id: 1, workspaceId: 'default', title: 'a', status: 'idle', policy: basePolicy, createdAt: '' },
  ], transcript: [] } as any);
  const change = applyEvent(view, { workspaceId: 'default', seq: 2, agentId: 1, kind: 'agent.policy', data: { policy: auto, from: basePolicy } });
  assert.deepEqual(change, { tree: true, agentId: 1 });
  assert.deepEqual(view.agents[0].policy, auto);
  assert.equal(applyEvent(view, { workspaceId: 'default', seq: 3, agentId: 1, kind: 'agent.policy', data: { policy: { access: 'x' } } }), null);
});
