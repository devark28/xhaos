// Settings → Secrets: the workspace's vault. Values are written here and
// never shown again unless revealed with the passphrase; agents see only
// names. Granting a secret to an agent lets its commands ask to use it;
// trusting it (passphrase again) lets them use it without asking each time.
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors, fonts } from './theme/tokens.stylex.ts';
import { Button, Info } from './kit.tsx';
import { activeAgents } from '../model.ts';
import { call } from './state/backend.ts';
import { useApp, workspaceOf, toast, errorText, get } from './state/store.ts';

interface Grant { agentId: number; mode: 'ask' | 'trusted' }
interface SecretView { name: string; kind: 'token' | 'ssh'; description: string; hosts: string[]; grants: Grant[]; updatedAt: string }

const hostsOf = (text: string) => text.split(/[\s,]+/).map(h => h.trim()).filter(Boolean);

export function useSecrets() {
  const version = useApp(s => s.secretsVersion), ws = useApp(s => s.active);
  const [list, setList] = useState<SecretView[] | null>(null);
  const load = async () => { try { setList(await call<SecretView[]>('secret', 'Secrets', ws)); } catch (error) { toast(`Could not load secrets: ${errorText(error)}`); } };
  useEffect(() => { void load(); }, [version, ws]);
  return { list, load };
}

export function Secrets() {
  const { list, load } = useSecrets();
  const [editing, setEditing] = useState<SecretView | 'new' | null>(null);
  if (!list) return <p {...stylex.props(s.muted)}>Loading…</p>;
  return (
    <div data-secrets {...stylex.props(s.root)}>
      <p {...stylex.props(s.muted)}>
        Values are encrypted with your passphrase's key and never shown to agents. A command uses a secret as <code>$NAME</code>, can reach only the secret's hosts, and cannot change the project; the value is removed from its output.
      </p>
      {list.length === 0 && !editing && <p {...stylex.props(s.muted)}>No secrets yet.</p>}
      {list.map(v => editing !== 'new' && editing?.name === v.name
        ? <SecretForm key={v.name} existing={v} onDone={() => { setEditing(null); void load(); }} />
        : <SecretRow key={v.name} v={v} onEdit={() => setEditing(v)} onChanged={load} />)}
      {editing === 'new'
        ? <SecretForm onDone={() => { setEditing(null); void load(); }} />
        : <div {...stylex.props(s.actions)}><Button data-secret-new onClick={() => setEditing('new')}>Add a secret</Button></div>}
    </div>
  );
}

function SecretRow({ v, onEdit, onChanged }: { v: SecretView; onEdit(): void; onChanged(): void }) {
  const agents = activeAgents(useApp(st => workspaceOf(st).agents)).filter(a => !a.sample && a.status !== 'terminated');
  const [revealing, setRevealing] = useState(false), [pass, setPass] = useState(''), [value, setValue] = useState<string | null>(null);
  const [agentId, setAgentId] = useState(''), [mode, setMode] = useState<'ask' | 'trusted'>('ask'), [grantPass, setGrantPass] = useState('');
  const ws = get().active;
  const reveal = async () => {
    try { setValue(await call<string>('secret', 'Reveal', { workspaceId: ws, name: v.name, passphrase: pass })); setPass(''); }
    catch (error) { toast(errorText(error)); }
  };
  const grant = async (id: number, m: '' | 'ask' | 'trusted', passphrase = '') => {
    try { await call('secret', 'Grant', { workspaceId: ws, name: v.name, agentId: id, mode: m, passphrase }); setAgentId(''); setGrantPass(''); onChanged(); }
    catch (error) { toast(errorText(error)); }
  };
  const remove = async () => {
    if (!confirm(`Delete secret ${v.name}? Agents lose access to it.`)) return;
    try { await call('secret', 'Delete', { workspaceId: ws, name: v.name }); onChanged(); } catch (error) { toast(errorText(error)); }
  };
  const ungranted = agents.filter(a => !v.grants.some(g => g.agentId === a.id));
  return (
    <section data-secret={v.name} {...stylex.props(s.row)}>
      <div {...stylex.props(s.head)}>
        <code {...stylex.props(s.name)}>{v.name}</code>
        {v.kind === 'ssh' && <span data-secret-kind="ssh" {...stylex.props(s.kind)}>SSH key</span>}
        <span {...stylex.props(s.desc)}>{v.description}</span>
        <Button data-secret-reveal={v.name} onClick={() => { setRevealing(!revealing); setValue(null); }}>{revealing ? 'Hide' : 'Reveal'}</Button>
        <Button data-secret-edit={v.name} onClick={onEdit}>Edit</Button>
        <Button data-secret-delete={v.name} onClick={remove}>Delete</Button>
      </div>
      <div {...stylex.props(s.meta)}>{v.hosts.length ? <>reaches {v.hosts.map(h => <code key={h} {...stylex.props(s.host)}>{h}</code>)}</> : 'no network'}</div>
      {revealing && (value === null
        ? <div {...stylex.props(s.inline)}>
            <input type="password" autoComplete="current-password" placeholder="Your passphrase" value={pass} onChange={e => setPass(e.target.value)} onKeyDown={e => e.key === 'Enter' && reveal()} data-secret-pass {...stylex.props(s.input)} />
            <Button data-secret-show onClick={reveal}>Show</Button>
          </div>
        : <div {...stylex.props(s.inline)}>
            <code data-secret-value {...stylex.props(s.value, v.kind === 'ssh' && s.valueBlock)}>{value}</code>
            <Button onClick={() => { if (!navigator.clipboard) { toast('Copying needs HTTPS here; select the value instead.'); return; } void navigator.clipboard.writeText(value).then(() => toast('Copied.')); }}>Copy</Button>
          </div>)}
      <div {...stylex.props(s.grants)}>
        <span {...stylex.props(s.label)}>Agents<Info text="Granted: each command using it asks you first. Trusted: commands using it are gated like any other command (your passphrase is needed to trust). Never granted by default." /></span>
        {v.grants.length === 0 && <span {...stylex.props(s.muted)}>none</span>}
        {v.grants.map(g => (
          <span key={g.agentId} data-grant={g.agentId} {...stylex.props(s.chip, g.mode === 'trusted' && s.chipTrusted)}>
            #{g.agentId} {g.mode === 'trusted' ? 'trusted' : 'asks'}
            <button type="button" aria-label={`Revoke from #${g.agentId}`} data-revoke={g.agentId} onClick={() => grant(g.agentId, '')} {...stylex.props(s.x)}>×</button>
          </span>
        ))}
      </div>
      {ungranted.length > 0 && (
        <div {...stylex.props(s.inline)}>
          <select value={agentId} onChange={e => setAgentId(e.target.value)} data-grant-agent {...stylex.props(s.input)}>
            <option value="">Grant to agent…</option>
            {ungranted.map(a => <option key={a.id} value={a.id}>#{a.id} {a.title.slice(0, 40)}</option>)}
          </select>
          <select value={mode} onChange={e => setMode(e.target.value as 'ask' | 'trusted')} data-grant-mode {...stylex.props(s.input)}>
            <option value="ask">ask each use</option>
            <option value="trusted">trusted</option>
          </select>
          {mode === 'trusted' && <input type="password" placeholder="Passphrase" value={grantPass} onChange={e => setGrantPass(e.target.value)} data-grant-pass {...stylex.props(s.input)} />}
          <Button data-grant-add disabled={!agentId} onClick={() => grant(Number(agentId), mode, grantPass)}>Grant</Button>
        </div>
      )}
    </section>
  );
}

function SecretForm({ existing, onDone }: { existing?: SecretView; onDone(): void }) {
  const [name, setName] = useState(existing?.name || ''), [value, setValue] = useState('');
  const [kind, setKind] = useState<'token' | 'ssh'>(existing?.kind || 'token');
  const [description, setDescription] = useState(existing?.description || ''), [hosts, setHosts] = useState(existing?.hosts.join(', ') || '');
  const save = async () => {
    try {
      await call('secret', 'Save', { workspaceId: get().active, name: name.trim(), kind, value, description, hosts: hostsOf(hosts) });
      toast(`Saved ${name.trim()}.`); onDone();
    } catch (error) { toast(errorText(error)); }
  };
  return (
    <section data-secret-form {...stylex.props(s.row, s.form)}>
      <label {...stylex.props(s.field)}><span {...stylex.props(s.label)}>Kind<Info text="Token: an API token or password, given to the command as $NAME. SSH key: a private key; the command's ssh (and git over ssh) use it for its hosts, and $NAME is the key file's path." /></span>
        <select value={kind} disabled={Boolean(existing)} onChange={e => setKind(e.target.value as 'token' | 'ssh')} data-secret-kind-input {...stylex.props(s.input)}>
          <option value="token">Token</option><option value="ssh">SSH key</option>
        </select></label>
      <label {...stylex.props(s.field)}><span {...stylex.props(s.label)}>Name<Info text="Used as an environment variable: capitals, digits and _ (like GITHUB_TOKEN)." /></span>
        <input value={name} disabled={Boolean(existing)} onChange={e => setName(e.target.value.toUpperCase())} placeholder="GITHUB_TOKEN" data-secret-name {...stylex.props(s.input, s.mono)} /></label>
      <label {...stylex.props(s.field)}><span {...stylex.props(s.label)}>{kind === 'ssh' ? 'Private key' : 'Value'}<Info text={kind === 'ssh' ? 'The whole private key, -----BEGIN … to -----END … PRIVATE KEY-----, without a passphrase (ideally a key made just for this). Leave empty when editing to keep it.' : 'Leave empty when editing to keep the saved value.'} /></span>
        {kind === 'ssh'
          ? <textarea autoComplete="off" spellCheck={false} rows={4} value={value} onChange={e => setValue(e.target.value)} placeholder={existing ? '(unchanged)' : '-----BEGIN OPENSSH PRIVATE KEY-----'} data-secret-value-input {...stylex.props(s.input, s.mono, s.keyBox)} />
          : <input type="password" autoComplete="off" value={value} onChange={e => setValue(e.target.value)} placeholder={existing ? '(unchanged)' : ''} data-secret-value-input {...stylex.props(s.input, s.mono)} />}</label>
      <label {...stylex.props(s.field)}><span {...stylex.props(s.label)}>What it's for</span>
        <input value={description} onChange={e => setDescription(e.target.value)} placeholder="Push to my GitHub repos" data-secret-desc {...stylex.props(s.input)} /></label>
      <label {...stylex.props(s.field)}><span {...stylex.props(s.label)}>Hosts<Info text={kind === 'ssh'
          ? 'Where this key may connect: [user@]host[:port], comma or space separated (port 22 by default), e.g. deploy@192.168.1.47, git@github.com. A LAN address works when you name it exactly.'
          : 'The only hosts a command using it can reach, over HTTPS (comma or space separated; *.example.com covers its subdomains; an IP address like 192.168.1.47 works). Empty: no network.'} /></span>
        <input value={hosts} onChange={e => setHosts(e.target.value)} placeholder={kind === 'ssh' ? 'deploy@192.168.1.47, git@github.com' : 'github.com, api.github.com'} data-secret-hosts {...stylex.props(s.input, s.mono)} /></label>
      <div {...stylex.props(s.actions)}>
        <Button onClick={onDone}>Cancel</Button>
        <Button data-secret-save onClick={save}>Save</Button>
      </div>
    </section>
  );
}

const s = stylex.create({
  root: { display: 'flex', flexDirection: 'column', gap: 8, fontSize: 11 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5, lineHeight: 1.5 },
  row: { display: 'flex', flexDirection: 'column', gap: 6, paddingBlock: 8, paddingInline: 10, borderRadius: 5, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, backgroundColor: colors.sunken },
  form: { borderColor: colors.line },
  head: { display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 },
  name: { fontFamily: fonts.mono, fontSize: 11, color: colors.textStrong },
  desc: { flexGrow: 1, minWidth: 0, overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis', color: colors.textMuted },
  meta: { fontSize: 10, color: colors.textFaint, display: 'flex', gap: 5, flexWrap: 'wrap', alignItems: 'center' },
  host: { fontFamily: fonts.mono, fontSize: 10, color: colors.textSoft, backgroundColor: colors.card, paddingInline: 4, borderRadius: 3 },
  inline: { display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' },
  input: { minWidth: 0, flexGrow: 1, backgroundColor: colors.card, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 7, fontSize: 11, fontFamily: 'inherit' },
  mono: { fontFamily: fonts.mono },
  keyBox: { resize: 'vertical', minHeight: 70, fontSize: 10, lineHeight: 1.4 },
  kind: { flexShrink: 0, fontSize: 9, paddingInline: 5, paddingBlock: 1, borderRadius: 3, color: colors.textSoft, backgroundColor: colors.card },
  valueBlock: { whiteSpace: 'pre', overflow: 'auto', maxHeight: 160, fontSize: 10 },
  value: { fontFamily: fonts.mono, fontSize: 11, color: colors.textStrong, backgroundColor: colors.card, paddingBlock: 4, paddingInline: 6, borderRadius: 3, overflowWrap: 'anywhere', userSelect: 'all', flexGrow: 1 },
  grants: { display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' },
  label: { color: colors.textFaint, fontSize: 10 },
  chip: { display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10, paddingInline: 6, paddingBlock: 1, borderRadius: 9, backgroundColor: colors.card, color: colors.textSoft },
  chipTrusted: { backgroundColor: colors.brandSoft, color: colors.brand },
  x: { borderWidth: 0, backgroundColor: 'transparent', color: 'inherit', cursor: 'pointer', padding: 0, fontSize: 11 },
  field: { display: 'grid', gridTemplateColumns: '110px 1fr', alignItems: 'center', gap: 8 },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 8 },
});
