import * as stylex from '@stylexjs/stylex';

// The palette: a dark neutral UI with one brand color for "needs you" and
// interactive emphasis. Greys go from strong (text) to faint (hints).
export const colors = stylex.defineVars({
  bg: '#111111',
  card: '#1b1b1b',
  sunken: '#181818',
  raised: '#222222',
  panelHead: '#202020',
  chip: '#282828',
  selected: '#292929',
  attached: '#242424',
  menu: '#232323',
  input: '#161616',
  hover: 'rgba(255,255,255,0.03)',

  line: '#333333',
  lineSoft: '#2d2d2d',
  lineFaint: '#262626',
  lineStrong: '#444444',

  textStrong: '#eeeeee',
  text: '#dddddd',
  textSoft: '#cccccc',
  textMuted: '#aaaaaa',
  textDim: '#999999',
  textFaint: '#888888',
  textGhost: '#777777',

  brand: '#a78bfa',
  brandStrong: '#c4b5fd',
  brandSoft: 'rgba(167,139,250,0.12)',
  brandLine: 'rgba(167,139,250,0.45)',
  brandGlow: '#f3efff',

  running: '#20e878',
  complete: '#389cff',
  waiting: '#ffb800',
  idle: '#888888',

  danger: '#ee8888',
  warn: '#c98a5a',
  effectRead: '#7fb88a',
  effectWrite: '#d6b25e',

  light: '#dddddd',
  lightHover: '#eeeeee',
  onLight: '#222222',
  shadow: 'rgba(0,0,0,0.55)',
});

export const fonts = stylex.defineVars({
  sans: 'Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
  mono: 'ui-monospace, SFMono-Regular, Consolas, monospace',
});

export const breakpoints = stylex.defineConsts({
  narrow: '@media (max-width: 600px)',
  tablet: '@media (max-width: 760px)',
  wide: '@media (min-width: 761px)',
  tiny: '@media (max-width: 440px)',
  reducedMotion: '@media (prefers-reduced-motion: reduce)',
});
