// A published channel's link: how long it lasts (chosen when publishing) and
// what the page says about it. The server enforces all of it (package
// channel); this asks, parses and words it, with the same rules.
import { parseDurationIn, formatDuration } from './duration.ts';

// The choices when publishing. "No expiry" is separate: it is never
// preselected, and the server refuses a publish that names neither.
export const LINK_PRESETS = [
  { id: '1h', label: '1 h' },
  { id: '1d', label: '1 day' },
  { id: '7d', label: '7 days' },
] as const;
export const DEFAULT_LINK_PRESET = '1d';
export const LINK_MIN_SECONDS = 10 * 60;
export const LINK_MAX_SECONDS = 30 * 24 * 3600;

// parseLinkDuration: a custom duration, 10 minutes to 30 days.
export const parseLinkDuration = (text: string) => parseDurationIn(text, LINK_MIN_SECONDS, LINK_MAX_SECONDS, '10 minutes and 30 days');

export type LinkState = { kind: 'off' } | { kind: 'open' } | { kind: 'left'; ms: number } | { kind: 'expired' };

// linkState: not published; published with no expiry; published with this
// long left; or published but past its expiry (the link refuses everyone
// until the owner publishes again or unpublishes).
export function linkState(c: { published?: boolean; expiresAt?: string }, now = Date.now()): LinkState {
  if (!c.published) return { kind: 'off' };
  if (!c.expiresAt) return { kind: 'open' };
  const ms = new Date(c.expiresAt).getTime() - now;
  return ms > 0 ? { kind: 'left', ms } : { kind: 'expired' };
}

// leftText says how long a link has left, like the Danger badge does.
export const leftText = (ms: number) => `${formatDuration(ms / 1000)} left`;

// linkTag is the list's tag: "published", "published · 3h left" or "link expired".
export function linkTag(c: { published?: boolean; expiresAt?: string }, now = Date.now()): string {
  const state = linkState(c, now);
  if (state.kind === 'off') return '';
  if (state.kind === 'expired') return 'link expired';
  return state.kind === 'left' ? `published · ${leftText(state.ms)}` : 'published';
}

// expiryNote is the passphrase box's line about when the link stops.
export function expiryNote(expiresAt: string | undefined, now = Date.now(), at = (d: Date) => d.toLocaleString()): string {
  if (!expiresAt) return 'This link does not expire. Unpublish it to stop it.';
  const ms = new Date(expiresAt).getTime() - now;
  return ms > 0 ? `This link expires in ${formatDuration(ms / 1000)}, at ${at(new Date(expiresAt))}.` : 'This link has expired.';
}
