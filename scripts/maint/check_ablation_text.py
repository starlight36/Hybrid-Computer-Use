#!/usr/bin/env python3
"""Check that an ablated HCU no longer tells the model about what it removed.

Usage: check_ablation_text.py BINARY

For every subset of COMPUTER_USE_ABLATE (batching, wait, diff, guard) the
script starts BINARY as an MCP server, sends initialize, tools/list and AX
calls that use each removed mechanism, and generates the guides with
hcu.osworld.guide_ablation. It searches the server instructions, tool
definitions, error results and guides for the words of each removed
mechanism, and checks that every act example obeys the ablation. It also
checks that the server refuses to start on a misspelled ablation. No
desktop is needed: every call is rejected before input.

Exit status 1 lists each finding.
"""
from __future__ import annotations

import itertools
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO))

from hcu.osworld.guide_ablation import ABLATION_NAMES, ablated_guide  # noqa: E402

# Words of each mechanism. GUI keeps its own wait action and settle_ms, so
# the wait words name only the AX forms.
WORDS = {
    "wait": [r'"type":\s*"wait"', r"(?i)wait step", r'"settle"', r"settle needs",
             r"\bsettle\b(?!_ms)", r"timeout_ms", r"wait_timeout", r"waits with",
             r"`wait`"],
    "diff": [r"\bdiffs?\b", r"(?m)^effect:", r"(?m)^windows:", r"report what changed",
             r"reports the selection", r"reports what is now", r"reports each target",
             r"checked=false→true", r"listed as `opened`"],
    "batching": [r"ordered steps", r"into one `act`", r"(?i)start the batch with"],
    "guard": [],
}

# A call that uses each mechanism; the ablated server must reject it.
USES = {
    "wait": {"mode": "act", "actions": [{"type": "wait", "window": "Export"}]},
    "diff": {"mode": "act", "observe": "diff", "actions": [{"type": "click", "ref": "r1"}]},
    "batching": {"mode": "act", "actions": [{"type": "click", "ref": "r1"}, {"type": "click", "ref": "r2"}]},
}
BAD_CALL = {"mode": "act", "actions": []}


def mcp_session(binary: str, env: dict[str, str], calls: list[dict]) -> list[dict]:
    process = subprocess.Popen(
        [binary, "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, env=env, text=True,
    )
    assert process.stdin is not None and process.stdout is not None
    requests = [
        ("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                        "clientInfo": {"name": "hcu-ablation-check", "version": "1"}}),
        ("tools/list", {}),
    ] + [("tools/call", {"name": "AX", "arguments": call}) for call in calls]
    responses = []
    try:
        for request_id, (method, params) in enumerate(requests, 1):
            process.stdin.write(json.dumps(
                {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}) + "\n")
            process.stdin.flush()
            while True:
                line = process.stdout.readline()
                if not line:
                    raise RuntimeError(f"EOF before {method}: {process.stderr.read()}")
                response = json.loads(line)
                if response.get("id") == request_id:
                    if "error" in response:
                        raise RuntimeError(f"{method}: {response['error']}")
                    responses.append(response["result"])
                    break
    finally:
        process.terminate()
        process.wait(timeout=5)
    return responses


def act_examples(text: str) -> list[list[dict]]:
    """The action lists of every act call written in text."""
    found = []
    decoder = json.JSONDecoder()
    for match in re.finditer(r'\{\\?"mode\\?":\s*\\?"act\\?"', text):
        chunk = text[match.start():]
        if chunk.startswith('{\\"'):
            continue  # escaped copy inside JSON; the raw copy is checked
        try:
            call, _ = decoder.raw_decode(chunk)
        except json.JSONDecodeError:
            continue
        found.append(call.get("actions", []))
    return found


def check(binary: str, ablations: tuple[str, ...], guard_log: str) -> list[str]:
    env = {"PATH": "/usr/bin:/bin", "HOME": os.environ.get("HOME", "/tmp")}
    if ablations:
        env["COMPUTER_USE_ABLATE"] = ",".join(ablations)
    if "guard" in ablations:
        env["COMPUTER_USE_GUARD_LOG"] = guard_log
    calls = [BAD_CALL] + [USES[name] for name in ablations if name in USES]
    initialize, tools, *results = mcp_session(binary, env, calls)
    names = [tool["name"] for tool in tools["tools"]]
    findings = [] if names == ["AX", "GUI"] else [f"public tools {names}"]
    texts = {"instructions": initialize.get("instructions", "")}
    for tool in tools["tools"]:
        # Raw description plus the schema, so quoted examples are searchable.
        texts[f"{tool['name']} description"] = tool["description"]
        schema = tool["inputSchema"]
        if tool["name"] == "GUI":
            # Only settle_ms of GUI speaks about AX mechanisms.
            schema = schema["oneOf"][1]["properties"]["settle_ms"]
        texts[f"{tool['name']} schema"] = json.dumps(schema)
    for call, result in zip(calls, results):
        text = "\n".join(item.get("text", "") for item in result.get("content", []))
        texts[f"result of {json.dumps(call)}"] = text
        if not result.get("isError") or '"code":"invalid_argument"' not in text:
            findings.append(f"{json.dumps(call)} was not rejected as invalid_argument: {text[:200]}")
    for file_name in ("base.md", "hybrid.md"):
        source = (REPO / "instructions" / file_name).read_text(encoding="utf-8")
        texts[file_name] = ablated_guide(source, file_name, ablations)
    for name in ablations:
        for label, text in texts.items():
            if label.startswith("result of") and name in USES and label.endswith(json.dumps(USES[name])):
                continue  # the rejected call may echo what it sent
            for word in WORDS[name]:
                match = re.search(word, text)
                if match:
                    start = max(0, match.start() - 50)
                    findings.append(f"{name}: {label} still has {match.group(0)!r}: ...{text[start:match.end() + 50]!r}")
    for label, text in texts.items():
        if label.startswith("result of"):
            continue
        for actions in act_examples(text):
            inputs = [step for step in actions if step.get("type") != "wait"]
            if "batching" in ablations and len(inputs) > 1:
                findings.append(f"batching: {label} shows an act with {len(inputs)} input steps")
            if "wait" in ablations and len(inputs) != len(actions):
                findings.append(f"wait: {label} shows an act with a wait step")
    return findings


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    binary = sys.argv[1]
    failed = False
    with tempfile.TemporaryDirectory() as scratch:
        for count in range(len(ABLATION_NAMES) + 1):
            for ablations in itertools.combinations(ABLATION_NAMES, count):
                findings = check(binary, ablations, os.path.join(scratch, "guard.jsonl"))
                label = ",".join(ablations) or "(none)"
                print(f"{'FAIL' if findings else 'ok  '} {label}")
                for finding in findings:
                    print(f"     {finding}")
                failed = failed or bool(findings)
        for bad in ("waits", "wait,wait", "guard"):
            env = {"PATH": "/usr/bin:/bin", "COMPUTER_USE_ABLATE": bad}
            started = subprocess.run([binary, "mcp"], input="", capture_output=True,
                                     text=True, env=env, timeout=30)
            refused = started.returncode != 0 and "COMPUTER_USE_ABLATE" in started.stderr
            print(f"{'ok  ' if refused else 'FAIL'} refuses COMPUTER_USE_ABLATE={bad!r}"
                  + ("" if refused else f": exit {started.returncode}"))
            failed = failed or not refused
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
