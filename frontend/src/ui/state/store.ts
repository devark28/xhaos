// The application state, in one zustand store. Components subscribe to the
// slices they render (useApp(selector)), so a change re-renders only what
// reads it. Changes go through update(), an immer recipe: write it as plain
// mutation, and immer produces the next immutable state. Actions live in the
// feature modules beside this file (composer, agents, panels, ...).
import { create } from 'zustand';
import { immer } from 'zustand/middleware/immer';
import { enableMapSet } from 'immer';
import { hasPendingQuestions } from '../../questions.ts';
import { activeAgents, flatten, initialWorkspaces } from '../../model.ts';
import { loadModelChoice } from '../../models.ts';
import { hydrateDemoActivity } from '../../demo-activity.ts';
import type { Agent, FileEntry, McpServer, Memory, Project, Provider, ToolCatalog, Workspace, UsageReport } from '../../types.ts';

enableMapSet();

export type Layout = 'grid' | 'three' | 'list';
export type Mode = 'message' | 'draw';
export type PanelKind = 'questions' | 'files' | 'memories' | 'channels' | 'providers' | 'history' | 'status' | 'help' | 'settings' | 'tools' | 'layout' | 'btw';
export interface Panel { kind: PanelKind; section?: string; editId?: string; channelId?: string; message?: string; testing?: string; loading?: boolean }
export type Btw = { question: string; pending?: boolean; answer?: string; error?: string };
export type SlashCommand = [name: string, description: string, usage: string];
export type Tools = (ToolCatalog & { error?: undefined }) | { error: string } | null;

export interface State {
  ready: boolean;
  backendVersion: string | null; // set when served by the Go backend
  workspaces: Workspace[];
  active: string;
  memories: Memory[];
  providers: Provider[];
  tools: Tools; // loaded when the tools panel opens
  mcpServers: McpServer[];
  usage: UsageReport | null; // token use and cost, from the backend
  secretsVersion: number; // bumped by secret.changed, so the Secrets section reloads
  skillsVersion: number; // bumped by skill.changed
  dangerAgent: number | null; // the agent the dangerously-allow dialog is open for
  // On phones one view shows at a time: the agents, or the open panel.
  mobileView: 'agents' | 'panel';
  workspaceName: string; // the signed-in workspace, shown in the header
  projects: Project[];
  attachedProject: number | null; // project attached to the composer: where new agents work
  files: Record<string, FileEntry[] | { error: string }>; // "projectId:dir" -> listing
  openDirs: Set<string>; // expanded "projectId:dir" keys in the Files panel

  layout: Layout;
  search: string;
  collapsed: Set<number>;
  focusAgent: number | null; // the card opened last (#agent= in the link)
  showSamples: boolean;

  mode: Mode;
  panel: Panel; // what the side panel shows; it is always open
  selected: Set<number>; // agents attached to the composer
  composerModelId: string;
  pendingFiles: File[];
  menu: SlashCommand[]; // slash command matches
  sending: boolean;
  recording: boolean; // dictation is listening
  recordingSince: number; // when the take started (ms since the epoch), 0 when idle
  transcribing: boolean; // dictation is sending the take to the sidecar
  transcribeEstimate: number; // seconds, as shown while transcribing
  btw: Btw | null; // the latest side question
  toast: { message: string; id: number } | null;

  questionAgentId: number | null;
  knownQuestionBatches: Set<string>;
  questionSteps: Map<string, number>;

  commits: number; // bumped by afterCommit so App re-renders and runs the queue
}

const load = <T,>(key: string, fallback: () => T): T => {
  try { return JSON.parse(localStorage.getItem(key) || 'null') || fallback(); } catch { return fallback(); }
};

function initial(): State {
  const workspaces = hydrateDemoActivity(load('xhaos-v1', initialWorkspaces));
  return {
    ready: false,
    backendVersion: null,
    workspaces,
    active: workspaces[0].id,
    memories: load<Memory[]>('xhaos-memory-v1', () => []),
    providers: [],
    tools: null,
    mcpServers: [],
    usage: null,
    secretsVersion: 0,
    skillsVersion: 0,
    dangerAgent: null,
    mobileView: 'agents',
    workspaceName: '',
    projects: [],
    attachedProject: null,
    files: {},
    openDirs: new Set(),
    layout: (() => { try { return (JSON.parse(localStorage.getItem('xhaos-appearance') || '{}').layout as Layout) || 'grid'; } catch { return 'grid' as Layout; } })(),
    search: '',
    collapsed: new Set(workspaces.flatMap(w => flatten(w.agents)).map(a => a.id)),
    focusAgent: null,
    // Sample agents: a Developer mode tool (backend page only), off unless turned on there.
    showSamples: (() => { try { return localStorage.getItem('xhaos-samples') === 'on' && JSON.parse(localStorage.getItem('xhaos-appearance') || '{}').developer === 'on'; } catch { return false; } })(),
    mode: 'message',
    panel: { kind: 'questions' },
    selected: new Set(),
    composerModelId: loadModelChoice(),
    pendingFiles: [],
    menu: [],
    sending: false,
    recording: false,
    recordingSince: 0,
    transcribing: false,
    transcribeEstimate: 0,
    btw: null,
    toast: null,
    questionAgentId: null,
    knownQuestionBatches: new Set(),
    questionSteps: new Map(),
    commits: 0,
  };
}

export const useApp = create<State>()(immer(() => initial()));

// get returns the current (frozen) state; read it in actions, not render.
export const get = () => useApp.getState();

// update applies a recipe that mutates a draft, then the derived bookkeeping.
export function update(recipe: (draft: State) => void) {
  useApp.setState(draft => { recipe(draft); normalize(draft); });
}

// ---- derived state ---------------------------------------------------------
export const workspaceOf = (s: Pick<State, 'workspaces' | 'active'>): Workspace => s.workspaces.find(w => w.id === s.active) || s.workspaces[0];
export const workspace = () => workspaceOf(get());
export const findAgentIn = (s: State, id: number): Agent | undefined => flatten(workspaceOf(s).agents).find(agent => agent.id === id);
export const findAgent = (id: number) => findAgentIn(get(), id);
export const pendingQuestionsIn = (s: State): Agent[] => activeAgents(workspaceOf(s).agents).filter(hasPendingQuestions);
export const pendingQuestions = () => pendingQuestionsIn(get());
export const questionStepKey = (agent: Agent) => `${agent.id}:${agent.questionBatch?.id}`;

// New questions never take over the screen; the pulsing dot on the
// Questions tab announces them.
function normalize(s: State) {
  if (!s.ready) return;
  const pending = pendingQuestionsIn(s);
  const incoming = pending.filter(agent => !s.knownQuestionBatches.has(questionStepKey(agent)));
  pending.forEach(agent => s.knownQuestionBatches.add(questionStepKey(agent)));
  if (incoming.length && !pending.some(agent => agent.id === s.questionAgentId)) s.questionAgentId = incoming[incoming.length - 1].id;
}

// ---- after-render work -----------------------------------------------------
// afterCommit runs fn once the DOM reflects the current state (focus, scroll):
// it bumps `commits`, App re-renders, and its layout effect flushes the queue.
const afterQueue: (() => void)[] = [];
export function afterCommit(fn: () => void) { afterQueue.push(fn); update(s => { s.commits++; }); }
export function flushAfterCommit() { afterQueue.splice(0).forEach(fn => fn()); }
export const focusLater = (selector: string) => afterCommit(() => document.querySelector<HTMLElement>(selector)?.focus({ preventScroll: true }));

// ---- toast -----------------------------------------------------------------
let toastId = 0;
export function toast(message: string) { update(s => { s.toast = { message, id: ++toastId }; }); }
export const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error));

// ---- persistence (prototype mode only) -------------------------------------
export function save() {
  const s = get();
  if (s.backendVersion) return;
  try { localStorage.setItem('xhaos-v1', JSON.stringify(s.workspaces)); } catch { toast('Browser storage unavailable; changes last for this session.'); }
}
export function saveMemories() {
  if (get().backendVersion) return; // the server keeps them
  try { localStorage.setItem('xhaos-memory-v1', JSON.stringify(get().memories)); } catch { toast('Browser memory storage unavailable.'); }
}
