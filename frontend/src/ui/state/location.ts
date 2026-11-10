// Hash links: where you are lives in the URL hash, as query-style pairs, so
// a place can be bookmarked, reloaded, sent to another PC, or reached with
// the back button, without any routing on the server.
//
//   #panel=settings&section=usage    a side panel, and a Settings section
//   #panel=channels&channel=5        a channel
//   #agents=4,6,5                    the open agent cards
//   #panel=questions&agents=12       both at once
//
// The link follows the page: it lists the cards that are open, and opening
// a link opens exactly those (closing the rest). Changing panel adds a
// history entry (Back returns to the previous panel); opening agents and
// sections replaces the current one, so browsing cards does not flood the
// history.
import { get, update, useApp, workspaceOf, toast, type PanelKind, type State } from './store.ts';
import { openPanel, selectPanel } from './panels.ts';
import { flatten } from '../../model.ts';
import { agentPath } from '../../attention.ts';

const panels: PanelKind[] = ['questions', 'files', 'channels', 'boards', 'nodes', 'memories', 'settings', 'history', 'status', 'help'];

export interface Place { panel?: PanelKind; section?: string; channel?: string; agents?: number[] }

export function parseHash(hash: string): Place {
  const q = new URLSearchParams(hash.replace(/^#\/?/, ''));
  const place: Place = {};
  const panel = q.get('panel') as PanelKind | null;
  if (panel && panels.includes(panel)) place.panel = panel;
  if (q.get('section')) place.section = q.get('section')!;
  if (q.get('channel')) place.channel = q.get('channel')!;
  const list = (q.get('agents') || '').split(',').map(Number).filter(n => Number.isInteger(n) && n > 0);
  if (list.length) place.agents = [...new Set(list)];
  return place;
}

// openIds lists the open cards (top-level and subagents), in tree order.
export function openIds(s: Pick<State, 'workspaces' | 'active' | 'collapsed'>): number[] {
  return flatten(workspaceOf(s as State).agents).filter(a => !a.archivedAt && !s.collapsed.has(a.id)).map(a => a.id);
}

export function hashFor(s: Pick<State, 'panel' | 'focusAgent' | 'workspaces' | 'active' | 'collapsed'>): string {
  const q = new URLSearchParams();
  // Settings reached through its old aliases is still Settings.
  const kind = s.panel.kind === 'providers' || s.panel.kind === 'tools' ? 'settings' : s.panel.kind;
  if (panels.includes(kind)) q.set('panel', kind);
  const section = s.panel.kind === 'providers' ? 'models' : s.panel.kind === 'tools' ? 'tools' : s.panel.section;
  if (kind === 'settings' && section) q.set('section', section);
  if (kind === 'channels' && s.panel.channelId) q.set('channel', s.panel.channelId);
  const open = openIds(s);
  if (open.length) q.set('agents', open.join(','));
  const text = q.toString().replace(/%2C/g, ',');
  return text ? `#${text}` : '';
}

// openAgents opens exactly these cards (and their parents), closing the
// rest, then scrolls to the last one with a brief highlight.
export function openAgents(ids: number[]) {
  const all = flatten(workspaceOf(get()).agents);
  const known = ids.filter(id => all.some(a => a.id === id));
  if (!known.length) return false;
  const id = known[known.length - 1];
  update(s => {
    const keep = new Set(known.flatMap(k => agentPath(workspaceOf(s).agents, k)));
    for (const a of flatten(workspaceOf(s).agents)) { if (keep.has(a.id)) s.collapsed.delete(a.id); else s.collapsed.add(a.id); }
    s.focusAgent = id;
    s.mobileView = 'agents';
  });
  flash(id);
  return true;
}

// showAgent opens one card (and its parents), leaving the others as they
// are: the target of a #12 link in a message.
export function showAgent(id: number) {
  if (!flatten(workspaceOf(get()).agents).some(a => a.id === id)) { toast(`There is no agent #${id} in this workspace.`); return; }
  update(s => {
    agentPath(workspaceOf(s).agents, id).forEach(k => s.collapsed.delete(k));
    s.focusAgent = id;
    s.mobileView = 'agents';
  });
  flash(id);
}

function flash(id: number) {
  requestAnimationFrame(() => requestAnimationFrame(() => {
    const el = document.querySelector<HTMLElement>(`[data-agent-header="${id}"]`);
    if (!el) return;
    el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    el.dataset.flash = '';
    setTimeout(() => delete el.dataset.flash, 1600);
  }));
}

let applying = false;

// applyHash moves the page to the place in the hash. On first load a phone
// stays on the agents (the link always names a panel); following a link
// later shows its panel.
export function applyHash(hash = location.hash, initial = false) {
  const place = parseHash(hash);
  const view = get().mobileView;
  applying = true;
  try {
    if (place.panel === 'settings') openPanel('settings', place.section ? { section: place.section } : {});
    else if (place.panel === 'channels') openPanel('channels', place.channel ? { channelId: place.channel } : {});
    else if (place.panel) selectPanel(place.panel);
    if (place.agents) openAgents(place.agents);
    if (initial) update(s => { s.mobileView = view; });
  } finally { applying = false; }
}

// installHashNavigation follows the hash both ways. Call it once the agents
// are loaded, so an #agent link finds its card.
let installed = false;
export function installHashNavigation() {
  applyHash(location.hash, true);
  if (installed) return;
  installed = true;
  addEventListener('hashchange', () => applyHash());
  // #12 links in messages (markdown.ts).
  document.addEventListener('click', event => {
    const link = (event.target as Element).closest?.<HTMLElement>('[data-agent-link]');
    if (!link) return;
    event.preventDefault();
    showAgent(Number(link.dataset.agentLink));
  });
  useApp.subscribe((s, prev) => {
    if (applying || (s.panel === prev.panel && s.collapsed === prev.collapsed)) return;
    const next = hashFor(s);
    if (next === location.hash || (!next && !location.hash)) return;
    const url = next || location.pathname + location.search;
    // A different panel is a step Back can undo; the rest refines this step.
    if (s.panel.kind !== prev.panel.kind) history.pushState(null, '', url);
    else history.replaceState(null, '', url);
  });
}
