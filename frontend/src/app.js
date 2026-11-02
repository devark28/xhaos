import { installTypingFlow } from './input-focus.js';
import { richInput } from './rich-input.js';
import { editorExtras, bindEditor } from './editor.js';
import { storeFiles, readFile, fileSize } from './attachments.js';
import { markdown } from './markdown.js';
import { createAttentionTracker, agentPath } from './attention.js';
import { watchStickyColumns } from './sticky-columns.js';
import { highlight, highlightDiff, languageFor } from './highlight.js';
import { hydrateDemoActivity } from './demo-activity.js';
import { initialWorkspaces, flatten, transition, activeAgents, archiveAgent, restoreAgent, terminateAgent as markTerminated } from './model.js';
const $ = s => document.querySelector(s);
const esc = s => String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let workspaces;
try { workspaces = JSON.parse(localStorage.getItem('xhaos-v1')) || initialWorkspaces(); } catch { workspaces = initialWorkspaces(); }
hydrateDemoActivity(workspaces);
let active = workspaces[0].id, search = '', layout = 'grid', collapsed = new Set(workspaces.flatMap(w => flatten(w.agents)).map(a => a.id)), draft = '', agentDraft = null;
const pendingFiles = [];
const selectedAgents = new Set();
let sending = false;
const blobUrls = new Map();
let stopWatchingColumns = () => {};
const newlyWaitingAgents = createAttentionTracker();
const workspace = () => workspaces.find(w => w.id === active);
const save = () => { try { localStorage.setItem('xhaos-v1', JSON.stringify(workspaces)); } catch { toast('Browser storage unavailable; changes last for this session.'); } };
const labels = { running: 'Running', waiting: 'Needs you', paused: 'Paused', complete: 'Complete', terminated: 'Terminated' };
const icon = (name, size=18) => `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${({grid:'<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',list:'<path d="M4 5h16M4 12h16M4 19h16"/>',plus:'<path d="M12 5v14M5 12h14"/>',arrow:'<path d="m9 5 7 7-7 7"/>',send:'<path d="m4 12 16-8-6 16-3-7-7-1Z"/>',mic:'<rect x="9" y="2" width="6" height="13" rx="3"/><path d="M5 10v2a7 7 0 0 0 14 0v-2M12 19v3m-4 0h8"/>',search:'<circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/>',bolt:'<path d="m13 2-9 12h7l-1 8 10-12h-7l1-8Z"/>',plug:'<path d="M8 2v5m8-5v5M6 7h12v4a6 6 0 0 1-12 0V7Zm6 10v5"/>',chat:'<path d="M4 4h16v13H9l-5 4V4Z"/>',settings:'<path d="M9.2 3.5 9.8 2h4.4l.6 1.5.4 1.6 1.5.9 1.6-.5 1.6.2 2.2 3.8-.8 1.3-1.2 1.1v1.8l1.2 1.1.8 1.3-2.2 3.8-1.6.2-1.6-.5-1.5.9-.4 1.6-.6 1.5H9.8l-.6-1.5-.4-1.6-1.5-.9-1.6.5-1.6-.2-2.2-3.8.8-1.3 1.2-1.1v-1.8l-1.2-1.1-.8-1.3 2.2-3.8 1.6-.2 1.6.5 1.5-.9.4-1.6Z"/><circle cx="12" cy="12" r="3"/>',check:'<path d="m5 12 4 4L19 6"/>',pause:'<path d="M9 5v14M15 5v14"/>',close:'<path d="m6 6 12 12M6 18 18 6"/>',terminal:'<path d="m4 5 6 7-6 7m9 0h7"/>'})[name] || ''}</svg>`;
function badge(a) { if(a.status==='paused'||a.status==='terminated')return '';return `<span class="status-dot ${a.status}" role="img" aria-label="${labels[a.status]}" title="${labels[a.status]}"></span>`; }
function descendantAttention(a) {
 return a.status !== 'waiting' && activeAgents(a.children || []).some(agent => agent.status === 'waiting')
  ? '<span class="status-dot waiting" role="img" aria-label="Subagent needs your attention" title="Subagent needs your attention"></span>' : '';
}
function attachedFiles(files = []) {
 return files.length ? `<div class="message-files">${files.map(file=>`<div><button type="button" class="text-button" data-download="${esc(file.id)}">${esc(file.name)} <small>${fileSize(file.size)}</small> ↓</button>${/^image\/(png|jpeg|webp|gif|avif)$/.test(file.type)?`<button type="button" class="text-button" data-image="${esc(file.id)}" aria-expanded="false">View image</button>`:''}<div data-image-slot="${esc(file.id)}" hidden></div></div>`).join('')}</div>` : '';
}
async function loadAttachment(id) {
 const file=await readFile(id);
 if(!blobUrls.has(id))blobUrls.set(id,URL.createObjectURL(file.blob));
 return {...file,url:blobUrls.get(id)};
}
function activity(a) {
 const entries = a.activity || [{ kind: 'assistant', text: a.message }];
 const names = {user:'you',assistant:'agent',tool:'tool',diff:'edit',approval:'approval',system:'system'};
 return `<div class="transcript" data-transcript="${a.id}" aria-label="Agent ${a.id} activity">${a.demo?'<div class="sample-label">Sample transcript · commands are illustrative</div>':''}${entries.map(entry=>{
  const kind=names[entry.kind]?entry.kind:'assistant';
  if(kind==='tool'||kind==='diff') return `<details class="tool-entry"><summary><span class="entry-kind">${names[kind]}</span><span class="tool-title">${esc(entry.text)}</span><code>${highlight(entry.command||'',kind==='diff'?'text':'bash')}</code></summary><pre><code>${kind==='diff'?highlightDiff(entry.output||'',languageFor(entry.command||'')):highlight(entry.output||'',languageFor('',entry.output||''))}</code></pre></details>`;
  if(kind==='approval') return `<div class="request-entry"><span class="entry-kind">approval</span><p>${esc(entry.text)}</p><pre><code>${highlight(entry.command||'','javascript')}</code></pre><p class="request-context">${esc(entry.output||'')}</p></div>`;
  return `<div class="activity-entry ${kind==='user'?'user-entry':kind==='system'?'system-entry':''}" aria-label="${kind==='user'?'Your message':kind==='system'?'Status update':'Agent response'}"><div class="markdown-body">${markdown(entry.text)}</div>${attachedFiles(entry.attachments)}</div>`;
 }).join('')}${(a.notes||[]).map(note=>`<div class="activity-entry user-entry" aria-label="Your message"><div class="markdown-body">${markdown(note)}</div></div>`).join('')}</div>`;
}
function removeControl(a) {
 return `<details class="agent-menu"><summary aria-label="Actions for agent ${a.id}" title="Agent actions"><svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.7"/><circle cx="12" cy="12" r="1.7"/><circle cx="19" cy="12" r="1.7"/></svg></summary><div class="agent-menu-items"><button type="button" data-menu-target="${a.id}">${selectedAgents.has(a.id)?'Detach from':'Attach to'} composer</button><button type="button" data-terminate="${a.id}">Terminate agent</button></div></details>`;
}
function subagentChips(a) {
 const subs=(a.children||[]).filter(agent=>!agent.archivedAt);
 return subs.length?`<div class="subagent-chips" aria-label="Subagents">${subs.map(agent=>`<button type="button" data-reveal="${agent.id}" data-parent="${a.id}" aria-expanded="${!collapsed.has(agent.id)}" title="${collapsed.has(agent.id)?'Expand':'Collapse'} subagent #${agent.id}: ${esc(agent.title)}">${badge(agent)}#${agent.id}</button>`).join('')}</div>`:'';
}
function idControl(a) {
 return `<button class="agent-id id-target" data-target="${a.id}" aria-pressed="${selectedAgents.has(a.id)}" aria-label="${selectedAgents.has(a.id)?'Detach':'Attach'} agent ${a.id} ${selectedAgents.has(a.id)?'from':'to'} composer" title="Attach/detach agent in composer">#${a.id}</button>`;
}
function child(a) {
 const closed=collapsed.has(a.id);
 return `<div class="subagent-group"><div class="child"><div class="child-heading"><span class="branch" aria-hidden="true"></span>${idControl(a)}<button class="child-main" data-collapse="${a.id}" aria-expanded="${!closed}" aria-label="${closed?'Expand':'Collapse'} agent ${a.id}"><span class="role">${esc(a.role)}</span><span class="child-title">${esc(a.title)}</span></button>${subagentChips(a)}<span class="header-status">${badge(a)}${descendantAttention(a)}</span><button class="icon-button disclosure-toggle" data-collapse="${a.id}" aria-expanded="${!closed}" aria-label="${closed?'Expand':'Collapse'} agent ${a.id}"><span class="collapse ${closed?'closed':''}">${icon('arrow',12)}</span></button>${a.status==='waiting'?`<button class="allow" data-action="approve" data-id="${a.id}">Allow</button><button class="text-button deny" data-action="reject" data-id="${a.id}">Deny</button>`:''}${removeControl(a)}</div>${closed?'':activity(a)+children(a)}</div></div>`;
}
function children(a) {
 const visible=(a.children||[]).filter(agent=>!agent.archivedAt);
 return visible.length?`<div class="children">${visible.map(child).join('')}</div>`:'';
}
function parentCard(a) {
 const closed=collapsed.has(a.id);
 return `<article class="agent-card"><div class="agent-header">${idControl(a)}<button class="agent-row" data-collapse="${a.id}" aria-expanded="${!closed}" aria-label="${closed?'Expand':'Collapse'} agent ${a.id}"><span class="role">${esc(a.role)}</span><span class="agent-title" title="${esc(a.title)}">${esc(a.title)}</span></button>${subagentChips(a)}<span class="header-status">${badge(a)}${descendantAttention(a)}</span><button class="icon-button disclosure-toggle" data-collapse="${a.id}" aria-expanded="${!closed}" aria-label="${closed?'Expand':'Collapse'} agent ${a.id}"><span class="collapse ${closed?'closed':''}">${icon('arrow',12)}</span></button>${removeControl(a)}</div>${closed?'':`<div class="agent-body">${activity(a)}${children(a)}${a.status==='waiting'?`<div class="agent-controls"><button class="allow" data-action="approve" data-id="${a.id}">Allow</button><button class="text-button deny" data-action="reject" data-id="${a.id}">Deny</button></div>`:''}</div>`}</article>`;
}
function card(a) { return `<div class="agent-group">${parentCard(a)}</div>`; }
// Prototype display choices; provider API identifiers will be configured by the backend.
const modelProfiles = {
 anthropic: { model: 'Claude', modelId: 'opus-5.5', provider: 'anthropic' },
 openai: { model: 'GPT', modelId: '6.1-sol', provider: 'openai' },
 haiku: { model: 'Claude', modelId: 'haiku-5.5', provider: 'anthropic' },
 sonnet: { model: 'Claude', modelId: 'sonnet-5.5', provider: 'anthropic' },
 deepseek: { model: 'DeepSeek', modelId: 'chat', provider: 'deepseek' },
 local: { model: 'Local', modelId: 'small-quantized', provider: 'local' }
};
function draftCard() {
 return `<article class="agent-card draft-card"><div class="draft-heading"><span class="agent-id">#new</span><span>New agent</span><select id="new-agent-profile" name="profile" aria-label="Model and model ID">${Object.entries(modelProfiles).map(([id,profile])=>`<option value="${id}" ${agentDraft.profile===id?'selected':''}>${esc(profile.model)}-${esc(profile.modelId)}</option>`).join('')}</select></div><form id="create-agent-form"><div class="draft-input-row">${richInput('new-agent-prompt',agentDraft.prompt,'Agent prompt','What should this agent do?')}</div>${editorExtras('new-agent-prompt')}</form></article>`;
}
function handleDraftKey(event) {
 if(event.isComposing || sending)return;
 if(event.key==='Escape'){
  event.preventDefault();agentDraft=null;render();$('#new-agent').focus({preventScroll:true});
 }else if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();createFromDraft();}
}
async function createFromDraft() {
 if(sending)return;
 const prompt=agentDraft?.prompt.trim() || (agentDraft?.files.length ? 'Review attached files' : '');
 if(!prompt){$('#new-agent-prompt').focus();return;}
 const current=agentDraft;
 setSending(true);
 try {
  const files=current.files.length ? await storeFiles(current.files) : [];
  agentDraft=null;
  newAgent(prompt,modelProfiles[current.profile].provider,current.modelId.trim(),files);
 } catch(error){toast(`Attachment save failed: ${error.message}`);}
 finally {setSending(false);}
}

function render() {
 const transcripts=[...document.querySelectorAll('[data-transcript]')].map(node=>({id:node.dataset.transcript,scroll:node.scrollTop,open:[...node.querySelectorAll('.tool-entry')].map((tool,index)=>tool.open?index:-1).filter(index=>index>=0)}));
 const scrollTop = $('.agents-section')?.scrollTop || 0;
 const w = workspace();
 const agents = w.agents.filter(a => !a.archivedAt && activeAgents([a]).some(x=>`${x.title} ${x.id}`.toLowerCase().includes(search.toLowerCase())));
 $('#app').innerHTML = `<main><section class="command-center" aria-label="Command center"><header class="ribbon"><a class="brand" href="/" aria-label="Xhaos home">x<span>h</span></a><h1>xhaos</h1><div class="ribbon-actions"><button class="text-button" id="collapse-all">${activeAgents(w.agents).every(a=>collapsed.has(a.id))?'Expand':'Collapse'} all</button><div class="layout-switch"><button class="icon-button ${layout==='grid'?'chosen':''}" data-layout="grid" aria-label="Two columns">${icon('grid',14)}</button><button class="icon-button ${layout==='list'?'chosen':''}" data-layout="list" aria-label="One column">${icon('list',14)}</button></div><button class="text-button channels-trigger" id="channels">${icon('chat',14)} Channels</button><button class="icon-button" id="integrations" aria-label="Connections and MCP" title="Connections and MCP">${icon('plug',15)}</button><button class="icon-button" id="settings" aria-label="Settings" title="Settings">${icon('settings',15)}</button><button class="primary" id="new-agent">${icon('plus',14)} New agent</button></div></header>
 <div class="composer-area"><form class="composer" id="command-form"><div id="composer-targets" class="composer-targets">${recipientChips()}</div>${richInput('command',draft,'Agent command','Message selected recipients, start a task, or / for commands')}${editorExtras('command')}<div class="composer-bottom"><div class="composer-actions"><button type="button" class="icon-button" id="dictate" aria-label="Dictate command">${icon('mic',16)}</button><button class="send" aria-label="Send command">${icon('send',15)}</button></div></div><div id="command-menu" class="command-menu" aria-label="Slash commands" hidden></div></form></div></section>
 <section class="agents-section" aria-label="Agents"><section id="utility-panel" class="agent-card utility-panel" hidden></section>${search?`<div class="search-context"><span>Search: ${esc(search)}</span><button class="text-button" id="clear-search" aria-label="Clear search">${icon('close',12)}</button></div>`:''}<div class="agent-grid ${layout}">${agents.length || agentDraft ? (layout!=='list' ? Array.from({length:layout==='three'?3:2},(_,index)=>index).map(column=>`<div class="agent-column">${column===0&&agentDraft?draftCard():''}${agents.filter((_,index)=>index%(layout==='three'?3:2)===column).map(card).join('')}</div>`).join('') : `<div class="agent-column">${agentDraft?draftCard():''}${agents.map(card).join('')}</div>`) : '<div class="empty">No matching agents.</div>'}</div></section></main>`;
 bind();
 for(const state of transcripts){
  const node=document.querySelector(`[data-transcript="${state.id}"]`);
  if(node){node.querySelectorAll('.tool-entry').forEach((tool,index)=>tool.open=state.open.includes(index));node.scrollTop=state.scroll;}
 }
 $('.agents-section').scrollTop = scrollTop;
}
function toggleAgent(button) {
 const id = Number(button.dataset.collapse);
 const agent = flatten(workspace().agents).find(agent => agent.id === id);
 const scroller = $('.agents-section');
 const scrollTop = scroller.scrollTop;
 const isChild = Boolean(button.closest('.child'));
 const container = button.closest(isChild ? '.child' : '.agent-card');
 collapsed.has(id) ? collapsed.delete(id) : collapsed.add(id);
 // Replace only this disclosure, keeping sibling transcripts and tool output intact.
 if(isChild){const template=document.createElement('template');template.innerHTML=child(agent);container.replaceWith(template.content.querySelector('.child'));}
 else container.outerHTML=parentCard(agent);
 bind();
 document.querySelector(`[data-collapse="${id}"]`)?.focus({ preventScroll: true });
 scroller.scrollTop = scrollTop;
 $('#collapse-all').textContent = activeAgents(workspace().agents).every(agent => collapsed.has(agent.id)) ? 'Expand all' : 'Collapse all';
}
function setSending(value) {
 sending=value;
 for(const id of ['command-form','create-agent-form']) {
  const form=document.getElementById(id);
  if(form){form.inert=value;form.setAttribute('aria-busy',String(value));}
 }
}
function toast(message) { $('#toast').textContent=message; $('#toast').classList.add('visible'); clearTimeout(toast.timer); toast.timer=setTimeout(()=>$('#toast').classList.remove('visible'),4500); }
function newAgent(prompt, provider = 'anthropic', modelId = '', attachments = []) {
 const id=Math.max(0,...workspaces.flatMap(w=>flatten(w.agents)).map(a=>a.id))+1;
 const title=prompt.split('\n')[0].slice(0,120);
 workspace().agents.unshift({id,title,prompt,status:'paused',role:'New task',provider,agentType:'xhaos',modelId,model:modelId || ({anthropic:'Claude',openai:'GPT',deepseek:'DeepSeek',local:'Local model'}[provider]),time:'0m',progress:0,message:'Task queued locally. Connect a backend to execute.',activity:[{kind:'user',text:prompt,attachments},{kind:'system',text:'Task queued locally. No backend is connected.'}],children:[]});
 collapsed.add(id);search='';draft='';save();render();toast(`Agent #${id} created in the prototype.`);
}
function focusNewAgent() {
 agentDraft ||= {prompt:'',profile:'anthropic',modelId:modelProfiles.anthropic.modelId,files:[]};
 closePanel();render();$('.agents-section').scrollTop=0;
 $('#new-agent-prompt').focus({preventScroll:true});
}
function closePanel() { $('#utility-panel').hidden=true; }
function showPanel(title, content) {
 const panel=$('#utility-panel');
 panel.innerHTML=`<div class="panel-heading"><h2>${esc(title)}</h2><button class="icon-button" id="close-panel" aria-label="Close ${esc(title)}">${icon('close',14)}</button></div><div class="panel-content">${content}</div>`;
 panel.hidden=false;$('.agents-section').scrollTop=0;
 $('#close-panel').onclick=()=>{closePanel();$('#command').focus();};
 $('#close-panel').focus({preventScroll:true});
}
function showMcp() {
 showPanel('MCP connections','<p>Server-owned connections: filesystem, repositories, and custom MCP servers. No servers are connected in this prototype.</p>');
}
function terminateAgent(id) {
 markTerminated(workspace().agents,id);
 selectedAgents.delete(id);
 for(const target of [...selectedAgents])if(!activeAgents(workspace().agents).some(agent=>agent.id===target))selectedAgents.delete(target);
 save();render();toast(`Agent #${id} terminated. View retained history with /history.`);
}
function showHistory() {
 const archived=flatten(workspace().agents).filter(agent=>agent.archivedAt);
 showPanel('Terminated agents',archived.length?archived.map(agent=>`<div class="history-row"><span class="agent-id">#${agent.id}</span><span>${esc(agent.title)}</span><button class="text-button" data-restore="${agent.id}">Restore</button></div>`).join(''):'<p>No terminated agents. Use an agent’s ⋯ menu → Terminate agent to remove it from view.</p>');
 document.querySelectorAll('[data-restore]').forEach(button=>button.onclick=()=>{restoreAgent(workspace().agents,Number(button.dataset.restore));save();render();showHistory();});
}
function showStatus() {
 showPanel('Status indicators',`<div class="status-examples">${Object.entries(labels).filter(([status])=>!['paused','terminated'].includes(status)).map(([status,label])=>`<span>${badge({status})}${label}</span>`).join('')}</div><p>The yellow ring expands around a solid attention dot. An extra yellow dot on a parent means a subagent needs attention.</p>`);
}
function showChannels(channelId) {
 const w=workspace(), channels=w.channels||[];
 const current=channels.find(channel=>channel.id===channelId)||channels[0];
 const identity=id=>{const agent=flatten(w.agents).find(agent=>agent.id===Number(id));return agent?`#${agent.id} ${esc(agent.title)}`:id==='user'?'User (legacy preview message)':esc(id);};
 showPanel('Agent channels',channels.length?`<div class="channel-tabs">${channels.map(channel=>`<button class="text-button ${channel.id===current.id?'chosen':''}" data-channel="${esc(channel.id)}"># ${esc(channel.subject)}</button>`).join('')}</div><div class="channel-member-chips">${current.participants.map(id=>`<span>${identity(id)}</span>`).join('')}</div><div class="channel-log">${current.messages.map(message=>`<article><div class="channel-meta">${identity(message.writer)} → ${message.readers.map(identity).join(', ')} <time>${esc(message.timestamp)}</time></div><div class="markdown-body">${markdown(message.text)}</div>${attachedFiles(message.attachments)}</article>`).join('')||'<p class="muted">No agent messages in this channel yet.</p>'}</div><p class="channel-local-note">Read-only channel log. Agents create subjects and exchange messages through the backend; this prototype does not deliver them.</p>`:'<div class="channels-empty"><p>No agent channels yet.</p><p>Agents will create channels as they need to coordinate. Their participants and conversations will appear here automatically.</p><p class="muted">Agent execution and channel delivery are not connected in this prototype.</p></div>');
 document.querySelectorAll('[data-channel]').forEach(button=>button.onclick=()=>showChannels(button.dataset.channel));
 bindAttachments();
}
function recipientChips() {
 const agents=activeAgents(workspace().agents);
 return [...selectedAgents].map(id=>{const agent=agents.find(agent=>agent.id===id);return agent?`<span class="recipient-chip">${badge(agent)}<span title="${esc(agent.title)}">#${id} ${esc(agent.title)}</span><button type="button" data-detach-agent="${id}" aria-label="Detach agent ${id}">×</button></span>`:'';}).join('');
}
function bindRecipientChips() {
 document.querySelectorAll('[data-detach-agent]').forEach(button=>button.onclick=()=>{selectedAgents.delete(Number(button.dataset.detachAgent));updateRecipients();});
}
function updateRecipients() {
 $('#composer-targets').innerHTML=recipientChips();bindRecipientChips();
 document.querySelectorAll('[data-menu-target]').forEach(button=>button.textContent=selectedAgents.has(Number(button.dataset.menuTarget))?'Detach from composer':'Attach to composer');
 document.querySelectorAll('[data-target]').forEach(button=>{const id=Number(button.dataset.target),selected=selectedAgents.has(id);button.setAttribute('aria-pressed',String(selected));button.setAttribute('aria-label',`${selected?'Detach':'Attach'} agent ${id} ${selected?'from':'to'} composer`);});
}
function toggleRecipient(id) {
 selectedAgents.has(id)?selectedAgents.delete(id):selectedAgents.add(id);
 updateRecipients();$('#command').focus({preventScroll:true});
}
function refreshTranscript(agent) {
 const current=document.querySelector(`[data-transcript="${agent.id}"]`);
 if(!current)return;
 const scroll=current.scrollTop;
 const open=[...current.querySelectorAll('.tool-entry')].map((tool,index)=>tool.open?index:-1);
 const template=document.createElement('template');template.innerHTML=activity(agent);
 const next=template.content.firstElementChild;
 next.querySelectorAll('.tool-entry').forEach((tool,index)=>tool.open=open.includes(index));
 current.replaceWith(next);next.scrollTop=scroll;
}
function clearComposer() {draft='';$('#command').value='';$('#command-menu').hidden=true;}
function sendToRecipients(text, attachments) {
 const agents=activeAgents(workspace().agents).filter(agent=>selectedAgents.has(agent.id));
 if(!agents.length){toast('Choose an active recipient.');return;}
 agents.forEach(agent=>(agent.activity ||= [{kind:'assistant',text:agent.message}]).push({kind:'user',text,attachments}));
 const panelScroll=$('.agents-section').scrollTop;
 save();agents.forEach(refreshTranscript);clearComposer();bind();$('.agents-section').scrollTop=panelScroll;toast('Message saved to selected recipients. No backend is connected.');
}

function notifyAttention() {
 const ids = newlyWaitingAgents(active, workspace().agents);
 if (!ids.length || search || !globalThis.requestAnimationFrame) return;
 const path = agentPath(workspace().agents, ids[0]);
 requestAnimationFrame(() => {
  // Find the request itself or its closest visible ancestor, without opening trees.
  const target = path.reverse().map(id => document.querySelector(`[data-target="${id}"]`)).find(Boolean);
  if (!target || !target.isConnected) return;
  const scroller = $('.agents-section');
  const viewport = scroller.getBoundingClientRect();
  const rect = target.getBoundingClientRect();
  if (rect.top >= viewport.top + 8 && rect.bottom <= viewport.bottom - 16) return;
  const top = scroller.scrollTop + rect.top - viewport.top - (scroller.clientHeight - rect.height) / 2;
  scroller.scrollTo({ top: Math.max(0, top), behavior: globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
 });
}
function bind() {
 notifyAttention();
 stopWatchingColumns();
 stopWatchingColumns = watchStickyColumns($('.agents-section'), [...document.querySelectorAll('.agent-grid:not(.list) > .agent-column')]);
 document.querySelectorAll('[data-layout]').forEach(b=>b.onclick=()=>{layout=b.dataset.layout;render();});
 document.querySelectorAll('[data-collapse]').forEach(button=>button.onclick=()=>toggleAgent(button));
 document.querySelectorAll('[data-target]').forEach(button=>button.onclick=()=>toggleRecipient(Number(button.dataset.target)));
 bindRecipientChips();
 document.querySelectorAll('[data-reveal]').forEach(button=>button.onclick=()=>{
  const path=agentPath(workspace().agents,Number(button.dataset.reveal));
  const id=Number(button.dataset.reveal);
  path.slice(0,-1).forEach(parentId=>collapsed.delete(parentId));
  collapsed.has(id)?collapsed.delete(id):collapsed.add(id);
  render();
  document.querySelector(`[data-collapse="${button.dataset.reveal}"]`)?.focus({preventScroll:true});
 });
 document.querySelectorAll('[data-menu-target]').forEach(button=>button.onclick=()=>{toggleRecipient(Number(button.dataset.menuTarget));button.closest('details').open=false;});

 document.querySelectorAll('[data-terminate]').forEach(button=>button.onclick=()=>terminateAgent(Number(button.dataset.terminate)));
 document.querySelectorAll('[data-action]').forEach(b=>b.onclick=()=>{transition(flatten(workspace().agents).find(a=>a.id===+b.dataset.id),b.dataset.action);save();render();});
 $('#collapse-all').onclick=()=>{const close=!activeAgents(workspace().agents).every(a=>collapsed.has(a.id));activeAgents(workspace().agents).forEach(a=>close?collapsed.add(a.id):collapsed.delete(a.id));render();};
 $('#new-agent').onclick=focusNewAgent;
 $('#channels').onclick=()=>showChannels();
 if(agentDraft){
  $('#new-agent-prompt').oninput=e=>{agentDraft.prompt=e.target.value;};
  $('#new-agent-profile').onchange=e=>{agentDraft.profile=e.target.value;agentDraft.modelId=modelProfiles[e.target.value].modelId;};
  $('#new-agent-profile').onkeydown=e=>{if(e.key==='Escape')handleDraftKey(e);};
  $('#create-agent-form').onsubmit=e=>{e.preventDefault();createFromDraft();};
 }
 if($('#clear-search')) $('#clear-search').onclick=()=>{search='';render();};
 $('#integrations').onclick=showMcp;
 $('#settings').onclick=()=>showPanel('Xhaos settings','<p>Xhaos is a browser-first prototype for a desktop and remote agent harness.</p><p>This UI stores mock tasks locally. Rust execution, remote connections, MCP, and automatic approval classification are planned.</p><p class="muted">No Rust toolchain, dependencies, or build cache required.</p>');
 $('#command-form').onsubmit=e=>{e.preventDefault();submitCommand($('#command').value);};
 $('#dictate').onclick=dictate;
 $('#command').oninput=e=>{draft=e.target.value;updateCommandMenu();};
 $('#command').onkeydown=e=>{if(e.key==='Escape'){ $('#command-menu').hidden=true;return;}if(e.key==='ArrowDown'&&!$('#command-menu').hidden){e.preventDefault();$('#command-menu button')?.focus();}if(e.key==='Enter'&&!e.shiftKey){e.preventDefault();submitCommand(e.target.value);}};
 const commandKeys=$('#command').onkeydown;
 bindEditor('command',{getFiles:()=>pendingFiles,setText:value=>{draft=value;},onText:updateCommandMenu,onKeyDown:commandKeys,notify:toast});
 if(agentDraft)bindEditor('new-agent-prompt',{getFiles:()=>agentDraft.files,setText:value=>{agentDraft.prompt=value;},notify:toast,onKeyDown:handleDraftKey});
 bindAttachments();
}
function bindAttachments() {
 document.querySelectorAll('[data-download]').forEach(button=>button.onclick=async()=>{
  try{const file=await loadAttachment(button.dataset.download);const link=document.createElement('a');link.href=file.url;link.download=file.name;link.click();}catch(error){toast(error.message);}
 });
 document.querySelectorAll('[data-image]').forEach(button=>button.onclick=async()=>{
  const slot=button.parentElement.querySelector('[data-image-slot]');
  if(!slot.hidden){slot.hidden=true;button.setAttribute('aria-expanded','false');return;}
  try{const file=await loadAttachment(button.dataset.image);const img=document.createElement('img');img.src=file.url;img.alt=file.name;slot.replaceChildren(img);slot.hidden=false;button.setAttribute('aria-expanded','true');}catch(error){toast(error.message);}
 });


}

const slashCommands = [
 ['new', 'New agent', '/new Describe the task'],
 ['mcp', 'MCP connections', '/mcp'],
 ['layout', 'Choose columns: 1, 2, or 3', '/layout 3'],
 ['terminate', 'Terminate an agent and its subagents', '/terminate [agent ID]'],
 ['history', 'Terminated agents and retained history', '/history'],
 ['restore', 'Restore a removed agent', '/restore 11'],
 ['channels', 'Agent communication channels', '/channels'],
 ['search', 'Find agents; no query clears search', '/search title or ID'],
 ['allow', 'Allow a pending request', '/allow [agent ID]'],
 ['deny', 'Deny a pending request', '/deny [agent ID]'],
 ['pause', 'Pause an agent', '/pause 11'],
 ['resume', 'Resume an agent', '/resume 11'],
 ['help', 'Show command reference', '/help']
];
function updateCommandMenu() {
 const menu=$('#command-menu'), value=$('#command').value;
 if(!value.startsWith('/') || /\s/.test(value)){menu.hidden=true;return;}
 const matches=slashCommands.filter(([name])=>name.startsWith(value.slice(1).toLowerCase()));
 menu.innerHTML=matches.map(([name,description])=>`<button type="button" data-command="${name}"><code>/${name}</code><span>${description}</span></button>`).join('');
 menu.hidden=!matches.length;
 menu.querySelectorAll('button').forEach((button,i)=>{
  button.onclick=()=>{draft=`/${button.dataset.command} `;$('#command').value=draft;menu.hidden=true;$('#command').focus();};
  button.onkeydown=e=>{if(e.key==='Escape'){menu.hidden=true;$('#command').focus();}if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();const buttons=[...menu.querySelectorAll('button')];buttons[(i+(e.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length].focus();}};
 });
}
function runSlashCommand(value, attachments = []) {
 const [,name,args='']=value.match(/^\/(\w+)(?:\s+([\s\S]*))?$/)||[];
 if(name==='help'){
  if(args.trim().toLowerCase()==='status'){showStatus();return;}
  showPanel('Commands',slashCommands.map(([,description,usage])=>`<div class="command-reference"><code>${esc(usage)}</code><p>${description}</p></div>`).join('')+'<div class="command-reference"><button class="text-button" id="help-status">/help status — status indicators</button></div>');
  $('#help-status').onclick=showStatus;return;
 }
 if(name==='mcp'){showMcp();return;}
 if(name==='layout'){const count=Number(args.trim());if(![1,2,3].includes(count)){showPanel('Layout','<p>Use /layout 1, /layout 2, or /layout 3.</p>');return;}layout=count===1?'list':count===3?'three':'grid';draft='';render();return;}
 if(name==='history'){showHistory();return;}
 if(name==='terminate'){
  const explicit=args.trim();
  if(explicit&&!/^#?\d+$/.test(explicit)){toast('Use /terminate, or /terminate followed by one agent ID.');return;}
  const candidates=activeAgents(workspace().agents).filter(agent=>explicit?agent.id===Number(explicit.replace('#','')):selectedAgents.has(agent.id));
  if(candidates.length!==1){toast('Attach one active agent, or specify its ID.');return;}
  terminateAgent(candidates[0].id);return;
 }
 if(name==='remove'||name==='restore'){if(!/^#?\d+$/.test(args.trim())){toast(`Use /${name} followed by an agent ID.`);return;}const id=Number(args.trim().replace('#',''));if(!flatten(workspace().agents).some(agent=>agent.id===id)){toast('Agent not found.');return;}if(name==='remove')terminateAgent(id);else{restoreAgent(workspace().agents,id);save();render();}return;}
 if(name==='channels'){showChannels();return;}
 if(name==='new'){if(args.trim())newAgent(args.trim(),'anthropic','',attachments);else focusNewAgent();return;}
 if(name==='search'){search=args.trim();draft='';closePanel();render();return;}
 if(name==='tell'){if(!/^#?\d+\s+\S/.test(args)){toast('Use /tell 11 your message');return;}dispatchCommand('tell agent '+args.replace(/^#/,''),attachments);return;}
 if(name==='allow'||name==='deny'){
  const explicit=args.trim();
  if(explicit&&!/^#?\d+$/.test(explicit)){toast(`Use /${name}, or /${name} followed by one agent ID.`);return;}
  const candidates=activeAgents(workspace().agents).filter(agent=>explicit?agent.id===Number(explicit.replace('#','')):selectedAgents.has(agent.id));
  if(candidates.length!==1){toast('Attach one agent to the composer, or specify its ID.');return;}
  const agent=candidates[0];
  if(agent.status!=='waiting'){toast('That agent has no pending request.');return;}
  transition(agent,name==='allow'?'approve':'reject');draft='';save();render();return;
 }
 if(['pause','resume','approve','decline'].includes(name)){
  if(!/^#?\d+$/.test(args.trim())){toast(`Use /${name} followed by an agent ID`);return;}
  const agent=flatten(workspace().agents).find(a=>a.id===Number(args.trim().replace('#','')));
  if(!agent){toast('Agent not found in this workspace.');return;}
  const allowed={pause:'running',resume:'paused',approve:'waiting',decline:'waiting'};
  if(agent.status!==allowed[name]){toast(`Cannot /${name} this agent in its current state.`);return;}
  transition(agent,({resume:'pause',decline:'reject'})[name]||name);
  draft='';save();render();return;
 }
 toast('Unknown command. Type / to see commands.');
}
async function submitCommand(value) {
 if(sending)return;
 value=value.trim();
 if(!pendingFiles.length){dispatchCommand(value);return;}
 if(value==='/new'){
  focusNewAgent();agentDraft.files.push(...pendingFiles.splice(0));render();$('#new-agent-prompt').focus();return;
 }
 if(value.startsWith('/')&&!/^\/(new|tell)\s+\S/.test(value)){toast('Use attachments with a new task or /tell agent message.');return;}
 const tell=value.match(/^(?:\/tell|tell\s+(?:agent\s+)?)\s*#?(\d+)\s+([\s\S]+)$/i);
 if((value.startsWith('/tell')||(!selectedAgents.size&&/^tell\s/i.test(value)))&&(!tell||!flatten(workspace().agents).some(a=>a.id===Number(tell[1])))){toast('Specify a valid agent and message before sending attachments.');return;}
 setSending(true);
 const files=[...pendingFiles];
 try {
  const attachments=await storeFiles(files);
  pendingFiles.splice(0,files.length);
  dispatchCommand(value || 'Review attached files',attachments);
 } catch(error){toast(`Attachment save failed: ${error.message}`);}
 finally{setSending(false);}
}
function dispatchCommand(value, attachments = []) { value=value.trim();if(!value)return;if(value.startsWith('/')){runSlashCommand(value,attachments);return;}if(selectedAgents.size){sendToRecipients(value,attachments);return;}const match=value.match(/^tell\s+(?:agent\s+)?#?(\d+)\s*[:,]?\s+([\s\S]+)$/i);if(match){const a=flatten(workspace().agents).find(a=>a.id===+match[1]);if(!a){toast('That agent is not in this workspace.');return;} (a.activity ||= [{kind:'assistant',text:a.message}]).push({kind:'user',text:match[2],attachments});save();refreshTranscript(a);clearComposer();bind();toast(`Note saved for agent #${a.id}.`);}else newAgent(value.replace(/^(?:start|create) (?:a )?new agent\s*(?:with (?:a )?task)?\s*:?\s*/i,'' ) || value,'anthropic','',attachments); }
function dictate() { const Speech=window.SpeechRecognition||window.webkitSpeechRecognition;if(!Speech){toast('Dictation is unavailable in this browser. You can type a command.');return;}showPanel('Voice dictation','<p>Your browser’s speech service may send audio to its provider. The transcript will appear in the command bar for you to review.</p><button class="primary" id="start-dictation">Start microphone</button>');$('#start-dictation').onclick=()=>{const recognition=new Speech();recognition.lang='en-US';recognition.interimResults=false;recognition.onresult=e=>{draft=e.results[0][0].transcript;$('#command').value=draft;$('#command').focus();toast('Transcript ready. Review it and press send.');};recognition.onerror=e=>toast(`Dictation: ${e.error}`);recognition.onend=()=>$('#dictate')?.classList.remove('recording');closePanel();try{recognition.start();$('#dictate').classList.add('recording');toast('Listening…');}catch{toast('Could not start microphone.');}}; }
window.addEventListener('click',event=>{if(!event.target.closest('.agent-menu'))document.querySelectorAll('.agent-menu[open]').forEach(menu=>menu.open=false);});
window.addEventListener('beforeunload',()=>blobUrls.forEach(url=>URL.revokeObjectURL(url)));
window.addEventListener('keydown',e=>{if(e.key==='Escape'){const menus=[...document.querySelectorAll('.agent-menu[open]')];if(menus.length){menus.forEach(menu=>menu.open=false);menus[0].querySelector('summary').focus({preventScroll:true});return;}}if(e.key.toLowerCase()==='n'&&(e.ctrlKey||e.metaKey)&&e.altKey){e.preventDefault();focusNewAgent();}if((e.ctrlKey||e.metaKey)&&e.key==='k'){e.preventDefault();$('#command').focus();}});
installTypingFlow(document,window);
render();
