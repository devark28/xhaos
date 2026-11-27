// A title the agent proposed for itself: a slim, non-modal line under its
// card's header, "Rename to “X”? (reason) [Rename] [Keep]". The agent never
// waits for it (app/agent/title.go); answering does not wake it.
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { call } from './state/backend.ts';
import { get, toast, errorText } from './state/store.ts';
import { idempotencyKey } from '../server-client.ts';
import type { Agent } from '../types.ts';

export async function decideTitle(agent: Agent, rename: boolean) {
  const title = agent.titleProposal?.title;
  if (!title) return;
  try {
    await call('agent', 'DecideTitle', { idempotencyKey: idempotencyKey(), workspaceId: get().active, agentId: agent.id, rename, title });
    toast(rename ? `#${agent.id} is now “${title}”.` : `#${agent.id} keeps its title.`);
  } catch (error) { toast(errorText(error)); }
}

export function TitleProposal({ agent, nested = false }: { agent: Agent; nested?: boolean }) {
  const p = agent.titleProposal;
  if (!p || agent.sample || agent.status === 'terminated') return null;
  return (
    <div data-title-proposal={agent.id} role="group" aria-label={`Agent ${agent.id} proposes a new title`} {...stylex.props(s.root, nested && s.nested)}>
      <span {...stylex.props(s.text)} title={p.reason ? `${p.title} (${p.reason})` : p.title}>
        Rename to <span {...stylex.props(s.title)}>“{p.title}”</span>?{p.reason && <span {...stylex.props(s.reason)}> ({p.reason})</span>}
      </span>
      <Button data-title-rename={agent.id} onClick={() => void decideTitle(agent, true)} sx={s.rename}>Rename</Button>
      <Button data-title-keep={agent.id} onClick={() => void decideTitle(agent, false)} sx={s.keep}>Keep</Button>
    </div>
  );
}

const s = stylex.create({
  root: {
    display: 'flex', alignItems: 'center', gap: 6, minWidth: 0,
    marginInline: 8, marginBottom: 6, paddingBlock: 3, paddingLeft: 8, paddingRight: 3,
    borderLeftWidth: 2, borderLeftStyle: 'solid', borderLeftColor: colors.brandLine,
    backgroundColor: colors.brandSoft, borderRadius: 2, fontSize: 10.5,
  },
  nested: { marginInline: 7, fontSize: 10 },
  text: { flexGrow: 1, minWidth: 0, color: colors.textMuted, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' },
  title: { color: colors.text },
  reason: { color: colors.textFaint },
  rename: { flexShrink: 0, paddingBlock: 2, paddingInline: 7, borderRadius: 3, color: colors.brandStrong, fontSize: 10 },
  keep: { flexShrink: 0, paddingBlock: 2, paddingInline: 7, borderRadius: 3, color: colors.textDim, fontSize: 10 },
});
