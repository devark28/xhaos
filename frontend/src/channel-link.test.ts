import { test } from 'node:test';
import assert from 'node:assert/strict';
import { linkState, linkTag, leftText, expiryNote, parseLinkDuration, LINK_PRESETS, DEFAULT_LINK_PRESET } from './channel-link.ts';
import { parseDuration } from './duration.ts';

const now = Date.parse('2030-01-01T12:00:00Z');
const at = (ms: number) => new Date(now + ms).toISOString();
const H = 3600_000;

test('the list says how long a link has left', () => {
  assert.equal(linkTag({}, now), '');
  assert.equal(linkTag({ published: true }, now), 'published');
  assert.equal(linkTag({ published: true, expiresAt: at(3 * H) }, now), 'published · 3h left');
  assert.equal(linkTag({ published: true, expiresAt: at(90 * 60_000) }, now), 'published · 1h 30m left');
  assert.equal(linkTag({ published: true, expiresAt: at(26 * H) }, now), 'published · 1d 2h left');
  assert.equal(linkTag({ published: true, expiresAt: at(45_000) }, now), 'published · 45s left');
  assert.equal(leftText(7 * 24 * H), '7d left');
});

test('a link past its expiry reads "link expired", at the expiry itself too', () => {
  assert.equal(linkTag({ published: true, expiresAt: at(-1) }, now), 'link expired');
  assert.equal(linkTag({ published: true, expiresAt: at(0) }, now), 'link expired');
  assert.deepEqual(linkState({ published: true, expiresAt: at(-H) }, now), { kind: 'expired' });
  assert.deepEqual(linkState({ published: true, expiresAt: at(H) }, now), { kind: 'left', ms: H });
  assert.deepEqual(linkState({ published: true }, now), { kind: 'open' });
  assert.deepEqual(linkState({ expiresAt: at(H) }, now), { kind: 'off' });
});

test('the passphrase box says when the link expires', () => {
  const when = () => 'the day';
  assert.equal(expiryNote(at(24 * H), now, when), 'This link expires in 1d, at the day.');
  assert.equal(expiryNote(undefined, now, when), 'This link does not expire. Unpublish it to stop it.');
  assert.equal(expiryNote(at(-1), now, when), 'This link has expired.');
});

test('a custom link duration is 10 minutes to 30 days, unlike "dangerously allow"', () => {
  for (const [text, seconds] of Object.entries({ '10m': 600, '1h': 3600, '30d': 30 * 86400, '1d12h': 129600, ' 2D ': 172800 })) {
    assert.deepEqual(parseLinkDuration(text), { seconds }, text);
  }
  for (const bad of ['', '9m', '599s', '31d', '721h', '1w', '15', 'soon']) assert.ok('error' in parseLinkDuration(bad), bad);
  assert.deepEqual(parseLinkDuration('5m'), { error: 'Choose between 10 minutes and 30 days.' });
  // The Danger dialog keeps its own range.
  assert.deepEqual(parseDuration('30s'), { seconds: 30 });
  assert.ok('error' in parseDuration('8d'));
});

test('the presets are 1 h, 1 day (the default) and 7 days; "no expiry" is not one of them', () => {
  assert.deepEqual(LINK_PRESETS.map(p => p.label), ['1 h', '1 day', '7 days']);
  assert.equal(DEFAULT_LINK_PRESET, '1d');
  for (const p of LINK_PRESETS) assert.ok('seconds' in parseLinkDuration(p.id), p.id);
});
