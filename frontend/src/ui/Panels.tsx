// The side panel's contents. Every panel uses Shell, so they share the
// heading and scrolling.
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode, type KeyboardEvent as ReactKeyboardEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { markdown } from '../markdown.ts';
import { flatten } from '../model.ts';
import { colors, fonts } from './theme/tokens.stylex.ts';
import { Button, StatusDot } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { labels } from './AgentCard.tsx';
import { AttachedFiles } from './Transcript.tsx';
import { QuestionInbox } from './QuestionInbox.tsx';
import { authPost } from './SignIn.tsx';
import { UsagePanel, usageLine } from './Usage.tsx';
import { AppearanceSettings, DeveloperSettings } from './AppearanceSettings.tsx';
import { DictationSettings } from './DictationSettings.tsx';
import { Secrets } from './Secrets.tsx';
import { Skills } from './Skills.tsx';
import { WebSettings } from './WebSettings.tsx';
import { ClassifierSettings } from './ClassifierSettings.tsx';
import { formatTime } from './appearance.ts';
import { Files } from './Files.tsx';
import { BoardList } from './Boards.tsx';
import { Nodes, AddNodeButton } from './Nodes.tsx';
import type { Channel as ChannelView } from '../types.ts';
import { McpServers } from './Mcp.tsx';
import { useApp, workspaceOf, update, toast } from './state/store.ts';
import { visibleCommands, developerMode, closeBtw } from './state/composer.ts';
import { newBoard } from './state/boards.ts';
import { restore } from './state/agents.ts';
import { loadChannel } from './state/backend.ts';
import {
  openPanel, showTools, closeChannel, publishChannel, unpublishChannel, saveMemory, deleteMemory, saveProvider, testProvider, removeProvider, type ProviderForm,
} from './state/panels.ts';

const html = (value: string) => ({ __html: value });

function Shell({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return <>
    <div data-panel-heading {...stylex.props(shell.heading)}>
      <h2 {...stylex.props(shell.title)}>{title}</h2>
      <div {...stylex.props(shell.actions)}>{action}</div>
    </div>
    <div data-panel-content {...stylex.props(shell.content)}>{children}</div>
  </>;
}

// Body text in panels.
function P({ children, muted = false }: { children: ReactNode; muted?: boolean }) {
  return <p {...stylex.props(shell.p, muted && shell.muted)}>{children}</p>;
}

function Memories({ editId }: { editId?: string }) {
  const memories = useApp(s => s.memories);
  const active = useApp(s => s.active);
  const visible = memories.filter(memory => memory.workspaceId === active);
  const editing = memories.find(memory => memory.id === editId);
  const open = Boolean(editing) || editId === 'new';
  const input = useRef<HTMLTextAreaElement>(null);
  const fit = () => {
    const el = input.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 180)}px`;
    el.style.overflowY = el.scrollHeight > 180 ? 'auto' : 'hidden';
  };
  useLayoutEffect(() => { if (open) { input.current?.focus({ preventScroll: true }); fit(); } }, [open, editId]);
  const submit = (event: { preventDefault(): void }) => {
    event.preventDefault();
    const content = input.current!.value.trim();
    if (!content) { input.current!.focus(); return; }
    saveMemory(editId === 'new' ? null : editId || null, content);
  };
  const add = !open && <Button id="add-memory" onClick={() => openPanel('memories', { editId: 'new' })}>＋ Add memory</Button>;
  // The form sits where its result goes: a new memory at the top (newest
  // first), an edited one in its own place, so a long list never has you
  // scroll between the box and what it changes.
  const form = (
    <form id="memory-form" data-memory-form={editId} onSubmit={submit} {...stylex.props(memory.form)}
      onKeyDown={event => {
        if (event.key === 'Escape') { event.preventDefault(); openPanel('memories'); requestAnimationFrame(() => document.getElementById('add-memory')?.focus({ preventScroll: true })); }
        else if (event.target === input.current && event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.requestSubmit(); }
      }}>
      <textarea key={editId || 'none'} id="memory-text" ref={input} rows={1} aria-label="Workspace memory" placeholder="A preference, project convention, or decision" defaultValue={editing?.content || ''} onInput={fit} {...stylex.props(memory.text)} />
      <div {...stylex.props(memory.formActions)}>
        <span {...stylex.props(memory.scope)}>{editId === 'new' ? 'New · workspace' : 'Editing'}</span>
        <Button id="cancel-memory" onClick={() => openPanel('memories')} sx={memory.pushRight}>Cancel</Button>
        <Button type="submit" id="save-memory">Save</Button>
      </div>
    </form>
  );
  const backend = useApp(s => s.backendVersion !== null);
  return (
    <Shell title="Workspace memories" action={add}>
      <p {...stylex.props(memory.note)}>Every agent in this workspace sees these in its prompt (first line as an index; full text on request). Agents can add them too, with your approval under manual review.</p>
      {editId === 'new' && form}
      <div {...stylex.props(memory.list)}>
        {visible.length ? visible.map(item => item.id === editId ? <div key={item.id}>{form}</div> : (
          <article key={item.id} data-memory={item.id} {...stylex.props(memory.row)}>
            <div {...stylex.props(memory.content)}><div className="markdown-body" dangerouslySetInnerHTML={html(markdown(item.content))} />{/^agent /.test(item.source || '') && <small data-memory-source {...stylex.props(shell.muted)}>from {item.source}</small>}</div>
            <div {...stylex.props(memory.actions)}>
              <Button data-edit-memory={item.id} onClick={() => openPanel('memories', { editId: item.id })} sx={memory.small}>Edit</Button>
              <Button data-delete-memory={item.id} onClick={() => deleteMemory(item.id)} sx={memory.small}>Delete</Button>
            </div>
          </article>
        )) : editId !== 'new' && <p {...stylex.props(memory.empty)}>No saved memories for this workspace.</p>}
      </div>
      {!backend && <p {...stylex.props(memory.note, memory.noteAfter)}>Prototype mode: saved in this browser only.</p>}
    </Shell>
  );
}

// Channels: an accordion, one channel open at a time (the panel is narrow
// and channels keep receiving messages). A closed row shows the subject,
// members, status and the latest message; a dot marks messages you have not
// seen open. The most recently active channel comes first, closed ones last.
const SEEN = 'xhaos-channels-seen';
const readSeen = (): Record<string, number> => { try { return JSON.parse(localStorage.getItem(SEEN) || '{}'); } catch { return {}; } };
const lastAt = (c: ChannelView) => c.messages.at(-1)?.timestamp || '';
const plain = (text: string) => text.replace(/[*_`#>|]/g, '').replace(/\s+/g, ' ').trim();

function Channels({ channelId }: { channelId?: string }) {
  const w = useApp(s => workspaceOf(s));
  const channels = [...(w.channels || [])].sort((a, b) => Number(Boolean(a.closed)) - Number(Boolean(b.closed)) || lastAt(b).localeCompare(lastAt(a)) || Number(b.id) - Number(a.id));
  const openId = channelId ?? (channels.length === 1 ? channels[0].id : '');
  const [seen, setSeen] = useState(readSeen);
  const all = flatten(w.agents);
  const title = (id: number | string) => all.find(agent => agent.id === Number(id))?.title || '';
  const name = (id: number | string) => (typeof id === 'string' && id.startsWith('outside:') ? `${id.slice(8)} (outside)` : `#${id}`);
  const open = channels.find(c => c.id === openId);
  const openCount = open?.count ?? open?.messages.length ?? 0;
  useEffect(() => {
    if (!open || seen[open.id] === openCount) return;
    const next = { ...readSeen(), [open.id]: openCount };
    try { localStorage.setItem(SEEN, JSON.stringify(next)); } catch { /* private mode */ }
    setSeen(next);
  }, [open?.id, openCount]);
  if (!channels.length) {
    return (
      <Shell title="Agent channels">
        <P>No agent channels yet.</P>
        <P muted>Agents open channels to coordinate (across projects too), and every spawned subagent gets one with its parent. They appear here live.</P>
      </Shell>
    );
  }
  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    const rows = [...event.currentTarget.querySelectorAll<HTMLElement>('[data-channel]')];
    const at = rows.indexOf(document.activeElement as HTMLElement);
    if (at < 0) return;
    event.preventDefault();
    rows[Math.max(0, Math.min(rows.length - 1, at + (event.key === 'ArrowDown' ? 1 : -1)))].focus();
  };
  return (
    <Shell title="Agent channels">
      <div data-channel-list onKeyDown={onKeyDown} {...stylex.props(channel.list)}>
        {channels.map(c => {
          const isOpen = c.id === openId, last = c.messages.at(-1);
          const unread = !isOpen && (c.count ?? c.messages.length) > (seen[c.id] ?? 0);
          return (
            <section key={c.id} data-channel-row={c.id} data-open={isOpen || undefined} {...stylex.props(channel.row, isOpen && channel.rowOpen)}>
              <button type="button" data-channel={c.id} aria-expanded={isOpen} aria-controls={`channel-body-${c.id}`}
                onClick={() => openPanel('channels', { channelId: isOpen ? '' : c.id })} {...stylex.props(channel.summary)}>
                <span {...stylex.props(channel.line)}>
                  <span aria-hidden="true" {...stylex.props(channel.chevron, isOpen && channel.chevronOpen)}>›</span>
                  <span data-channel-id {...stylex.props(channel.num)}>#{c.id}</span>
                  <span {...stylex.props(channel.subject)}>{c.subject}</span>
                  {unread && <span data-unread title="New messages" {...stylex.props(channel.dot)} />}
                  {c.closed ? <span {...stylex.props(channel.tag)}>closed</span> : c.published && <span {...stylex.props(channel.tag, channel.tagOn)}>published</span>}
                  <span {...stylex.props(channel.when)}>{last ? formatTime(last.timestamp) : ''}</span>
                </span>
                <span {...stylex.props(channel.sub)}>{c.participants.length === 1 ? 'agent ' : 'agents '}{c.participants.map(id => `#${id}`).join(' · ')}{last && <> — <span {...stylex.props(channel.preview)}>{name(last.writer)}: {plain(last.text).slice(0, 140)}</span></>}</span>
              </button>
              {isOpen && <ChannelBody c={c} title={title} name={name} />}
            </section>
          );
        })}
      </div>
    </Shell>
  );
}

function ChannelBody({ c, title, name }: { c: ChannelView; title(id: number | string): string; name(id: number | string): string }) {
  const [issued, setIssued] = useState<{ passphrase: string; path: string } | null>(null);
  const log = useRef<HTMLDivElement>(null);
  // Start at the newest message, and follow new ones while at the bottom.
  const pinned = useRef(true);
  useLayoutEffect(() => { const el = log.current; if (el && pinned.current) el.scrollTop = el.scrollHeight; }, [c.messages.length]);
  // The list has only each channel's last message: load the rest on opening.
  useEffect(() => { if (c.loaded === false) void loadChannel(c.id); }, [c.id, c.loaded]);
  const issue = async () => { const r = await publishChannel(c.id); if (r) setIssued(r); };
  return (
    <div id={`channel-body-${c.id}`} data-channel-head={c.id} {...stylex.props(channel.body)}>
      <div {...stylex.props(channel.bar)}>
        <div {...stylex.props(channel.members)}>
          {c.participants.map((id, i) => {
            const full = title(id), short = full.length > 22 ? `${full.slice(0, 21)}…` : full;
            return <span key={id}>{i > 0 && ' · '}<a href="#" data-agent-link={id} title={full}>#{id}</a> <span {...stylex.props(channel.memberTitle)}>{short}</span></span>;
          })}
        </div>
        <div data-channel-actions {...stylex.props(channel.actions)}>
          {!c.closed && <>
            {c.published
              ? <><Button data-reset-link={c.id} title="Issue a new passphrase; the old one stops working" onClick={issue}>Reset</Button>
                  <Button data-unpublish={c.id} title="Take the channel off its link" onClick={() => { setIssued(null); void unpublishChannel(c.id); }}>Unpublish</Button></>
              : <Button data-publish={c.id} title="Let programs outside Xhaos (a script, another Claude Code session) read this channel and message its agents, with a passphrase shown once" onClick={issue}>Publish</Button>}
            <Button data-close-channel={c.id} title="Tell the members and refuse further messages" onClick={() => closeChannel(c.id)}>Close</Button>
          </>}
        </div>
      </div>
      {issued && <ChannelSecret id={c.id} passphrase={issued.passphrase} path={issued.path} onDone={() => setIssued(null)} />}
      <div ref={log} data-channel-log onScroll={e => { const el = e.currentTarget; pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24; }} {...stylex.props(channel.log)}>
        {c.loaded === false ? <P muted>Loading messages…</P> : c.messages.length ? c.messages.map((message, index) => (
          <article key={index} {...stylex.props(channel.message)}>
            <div {...stylex.props(channel.meta)}>
              <span title={title(message.writer)}>{name(message.writer)}</span>
              <time dateTime={message.timestamp} title={message.timestamp} {...stylex.props(channel.time)}>{formatTime(message.timestamp)}</time>
            </div>
            <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(message.text, { agentLinks: true }))} />
            <AttachedFiles files={message.attachments} />
          </article>
        )) : <P muted>No messages yet.</P>}
      </div>
    </div>
  );
}

// ChannelSecret shows a just-issued passphrase, once, with how to use it.
function ChannelSecret({ id, passphrase, path, onDone }: { id: string; passphrase: string; path: string; onDone(): void }) {
  const url = `${location.origin}${path}`;
  const example = `curl -s -X POST ${url} \\\n  -H 'Authorization: Bearer ${passphrase}' \\\n  -H 'Content-Type: application/json' \\\n  -d '{"from":"my script","body":"hello agents"}'\n\n# read it, waiting up to 30s for new messages:\ncurl -s '${url}?after=0&wait=30' -H 'Authorization: Bearer ${passphrase}'`;
  return (
    <div data-channel-link={id} {...stylex.props(channel.secretBox)}>
      <P>Copy the passphrase now; it is not shown again.</P>
      <div {...stylex.props(channel.secretRow)}>
        <code data-passphrase {...stylex.props(channel.secret)}>{passphrase}</code>
        <Button data-copy-passphrase onClick={() => {
          // navigator.clipboard is missing on insecure origins (a LAN address).
          if (!navigator.clipboard) { toast('This page cannot copy here (plain HTTP); select the passphrase and copy it.'); return; }
          navigator.clipboard.writeText(passphrase).then(() => toast('Passphrase copied.'), () => toast('Copy failed; select the passphrase instead.'));
        }}>Copy</Button>
        <Button data-secret-done onClick={onDone}>Done</Button>
      </div>
      <pre data-link-example {...stylex.props(channel.example)}>{example}</pre>
    </div>
  );
}

// Presets fill the form; every field stays editable. OpenAI-compatible
// endpoints (OpenAI, DeepSeek, local servers) share one adapter kind.
const presets = {
  anthropic: { id: 'anthropic', kind: 'anthropic', name: 'Anthropic', baseUrl: '', key: 'sk-ant-…' },
  openai: { id: 'openai', kind: 'openai', name: 'OpenAI', baseUrl: 'https://api.openai.com/v1', key: 'sk-…' },
  deepseek: { id: 'deepseek', kind: 'openai', name: 'DeepSeek', baseUrl: 'https://api.deepseek.com/v1', key: 'sk-…' },
  local: { id: 'local', kind: 'openai', name: 'Local (Ollama)', baseUrl: 'http://localhost:11434/v1', key: 'none needed' },
  sim: { id: 'sim', kind: 'sim', name: 'Simulated', baseUrl: '', key: 'none needed' },
  other: { id: '', kind: 'openai', name: '', baseUrl: '', key: 'if the endpoint needs one' },
} as const;
type PresetName = keyof typeof presets;
const blank = (preset: PresetName): ProviderForm => ({ id: presets[preset].id, kind: presets[preset].kind, name: presets[preset].name, baseUrl: presets[preset].baseUrl, apiKey: '', models: '', context: '' });

function Providers({ message, testing }: { message?: string; testing?: string }) {
  const backend = useApp(s => s.backendVersion !== null);
  const providers = useApp(s => s.providers);
  // The form starts empty: a preset fills only its own defaults, and Edit
  // is the one way to load a saved provider into it.
  const [preset, setPreset] = useState<PresetName | ''>('');
  const [form, setForm] = useState<ProviderForm>(() => blank('other'));
  const [editingId, setEditingId] = useState('');
  if (!backend) return <P>Providers are configured on the Go backend. Run scripts/server.sh and open its page.</P>;
  const editing = providers.find(p => p.id === editingId);
  const clash = !editing && providers.some(p => p.id === form.id.trim());
  const set = (field: keyof ProviderForm) => (event: { target: { value: string } }) => setForm({ ...form, [field]: event.target.value });
  const choose = (name: PresetName | '') => { setPreset(name); setEditingId(''); setForm(blank(name || 'other')); };
  const edit = (id: string) => {
    const p = providers.find(x => x.id === id);
    if (!p) return;
    setPreset(p.kind === 'anthropic' ? 'anthropic' : p.kind === 'sim' ? 'sim' : 'other');
    setEditingId(p.id);
    setForm({ id: p.id, kind: p.kind, name: p.name, baseUrl: p.baseUrl || '', apiKey: '', models: p.models.join(', '), context: p.contextTokens ? String(p.contextTokens) : '' });
  };
  const save = async () => { if (preset && await saveProvider(form)) choose(''); };
  const onEnter = (event: React.KeyboardEvent) => { if (event.key === 'Enter' && !event.nativeEvent.isComposing) { event.preventDefault(); save(); } };
  const openai = form.kind === 'openai', simulated = form.kind === 'sim';
  return (
    <>
      <P muted>Providers give agents their models. Each new agent uses the model picked next to the composer.</P>
      <div>
        {providers.length ? providers.map(p => (
          <div key={p.id} data-provider={p.id} {...stylex.props(provider.row)}>
            <div>
              <strong>{p.name}</strong> <span {...stylex.props(shell.muted)}>{p.kind === 'openai' ? 'OpenAI-compatible' : p.kind === 'sim' ? 'simulated, no tokens' : p.kind}{p.baseUrl ? ` · ${p.baseUrl}` : ''}{p.hasKey ? ` · key ${p.keyHint}` : ' · no key'}{p.contextTokens ? ` · ${p.contextTokens.toLocaleString()} tokens` : ''}</span>
              <div {...stylex.props(shell.muted, provider.models)}>{p.models.join(', ')}</div>
            </div>
            <div {...stylex.props(provider.actions)}>
              <Button data-edit-provider={p.id} onClick={() => edit(p.id)}>Edit</Button>
              <Button data-test-provider={p.id} onClick={() => testProvider(p.id)}>{testing === p.id ? 'Testing…' : 'Test'}</Button>
              <Button data-remove-provider={p.id} onClick={() => removeProvider(p.id)}>Remove</Button>
            </div>
          </div>
        )) : <P muted>No providers yet.</P>}
      </div>
      {/* Not a <form> with a password field: browsers would offer to store the
          key in their password manager (and Chromium's prompt steals focus). */}
      <div id="provider-form" onKeyDown={onEnter} {...stylex.props(provider.form)}>
        <div {...stylex.props(provider.formHead)}>
          <span {...stylex.props(provider.label)}>{editing ? `Edit ${editing.name}` : 'Add a provider'}</span>
          <select id="provider-preset" aria-label="Provider preset" value={preset} onChange={event => choose(event.target.value as PresetName | '')} {...stylex.props(provider.input)}>
            <option value="">Choose a provider…</option>
            <option value="anthropic">Anthropic</option>
            <option value="openai">OpenAI</option>
            <option value="deepseek">DeepSeek</option>
            <option value="local">Local (Ollama)</option>
            <option value="other">Other OpenAI-compatible</option>
            {/* A testing aid: offered in Developer mode (an existing Simulated provider stays usable). */}
            {(developerMode() || preset === 'sim') && <option value="sim">Simulated (no model, no tokens)</option>}
          </select>
        </div>
        {preset && <>
        <div {...stylex.props(provider.grid)}>
          <label htmlFor="provider-id" {...stylex.props(provider.fieldLabel)}>ID</label>
          <input id="provider-id" value={form.id} onChange={set('id')} placeholder="e.g. groq" autoComplete="off" spellCheck="false" {...stylex.props(provider.input)} />
          <label htmlFor="provider-name" {...stylex.props(provider.fieldLabel)}>Name</label>
          <input id="provider-name" value={form.name} onChange={set('name')} placeholder="Shown in the model picker" autoComplete="off" {...stylex.props(provider.input)} />
          {openai && <>
            <label htmlFor="provider-url" {...stylex.props(provider.fieldLabel)}>Base URL</label>
            <input id="provider-url" value={form.baseUrl} onChange={set('baseUrl')} placeholder="https://…/v1 (the part before /chat/completions)" autoComplete="off" spellCheck="false" {...stylex.props(provider.input)} />
          </>}
          {!simulated && <>
          <label htmlFor="provider-key" {...stylex.props(provider.fieldLabel)}>API key</label>
          <input id="provider-key" type="text" name="xhaos-provider-key" value={form.apiKey} onChange={set('apiKey')} autoComplete="off" autoCapitalize="off" spellCheck="false" data-1p-ignore="" data-lpignore="true" data-bwignore=""
            placeholder={editing?.hasKey ? 'Leave empty to keep the saved key' : presets[preset].key} {...stylex.props(provider.input, provider.key)} />
          </>}
          <label htmlFor="provider-models" {...stylex.props(provider.fieldLabel)}>Models</label>
          <input id="provider-models" value={form.models} onChange={set('models')} autoComplete="off" spellCheck="false"
            placeholder={openai ? 'Comma-separated; empty asks the endpoint for its list' : 'Comma-separated; empty uses the defaults'} {...stylex.props(provider.input)} />
          <label htmlFor="provider-context" {...stylex.props(provider.fieldLabel)}>Context</label>
          <input id="provider-context" value={form.context} onChange={set('context')} inputMode="numeric" autoComplete="off"
            placeholder={`Tokens before older messages are compacted; empty: ${openai ? '128,000' : '200,000'}`} {...stylex.props(provider.input)} />
        </div>
        {simulated && <P muted>A scripted stand-in for a model: agents list the project, read a file, and then act on words in the task. "split" or "spawn" starts two subagents, "contact #N" opens a channel with agent #N, "ask" asks you a question, "write" writes sim-notes.md, and "run" runs git status. Every approval and channel works as with a real model. No provider is called and nothing is billed. scripted-fast skips the typing delay.</P>}
        {clash && <p {...stylex.props(provider.message)}>A provider with ID “{form.id.trim()}” exists: saving updates it (an empty key or model list keeps its own).</p>}
        <div {...stylex.props(provider.formActions)}>
          <Button id="provider-cancel" onClick={() => choose('')}>Cancel</Button>
          <Button id="provider-save" onClick={save}>{editing || clash ? 'Save changes' : 'Add provider'}</Button>
        </div>
        </>}
        <P muted>Keys are sent to the Xhaos server, encrypted at rest, and never shown again. Anyone who can open this page can use them. Local servers (Ollama, llama.cpp, vLLM) need no key: the Xhaos server talks to them, so the URL is as seen from the server.</P>
      </div>
      {message && <p id="provider-message" {...stylex.props(provider.message)}>{message}</p>}
    </>
  );
}

function History() {
  const archived = flatten(useApp(s => workspaceOf(s).agents)).filter(agent => agent.archivedAt);
  return (
    <Shell title="Terminated agents">
      {archived.length ? archived.map(agent => (
        <div key={agent.id} {...stylex.props(list.row)}>
          <span {...stylex.props(list.id)}>#{agent.id}</span><span>{agent.title}</span>
          <Button data-restore={agent.id} onClick={() => restore(agent.id)} sx={list.pushRight}>Restore</Button>
        </div>
      )) : <P>No terminated agents. Use an agent’s ⋯ menu → Terminate agent to remove it from view.</P>}
    </Shell>
  );
}

function Status() {
  const shown = (Object.entries(labels) as [keyof typeof labels, string][]).filter(([status]) => !['idle', 'paused', 'terminated'].includes(status));
  return (
    <Shell title="Status indicators">
      <div {...stylex.props(list.examples)}>{shown.map(([status, label]) => <span key={status} {...stylex.props(list.example)}><StatusDot status={status} label={label} size={9} />{label}</span>)}</div>
      <P>The yellow ring expands around a solid attention dot. A parent shows only its own status; a waiting subagent shows its dot on its own header and ID chip.</P>
    </Shell>
  );
}

function Help() {
  return (
    <Shell title="Commands">
      {visibleCommands().map(([name, description, usage]) => (
        <div key={name} {...stylex.props(list.command)}><code>{usage}</code><p {...stylex.props(list.commandText)}>{description}</p></div>
      ))}
      <div {...stylex.props(list.command)}><Button id="help-status" onClick={() => openPanel('status')}>/help status — status indicators</Button></div>
    </Shell>
  );
}

// Settings holds the configuration you rarely reach for, as sections that
// expand in place (no sub-pages). /providers and /tools open their section.
type SectionId = 'models' | 'usage' | 'tools' | 'skills' | 'secrets' | 'dictation' | 'appearance' | 'security' | 'developer';
function Section({ id, title, summary, open, onToggle, children }: { id: string; title: string; summary: string; open: boolean; onToggle(): void; children: ReactNode }) {
  return (
    <section data-settings-section={id} {...stylex.props(settings.section)}>
      <button type="button" id={id === 'models' ? 'open-providers' : id === 'tools' ? 'integrations' : `settings-${id}`} aria-expanded={open} onClick={onToggle} {...stylex.props(settings.head)}>
        <span {...stylex.props(settings.chevron, open && settings.chevronOpen)}><Icon name="arrow" size={12} /></span>
        <span {...stylex.props(settings.title)}>{title}</span>
        <span {...stylex.props(settings.summary)}>{summary}</span>
      </button>
      {open && <div {...stylex.props(settings.body)}>{children}</div>}
    </section>
  );
}

function Settings({ focus, message, testing, request }: { focus?: SectionId; message?: string; testing?: string; request?: object }) {
  const version = useApp(s => s.backendVersion);
  const providers = useApp(s => s.providers.length);
  const servers = useApp(s => s.mcpServers.length);
  const catalogCount = useApp(s => (s.tools && 'tools' in s.tools ? s.tools.tools.length : 0));
  const usageSummary = useApp(s => usageLine(s.usage));
  const [open, setOpen] = useState<Set<SectionId>>(() => new Set(focus ? [focus] : []));
  // Opening a section from elsewhere (/providers, /tools, a provider action)
  // expands it and brings it into view.
  useEffect(() => {
    if (!focus) return;
    setOpen(o => (o.has(focus) ? o : new Set([...o, focus])));
    requestAnimationFrame(() => document.querySelector(`[data-settings-section="${focus}"]`)?.scrollIntoView({ block: 'nearest' }));
  }, [focus, request]); // each request to show a section expands it, even the one already in the link
  // The tool list loads whenever Tools is open, however it was opened (a
  // link or a reload included), not only when its header is clicked.
  const toolsOpen = open.has('tools');
  const catalogLoaded = useApp(s => s.tools !== null);
  useEffect(() => { if (toolsOpen && !catalogLoaded && version) void showTools(false); }, [toolsOpen, catalogLoaded, version]);
  const toggle = (id: SectionId) => {
    const next = new Set(open);
    if (next.has(id)) next.delete(id); else { next.add(id); if (id === 'tools') showTools(false); }
    setOpen(next);
    // The section opened last goes in the link (#panel=settings&section=…).
    if (next.has(id)) update(s => { if (s.panel.kind === 'settings') s.panel.section = id; });
  };
  return (
    <Shell title="Settings">
      {version ? <>
        <Section id="models" title="Models" summary={providers ? `${providers} provider${providers > 1 ? 's' : ''}` : 'none set up'} open={open.has('models')} onToggle={() => toggle('models')}>
          <Providers message={message} testing={testing} />
        </Section>
        <Section id="usage" title="Usage and cost" summary={usageSummary} open={open.has('usage')} onToggle={() => toggle('usage')}>
          <UsagePanel />
        </Section>
        <Section id="tools" title="Tools and MCP" summary={`${catalogCount ? `${catalogCount} tools` : 'tools'}${servers ? ` · ${servers} MCP server${servers > 1 ? 's' : ''}` : ''}`} open={open.has('tools')} onToggle={() => toggle('tools')}>
          <Tools />
        </Section>
        <Section id="skills" title="Skills" summary=".skill files and project skills" open={open.has('skills')} onToggle={() => toggle('skills')}>
          <Skills />
        </Section>
        <Section id="secrets" title="Secrets" summary="tokens for agents' commands" open={open.has('secrets')} onToggle={() => toggle('secrets')}>
          <Secrets />
        </Section>
        <Section id="dictation" title="Dictation" summary="hints for the speech model" open={open.has('dictation')} onToggle={() => toggle('dictation')}>
          <DictationSettings />
        </Section>
        <Section id="appearance" title="Appearance" summary="text size, theme, fonts" open={open.has('appearance')} onToggle={() => toggle('appearance')}>
          <AppearanceSettings />
        </Section>
        <Section id="security" title="Passphrase" summary="signs in and encrypts keys" open={open.has('security')} onToggle={() => toggle('security')}>
          <Security />
        </Section>
        <Section id="developer" title="Developer" summary={developerMode() ? 'on' : 'off'} open={open.has('developer')} onToggle={() => toggle('developer')}>
          <DeveloperSettings />
        </Section>
      </> : null}
      <P muted>{version ? `Connected to the Go backend ${version}. Agents and messages are stored on the server.` : 'Prototype mode: tasks are stored in this browser. Run scripts/server.sh to use the Go backend.'}</P>
    </Shell>
  );
}

// Security: change the passphrase (the same data key is rewrapped, so saved
// keys stay readable) or sign this browser out.
function Security() {
  const [old, setOld] = useState(''), [next, setNext] = useState(''), [note, setNote] = useState('');
  const change = async () => {
    const failed = await authPost('change', { passphrase: old, next });
    setNote(failed ? `Not changed: ${failed}.` : 'Passphrase changed. Other signed-in browsers stay signed in.');
    if (!failed) { setOld(''); setNext(''); }
  };
  return <>
    <P muted>This workspace's passphrase signs browsers in to it and wraps the key that encrypts its API keys, MCP secrets and secrets. After a server restart, its agents wait until someone signs in to it.</P>
    <div {...stylex.props(provider.grid)}>
      <label htmlFor="passphrase-old" {...stylex.props(provider.fieldLabel)}>Current</label>
      <input id="passphrase-old" type="password" autoComplete="current-password" value={old} onChange={e => setOld(e.target.value)} {...stylex.props(provider.input)} />
      <label htmlFor="passphrase-new" {...stylex.props(provider.fieldLabel)}>New</label>
      <input id="passphrase-new" type="password" autoComplete="new-password" value={next} onChange={e => setNext(e.target.value)} {...stylex.props(provider.input)} />
    </div>
    <div {...stylex.props(provider.formActions)}>
      <Button id="lock-workspace" title="Forget this workspace's key and sign every browser out of it: its agents pause until someone signs in again" onClick={async () => {
        if (!confirm('Lock this workspace? Its agents stop until someone signs in to it again, and every browser is signed out of it.')) return;
        await authPost('lock', {}); location.href = '/';
      }}>Lock workspace</Button>
      <Button id="sign-out" title="Sign this browser out (the logo does the same)" onClick={async () => { await authPost('signout', {}); location.href = '/'; }}>Sign out</Button>
      <Button id="passphrase-change" onClick={change}>Change passphrase</Button>
    </div>
    {note && <p id="passphrase-note" {...stylex.props(provider.message)}>{note}</p>}
  </>;
}

// Tool groups in the Tools section, by catalog source.
const toolGroups: [string, string, string][] = [
  ['built-in', 'Files and commands', "Part of Xhaos itself, no MCP server needed. Every agent can call them inside its project's folder (see Files); each call passes the agent's policy first."],
  ['channels', 'Agents and channels', 'Opening a channel to agents outside its own family always waits for you, showing who they are and what they work on; within the family, and on channels already open, talking runs at once. Starting a subagent is gated like a write. Top-level agents reach each other across projects; subagents only their parent and siblings.'],
  ['memory', 'Memories', "Every agent sees the workspace's memories in its prompt; saving one is gated like a write."],
  ['boards', 'Boards', 'Agents read your boards (the Draw tab) freely. Creating a board or adding to one is gated like a write, and the approval shows what will be added. What an agent adds is grouped, tagged "added by #N", and placed beside your drawing, never over it.'],
];
const effectNotes: Record<string, string> = {
  ask: 'waits for your answers in Questions',
  read: 'runs at once',
  write: 'asks under manual review; runs under auto',
  exec: 'read-only commands run at once; git add, commit and switch run under auto; anything else always asks',
  net: 'reaches the web: asks under manual review; runs under auto, read-only agents too',
};
function Tools() {
  const catalog = useApp(s => s.tools);
  const backend = useApp(s => s.backendVersion !== null);
  return (
    <>
      {!backend ? <P>Tools run on the Go backend. Run scripts/server.sh and open its page.</P>
        : !catalog ? <P muted>Loading…</P>
        : catalog.error !== undefined ? <P>Could not load tools: {catalog.error}</P>
        : <>
          <h3 {...stylex.props(tools.subheading, tools.first)}>Web</h3>
          <WebSettings />
          <h3 {...stylex.props(tools.subheading)}>Commands</h3>
          <ClassifierSettings />
          {catalog.sandbox && (
            <div data-sandbox={catalog.sandbox.active ? 'on' : 'off'} {...stylex.props(tools.sandbox, !catalog.sandbox.active && tools.sandboxOff)}>
              <strong>{catalog.sandbox.active ? `Commands are sandboxed (Landlock v${catalog.sandbox.abi})` : 'Commands are not sandboxed'}</strong>
              <span>{catalog.sandbox.note}{catalog.sandbox.active ? '' : ' Start the server with -sandbox auto on a kernel with Landlock (Linux 5.13+).'}</span>
            </div>
          )}
          {toolGroups.map(([source, title, note], i) => {
            const list = catalog.tools.filter(t => t.source === source);
            return list.length ? <div key={source} data-tool-group={source}>
              <h3 {...stylex.props(tools.subheading, i === 0 && tools.first)}>{title}</h3>
              <P>{note}</P>
              <div {...stylex.props(tools.list)}>
                {list.map(t => (
                  <div key={t.name} data-tool={t.name} {...stylex.props(tools.row)}>
                    <code {...stylex.props(tools.name)}>{t.name}</code>
                    <span data-effect={t.effect} {...stylex.props(tools.effect, t.effect === 'read' ? tools.read : tools.gated)}>{t.effect} · {effectNotes[t.effect] || ''}</span>
                    <p {...stylex.props(tools.description)}>{t.description}</p>
                  </div>
                ))}
              </div>
            </div> : null;
          })}
          <h3 {...stylex.props(tools.subheading)}>MCP servers</h3>
          <McpServers />
        </>}
    </>
  );
}

const simple = (): Partial<Record<string, [string, ReactNode]>> => ({
  layout: ['Layout', <P>Use /layout 1, /layout 2, or /layout 3.</P>],
});

// The latest /btw answer: a side question about the agents, answered from a
// read-only digest. It is not part of any agent; closing it returns to the
// panel shown before.
function BtwPanel() {
  const btw = useApp(s => s.btw);
  if (!btw) return <Shell title="Side answer"><P muted>No side question. Ask one with /btw, e.g. /btw what is #12 doing?</P></Shell>;
  return (
    <Shell title="Side answer" action={<Button variant="icon" id="close-btw" aria-label="Close the side answer" title="Close (Esc in the composer)" onClick={closeBtw}><Icon name="close" size={12} /></Button>}>
      <div id="btw" aria-live="polite" aria-label="Side answer">
        <p data-btw-question {...stylex.props(btwStyle.question)}><span {...stylex.props(btwStyle.kind)}>/btw</span> {btw.question}</p>
        {btw.pending ? <P muted>Reading the agents…</P>
          : btw.error ? <p {...stylex.props(btwStyle.error)}>Could not answer: {btw.error}</p>
          : <div className="markdown-body" data-answer dangerouslySetInnerHTML={html(markdown(btw.answer || ''))} />}
      </div>
    </Shell>
  );
}

const btwStyle = stylex.create({
  question: { marginTop: 2, marginBottom: 10, marginInline: 0, paddingBottom: 8, borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: colors.lineFaint, color: colors.textMuted, fontSize: 12 },
  kind: { fontFamily: fonts.mono, fontSize: 10, color: colors.textFaint },
  error: { color: colors.danger, margin: 0 },
});

export function Panel() {
  const panel = useApp(s => s.panel);
  let body: ReactNode = null;
  if (panel.kind === 'questions') body = <Shell title="Questions"><div id="question-inbox" {...stylex.props(shell.questions)}><QuestionInbox /></div></Shell>;
  else if (panel.kind === 'files') body = <Shell title="Files"><Files /></Shell>;
  else if (panel.kind === 'memories') body = <Memories editId={panel.editId} />;
  else if (panel.kind === 'channels') body = <Channels channelId={panel.channelId} />;
  else if (panel.kind === 'boards') body = <Shell title="Boards" action={<Button id="new-board" onClick={() => void newBoard()}>New board</Button>}><BoardList /></Shell>;
  else if (panel.kind === 'nodes') body = <Shell title="Nodes" action={<AddNodeButton />}><Nodes adding={panel.editId === 'new'} /></Shell>;
  else if (panel.kind === 'providers') body = <Settings focus="models" message={panel.message} testing={panel.testing} />;
  else if (panel.kind === 'history') body = <History />;
  else if (panel.kind === 'status') body = <Status />;
  else if (panel.kind === 'help') body = <Help />;
  else if (panel.kind === 'btw') body = <BtwPanel />;
  else if (panel.kind === 'settings') body = <Settings focus={panel.section as SectionId | undefined} request={panel} />;
  else if (panel.kind === 'tools') body = <Settings focus="tools" />;
  else { const entry = simple()[panel.kind]; if (entry) body = <Shell title={entry[0]}>{entry[1]}</Shell>; }
  return <section id="utility-panel" aria-label="Panel" {...stylex.props(shell.root)}>{body}</section>;
}

const settings = stylex.create({
  section: { borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: colors.lineFaint },
  head: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    width: '100%',
    paddingBlock: 9,
    paddingInline: 2,
    borderWidth: 0,
    backgroundColor: { default: 'transparent', ':hover': colors.hover },
    color: 'inherit',
    fontFamily: 'inherit',
    textAlign: 'left',
    cursor: 'pointer',
  },
  chevron: { display: 'inline-flex', color: colors.textFaint },
  chevronOpen: { transform: 'rotate(90deg)' },
  title: { fontSize: 12, color: colors.text },
  summary: { marginLeft: 'auto', fontSize: 10, color: colors.textFaint },
  body: { paddingBottom: 12, paddingLeft: 20 },
});

const shell = stylex.create({
  root: { flexGrow: 1, minHeight: 0, display: 'flex', flexDirection: 'column', margin: 0 },
  heading: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', paddingBlock: 8, paddingInline: 10, backgroundColor: colors.panelHead },
  title: { fontSize: 12, fontWeight: 500, margin: 0 },
  actions: { display: 'flex', alignItems: 'center', gap: 10 },
  content: { flexGrow: 1, minHeight: 0, overflow: 'auto', paddingBlock: 8, paddingInline: 12, fontSize: 11, lineHeight: 1.65 },
  p: { color: colors.textMuted, marginTop: 6, marginBottom: 12, marginInline: 0 },
  muted: { color: colors.textFaint },
  questions: { height: '100%', display: 'flex', flexDirection: 'column' },
});

const memory = stylex.create({
  form: { marginBlock: 8 },
  note: { fontSize: 10, color: colors.textFaint, marginTop: 0, marginInline: 0, marginBottom: 8 },
  noteAfter: { marginTop: 8 },
  list: { display: 'flex', flexDirection: 'column', gap: 8 },
  row: { position: 'relative', paddingBlock: 10, paddingLeft: 10, paddingRight: 88, borderLeftWidth: 2, borderLeftStyle: 'solid', borderLeftColor: colors.line, borderRadius: 3, backgroundColor: colors.sunken, minHeight: 40 },
  content: { fontSize: 11, color: colors.text, lineHeight: 1.45 },
  actions: { position: 'absolute', right: 5, top: 5, display: 'flex', gap: 4 },
  small: { fontSize: 9 },
  empty: { paddingBlock: 8, color: colors.textFaint, fontSize: 10, margin: 0 },
  text: {
    display: 'block',
    width: 'min(100%, 720px)',
    minHeight: 22,
    maxHeight: 180,
    resize: 'none',
    overflowY: 'hidden',
    paddingBlock: 4,
    paddingInline: 0,
    backgroundColor: 'transparent',
    borderWidth: 0,
    borderBottomWidth: 1,
    borderBottomStyle: 'solid',
    borderBottomColor: colors.line,
    borderRadius: 0,
    color: colors.text,
    fontSize: 11,
    lineHeight: 1.5,
    marginTop: 3,
    marginBottom: 6,
  },
  formActions: { display: 'flex', alignItems: 'center', gap: 10 },
  scope: { fontSize: 10, color: colors.textGhost },
  pushRight: { marginLeft: 'auto' },
});

const channel = stylex.create({
  list: { display: 'flex', flexDirection: 'column', gap: 4 },
  row: { borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, borderRadius: 5, backgroundColor: colors.sunken },
  rowOpen: { borderColor: colors.line, backgroundColor: 'transparent' },
  summary: { display: 'flex', flexDirection: 'column', gap: 2, width: '100%', paddingBlock: 6, paddingInline: 8, borderWidth: 0, borderRadius: 5, backgroundColor: { default: 'transparent', ':hover': colors.hover }, textAlign: 'left', fontFamily: 'inherit', color: 'inherit', cursor: 'pointer', outlineOffset: -1 },
  line: { display: 'flex', alignItems: 'center', gap: 7, minWidth: 0 },
  chevron: { width: 8, flexShrink: 0, color: colors.textGhost, transitionProperty: 'transform', transitionDuration: '.12s' },
  chevronOpen: { transform: 'rotate(90deg)' },
  num: { flexShrink: 0, fontSize: 10, color: colors.textFaint, fontVariantNumeric: 'tabular-nums' },
  subject: { flexGrow: 1, minWidth: 0, overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis', fontSize: 11.5, fontWeight: 500, color: colors.textStrong },
  dot: { width: 6, height: 6, flexShrink: 0, borderRadius: '50%', backgroundColor: colors.brand },
  tag: { flexShrink: 0, fontSize: 9, paddingInline: 5, paddingBlock: 1, borderRadius: 3, color: colors.textFaint, backgroundColor: colors.card },
  tagOn: { color: colors.brand, backgroundColor: colors.brandSoft },
  when: { flexShrink: 0, fontSize: 9, color: colors.textGhost },
  sub: { paddingLeft: 15, fontSize: 10, color: colors.textFaint, overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis' },
  preview: { color: colors.textGhost },
  body: { paddingInline: 8, paddingBottom: 6 },
  bar: { display: 'flex', alignItems: 'center', gap: 10, paddingBlock: 4, borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: colors.lineFaint },
  members: { flexGrow: 1, minWidth: 0, fontSize: 10, color: colors.textFaint, overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis' },
  memberTitle: { color: colors.textGhost },
  actions: { display: 'flex', gap: 6, alignItems: 'center', flexShrink: 0 },
  log: { maxHeight: '55vh', overflow: 'auto', scrollbarWidth: 'thin', paddingTop: 2 },
  message: { marginTop: 10, marginBottom: 14 },
  meta: { display: 'flex', gap: 8, fontSize: 9, color: colors.textGhost, marginBottom: 4 },
  time: { marginLeft: 'auto' },
  secretBox: { marginBlock: 8, paddingBlock: 8, paddingInline: 10, borderRadius: 4, backgroundColor: colors.sunken, fontSize: 11 },
  secretRow: { display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', marginBlock: 6 },
  secret: { fontFamily: fonts.mono, fontSize: 11, backgroundColor: colors.card, paddingBlock: 4, paddingInline: 6, borderRadius: 3, overflowWrap: 'anywhere', userSelect: 'all' },
  example: { fontFamily: fonts.mono, fontSize: 10, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: colors.textMuted, margin: 0 },
});

const provider = stylex.create({
  row: { display: 'flex', justifyContent: 'space-between', gap: 12, paddingBlock: 8, borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: colors.lineFaint, fontSize: 11 },
  models: { fontSize: 10, marginTop: 3 },
  actions: { display: 'flex', gap: 10, alignItems: 'flex-start' },
  form: { marginTop: 12, fontSize: 11 },
  label: { color: colors.textMuted },
  formHead: { display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 },
  grid: { display: 'grid', gridTemplateColumns: '70px 1fr', alignItems: 'center', columnGap: 8, rowGap: 6 },
  fieldLabel: { color: colors.textFaint, fontSize: 10 },
  input: { minWidth: 0, backgroundColor: colors.sunken, borderWidth: 0, borderRadius: 3, color: colors.text, paddingBlock: 6, paddingInline: 8, fontSize: 11, fontFamily: 'inherit' },
  // Masked without type=password, so password managers leave it alone.
  key: { WebkitTextSecurity: 'disc' },
  formActions: { display: 'flex', justifyContent: 'flex-end', gap: 10, marginTop: 8 },
  message: { fontSize: 11, color: colors.textSoft, marginTop: 10 },
});

const list = stylex.create({
  row: { display: 'flex', alignItems: 'center', gap: 10, paddingBlock: 8 },
  id: { fontFamily: 'ui-monospace, monospace', color: colors.textMuted, fontSize: 10, flexShrink: 0 },
  pushRight: { marginLeft: 'auto' },
  examples: { display: 'flex', gap: 24, flexWrap: 'wrap', paddingBlock: 14 },
  example: { display: 'flex', alignItems: 'center', gap: 12 },
  command: { paddingBlock: 8, borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: colors.line },
  commandText: { marginBlock: 4, marginInline: 0, color: colors.textMuted },
});

const tools = stylex.create({
  sandbox: { display: 'flex', flexDirection: 'column', gap: 3, marginBottom: 12, paddingBlock: 7, paddingInline: 10, borderRadius: 4, fontSize: 10.5, lineHeight: 1.5, color: colors.textMuted, backgroundColor: colors.sunken, borderLeftWidth: 2, borderLeftStyle: 'solid', borderLeftColor: colors.brand },
  sandboxOff: { borderLeftColor: colors.warn },
  list: { display: 'flex', flexDirection: 'column', gap: 2 },
  row: { display: 'grid', gridTemplateColumns: '110px 1fr', columnGap: 12, paddingBlock: 6, borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: colors.raised },
  name: { color: colors.text },
  effect: { fontSize: 10 },
  read: { color: colors.effectRead },
  gated: { color: colors.effectWrite },
  description: { gridColumn: '2', marginTop: 2, marginBottom: 0, color: colors.textDim },
  subheading: { fontSize: 12, fontWeight: 600, marginTop: 14, marginBottom: 4, color: colors.text },
  first: { marginTop: 0 },
});
