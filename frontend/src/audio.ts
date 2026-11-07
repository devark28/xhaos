// Pure audio helpers for dictation: resample the microphone to 16 kHz mono,
// convert to signed 16-bit little-endian PCM, and encode it for the Go call.
// No browser APIs here, so the tests run under node.

export const DICTATION_RATE = 16000;
export const DICTATION_MAX_SECONDS = 300; // the backend refuses longer utterances

// resampler returns a stateful function that converts successive chunks of
// mono samples from inRate to outRate. Each output sample averages the input
// samples its window covers (a box filter, which limits aliasing when
// decimating); the fractional position carries over between chunks, so
// splitting the input into chunks gives the same output as one call.
export function resampler(inRate: number, outRate = DICTATION_RATE): (chunk: Float32Array) => Float32Array {
  const step = inRate / outRate;
  let carry = new Float32Array(0); // input not yet consumed, starting at pos
  let pos = 0;
  return chunk => {
    const buf = new Float32Array(carry.length + chunk.length);
    buf.set(carry);
    buf.set(chunk, carry.length);
    const out: number[] = [];
    while (pos + step <= buf.length) {
      const end = pos + step;
      let sum = 0, t = pos, j = Math.floor(pos);
      while (t < end) {
        const next = Math.min(j + 1, end);
        sum += buf[j] * (next - t);
        t = next; j++;
      }
      out.push(sum / step);
      pos = end;
    }
    const keep = Math.floor(pos);
    carry = buf.slice(keep);
    pos -= keep;
    return Float32Array.from(out);
  };
}

// toInt16 maps [-1, 1] floats to signed 16-bit samples, clamping overshoot.
export function toInt16(samples: Float32Array): Int16Array {
  const out = new Int16Array(samples.length);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    out[i] = Math.round(s < 0 ? s * 0x8000 : s * 0x7fff);
  }
  return out;
}

// concatInt16 joins the chunks of one utterance.
export function concatInt16(chunks: Int16Array[]): Int16Array {
  const total = chunks.reduce((n, c) => n + c.length, 0);
  const out = new Int16Array(total);
  let at = 0;
  for (const c of chunks) { out.set(c, at); at += c.length; }
  return out;
}

// pcm16le writes samples as little-endian bytes, whatever the platform's order.
export function pcm16le(samples: Int16Array): Uint8Array {
  const bytes = new Uint8Array(samples.length * 2);
  const view = new DataView(bytes.buffer);
  for (let i = 0; i < samples.length; i++) view.setInt16(i * 2, samples[i], true);
  return bytes;
}

// base64 encodes bytes in chunks (a spread over a huge array overflows the stack).
export function base64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

// utteranceBase64 is the wire format of Transcribe's audio argument.
export function utteranceBase64(chunks: Int16Array[]): string {
  return base64(pcm16le(concatInt16(chunks)));
}
