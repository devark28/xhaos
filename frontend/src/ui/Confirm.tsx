// A confirm dialog for destructive actions, in the style of the Dangerously
// allow dialog: a modal <dialog> with Cancel and a red action button. Focus
// starts on the action and stays inside (Tab and Shift+Tab wrap); Escape
// cancels; Enter clicks the focused button, or confirms when no button has
// focus (keys: dialog-keys.ts).
import { useEffect, useId, useRef, type KeyboardEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { dialogKey } from '../dialog-keys.ts';

export function ConfirmDialog({ open, text, action, busy = false, onConfirm, onCancel }: {
  open: boolean;
  text: string;
  action: string; // the red button's label
  busy?: boolean; // the action is running: its button is disabled and pulses
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const confirm = useRef<HTMLButtonElement>(null);
  const textId = useId();
  useEffect(() => {
    const d = dialog.current;
    if (!d) return;
    if (open && !d.open) { d.showModal(); confirm.current?.focus(); }
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
  return (
    <dialog ref={dialog} data-confirm-dialog aria-describedby={textId} onKeyDown={onKeyDown}
      onCancel={event => { event.preventDefault(); if (!busy) onCancel(); }} onClose={() => { if (open && !busy) onCancel(); }}
      {...stylex.props(s.dialog)}>
      <p id={textId} data-confirm-text {...stylex.props(s.text)}>{text}</p>
      <div {...stylex.props(s.actions)}>
        <Button data-confirm-cancel onClick={onCancel} disabled={busy}>Cancel</Button>
        <button ref={confirm} type="button" data-confirm-action onClick={onConfirm} disabled={busy} aria-busy={busy || undefined}
          {...stylex.props(s.danger, busy && s.busy)}>{action}</button>
      </div>
    </dialog>
  );
}

const pulse = stylex.keyframes({ '0%': { opacity: 1 }, '50%': { opacity: 0.55 }, '100%': { opacity: 1 } });

const s = stylex.create({
  dialog: { maxWidth: 400, width: 'calc(100% - 32px)', padding: 18, borderRadius: 8, borderWidth: 1, borderStyle: 'solid', borderColor: '#c0392b', backgroundColor: colors.menu, color: colors.text, fontSize: 12, '::backdrop': { backgroundColor: 'rgba(0,0,0,0.55)' } },
  text: { margin: 0, lineHeight: 1.5, color: colors.textStrong, overflowWrap: 'anywhere' },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 14 },
  danger: { color: '#fff', backgroundColor: { default: '#c0392b', ':hover': '#a93226' }, borderWidth: 0, paddingInline: 10, paddingBlock: 5, borderRadius: 4, fontFamily: 'inherit', fontSize: 'inherit', cursor: { default: 'pointer', ':disabled': 'default' } },
  busy: { animationName: { default: pulse, [breakpoints.reducedMotion]: 'none' }, animationDuration: '1s', animationIterationCount: 'infinite' },
});
