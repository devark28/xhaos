// The dictation status beside the mic: the timer while listening (from 4:30,
// with the time left before the cap), and while transcribing the time so far
// against the estimate. It is one element that stays in place, hidden when idle,
// so the waveform can keep clear of it. The mic itself is disabled and spins
// while the take is transcribed.
import { useEffect, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { useApp } from './state/store.ts';
import { recordingTimer, transcribingText } from '../dictation-format.ts';
import { colors } from './theme/tokens.stylex.ts';

export function DictationStatus() {
  const recording = useApp(s => s.recording);
  const since = useApp(s => s.recordingSince);
  const transcribing = useApp(s => s.transcribing);
  const transcribedSince = useApp(s => s.transcribingSince);
  const estimate = useApp(s => s.transcribeEstimate);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!recording && !transcribing) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(id);
  }, [recording, since, transcribing, transcribedSince]);
  if (recording) {
    const t = recordingTimer((now - since) / 1000);
    return <span id="dictation-status" aria-label="Recording time" data-dictation-timer role="status" {...stylex.props(s.status, t.nearCap && s.near)}>{t.text}</span>;
  }
  if (transcribing) {
    const elapsed = (now - transcribedSince) / 1000;
    return <span id="dictation-status" role="status" {...stylex.props(s.status)}>{transcribingText(elapsed, estimate)}</span>;
  }
  return <span id="dictation-status" role="status" hidden {...stylex.props(s.status)} />;
}

const spin = stylex.keyframes({ to: { transform: 'rotate(360deg)' } });

// dictateStyles dress the mic button while its take is transcribed.
export const dictateStyles = stylex.create({
  busy: { position: 'relative', cursor: 'progress', opacity: { default: 1, ':disabled': 0.6 } },
  ring: {
    position: 'absolute', top: 3, right: 3, bottom: 3, left: 3,
    borderRadius: '50%', borderWidth: 2, borderStyle: 'solid', borderColor: 'transparent', borderTopColor: colors.brand,
    animationName: spin, animationDuration: '0.9s', animationTimingFunction: 'linear', animationIterationCount: 'infinite',
    pointerEvents: 'none',
  },
});

const s = stylex.create({
  status: { alignSelf: 'center', color: colors.textFaint, fontSize: 11, fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap' },
  near: { color: colors.warn },
});
