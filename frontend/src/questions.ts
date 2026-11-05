import type { Agent, Answer, Question, QuestionBatch, Workspace } from './types.ts';

export const hasPendingQuestions = (agent: Agent): boolean => agent.questionBatch?.status === 'pending';

// answerQuestions validates the owner's answers, records them on the batch,
// and adds them to the transcript as one owner message.
// OWN marks "your own answer" as the chosen option in a draft. It is a
// choice like the agent's options: on a single-answer question it replaces
// them; the written text is only sent while it is chosen.
export const OWN = '__own';
export const ownChosen = (q: Question, a: Partial<Answer>) => !q.options?.length || (a.choices || []).includes(OWN) || (!(a.choices || []).length && Boolean((a.text || '').trim()));
// finalAnswer is a draft as it is sent: the agent's options chosen, and the
// text only when your own answer is the choice.
export function finalAnswer(q: Question, a: Partial<Answer> = {}): Answer {
  return { choices: (a.choices || []).filter(c => c !== OWN), text: ownChosen(q, a) ? String(a.text || '').trim() : '' };
}

export function answerQuestions(agent: Agent, batchId: string, answers: Record<string, Partial<Answer>>): Record<string, Answer> {
  const batch = agent.questionBatch;
  if (!batch || !hasPendingQuestions(agent) || batch.id !== batchId || agent.status === 'terminated') {
    throw new Error('This question request is no longer pending.');
  }
  const validated: Record<string, Answer> = {};
  for (const question of batch.questions) {
    const answer = finalAnswer(question, answers[question.id]);
    const choices = [...new Set(answer.choices || [])];
    const text = String(answer.text || '').trim();
    const optionIds = new Set((question.options || []).map(option => option.id));
    if (choices.some(id => !optionIds.has(id)) || (!question.multiple && choices.length > 1)) {
      throw new Error(`Choose a valid answer for “${question.prompt}”.`);
    }
    if (question.required !== false && !choices.length && !text) {
      throw new Error(`Answer “${question.prompt}” or write your own response.`);
    }
    validated[question.id] = { choices, text };
  }
  batch.answers = validated;
  batch.status = 'answered';
  batch.answeredAt = new Date().toISOString();
  delete batch.draft;
  const text = batch.questions.map(question => {
    const answer = validated[question.id];
    const selected = (question.options || []).filter(option => answer.choices.includes(option.id)).map(option => option.label).join(', ');
    return `${question.prompt}\n${[selected, answer.text].filter(Boolean).join('\n') || '(Skipped)'}`;
  }).join('\n\n');
  (agent.activity ||= []).push({ kind: 'user', text, questionBatchId: batchId });
  if (agent.status === 'waiting') agent.status = 'running';
  return validated;
}

export function demoQuestions(): QuestionBatch {
  return {
    id: 'demo-backend-questions-v1',
    status: 'pending',
    title: 'A few decisions before I continue',
    questions: [
      {
        id: 'runner', title: 'Backend route', prompt: 'Which route should the first dogfooding adapter use?',
        options: [
          { id: 'cli', label: 'Existing CLI runner', description: 'Use an installed agent runner for the first end-to-end path.' },
          { id: 'api', label: 'Direct model API', description: 'Own the model conversation and tool loop inside Xhaos.' },
        ],
      },
      {
        id: 'scope', title: 'Change scope', prompt: 'What should the first agent be allowed to change?',
        options: [{ id: 'docs', label: 'Documentation only' }, { id: 'feature', label: 'One small frontend feature' }],
      },
      { id: 'notes', title: 'Constraints', prompt: 'Any constraints I should know about?', required: false, options: [] },
    ],
  };
}

// ensureQuestionDemo adds the sample agent that asks questions, once.
export function ensureQuestionDemo(workspaces: Workspace[], workspace: Workspace): Agent {
  const existing = workspace.agents.find(agent => agent.demoScenario === 'composer-question-v1');
  if (existing) return existing;
  const ids: number[] = [];
  const visit = (agents: Agent[]) => agents.forEach(agent => { ids.push(agent.id); visit(agent.children || []); });
  workspaces.forEach(w => visit(w.agents));
  const agent: Agent = {
    id: Math.max(0, ...ids) + 1,
    title: 'Clarify the first backend dogfooding run',
    role: 'Planning',
    status: 'waiting',
    model: 'GPT-6.1-sol',
    time: '0m',
    progress: 0,
    demo: true,
    demoScenario: 'composer-question-v1',
    message: 'I need a few decisions before I proceed.',
    activity: [{ kind: 'assistant', text: 'This is a simulated question request. Please choose an option or answer in your own words for the batch shown in the composer.' }],
    questionBatch: demoQuestions(),
    children: [],
  };
  workspace.agents.push(agent);
  return agent;
}
