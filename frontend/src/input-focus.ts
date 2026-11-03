// Typing anywhere on the page (outside a field) continues in the last-used
// composer field, so the owner never has to click into it first.
import type { XhaosInput } from './rich-input.ts';

interface TypingContext { editing?: boolean; menuOpen?: boolean; textSelected?: boolean; activationTarget?: boolean }
type KeyLike = Pick<KeyboardEvent, 'defaultPrevented' | 'isComposing' | 'ctrlKey' | 'metaKey' | 'altKey' | 'key'>;

export function shouldResumeTyping(event: KeyLike, { editing = false, menuOpen = false, textSelected = false, activationTarget = false }: TypingContext = {}): boolean {
  return !event.defaultPrevented && !event.isComposing && !event.ctrlKey && !event.metaKey && !event.altKey
    && event.key.length === 1 && !editing && !menuOpen && !textSelected && !(event.key === ' ' && activationTarget);
}

export function installTypingFlow(doc: Document, win: Window) {
  let lastEditor = 'command';
  doc.addEventListener('focusin', event => {
    const editor = (event.target as Element).closest?.('xhaos-input');
    if (editor) lastEditor = editor.id;
  });
  // Capture the caret before a mouse click moves browser focus/selection elsewhere.
  doc.addEventListener('pointerdown', () => (doc.activeElement as XhaosInput | null)?.captureSelection?.(), true);
  win.addEventListener('keydown', event => {
    const focused = doc.activeElement as HTMLElement | null;
    const selection = win.getSelection();
    const resume = shouldResumeTyping(event, {
      editing: Boolean(focused?.isContentEditable || focused?.closest?.('input,textarea,select')),
      menuOpen: Boolean(doc.querySelector('[data-agent-menu][open]') || !(doc.querySelector('#command-menu') as HTMLElement | null)?.hidden),
      textSelected: Boolean(selection && !selection.isCollapsed && !selection.anchorNode?.parentElement?.closest('xhaos-input')),
      activationTarget: Boolean(focused?.closest?.('button,a,summary')),
    });
    if (!resume) return;
    const editor = (doc.getElementById(lastEditor) || doc.getElementById('command')) as XhaosInput | null;
    if (!editor || editor.closest('[hidden]') || (editor.closest('form') as HTMLFormElement | null)?.inert) return;
    event.preventDefault();
    editor.focus({ preventScroll: true });
    editor.insertText(event.key);
  });
}
