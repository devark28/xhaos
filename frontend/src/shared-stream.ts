// One live stream per browser. Over plain HTTP a browser allows six open
// connections per server, and every Xhaos tab used to hold one for its
// event stream, so with a few tabs open, clicks queued behind them. Now one
// tab (the leader) holds the stream and relays every message to the other
// tabs over a BroadcastChannel.
//
// Election works without the Web Locks API (missing on insecure origins such
// as a LAN address): the leader keeps a lease in localStorage and renews it;
// when the lease runs out (the tab closed, crashed, or was throttled), the
// next tab to notice takes it. A tab that finds someone else holding its
// lease steps down. Every time a tab starts leading, its stream's open runs
// onOpen in every tab, so they all catch up on anything missed in between.
import type { WailsEvents } from './server-client.ts';

export interface StreamDeps {
  open(url: string): EventSourceLike;
  channel(name: string): ChannelLike;
  storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
  now(): number;
  every(ms: number, fn: () => void): () => void;
  onLeave(fn: () => void): void;
  onRole?(role: 'leader' | 'follower'): void;
  id: string;
}
export interface EventSourceLike {
  addEventListener(name: string, fn: (e: { data: string }) => void): void;
  onopen: (() => void) | null;
  onerror: (() => void) | null;
  close(): void;
}
export interface ChannelLike { postMessage(msg: unknown): void; onmessage: ((e: { data: any }) => void) | null; close(): void }

const KEY = 'xhaos-stream-lease';
export const LEASE_MS = 8000, RENEW_MS = 2000;

type Wire = { type: 'event'; name: string; data: string } | { type: 'open' } | { type: 'lost' } | { type: 'resign' };

export function sharedStream(url: string, names: string[], deps: StreamDeps): WailsEvents & { role(): 'leader' | 'follower'; stop(): void } {
  const handlers = new Map<string, Set<(e: { data: unknown }) => void>>();
  const opened: (() => void)[] = [], lost: (() => void)[] = [];
  const bus = deps.channel('xhaos-stream');
  let source: EventSourceLike | null = null;

  const deliver = (name: string, data: string) => {
    let parsed: unknown;
    try { parsed = JSON.parse(data); } catch { return; }
    handlers.get(name)?.forEach(fn => fn({ data: parsed }));
  };
  const lease = (): { id: string; until: number } | null => { try { return JSON.parse(deps.storage.getItem(KEY) || 'null'); } catch { return null; } };
  const write = () => { try { deps.storage.setItem(KEY, JSON.stringify({ id: deps.id, until: deps.now() + LEASE_MS })); } catch { /* no storage: stays leader alone */ } };

  function lead() {
    if (source) return;
    write();
    source = deps.open(url);
    for (const name of names) source.addEventListener(name, e => { deliver(name, e.data); bus.postMessage({ type: 'event', name, data: e.data } satisfies Wire); });
    source.onopen = () => { opened.forEach(fn => fn()); bus.postMessage({ type: 'open' } satisfies Wire); };
    source.onerror = () => { lost.forEach(fn => fn()); bus.postMessage({ type: 'lost' } satisfies Wire); };
    deps.onRole?.('leader');
  }
  function follow() { source?.close(); source = null; deps.onRole?.('follower'); }

  // Each tick: the leader renews (or steps down if someone took over); a
  // follower takes an expired lease.
  function tick() {
    const held = lease();
    if (source) { if (held && held.id !== deps.id && held.until > deps.now()) follow(); else write(); return; }
    if (!held || held.until <= deps.now()) lead();
  }
  bus.onmessage = ({ data }: { data: Wire }) => {
    if (data.type === 'event') { if (!source) deliver(data.name, data.data); }
    else if (data.type === 'open') { if (!source) opened.forEach(fn => fn()); }
    else if (data.type === 'lost') { if (!source) lost.forEach(fn => fn()); }
    else if (data.type === 'resign') tick();
  };
  const stopTimer = deps.every(RENEW_MS, tick);
  const stop = () => {
    stopTimer();
    if (source) {
      follow();
      try { if (lease()?.id === deps.id) deps.storage.removeItem(KEY); } catch { /* ignore */ }
      bus.postMessage({ type: 'resign' } satisfies Wire);
    }
    bus.close();
  };
  deps.onLeave(stop);
  tick();
  if (!source) deps.onRole?.('follower');

  return {
    On(name, fn) {
      if (!handlers.has(name)) handlers.set(name, new Set());
      handlers.get(name)!.add(fn);
      return () => handlers.get(name)?.delete(fn);
    },
    onOpen: fn => { opened.push(fn); },
    onLost: fn => { lost.push(fn); },
    role: () => (source ? 'leader' : 'follower'),
    stop,
  };
}

// browserStream is sharedStream wired to the real browser, or null where a
// piece is missing (then each tab keeps its own stream).
export function browserStream(url: string, names: string[], id: string) {
  if (typeof BroadcastChannel === 'undefined' || typeof EventSource === 'undefined') return null;
  try { localStorage.getItem(KEY); } catch { return null; }
  return sharedStream(url, names, {
    open: u => new EventSource(u) as unknown as EventSourceLike,
    channel: n => new BroadcastChannel(n) as unknown as ChannelLike,
    storage: localStorage,
    now: () => Date.now(),
    every: (ms, fn) => { const t = setInterval(fn, ms); return () => clearInterval(t); },
    onLeave: fn => {
      addEventListener('pagehide', fn);
      // Back/forward cache: the stream was stopped on the way out.
      addEventListener('pageshow', e => { if (e.persisted) location.reload(); });
    },
    onRole: role => { document.documentElement.dataset.stream = role; },
    id,
  });
}
