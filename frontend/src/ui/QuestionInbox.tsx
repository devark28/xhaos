// Questions an agent asked, answered one at a time in the composer area.
import { useState, type KeyboardEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button, Kbd, StatusDot } from './kit.tsx';
import { activeAgents } from '../model.ts';
import { hasPendingQuestions } from '../questions.ts';
import { useApp, workspaceOf, afterCommit } from './state/store.ts';
import { questionStepIn, setQuestionStep, setQuestionAgent, setAnswer, advanceOrSubmit, dismissQuestions } from './state/questions.ts';
import type { Agent, Answer, Question as QuestionData, QuestionOption } from '../types.ts';

const emptyAnswer: Answer = { choices: [], text: '' };
const tabTitle = (q: QuestionData) => q.title || q.prompt.split(' ').slice(0, 4).join(' ');
const answered = (a?: Partial<Answer>) => Boolean(a && ((a.choices || []).length || (a.text || '').trim()));
const draftOf = (agent: Agent, id: string): Answer => ({ ...emptyAnswer, ...agent.questionBatch?.draft?.[id] });

function Mark({ square = false, pencil = false, chosen = false }) {
  return (
    <span aria-hidden="true" {...stylex.props(styles.mark, square && styles.markSquare, pencil && styles.markPencil, chosen && styles.markChosen)}>
      {chosen && <span {...stylex.props(styles.markDot, square && styles.markDotSquare)} />}
    </span>
  );
}

function Option({ question, option, answer, onPick }: { question: QuestionData; option: QuestionOption; answer: Answer; onPick(id: string): void }) {
  const chosen = answer.choices.includes(option.id);
  const [ring, setRing] = useState(false);
  return (
    <label data-option={option.id} data-chosen={chosen || undefined} {...stylex.props(styles.option, chosen && styles.optionChosen, ring && styles.optionFocus)}>
      <input type={question.multiple ? 'checkbox' : 'radio'} name={`choice:${question.id}`} value={option.id} checked={chosen} onChange={() => onPick(option.id)}
        onFocus={event => setRing(event.currentTarget.matches(':focus-visible'))} onBlur={() => setRing(false)} {...stylex.props(styles.hiddenInput)} />
      <Mark square={question.multiple} chosen={chosen} />
      <span {...stylex.props(styles.optionText)}>
        <span {...stylex.props(styles.optionLabel)}>{option.label}</span>
        {option.description && <small {...stylex.props(styles.optionDescription)}>{option.description}</small>}
      </span>
    </label>
  );
}

function Question({ agent, question }: { agent: Agent; question: QuestionData }) {
  const answer = draftOf(agent, question.id);
  const [writing, setWriting] = useState(Boolean(answer.text));
  const hasOptions = Boolean(question.options?.length);
  const pick = (id: string) => {
    const choices = question.multiple
      ? (answer.choices.includes(id) ? answer.choices.filter(c => c !== id) : [...answer.choices, id])
      : [id];
    setAnswer(agent.id, question.id, { ...answer, choices });
  };
  const fit = (el: HTMLTextAreaElement | null) => { if (!el) return; el.style.height = 'auto'; el.style.height = `${el.scrollHeight}px`; };
  return (
    <fieldset {...stylex.props(styles.question)}>
      <legend {...stylex.props(styles.prompt)}>{question.prompt}{question.required === false && <span {...stylex.props(styles.optional)}>optional</span>}</legend>
      {hasOptions && (
        <div role={question.multiple ? 'group' : 'radiogroup'} aria-label={question.prompt} {...stylex.props(styles.options)}>
          {question.options.map(option => <Option key={option.id} question={question} option={option} answer={answer} onPick={pick} />)}
        </div>
      )}
      {hasOptions && !writing && (
        <button type="button" data-write-answer aria-expanded="false" {...stylex.props(styles.write)}
          onClick={() => { setWriting(true); afterCommit(() => document.querySelector<HTMLElement>('[data-answer-text]')?.focus({ preventScroll: true })); }}>
          <Mark pencil />Write your own answer…
        </button>
      )}
      {(!hasOptions || writing) && (
        <textarea data-answer-text ref={fit} name={`text:${question.id}`} aria-label={`Your answer: ${question.prompt}`} rows={1} placeholder="Your answer…" value={answer.text}
          onChange={event => { fit(event.target); setAnswer(agent.id, question.id, { ...answer, text: event.target.value }); }} {...stylex.props(styles.text)} />
      )}
    </fieldset>
  );
}

function Review({ agent }: { agent: Agent }) {
  const batch = agent.questionBatch!;
  return (
    <div {...stylex.props(styles.review)}>
      {batch.questions.map((q, index) => {
        const answer = draftOf(agent, q.id);
        const chosen = (q.options || []).filter(option => answer.choices.includes(option.id)).map(option => option.label);
        const text = [...chosen, answer.text].filter(Boolean).join(' · ');
        return (
          <button type="button" key={q.id} onClick={() => setQuestionStep(agent, index)} {...stylex.props(styles.reviewRow)}>
            <span {...stylex.props(styles.reviewTitle)}>{tabTitle(q)}</span>
            <p {...stylex.props(styles.reviewText, !text && styles.reviewMissing)}>{text || (q.required === false ? 'Skipped' : 'Not answered')}</p>
          </button>
        );
      })}
    </div>
  );
}

export function QuestionInbox() {
  const pending = activeAgents(useApp(s => workspaceOf(s).agents)).filter(hasPendingQuestions);
  const chosenId = useApp(s => s.questionAgentId);
  const backend = useApp(s => s.backendVersion !== null);
  const steps = useApp(s => s.questionSteps);
  const agent = pending.find(a => a.id === chosenId) || pending[0];
  if (!agent?.questionBatch) return <p {...stylex.props(styles.muted)}>No pending questions.</p>;
  const batch = agent.questionBatch;
  const step = questionStepIn({ questionSteps: steps }, agent);
  const question = batch.questions[step];
  const others = pending.filter(a => a.id !== agent.id);
  const onKeyDown = (event: KeyboardEvent<HTMLFormElement>) => {
    if (event.target === event.currentTarget && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
      event.preventDefault(); setQuestionStep(agent, step + (event.key === 'ArrowRight' ? 1 : -1)); return;
    }
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && (event.target as HTMLElement).tagName !== 'BUTTON') { event.preventDefault(); advanceOrSubmit(agent); }
  };
  const tab = (index: number, label: string, done = false) => (
    <button key={index} type="button" role="tab" aria-selected={index === step} data-question-step={index} data-question-owner={agent.id}
      onClick={() => setQuestionStep(agent, index)} {...stylex.props(styles.tab, index === step && styles.tabChosen)}>
      {done && <span {...stylex.props(styles.tick)}>✓</span>}{label}
      {index === step && <span aria-hidden="true" {...stylex.props(styles.underline)} />}
    </button>
  );
  return (
    <div {...stylex.props(styles.root)}>
      <div {...stylex.props(styles.head)}>
        <StatusDot status="waiting" />
        <span {...stylex.props(styles.agentId)}>#{agent.id}</span>
        <span {...stylex.props(styles.agentTitle)}>{agent.title}</span>
        <span {...stylex.props(styles.progress)}>{question ? `${step + 1} / ${batch.questions.length}` : 'Review'}</span>
        {backend && !agent.sample && <Button data-dismiss-questions={agent.id} title="Tell the agent you won't answer" onClick={() => dismissQuestions(agent)} sx={styles.next}>Dismiss</Button>}
        {others.length > 0 && (
          <Button onClick={() => setQuestionAgent(others[0].id)} sx={styles.next}>{others.length} more waiting →</Button>
        )}
      </div>
      <div role="tablist" aria-label="Questions" {...stylex.props(styles.tabs)}>
        {batch.questions.map((q, index) => tab(index, tabTitle(q), answered(batch.draft?.[q.id])))}
        {tab(batch.questions.length, 'Review')}
      </div>
      <form tabIndex={0} aria-label={question ? `Question ${step + 1} of ${batch.questions.length}` : 'Review answers'}
        data-questions-agent={agent.id} data-question-batch={batch.id} data-question-step={step} data-question-count={batch.questions.length} data-question-id={question?.id || ''}
        onSubmit={event => { event.preventDefault(); advanceOrSubmit(agent); }} onKeyDown={onKeyDown} {...stylex.props(styles.body)}>
        {question ? <Question key={`${agent.id}:${question.id}`} agent={agent} question={question} /> : <Review agent={agent} />}
      </form>
      <div aria-hidden="true" {...stylex.props(styles.hints)}>
        {question
          ? <><Kbd>Enter</Kbd> next <Kbd>←</Kbd><Kbd>→</Kbd> questions <Kbd>Shift</Kbd>+<Kbd>Enter</Kbd> new line</>
          : <><Kbd>Enter</Kbd> send answers to #{agent.id}</>}
      </div>
    </div>
  );
}

const styles = stylex.create({
  root: { flexGrow: 1, minHeight: 0, display: 'flex', flexDirection: 'column', gap: 6 },
  muted: { color: colors.textFaint },
  head: { display: 'flex', alignItems: 'center', gap: 8, minWidth: 0, fontSize: 11, color: colors.textDim },
  agentId: { fontFamily: 'ui-monospace, monospace', fontSize: 10, color: colors.textGhost },
  agentTitle: { minWidth: 0, overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis', color: colors.text },
  progress: { marginLeft: 'auto', flexShrink: 0, color: colors.textGhost, fontVariantNumeric: 'tabular-nums' },
  next: { flexShrink: 0, paddingBlock: 2, paddingInline: 6, borderRadius: 3, fontSize: 10, color: colors.brand, backgroundColor: { default: 'transparent', ':hover': colors.brandSoft } },
  tabs: { display: 'flex', gap: 2, flexShrink: 0, overflowX: 'auto', scrollbarWidth: 'none', borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: colors.lineFaint },
  tab: {
    position: 'relative',
    paddingTop: 3,
    paddingInline: 10,
    paddingBottom: 6,
    borderWidth: 0,
    backgroundColor: 'transparent',
    fontFamily: 'inherit',
    fontSize: 11,
    color: { default: colors.textFaint, ':hover': colors.textSoft },
    whiteSpace: 'nowrap',
    cursor: 'pointer',
  },
  tabChosen: { color: { default: colors.textStrong, ':hover': colors.textStrong } },
  underline: { position: 'absolute', left: 8, right: 8, bottom: -1, height: 2, borderRadius: 2, backgroundColor: colors.brand },
  tick: { marginRight: 5, fontSize: 10, color: colors.brand },
  body: { flexGrow: 1, minHeight: 0, overflow: 'auto', paddingTop: 2, paddingInline: 2, paddingBottom: 4, borderRadius: 4, boxShadow: { default: null, ':focus-visible': `0 0 0 1px ${colors.brandLine}` } },
  question: { minWidth: 0, margin: 0, padding: 0, borderWidth: 0 },
  prompt: { width: '100%', marginTop: 0, marginInline: 0, marginBottom: 6, padding: 0, fontSize: 12.5, fontWeight: 500, lineHeight: 1.45, color: colors.textStrong },
  optional: { marginLeft: 8, fontSize: 10, fontWeight: 400, color: colors.textGhost },
  options: { display: 'flex', flexDirection: 'column', gap: 3 },
  option: {
    borderWidth: 1,
    borderStyle: 'solid',
    borderRadius: 5,
    position: 'relative',
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    paddingBlock: 5,
    paddingInline: 10,
    borderColor: { default: colors.menu, ':hover': colors.line },
    backgroundColor: { default: colors.sunken, ':hover': colors.card },
    cursor: 'pointer',
    transitionProperty: 'background-color, border-color',
    transitionDuration: '.12s',
    outlineOffset: 1,
  },
  // Keyboard focus on the (visually hidden) input.
  optionFocus: { outlineWidth: 1, outlineStyle: 'solid', outlineColor: colors.brand },
  optionChosen: { borderColor: { default: colors.brandLine, ':hover': colors.brandLine }, backgroundColor: { default: colors.brandSoft, ':hover': colors.brandSoft } },
  hiddenInput: { position: 'absolute', width: 1, height: 1, opacity: 0, pointerEvents: 'none' },
  mark: { display: 'grid', placeItems: 'center', flexShrink: 0, width: 14, height: 14, borderWidth: 1.5, borderStyle: 'solid', borderColor: colors.lineBright, borderRadius: '50%' },
  markSquare: { borderRadius: 3 },
  markPencil: { borderStyle: 'dashed' },
  markChosen: { borderColor: colors.brand },
  markDot: { width: 6, height: 6, borderRadius: '50%', backgroundColor: colors.brand },
  markDotSquare: { borderRadius: 1 },
  optionText: { display: 'flex', flexDirection: 'row', flexWrap: 'wrap', alignItems: 'baseline', columnGap: 10, rowGap: 1, minWidth: 0 },
  optionLabel: { fontSize: 12, color: colors.text },
  optionDescription: { fontSize: 11, lineHeight: 1.45, color: colors.textFaint },
  write: {
    borderWidth: 1,
    borderRadius: 5,
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    width: '100%',
    marginTop: 3,
    paddingBlock: 4,
    paddingInline: 10,
    borderStyle: 'dashed',
    borderColor: { default: colors.lineSoft, ':hover': colors.lineStrong },
    backgroundColor: 'transparent',
    fontFamily: 'inherit',
    fontSize: 11,
    color: { default: colors.textFaint, ':hover': colors.text },
    textAlign: 'left',
    cursor: 'pointer',
  },
  text: {
    borderWidth: 1,
    borderStyle: 'solid',
    borderRadius: 5,
    display: 'block',
    width: '100%',
    minHeight: 34,
    marginTop: 6,
    paddingBlock: 8,
    paddingInline: 10,
    overflow: 'hidden',
    resize: 'none',
    borderColor: { default: colors.lineSoft, ':focus': colors.brandLine },
    outline: 'none',
    backgroundColor: colors.input,
    fontFamily: 'inherit',
    fontSize: 12,
    lineHeight: 1.55,
    color: colors.textStrong,
  },
  review: { display: 'flex', flexDirection: 'column', gap: 4 },
  reviewRow: {
    borderWidth: 1,
    borderStyle: 'solid',
    borderRadius: 5,
    display: 'grid',
    gridTemplateColumns: 'minmax(120px, 180px) 1fr',
    gap: 12,
    paddingBlock: 6,
    paddingInline: 10,
    borderColor: { default: colors.menu, ':hover': colors.line },
    backgroundColor: colors.sunken,
    fontFamily: 'inherit',
    fontSize: 11,
    textAlign: 'left',
    color: 'inherit',
    cursor: 'pointer',
  },
  reviewTitle: { color: colors.textFaint },
  reviewText: { margin: 0, color: colors.text },
  reviewMissing: { color: colors.warn },
  hints: { display: 'flex', alignItems: 'center', gap: 5, flexShrink: 0, fontSize: 10, color: colors.textHint },
});
