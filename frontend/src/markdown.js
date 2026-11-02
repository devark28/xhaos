import { highlight } from './highlight.js';
const esc = text => String(text).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function inline(text) {
  const pattern = /(`[^`\n]+`|\*\*\*[^*\n]+\*\*\*|\*\*[^*\n]+\*\*|\*[^*\n]+\*|\[[^\]\n]+\]\([^\s)]+\))/g;
  let result='', end=0;
  for(const match of text.matchAll(pattern)) {
    result+=esc(text.slice(end,match.index));
    const token=match[0];
    if(token.startsWith('`')) result+=`<code>${esc(token.slice(1,-1))}</code>`;
    else if(token.startsWith('***')) result+=`<strong><em>${esc(token.slice(3,-3))}</em></strong>`;
    else if(token.startsWith('**')) result+=`<strong>${esc(token.slice(2,-2))}</strong>`;
    else if(token.startsWith('*')) result+=`<em>${esc(token.slice(1,-1))}</em>`;
    else {
      const [,label,url]=token.match(/^\[([^\]]+)\]\((.+)\)$/);
      result+=/^(https?:\/\/|mailto:)/i.test(url) ? `<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(label)}</a>` : esc(token);
    }
    end=match.index+token.length;
  }
  return result+esc(text.slice(end));
}
export function markdown(text) {
  const lines=String(text).split('\n');
  let html='', paragraph=[], list=null, fence=null, code=[];
  const flush=()=>{if(paragraph.length){html+=`<p>${paragraph.map(inline).join('<br>')}</p>`;paragraph=[];}if(list){html+=`</${list}>`;list=null;}};
  for(const line of lines) {
    if(fence!==null){if(/^```\s*$/.test(line)){html+=`<pre><code>${highlight(code.join('\n'),fence)}</code></pre>`;fence=null;code=[];}else code.push(line);continue;}
    const open=line.match(/^```([\w-]*)\s*$/);
    if(open){flush();fence=({js:'javascript',rs:'rust',sh:'bash'})[open[1]]||open[1]||'text';continue;}
    if(!line.trim()){flush();continue;}
    const heading=line.match(/^(#{1,6})\s+(.+)$/), item=line.match(/^\s*(?:([-*])|\d+\.)\s+(.+)$/);
    if(heading){flush();html+=`<h${heading[1].length}>${inline(heading[2])}</h${heading[1].length}>`;}
    else if(item){const type=item[1]?'ul':'ol';if(list!==type){flush();list=type;html+=`<${type}>`;}html+=`<li>${inline(item[2])}</li>`;}
    else if(/^>\s?/.test(line)){flush();html+=`<blockquote>${inline(line.replace(/^>\s?/,''))}</blockquote>`;}
    else {if(list)flush();paragraph.push(line);}
  }
  if(fence!==null)html+=`<pre><code>${highlight(code.join('\n'),fence)}</code></pre>`;
  flush();return html;
}
export function formatSelection(text, start, end, kind) {
  const selected=text.slice(start,end), wraps={bold:['**','**'],italic:['*','*'],code:['`','`'],link:['[','](https://)'],block:['```\n','\n```']};
  if(kind==='list'||kind==='heading') {
    const lineStart=text.lastIndexOf('\n',start-1)+1;
    const prefix=kind==='list'?'- ':'## ';
    const content=text.slice(lineStart,end).split('\n').map(line=>prefix+line).join('\n');
    return {text:text.slice(0,lineStart)+content+text.slice(end),start:lineStart,end:lineStart+content.length};
  }
  const [left,right]=wraps[kind];
  return {text:text.slice(0,start)+left+selected+right+text.slice(end),start:start+left.length,end:end+left.length};
}
