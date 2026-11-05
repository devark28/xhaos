// Questions an agent asked, answered one at a time in the composer area.
import { useState, type KeyboardEvent, type ReactNode } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button, Kbd, StatusDot } from './kit.tsx';
import { activeAgents } from '../model.ts';
import { hasPendingQuestions, OWN, ownChosen, finalAnswer } from '../questions.ts';
import { useApp, workspaceOf, afterCommit } from './state/store.ts';
import { questionStepIn, setQuestionStep, setQuestionAgent, setAnswer, advanceOrSubmit, dismissQuestions } from './state/questions.ts';
import type { Agent, Answer, Question as QuestionData } from '../types.ts';

const emptyAnswer: Answer = { choices: [], text: '' };
const tabTitle = (q: QuestionData) => q.title || q.prompt.split(' ').slice(0, 4).join(' ');
const draftOf = (agent: Agent, id: string): Answer => ({ ...emptyAnswer, ...agent.questionBatch?.draft?.[id] });

function Mark({ square = false, pencil = false, chosen = false }) {
  return (
    <span aria-hidden="true" {...stylex.props(styles.mark, square && styles.markSquare, pencil && !chosen && styles.markPencil, chosen && styles.markChosen)}>
      {chosen && <span {...stylex.props(styles.markDot, square && styles.markDotSquare)} />}
    </span>
  );
}

// Choice is one row of a question: an agent's option, or your own answer.
// Its input is visually hidden; ↑ ↓ move between rows, Space chooses.
function Choice({ question, id, chosen, onPick, pencil = false, children }: { question: QuestionData; id: string; chosen: boolean; onPick(): void; pencil?: boolean; children: ReactNode }) {
  const [ring, setRing] = useState(false);
  return (
    <label data-option={id} data-chosen={chosen || undefined} {...stylex.props(styles.option, chosen && styles.optionChosen, ring && styles.optionFocus)}>
      <input data-choice-input type={question.multiple ? 'checkbox' : 'radio'} name={`choice:${question.id}`} value={id} checked={chosen} onChange={onPick}
        onFocus={event => setRing(event.currentTarget.matches(':focus-visible'))} onBlur={() => setRing(false)} {...stylex.props(styles.hiddenInput)} />
      <Mark square={question.multiple} pencil={pencil} chosen={chosen} />
      <span {...stylex.props(styles.optionText)}>{children}</span>
    </label>
  );
}

function Question({ agent, question }: { agent: Agent; question: QuestionData }) {
  const answer = draftOf(agent, question.id);
  const hasOptions = Boolean(question.options?.length);
  const own = ownChosen(question, answer);
  const agentChoices = answer.choices.filter(c => c !== OWN);
  const set = (choices: string[], text = answer.text) => setAnswer(agent.id, question.id, { choices, text });
  const pick = (id: string) => set(question.multiple
    ? (agentChoices.includes(id) ? agentChoices.filter(c => c !== id) : [...agentChoices, id]).concat(own ? [OWN] : [])
    : [id]);
  const focusText = () => afterCommit(() => document.querySelector<HTMLElement>('[data-answer-text]')?.focus({ preventScroll: true }));
  const pickOwn = () => {
    if (question.multiple) set(own ? agentChoices : [...agentChoices, OWN]);
    else set([OWN]);
    if (!own) focusText();
  };
  const fit = (el: HTMLTextAreaElement | null) => { if (!el) return; el.style.height = 'auto'; el.style.height = `${el.scrollHeight}px`; };
  const text = (
    <textarea data-answer-text ref={fit} name={`text:${question.id}`} aria-label={`Your answer: ${question.prompt}`} rows={1} placeholder="Your answer…" value={answer.text}
      onChange={event => { fit(event.target); set(question.multiple || !hasOptions ? (own ? answer.choices : [...agentChoices, OWN]) : [OWN], event.target.value); }}
      onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); document.querySelector<HTMLElement>(`[data-option="${OWN}"] input`)?.focus(); } }}
      {...stylex.props(styles.text)} />
  );
  return (
    <fieldset {...stylex.props(styles.question)}>
      <legend {...stylex.props(styles.prompt)}>{question.prompt}{question.required === false && <span {...stylex.props(styles.optional)}>optional</span>}
        {hasOptions && <span {...stylex.props(styles.optional)}>{question.multiple ? 'choose any' : 'choose one'}</span>}</legend>
      {hasOptions ? (
        <div role={question.multiple ? 'group' : 'radiogroup'} aria-label={question.prompt} {...stylex.props(styles.options)}>
          {question.options.map(option => (
            <Choice key={option.id} question={question} id={option.id} chosen={agentChoices.includes(option.id)} onPick={() => pick(option.id)}>
              <span {...stylex.props(styles.optionLabel)}>{option.label}</span>
              {option.description && <small {...stylex.props(styles.optionDescription)}>{option.description}</small>}
            </Choice>
          ))}
          <Choice question={question} id={OWN} chosen={own} onPick={pickOwn} pencil>
            <span {...stylex.props(styles.optionLabel, !own && styles.ownLabel)}>Your own answer</span>
          </Choice>
          {own && text}
        </div>
      ) : text}
    </fieldset>
  );
}

// summary is what a question's row shows while closed: the answer, or why not.
function summary(q: QuestionData, a: Answer) {
  const f = finalAnswer(q, a);
  const chosen = (q.options || []).filter(option => f.choices.includes(option.id)).map(option => option.label);
  return [...chosen, f.text].filter(Boolean).join(' · ');
}

// QuestionInbox: one agent's questions as an accordion, one open at a time.
// Closed rows show their answers, so the last step (all closed) is the
// review; Enter goes to the next question, then sends.
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
  const last = batch.questions.length;
  // ← → previous and next question from anywhere but the text box (radio
  // buttons would otherwise take the arrows); ↑ ↓ move between choices
  // without choosing; Space chooses; Enter goes on.
  const onKeyDown = (event: KeyboardEvent<HTMLFormElement>) => {
    const target = event.target as HTMLElement;
    const inText = target.tagName === 'TEXTAREA';
    if (!inText && !event.altKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
      event.preventDefault(); setQuestionStep(agent, step + (event.key === 'ArrowRight' ? 1 : -1)); return;
    }
    if (!inText && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
      const inputs = [...event.currentTarget.querySelectorAll<HTMLElement>('[data-choice-input], [data-question-row]')];
      if (!inputs.length) return;
      event.preventDefault();
      const at = inputs.indexOf(target);
      const next = at < 0 ? (event.key === 'ArrowDown' ? 0 : inputs.length - 1) : Math.max(0, Math.min(inputs.length - 1, at + (event.key === 'ArrowDown' ? 1 : -1)));
      inputs[next].focus();
      return;
    }
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && target.tagName !== 'BUTTON') { event.preventDefault(); advanceOrSubmit(agent); }
  };
  return (
    <div {...stylex.props(styles.root)}>
      <div {...stylex.props(styles.head)}>
        <StatusDot status="waiting" />
        <span {...stylex.props(styles.agentId)}>#{agent.id}</span>
        <span {...stylex.props(styles.agentTitle)}>{agent.title}</span>
        <span {...stylex.props(styles.progress)}>{question ? `${step + 1} / ${last}` : 'Review'}</span>
        {backend && !agent.sample && <Button data-dismiss-questions={agent.id} title="Tell the agent you won't answer" onClick={() => dismissQuestions(agent)} sx={styles.next}>Dismiss</Button>}
        {others.length > 0 && (
          <Button onClick={() => setQuestionAgent(others[0].id)} sx={styles.next}>{others.length} more waiting →</Button>
        )}
      </div>
      <form tabIndex={0} aria-label={question ? `Question ${step + 1} of ${last}` : 'Review answers'}
        data-questions-agent={agent.id} data-question-batch={batch.id} data-question-step={step} data-question-count={last} data-question-id={question?.id || ''}
        onSubmit={event => { event.preventDefault(); advanceOrSubmit(agent); }} onKeyDown={onKeyDown} {...stylex.props(styles.body)}>
        {batch.questions.map((q, index) => {
          const isOpen = index === step;
          const text = summary(q, draftOf(agent, q.id));
          return (
            <section key={q.id} data-question={q.id} data-open={isOpen || undefined} {...stylex.props(styles.row, isOpen && styles.rowOpen)}>
              <button type="button" data-question-row={index} data-question-owner={agent.id} aria-expanded={isOpen}
                onClick={() => setQuestionStep(agent, isOpen ? last : index)} {...stylex.props(styles.rowHead)}>
                <span {...stylex.props(styles.rowNumber)}>{text ? <span {...stylex.props(styles.tick)}>✓</span> : index + 1}</span>
                <span {...stylex.props(styles.rowTitle)}>{tabTitle(q)}</span>
                {!isOpen && <span {...stylex.props(styles.rowAnswer, !text && q.required !== false && styles.reviewMissing)}>{text || (q.required === false ? 'Skipped' : 'Not answered')}</span>}
              </button>
              {isOpen && <div {...stylex.props(styles.rowBody)}><Question key={`${agent.id}:${q.id}`} agent={agent} question={q} /></div>}
            </section>
          );
        })}
        <div {...stylex.props(styles.send)}>
          <Button type="submit" data-question-next>{question ? (step + 1 < last ? 'Next question' : 'Review') : `Send answers to #${agent.id}`}</Button>
        </div>
      </form>
      <div aria-hidden="true" {...stylex.props(styles.hints)}>
        {question
          ? <><Kbd>↑</Kbd><Kbd>↓</Kbd> choices <Kbd>Space</Kbd> choose <Kbd>Enter</Kbd> next <Kbd>←</Kbd><Kbd>→</Kbd> questions <Kbd>Esc</Kbd> leave text</>
          : <><Kbd>Enter</Kbd> send answers to #{agent.id} <Kbd>←</Kbd> back to the questions</>}
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
  row: { borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, borderRadius: 5, marginBottom: 4 },
  rowOpen: { borderColor: colors.line },
  rowHead: { display: 'flex', alignItems: 'baseline', gap: 8, width: '100%', paddingBlock: 6, paddingInline: 8, borderWidth: 0, borderRadius: 5, backgroundColor: { default: 'transparent', ':hover': colors.hover }, textAlign: 'left', fontFamily: 'inherit', fontSize: 11, color: 'inherit', cursor: 'pointer' },
  rowNumber: { width: 14, flexShrink: 0, fontSize: 10, color: colors.textGhost, textAlign: 'center' },
  rowTitle: { flexShrink: 0, maxWidth: '45%', overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis', color: colors.textSoft },
  rowAnswer: { minWidth: 0, overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis', color: colors.text },
  rowBody: { paddingInline: 10, paddingBottom: 10 },
  send: { display: 'flex', justifyContent: 'flex-end', marginTop: 6 },
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
  ownLabel: { color: colors.textFaint },
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
  reviewMissing: { color: colors.warn },
  hints: { display: 'flex', alignItems: 'center', gap: 5, flexShrink: 0, fontSize: 10, color: colors.textHint },
});
