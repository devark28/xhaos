import { shouldAttachText, textFile, fileSize } from './attachments.js';
const esc = text => String(text).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export function editorExtras(id) {
  return `<div class="editor-tools" data-editor="${id}"><button type="button" data-pick title="Attach files" aria-label="Attach files">＋</button><input type="file" data-files multiple hidden></div><div class="pending-files" data-pending="${id}"></div>`;
}
export function bindEditor(id, { getFiles, setText, onText, onKeyDown, notify }) {
  const input=document.getElementById(id), tools=document.querySelector(`[data-editor="${id}"]`), holder=input.closest('form');
  const pending=document.querySelector(`[data-pending="${id}"]`);
  const paintFiles=()=>{
    pending.innerHTML=getFiles().map((file,index)=>`<span class="file-chip"><span title="${esc(file.type||'File')}">${esc(file.name)} <small>${fileSize(file.size)}</small></span><button type="button" data-remove="${index}" aria-label="Remove ${esc(file.name)}">×</button></span>`).join('');
    pending.querySelectorAll('[data-remove]').forEach(button=>button.onclick=()=>{getFiles().splice(Number(button.dataset.remove),1);paintFiles();});
  };
  const add=files=>{getFiles().push(...files);paintFiles();};
  const changed=()=>{setText(input.value);input.dataset.empty=String(!input.value.trim());onText?.();};
  input.oninput=changed;
  const insertText=text=>{
    input.focus();const selection=window.getSelection();
    if(!selection.rangeCount)return;
    const range=selection.getRangeAt(0);
    if(!input.contains(range.commonAncestorContainer))return;
    range.deleteContents();const node=document.createTextNode(text);range.insertNode(node);range.setStartAfter(node);range.collapse(true);selection.removeAllRanges();selection.addRange(range);changed();
  };
  tools.querySelector('[data-pick]').onclick=()=>tools.querySelector('[data-files]').click();
  tools.querySelector('[data-files]').onchange=e=>{add([...e.target.files]);e.target.value='';};
  input.onpaste=e=>{
    if(e.clipboardData.files.length){e.preventDefault();add([...e.clipboardData.files]);return;}
    const text=e.clipboardData.getData('text/plain');
    if(shouldAttachText(text)){e.preventDefault();add([textFile(text)]);notify('Large paste added as a text attachment.');}
    else {e.preventDefault();insertText(text);}
  };
  input.onkeydown=e=>{if(e.isComposing)return;onKeyDown?.(e);};
  holder.ondragover=e=>{if([...e.dataTransfer.types].includes('Files')){e.preventDefault();holder.classList.add('drag-files');}};
  holder.ondragleave=e=>{if(!holder.contains(e.relatedTarget))holder.classList.remove('drag-files');};
  holder.ondrop=e=>{holder.classList.remove('drag-files');if(e.dataTransfer.files.length){e.preventDefault();add([...e.dataTransfer.files]);}else if(input.contains(e.target)){e.preventDefault();const text=e.dataTransfer.getData('text/plain');if(shouldAttachText(text))add([textFile(text)]);else insertText(text);}};
  paintFiles();
}
