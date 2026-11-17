"""Ablated interaction guides: removed passages and the added statements."""

from __future__ import annotations

import itertools

import pytest

from hcu.osworld.guide_ablation import ABLATION_NAMES, STATEMENTS, ablated_guide
from hcu.paths import DEFAULT_INSTRUCTIONS_FILE

GUIDES = DEFAULT_INSTRUCTIONS_FILE.parent
ALL_SETS = [
    combo
    for count in range(len(ABLATION_NAMES) + 1)
    for combo in itertools.combinations(ABLATION_NAMES, count)
]


def source(file_name: str) -> str:
    return (GUIDES / file_name).read_text(encoding="utf-8")


@pytest.mark.parametrize("file_name", ["base.md", "hybrid.md"])
def test_guides_without_ablation_are_unchanged(file_name):
    assert ablated_guide(source(file_name), file_name, ()) == source(file_name)


def test_guard_changes_no_guide():
    for file_name in ("base.md", "hybrid.md"):
        assert ablated_guide(source(file_name), file_name, ("guard",)) == source(file_name)


@pytest.mark.parametrize("ablations", ALL_SETS, ids=lambda combo: ",".join(combo) or "full")
def test_each_ablation_states_the_changed_behavior_once(ablations):
    guide = ablated_guide(source("hybrid.md"), "hybrid.md", ablations)
    one_step = "batching" in ablations and "wait" in ablations
    for name, statement in STATEMENTS.items():
        expected = 1 if name in ablations and not (name == "batching" and one_step) else 0
        assert guide.count(statement) == expected, name
    assert guide.count("- Each `act` sends one step.") == (1 if one_step else 0)
    # base.md is common policy; no ablation adds to it.
    assert ablated_guide(source("base.md"), "base.md", ablations) == source("base.md")


@pytest.mark.parametrize("ablations", ALL_SETS, ids=lambda combo: ",".join(combo) or "full")
def test_ablations_only_remove_text_besides_the_statements(ablations):
    original = source("hybrid.md")
    guide = ablated_guide(original, "hybrid.md", ablations)
    for statement in [*STATEMENTS.values(), "- Each `act` sends one step. Send further steps in later calls.\n"]:
        guide = guide.replace(statement, "", 1)
    # What remains is the original with characters removed, nothing added.
    remaining = iter(original)
    assert all(character in remaining for character in guide)


@pytest.mark.parametrize("ablations", ALL_SETS, ids=lambda combo: ",".join(combo) or "full")
def test_batch_window_guidance_follows_batching_ablation(ablations):
    guide = " ".join(ablated_guide(source("hybrid.md"), "hybrid.md", ablations).split())
    expected = 0 if "batching" in ablations else 1
    for fragment in (
        "Use `activate` with a `wN` or title to switch the batch target.",
        "Later refs and ref-less steps use that window.",
        "AX rejects refs from another window without explicit `activate`",
        "activate back before reusing the previous window's refs.",
        "Each step reports its window; the final observation uses the last executed window.",
    ):
        assert guide.count(fragment) == expected, fragment


def test_diff_guide_keeps_the_returned_state_and_drops_the_report():
    guide = ablated_guide(source("hybrid.md"), "hybrid.md", ("diff",))
    # An act returns the whole tree, so reading the returned state stays.
    assert "Read the\n  returned state and send only what remains." in guide
    assert "Read the returned state and revise the plan" in guide
    assert "The message says what was seen instead" in guide
    # The report of what changed, and advice that relies on it, goes.
    for removed in ("then a diff", "effect: r58", "windows: opened", "checked=false→true",
                    "listed as `opened`", "reports what is now"):
        assert removed not in guide, removed
    assert "shows the whole current tree of its window" in STATEMENTS["diff"]
