import test from 'node:test';
import assert from 'node:assert/strict';
import { clock, recordingTimer, transcribeEstimate, transcribingText, emptyText, gainTracker, MAX_GAIN, GAIN_TARGET } from './dictation-format.ts';

test('clock formats seconds as m:ss', () => {
  assert.equal(clock(0), '0:00');
  assert.equal(clock(9.9), '0:09');
  assert.equal(clock(65), '1:05');
  assert.equal(clock(300), '5:00');
  assert.equal(clock(-3), '0:00');
});

test('the timer shows elapsed time, and the time left from 4:30', () => {
  assert.deepEqual(recordingTimer(12.4), { text: '0:12', nearCap: false });
  assert.deepEqual(recordingTimer(269.9), { text: '4:29', nearCap: false });
  assert.deepEqual(recordingTimer(270), { text: '4:30 · 0:30 left', nearCap: true });
  assert.deepEqual(recordingTimer(299.5), { text: '4:59 · 0:01 left', nearCap: true });
  assert.deepEqual(recordingTimer(312), { text: '5:00 · 0:00 left', nearCap: true });
});

test('the transcribing estimate is half the length by default, at least 1 s', () => {
  assert.equal(transcribeEstimate(12), 6);
  assert.equal(transcribeEstimate(300), 150);
  assert.equal(transcribeEstimate(0.5), 1);
});

test('the transcribing estimate follows a measured rate', () => {
  assert.equal(transcribeEstimate(10, 2), 20);
});

test('the transcribing text keeps its words and adds the estimate', () => {
  assert.equal(transcribingText(6), 'Transcribing… (about 6 s)');
});

test('the empty-transcript message names the length of the audio', () => {
  assert.equal(emptyText(12), 'No speech heard in 12 s of audio.');
  assert.equal(emptyText(0.3), 'No speech heard in 1 s of audio.');
});

test('quiet input is lifted toward the target, up to the maximum gain', () => {
  const gain = gainTracker(0.012);
  let g = 0;
  for (let i = 0; i < 10; i++) g = gain(0.02);
  assert.equal(g, MAX_GAIN); // 0.2 / 0.02 = 10, capped at 8
});

test('loud input is never turned down', () => {
  const gain = gainTracker(0.012);
  assert.equal(gain(0.5), 1);
  assert.equal(gain(0.5), 1);
});

test('a pause keeps the gain, and silence below the floor does not move it', () => {
  const gain = gainTracker(0.012);
  const lifted = gain(0.05); // 0.2 / 0.05 = 4
  assert.equal(lifted, GAIN_TARGET / 0.05);
  for (let i = 0; i < 100; i++) assert.equal(gain(0.002), lifted);
});

test('the gain falls back when a loud burst raises the peak', () => {
  const gain = gainTracker(0.012);
  gain(0.02);
  const high = gain(0.02);
  const after = gain(0.5);
  assert.ok(after <= high);
  assert.equal(after, 1);
});
