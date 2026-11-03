// Answering an agent's questions, one step at a time.
import { answerQuestions } from '../../questions.ts';
import { update, get, findAgentIn, pendingQuestions, focusLater, toast, errorText, save, questionStepKey, type State } from './store.ts';
import { focusComposer } from './composer.ts';
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

export function advanceOrSubmit(agent: Agent) {
  const batch = agent.questionBatch;
  if (!batch) return;
  const step = get().questionSteps.get(questionStepKey(agent)) || 0;
  if (step < batch.questions.length) { setQuestionStep(agent, step + 1); return; }
  try {
    update(s => { const target = findAgentIn(s, agent.id); if (target) answerQuestions(target, batch.id, batch.draft || {}); });
    save();
    if (pendingQuestions().length) focusLater('[data-questions-agent]'); else focusComposer();
    toast(`Answers saved for agent #${agent.id}.`);
  } catch (error) { toast(errorText(error)); }
}
