// An agent's card: a header row (ID, project, title, status, menu) and, when
// open, its transcript, subagents, and a footer with the model. Subagents
// render the same way, nested.
import { memo, type KeyboardEvent, type MouseEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { hasPendingQuestions } from '../questions.ts';
import { colors, fonts, breakpoints } from './theme/tokens.stylex.ts';
import { Button, StatusDot } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { Transcript } from './Transcript.tsx';
import { DecisionButtons, useNeedsDecision } from './Decisions.tsx';
import { clearClick, onAttachKey, onToggleClick } from './clicks.ts';
import { useApp, findAgentIn } from './state/store.ts';
import { toggleAgent, toggleRecipient, revealAgent, terminateAsk, stopAgent } from './state/agents.ts';
import { showQuestions } from './state/panels.ts';
import type { Agent } from '../types.ts';
import { ConfirmDialog } from './Confirm.tsx';
import { terminatePrompt } from '../terminate-confirm.ts';
import { TitleProposal } from './TitleProposal.tsx';
import { ApprovalItems, PolicyLabel, closeMenu } from './PolicyMenu.tsx';

export const labels = { idle: 'Idle', running: 'Running', waiting: 'Needs you', paused: 'Paused', complete: 'Complete', terminated: 'Terminated' };

// The status dot shown for an agent; idle, paused and terminated show none.
export function Badge({ agent, size }: { agent: Pick<Agent, 'status'> & Partial<Agent>; size?: number }) {
  if (['idle', 'paused', 'terminated'].includes(agent.status)) return null;
  const title = agent.questionBatch && hasPendingQuestions(agent as Agent) ? 'Questions need answers' : labels[agent.status];
  return <StatusDot status={agent.status} label={labels[agent.status]} title={title} size={size} />;
}

// ---- header ----------------------------------------------------------------
// Click toggles the card; double-click, Ctrl/Cmd+click, or Alt+Enter attaches it (clicks.ts).
const clickKey = (id: number) => `agent:${id}`;
const onRowClick = (event: MouseEvent, id: number) => onToggleClick(event, clickKey(id), () => toggleAgent(id), () => toggleRecipient(id));
const onRowKey = (event: KeyboardEvent, id: number) => onAttachKey(event, clickKey(id), () => toggleRecipient(id));

function HeaderButton({ agent, closed, nested, attached }: { agent: Agent; closed: boolean; nested: boolean; attached: boolean }) {
  // The project is always named; the role and model are not (the model is
  // in the footer of the open card).
  const project = useApp(s => s.projects.find(p => p.id === agent.projectId)?.name);
  return (
    <button type="button" data-collapse={agent.id} data-agent-row-id={agent.id} aria-keyshortcuts="Alt+Enter" title="Click to open or close; double-click or Ctrl+click (Alt+Enter when focused) to attach to the composer" aria-expanded={!closed}
      aria-label={`${closed ? 'Expand' : 'Collapse'} agent ${agent.id}: ${agent.title}${attached ? ', attached to composer' : ''}`}
      onClick={event => onRowClick(event, agent.id)} onKeyDown={event => onRowKey(event, agent.id)}
      {...stylex.props(header.row, nested && header.rowNested)}>
      <span aria-hidden="true" {...stylex.props(header.id, nested && header.idNested, attached && header.idAttached)}>#{agent.id}</span>
      {/* The project only when there are several; the model lives in the footer. */}
      {(project && !nested) || agent.sample ? <span data-role {...stylex.props(header.role, nested && header.roleNested)}>{[!nested && project, agent.sample && 'sample'].filter(Boolean).join(' · ')}</span> : null}
      <span data-title {...stylex.props(header.title, nested && header.titleNested)}>{agent.title}</span>
    </button>
  );
}

function SubagentChips({ agent }: { agent: Agent }) {
  const collapsed = useApp(s => s.collapsed);
  const subs = (agent.children || []).filter(child => !child.archivedAt);
  if (!subs.length) return null;
  return (
    <div aria-label="Subagents" {...stylex.props(header.chips)}>
      {subs.map(child => (
        <button key={child.id} type="button" data-reveal={child.id} data-parent={agent.id} aria-expanded={!collapsed.has(child.id)}
          title={`${collapsed.has(child.id) ? 'Expand' : 'Collapse'} subagent #${child.id}: ${child.title}`} onClick={() => revealAgent(child.id)}
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
      onClick={() => { clearClick(clickKey(agent.id)); toggleAgent(agent.id); }} sx={header.disclosure}>
      <span {...stylex.props(header.chevron, !closed && header.chevronOpen)}><Icon name="arrow" size={16} /></span>
    </Button>
  );
}

function AgentMenu({ agent, attached }: { agent: Agent; attached: boolean }) {
  const backend = useApp(s => s.backendVersion !== null);
  const close = (event: MouseEvent<HTMLElement>) => { const menu = event.currentTarget.closest('details'); if (menu) menu.open = false; };
  return (
    <details data-agent-menu={agent.id} {...stylex.props(menu.root)}>
      <summary aria-label={`Actions for agent ${agent.id}`} title="Agent actions" {...stylex.props(menu.summary)}>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.7" /><circle cx="12" cy="12" r="1.7" /><circle cx="19" cy="12" r="1.7" /></svg>
      </summary>
      <div {...stylex.props(menu.items)}>
        <Button variant="menu" data-menu-target={agent.id} onClick={event => { close(event); toggleRecipient(agent.id); }}>{attached ? 'Detach from' : 'Attach to'} composer</Button>
        {backend && !agent.sample && agent.status === 'running' && <Button variant="menu" data-stop={agent.id} onClick={event => { close(event); stopAgent(agent.id); }}>Stop</Button>}
        {/* Approvals: manual, auto, dangerously allow; access and commits (PolicyMenu.tsx). */}
        {backend && !agent.sample && agent.status !== 'terminated' && <>
          <div role="separator" {...stylex.props(menu.divider)} />
          <ApprovalItems agent={agent} close={closeMenu} />
        </>}
        {/* Set apart from the approvals above it, and it asks first. */}
        <div role="separator" {...stylex.props(menu.divider)} />
        <Button variant="menu" data-terminate={agent.id} onClick={event => { close(event); terminateAsk.ask(agent.id); }}>Terminate agent…</Button>
      </div>
    </details>
  );
}

// The dialog behind the menu's Terminate agent…: Cancel has focus, so a
// stray Enter does nothing; Escape and a click outside cancel too.
export function TerminateDialog() {
  const agent = useApp(s => (s.terminateAsk === null ? undefined : findAgentIn(s, s.terminateAsk)));
  const prompt = agent ? terminatePrompt(agent) : { title: '', text: '' };
  return <ConfirmDialog open={agent !== undefined} title={prompt.title} text={prompt.text} action="Terminate" focusCancel
    onConfirm={terminateAsk.confirm} onCancel={terminateAsk.cancel} />;
}

function QuestionSummary({ agent }: { agent: Agent }) {
  if (!hasPendingQuestions(agent)) return null;
  return (
    <Button data-show-questions={agent.id} onClick={() => showQuestions(agent.id)} sx={body.questions}>
      Questions waiting — answer them <Icon name="arrow" size={12} />
    </Button>
  );
}

// The header row, shared by cards and subagents.
function Header({ agent, closed, nested }: { agent: Agent; closed: boolean; nested: boolean }) {
  const attached = useApp(s => s.selected.has(agent.id));
  const decision = useNeedsDecision(agent);
  return (
    <div data-agent-header={agent.id} data-attached={attached || undefined} {...stylex.props(header.root, nested && header.rootNested, attached && header.attached)}>
      {nested && <span aria-hidden="true" {...stylex.props(header.branch)} />}
      <HeaderButton agent={agent} closed={closed} nested={nested} attached={attached} />
      <SubagentChips agent={agent} />
      <PolicyLabel agent={agent} />
      {!['idle', 'paused', 'terminated'].includes(agent.status) && <span {...stylex.props(header.status, nested && header.statusNested)}><Badge agent={agent} /></span>}
      <Disclosure agent={agent} closed={closed} />
      {nested && decision && <DecisionButtons agent={agent} />}
      <AgentMenu agent={agent} attached={attached} />
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
  const closed = useApp(s => s.collapsed.has(agent.id));
  return (
    <div {...stylex.props(body.child)}>
      <Header agent={agent} closed={closed} nested />
      <TitleProposal agent={agent} nested />
      {!closed && <>
        <Transcript agent={agent} nested />
        <div {...stylex.props(body.childExtras)}><QuestionSummary agent={agent} /></div>
        <Children agent={agent} nested />
      </>}
    </div>
  );
}

// The model, at the foot of an open card. Placement experiment: sample #9012
// shows it on the right, every other card on the left.
function Footer({ agent }: { agent: Agent }) {
  const model = agent.modelLabel || agent.model;
  if (!model) return null;
  const right = agent.id === 9012;
  return <div data-agent-footer={agent.id} data-side={right ? 'right' : 'left'} {...stylex.props(body.footer, right && body.footerRight)} title="Model">{model}</div>;
}

// Memoized: an agent object only changes when its own subtree does, so a
// change elsewhere does not re-render this card.
export const AgentCard = memo(function AgentCard({ agent }: { agent: Agent }) {
  const closed = useApp(s => s.collapsed.has(agent.id));
  const decision = useNeedsDecision(agent);
  return (
    <article data-agent-card={agent.id} {...stylex.props(body.card)}>
      <Header agent={agent} closed={closed} nested={false} />
      <TitleProposal agent={agent} />
      {!closed && (
        <div {...stylex.props(body.body)}>
          <Transcript agent={agent} />
          <QuestionSummary agent={agent} />
          <Children agent={agent} nested={false} />
          {decision && <div data-agent-controls {...stylex.props(body.controls)}><DecisionButtons agent={agent} /></div>}
          <Footer agent={agent} />
        </div>
      )}
    </article>
  );
});

const header = stylex.create({
  // The whole header row highlights on hover, not just the title button.
  root: { display: 'flex', alignItems: 'center', borderRadius: 3, backgroundColor: { default: 'transparent', ':hover': colors.hover } },
  rootNested: { position: 'relative', minHeight: 32 },
  attached: { backgroundColor: { default: colors.attached, ':hover': colors.attached } },
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
    backgroundColor: { default: 'transparent', ':focus-visible': colors.selected },
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
  // The project keeps its name (up to 40% of the row); the title gives way first.
  role: { color: colors.textFaint, fontSize: { default: 10, [breakpoints.tiny]: 9 }, flexShrink: 0, maxWidth: '40%', minWidth: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' },
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
    width: 'max-content',
    maxWidth: 'min(300px, calc(100vw - 32px))',
    padding: 4,
    backgroundColor: colors.selected,
    borderRadius: 4,
    boxShadow: `0 6px 20px ${colors.shadow}`,
    zIndex: 40,
  },
  divider: { height: 1, marginBlock: 4, marginInline: 6, backgroundColor: colors.line },
});

const body = stylex.create({
  footer: { display: 'flex', fontSize: 10, color: colors.textGhost, paddingTop: 6, paddingInline: 2 },
  footerRight: { justifyContent: 'flex-end' },
  card: { borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, borderRadius: 4, backgroundColor: colors.card },
  body: { borderTopWidth: 1, borderTopStyle: 'solid', borderTopColor: colors.line, padding: 10 },
  children: { display: 'flex', flexDirection: 'column', gap: 6, borderLeftWidth: 1, borderLeftStyle: 'solid', borderLeftColor: colors.lineSoft, paddingTop: 1 },
  childrenTop: { marginTop: 10, marginLeft: 5 },
  childrenNested: { marginTop: 7, marginRight: 9, marginBottom: 9, marginLeft: 12 },
  child: { position: 'relative', marginLeft: 12, borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, backgroundColor: colors.raised, borderRadius: 3 },
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
