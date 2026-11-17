"""Interaction guides for HCU mechanism ablations.

``COMPUTER_USE_ABLATE`` removes HCU mechanisms from the computer-use server
(see ``computer-use/internal/contract/ablation.go``). The guides given to the
agent must not describe a removed mechanism either, so this module deletes
the passages that describe it. It also adds one sentence per ablation that
states how the ablated tools behave, the same fact the tool description
states, and no new advice: otherwise an ablation run would also measure
the agent learning the changed rules from rejected calls. Nothing else
changes.

The passages are listed here as exact text, not marked inside the guides:
the guides are model input, and markers in them would change the text of
every run, including runs without an ablation. Each passage must occur
exactly once in its file. Without ablations the guides are returned
unchanged.

Usage: python -m hcu.osworld.guide_ablation ABLATE OUTPUT_DIR [INSTRUCTIONS_DIR]
writes the ablated ``base.md`` and ``hybrid.md`` to OUTPUT_DIR.
"""

from __future__ import annotations

import sys
from pathlib import Path

ABLATION_ENV = "COMPUTER_USE_ABLATE"
GUARD_LOG_ENV = "COMPUTER_USE_GUARD_LOG"
# Canonical order; the server accepts the same names.
ABLATION_NAMES = ("batching", "wait", "diff", "guard")

_PLAN_FROM_ONE_READ = """\
- Plan from one read. After `observe` or `find`, put every action you can
  already determine into one `act`, while later refs and focus remain valid.
  Split when the next action needs a new observation.
- Use `activate` with a `wN` or title to switch the batch target.
  Later refs and ref-less steps use that window. AX rejects refs from another
  window without explicit `activate`; activate back before reusing the
  previous window's refs. Each step reports its window; the final observation
  uses the last executed window.
"""

_ACT_REPORT = """\
- Every `act` result starts with what happened, then a diff:

```text
effect: r58 checkbox "Interface" checked=false→true
windows: opened w5 "Insert Sheet" (dialog, focused)
```

  `effect` gives each target's changed fields, `no visible change`, or
  `unknown` with the reason. `(only state flags changed in this window)` means
  no page, content, or window changed; if the control should have switched
  something, the application did not respond. `windows` names windows that opened, closed, or
  took focus, with a `wN` you can use at once. When these lines already show
  the result, do not read or wait again. Continue from the diff for other
  changes; read again only when the next decision needs more.
"""

_WAIT_STEPS = """\
- One `act` can mix actions and `wait` steps; the first failed step stops
  the batch. Add a `wait` only when a later step needs a state that may
  arrive after the action returns: a slow dialog, a menu item, a value
  computed later. A checkbox that shows `checked=false→true`, or a dialog
  listed as `opened`, needs no wait.
- Wait for a known state with `window`, `query`, `ref`, or `text`. When you
  know the UI will change but not what it becomes, such as the exact title of
  the next dialog, use `{"type":"wait","settle":true}`. It waits until the UI
  changes and then stops changing, names what changed, and fails with "no
  change" only when nothing changed. Keys after it go to the window that took
  focus. Give a slow operation a longer `timeout_ms`.
"""

# The examples batch several actions with wait steps between them.
_ACT_EXAMPLES = """
```json
{"mode":"act","actions":[{"type":"click","ref":"r12"},{"type":"wait","window":"Export"},{"type":"type","text":"q3.csv"},{"type":"keypress","keys":["ENTER"]},{"type":"wait","window":"Export","gone":true}]}
{"mode":"act","actions":[{"type":"click","ref":"r7"},{"type":"wait","query":{"role":"menu item","name_contains":"Paragraph"}},{"type":"click","ref":"r9"}]}
```

"""

_SELECT_READBACK = """ The result reports what is now
  selected; check it before applying the command."""

_WAIT_TIMEOUT = """\
- `wait_timeout`: the batch stopped at that `wait`; the steps before it ran
  and none after it. The message says what was seen instead, or that the
  state could not be read. Read the returned state and revise the plan; do
  not resend the same batch.
"""

# The wait advice names report lines that the diff ablation removes.
_REPORT_NEEDS_NO_WAIT = """ A checkbox that shows `checked=false→true`, or a dialog
  listed as `opened`, needs no wait."""

_APP_STARTING_WAIT = """if the application is still starting, wait once for
  its window with `{"mode":"act","actions":[{"type":"wait","window":"..."}]}`;
  otherwise """

# Removed passages per ablation, as (file name, exact text). A passage may
# belong to several ablations; overlapping passages are removed once.
PASSAGES: dict[str, tuple[tuple[str, str], ...]] = {
    "batching": (
        ("hybrid.md", _PLAN_FROM_ONE_READ),
        ("hybrid.md", _ACT_EXAMPLES),
        ("hybrid.md", "- `no_focused_window`: start the batch with `activate`.\n"),
    ),
    "wait": (
        ("hybrid.md", " or wait again"),
        ("hybrid.md", _WAIT_STEPS),
        ("hybrid.md", _ACT_EXAMPLES),
        ("hybrid.md", _WAIT_TIMEOUT),
        ("hybrid.md", _APP_STARTING_WAIT),
        ("hybrid.md", " or a timed-out wait"),
    ),
    "diff": (
        ("hybrid.md", _ACT_REPORT),
        ("hybrid.md", _SELECT_READBACK),
        ("hybrid.md", _REPORT_NEEDS_NO_WAIT),
    ),
    "guard": (),
}

# The sentence each ablation adds, after this bullet, which no ablation
# removes. guard adds none: the agent cannot notice the shadow mode.
_STATEMENT_ANCHOR = """\
- Once the source, target, and operation are known, act. Further checks must
  answer a concrete blocker.
"""
STATEMENTS = {
    "batching": """\
- Each `act` sends one input step, with optional `wait` steps before or after
  it. Send further input in later calls.
""",
    "wait": """\
- An AX `act` cannot pause for a later state. To check one, read again with
  `observe` or `find`.
""",
    "diff": """\
- An `act` result shows the whole current tree of its window after its
  actions instead of a report of what changed.
""",
}
# With both batching and wait ablated, an act holds one step and no waits.
_ONE_STEP = """\
- Each `act` sends one step. Send further steps in later calls.
"""


def parse_ablation(value: str | list[str] | tuple[str, ...] | None) -> tuple[str, ...]:
    """Return the ablations in canonical order.

    ``value`` is a comma-separated string, as in ``COMPUTER_USE_ABLATE``, or
    a list of names. Unknown and repeated names raise ValueError: a typo must
    not run the complete HCU under an ablation label.
    """
    if value is None:
        return ()
    parts = value.split(",") if isinstance(value, str) else list(value)
    names: list[str] = []
    for part in parts:
        if not isinstance(part, str):
            raise ValueError(f"ablation names must be strings, got {part!r}")
        name = part.strip()
        if not name:
            if isinstance(value, str) and not value.strip():
                continue
            raise ValueError(f"empty ablation name in {value!r}")
        if name not in ABLATION_NAMES:
            raise ValueError(
                f"unknown ablation {name!r}; use {', '.join(ABLATION_NAMES)}"
            )
        if name in names:
            raise ValueError(f"ablation {name!r} is listed twice")
        names.append(name)
    return tuple(name for name in ABLATION_NAMES if name in names)


def ablation_value(ablations: tuple[str, ...]) -> str:
    """The COMPUTER_USE_ABLATE value for parsed ablations; empty for none."""
    return ",".join(ablations)


def ablation_label(ablations: tuple[str, ...]) -> str:
    """A file-name-safe label: ``full`` or ``no-batching-wait``."""
    return "no-" + "-".join(ablations) if ablations else "full"


def ablated_guide(text: str, file_name: str, ablations: tuple[str, ...]) -> str:
    """Return ``text`` without the passages of ``ablations`` for ``file_name``."""
    ranges: list[tuple[int, int]] = []
    for name in ablations:
        for owner, passage in PASSAGES[name]:
            if owner != file_name:
                continue
            start = text.find(passage)
            if start < 0 or text.find(passage, start + 1) >= 0:
                raise ValueError(
                    f"{file_name}: the {name} passage must occur exactly once: "
                    f"{passage[:60]!r}"
                )
            ranges.append((start, start + len(passage)))
    kept, position = [], 0
    for start, end in sorted(ranges):
        if start > position:
            kept.append(text[position:start])
        position = max(position, end)
    kept.append(text[position:])
    text = "".join(kept)
    if file_name != "hybrid.md":
        return text
    added = [STATEMENTS[name] for name in ablations if name in STATEMENTS]
    if "batching" in ablations and "wait" in ablations:
        added[0] = _ONE_STEP
    if not added:
        return text
    if text.count(_STATEMENT_ANCHOR) != 1:
        raise ValueError("hybrid.md: the ablation statements' anchor must occur exactly once")
    return text.replace(_STATEMENT_ANCHOR, _STATEMENT_ANCHOR + "".join(added))


def write_ablated_guides(
    instructions_dir: Path, output_dir: Path, ablations: tuple[str, ...]
) -> dict[str, Path]:
    """Write the ablated base.md and hybrid.md; return their paths by name."""
    output_dir.mkdir(parents=True, exist_ok=True)
    written = {}
    for file_name in ("base.md", "hybrid.md"):
        source = (instructions_dir / file_name).read_text(encoding="utf-8")
        target = output_dir / file_name
        target.write_text(ablated_guide(source, file_name, ablations), encoding="utf-8")
        written[file_name] = target
    return written


def main(argv: list[str]) -> int:
    if len(argv) not in (2, 3):
        print(__doc__, file=sys.stderr)
        return 2
    instructions_dir = (
        Path(argv[2]) if len(argv) == 3
        else Path(__file__).resolve().parents[2] / "instructions"
    )
    try:
        ablations = parse_ablation(argv[0])
    except ValueError as error:
        print(error, file=sys.stderr)
        return 2
    for path in write_ablated_guides(instructions_dir, Path(argv[1]), ablations).values():
        print(path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
