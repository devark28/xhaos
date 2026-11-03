// Browser-side client for the Go backend. When the page is served by the Wails
// server, /wails/runtime.js exists and defines window.wails; under
// `npm run dev` it does not, and the prototype keeps running on mock state.
import type { BackendEvent, Snapshot } from './types.ts';

// Bound Go services, one per backend feature package.
const services = {
  health: 'xhaos/app/health.Service',
  workspace: 'xhaos/app/workspace.Service',
  agent: 'xhaos/app/agent.Service',
  eventlog: 'xhaos/app/eventlog.Service',
  provider: 'xhaos/app/provider.Service',
  btw: 'xhaos/app/btw.Service',
  tool: 'xhaos/app/tool.Service',
} as const;
export type ServiceName = keyof typeof services;

// The parts of the Wails runtime the client uses.
export interface WailsEvents { On(name: string, fn: (event: any) => void): () => void }
interface WailsRuntime { Call: { ByName(name: string, ...args: unknown[]): Promise<any> }; Events: WailsEvents }
export type Call = (service: ServiceName, method: string, ...args: unknown[]) => Promise<any>;
export interface Backend { status: { version: string }; call: Call; events: WailsEvents }
export const DELTA_EVENT_NAME = 'xhaos:delta';
export const EVENT_NAME = 'xhaos:event';

export async function connectBackend(
  load: () => Promise<unknown> = () => import('/wails/runtime.js' as string),
  scope: { wails?: WailsRuntime } = globalThis as { wails?: WailsRuntime },
): Promise<Backend | null> {
  try { await load(); } catch { return null; }
  const wails = scope.wails;
  if (!wails?.Call?.ByName) return null;
  const call: Call = (service, method, ...args) => wails.Call.ByName(`${services[service]}.${method}`, ...args);
  const status = await call('health', 'Status');
  return { status, call, events: wails.Events };
}

export const idempotencyKey = (): string => globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;

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
