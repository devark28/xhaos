// Dangerously allow: a red badge on the card while it is on, and the dialog
// that turns it on (passphrase, how long). The server enforces it (package
// danger); this only asks and shows.
import { useEffect, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { call } from './state/backend.ts';
import { useApp, update, get, toast, errorText, findAgent } from './state/store.ts';
import type { Agent } from '../types.ts';

// useNow re-renders every interval, for countdowns.
function useNow(ms: number) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), ms); return () => clearInterval(t); }, [ms]);
  return now;
}
export const dangerLeft = (agent: Agent, now = Date.now()) => (agent.danger ? new Date(agent.danger.until).getTime() - now : 0);
const left = (ms: number) => (ms >= 3600_000 ? `${Math.floor(ms / 3600_000)}h ${Math.round((ms % 3600_000) / 60_000)}m` : `${Math.max(1, Math.round(ms / 60_000))}m`);

export function DangerBadge({ agent }: { agent: Agent }) {
  const ms = dangerLeft(agent, useNow(30_000));
  if (ms <= 0) return null;
  return <span data-danger-badge={agent.id} title={`Dangerously allowed: calls run without asking for ${left(ms)} more${agent.danger?.branch ? `; git locked to ${agent.danger.branch}` : ''}`} {...stylex.props(s.badge)}>⚠ {left(ms)}</span>;
}

export function openDanger(id: number) { update(s => { s.dangerAgent = id; }); }

export async function dangerOff(id: number) {
  try { await call('danger', 'Set', { workspaceId: get().active, agentId: id, off: true }); toast(`#${id} asks again before risky calls.`); }
  catch (error) { toast(errorText(error)); }
}

export function DangerDialog() {
  const id = useApp(s => s.dangerAgent);
  const agent = id ? findAgent(id) : undefined;
  const dialog = useRef<HTMLDialogElement>(null);
  const [pass, setPass] = useState(''), [minutes, setMinutes] = useState(60), [busy, setBusy] = useState(false);
  useEffect(() => { const d = dialog.current; if (!d) return; if (agent && !d.open) d.showModal(); if (!agent && d.open) d.close(); }, [agent]);
  const close = () => { setPass(''); update(s => { s.dangerAgent = null; }); };
  const turnOn = async () => {
    if (!agent) return;
    setBusy(true);
    try {
      const a = await call<{ danger?: { branch?: string } }>('danger', 'Set', { workspaceId: get().active, agentId: agent.id, passphrase: pass, minutes });
      toast(`#${agent.id} is dangerously allowed for ${left(minutes * 60_000)}${a.danger?.branch ? `; git locked to ${a.danger.branch}` : ''}.`);
      close();
    } catch (error) { toast(errorText(error)); } finally { setBusy(false); }
  };
  return (
    <dialog ref={dialog} data-danger-dialog onClose={close} {...stylex.props(s.dialog)}>
      {agent && <form method="dialog" onSubmit={e => { e.preventDefault(); void turnOn(); }}>
        <h2 {...stylex.props(s.title)}>Dangerously allow #{agent.id}?</h2>
        <p {...stylex.props(s.text)}>For the time you choose, <strong>{agent.title}</strong> runs every call that would wait for you: writes, any command, commits and pushes, MCP tools.</p>
        <ul {...stylex.props(s.list)}>
          <li>It stays in the sandbox: only its project folder.</li>
          <li>Git is locked to its current branch (a new <code>xhaos/…</code> branch if it is on main): no switching branches, no force push, no pushing elsewhere.</li>
          <li>Secrets still ask each use unless you trusted them to it; channels to agents outside its family still ask.</li>
          <li>Its subagents do not get it. You can turn it off any time from its menu.</li>
        </ul>
        <div {...stylex.props(s.row)}>
          <label {...stylex.props(s.label)} htmlFor="danger-minutes">For</label>
          <select id="danger-minutes" data-danger-minutes value={minutes} onChange={e => setMinutes(Number(e.target.value))} {...stylex.props(s.input)}>
            <option value={15}>15 minutes</option><option value={60}>1 hour</option><option value={240}>4 hours</option>
          </select>
        </div>
        <div {...stylex.props(s.row)}>
          <label {...stylex.props(s.label)} htmlFor="danger-pass">Passphrase</label>
          <input id="danger-pass" data-danger-pass type="password" autoComplete="current-password" autoFocus value={pass} onChange={e => setPass(e.target.value)} {...stylex.props(s.input)} />
        </div>
        <div {...stylex.props(s.actions)}>
          <Button type="button" onClick={close}>Cancel</Button>
          <Button type="submit" data-danger-on disabled={busy || !pass} sx={s.danger}>Dangerously allow</Button>
        </div>
      </form>}
    </dialog>
  );
}

const s = stylex.create({
  badge: { flexShrink: 0, fontSize: 9, fontWeight: 600, paddingInline: 5, paddingBlock: 1, borderRadius: 3, color: '#fff', backgroundColor: '#c0392b' },
  dialog: { maxWidth: 440, width: 'calc(100% - 32px)', padding: 18, borderRadius: 8, borderWidth: 1, borderStyle: 'solid', borderColor: '#c0392b', backgroundColor: colors.menu, color: colors.text, fontSize: 12, '::backdrop': { backgroundColor: 'rgba(0,0,0,0.55)' } },
  title: { margin: 0, marginBottom: 10, fontSize: 14, color: colors.textStrong },
  text: { margin: 0, marginBottom: 8, lineHeight: 1.5 },
  list: { margin: 0, marginBottom: 14, paddingLeft: 18, lineHeight: 1.55, color: colors.textMuted },
  row: { display: 'grid', gridTemplateColumns: '90px 1fr', alignItems: 'center', gap: 8, marginBottom: 8 },
  label: { color: colors.textFaint, fontSize: 11 },
  input: { minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 6, paddingInline: 8, fontSize: 12, fontFamily: 'inherit' },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 12 },
  danger: { color: '#fff', backgroundColor: { default: '#c0392b', ':hover': '#a93226' }, paddingInline: 10, paddingBlock: 5, borderRadius: 4 },
});
