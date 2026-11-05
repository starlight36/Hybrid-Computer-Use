"""Private, opt-in desktop cursor cues and acknowledged capture exclusion.

The Go host owns rendering. Pose events are best effort; capture barriers
must succeed before any pixels are read. Neither path carries user text.
"""

from contextlib import contextmanager
import json
import math
import os
import select
import socket
import time


class CursorChannel:
    def __init__(self, connection):
        self.connection = connection
        self.connection.setblocking(False)
        self.sequence = 0
        self.pending = b""
        self.failed = False

    def cue(self, kind, x, y, rect=None):
        if self.failed:
            return
        event = {"schema_version": 1, "kind": kind, "x": x, "y": y}
        if rect is not None:
            event["rect"] = rect
        raw = json.dumps(event, separators=(",", ":"), allow_nan=False).encode() + b"\n"
        try:
            count = self.connection.send(raw)
            if count != len(raw):
                self.failed = True
                self.connection.close()
        except BlockingIOError:
            # No bytes were sent; dropping an intermediate pose is harmless.
            pass
        except OSError:
            self.failed = True

    def barrier(self, kind, sequence):
        if self.failed:
            raise RuntimeError("cursor capture exclusion unavailable")
        event = {"schema_version": 1, "kind": kind, "sequence": sequence}
        remaining = json.dumps(event, separators=(",", ":")).encode() + b"\n"
        deadline = time.monotonic() + 4.0
        try:
            while remaining:
                _, ready, _ = select.select([], [self.connection], [], max(0, deadline-time.monotonic()))
                if not ready:
                    raise TimeoutError()
                count = self.connection.send(remaining)
                if not count:
                    raise OSError("closed cursor channel")
                remaining = remaining[count:]
            while b"\n" not in self.pending:
                ready, _, _ = select.select([self.connection], [], [], max(0, deadline-time.monotonic()))
                if not ready:
                    raise TimeoutError()
                chunk = self.connection.recv(4096)
                if not chunk:
                    raise OSError("closed cursor channel")
                self.pending += chunk
                if len(self.pending) > 4096:
                    raise ValueError("oversized cursor acknowledgement")
            raw, self.pending = self.pending.split(b"\n", 1)
            ack = json.loads(raw)
            session = None
            if kind == "capture_identity" and isinstance(ack, dict) and "session" in ack:
                value = ack.pop("session")
                if not isinstance(value, str) or len(value) != 32:
                    raise ValueError("invalid compositor session")
                session = bytes.fromhex(value)
                if len(session) != 16:
                    raise ValueError("invalid compositor session")
            if ack != {"schema_version": 1, "sequence": sequence, "ok": True}:
                raise ValueError("invalid cursor acknowledgement")
            return session
        except (OSError, ValueError, TypeError):
            self.failed = True
            self.connection.close()
            raise RuntimeError("cursor capture exclusion unavailable") from None

    def capture_identity(self):
        self.sequence += 1
        return self.barrier("capture_identity", self.sequence)

    @contextmanager
    def capture(self):
        self.sequence += 1
        sequence = self.sequence
        self.barrier("capture_begin", sequence)
        try:
            yield
        finally:
            self.barrier("capture_end", sequence)


_channel = None
_initialized = False


def reset_required():
    """A broken exclusion channel invalidates this worker, never future workers."""
    return _channel is not None and _channel.failed


def channel():
    global _channel, _initialized
    if not _initialized:
        _initialized = True
        descriptor = os.environ.pop("COMPUTER_USE_CURSOR_FD", "")
        if descriptor:
            os.set_inheritable(int(descriptor), False)
            _channel = CursorChannel(socket.socket(fileno=int(descriptor)))
    return _channel


def compositor_session():
    current = channel()
    session = current.capture_identity() if current is not None else None
    if session is None:
        raise RuntimeError("compositor capture session unavailable")
    return session


@contextmanager
def clean_capture():
    current = channel()
    if current is None:
        yield
    else:
        with current.capture():
            yield


def pointer(x, y, event):
    current = channel()
    if current is None:
        return
    kind = "move"
    if event.startswith("b"):
        kind = "press" if event.endswith(("p", "c")) else "release"
    current.cue(kind, float(x), float(y))
    if event.startswith("b") and event.endswith("c"):
        current.cue("release", float(x), float(y))


def target_rectangle(record, window):
    bounds = (record or {}).get("frame")
    if not isinstance(bounds, dict) or not isinstance(window, dict):
        return None
    rect = [bounds.get(key) for key in ("x", "y", "width", "height")]
    if not all(isinstance(n, (int, float)) and not isinstance(n, bool)
               and math.isfinite(n) and abs(n) <= 1 << 24 for n in rect):
        return None
    x, y, width, height = rect
    if width <= 0 or height <= 0:
        return None
    # AX frames are window-relative; cursor poses use native X root pixels.
    wx, wy = window.get("x"), window.get("y")
    if not all(isinstance(n, (int, float)) and not isinstance(n, bool)
               and math.isfinite(n) for n in (wx, wy)):
        return None
    return [x+wx, y+wy, width, height]


@contextmanager
def action(record, window):
    current = channel()
    rect = target_rectangle(record, window) if current is not None else None
    if rect is None:
        yield
        return
    x, y, width, height = rect
    current.cue("target", x+width/2, y+height/2, rect)
    kind = "end"
    try:
        yield
    except BaseException:
        kind = "error"
        raise
    finally:
        current.cue(kind, x+width/2, y+height/2, rect)
