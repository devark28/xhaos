// Views and panels: the composer's Message/Draw tab, which side panel is
// open, and the panels' own actions (memories, providers, tools, dictation).
import { update, get, focusLater, toast, errorText, saveMemories, pendingQuestions, type Layout, type Panel, type PanelKind } from './store.ts';
import { call, connected, refreshProviders, pushAppearance } from './backend.ts';
import { loadAppearance, saveAppearance } from '../appearance.ts';
import { focusComposer, setDraft } from './composer.ts';
import { refreshProjects } from './projects.ts';
import type { McpServer, McpTrust, Provider } from '../../types.ts';
import { newId } from '../../ids.ts';
import { isNarrow } from '../narrow.ts';

// Opening a panel shows it; on a phone that means switching to it.
export function openPanel(kind: PanelKind, args: Omit<Panel, 'kind'> = {}) { update(s => { s.panel = { kind, ...args }; if (isNarrow()) s.mobileView = 'panel'; }); }
// showAgentsView goes back to the agents (phones; a no-op elsewhere).
export function showAgentsView() { update(s => { s.mobileView = 'agents'; }); }
export function showMessageComposer() { update(s => { s.mode = 'message'; }); focusComposer(); }
export function showDraw() { update(s => { s.mode = 'draw'; }); }
// selectPanel shows a tab's panel in the side panel (which is always open).
// Settings also refreshes the MCP server count it shows.
export function selectPanel(kind: PanelKind) {
  if (kind === 'questions') showQuestions(get().questionAgentId);
  else if (kind === 'tools') showTools();
  else if (kind === 'files') showFiles();
  else if (kind === 'settings') { openPanel('settings'); if (connected()) refreshMcp(); }
  else if (kind === 'providers') showProviders();
  else openPanel(kind);
}
export function showQuestions(id: number | null) {
  const pending = pendingQuestions();
  update(s => {
    if (!pending.some(agent => agent.id === s.questionAgentId)) s.questionAgentId = id;
    s.panel = { kind: 'questions' };
    if (isNarrow()) s.mobileView = 'panel';
  });
  focusLater('[data-questions-agent]');
}
// setLayout changes the card columns and keeps the choice (Settings → Appearance → Columns).
export function setLayout(layout: Layout) {
  update(s => { s.layout = layout; });
  const a = { ...loadAppearance(), layout };
  saveAppearance(a); void pushAppearance(a);
}
export function clearSearch() { update(s => { s.search = ''; }); }

// ---- memories --------------------------------------------------------------
// closeChannel closes an agent channel: its members are told, and nothing
// more can be sent there.
export async function closeChannel(id: string) {
  try { await call('channel', 'Close', { workspaceId: get().active, channelId: Number(id) }); toast('Channel closed. Its agents can no longer send there.'); }
  catch (error) { toast(`Could not close the channel: ${errorText(error)}`); }
}

// publishChannel puts a channel on its link with a new passphrase (on a
// published channel: a reset). The passphrase comes back this once.
export async function publishChannel(id: string): Promise<{ passphrase: string; path: string } | null> {
  try { return await call('channel', 'Publish', { workspaceId: get().active, channelId: Number(id) }); }
  catch (error) { toast(`Could not publish the channel: ${errorText(error)}`); return null; }
}
export async function unpublishChannel(id: string) {
  try { await call('channel', 'Unpublish', { workspaceId: get().active, channelId: Number(id) }); toast('Unpublished. The passphrase no longer works.'); }
  catch (error) { toast(`Could not unpublish the channel: ${errorText(error)}`); }
}

export async function saveMemory(editId: string | null, content: string) {
  if (connected()) {
    try { await call('memory', 'Save', { workspaceId: get().active, id: editId ? Number(editId) : 0, content }); }
    catch (error) { toast(`Could not save the memory: ${errorText(error)}`); return; }
    openPanel('memories'); toast('Memory saved. Every agent in the workspace sees it from its next turn.');
    return;
  }
  update(s => {
    const editing = s.memories.find(memory => memory.id === editId);
    if (editing) Object.assign(editing, { content, workspaceId: s.active, updatedAt: new Date().toISOString() });
    else s.memories.unshift({ id: newId(), content, workspaceId: s.active, source: 'owner', createdAt: new Date().toISOString() });
  });
  saveMemories(); openPanel('memories'); toast('Workspace memory saved in this browser.');
}
export async function deleteMemory(id: string) {
  if (connected()) {
    try { await call('memory', 'Delete', get().active, Number(id)); }
    catch (error) { toast(`Could not delete the memory: ${errorText(error)}`); }
    openPanel('memories'); return;
  }
  update(s => { s.memories = s.memories.filter(memory => memory.id !== id); });
  saveMemories(); openPanel('memories');
}

// ---- files -------------------------------------------------------------------
// showFiles opens the Files panel with the project list fresh from the server.
export async function showFiles() {
  openPanel('files');
  try { await refreshProjects(); } catch (error) { toast(`Could not load projects: ${errorText(error)}`); }
}

// ---- tools and providers ---------------------------------------------------
// showTools opens Settings at Tools and MCP (open = false only loads them).
export async function showTools(open = true) {
  if (open) openPanel('tools');
  if (!connected()) return;
  try { const tools = await call('tool', 'Catalog'); update(s => { s.tools = tools; }); }
  catch (error) { update(s => { s.tools = { error: errorText(error) }; }); }
  await refreshMcp();
}

// ---- MCP servers ---------------------------------------------------------------
export async function refreshMcp() {
  try { const servers = await call<McpServer[]>('mcp', 'Servers', get().active); update(s => { s.mcpServers = servers; }); }
  catch (error) { toast(`Could not load MCP servers: ${errorText(error)}`); }
}
export interface McpForm {
  id: string; name: string; transport: 'stdio' | 'http'; command: string; url: string;
  env: string; headers: string; trust: McpTrust; enabled: boolean;
}
// Lines "KEY=value" (env) or "Name: value" (headers). Empty text keeps the
// saved values (null); they are never sent back to the page.
const pairs = (text: string, sep: RegExp): Record<string, string> | null => {
  const lines = text.split('\n').map(l => l.trim()).filter(Boolean);
  if (!lines.length) return null;
  return Object.fromEntries(lines.map(l => { const m = l.match(sep); return m ? [l.slice(0, m.index).trim(), l.slice(m.index! + m[0].length).trim()] : [l, '']; }));
};
// A command line split into words, honoring quotes.
export const words = (line: string): string[] => (line.match(/"[^"]*"|'[^']*'|\S+/g) || []).map(w => w.replace(/^(["'])(.*)\1$/, '$2'));
export async function saveMcp(form: McpForm): Promise<McpServer | null> {
  const [command, ...args] = words(form.command);
  try {
    const server = await call<McpServer>('mcp', 'Save', {
      workspaceId: get().active, id: form.id.trim(), name: form.name.trim(), transport: form.transport,
      command: command || '', args, url: form.url.trim(), env: pairs(form.env, /=/), headers: pairs(form.headers, /:\s*/),
      trust: form.trust, enabled: form.enabled,
    });
    await refreshMcp();
    toast(server.status === 'connected' ? `Connected to ${server.name}: ${server.tools.length} tools.` : `Saved ${server.name}; it did not connect: ${server.error || server.status}`);
    return server;
  } catch (error) { toast(`Could not save MCP server: ${errorText(error)}`); return null; }
}
// updateMcp changes trust or enabled without touching the saved secrets.
export async function updateMcp(server: McpServer, change: Partial<Pick<McpServer, 'trust' | 'enabled'>>) {
  const next = { ...server, ...change };
  try {
    await call('mcp', 'Save', { workspaceId: server.workspaceId, id: server.id, name: server.name, transport: server.transport, command: server.command || '', args: server.args || [], url: server.url || '', env: null, headers: null, trust: next.trust, enabled: next.enabled });
    await refreshMcp();
  } catch (error) { toast(`Could not update ${server.name}: ${errorText(error)}`); }
}
export async function reconnectMcp(server: McpServer) {
  try { await call('mcp', 'Reconnect', { workspaceId: server.workspaceId, id: server.id }); await refreshMcp(); }
  catch (error) { toast(errorText(error)); }
}
export async function deleteMcp(server: McpServer) {
  try { await call('mcp', 'Delete', { workspaceId: server.workspaceId, id: server.id }); await refreshMcp(); toast(`Removed ${server.name}.`); }
  catch (error) { toast(errorText(error)); }
}
export async function showProviders(message = '') {
  openPanel('providers', { message, loading: connected() });
  if (!connected()) return;
  // Always start from the server's current configs (another client may have changed them).
  try { await refreshProviders(); } catch (error) { message ||= `Could not load providers: ${errorText(error)}`; }
  if (get().panel.kind === 'providers') openPanel('providers', { message });
}
export interface ProviderForm { id: string; kind: string; name: string; baseUrl: string; apiKey: string; models: string; context: string }
// saveProvider creates or updates a provider. An empty key keeps the saved
// one; an empty model list keeps the saved list, else uses the kind's
// defaults, else asks the endpoint which models it offers.
export async function saveProvider(form: ProviderForm): Promise<boolean> {
  const existing: Provider | undefined = get().providers.find(p => p.id === form.id.trim());
  const models = form.models.split(/[\s,]+/).map(m => m.trim()).filter(Boolean);
  try {
    await call('provider', 'Save', {
      workspaceId: get().active,
      // Anthropic's form has no URL field: keep a saved custom endpoint.
      id: form.id.trim(), kind: form.kind, name: form.name.trim() || existing?.name || form.id.trim(),
      baseUrl: form.baseUrl.trim() || (form.kind === 'anthropic' ? existing?.baseUrl || '' : ''),
      models: models.length ? models : existing?.models || [], apiKey: form.apiKey,
      contextTokens: Number(form.context.replace(/[^0-9]/g, '')) || existing?.contextTokens || 0,
    });
    await refreshProviders(); showProviders('Saved. Use Test to check it.');
    return true;
  } catch (error) { showProviders(`Save failed: ${errorText(error)}`); return false; }
}
export async function testProvider(id: string) {
  openPanel('providers', { message: '', testing: id });
  try { const result = await call<{ message: string }>('provider', 'Test', get().active, id); showProviders(result.message); }
  catch (error) { showProviders(`Test failed: ${errorText(error)}`); }
}
export async function removeProvider(id: string) {
  try { await call('provider', 'Delete', get().active, id); await refreshProviders(); showProviders('Removed.'); }
  catch (error) { showProviders(`Remove failed: ${errorText(error)}`); }
}

// ---- dictation -------------------------------------------------------------
// The Web Speech API is not in TypeScript's DOM types yet.
interface Recognition { lang: string; interimResults: boolean; start(): void; onresult: (e: any) => void; onerror: (e: any) => void; onend: () => void }
const speechApi = (): (new () => Recognition) | undefined => (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition;
export function dictate() {
  if (!speechApi()) { toast('Dictation is unavailable in this browser. You can type a command.'); return; }
  openPanel('dictation');
}
export function startDictation() {
  const Speech = speechApi();
  if (!Speech) return;
  const recognition = new Speech();
  recognition.lang = 'en-US'; recognition.interimResults = false;
  recognition.onresult = e => { setDraft(e.results[0][0].transcript); focusComposer(); toast('Transcript ready. Review it and press send.'); };
  recognition.onerror = e => toast(`Dictation: ${e.error}`);
  recognition.onend = () => update(s => { s.recording = false; });
  showQuestions(get().questionAgentId);
  try { recognition.start(); update(s => { s.recording = true; }); toast('Listening…'); } catch { toast('Could not start microphone.'); }
}
