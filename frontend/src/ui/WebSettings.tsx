// Settings → Tools → Web: the search backend for web_search (web_fetch
// needs no setup). DuckDuckGo works with no key; Brave Search needs an API
// key (encrypted, never shown again); SearXNG is your own instance.
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button, Info } from './kit.tsx';
import { call } from './state/backend.ts';
import { toast, errorText, get } from './state/store.ts';

interface Settings { backend: 'off' | 'duckduckgo' | 'brave' | 'searxng'; baseUrl: string; hasKey: boolean }

export function WebSettings() {
  const [st, setSt] = useState<Settings | null>(null);
  const [backend, setBackend] = useState<Settings['backend']>('duckduckgo'), [baseUrl, setBaseUrl] = useState(''), [key, setKey] = useState('');
  const [note, setNote] = useState('');
  const load = async () => {
    try { const s = await call<Settings>('web', 'Settings', get().active); setSt(s); setBackend(s.backend); setBaseUrl(s.baseUrl); }
    catch (error) { setNote(errorText(error)); }
  };
  useEffect(() => { void load(); }, []);
  const save = async () => {
    try { const s = await call<Settings>('web', 'Save', { workspaceId: get().active, backend, baseUrl, apiKey: key }); setSt(s); setKey(''); setNote('Saved.'); }
    catch (error) { toast(errorText(error)); }
  };
  const test = async () => {
    setNote('Searching…');
    try {
      const r = await call<{ title: string; url: string }[]>('web', 'Test', get().active, 'xhaos agent harness');
      setNote(r.length ? `${r.length} results, first: ${r[0].title} (${r[0].url})` : 'The search worked but found nothing.');
    } catch (error) { setNote(`Search failed: ${errorText(error)}`); }
  };
  if (!st) return <p {...stylex.props(s.muted)}>{note || 'Loading…'}</p>;
  return (
    <div data-web-settings {...stylex.props(s.root)}>
      <p {...stylex.props(s.muted)}>Agents read pages with <code>web_fetch</code> and search with <code>web_search</code>. The server makes the requests, only to public addresses; pages are marked as untrusted for the model. Both ask under manual review.</p>
      <label {...stylex.props(s.row)}><span {...stylex.props(s.label)}>Search with<Info text="DuckDuckGo: no key, reads its results page (best effort; it may rate-limit). Brave Search: an API key from api-dashboard.search.brave.com (free tier). SearXNG: your own instance, with its JSON format enabled (it may be on your LAN)." /></span>
        <select data-web-backend value={backend} onChange={e => setBackend(e.target.value as Settings['backend'])} {...stylex.props(s.input)}>
          <option value="duckduckgo">DuckDuckGo (no key)</option><option value="brave">Brave Search (API key)</option><option value="searxng">SearXNG (your own)</option><option value="off">Off</option>
        </select></label>
      {backend === 'brave' && <label {...stylex.props(s.row)}><span {...stylex.props(s.label)}>API key</span>
        <input type="password" data-web-key value={key} onChange={e => setKey(e.target.value)} placeholder={st.hasKey ? '(saved; leave empty to keep)' : 'BSA…'} {...stylex.props(s.input)} /></label>}
      {backend === 'searxng' && <label {...stylex.props(s.row)}><span {...stylex.props(s.label)}>Address</span>
        <input data-web-base value={baseUrl} onChange={e => setBaseUrl(e.target.value)} placeholder="https://search.example.com" {...stylex.props(s.input)} /></label>}
      <div {...stylex.props(s.actions)}>
        {note && <span data-web-note {...stylex.props(s.note)}>{note}</span>}
        <Button data-web-test onClick={test}>Test</Button>
        <Button data-web-save onClick={save}>Save</Button>
      </div>
    </div>
  );
}

const s = stylex.create({
  root: { display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 12, fontSize: 11 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5, lineHeight: 1.5 },
  row: { display: 'grid', gridTemplateColumns: '110px 1fr', alignItems: 'center', gap: 8 },
  label: { color: colors.textFaint, fontSize: 10 },
  input: { minWidth: 0, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 5, paddingInline: 7, fontSize: 11, fontFamily: 'inherit' },
  actions: { display: 'flex', justifyContent: 'flex-end', alignItems: 'center', gap: 8 },
  note: { flexGrow: 1, minWidth: 0, color: colors.textMuted, fontSize: 10, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' },
});
