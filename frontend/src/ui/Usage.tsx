// Token use and cost: a total at the left of the side panel's tab bar, and
// the Usage panel with the breakdown per model and per agent, and the price
// of each model (Anthropic's are built in; others are set here).
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { flatten } from '../model.ts';
import { useApp, workspaceOf, toast, errorText } from './state/store.ts';
import { call, refreshUsage } from './state/backend.ts';
import { openPanel } from './state/panels.ts';
import type { UsagePrice, UsageReport, UsageTokens } from '../types.ts';

export const tokens = (n: number) => (n >= 1e6 ? `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)}k` : String(n));
export const dollars = (c?: number) => (c === undefined ? '—' : c < 0.01 && c > 0 ? '<$0.01' : `$${c.toFixed(2)}`);

// usageLine is the short total: cost when any model is priced, else tokens.
export function usageLine(usage: UsageReport | null): string {
  if (!usage || !usage.total.calls) return 'nothing spent yet';
  const t = usage.total;
  return t.cost !== undefined ? dollars(t.cost) : `${tokens(t.input + t.output)} tok`;
}

// The total in the tab bar; it opens Settings → Usage and cost.
export function UsageTotal() {
  const usage = useApp(s => s.usage);
  const chosen = useApp(s => s.panel.kind === 'settings' && s.panel.section === 'usage');
  if (!usage || !usage.total.calls) return null;
  const t = usage.total;
  const label = usageLine(usage);
  const title = `${tokens(t.input)} in (${tokens(t.cacheRead)} from cache) · ${tokens(t.output)} out · ${t.calls} calls${usage.unpriced ? ` · ${tokens(usage.unpriced)} tokens without a price` : ''}`;
  return <Button id="usage-total" aria-label={`Usage: ${label}`} title={title} chosen={chosen} aria-pressed={chosen} onClick={() => openPanel('settings', { section: 'usage' })} sx={s.total}>{label}</Button>;
}

function Row({ name, t, sub }: { name: string; t: UsageTokens; sub?: string }) {
  return (
    <tr>
      <td {...stylex.props(s.name)}>{name}{sub && <div {...stylex.props(s.sub)}>{sub}</div>}</td>
      <td>{t.calls}</td><td>{tokens(t.input)}</td><td>{tokens(t.cacheRead)}</td><td>{tokens(t.output)}</td><td {...stylex.props(s.cost)}>{dollars(t.cost)}</td>
    </tr>
  );
}

function PriceForm({ model, price, done }: { model: string; price?: UsagePrice; done(): void }) {
  const [f, setF] = useState({ input: String(price?.input ?? ''), output: String(price?.output ?? ''), cacheRead: price?.source === 'yours' ? String(price.cacheRead) : '' });
  const save = async () => {
    try { await call('usage', 'SetPrice', { model, input: Number(f.input), output: Number(f.output), cacheRead: Number(f.cacheRead || 0), cacheWrite: 0 }); await refreshUsage(); done(); }
    catch (error) { toast(`Price not saved: ${errorText(error)}`); }
  };
  const reset = async () => { await call('usage', 'ResetPrice', model); await refreshUsage(); done(); };
  const field = (key: keyof typeof f, label: string) => (
    <label {...stylex.props(s.field)}>{label}<input data-price={key} inputMode="decimal" value={f[key]} onChange={e => setF({ ...f, [key]: e.target.value })} {...stylex.props(s.input)} /></label>
  );
  return (
    <div data-price-form={model} {...stylex.props(s.form)}>
      <div {...stylex.props(s.sub)}>USD per million tokens. An empty cache price is 10% of input.</div>
      <div {...stylex.props(s.fields)}>{field('input', 'Input')}{field('output', 'Output')}{field('cacheRead', 'Cache read')}</div>
      <div {...stylex.props(s.actions)}>
        {price?.source === 'yours' && <Button onClick={reset}>Use built-in</Button>}
        <Button onClick={done}>Cancel</Button>
        <Button data-price-save onClick={save}>Save price</Button>
      </div>
    </div>
  );
}

export function UsagePanel() {
  const usage = useApp(s => s.usage);
  const tree = useApp(s => workspaceOf(s).agents); // a stable reference; flattened below
  const agents = flatten(tree);
  const [editing, setEditing] = useState('');
  useEffect(() => { void refreshUsage(); }, []);
  if (!usage) return <p {...stylex.props(s.sub)}>Usage is counted on the server.</p>;
  const title = (id: number) => agents.find(a => a.id === id)?.title || '';
  const head = <thead><tr><th>Model</th><th>Calls</th><th>Input</th><th>Cached</th><th>Output</th><th>Cost</th></tr></thead>;
  return <>
    <p {...stylex.props(s.note)}>
      Every model call is counted: agent turns, compaction summaries, titles and /btw. Input is the whole prompt; "cached" is the part read from the provider's prompt cache, billed at a fraction of the input price. Cost uses the prices below and changes when a price does.
    </p>
    <table {...stylex.props(s.table)}>{head}<tbody>
      <Row name="Total" t={usage.total} sub={usage.unpriced ? `${tokens(usage.unpriced)} tokens have no price yet` : undefined} />
    </tbody></table>
    <h3 {...stylex.props(s.h)}>By model</h3>
    <table data-usage-models {...stylex.props(s.table)}>{head}<tbody>
      {usage.models.map(m => {
        const p = m.price;
        const priced = p ? `$${p.input}/$${p.output} per MTok${p.cacheRead ? `, cache $${p.cacheRead}` : ''}${p.longAfter ? `, $${p.longInput}/$${p.longOutput} above ${tokens(p.longAfter)}` : ''} · ${p.source}` : m.kind === 'sim' ? 'simulated, free' : m.kind === 'claude-cli' ? 'included in your Claude subscription' : 'no price';
        const purposes = Object.entries(m.purposes).map(([k, n]) => `${n} ${k}`).join(', ');
        return [
          <Row key={m.provider + m.model} name={m.model} t={m.tokens} sub={`${m.provider} · ${priced} · ${purposes}`} />,
          m.kind !== 'sim' && <tr key={m.model + '-edit'}><td colSpan={6}>
            {editing === m.model ? <PriceForm model={m.model} price={p} done={() => setEditing('')} /> : <Button data-edit-price={m.model} onClick={() => setEditing(m.model)} sx={s.link}>{p ? 'Change price' : 'Set price'}</Button>}
          </td></tr>,
        ];
      })}
    </tbody></table>
    {usage.agents.length > 0 && <>
      <h3 {...stylex.props(s.h)}>By agent</h3>
      <table data-usage-agents {...stylex.props(s.table)}>{head}<tbody>
        {[...usage.agents].sort((a, b) => (b.tokens.cost ?? 0) - (a.tokens.cost ?? 0) || b.tokens.input - a.tokens.input).map(a => <Row key={a.agentId} name={`#${a.agentId}`} sub={title(a.agentId)} t={a.tokens} />)}
      </tbody></table>
    </>}
  </>;
}

const s = stylex.create({
  total: { fontSize: 11, color: colors.textMuted, paddingInline: 8, height: 26, marginRight: 'auto', fontVariantNumeric: 'tabular-nums' },
  note: { fontSize: 10, color: colors.textGhost, lineHeight: 1.6, marginTop: 0 },
  h: { fontSize: 11, color: colors.textMuted, marginTop: 14, marginBottom: 4 },
  table: { width: '100%', borderCollapse: 'collapse', fontSize: 11, fontVariantNumeric: 'tabular-nums', color: colors.text },
  name: { textAlign: 'left', maxWidth: 180, overflowWrap: 'anywhere' },
  sub: { fontSize: 10, color: colors.textGhost },
  cost: { textAlign: 'right', color: colors.textStrong },
  link: { fontSize: 10, color: colors.textFaint, paddingInline: 0 },
  form: { display: 'grid', gap: 6, paddingBlock: 6 },
  fields: { display: 'flex', gap: 8, flexWrap: 'wrap' },
  field: { display: 'grid', gap: 2, fontSize: 10, color: colors.textFaint },
  input: { width: 80, backgroundColor: colors.sunken, color: colors.text, borderWidth: 0, borderRadius: 3, paddingBlock: 4, paddingInline: 6, fontSize: 11, fontFamily: 'inherit' },
  actions: { display: 'flex', gap: 8, justifyContent: 'flex-end' },
});
