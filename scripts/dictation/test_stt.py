"""Tests for stt.py: the one-shot mode and the warm server, with a stub model.

The stub stands in for faster-whisper and numpy (neither is needed to test the
HTTP and queue behaviour), and writes what it was asked to a log, so the tests
can see that the model loads once, that takes queue, and what each take got.

Run: python3 -m unittest discover -s scripts/dictation -v
"""

import http.client
import io
import json
import os
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import wave
from pathlib import Path

HERE = Path(__file__).resolve().parent
STT = HERE / "stt.py"

# The stub: faster_whisper.WhisperModel and the little of numpy stt.py uses.
STUB = {
    "faster_whisper.py": '''
import os, time
LOG = os.environ["STUB_LOG"]

def _log(line):
    with open(LOG, "a") as f:
        f.write(line + "\\n")

class _Seg:
    def __init__(self, text):
        self.text = text

class WhisperModel:
    def __init__(self, model, device, compute_type, cpu_threads):
        _log(f"load {model} {cpu_threads}")

    def transcribe(self, audio, language=None, hotwords=None, **kw):
        _log(f"start {language} {hotwords!r}")
        slow = float(os.environ.get("STUB_SLOW", "0"))
        if slow:
            time.sleep(slow)
        if os.environ.get("STUB_FAIL") == "1":
            _log("end fail")
            raise RuntimeError("model exploded")
        _log("end ok")
        return [_Seg(" heard %d samples " % len(audio))], None
''',
    "numpy.py": '''
float32 = "float32"

class _Array:
    def __init__(self, n):
        self.n = n
    def astype(self, dtype):
        return self
    def __truediv__(self, other):
        return self
    def __len__(self):
        return self.n

def frombuffer(buf, dtype=None):
    return _Array(len(buf) // 2)
''',
}


def make_wav(seconds=1.0, rate=16000, channels=1, width=2):
    buf = io.BytesIO()
    frames = int(seconds * rate) * channels
    with wave.open(buf, "wb") as w:
        w.setnchannels(channels)
        w.setsampwidth(width)
        w.setframerate(rate)
        w.writeframes(struct.pack("<h", 1000) * frames)
    return buf.getvalue()


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Env(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        root = Path(self.dir.name)
        for name, body in STUB.items():
            (root / name).write_text(body)
        self.log = root / "stub.log"
        self.env = dict(os.environ, PYTHONPATH=str(root), STUB_LOG=str(self.log))
        self.env.pop("STUB_SLOW", None)
        self.env.pop("STUB_FAIL", None)

    def stub_log(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def start_server(self, *extra):
        port = free_port()
        proc = subprocess.Popen(
            [sys.executable, str(STT), "--serve", "--model", "/models/x", "--port", str(port), "--threads", "2", *extra],
            env=self.env, stderr=subprocess.PIPE, text=True,
        )
        self.addCleanup(self.stop, proc)
        deadline = time.time() + 20
        while time.time() < deadline:
            if proc.poll() is not None:
                self.fail("server exited: " + proc.stderr.read())
            try:
                conn = http.client.HTTPConnection("127.0.0.1", port, timeout=1)
                conn.request("GET", "/health")
                if conn.getresponse().status == 200:
                    return port
            except OSError:
                time.sleep(0.1)
        self.fail("server did not answer /health")

    def stop(self, proc):
        proc.terminate()
        try:
            proc.wait(10)
        except subprocess.TimeoutExpired:
            proc.kill()
        proc.stderr.close()

    def post(self, port, path, body, headers=None, timeout=30):
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
        conn.request("POST", path, body=body, headers=headers or {})
        resp = conn.getresponse()
        data = resp.read().decode("utf-8")
        conn.close()
        return resp.status, data


class OneShotTest(Env):
    def run_stt(self, *args):
        return subprocess.run([sys.executable, str(STT), *args], env=self.env, capture_output=True, text=True)

    def test_prints_transcript_and_loads_once_per_take(self):
        wav = Path(self.dir.name) / "take.wav"
        wav.write_bytes(make_wav(1.0))
        res = self.run_stt(str(wav), "--model", "/models/x", "--language=de", "--prompt=Xhaos Pi", "--threads", "3")
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(res.stdout.strip(), "heard 16000 samples")
        self.assertEqual(self.stub_log(), ["load /models/x 3", "start de 'Xhaos Pi'", "end ok"])

    def test_bad_wav_is_exit_1_and_no_model_load(self):
        wav = Path(self.dir.name) / "bad.wav"
        wav.write_bytes(make_wav(1.0, rate=44100))
        res = self.run_stt(str(wav), "--model", "/models/x")
        self.assertEqual(res.returncode, 1)
        self.assertIn("expected 16 kHz mono 16-bit WAV", res.stderr)
        self.assertEqual(self.stub_log(), [])

    def test_wav_argument_is_required_without_serve(self):
        res = self.run_stt("--model", "/models/x")
        self.assertEqual(res.returncode, 2)

    def test_serve_takes_no_wav(self):
        res = self.run_stt("--serve", "x.wav", "--model", "/models/x")
        self.assertEqual(res.returncode, 2)

    def test_serve_refuses_a_public_address(self):
        res = self.run_stt("--serve", "--host", "0.0.0.0", "--model", "/models/x", "--port", str(free_port()))
        self.assertEqual(res.returncode, 2)
        self.assertIn("loopback", res.stderr)
        self.assertEqual(self.stub_log(), [])


class ServerTest(Env):
    def test_loads_model_once_and_answers_each_take(self):
        port = self.start_server()
        wav = make_wav(2.0)
        for _ in range(3):
            status, text = self.post(port, "/transcribe?language=auto&prompt=Xhaos%20Pi", wav)
            self.assertEqual((status, text), (200, "heard 32000 samples"))
        loads = [line for line in self.stub_log() if line.startswith("load")]
        self.assertEqual(loads, ["load /models/x 2"], "the model must load once, at start")
        self.assertEqual(self.stub_log().count("start None 'Xhaos Pi'"), 3)

    def test_default_language_and_empty_prompt(self):
        port = self.start_server()
        self.post(port, "/transcribe", make_wav(1.0))
        self.assertIn("start en None", self.stub_log())

    def test_bad_wav_is_400_and_server_keeps_going(self):
        port = self.start_server()
        status, text = self.post(port, "/transcribe", make_wav(1.0, width=1))
        self.assertEqual(status, 400)
        self.assertIn("16-bit", text)
        status, _ = self.post(port, "/transcribe", b"not a wav at all")
        self.assertEqual(status, 400)
        status, _ = self.post(port, "/transcribe", make_wav(1.0))
        self.assertEqual(status, 200)

    def test_missing_length_is_411(self):
        port = self.start_server()
        with socket.create_connection(("127.0.0.1", port), timeout=10) as s:
            s.sendall(b"POST /transcribe HTTP/1.1\r\nHost: x\r\n\r\n")
            reply = s.recv(4096).decode("latin-1")
        self.assertIn(" 411 ", reply.split("\r\n", 1)[0])

    def test_too_big_is_413_without_reading_the_body(self):
        port = self.start_server()
        with socket.create_connection(("127.0.0.1", port), timeout=10) as s:
            s.sendall(b"POST /transcribe HTTP/1.1\r\nHost: x\r\nContent-Length: 999999999\r\n\r\n")
            reply = s.recv(4096).decode("latin-1")
        self.assertIn(" 413 ", reply.split("\r\n", 1)[0])

    def test_unknown_path_is_404(self):
        port = self.start_server()
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        conn.request("GET", "/nope")
        self.assertEqual(conn.getresponse().status, 404)

    def test_model_failure_is_500_and_server_keeps_serving(self):
        port = self.start_server_with({"STUB_FAIL": "1"})
        status, text = self.post(port, "/transcribe", make_wav(1.0))
        self.assertEqual(status, 500)
        self.assertIn("model exploded", text)
        self.assertEqual(self.health(port), 200)

    def start_server_with(self, extra_env):
        saved = self.env
        self.env = dict(saved, **extra_env)
        try:
            return self.start_server()
        finally:
            self.env = saved

    def health(self, port):
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        conn.request("GET", "/health")
        return conn.getresponse().status

    def test_one_take_at_a_time_a_second_is_503_and_health_answers_during_one(self):
        port = self.start_server_with({"STUB_SLOW": "1.0"})
        wav = make_wav(1.0)
        results = []

        def take():
            results.append(self.post(port, "/transcribe", wav)[0])

        first = threading.Thread(target=take)
        first.start()
        time.sleep(0.3)  # the first take is running
        busy_started = time.time()
        self.assertEqual(self.post(port, "/transcribe", wav)[0], 503)
        self.assertLess(time.time() - busy_started, 0.8, "a busy service must answer at once, not queue")
        self.assertEqual(self.health(port), 200)
        first.join(30)
        self.assertEqual(results, [200])
        # Free again once the first take is done.
        self.assertEqual(self.post(port, "/transcribe", wav)[0], 200)
        events = [line for line in self.stub_log() if line.startswith(("start", "end"))]
        self.assertEqual(events, ["start en None", "end ok"] * 2)

    def test_empty_body_is_400_and_server_keeps_going(self):
        port = self.start_server_with({})
        self.assertEqual(self.post(port, "/transcribe", b"")[0], 400)
        self.assertEqual(self.post(port, "/transcribe", make_wav(0.5))[0], 200)

if __name__ == "__main__":
    unittest.main()
