// Shared shapes for the client. View types are what the UI renders; DTO types
// mirror the Go backend's JSON (app/agent, app/provider, app/tool, app/btw).

// ---- view ------------------------------------------------------------------
export type Status = 'idle' | 'running' | 'waiting' | 'paused' | 'complete' | 'terminated';
export type EntryKind = 'user' | 'assistant' | 'tool' | 'diff' | 'approval' | 'system' | 'channel';
export type ToolState = 'pending' | 'allowed' | 'denied' | 'done' | 'failed';

export interface Attachment {
  id: string;
  name: string;
  size: number;
  type: string;
  server?: boolean; // stored by the backend (else in this browser's IndexedDB)
}

// One transcript item. Tool entries carry the call they show (callId) and
// replace each other as the call's state changes.
export interface Entry {
  kind: EntryKind;
  text: string;
  callId?: string;
  state?: ToolState;
  command?: string;
  output?: string;
  language?: string;
  seq?: number;
  attachments?: Attachment[];
  questionBatchId?: string;
  ask?: boolean; // an ask_user call (answered, not allowed)
  stagedImage?: boolean; // a board_add_image waiting for approval: its card shows the image (board.Service.StagedImage)
  board?: { id: number; name: string; added: string }; // a finished board write: "added a note to" the board, linked (board.ts boardAdded)
}

export interface QuestionOption {
  id: string;
  label: string;
  description?: string;
}
export interface Question {
  id: string;
  title: string;
  prompt: string;
  options: QuestionOption[];
  required?: boolean;
  multiple?: boolean;
}
export interface Answer {
  choices: string[];
  text: string;
}
export interface QuestionBatch {
  id: string;
  status: 'pending' | 'answered' | 'cancelled';
  title: string;
  questions: Question[];
  answers?: Record<string, Answer>;
  answeredAt?: string;
  draft?: Record<string, Partial<Answer>>;
}

// Dangerously allow (package danger): until when, and the branch git is locked to.
export interface Danger { until: string; branch?: string; calls?: number } // calls: 0 or absent = the normal limit, -1 = none

export interface Agent {
  id: number;
  danger?: Danger;
  parentId?: number;
  projectId?: number;
  title: string;
  status: Status;
  role?: string;
  provider?: string;
  model?: string;
  modelId?: string;
  modelLabel?: string;
  agentType?: string;
  policy?: unknown;
  prompt?: string;
  time?: string;
  progress?: number;
  message?: string;
  activity?: Entry[];
  notes?: string[];
  children?: Agent[];
  live?: string; // reply text streaming in, not yet saved
  older?: number; // the server has older transcript events, before this seq
  archivedAt?: string;
  terminatedAt?: string;
  demo?: boolean;
  demoScenario?: string;
  sample?: boolean;
  questionBatch?: QuestionBatch;
}

export interface ChannelMessage {
  id: string;
  timestamp: string;
  writer: number | string;
  readers: (number | string)[];
  text: string;
  attachments?: Attachment[];
}
export interface Channel {
  id: string;
  subject: string;
  creatorId: number;
  participants: number[];
  messages: ChannelMessage[];
  count?: number; // messages in all; from the server only the last is sent until it is opened
  loaded?: boolean; // false: messages holds only the last one; open it to load the rest
  closed?: boolean; // the user closed it; nothing more can be sent
  published?: boolean; // on its link: readable and writable from outside with its passphrase
  expiresAt?: string; // when the link stops working; none: it never does. Past it, the channel stays published until published again or unpublished
}

// A channel as the backend sends it (channel.Service.Channels).
export interface BackendChannel {
  id: number;
  subject: string;
  kind?: string;
  creatorId: number;
  members: number[];
  createdAt: string;
  closed?: boolean;
  published?: boolean;
  expiresAt?: string;
  // senderId 0: sent from outside through the link, by senderName.
  count?: number; // messages in all (the list carries only the last)
  messages: { id: number; channelId: number; senderId: number; senderName?: string; body: string; createdAt: string }[];
}

export interface Workspace {
  id: string;
  name: string;
  initials?: string;
  channel?: string;
  agents: Agent[];
  channels?: Channel[];
  backend?: boolean;
}

export interface Memory {
  id: string;
  content: string;
  workspaceId: string;
  source?: string;
  createdAt?: string;
  updatedAt?: string;
}

// A Draw board, as the picker lists it (the scene loads when it is opened).
export interface BoardSummary {
  id: number;
  name: string;
  createdAt: string;
  updatedAt: string;
}

export interface ModelChoice {
  id: string;
  provider: string;
  model: string;
  label: string;
}

// ---- backend DTOs ----------------------------------------------------------
export interface BackendAgent {
  id: number;
  workspaceId: string;
  parentId?: number;
  projectId?: number;
  title: string;
  role?: string;
  status: string;
  provider?: string;
  model?: string;
  policy: unknown;
  createdAt: string;
  danger?: Danger;
}

export interface BackendEvent {
  workspaceId: string;
  seq: number;
  at?: string;
  agentId: number;
  kind: string;
  data: any; // shape depends on kind; see MessageData/ToolData/StatusData
}

export interface MessageData {
  from: 'owner' | 'agent' | 'system' | 'tool' | 'compaction';
  body: string;
  covers?: number; // compaction: how many earlier messages it summarizes
}

export interface ToolData {
  callId: string;
  name: string;
  effect: string;
  summary: string;
  preview?: string;
  commit?: boolean;
  state: ToolState;
  by?: string;
  reason?: string;
  output?: string;
}

export interface Snapshot {
  seq: number;
  workspace: { id: string; name: string };
  agents: BackendAgent[];
  transcript: BackendEvent[];
  older?: Record<string, number>; // agent ID -> seq before which it has older events
}

// TranscriptPage is agent.TranscriptBefore's answer: older events, in order.
export interface TranscriptPage {
  events: BackendEvent[];
  more: boolean;
  before?: number; // where the next page starts (its events can be fewer than read)
}

export interface Delta {
  workspaceId: string;
  agentId: number;
  text: string;
}

export interface Provider {
  id: string;
  kind: string;
  name: string;
  baseUrl?: string;
  models: string[];
  hasKey: boolean;
  keyHint?: string;
  contextTokens?: number; // 0 or absent: the kind's default
}

export interface Project {
  id: number;
  workspaceId: string;
  name: string;
  path: string; // a folder on the server
}
export interface FileEntry {
  name: string;
  path: string; // relative to the project
  dir?: boolean;
  size?: number;
}

// Token use and cost (usage.Service.Usage). Costs are USD; absent when the
// model has no price.
export interface UsageTokens { calls: number; input: number; cacheRead: number; cacheWrite: number; output: number; cost?: number }
export interface UsagePrice { model: string; input: number; output: number; cacheRead: number; cacheWrite: number; longAfter?: number; longInput?: number; longOutput?: number; source: 'built-in' | 'yours' }
export interface UsageReport {
  total: UsageTokens;
  unpriced: number;
  models: { provider: string; kind: string; model: string; price?: UsagePrice; tokens: UsageTokens; purposes: Record<string, number> }[];
  agents: { agentId: number; tokens: UsageTokens }[];
}

export interface ToolInfo {
  name: string;
  description: string;
  effect: 'read' | 'write' | 'exec' | 'ask' | 'contact';
  source: string; // built-in, channels, or memory
}
export interface McpTool {
  name: string;
  agentName: string;
  description: string;
  readOnly: boolean; // the server's own (untrusted) hint
}
export type McpTrust = 'ask' | 'auto' | 'read';
export interface McpServer {
  id: string;
  workspaceId: string;
  name: string;
  transport: 'stdio' | 'http';
  command?: string;
  args?: string[];
  url?: string;
  envKeys?: string[];
  headerKeys?: string[];
  trust: McpTrust;
  enabled: boolean;
  status: 'connected' | 'connecting' | 'error' | 'disabled';
  error?: string;
  tools: McpTool[];
}

export interface ToolCatalog {
  tools: ToolInfo[];
  // How agents' shell commands are confined (Landlock, package sandbox).
  sandbox?: { mode: 'auto' | 'on' | 'off'; active: boolean; abi: number; note: string };
}
