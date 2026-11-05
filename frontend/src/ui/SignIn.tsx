// The sign-in screen, shown instead of the app until this browser has a
// session. On first run it sets the passphrase; after a server restart the
// first sign-in also unlocks the API keys, and agents resume.
import { useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';

export interface AuthState { state: 'setup' | 'locked' | 'unlocked'; signedIn: boolean; minLength: number }

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

export function SignIn({ auth }: { auth: AuthState }) {
  const setup = auth.state === 'setup';
  const [pass, setPass] = useState(''), [again, setAgain] = useState('');
  const [error, setError] = useState(''), [busy, setBusy] = useState(false);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (setup && pass.length < auth.minLength) return setError(`Use at least ${auth.minLength} characters.`);
    if (setup && pass !== again) return setError('The two passphrases differ.');
    if (busy) return; // one attempt at a time
    setBusy(true); setError('');
    const failed = await authPost(setup ? 'setup' : 'signin', { passphrase: pass });
    setBusy(false);
    if (failed) { setError(failed[0].toUpperCase() + failed.slice(1) + '.'); return; }
    location.reload();
  };
  return (
    <main id="sign-in" {...stylex.props(s.page)}>
      <form onSubmit={submit} {...stylex.props(s.card)}>
        <h1 {...stylex.props(s.title)}>Xhaos</h1>
        <p {...stylex.props(s.text)}>
          {setup
            ? 'Set a passphrase. It signs browsers in and encrypts your API keys and MCP secrets; the server keeps only a wrapped key. There is no recovery: if you lose it, you re-enter your keys.'
            : auth.state === 'locked'
              ? 'The server restarted, so your API keys are locked. Sign in to unlock them; waiting agents then resume.'
              : 'Sign in with the passphrase.'}
        </p>
        {/* A username lets password managers file the passphrase under Xhaos. */}
        <input type="text" name="username" value="xhaos" autoComplete="username" readOnly hidden />
        <input id="passphrase" type="password" autoFocus value={pass} onChange={e => setPass(e.target.value)} placeholder="Passphrase"
          autoComplete={setup ? 'new-password' : 'current-password'} {...stylex.props(s.input)} />
        {setup && <input id="passphrase-again" type="password" value={again} onChange={e => setAgain(e.target.value)} placeholder="Passphrase again" autoComplete="new-password" {...stylex.props(s.input)} />}
        {error && <p id="sign-in-error" role="alert" {...stylex.props(s.error)}>{error}</p>}
        <Button id="sign-in-submit" type="submit" variant="allow" disabled={busy} sx={s.submit}>{busy ? 'Checking…' : setup ? 'Set passphrase' : 'Sign in'}</Button>
      </form>
    </main>
  );
}

const s = stylex.create({
  page: { minHeight: '100vh', display: 'grid', placeItems: 'center', backgroundColor: colors.bg, color: colors.text, paddingInline: 16 },
  card: { width: '100%', maxWidth: 360, display: 'flex', flexDirection: 'column', gap: 10, backgroundColor: colors.card, borderRadius: 6, padding: 24, fontSize: 12 },
  title: { margin: 0, fontSize: 16, color: colors.textStrong, fontWeight: 600 },
  text: { margin: 0, color: colors.textMuted, lineHeight: 1.5 },
  input: { backgroundColor: colors.sunken, borderWidth: 1, borderStyle: 'solid', borderColor: colors.line, borderRadius: 3, color: colors.text, paddingBlock: 8, paddingInline: 10, fontSize: 12, fontFamily: 'inherit' },
  error: { margin: 0, color: colors.waiting },
  submit: { alignSelf: 'flex-end' },
});
