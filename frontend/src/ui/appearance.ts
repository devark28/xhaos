// Appearance settings, kept per browser: text size, theme, accent, density,
// code font, transcript size, motion, contrast, code wrapping, timestamps.
//
// Colours are StyleX variables (theme/tokens.stylex.ts); the dark palette is
// their default. Another theme, the accent, and high contrast set those
// variables on <html>, by the names StyleX gave them. Global CSS (Markdown,
// syntax colours) reads plain --x-* variables set the same way. Sizes use
// CSS zoom, so every fixed pixel size scales together.
import { useEffect, useState } from 'react';
import { colors, fonts } from './theme/tokens.stylex.ts';

export interface Appearance {
  scale: number; // 0.9 · 1 · 1.15 · 1.3
  theme: 'dark' | 'light' | 'system';
  accent: 'violet' | 'blue' | 'green' | 'amber' | 'pink';
  density: 'compact' | 'comfortable';
  mono: 'system' | 'jetbrains' | 'fira' | 'plex';
  transcript: number; // transcript text, relative to the rest
  motion: 'full' | 'reduce';
  contrast: 'normal' | 'high';
  wrap: 'wrap' | 'scroll';
  time: 'relative' | 'exact';
  mentions: 'attach' | 'text'; // typing "#12 " in the composer attaches agent 12, or stays text
}

export const defaults: Appearance = { scale: 1, theme: 'dark', accent: 'violet', density: 'compact', mono: 'jetbrains', transcript: 1, motion: 'full', contrast: 'normal', wrap: 'wrap', time: 'exact', mentions: 'attach' };
const KEY = 'xhaos-appearance';

export function loadAppearance(): Appearance {
  try { return { ...defaults, ...JSON.parse(localStorage.getItem(KEY) || '{}') }; } catch { return { ...defaults }; }
}
export function saveAppearance(a: Appearance) {
  try { localStorage.setItem(KEY, JSON.stringify(a)); } catch { /* private window: this visit only */ }
}

type Palette = Record<string, string>; // token name -> value

const light: Palette = {
  bg: '#f6f6f4', card: '#ffffff', sunken: '#efefed', raised: '#fafaf9', panelHead: '#f1f1ef', chip: '#e9e9e7', selected: '#e6e6e3', attached: '#ecebf3',
  menu: '#ffffff', input: '#ffffff', hover: 'rgba(0,0,0,0.035)', control: '#e2e2df',
  line: '#d9d9d6', lineSoft: '#e1e1de', lineFaint: '#eaeae7', lineStrong: '#cacac6', lineBright: '#b5b5b0',
  textStrong: '#111111', text: '#1f1f1f', textSoft: '#2e2e2e', textSub: '#3d3d3d', textMuted: '#4d4d4d', textDim: '#5c5c5c', textFaint: '#6b6b6b', textGhost: '#7a7a7a', textHint: '#8f8f8f',
  running: '#0f9a4a', complete: '#1c6fd1', waiting: '#b97a00', idle: '#8a8a8a',
  danger: '#c23b3b', warn: '#a8622a', effectRead: '#2f7a40', effectWrite: '#8a6a12',
  light: '#262626', lightHover: '#000000', onLight: '#ffffff', shadow: 'rgba(0,0,0,0.14)',
};
const highContrast: Record<'dark' | 'light', Palette> = {
  dark: { text: '#f2f2f2', textSoft: '#e6e6e6', textSub: '#dadada', textMuted: '#cfcfcf', textDim: '#c4c4c4', textFaint: '#b8b8b8', textGhost: '#acacac', textHint: '#a0a0a0', line: '#5a5a5a', lineSoft: '#505050', lineFaint: '#454545', lineStrong: '#6a6a6a' },
  light: { text: '#000000', textSoft: '#111111', textSub: '#1a1a1a', textMuted: '#262626', textDim: '#333333', textFaint: '#3d3d3d', textGhost: '#474747', textHint: '#555555', line: '#9a9a9a', lineSoft: '#a6a6a6', lineFaint: '#b3b3b3', lineStrong: '#8a8a8a' },
};
// brand, brandStrong, brandSoft (fill), brandLine (border), brandGlow, per theme.
const accents: Record<Appearance['accent'], { dark: string[]; light: string[] }> = {
  violet: { dark: ['#a78bfa', '#c4b5fd', 'rgba(167,139,250,0.12)', 'rgba(167,139,250,0.45)', '#f3efff'], light: ['#6d4ce6', '#5634d4', 'rgba(109,76,230,0.10)', 'rgba(109,76,230,0.45)', '#2b1a73'] },
  blue: { dark: ['#7cb3ff', '#a9cdff', 'rgba(124,179,255,0.12)', 'rgba(124,179,255,0.45)', '#eef5ff'], light: ['#2563d9', '#1a4fb8', 'rgba(37,99,217,0.10)', 'rgba(37,99,217,0.45)', '#0d2a66'] },
  green: { dark: ['#6fd69a', '#a3e8bd', 'rgba(111,214,154,0.12)', 'rgba(111,214,154,0.45)', '#effcf4'], light: ['#16884a', '#0f6d3a', 'rgba(22,136,74,0.10)', 'rgba(22,136,74,0.45)', '#073a1e'] },
  amber: { dark: ['#f5b956', '#f9d28f', 'rgba(245,185,86,0.12)', 'rgba(245,185,86,0.45)', '#fff7e8'], light: ['#b36b00', '#8f5500', 'rgba(179,107,0,0.10)', 'rgba(179,107,0,0.45)', '#4a2c00'] },
  pink: { dark: ['#f58bc8', '#f9b5dc', 'rgba(245,139,200,0.12)', 'rgba(245,139,200,0.45)', '#fff0f8'], light: ['#c72f86', '#a3246c', 'rgba(199,47,134,0.10)', 'rgba(199,47,134,0.45)', '#5c1440'] },
};
// Global CSS (Markdown, syntax colours) per theme.
const globals: Record<'dark' | 'light', Record<string, string>> = {
  dark: { bg: '#111111', text: '#dedede', heading: '#dddddd', quote: '#aaaaaa', link: '#9cbde0', code: '#c5b4df', keyword: '#c4a7e7', string: '#a8c49b', number: '#deb98c', function: '#8eb8dc', comment: '#777777', addition: '#9fc79f', deletion: '#d49e9e', pre: '#dddddd', 'table-line': '#333333', 'table-head': '#1b1b1b' },
  light: { bg: '#f6f6f4', text: '#1f1f1f', heading: '#111111', quote: '#555555', link: '#1f5fae', code: '#6a3fb5', keyword: '#7b3fbf', string: '#3c7a2e', number: '#a0581a', function: '#1f6aa8', comment: '#7a7a7a', addition: '#2f7a40', deletion: '#b23b3b', pre: '#1f1f1f', 'table-line': '#d9d9d6', 'table-head': '#efefed' },
};
// The named fonts are bundled (fonts.css); "system" is the OS's own.
const monoStacks: Record<Appearance['mono'], string> = {
  system: 'ui-monospace, SFMono-Regular, Consolas, monospace',
  jetbrains: '"JetBrains Mono", ui-monospace, monospace',
  fira: '"Fira Code", ui-monospace, monospace',
  plex: '"IBM Plex Mono", ui-monospace, monospace',
};

const varName = (ref: string) => /var\((--[^),\s]+)/.exec(ref)?.[1];
const set = (style: CSSStyleDeclaration, key: string, value: string | null) => (value === null ? style.removeProperty(key) : style.setProperty(key, value));

let systemQuery: MediaQueryList | null = null;

// applyAppearance puts a onto the page (and keeps "system" in step).
export function applyAppearance(a: Appearance = loadAppearance()) {
  const root = document.documentElement, style = root.style;
  const mode: 'dark' | 'light' = a.theme === 'system' ? (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark') : a.theme;
  // Start from the defaults (dark), then layer theme, contrast, accent.
  const palette: Palette = { ...(mode === 'light' ? light : {}), ...(a.contrast === 'high' ? highContrast[mode] : {}) };
  const [brand, brandStrong, brandSoft, brandLine, brandGlow] = accents[a.accent][mode];
  if (a.accent !== 'violet' || mode === 'light') Object.assign(palette, { brand, brandStrong, brandSoft, brandLine, brandGlow });
  for (const [key, ref] of Object.entries(colors as unknown as Record<string, unknown>)) {
    if (key.startsWith('__')) continue;
    const name = varName(String(ref));
    if (name) set(style, name, palette[key] ?? null);
  }
  for (const [key, value] of Object.entries(globals[mode])) style.setProperty(`--x-${key}`, value);
  style.setProperty('--x-brand', brand);
  style.colorScheme = mode;
  const mono = monoStacks[a.mono];
  const monoVar = varName(String(fonts.mono));
  if (monoVar) set(style, monoVar, mono);
  style.setProperty('--x-mono', mono);
  style.setProperty('--x-zoom', String(a.scale));
  style.setProperty('--x-transcript-zoom', String(a.transcript));
  Object.assign(root.dataset, { theme: mode, density: a.density, motion: a.motion, wrap: a.wrap, contrast: a.contrast });
  if (a.theme === 'system' && !systemQuery) {
    systemQuery = matchMedia('(prefers-color-scheme: light)');
    systemQuery.addEventListener('change', () => { if (loadAppearance().theme === 'system') applyAppearance(); });
  }
}

// useThemeMode follows the applied theme ("dark" or "light"), for widgets
// that take a theme of their own (the Excalidraw canvas).
export function useThemeMode(): 'dark' | 'light' {
  const read = () => (document.documentElement.dataset.theme === 'light' ? 'light' : 'dark');
  const [mode, setMode] = useState<'dark' | 'light'>(read);
  useEffect(() => {
    const watch = new MutationObserver(() => setMode(read()));
    watch.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => watch.disconnect();
  }, []);
  return mode;
}

// formatTime renders a timestamp per the setting: "14:05" (today), "Nov 3 14:05", or "2m ago".
export function formatTime(iso: string, mode: Appearance['time'] = loadAppearance().time, now = Date.now()): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  if (mode === 'relative') {
    const s = Math.max(0, Math.round((now - at.getTime()) / 1000));
    if (s < 45) return 'just now';
    if (s < 3600) return `${Math.round(s / 60)}m ago`;
    if (s < 86400) return `${Math.round(s / 3600)}h ago`;
    return `${Math.round(s / 86400)}d ago`;
  }
  const time = at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return new Date(now).toDateString() === at.toDateString() ? time : `${at.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${time}`;
}
