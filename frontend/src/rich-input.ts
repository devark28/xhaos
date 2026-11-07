// <xhaos-input>: a contenteditable field whose value is Markdown. Pasted or
// typed formatting (bold, italic, code, lists, links) serializes back to the
// Markdown the model sees; setting value renders Markdown as HTML.
import { markdown } from './markdown.ts';

// The parts of a DOM node serialization reads (tests pass plain objects).
interface RichNode {
  nodeType: number;
  textContent?: string | null;
  tagName?: string;
  childNodes?: ArrayLike<RichNode>;
  style?: { fontWeight?: string; fontStyle?: string };
  parentElement?: { tagName: string } | null;
  getAttribute?(name: string): string | null;
}

export function serializeRichText(node: RichNode): string {
  if (node.nodeType === 3) return node.textContent || '';
  const tag = node.tagName?.toLowerCase() || '';
  if (tag === 'script' || tag === 'style') return '';
  if (tag === 'br') return '\n';
  const text = Array.from(node.childNodes || []).map(serializeRichText).join('');
  const weight = node.style?.fontWeight;
  const bold = tag === 'b' || tag === 'strong' || weight === 'bold' || Number(weight) >= 600;
  const italic = tag === 'i' || tag === 'em' || node.style?.fontStyle === 'italic';
  if (tag === 'pre') return `\n\`\`\`\n${node.textContent}\n\`\`\`\n`;
  if (tag === 'code' && node.parentElement?.tagName !== 'PRE') return `\`${text}\``;
  if (tag === 'li') return `- ${text.trim()}\n`;
  if (/^h[1-6]$/.test(tag)) return `${'#'.repeat(Number(tag[1]))} ${text}\n`;
  if (tag === 'blockquote') return text.split('\n').map(line => `> ${line}`).join('\n') + '\n';
  if (tag === 'a') {
    const href = node.getAttribute?.('href') || '';
    if (/^(https?:\/\/|mailto:)/i.test(href)) return `[${text}](${href})`;
  }
  if (!text.replace(/\n/g, '')) return text;
  const inline = (bold ? '**' : '') + (italic ? '*' : '') + text + (italic ? '*' : '') + (bold ? '**' : '');
  return inline + (['div', 'p', 'ul', 'ol'].includes(tag) ? '\n' : '');
}


// Caret positions by element ID, as character offsets, so focus can return
// to the same place after the element's DOM was replaced.
const bookmarks = new Map<string, { start: number; end: number }>();

// Node (tests) has no DOM; the class is only defined in a browser.
const Base = (globalThis.HTMLElement ?? class {}) as typeof HTMLElement;

export class XhaosInput extends Base {
  savedRange: Range | null = null;
  moveCaretOnFocus = false;
  private onSelection = () => this.captureSelection();

  connectedCallback() {
    document.addEventListener('selectionchange', this.onSelection);
    this.addEventListener('focusout', this.onSelection);
  }
  disconnectedCallback() {
    document.removeEventListener('selectionchange', this.onSelection);
    this.removeEventListener('focusout', this.onSelection);
  }
  captureSelection() {
    const selection = window.getSelection();
    if (!selection?.rangeCount) return;
    const range = selection.getRangeAt(0);
    if (!this.contains(range.startContainer) || !this.contains(range.endContainer)) return;
    this.savedRange = range.cloneRange();
    const before = document.createRange(); before.selectNodeContents(this); before.setEnd(range.startContainer, range.startOffset);
    const through = document.createRange(); through.selectNodeContents(this); through.setEnd(range.endContainer, range.endOffset);
    bookmarks.set(this.id, { start: before.toString().length, end: through.toString().length });
  }
  restoreSelection() {
    const selection = window.getSelection();
    if (!selection) return;
    if (this.savedRange?.startContainer.isConnected && this.contains(this.savedRange.startContainer)) {
      selection.removeAllRanges(); selection.addRange(this.savedRange.cloneRange()); return;
    }
    const saved = bookmarks.get(this.id);
    if (!saved) { this.placeCaretAtEnd(); return; }
    const nodes: Text[] = [];
    const walker = document.createTreeWalker(this, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) nodes.push(node as Text);
    const point = (offset: number): [Node, number] => {
      for (const text of nodes) { if (offset <= text.length) return [text, offset]; offset -= text.length; }
      return nodes.length ? [nodes[nodes.length - 1], nodes[nodes.length - 1].length] : [this, 0];
    };
    const range = document.createRange(); range.setStart(...point(saved.start)); range.setEnd(...point(saved.end));
    selection.removeAllRanges(); selection.addRange(range);
  }
  get value(): string {
    return Array.from(this.childNodes as NodeListOf<ChildNode & RichNode>).map(serializeRichText).join('').replace(/\n+$/, '');
  }
  // Setting the value while the field has focus goes through the browser's
  // editing commands, so Ctrl+Z can undo it (a turned-into-chip "#12 ", a
  // chosen slash command, a dictated transcript). Replacing innerHTML, the
  // fallback, would wipe the field's undo history.
  set value(text: string) {
    if (document.activeElement === this && this.value !== text && this.replaceUndoably(text)) {
      this.dataset.empty = String(!text);
      this.captureSelection();
      return;
    }
    this.innerHTML = markdown(text);
    this.dataset.empty = String(!text);
    this.savedRange = null;
    bookmarks.delete(this.id);
    this.moveCaretOnFocus = true;
    if (document.activeElement === this) this.placeCaretAtEnd();
  }
  // placeCaretAtEnd puts the caret at the end of the last text, not after the
  // last paragraph: Firefox would otherwise type outside it, on a new line.
  placeCaretAtEnd() {
    const range = document.createRange();
    const walker = document.createTreeWalker(this, NodeFilter.SHOW_TEXT);
    let last: Text | null = null;
    for (let node = walker.nextNode(); node; node = walker.nextNode()) last = node as Text;
    if (last) range.setStart(last, last.length); else { range.selectNodeContents(this); }
    range.collapse(!last ? false : true);
    const selection = window.getSelection();
    selection?.removeAllRanges(); selection?.addRange(range);
    this.moveCaretOnFocus = false;
    this.captureSelection();
  }
  focus(options?: FocusOptions) {
    super.focus(options);
    if (this.moveCaretOnFocus) this.placeCaretAtEnd(); else this.restoreSelection();
  }
  // replaceUndoably swaps the whole content as one undoable edit.
  private replaceUndoably(text: string): boolean {
    const selection = window.getSelection();
    if (!selection) return false;
    const all = document.createRange();
    all.selectNodeContents(this);
    selection.removeAllRanges(); selection.addRange(all);
    return text ? document.execCommand('insertHTML', false, markdown(text)) : document.execCommand('delete');
  }
  // insertText types text at the caret as the browser would (undoable, and it
  // fires the input event); hand-made DOM edits are the fallback.
  insertText(text: string) {
    const selection = window.getSelection();
    if (!selection) return;
    if (!selection.rangeCount) this.placeCaretAtEnd();
    if (this.contains(selection.getRangeAt(0).commonAncestorContainer) && document.execCommand('insertText', false, text)) {
      this.captureSelection();
      return;
    }
    const range = selection.getRangeAt(0);
    if (!this.contains(range.commonAncestorContainer)) return;
    range.deleteContents();
    const node = document.createTextNode(text);
    range.insertNode(node); range.setStartAfter(node); range.collapse(true);
    selection.removeAllRanges(); selection.addRange(range);
    this.captureSelection();
    this.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: text }));
  }
}

if (globalThis.customElements && !customElements.get('xhaos-input')) customElements.define('xhaos-input', XhaosInput);

declare global {
  interface HTMLElementTagNameMap { 'xhaos-input': XhaosInput }
}
