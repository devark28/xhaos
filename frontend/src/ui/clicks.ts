// Agent and project rows: a click toggles; a double-click attaches the row
// to the composer instead. Telling them apart means a single click waits
// briefly for a possible second one (WAIT). Ctrl/Cmd+click attaches at once,
// keyboard activation (detail 0) toggles at once, and Alt+Enter attaches.
import type { KeyboardEvent, MouseEvent } from 'react';

const WAIT = 220; // ms; most double-clicks land well within this
const timers = new Map<string, ReturnType<typeof setTimeout>>();
const clear = (key: string) => { clearTimeout(timers.get(key)); timers.delete(key); };

export function onToggleClick(event: MouseEvent, key: string, toggle: () => void, attach: () => void) {
  clear(key);
  if (event.ctrlKey || event.metaKey) { event.preventDefault(); attach(); return; }
  if (event.detail === 0) { toggle(); return; }
  if (event.detail === 2) { attach(); return; }
  if (event.detail > 2) return;
  timers.set(key, setTimeout(() => { timers.delete(key); toggle(); }, WAIT));
}

export function onAttachKey(event: KeyboardEvent, key: string, attach: () => void) {
  if (event.altKey && event.key === 'Enter') { event.preventDefault(); event.stopPropagation(); clear(key); attach(); }
}

// clearClick drops a pending single-click toggle (e.g. the chevron was used).
export const clearClick = clear;
