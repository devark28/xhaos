// The dictation waveform: a thin line across the composer, between the model
// picker and the mic button. Hidden when idle. While listening it strokes in
// from the right, stays flat in silence, and sends a bump leftward for each
// burst of sound, sized by the input level. One canvas draw per frame; the
// per-frame data never touches React or the store.
import { useEffect, useLayoutEffect, useRef } from 'react';
import * as stylex from '@stylexjs/stylex';
import { useApp } from './state/store.ts';
import { levelAnalyser } from './state/dictation.ts';
import { colors } from './theme/tokens.stylex.ts';

// ---- style choices: edit these to experiment ----------------------------------
const LINES = 1;              // lines drawn; extra lines lag the first (start with 1)
const LINE_PHASE = 0.06;      // each extra line lags by this fraction of the width
const LINE_ALPHA = 0.45;      // opacity of the extra lines
const SHAPE: 'curved' | 'sharp' = 'curved'; // bump profile: a smooth arch, or a sharp peak
const HARMONICS: readonly number[] = [];    // ripple on the bump: [0.25] adds a 2nd harmonic
const BUMP_WIDTH = 0.12;      // bump width, as a fraction of the line
const BUMP_SPEED = 0.45;      // widths per second, right to left
const BUMP_EVERY_MS = 220;    // while there is sound, at most one bump this often
const MAX_BUMPS = 6;          // bumps on the line at once
const SOUND_FLOOR = 0.012;    // RMS below this counts as silence
const LEVEL_GAIN = 4;         // RMS to bump height (before the cap)
const AMPLITUDE_MAX = 0.9;    // the tallest bump, as a fraction of the half-height
const HEIGHT = 22;            // CSS px of the band
const DRAW_MS = 400;          // the draw-on at start
const GAP = 6;                // CSS px between the line and each control

interface Bump { at: number; amp: number; sign: number }

// profile is a bump's height at x, in bump widths from its centre (zero beyond 1).
function profile(x: number): number {
  if (Math.abs(x) >= 1) return 0;
  const base = SHAPE === 'curved' ? 0.5 * (1 + Math.cos(Math.PI * x)) : 1 - Math.abs(x);
  let ripple = 1;
  HARMONICS.forEach((a, i) => { ripple += a * Math.cos((i + 2) * Math.PI * x); });
  return base * ripple;
}

// place sizes the canvas to the span from the model picker's right edge (or the
// attach button when the picker is hidden) to the mic button's left edge, and
// centres it on the mic. In the DOM the picker comes before the mic, so the
// span runs left to right across the toolbar.
function place(canvas: HTMLCanvasElement) {
  const form = canvas.parentElement!;
  const mic = document.getElementById('dictate');
  const picker = document.getElementById('model-pick');
  const from = picker && picker.getClientRects().length ? picker : form.querySelector<HTMLElement>('[data-pick]');
  if (!mic || !from) return;
  const f = form.getBoundingClientRect(), a = from.getBoundingClientRect(), m = mic.getBoundingClientRect();
  const left = a.right - f.left + GAP, width = m.left - f.left - GAP - left;
  if (width < 24) { canvas.style.width = '0px'; return; }
  const middle = m.top + m.height / 2 - f.top;
  const dpr = globalThis.devicePixelRatio || 1;
  Object.assign(canvas.style, { left: `${left}px`, top: `${middle - HEIGHT / 2}px`, width: `${width}px`, height: `${HEIGHT}px` });
  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(HEIGHT * dpr);
}

export function Waveform() {
  const recording = useApp(s => s.recording);
  const canvas = useRef<HTMLCanvasElement>(null);

  // Keep the span right while the composer, the picker or the mic change size.
  useLayoutEffect(() => {
    if (!recording) return;
    const el = canvas.current!;
    place(el);
    const watch = new ResizeObserver(() => place(el));
    for (const target of [el.parentElement, document.getElementById('model-pick'), document.getElementById('dictate')]) if (target) watch.observe(target);
    return () => watch.disconnect();
  }, [recording]);

  // The loop runs only while listening, and only while the page is visible.
  useEffect(() => {
    if (!recording) return;
    const el = canvas.current!;
    const ctx = el.getContext('2d');
    const analyser = levelAnalyser();
    if (!ctx) return;
    const reduced = globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches || document.documentElement.dataset.motion === 'reduce';
    const color = getComputedStyle(el).color;
    const samples = new Float32Array(analyser?.fftSize ?? 1024);
    const started = performance.now();
    let bumps: Bump[] = [], lastBump = -Infinity, count = 0, meter = 0, frame = 0;

    const rms = (): number => {
      if (!analyser) return 0;
      analyser.getFloatTimeDomainData(samples);
      let sum = 0;
      for (const v of samples) sum += v * v;
      return Math.sqrt(sum / samples.length);
    };
    const centre = (b: Bump, now: number) => 1 - ((now - b.at) / 1000) * BUMP_SPEED;

    const paint = (now: number) => {
      const w = el.width, h = el.height, dpr = globalThis.devicePixelRatio || 1;
      const level = rms();
      if (!reduced && level > SOUND_FLOOR && now - lastBump >= BUMP_EVERY_MS && bumps.length < MAX_BUMPS) {
        bumps.push({ at: now, amp: Math.min(AMPLITUDE_MAX, level * LEVEL_GAIN), sign: count++ % 2 ? -1 : 1 });
        lastBump = now;
      }
      bumps = bumps.filter(b => centre(b, now) > -BUMP_WIDTH);
      ctx.clearRect(0, 0, w, h);
      ctx.strokeStyle = color; ctx.fillStyle = color;
      ctx.lineWidth = Math.max(1, dpr); ctx.lineJoin = 'round';

      // The line strokes in from the right over DRAW_MS (at once with reduced motion).
      const grow = reduced ? 1 : Math.min(1, (now - started) / DRAW_MS);
      const shown = 1 - (1 - grow) ** 3;
      const x0 = w * (1 - shown);
      const mid = h / 2;
      const n = Math.max(2, Math.round((w - x0) / (2 * dpr)));
      for (let k = 0; k < LINES; k++) {
        ctx.globalAlpha = k ? LINE_ALPHA : 1;
        ctx.beginPath();
        for (let i = 0; i <= n; i++) {
          const x = x0 + ((w - x0) * i) / n;
          const f = x / w;
          let dy = 0;
          for (const b of bumps) dy += b.sign * b.amp * profile((f - (centre(b, now) + k * LINE_PHASE)) / BUMP_WIDTH);
          const y = mid - dy * (h / 2);
          if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
        }
        ctx.stroke();
      }
      ctx.globalAlpha = 1;

      // Reduced motion: no bumps travel; one meter bar in place shows the level.
      if (reduced) {
        meter = Math.max(Math.min(1, level * LEVEL_GAIN), meter * 0.92);
        const bar = Math.max(1, meter * h);
        ctx.fillRect(w / 2 - dpr, mid - bar / 2, 2 * dpr, bar);
      }
    };

    const tick = (now: number) => { paint(now); frame = requestAnimationFrame(tick); };
    const onVisibility = () => {
      cancelAnimationFrame(frame); frame = 0;
      if (!document.hidden) frame = requestAnimationFrame(tick);
    };
    onVisibility();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      document.removeEventListener('visibilitychange', onVisibility);
      cancelAnimationFrame(frame);
      ctx.clearRect(0, 0, el.width, el.height);
    };
  }, [recording]);

  return <canvas ref={canvas} hidden={!recording} aria-hidden="true" {...stylex.props(wave.root)} />;
}

const wave = stylex.create({
  root: {
    position: 'absolute',
    pointerEvents: 'none',
    color: colors.brand,
  },
});
