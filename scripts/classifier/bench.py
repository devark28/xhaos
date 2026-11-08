#!/usr/bin/env python3
"""Quick latency/accuracy bench for the command classifier model (stdlib only).

The authoritative eval is app/classifier/eval_test.go (it goes through the Go
client, the floor and the real gate). This script is for fast model-side
experiments: it reads systemPrompt, userPrefix, schema and maxTokens straight
from app/classifier/prompt.go and builds the same request as client.go
(system + userPrefix + JSON-quoted command, temperature 0, strict json_schema),
then reports model-only accuracy, false allows/asks, latency and token counts.
--max-tokens and --reason-max override the shipped values to measure their
effect on latency; nothing is written back to prompt.go.

Usage: bench.py [--url http://127.0.0.1:18080] [--max-tokens N] [--reason-max N] [--out f.json]
"""
import argparse
import json
import os
import re
import statistics
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
PROMPT_GO = os.path.join(HERE, "..", "..", "app", "classifier", "prompt.go")


def load_prompt_go(path):
    src = open(path).read()

    def const(name):
        m = re.search(r"const " + name + r" = (`[^`]*`|\"(?:[^\"\\]|\\.)*\"|\d+)", src)
        if not m:
            raise SystemExit(f"{path}: const {name} not found")
        v = m.group(1)
        if v.startswith("`"):
            return v[1:-1]
        if v.startswith('"'):
            return json.loads(v)  # Go and JSON escapes agree for these strings
        return int(v)

    return const("systemPrompt"), const("userPrefix"), json.loads(const("schema")), const("maxTokens")


def ask(url, system, prefix, schema, max_tokens, command, timeout=60):
    body = {
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": prefix + json.dumps(command, ensure_ascii=False)},
        ],
        "temperature": 0,
        "max_tokens": max_tokens,
        "stream": False,
        "response_format": {"type": "json_schema",
                            "json_schema": {"name": "command_verdict", "strict": True, "schema": schema}},
    }
    req = urllib.request.Request(url.rstrip("/") + "/v1/chat/completions", data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        data = json.load(resp)
    ms = (time.perf_counter() - t0) * 1000
    content = data["choices"][0]["message"]["content"]
    try:
        v = json.loads(content)
        verdict, reason = v.get("verdict", "error"), v.get("reason", "")
    except json.JSONDecodeError:
        verdict, reason = "error", "bad json: " + content[:80]
    return verdict, reason, ms, data.get("timings", {})


def pct(sorted_vals, p):
    rank = max(0, min(len(sorted_vals) - 1, -int(-p * len(sorted_vals) // 1) - 1))
    return sorted_vals[rank]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://127.0.0.1:18080")
    ap.add_argument("--data", default=os.path.join(HERE, "eval.jsonl"))
    ap.add_argument("--max-tokens", type=int)
    ap.add_argument("--reason-max", type=int, help="override the schema's reason maxLength")
    ap.add_argument("--out")
    a = ap.parse_args()

    system, prefix, schema, max_tokens = load_prompt_go(PROMPT_GO)
    if a.max_tokens:
        max_tokens = a.max_tokens
    if a.reason_max:
        schema["properties"]["reason"]["maxLength"] = a.reason_max
    with open(a.data) as f:
        cases = [json.loads(line) for line in f if line.strip()]

    for cmd in ("go build ./...", "npm install", "make test"):  # warm-up, not scored
        ask(a.url, system, prefix, schema, max_tokens, cmd)

    results = []
    for c in cases:
        try:
            v, r, ms, tm = ask(a.url, system, prefix, schema, max_tokens, c["command"])
        except Exception as e:  # noqa: BLE001 - fail closed, keep going
            v, r, ms, tm = "error", str(e), 0.0, {}
        results.append(dict(c, model=v, reason=r, latency_ms=round(ms, 1), prompt_n=tm.get("prompt_n"),
                            cache_n=tm.get("cache_n"), predicted_n=tm.get("predicted_n")))

    ok = sum(1 for x in results if (x["model"] if x["model"] == "allow" else "ask") == x["expect"])
    fa = [x for x in results if x["expect"] == "ask" and x["model"] == "allow"]
    fk = [x for x in results if x["expect"] == "allow" and x["model"] != "allow"]
    lat = sorted(x["latency_ms"] for x in results if x["model"] != "error")
    gen = [x["predicted_n"] for x in results if x["predicted_n"]]
    print(f"max_tokens {max_tokens}  reason maxLength {schema['properties']['reason'].get('maxLength')}")
    print(f"lines {len(results)}  model-only accuracy {100 * ok / len(results):.1f}%  "
          f"false allows {len(fa)}  false asks {len(fk)}  errors {sum(x['model'] == 'error' for x in results)}")
    if lat:
        print(f"latency ms: median {statistics.median(lat):.0f}  p95 {pct(lat, .95):.0f}  max {lat[-1]:.0f}")
    if gen:
        print(f"generated tokens: median {statistics.median(gen):.0f}  max {max(gen)}")
    for x in fa:
        print(f"  FALSE ALLOW [{x['category']}] {x['command']}  -- {x['reason']}")
    if a.out:
        with open(a.out, "w") as f:
            json.dump(results, f, indent=1)


if __name__ == "__main__":
    main()
