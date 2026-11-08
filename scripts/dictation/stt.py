#!/usr/bin/env python3
"""Reference dictation sidecar for Xhaos: speech to text with faster-whisper.

Xhaos runs it once per take, with the take as a 16 kHz mono 16-bit WAV, and
reads the transcript from stdout. Errors go to stderr with exit status 1 (or 2
for bad arguments). Nothing is written to disk.

Start Xhaos with, for example:

  xhaos -dictation-cmd /usr/bin/python3 \\
        -dictation-args "/opt/xhaos/stt.py {wav} --model {model} --language={language} --prompt={prompt}" \\
        -dictation-model /opt/models/whisper-large-v3-turbo-ct2

Install: pip install faster-whisper. {model} is a CTranslate2 model directory
(or a model name faster-whisper can download). {language} is the dictation
language set in Settings → Dictation: a code such as en, or auto to detect it.
{prompt} is the workspace's dictation hints, set in Settings → Dictation; with
no hints the prompt is empty. The hints are passed as faster-whisper's hotwords,
which apply to every 30-second window of the take.
"""

import argparse
import os
import subprocess
import sys
import wave

SAMPLE_RATE = 16000


def physical_cores() -> int:
    """Physical cores: where the CPU reports them, else the logical count."""
    try:
        if sys.platform == "darwin":
            out = subprocess.run(["sysctl", "-n", "hw.physicalcpu"], capture_output=True, text=True, check=True)
            return max(1, int(out.stdout.strip()))
        pairs, physical, core = set(), None, None
        with open("/proc/cpuinfo") as info:
            for line in info:
                if line.startswith("physical id"):
                    physical = line.split(":", 1)[1].strip()
                elif line.startswith("core id"):
                    core = line.split(":", 1)[1].strip()
                elif not line.strip():
                    if physical is not None and core is not None:
                        pairs.add((physical, core))
                    physical = core = None
        if pairs:
            return len(pairs)
    except (OSError, subprocess.SubprocessError, ValueError):
        pass
    return os.cpu_count() or 1


def read_wav(path: str):
    """Return the WAV's frames, after checking it is 16 kHz mono 16-bit PCM."""
    with wave.open(path, "rb") as w:
        if w.getnchannels() != 1 or w.getsampwidth() != 2 or w.getframerate() != SAMPLE_RATE:
            raise ValueError(
                f"expected 16 kHz mono 16-bit WAV, got {w.getframerate()} Hz, "
                f"{w.getnchannels()} channel(s), {8 * w.getsampwidth()}-bit"
            )
        return w.readframes(w.getnframes())


def transcribe(wav: str, model: str, language: str, prompt: str, threads: int) -> str:
    import numpy as np
    from faster_whisper import WhisperModel

    pcm = read_wav(wav)
    audio = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
    # int8 on the CPU keeps large-v3-turbo usable on a laptop; use "float16" on a GPU host.
    whisper = WhisperModel(model, device="cpu", compute_type="int8", cpu_threads=threads)
    segments, _info = whisper.transcribe(
        audio,
        language=None if language == "auto" else language,  # None: detect it on each take
        # The hints are hotwords: faster-whisper puts them in the prompt of every
        # 30-second window (initial_prompt would reach only the first). The model
        # keeps about 223 tokens of them, roughly the first 150 words.
        hotwords=prompt or None,
        # Each window is decoded alone. Carrying the text over to the next window
        # is what lets Whisper fall into repetition loops on long takes.
        condition_on_previous_text=False,
        vad_filter=True,  # skip silence, where Whisper tends to invent text
        beam_size=5,
    )
    return " ".join(segment.text.strip() for segment in segments).strip()


def main() -> int:
    parser = argparse.ArgumentParser(description="Transcribe one Xhaos dictation take to text on stdout.")
    parser.add_argument("wav", help="the take: 16 kHz mono 16-bit WAV")
    parser.add_argument("--model", required=True, help="model directory (or a faster-whisper model name)")
    parser.add_argument("--language", default="en", help="language code such as en, or auto to detect it (default: en)")
    parser.add_argument("--prompt", default="", help="dictation hints: a punctuated sentence with the expected words")
    parser.add_argument("--threads", type=int, default=0, help="CPU threads (default: the physical cores)")
    args = parser.parse_args()
    if not os.path.exists(args.wav):
        print(f"no such file: {args.wav}", file=sys.stderr)
        return 2
    threads = args.threads if args.threads > 0 else physical_cores()
    try:
        text = transcribe(args.wav, args.model, args.language.strip().lower(), args.prompt.strip(), threads)
    except ImportError as err:
        print(f"faster-whisper is not installed (pip install faster-whisper): {err}", file=sys.stderr)
        return 1
    except (ValueError, wave.Error) as err:
        print(str(err), file=sys.stderr)
        return 1
    print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
