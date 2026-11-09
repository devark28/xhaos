// Settings → Past sessions: search the past sessions imported into this workspace
// (Claude Code transcripts, see -history-import), and the switch that lets
// agents search every project's history instead of their own project's.
import { useEffect, useState, type FormEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { call } from './state/backend.ts';
import { errorText, useApp } from './state/store.ts';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { Toggle } from './AppearanceSettings.tsx';
import { historySummary, resultLabel } from '../history-format.ts';

const pulse = stylex.keyframes({ '0%': { opacity: 1 }, '50%': { opacity: 0.55 }, '100%': { opacity: 1 } });

const HINT = 'Off: an agent searches the past sessions of its own project only. On: every agent in this workspace can search the past sessions of every project, and can name a project to search it. Turn it on only if you are happy for one project\'s agents to read another project\'s history.';

interface Health { ok: boolean; detail?: string }
interface Stats { chunks: number; scrubbed: number }
interface Result { id: number; projectName: string; session: string; at: string; role: string; excerpt: string }

export function PastSessionsSettings() {
  const ws = useApp(s => s.active);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [all, setAll] = useState<boolean | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<Result[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [note, setNote] = useState('');

  useEffect(() => {
    let current = true;
    call<boolean>('history', 'Enabled').then(
      yes => {
        if (!current) return;
        setEnabled(yes);
        if (yes) call<Health>('history', 'Health').then(
          h => { if (current) setHealth(h); },
          error => { if (current) setHealth({ ok: false, detail: errorText(error) }); },
        );
      },
      () => { if (current) setEnabled(false); },
    );
    call<boolean>('history', 'AllProjects').then(
      v => { if (current) setAll(v); },
      () => { if (current) setAll(false); },
    );
    return () => { current = false; };
  }, []);

  useEffect(() => {
    let current = true;
    setResults(null);
    call<Stats>('history', 'Stats', ws).then(
      s => { if (current) setStats(s); },
      () => { if (current) setStats(null); },
    );
    return () => { current = false; };
  }, [ws]);

  const setAllProjects = (next: boolean) => {
    const previous = all;
    setNote('');
    setAll(next);
    call('history', 'SetAllProjects', next).then(
      () => {},
      error => { setAll(previous); setNote(errorText(error)); },
    );
  };

  const search = (e: FormEvent) => {
    e.preventDefault();
    const q = query.trim();
    if (!q || searching) return;
    setSearching(true);
    setNote('');
    call<Result[]>('history', 'Search', { workspaceId: ws, query: q }).then(
      list => setResults(list),
      error => { setResults(null); setNote(errorText(error)); },
    ).finally(() => setSearching(false));
  };

  if (enabled === null || all === null) return null;
  return <div data-history-settings {...stylex.props(s.root)}>
    <Toggle name="history-all" label="Agents search every project" hint={HINT} on={all} onChange={setAllProjects} />
    <div {...stylex.props(s.status)}>
      {enabled ? <span data-history-health={health === null ? 'checking' : health.ok ? 'ok' : 'down'} {...stylex.props(s.muted)}>
        {health === null ? 'Embedding server: checking…' : health.ok ? 'Embedding server: running' : `Embedding server: not reachable${health.detail ? ` (${health.detail})` : ''}`}
      </span> : <span {...stylex.props(s.muted)}>Off: start Xhaos with -history-url to turn search on.</span>}
    </div>
    {stats && <p data-history-stats {...stylex.props(s.muted)}>{historySummary(stats.chunks, stats.scrubbed)}</p>}
    {enabled && <form onSubmit={search} {...stylex.props(s.form)}>
      <input data-history-query value={query} onChange={e => setQuery(e.target.value)} placeholder="What did we decide about…" aria-label="Search past sessions" {...stylex.props(s.input)} />
      <Button data-history-search type="submit" disabled={searching || !query.trim()} sx={searching ? s.busy : undefined}>{searching ? 'Searching…' : 'Search'}</Button>
    </form>}
    {note && <p {...stylex.props(s.muted)}>{note}</p>}
    {results && (results.length === 0
      ? <p {...stylex.props(s.muted)}>No matching passages.</p>
      : <ol data-history-results {...stylex.props(s.results)}>
        {results.map(r => <li key={r.id} data-history-result {...stylex.props(s.result)}>
          <span {...stylex.props(s.muted)}>{resultLabel(r.projectName, r.at, r.session, r.role)}</span>
          <span {...stylex.props(s.excerpt)}>{r.excerpt}</span>
        </li>)}
      </ol>)}
    <p {...stylex.props(s.muted)}>Your past sessions stay on this server. The search runs on a local model: nothing leaves the machine.</p>
  </div>;
}

const s = stylex.create({
  root: { display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 12, fontSize: 11 },
  status: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 },
  form: { display: 'flex', alignItems: 'center', gap: 6 },
  input: { flex: 1, minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 7, fontSize: 11, fontFamily: 'inherit' },
  busy: { animationName: { default: pulse, [breakpoints.reducedMotion]: 'none' }, animationDuration: '1s', animationIterationCount: 'infinite' },
  results: { margin: 0, paddingLeft: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 },
  result: { display: 'flex', flexDirection: 'column', gap: 2 },
  excerpt: { color: colors.text, lineHeight: 1.5 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5, lineHeight: 1.5 },
});
