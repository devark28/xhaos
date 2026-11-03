// Allow/Deny for waiting tool calls: per call in the transcript, and for all
// of an agent's waiting calls on its card.
import { pendingCalls } from '../backend-view.ts';
import { hasPendingQuestions } from '../questions.ts';
import { Button } from './kit.tsx';
import { S, decide } from './store.ts';
import type { Agent } from '../types.ts';

// Backend agents need a decision only while a tool call waits; a backend
// agent can also need attention for an error note, which has no Allow/Deny.
// Each waiting call has its own buttons in the transcript; the card-level
// buttons appear when they add something: several calls, or a closed card.
export function needsDecision(agent: Agent): boolean {
  if (agent.status !== 'waiting' || hasPendingQuestions(agent)) return false;
  if (!S.backend || agent.sample) return true;
  const waiting = pendingCalls(agent).length;
  return waiting > 1 || (waiting === 1 && S.collapsed.has(agent.id));
}

export function DecisionButtons({ agent, callId }: { agent: Agent; callId?: string }) {
  const count = callId ? 1 : S.backend && !agent.sample ? pendingCalls(agent).length : 1;
  const all = count > 1 ? ` all ${count}` : '';
  return <>
    <Button variant="allow" data-action="approve" data-id={agent.id} data-call-id={callId} onClick={() => decide(agent.id, 'approve', callId)}>Allow{all}</Button>
    <Button variant="deny" data-action="reject" data-id={agent.id} data-call-id={callId} onClick={() => decide(agent.id, 'reject', callId)}>Deny{all}</Button>
  </>;
}
