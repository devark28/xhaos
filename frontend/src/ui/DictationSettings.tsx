// Settings → Dictation: hints for the speech model, kept per workspace. The
// server passes them to the sidecar as {prompt} (see docs/guide.md, Dictation).
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { call } from './state/backend.ts';
import { errorText, useApp } from './state/store.ts';
import { colors } from './theme/tokens.stylex.ts';
import { Info } from './kit.tsx';

const HINT = 'One punctuated sentence with the words and names this workspace uses, so the model spells them right. Example: "We are testing Xhaos on a Pi Zero 2 W with the Duo camera."';

export function DictationSettings() {
  const workspace = useApp(s => s.active);
  const [text, setText] = useState('');
  const [saved, setSaved] = useState('');
  const [note, setNote] = useState('');
  useEffect(() => {
    let current = true;
    setNote('');
    call<string>('dictation', 'Hints', workspace).then(
      v => { if (current) { setText(v ?? ''); setSaved(v ?? ''); } },
      error => { if (current) setNote(errorText(error)); },
    );
    return () => { current = false; };
  }, [workspace]);
  const save = () => {
    const next = text.trim().split(/\s+/).filter(Boolean).join(' ');
    if (next === saved) return;
    call('dictation', 'SetHints', workspace, next).then(
      () => { setSaved(next); setText(next); setNote('Saved.'); },
      error => setNote(errorText(error)),
    );
  };
  return <>
    <label {...stylex.props(s.row)}>
      <span {...stylex.props(s.label)}>Hints<Info text={HINT} /></span>
      <input id="dictation-hints" type="text" value={text} placeholder="Words and names this workspace uses"
        onChange={e => setText(e.target.value)} onBlur={save} onKeyDown={e => { if (e.key === 'Enter') save(); }} {...stylex.props(s.input)} />
    </label>
    {note && <p {...stylex.props(s.note)}>{note}</p>}
  </>;
}

const s = stylex.create({
  row: { display: 'grid', gridTemplateColumns: '110px 1fr', alignItems: 'center', columnGap: 8, fontSize: 11 },
  label: { color: colors.textFaint, fontSize: 10 },
  input: { minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 6, fontSize: 11, fontFamily: 'inherit' },
  note: { color: colors.textFaint, fontSize: 10, marginTop: 4 },
});
