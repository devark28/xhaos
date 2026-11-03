// Views and panels: the composer's Message/Draw tab, which side panel is
// open, and the panels' own actions (memories, providers, tools, dictation).
import { update, get, focusLater, toast, errorText, saveMemories, pendingQuestions, type Layout, type Panel, type PanelKind } from './store.ts';
import { call, connected, refreshProviders } from './backend.ts';
import { focusComposer, setDraft } from './composer.ts';
import { refreshProjects } from './projects.ts';
import type { Provider } from '../../types.ts';

export function openPanel(kind: PanelKind, args: Omit<Panel, 'kind'> = {}) { update(s => { s.panel = { kind, ...args }; }); }
export function showMessageComposer() { update(s => { s.mode = 'message'; }); focusComposer(); }
export function showDraw() { update(s => { s.mode = 'draw'; }); }
// selectPanel shows a tab's panel in the side panel (which is always open).
export function selectPanel(kind: PanelKind) {
  if (kind === 'questions') showQuestions(get().questionAgentId);
  else if (kind === 'tools') showTools();
  else if (kind === 'files') showFiles();
  else if (kind === 'providers') showProviders();
  else openPanel(kind);
}
export function showQuestions(id: number | null) {
  const pending = pendingQuestions();
  update(s => {
    if (!pending.some(agent => agent.id === s.questionAgentId)) s.questionAgentId = id;
    s.panel = { kind: 'questions' };
  });
  focusLater('[data-questions-agent]');
}
export function setLayout(layout: Layout) { update(s => { s.layout = layout; }); }
export function clearSearch() { update(s => { s.search = ''; }); }

// ---- memories --------------------------------------------------------------
export function saveMemory(editId: string | null, content: string) {
  update(s => {
    const editing = s.memories.find(memory => memory.id === editId);
    if (editing) Object.assign(editing, { content, workspaceId: s.active, updatedAt: new Date().toISOString() });
    else s.memories.unshift({ id: crypto.randomUUID(), content, workspaceId: s.active, source: 'owner', createdAt: new Date().toISOString() });
  });
  saveMemories(); openPanel('memories'); toast('Workspace memory saved in this browser.');
}
export function deleteMemory(id: string) {
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
export async function showTools() {
  openPanel('tools');
  if (!connected()) return;
  try { const tools = await call('tool', 'Catalog'); update(s => { s.tools = tools; }); }
  catch (error) { update(s => { s.tools = { error: errorText(error) }; }); }
}
export async function showProviders(message = '') {
  openPanel('providers', { message, loading: connected() });
  if (!connected()) return;
  // Always start from the server's current configs (another client may have changed them).
  try { await refreshProviders(); } catch (error) { message ||= `Could not load providers: ${errorText(error)}`; }
  if (get().panel.kind === 'providers') openPanel('providers', { message });
}
export interface ProviderForm { id: string; kind: string; name: string; baseUrl: string; apiKey: string; models: string }
// saveProvider creates or updates a provider. An empty key keeps the saved
// one; an empty model list keeps the saved list, else uses the kind's
// defaults, else asks the endpoint which models it offers.
export async function saveProvider(form: ProviderForm): Promise<boolean> {
  const existing: Provider | undefined = get().providers.find(p => p.id === form.id.trim());
  const models = form.models.split(/[\s,]+/).map(m => m.trim()).filter(Boolean);
  try {
    await call('provider', 'Save', {
      // Anthropic's form has no URL field: keep a saved custom endpoint.
      id: form.id.trim(), kind: form.kind, name: form.name.trim() || existing?.name || form.id.trim(),
      baseUrl: form.baseUrl.trim() || (form.kind === 'anthropic' ? existing?.baseUrl || '' : ''),
      models: models.length ? models : existing?.models || [], apiKey: form.apiKey,
    });
    await refreshProviders(); showProviders('Saved. Use Test to check it.');
    return true;
  } catch (error) { showProviders(`Save failed: ${errorText(error)}`); return false; }
}
export async function testProvider(id: string) {
  openPanel('providers', { message: '', testing: id });
  try { const result = await call<{ message: string }>('provider', 'Test', id); showProviders(result.message); }
  catch (error) { showProviders(`Test failed: ${errorText(error)}`); }
}
export async function removeProvider(id: string) {
  try { await call('provider', 'Delete', id); await refreshProviders(); showProviders('Removed.'); }
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
