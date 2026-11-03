import { useEffect, useLayoutEffect, useRef, useSyncExternalStore } from 'react';
import * as stylex from '@stylexjs/stylex';
import { activeAgents } from '../model.ts';
import { watchStickyColumns } from '../sticky-columns.ts';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { AgentCard } from './AgentCard.tsx';
import { ComposerArea } from './Composer.tsx';
import { S, subscribe, getVersion, flushAfterCommit, workspace, setLayout, toggleCollapseAll, openPanel, clearSearch, notifyAttention, showTools } from './store.ts';

function Ribbon() {
  const agents = activeAgents(workspace().agents);
  return (
    <header {...stylex.props(ribbon.root)}>
      <a href="/" aria-label="Xhaos home" {...stylex.props(ribbon.brand)}>x<span {...stylex.props(ribbon.brandTail)}>h</span></a>
      <h1 {...stylex.props(ribbon.title)}>xhaos</h1>
      <div {...stylex.props(ribbon.actions)}>
        <Button id="collapse-all" onClick={toggleCollapseAll}>{agents.every(a => S.collapsed.has(a.id)) ? 'Expand' : 'Collapse'} all</Button>
        <div {...stylex.props(ribbon.layouts)}>
          <Button variant="icon" data-layout="grid" aria-label="Two columns" chosen={S.layout === 'grid'} onClick={() => setLayout('grid')}><Icon name="grid" size={14} /></Button>
          <Button variant="icon" data-layout="list" aria-label="One column" chosen={S.layout === 'list'} onClick={() => setLayout('list')}><Icon name="list" size={14} /></Button>
        </div>
        <Button variant="icon" id="channels" aria-label="Channels" title="Channels" onClick={() => openPanel('channels')}><Icon name="chat" size={15} /></Button>
        <Button variant="icon" id="memories" aria-label="Memories" title="Memories" onClick={() => openPanel('memories')}><Icon name="memory" size={15} /></Button>
        <Button variant="icon" id="integrations" aria-label="Tools and MCP" title="Tools and MCP" onClick={() => showTools()}><Icon name="plug" size={15} /></Button>
        <Button variant="icon" id="settings" aria-label="Settings" title="Settings" onClick={() => openPanel('settings')}><Icon name="settings" size={15} /></Button>
      </div>
    </header>
  );
}

function AgentsSection() {
  const scroller = useRef<HTMLElement>(null);
  const query = S.search.toLowerCase();
  const agents = workspace().agents.filter(a => !a.archivedAt && activeAgents([a]).some(x => `${x.title} ${x.id}`.toLowerCase().includes(query)));
  const columns = S.layout === 'list' ? 1 : S.layout === 'three' ? 3 : 2;
  // Independent sticky columns: recalculated after every render.
  useLayoutEffect(() => watchStickyColumns(scroller.current!, [...scroller.current!.querySelectorAll<HTMLElement>('[data-sticky-column]')]));
  return (
    <section id="agents" aria-label="Agents" ref={scroller} {...stylex.props(board.section, S.mode === 'draw' && board.hidden)}>
      {S.search && (
        <div {...stylex.props(board.search)}>
          <span>Search: {S.search}</span>
          <Button variant="icon" id="clear-search" aria-label="Clear search" onClick={clearSearch}><Icon name="close" size={12} /></Button>
        </div>
      )}
      <div data-layout={S.layout} {...stylex.props(board.grid, S.layout === 'list' ? board.list : S.layout === 'three' ? board.three : board.two)}>
        {!agents.length
          ? <div data-empty-board {...stylex.props(board.empty)}>{S.search ? 'No matching agents.' : 'No agents yet. Describe a task in the composer and press Enter.'}</div>
          : Array.from({ length: columns }, (_, column) => (
            <div key={column} data-sticky-column={columns > 1 || undefined} {...stylex.props(board.column, columns > 1 && board.sticky)}>
              {agents.filter((_, index) => columns === 1 || index % columns === column).map(agent => <AgentCard key={agent.id} agent={agent} />)}
            </div>
          ))}
      </div>
    </section>
  );
}

export function App() {
  useSyncExternalStore(subscribe, getVersion);
  useLayoutEffect(() => { flushAfterCommit(); });
  useEffect(() => { if (S.ready) notifyAttention(); });
  if (!S.ready) return null;
  const drawing = S.mode === 'draw';
  return (
    <main {...stylex.props(page.main, drawing && page.drawing)}>
      <section id="command-center" aria-label="Command center" {...stylex.props(page.command, drawing && page.clip)}>
        <Ribbon />
        <ComposerArea />
      </section>
      <AgentsSection />
    </main>
  );
}

const page = stylex.create({
  // A fixed-height command row, so the agents below never move when its
  // content changes; drawing gives the sketch pad the whole page.
  main: { height: '100dvh', display: 'grid', gridTemplateRows: 'max(200px, 30dvh) minmax(0, 1fr)', backgroundColor: colors.bg },
  drawing: { gridTemplateRows: 'minmax(0, 1fr) 0', overflow: 'hidden' },
  command: { display: 'flex', flexDirection: 'column', minHeight: 0, position: 'relative', zIndex: 5 },
  clip: { overflow: 'hidden' },
});

const ribbon = stylex.create({
  root: {
    height: 44,
    flexShrink: 0,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingInline: { default: 16, [breakpoints.tablet]: 10 },
    position: 'relative',
  },
  brand: { textDecoration: 'none', color: colors.textStrong, fontSize: 28, letterSpacing: -6, fontWeight: 900, width: 32, lineHeight: 1, paddingBottom: 5 },
  brandTail: { color: colors.textMuted },
  title: {
    position: { default: 'absolute', [breakpoints.narrow]: 'static' },
    left: '50%',
    transform: { default: 'translateX(-50%)', [breakpoints.narrow]: 'none' },
    marginBlock: 0,
    marginLeft: { default: 0, [breakpoints.narrow]: 10 },
    marginRight: { default: 0, [breakpoints.narrow]: 'auto' },
    fontSize: 12,
    fontWeight: 500,
    letterSpacing: 0.6,
    color: colors.textMuted,
    pointerEvents: 'none',
  },
  actions: { display: 'flex', alignItems: 'center', gap: { default: 8, [breakpoints.narrow]: 2 } },
  layouts: { display: 'flex', gap: { default: 2, [breakpoints.narrow]: 0 } },
});

const board = stylex.create({
  section: {
    display: 'flex',
    flexDirection: 'column',
    minHeight: 0,
    overflow: 'auto',
    paddingTop: 8,
    paddingBottom: 16,
    paddingInline: { default: 16, [breakpoints.tablet]: 10 },
  },
  hidden: { display: 'none' },
  search: { display: 'flex', alignItems: 'center', gap: 8, color: colors.textDim, fontSize: 10, marginBottom: 8, flexShrink: 0 },
  grid: { display: { default: 'grid', [breakpoints.tablet]: 'flex' }, flexDirection: 'column', gap: 10, alignItems: 'start', flexShrink: 0, width: '100%' },
  two: { gridTemplateColumns: 'repeat(2, minmax(0, 1fr))' },
  three: { gridTemplateColumns: 'repeat(3, minmax(0, 1fr))' },
  list: { display: 'grid', gridTemplateColumns: '1fr', alignItems: 'stretch' },
  column: { display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0, width: { default: null, [breakpoints.tablet]: '100%' } },
  // A tall column scrolls until its bottom reaches the viewport; see sticky-columns.ts.
  sticky: { position: { default: null, [breakpoints.wide]: 'sticky' }, top: 'var(--column-sticky-top, 0px)' },
  empty: { padding: 20, color: colors.textFaint },
});
