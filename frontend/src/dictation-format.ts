// Pure helpers for the dictation UI: the timer beside the mic, the estimate
// shown while a take is transcribed, the message for an empty transcript, and
// the waveform's automatic gain. No browser APIs here, so the tests run under
// node.

import { DICTATION_MAX_SECONDS } from './audio.ts';

export const WARN_FROM_SECONDS = 270; // 4:30: the timer also shows the time left before the cap

// clock formats seconds as m:ss.
export function clock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

// recordingTimer is the text beside the mic while listening. From 4:30 it also
// gives the time left before the cap, where the take stops by itself.
export function recordingTimer(elapsed: number, cap = DICTATION_MAX_SECONDS): { text: string; nearCap: boolean } {
  const e = Math.min(cap, Math.max(0, Math.floor(elapsed)));
  if (e < WARN_FROM_SECONDS) return { text: clock(e), nearCap: false };
  return { text: `${clock(e)} · ${clock(cap - e)} left`, nearCap: true };
}

// DEFAULT_RATE is the sidecar's speed assumed until a take has been timed: half
// a second of work per second of speech, the slope of its time limit.
export const DEFAULT_RATE = 0.5;

// transcribeEstimate is the "about N s" for a take of lengthSeconds. rate is the
// seconds of work per second of speech, measured on an earlier take.
export function transcribeEstimate(lengthSeconds: number, rate = DEFAULT_RATE): number {
  return Math.max(1, Math.round(lengthSeconds * rate));
}

export function transcribingText(estimate: number): string {
  return `Transcribing… (about ${estimate} s)`;
}

// emptyText is the message for a take whose transcript is empty. Timeouts and
// sidecar errors keep their own wording.
export function emptyText(lengthSeconds: number): string {
  return `No speech heard in ${Math.max(1, Math.round(lengthSeconds))} s of audio.`;
}

export const GAIN_TARGET = 0.2; // the peak loudness (RMS) that fills the band
export const MAX_GAIN = 8; // a quiet input is lifted at most this much
const PEAK_DECAY = 0.985; // per frame, a peak quieter than the input gives way

// gainTracker returns a per-frame gain for the waveform. It lifts quiet input
// toward GAIN_TARGET, never turns loud input down (the gain is at least 1), and
// stops at MAX_GAIN, so room hiss is not amplified without limit. The peak
// follows only sound above floor, so a pause keeps the gain it had.
export function gainTracker(floor: number): (level: number) => number {
  let peak = 0;
  return level => {
    if (level > floor) peak = Math.max(level, peak * PEAK_DECAY);
    if (peak <= 0) return 1;
    return Math.min(MAX_GAIN, Math.max(1, GAIN_TARGET / peak));
  };
}
