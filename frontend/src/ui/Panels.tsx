// The side panel's contents. Every panel uses Shell, so they share the
// heading and scrolling.
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import * as stylex from '@stylexjs/stylex';
import { markdown } from '../markdown.ts';
import { flatten } from '../model.ts';
import { colors } from './theme/tokens.stylex.ts';
import { Button, Chip, StatusDot } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { labels } from './AgentCard.tsx';
import { AttachedFiles } from './Transcript.tsx';
import { QuestionInbox } from './QuestionInbox.tsx';
import { Files } from './Files.tsx';
import { McpServers } from './Mcp.tsx';
import { useApp, workspaceOf } from './state/store.ts';
import { slashCommands } from './state/composer.ts';
import { restore } from './state/agents.ts';
import {
  openPanel, showTools, saveMemory, deleteMemory, saveProvider, testProvider, removeProvider, startDictation, type ProviderForm,
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
  return (
    <Shell title="Workspace memories" action={add}>
      <p {...stylex.props(memory.note)}>Every agent in this workspace sees these in its prompt (first line as an index; full text on request). Agents can add them too, with your approval under manual review.</p>
      <div {...stylex.props(memory.list)}>
        {visible.length ? visible.map(item => (
          <article key={item.id} {...stylex.props(memory.row)}>
            <div {...stylex.props(memory.content)}><div className="markdown-body" dangerouslySetInnerHTML={html(markdown(item.content))} />{/^agent /.test(item.source || '') && <small data-memory-source {...stylex.props(shell.muted)}>from {item.source}</small>}</div>
            <div {...stylex.props(memory.actions)}>
              <Button data-edit-memory={item.id} onClick={() => openPanel('memories', { editId: item.id })} sx={memory.small}>Edit</Button>
              <Button data-delete-memory={item.id} onClick={() => deleteMemory(item.id)} sx={memory.small}>Delete</Button>
            </div>
          </article>
        )) : <p {...stylex.props(memory.empty)}>No saved memories for this workspace.</p>}
      </div>
      <form id="memory-form" hidden={!open} onSubmit={submit}
        onKeyDown={event => {
          if (event.key === 'Escape') { event.preventDefault(); openPanel('memories'); requestAnimationFrame(() => document.getElementById('add-memory')?.focus({ preventScroll: true })); }
          else if (event.target === input.current && event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.requestSubmit(); }
        }}>
        <textarea key={editId || 'none'} id="memory-text" ref={input} rows={1} aria-label="Workspace memory" placeholder="A preference, project convention, or decision" defaultValue={editing?.content || ''} onInput={fit} {...stylex.props(memory.text)} />
        <div {...stylex.props(memory.formActions)}>
          <span {...stylex.props(memory.scope)}>Workspace</span>
          <Button id="cancel-memory" onClick={() => openPanel('memories')} sx={memory.pushRight}>Cancel</Button>
          <Button type="submit" id="save-memory">Save</Button>
        </div>
      </form>
      <p {...stylex.props(memory.note, memory.noteAfter)}>Saved in this browser only. Backend storage and retrieval are planned.</p>
    </Shell>
  );
}

function Channels({ channelId }: { channelId?: string }) {
  const w = useApp(s => workspaceOf(s)), channels = w.channels || [];
  const current = channels.find(channel => channel.id === channelId) || channels[0];
  const identity = (id: number | string) => {
    const agent = flatten(w.agents).find(agent => agent.id === Number(id));
    return agent ? `#${agent.id} ${agent.title}` : id === 'user' ? 'User (legacy preview message)' : String(id);
  };
  if (!current) {
    return (
      <Shell title="Agent channels">
        <P>No agent channels yet.</P>
        <P>Agents open channels to coordinate (across projects too) and spawned subagents get one with their parent. Their members and messages appear here as they happen.</P>
      </Shell>
    );
  }
  return (
    <Shell title="Agent channels">
      <div {...stylex.props(channel.tabs)}>
        {channels.map(c => <Button key={c.id} data-channel={c.id} chosen={c.id === current.id} onClick={() => openPanel('channels', { channelId: c.id })} sx={channel.tab}># {c.subject}</Button>)}
      </div>
      <div {...stylex.props(channel.members)}>{current.participants.map(id => <Chip key={id}>{identity(id)}</Chip>)}</div>
      <div {...stylex.props(channel.log)}>
        {current.messages.length ? current.messages.map((message, index) => (
          <article key={index} {...stylex.props(channel.message)}>
            <div {...stylex.props(channel.meta)}>{identity(message.writer)} → {message.readers.map(identity).join(', ')} <time {...stylex.props(channel.time)}>{message.timestamp}</time></div>
            <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(message.text))} />
            <AttachedFiles files={message.attachments} />
          </article>
        )) : <P muted>No agent messages in this channel yet.</P>}
      </div>
      <p {...stylex.props(memory.note, memory.noteAfter)}>Read-only: agents write here. Top-level agents can talk to each other and to their own subagents; subagents only to their parent and siblings.</p>
    </Shell>
  );
}

// Presets fill the form; every field stays editable. OpenAI-compatible
// endpoints (OpenAI, DeepSeek, local servers) share one adapter kind.
const presets = {
  anthropic: { id: 'anthropic', kind: 'anthropic', name: 'Anthropic', baseUrl: '', key: 'sk-ant-…' },
  openai: { id: 'openai', kind: 'openai', name: 'OpenAI', baseUrl: 'https://api.openai.com/v1', key: 'sk-…' },
  deepseek: { id: 'deepseek', kind: 'openai', name: 'DeepSeek', baseUrl: 'https://api.deepseek.com/v1', key: 'sk-…' },
  local: { id: 'local', kind: 'openai', name: 'Local (Ollama)', baseUrl: 'http://localhost:11434/v1', key: 'none needed' },
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
    setPreset(p.kind === 'anthropic' ? 'anthropic' : 'other');
    setEditingId(p.id);
    setForm({ id: p.id, kind: p.kind, name: p.name, baseUrl: p.baseUrl || '', apiKey: '', models: p.models.join(', '), context: p.contextTokens ? String(p.contextTokens) : '' });
  };
  const save = async () => { if (preset && await saveProvider(form)) choose(''); };
  const onEnter = (event: React.KeyboardEvent) => { if (event.key === 'Enter' && !event.nativeEvent.isComposing) { event.preventDefault(); save(); } };
  const openai = form.kind === 'openai';
  return (
    <>
      <P muted>Providers give agents their models. Each new agent uses the model picked next to the composer.</P>
      <div>
        {providers.length ? providers.map(p => (
          <div key={p.id} data-provider={p.id} {...stylex.props(provider.row)}>
            <div>
              <strong>{p.name}</strong> <span {...stylex.props(shell.muted)}>{p.kind === 'openai' ? 'OpenAI-compatible' : p.kind}{p.baseUrl ? ` · ${p.baseUrl}` : ''}{p.hasKey ? ` · key ${p.keyHint}` : ' · no key'}{p.contextTokens ? ` · ${p.contextTokens.toLocaleString()} tokens` : ''}</span>
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
          <label htmlFor="provider-key" {...stylex.props(provider.fieldLabel)}>API key</label>
          <input id="provider-key" type="text" name="xhaos-provider-key" value={form.apiKey} onChange={set('apiKey')} autoComplete="off" autoCapitalize="off" spellCheck="false" data-1p-ignore="" data-lpignore="true" data-bwignore=""
            placeholder={editing?.hasKey ? 'Leave empty to keep the saved key' : presets[preset].key} {...stylex.props(provider.input, provider.key)} />
          <label htmlFor="provider-models" {...stylex.props(provider.fieldLabel)}>Models</label>
          <input id="provider-models" value={form.models} onChange={set('models')} autoComplete="off" spellCheck="false"
            placeholder={openai ? 'Comma-separated; empty asks the endpoint for its list' : 'Comma-separated; empty uses the defaults'} {...stylex.props(provider.input)} />
          <label htmlFor="provider-context" {...stylex.props(provider.fieldLabel)}>Context</label>
          <input id="provider-context" value={form.context} onChange={set('context')} inputMode="numeric" autoComplete="off"
            placeholder={`Tokens before older messages are compacted; empty: ${openai ? '128,000' : '200,000'}`} {...stylex.props(provider.input)} />
        </div>
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
      {slashCommands.map(([name, description, usage]) => (
        <div key={name} {...stylex.props(list.command)}><code>{usage}</code><p {...stylex.props(list.commandText)}>{description}</p></div>
      ))}
      <div {...stylex.props(list.command)}><Button id="help-status" onClick={() => openPanel('status')}>/help status — status indicators</Button></div>
    </Shell>
  );
}

// Settings holds the configuration you rarely reach for, as sections that
// expand in place (no sub-pages). /providers and /tools open their section.
type SectionId = 'models' | 'tools';
function Section({ id, title, summary, open, onToggle, children }: { id: string; title: string; summary: string; open: boolean; onToggle(): void; children: ReactNode }) {
  return (
    <section data-settings-section={id} {...stylex.props(settings.section)}>
      <button type="button" id={id === 'models' ? 'open-providers' : 'integrations'} aria-expanded={open} onClick={onToggle} {...stylex.props(settings.head)}>
        <span {...stylex.props(settings.chevron, open && settings.chevronOpen)}><Icon name="arrow" size={12} /></span>
        <span {...stylex.props(settings.title)}>{title}</span>
        <span {...stylex.props(settings.summary)}>{summary}</span>
      </button>
      {open && <div {...stylex.props(settings.body)}>{children}</div>}
    </section>
  );
}

function Settings({ focus, message, testing }: { focus?: SectionId; message?: string; testing?: string }) {
  const version = useApp(s => s.backendVersion);
  const providers = useApp(s => s.providers.length);
  const servers = useApp(s => s.mcpServers.length);
  const catalogCount = useApp(s => (s.tools && 'tools' in s.tools ? s.tools.tools.length : 0));
  const [open, setOpen] = useState<Set<SectionId>>(() => new Set(focus ? [focus] : []));
  // Opening a section from elsewhere (/providers, /tools, a provider action)
  // expands it and brings it into view.
  useEffect(() => {
    if (!focus) return;
    setOpen(o => (o.has(focus) ? o : new Set([...o, focus])));
    requestAnimationFrame(() => document.querySelector(`[data-settings-section="${focus}"]`)?.scrollIntoView({ block: 'nearest' }));
  }, [focus]);
  const toggle = (id: SectionId) => {
    const next = new Set(open);
    if (next.has(id)) next.delete(id); else { next.add(id); if (id === 'tools') showTools(false); }
    setOpen(next);
  };
  return (
    <Shell title="Settings">
      {version ? <>
        <Section id="models" title="Models" summary={providers ? `${providers} provider${providers > 1 ? 's' : ''}` : 'none set up'} open={open.has('models')} onToggle={() => toggle('models')}>
          <Providers message={message} testing={testing} />
        </Section>
        <Section id="tools" title="Tools and MCP" summary={`${catalogCount ? `${catalogCount} tools` : 'tools'}${servers ? ` · ${servers} MCP server${servers > 1 ? 's' : ''}` : ''}`} open={open.has('tools')} onToggle={() => toggle('tools')}>
          <Tools />
        </Section>
      </> : null}
      <P muted>{version ? `Connected to the Go backend ${version}. Agents and messages are stored on the server.` : 'Prototype mode: tasks are stored in this browser. Run scripts/server.sh to use the Go backend.'}</P>
    </Shell>
  );
}

// Tool groups in the Tools section, by catalog source.
const toolGroups: [string, string, string][] = [
  ['built-in', 'Files and commands', "Part of Xhaos itself, no MCP server needed. Every agent can call them inside its project's folder (see Files); each call passes the agent's policy first."],
  ['channels', 'Agents and channels', 'Talking to other agents changes no files, so it runs at once; starting a subagent is gated like a write. Top-level agents reach each other across projects; subagents only their parent and siblings.'],
  ['memory', 'Memories', "Every agent sees the workspace's memories in its prompt; saving one is gated like a write."],
];
const effectNotes: Record<string, string> = {
  ask: 'waits for your answers in Questions',
  read: 'runs at once',
  write: 'asks under manual review; runs under auto',
  exec: 'read-only commands run at once; git add, commit and switch run under auto; anything else always asks',
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
  dictation: ['Voice dictation', <>
    <P>Your browser’s speech service may send audio to its provider. The transcript will appear in the command bar for you to review.</P>
    <Button variant="send" id="start-dictation" onClick={startDictation} sx={shell.wide}>Start microphone</Button>
  </>],
});

export function Panel() {
  const panel = useApp(s => s.panel);
  let body: ReactNode = null;
  if (panel.kind === 'questions') body = <Shell title="Questions"><div id="question-inbox" {...stylex.props(shell.questions)}><QuestionInbox /></div></Shell>;
  else if (panel.kind === 'files') body = <Shell title="Files"><Files /></Shell>;
  else if (panel.kind === 'memories') body = <Memories editId={panel.editId} />;
  else if (panel.kind === 'channels') body = <Channels channelId={panel.channelId} />;
  else if (panel.kind === 'providers') body = <Settings focus="models" message={panel.message} testing={panel.testing} />;
  else if (panel.kind === 'history') body = <History />;
  else if (panel.kind === 'status') body = <Status />;
  else if (panel.kind === 'help') body = <Help />;
  else if (panel.kind === 'settings') body = <Settings />;
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
  muted: { color: '#858585' },
  wide: { width: 'auto', paddingInline: 12, marginBlock: 8 },
  questions: { height: '100%', display: 'flex', flexDirection: 'column' },
});

const memory = stylex.create({
  note: { fontSize: 10, color: colors.textFaint, marginTop: 0, marginInline: 0, marginBottom: 8 },
  noteAfter: { marginTop: 8 },
  list: { display: 'flex', flexDirection: 'column', gap: 1 },
  row: { position: 'relative', paddingBlock: 8, paddingLeft: 7, paddingRight: 88, borderLeftWidth: 2, borderLeftStyle: 'solid', borderLeftColor: '#3b3b3b', backgroundColor: colors.sunken, minHeight: 40 },
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
  tabs: { display: 'flex', gap: 7, flexWrap: 'wrap' },
  tab: { paddingBlock: 5, paddingInline: 8 },
  members: { display: 'flex', gap: 6, flexWrap: 'wrap', marginBlock: 10 },
  log: { maxHeight: 260, overflow: 'auto', scrollbarWidth: 'thin' },
  message: { marginTop: 10, marginBottom: 16 },
  meta: { fontSize: 9, color: colors.textGhost, marginBottom: 5 },
  time: { marginLeft: 8 },
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
