"""Shadow mode of the input guards for the guard ablation.

With ``guard`` in COMPUTER_USE_ABLATE, the AX target guard and the GUI
layout guard still make their decision, but a decision to stop input is
only recorded: one JSON line per decision in COMPUTER_USE_GUARD_LOG, which
the model never sees. The input then goes ahead. Checks that find nothing
to act on (no node on the snapshot path, no captured frame) are not guards
and still fail.
"""

import contextlib
import json
import os
import time

_enforced = 0


@contextlib.contextmanager
def guards_enforced():
    """Keep guards deciding normally, for checks that only choose a route."""
    global _enforced
    _enforced += 1
    try:
        yield
    finally:
        _enforced -= 1


def guard_ablated():
    names = os.environ.get("COMPUTER_USE_ABLATE", "").split(",")
    return "guard" in {name.strip() for name in names}


def guards_shadowed():
    """Report whether a stopped input would be logged and let through."""
    return not _enforced and guard_ablated()


def shadow_guard(channel, guard, reason, **detail):
    """Return True when the guard ablation lets the stopped input through.

    The decision is appended to the guard log first. Without the ablation
    nothing is written and the caller stops the input as before.
    """
    if not guards_shadowed():
        return False
    record = {
        "at": time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime())
        + ".%03dZ" % (int(time.time() * 1000) % 1000),
        "channel": channel,
        "guard": guard,
        "reason": reason,
        "would_block": True,
    }
    record.update(detail)
    path = os.environ.get("COMPUTER_USE_GUARD_LOG", "").strip()
    if path:
        # A lost log line must not fail the input it describes.
        try:
            directory = os.path.dirname(path)
            if directory:
                os.makedirs(directory, exist_ok=True)
            with open(path, "a", encoding="utf-8") as log:
                log.write(json.dumps(record, sort_keys=True, default=str) + "\n")
        except OSError:
            pass
    return True


def shadow_layout_guard(reason, frame_id, actions=(), region=None):
    """shadow_guard for the GUI layout guard; logs each action's point."""
    points = []
    for action in actions:
        point = {"type": action.get("type")}
        point.update({key: action[key] for key in ("x", "y", "path") if key in action})
        points.append(point)
    detail = {"frame_id": frame_id, "actions": points}
    if region is not None:
        detail["region"] = region
    return shadow_guard("gui", "zoom_layout" if region is not None else "layout", reason, **detail)
