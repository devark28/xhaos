// One click toggles (after a short delay); a double-click attaches the item
// to the composer instead. Keyboard activation (detail 0) toggles at once,
// and Alt+Enter attaches. Agent rows and project rows share this.
import type { KeyboardEvent, MouseEvent } from 'react';

const timers = new Map<string, ReturnType<typeof setTimeout>>();
export const clearClick = (key: string) => { clearTimeout(timers.get(key)); timers.delete(key); };

export function onToggleClick(event: MouseEvent, key: string, toggle: () => void, attach: () => void) {
  clearClick(key);
  if (event.detail >= 2) { if (event.detail === 2) attach(); return; }
  if (event.detail === 0) { toggle(); return; }
  timers.set(key, setTimeout(() => { timers.delete(key); toggle(); }, 300));
}

export function onAttachKey(event: KeyboardEvent, key: string, attach: () => void) {
  if (event.altKey && event.key === 'Enter') { event.preventDefault(); event.stopPropagation(); clearClick(key); attach(); }
}
