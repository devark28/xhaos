// Settings → Agents: how much agents may do about themselves without
// asking. For now, renaming: an agent that proposes a new title
// (propose_title) asks you on its card, unless this is on.
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Toggle } from './AppearanceSettings.tsx';
import { call } from './state/backend.ts';
import { get, toast, errorText } from './state/store.ts';

interface AgentSettingsData { selfRename: boolean }

const HINT = 'An agent proposes a new title when it starts a new task or its focus changes. Off: the proposal waits on its card (Rename or Keep) and the agent keeps working meanwhile. On: it is renamed at once, for every agent; the history notes each rename. An agent that is dangerously allowed renames itself either way.';

export function AgentSettings() {
  const [st, setSt] = useState<AgentSettingsData | null>(null);
  const [note, setNote] = useState('');
  useEffect(() => {
    let current = true;
    call<AgentSettingsData>('agent', 'Settings', get().active).then(v => { if (current) setSt(v); }, error => { if (current) setNote(errorText(error)); });
    return () => { current = false; };
  }, []);
  const save = async (selfRename: boolean) => {
    try { setSt(await call<AgentSettingsData>('agent', 'SaveSettings', { workspaceId: get().active, selfRename })); }
    catch (error) { toast(errorText(error)); }
  };
  if (!st) return <p {...stylex.props(s.muted)}>{note || 'Loading…'}</p>;
  return (
    <div data-agent-settings {...stylex.props(s.root)}>
      <Toggle name="self-rename" label="Let agents rename themselves without asking" hint={HINT} on={st.selfRename} onChange={on => void save(on)} />
    </div>
  );
}

const s = stylex.create({
  root: { display: 'grid', gap: 8 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5 },
});
