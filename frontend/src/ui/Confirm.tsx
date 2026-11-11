// A confirm dialog for destructive actions, in the style of the Dangerously
// allow dialog: a modal <dialog> with Cancel and a red action button. Focus
// starts on the action (or on Cancel, with focusCancel) and stays inside (Tab
// and Shift+Tab wrap); Escape and a click outside cancel; Enter clicks the
// focused button, or confirms when no button has focus (keys: dialog-keys.ts).
import { useEffect, useId, useRef, type KeyboardEvent, type MouseEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { dialogKey } from '../dialog-keys.ts';

export function ConfirmDialog({ open, title, text, action, busy = false, focusCancel = false, onConfirm, onCancel }: {
  open: boolean;
  title?: string; // a heading above the text
  text: string;
  action: string; // the red button's label
  busy?: boolean; // the action is running: its button is disabled and pulses
  focusCancel?: boolean; // start on Cancel, for actions a stray Enter must not do
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const confirm = useRef<HTMLButtonElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const textId = useId(), titleId = useId();
  useEffect(() => {
    const d = dialog.current;
    if (!d) return;
    if (open && !d.open) { d.showModal(); (focusCancel ? cancel : confirm).current?.focus(); }
    if (!open && d.open) d.close();
  }, [open]);
  const onKeyDown = (event: KeyboardEvent<HTMLDialogElement>) => {
    const buttons = [...dialog.current!.querySelectorAll<HTMLButtonElement>('button:not([disabled])')];
    const key = dialogKey(event.nativeEvent, buttons.indexOf(document.activeElement as HTMLButtonElement), buttons.length);
    if (key.kind === 'default') return;
    event.preventDefault(); // also keeps Escape from closing anything behind the dialog
    if (key.kind === 'focus') buttons[key.index]?.focus();
    else if (key.kind === 'cancel') { if (!busy) onCancel(); }
    else if (!busy) onConfirm();
  };
  // A click on the backdrop lands on the <dialog> itself, outside its box.
  const onClick = (event: MouseEvent<HTMLDialogElement>) => {
    if (event.target !== dialog.current || busy) return;
    const r = dialog.current.getBoundingClientRect();
    if (event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom) onCancel();
  };
  return (
    <dialog ref={dialog} data-confirm-dialog aria-labelledby={title ? titleId : undefined} aria-describedby={textId} onKeyDown={onKeyDown} onClick={onClick}
      onCancel={event => { event.preventDefault(); if (!busy) onCancel(); }} onClose={() => { if (open && !busy) onCancel(); }}
      {...stylex.props(s.dialog)}>
      {title && <h2 id={titleId} data-confirm-title {...stylex.props(s.title)}>{title}</h2>}
      <p id={textId} data-confirm-text {...stylex.props(s.text)}>{text}</p>
      <div {...stylex.props(s.actions)}>
        <Button ref={cancel} data-confirm-cancel onClick={onCancel} disabled={busy}>Cancel</Button>
        <button ref={confirm} type="button" data-confirm-action onClick={onConfirm} disabled={busy} aria-busy={busy || undefined}
          {...stylex.props(s.danger, busy && s.busy)}>{action}</button>
      </div>
    </dialog>
  );
}

const pulse = stylex.keyframes({ '0%': { opacity: 1 }, '50%': { opacity: 0.55 }, '100%': { opacity: 1 } });

const s = stylex.create({
  dialog: { maxWidth: 400, width: 'calc(100% - 32px)', padding: 18, borderRadius: 8, borderWidth: 1, borderStyle: 'solid', borderColor: '#c0392b', backgroundColor: colors.menu, color: colors.text, fontSize: 12, '::backdrop': { backgroundColor: 'rgba(0,0,0,0.55)' } },
  title: { margin: 0, marginBottom: 10, fontSize: 14, color: colors.textStrong },
  text: { margin: 0, lineHeight: 1.5, color: colors.textStrong, overflowWrap: 'anywhere' },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 14 },
  danger: { color: '#fff', backgroundColor: { default: '#c0392b', ':hover': '#a93226' }, borderWidth: 0, paddingInline: 10, paddingBlock: 5, borderRadius: 4, fontFamily: 'inherit', fontSize: 'inherit', cursor: { default: 'pointer', ':disabled': 'default' } },
  busy: { animationName: { default: pulse, [breakpoints.reducedMotion]: 'none' }, animationDuration: '1s', animationIterationCount: 'infinite' },
});
