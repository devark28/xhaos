// Keys in a confirm dialog: Tab and Shift+Tab cycle through its buttons and
// never leave it, Escape cancels, Enter confirms. Enter on a focused button
// is left to the browser, which clicks that button (Cancel cancels).
export type DialogKey =
  | { kind: 'focus'; index: number }
  | { kind: 'cancel' }
  | { kind: 'confirm' }
  | { kind: 'default' };

type KeyLike = Pick<KeyboardEvent, 'key' | 'shiftKey' | 'ctrlKey' | 'metaKey' | 'altKey' | 'isComposing'>;

// dialogKey says what a key does. focused is the index of the focused button
// among the dialog's count enabled buttons, or -1 when none has focus.
export function dialogKey(event: KeyLike, focused: number, count: number): DialogKey {
  if (event.isComposing || event.ctrlKey || event.metaKey || event.altKey) return { kind: 'default' };
  switch (event.key) {
    case 'Escape':
      return { kind: 'cancel' };
    case 'Enter':
      return focused >= 0 ? { kind: 'default' } : { kind: 'confirm' };
    case 'Tab': {
      if (count === 0) return { kind: 'focus', index: -1 }; // nothing to move to: stay put
      const step = event.shiftKey ? -1 : 1;
      const index = focused < 0 ? (event.shiftKey ? count - 1 : 0) : (focused + step + count) % count;
      return { kind: 'focus', index };
    }
    default:
      return { kind: 'default' };
  }
}
