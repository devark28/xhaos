#!/usr/bin/env python3
"""Reference dictation sidecar for Xhaos: speech to text with faster-whisper.

Two modes, the same model and settings in both:

One-shot (the fallback). Xhaos runs it once per take, with the take as a
16 kHz mono 16-bit WAV, and reads the transcript from stdout. It loads the
model for every take. Errors go to stderr with exit status 1 (or 2 for bad
arguments). Nothing is written to disk.

  xhaos -dictation-cmd /usr/bin/python3 \\
        -dictation-args "/opt/xhaos/stt.py {wav} --model {model} --language={language} --prompt={prompt}" \\
        -dictation-model /opt/models/whisper-large-v3-turbo-ct2

Server (warm). Loads the model once and answers each take over HTTP, so no take
pays the model load. It listens on 127.0.0.1 only (--host takes a loopback
address and no other), and takes one take at a time: a take that arrives
during another gets 503 at once, and Xhaos runs it with the command instead.

  stt.py --serve --model /opt/models/whisper-large-v3-turbo-ct2 --port 8190
  stt.py --serve --model ... --socket /run/xhaos-dictation/dictation.sock

On a loopback port, any local process can listen there while the service is
down and answer in its place; the installer uses --socket, in a folder only
the xhaos user can reach.

  GET  /health                  200 "ok" once the model is loaded.
  POST /transcribe?language=en&prompt=...
       body: the take as a 16 kHz mono 16-bit WAV (Content-Length required).
       200: the transcript as text/plain. 400: not a valid WAV. 413: too big.
       500: the model failed (the text says why).

  xhaos -dictation-url unix:/run/xhaos-dictation/dictation.sock -dictation-cmd ... (the fallback)

Install: pip install faster-whisper. {model} is a CTranslate2 model directory
(or a model name faster-whisper can download). {language} is the dictation
language set in Settings → Dictation: a code such as en, or auto to detect it.
{prompt} is the workspace's dictation hints, set in Settings → Dictation; with
no hints the prompt is empty. The hints are passed as faster-whisper's hotwords,
which apply to every 30-second window of the take.
"""

import argparse
import ipaddress
import os
import subprocess
import sys
import socketserver
import threading
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from io import BytesIO
from urllib.parse import parse_qs, urlsplit

SAMPLE_RATE = 16000
# More threads than this was slower in every measurement: a laptop (4 beat 8)
# and an 8-vCPU VPS (84 s with 4 threads, 140 s with 8, for a 66 s take).
MAX_DEFAULT_THREADS = 4
DEFAULT_PORT = 8190
# A five-minute take is 9.6 MB of 16 kHz mono 16-bit PCM; the WAV header adds 44 bytes.
MAX_BODY = 10 * 1024 * 1024


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


def read_wav(source):
    """Return the WAV's frames, after checking it is 16 kHz mono 16-bit PCM.

    source is a path or a binary file object.
    """
    with wave.open(source, "rb") as w:
        if w.getnchannels() != 1 or w.getsampwidth() != 2 or w.getframerate() != SAMPLE_RATE:
            raise ValueError(
                f"expected 16 kHz mono 16-bit WAV, got {w.getframerate()} Hz, "
                f"{w.getnchannels()} channel(s), {8 * w.getsampwidth()}-bit"
            )
        return w.readframes(w.getnframes())


def load_model(model: str, threads: int):
    from faster_whisper import WhisperModel

    # int8 on the CPU keeps large-v3-turbo usable on a laptop; use "float16" on a GPU host.
    return WhisperModel(model, device="cpu", compute_type="int8", cpu_threads=threads)


def transcribe_pcm(whisper, pcm: bytes, language: str, prompt: str) -> str:
    import numpy as np

    audio = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
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


def transcribe(wav: str, model: str, language: str, prompt: str, threads: int) -> str:
    pcm = read_wav(wav)  # a bad file fails before the model loads
    return transcribe_pcm(load_model(model, threads), pcm, language, prompt)


class Server(ThreadingHTTPServer):
    """The warm service: one loaded model, one take transcribed at a time."""

    daemon_threads = True

    def __init__(self, address, whisper):
        super().__init__(address, Handler)
        self.whisper = whisper
        # One take at a time; /health does not take this lock, so it answers during a take.
        self.turn = threading.Lock()


class UnixServer(socketserver.ThreadingUnixStreamServer):
    """The warm service on a Unix socket (--socket): what Xhaos's installer
    uses, in a folder only its user can reach, so no other process can
    answer in the service's place while it is down."""

    daemon_threads = True

    def __init__(self, path, whisper):
        super().__init__(path, Handler)
        self.whisper = whisper
        self.turn = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    server_version = "xhaos-dictation"
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        if urlsplit(self.path).path != "/health":
            return self.reply(404, "no such path")
        self.reply(200, "ok")

    def do_POST(self):
        url = urlsplit(self.path)
        if url.path != "/transcribe":
            return self.reply(404, "no such path")
        length = self.headers.get("Content-Length")
        if length is None or not (length.isascii() and length.isdigit()):
            return self.reply(411, "Content-Length is required")
        if int(length) > MAX_BODY:
            return self.reply(413, "the take is too long")
        body = self.rfile.read(int(length))
        query = parse_qs(url.query, keep_blank_values=True)
        language = query.get("language", ["en"])[0].strip().lower() or "en"
        prompt = query.get("prompt", [""])[0].strip()
        try:
            pcm = read_wav(BytesIO(body))
        except (ValueError, EOFError, wave.Error) as err:
            return self.reply(400, str(err) or "not a WAV")
        # One take at a time. A busy service says so at once instead of queueing:
        # Xhaos then runs the take with the command, and a take never spends its
        # time limit waiting behind another (or behind one Xhaos gave up on).
        if not self.server.turn.acquire(blocking=False):
            return self.reply(503, "busy with another take")
        try:
            text = transcribe_pcm(self.server.whisper, pcm, language, prompt)
        except Exception as err:  # the model failed: say so, keep serving
            print(f"transcription failed: {err}", file=sys.stderr, flush=True)
            return self.reply(500, f"transcription failed: {err}")
        finally:
            self.server.turn.release()
        self.reply(200, text)

    def reply(self, status: int, text: str):
        if status >= 400:
            self.close_connection = True  # a refused body may be unread: drop the connection
        data = text.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt, *args):
        # Quiet: one line per take is noise; errors are printed where they happen.
        pass


def serve(host: str, port: int, model: str, threads: int, sock: str = "") -> int:
    if not sock:
        try:
            address = ipaddress.ip_address(host)
        except ValueError:
            address = None
        if host != "localhost" and (address is None or not address.is_loopback):
            print(f"--host must be a loopback address (127.0.0.1), not {host}", file=sys.stderr)
            return 2
    elif not os.path.isabs(sock):
        print(f"--socket must be an absolute path, not {sock}", file=sys.stderr)
        return 2
    try:
        whisper = load_model(model, threads)
    except ImportError as err:
        print(f"faster-whisper is not installed (pip install faster-whisper): {err}", file=sys.stderr)
        return 1
    if sock:
        # A socket left by an earlier run would stop the bind.
        try:
            os.unlink(sock)
        except FileNotFoundError:
            pass
        old = os.umask(0o077)
        try:
            server = UnixServer(sock, whisper)
        finally:
            os.umask(old)
        where = sock
    else:
        server = Server((host, port), whisper)
        where = f"{host}:{port}"
    print(f"dictation service ready on {where} ({threads} threads)", file=sys.stderr, flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Transcribe Xhaos dictation takes to text: one take on stdout, or a warm server.")
    parser.add_argument("wav", nargs="?", help="the take: 16 kHz mono 16-bit WAV (one-shot mode)")
    parser.add_argument("--serve", action="store_true", help="run the warm service instead: load the model once, answer over HTTP")
    parser.add_argument("--host", default="127.0.0.1", help="with --serve: the loopback address to listen on (default: 127.0.0.1)")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT, help=f"with --serve: the port (default: {DEFAULT_PORT})")
    parser.add_argument("--socket", default="", help="with --serve: listen on this Unix socket instead of --host and --port")
    parser.add_argument("--model", required=True, help="model directory (or a faster-whisper model name)")
    parser.add_argument("--language", default="en", help="language code such as en, or auto to detect it (default: en); one-shot mode")
    parser.add_argument("--prompt", default="", help="dictation hints: a punctuated sentence with the expected words; one-shot mode")
    parser.add_argument("--threads", type=int, default=0, help=f"CPU threads (default: the physical cores, at most {MAX_DEFAULT_THREADS})")
    args = parser.parse_args()
    threads = args.threads if args.threads > 0 else min(physical_cores(), MAX_DEFAULT_THREADS)
    if args.serve:
        if args.wav is not None:
            parser.error("--serve takes no WAV file")
        return serve(args.host, args.port, args.model, threads, args.socket)
    if args.wav is None:
        parser.error("a WAV file is needed (or --serve)")
    if not os.path.exists(args.wav):
        print(f"no such file: {args.wav}", file=sys.stderr)
        return 2
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
