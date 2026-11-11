// Nodes: machines that joined this server (docs/nodes.md), for the Nodes
// tab. The list is loaded when the tab opens and reloaded on node.changed;
// going online and offline and job starts and ends arrive as live notices
// (not stored), applied in place.
import { call, connected } from './backend.ts';
import { get, update, toast, errorText } from './store.ts';

export interface NodeFacts {
  version: string; protocol: number; hostname: string; os: string; arch: string;
  kernel?: string; distro?: string; model?: string; cpus: number; memoryMB?: number;
  root: string; landlock: number; sandboxed: boolean; devices?: string[]; hardware?: string[]; unsandboxed?: boolean;
  sandbox?: 'landlock' | 'bwrap' | 'off' | 'none'; sandboxNote?: string;
}
export interface NodeJob { id: string; command: string; dir?: string; started: number; ended?: number; running: boolean; exit: number; signal?: string; size: number; dropped: number }
export type NodeState = 'waiting' | 'online' | 'offline';
export interface NodeView {
  id: number; name: string; labels: string[]; state: NodeState; facts?: NodeFacts;
  createdAt: string; joinedAt?: string; lastSeen?: string; codeExpires?: string;
  grants: number[]; jobs: NodeJob[];
}
export interface NodeList { url: string; note?: string; nodes: NodeView[] }
export interface NodeNotice { id: number; state: NodeState; lastSeen?: string; facts?: NodeFacts; jobs: NodeJob[] }
export interface PiSetup { hostname: string; user: string; sshKey: string; wifiSsid: string; wifiPassword: string; country: string; usbConfig: boolean }
export interface NodeFile { name: string; content: string }
// A join code, shown once: the command to run, or the Pi's first-boot files
// and the command that flashes a card with them.
export interface NodeAdded { node: NodeView; code: string; expires: string; command: string; files?: NodeFile[]; warning?: string }

export async function refreshNodes() {
  if (!connected()) return;
  try {
    const list = await call<NodeList>('node', 'Nodes', get().active);
    update(s => { s.nodes = list; });
  } catch (error) { toast(`Could not load nodes: ${errorText(error)}`); }
}

// onNodeNotice applies a node's live state from the stream.
export function onNodeNotice(n: NodeNotice) {
  update(s => {
    const v = s.nodes?.nodes.find(node => node.id === n.id);
    if (!v) return;
    v.state = n.state;
    if (n.lastSeen) v.lastSeen = n.lastSeen;
    if (n.facts) v.facts = n.facts;
    v.jobs = n.jobs ?? [];
  });
}

// nextNodeName is the first free "Node N".
export function nextNodeName(nodes: NodeView[]): string {
  const taken = new Set(nodes.map(n => n.name));
  for (let i = 1; ; i++) if (!taken.has(`Node ${i}`)) return `Node ${i}`;
}

export const labelsOf = (text: string) => text.split(/[\s,]+/).map(l => l.trim().toLowerCase()).filter(Boolean);

async function act<T>(what: string, run: () => Promise<T>): Promise<T | null> {
  try { const out = await run(); void refreshNodes(); return out; }
  catch (error) { toast(`Could not ${what}: ${errorText(error)}`); return null; }
}

export const addNode = (name: string, labels: string[], pi?: PiSetup) =>
  act('add the node', () => call<NodeAdded>('node', 'Add', { workspaceId: get().active, name, labels, pi }));
export const newNodeCode = (nodeId: number, pi?: PiSetup) =>
  act('make a join code', () => call<NodeAdded>('node', 'NewCode', { workspaceId: get().active, nodeId, pi }));
export const updateNode = (nodeId: number, name: string, labels: string[]) =>
  act('save the node', () => call<NodeView>('node', 'Update', { workspaceId: get().active, nodeId, name, labels }));
export const removeNode = (nodeId: number) =>
  act('remove the node', () => call('node', 'Remove', { workspaceId: get().active, nodeId }));
export const grantNode = (nodeId: number, agentId: number, granted: boolean) =>
  act(granted ? 'grant the node' : 'revoke the node', () => call<NodeView>('node', 'Grant', { workspaceId: get().active, nodeId, agentId, granted }));
export const stopNodeJob = (nodeId: number, jobId: string) =>
  act('stop the job', () => call('node', 'StopJob', { workspaceId: get().active, nodeId, jobId }));
