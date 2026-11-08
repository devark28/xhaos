import test from 'node:test';
import assert from 'node:assert/strict';
import { clock, recordingTimer, transcribeEstimate, transcribingText, emptyText, gainTracker, hintWarning, hintWords, HINT_WORDS, MAX_GAIN, GAIN_TARGET, DEFAULT_RATE } from './dictation-format.ts';

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

test('the transcribing estimate is 15 s per minute of speech by default, at least 1 s', () => {
  assert.equal(DEFAULT_RATE, 0.25);
  assert.equal(transcribeEstimate(60), 15);
  assert.equal(transcribeEstimate(12), 3);
  assert.equal(transcribeEstimate(300), 75);
  assert.equal(transcribeEstimate(0.5), 1);
});

test('the transcribing estimate follows a measured rate', () => {
  assert.equal(transcribeEstimate(10, 2), 20);
});

test('the transcribing text counts the elapsed time up against the estimate', () => {
  assert.equal(transcribingText(0, 15), 'Transcribing… 0 s of about 15 s');
  assert.equal(transcribingText(4.9, 15), 'Transcribing… 4 s of about 15 s');
  assert.equal(transcribingText(-1, 15), 'Transcribing… 0 s of about 15 s');
});

test('the hint counter counts words, and the warning starts past 150', () => {
  assert.equal(hintWords('  Xhaos,  Verlet and the Duo. '), 5);
  assert.equal(hintWords(''), 0);
  const fits = Array.from({ length: HINT_WORDS }, () => 'word').join(' ');
  assert.equal(hintWarning(fits), '');
  const over = `${fits} extra`;
  assert.match(hintWarning(over), /first 150 words/);
  assert.match(hintWarning(over), /ignored/);
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
