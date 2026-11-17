"""The runner side of the HCU mechanism ablations, without a guest or model calls."""

import argparse
import importlib.util
import hashlib
import json
from pathlib import Path
import sys
from types import ModuleType
from unittest.mock import Mock

import pytest

from hcu.osworld import agents
from hcu.osworld.config import load_config, runner_command
from hcu.osworld.guest import GUEST_GUARD_LOG
from hcu.osworld.guide_ablation import ABLATION_NAMES, ablated_guide
from hcu.osworld.runtime import public_mcp_probe_config

ROOT = Path(__file__).resolve().parents[1]
GROUPS = {
    "full": [],
    "no-batching": ["batching"],
    "no-wait": ["wait"],
    "no-diff": ["diff"],
    "no-guard": ["guard"],
    "no-all4": list(ABLATION_NAMES),
}


def write_config(path: Path, **values) -> Path:
    config = {"tasks": ["a"], "results_dir": str(path.parent / "runs"), **values}
    path.write_text(json.dumps(config), encoding="utf-8")
    return path


# --- configuration and command line -------------------------------------------------


def test_config_ablate_is_canonical_and_reaches_the_runner(tmp_path):
    config = load_config(write_config(tmp_path / "run.json", ablate="guard, batching"))
    assert config["ablate"] == ["batching", "guard"]
    command = runner_command(config, config["tasks"][0], tmp_path / "a")
    assert command[command.index("--ablate") + 1] == "batching,guard"
    listed = load_config(write_config(tmp_path / "list.json", ablate=["diff", "wait"]))
    assert listed["ablate"] == ["wait", "diff"]


@pytest.mark.parametrize("values", [{}, {"ablate": []}, {"ablate": ""}])
def test_config_without_ablation_passes_no_flag(tmp_path, values):
    config = load_config(write_config(tmp_path / "run.json", **values))
    assert "--ablate" not in runner_command(config, config["tasks"][0], tmp_path / "a")
    # A config without the key keeps its saved form, so existing runs resume.
    assert ("ablate" in config) == ("ablate" in values)


@pytest.mark.parametrize("value", ["gaurd", "wait,wait", "wait,,diff", ["wait", 1]])
def test_config_rejects_invalid_ablation(tmp_path, value):
    with pytest.raises(ValueError):
        load_config(write_config(tmp_path / "run.json", ablate=value))


@pytest.mark.parametrize(
    "values, message",
    [
        ({"instructions_file": "custom.md"}, "instructions file"),
        ({"instructions_file": ""}, "instructions file"),
        ({"base_instructions_file": "custom-base.md"}, "base instructions file"),
        ({"code_mode": True}, "code-mode"),
    ],
)
def test_config_refuses_guides_that_may_describe_removed_mechanisms(tmp_path, values, message):
    with pytest.raises(ValueError, match=message):
        load_config(write_config(tmp_path / "run.json", ablate="wait", **values))
    # Without an ablation the same guide choice stays valid.
    load_config(write_config(tmp_path / "plain.json", **values))


def test_config_accepts_the_default_guides_named_explicitly(tmp_path):
    config = load_config(write_config(
        tmp_path / "run.json", ablate="diff",
        instructions_file=str(ROOT / "instructions/hybrid.md"),
        base_instructions_file=str(ROOT / "instructions/base.md"),
    ))
    assert config["ablate"] == ["diff"]


def test_ablation_example_configs_load_with_their_group():
    paths = sorted((ROOT / "configs/ablation").glob("*.example.json"))
    assert {path.name for path in paths} == {
        f"osworld-v1-codex-{label}.example.json" for label in GROUPS
    }
    reference = load_config(ROOT / "configs/osworld-v1-codex-queue.example.json")
    for path in paths:
        label = path.name.removeprefix("osworld-v1-codex-").removesuffix(".example.json")
        config = load_config(path)
        assert config["ablate"] == GROUPS[label]
        for key in ("benchmark_version", "agent", "model", "effort", "max_model_calls", "tasks"):
            assert config[key] == reference[key], (path, key)
        command = runner_command(config, config["tasks"][0], Path("/unused"))
        assert ("--ablate" in command) == bool(GROUPS[label])


# --- harness MCP environments --------------------------------------------------------


def render(agent: str, **kwargs) -> str:
    if agent == "codex":
        return agents.codex_config(
            model="m", effort="xhigh", base_url="http://gateway/v1", codex_home="/codex",
            base_instructions="/base.md", mcp_command="/computer-use", **kwargs,
        )
    if agent == "claude-code":
        return agents.claude_mcp_config(**kwargs)
    if agent == "gemini-cli":
        return agents.gemini_settings(model="m", effort="high", **kwargs)
    return agents.grok_config(model="m", base_url="http://gateway/v1", effort="xhigh", **kwargs)


HARNESSES = ("codex", "claude-code", "gemini-cli", "grok-build")


@pytest.mark.parametrize("agent", HARNESSES)
@pytest.mark.parametrize("channels", ["", "a11y,keyboard", "gui,keyboard"])
def test_full_hcu_renders_the_same_harness_config(agent, channels):
    plain = render(agent, channels=channels)
    assert render(agent, channels=channels, ablate=()) == plain
    assert "COMPUTER_USE_ABLATE" not in plain and "COMPUTER_USE_GUARD_LOG" not in plain
    if agent == "gemini-cli":
        redaction = json.loads(plain)["security"]["environmentVariableRedaction"]
        assert redaction == {"allowed": ["COMPUTER_USE_CHANNELS"]}


@pytest.mark.parametrize("agent", HARNESSES)
@pytest.mark.parametrize(
    "ablate, expected",
    [
        (("wait",), {"COMPUTER_USE_ABLATE": "wait"}),
        (("batching", "diff"), {"COMPUTER_USE_ABLATE": "batching,diff"}),
        (ABLATION_NAMES, {
            "COMPUTER_USE_ABLATE": "batching,wait,diff,guard",
            "COMPUTER_USE_GUARD_LOG": GUEST_GUARD_LOG,
        }),
        (("guard",), {
            "COMPUTER_USE_ABLATE": "guard", "COMPUTER_USE_GUARD_LOG": GUEST_GUARD_LOG,
        }),
    ],
)
@pytest.mark.parametrize("channels", ["", "gui,keyboard"])
def test_ablation_reaches_every_harness_mcp_env(agent, ablate, expected, channels):
    pytest.importorskip("tomllib")
    config = render(agent, channels=channels, ablate=ablate)
    want = {**({"COMPUTER_USE_CHANNELS": channels} if channels else {}), **expected}
    # The preflight probe starts the server with the env it reads from the config.
    spec = public_mcp_probe_config(config, agent, list(agents.public_tools(channels)))
    assert spec["env"] == want
    if agent == "gemini-cli":
        allowed = json.loads(config)["security"]["environmentVariableRedaction"]["allowed"]
        assert set(want) <= set(allowed)


def test_harness_rejects_ablations_out_of_canonical_order():
    with pytest.raises(ValueError, match="canonical"):
        agents.mcp_server_env("", ("guard", "wait"))
    with pytest.raises(ValueError):
        agents.mcp_server_env("", ("typo",))


# --- runner modules loaded without OSWorld -------------------------------------------


@pytest.fixture
def runner(monkeypatch):
    """Fresh runner modules with the OSWorld imports replaced by test doubles."""
    stubs = {
        "desktop_env": {},
        "desktop_env.cache": {"run_scoped_cache_dir": None},
        "desktop_env.desktop_env": {
            "DesktopEnv": Mock(side_effect=AssertionError("No guest may be constructed")),
        },
        "desktop_env.file_source": {"configure": None},
        "hcu.osworld.preflight": {"docker_runtime": None},
        "hcu.osworld.task_loader": {"load_task_config": None},
    }
    for name, attributes in stubs.items():
        module = ModuleType(name)
        vars(module).update(attributes)
        monkeypatch.setitem(sys.modules, name, module)
    modules = {}
    for name in ("staging", "guest_lifecycle", "phases", "run_task"):
        spec = importlib.util.spec_from_file_location(
            f"hcu.osworld.{name}", ROOT / f"hcu/osworld/{name}.py",
        )
        modules[name] = importlib.util.module_from_spec(spec)
        monkeypatch.setitem(sys.modules, spec.name, modules[name])
        spec.loader.exec_module(modules[name])
    staging = modules["staging"]
    monkeypatch.setattr(staging, "load_task_config", lambda *a, **kw: {"instruction": "offline"})
    monkeypatch.setattr(staging, "resolve_guest_image", lambda *a: "/unused/image.qcow2")
    return modules


def parse(runner, monkeypatch, *flags):
    monkeypatch.setattr(sys, "argv", ["run_task", "--task-id", "t", *flags])
    return runner["run_task"].parse_args()


def test_run_task_parses_ablate_in_canonical_order(runner, monkeypatch):
    assert parse(runner, monkeypatch).ablate == ()
    assert parse(runner, monkeypatch, "--ablate", "guard,wait").ablate == ("wait", "guard")


@pytest.mark.parametrize(
    "flags",
    [
        ["--ablate", "batching,typo"],
        ["--ablate", "diff,diff"],
        ["--ablate", "wait", "--instructions-file", ""],
        ["--ablate", "wait", "--instructions-file", "instructions/ax-only.md"],
        ["--ablate", "wait", "--code-mode"],
    ],
)
def test_run_task_rejects_invalid_ablation_before_any_work(runner, monkeypatch, flags):
    with pytest.raises(SystemExit) as exit_info:
        parse(runner, monkeypatch, *flags)
    assert exit_info.value.code == 2


def prepared_run(runner, tmp_path, agent="codex", ablate=()):
    artifact = tmp_path / "artifact"
    artifact.touch()
    profile = agents.AGENT_PROFILES[agent]
    args = argparse.Namespace(
        task_id="t", agent=agent, model=profile["default_model"],
        effort=profile["default_effort"], gateway="http://gateway:8611",
        result_dir=str(tmp_path / "task"), token_log=str(tmp_path / "tokens.jsonl"),
        max_model_calls=0, artifact_dir=str(tmp_path), agent_binary=str(artifact),
        mcp_binary=str(artifact),
        instructions_file=str(ROOT / "instructions/hybrid.md"),
        base_instructions_file=str(ROOT / "instructions/base.md"),
        channels="", code_mode=False, task_domain="", benchmark_version="v1",
        run_started_at="2000-01-01T00:00:00+00:00", timezone="UTC",
        asset_cache="/unused", offline_assets=False, retain_container=False, ablate=ablate,
    )
    if agent == "gemini-cli":
        (tmp_path / "node").touch()
    staging = runner["staging"]
    run = staging.prepare_run(args)
    staging.stage_local_files(run)
    run.meta = staging.initial_meta(run, {})
    return run


def test_full_hcu_uses_the_default_guides_unchanged(runner, tmp_path):
    run = prepared_run(runner, tmp_path)
    assert run.instructions_file == ROOT / "instructions/hybrid.md"
    assert run.base_instructions_file == ROOT / "instructions/base.md"
    assert not (run.result_dir / "ablated-instructions").exists()
    assert run.meta["ablate"] == []


@pytest.mark.parametrize("agent", HARNESSES)
def test_ablation_run_gives_the_agent_ablated_guides(runner, tmp_path, agent):
    ablate = ("wait", "diff")
    run = prepared_run(runner, tmp_path, agent, ablate)
    guides = run.result_dir / "ablated-instructions"
    assert run.instructions_file == guides / "hybrid.md"
    assert run.base_instructions_file == guides / "base.md"
    hybrid = ablated_guide(
        (ROOT / "instructions/hybrid.md").read_text(encoding="utf-8"), "hybrid.md", ablate,
    )
    assert hybrid != (ROOT / "instructions/hybrid.md").read_text(encoding="utf-8")
    assert run.instructions_file.read_text(encoding="utf-8") == hybrid
    # Each harness receives the ablated guide through its own channel.
    if agent in {"codex", "gemini-cli"}:
        assert run.instructions_copy.read_text(encoding="utf-8") == hybrid
    else:
        assert run.base_instructions_copy.read_text(encoding="utf-8").endswith(hybrid)
    assert "COMPUTER_USE_ABLATE" in run.config_path.read_text(encoding="utf-8")
    assert run.meta["ablate"] == ["wait", "diff"]
    assert run.meta["mcp_binary_sha256"] == hashlib.sha256(run.mcp_binary.read_bytes()).hexdigest()
    assert run.meta["instructions_file"] == str(guides / "hybrid.md")


@pytest.mark.parametrize("ablate", [(), ("batching", "guard")])
def test_result_records_the_ablation(runner, tmp_path, ablate):
    run = prepared_run(runner, tmp_path, ablate=ablate)
    result = runner["phases"].write_result(run, "error", error="test")
    assert result["ablate"] == list(ablate)
    saved = json.loads((run.result_dir / "result.json").read_text(encoding="utf-8"))
    assert saved["ablate"] == list(ablate)
    assert json.loads((run.result_dir / "meta.json").read_text())["ablate"] == list(ablate)


class FakeEnv:
    def __init__(self):
        self.setup_controller = type("Controller", (), {"http_server": "http://guest:5000"})()
        self.closed = False

    def close(self):
        self.closed = True


@pytest.mark.parametrize("guest_log", [b'{"decision":"skipped"}\n', None])
def test_guard_ablation_copies_the_guest_guard_log(runner, tmp_path, monkeypatch, guest_log):
    lifecycle = runner["guest_lifecycle"]
    copied = []

    def save(base_url, guest_path, local_path):
        copied.append((base_url, guest_path))
        if guest_log is not None:
            local_path.write_bytes(guest_log)
        return guest_log

    monkeypatch.setattr(lifecycle, "save_guest_artifact", save)
    run = prepared_run(runner, tmp_path, ablate=("guard",))
    env = FakeEnv()
    lifecycle.close_guest(run, env)
    assert copied == [("http://guest:5000", GUEST_GUARD_LOG)]
    assert env.closed
    meta = json.loads((run.result_dir / "meta.json").read_text())
    if guest_log is None:
        assert meta["guard_log"] is None
        assert not (run.result_dir / "guard.jsonl").exists()
    else:
        assert meta["guard_log"] == "guard.jsonl"
        assert (run.result_dir / "guard.jsonl").read_bytes() == guest_log


@pytest.mark.parametrize("ablate", [(), ("wait", "diff")])
def test_runs_with_the_guard_do_not_read_a_guard_log(runner, tmp_path, monkeypatch, ablate):
    lifecycle = runner["guest_lifecycle"]
    monkeypatch.setattr(lifecycle, "save_guest_artifact", Mock(side_effect=AssertionError))
    run = prepared_run(runner, tmp_path, ablate=ablate)
    lifecycle.close_guest(run, FakeEnv())
    assert "guard_log" not in run.meta
