// The Go backend connection and startup. The connection object lives here,
// not in the store: it holds the Wails runtime, which must not be frozen.
import { BOARD_EVENT_NAME, connectBackend, createSync, DELTA_EVENT_NAME, type Backend } from '../../server-client.ts';
import { onBoardEvent, onBoardNotice, refreshBoards, resyncOpenBoard } from './boards.ts';
import { viewFromSnapshot, applyEvent, applyDelta, applyChannelEvent, channelsFromBackend, prependOlder, setChannelMessages } from '../../backend-view.ts';
import { ensureQuestionDemo, hasPendingQuestions } from '../../questions.ts';
import { flatten } from '../../model.ts';
import { update, get, workspaceOf, findAgentIn, toast, errorText, save } from './store.ts';
import { refreshProjects } from './projects.ts';
import { installHashNavigation } from './location.ts';
import { restoreDraft } from './composer.ts';
import { applyAppearance, defaults as appearanceDefaults, loadAppearance, saveAppearance, type Appearance } from '../appearance.ts';
import type { Agent, BackendChannel, BackendEvent, Memory, Snapshot, TranscriptPage, Workspace, UsageReport } from '../../types.ts';

let backend: Backend | null = null;
let syncing: { catchUp(): Promise<void> } | null = null;
const saved = new Map<number, number>(); // agent -> replies saved (see the delta batching)
// catchUp reads events the live stream may not have delivered yet (after an
// action, so its result shows even when the stream lags).
export const catchUp = () => syncing?.catchUp() ?? Promise.resolve();
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

// loadChannel fetches an opened channel's messages (the list has only its last).
const loadingChannels = new Set<string>();
export async function loadChannel(id: string) {
  if (!backend || loadingChannels.has(id)) return;
  loadingChannels.add(id);
  try {
    const list = await call<BackendChannel['messages']>('channel', 'Messages', get().active, Number(id));
    update(s => { setChannelMessages(workspaceOf(s), id, list); });
  } catch (error) {
    toast(`Couldn't load channel #${id}: ${errorText(error)}`);
  } finally {
    loadingChannels.delete(id);
  }
}

// ---- appearance, kept on the server ---------------------------------------------
// The browser's copy paints the first frame; the server's is the one that
// counts, so every browser (and address) that signs in looks the same.
export async function pushAppearance(a: Appearance) {
  if (!connected()) return;
  try { await call('prefs', 'Set', 'appearance', a); } catch { /* kept in this browser */ }
}
async function pullAppearance() {
  try {
    const stored = await call<Partial<Appearance> | null>('prefs', 'Get', 'appearance');
    if (stored && typeof stored === 'object') {
      const a = { ...appearanceDefaults, ...stored };
      saveAppearance(a); applyAppearance(a);
      const samples = get().showSamples && a.developer === 'on';
      update(s => { s.layout = a.layout; s.showSamples = samples; });
      if (!samples) applySamples();
    } else {
      const local = loadAppearance();
      if (JSON.stringify(local) !== JSON.stringify(appearanceDefaults)) await pushAppearance(local); // first sync: this browser's choice
    }
  } catch { /* keep this browser's */ }
}

// ---- usage ---------------------------------------------------------------------
// refreshUsage loads token use and cost; replies refresh it (at most every 2s).
export async function refreshUsage() {
  try { const report = await call<UsageReport>('usage', 'Usage', get().active); update(s => { s.usage = report; }); } catch { /* keep the last one */ }
}
let usageTimer: ReturnType<typeof setTimeout> | null = null;
function usageSoon() {
  if (usageTimer) return;
  usageTimer = setTimeout(() => { usageTimer = null; void refreshUsage(); }, 2000);
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

// loadOlder fetches the page of an agent's transcript before what it shows
// (the snapshot sends only the recent end), once at a time per agent.
const loadingOlder = new Set<number>();
export async function loadOlder(agentId: number) {
  const before = findAgentIn(get(), agentId)?.older;
  if (!backend || !before || loadingOlder.has(agentId)) return;
  loadingOlder.add(agentId);
  try {
    const page: TranscriptPage = await call('agent', 'TranscriptBefore', get().active, agentId, before, 50);
    update(s => { prependOlder(workspaceOf(s), agentId, page); });
  } catch (error) {
    toast(`Couldn't load earlier messages: ${errorText(error)}`);
  } finally {
    loadingOlder.delete(agentId);
  }
}

export async function refreshProviders() {
  const providers = await call('provider', 'Providers', get().active);
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
  installHashNavigation();
  restoreDraft();
}

// Served by the Go backend: render its state and follow its events.
async function startBackend(connection: Backend, workspaceId: string) {
  backend = connection;
  update(s => { s.active = workspaceId; });
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
  const onEvent = (event: BackendEvent) => {
    if (onBoardEvent(event)) return; // boards keep their own state (state/boards.ts)
    if (event.kind === 'agent.message' && event.data?.from !== 'owner') usageSoon();
    if (event.kind === 'agent.message' && event.data?.from === 'agent') saved.set(event.agentId, (saved.get(event.agentId) || 0) + 1);
    update(s => {
    if (event.kind === 'secret.changed') { s.secretsVersion++; return; }
    if (event.kind === 'skill.changed') { s.skillsVersion++; return; }
    if (applyChannelEvent(workspaceOf(s), event) || applyMemoryEvent(s, event)) return;
    const change = applyEvent(workspaceOf(s), event);
    if (change?.created) s.collapsed.add(change.agentId);
  }); };
  const sync = createSync({ call: connection.call, events: connection.events, workspaceId, onSnapshot, onEvent, onError: () => toast('Lost sync with the backend; will retry.') });
  (globalThis as any).xhaos = { ...connection, sync }; // for console use
  syncing = sync;
  try { update(s => { s.backendVersion = connection.status.version; }); await refreshProviders(); } catch { /* providers stay empty */ }
  // Live board saves from other pages (not stored; see board.Notice).
  connection.events?.On?.(BOARD_EVENT_NAME, event => onBoardNotice(event?.data ?? event));
  // Live reply text: apply at most once per frame while replies stream.
  // Each streamed piece belongs to the reply being written when it arrived;
  // once that reply is saved, pieces still waiting for the next frame are
  // dropped, or they would bring back the streamed draft beside it.
  let pending: { delta: { workspaceId: string; agentId: number; text: string }; reply: number }[] = [];
  connection.events?.On?.(DELTA_EVENT_NAME, event => {
    if (!pending.length) requestAnimationFrame(() => {
      const batch = pending; pending = [];
      update(s => { batch.forEach(({ delta, reply }) => { if ((saved.get(delta.agentId) || 0) === reply) applyDelta(workspaceOf(s), delta); }); });
    });
    const delta = event?.data ?? event;
    pending.push({ delta, reply: saved.get(delta.agentId) || 0 });
  });
  await sync.start();
  await pullAppearance();
  installHashNavigation(); // the agents are loaded: #agents= links find their cards
  // After the snapshot: until then the active workspace is the prototype's.
  try { await refreshProjects(); } catch { /* projects stay empty */ }
  await refreshBoards(); // the Boards panel lists them whether or not Draw was opened
  // Ask once whether dictation can run here, so the mic shows its state at once.
  void connection.call('dictation', 'Enabled').then(on => update(s => { s.dictationEnabled = on === true; }), () => update(s => { s.dictationEnabled = false; }));
  restoreDraft(); // after agents and projects, so its attachments find them
  // The stream reconnects by itself; catch up on what it missed meanwhile,
  // and if it dropped because the session ended, show the sign-in screen.
  connection.events?.onOpen?.(() => { void sync.catchUp(); resyncOpenBoard(); });
  connection.events?.onLost?.(() => { void fetch('/xhaos/auth').then(r => r.json()).then(a => { if (!a.signedIn) location.reload(); }).catch(() => {}); });
  await refreshChannels();
  await refreshMemories();
  void refreshUsage();
  const resync = () => { if (document.visibilityState === 'visible') { sync.catchUp(); resyncOpenBoard(); } };
  document.addEventListener('visibilitychange', resync);
  addEventListener('online', resync);
  document.documentElement.dataset.backend = connection.status.version;
  toast(`Connected to Xhaos backend ${connection.status.version}.`);
}

// start connects to the backend when the page is served by it; otherwise
// (npm run dev) it runs the local prototype on sample data.
// A page served by the Xhaos server (served) never falls back to the
// prototype: it retries the connection, then says what failed.
// start connects to the server for the signed-in workspace (the session's:
// the server refuses calls about any other).
export async function start(served = false, workspaceId = 'default', workspaceName = '') {
  update(s => { s.workspaceName = workspaceName; });
  let lastError: unknown = null;
  for (let attempt = 0; attempt < (served ? 4 : 1); attempt++) {
    if (attempt) await new Promise(r => setTimeout(r, 1500 * attempt));
    try {
      const connection = await connectBackend(undefined, undefined, undefined, served);
      if (connection) return startBackend(connection, workspaceId);
      if (!served) return startPrototype();
    } catch (error) { lastError = error; console.error('xhaos: connect', error); }
  }
  // Show the failure instead of mock data, so nothing is "queued locally".
  update(s => { s.workspaces = [{ id: workspaceId, name: workspaceName || 'Default', agents: [], channels: [] }]; s.active = workspaceId; s.ready = true; });
  toast(`Could not connect to the Xhaos server: ${errorText(lastError)}. Reload the page to try again.`);
}
