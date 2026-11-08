import test from 'node:test';
import assert from 'node:assert/strict';
import { channelsFromBackend, setChannelMessages, applyChannelEvent } from './backend-view.ts';

const msg = (id: number, body: string) => ({ id, channelId: 20, senderId: 32, body, createdAt: `2026-11-08T15:2${id}:00Z` });

test('the list has a channel\'s last message and count; opening it loads the rest', () => {
  const view: any = { id: 'default', agents: [], channels: channelsFromBackend([{ id: 20, subject: 'Nodes', creatorId: 32, members: [32], createdAt: '', count: 3, messages: [msg(3, 'third')] }] as any) };
  const c = view.channels[0];
  assert.equal(c.count, 3);
  assert.equal(c.loaded, false, 'only the preview is there');
  assert.deepEqual(c.messages.map((m: any) => m.text), ['third']);
  // A message arrives live before the channel is opened.
  applyChannelEvent(view, { kind: 'channel.message', data: msg(4, 'fourth') } as any);
  assert.equal(c.count, 4);
  // Opening loads the stored ones; the live one stays after them, once.
  assert.equal(setChannelMessages(view, '20', [msg(1, 'first'), msg(2, 'second'), msg(3, 'third')] as any), true);
  assert.deepEqual(c.messages.map((m: any) => m.text), ['first', 'second', 'third', 'fourth']);
  assert.equal(c.loaded, true);
  assert.equal(c.count, 4);
  assert.equal(setChannelMessages(view, '99', []), false);
});

test('a channel with all its messages in the list counts as loaded', () => {
  const [c] = channelsFromBackend([{ id: 1, subject: 'x', creatorId: 1, members: [1], createdAt: '', messages: [] }] as any);
  assert.equal(c.loaded, true);
  assert.equal(c.count, 0);
});
