// Panels open inside the composer section and replace the composer until
// closed (Esc or ×). Every panel uses Shell, so they share heading, close
// button, scrolling, and Esc handling (in keys.ts).
import { useEffect, useLayoutEffect, useRef, type ReactNode } from 'react';
import * as stylex from '@stylexjs/stylex';
import { markdown } from '../markdown.ts';
import { flatten } from '../model.ts';
import { colors } from './theme/tokens.stylex.ts';
import { Button, Chip, StatusDot } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { labels } from './AgentCard.tsx';
import { AttachedFiles } from './Transcript.tsx';
import {
  S, workspace, openPanel, closePanel, focusComposer, saveMemory, deleteMemory, restore, slashCommands,
  showProviders, saveProviderKey, testProvider, removeProvider, startDictation,
} from './store.ts';

const html = (value: string) => ({ __html: value });

function Shell({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return <>
    <div data-panel-heading {...stylex.props(shell.heading)}>
      <h2 {...stylex.props(shell.title)}>{title}</h2>
      <div {...stylex.props(shell.actions)}>
        {action}
        <Button variant="icon" id="close-panel" aria-label={`Close ${title}`} onClick={() => { closePanel(); focusComposer(); }}><Icon name="close" size={14} /></Button>
      </div>
    </div>
    <div data-panel-content {...stylex.props(shell.content)}>{children}</div>
  </>;
}

// Body text in panels.
function P({ children, muted = false }: { children: ReactNode; muted?: boolean }) {
  return <p {...stylex.props(shell.p, muted && shell.muted)}>{children}</p>;
}

function Memories({ editId }: { editId?: string }) {
  const visible = S.memories.filter(memory => memory.workspaceId === S.active);
  const editing = S.memories.find(memory => memory.id === editId);
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
      <p {...stylex.props(memory.note)}>Shared across agents in this workspace. Session summaries stay with their conversation.</p>
      <div {...stylex.props(memory.list)}>
        {visible.length ? visible.map(item => (
          <article key={item.id} {...stylex.props(memory.row)}>
            <div {...stylex.props(memory.content)}><div className="markdown-body" dangerouslySetInnerHTML={html(markdown(item.content))} /></div>
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
  const w = workspace(), channels = w.channels || [];
  const current = channels.find(channel => channel.id === channelId) || channels[0];
  const identity = (id: number | string) => {
    const agent = flatten(w.agents).find(agent => agent.id === Number(id));
    return agent ? `#${agent.id} ${agent.title}` : id === 'user' ? 'User (legacy preview message)' : String(id);
  };
  if (!current) {
    return (
      <Shell title="Agent channels">
        <P>No agent channels yet.</P>
        <P>Agents will create channels as they need to coordinate. Their participants and conversations will appear here automatically.</P>
        <P muted>Agent execution and channel delivery are not connected in this prototype.</P>
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
      <p {...stylex.props(memory.note, memory.noteAfter)}>Read-only channel log. Agents create subjects and exchange messages through the backend; this prototype does not deliver them.</p>
    </Shell>
  );
}

function Providers({ message, testing }: { message?: string; testing?: string }) {
  const key = useRef<HTMLInputElement>(null);
  if (!S.backend) return <Shell title="Model providers"><P>Providers are configured on the Go backend. Run scripts/server.sh and open its page.</P></Shell>;
  const anthropic = S.providers.find(p => p.id === 'anthropic');
  const save = () => saveProviderKey(key.current!.value);
  return (
    <Shell title="Model providers">
      <div>
        {S.providers.length ? S.providers.map(p => (
          <div key={p.id} data-provider={p.id} {...stylex.props(provider.row)}>
            <div>
              <strong>{p.name}</strong> <span {...stylex.props(shell.muted)}>{p.kind}{p.hasKey ? ` · key ${p.keyHint}` : ' · no key'}</span>
              <div {...stylex.props(shell.muted, provider.models)}>{p.models.join(', ')}</div>
            </div>
            <div {...stylex.props(provider.actions)}>
              <Button data-test-provider={p.id} onClick={() => testProvider(p.id)}>{testing === p.id ? 'Testing…' : 'Test'}</Button>
              <Button data-remove-provider={p.id} onClick={() => removeProvider(p.id)}>Remove</Button>
            </div>
          </div>
        )) : <P muted>No providers yet.</P>}
      </div>
      {/* Not a <form> with a password field: browsers would offer to store the
          key in their password manager (and Chromium's prompt steals focus). */}
      <div id="provider-form" {...stylex.props(provider.form)}>
        <label htmlFor="provider-key" {...stylex.props(provider.label)}>Anthropic API key{anthropic?.hasKey ? ' (leave empty to keep the saved key)' : ''}</label>
        <div {...stylex.props(provider.keyRow)}>
          <input id="provider-key" ref={key} type="text" name="xhaos-provider-key" autoComplete="off" autoCapitalize="off" spellCheck="false" data-1p-ignore="" data-lpignore="true" data-bwignore="" placeholder="sk-ant-…"
            onKeyDown={event => { if (event.key === 'Enter' && !event.nativeEvent.isComposing) { event.preventDefault(); save(); } }} {...stylex.props(provider.key)} />
          <Button id="provider-save" onClick={save}>Save</Button>
        </div>
        <P muted>Keys are sent to the Xhaos server, encrypted at rest, and never shown again. Anyone who can open this page can use them.</P>
      </div>
      {message && <p id="provider-message" {...stylex.props(provider.message)}>{message}</p>}
    </Shell>
  );
}

function History() {
  const archived = flatten(workspace().agents).filter(agent => agent.archivedAt);
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

function Settings() {
  return (
    <Shell title="Xhaos settings">
      <P>Xhaos is a browser-first agent harness with a headless Go backend.</P>
      <P>{S.backend ? `Connected to the Go backend ${S.backend.status.version}. Agents and messages are stored on the server.` : 'Prototype mode: tasks are stored in this browser. Run scripts/server.sh to use the Go backend.'}</P>
      {S.backend && <Button id="open-providers" onClick={() => showProviders()}>Model providers…</Button>}
      <P muted>MCP servers and automatic approval classification are planned.</P>
    </Shell>
  );
}

const effectNotes = { read: 'runs at once', write: 'gated: asks under manual review', exec: 'gated like a write' };
function Tools() {
  const catalog = S.tools;
  return (
    <Shell title="Tools and MCP">
      {!S.backend ? <P>Tools run on the Go backend. Run scripts/server.sh and open its page.</P>
        : !catalog ? <P muted>Loading…</P>
        : catalog.error !== undefined ? <P>Could not load tools: {catalog.error}</P>
        : <>
          <P>Built-in tools, part of Xhaos (not MCP). Every agent can call them in <code>{catalog.root}</code>; each call passes the agent's policy first.</P>
          <div {...stylex.props(tools.list)}>
            {catalog.tools.map(t => (
              <div key={t.name} data-tool={t.name} {...stylex.props(tools.row)}>
                <code {...stylex.props(tools.name)}>{t.name}</code>
                <span data-effect={t.effect} {...stylex.props(tools.effect, t.effect === 'read' ? tools.read : tools.gated)}>{t.effect} · {effectNotes[t.effect]}</span>
                <p {...stylex.props(tools.description)}>{t.description}</p>
              </div>
            ))}
          </div>
          <h3 {...stylex.props(tools.subheading)}>MCP servers</h3>
          <P muted>None yet. Adding MCP servers (local commands and remote URLs) is the next step; their tools will appear here and pass the same policy gate.</P>
        </>}
    </Shell>
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
  const panel = S.panel;
  // Focus the close button when a panel opens (memory editing focuses its field).
  useEffect(() => {
    if (panel && !(panel.kind === 'memories' && panel.editId)) document.getElementById('close-panel')?.focus({ preventScroll: true });
  }, [panel?.kind]);
  if (!panel) return <section id="utility-panel" aria-label="Panel" hidden />;
  let body: ReactNode = null;
  if (panel.kind === 'memories') body = <Memories editId={panel.editId} />;
  else if (panel.kind === 'channels') body = <Channels channelId={panel.channelId} />;
  else if (panel.kind === 'providers') body = <Providers message={panel.message} testing={panel.testing} />;
  else if (panel.kind === 'history') body = <History />;
  else if (panel.kind === 'status') body = <Status />;
  else if (panel.kind === 'help') body = <Help />;
  else if (panel.kind === 'settings') body = <Settings />;
  else if (panel.kind === 'tools') body = <Tools />;
  else { const entry = simple()[panel.kind]; if (entry) body = <Shell title={entry[0]}>{entry[1]}</Shell>; }
  return <section id="utility-panel" aria-label="Panel" {...stylex.props(shell.root)}>{body}</section>;
}

const shell = stylex.create({
  root: { flexGrow: 1, minHeight: 0, display: 'flex', flexDirection: 'column', margin: 0 },
  heading: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', paddingBlock: 8, paddingInline: 10, backgroundColor: colors.panelHead },
  title: { fontSize: 12, fontWeight: 500, margin: 0 },
  actions: { display: 'flex', alignItems: 'center', gap: 10 },
  content: { flexGrow: 1, minHeight: 0, overflow: 'auto', paddingBlock: 8, paddingInline: 12, fontSize: 11, lineHeight: 1.65 },
  p: { color: colors.textMuted, marginTop: 6, marginBottom: 12, marginInline: 0 },
  muted: { color: '#858585' },
  wide: { width: 'auto', paddingInline: 12, marginBlock: 8 },
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
  label: { display: 'block', color: colors.textMuted, marginBottom: 6 },
  keyRow: { display: 'flex', gap: 10, alignItems: 'center' },
  // Masked without type=password, so password managers leave it alone.
  key: { flexGrow: 1, minWidth: 0, backgroundColor: colors.sunken, borderWidth: 0, borderRadius: 3, color: colors.text, paddingBlock: 7, paddingInline: 8, fontSize: 11, WebkitTextSecurity: 'disc' },
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
});
