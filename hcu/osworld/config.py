"""The run configuration: one JSON file that lists tasks and run options.

Relative paths in the file are relative to the file itself. Omitted paths
default to locations inside this repository (``artifacts/``, ``runs/``,
``assets/``, ``instructions/``). Unknown keys are rejected so a typo cannot
silently fall back to a default.
"""

from __future__ import annotations

import json
from pathlib import Path
import sys

from hcu.osworld.guide_ablation import ablation_value, parse_ablation
from hcu.paths import (
    DEFAULT_ASSET_CACHE,
    DEFAULT_BASE_INSTRUCTIONS_FILE,
    DEFAULT_INSTRUCTIONS_FILE,
    DEFAULT_RESULTS_DIR,
)

# Config key -> run_task flag, for options passed through unchanged.
VALUE_OPTIONS = {
    "benchmark_version": "--benchmark-version",
    "agent": "--agent",
    "model": "--model",
    "effort": "--effort",
    "gateway": "--gateway",
    "token_log": "--token-log",
    "guest_image": "--guest-image",
    "artifact_dir": "--artifact-dir",
    "agent_binary": "--agent-binary",
    "mcp_binary": "--mcp-binary",
    "instructions_file": "--instructions-file",
    "base_instructions_file": "--base-instructions-file",
    "channels": "--channels",
    "client_password": "--client-password",
    "max_model_calls": "--max-model-calls",
    "timeout_seconds": "--timeout",
    "asset_cache": "--asset-cache",
    "run_timezone": "--timezone",
}
SWITCHES = {
    "code_mode": "--code-mode",
    "offline_assets": "--offline-assets",
    "retain_containers": "--retain-container",
    "enable_vnc": "--enable-vnc",
    "enable_recording": "--enable-recording",
}
TASK_OPTIONS = {
    "task_domain": "--task-domain",
    "volume_size": "--volume-size",
    "ram_size": "--ram-size",
}
PATH_KEYS = (
    "token_log", "guest_image", "artifact_dir", "agent_binary", "mcp_binary",
    "instructions_file", "base_instructions_file", "asset_cache", "results_dir",
)
# ``ablate`` is a comma-separated string or a list; the loader stores it as a
# list in canonical order and passes it on as one --ablate value.
ABLATION_KEY = "ablate"
QUEUE_KEYS = ("run_id", "tasks", "results_dir", "max_active_tasks", "launch_stagger_seconds")
CONFIG_KEYS = (
    frozenset(VALUE_OPTIONS) | set(SWITCHES) | set(TASK_OPTIONS) | set(QUEUE_KEYS)
    | {ABLATION_KEY}
)
TASK_KEYS = frozenset(TASK_OPTIONS) | {"id", "stratum"}


def single_path_component(value: object, label: str) -> str:
    if (
        not isinstance(value, str) or not value or value != value.strip()
        or value in {".", ".."} or "/" in value or "\\" in value
    ):
        raise ValueError(f"{label} must be a nonempty single path component: {value!r}")
    return value


def ablation_input_error(
    ablate: tuple[str, ...],
    *,
    instructions_file: str | Path | None,
    base_instructions_file: str | Path | None,
    code_mode: bool,
) -> str | None:
    """Why the selected guides cannot run under ``ablate``, or None.

    An ablation run uses copies of the default guides without the passages
    that describe the removed mechanisms. Other guides, no guide, and the
    code-mode add-on may still describe them, so they are refused.
    """
    if not ablate:
        return None
    if code_mode:
        return "--ablate cannot be combined with --code-mode"
    for label, value, default in (
        ("instructions file", instructions_file, DEFAULT_INSTRUCTIONS_FILE),
        ("base instructions file", base_instructions_file, DEFAULT_BASE_INSTRUCTIONS_FILE),
    ):
        if value is None:
            continue
        if not value or Path(value).expanduser().resolve() != default.resolve():
            return (
                f"--ablate uses ablated copies of the default guides; "
                f"the {label} must be {default}, got {str(value)!r}"
            )
    return None


def load_config(path: Path) -> dict:
    """Read, validate and normalize a run configuration file."""
    path = path.expanduser().resolve()
    config = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(config, dict):
        raise ValueError(f"{path}: expected a JSON object")
    unknown = sorted(set(config) - CONFIG_KEYS)
    if unknown:
        raise ValueError("Unknown config keys: " + ", ".join(unknown))
    config.setdefault("run_id", path.stem)
    config.setdefault("benchmark_version", "v2")
    config.setdefault("max_active_tasks", 1)
    if config["benchmark_version"] not in {"v1", "v2"}:
        raise ValueError("benchmark_version must be v1 or v2")
    single_path_component(config["run_id"], "run_id")
    tasks = []
    for entry in config.get("tasks") or []:
        task = {"id": entry} if isinstance(entry, str) else dict(entry)
        unknown = sorted(set(task) - TASK_KEYS)
        if unknown:
            raise ValueError(f"Unknown keys for task {task.get('id')}: " + ", ".join(unknown))
        single_path_component(task.get("id"), "task id")
        tasks.append(task)
    if not tasks:
        raise ValueError("tasks must not be empty")
    if len({task["id"] for task in tasks}) != len(tasks):
        raise ValueError("task ids must be unique")
    config["tasks"] = tasks
    if ABLATION_KEY in config:
        config[ABLATION_KEY] = list(parse_ablation(config[ABLATION_KEY]))
    if int(config["max_active_tasks"]) < 1:
        raise ValueError("max_active_tasks must be at least 1")
    config.setdefault("results_dir", str(DEFAULT_RESULTS_DIR))
    config.setdefault("asset_cache", str(DEFAULT_ASSET_CACHE))
    for key in PATH_KEYS:
        # An empty instructions_file disables the profile; keep it empty.
        if config.get(key):
            config[key] = str((path.parent / Path(config[key]).expanduser()).resolve())
    error = ablation_input_error(
        tuple(config.get(ABLATION_KEY) or ()),
        instructions_file=config.get("instructions_file"),
        base_instructions_file=config.get("base_instructions_file"),
        code_mode=bool(config.get("code_mode")),
    )
    if error:
        raise ValueError(error)
    # The gateway appends here; the runner reads budget and usage from it.
    config.setdefault(
        "token_log", str(Path(config["results_dir"]) / config["run_id"] / "gateway-tokens.jsonl")
    )
    return config


def run_dir(config: dict) -> Path:
    return Path(config["results_dir"]) / config["run_id"]


def runner_command(config: dict, task: dict, result_dir: Path, run_started_at: str = "") -> list[str]:
    """The run_task command line for one task of a configuration."""
    command = [
        sys.executable, "-m", "hcu.osworld.run_task",
        "--task-id", task["id"], "--result-dir", str(result_dir),
    ]
    if run_started_at:
        command += ["--run-started-at", run_started_at]
    for key, flag in VALUE_OPTIONS.items():
        if config.get(key) is not None:
            command += [flag, str(config[key])]
    for key, flag in TASK_OPTIONS.items():
        value = task.get(key, config.get(key))
        if value is not None:
            command += [flag, str(value)]
    if config.get(ABLATION_KEY):
        command += ["--ablate", ablation_value(tuple(config[ABLATION_KEY]))]
    command += [flag for key, flag in SWITCHES.items() if config.get(key)]
    return command
