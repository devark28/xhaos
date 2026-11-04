// The Go backend connection and startup. The connection object lives here,
// not in the store: it holds the Wails runtime, which must not be frozen.
import { connectBackend, createSync, DELTA_EVENT_NAME, type Backend } from '../../server-client.ts';
import { viewFromSnapshot, applyEvent, applyDelta, applyChannelEvent, channelsFromBackend } from '../../backend-view.ts';
import { ensureQuestionDemo, hasPendingQuestions } from '../../questions.ts';
import { flatten } from '../../model.ts';
import { update, get, workspaceOf, toast, errorText, save } from './store.ts';
import { refreshProjects } from './projects.ts';
import type { Agent, BackendChannel, BackendEvent, Memory, Snapshot, Workspace } from '../../types.ts';

let backend: Backend | null = null;
let synced = false; // a snapshot after the first one (a resync) reloads memories
// call is the backend RPC; it throws when the page runs without the backend.
export function call<T = any>(service: Parameters<Backend['call']>[0], method: string, ...args: unknown[]): Promise<T> {
  if (!backend) return Promise.reject(new Error('Not connected to the Xhaos backend.'));
  return backend.call(service, method, ...args);
}
export const connected = () => backend !== null;

// Sample agents shown on the backend page are prototype data, renumbered
// above SAMPLE_ID_OFFSET so they never collide with real agents.
const SAMPLE_ID_OFFSET = 9000;
export const isSample = (id: number) => connected() && id >= SAMPLE_ID_OFFSET;
let prototypeWorkspaces: Workspace[] = [];
let samples: Agent[] | null = null;
function sampleAgents(): Agent[] {
  if (samples) return samples;
  const copy: Workspace[] = structuredClone(prototypeWorkspaces);
  const source = copy[0];
  if (!source) return [];
  ensureQuestionDemo(copy, source);
  const renumber = (agent: Agent): Agent => ({ ...agent, id: agent.id + SAMPLE_ID_OFFSET, sample: true, children: (agent.children || []).map(renumber) });
  samples = source.agents.map(renumber);
  return samples;
}

// applySamples adds or removes the sample agents per the showSamples setting.
export function applySamples() {
  update(s => {
    const view = workspaceOf(s);
    view.agents = view.agents.filter(agent => !agent.sample);
    if (s.showSamples) {
      const extra = structuredClone(sampleAgents());
      view.agents.push(...extra);
      flatten(extra).forEach(agent => s.collapsed.add(agent.id));
    }
    for (const id of [...s.selected]) if (!flatten(view.agents).some(agent => agent.id === id)) s.selected.delete(id);
  });
}

// refreshChannels loads the workspace's channels (live events keep them current).
export async function refreshChannels() {
  try {
    const list = await call<BackendChannel[]>('channel', 'Channels', get().active);
    update(s => { workspaceOf(s).channels = channelsFromBackend(list); });
  } catch { /* the Channels tab shows none */ }
}

// ---- memories ----------------------------------------------------------------
interface BackendMemory { id: number; workspaceId: string; content: string; source: string; createdAt: string; updatedAt: string }
const toMemory = (m: BackendMemory): Memory => ({ id: String(m.id), workspaceId: m.workspaceId, content: m.content, source: m.source, createdAt: m.createdAt, updatedAt: m.updatedAt });

// refreshMemories loads the workspace's memories from the server. Notes an
// older version kept in this browser are imported once, then dropped here,
// so the server is their only copy.
export async function refreshMemories() {
  const ws = get().active;
  try {
    const local = get().memories.filter(m => m.workspaceId === ws && !/^agent /.test(m.source || '')).map(m => m.content);
    const list = await call<BackendMemory[]>('memory', local.length ? 'Import' : 'Memories', ...(local.length ? [ws, local] : [ws]));
    update(s => { s.memories = list.map(toMemory); });
    if (local.length) { try { localStorage.removeItem('xhaos-memory-v1'); } catch { /* nothing to drop */ } toast(`Moved ${local.length} memor${local.length === 1 ? 'y' : 'ies'} from this browser to the server.`); }
  } catch (error) { toast(`Could not load memories: ${errorText(error)}`); }
}

// applyMemoryEvent applies memory.saved / memory.deleted; true if it did.
function applyMemoryEvent(s: { memories: Memory[] }, event: BackendEvent): boolean {
  if (event.kind === 'memory.saved') {
    const m = toMemory(event.data as BackendMemory);
    const at = s.memories.findIndex(x => x.id === m.id);
    if (at >= 0) s.memories[at] = m; else s.memories.unshift(m);
    return true;
  }
  if (event.kind === 'memory.deleted') {
    s.memories = s.memories.filter(x => x.id !== String(event.data?.id));
    return true;
  }
  return false;
}

export async function refreshProviders() {
  const providers = await call('provider', 'Providers');
  update(s => { s.providers = providers; });
}

function startPrototype() {
  update(s => {
    const questionDemo = ensureQuestionDemo(s.workspaces, workspaceOf(s));
    s.collapsed.add(questionDemo.id);
    if (hasPendingQuestions(questionDemo)) s.questionAgentId = questionDemo.id;
    s.ready = true;
  });
  save();
}

// Served by the Go backend: render its state and follow its events.
async function startBackend(connection: Backend) {
  backend = connection;
  prototypeWorkspaces = structuredClone(get().workspaces);
  const onSnapshot = (snapshot: Snapshot) => { update(s => {
    const view = viewFromSnapshot(snapshot);
    if (s.showSamples) view.agents.push(...structuredClone(sampleAgents()));
    s.workspaces = [view]; s.active = view.id; s.questionAgentId = null;
    s.collapsed = new Set(flatten(view.agents).map(agent => agent.id));
    for (const id of [...s.selected]) if (!flatten(view.agents).some(agent => agent.id === id)) s.selected.delete(id);
    s.backendVersion = connection.status.version;
    s.ready = true;
  }); void refreshChannels(); if (synced) void refreshMemories(); synced = true; };
  const onEvent = (event: BackendEvent) => update(s => {
    if (applyChannelEvent(workspaceOf(s), event) || applyMemoryEvent(s, event)) return;
    const change = applyEvent(workspaceOf(s), event);
    if (change?.created) s.collapsed.add(change.agentId);
  });
  const sync = createSync({ call: connection.call, events: connection.events, workspaceId: 'default', onSnapshot, onEvent, onError: () => toast('Lost sync with the backend; will retry.') });
  (globalThis as any).xhaos = { ...connection, sync }; // for console use
  try { update(s => { s.backendVersion = connection.status.version; }); await refreshProviders(); } catch { /* providers stay empty */ }
  try { await refreshProjects(); } catch { /* projects stay empty */ }
  // Live reply text: apply at most once per frame while replies stream.
  let pending: { workspaceId: string; agentId: number; text: string }[] = [];
  connection.events?.On?.(DELTA_EVENT_NAME, event => {
    if (!pending.length) requestAnimationFrame(() => {
      const batch = pending; pending = [];
      update(s => { batch.forEach(delta => applyDelta(workspaceOf(s), delta)); });
    });
    pending.push(event?.data ?? event);
  });
  await sync.start();
  await refreshChannels();
  await refreshMemories();
  const resync = () => { if (document.visibilityState === 'visible') sync.catchUp(); };
  document.addEventListener('visibilitychange', resync);
  addEventListener('online', resync);
  document.documentElement.dataset.backend = connection.status.version;
  toast(`Connected to Xhaos backend ${connection.status.version}.`);
}

// start connects to the backend when the page is served by it; otherwise
// (npm run dev) it runs the local prototype on sample data.
export function start() {
  connectBackend().then(connection => (connection ? startBackend(connection) : startPrototype())).catch(error => {
    // The runtime loaded but the backend failed: show that instead of mock data.
    update(s => { s.workspaces = [{ id: 'default', name: 'Default', agents: [], channels: [] }]; s.active = 'default'; s.ready = true; });
    toast(`Backend did not respond: ${errorText(error)}`);
  });
}
