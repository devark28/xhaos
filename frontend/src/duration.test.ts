import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseDuration, formatDuration, callsSetting, callsValid, DEFAULT_CALLS } from './duration.ts';

test('durations parse like the server', () => {
  const ok: Record<string, number> = { '10s': 10, '15m': 900, '1h': 3600, '10h': 36000, '1d': 86400, '1h30m': 5400, ' 2H ': 7200, '7d': 604800 };
  for (const [text, seconds] of Object.entries(ok)) assert.deepEqual(parseDuration(text), { seconds }, text);
  for (const bad of ['', '9s', '8d', '1w', '15', 'm', '1h and 2m', '-5m', '9999999h']) assert.ok('error' in parseDuration(bad), bad);
});

test('durations print short', () => {
  assert.equal(formatDuration(45), '45s');
  assert.equal(formatDuration(900), '15m');
  assert.equal(formatDuration(5400), '1h 30m');
  assert.equal(formatDuration(3600), '1h');
  assert.equal(formatDuration(86400 * 2 + 3600 * 4), '2d 4h');
});

test('the call limit maps to what the server stores', () => {
  assert.equal(callsSetting(true, DEFAULT_CALLS), 0); // the normal limit
  assert.equal(callsSetting(true, 150), 150);
  assert.equal(callsSetting(false, 150), -1); // switched off: no limit
  assert.ok(callsValid(1) && callsValid(1000) && !callsValid(0) && !callsValid(1001) && !callsValid(2.5));
});
