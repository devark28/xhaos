// Agent actions: expand/collapse, attach to the composer, decide tool calls,
// stop, terminate, create, message.
import { pendingCalls } from '../../backend-view.ts';
import { hasPendingQuestions } from '../../questions.ts';
import { createAttentionTracker, agentPath } from '../../attention.ts';
import { idempotencyKey } from '../../server-client.ts';
import { findModel } from '../../models.ts';
import { activeAgents, flatten, transition, restoreAgent, terminateAgent as markTerminated } from '../../model.ts';
import { update, get, workspace, workspaceOf, findAgent, findAgentIn, focusLater, toast, errorText, save } from './store.ts';
import { call, connected, isSample } from './backend.ts';
import { clearComposer, currentChoice, focusComposer } from './composer.ts';
import { showProviders } from './panels.ts';
import type { Agent, Attachment } from '../../types.ts';

export function toggleAgent(id: number) {
  update(s => { if (s.collapsed.has(id)) s.collapsed.delete(id); else s.collapsed.add(id); });
}
export function toggleCollapseAll() {
  update(s => {
    const agents = activeAgents(workspaceOf(s).agents);
    const close = !agents.every(a => s.collapsed.has(a.id));
    agents.forEach(a => (close ? s.collapsed.add(a.id) : s.collapsed.delete(a.id)));
  });
}
export function toggleRecipient(id: number) {
  update(s => {
    s.mode = 'message';
    if (s.selected.has(id)) s.selected.delete(id); else s.selected.add(id);
  });
  focusComposer();
}
export function detachRecipient(id: number) { update(s => { s.selected.delete(id); }); }
// revealAgent opens an agent's parents and toggles the agent itself.
export function revealAgent(id: number) {
  update(s => {
    agentPath(workspaceOf(s).agents, id).slice(0, -1).forEach(parentId => s.collapsed.delete(parentId));
    if (s.collapsed.has(id)) s.collapsed.delete(id); else s.collapsed.add(id);
  });
  focusLater(`[data-collapse="${id}"]`);
}

// decide answers one waiting tool call (callId), or every waiting call of
// the agent when callId is omitted.
export async function decide(id: number, action: 'approve' | 'reject', callId?: string) {
  if (connected() && !isSample(id)) {
    const agent = findAgent(id);
    const calls = callId ? [callId] : agent ? pendingCalls(agent) : [];
    if (!calls.length) { toast(`#${id} has nothing waiting for a decision.`); return; }
    for (const callId of calls) {
      try { await call('agent', 'Decide', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentId: id, callId, allow: action === 'approve' }); }
      catch (error) { toast(errorText(error)); return; }
    }
    return;
  }
  update(s => { const agent = findAgentIn(s, id); if (agent) transition(agent, action); });
  save();
}

// stopAgent cancels a backend agent's running turn (and any running command).
export async function stopAgent(id: number) {
  try {
    await call('agent', 'Stop', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentId: id });
    toast(`Stopped #${id}. Send it a message to continue.`);
  } catch (error) { toast(errorText(error)); }
}

export async function terminateAgent(id: number) {
  if (connected() && !isSample(id)) {
    try { await call('agent', 'Terminate', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentId: id }); }
    catch (error) { toast(errorText(error)); return; }
    update(s => { s.selected.delete(id); });
  } else {
    update(s => {
      markTerminated(workspaceOf(s).agents, id);
      s.selected.delete(id);
      const active = activeAgents(workspaceOf(s).agents);
      for (const target of [...s.selected]) if (!active.some(agent => agent.id === target)) s.selected.delete(target);
    });
    save();
  }
  toast(`Agent #${id} terminated. View retained history with /history.`);
}
export function restore(id: number) { update(s => { restoreAgent(workspaceOf(s).agents, id); }); save(); }

// Ctrl/Cmd+Alt+N and bare /new: detach recipients so Enter creates a new agent.
export function focusNewAgent() {
  update(s => { s.selected.clear(); s.mode = 'message'; });
  focusComposer();
}

export async function newAgent(prompt: string, attachments: Attachment[] = [], parentId = 0): Promise<boolean> {
  const choice = currentChoice();
  if (connected() && !isSample(parentId)) {
    if (!choice) { toast('Add a model provider first.'); showProviders(); return false; }
    if (attachments.length) { toast('Attachments are not sent to the backend yet; remove them to create the agent.'); return false; }
    try {
      const agent = await call<Agent>('agent', 'Create', { idempotencyKey: idempotencyKey(), workspaceId: get().active, parentId, prompt, provider: choice.provider, model: choice.model });
      clearComposer();
      toast(`Agent #${agent.id} created${parentId ? ` under #${parentId}` : ''}.`);
      return true;
    } catch (error) { toast(`Could not create agent: ${errorText(error)}`); return false; }
  }
  const local = choice || findModel(get().composerModelId);
  if (parentId && !findAgent(parentId)) { toast(`Agent #${parentId} not found.`); return false; }
  const id = Math.max(0, ...get().workspaces.flatMap(w => flatten(w.agents)).map(a => a.id)) + 1;
  update(s => {
    const parent = parentId ? findAgentIn(s, parentId) : undefined;
    const agent: Agent = {
      id, title: prompt.split('\n')[0].slice(0, 120), prompt, status: 'paused', role: parent ? 'Subagent' : 'New task',
      provider: local.provider, agentType: 'xhaos', modelId: local.model, model: local.label, modelLabel: local.label, time: '0m', progress: 0,
      message: 'Task queued locally. Connect a backend to execute.',
      activity: [{ kind: 'user', text: prompt, attachments }, { kind: 'system', text: 'Task queued locally. No backend is connected.' }],
      children: [],
    };
    if (parent) (parent.children ||= []).push(agent); else workspaceOf(s).agents.unshift(agent);
    s.collapsed.add(id); s.search = '';
  });
  save(); clearComposer(); toast(`Agent #${id} created in the prototype.`);
  return true;
}

export async function sendMessage(agentIds: number[], text: string, attachments: Attachment[] = []) {
  const real = agentIds.filter(id => connected() && !isSample(id));
  if (real.length) {
    if (attachments.length) { toast('Attachments are not sent to the backend yet; remove them to send.'); return; }
    try { await call('agent', 'Message', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentIds: real, body: text }); }
    catch (error) { toast(`Could not send: ${errorText(error)}`); return; }
    agentIds = agentIds.filter(id => !real.includes(id));
    if (!agentIds.length) { clearComposer(); toast(`Sent to ${real.map(id => '#' + id).join(', ')}.`); return; }
  }
  if (!activeAgents(workspace().agents).some(agent => agentIds.includes(agent.id))) { toast('That agent is not active in this workspace.'); return; }
  update(s => {
    activeAgents(workspaceOf(s).agents).filter(agent => agentIds.includes(agent.id))
      .forEach(agent => (agent.activity ||= [{ kind: 'assistant', text: agent.message || '' }]).push({ kind: 'user', text, attachments }));
  });
  save(); clearComposer(); toast('Message saved locally. No backend is connected.');
}

// ---- attention -------------------------------------------------------------
const newlyWaitingAgents = createAttentionTracker();
// Scrolls a newly waiting agent (or its nearest visible ancestor) into view once.
export function notifyAttention() {
  const s = get();
  const agents = workspaceOf(s).agents;
  const ids = newlyWaitingAgents(s.active, agents).filter(id => { const agent = findAgentIn(s, id); return !agent || !hasPendingQuestions(agent); });
  if (!ids.length || s.search || !globalThis.requestAnimationFrame) return;
  const path = agentPath(agents, ids[0]);
  requestAnimationFrame(() => {
    const target = path.reverse().map(id => document.querySelector(`[data-agent-row-id="${id}"]`)).find(Boolean);
    const scroller = document.getElementById('agents');
    if (!target || !target.isConnected || !scroller) return;
    const viewport = scroller.getBoundingClientRect();
    const rect = target.getBoundingClientRect();
    if (rect.top >= viewport.top + 8 && rect.bottom <= viewport.bottom - 16) return;
    const top = scroller.scrollTop + rect.top - viewport.top - (scroller.clientHeight - rect.height) / 2;
    scroller.scrollTo({ top: Math.max(0, top), behavior: globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
  });
}
