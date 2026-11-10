// Settings → Dictation: the language for the speech model (server-wide), the
// hints for it (kept per workspace), and whether the warm dictation service
// answers (when the server has one, -dictation-url). The server passes the
// language as {language} and the hints as {prompt} to the sidecar (see
// docs/guide.md, Dictation).
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { call } from './state/backend.ts';
import { errorText, useApp } from './state/store.ts';
import { colors } from './theme/tokens.stylex.ts';
import { Button, Info } from './kit.tsx';
import { HINT_MAX, hintWarning } from '../dictation-format.ts';

const HINT = 'The words and names this workspace uses, so the model spells them right. The model reads only about the first 150 words, and names and jargon cost several tokens each. It works best as one punctuated sentence, or a list of the words actually said. Hints can change words, not just spelling: in unclear passages the model leans toward your hint words. Example: "We are testing Xhaos on a Pi Zero 2 W with the Duo camera."';
const LANGUAGE_HINT = 'The language of the speech. English is the default. Auto-detect works from the start of each take and can guess wrong on a quiet start, and it costs time. English-only models ignore any other language.';
const SERVICE_HINT = 'The dictation service keeps the speech model loaded, so a take does not wait for the model to load. When it is not reachable, takes go to the sidecar command instead (one new process per take), if the server has one.';

interface Health { ok: boolean; detail?: string }

// LANGUAGES are the choices offered; the server accepts any two- or three-letter code.
const LANGUAGES: [string, string][] = [
  ['en', 'English'],
  ['auto', 'Auto-detect'],
  ['de', 'German'],
  ['es', 'Spanish'],
  ['fr', 'French'],
  ['it', 'Italian'],
  ['ja', 'Japanese'],
  ['nl', 'Dutch'],
  ['pt', 'Portuguese'],
  ['zh', 'Chinese'],
];

export function DictationSettings() {
  const workspace = useApp(s => s.active);
  const [text, setText] = useState('');
  const [saved, setSaved] = useState('');
  const [language, setLanguage] = useState('en');
  const [note, setNote] = useState('');
  const [serviceSet, setServiceSet] = useState<boolean | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [checking, setChecking] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);

  const check = () => {
    setChecking(true);
    call<Health>('dictation', 'Health').then(
      h => setHealth(h),
      error => setHealth({ ok: false, detail: errorText(error) }),
    ).finally(() => setChecking(false));
  };

  useEffect(() => {
    let current = true;
    call<boolean>('dictation', 'ServiceEnabled').then(
      yes => {
        if (!current) return;
        setServiceSet(yes);
        if (yes) check();
      },
      () => { if (current) setServiceSet(false); },
    );
    return () => { current = false; };
    // check is stable for this mount.
  }, []);

  useEffect(() => {
    let current = true;
    setNote('');
    call<string>('dictation', 'Hints', workspace).then(
      v => { if (current) { setText(v ?? ''); setSaved(v ?? ''); } },
      error => { if (current) setNote(errorText(error)); },
    );
    return () => { current = false; };
  }, [workspace]);

  useEffect(() => {
    let current = true;
    call<string>('dictation', 'Language').then(
      v => { if (current && v) setLanguage(v); },
      () => {},
    );
    return () => { current = false; };
  }, []);

  // Grow with the text: at least four rows, then as tall as the words need.
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${el.scrollHeight}px`;
  }, [text]);

  const save = () => {
    const next = text.trim().split(/\s+/).filter(Boolean).join(' ');
    if (next === saved) return;
    call('dictation', 'SetHints', workspace, next).then(
      () => { setSaved(next); setText(next); setNote('Saved.'); },
      error => setNote(errorText(error)),
    );
  };
  const chooseLanguage = (code: string) => {
    const previous = language;
    setLanguage(code);
    call('dictation', 'SetLanguage', code).then(
      () => setNote('Saved.'),
      error => { setLanguage(previous); setNote(errorText(error)); },
    );
  };
  const warning = hintWarning(text);
  return <>
    <label {...stylex.props(s.row)}>
      <span {...stylex.props(s.label)}>Language<Info text={LANGUAGE_HINT} /></span>
      <select id="dictation-language" value={language} onChange={e => chooseLanguage(e.target.value)} {...stylex.props(s.input)}>
        {LANGUAGES.map(([code, name]) => <option key={code} value={code}>{name}</option>)}
      </select>
    </label>
    <div {...stylex.props(s.row)}>
      <span {...stylex.props(s.label)}>Hints<Info text={HINT} /></span>
      <div {...stylex.props(s.column)}>
        <textarea id="dictation-hints" ref={area} rows={4} value={text} maxLength={HINT_MAX}
          placeholder="Words and names this workspace uses"
          onChange={e => setText(e.target.value)} onBlur={save}
          onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); save(); } }}
          {...stylex.props(s.input, s.area)} />
        <div {...stylex.props(s.counts)}>
          <span id="dictation-hints-count" aria-live="polite">{[...text].length} / {HINT_MAX}</span>
          {warning && <span id="dictation-hints-warning" role="alert" {...stylex.props(s.warn)}>{warning}</span>}
        </div>
      </div>
    </div>
    {serviceSet === true && <div {...stylex.props(s.row)}>
      <span {...stylex.props(s.label)}>Service<Info text={SERVICE_HINT} /></span>
      <div {...stylex.props(s.status)}>
        <span data-dictation-health={health === null ? 'checking' : health.ok ? 'ok' : 'down'} {...stylex.props(s.muted)}>
          {health === null ? 'Checking…' : health.ok ? 'Running: the model is loaded' : `Not reachable${health.detail ? ` (${health.detail})` : ''}`}
        </span>
        <Button data-dictation-check disabled={checking} onClick={check}>{checking ? 'Checking…' : 'Check again'}</Button>
      </div>
    </div>}
    {note && <p {...stylex.props(s.note)}>{note}</p>}
  </>;
}

const s = stylex.create({
  row: { display: 'grid', gridTemplateColumns: '110px 1fr', alignItems: 'start', columnGap: 8, rowGap: 6, fontSize: 11, marginBottom: 6 },
  label: { color: colors.textFaint, fontSize: 10, paddingTop: 5 },
  column: { display: 'grid', gap: 3, minWidth: 0 },
  input: { minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 6, fontSize: 11, fontFamily: 'inherit' },
  area: { resize: 'none', overflow: 'hidden', lineHeight: 1.4 },
  counts: { display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', gap: 6, color: colors.textFaint, fontSize: 10, fontVariantNumeric: 'tabular-nums' },
  warn: { color: colors.warn },
  note: { color: colors.textFaint, fontSize: 10, marginTop: 4 },
  status: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, paddingTop: 2 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5, lineHeight: 1.5 },
});
