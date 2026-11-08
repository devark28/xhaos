// Settings → Appearance: each choice applies at once, is kept in this
// browser for the first paint, and is saved on the server so every browser
// that signs in looks the same (appearance.ts, state/backend.ts).
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { applyAppearance, defaults, loadAppearance, saveAppearance, type Appearance } from './appearance.ts';
import { Button, Info } from './kit.tsx';
import { call, pushAppearance } from './state/backend.ts';
import { errorText, update, useApp } from './state/store.ts';
import { setSamples } from './state/composer.ts';

// A choice is a dropdown; one with `toggle` [off, on] is a switch.
type Choice<K extends keyof Appearance> = { key: K; label: string; hint?: string; options?: [Appearance[K], string][]; toggle?: [Appearance[K], Appearance[K]] };
const sizes: [number, string][] = [[0.9, 'Small'], [1, 'Normal'], [1.15, 'Large'], [1.3, 'Larger']];
const choices: Choice<keyof Appearance>[] = [
  { key: 'scale', label: 'Text size', hint: 'Scales the whole interface.', options: sizes },
  { key: 'transcript', label: 'Transcript size', hint: 'On top of the text size, for agent transcripts only.', options: sizes },
  { key: 'layout', label: 'Columns', hint: 'How many columns of agent cards (also the buttons above them).', options: [['list', 'One'], ['grid', 'Two'], ['three', 'Three']] },
  { key: 'theme', label: 'Theme', options: [['dark', 'Dark'], ['light', 'Light'], ['system', 'System']] },
  { key: 'accent', label: 'Accent', hint: 'For trying colours out; one will become the brand colour.', options: [['violet', 'Violet'], ['blue', 'Blue'], ['green', 'Green'], ['amber', 'Amber'], ['pink', 'Pink']] },
  { key: 'mono', label: 'Code font', hint: 'For transcripts, code and the message box. Bundled with Xhaos.', options: [['jetbrains', 'JetBrains Mono'], ['fira', 'Fira Code'], ['plex', 'IBM Plex Mono'], ['system', 'System monospace']] },
  { key: 'contrast', label: 'High contrast', toggle: ['normal', 'high'] },
  { key: 'density', label: 'Roomier spacing', toggle: ['compact', 'comfortable'] },
  { key: 'wrap', label: 'Wrap long code lines', hint: 'Off: scroll them sideways.', toggle: ['scroll', 'wrap'] },
  { key: 'motion', label: 'Animations', toggle: ['reduce', 'full'] },
  { key: 'time', label: 'Relative timestamps', hint: '"2m ago" instead of the time.', toggle: ['exact', 'relative'] },
  { key: 'mentions', label: 'Typing #12 attaches agent 12', hint: 'In the message box, "#12 " becomes a chip for agent 12; \\#12 always stays text.', toggle: ['text', 'attach'] },
] as Choice<keyof Appearance>[];

export function AppearanceSettings() {
  const [a, setA] = useState<Appearance>(loadAppearance);
  const change = (next: Appearance) => { setA(next); saveAppearance(next); applyAppearance(next); void pushAppearance(next); update(s => { s.layout = next.layout; }); };
  const layout = useApp(s => s.layout);
  if (layout !== a.layout) setA({ ...a, layout }); // changed with the buttons above the cards
  return <>
    <div {...stylex.props(s.grid)}>
      {choices.map(c => c.toggle ? (
        <Toggle key={c.key} name={c.key} label={c.label} hint={c.hint} on={a[c.key] === c.toggle[1]} onChange={on => change({ ...a, [c.key]: c.toggle![on ? 1 : 0] })} />
      ) : (
        <label key={c.key} {...stylex.props(s.row)}>
          <span {...stylex.props(s.label)}>{c.label}{c.hint && <Info text={c.hint} />}</span>
          <select data-appearance={c.key} value={String(a[c.key])} onChange={e => {
            const raw = e.target.value;
            change({ ...a, [c.key]: typeof defaults[c.key] === 'number' ? Number(raw) : raw });
          }} {...stylex.props(s.select)}>
            {c.options!.map(([value, text]) => <option key={String(value)} value={String(value)}>{text}</option>)}
          </select>
        </label>
      ))}
    </div>
    <div {...stylex.props(s.actions)}><Button id="appearance-reset" onClick={() => change({ ...defaults })}>Reset to defaults</Button></div>
  </>;
}

// DeveloperSettings: tools for working on Xhaos itself, off by default.
// Turning it off also hides the sample agents and the /samples command.
export function DeveloperSettings() {
  const [a, setA] = useState<Appearance>(loadAppearance);
  const samples = useApp(s => s.showSamples);
  const set = (developer: Appearance['developer']) => {
    const next = { ...loadAppearance(), developer };
    setA(next); saveAppearance(next); void pushAppearance(next);
    if (developer === 'off' && samples) setSamples(false);
  };
  return (
    <div {...stylex.props(s.grid)}>
      <Toggle name="developer" label="Developer mode" hint="For working on Xhaos itself. Off hides everything below and the /samples command." on={a.developer === 'on'} onChange={on => set(on ? 'on' : 'off')} />
      {a.developer === 'on' && (
        <Toggle name="samples" label="Sample agents" hint="Demo agents mixed into the page (never sent to the server). Same as /samples." on={samples} onChange={setSamples} />
      )}
      {a.developer === 'on' && <KeepRecordings />}
    </div>
  );
}

// KeepRecordings is the server-wide switch that keeps each dictation take as a
// WAV beside the database. Off by default. The recordings are the user's voice.
function KeepRecordings() {
  const [keep, setKeep] = useState<boolean | null>(null);
  const [note, setNote] = useState('');
  useEffect(() => {
    call<boolean>('dictation', 'Keeping').then(setKeep, error => setNote(errorText(error)));
  }, []);
  const set = (on: boolean) => {
    setNote('');
    call('dictation', 'SetKeeping', on).then(() => setKeep(on), error => setNote(errorText(error)));
  };
  const clear = () => {
    if (!window.confirm('Delete every kept dictation recording and its log?')) return;
    call('dictation', 'DeleteRecordings').then(() => setNote('Deleted.'), error => setNote(errorText(error)));
  };
  return <>
    <Toggle name="keep-recordings" label="Keep dictation recordings" hint="Off by default. When on, each take is saved as a WAV in dictation-debug/ beside the database, with a line in its log. These are voice recordings: keep them only while you need them." on={keep === true} onChange={set} />
    <div {...stylex.props(s.actions)}>
      <Button id="dictation-delete" disabled={keep === null} onClick={clear}>Delete all recordings</Button>
      {note && <span {...stylex.props(s.note)}>{note}</span>}
    </div>
  </>;
}

// Toggle is an on/off switch (role="switch"), for settings with two states.
export function Toggle({ name, label, hint, on, onChange }: { name: string; label: string; hint?: string; on: boolean; onChange(on: boolean): void }) {
  return (
    <div {...stylex.props(s.toggleRow)}>
      <span {...stylex.props(s.toggleLabel)}>{label}{hint && <Info text={hint} />}</span>
      <button type="button" role="switch" aria-checked={on} aria-label={label} data-toggle={name} onClick={() => onChange(!on)} {...stylex.props(s.switch, on && s.switchOn)}>
        <span aria-hidden="true" {...stylex.props(s.knob, on && s.knobOn)} />
      </button>
    </div>
  );
}

const s = stylex.create({
  grid: { display: 'grid', gap: 8 },
  note: { color: colors.textFaint, fontSize: 10 },
  row: { display: 'grid', gridTemplateColumns: '110px 1fr', alignItems: 'center', columnGap: 8, rowGap: 2, fontSize: 11 },
  label: { color: colors.textFaint, fontSize: 10 },
  select: { minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 6, fontSize: 11, fontFamily: 'inherit' },
  hint: { gridColumn: '2', color: colors.textGhost, fontSize: 10 },
  toggleRow: { display: 'grid', gridTemplateColumns: '1fr auto', alignItems: 'center', columnGap: 10, rowGap: 2, fontSize: 11 },
  toggleLabel: { color: colors.text, fontSize: 11 },
  toggleHint: { gridColumn: '1 / -1', color: colors.textGhost, fontSize: 10 },
  switch: { position: 'relative', width: 28, height: 16, flexShrink: 0, padding: 0, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineStrong, borderRadius: 9, backgroundColor: colors.sunken, cursor: 'pointer', transitionProperty: 'background-color, border-color', transitionDuration: '.12s', outlineOffset: 2 },
  switchOn: { backgroundColor: colors.brand, borderColor: colors.brand },
  knob: { position: 'absolute', top: 2, left: 2, width: 10, height: 10, borderRadius: '50%', backgroundColor: colors.textMuted, transitionProperty: 'transform', transitionDuration: '.12s' },
  knobOn: { transform: 'translateX(12px)', backgroundColor: colors.onLight },
  actions: { display: 'flex', justifyContent: 'flex-end', marginTop: 10 },
});
