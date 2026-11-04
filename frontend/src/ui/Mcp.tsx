// MCP servers in the Tools panel: their state and tools, how far their calls
// are trusted, and a form to add or edit one.
import { useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button, StatusDot } from './kit.tsx';
import { useApp } from './state/store.ts';
import { deleteMcp, reconnectMcp, saveMcp, updateMcp, type McpForm } from './state/panels.ts';
import type { McpServer, McpTrust } from '../types.ts';

const trustLabels: Record<McpTrust, string> = {
  ask: 'Ask every call',
  auto: 'Run under auto review',
  read: 'Read-only: run at once',
};
const statusDot = { connected: 'running', connecting: 'waiting', error: 'paused', disabled: 'idle' } as const;
const blank: McpForm = { id: '', name: '', transport: 'stdio', command: '', url: '', env: '', headers: '', trust: 'ask', enabled: true };

function ServerRow({ server, onEdit }: { server: McpServer; onEdit(): void }) {
  const where = server.transport === 'stdio' ? [server.command, ...(server.args || [])].join(' ') : server.url;
  return (
    <div data-mcp={server.id} data-status={server.status} {...stylex.props(styles.server)}>
      <div {...stylex.props(styles.head)}>
        <StatusDot status={statusDot[server.status]} label={server.status} size={7} />
        <strong {...stylex.props(styles.name)}>{server.name}</strong>
        <code title={where} {...stylex.props(styles.where)}>{where}</code>
      </div>
      {server.error && <p {...stylex.props(styles.error)}>{server.error}</p>}
      <div {...stylex.props(styles.controls)}>
        <select aria-label={`Trust for ${server.name}`} data-mcp-trust={server.id} value={server.trust}
          onChange={event => updateMcp(server, { trust: event.target.value as McpTrust })} {...stylex.props(styles.select)}>
          {(Object.keys(trustLabels) as McpTrust[]).map(t => <option key={t} value={t}>{trustLabels[t]}</option>)}
        </select>
        <label {...stylex.props(styles.check)}>
          <input type="checkbox" data-mcp-enabled={server.id} checked={server.enabled} onChange={event => updateMcp(server, { enabled: event.target.checked })} /> Enabled
        </label>
        <span {...stylex.props(styles.spacer)} />
        <Button data-mcp-reconnect={server.id} onClick={() => reconnectMcp(server)}>Reconnect</Button>
        <Button data-mcp-edit={server.id} onClick={onEdit}>Edit</Button>
        <Button data-mcp-remove={server.id} onClick={() => deleteMcp(server)}>Remove</Button>
      </div>
      {server.tools.length > 0 && (
        <details {...stylex.props(styles.tools)}>
          <summary {...stylex.props(styles.summary)}>{server.tools.length} tool{server.tools.length > 1 ? 's' : ''}</summary>
          {server.tools.map(t => (
            <div key={t.name} data-mcp-tool={t.agentName} {...stylex.props(styles.tool)}>
              <code {...stylex.props(styles.toolName)}>{t.name}</code>{t.readOnly && <span {...stylex.props(styles.hint)}>says read-only</span>}
              <p {...stylex.props(styles.toolText)}>{t.description}</p>
            </div>
          ))}
        </details>
      )}
    </div>
  );
}

export function McpServers() {
  const servers = useApp(s => s.mcpServers);
  const [form, setForm] = useState<McpForm | null>(null);
  const [editing, setEditing] = useState<McpServer | null>(null);
  const set = (field: keyof McpForm) => (event: { target: { value: string } }) => setForm(f => f && { ...f, [field]: event.target.value });
  const edit = (s: McpServer) => {
    setEditing(s);
    setForm({ id: s.id, name: s.name, transport: s.transport, command: [s.command, ...(s.args || [])].filter(Boolean).join(' '), url: s.url || '', env: '', headers: '', trust: s.trust, enabled: s.enabled });
  };
  const save = async () => { if (form && await saveMcp(form)) { setForm(null); setEditing(null); } };
  return <>
    {servers.length ? servers.map(s => <ServerRow key={s.id} server={s} onEdit={() => edit(s)} />)
      : <p {...stylex.props(styles.muted)}>None yet. Add a local command (stdio) or a remote URL; its tools appear here and pass the same policy gate as the built-in ones.</p>}
    {!form && <Button id="mcp-add" onClick={() => { setEditing(null); setForm(blank); }} sx={styles.add}>＋ Add MCP server</Button>}
    {form && (
      <div id="mcp-form" {...stylex.props(styles.form)}
        onKeyDown={event => { if (event.key === 'Enter' && (event.target as HTMLElement).tagName === 'INPUT' && !event.nativeEvent.isComposing) { event.preventDefault(); save(); } }}>
        <span {...stylex.props(styles.formTitle)}>{editing ? `Edit ${editing.name}` : 'Add an MCP server'}</span>
        <div {...stylex.props(styles.grid)}>
          <label htmlFor="mcp-id" {...stylex.props(styles.label)}>ID</label>
          <input id="mcp-id" value={form.id} onChange={set('id')} disabled={Boolean(editing)} placeholder="e.g. github (lowercase, used in tool names)" autoComplete="off" spellCheck="false" {...stylex.props(styles.input)} />
          <label htmlFor="mcp-name" {...stylex.props(styles.label)}>Name</label>
          <input id="mcp-name" value={form.name} onChange={set('name')} placeholder="Shown to you and the agents" autoComplete="off" {...stylex.props(styles.input)} />
          <label htmlFor="mcp-transport" {...stylex.props(styles.label)}>Kind</label>
          <select id="mcp-transport" value={form.transport} onChange={set('transport')} {...stylex.props(styles.input)}>
            <option value="stdio">Local command (stdio)</option>
            <option value="http">Remote URL (Streamable HTTP)</option>
          </select>
          {form.transport === 'stdio' ? <>
            <label htmlFor="mcp-command" {...stylex.props(styles.label)}>Command</label>
            <input id="mcp-command" value={form.command} onChange={set('command')} placeholder="e.g. npx -y @modelcontextprotocol/server-everything" autoComplete="off" spellCheck="false" {...stylex.props(styles.input)} />
            <label htmlFor="mcp-env" {...stylex.props(styles.label)}>Env</label>
            <textarea id="mcp-env" value={form.env} onChange={set('env')} rows={2} spellCheck="false"
              placeholder={editing?.envKeys?.length ? `Saved: ${editing.envKeys.join(', ')}. Leave empty to keep.` : 'KEY=value, one per line (encrypted, never shown again)'} {...stylex.props(styles.input, styles.secret)} />
          </> : <>
            <label htmlFor="mcp-url" {...stylex.props(styles.label)}>URL</label>
            <input id="mcp-url" value={form.url} onChange={set('url')} placeholder="https://…/mcp" autoComplete="off" spellCheck="false" {...stylex.props(styles.input)} />
            <label htmlFor="mcp-headers" {...stylex.props(styles.label)}>Headers</label>
            <textarea id="mcp-headers" value={form.headers} onChange={set('headers')} rows={2} spellCheck="false"
              placeholder={editing?.headerKeys?.length ? `Saved: ${editing.headerKeys.join(', ')}. Leave empty to keep.` : 'Authorization: Bearer …, one per line (encrypted)'} {...stylex.props(styles.input, styles.secret)} />
          </>}
          <label htmlFor="mcp-trust" {...stylex.props(styles.label)}>Trust</label>
          <select id="mcp-trust" value={form.trust} onChange={set('trust')} {...stylex.props(styles.input)}>
            {(Object.keys(trustLabels) as McpTrust[]).map(t => <option key={t} value={t}>{trustLabels[t]}</option>)}
          </select>
        </div>
        <p {...stylex.props(styles.muted)}>The Xhaos server runs local commands as its own user and makes remote calls itself. Anyone who can open this page can add servers.</p>
        <div {...stylex.props(styles.actions)}>
          <Button id="mcp-cancel" onClick={() => { setForm(null); setEditing(null); }}>Cancel</Button>
          <Button id="mcp-save" onClick={save}>{editing ? 'Save and reconnect' : 'Add and connect'}</Button>
        </div>
      </div>
    )}
  </>;
}

const styles = stylex.create({
  muted: { color: '#858585', fontSize: 10, marginTop: 6, marginBottom: 8 },
  server: { borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, borderRadius: 4, backgroundColor: colors.sunken, paddingBlock: 8, paddingInline: 10, marginBottom: 6 },
  head: { display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 },
  name: { fontSize: 12, color: colors.text, flexShrink: 0 },
  where: { fontSize: 10, color: colors.textGhost, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' },
  error: { color: colors.danger, fontSize: 10, marginTop: 4, marginBottom: 0, overflowWrap: 'anywhere' },
  controls: { display: 'flex', alignItems: 'center', gap: 8, marginTop: 6, flexWrap: 'wrap' },
  select: { backgroundColor: colors.bg, color: colors.textSoft, borderWidth: 0, borderRadius: 3, paddingBlock: 3, paddingInline: 6, fontSize: 10 },
  check: { display: 'flex', alignItems: 'center', gap: 4, fontSize: 10, color: colors.textMuted },
  spacer: { flexGrow: 1 },
  tools: { marginTop: 6 },
  summary: { cursor: 'pointer', fontSize: 10, color: colors.textMuted },
  tool: { paddingBlock: 4, paddingLeft: 12 },
  toolName: { color: colors.text, fontSize: 10 },
  hint: { marginLeft: 6, fontSize: 9, color: colors.textGhost },
  toolText: { marginTop: 1, marginBottom: 0, fontSize: 10, color: colors.textDim },
  add: { marginTop: 4 },
  form: { marginTop: 8, borderTopWidth: 1, borderTopStyle: 'solid', borderTopColor: colors.lineFaint, paddingTop: 8 },
  formTitle: { display: 'block', color: colors.textMuted, marginBottom: 8 },
  grid: { display: 'grid', gridTemplateColumns: '70px 1fr', alignItems: 'center', columnGap: 8, rowGap: 6 },
  label: { color: colors.textFaint, fontSize: 10 },
  input: { minWidth: 0, backgroundColor: colors.sunken, borderWidth: 0, borderRadius: 3, color: colors.text, paddingBlock: 6, paddingInline: 8, fontSize: 11, fontFamily: 'inherit', resize: 'vertical' },
  // Secret values are masked as you type.
  secret: { WebkitTextSecurity: 'disc' },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 10, marginTop: 4 },
});
