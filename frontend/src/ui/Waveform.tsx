// The dictation waveform: a thin line across the composer, between the model
// picker and the mic button. It is a flex item in the composer's bottom row,
// so the browser lays it out (at any text size or browser zoom); only its
// bitmap follows its size. Hidden when idle. While listening it strokes in
// from the right, stays flat in silence, and sends a bump leftward for each
// burst of sound, sized by the input level after automatic gain. While the
// take is transcribed it breathes slowly in place. One canvas draw per frame;
// the per-frame data never touches React or the store.
import { useEffect, useLayoutEffect, useRef } from 'react';
import * as stylex from '@stylexjs/stylex';
import { useApp } from './state/store.ts';
import { levelAnalyser } from './state/dictation.ts';
import { gainTracker } from '../dictation-format.ts';
import { colors } from './theme/tokens.stylex.ts';

// ---- style choices: edit these to experiment ----------------------------------
const LINES = 1;              // lines drawn; extra lines lag the first (start with 1)
const LINE_PHASE = 0.06;      // each extra line lags by this fraction of the width
const LINE_ALPHA = 0.45;      // opacity of the extra lines
const LINE_WIDTH = 0.75;      // CSS px; below 1 is fine on high-DPI screens
const SHAPE: 'curved' | 'sharp' = 'curved'; // bump profile: a smooth arch, or a sharp peak
const HARMONICS: readonly number[] = [];    // ripple on the bump: [0.25] adds a 2nd harmonic
const BUMP_WIDTH = 0.12;      // bump width, as a fraction of the line
const BUMP_SPEED = 0.45;      // widths per second, right to left
const BUMP_EVERY_MS = 220;    // while there is sound, at most one bump this often
const MAX_BUMPS = 6;          // bumps on the line at once
const SOUND_FLOOR = 0.012;    // RMS below this counts as silence (before the gain)
const LEVEL_GAIN = 3;         // gained RMS to bump height; the gain itself is in dictation-format.ts
const AMPLITUDE_MAX = 0.9;    // the tallest bump, as a fraction of the half-height: loud speech stops here
const BUSY_AMP = 0.35;        // the breathing line while transcribing, as a fraction of the half-height
const BUSY_PERIOD_MS = 1600;  // one breath
const HEIGHT = 22;            // CSS px of the band
const DRAW_MS = 400;          // the draw-on at start
const GAP = 6;                // CSS px between the line and the controls beside it

interface Bump { at: number; amp: number; sign: number }

// profile is a bump's height at x, in bump widths from its centre (zero beyond 1).
function profile(x: number): number {
  if (Math.abs(x) >= 1) return 0;
  const base = SHAPE === 'curved' ? 0.5 * (1 + Math.cos(Math.PI * x)) : 1 - Math.abs(x);
  let ripple = 1;
  HARMONICS.forEach((a, i) => { ripple += a * Math.cos((i + 2) * Math.PI * x); });
  return base * ripple;
}

export function Waveform() {
  const recording = useApp(s => s.recording);
  const transcribing = useApp(s => s.transcribing);
  const shown = recording || transcribing;
  const canvas = useRef<HTMLCanvasElement>(null);
  // Whether the mic is live. The loop reads it each frame, so the line stays
  // up when recording stops and only the motion changes.
  const live = useRef(false);
  useEffect(() => { live.current = recording; }, [recording]);

  // The bitmap matches the canvas's size in device pixels, so the line stays
  // sharp; the layout itself is CSS's.
  useLayoutEffect(() => {
    if (!shown) return;
    const el = canvas.current!;
    // On-screen size (CSS zoom included) times the screen's density (browser
    // zoom included): the device pixels the line covers.
    const fit = () => {
      const r = el.getBoundingClientRect(), d = globalThis.devicePixelRatio || 1;
      const w = Math.max(1, Math.round(r.width * d)), h = Math.max(1, Math.round(r.height * d));
      if (el.width !== w) el.width = w;
      if (el.height !== h) el.height = h;
    };
    fit();
    const watch = new ResizeObserver(fit);
    watch.observe(el);
    // Browser zoom and moving to another screen change the density alone.
    let density: MediaQueryList | null = null;
    const onDensity = () => { density?.removeEventListener('change', onDensity); fit(); density = matchMedia(`(resolution: ${globalThis.devicePixelRatio || 1}dppx)`); density.addEventListener('change', onDensity); };
    onDensity();
    return () => { watch.disconnect(); density?.removeEventListener('change', onDensity); };
  }, [shown]);

  // The loop runs only while the line is shown, and only while the page is visible.
  useEffect(() => {
    if (!shown) return;
    const el = canvas.current!;
    const ctx = el.getContext('2d');
    const analyser = levelAnalyser();
    if (!ctx) return;
    const reduced = globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches || document.documentElement.dataset.motion === 'reduce';
    const samples = new Float32Array(analyser?.fftSize ?? 1024);
    const gainOf = gainTracker(SOUND_FLOOR);
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
      // Bitmap pixels per CSS pixel: the screen's density times any zoom.
      const w = el.width, h = el.height, dpr = el.width / Math.max(1, el.clientWidth);
      const listening = live.current;
      const level = listening ? rms() : 0;
      const gain = gainOf(level);
      if (listening && !reduced && level > SOUND_FLOOR && now - lastBump >= BUMP_EVERY_MS && bumps.length < MAX_BUMPS) {
        bumps.push({ at: now, amp: Math.min(AMPLITUDE_MAX, level * gain * LEVEL_GAIN), sign: count++ % 2 ? -1 : 1 });
        lastBump = now;
      }
      bumps = bumps.filter(b => centre(b, now) > -BUMP_WIDTH);
      ctx.clearRect(0, 0, w, h);
      // Read every frame, so a theme or accent change shows at once.
      const color = getComputedStyle(el).color;
      ctx.strokeStyle = color; ctx.fillStyle = color;
      ctx.lineWidth = LINE_WIDTH * dpr; ctx.lineJoin = 'round';

      // The line strokes in from the right over DRAW_MS (at once with reduced motion).
      const grow = reduced ? 1 : Math.min(1, (now - started) / DRAW_MS);
      const shownFrac = 1 - (1 - grow) ** 3;
      const x0 = w * (1 - shownFrac);
      const mid = h / 2;
      // While transcribing there are no bumps: the whole line breathes in place.
      const breath = listening ? 0 : BUSY_AMP * (reduced ? 0.5 : 0.5 + 0.5 * Math.sin((2 * Math.PI * now) / BUSY_PERIOD_MS));
      const n = Math.max(2, Math.round((w - x0) / (2 * dpr)));
      for (let k = 0; k < LINES; k++) {
        ctx.globalAlpha = k ? LINE_ALPHA : 1;
        ctx.beginPath();
        for (let i = 0; i <= n; i++) {
          const x = x0 + ((w - x0) * i) / n;
          const f = x / w;
          let dy = breath * Math.sin(2 * Math.PI * f);
          for (const b of bumps) dy += b.sign * b.amp * profile((f - (centre(b, now) + k * LINE_PHASE)) / BUMP_WIDTH);
          const y = mid - dy * (h / 2);
          if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
        }
        ctx.stroke();
      }
      ctx.globalAlpha = 1;

      // Reduced motion: no bumps travel; one meter bar in place shows the level.
      if (reduced && listening) {
        meter = Math.max(Math.min(1, level * gain * LEVEL_GAIN), meter * 0.92);
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
  }, [shown]);

  return <canvas ref={canvas} hidden={!shown} aria-hidden="true" {...stylex.props(wave.root)} />;
}

const wave = stylex.create({
  root: {
    // Width and basis 0, grown into the free space: a canvas's natural width
    // is its bitmap's, which follows the canvas, so letting it count would
    // widen the row (and the composer) every time the bitmap is resized.
    width: 0,
    flexGrow: 1,
    flexShrink: 1,
    flexBasis: 0,
    minWidth: 0,
    height: HEIGHT,
    marginInline: GAP,
    pointerEvents: 'none',
    color: colors.brand,
  },
});
