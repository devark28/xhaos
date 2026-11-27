// Settings → MCP access: tokens that let an MCP client (Claude Code, say)
// reach this workspace at /mcp and act for you within their scopes. A token
// is shown once; only its hash is kept. Creating one asks for the
// passphrase: it works from anywhere the server is reachable.
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors, fonts } from './theme/tokens.stylex.ts';
import { Button, Info } from './kit.tsx';
import { call } from './state/backend.ts';
import { useApp, toast, errorText, get } from './state/store.ts';
import { formatTime } from './appearance.ts';

interface TokenView { id: number; name: string; scopes: string[]; createdAt: string; lastUsedAt?: string }
interface Issued { token: TokenView; secret: string; path: string }

// The scopes, as mcpserver.Scopes lists them.
export const scopeInfo: { id: string; label: string; text: string }[] = [
  { id: 'read', label: 'Read', text: 'List agents, read their messages, questions, channels and status, and wait for events.' },
  { id: 'drive', label: 'Drive', text: 'Create agents, send them messages and tasks, start and stop them, open channels and post to them.' },
  { id: 'answer', label: 'Answer questions', text: "Answer agents' questions (ask_user) for you." },
  { id: 'approve-feature-branch', label: 'Approve feature-branch commits', text: 'Allow or deny git commits and pushes, only plain git commands on branches that are not protected. main, integration, release… and tags stay yours.' },
];

// mcpCommand is the line that adds this server to Claude Code.
export const mcpCommand = (origin: string, path: string, secret: string) =>
  `claude mcp add --transport http xhaos ${origin}${path} --header "Authorization: Bearer ${secret}"`;

export function McpAccess() {
  const ws = useApp(s => s.active);
  const [list, setList] = useState<TokenView[] | null>(null);
  const [adding, setAdding] = useState(false), [issued, setIssued] = useState<Issued | null>(null);
  const load = async () => { try { setList(await call<TokenView[]>('mcpserver', 'Tokens', ws)); } catch (error) { toast(`Could not load MCP tokens: ${errorText(error)}`); } };
  useEffect(() => { void load(); }, [ws]);
  const revoke = async (t: TokenView) => {
    if (!confirm(`Revoke the token ${t.name}? Clients using it are refused from now on.`)) return;
    try { await call('mcpserver', 'Revoke', { workspaceId: get().active, id: t.id }); toast(`Revoked ${t.name}.`); void load(); } catch (error) { toast(errorText(error)); }
  };
  if (!list) return <p {...stylex.props(s.muted)}>Loading…</p>;
  return (
    <div data-mcp-access {...stylex.props(s.root)}>
      <p {...stylex.props(s.muted)}>
        An MCP client such as Claude Code can reach this workspace at <code {...stylex.props(s.code)}>{location.origin}/mcp</code> with a token: list and start agents, give them tasks, read and answer their questions, and talk in channels, for you. What it does shows as <em>via MCP</em> with the token's name. Give each token only the scopes it needs. While the workspace is locked (after a restart, until you sign in) its tools refuse.
      </p>
      {issued && <Secret issued={issued} onDone={() => setIssued(null)} />}
      {list.length === 0 && !adding && !issued && <p {...stylex.props(s.muted)}>No tokens yet.</p>}
      {list.map(t => (
        <section key={t.id} data-mcp-token={t.name} {...stylex.props(s.row)}>
          <div {...stylex.props(s.head)}>
            <span {...stylex.props(s.name)}>{t.name}</span>
            <span {...stylex.props(s.scopes)}>{t.scopes.map(sc => <span key={sc} data-scope={sc} {...stylex.props(s.chip, sc !== 'read' && s.chipStrong)}>{scopeInfo.find(x => x.id === sc)?.label || sc}</span>)}</span>
            <Button data-mcp-revoke={t.name} onClick={() => revoke(t)}>Revoke</Button>
          </div>
          <div {...stylex.props(s.meta)}>created {formatTime(t.createdAt)} · {t.lastUsedAt ? `last used ${formatTime(t.lastUsedAt)}` : 'never used'}</div>
        </section>
      ))}
      {adding
        ? <TokenForm onDone={result => { setAdding(false); if (result) { setIssued(result); void load(); } }} />
        : <div {...stylex.props(s.actions)}><Button data-mcp-new onClick={() => { setIssued(null); setAdding(true); }}>Create a token</Button></div>}
    </div>
  );
}

function TokenForm({ onDone }: { onDone(result: Issued | null): void }) {
  const [name, setName] = useState('Claude Code'), [pass, setPass] = useState('');
  const [scopes, setScopes] = useState<Set<string>>(() => new Set(['read', 'drive']));
  const [busy, setBusy] = useState(false);
  const toggle = (id: string) => setScopes(old => { const next = new Set(old); if (next.has(id)) next.delete(id); else next.add(id); return next; });
  const create = async () => {
    setBusy(true);
    try { onDone(await call<Issued>('mcpserver', 'Create', { workspaceId: get().active, name: name.trim(), scopes: [...scopes], passphrase: pass })); }
    catch (error) { toast(errorText(error)); }
    finally { setBusy(false); }
  };
  return (
    <section data-mcp-form {...stylex.props(s.row, s.form)}>
      <label {...stylex.props(s.field)}><span {...stylex.props(s.label)}>Name<Info text="Shown beside everything the client does with this token, in transcripts and channels." /></span>
        <input value={name} maxLength={40} onChange={e => setName(e.target.value)} data-mcp-name {...stylex.props(s.input)} /></label>
      <div {...stylex.props(s.field)}><span {...stylex.props(s.label)}>Scopes</span>
        <div {...stylex.props(s.scopeList)}>
          {scopeInfo.map(sc => (
            <label key={sc.id} {...stylex.props(s.scope)}>
              <input type="checkbox" checked={scopes.has(sc.id)} onChange={() => toggle(sc.id)} data-mcp-scope={sc.id} />
              <span><span {...stylex.props(s.scopeName)}>{sc.label}</span> <span {...stylex.props(s.scopeText)}>{sc.text}</span></span>
            </label>
          ))}
        </div>
      </div>
      <label {...stylex.props(s.field)}><span {...stylex.props(s.label)}>Passphrase<Info text="This workspace's passphrase: a token acts for you from anywhere the server is reachable." /></span>
        <input type="password" autoComplete="current-password" value={pass} onChange={e => setPass(e.target.value)} onKeyDown={e => e.key === 'Enter' && !busy && create()} data-mcp-pass {...stylex.props(s.input)} /></label>
      <div {...stylex.props(s.actions)}>
        <Button onClick={() => onDone(null)}>Cancel</Button>
        <Button data-mcp-create disabled={busy || !name.trim() || scopes.size === 0} onClick={create}>Create</Button>
      </div>
    </section>
  );
}

// Secret shows a new token once, with the command that adds it to Claude Code.
function Secret({ issued, onDone }: { issued: Issued; onDone(): void }) {
  const command = mcpCommand(location.origin, issued.path, issued.secret);
  const copy = (text: string, what: string) => {
    // navigator.clipboard is missing on insecure origins (a LAN address).
    if (!navigator.clipboard) { toast('This page cannot copy here (plain HTTP); select the text and copy it.'); return; }
    navigator.clipboard.writeText(text).then(() => toast(`${what} copied.`), () => toast('Copy failed; select the text instead.'));
  };
  return (
    <section data-mcp-secret {...stylex.props(s.row, s.secretBox)}>
      <p {...stylex.props(s.text)}>Copy the token for <strong>{issued.token.name}</strong> now; it is not shown again.</p>
      <div {...stylex.props(s.inline)}>
        <code data-mcp-token-value {...stylex.props(s.value)}>{issued.secret}</code>
        <Button data-mcp-copy-token onClick={() => copy(issued.secret, 'Token')}>Copy</Button>
      </div>
      <p {...stylex.props(s.muted)}>Add it to Claude Code:</p>
      <div {...stylex.props(s.inline)}>
        <code data-mcp-command {...stylex.props(s.value, s.command)}>{command}</code>
        <Button data-mcp-copy-command onClick={() => copy(command, 'Command')}>Copy</Button>
      </div>
      <div {...stylex.props(s.actions)}><Button data-mcp-done onClick={onDone}>Done</Button></div>
    </section>
  );
}

const s = stylex.create({
  root: { display: 'flex', flexDirection: 'column', gap: 8, fontSize: 11 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5, lineHeight: 1.5 },
  text: { margin: 0, color: colors.text, fontSize: 11, lineHeight: 1.5 },
  code: { fontFamily: fonts.mono, fontSize: 10, color: colors.textSoft },
  row: { display: 'flex', flexDirection: 'column', gap: 6, paddingBlock: 8, paddingInline: 10, borderRadius: 5, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, backgroundColor: colors.sunken, minWidth: 0 },
  form: { borderColor: colors.line },
  secretBox: { borderColor: colors.brand },
  head: { display: 'flex', alignItems: 'flex-start', gap: 8, minWidth: 0 },
  name: { fontSize: 11, fontWeight: 600, color: colors.textStrong, flexShrink: 0, paddingTop: 1 },
  scopes: { display: 'flex', gap: 4, flexWrap: 'wrap', flexGrow: 1, minWidth: 0 },
  chip: { fontSize: 9.5, paddingInline: 6, paddingBlock: 1, borderRadius: 9, backgroundColor: colors.card, color: colors.textSoft },
  chipStrong: { backgroundColor: colors.brandSoft, color: colors.brand },
  meta: { fontSize: 10, color: colors.textFaint },
  inline: { display: 'flex', gap: 6, alignItems: 'center', minWidth: 0 },
  value: { fontFamily: fonts.mono, fontSize: 10.5, color: colors.textStrong, backgroundColor: colors.card, paddingBlock: 4, paddingInline: 6, borderRadius: 3, overflowWrap: 'anywhere', userSelect: 'all', flexGrow: 1, minWidth: 0 },
  command: { fontSize: 10, whiteSpace: 'pre-wrap' },
  field: { display: 'grid', gridTemplateColumns: '110px minmax(0, 1fr)', alignItems: 'start', gap: 8 },
  label: { color: colors.textFaint, fontSize: 10, paddingTop: 5 },
  input: { minWidth: 0, backgroundColor: colors.card, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 7, fontSize: 11, fontFamily: 'inherit' },
  scopeList: { display: 'flex', flexDirection: 'column', gap: 5 },
  scope: { display: 'flex', gap: 6, alignItems: 'flex-start', cursor: 'pointer', lineHeight: 1.4 },
  scopeName: { color: colors.text, fontWeight: 600 },
  scopeText: { color: colors.textFaint, fontSize: 10.5 },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 8 },
});
