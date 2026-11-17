"""Exercise the actual preflight program against a JSON-RPC child."""

import json
import os
import subprocess
import sys

import pytest

from hcu.osworld.agents import claude_mcp_config, codex_config, gemini_settings
from hcu.osworld.runtime import public_mcp_probe_config, public_mcp_probe_script


SERVER = r'''
import json, os, sys
scenario = os.environ.get("PROBE_SCENARIO", "ok")
ablated = [n for n in os.environ.get("COMPUTER_USE_ABLATE", "").split(",") if n]
# The "ablated" server applies the switches and refuses unknown ones; the
# "stale" server ignores them, as a build without the switches does.
if scenario == "ablated" and any(n not in ("batching", "wait", "diff", "guard") for n in ablated):
    sys.exit("unknown COMPUTER_USE_ABLATE")
REFUSED = {"wait": "actions[0].type", "batching": "actions[1]", "diff": "observe"}
for line in sys.stdin:
    req = json.loads(line)
    if "id" not in req:
        continue
    method, params = req["method"], req.get("params", {})
    if method == "initialize":
        result = {"protocolVersion": "2025-03-26" if scenario == "legacy" else "2025-06-18"}
    elif method == "tools/list":
        result = {"tools": [{"name": x} for x in json.loads(sys.argv[1])]}
    else:
        arguments = params.get("arguments", {})
        mode = arguments.get("mode", "screenshot")
        uses = ("wait" if "wait" in json.dumps(arguments) else
                "batching" if len(arguments.get("actions", [])) > 1 else
                "diff" if arguments.get("observe") == "diff" else None)
        if uses and scenario == "ablated" and uses in ablated:
            evidence = {"ok": False, "code": "invalid_argument", "path": REFUSED[uses]}
            print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": {
                "isError": True, "content": [{"type": "text", "text": json.dumps(evidence)}]}}), flush=True)
            continue
        if mode == "apps":
            text = 'w1 "Fixture" app=fixture active\n'
            if scenario == "empty":
                text = "No top-level windows are visible to the accessibility provider."
        elif mode == "observe":
            text = 'app: "fixture"\nwindow: "Fixture" [ref=w1]\nax:\n - button "Test" [ref=r1]\n'
        elif mode == "find":
            text = 'window: "Fixture"\nmatches: 1\nr1 button "Test"\n'
        else:
            text = "screenshot"
        result = {"content": [{"type": "text", "text": '{"ok":true}'}, {"type": "text", "text": text}]}
        if mode == "screenshot" and scenario != "missing_image":
            result["content"].append({"type": "image", "data": "aW1hZ2U="})
        if scenario == "contract_error" and mode == "apps":
            print(json.dumps({"id": req["id"], "error": {"message": "facts minProperties"}}), flush=True)
            continue
        if scenario == "is_error" and mode == "observe":
            result["isError"] = True
        if scenario == "header_error" and mode == "find":
            result["content"][0]["text"] = '{"ok":false,"error":{"code":"provider_failed"}}'
        if scenario == "no_tree" and mode == "observe":
            result["content"] = [{"type": "text", "text": '{"ok":true}'}]
        if scenario == "zero_matches" and mode == "find":
            result["content"] = [{"type": "text", "text": 'matches: 0'}]
        if scenario == "structured_error" and mode == "apps":
            result["structuredContent"] = {"ok": False}
    print(json.dumps({"jsonrpc":"2.0", "id": req["id"], "result": result}), flush=True)
'''


def probe(tmp_path, scenario="ok", names=None, ablate=""):
    names = ["AX", "GUI"] if names is None else names
    server = tmp_path / "server.py"
    server.write_text(SERVER)
    env = {"PROBE_SCENARIO": scenario}
    if ablate:
        env["COMPUTER_USE_ABLATE"] = ablate
    config = {
        "command": sys.executable,
        "args": [str(server), json.dumps(names)],
        "env": env,
        "expected_tools": names,
        "protocol": "2026-07-28",
    }
    return subprocess.run(
        [sys.executable, "-c", public_mcp_probe_script(), json.dumps(config)],
        env={**os.environ, "PROBE_SCENARIO": "must-be-overridden"},
        capture_output=True, text=True, timeout=10,
    )


@pytest.mark.parametrize("names", [["AX", "GUI"], ["AX"], ["GUI"]])
def test_probe_reads_enabled_channels_with_configured_child_env(tmp_path, names):
    result = probe(tmp_path, names=names)
    assert result.returncode == 0, result.stderr
    report = json.loads(result.stdout)
    assert report["public_tools"] == names
    assert report["requested_protocol"] == "2026-07-28"
    assert report["negotiated_protocol"] == "2025-06-18"
    expected = ({"apps", "observe", "find"} if "AX" in names else set())
    expected |= {"screenshot"} if "GUI" in names else set()
    assert report["checks"] == {key: "passed" for key in expected}


@pytest.mark.parametrize("scenario", [
    "contract_error", "is_error", "header_error", "no_tree",
    "zero_matches", "structured_error", "missing_image", "legacy",
])
def test_probe_rejects_errors_or_missing_evidence(tmp_path, scenario):
    result = probe(tmp_path, scenario)
    assert result.returncode != 0
    assert "RuntimeError" in result.stderr


@pytest.mark.parametrize("ablate", ["guard", "wait", "batching,wait,diff,guard"])
def test_probe_confirms_the_server_applies_the_ablation(tmp_path, ablate):
    result = probe(tmp_path, "ablated", ablate=ablate)
    assert result.returncode == 0, result.stderr
    assert json.loads(result.stdout)["checks"]["ablation"] == "passed: " + ablate
    # A build without the switches runs full HCU; the probe must stop it.
    stale = probe(tmp_path, "stale", ablate=ablate)
    assert stale.returncode != 0
    assert "ignores the" in stale.stderr or "unknown COMPUTER_USE_ABLATE" in stale.stderr


def test_empty_desktop_does_not_require_creating_a_task_window(tmp_path):
    result = probe(tmp_path, "empty", ["AX"])
    assert result.returncode == 0, result.stderr
    checks = json.loads(result.stdout)["checks"]
    assert checks == {"apps": "passed", "observe": "skipped: no addressable windows",
                      "find": "skipped: no addressable windows"}


def test_probe_uses_rendered_codex_transport():
    config = codex_config(model="model", effort="xhigh", base_url="http://gateway/v1",
                          codex_home="/agent", base_instructions="/base.md",
                          mcp_command="/custom/mcp", channels="a11y,keyboard")
    spec = public_mcp_probe_config(config, "codex", ["AX"])
    assert spec["command"] == "/custom/mcp"
    assert spec["args"] == ["mcp"]
    assert spec["env"] == {"COMPUTER_USE_CHANNELS": "a11y,keyboard"}


def test_probe_preserves_claude_adapter_and_args():
    config = claude_mcp_config(adapter="/custom/adapter.py", mcp_command="/custom/mcp")
    spec = public_mcp_probe_config(config, "claude-code", ["AX", "GUI"])
    assert spec["command"] == "/usr/bin/python3"
    assert spec["args"] == ["/custom/adapter.py", "/custom/mcp", "mcp"]


def test_probe_uses_gemini_server_alias():
    config = gemini_settings(model="model", effort="native", mcp_command="/custom/mcp",
                             channels="gui,keyboard")
    spec = public_mcp_probe_config(config, "gemini-cli", ["GUI"])
    assert spec["command"] == "/custom/mcp"
    assert spec["args"] == ["mcp"]
    assert spec["env"] == {"COMPUTER_USE_CHANNELS": "gui,keyboard"}


def test_disabled_mcp_is_only_allowed_with_no_expected_tools():
    config = '[mcp_servers.computer_use]\nenabled=false\n'
    spec = public_mcp_probe_config(config, "codex", [])
    result = subprocess.run([sys.executable, "-c", public_mcp_probe_script(), json.dumps(spec)],
                            capture_output=True, text=True, timeout=5)
    assert result.returncode == 0, result.stderr
    assert json.loads(result.stdout)["skipped"] == "MCP disabled"
    with pytest.raises(ValueError, match="disabled"):
        public_mcp_probe_config(config, "codex", ["AX"])
