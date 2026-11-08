import test from 'node:test';
import assert from 'node:assert/strict';
import { resampler, toInt16, concatInt16, pcm16le, base64, utteranceBase64, DICTATION_RATE } from './audio.ts';

test('resampling 48 kHz to 16 kHz keeps a constant level and the length ratio', () => {
  const down = resampler(48000);
  const out = down(new Float32Array(4800).fill(0.5));
  assert.equal(out.length, 1600);
  assert.ok(out.every(v => Math.abs(v - 0.5) < 1e-6));
});

test('chunked resampling matches a single call', () => {
  const input = Float32Array.from({ length: 1000 }, (_, i) => Math.sin(i / 7));
  const whole = resampler(44100)(input);
  const split = resampler(44100);
  const parts = [split(input.subarray(0, 333)), split(input.subarray(333, 700)), split(input.subarray(700))];
  const joined = new Float32Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) { joined.set(p, at); at += p.length; }
  // The fractional position is carried, so the two agree; a boundary sample may differ by float rounding.
  assert.ok(Math.abs(joined.length - whole.length) <= 1);
  const n = Math.min(joined.length, whole.length);
  assert.ok(Array.from({ length: n }, (_, i) => Math.abs(joined[i] - whole[i]) < 1e-5).every(Boolean));
});

test('a 16 kHz input passes through unchanged', () => {
  const input = Float32Array.from([0.1, -0.2, 0.3, -0.4]);
  assert.deepEqual([...resampler(DICTATION_RATE)(input)].map(v => Math.round(v * 1e6) / 1e6), [0.1, -0.2, 0.3, -0.4]);
});

test('float samples become clamped signed 16-bit values', () => {
  assert.deepEqual([...toInt16(Float32Array.from([0, 1, -1, 1.5, -1.5, 0.5]))], [0, 32767, -32768, 32767, -32768, 16384]);
});

test('chunks concatenate in order and encode as little-endian PCM', () => {
  const joined = concatInt16([Int16Array.from([1, 2]), Int16Array.from([-1])]);
  assert.deepEqual([...joined], [1, 2, -1]);
  assert.deepEqual([...pcm16le(Int16Array.from([1, 0, -1]))], [1, 0, 0, 0, 255, 255]);
});

test('base64 matches the standard encoding, also for long inputs', () => {
  assert.equal(base64(Uint8Array.from([1, 0, 255, 255])), 'AQD//w==');
  const long = new Uint8Array(100000).map((_, i) => i % 251);
  assert.equal(base64(long), Buffer.from(long).toString('base64'));
});

test('an utterance encodes to base64 of its PCM bytes', () => {
  assert.equal(utteranceBase64([Int16Array.from([1]), Int16Array.from([0, -1])]), Buffer.from([1, 0, 0, 0, 255, 255]).toString('base64'));
});
