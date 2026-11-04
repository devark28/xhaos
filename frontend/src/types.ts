// Shared shapes for the client. View types are what the UI renders; DTO types
// mirror the Go backend's JSON (app/agent, app/provider, app/tool, app/btw).

// ---- view ------------------------------------------------------------------
export type Status = 'idle' | 'running' | 'waiting' | 'paused' | 'complete' | 'terminated';
export type EntryKind = 'user' | 'assistant' | 'tool' | 'diff' | 'approval' | 'system';
export type ToolState = 'pending' | 'allowed' | 'denied' | 'done' | 'failed';

export interface Attachment {
  id: string;
  name: string;
  size: number;
  type: string;
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

export interface Agent {
  id: number;
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
  from: 'owner' | 'agent' | 'system' | 'tool';
  body: string;
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

export interface ToolInfo {
  name: string;
  description: string;
  effect: 'read' | 'write' | 'exec';
  source: string;
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
}
