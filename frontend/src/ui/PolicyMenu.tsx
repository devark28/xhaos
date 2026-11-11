// An agent's Approvals: how much runs without asking, one setting in its
// menu (the ⋯ beside the card's header). A ladder from least to most trust:
// Manual, Auto, Dangerously allow… (a timed override on top of the stored
// mode: when it ends or you pick Manual or Auto, the agent is back on that
// mode). Access and commits sit below. The card shows one label: "manual",
// "auto" or "danger 42m"; clicking it opens the menu.
// Only you can change it (the page is the owner's; MCP clients and agents
// cannot). A change applies from the agent's next tool call; calls already
// waiting for you stay as they are.
import { useState, type MouseEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { call } from './state/backend.ts';
import { get, toast, errorText, useApp, findAgentIn } from './state/store.ts';
import { idempotencyKey } from '../server-client.ts';
import { approvalOf, dangerHint, isPolicy, pickReview, policyLabel, policyMenu, policyText, reviewChoices, withField, type PolicyField } from '../policy.ts';
import { dangerLeft, dangerLeftText, dangerOff, dangerTitle, openDanger, useNow } from './Danger.tsx';
import type { Agent, Policy } from '../types.ts';

export async function setPolicy(agentId: number, policy: Policy) {
  await call('agent', 'SetPolicy', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentId, policy });
}

// editable: a real agent that is not terminated (sample cards have no server).
const editable = (agent: Agent): agent is Agent & { policy: Policy } => !agent.sample && agent.status !== 'terminated' && isPolicy(agent.policy);

// closeMenu shuts the menu or popover that holds target.
export const closeMenu = (target: HTMLElement) => { const d = target.closest('details[data-agent-menu]') as HTMLDetailsElement | null; if (d) d.open = false; };

// PolicyLabel is the card's one label: "manual", "auto", "⚠ danger 42m" or
// "read-only". It opens the Approvals choices right there (a popover with
// the same items as the ⋯ menu): Enter or Space opens it, Escape closes it
// (keys.ts closes every open [data-agent-menu]), so does a click elsewhere.
export function PolicyLabel({ agent }: { agent: Agent }) {
  const backend = useApp(s => s.backendVersion !== null);
  const ms = dangerLeft(agent, useNow(1_000));
  if (!backend || !editable(agent)) return null;
  const p = agent.policy;
  const mode = approvalOf(p, ms);
  const text = policyLabel(p, ms, dangerLeftText);
  const title = mode === 'danger' ? dangerTitle(agent, ms) : `Approvals: ${p.review} (${policyText(p)}). Click to change.`;
  return (
    <details data-agent-menu={`approvals-${agent.id}`} data-approvals-popover={agent.id} {...stylex.props(s.popRoot)}>
      <summary data-policy-label={agent.id} data-approval={mode} title={title} aria-label={`Approvals for agent #${agent.id}: ${text}`} aria-haspopup="menu"
        {...stylex.props(s.label, mode === 'auto' && p.access === 'rw' && s.labelAuto, mode === 'danger' && s.labelDanger)}>
        {mode === 'danger' && <span aria-hidden="true">⚠ </span>}{text}
      </summary>
      <div {...stylex.props(s.pop)}>
        <ApprovalItems agent={agent} where="label" close={closeMenu} />
      </div>
    </details>
  );
}

// ApprovalItems are the Approvals section of the agent menu. close shuts it.
export function ApprovalItems({ agent, close, where = 'menu' }: { agent: Agent; close(target: HTMLElement): void; where?: 'menu' | 'label' }) {
  const backend = useApp(s => s.backendVersion !== null);
  const parent = useApp(s => (agent.parentId ? findAgentIn(s, agent.parentId)?.policy : undefined));
  const ms = dangerLeft(agent, useNow(1_000));
  const [busy, setBusy] = useState(false);
  if (!backend || !editable(agent)) return null;
  const p = agent.policy;
  const mode = approvalOf(p, ms);
  const run = async (target: HTMLElement, work: () => Promise<void>) => {
    setBusy(true);
    try { await work(); close(target); }
    catch (error) { toast(errorText(error)); }
    finally { setBusy(false); }
  };
  const pick = (event: MouseEvent<HTMLElement>, review: Policy['review']) => {
    const plan = pickReview(p, ms > 0, review);
    if (!plan.endDanger && !plan.policy) { close(event.currentTarget); return; }
    void run(event.currentTarget, async () => {
      if (plan.endDanger) await call('danger', 'Set', { workspaceId: get().active, agentId: agent.id, off: true });
      if (plan.policy) await setPolicy(agent.id, plan.policy);
    });
  };
  const setField = (event: MouseEvent<HTMLElement>, field: PolicyField, value: string) => {
    if (p[field] === value) { close(event.currentTarget); return; }
    void run(event.currentTarget, () => setPolicy(agent.id, withField(p, field, value as never)));
  };
  const groups = policyMenu(p, isPolicy(parent) ? parent : undefined);
  const reviewAbove = groups.find(g => g.field === 'review')!.items;
  const radio = (on: boolean) => <span aria-hidden="true" {...stylex.props(s.radio, on && s.radioOn)} />;
  return (
    <div data-approvals={agent.id} data-approvals-in={where} role="group" aria-label={`Approvals for agent #${agent.id}`}>
      <span {...stylex.props(s.heading)}>Approvals{p.access === 'ro' ? ' · read-only: changes are refused' : ''}</span>
      {reviewChoices.map(c => {
        const above = reviewAbove.find(i => i.value === c.value)?.aboveParent;
        const stored = p.review === c.value;
        return (
          <Button key={c.value} variant="menu" role="menuitemradio" aria-checked={mode === c.value} disabled={busy} data-approval-choice={c.value}
            title={above ? `Above its parent #${agent.parentId}'s policy: you may still choose it.` : undefined}
            onClick={event => pick(event, c.value)} sx={s.item}>
            {radio(mode === c.value)}
            <span {...stylex.props(s.text)}>
              <span {...stylex.props(s.name, mode === c.value && s.nameOn)}>
                {c.label}
                {mode === 'danger' && stored && <span {...stylex.props(s.after)}> · back to this after danger</span>}
                {above && <span {...stylex.props(s.above)}> ↑ above parent</span>}
              </span>
              <span {...stylex.props(s.hint)}>{c.hint}</span>
            </span>
          </Button>
        );
      })}
      <Button variant="menu" role="menuitemradio" aria-checked={mode === 'danger'} disabled={busy} data-approval-choice="danger"
        onClick={event => { close(event.currentTarget); openDanger(agent.id); }} sx={s.item}>
        {radio(mode === 'danger')}
        <span {...stylex.props(s.text)}>
          <span {...stylex.props(s.name, s.nameDanger, mode === 'danger' && s.nameOn)}>Dangerously allow…{mode === 'danger' && <span {...stylex.props(s.after)}> · {dangerLeftText(ms)} left</span>}</span>
          <span {...stylex.props(s.hint)}>{mode === 'danger' ? 'or pick Manual or Auto to end it now' : dangerHint}</span>
        </span>
      </Button>
      {mode === 'danger' && (
        <Button variant="menu" data-danger-off={agent.id} disabled={busy} onClick={event => { close(event.currentTarget); void dangerOff(agent.id); }} sx={s.off}>
          Turn off dangerously allow <span {...stylex.props(s.after)}>· back to {p.review}</span>
        </Button>
      )}
      <div role="separator" {...stylex.props(s.divider)} />
      {groups.filter(g => g.field !== 'review').map(g => (
        <div key={g.field} data-policy-field={g.field} {...stylex.props(s.group)}>
          <span {...stylex.props(s.groupLabel)}>{g.label}</span>
          <div {...stylex.props(s.options)}>
            {g.items.map(item => (
              <Button key={item.value} variant="menu" chosen={item.chosen} role="menuitemradio" aria-checked={item.chosen} disabled={busy}
                data-policy-option={`${g.field}=${item.value}`} title={item.hint + (item.aboveParent ? ` Above its parent #${agent.parentId}'s policy: you may still choose it.` : '')}
                onClick={event => setField(event, g.field, item.value)} sx={s.option}>
                {item.label}{item.aboveParent && <span {...stylex.props(s.above)}> ↑</span>}
              </Button>
            ))}
          </div>
        </div>
      ))}
      <p {...stylex.props(s.note)}>Applies from its next tool call; calls already waiting for you stay as they are.</p>
    </div>
  );
}

const DANGER = '#c0392b';
const s = stylex.create({
  label: {
    flexShrink: 0,
    paddingBlock: 2,
    paddingInline: 6,
    marginInline: 4,
    borderWidth: 0,
    borderRadius: 3,
    fontFamily: 'ui-monospace, monospace',
    fontSize: 9,
    whiteSpace: 'nowrap',
    cursor: 'pointer',
    listStyle: 'none',
    userSelect: 'none',
    color: { default: colors.textFaint, ':hover': colors.text },
    backgroundColor: colors.chip,
  },
  popRoot: { position: 'relative', flexShrink: 0 },
  pop: {
    position: 'absolute',
    right: 0,
    top: 'calc(100% + 3px)',
    width: 'max-content',
    minWidth: 175,
    maxWidth: 'min(300px, calc(100vw - 32px))',
    padding: 4,
    backgroundColor: colors.selected,
    borderRadius: 4,
    boxShadow: `0 6px 20px ${colors.shadow}`,
    zIndex: 40,
  },
  off: { paddingLeft: 27, color: '#e06c5f', fontSize: 10.5 },
  labelAuto: { color: { default: colors.brand, ':hover': colors.brandStrong } },
  labelDanger: { fontWeight: 600, color: { default: '#fff', ':hover': '#fff' }, backgroundColor: DANGER },
  heading: { display: 'block', paddingTop: 4, paddingBottom: 2, paddingInline: 10, fontSize: 9.5, color: colors.textFaint },
  item: { display: 'flex', alignItems: 'flex-start', gap: 8, paddingBlock: 7 },
  radio: { width: 9, height: 9, marginTop: 2, flexShrink: 0, borderRadius: '50%', borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineBright, boxSizing: 'border-box' },
  radioOn: { borderWidth: 3, borderColor: colors.brand },
  text: { display: 'grid', gap: 2, minWidth: 0 },
  name: { color: colors.textSoft },
  nameOn: { color: colors.textStrong, fontWeight: 500 },
  nameDanger: { color: '#e06c5f' },
  after: { color: colors.textGhost, fontWeight: 400, fontSize: 9.5 },
  hint: { color: colors.textGhost, fontSize: 9.5, lineHeight: 1.4, whiteSpace: 'normal' },
  above: { color: colors.waiting, fontSize: 9.5 },
  divider: { height: 1, marginBlock: 4, marginInline: 6, backgroundColor: colors.line },
  group: { display: 'grid', gridTemplateColumns: '58px 1fr', alignItems: 'center', gap: 6, paddingInline: 10, paddingBlock: 2 },
  groupLabel: { fontSize: 9.5, color: colors.textFaint },
  options: { display: 'flex', gap: 3 },
  option: { width: 'auto', flexGrow: 1, flexBasis: 0, textAlign: 'center', paddingBlock: 5, paddingInline: 4, fontSize: 10, whiteSpace: 'nowrap' },
  note: { margin: 0, paddingInline: 10, paddingTop: 4, paddingBottom: 4, fontSize: 9.5, lineHeight: 1.4, color: colors.textGhost },
});
