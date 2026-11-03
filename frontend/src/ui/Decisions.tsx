// Allow/Deny for waiting tool calls: per call in the transcript, and for all
// of an agent's waiting calls on its card.
import { pendingCalls } from '../backend-view.ts';
import { hasPendingQuestions } from '../questions.ts';
import { Button } from './kit.tsx';
import { useApp, type State } from './state/store.ts';
import { decide } from './state/agents.ts';
import type { Agent } from '../types.ts';

// Backend agents need a decision only while a tool call waits; a backend
// agent can also need attention for an error note, which has no Allow/Deny.
// Each waiting call has its own buttons in the transcript; the card-level
// buttons appear when they add something: several calls, or a closed card.
export function needsDecision(s: Pick<State, 'backendVersion' | 'collapsed'>, agent: Agent): boolean {
  if (agent.status !== 'waiting' || hasPendingQuestions(agent)) return false;
  if (!s.backendVersion || agent.sample) return true;
  const waiting = pendingCalls(agent).length;
  return waiting > 1 || (waiting === 1 && s.collapsed.has(agent.id));
}
export const useNeedsDecision = (agent: Agent) => useApp(s => needsDecision(s, agent));

export function DecisionButtons({ agent, callId }: { agent: Agent; callId?: string }) {
  const backend = useApp(s => s.backendVersion !== null);
  const count = callId ? 1 : backend && !agent.sample ? pendingCalls(agent).length : 1;
  const all = count > 1 ? ` all ${count}` : '';
  return <>
    <Button variant="allow" data-action="approve" data-id={agent.id} data-call-id={callId} onClick={() => decide(agent.id, 'approve', callId)}>Allow{all}</Button>
    <Button variant="deny" data-action="reject" data-id={agent.id} data-call-id={callId} onClick={() => decide(agent.id, 'reject', callId)}>Deny{all}</Button>
  </>;
}
