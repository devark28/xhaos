// The side panel: a full-height column on the right, always open at 30% of
// the width. Its tab icons stay in one place, at the top; a tab switches
// what the panel shows. On narrow screens it sits below the agents.
import * as stylex from '@stylexjs/stylex';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import { Button, StatusDot } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { Panel } from './Panels.tsx';
import { UsageTotal } from './Usage.tsx';
import { useApp, pendingQuestionsIn, type PanelKind } from './state/store.ts';
import { selectPanel, showAgentsView } from './state/panels.ts';
import { useNarrow } from './narrow.ts';
import type { IconName } from './icons.ts';

// Each tab keeps the element ID its old ribbon button had.
const tabs: { kind: PanelKind; id: string; icon: IconName; label: string; also?: PanelKind[] }[] = [
  { kind: 'questions', id: 'show-questions', icon: 'question', label: 'Questions' },
  { kind: 'files', id: 'files', icon: 'folder', label: 'Files' },
  { kind: 'channels', id: 'channels', icon: 'chat', label: 'Channels' },
  { kind: 'memories', id: 'memories', icon: 'memory', label: 'Memories' },
  // Configuration you rarely reach for (models, tools and MCP) lives under Settings.
  { kind: 'settings', id: 'settings', icon: 'settings', label: 'Settings', also: ['providers', 'tools'] },
];

// The /btw answer's tab, shown while there is one.
const btwTab: (typeof tabs)[number] = { kind: 'btw', id: 'show-btw', icon: 'aside', label: 'Side answer (/btw)' };

export function SidePanel() {
  const kind = useApp(s => s.panel.kind);
  const hasBtw = useApp(s => s.btw !== null);
  const pending = useApp(s => pendingQuestionsIn(s).length);
  const narrow = useNarrow();
  const view = useApp(s => s.mobileView);
  const agentsView = narrow && view === 'agents';
  return (
    <aside id="side" aria-label="Side panel" {...stylex.props(side.root, narrow && side.phone, agentsView && side.phoneHidden)}>
      {/* On a phone the tabs are a navigation bar at the bottom, with the agents first. */}
      <nav aria-label="Panels" {...stylex.props(side.tabs, narrow && side.phoneNav)}>
        {narrow
          ? <Button variant="icon" id="show-agents" aria-label="Agents" title="Agents" aria-pressed={agentsView} chosen={agentsView} onClick={showAgentsView} sx={[side.tab, side.phoneTab]}><Icon name="list" size={18} /></Button>
          : <UsageTotal />}
        {(hasBtw ? [btwTab, ...tabs] : tabs).map(tab => {
          const chosen = !agentsView && (kind === tab.kind || Boolean(tab.also?.includes(kind)));
          const waiting = tab.kind === 'questions' && pending > 0;
          return (
            <Button key={tab.kind} variant="icon" id={tab.id} aria-label={waiting ? `${tab.label} · ${pending} waiting` : tab.label} title={tab.label}
              aria-pressed={chosen} chosen={chosen} onClick={() => selectPanel(tab.kind)} sx={[side.tab, narrow && side.phoneTab, waiting && side.attention]}>
              <Icon name={tab.icon} size={narrow ? 18 : 15} />
              {waiting && <span {...stylex.props(side.badge)}><StatusDot status="waiting" size={6} />{pending}</span>}
            </Button>
          );
        })}
      </nav>
      {!agentsView && <Panel />}
    </aside>
  );
}

const side = stylex.create({
  root: {
    display: 'flex',
    flexDirection: 'column',
    minWidth: 0,
    minHeight: 0,
    borderLeftWidth: { default: 1, [breakpoints.tablet]: 0 },
    borderTopWidth: { default: 0, [breakpoints.tablet]: 1 },
    borderStyle: 'solid',
    borderColor: colors.lineFaint,
    backgroundColor: colors.bg,
  },
  tabs: { display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 4, height: 44, flexShrink: 0, paddingInline: 10 },
  tab: { position: 'relative', width: 30, height: 30, padding: 0 },
  attention: { color: colors.brand },
  phone: { gridArea: 'view', borderTopWidth: 0, borderLeftWidth: 0 },
  // Showing the agents: the panel takes no space; its bar stays.
  phoneHidden: { display: 'contents' },
  phoneNav: {
    position: 'fixed', left: 0, right: 0, bottom: 0, zIndex: 30,
    height: 'calc(56px + env(safe-area-inset-bottom))', paddingBottom: 'env(safe-area-inset-bottom)', paddingInline: 6,
    justifyContent: 'space-around', gap: 0,
    borderTopWidth: 1, borderTopStyle: 'solid', borderTopColor: colors.lineFaint, backgroundColor: colors.bg,
  },
  phoneTab: { width: 48, height: 44 },
  badge: {
    position: 'absolute',
    top: 0,
    right: -5,
    display: 'inline-flex',
    alignItems: 'center',
    gap: 2,
    paddingInline: 3,
    borderRadius: 6,
    backgroundColor: colors.bg,
    fontSize: 9,
    color: colors.brand,
  },
});
