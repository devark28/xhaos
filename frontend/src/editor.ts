// Wires a composer field: text changes, keys, and files from paste or drop.
// Large pasted text becomes a text attachment instead of flooding the field.
import { shouldAttachText, textFile } from './attachments.ts';
import type { XhaosInput } from './rich-input.ts';

export interface EditorHooks {
  addFiles(files: File[]): void;
  setText(value: string): void;
  onText?(): void;
  onKeyDown?(event: KeyboardEvent): void;
  onDragging?(dragging: boolean): void;
  notify(message: string): void;
}

export function bindEditor(input: XhaosInput, holder: HTMLElement, { addFiles, setText, onText, onKeyDown, onDragging, notify }: EditorHooks) {
  const changed = () => {
    setText(input.value);
    input.dataset.empty = String(!input.value.trim());
    onText?.();
  };
  // Pasted text goes in like typing, so Ctrl+Z undoes the paste itself.
  const insertText = (text: string) => {
    input.focus();
    input.pasteText(text); // its own undo step; fires input, which runs changed()
  };
  const pasteOrDrop = (data: DataTransfer) => {
    if (data.files.length) { addFiles([...data.files]); return; }
    const text = data.getData('text/plain');
    if (shouldAttachText(text)) { addFiles([textFile(text)]); notify('Large paste added as a text attachment.'); }
    else insertText(text);
  };
  const hasFiles = (event: DragEvent) => [...(event.dataTransfer?.types || [])].includes('Files');

  const onInput = () => changed();
  const onPaste = (event: ClipboardEvent) => { if (!event.clipboardData) return; event.preventDefault(); pasteOrDrop(event.clipboardData); };
  const onKey = (event: KeyboardEvent) => { if (!event.isComposing) onKeyDown?.(event); };
  const onDragOver = (event: DragEvent) => { if (hasFiles(event)) { event.preventDefault(); onDragging?.(true); } };
  const onDragLeave = (event: DragEvent) => { if (!holder.contains(event.relatedTarget as Node | null)) onDragging?.(false); };
  const onDrop = (event: DragEvent) => {
    onDragging?.(false);
    if (!event.dataTransfer) return;
    if (event.dataTransfer.files.length || input.contains(event.target as Node)) { event.preventDefault(); pasteOrDrop(event.dataTransfer); }
  };
  input.addEventListener('input', onInput);
  input.addEventListener('paste', onPaste);
  input.addEventListener('keydown', onKey);
  holder.addEventListener('dragover', onDragOver);
  holder.addEventListener('dragleave', onDragLeave);
  holder.addEventListener('drop', onDrop);
  return () => {
    input.removeEventListener('input', onInput);
    input.removeEventListener('paste', onPaste);
    input.removeEventListener('keydown', onKey);
    holder.removeEventListener('dragover', onDragOver);
    holder.removeEventListener('dragleave', onDragLeave);
    holder.removeEventListener('drop', onDrop);
  };
}
