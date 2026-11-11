import test from 'node:test';
import assert from 'node:assert/strict';
import { questionsDoc, questionsJSON } from './questions-json.ts';

const now = new Date('2026-11-11T10:00:00Z');
const agent = (id: number, title: string, status = 'pending') => ({
  id, title, status: 'waiting',
  questionBatch: {
    id: `call_${id}`, status, title: 'A few decisions',
    questions: [
      { id: 'q1', title: 'Which approach', prompt: 'Which approach should I take?', options: [
        { id: 'o1', label: 'The small change', description: 'touch as little as possible' },
        { id: 'o2', label: 'The thorough one' },
      ] },
      { id: 'q2', title: 'Which checks', prompt: 'Which checks should I run?', multiple: true, options: [{ id: 'o1', label: 'Unit tests' }, { id: 'o2', label: 'Lint', description: 'gofmt and go vet' }] },
      { id: 'q3', title: 'Anything else', prompt: 'Anything else I should know?', required: false, options: [] },
    ],
  },
}) as any;

test('one agent: its number and title, MCP ids, questions in panel order', () => {
  const doc = questionsDoc(agent(7, 'Fix the login form'), now)!;
  assert.deepEqual(doc.agent, { number: 7, title: 'Fix the login form' });
  assert.equal(doc.copied_at, '2026-11-11T10:00:00.000Z');
  assert.equal(doc.question_id, '7/call_7');
  assert.match(doc.answer_via_mcp, /question_id "7\/call_7"/);
  assert.deepEqual(doc.questions.map(q => [q.index, q.id, q.kind]), [[1, 'q1', 'choose'], [2, 'q2', 'choose'], [3, 'q3', 'ask']]);
});

test('labels and descriptions stay apart; multi-select and required are kept', () => {
  const [q1, q2, q3] = questionsDoc(agent(7, 'T'), now)!.questions;
  assert.deepEqual(q1.options, [
    { id: 'o1', label: 'The small change', description: 'touch as little as possible' },
    { id: 'o2', label: 'The thorough one', description: '' },
  ]);
  assert.equal(q1.question, 'Which approach should I take?');
  assert.equal(q1.header, 'Which approach');
  assert.equal(q1.multi_select, false);
  assert.equal(q2.multi_select, true);
  assert.equal(q2.options[1].label, 'Lint');
  assert.equal(q2.options[1].description, 'gofmt and go vet');
  assert.equal(q3.required, false);
  assert.equal(q1.required, true);
  assert.ok([q1, q2, q3].every(q => q.allows_other));
});

test('only pending questions; JSON is pretty-printed with 2 spaces', () => {
  assert.equal(questionsDoc(agent(3, 'Done', 'answered'), now), null);
  assert.equal(questionsDoc({ id: 4, title: 'None', status: 'idle' } as any, now), null);
  const one = questionsJSON([agent(7, 'Seven')], { now });
  assert.equal(one.count, 3);
  assert.ok(one.text.startsWith('{\n  "agent": {\n    "number": 7,'));
  assert.deepEqual(JSON.parse(one.text), questionsDoc(agent(7, 'Seven'), now));
});

test('all agents: an array of documents, skipping agents with nothing pending', () => {
  const all = questionsJSON([agent(9, 'Nine'), agent(3, 'Done', 'answered'), agent(2, 'Two')], { all: true, now });
  const parsed = JSON.parse(all.text);
  assert.equal(all.count, 6);
  assert.deepEqual(parsed.map((d: any) => [d.agent.number, d.agent.title]), [[9, 'Nine'], [2, 'Two']]);
  assert.equal(JSON.parse(questionsJSON([agent(2, 'Two')], { all: true, now }).text).length, 1);
});
