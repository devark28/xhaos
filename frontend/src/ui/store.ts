// Application state and actions. Components read S and re-render on bump();
// pure helpers (model, questions, backend-view, ...) stay in their modules.
import { answerQuestions, hasPendingQuestions, ensureQuestionDemo } from '../questions.ts';
import { applyAgentCommand } from '../agent-actions.ts';
import { storeFiles } from '../attachments.ts';
import { createAttentionTracker, agentPath } from '../attention.ts';
import { hydrateDemoActivity } from '../demo-activity.ts';
import { connectBackend, createSync, idempotencyKey, DELTA_EVENT_NAME } from '../server-client.ts';
import { modelChoices, findModel, loadModelChoice, saveModelChoice } from '../models.ts';
import { resolveSubmit, takeMention } from '../composer-target.ts';
import { viewFromSnapshot, applyEvent, applyDelta, pickerChoices, pendingCalls } from '../backend-view.ts';
import { initialWorkspaces, flatten, transition, activeAgents, restoreAgent, terminateAgent as markTerminated } from '../model.ts';
import type { Backend } from '../server-client.ts';
import type { XhaosInput } from '../rich-input.ts';
import type { Agent, Answer, Attachment, BackendEvent, Memory, Provider, Snapshot, ToolCatalog, Workspace } from '../types.ts';

const load = <T,>(key: string, fallback: () => T): T => { try { return JSON.parse(localStorage.getItem(key) || 'null') || fallback(); } catch { return fallback(); } };
const SAMPLE_ID_OFFSET = 9000;

export type Layout = 'grid' | 'three' | 'list';
export type Mode = 'message' | 'questions' | 'draw';
export type PanelKind = 'memories' | 'channels' | 'providers' | 'history' | 'status' | 'help' | 'settings' | 'tools' | 'layout' | 'dictation';
export interface Panel { kind: PanelKind; editId?: string; channelId?: string; message?: string; testing?: string; loading?: boolean }
export type Btw = { question: string; pending?: boolean; answer?: string; error?: string };
export type SlashCommand = [name: string, description: string, usage: string];

interface State {
  workspaces: Workspace[];
  memories: Memory[];
  active: string;
  search: string;
  layout: Layout;
  collapsed: Set<number>;
  draft: string;
  backend: Backend | null; // set when served by the Go backend
  showSamples: boolean;
  prototypeWorkspaces: Workspace[] | null;
  samples: Agent[] | null;
  providers: Provider[];
  composerModelId: string;
  pendingFiles: File[];
  selected: Set<number>;
  sending: boolean;
  mode: Mode;
  questionAgentId: number | null;
  knownQuestionBatches: Set<string>;
  questionSteps: Map<string, number>;
  panel: Panel | null; // opens inside the composer section
  menu: SlashCommand[]; // slash command matches
  tools: (ToolCatalog & { error?: undefined }) | { error: string } | null; // loaded when the tools panel opens
  btw: Btw | null; // the latest side question
  ready: boolean;
  recording: boolean; // dictation is listening
}

export const S: State = {
  workspaces: load('xhaos-v1', initialWorkspaces),
  memories: load('xhaos-memory-v1', () => []),
  active: '',
  search: '',
  layout: 'grid', // 'grid' (2), 'three', 'list'
  collapsed: new Set<number>(),
  draft: '',
  backend: null, // set when served by the Go backend
  showSamples: (() => { try { return localStorage.getItem('xhaos-samples') !== 'off'; } catch { return true; } })(),
  prototypeWorkspaces: null,
  samples: null,
  providers: [],
  composerModelId: loadModelChoice(),
  pendingFiles: [],
  selected: new Set<number>(),
  sending: false,
  mode: 'message', // 'message' | 'questions' | 'draw'
  questionAgentId: null,
  knownQuestionBatches: new Set<string>(),
  questionSteps: new Map<string, number>(),
  panel: null, // {kind, ...args}; opens inside the composer section
  menu: [], // slash command matches
  tools: null, // the backend's tool catalog, loaded when the tools panel opens
  btw: null, // {question, pending, answer?, error?}: the latest side question
  ready: false,
  recording: false,
};
hydrateDemoActivity(S.workspaces);
S.active = S.workspaces[0].id;
S.collapsed = new Set(S.workspaces.flatMap(w => flatten(w.agents)).map(a => a.id));

// ---- render plumbing -------------------------------------------------------
let version = 0;
const listeners = new Set<() => void>();
const afterQueue: (() => void)[] = [];
export const subscribe = (fn: () => void) => { listeners.add(fn); return () => listeners.delete(fn); };
export const getVersion = () => version;
export function bump() { normalize(); version++; listeners.forEach(fn => fn()); }
// afterCommit runs fn once the DOM reflects the current state (focus, scroll).
export function afterCommit(fn: () => void) { afterQueue.push(fn); bump(); }
export function flushAfterCommit() { afterQueue.splice(0).forEach(fn => fn()); }
const focusLater = (selector: string) => afterCommit(() => document.querySelector<HTMLElement>(selector)?.focus({ preventScroll: true }));

// Derived bookkeeping that the old render() did before painting.
function normalize() {
  if (!S.ready) return;
  const pending = pendingQuestions();
  const incoming = pending.filter(agent => !S.knownQuestionBatches.has(questionStepKey(agent)));
  pending.forEach(agent => S.knownQuestionBatches.add(questionStepKey(agent)));
  // New questions never take over the composer; the pulsing dot and the Questions tab announce them.
  if (incoming.length && !pending.some(agent => agent.id === S.questionAgentId)) S.questionAgentId = incoming[incoming.length - 1].id;
  if (!pending.length && S.mode === 'questions') S.mode = 'message';
}

// ---- basics ----------------------------------------------------------------
export const workspace = (): Workspace => S.workspaces.find(w => w.id === S.active) || S.workspaces[0];
export const isSample = (id: number) => Boolean(S.backend) && id >= SAMPLE_ID_OFFSET;
let toastTimer: ReturnType<typeof setTimeout> | undefined;
export function toast(message: string) {
  const el = document.getElementById('toast');
  if (!el) return;
  el.textContent = message; el.classList.add('visible');
  clearTimeout(toastTimer); toastTimer = setTimeout(() => el.classList.remove('visible'), 4500);
}
const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error));
const notWired = (what: string) => toast(`${what} isn't connected to the backend yet.`);
export function save() {
  if (S.backend) return;
  try { localStorage.setItem('xhaos-v1', JSON.stringify(S.workspaces)); } catch { toast('Browser storage unavailable; changes last for this session.'); }
}
export function saveMemories() {
  try { localStorage.setItem('xhaos-memory-v1', JSON.stringify(S.memories)); } catch { toast('Browser memory storage unavailable.'); }
}
export const pendingQuestions = (): Agent[] => activeAgents(workspace().agents).filter(hasPendingQuestions);
export const findAgent = (id: number): Agent | undefined => flatten(workspace().agents).find(agent => agent.id === id);
const commandInput = () => document.getElementById('command') as XhaosInput | null;

// ---- composer --------------------------------------------------------------
export const modelOptions = () => (S.backend ? pickerChoices(S.providers) : modelChoices);
export function currentChoice() {
  const options = modelOptions();
  return options.find(choice => choice.id === S.composerModelId) || options[0] || null;
}
export function setModel(id: string) {
  if (id === '__providers') { showProviders(); return; }
  S.composerModelId = id; saveModelChoice(id); bump();
}
export function clearComposer() {
  S.draft = ''; S.menu = [];
  const input = commandInput(); if (input) input.value = '';
  bump();
}
export function focusComposer() { focusLater('#command'); }

export const slashCommands: SlashCommand[] = [
  ['new', 'New agent', '/new Describe the task'],
  ['tools', 'Tools agents can call, and MCP servers', '/tools'],
  ['layout', 'Choose columns: 1, 2, or 3', '/layout 3'],
  ['terminate', 'Terminate an agent and its subagents', '/terminate [agent ID]'],
  ['history', 'Terminated agents and retained history', '/history'],
  ['restore', 'Restore a removed agent', '/restore 11'],
  ['channels', 'Agent communication channels', '/channels'],
  ['memory', 'View and edit saved memories', '/memory'],
  ['samples', 'Show or hide sample agents on the backend page', '/samples off'],
  ['providers', 'Model providers and API keys', '/providers'],
  ['search', 'Find agents; no query clears search', '/search title or ID'],
  ['allow', 'Allow pending requests for attached agents', '/allow [agent ID]'],
  ['deny', 'Deny pending requests for attached agents', '/deny [agent ID]'],
  ['pause', 'Pause attached agents or one ID', '/pause [agent ID]'],
  ['resume', 'Resume attached agents or one ID', '/resume [agent ID]'],
  ['btw', 'Ask about your agents without messaging them', '/btw what is #12 doing?'],
  ['help', 'Show command reference', '/help'],
];
export function updateCommandMenu() {
  let value = commandInput()?.value || '';
  const mention = takeMention(value, id => activeAgents(workspace().agents).some(a => a.id === id && a.status !== 'terminated'));
  if (mention) {
    if (S.mode !== 'message') S.mode = 'message';
    S.selected.add(mention.id);
    S.draft = value = mention.text; commandInput()!.value = value;
    bump();
  }
  const next = !value.startsWith('/') || /\s/.test(value) ? [] : slashCommands.filter(([name]) => name.startsWith(value.slice(1).toLowerCase()));
  if (next.length === S.menu.length && next.every((c, i) => c === S.menu[i])) return;
  S.menu = next; bump();
}
export function chooseCommand(name: string) {
  S.draft = `/${name} `; const input = commandInput(); if (input) input.value = S.draft;
  S.menu = []; bump(); input?.focus();
}
export function closeCommandMenu() { if (S.menu.length) { S.menu = []; bump(); } }

// ---- views and panels ------------------------------------------------------
export function openPanel(kind: PanelKind, args: Omit<Panel, 'kind'> = {}) { S.panel = { kind, ...args }; bump(); }
export function closePanel() { if (S.panel) { S.panel = null; bump(); } }
export function showMessageComposer() { S.panel = null; S.mode = 'message'; focusComposer(); }
export function showDraw() { S.panel = null; S.mode = 'draw'; bump(); }
export function showQuestions(id: number | null) {
  if (!pendingQuestions().some(agent => agent.id === S.questionAgentId)) S.questionAgentId = id;
  S.panel = null; S.mode = 'questions'; focusLater('[data-questions-agent]');
}
export function setLayout(layout: Layout) { S.layout = layout; bump(); }
export function clearSearch() { S.search = ''; bump(); }
export function toggleCollapseAll() {
  const agents = activeAgents(workspace().agents);
  const close = !agents.every(a => S.collapsed.has(a.id));
  agents.forEach(a => (close ? S.collapsed.add(a.id) : S.collapsed.delete(a.id)));
  bump();
}

// ---- agents ----------------------------------------------------------------
export function toggleAgent(id: number) { S.collapsed.has(id) ? S.collapsed.delete(id) : S.collapsed.add(id); bump(); }
export function toggleRecipient(id: number) {
  if (S.mode !== 'message') S.mode = 'message';
  S.selected.has(id) ? S.selected.delete(id) : S.selected.add(id);
  focusComposer();
}
export function detachRecipient(id: number) { S.selected.delete(id); bump(); }
export function revealAgent(id: number) {
  agentPath(workspace().agents, id).slice(0, -1).forEach(parentId => S.collapsed.delete(parentId));
  S.collapsed.has(id) ? S.collapsed.delete(id) : S.collapsed.add(id);
  focusLater(`[data-collapse="${id}"]`);
}
// decide answers one waiting tool call (callId), or every waiting call of
// the agent when callId is omitted.
export async function decide(id: number, action: 'approve' | 'reject', callId?: string) {
  if (S.backend && !isSample(id)) {
    const agent = findAgent(id);
    const calls = callId ? [callId] : agent ? pendingCalls(agent) : [];
    if (!calls.length) { toast(`#${id} has nothing waiting for a decision.`); return; }
    for (const call of calls) {
      try { await S.backend.call('agent', 'Decide', { idempotencyKey: idempotencyKey(), workspaceId: S.active, agentId: id, callId: call, allow: action === 'approve' }); }
      catch (error) { toast(errorText(error)); return; }
    }
    return;
  }
  const agent = findAgent(id);
  if (agent) transition(agent, action);
  save(); bump();
}
// stopAgent cancels a backend agent's running turn (and any running command).
export async function stopAgent(id: number) {
  if (!S.backend) return;
  try { await S.backend.call('agent', 'Stop', { idempotencyKey: idempotencyKey(), workspaceId: S.active, agentId: id }); toast(`Stopped #${id}. Send it a message to continue.`); }
  catch (error) { toast(errorText(error)); }
}
export async function terminateAgent(id: number) {
  if (S.backend && !isSample(id)) {
    try { await S.backend.call('agent', 'Terminate', { idempotencyKey: idempotencyKey(), workspaceId: S.active, agentId: id }); }
    catch (error) { toast(errorText(error)); return; }
    S.selected.delete(id);
    bump(); toast(`Agent #${id} terminated. View retained history with /history.`);
    return;
  }
  markTerminated(workspace().agents, id);
  S.selected.delete(id);
  for (const target of [...S.selected]) if (!activeAgents(workspace().agents).some(agent => agent.id === target)) S.selected.delete(target);
  save(); bump(); toast(`Agent #${id} terminated. View retained history with /history.`);
}
export function restore(id: number) { restoreAgent(workspace().agents, id); save(); bump(); }

// Ctrl/Cmd+Alt+N and bare /new: detach recipients so Enter creates a new agent.
export function focusNewAgent() { S.panel = null; S.selected.clear(); S.mode = 'message'; focusComposer(); }

async function newAgent(prompt: string, attachments: Attachment[] = [], parentId = 0): Promise<boolean> {
  const choice = currentChoice();
  if (S.backend && !isSample(parentId)) {
    if (!choice) { toast('Add a model provider first.'); showProviders(); return false; }
    if (attachments.length) { toast('Attachments are not sent to the backend yet; remove them to create the agent.'); return false; }
    try {
      const agent = await S.backend.call('agent', 'Create', { idempotencyKey: idempotencyKey(), workspaceId: S.active, parentId, prompt, provider: choice.provider, model: choice.model });
      clearComposer(); toast(`Agent #${agent.id} created${parentId ? ` under #${parentId}` : ''}.`); return true;
    } catch (error) { toast(`Could not create agent: ${errorText(error)}`); return false; }
  }
  const local = choice || findModel(S.composerModelId);
  const parent = parentId && findAgent(parentId);
  if (parentId && !parent) { toast(`Agent #${parentId} not found.`); return false; }
  const id = Math.max(0, ...S.workspaces.flatMap(w => flatten(w.agents)).map(a => a.id)) + 1;
  const title = prompt.split('\n')[0].slice(0, 120);
  const agent: Agent = { id, title, prompt, status: 'paused', role: parent ? 'Subagent' : 'New task', provider: local.provider, agentType: 'xhaos', modelId: local.model, model: local.label, modelLabel: local.label, time: '0m', progress: 0, message: 'Task queued locally. Connect a backend to execute.', activity: [{ kind: 'user', text: prompt, attachments }, { kind: 'system', text: 'Task queued locally. No backend is connected.' }], children: [] };
  if (parent) (parent.children ||= []).push(agent); else workspace().agents.unshift(agent);
  S.collapsed.add(id); S.search = ''; save(); clearComposer(); toast(`Agent #${id} created in the prototype.`); return true;
}

async function sendMessage(agentIds: number[], text: string, attachments: Attachment[] = []) {
  const real = agentIds.filter(id => S.backend && !isSample(id));
  if (real.length) {
    if (attachments.length) { toast('Attachments are not sent to the backend yet; remove them to send.'); return; }
    try { await S.backend!.call('agent', 'Message', { idempotencyKey: idempotencyKey(), workspaceId: S.active, agentIds: real, body: text }); }
    catch (error) { toast(`Could not send: ${errorText(error)}`); return; }
    agentIds = agentIds.filter(id => !real.includes(id));
    if (!agentIds.length) { clearComposer(); toast(`Sent to ${real.map(id => '#' + id).join(', ')}.`); return; }
  }
  const agents = activeAgents(workspace().agents).filter(agent => agentIds.includes(agent.id));
  if (!agents.length) { toast('That agent is not active in this workspace.'); return; }
  agents.forEach(agent => (agent.activity ||= [{ kind: 'assistant', text: agent.message || '' }]).push({ kind: 'user', text, attachments }));
  save(); clearComposer(); toast('Message saved locally. No backend is connected.');
}

function setSending(value: boolean) { S.sending = value; bump(); }
export async function submitCommand(value: string) {
  if (S.sending) return;
  value = value.trim();
  if (!S.pendingFiles.length) { await dispatchCommand(value); return; }
  if (value.startsWith('/') && !/^\/(new|tell)\s+\S/.test(value)) { toast('Use attachments with a new task or /tell agent message.'); return; }
  const tell = value.match(/^(?:\/tell|tell\s+(?:agent\s+)?)\s*#?(\d+)\s+([\s\S]+)$/i);
  if ((value.startsWith('/tell') || (!S.selected.size && /^tell\s/i.test(value))) && (!tell || !findAgent(Number(tell[1])))) { toast('Specify a valid agent and message before sending attachments.'); return; }
  setSending(true);
  const files = [...S.pendingFiles];
  try {
    const attachments = await storeFiles(files);
    S.pendingFiles.splice(0, files.length);
    await dispatchCommand(value || 'Review attached files', attachments);
  } catch (error) { toast(`Attachment save failed: ${errorText(error)}`); }
  finally { setSending(false); }
}
export function addFiles(files: File[]) { S.pendingFiles.push(...files); bump(); }
export function removeFile(index: number) { S.pendingFiles.splice(index, 1); bump(); }

async function dispatchCommand(value: string, attachments: Attachment[] = []): Promise<void> {
  const intent = resolveSubmit(value, [...S.selected]);
  if (intent.kind === 'none') return;
  if (intent.kind === 'error') { toast(intent.message); return; }
  if (intent.kind === 'focus') { focusNewAgent(); return; }
  if (intent.kind === 'command') { runSlashCommand(intent.text, attachments); return; }
  if (intent.kind === 'message') { await sendMessage(intent.agentIds, intent.body, attachments); return; }
  // Open the parent first so the new subagent is visible when its event renders.
  if (intent.parentId && S.collapsed.delete(intent.parentId)) bump();
  await newAgent(intent.prompt, attachments, intent.parentId);
}

function runSlashCommand(value: string, attachments: Attachment[] = []) {
  const [, name = '', args = ''] = value.match(/^\/(\w+)(?:\s+([\s\S]*))?$/) || [];
  const done = () => { clearComposer(); };
  if (name === 'help') { done(); openPanel(args.trim().toLowerCase() === 'status' ? 'status' : 'help'); return; }
  if (name === 'tools' || name === 'mcp') { done(); showTools(); return; }
  if (name === 'layout') {
    const count = Number(args.trim());
    if (![1, 2, 3].includes(count)) { openPanel('layout'); return; }
    S.layout = count === 1 ? 'list' : count === 3 ? 'three' : 'grid'; done(); return;
  }
  if (name === 'history') { done(); openPanel('history'); return; }
  if (name === 'terminate') {
    const explicit = args.trim();
    if (explicit && !/^#?\d+$/.test(explicit)) { toast('Use /terminate, or /terminate followed by one agent ID.'); return; }
    const candidates = activeAgents(workspace().agents).filter(agent => (explicit ? agent.id === Number(explicit.replace('#', '')) : S.selected.has(agent.id)));
    if (candidates.length !== 1) { toast('Attach one active agent, or specify its ID.'); return; }
    done(); terminateAgent(candidates[0].id); return;
  }
  if ((name === 'remove' || name === 'restore') && S.backend && !isSample(Number(args.trim().replace('#', '')))) { notWired(`/${name}`); return; }
  if (name === 'remove' || name === 'restore') {
    if (!/^#?\d+$/.test(args.trim())) { toast(`Use /${name} followed by an agent ID.`); return; }
    const id = Number(args.trim().replace('#', ''));
    if (!findAgent(id)) { toast('Agent not found.'); return; }
    done(); if (name === 'remove') terminateAgent(id); else restore(id); return;
  }
  if (name === 'btw') { askBtw(args.trim()); return; }
  if (name === 'channels') { done(); openPanel('channels'); return; }
  if (name === 'memory') { done(); openPanel('memories'); return; }
  if (name === 'providers') { done(); showProviders(); return; }
  if (name === 'samples') {
    const value = args.trim().toLowerCase();
    if (!['on', 'off', ''].includes(value)) { toast('Use /samples on or /samples off.'); return; }
    S.showSamples = value ? value === 'on' : !S.showSamples;
    try { localStorage.setItem('xhaos-samples', S.showSamples ? 'on' : 'off'); } catch { /* per-browser only */ }
    done(); if (S.backend) applySamples();
    toast(S.backend ? `Sample agents ${S.showSamples ? 'shown' : 'hidden'}.` : 'Samples are the prototype data here; this setting applies on the backend page.');
    return;
  }
  if (name === 'new') { done(); focusNewAgent(); return; }
  if (name === 'search') { S.search = args.trim(); S.panel = null; done(); return; }
  if (name === 'tell') {
    if (!/^#?\d+\s+\S/.test(args)) { toast('Use /tell 11 your message'); return; }
    dispatchCommand('tell agent ' + args.replace(/^#/, ''), attachments); return;
  }
  if (['allow', 'deny', 'pause', 'resume', 'approve', 'decline'].includes(name)) {
    const explicitId = Number(args.trim().replace('#', ''));
    const targets = explicitId ? [explicitId] : [...S.selected];
    if (S.backend && targets.some(id => !isSample(id))) {
      if (!targets.length) { toast(`Attach an agent or use /${name} 12.`); return; }
      done();
      if (name === 'resume') { toast('Send the agent a message to continue it.'); return; }
      for (const id of targets) {
        if (name === 'pause') stopAgent(id);
        else decide(id, ['allow', 'approve'].includes(name) ? 'approve' : 'reject');
      }
      return;
    }
    try {
      const result = applyAgentCommand(workspace().agents, S.selected, name, args);
      save(); done();
      toast(`Applied to ${result.applied.map(id => '#' + id).join(', ')}${result.skipped.length ? `; skipped ${result.skipped.length} ineligible` : ''}.`);
    } catch (error) { toast(errorText(error)); }
    return;
  }
  toast('Unknown command. Type / to see commands.');
}

// ---- side questions ------------------------------------------------------
// /btw asks a model about the workspace from a read-only digest of the agents.
// The answer shows above the composer only; no agent or transcript changes.
export async function askBtw(question: string) {
  if (!question) { toast('Ask a question after /btw, e.g. /btw what is #12 doing?'); return; }
  if (!S.backend) { toast('/btw needs the Go backend.'); return; }
  const choice = currentChoice();
  if (!choice) { toast('Add a model provider first.'); showProviders(); return; }
  const ask: Btw = { question, pending: true };
  S.btw = ask; clearComposer(); bump();
  let next: Btw;
  try {
    const answer = await S.backend.call('btw', 'Ask', { workspaceId: S.active, question, provider: choice.provider, model: choice.model });
    next = { question, answer: answer.text || '(no answer)' };
  } catch (error) { next = { question, error: errorText(error) }; }
  if (S.btw === ask) { S.btw = next; bump(); }
}
export function closeBtw() { if (S.btw) { S.btw = null; bump(); } }

// ---- questions -------------------------------------------------------------
export const questionStepKey = (agent: Agent) => `${agent.id}:${agent.questionBatch?.id}`;
export const questionStep = (agent: Agent) => Math.min(S.questionSteps.get(questionStepKey(agent)) || 0, agent.questionBatch?.questions.length || 0);
export function setQuestionStep(agent: Agent, step: number) {
  S.questionSteps.set(questionStepKey(agent), Math.max(0, Math.min(agent.questionBatch?.questions.length || 0, step)));
  focusLater('[data-questions-agent]');
}
export function setAnswer(agent: Agent, questionId: string, answer: Partial<Answer>) {
  const batch = agent.questionBatch;
  if (!batch) return;
  batch.draft = { ...(batch.draft || {}), [questionId]: answer };
  save(); bump();
}
export function advanceOrSubmit(agent: Agent) {
  const batch = agent.questionBatch;
  if (!batch) return;
  const key = questionStepKey(agent), step = S.questionSteps.get(key) || 0;
  if (step < batch.questions.length) { setQuestionStep(agent, step + 1); return; }
  try {
    answerQuestions(agent, batch.id, batch.draft || {});
    save();
    if (pendingQuestions().length) focusLater('[data-questions-agent]'); else focusComposer();
    toast(`Answers saved for agent #${agent.id}.`);
  } catch (error) { toast(errorText(error)); }
}

// ---- memories --------------------------------------------------------------
export function saveMemory(editId: string | null, content: string) {
  const editing = S.memories.find(memory => memory.id === editId);
  if (editing) Object.assign(editing, { content, workspaceId: S.active, updatedAt: new Date().toISOString() });
  else S.memories.unshift({ id: crypto.randomUUID(), content, workspaceId: S.active, source: 'owner', createdAt: new Date().toISOString() });
  saveMemories(); openPanel('memories'); toast('Workspace memory saved in this browser.');
}
export function deleteMemory(id: string) { S.memories = S.memories.filter(memory => memory.id !== id); saveMemories(); openPanel('memories'); }

// ---- providers -------------------------------------------------------------
export async function showTools() {
  openPanel('tools');
  if (!S.backend) return;
  try { S.tools = await S.backend.call('tool', 'Catalog'); }
  catch (error) { S.tools = { error: errorText(error) }; }
  bump();
}
export async function refreshProviders() { if (!S.backend) return; S.providers = await S.backend.call('provider', 'Providers'); bump(); }
export async function showProviders(message = '') {
  openPanel('providers', { message, loading: Boolean(S.backend) });
  if (!S.backend) return;
  // Always start from the server's current configs (another client may have changed them).
  try { await refreshProviders(); } catch (error) { message ||= `Could not load providers: ${errorText(error)}`; }
  if (S.panel?.kind === 'providers') openPanel('providers', { message });
}
export async function saveProviderKey(apiKey: string) {
  if (!S.backend) return;
  const anthropic = S.providers.find(p => p.id === 'anthropic');
  // Re-saving keeps the provider's other settings; only the key changes.
  try {
    await S.backend.call('provider', 'Save', { id: 'anthropic', kind: 'anthropic', name: anthropic?.name || 'Anthropic', baseUrl: anthropic?.baseUrl || '', models: anthropic?.models || [], apiKey });
    await refreshProviders(); showProviders('Saved. Use Test to check the key.');
  } catch (error) { showProviders(`Save failed: ${errorText(error)}`); }
}
export async function testProvider(id: string) {
  if (!S.backend) return;
  openPanel('providers', { message: '', testing: id });
  try { const result = await S.backend.call('provider', 'Test', id); showProviders(result.message); }
  catch (error) { showProviders(`Test failed: ${errorText(error)}`); }
}
export async function removeProvider(id: string) {
  if (!S.backend) return;
  try { await S.backend.call('provider', 'Delete', id); await refreshProviders(); showProviders('Removed.'); }
  catch (error) { showProviders(`Remove failed: ${errorText(error)}`); }
}

// ---- dictation -------------------------------------------------------------
// The Web Speech API is not in TypeScript's DOM types yet.
interface Recognition { lang: string; interimResults: boolean; start(): void; onresult: (e: any) => void; onerror: (e: any) => void; onend: () => void }
const speechApi = (): (new () => Recognition) | undefined => (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition;
export function dictate() {
  const Speech = speechApi();
  if (!Speech) { toast('Dictation is unavailable in this browser. You can type a command.'); return; }
  openPanel('dictation');
}
export function startDictation() {
  const Speech = speechApi();
  if (!Speech) return;
  const recognition = new Speech();
  recognition.lang = 'en-US'; recognition.interimResults = false;
  recognition.onresult = e => { S.draft = e.results[0][0].transcript; const input = commandInput(); if (input) { input.value = S.draft; input.focus(); } toast('Transcript ready. Review it and press send.'); };
  recognition.onerror = e => toast(`Dictation: ${e.error}`);
  recognition.onend = () => { S.recording = false; bump(); };
  closePanel();
  try { recognition.start(); S.recording = true; bump(); toast('Listening…'); } catch { toast('Could not start microphone.'); }
}

// ---- attention -------------------------------------------------------------
const newlyWaitingAgents = createAttentionTracker();
// Scrolls a newly waiting agent (or its nearest visible ancestor) into view once.
export function notifyAttention() {
  const ids = newlyWaitingAgents(S.active, workspace().agents).filter(id => { const agent = findAgent(id); return !agent || !hasPendingQuestions(agent); });
  if (!ids.length || S.search || !globalThis.requestAnimationFrame) return;
  const path = agentPath(workspace().agents, ids[0]);
  requestAnimationFrame(() => {
    const target = path.reverse().map(id => document.querySelector(`[data-agent-row-id="${id}"]`)).find(Boolean);
    if (!target || !target.isConnected) return;
    const scroller = document.getElementById('agents');
    if (!scroller) return;
    const viewport = scroller.getBoundingClientRect();
    const rect = target.getBoundingClientRect();
    if (rect.top >= viewport.top + 8 && rect.bottom <= viewport.bottom - 16) return;
    const top = scroller.scrollTop + rect.top - viewport.top - (scroller.clientHeight - rect.height) / 2;
    scroller.scrollTo({ top: Math.max(0, top), behavior: globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
  });
}

// ---- samples and startup ---------------------------------------------------
function sampleAgents(): Agent[] {
  if (S.samples) return S.samples;
  const prototype = S.prototypeWorkspaces || [];
  const source = prototype[0];
  if (!source) return [];
  ensureQuestionDemo(prototype, source);
  const renumber = (agent: Agent): Agent => ({ ...agent, id: agent.id + SAMPLE_ID_OFFSET, sample: true, children: (agent.children || []).map(renumber) });
  S.samples = source.agents.map(renumber);
  return S.samples;
}
function applySamples() {
  const view = workspace();
  view.agents = view.agents.filter(agent => !agent.sample);
  if (S.showSamples) { const samples = sampleAgents(); view.agents.push(...samples); flatten(samples).forEach(agent => S.collapsed.add(agent.id)); }
  for (const id of [...S.selected]) if (!flatten(view.agents).some(agent => agent.id === id)) S.selected.delete(id);
  bump();
}
function startPrototype() {
  const questionDemo = ensureQuestionDemo(S.workspaces, workspace());
  S.collapsed.add(questionDemo.id);
  if (hasPendingQuestions(questionDemo)) S.questionAgentId = questionDemo.id;
  S.ready = true; save(); bump();
}
// Served by the Go backend: render its state and follow its events. Otherwise
// (npm run dev) run the local prototype. window.xhaos is for console use.
async function startBackend(connected: Backend) {
  const backend = connected;
  S.backend = backend;
  S.prototypeWorkspaces = S.workspaces;
  const onSnapshot = (snapshot: Snapshot) => {
    const view = viewFromSnapshot(snapshot);
    if (S.showSamples) view.agents.push(...sampleAgents());
    S.workspaces = [view]; S.active = view.id; S.questionAgentId = null;
    S.collapsed = new Set(flatten(view.agents).map(agent => agent.id));
    for (const id of [...S.selected]) if (!flatten(view.agents).some(agent => agent.id === id)) S.selected.delete(id);
    S.ready = true; bump();
  };
  const onEvent = (event: BackendEvent) => {
    const change = applyEvent(workspace(), event);
    if (!change) return;
    if (change.created) S.collapsed.add(change.agentId);
    bump();
  };
  const sync = createSync({ call: backend.call, events: backend.events, workspaceId: 'default', onSnapshot, onEvent, onError: () => toast('Lost sync with the backend; will retry.') });
  (globalThis as any).xhaos = { ...backend, sync }; // for console use
  try { S.providers = await backend.call('provider', 'Providers'); } catch { S.providers = []; }
  // Live reply text: repaint at most once per frame while replies stream.
  let painting = false;
  backend.events?.On?.(DELTA_EVENT_NAME, event => {
    if (applyDelta(workspace(), event?.data ?? event) == null || painting) return;
    painting = true;
    requestAnimationFrame(() => { painting = false; bump(); });
  });
  await sync.start();
  const resync = () => { if (document.visibilityState === 'visible') sync.catchUp(); };
  document.addEventListener('visibilitychange', resync); addEventListener('online', resync);
  document.documentElement.dataset.backend = backend.status.version;
  toast(`Connected to Xhaos backend ${backend.status.version}.`);
}
export function start() {
  connectBackend().then(connected => (connected ? startBackend(connected) : startPrototype())).catch(error => {
    // The runtime loaded but the backend failed: show that instead of mock data.
    S.workspaces = [{ id: 'default', name: 'Default', agents: [], channels: [] }]; S.active = 'default'; S.ready = true; bump();
    toast(`Backend did not respond: ${errorText(error)}`);
  });
}
