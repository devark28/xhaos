// Settings → Tools → Commands: the command classifier (server-wide). The
// switch is always shown: it turns off the built-in list of build, test and
// lint commands, and the optional local model's answers with it. The model
// status line appears only when a model server is configured (-classifier-url,
// set up by install.sh, which adds it by default).
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { call } from './state/backend.ts';
import { errorText } from './state/store.ts';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { Toggle } from './AppearanceSettings.tsx';

const HINT = 'Lets agents on auto review run ordinary build, test and lint commands (go test, npm test, make, pytest, ruff check...) without asking you. The list is fixed: no model, nothing new to install. Anything it does not list goes to the local model when one is set up, and asks otherwise; every dangerous command still asks (sudo, deletes, installs, publishes, network tools, anything outside the project). Commands with secrets are never listed. It can only make a command run, never stop one. Off: every command asks, as before. Only agents on auto review with read-write access use it; read-only and manual agents are unchanged. An allowed command’s card gives the reason.';

interface Health { ok: boolean; detail?: string }

export function ClassifierSettings() {
  const [on, setOn] = useState<boolean | null>(null);
  const [modelSet, setModelSet] = useState<boolean | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [checking, setChecking] = useState(false);
  const [note, setNote] = useState('');

  const check = () => {
    setChecking(true);
    call<Health>('classifier', 'Health').then(
      h => setHealth(h),
      error => setHealth({ ok: false, detail: errorText(error) }),
    ).finally(() => setChecking(false));
  };

  useEffect(() => {
    let current = true;
    call<boolean>('classifier', 'On').then(
      v => { if (current) setOn(v); },
      error => { if (current) { setOn(false); setNote(errorText(error)); } },
    );
    call<boolean>('classifier', 'Enabled').then(
      yes => {
        if (!current) return;
        setModelSet(yes);
        if (yes) check();
      },
      () => { if (current) setModelSet(false); },
    );
    return () => { current = false; };
    // check is stable for this mount.
  }, []);

  const set = (next: boolean) => {
    const previous = on;
    setNote('');
    setOn(next);
    call('classifier', 'SetOn', next).then(
      () => {},
      error => { setOn(previous); setNote(errorText(error)); },
    );
  };

  if (on === null) return null;
  return <div data-classifier-settings {...stylex.props(s.root)}>
    <Toggle name="classifier" label="Command classifier" hint={HINT} on={on} onChange={set} />
    {modelSet === true && <div {...stylex.props(s.status)}>
      <span data-classifier-health={health === null ? 'checking' : health.ok ? 'ok' : 'down'} {...stylex.props(s.muted)}>
        {health === null ? 'Model server: checking…' : health.ok ? 'Model server: running' : `Model server: not reachable${health.detail ? ` (${health.detail})` : ''}`}
      </span>
      <Button data-classifier-check disabled={checking} onClick={check}>{checking ? 'Checking…' : 'Check again'}</Button>
    </div>}
    {note && <p {...stylex.props(s.muted)}>{note}</p>}
  </div>;
}

const s = stylex.create({
  root: { display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 12, fontSize: 11 },
  status: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5, lineHeight: 1.5 },
});
