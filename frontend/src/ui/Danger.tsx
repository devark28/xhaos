// Dangerously allow: a red badge on the card while it is on, and the dialog
// that turns it on (passphrase, how long). The server enforces it (package
// danger); this only asks and shows.
import { useEffect, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button, Info } from './kit.tsx';
import { Toggle } from './AppearanceSettings.tsx';
import { DURATION_PRESETS, DEFAULT_CALLS, MAX_CALLS, parseDuration, formatDuration, callsSetting, callsValid } from '../duration.ts';
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
const left = (ms: number) => formatDuration(ms / 1000);

export function DangerBadge({ agent }: { agent: Agent }) {
  const ms = dangerLeft(agent, useNow(1_000));
  if (ms <= 0) return null;
  return <span data-danger-badge={agent.id} title={`Dangerously allowed: calls run without asking for ${left(ms)} more${agent.danger?.branch ? `; git locked to ${agent.danger.branch}` : ''}; ${callsText(agent.danger?.calls)}`} {...stylex.props(s.badge)}>⚠ {left(ms)}</span>;
}

// callsText says what a session does to the limit on model calls in a row.
const callsText = (calls?: number) => (calls === -1 ? 'no limit on model calls in a row' : `stops after ${calls || DEFAULT_CALLS} model calls in a row`);

export function openDanger(id: number) { update(s => { s.dangerAgent = id; }); }

export async function dangerOff(id: number) {
  try { await call('danger', 'Set', { workspaceId: get().active, agentId: id, off: true }); toast(`#${id} asks again before risky calls.`); }
  catch (error) { toast(errorText(error)); }
}

export function DangerDialog() {
  const id = useApp(s => s.dangerAgent);
  const agent = id ? findAgent(id) : undefined;
  const dialog = useRef<HTMLDialogElement>(null);
  const [pass, setPass] = useState(''), [busy, setBusy] = useState(false);
  // How long: a preset chip, or the owner's own <n><unit> in the box.
  const [preset, setPreset] = useState<string | null>('1h'), [custom, setCustom] = useState('');
  // The call limit for this session (behind "Call limit"): on at the normal 40.
  const [limitOpen, setLimitOpen] = useState(false), [limitOn, setLimitOn] = useState(true), [calls, setCalls] = useState(DEFAULT_CALLS);
  const durationText = preset ?? custom;
  const parsed = parseDuration(durationText);
  const durationError = preset === null && custom.trim() !== '' && 'error' in parsed ? parsed.error : '';
  const callsError = limitOn && !callsValid(calls) ? `1 to ${MAX_CALLS}` : '';
  const ready = !('error' in parsed) && !callsError && pass !== '';
  useEffect(() => { const d = dialog.current; if (!d) return; if (agent && !d.open) d.showModal(); if (!agent && d.open) d.close(); }, [agent]);
  const close = () => {
    setPass(''); setPreset('1h'); setCustom(''); setLimitOpen(false); setLimitOn(true); setCalls(DEFAULT_CALLS);
    update(s => { s.dangerAgent = null; });
  };
  const turnOn = async () => {
    if (!agent) return;
    setBusy(true);
    try {
      if ('error' in parsed) return;
      const a = await call<{ danger?: { branch?: string; calls?: number } }>('danger', 'Set', { workspaceId: get().active, agentId: agent.id, passphrase: pass, duration: durationText, calls: callsSetting(limitOn, calls) });
      toast(`#${agent.id} is dangerously allowed for ${formatDuration(parsed.seconds)}; ${callsText(a.danger?.calls)}${a.danger?.branch ? `; git locked to ${a.danger.branch}` : ''}.`);
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
          <span {...stylex.props(s.label)} id="danger-for-label">For <Info text="A number and a unit: s, m, h or d, like 10s, 15m, 10h or 1d (or combined, 1h30m). Between 10 seconds and 7 days." /></span>
          <div role="radiogroup" aria-labelledby="danger-for-label" data-danger-duration {...stylex.props(s.chips)}>
            {DURATION_PRESETS.map(p => (
              <button key={p} type="button" role="radio" aria-checked={preset === p} data-danger-preset={p} onClick={() => setPreset(p)} {...stylex.props(s.chip, preset === p && s.chipOn)}>{p}</button>
            ))}
            <input data-danger-custom aria-label="Your own duration, like 10h" placeholder="other: 10h" value={custom}
              onFocus={() => setPreset(null)} onChange={e => { setCustom(e.target.value); setPreset(null); }}
              {...stylex.props(s.input, s.custom, preset === null && s.chipOn, durationError !== '' && s.bad)} />
          </div>
          {durationError && <span data-danger-duration-error {...stylex.props(s.error)}>{durationError}</span>}
        </div>
        <div {...stylex.props(s.limit)}>
          <button type="button" aria-expanded={limitOpen} data-danger-limit-toggle onClick={() => setLimitOpen(o => !o)} {...stylex.props(s.limitHead)}>
            <span aria-hidden="true" {...stylex.props(s.chevron, limitOpen && s.chevronOpen)}>›</span>
            Call limit
            <span {...stylex.props(s.limitSummary)}>{limitOn ? `${calls} in a row` : 'none'}</span>
          </button>
          {limitOpen && <div data-danger-limit {...stylex.props(s.limitBody)}>
            <Toggle name="danger-limit" label="Stop after a number of model calls in a row"
              hint={`Normally an agent stops after ${DEFAULT_CALLS} model calls in a row until you write to it. This changes it for this session only; when the session ends it is ${DEFAULT_CALLS} again. Off: no limit, which can spend a lot.`}
              on={limitOn} onChange={setLimitOn} />
            <div {...stylex.props(s.row, s.limitRow)}>
              <label {...stylex.props(s.label)} htmlFor="danger-calls">Calls</label>
              <input id="danger-calls" data-danger-calls type="number" min={1} max={MAX_CALLS} step={1} disabled={!limitOn} value={calls}
                onChange={e => setCalls(Number(e.target.value))} {...stylex.props(s.input, s.calls, !limitOn && s.disabled, callsError !== '' && s.bad)} />
              {callsError && <span {...stylex.props(s.error)}>{callsError}</span>}
            </div>
          </div>}
        </div>
        <div {...stylex.props(s.row)}>
          <label {...stylex.props(s.label)} htmlFor="danger-pass">Passphrase</label>
          <input id="danger-pass" data-danger-pass type="password" autoComplete="current-password" autoFocus value={pass} onChange={e => setPass(e.target.value)} {...stylex.props(s.input)} />
        </div>
        <div {...stylex.props(s.actions)}>
          <Button type="button" onClick={close}>Cancel</Button>
          <Button type="submit" data-danger-on disabled={busy || !ready} sx={s.danger}>Dangerously allow</Button>
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
  chips: { display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6 },
  chip: { paddingBlock: 5, paddingInline: 10, borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, borderRadius: 14, backgroundColor: { default: 'transparent', ':hover': colors.hover }, color: colors.text, fontSize: 12, fontFamily: 'inherit', cursor: 'pointer' },
  chipOn: { borderColor: '#c0392b', backgroundColor: 'rgba(192,57,43,0.18)' },
  custom: { width: 96, borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, borderRadius: 14, paddingInline: 10 },
  bad: { borderColor: colors.danger },
  error: { gridColumn: '2', color: colors.danger, fontSize: 11 },
  limit: { marginBlock: 4, borderTopWidth: 1, borderTopStyle: 'solid', borderTopColor: colors.lineFaint },
  limitHead: { display: 'flex', alignItems: 'center', gap: 6, width: '100%', paddingBlock: 8, paddingInline: 0, borderWidth: 0, backgroundColor: 'transparent', color: colors.textMuted, fontSize: 11, fontFamily: 'inherit', textAlign: 'left', cursor: 'pointer' },
  chevron: { display: 'inline-block', width: 10, transitionProperty: 'transform', transitionDuration: '.12s' },
  chevronOpen: { transform: 'rotate(90deg)' },
  limitSummary: { marginLeft: 'auto', color: colors.textFaint },
  limitBody: { display: 'grid', gap: 8, paddingBottom: 8 },
  limitRow: { marginBottom: 0 },
  calls: { width: 90 },
  disabled: { opacity: 0.45, cursor: 'not-allowed' },
  danger: { color: '#fff', backgroundColor: { default: '#c0392b', ':hover': '#a93226' }, paddingInline: 10, paddingBlock: 5, borderRadius: 4 },
});
