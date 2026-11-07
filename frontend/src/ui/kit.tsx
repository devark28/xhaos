// Shared building blocks. Every button, status dot, chip and panel in the app
// comes from here, so they look and behave the same everywhere.
import * as stylex from '@stylexjs/stylex';
import type { StyleXStyles } from '@stylexjs/stylex';
import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import type { Status } from '../types.ts';

// ---- buttons ---------------------------------------------------------------
type Variant = 'text' | 'icon' | 'allow' | 'deny' | 'send' | 'menu';
type ButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'className' | 'style'> & {
  variant?: Variant;
  chosen?: boolean;
  sx?: StyleXStyles;
  [data: `data-${string}`]: string | number | boolean | undefined;
};

export function Button({ variant = 'text', chosen = false, sx, type = 'button', ...rest }: ButtonProps) {
  return <button type={type} {...rest} {...stylex.props(buttons.base, buttons[variant], chosen && buttons.chosen, sx)} />;
}

const buttons = stylex.create({
  base: {
    cursor: { default: 'pointer', ':disabled': 'default' },
    color: 'inherit',
    backgroundColor: { default: 'transparent', ':hover': colors.hover },
    borderWidth: 0,
    opacity: { default: 1, ':disabled': 0.3 },
    fontFamily: 'inherit',
    fontSize: 'inherit',
    padding: 0,
  },
  text: { padding: 3, color: colors.textDim, fontSize: 10 },
  icon: {
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    padding: { default: 5, [breakpoints.tiny]: 3 },
    borderRadius: 3,
    color: colors.textDim,
  },
  chosen: { color: colors.text, backgroundColor: { default: colors.control, ':hover': colors.control } },
  allow: {
    borderWidth: 1,
    borderStyle: 'solid',
    borderColor: 'transparent',
    fontSize: 10,
    whiteSpace: 'nowrap',
    borderRadius: 3,
    // Big enough to tap on a phone.
    paddingBlock: { default: 3, [breakpoints.narrow]: 9 },
    paddingInline: { default: 7, [breakpoints.narrow]: 16 },
    color: colors.onLight,
    backgroundColor: { default: colors.brand, ':hover': colors.brandStrong },
  },
  deny: { marginInline: 6, fontSize: 10, color: colors.textMuted, paddingBlock: { default: 3, [breakpoints.narrow]: 9 }, paddingInline: { default: 3, [breakpoints.narrow]: 14 } },
  send: {
    width: 28,
    height: 28,
    display: 'grid',
    placeItems: 'center',
    borderRadius: 3,
    color: colors.onLight,
    backgroundColor: { default: colors.light, ':hover': colors.lightHover },
  },
  menu: {
    display: 'block',
    width: '100%',
    textAlign: 'left',
    paddingBlock: 9,
    paddingInline: 10,
    fontSize: 11,
    color: colors.textSoft,
    borderRadius: 3,
  },
});

// ---- status dot ------------------------------------------------------------
const pulse = stylex.keyframes({
  '0%': { transform: 'scale(.65)', opacity: 0.9 },
  '85%': { transform: 'scale(1.9)', opacity: 0 },
  '100%': { transform: 'scale(1.9)', opacity: 0 },
});

export function StatusDot({ status, label, title, size = 7 }: { status: Status | string; label?: string; title?: string; size?: number }) {
  const color = dotColors[status as keyof typeof dotColors] ?? dots.idle;
  return (
    <span role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : true} title={title} data-status={status}
      {...stylex.props(dots.base, dots.size(size), color, status === 'waiting' && dots.waiting)} />
  );
}

const dots = stylex.create({
  base: { display: 'inline-block', borderRadius: '50%', flexShrink: 0, verticalAlign: 'middle', position: 'relative' },
  size: (size: number) => ({ width: size, height: size }),
  idle: { backgroundColor: colors.idle },
  running: { backgroundColor: colors.running },
  complete: { backgroundColor: colors.complete },
  waitingColor: { backgroundColor: colors.waiting, color: colors.waiting },
  waiting: {
    '::after': {
      content: '""',
      position: 'absolute',
      inset: -3,
      borderWidth: 1.5,
      borderStyle: 'solid',
      borderColor: 'currentColor',
      borderRadius: '50%',
      pointerEvents: 'none',
      animationName: { default: pulse, [breakpoints.reducedMotion]: 'none' },
      animationDuration: '1.5s',
      animationTimingFunction: 'ease-out',
      animationIterationCount: 'infinite',
      opacity: { default: null, [breakpoints.reducedMotion]: 0.6 },
    },
  },
});
const dotColors = { running: dots.running, complete: dots.complete, waiting: dots.waitingColor, paused: dots.idle, idle: dots.idle };

// ---- chips and small text --------------------------------------------------
export function Chip({ children, sx }: { children: ReactNode; sx?: StyleXStyles }) {
  return <span {...stylex.props(chips.base, sx)}>{children}</span>;
}
const chips = stylex.create({
  base: { display: 'inline-flex', alignItems: 'center', gap: 6, backgroundColor: colors.chip, borderRadius: 3, paddingBlock: 3, paddingInline: 6, fontSize: 10, color: colors.textMuted, maxWidth: '100%' },
});

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd {...stylex.props(kbd.base)}>{children}</kbd>;
}
const kbd = stylex.create({
  base: { paddingBlock: 1, paddingInline: 4, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineSoft, borderRadius: 3, backgroundColor: colors.card, fontFamily: 'inherit', fontSize: 9, color: colors.textDim },
});

// Common text styles, for use with stylex.props in other components.
export const text = stylex.create({
  muted: { color: colors.textFaint },
  faint: { color: colors.textGhost, fontSize: 10 },
  mono: { fontFamily: 'ui-monospace, monospace' },
  ellipsis: { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', minWidth: 0 },
});

// Info is a small round (i) that shows a hint as a tooltip on hover and on
// keyboard focus (global.css: .x-info); screen readers read it as its label.
export function Info({ text }: { text: string }) {
  // Before showing, flip the bubble leftward when 240px would not fit to the
  // right inside the nearest dialog or panel (or the window).
  const place = (event: { currentTarget: HTMLButtonElement }) => {
    const el = event.currentTarget;
    const box = el.closest('dialog, [data-panel-content], #side')?.getBoundingClientRect();
    const right = box ? box.right : window.innerWidth;
    el.toggleAttribute('data-tip-flip', el.getBoundingClientRect().left + 240 > right - 8);
  };
  return <button type="button" className="x-info" data-tip={text} aria-label={text} onPointerEnter={place} onFocus={place} onClick={event => event.preventDefault()}>i</button>;
}
