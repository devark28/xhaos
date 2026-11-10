// The Nodes tab: machines that joined this server (docs/nodes.md). One card
// per node, with its state, what it is, the agents allowed to use it, its
// jobs, and its own actions. Add node makes a card and a one-time join code;
// the command (or a Raspberry Pi's first-boot files) shows once, on the card.
import { useState, type FormEvent, type ReactNode } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors, fonts } from './theme/tokens.stylex.ts';
import { Button, StatusDot, Info } from './kit.tsx';
import { Toggle } from './AppearanceSettings.tsx';
import { ConfirmDialog } from './Confirm.tsx';
import { formatTime } from './appearance.ts';
import { activeAgents } from '../model.ts';
import { useApp, workspaceOf, toast } from './state/store.ts';
import { openPanel } from './state/panels.ts';
import { connected } from './state/backend.ts';
import {
  addNode, newNodeCode, updateNode, removeNode, grantNode, stopNodeJob, nextNodeName, labelsOf,
  type NodeAdded, type NodeView, type NodeJob, type PiSetup,
} from './state/nodes.ts';

// AddNodeButton is the tab's heading action. It is off until the server has
// a public address for nodes (-node-url), and says why.
export function AddNodeButton() {
  const list = useApp(s => s.nodes);
  const ready = Boolean(list?.url);
  return <Button id="add-node" disabled={!ready} title={ready ? 'Add a machine as a node' : list?.note ?? 'Loading…'}
    onClick={() => openPanel('nodes', { editId: 'new' })}>Add node</Button>;
}

export function Nodes({ adding }: { adding: boolean }) {
  const list = useApp(s => s.nodes);
  // Join codes just made, by node: shown once, on the node's card.
  const [issued, setIssued] = useState<Record<number, NodeAdded>>({});
  const show = (out: NodeAdded) => setIssued(all => ({ ...all, [out.node.id]: out }));
  const hide = (id: number) => setIssued(all => { const next = { ...all }; delete next[id]; return next; });
  if (!connected()) return <p {...stylex.props(s.muted)}>Nodes need the Xhaos server.</p>;
  if (!list) return <p {...stylex.props(s.muted)}>Loading…</p>;
  return (
    <div data-nodes {...stylex.props(s.root)}>
      {list.note && <p data-nodes-note {...stylex.props(s.note)}>{list.note}</p>}
      {adding && list.url && <AddForm names={list.nodes} onAdded={out => { show(out); openPanel('nodes'); }} onCancel={() => openPanel('nodes')} />}
      {list.nodes.length === 0 && !adding && (
        <p {...stylex.props(s.muted)}>No nodes yet. A node is a machine (a build PC, a Raspberry Pi) whose commands and files your agents can use. It connects out to this server, so it needs no open port.</p>
      )}
      {list.nodes.map(n => <NodeCard key={n.id} n={n} issued={issued[n.id]} onIssued={show} onDone={() => hide(n.id)} />)}
    </div>
  );
}

// ---- adding ----------------------------------------------------------------

function AddForm({ names, onAdded, onCancel }: { names: NodeView[]; onAdded(out: NodeAdded): void; onCancel(): void }) {
  const [name, setName] = useState(() => nextNodeName(names));
  const [labels, setLabels] = useState('');
  const [pi, setPi] = useState(false);
  const [setup, setSetup] = useState<PiSetup>(blankPi);
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    const out = await addNode(name.trim(), labelsOf(labels), pi ? setup : undefined);
    setBusy(false);
    if (out) onAdded(out);
  };
  return (
    <form data-node-add onSubmit={submit} {...stylex.props(s.card, s.form)}>
      <Field label="Name"><input autoFocus value={name} onChange={e => setName(e.target.value)} data-node-name-input {...stylex.props(s.input)} /></Field>
      <Field label="Labels" hint="Words agents can pick a node by, comma or space separated, like gpu, arm64, camera.">
        <input value={labels} onChange={e => setLabels(e.target.value)} placeholder="gpu, camera" data-node-labels-input {...stylex.props(s.input)} />
      </Field>
      <Toggle name="node-pi" label="Raspberry Pi first-boot files" on={pi} onChange={setPi}
        hint="For a Pi with a fresh card: files for the card's boot partition that set up its user, SSH, Wi-Fi and this node on first boot. Needs Raspberry Pi OS (64-bit) from November 2025 or later (Trixie, cloud-init)." />
      {pi && <PiFields setup={setup} onChange={setSetup} />}
      <div {...stylex.props(s.actions)}>
        <Button onClick={onCancel}>Cancel</Button>
        <Button type="submit" data-node-add-submit disabled={busy || !name.trim()} sx={busy && s.pulse}>{busy ? 'Adding…' : 'Add'}</Button>
      </div>
    </form>
  );
}

const blankPi: PiSetup = { hostname: '', user: 'pi', sshKey: '', wifiSsid: '', wifiPassword: '', country: '', usbConfig: false };

function PiFields({ setup, onChange }: { setup: PiSetup; onChange(next: PiSetup): void }) {
  const set = (key: Exclude<keyof PiSetup, 'usbConfig'>) => (e: { target: { value: string } }) => onChange({ ...setup, [key]: e.target.value });
  return <>
    <Field label="Hostname"><input value={setup.hostname} onChange={set('hostname')} placeholder="pi5" data-pi-hostname {...stylex.props(s.input, s.mono)} /></Field>
    <Field label="User" hint="Your login on the Pi. It has no password: sign in with the SSH key below."><input value={setup.user} onChange={set('user')} data-pi-user {...stylex.props(s.input, s.mono)} /></Field>
    <Field label="SSH public key" hint="Optional: one line, ssh-ed25519 AAAA… comment. Without it you cannot sign in to the Pi; the node still works.">
      <input value={setup.sshKey} onChange={set('sshKey')} placeholder="ssh-ed25519 AAAA…" spellCheck={false} data-pi-ssh {...stylex.props(s.input, s.mono)} />
    </Field>
    <Field label="Wi-Fi name" hint="Leave empty for wired Ethernet."><input value={setup.wifiSsid} onChange={set('wifiSsid')} autoComplete="off" data-pi-ssid {...stylex.props(s.input)} /></Field>
    {setup.wifiSsid && <>
      <Field label="Wi-Fi password" hint="Written into the files in plain text; not kept by Xhaos."><input type="password" autoComplete="off" value={setup.wifiPassword} onChange={set('wifiPassword')} data-pi-pass {...stylex.props(s.input)} /></Field>
      <Field label="Country" hint="Where the Pi is used, as a two-letter code (GB, RW, US): Wi-Fi channels depend on it."><input value={setup.country} onChange={set('country')} maxLength={2} placeholder="RW" data-pi-country {...stylex.props(s.input, s.mono, s.short)} /></Field>
    </>}
    <Toggle name="pi-usb-config" label="USB stick settings" on={setup.usbConfig} onChange={on => onChange({ ...setup, usbConfig: on })}
      hint="Lets a USB stick labelled XHAOS give the Pi a new Wi-Fi network or join code later, with no screen or SSH. Anyone who can plug a stick labelled XHAOS into the Pi can then change its Wi-Fi and move it to another server. Off: change them over SSH." />
  </>;
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return <label {...stylex.props(s.field)}><span {...stylex.props(s.label)}>{label}{hint && <Info text={hint} />}</span>{children}</label>;
}

// ---- a node's card ---------------------------------------------------------

const stateText = (n: NodeView) => {
  if (n.state === 'online') return 'online';
  if (n.state === 'waiting') return n.codeExpires && Date.parse(n.codeExpires) > Date.now() ? `waiting to join · code expires ${formatTime(n.codeExpires, 'exact')}` : 'waiting to join · no live code';
  return n.lastSeen ? `offline · last seen ${formatTime(n.lastSeen, 'relative')}` : 'offline';
};
const dot = { online: 'running', waiting: 'waiting', offline: 'idle' } as const;

function NodeCard({ n, issued, onIssued, onDone }: { n: NodeView; issued?: NodeAdded; onIssued(out: NodeAdded): void; onDone(): void }) {
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(n.name), [labels, setLabels] = useState(n.labels.join(', '));
  const [piForm, setPiForm] = useState(false), [setup, setSetup] = useState<PiSetup>(blankPi);
  const [asking, setAsking] = useState(false), [busy, setBusy] = useState<'' | 'code' | 'remove'>('');
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (await updateNode(n.id, name.trim(), labelsOf(labels))) setEditing(false);
  };
  const code = async (pi?: PiSetup) => {
    setBusy('code');
    const out = await newNodeCode(n.id, pi);
    setBusy('');
    if (out) { onIssued(out); setPiForm(false); }
  };
  const remove = async () => {
    setBusy('remove');
    await removeNode(n.id);
    setBusy(''); setAsking(false);
  };
  return (
    <section data-node={n.id} data-node-state={n.state} {...stylex.props(s.card, n.state === 'online' && s.cardOnline)}>
      {editing ? (
        <form onSubmit={save} {...stylex.props(s.form)}>
          <Field label="Name"><input autoFocus value={name} onChange={e => setName(e.target.value)} onKeyDown={e => { if (e.key === 'Escape') setEditing(false); }} data-node-rename {...stylex.props(s.input)} /></Field>
          <Field label="Labels"><input value={labels} onChange={e => setLabels(e.target.value)} data-node-relabel {...stylex.props(s.input)} /></Field>
          <div {...stylex.props(s.actions)}>
            <Button onClick={() => setEditing(false)}>Cancel</Button>
            <Button type="submit" data-node-save disabled={!name.trim()}>Save</Button>
          </div>
        </form>
      ) : (
        <div {...stylex.props(s.head)}>
          <StatusDot status={dot[n.state]} label={n.state} />
          <span data-node-name {...stylex.props(s.name)}>{n.name}</span>
          <span {...stylex.props(s.state)}>{stateText(n)}</span>
        </div>
      )}
      {n.labels.length > 0 && !editing && <div {...stylex.props(s.chips)}>{n.labels.map(l => <span key={l} data-node-label={l} {...stylex.props(s.chip)}>{l}</span>)}</div>}
      {n.facts && <Facts n={n} />}
      {issued && <JoinCode out={issued} onDone={onDone} />}
      {piForm && !issued && (
        <div data-node-pi {...stylex.props(s.form, s.divided)}>
          <PiFields setup={setup} onChange={setSetup} />
          <div {...stylex.props(s.actions)}>
            <Button onClick={() => setPiForm(false)}>Cancel</Button>
            <Button data-node-pi-make disabled={busy === 'code'} sx={busy === 'code' && s.pulse} onClick={() => void code(setup)}>Make the files</Button>
          </div>
        </div>
      )}
      <Grants n={n} />
      {n.jobs.length > 0 && <Jobs n={n} />}
      {!editing && (
        <div {...stylex.props(s.actions, s.cardActions)}>
          <Button data-node-edit={n.id} onClick={() => { setName(n.name); setLabels(n.labels.join(', ')); setEditing(true); }}>Rename</Button>
          {n.state === 'waiting' && <Button data-node-code={n.id} disabled={busy === 'code'} sx={busy === 'code' && !piForm && s.pulse} title="A new one-time join code (the old one stops working)" onClick={() => void code()}>New code</Button>}
          {n.state === 'waiting' && <Button data-node-pi-files={n.id} title="Raspberry Pi first-boot files, with a new join code" onClick={() => setPiForm(!piForm)}>Pi files</Button>}
          <Button data-node-remove={n.id} onClick={() => setAsking(true)}>Remove</Button>
        </div>
      )}
      <ConfirmDialog open={asking} action="Remove" busy={busy === 'remove'} onConfirm={() => void remove()} onCancel={() => setAsking(false)}
        text={`Remove ${n.name}? Its agents lose it, its connection closes, and it can no longer join with its key. Running jobs on it keep running until the machine stops them.`} />
    </section>
  );
}

function Facts({ n }: { n: NodeView }) {
  const f = n.facts!;
  const what = [f.model || `${f.os}/${f.arch}`, f.distro, `${f.cpus} CPU${f.cpus === 1 ? '' : 's'}`, f.memoryMB ? `${(f.memoryMB / 1024).toFixed(f.memoryMB < 10240 ? 1 : 0)} GB` : '', `xhaos-node ${f.version}`].filter(Boolean);
  const sandbox = f.unsandboxed
    ? { text: 'unsandboxed', warn: true, tip: 'Its owner ran it with --unsandboxed: commands run as the xhaos-node user without Landlock, and can reach everything that user can.' }
    : f.landlock === 0
      ? { text: 'no Landlock: runs no commands', warn: true, tip: 'This kernel has no Landlock, so the node refuses commands. Its owner can allow them unconfined with install-node.sh --unsandboxed.' }
      : { text: `Landlock ${f.landlock}`, warn: false, tip: `Commands run inside ${f.root}, confined by Landlock.` };
  return (
    <div data-node-facts {...stylex.props(s.facts)}>
      <span>{what.join(' · ')}</span>
      <span {...stylex.props(s.factLine)}>
        <code {...stylex.props(s.code)}>{f.root}</code>
        <span data-node-sandbox {...stylex.props(sandbox.warn && s.warn)}>{sandbox.text}</span><Info text={sandbox.tip} />
      </span>
      {((f.devices?.length ?? 0) > 0 || (f.hardware?.length ?? 0) > 0) && (
        <span {...stylex.props(s.factLine)}>
          {f.devices?.map(d => <code key={d} title="Commands may open it" {...stylex.props(s.code, s.allowed)}>{d}</code>)}
          {f.hardware?.filter(h => !f.devices?.includes(h)).map(h => <code key={h} title="Seen, not opened to commands (install-node.sh --device)" {...stylex.props(s.code)}>{h}</code>)}
        </span>
      )}
    </div>
  );
}

// Grants: every agent is a chip; pressed means it may use this node.
function Grants({ n }: { n: NodeView }) {
  const agents = activeAgents(useApp(st => workspaceOf(st).agents)).filter(a => !a.sample && a.status !== 'terminated');
  const known = new Set(agents.map(a => a.id));
  const gone = n.grants.filter(id => !known.has(id));
  return (
    <div data-node-grants {...stylex.props(s.chips)}>
      <span {...stylex.props(s.label)}>Agents<Info text="Agents allowed to use this node: run commands there and copy files to and from it. Each command is approved like the agent's other commands. None by default." /></span>
      {agents.length === 0 && gone.length === 0 && <span {...stylex.props(s.muted)}>no agents yet</span>}
      {agents.map(a => {
        const on = n.grants.includes(a.id);
        return (
          <button key={a.id} type="button" aria-pressed={on} data-node-grant={a.id} title={on ? `Revoke from #${a.id}` : `Allow #${a.id} ${a.title}`}
            onClick={() => void grantNode(n.id, a.id, !on)} {...stylex.props(s.chip, s.toggle, on && s.chipOn)}>
            #{a.id} {a.title.slice(0, 24)}
          </button>
        );
      })}
      {gone.map(id => (
        <button key={id} type="button" aria-pressed data-node-grant={id} title={`Revoke from #${id}`} onClick={() => void grantNode(n.id, id, false)} {...stylex.props(s.chip, s.toggle, s.chipOn)}>#{id} ×</button>
      ))}
    </div>
  );
}

function Jobs({ n }: { n: NodeView }) {
  const [stopping, setStopping] = useState<string | null>(null);
  const stop = async (j: NodeJob) => { setStopping(j.id); await stopNodeJob(n.id, j.id); setStopping(null); };
  const jobs = [...n.jobs].sort((a, b) => Number(b.running) - Number(a.running) || b.started - a.started).slice(0, 8);
  return (
    <div data-node-jobs {...stylex.props(s.jobs)}>
      <span {...stylex.props(s.label)}>Jobs</span>
      {jobs.map(j => (
        <div key={j.id} data-node-job={j.id} {...stylex.props(s.job)}>
          <StatusDot status={j.running ? 'running' : j.exit === 0 && !j.signal ? 'complete' : 'idle'} size={6} />
          <code title={j.command} {...stylex.props(s.jobCommand)}>{j.command}</code>
          <span {...stylex.props(s.muted)}>{j.running ? formatTime(new Date(j.started).toISOString(), 'relative') : j.signal ? j.signal : `exit ${j.exit}`}</span>
          {j.running && <Button data-node-job-stop={j.id} disabled={stopping === j.id} sx={stopping === j.id && s.pulse} onClick={() => void stop(j)}>Stop</Button>}
        </div>
      ))}
    </div>
  );
}

// ---- a join code, shown once -----------------------------------------------

const copy = (text: string) => {
  if (!navigator.clipboard) { toast('Copying needs HTTPS here; select the text instead.'); return; }
  void navigator.clipboard.writeText(text).then(() => toast('Copied.'));
};
const download = (name: string, content: string) => {
  const url = URL.createObjectURL(new Blob([content], { type: 'text/plain' }));
  const a = Object.assign(document.createElement('a'), { href: url, download: name });
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
};

function JoinCode({ out, onDone }: { out: NodeAdded; onDone(): void }) {
  return (
    <div data-node-join {...stylex.props(s.inset, s.form)}>
      {out.files?.length ? <>
        <span>Copy these files to the card's boot partition (<code {...stylex.props(s.code)}>bootfs</code>), replacing the ones there, after flashing Raspberry Pi OS Lite (64-bit). On first boot the Pi joins as {out.node.name}.</span>
        <div {...stylex.props(s.chips)}>
          {out.files.map(f => <Button key={f.name} data-node-file={f.name} onClick={() => download(f.name, f.content)}>Download {f.name}</Button>)}
        </div>
        <span>Or, on a Linux computer with the card in a reader, after downloading both files: this writes Raspberry Pi OS and the files in one go (you pick the card and type its name to erase it).</span>
        <code data-node-flash-command {...stylex.props(s.command)}>{out.command}</code>
        <div {...stylex.props(s.chips)}><Button data-node-copy onClick={() => copy(out.command)}>Copy command</Button></div>
        {out.warning && <span data-node-warning {...stylex.props(s.warn)}>{out.warning}</span>}
      </> : <>
        <span>Run this on the machine (Linux, amd64 or arm64). It installs xhaos-node as a service of its own user and joins as {out.node.name}.</span>
        <code data-node-command {...stylex.props(s.command)}>{out.command}</code>
        <div {...stylex.props(s.chips)}><Button data-node-copy onClick={() => copy(out.command)}>Copy command</Button></div>
      </>}
      <span {...stylex.props(s.muted)}>
        Code <code data-node-code-value {...stylex.props(s.code)}>{out.code}</code> works once, until {formatTime(out.expires, 'exact')}. It is shown only now; New code makes another.
      </span>
      <div {...stylex.props(s.actions)}><Button data-node-join-done onClick={onDone}>Done</Button></div>
    </div>
  );
}

const pulse = stylex.keyframes({ '0%': { opacity: 1 }, '50%': { opacity: 0.45 }, '100%': { opacity: 1 } });

const s = stylex.create({
  root: { display: 'flex', flexDirection: 'column', gap: 8, fontSize: 11 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5, lineHeight: 1.5 },
  note: { margin: 0, paddingBlock: 6, paddingInline: 8, borderRadius: 4, backgroundColor: colors.sunken, color: colors.textMuted, fontSize: 10.5, lineHeight: 1.5 },
  card: { display: 'flex', flexDirection: 'column', gap: 6, paddingBlock: 8, paddingInline: 10, borderRadius: 5, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, backgroundColor: colors.sunken, minWidth: 0 },
  cardOnline: { borderColor: colors.line },
  form: { display: 'flex', flexDirection: 'column', gap: 6 },
  divided: { paddingTop: 8, borderTopWidth: 1, borderTopStyle: 'solid', borderTopColor: colors.lineFaint },
  inset: { paddingBlock: 8, paddingInline: 8, borderRadius: 4, backgroundColor: colors.card, lineHeight: 1.5 },
  head: { display: 'flex', alignItems: 'center', gap: 7, minWidth: 0 },
  name: { fontSize: 12, fontWeight: 500, color: colors.textStrong, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' },
  state: { marginLeft: 'auto', flexShrink: 0, fontSize: 10, color: colors.textFaint },
  facts: { display: 'flex', flexDirection: 'column', gap: 3, fontSize: 10, color: colors.textMuted },
  factLine: { display: 'flex', alignItems: 'center', gap: 5, flexWrap: 'wrap' },
  code: { fontFamily: fonts.mono, fontSize: 10, color: colors.textSoft, backgroundColor: colors.card, paddingInline: 4, borderRadius: 3, overflowWrap: 'anywhere' },
  allowed: { color: colors.brand },
  warn: { color: colors.warn },
  command: { display: 'block', fontFamily: fonts.mono, fontSize: 10, color: colors.textStrong, backgroundColor: colors.sunken, paddingBlock: 6, paddingInline: 7, borderRadius: 3, overflowWrap: 'anywhere', userSelect: 'all', lineHeight: 1.45 },
  chips: { display: 'flex', gap: 5, alignItems: 'center', flexWrap: 'wrap' },
  chip: { display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10, paddingInline: 6, paddingBlock: 1, borderRadius: 9, backgroundColor: colors.card, color: colors.textSoft, borderWidth: 0, fontFamily: 'inherit' },
  toggle: { cursor: 'pointer', opacity: { default: 0.75, ':hover': 1 } },
  chipOn: { backgroundColor: colors.brandSoft, color: colors.brand, opacity: 1 },
  label: { display: 'inline-flex', alignItems: 'center', color: colors.textFaint, fontSize: 10 },
  jobs: { display: 'flex', flexDirection: 'column', gap: 3 },
  job: { display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, fontSize: 10 },
  jobCommand: { flexGrow: 1, minWidth: 0, fontFamily: fonts.mono, fontSize: 10, color: colors.textSoft, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 8, flexWrap: 'wrap' },
  cardActions: { justifyContent: 'flex-start' },
  field: { display: 'grid', gridTemplateColumns: '110px 1fr', alignItems: 'center', gap: 8 },
  input: { minWidth: 0, flexGrow: 1, backgroundColor: colors.card, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 7, fontSize: 11, fontFamily: 'inherit' },
  mono: { fontFamily: fonts.mono },
  short: { maxWidth: 60 },
  pulse: { animationName: pulse, animationDuration: '1s', animationIterationCount: 'infinite' },
});
