import { highlight } from './highlight.ts';
const esc = (text: unknown): string => String(text).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'} as Record<string, string>)[c]);
// #12 in a transcript becomes a link to agent 12's card (main.tsx handles
// the click). Off for the composer, where #12 is typed text.
let agentLinks=false;
const plain=(text: string): string => agentLinks ? esc(text).replace(/(^|[^\w&#/])#(\d+)\b/g,'$1<a href="#" data-agent-link="$2">#$2</a>') : esc(text);
function inline(text: string): string {
  const pattern = /(`[^`\n]+`|\*\*\*[^*\n]+\*\*\*|\*\*[^*\n]+\*\*|\*[^*\n]+\*|\[[^\]\n]+\]\([^\s)]+\))/g;
  let result='', end=0;
  for(const match of text.matchAll(pattern)) {
    result+=plain(text.slice(end,match.index!));
    const token=match[0];
    if(token.startsWith('`')) result+=`<code>${esc(token.slice(1,-1))}</code>`;
    else if(token.startsWith('***')) result+=`<strong><em>${esc(token.slice(3,-3))}</em></strong>`;
    else if(token.startsWith('**')) result+=`<strong>${esc(token.slice(2,-2))}</strong>`;
    else if(token.startsWith('*')) result+=`<em>${esc(token.slice(1,-1))}</em>`;
    else {
      const [,label,url]=token.match(/^\[([^\]]+)\]\((.+)\)$/)!;
      result+=/^(https?:\/\/|mailto:)/i.test(url) ? `<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(label)}</a>` : esc(token);
    }
    end=match.index!+token.length;
  }
  return result+plain(text.slice(end));
}
// GitHub-style tables: a header row, a separator row (---, :--, --:, :-:),
// then body rows. Cells split on | (not inside `code`); outer pipes optional.
const tableSep=/^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
function cells(row: string): string[] {
  const out: string[]=[]; let cur='', code=false;
  const s=row.trim().replace(/^\|/,'').replace(/\|$/,'');
  for(let i=0;i<s.length;i++){const c=s[i];
    if(c==='\\'&&s[i+1]==='|'){cur+='|';i++;continue;}
    if(c==='`')code=!code;
    if(c==='|'&&!code){out.push(cur.trim());cur='';}else cur+=c;}
  out.push(cur.trim()); return out;
}
function table(head: string, sep: string, rows: string[]): string {
  const align=cells(sep).map(c=>c.startsWith(':')&&c.endsWith(':')?'center':c.endsWith(':')?'right':'');
  const td=(tag: string, row: string)=>`<tr>${cells(row).map((c,i)=>`<${tag}${align[i]?` style="text-align:${align[i]}"`:''}>${inline(c)}</${tag}>`).join('')}</tr>`;
  return `<div class="md-table"><table><thead>${td('th',head)}</thead><tbody>${rows.map(r=>td('td',r)).join('')}</tbody></table></div>`;
}
export function markdown(text: unknown, options: { agentLinks?: boolean } = {}): string {
  agentLinks=Boolean(options.agentLinks);
  try { return render(text); } finally { agentLinks=false; }
}
function render(text: unknown): string {
  const lines=String(text).split('\n');
  let html='', paragraph: string[]=[], list: string|null=null, fence: string|null=null, code: string[]=[];
  const flush=()=>{if(paragraph.length){html+=`<p>${paragraph.map(inline).join('<br>')}</p>`;paragraph=[];}if(list){html+=`</${list}>`;list=null;}};
  for(let n=0;n<lines.length;n++) {
    const line=lines[n];
    if(fence!==null){if(/^```\s*$/.test(line)){html+=`<pre><code>${highlight(code.join('\n'),fence)}</code></pre>`;fence=null;code=[];}else code.push(line);continue;}
    const open=line.match(/^```([\w-]*)\s*$/);
    if(open){flush();fence=({js:'javascript',rs:'rust',sh:'bash'} as Record<string, string>)[open[1]]||open[1]||'text';continue;}
    if(!line.trim()){flush();continue;}
    if(line.includes('|')&&n+1<lines.length&&tableSep.test(lines[n+1])&&lines[n+1].includes('|')&&cells(lines[n+1]).length===cells(line).length){
      flush(); const rows: string[]=[]; let k=n+2;
      while(k<lines.length&&lines[k].trim()&&lines[k].includes('|')){rows.push(lines[k]);k++;}
      html+=table(line,lines[n+1],rows); n=k-1; continue;
    }
    const heading=line.match(/^(#{1,6})\s+(.+)$/), item=line.match(/^\s*(?:([-*])|\d+\.)\s+(.+)$/);
    if(heading){flush();html+=`<h${heading[1].length}>${inline(heading[2])}</h${heading[1].length}>`;}
    else if(item){const type=item[1]?'ul':'ol';if(list!==type){flush();list=type;html+=`<${type}>`;}html+=`<li>${inline(item[2])}</li>`;}
    else if(/^>\s?/.test(line)){flush();html+=`<blockquote>${inline(line.replace(/^>\s?/,''))}</blockquote>`;}
    else {if(list)flush();paragraph.push(line);}
  }
  if(fence!==null)html+=`<pre><code>${highlight(code.join('\n'),fence)}</code></pre>`;
  flush();return html;
}
export type Format = 'bold'|'italic'|'code'|'link'|'block'|'list'|'heading';
export function formatSelection(text: string, start: number, end: number, kind: Format) {
  const selected=text.slice(start,end), wraps: Record<string, [string, string]>={bold:['**','**'],italic:['*','*'],code:['`','`'],link:['[','](https://)'],block:['```\n','\n```']};
  if(kind==='list'||kind==='heading') {
    const lineStart=text.lastIndexOf('\n',start-1)+1;
    const prefix=kind==='list'?'- ':'## ';
    const content=text.slice(lineStart,end).split('\n').map(line=>prefix+line).join('\n');
    return {text:text.slice(0,lineStart)+content+text.slice(end),start:lineStart,end:lineStart+content.length};
  }
  const [left,right]=wraps[kind];
  return {text:text.slice(0,start)+left+selected+right+text.slice(end),start:start+left.length,end:end+left.length};
}
