export function shouldResumeTyping(event, {editing=false, menuOpen=false, textSelected=false, activationTarget=false}={}) {
  return !event.defaultPrevented&&!event.isComposing&&!event.ctrlKey&&!event.metaKey&&!event.altKey&&
    event.key.length===1&&!editing&&!menuOpen&&!textSelected&&!(event.key===' '&&activationTarget);
}
export function installTypingFlow(doc, win) {
  let lastEditor='command';
  doc.addEventListener('focusin',event=>{
    const editor=event.target.closest?.('xhaos-input');
    if(editor)lastEditor=editor.id;
  });
  // Capture the caret before a mouse click moves browser focus/selection elsewhere.
  doc.addEventListener('pointerdown',()=>doc.activeElement?.captureSelection?.(),true);
  win.addEventListener('keydown',event=>{
    const focused=doc.activeElement;
    const selection=win.getSelection();
    if(!shouldResumeTyping(event,{
      editing:Boolean(focused?.isContentEditable||focused?.closest?.('input,textarea,select')),
      menuOpen:Boolean(doc.querySelector('.agent-menu[open]')||!doc.querySelector('#command-menu')?.hidden),
      textSelected:Boolean(selection&&!selection.isCollapsed&&!selection.anchorNode?.parentElement?.closest('xhaos-input')),
      activationTarget:Boolean(focused?.closest?.('button,a,summary'))
    }))return;
    const editor=doc.getElementById(lastEditor)||doc.getElementById('command');
    if(!editor||editor.closest('form')?.inert)return;
    event.preventDefault();
    editor.focus({preventScroll:true});
    editor.insertText(event.key);
  });
}
