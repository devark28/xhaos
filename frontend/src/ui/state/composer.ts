// The composer: its text, model choice, attachments, slash commands, and what
// submitting does. The text lives in the <xhaos-input> element itself (it
// owns its content); `draft` mirrors it so it survives remounts.
import { resolveSubmit, takeMention } from '../../composer-target.ts';
import { loadAppearance } from '../appearance.ts';
import { pickerChoices } from '../../backend-view.ts';
import { modelChoices, saveModelChoice } from '../../models.ts';
import { activeAgents } from '../../model.ts';
import { storeFiles, uploadFiles } from '../../attachments.ts';
import { applyAgentCommand } from '../../agent-actions.ts';
import { update, get, workspaceOf, findAgent, focusLater, toast, errorText, save, type Btw, type Panel, type SlashCommand } from './store.ts';
import { call, connected, isSample, applySamples } from './backend.ts';
import { newAgent, sendMessage, focusNewAgent, terminateAgent, restore, stopAgent, decide } from './agents.ts';
import { openPanel, showProviders, showTools } from './panels.ts';
import type { XhaosInput } from '../../rich-input.ts';
import type { Attachment, ModelChoice } from '../../types.ts';

// ---- the input element -----------------------------------------------------
let input: XhaosInput | null = null;
let draft = '';
export function registerInput(element: XhaosInput | null) { input = element; if (input) input.value = draft; }
export const getDraft = () => draft;
export function setDraft(value: string) { draft = value; if (input && input.value !== value) input.value = value; keepDraft(); }

// The unsent draft (text, attached agents and project) survives a reload, a
// closed tab or a server restart: it is kept in this browser as you type and
// cleared when sent. (Files waiting to be attached are not kept.)
const DRAFT_KEY = 'xhaos-draft';
let keepTimer: ReturnType<typeof setTimeout> | null = null;
function writeDraft() {
  try {
    const s = get();
    if (!draft.trim()) localStorage.removeItem(DRAFT_KEY);
    else localStorage.setItem(DRAFT_KEY, JSON.stringify({ text: draft, agents: [...s.selected], project: s.attachedProject, at: Date.now() }));
  } catch { /* storage unavailable: the draft lives in this page only */ }
}
function keepDraft() {
  if (keepTimer) clearTimeout(keepTimer);
  keepTimer = setTimeout(() => { keepTimer = null; writeDraft(); }, 250);
}
addEventListener('pagehide', () => { if (keepTimer) { clearTimeout(keepTimer); keepTimer = null; writeDraft(); } });

// restoreDraft brings back a kept draft, once the agents are loaded (only
// agents and projects that still exist are reattached).
export function restoreDraft() {
  let kept: { text?: string; agents?: number[]; project?: number | null } | null = null;
  try { kept = JSON.parse(localStorage.getItem(DRAFT_KEY) || 'null'); } catch { return; }
  if (!kept?.text || draft.trim()) return;
  const known = new Set(activeAgents(workspaceOf(get()).agents).filter(a => a.status !== 'terminated').map(a => a.id));
  update(s => {
    for (const id of kept!.agents || []) if (known.has(id)) s.selected.add(id);
    if (kept!.project && s.projects.some(p => p.id === kept!.project)) s.attachedProject = kept!.project;
  });
  setDraft(kept.text);
}
export function focusComposer() { focusLater('#command'); }
export function clearComposer() {
  setDraft('');
  if (get().menu.length) update(s => { s.menu = []; });
}

// ---- model -----------------------------------------------------------------
export const modelOptions = (): ModelChoice[] => (connected() ? pickerChoices(get().providers) : modelChoices);
export function currentChoice(): ModelChoice | null {
  const options = modelOptions();
  return options.find(choice => choice.id === get().composerModelId) || options[0] || null;
}
export function setModel(id: string) {
  if (id === '__providers') { showProviders(); return; }
  update(s => { s.composerModelId = id; });
  saveModelChoice(id);
}

// ---- attachments -----------------------------------------------------------
export function addFiles(files: File[]) { update(s => { s.pendingFiles.push(...files); }); }
export function removeFile(index: number) { update(s => { s.pendingFiles.splice(index, 1); }); }

// ---- slash commands --------------------------------------------------------
export const developerMode = () => loadAppearance().developer === 'on';
// visibleCommands leaves out developer commands unless Developer mode is on.
export const visibleCommands = () => slashCommands.filter(([name]) => name !== 'samples' || developerMode());
// setSamples shows or hides the sample agents (a Developer mode tool).
export function setSamples(show: boolean) {
  update(s => { s.showSamples = show; });
  try { localStorage.setItem('xhaos-samples', show ? 'on' : 'off'); } catch { /* per-browser only */ }
  if (connected()) applySamples();
  toast(connected() ? `Sample agents ${show ? 'shown' : 'hidden'}.` : 'Samples are the prototype data here; this setting applies on the backend page.');
}
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

// onText runs on every edit: it turns a just-typed "#12 " into an attached
// agent, and matches the slash-command menu.
export function onText(value: string) {
  draft = value;
  keepDraft();
  const s = get();
  const mention = loadAppearance().mentions === 'text' ? null : takeMention(value, id => activeAgents(workspaceOf(s).agents).some(a => a.id === id && a.status !== 'terminated'));
  if (mention) {
    update(d => { d.mode = 'message'; d.selected.add(mention.id); });
    setDraft(value = mention.text);
  }
  const next = !value.startsWith('/') || /\s/.test(value) ? [] : visibleCommands().filter(([name]) => name.startsWith(value.slice(1).toLowerCase()));
  const menu = get().menu;
  if (next.length === menu.length && next.every((c, i) => c === menu[i])) return;
  update(d => { d.menu = next; });
}
export function chooseCommand(name: string) {
  setDraft(`/${name} `);
  update(s => { s.menu = []; });
  input?.focus();
}
export function closeCommandMenu() { if (get().menu.length) update(s => { s.menu = []; }); }

// ---- submit ----------------------------------------------------------------
export async function submitCommand(value: string) {
  if (get().sending) return;
  value = value.trim();
  const files = [...get().pendingFiles];
  if (!files.length) { await dispatchCommand(value); return; }
  if (value.startsWith('/') && !/^\/(new|tell)\s+\S/.test(value)) { toast('Use attachments with a new task or /tell agent message.'); return; }
  const tell = value.match(/^(?:\/tell|tell\s+(?:agent\s+)?)\s*#?(\d+)\s+([\s\S]+)$/i);
  if ((value.startsWith('/tell') || (!get().selected.size && /^tell\s/i.test(value))) && (!tell || !findAgent(Number(tell[1])))) {
    toast('Specify a valid agent and message before sending attachments.'); return;
  }
  update(s => { s.sending = true; });
  try {
    // With a backend, files go to the server so agents can read them; the
    // prototype keeps them in this browser.
    const attachments = connected() ? await uploadFiles(call, get().active, files) : await storeFiles(files);
    update(s => { s.pendingFiles.splice(0, files.length); });
    await dispatchCommand(value || 'Review attached files', attachments);
  } catch (error) { toast(`Attachment save failed: ${errorText(error)}`); }
  finally { update(s => { s.sending = false; }); }
}

async function dispatchCommand(value: string, attachments: Attachment[] = []): Promise<void> {
  const intent = resolveSubmit(value, [...get().selected]);
  if (intent.kind === 'none') return;
  if (intent.kind === 'error') { toast(intent.message); return; }
  if (intent.kind === 'focus') { focusNewAgent(); return; }
  if (intent.kind === 'command') { runSlashCommand(intent.text, attachments); return; }
  if (intent.kind === 'message') { await sendMessage(intent.agentIds, intent.body, attachments); return; }
  // Open the parent first so the new subagent is visible when its event renders.
  if (intent.parentId) update(s => { s.collapsed.delete(intent.parentId); });
  await newAgent(intent.prompt, attachments, intent.parentId);
}

const notWired = (what: string) => toast(`${what} isn't connected to the backend yet.`);
const idArg = (args: string) => Number(args.trim().replace('#', ''));

function runSlashCommand(value: string, attachments: Attachment[] = []) {
  const [, name = '', args = ''] = value.match(/^\/(\w+)(?:\s+([\s\S]*))?$/) || [];
  const done = clearComposer;
  switch (name) {
    case 'help': done(); openPanel(args.trim().toLowerCase() === 'status' ? 'status' : 'help'); return;
    case 'tools': case 'mcp': done(); showTools(); return;
    case 'history': done(); openPanel('history'); return;
    case 'channels': done(); openPanel('channels'); return;
    case 'memory': done(); openPanel('memories'); return;
    case 'providers': done(); showProviders(); return;
    case 'new': done(); focusNewAgent(); return;
    case 'btw': askBtw(args.trim()); return;
    case 'search': update(s => { s.search = args.trim(); }); done(); return;
    case 'layout': {
      const count = Number(args.trim());
      if (![1, 2, 3].includes(count)) { openPanel('layout'); return; }
      update(s => { s.layout = count === 1 ? 'list' : count === 3 ? 'three' : 'grid'; }); done(); return;
    }
    case 'terminate': {
      const explicit = args.trim();
      if (explicit && !/^#?\d+$/.test(explicit)) { toast('Use /terminate, or /terminate followed by one agent ID.'); return; }
      const s = get();
      const candidates = activeAgents(workspaceOf(s).agents).filter(agent => (explicit ? agent.id === idArg(explicit) : s.selected.has(agent.id)));
      if (candidates.length !== 1) { toast('Attach one active agent, or specify its ID.'); return; }
      done(); terminateAgent(candidates[0].id); return;
    }
    case 'remove': case 'restore': {
      if (connected() && !isSample(idArg(args))) { notWired(`/${name}`); return; }
      if (!/^#?\d+$/.test(args.trim())) { toast(`Use /${name} followed by an agent ID.`); return; }
      const id = idArg(args);
      if (!findAgent(id)) { toast('Agent not found.'); return; }
      done(); if (name === 'remove') terminateAgent(id); else restore(id); return;
    }
    case 'samples': {
      if (!developerMode()) { toast('Turn on Developer mode in Settings → Developer first.'); return; }
      const choice = args.trim().toLowerCase();
      if (!['on', 'off', ''].includes(choice)) { toast('Use /samples on or /samples off.'); return; }
      done(); setSamples(choice ? choice === 'on' : !get().showSamples);
      return;
    }
    case 'tell':
      if (!/^#?\d+\s+\S/.test(args)) { toast('Use /tell 11 your message'); return; }
      dispatchCommand('tell agent ' + args.replace(/^#/, ''), attachments); return;
    case 'allow': case 'deny': case 'pause': case 'resume': case 'approve': case 'decline': {
      const targets = idArg(args) ? [idArg(args)] : [...get().selected];
      if (connected() && (!targets.length || targets.some(id => !isSample(id)))) {
        if (!targets.length) { toast(`Attach an agent or use /${name} 12.`); return; }
        done();
        if (name === 'resume') { toast('Send the agent a message to continue it.'); return; }
        for (const id of targets) {
          if (name === 'pause') stopAgent(id);
          else decide(id, name === 'allow' || name === 'approve' ? 'approve' : 'reject');
        }
        return;
      }
      try {
        let result = { applied: [] as number[], skipped: [] as number[] };
        update(s => { result = applyAgentCommand(workspaceOf(s).agents, s.selected, name, args); });
        save(); done();
        toast(`Applied to ${result.applied.map(id => '#' + id).join(', ')}${result.skipped.length ? `; skipped ${result.skipped.length} ineligible` : ''}.`);
      } catch (error) { toast(errorText(error)); }
      return;
    }
    default: toast('Unknown command. Type / to see commands.');
  }
}

// ---- side questions ----------------------------------------------------------
// /btw asks a model about the workspace from a read-only digest of the agents.
// The answer opens in the side panel's btw panel; no agent or transcript
// changes. Closing it returns to the panel (and, on a phone, the view) shown
// before.
let btwRequest = 0;
let beforeBtw: { panel: Panel; mobileView: 'agents' | 'panel' } | null = null;
export async function askBtw(question: string) {
  if (!question) { toast('Ask a question after /btw, e.g. /btw what is #12 doing?'); return; }
  if (!connected()) { toast('/btw needs the Go backend.'); return; }
  const choice = currentChoice();
  if (!choice) { toast('Add a model provider first.'); showProviders(); return; }
  const request = ++btwRequest;
  const s0 = get();
  if (s0.panel.kind !== 'btw') beforeBtw = { panel: s0.panel, mobileView: s0.mobileView };
  update(s => { s.btw = { question, pending: true }; });
  openPanel('btw');
  clearComposer();
  let next: Btw;
  try {
    const answer = await call<{ text: string }>('btw', 'Ask', { workspaceId: get().active, question, provider: choice.provider, model: choice.model });
    next = { question, answer: answer.text || '(no answer)' };
  } catch (error) { next = { question, error: errorText(error) }; }
  if (request === btwRequest && get().btw) update(s => { s.btw = next; });
}
export function closeBtw() {
  btwRequest++;
  if (!get().btw) return;
  const back = beforeBtw;
  beforeBtw = null;
  update(s => {
    s.btw = null;
    if (s.panel.kind === 'btw') {
      s.panel = back?.panel ?? { kind: 'questions' };
      if (back) s.mobileView = back.mobileView;
    }
  });
}
