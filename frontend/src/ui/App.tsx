import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { DangerDialog } from './Danger.tsx';
import * as stylex from '@stylexjs/stylex';
import { activeAgents } from '../model.ts';
import { watchStickyColumns } from '../sticky-columns.ts';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { AgentCard } from './AgentCard.tsx';
import { ComposerArea } from './Composer.tsx';
import { useApp, workspaceOf, flushAfterCommit } from './state/store.ts';
import { toggleCollapseAll, notifyAttention } from './state/agents.ts';
import { setLayout, clearSearch } from './state/panels.ts';
import { SidePanel } from './SidePanel.tsx';

// The bar above the agents: search context, expand/collapse all, columns.
function AgentsToolbar() {
  const agents = activeAgents(useApp(s => workspaceOf(s).agents));
  const collapsed = useApp(s => s.collapsed);
  const layout = useApp(s => s.layout);
  const search = useApp(s => s.search);
  return (
    <div data-agents-toolbar {...stylex.props(board.toolbar)}>
      {search && <span {...stylex.props(board.search)}>Search: {search}<Button variant="icon" id="clear-search" aria-label="Clear search" onClick={clearSearch}><Icon name="close" size={12} /></Button></span>}
      <div {...stylex.props(board.tools)}>
        <Button id="collapse-all" onClick={toggleCollapseAll}>{agents.every(a => collapsed.has(a.id)) ? 'Expand' : 'Collapse'} all</Button>
        <Button variant="icon" data-layout="list" aria-label="One column" chosen={layout === 'list'} onClick={() => setLayout('list')}><Icon name="list" size={14} /></Button>
        <Button variant="icon" data-layout="grid" aria-label="Two columns" chosen={layout === 'grid'} onClick={() => setLayout('grid')}><Icon name="grid" size={14} /></Button>
        <Button variant="icon" data-layout="three" aria-label="Three columns" chosen={layout === 'three'} onClick={() => setLayout('three')}><Icon name="columns" size={14} /></Button>
      </div>
    </div>
  );
}

function AgentsSection() {
  const scroller = useRef<HTMLElement>(null);
  const search = useApp(s => s.search);
  const layout = useApp(s => s.layout);
  const drawing = useApp(s => s.mode === 'draw');
  const query = search.toLowerCase();
  const agents = useApp(s => workspaceOf(s).agents).filter(a => !a.archivedAt && activeAgents([a]).some(x => `${x.title} ${x.id}`.toLowerCase().includes(query)));
  const columns = layout === 'list' ? 1 : layout === 'three' ? 3 : 2;
  // Independent sticky columns: recalculated after every render.
  useLayoutEffect(() => watchStickyColumns(scroller.current!, [...scroller.current!.querySelectorAll<HTMLElement>('[data-sticky-column]')]));
  return (
    <section id="agents" aria-label="Agents" ref={scroller} {...stylex.props(board.section, drawing && board.hidden)}>
      <AgentsToolbar />
      <div data-layout={layout} {...stylex.props(board.grid, layout === 'list' ? board.list : layout === 'three' ? board.three : board.two)}>
        {!agents.length
          ? <div data-empty-board {...stylex.props(board.empty)}>{search ? 'No matching agents.' : 'No agents yet. Describe a task in the composer and press Enter.'}</div>
          : Array.from({ length: columns }, (_, column) => (
            <div key={column} data-sticky-column={columns > 1 || undefined} {...stylex.props(board.column, columns > 1 && board.sticky)}>
              {agents.filter((_, index) => columns === 1 || index % columns === column).map(agent => <AgentCard key={agent.id} agent={agent} />)}
            </div>
          ))}
      </div>
    </section>
  );
}

// The toast: one short status line at the bottom, replaced by the next.
function Toast() {
  const toast = useApp(s => s.toast);
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    if (!toast) return;
    setVisible(true);
    const timer = setTimeout(() => setVisible(false), 4500);
    return () => clearTimeout(timer);
  }, [toast]);
  return <div id="toast" role="status" data-visible={visible || undefined} {...stylex.props(page.toast, visible && page.toastVisible)}>{toast?.message}</div>;
}

export function App() {
  const ready = useApp(s => s.ready);
  const drawing = useApp(s => s.mode === 'draw');
  const commits = useApp(s => s.commits);
  const agents = useApp(s => workspaceOf(s).agents);
  useLayoutEffect(() => { flushAfterCommit(); }, [commits]);
  useEffect(() => { if (ready) notifyAttention(); }, [ready, agents]);
  if (!ready) return <Toast />;
  return (
    <main {...stylex.props(page.main)}>
      <div {...stylex.props(page.left, drawing && page.drawing)}>
        <section id="command-center" aria-label="Command center" {...stylex.props(page.command, drawing && page.clip)}>
          <ComposerArea />
        </section>
        <AgentsSection />
      </div>
      <SidePanel />
      <DangerDialog />
      <Toast />
    </main>
  );
}

const page = stylex.create({
  // The page: the work column on the left and the side panel (30% of the
  // width) on the right. Narrow screens stack the panel below the work.
  main: {
    height: 'calc(100dvh / var(--x-zoom, 1))', // the app is zoomed by the text size setting
    display: 'grid',
    gridTemplateColumns: { default: 'minmax(0, 1fr) 30%', [breakpoints.tablet]: 'minmax(0, 1fr)' },
    gridTemplateRows: { default: 'minmax(0, 1fr)', [breakpoints.tablet]: 'minmax(0, 1fr) calc(45dvh / var(--x-zoom, 1))' },
    backgroundColor: colors.bg,
  },
  // A fixed-height command row, so the agents below never move when its
  // content changes; drawing gives the sketch pad the whole column.
  left: { minWidth: 0, minHeight: 0, display: 'grid', gridTemplateRows: 'max(170px, 26dvh) minmax(0, 1fr)' },
  drawing: { gridTemplateRows: 'minmax(0, 1fr) 0', overflow: 'hidden' },
  command: { display: 'flex', flexDirection: 'column', minHeight: 0, paddingTop: 8, position: 'relative', zIndex: 5 },
  clip: { overflow: 'hidden' },
  toast: {
    position: 'fixed',
    bottom: 20,
    left: '50%',
    transform: 'translateX(-50%)',
    zIndex: 40,
    maxWidth: '90%',
    paddingBlock: 10,
    paddingInline: 16,
    borderRadius: 5,
    backgroundColor: colors.light,
    color: colors.onLight,
    boxShadow: '0 4px 25px rgba(0,0,0,0.45)',
    opacity: 0,
    pointerEvents: 'none',
  },
  toastVisible: { opacity: 1 },
});

const board = stylex.create({
  section: {
    display: 'flex',
    flexDirection: 'column',
    minHeight: 0,
    overflow: 'auto',
    paddingTop: 4,
    paddingBottom: 16,
    paddingInline: { default: 16, [breakpoints.tablet]: 10 },
  },
  hidden: { display: 'none' },
  toolbar: { display: 'flex', alignItems: 'center', gap: 8, minHeight: 28, marginBottom: 6, flexShrink: 0 },
  search: { display: 'flex', alignItems: 'center', gap: 4, color: colors.textDim, fontSize: 10 },
  tools: { display: 'flex', alignItems: 'center', gap: 2, marginLeft: 'auto' },
  grid: { display: { default: 'grid', [breakpoints.tablet]: 'flex' }, flexDirection: 'column', gap: 10, alignItems: 'start', flexShrink: 0, width: '100%' },
  two: { gridTemplateColumns: 'repeat(2, minmax(0, 1fr))' },
  three: { gridTemplateColumns: 'repeat(3, minmax(0, 1fr))' },
  list: { display: 'grid', gridTemplateColumns: '1fr', alignItems: 'stretch' },
  column: { display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0, width: { default: null, [breakpoints.tablet]: '100%' } },
  // A tall column scrolls until its bottom reaches the viewport; see sticky-columns.ts.
  sticky: { position: { default: null, [breakpoints.wide]: 'sticky' }, top: 'var(--column-sticky-top, 0px)' },
  empty: { padding: 20, color: colors.textFaint },
});
