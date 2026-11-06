// The sign-in screen, shown instead of the app until this browser has a
// session. On first run it sets the passphrase; after a server restart the
// first sign-in also unlocks the API keys, and agents resume.
import { useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors, fonts } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';

export interface WorkspaceCard { id: string; name: string; state: 'setup' | 'locked' | 'unlocked' }
export interface AuthState {
  setup: boolean; // the server has no passphrase yet (first run)
  workspaces: WorkspaceCard[];
  signedIn: boolean;
  workspace?: string; // the session's workspace
  workspaceName?: string;
  state: 'setup' | 'locked' | 'unlocked';
  minLength: number;
}

// authState asks the server whether this browser is signed in; null when
// there is no Xhaos server (npm run dev), so the prototype runs as before.
export async function authState(): Promise<AuthState | null> {
  try {
    const res = await fetch('/xhaos/auth', { cache: 'no-store' });
    if (!res.ok || !(res.headers.get('content-type') || '').includes('json')) return null;
    return await res.json();
  } catch { return null; }
}

// authPost returns null on success, else a message to show. It never hangs:
// after 20 seconds it gives up, and network errors become messages.
export async function authPost(path: string, body: Record<string, string>): Promise<string | null> {
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), 20_000);
  try {
    const res = await fetch(`/xhaos/auth/${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal: abort.signal });
    if (res.ok) return null;
    try { return (await res.json()).error || res.statusText; } catch { return res.statusText || `error ${res.status}`; }
  } catch (error) {
    return abort.signal.aborted ? 'the server did not answer in 20 seconds' : `could not reach the server (${error instanceof Error ? error.message : String(error)})`;
  } finally { clearTimeout(timer); }
}

// Each workspace's colour tints its whole card. Colours go in creation
// order (the list's order), so the first eight differ and none changes when a
// workspace is added. Hues that read on both dark and light pages.
const hues = [262, 210, 150, 35, 330, 185, 10, 95];
export const workspaceHue = (index: number) => hues[index % hues.length];

// The picker: one card per workspace, like profiles. Choosing one asks for
// its passphrase (each workspace has its own, and its own keys); New
// workspace makes one, with the passphrase of an existing workspace to
// prove you may. The first run sets up the first workspace instead.
export function SignIn({ auth }: { auth: AuthState }) {
  const [picked, setPicked] = useState<WorkspaceCard | 'new' | null>(null);
  if (auth.setup) return <Page><Setup auth={auth} /></Page>;
  if (picked === 'new') return <Page><Create auth={auth} back={() => setPicked(null)} /></Page>;
  if (picked) return <Page><Enter auth={auth} card={picked} back={() => setPicked(null)} /></Page>;
  return (
    <Page wide>
      <h1 {...stylex.props(s.heading)}>Which workspace?</h1>
      <div data-workspace-cards {...stylex.props(s.cards)}>
        {auth.workspaces.map((w, index) => (
          <button key={w.id} type="button" data-workspace-card={w.id} onClick={() => setPicked(w)} {...stylex.props(s.tile, s.tinted(workspaceHue(index)))}>
            <span aria-hidden="true" {...stylex.props(s.initial)}>{w.name.trim().slice(0, 1).toUpperCase() || '?'}</span>
            <span {...stylex.props(s.tileName)}>{w.name}</span>
            {w.state === 'locked' && <small {...stylex.props(s.tileState)}>locked</small>}
          </button>
        ))}
        <button type="button" data-workspace-new onClick={() => setPicked('new')} {...stylex.props(s.tile, s.tileNew)}>
          <span aria-hidden="true" {...stylex.props(s.initial, s.initialNew)}>+</span>
          <span {...stylex.props(s.tileName)}>New workspace</span>
        </button>
      </div>
    </Page>
  );
}

function Page({ children, wide = false }: { children: React.ReactNode; wide?: boolean }) {
  return <main id="sign-in" {...stylex.props(s.page)}><div {...stylex.props(s.frame, wide && s.frameWide)}>{children}</div></main>;
}

function useSubmit(run: () => Promise<string | null>) {
  const [error, setError] = useState(''), [busy, setBusy] = useState(false);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (busy) return; // one attempt at a time
    setBusy(true); setError('');
    const failed = await run();
    setBusy(false);
    if (failed) { setError(failed[0].toUpperCase() + failed.slice(1) + (/[.!?]$/.test(failed) ? '' : '.')); return; }
    location.reload();
  };
  return { error, setError, busy, submit };
}

// Enter: the passphrase of one workspace. The workspace's name is the
// form's username, so a password manager keeps one entry per workspace.
function Enter({ auth, card, back }: { auth: AuthState; card: WorkspaceCard; back?: () => void }) {
  const [pass, setPass] = useState('');
  const { error, busy, submit } = useSubmit(() => authPost('signin', { workspace: card.id, passphrase: pass }));
  return (
    <form onSubmit={submit} data-sign-in-form={card.id} {...stylex.props(s.card)}>
      <h1 {...stylex.props(s.title)}>{card.name}</h1>
      <p {...stylex.props(s.text)}>{card.state === 'locked'
        ? 'This workspace is locked since the server started: its keys are encrypted until someone signs in, and its agents wait. Sign in to unlock it.'
        : 'Sign in with this workspace\'s passphrase.'}</p>
      <input type="text" name="username" value={card.name} autoComplete="username" readOnly {...stylex.props(s.hiddenField)} aria-hidden="true" tabIndex={-1} />
      <input id="passphrase" type="password" name="password" autoFocus value={pass} onChange={e => setPass(e.target.value)} placeholder="Passphrase" autoComplete="current-password" {...stylex.props(s.input)} />
      {error && <p id="sign-in-error" role="alert" {...stylex.props(s.error)}>{error}</p>}
      <div {...stylex.props(s.actions)}>
        {back && <Button type="button" id="sign-in-back" onClick={back}>Other workspaces</Button>}
        <Button id="sign-in-submit" type="submit" variant="allow" disabled={busy || !pass}>{busy ? 'Checking…' : 'Sign in'}</Button>
      </div>
    </form>
  );
}

// Setup: the first run names the first workspace and sets its passphrase.
function Setup({ auth }: { auth: AuthState }) {
  const [name, setName] = useState(auth.workspaces[0]?.name === 'Default' ? '' : auth.workspaces[0]?.name || ''), [pass, setPass] = useState(''), [again, setAgain] = useState('');
  const { error, setError, busy, submit } = useSubmit(async () => {
    if (pass.length < auth.minLength) return `use at least ${auth.minLength} characters`;
    if (pass !== again) return 'the two passphrases differ';
    return authPost('setup', { name: name.trim() || 'Default', passphrase: pass });
  });
  void setError;
  return (
    <form onSubmit={submit} {...stylex.props(s.card)}>
      <h1 {...stylex.props(s.title)}>Xhaos</h1>
      <p {...stylex.props(s.text)}>Name your first workspace and set its passphrase. The passphrase signs browsers in to it and encrypts its API keys and secrets; the server keeps only a wrapped key. There is no recovery: if you lose it, you re-enter that workspace's keys.</p>
      <input id="workspace-name" value={name} onChange={e => setName(e.target.value)} placeholder="Workspace name (e.g. Personal)" autoComplete="username" {...stylex.props(s.input)} />
      <input id="passphrase" type="password" autoFocus value={pass} onChange={e => setPass(e.target.value)} placeholder="Passphrase" autoComplete="new-password" {...stylex.props(s.input)} />
      <input id="passphrase-again" type="password" value={again} onChange={e => setAgain(e.target.value)} placeholder="Passphrase again" autoComplete="new-password" {...stylex.props(s.input)} />
      {error && <p id="sign-in-error" role="alert" {...stylex.props(s.error)}>{error}</p>}
      <div {...stylex.props(s.actions)}><Button id="sign-in-submit" type="submit" variant="allow" disabled={busy}>{busy ? 'Checking…' : 'Set passphrase'}</Button></div>
    </form>
  );
}

// Create: a new workspace, its own passphrase, and the passphrase of an
// existing workspace (so a stranger who reaches the page cannot make one).
function Create({ auth, back }: { auth: AuthState; back: () => void }) {
  const [name, setName] = useState(''), [pass, setPass] = useState(''), [again, setAgain] = useState(''), [authorizer, setAuthorizer] = useState('');
  const { error, busy, submit } = useSubmit(async () => {
    if (!name.trim()) return 'give the workspace a name';
    if (pass.length < auth.minLength) return `use at least ${auth.minLength} characters`;
    if (pass !== again) return 'the two passphrases differ';
    return authPost('create', { name: name.trim(), passphrase: pass, authorizer });
  });
  return (
    <form onSubmit={submit} data-create-workspace {...stylex.props(s.card)}>
      <h1 {...stylex.props(s.title)}>New workspace</h1>
      <p {...stylex.props(s.text)}>It gets its own passphrase, its own keys (models, MCP, secrets) and its own agents, projects and channels.</p>
      <input id="workspace-name" value={name} onChange={e => setName(e.target.value)} placeholder="Name" autoComplete="username" autoFocus {...stylex.props(s.input)} />
      <input id="new-passphrase" type="password" value={pass} onChange={e => setPass(e.target.value)} placeholder="Its passphrase" autoComplete="new-password" {...stylex.props(s.input)} />
      <input id="new-passphrase-again" type="password" value={again} onChange={e => setAgain(e.target.value)} placeholder="Its passphrase again" autoComplete="new-password" {...stylex.props(s.input)} />
      <label htmlFor="authorizer" {...stylex.props(s.text)}>To show you may: the passphrase of any existing workspace.</label>
      <input id="authorizer" type="password" value={authorizer} onChange={e => setAuthorizer(e.target.value)} placeholder="An existing workspace's passphrase" autoComplete="off" {...stylex.props(s.input)} />
      {error && <p id="sign-in-error" role="alert" {...stylex.props(s.error)}>{error}</p>}
      <div {...stylex.props(s.actions)}>
        <Button type="button" id="sign-in-back" onClick={back}>Cancel</Button>
        <Button id="create-submit" type="submit" variant="allow" disabled={busy}>{busy ? 'Creating…' : 'Create workspace'}</Button>
      </div>
    </form>
  );
}

const s = stylex.create({
  // The page is zoomed by the text size setting (#app): divide, or it overflows.
  // The code font (JetBrains Mono by default), like the transcripts.
  page: { fontFamily: fonts.mono, minHeight: 'calc(100dvh / var(--x-zoom, 1))', boxSizing: 'border-box', display: 'grid', placeItems: 'center', backgroundColor: colors.bg, color: colors.text, paddingInline: 16 },
  frame: { width: '100%', maxWidth: 360 },
  frameWide: { maxWidth: 720 },
  heading: { margin: 0, marginBottom: 20, textAlign: 'center', fontSize: 18, fontWeight: 500, color: colors.textStrong },
  cards: { display: 'flex', flexWrap: 'wrap', justifyContent: 'center', gap: 16 },
  tile: { display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6, width: 136, paddingBlock: 20, paddingInline: 10, borderWidth: 1, borderStyle: 'solid', borderColor: { default: colors.lineFaint, ':focus-visible': colors.brand }, borderRadius: 10, backgroundColor: { default: colors.card, ':hover': colors.hover }, color: colors.text, cursor: 'pointer', fontFamily: 'inherit', outline: 'none', transitionProperty: 'transform, filter', transitionDuration: '.12s', transform: { default: 'none', ':hover': 'translateY(-2px)' }, filter: { default: 'none', ':hover': 'brightness(1.12)' } },
  // The workspace's colour over the whole card.
  tinted: (hue: number) => ({ backgroundColor: `hsl(${hue} 55% 50% / 0.16)`, borderColor: `hsl(${hue} 60% 60% / 0.45)`, color: `light-dark(hsl(${hue} 60% 32%), hsl(${hue} 70% 78%))` }),
  tileNew: { borderStyle: 'dashed' },
  initial: { display: 'grid', placeItems: 'center', width: 56, height: 56, fontSize: 40, fontWeight: 600, lineHeight: 1 },
  initialNew: { width: 48, height: 48, marginBlock: 4, fontSize: 26, fontWeight: 400, color: colors.textFaint, borderWidth: 1, borderStyle: 'dashed', borderColor: colors.lineStrong, borderRadius: 10 },
  tileName: { maxWidth: '100%', overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis', fontSize: 12, fontWeight: 500 },
  tileState: { fontSize: 9.5, opacity: 0.7 },
  actions: { display: 'flex', justifyContent: 'flex-end', gap: 8 },
  // Present for password managers (the workspace is the username), not shown.
  hiddenField: { position: 'absolute', width: 1, height: 1, opacity: 0, pointerEvents: 'none' },
  card: { width: '100%', maxWidth: 360, display: 'flex', flexDirection: 'column', gap: 10, backgroundColor: colors.card, borderRadius: 6, padding: 24, fontSize: 12 },
  title: { margin: 0, fontSize: 16, color: colors.textStrong, fontWeight: 600 },
  text: { margin: 0, color: colors.textMuted, lineHeight: 1.5 },
  input: { backgroundColor: colors.sunken, borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, borderRadius: 3, color: colors.text, paddingBlock: 8, paddingInline: 10, fontSize: 12, fontFamily: 'inherit' },
  error: { margin: 0, color: colors.waiting },
});
