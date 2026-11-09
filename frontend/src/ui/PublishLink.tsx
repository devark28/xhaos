// The dialog that publishes a channel on its link (or publishes it again):
// how long the link lasts, then a new passphrase. Like the "Dangerously
// allow" dialog it offers presets and a custom duration; "no expiry" must be
// picked on purpose. The server enforces it all (package channel).
import { useEffect, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button, Info } from './kit.tsx';
import { LINK_PRESETS, DEFAULT_LINK_PRESET, parseLinkDuration } from '../channel-link.ts';

type Choice = (typeof LINK_PRESETS)[number]['id'] | 'custom' | 'none';
export type Issued = { passphrase: string; path: string; channel: { expiresAt?: string } };

export function PublishLinkDialog({ id, again, onClose, onPublish }: {
  id: string;
  again: boolean; // already published (live or expired): the old passphrase stops
  onClose(): void;
  onPublish(duration: string | null): Promise<void>; // null: no expiry
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [choice, setChoice] = useState<Choice>(DEFAULT_LINK_PRESET), [custom, setCustom] = useState(''), [busy, setBusy] = useState(false);
  const parsed = parseLinkDuration(custom);
  const customError = choice === 'custom' && custom.trim() !== '' && 'error' in parsed ? parsed.error : '';
  const ready = choice !== 'custom' || !('error' in parsed);
  useEffect(() => { const d = dialog.current; if (d && !d.open) d.showModal(); }, []);
  const submit = async () => {
    if (!ready || busy) return;
    setBusy(true);
    try { await onPublish(choice === 'none' ? null : choice === 'custom' ? custom : choice); } finally { setBusy(false); }
  };
  return (
    <dialog ref={dialog} data-publish-dialog={id} onClose={onClose} {...stylex.props(s.dialog)}>
      <form method="dialog" onSubmit={e => { e.preventDefault(); void submit(); }}>
        <h2 {...stylex.props(s.title)}>{again ? `Publish #${id} again?` : `Publish #${id} on a link?`}</h2>
        <p {...stylex.props(s.text)}>
          Programs outside Xhaos that have the passphrase can read this channel and message its agents. You get a new passphrase, shown once.
          {again && <> <strong>The old passphrase stops working at once.</strong></>}
        </p>
        <div {...stylex.props(s.row)}>
          <span {...stylex.props(s.label)} id={`publish-for-${id}`}>Lasts <Info text="Pick how long the link works, or type your own: a number and a unit (m, h or d) like 90m, 12h or 2d, from 10 minutes to 30 days. After that it refuses everyone, like a wrong passphrase, until you publish again. No expiry works until you unpublish it." /></span>
          <div role="radiogroup" aria-labelledby={`publish-for-${id}`} data-publish-duration {...stylex.props(s.chips)}>
            {LINK_PRESETS.map(p => (
              <button key={p.id} type="button" role="radio" aria-checked={choice === p.id} data-publish-preset={p.id} onClick={() => setChoice(p.id)} {...stylex.props(s.chip, choice === p.id && s.chipOn)}>{p.label}</button>
            ))}
            <input data-publish-custom aria-label="Your own duration, like 12h" placeholder="other: 12h" value={custom}
              onFocus={() => setChoice('custom')} onChange={e => { setCustom(e.target.value); setChoice('custom'); }}
              {...stylex.props(s.input, s.custom, choice === 'custom' && s.chipOn, customError !== '' && s.bad)} />
            <button type="button" role="radio" aria-checked={choice === 'none'} data-publish-preset="none" onClick={() => setChoice('none')} {...stylex.props(s.chip, choice === 'none' && s.chipOn)}>no expiry</button>
          </div>
          {customError && <span data-publish-error {...stylex.props(s.error)}>{customError}</span>}
          {choice === 'none' && <span data-publish-none-note {...stylex.props(s.note)}>The link works until you unpublish it.</span>}
        </div>
        <div {...stylex.props(s.actions)}>
          <Button type="button" onClick={() => dialog.current?.close()}>Cancel</Button>
          <Button type="submit" data-publish-go disabled={busy || !ready}>{again ? 'Publish again' : 'Publish'}</Button>
        </div>
      </form>
    </dialog>
  );
}

const s = stylex.create({
  dialog: { maxWidth: 420, width: 'calc(100% - 32px)', padding: 18, borderRadius: 8, borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, backgroundColor: colors.menu, color: colors.text, fontSize: 12, '::backdrop': { backgroundColor: 'rgba(0,0,0,0.55)' } },
  title: { margin: 0, marginBottom: 10, fontSize: 14, color: colors.textStrong },
  text: { margin: 0, marginBottom: 12, lineHeight: 1.5 },
  row: { display: 'grid', gridTemplateColumns: '60px 1fr', alignItems: 'center', gap: 8, marginBottom: 8 },
  label: { color: colors.textFaint, fontSize: 11 },
  chips: { display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6 },
  chip: { paddingBlock: 5, paddingInline: 10, borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, borderRadius: 14, backgroundColor: { default: 'transparent', ':hover': colors.hover }, color: colors.text, fontSize: 12, fontFamily: 'inherit', cursor: 'pointer' },
  chipOn: { borderColor: colors.brandLine, backgroundColor: colors.brandSoft },
  input: { minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 6, paddingInline: 8, fontSize: 12, fontFamily: 'inherit' },
  custom: { width: 100, borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, borderRadius: 14, paddingInline: 10 },
  bad: { borderColor: colors.danger },
  error: { gridColumn: '2', color: colors.danger, fontSize: 11 },
  note: { gridColumn: '2', color: colors.warn, fontSize: 11 },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 12 },
});
