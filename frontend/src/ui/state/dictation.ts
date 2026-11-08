// Voice dictation. The page captures the microphone (AudioContext +
// AudioWorklet), resamples it to 16 kHz mono, and on stop sends the one
// utterance to the backend's dictation service. The transcript lands in the
// composer draft for review; nothing is sent.
import { update, get, toast, errorText } from './store.ts';
import { call, connected } from './backend.ts';
import { focusComposer, getDraft, setDraft } from './composer.ts';
import { resampler, toInt16, utteranceBase64, DICTATION_RATE, DICTATION_MAX_SECONDS } from '../../audio.ts';
import { transcribeEstimate, transcribingText, emptyText, DICTATION_OFF } from '../../dictation-format.ts';

const MAX_SAMPLES = DICTATION_RATE * DICTATION_MAX_SECONDS; // the backend's limit, in samples
const MIN_SAMPLES = DICTATION_RATE / 4; // less than a quarter second: nothing to transcribe

// The worklet posts each block of the first input channel to the page.
const WORKLET = `
class XhaosCapture extends AudioWorkletProcessor {
  process(inputs) { const channel = inputs[0] && inputs[0][0]; if (channel) this.port.postMessage(channel.slice(0)); return true; }
}
registerProcessor('xhaos-capture', XhaosCapture);`;

type Phase = 'idle' | 'starting' | 'recording' | 'transcribing';
interface Session {
  workspace: string;
  stream: MediaStream;
  context: AudioContext;
  source: MediaStreamAudioSourceNode;
  analyser: AnalyserNode;
  node: AudioWorkletNode;
  mute: GainNode; // silent sink: keeps the graph running without playing the mic
  chunks: Int16Array[];
  samples: number;
  timer?: ReturnType<typeof setTimeout>; // the 5-minute limit
  closed?: boolean;
}
let phase: Phase = 'idle';
let session: Session | null = null;
let measuredRate: number | undefined; // seconds of work per second of speech, from the last timed take
// Shorter takes are mostly the sidecar's start-up (it loads the model per take),
// so only a take this long times the rate.
const RATE_MIN_SECONDS = 30;

// levelAnalyser is the live input's analyser (for the waveform), or null.
// It is read per frame by the waveform; it is not store state.
export const levelAnalyser = (): AnalyserNode | null => session?.analyser ?? null;

// toggleDictation is the mic button: start, or stop and transcribe.
export async function toggleDictation() {
  if (phase === 'recording') return finish();
  if (phase === 'idle') return start();
  toast(phase === 'starting' ? 'The microphone is starting…' : 'Still transcribing the last recording.');
}

async function start() {
  phase = 'starting';
  try {
    if (get().dictationEnabled === false) { toast(DICTATION_OFF); return; }
    if (!connected()) { toast('Dictation needs the Xhaos backend, which is not connected in this page.'); return; }
    if (!navigator.mediaDevices?.getUserMedia) { toast('Dictation needs a secure page (HTTPS or localhost) with a microphone.'); return; }
    if (!globalThis.AudioWorkletNode) { toast('Dictation needs a browser with AudioWorklet support.'); return; }
    session = await open(get().active);
    phase = 'recording';
    update(s => { s.recording = true; s.recordingSince = Date.now(); });
    toast('Listening… Press the mic again to stop.');
  } catch (error) {
    toast(`Could not start the microphone: ${errorText(error)}`);
  } finally {
    if (phase === 'starting') phase = 'idle';
  }
}

async function open(workspace: string): Promise<Session> {
  const stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, autoGainControl: false, noiseSuppression: false, echoCancellation: false } });
  let context: AudioContext | null = null;
  try {
    context = new AudioContext();
    const url = URL.createObjectURL(new Blob([WORKLET], { type: 'text/javascript' }));
    try { await context.audioWorklet.addModule(url); } finally { URL.revokeObjectURL(url); }
    await context.resume().catch(() => {});
    const source = context.createMediaStreamSource(stream);
    const analyser = context.createAnalyser();
    analyser.fftSize = 1024;
    const node = new AudioWorkletNode(context, 'xhaos-capture');
    const mute = context.createGain();
    mute.gain.value = 0;
    source.connect(analyser);
    source.connect(node);
    analyser.connect(mute);
    node.connect(mute);
    mute.connect(context.destination);

    const resample = resampler(context.sampleRate);
    const current: Session = { workspace, stream, context, source, analyser, node, mute, chunks: [], samples: 0, closed: false };
    node.port.onmessage = (event: MessageEvent<Float32Array>) => {
      if (current.closed) return;
      const chunk = toInt16(resample(event.data));
      const room = MAX_SAMPLES - current.samples;
      const kept = chunk.length > room ? chunk.subarray(0, room) : chunk;
      current.chunks.push(kept);
      current.samples += kept.length;
      if (current.samples >= MAX_SAMPLES) void finish(`Dictation reached ${DICTATION_MAX_SECONDS / 60} minutes and stopped.`);
    };
    // A microphone that disappears (unplugged, revoked) ends the take.
    stream.getAudioTracks()[0]?.addEventListener('ended', () => { void finish('The microphone stopped.'); });
    current.timer = setTimeout(() => { void finish(`Dictation reached ${DICTATION_MAX_SECONDS / 60} minutes and stopped.`); }, DICTATION_MAX_SECONDS * 1000);
    return current;
  } catch (error) {
    stream.getTracks().forEach(track => track.stop());
    void context?.close().catch(() => {});
    throw error;
  }
}

// close stops the tracks and the audio graph, and releases the device.
function close(s: Session) {
  s.closed = true;
  clearTimeout(s.timer);
  s.node.port.onmessage = null;
  s.stream.getTracks().forEach(track => track.stop());
  s.source.disconnect(); s.analyser.disconnect(); s.node.disconnect(); s.mute.disconnect();
  void s.context.close().catch(() => {});
}

// finish ends the take and sends it. Only one take runs at a time.
async function finish(reason?: string) {
  const s = session;
  if (!s || phase !== 'recording') return;
  session = null;
  phase = 'transcribing';
  close(s);
  update(d => { d.recording = false; d.recordingSince = 0; });
  const length = s.samples / DICTATION_RATE;
  const said = (message: string) => (reason ? `${reason} ${message}` : message);
  try {
    if (s.samples < MIN_SAMPLES) { toast(said('Nothing recorded. Hold the mic a little longer.')); return; }
    const estimate = transcribeEstimate(length, measuredRate);
    update(d => { d.transcribing = true; d.transcribeEstimate = estimate; d.transcribingSince = Date.now(); });
    toast(said(transcribingText(0, estimate)));
    const started = performance.now();
    const text = String(await call<string>('dictation', 'Transcribe', s.workspace, utteranceBase64(s.chunks)) ?? '').trim();
    if (length >= RATE_MIN_SECONDS) measuredRate = (performance.now() - started) / 1000 / length;
    if (!text) { toast(emptyText(length)); return; }
    insert(text);
  } catch (error) {
    toast(`Dictation failed: ${errorText(error)}`);
  } finally {
    phase = 'idle';
    update(d => { d.transcribing = false; d.transcribingSince = 0; });
  }
}

// insert appends the transcript to the draft, for review. Never sends.
function insert(text: string) {
  const draft = getDraft().replace(/\s+$/, '');
  setDraft(draft ? `${draft} ${text}` : text);
  focusComposer();
  toast('Transcript ready. Review it and press send.');
}
