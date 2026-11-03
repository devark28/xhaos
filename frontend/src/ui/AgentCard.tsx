// An agent's card: a header row (ID, role, title, status, menu) and, when
// open, its transcript and subagents. Subagents render the same way, nested.
import type { KeyboardEvent, MouseEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { hasPendingQuestions } from '../questions.ts';
import { colors, fonts, breakpoints } from './theme/tokens.stylex.ts';
import { Button, StatusDot } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { Transcript } from './Transcript.tsx';
import { DecisionButtons, needsDecision } from './Decisions.tsx';
import { WorkingLabel } from './Working.tsx';
import { S, toggleAgent, toggleRecipient, revealAgent, terminateAgent, stopAgent, showQuestions } from './store.ts';
import type { Agent } from '../types.ts';

export const labels = { idle: 'Idle', running: 'Running', waiting: 'Needs you', paused: 'Paused', complete: 'Complete', terminated: 'Terminated' };

// The status dot shown for an agent; idle, paused and terminated show none.
export function Badge({ agent, size }: { agent: Pick<Agent, 'status'> & Partial<Agent>; size?: number }) {
  if (['idle', 'paused', 'terminated'].includes(agent.status)) return null;
  const title = agent.questionBatch && hasPendingQuestions(agent as Agent) ? 'Questions need answers' : labels[agent.status];
  return <StatusDot status={agent.status} label={labels[agent.status]} title={title} size={size} />;
}

// ---- header ----------------------------------------------------------------
// One click toggles (after a short delay); a double-click attaches the agent
// to the composer instead. Keyboard activation (detail 0) toggles at once.
const clickTimers = new Map<number, ReturnType<typeof setTimeout>>();
const clearClickTimer = (id: number) => { clearTimeout(clickTimers.get(id)); clickTimers.delete(id); };
function onRowClick(event: MouseEvent, id: number) {
  clearClickTimer(id);
  if (event.detail >= 2) { if (event.detail === 2) toggleRecipient(id); return; }
  if (event.detail === 0) { toggleAgent(id); return; }
  clickTimers.set(id, setTimeout(() => { clickTimers.delete(id); toggleAgent(id); }, 300));
}
function onRowKey(event: KeyboardEvent, id: number) {
  if (event.altKey && event.key === 'Enter') { event.preventDefault(); event.stopPropagation(); clearClickTimer(id); toggleRecipient(id); }
}

function HeaderButton({ agent, closed, nested }: { agent: Agent; closed: boolean; nested: boolean }) {
  const attached = S.selected.has(agent.id);
  return (
    <button type="button" data-collapse={agent.id} data-agent-row-id={agent.id} aria-keyshortcuts="Alt+Enter" title="Double-click to attach or detach; Alt+Enter when focused" aria-expanded={!closed}
      aria-label={`${closed ? 'Expand' : 'Collapse'} agent ${agent.id}: ${agent.title}${attached ? ', attached to composer' : ''}`}
      onClick={event => onRowClick(event, agent.id)} onKeyDown={event => onRowKey(event, agent.id)}
      {...stylex.props(header.row, nested && header.rowNested)}>
      <span aria-hidden="true" {...stylex.props(header.id, nested && header.idNested, attached && header.idAttached)}>#{agent.id}</span>
      <span data-role {...stylex.props(header.role, nested && header.roleNested)}>{agent.role || agent.modelLabel || ''}{agent.sample ? ' · sample' : ''}</span>
      <span data-title {...stylex.props(header.title, nested && header.titleNested)}>{agent.title}</span>
    </button>
  );
}

function SubagentChips({ agent }: { agent: Agent }) {
  const subs = (agent.children || []).filter(child => !child.archivedAt);
  if (!subs.length) return null;
  return (
    <div aria-label="Subagents" {...stylex.props(header.chips)}>
      {subs.map(child => (
        <button key={child.id} type="button" data-reveal={child.id} data-parent={agent.id} aria-expanded={!S.collapsed.has(child.id)}
          title={`${S.collapsed.has(child.id) ? 'Expand' : 'Collapse'} subagent #${child.id}: ${child.title}`} onClick={() => revealAgent(child.id)}
          {...stylex.props(header.chip)}>
          <Badge agent={child} size={5} />#{child.id}
        </button>
      ))}
    </div>
  );
}

function Disclosure({ agent, closed }: { agent: Agent; closed: boolean }) {
  return (
    <Button variant="icon" data-collapse={agent.id} aria-expanded={!closed} aria-label={`${closed ? 'Expand' : 'Collapse'} agent ${agent.id}`}
      onClick={() => { clearClickTimer(agent.id); toggleAgent(agent.id); }} sx={header.disclosure}>
      <span {...stylex.props(header.chevron, !closed && header.chevronOpen)}><Icon name="arrow" size={16} /></span>
    </Button>
  );
}

function AgentMenu({ agent }: { agent: Agent }) {
  const close = (event: MouseEvent<HTMLElement>) => { const menu = event.currentTarget.closest('details'); if (menu) menu.open = false; };
  return (
    <details data-agent-menu={agent.id} {...stylex.props(menu.root)}>
      <summary aria-label={`Actions for agent ${agent.id}`} title="Agent actions" {...stylex.props(menu.summary)}>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.7" /><circle cx="12" cy="12" r="1.7" /><circle cx="19" cy="12" r="1.7" /></svg>
      </summary>
      <div {...stylex.props(menu.items)}>
        <Button variant="menu" data-menu-target={agent.id} onClick={event => { close(event); toggleRecipient(agent.id); }}>{S.selected.has(agent.id) ? 'Detach from' : 'Attach to'} composer</Button>
        {S.backend && !agent.sample && agent.status === 'running' && <Button variant="menu" data-stop={agent.id} onClick={event => { close(event); stopAgent(agent.id); }}>Stop</Button>}
        <Button variant="menu" data-terminate={agent.id} onClick={event => { close(event); terminateAgent(agent.id); }}>Terminate agent</Button>
      </div>
    </details>
  );
}

function QuestionSummary({ agent }: { agent: Agent }) {
  if (!hasPendingQuestions(agent)) return null;
  return (
    <Button data-show-questions={agent.id} onClick={() => showQuestions(agent.id)} sx={body.questions}>
      Questions waiting — answer in composer <Icon name="arrow" size={12} />
    </Button>
  );
}

// The header row, shared by cards and subagents.
function Header({ agent, closed, nested }: { agent: Agent; closed: boolean; nested: boolean }) {
  return (
    <div data-agent-header={agent.id} data-attached={S.selected.has(agent.id) || undefined} {...stylex.props(header.root, nested && header.rootNested, S.selected.has(agent.id) && header.attached)}>
      {nested && <span aria-hidden="true" {...stylex.props(header.branch)} />}
      <HeaderButton agent={agent} closed={closed} nested={nested} />
      <WorkingLabel agent={agent} />
      <SubagentChips agent={agent} />
      {!['idle', 'paused', 'terminated'].includes(agent.status) && <span {...stylex.props(header.status, nested && header.statusNested)}><Badge agent={agent} /></span>}
      <Disclosure agent={agent} closed={closed} />
      {nested && needsDecision(agent) && <DecisionButtons agent={agent} />}
      <AgentMenu agent={agent} />
    </div>
  );
}

// ---- cards -----------------------------------------------------------------
function Children({ agent, nested }: { agent: Agent; nested: boolean }) {
  const visible = (agent.children || []).filter(child => !child.archivedAt);
  if (!visible.length) return null;
  return (
    <div {...stylex.props(body.children, nested ? body.childrenNested : body.childrenTop)}>
      {visible.map(child => <Child key={child.id} agent={child} />)}
    </div>
  );
}

function Child({ agent }: { agent: Agent }) {
  const closed = S.collapsed.has(agent.id);
  return (
    <div {...stylex.props(body.child)}>
      <Header agent={agent} closed={closed} nested />
      {!closed && <>
        <Transcript agent={agent} nested />
        <div {...stylex.props(body.childExtras)}><QuestionSummary agent={agent} /></div>
        <Children agent={agent} nested />
      </>}
    </div>
  );
}

export function AgentCard({ agent }: { agent: Agent }) {
  const closed = S.collapsed.has(agent.id);
  return (
    <article data-agent-card={agent.id} {...stylex.props(body.card)}>
      <Header agent={agent} closed={closed} nested={false} />
      {!closed && (
        <div {...stylex.props(body.body)}>
          <Transcript agent={agent} />
          <QuestionSummary agent={agent} />
          <Children agent={agent} nested={false} />
          {needsDecision(agent) && <div data-agent-controls {...stylex.props(body.controls)}><DecisionButtons agent={agent} /></div>}
        </div>
      )}
    </article>
  );
}

const header = stylex.create({
  root: { display: 'flex', alignItems: 'center' },
  rootNested: { position: 'relative', minHeight: 32 },
  attached: { backgroundColor: colors.attached },
  branch: { position: 'absolute', width: 13, borderTopWidth: 1, borderTopStyle: 'solid', borderTopColor: colors.lineSoft, left: -14, top: '50%' },
  row: {
    flexGrow: 1,
    flexShrink: 1,
    flexBasis: 'auto',
    minWidth: 0,
    minHeight: 36,
    display: 'flex',
    gap: { default: 8, [breakpoints.tiny]: 6 },
    alignItems: 'center',
    textAlign: 'left',
    paddingBlock: 8,
    paddingRight: 10,
    paddingLeft: 0,
    overflow: 'hidden',
    userSelect: 'none',
    cursor: 'pointer',
    color: 'inherit',
    fontFamily: 'inherit',
    fontSize: 'inherit',
    borderWidth: 0,
    backgroundColor: { default: 'transparent', ':hover': colors.hover, ':focus-visible': '#2b2b2b' },
  },
  rowNested: { alignSelf: 'stretch', minHeight: 32, gap: 7, paddingBlock: 7, paddingRight: 8, borderRadius: 2, fontSize: 11 },
  id: {
    display: 'inline-flex',
    alignItems: 'center',
    minWidth: 40,
    minHeight: 20,
    paddingInline: 9,
    flexShrink: 0,
    color: colors.textFaint,
    fontFamily: 'ui-monospace, monospace',
    fontSize: 10,
  },
  idNested: { minWidth: 36, minHeight: 18, paddingInline: 7, fontSize: 9 },
  idAttached: { color: colors.textStrong },
  role: { color: colors.textFaint, fontSize: { default: 10, [breakpoints.tiny]: 9 }, flexShrink: 1, minWidth: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' },
  roleNested: { display: { default: null, [breakpoints.tiny]: 'none' } },
  title: { fontSize: { default: 12, [breakpoints.tiny]: 11 }, fontWeight: 450, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', flexGrow: 1, flexShrink: 1, flexBasis: 'auto', minWidth: 0 },
  titleNested: { fontSize: 11, fontWeight: 400, color: colors.textSoft },
  chips: { display: 'flex', alignItems: 'center', gap: 4, maxWidth: { default: '32%', [breakpoints.narrow]: '25%' }, overflowX: 'auto', scrollbarWidth: 'none', flexShrink: 0 },
  chip: {
    display: 'flex',
    alignItems: 'center',
    gap: 5,
    paddingBlock: 4,
    paddingInline: 6,
    backgroundColor: colors.chip,
    borderWidth: 0,
    borderRadius: 3,
    fontFamily: 'ui-monospace, monospace',
    fontSize: 9,
    whiteSpace: 'nowrap',
    color: colors.textMuted,
    cursor: 'pointer',
  },
  status: { display: 'flex', alignItems: 'center', gap: 8, paddingRight: 8, paddingLeft: 16, flexShrink: 0 },
  statusNested: { paddingLeft: 8 },
  disclosure: { flexShrink: 0, width: 32, height: 34, padding: 0 },
  chevron: { display: 'inline-flex', color: colors.textFaint },
  chevronOpen: { transform: 'rotate(90deg)' },
});

const menu = stylex.create({
  root: { position: 'relative', flexShrink: 0, marginRight: 3 },
  summary: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    width: 32,
    height: 34,
    cursor: 'pointer',
    listStyle: 'none',
    borderRadius: 3,
    color: { default: colors.textDim, ':hover': colors.text },
    backgroundColor: { default: null, ':hover': colors.hover },
  },
  items: {
    position: 'absolute',
    right: 0,
    top: 'calc(100% + 3px)',
    minWidth: 175,
    padding: 4,
    backgroundColor: colors.selected,
    borderRadius: 4,
    boxShadow: `0 6px 20px ${colors.shadow}`,
    zIndex: 40,
  },
});

const body = stylex.create({
  card: { borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, borderRadius: 4, backgroundColor: colors.card },
  body: { borderTopWidth: 1, borderTopStyle: 'solid', borderTopColor: '#303030', padding: 10 },
  children: { display: 'flex', flexDirection: 'column', gap: 6, borderLeftWidth: 1, borderLeftStyle: 'solid', borderLeftColor: colors.lineSoft, paddingTop: 1 },
  childrenTop: { marginTop: 10, marginLeft: 5 },
  childrenNested: { marginTop: 7, marginRight: 9, marginBottom: 9, marginLeft: 12 },
  child: { position: 'relative', marginLeft: 12, borderWidth: 1, borderStyle: 'solid', borderColor: '#363636', backgroundColor: colors.raised, borderRadius: 3 },
  childExtras: { paddingLeft: 9 },
  controls: { display: 'flex', justifyContent: 'flex-end', gap: 9, marginTop: 8 },
  questions: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    paddingBlock: 8,
    paddingInline: 0,
    fontSize: 10,
    color: { default: colors.brand, ':hover': colors.brandStrong },
    fontFamily: fonts.sans,
  },
});
