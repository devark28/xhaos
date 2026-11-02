import { markdown } from './markdown.js';
const escape = value => String(value).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export function serializeRichText(node) {
  if(node.nodeType===3)return node.textContent;
  const tag=node.tagName?.toLowerCase();
  if(['script','style'].includes(tag))return '';
  if(tag==='br')return '\n';
  const text=[...(node.childNodes||[])].map(serializeRichText).join('');
  const weight=node.style?.fontWeight;
  const bold=tag==='b'||tag==='strong'||weight==='bold'||Number(weight)>=600;
  const italic=tag==='i'||tag==='em'||node.style?.fontStyle==='italic';
  if(tag==='pre')return `\n\`\`\`\n${node.textContent}\n\`\`\`\n`;
  if(tag==='code'&&node.parentElement?.tagName!=='PRE')return `\`${text}\``;
  if(tag==='li')return `- ${text.trim()}\n`;
  if(/^h[1-6]$/.test(tag))return `${'#'.repeat(Number(tag[1]))} ${text}\n`;
  if(tag==='blockquote')return text.split('\n').map(line=>`> ${line}`).join('\n')+'\n';
  if(tag==='a'){
    const href=node.getAttribute('href')||'';
    if(/^(https?:\/\/|mailto:)/i.test(href))return `[${text}](${href})`;
  }
  if(!text.replace(/\n/g,''))return text;
  const inline=(bold?'**':'')+(italic?'*':'')+text+(italic?'*':'')+(bold?'**':'');
  return inline+(['div','p','ul','ol'].includes(tag)?'\n':'');
}
export function richInput(id, value, label, placeholder) {
  return `<xhaos-input id="${escape(id)}" class="rich-input markdown-body" contenteditable="true" role="textbox" aria-multiline="true" aria-label="${escape(label)}" data-placeholder="${escape(placeholder)}" data-empty="${!value}" spellcheck="true">${markdown(value)}</xhaos-input>`;
}
const bookmarks=new Map();
if(globalThis.customElements&&!customElements.get('xhaos-input')) {
  customElements.define('xhaos-input',class extends HTMLElement {
    connectedCallback(){
      this.onSelection=()=>this.captureSelection();
      document.addEventListener('selectionchange',this.onSelection);
      this.addEventListener('focusout',this.onSelection);
    }
    disconnectedCallback(){document.removeEventListener('selectionchange',this.onSelection);this.removeEventListener('focusout',this.onSelection);}
    captureSelection(){
      const selection=window.getSelection();
      if(!selection.rangeCount)return;
      const range=selection.getRangeAt(0);
      if(!this.contains(range.startContainer)||!this.contains(range.endContainer))return;
      this.savedRange=range.cloneRange();
      const before=document.createRange();before.selectNodeContents(this);before.setEnd(range.startContainer,range.startOffset);
      const through=document.createRange();through.selectNodeContents(this);through.setEnd(range.endContainer,range.endOffset);
      bookmarks.set(this.id,{start:before.toString().length,end:through.toString().length});
    }
    restoreSelection(){
      const selection=window.getSelection();
      if(this.savedRange&&this.savedRange.startContainer.isConnected&&this.contains(this.savedRange.startContainer)){
        selection.removeAllRanges();selection.addRange(this.savedRange.cloneRange());return;
      }
      const saved=bookmarks.get(this.id);
      if(!saved){this.placeCaretAtEnd();return;}
      const nodes=[];const walker=document.createTreeWalker(this,NodeFilter.SHOW_TEXT);let node;
      while((node=walker.nextNode()))nodes.push(node);
      const point=offset=>{for(const text of nodes){if(offset<=text.length)return [text,offset];offset-=text.length;}return nodes.length?[nodes.at(-1),nodes.at(-1).length]:[this,0];};
      const range=document.createRange();range.setStart(...point(saved.start));range.setEnd(...point(saved.end));selection.removeAllRanges();selection.addRange(range);
    }
    get value(){return [...this.childNodes].map(serializeRichText).join('').replace(/\n+$/,'');}
    set value(text){this.innerHTML=markdown(text);this.dataset.empty=String(!text);this.savedRange=null;bookmarks.delete(this.id);this.moveCaretOnFocus=true;if(document.activeElement===this)this.placeCaretAtEnd();}
    placeCaretAtEnd(){const range=document.createRange();range.selectNodeContents(this);range.collapse(false);const selection=window.getSelection();selection.removeAllRanges();selection.addRange(range);this.moveCaretOnFocus=false;this.captureSelection();}
    focus(options){super.focus(options);if(this.moveCaretOnFocus)this.placeCaretAtEnd();else this.restoreSelection();}
    insertText(text){
      const selection=window.getSelection();if(!selection.rangeCount)this.placeCaretAtEnd();
      const range=selection.getRangeAt(0);if(!this.contains(range.commonAncestorContainer))return;
      range.deleteContents();const node=document.createTextNode(text);range.insertNode(node);range.setStartAfter(node);range.collapse(true);selection.removeAllRanges();selection.addRange(range);this.captureSelection();
      this.dispatchEvent(new InputEvent('input',{bubbles:true,inputType:'insertText',data:text}));
    }
  });
}
