// Answering an agent's questions, one step at a time.
import { answerQuestions } from '../../questions.ts';
import { update, get, findAgentIn, pendingQuestions, focusLater, toast, errorText, save, questionStepKey, type State } from './store.ts';
import { focusComposer } from './composer.ts';
import { call, connected, isSample } from './backend.ts';
import { idempotencyKey } from '../../server-client.ts';
import type { Agent, Answer } from '../../types.ts';

export const questionStepIn = (s: Pick<State, 'questionSteps'>, agent: Agent) => Math.min(s.questionSteps.get(questionStepKey(agent)) || 0, agent.questionBatch?.questions.length || 0);

export function setQuestionStep(agent: Agent, step: number) {
  update(s => { s.questionSteps.set(questionStepKey(agent), Math.max(0, Math.min(agent.questionBatch?.questions.length || 0, step))); });
  focusLater('[data-questions-agent]');
}
export function setQuestionAgent(id: number) { update(s => { s.questionAgentId = id; }); }

export function setAnswer(agentId: number, questionId: string, answer: Partial<Answer>) {
  update(s => {
    const batch = findAgentIn(s, agentId)?.questionBatch;
    if (batch) batch.draft = { ...(batch.draft || {}), [questionId]: answer };
  });
  save();
}

// sendAnswers answers a real agent's ask_user call; the agent continues
// once the server records them (the batch then clears itself).
async function sendAnswers(agent: Agent) {
  const batch = agent.questionBatch!;
  const answers = Object.fromEntries(batch.questions.map(q => {
    const a = batch.draft?.[q.id] || {};
    return [q.id, { choices: a.choices || [], text: a.text || '' }];
  }));
  try {
    await call('agent', 'Answer', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentId: agent.id, callId: batch.id, answers });
    toast(`Answers sent to #${agent.id}.`);
    if (pendingQuestions().some(a => a.id !== agent.id)) focusLater('[data-questions-agent]'); else focusComposer();
  } catch (error) { toast(errorText(error)); }
}

// dismissQuestions tells a real agent the user will not answer.
export async function dismissQuestions(agent: Agent) {
  const batch = agent.questionBatch;
  if (!batch) return;
  try { await call('agent', 'Decide', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentId: agent.id, callId: batch.id, allow: false }); toast(`Dismissed #${agent.id}'s questions.`); }
  catch (error) { toast(errorText(error)); }
}

export function advanceOrSubmit(agent: Agent) {
  const batch = agent.questionBatch;
  if (!batch) return;
  const step = get().questionSteps.get(questionStepKey(agent)) || 0;
  if (step < batch.questions.length) { setQuestionStep(agent, step + 1); return; }
  if (connected() && !isSample(agent.id)) { sendAnswers(agent); return; }
  try {
    update(s => { const target = findAgentIn(s, agent.id); if (target) answerQuestions(target, batch.id, batch.draft || {}); });
    save();
    if (pendingQuestions().length) focusLater('[data-questions-agent]'); else focusComposer();
    toast(`Answers saved for agent #${agent.id}.`);
  } catch (error) { toast(errorText(error)); }
}
