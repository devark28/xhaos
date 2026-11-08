// Durations for "dangerously allow": <n><unit> parts such as 10s, 15m, 2h, 1d
// or 1h30m, between 10 seconds and 7 days. The server checks the same rules
// (package danger); this lets the dialog say what is wrong before sending.

export const DURATION_PRESETS = ['15m', '1h', '4h', '1d'] as const;
export const MIN_SECONDS = 10;
export const MAX_SECONDS = 7 * 24 * 3600;
// The normal limit on model calls in a row, and the highest a session may set.
export const DEFAULT_CALLS = 40;
export const MAX_CALLS = 1000;

const UNIT: Record<string, number> = { s: 1, m: 60, h: 3600, d: 86400 };

// parseDurationIn returns the length in seconds, or why the text is not one,
// for a total from min to max seconds (range: how the error names them).
export function parseDurationIn(text: string, min: number, max: number, range: string): { seconds: number } | { error: string } {
  const t = text.trim();
  const parts = [...t.matchAll(/(\d+)\s*([smhd])/gi)];
  if (!t || !parts.length || t.replace(/(\d+)\s*([smhd])/gi, '').trim() !== '') {
    return { error: 'Write a number and a unit: 10s, 15m, 2h or 1d.' };
  }
  let seconds = 0;
  for (const [, n, unit] of parts) {
    if (n.length > 6) return { error: 'That is too long.' };
    seconds += Number(n) * UNIT[unit.toLowerCase()];
  }
  if (seconds < min || seconds > max) return { error: `Choose between ${range}.` };
  return { seconds };
}

// parseDuration: the "dangerously allow" range, 10 seconds to 7 days.
export const parseDuration = (text: string) => parseDurationIn(text, MIN_SECONDS, MAX_SECONDS, '10 seconds and 7 days');

// formatDuration writes seconds the short way: 45s, 15m, 1h 30m, 2d 4h.
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  if (d) return h ? `${d}d ${h}h` : `${d}d`;
  if (h) return m ? `${h}h ${m}m` : `${h}h`;
  return `${m}m`;
}

// callsSetting is what the server stores for the session's call limit: 0
// keeps the normal limit, -1 lifts it, else the number.
export function callsSetting(limitOn: boolean, calls: number): number {
  if (!limitOn) return -1;
  return calls === DEFAULT_CALLS ? 0 : calls;
}

// callsValid reports whether a typed call limit can be sent.
export const callsValid = (calls: number) => Number.isInteger(calls) && calls >= 1 && calls <= MAX_CALLS;
