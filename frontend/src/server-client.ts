// Browser-side client for the Go backend. When the page is served by the Wails
// server, /wails/runtime.js exists and defines window.wails; under
// `npm run dev` it does not, and the prototype keeps running on mock state.
import type { BackendEvent, Snapshot } from './types.ts';
import { newId } from './ids.ts';
import { browserStream } from './shared-stream.ts';

// Bound Go services, one per backend feature package.
const services = {
  health: 'github.com/devark28/xhaos/app/health.Service',
  workspace: 'github.com/devark28/xhaos/app/workspace.Service',
  agent: 'github.com/devark28/xhaos/app/agent.Service',
  eventlog: 'github.com/devark28/xhaos/app/eventlog.Service',
  provider: 'github.com/devark28/xhaos/app/provider.Service',
  btw: 'github.com/devark28/xhaos/app/btw.Service',
  tool: 'github.com/devark28/xhaos/app/tool.Service',
  project: 'github.com/devark28/xhaos/app/project.Service',
  mcp: 'github.com/devark28/xhaos/app/mcp.Service',
  channel: 'github.com/devark28/xhaos/app/channel.Owner',
  attachment: 'github.com/devark28/xhaos/app/attachment.Service',
  memory: 'github.com/devark28/xhaos/app/memory.Service',
  usage: 'github.com/devark28/xhaos/app/usage.Service',
  prefs: 'github.com/devark28/xhaos/app/prefs.Service',
  secret: 'github.com/devark28/xhaos/app/secret.Service',
  danger: 'github.com/devark28/xhaos/app/danger.Service',
  skill: 'github.com/devark28/xhaos/app/skill.Service',
  web: 'github.com/devark28/xhaos/app/web.Service',
  board: 'github.com/devark28/xhaos/app/board.Service',
  dictation: 'github.com/devark28/xhaos/app/dictation.Service',
  node: 'github.com/devark28/xhaos/app/node.Service',
  classifier: 'github.com/devark28/xhaos/app/classifier.Service',
} as const;
export type ServiceName = keyof typeof services;

// Live events: On subscribes to one event name; onOpen runs each time the
// stream (re)connects, so the caller can catch up on anything it missed.
export interface WailsEvents { On(name: string, fn: (event: any) => void): () => void; onOpen?(fn: () => void): void; onLost?(fn: () => void): void }
interface WailsRuntime { Call: { ByName(name: string, ...args: unknown[]): Promise<any> }; Events: WailsEvents }

// streamEvents follows /xhaos/events, the server's Server-Sent Events stream
// (it needs the session cookie, unlike the Wails event socket). EventSource
// reconnects by itself; onLost runs when a connection drops.
export function streamEvents(url = '/xhaos/events'): WailsEvents {
  const source = new EventSource(url);
  const opened: (() => void)[] = [], lost: (() => void)[] = [];
  source.onopen = () => opened.forEach(fn => fn());
  source.onerror = () => lost.forEach(fn => fn());
  return {
    On(name, fn) {
      const handle = (e: MessageEvent) => { try { fn({ data: JSON.parse(e.data) }); } catch { /* not JSON: ignore */ } };
      source.addEventListener(name, handle as EventListener);
      return () => source.removeEventListener(name, handle as EventListener);
    },
    onOpen: fn => { opened.push(fn); },
    onLost: fn => { lost.push(fn); },
  };
}
export type Call = (service: ServiceName, method: string, ...args: unknown[]) => Promise<any>;
export interface Backend { status: { version: string }; call: Call; events: WailsEvents }
export const DELTA_EVENT_NAME = 'xhaos:delta';
export const EVENT_NAME = 'xhaos:event';
export const BOARD_EVENT_NAME = 'xhaos:board'; // live board saves, not stored
export const NODE_EVENT_NAME = 'xhaos:node'; // nodes going online and offline, and their jobs; not stored

export async function connectBackend(
  load: () => Promise<unknown> = () => import('/wails/runtime.js' as string),
  scope: { wails?: WailsRuntime } = globalThis as { wails?: WailsRuntime },
  // One stream per browser, shared by its tabs (shared-stream.ts); a tab of
  // its own where BroadcastChannel or storage is missing.
  events: () => WailsEvents | undefined = () => (typeof EventSource === 'undefined' ? undefined : browserStream('/xhaos/events', [EVENT_NAME, DELTA_EVENT_NAME, BOARD_EVENT_NAME, NODE_EVENT_NAME], newId()) ?? streamEvents()),
  required = false, // served by Xhaos: a missing runtime is an error, not prototype mode
): Promise<Backend | null> {
  try { await load(); } catch (error) {
    if (required) throw new Error(`the Wails runtime did not load (${error instanceof Error ? error.message : String(error)})`);
    return null;
  }
  const wails = scope.wails;
  if (!wails?.Call?.ByName) {
    if (required) throw new Error('the Wails runtime loaded without its call API');
    return null;
  }
  const call: Call = (service, method, ...args) => wails.Call.ByName(`${services[service]}.${method}`, ...args);
  const status = await call('health', 'Status');
  return { status, call, events: events() ?? wails.Events };
}

export const idempotencyKey = (): string => newId();

// Keeps a workspace view in step with the backend. The snapshot is the state;
// live events are applied strictly in seq order. Duplicates (seq <= current)
// are dropped; a gap triggers a catch-up through EventsAfter, so events missed
// while the socket was down are never silently skipped.
export interface SyncOptions {
  call: Call;
  events?: WailsEvents;
  workspaceId: string;
  onSnapshot(snapshot: Snapshot): void;
  onEvent(event: BackendEvent): void;
  onError?(error: unknown): void;
}
export function createSync({ call, events, workspaceId, onSnapshot, onEvent, onError = () => {} }: SyncOptions) {
  let seq = 0, started = false, again = false;
  let catching: Promise<void> | null = null;
  const apply = (event: BackendEvent) => {
    if (event.workspaceId !== workspaceId || event.seq <= seq) return;
    if (event.seq !== seq + 1) { catchUp(); return; }
    seq = event.seq;
    onEvent(event);
  };
  async function drain() {
    do {
      again = false;
      for (;;) {
        const batch: BackendEvent[] = await call('eventlog', 'EventsAfter', workspaceId, seq, 500);
        batch.forEach(apply);
        if (batch.length < 500) break;
      }
    } while (again);
  }
  function catchUp(): Promise<void> {
    if (!started) return Promise.resolve();
    if (catching) { again = true; return catching; }
    catching = drain().catch(onError).finally(() => { catching = null; });
    return catching;
  }
  const live = (event: { data?: BackendEvent } | BackendEvent) => apply(('data' in event && event.data && 'seq' in event.data ? event.data : event) as BackendEvent);
  return {
    get seq() { return seq; },
    async start() {
      const snapshot: Snapshot = await call('agent', 'Snapshot', workspaceId);
      seq = snapshot.seq;
      started = true;
      onSnapshot(snapshot);
      const off = events?.On?.(EVENT_NAME, live);
      await catchUp(); // anything committed between the snapshot and subscribing
      return off;
    },
    catchUp,
    receive: live,
  };
}
