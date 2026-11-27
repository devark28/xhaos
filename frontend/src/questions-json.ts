// Copy questions as JSON: an agent's pending questions (the Questions
// panel's "n / m"), with everything needed to answer them elsewhere (a chat
// with Claude Code) and map the answers back: the agent's number and title,
// each question's id and position, its options with labels and
// descriptions apart, and the MCP ids for answer_question. Framework-free.
import type { Agent } from './types.ts';

export interface CopiedOption { id: string; label: string; description: string }
export interface CopiedQuestion {
  id: string; // the question's id within its batch (q1, q2…), as answer_question takes it
  index: number; // 1-based, as the panel's "n / m"
  kind: 'choose' | 'ask'; // choose: has options (and your own answer); ask: a written answer
  header: string;
  question: string;
  multi_select: boolean;
  required: boolean;
  options: CopiedOption[];
  allows_other: boolean; // a written answer besides (or instead of) the options
}
export interface CopiedQuestions {
  agent: { number: number; title: string };
  copied_at: string;
  title: string; // the batch's heading
  question_id: string; // the MCP id (list_questions / answer_question): <agent>/<call id>
  answer_via_mcp: string;
  questions: CopiedQuestion[];
}

// questionsDoc is one agent's pending questions, or null when it has none.
export function questionsDoc(agent: Agent, now: Date = new Date()): CopiedQuestions | null {
  const batch = agent.questionBatch;
  if (!batch || batch.status !== 'pending' || !batch.questions.length) return null;
  const mcpId = `${agent.id}/${batch.id}`;
  return {
    agent: { number: agent.id, title: agent.title },
    copied_at: now.toISOString(),
    title: batch.title,
    question_id: mcpId,
    answer_via_mcp: `answer_question with question_id "${mcpId}" and answers: [{ question: <id>, choices: [<option label or id>…], text: <written answer> }]`,
    questions: batch.questions.map((q, i) => ({
      id: q.id,
      index: i + 1,
      kind: q.options?.length ? 'choose' : 'ask',
      header: q.title,
      question: q.prompt,
      multi_select: Boolean(q.multiple),
      required: q.required !== false,
      options: (q.options || []).map(o => ({ id: o.id, label: o.label, description: o.description || '' })),
      allows_other: true, // the panel always offers "Your own answer"
    })),
  };
}

// questionsJSON is the text to copy for one agent (a document) or, with
// all, for several (an array of documents, in the order given; agents with
// nothing pending are left out). Pretty-printed with 2 spaces.
export function questionsJSON(agents: Agent[], { all = false, now = new Date() }: { all?: boolean; now?: Date } = {}): { text: string; count: number } {
  const docs = agents.map(a => questionsDoc(a, now)).filter((d): d is CopiedQuestions => d !== null);
  const count = docs.reduce((n, d) => n + d.questions.length, 0);
  return { text: JSON.stringify(all ? docs : docs[0] ?? null, null, 2), count };
}

// copyText puts text on the clipboard: the Clipboard API where it works (a
// secure page, within a click), else a hidden textarea and execCommand.
export async function copyText(text: string, doc: Document = document): Promise<void> {
  try {
    if (navigator.clipboard?.writeText) { await navigator.clipboard.writeText(text); return; }
  } catch { /* fall back */ }
  const area = doc.createElement('textarea');
  area.value = text;
  area.setAttribute('readonly', '');
  area.style.position = 'fixed';
  area.style.opacity = '0';
  doc.body.appendChild(area);
  area.select();
  const ok = doc.execCommand('copy');
  area.remove();
  if (!ok) throw new Error('the browser refused to copy');
}
