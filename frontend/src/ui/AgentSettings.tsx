// Settings → Agents: how much agents may do without asking. The default
// policy of new top-level agents (subagents inherit their parent's; change
// one agent's from the chip on its card), and renaming: an agent that
// proposes a new title (propose_title) asks you on its card, unless the
// switch is on.
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Toggle } from './AppearanceSettings.tsx';
import { Button, Info } from './kit.tsx';
import { call } from './state/backend.ts';
import { get, toast, errorText } from './state/store.ts';
import { fields, optionsFor, suggestAuto, withField, type PolicyField } from '../policy.ts';
import type { Policy } from '../types.ts';

interface AgentSettingsData { selfRename: boolean; defaultPolicy: Policy }

const HINT = 'An agent proposes a new title when it starts a new task or its focus changes. Off: the proposal waits on its card (Rename or Keep) and the agent keeps working meanwhile. On: it is renamed at once, for every agent; the history notes each rename. An agent that is dangerously allowed renames itself either way.';

const POLICY_HINT = 'The policy of each new agent you start here or an MCP client starts without naming one. Subagents take their parent’s. Agents already running keep theirs: change one from the policy chip on its card.';

export function AgentSettings() {
  const [st, setSt] = useState<AgentSettingsData | null>(null);
  const [classifierOn, setClassifierOn] = useState(false);
  const [note, setNote] = useState('');
  useEffect(() => {
    let current = true;
    call<AgentSettingsData>('agent', 'Settings', get().active).then(v => { if (current) setSt(v); }, error => { if (current) setNote(errorText(error)); });
    call<boolean>('classifier', 'On').then(v => { if (current) setClassifierOn(v); }, () => {});
    return () => { current = false; };
  }, []);
  const save = async (next: AgentSettingsData) => {
    try { setSt(await call<AgentSettingsData>('agent', 'SaveSettings', { workspaceId: get().active, ...next })); }
    catch (error) { toast(errorText(error)); }
  };
  if (!st) return <p {...stylex.props(s.muted)}>{note || 'Loading…'}</p>;
  const p = st.defaultPolicy;
  const setField = (field: PolicyField, value: string) => void save({ ...st, defaultPolicy: withField(p, field, value as never) });
  return (
    <div data-agent-settings {...stylex.props(s.root)}>
      <div data-default-policy {...stylex.props(s.policy)}>
        <span {...stylex.props(s.heading)}>Policy for new agents<Info text={POLICY_HINT} /></span>
        {fields.map(f => {
          const options = optionsFor(f.field, true);
          const chosen = options.find(o => o.value === p[f.field]);
          return (
            <label key={f.field} {...stylex.props(s.row)}>
              <span {...stylex.props(s.label)}>{f.label}</span>
              <select data-default-policy-field={f.field} value={p[f.field]} onChange={e => setField(f.field, e.target.value)} {...stylex.props(s.select)}>
                {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
              {chosen && <span {...stylex.props(s.hint)}>{chosen.hint}</span>}
            </label>
          );
        })}
        {suggestAuto(p, classifierOn) && (
          <div data-suggest-auto {...stylex.props(s.suggest)}>
            <span>The command classifier is on, but it only decides for agents on auto review. New agents start on manual, so their commands ask you.</span>
            <Button data-use-auto onClick={() => setField('review', 'auto')} sx={s.use}>Use auto review for new agents</Button>
          </div>
        )}
      </div>
      <Toggle name="self-rename" label="Let agents rename themselves without asking" hint={HINT} on={st.selfRename} onChange={on => void save({ ...st, selfRename: on })} />
    </div>
  );
}

const s = stylex.create({
  root: { display: 'grid', gap: 14 },
  policy: { display: 'grid', gap: 8 },
  heading: { fontSize: 11, color: colors.text },
  row: { display: 'grid', gridTemplateColumns: '80px 1fr', alignItems: 'center', columnGap: 8, rowGap: 3, fontSize: 11 },
  label: { color: colors.textFaint, fontSize: 10 },
  select: { minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 6, fontSize: 11, fontFamily: 'inherit' },
  hint: { gridColumn: '2', color: colors.textGhost, fontSize: 10, lineHeight: 1.45 },
  suggest: { display: 'grid', gap: 4, padding: 8, borderRadius: 3, backgroundColor: colors.brandSoft, color: colors.textSoft, fontSize: 10.5, lineHeight: 1.45, justifyItems: 'start' },
  use: { paddingInline: 0, color: { default: colors.brand, ':hover': colors.brandStrong }, fontSize: 10.5 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5 },
});
