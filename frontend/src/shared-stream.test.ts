import test from 'node:test';
import assert from 'node:assert/strict';
import { sharedStream, LEASE_MS, RENEW_MS, type ChannelLike, type EventSourceLike } from './shared-stream.ts';

// A fake browser: shared storage, a broadcast bus, a clock, and timers that
// only fire when the test advances time.
function world() {
  let now = 0;
  const store = new Map<string, string>();
  const channels = new Set<ChannelLike>();
  const sources: (EventSourceLike & { closed: boolean; emit(name: string, data: unknown): void })[] = [];
  const timers: { fn: () => void; ms: number; next: number; on: boolean }[] = [];
  const leaves: (() => void)[] = [];
  const tab = (id: string) => {
    let leave = () => {}; let timer: { on: boolean } | null = null;
    const s = sharedStream('/xhaos/events', ['xhaos:event'], {
      id,
      now: () => now,
      storage: { getItem: k => store.get(k) ?? null, setItem: (k, v) => void store.set(k, v), removeItem: k => void store.delete(k) },
      channel: () => {
        const ch: ChannelLike = { onmessage: null, postMessage: msg => channels.forEach(c => c !== ch && c.onmessage?.({ data: structuredClone(msg) })), close: () => void channels.delete(ch) };
        channels.add(ch); return ch;
      },
      open: () => {
        const listeners = new Map<string, (e: { data: string }) => void>();
        const src = { onopen: null as (() => void) | null, onerror: null, closed: false, addEventListener: (n: string, fn: (e: { data: string }) => void) => void listeners.set(n, fn), close() { this.closed = true; }, emit: (n: string, d: unknown) => listeners.get(n)?.({ data: JSON.stringify(d) }) };
        sources.push(src); return src;
      },
      every: (ms, fn) => { const t = { fn, ms, next: now + ms, on: true }; timer = t; timers.push(t); return () => { t.on = false; }; },
      onLeave: fn => { leave = fn; },
    });
    const got: unknown[] = []; let opens = 0;
    s.On('xhaos:event', e => got.push(e.data)); s.onOpen!(() => opens++);
    return { s, got, opens: () => opens, leave: () => leave(), freeze: (on: boolean) => { timer!.on = !on; } };
  };
  const advance = (ms: number) => { const end = now + ms; for (;;) { const t = timers.filter(t => t.on && t.next <= end).sort((a, b) => a.next - b.next)[0]; if (!t) break; now = t.next; t.next += t.ms; t.fn(); } now = end; };
  const live = () => sources.filter(s => !s.closed);
  return { tab, advance, live, leaves };
}

test('tabs share one stream and all receive its events', () => {
  const w = world();
  const a = w.tab('a'), b = w.tab('b'), c = w.tab('c');
  assert.equal(w.live().length, 1);
  assert.deepEqual([a.s.role(), b.s.role(), c.s.role()], ['leader', 'follower', 'follower']);
  w.live()[0].onopen!();
  assert.deepEqual([a.opens(), b.opens(), c.opens()], [1, 1, 1]);
  w.live()[0].emit('xhaos:event', { seq: 1 });
  for (const t of [a, b, c]) assert.deepEqual(t.got, [{ seq: 1 }]);
  w.advance(RENEW_MS * 5);
  assert.equal(w.live().length, 1, 'the leader keeps its lease');
});

test('a closed leader hands over at once', () => {
  const w = world();
  const a = w.tab('a'), b = w.tab('b');
  a.leave();
  assert.equal(b.s.role(), 'leader');
  assert.equal(w.live().length, 1);
});

test('a frozen leader is replaced when its lease runs out, and steps down when it wakes', () => {
  const w = world();
  const a = w.tab('a'), b = w.tab('b');
  a.freeze(true); // a background tab whose timers are throttled
  w.advance(LEASE_MS + RENEW_MS);
  assert.equal(b.s.role(), 'leader');
  w.live().at(-1)!.onopen!();
  assert.equal(b.opens(), 1);
  a.freeze(false);
  w.advance(RENEW_MS);
  assert.equal(a.s.role(), 'follower');
  assert.equal(w.live().length, 1);
  w.live()[0].emit('xhaos:event', { seq: 9 });
  assert.deepEqual(a.got, [{ seq: 9 }]);
});

test('two tabs that both take an expired lease settle on one leader', () => {
  const w = world();
  const a = w.tab('a');
  const b = w.tab('b');
  assert.equal(w.live().length, 1);
  w.advance(LEASE_MS * 3);
  assert.equal(w.live().length, 1);
  assert.notEqual(a.s.role(), b.s.role());
});
