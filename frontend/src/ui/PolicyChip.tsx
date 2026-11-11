// The policy chip on an agent's header ("rw · manual · commits review"): it
// opens a menu where you change the agent's policy. Only you can (the page
// is the owner's; MCP clients and agents cannot). A change applies from the
// agent's next tool call; calls already waiting for you stay as they are.
import { useState, type MouseEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { call } from './state/backend.ts';
import { get, toast, errorText, useApp, findAgentIn } from './state/store.ts';
import { idempotencyKey } from '../server-client.ts';
import { isPolicy, policyMenu, policyText, withField, type PolicyField } from '../policy.ts';
import type { Agent, Policy } from '../types.ts';

export async function setPolicy(agentId: number, policy: Policy) {
  await call('agent', 'SetPolicy', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentId, policy });
}

export function PolicyChip({ agent, nested }: { agent: Agent; nested: boolean }) {
  const backend = useApp(s => s.backendVersion !== null);
  const parent = useApp(s => (agent.parentId ? findAgentIn(s, agent.parentId)?.policy : undefined));
  const [busy, setBusy] = useState(false);
  if (!backend || agent.sample || !isPolicy(agent.policy) || agent.status === 'terminated') return null;
  const p = agent.policy;
  const choose = async (event: MouseEvent<HTMLElement>, field: PolicyField, value: string) => {
    const menu = event.currentTarget.closest('details');
    const next = withField(p, field, value as never);
    if (next[field] === p[field]) return;
    setBusy(true);
    try {
      await setPolicy(agent.id, next);
      if (menu) menu.open = false;
    } catch (error) { toast(errorText(error)); }
    finally { setBusy(false); }
  };
  const groups = policyMenu(p, isPolicy(parent) ? parent : undefined);
  return (
    <details data-policy-chip={agent.id} {...stylex.props(s.root)}>
      <summary title={`Policy: ${policyText(p)}. Click to change it.`} aria-label={`Policy of agent ${agent.id}: ${policyText(p)}. Change`}
        data-review={p.review} {...stylex.props(s.summary, nested && s.summaryNested, p.review === 'auto' && s.auto, p.access === 'ro' && s.readOnly)}>
        <span {...stylex.props(s.part)}>{p.access}</span>
        <span>{p.review}</span>
        <span {...stylex.props(s.part)}>commits {p.commit}</span>
      </summary>
      <div role="group" aria-label={`Policy of agent ${agent.id}`} {...stylex.props(s.menu)}>
        {groups.map(g => (
          <div key={g.field} data-policy-field={g.field} {...stylex.props(s.group)}>
            <span {...stylex.props(s.groupLabel)}>{g.label}</span>
            <div {...stylex.props(s.options)}>
              {g.items.map(item => (
                <Button key={item.value} variant="menu" chosen={item.chosen} disabled={busy} aria-pressed={item.chosen}
                  data-policy-option={`${g.field}=${item.value}`} title={item.hint + (item.aboveParent ? ` Above its parent #${agent.parentId}'s policy: you may still choose it.` : '')}
                  onClick={event => void choose(event, g.field, item.value)} sx={s.option}>
                  {item.label}{item.aboveParent && <span aria-label="above its parent" {...stylex.props(s.above)}> ↑</span>}
                </Button>
              ))}
            </div>
          </div>
        ))}
        <p {...stylex.props(s.note)}>Applies from its next tool call. Calls already waiting for you stay as they are.{p.review === 'manual' && p.access === 'rw' ? ' On manual review the command classifier never decides; auto lets it.' : ''}</p>
      </div>
    </details>
  );
}

const s = stylex.create({
  root: { position: 'relative', flexShrink: 0 },
  summary: {
    display: 'flex',
    alignItems: 'center',
    gap: 4,
    paddingBlock: 3,
    paddingInline: 6,
    marginInline: 4,
    listStyle: 'none',
    cursor: 'pointer',
    whiteSpace: 'nowrap',
    borderRadius: 3,
    fontFamily: 'ui-monospace, monospace',
    fontSize: 9,
    color: { default: colors.textFaint, ':hover': colors.text },
    backgroundColor: colors.chip,
  },
  summaryNested: { fontSize: 8.5, paddingBlock: 2 },
  auto: { color: { default: colors.brand, ':hover': colors.brandStrong } },
  readOnly: { color: { default: colors.textGhost, ':hover': colors.text } },
  // On a phone only the review shows; the menu names every field.
  part: { display: { default: 'inline', [breakpoints.narrow]: 'none' } },
  menu: {
    position: 'absolute',
    right: 0,
    top: 'calc(100% + 3px)',
    width: 250,
    maxWidth: 'calc(100vw - 32px)',
    padding: 8,
    display: 'grid',
    gap: 8,
    backgroundColor: colors.selected,
    borderRadius: 4,
    boxShadow: `0 6px 20px ${colors.shadow}`,
    zIndex: 40,
  },
  group: { display: 'grid', gap: 3 },
  groupLabel: { fontSize: 9.5, color: colors.textFaint },
  options: { display: 'flex', flexWrap: 'wrap', gap: 3 },
  option: { width: 'auto', flexGrow: 1, flexBasis: 0, textAlign: 'center', paddingBlock: 6, paddingInline: 6, fontSize: 10.5, whiteSpace: 'nowrap' },
  above: { color: colors.waiting },
  note: { margin: 0, fontSize: 9.5, lineHeight: 1.45, color: colors.textGhost },
});
