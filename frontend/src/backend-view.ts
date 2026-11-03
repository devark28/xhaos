// Turns backend state (snapshot + events) into the card tree the UI renders.
// Pure functions over plain objects, so a framework port can reuse them.
import { modelLabel } from './models.ts';
import type { Agent, BackendAgent, BackendEvent, Delta, Entry, ModelChoice, Provider, Snapshot, Status, ToolData, Workspace } from './types.ts';

const statuses: Record<string, Status> = { idle: 'idle', running: 'running', attention: 'waiting', paused: 'paused', complete: 'complete', terminated: 'terminated' };

function node(agent: BackendAgent): Agent & { children: Agent[]; activity: Entry[] } {
  return {
    id: agent.id,
    parentId: agent.parentId || 0,
    title: agent.title,
    role: agent.role || '',
    status: statuses[agent.status] || 'idle',
    provider: agent.provider,
    model: agent.model,
    modelLabel: modelLabel(agent.provider, agent.model),
    policy: agent.policy,
    archivedAt: agent.status === 'terminated' ? agent.createdAt : undefined,
    activity: [],
    children: [],
  };
}

const toolVerbs: Record<string, string> = { list_dir: 'List', read_file: 'Read', search: 'Search', write_file: 'Write', edit_file: 'Edit', bash: 'Run' };
const stateNotes: Record<string, string> = { pending: 'waiting for you', allowed: 'allowed', denied: 'denied', done: '', failed: 'failed' };

// toolEntry renders one tool call's latest state. Pending calls show as an
// approval with the command or diff; the rest as a collapsible tool entry.
function toolEntry(data: ToolData, seq: number): Entry {
  const write = data.effect === 'write';
  const verb = toolVerbs[data.name] || data.name;
  if (data.state === 'pending') {
    const what = data.name === 'bash' ? 'Run this command?' : `${verb} ${data.summary}?`;
    return { kind: 'approval', callId: data.callId, state: data.state, text: what, command: write ? data.preview || '' : data.summary, language: write ? 'diff' : 'bash', output: data.reason ? `Asking because ${data.reason}.` : '', seq };
  }
  const note = data.state === 'denied' ? `denied${data.by === 'policy' ? ` by policy: ${data.reason}` : ''}` : stateNotes[data.state] || data.state;
  const output = data.state === 'denied' ? '' : data.output || '';
  return {
    kind: write && data.preview && data.state !== 'failed' ? 'diff' : 'tool',
    callId: data.callId, state: data.state,
    text: `${verb}${note ? ` · ${note}` : ''}`,
    command: data.summary || '',
    output: write && data.preview && data.state !== 'failed' ? data.preview : output,
    seq,
  };
}

function entry(event: BackendEvent): Entry | null {
  if (event.kind === 'agent.message') {
    const from = event.data?.from;
    if (from === 'tool') return null; // results are shown on each tool entry
    const text = event.data?.body ?? '';
    if (from === 'agent' && !text) return null; // a reply that only called tools
    const kind = from === 'owner' ? 'user' : from === 'system' ? 'system' : 'assistant';
    return { kind, text, seq: event.seq };
  }
  if (event.kind === 'agent.tool') return toolEntry(event.data || {}, event.seq);
  return null; // kinds this view doesn't render yet are kept in the log only
}

// addEntry appends an entry, or replaces the earlier entry for the same tool call.
function addEntry(agent: Agent, item: Entry) {
  agent.activity ||= [];
  if (item.callId) {
    const at = agent.activity.findIndex(old => old.callId === item.callId);
    if (at >= 0) { agent.activity[at] = item; return; }
  }
  agent.activity.push(item);
}

// pendingCalls lists the IDs of an agent's tool calls waiting for the owner.
export const pendingCalls = (agent: Agent): string[] => (agent.activity || []).filter(item => item.callId && item.state === 'pending').map(item => item.callId as string);

type Index = Map<number, Agent>;
function index(view: Workspace): Index {
  const byId: Index = new Map();
  const walk = (agents: Agent[]) => agents.forEach(agent => { byId.set(agent.id, agent); walk(agent.children || []); });
  walk(view.agents);
  return byId;
}

function place(view: Workspace, byId: Index, agent: Agent) {
  byId.set(agent.id, agent);
  const parent = agent.parentId && byId.get(agent.parentId);
  // Newest first at the top level, like the prototype; subagents in creation order.
  if (parent) (parent.children ||= []).push(agent); else view.agents.unshift(agent);
}

// viewFromSnapshot builds the workspace view from a Snapshot DTO.
export function viewFromSnapshot(snapshot: Snapshot): Workspace {
  const view: Workspace = { id: snapshot.workspace.id, name: snapshot.workspace.name, agents: [], channels: [], backend: true };
  const byId: Index = new Map();
  [...snapshot.agents].sort((a, b) => a.id - b.id).forEach(agent => place(view, byId, node(agent)));
  for (const event of snapshot.transcript || []) {
    const item = entry(event);
    const agent = item && byId.get(event.agentId);
    if (agent) addEntry(agent, item);
  }
  return view;
}

// applyEvent mutates view for one live event and reports what changed:
// {tree, created} for a new card, {tree} for a header/status change,
// {agentId} when one transcript changed, or null when the view is unaffected.
export interface Change { tree?: boolean; created?: boolean; agentId: number }
export function applyEvent(view: Workspace, event: BackendEvent): Change | null {
  const byId = index(view);
  if (event.kind === 'agent.created') {
    if (byId.has(event.data.id)) return null;
    place(view, byId, node(event.data));
    return { tree: true, created: true, agentId: event.data.id };
  }
  if (event.kind === 'agent.status') {
    const agent = byId.get(event.agentId);
    if (!agent) return null;
    agent.status = statuses[event.data?.status] || agent.status;
    if (agent.status === 'terminated') agent.archivedAt ||= event.at || new Date().toISOString();
    if (agent.status !== 'running') delete agent.live;
    return { tree: true, agentId: agent.id };
  }
  const item = entry(event);
  const agent = item && byId.get(event.agentId);
  if (!agent) return null;
  addEntry(agent, item);
  if (item.kind === 'assistant') delete agent.live; // the saved reply replaces the streamed draft
  return { agentId: agent.id };
}

// applyDelta appends live reply text (not persisted) and returns the agent ID.
export function applyDelta(view: Workspace, delta: Delta): number | null {
  const agent = index(view).get(delta.agentId);
  if (!agent || delta.workspaceId !== view.id) return null;
  agent.live = (agent.live || '') + delta.text;
  return agent.id;
}

// pickerChoices lists "provider/model" options from configured providers.
export const pickerChoices = (providers: Provider[]): ModelChoice[] => providers.filter(p => p.hasKey).flatMap(p =>
  p.models.map(model => ({ id: `${p.id}/${model}`, provider: p.id, model, label: providers.length > 1 ? `${modelLabel(p.kind, model)} · ${p.name}` : modelLabel(p.kind, model) })));
