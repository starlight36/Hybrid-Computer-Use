"""Relative scrolling through an ancestor scrollbar, with exact value readback."""

import math

from .foundation import Atspi, OperationFailure, node_role, safe, state_contains
from .ax_actions import WHEEL_CLICKS_PER_PAGE
from .ax_select import _wait_until


def _scrollbar(node, direction):
    orientation = (Atspi.StateType.HORIZONTAL if direction in ("left", "right")
                   else Atspi.StateType.VERTICAL)
    current = node
    for _ in range(16):
        if current is None:
            break
        role = node_role(current)
        if role == "scroll bar":
            candidates = [current]
        elif role in {"scroll pane", "viewport"}:
            count = safe(lambda: int(current.get_child_count()), 0)
            if count > 64:
                return None
            candidates = [safe(lambda: current.get_child_at_index(index))
                          for index in range(count)]
        else:
            candidates = []
        matches = [candidate for candidate in candidates
                   if candidate is not None and node_role(candidate) == "scroll bar"
                   and state_contains(candidate, orientation)]
        if len(matches) == 1:
            return matches[0]
        if len(matches) > 1 or role in {"scroll pane", "viewport", "frame", "dialog", "window", "application"}:
            return None
        current = safe(lambda: current.get_parent())
    return None


def semantic_scroll(element, direction, pages, notes, evidence):
    scrollbar = _scrollbar(element, direction)
    value = safe(lambda: scrollbar.get_value_iface()) if scrollbar is not None else None
    if value is None:
        return False
    try:
        minimum = float(Atspi.Value.get_minimum_value(value))
        maximum = float(Atspi.Value.get_maximum_value(value))
        before = float(Atspi.Value.get_current_value(value))
        increment = float(Atspi.Value.get_minimum_increment(value))
    except Exception:
        return False
    if (not all(math.isfinite(number) for number in (minimum, maximum, before, increment))
            or maximum < minimum or not minimum <= before <= maximum or increment <= 0):
        return False
    sign = -1 if direction in ("up", "left") else 1
    requested = min(maximum, max(minimum, before + sign * float(pages) *
                                WHEEL_CLICKS_PER_PAGE * increment))
    evidence.update(route="semantic_value", readback_sampled=True,
                    readback_verified=False, input_emitted=False)
    if requested == before:
        if before != (minimum if sign < 0 else maximum):
            raise OperationFailure(
                "The requested scroll amount is too small to change the range value. "
                "No input was emitted.", failure_code="invalid_argument",
                failure_phase="precondition", input_events_emitted=False)
        evidence["readback_verified"] = True
        notes.append("[a11y][state] scroll_boundary=true. "
                     "Verified the scrollbar value. No input was emitted.")
        return True
    evidence["input_emitted"] = True
    def readback_matches():
        after = float(Atspi.Value.get_current_value(value))
        return after != before and math.isclose(after, requested, rel_tol=1e-9, abs_tol=1e-9)

    try:
        accepted = bool(Atspi.Value.set_current_value(value, requested))
        verified = accepted and _wait_until(readback_matches, 0.6)
    except Exception:
        verified = False
    if not verified:
        raise OperationFailure(
            "The scrollbar value update was attempted but its requested value could "
            "not be verified. No global input was sent. Observe before retrying.",
            failure_code="effect_unknown", failure_phase="readback",
            input_events_emitted=True, failure_subject="element")
    evidence["readback_verified"] = True
    notes.append("[a11y][semantic] Scrolled through the ancestor scrollbar Value "
                 "interface and verified {} -> {}. Each page approximates {} range "
                 "increments. No focus or pointer input was sent.".format(
                     before, requested, WHEEL_CLICKS_PER_PAGE))
    return True
