import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import type { Agent } from '../types.ts';

// Shown while an agent is running and no reply text has arrived yet, so the
// owner can see the request is alive. Verbs rotate; seconds count up.
const verbs = ['Thinking', 'Reading the thread', 'Considering', 'Drafting', 'Mulling it over', 'Connecting the dots', 'Working'];
const startedAt = new Map<number, number>(); // agent id -> first time seen running (this tab)

export function useWorking(agent: Agent) {
  const running = agent.status === 'running';
  if (running && !startedAt.has(agent.id)) startedAt.set(agent.id, Date.now());
  if (!running) startedAt.delete(agent.id);
  const [, tick] = useState(0);
  useEffect(() => {
    if (!running) return undefined;
    const timer = setInterval(() => tick(n => n + 1), 1000);
    return () => clearInterval(timer);
  }, [running]);
  if (!running) return null;
  const seconds = Math.floor((Date.now() - (startedAt.get(agent.id) ?? Date.now())) / 1000);
  return { verb: verbs[Math.floor(seconds / 3) % verbs.length], seconds };
}

export function WorkingLabel({ agent }: { agent: Agent }) {
  const working = useWorking(agent);
  return working ? <span data-working-label {...stylex.props(styles.word, styles.label)}>{working.verb}…</span> : null;
}

export function WorkingLine({ agent }: { agent: Agent }) {
  const working = useWorking(agent);
  if (!working || agent.live) return null;
  return (
    <div data-entry="working" role="status" aria-label="Agent is working" {...stylex.props(styles.line)}>
      <span {...stylex.props(styles.word)}>{working.verb}…</span>
      <span {...stylex.props(styles.time)}>{working.seconds}s</span>
    </div>
  );
}

const shimmer = stylex.keyframes({
  from: { backgroundPosition: '125% 0' },
  to: { backgroundPosition: '-125% 0' },
});

const styles = stylex.create({
  // A light sweep across brand-colored text.
  word: {
    backgroundImage: {
      default: `linear-gradient(90deg, ${colors.brand} 0%, ${colors.brand} 35%, ${colors.brandGlow} 50%, ${colors.brand} 65%, ${colors.brand} 100%)`,
      [breakpoints.reducedMotion]: 'none',
    },
    backgroundSize: '250% 100%',
    backgroundClip: 'text',
    WebkitBackgroundClip: 'text',
    color: { default: 'transparent', [breakpoints.reducedMotion]: colors.brand },
    animationName: shimmer,
    animationDuration: '1.8s',
    animationTimingFunction: 'linear',
    animationIterationCount: 'infinite',
  },
  label: { flexShrink: 0, marginBlock: 0, marginLeft: 4, marginRight: 8, fontSize: 10, whiteSpace: 'nowrap' },
  line: { display: 'flex', alignItems: 'baseline', gap: 8, paddingBlock: 6, fontSize: 11 },
  time: { fontSize: 10, color: '#666666', fontVariantNumeric: 'tabular-nums' },
});
