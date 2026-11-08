// Pure helpers for the dictation UI: the timer beside the mic, the estimate
// shown while a take is transcribed, the message for an empty transcript, and
// the waveform's automatic gain. No browser APIs here, so the tests run under
// node.

import { DICTATION_MAX_SECONDS } from './audio.ts';

export const WARN_FROM_SECONDS = 270; // 4:30: the timer also shows the time left before the cap

// DICTATION_OFF is the tooltip of the greyed-out mic, and the message on a click,
// when the server has no speech sidecar (-dictation-cmd is empty).
export const DICTATION_OFF = 'Dictation is off: this server has no speech model (see docs)';

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

// DEFAULT_RATE is the sidecar's speed assumed until a take has been timed: 15 s
// of work per minute of speech (large-v3-turbo on a laptop CPU measured about
// 14 s per minute). It is not the server's time limit, which is 15 s plus half
// the take's length.
export const DEFAULT_RATE = 0.25;

// transcribeEstimate is the "about N s" for a take of lengthSeconds. rate is the
// seconds of work per second of speech, measured on an earlier take.
export function transcribeEstimate(lengthSeconds: number, rate = DEFAULT_RATE): number {
  return Math.max(1, Math.round(lengthSeconds * rate));
}

// transcribingText shows the elapsed time counting up against the estimate.
export function transcribingText(elapsed: number, estimate: number): string {
  return `Transcribing… ${Math.max(0, Math.floor(elapsed))} s of about ${estimate} s`;
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

// HINT_MAX is the most characters of hints the server keeps (app/dictation).
export const HINT_MAX = 1000;
// HINT_WORDS is about how far the model reads the hints: faster-whisper keeps
// 223 tokens of them, and about 150 words fit that. Names and jargon cost
// several tokens each, so fewer words can already be too many. This counts
// words, not tokens, which the browser cannot do exactly.
export const HINT_WORDS = 150;

// hintWords counts the words in the hints.
export function hintWords(text: string): number {
  return text.split(/\s+/).filter(Boolean).length;
}

// hintWarning is the note under the hints box when the model would ignore the
// end of them, or "" when the hints fit.
export function hintWarning(text: string): string {
  if (hintWords(text) <= HINT_WORDS) return '';
  return `The model reads only about the first ${HINT_WORDS} words of these hints. The rest will be ignored.`;
}
