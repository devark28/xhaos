// Settings → Appearance: each choice applies at once, is kept in this
// browser for the first paint, and is saved on the server so every browser
// that signs in looks the same (appearance.ts, state/backend.ts).
import { useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { applyAppearance, defaults, loadAppearance, saveAppearance, type Appearance } from './appearance.ts';
import { Button } from './kit.tsx';
import { pushAppearance } from './state/backend.ts';

type Choice<K extends keyof Appearance> = { key: K; label: string; hint?: string; options: [Appearance[K], string][] };
const sizes: [number, string][] = [[0.9, 'Small'], [1, 'Normal'], [1.15, 'Large'], [1.3, 'Larger']];
const choices: Choice<keyof Appearance>[] = [
  { key: 'scale', label: 'Text size', hint: 'Scales the whole interface.', options: sizes },
  { key: 'transcript', label: 'Transcript size', hint: 'On top of the text size, for agent transcripts only.', options: sizes },
  { key: 'theme', label: 'Theme', options: [['dark', 'Dark'], ['light', 'Light'], ['system', 'System']] },
  { key: 'accent', label: 'Accent', hint: 'For trying colours out; one will become the brand colour.', options: [['violet', 'Violet'], ['blue', 'Blue'], ['green', 'Green'], ['amber', 'Amber'], ['pink', 'Pink']] },
  { key: 'contrast', label: 'Contrast', options: [['normal', 'Normal'], ['high', 'High']] },
  { key: 'density', label: 'Density', options: [['compact', 'Compact'], ['comfortable', 'Comfortable']] },
  { key: 'mono', label: 'Code font', hint: 'For transcripts, code and the message box. Bundled with Xhaos.', options: [['jetbrains', 'JetBrains Mono'], ['fira', 'Fira Code'], ['plex', 'IBM Plex Mono'], ['system', 'System monospace']] },
  { key: 'wrap', label: 'Long code lines', options: [['wrap', 'Wrap'], ['scroll', 'Scroll sideways']] },
  { key: 'motion', label: 'Motion', options: [['full', 'Animations on'], ['reduce', 'Reduce motion']] },
  { key: 'mentions', label: 'Typing #12', hint: 'In the message box. Attach turns "#12 " into a chip for agent 12; \\#12 always stays text.', options: [['attach', 'Attaches agent 12'], ['text', 'Stays text']] },
  { key: 'time', label: 'Timestamps', options: [['exact', 'Exact time'], ['relative', 'Relative (2m ago)']] },
] as Choice<keyof Appearance>[];

export function AppearanceSettings() {
  const [a, setA] = useState<Appearance>(loadAppearance);
  const change = (next: Appearance) => { setA(next); saveAppearance(next); applyAppearance(next); void pushAppearance(next); };
  return <>
    <div {...stylex.props(s.grid)}>
      {choices.map(c => (
        <label key={c.key} {...stylex.props(s.row)}>
          <span {...stylex.props(s.label)}>{c.label}</span>
          <select data-appearance={c.key} value={String(a[c.key])} onChange={e => {
            const raw = e.target.value;
            change({ ...a, [c.key]: typeof defaults[c.key] === 'number' ? Number(raw) : raw });
          }} {...stylex.props(s.select)}>
            {c.options.map(([value, text]) => <option key={String(value)} value={String(value)}>{text}</option>)}
          </select>
          {c.hint && <span {...stylex.props(s.hint)}>{c.hint}</span>}
        </label>
      ))}
    </div>
    <div {...stylex.props(s.actions)}><Button id="appearance-reset" onClick={() => change({ ...defaults })}>Reset to defaults</Button></div>
  </>;
}

const s = stylex.create({
  grid: { display: 'grid', gap: 8 },
  row: { display: 'grid', gridTemplateColumns: '110px 1fr', alignItems: 'center', columnGap: 8, rowGap: 2, fontSize: 11 },
  label: { color: colors.textFaint, fontSize: 10 },
  select: { minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 6, fontSize: 11, fontFamily: 'inherit' },
  hint: { gridColumn: '2', color: colors.textGhost, fontSize: 10 },
  actions: { display: 'flex', justifyContent: 'flex-end', marginTop: 10 },
});
