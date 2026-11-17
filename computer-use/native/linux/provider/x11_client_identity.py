"""Resolve missing window PIDs through the X-Resource extension.

The query runs in a bounded child process: Xlib's default error handler can
exit on a disappearing client, and an unresponsive server can block a read.
No title, WM_CLASS or process-name matching establishes ownership.
"""

import ctypes
import json
import subprocess
import sys


class _ClientIdSpec(ctypes.Structure):
    _fields_ = [("client", ctypes.c_ulong), ("mask", ctypes.c_uint)]


class _ClientIdValue(ctypes.Structure):
    _fields_ = [("spec", _ClientIdSpec), ("length", ctypes.c_long),
                ("value", ctypes.c_void_p)]


def _xres_libraries():
    xlib = ctypes.CDLL("libX11.so.6")
    xres = ctypes.CDLL("libXRes.so.1")
    xlib.XOpenDisplay.argtypes = [ctypes.c_char_p]
    xlib.XOpenDisplay.restype = ctypes.c_void_p
    xlib.XCloseDisplay.argtypes = [ctypes.c_void_p]
    xlib.XCloseDisplay.restype = ctypes.c_int
    xres.XResQueryVersion.argtypes = [
        ctypes.c_void_p, ctypes.POINTER(ctypes.c_int), ctypes.POINTER(ctypes.c_int)]
    xres.XResQueryVersion.restype = ctypes.c_int
    xres.XResQueryClientIds.argtypes = [
        ctypes.c_void_p, ctypes.c_long, ctypes.POINTER(_ClientIdSpec),
        ctypes.POINTER(ctypes.c_long), ctypes.POINTER(ctypes.POINTER(_ClientIdValue))]
    xres.XResQueryClientIds.restype = ctypes.c_int
    xres.XResGetClientPid.argtypes = [ctypes.POINTER(_ClientIdValue)]
    xres.XResGetClientPid.restype = ctypes.c_int
    xres.XResClientIdsDestroy.argtypes = [
        ctypes.c_long, ctypes.POINTER(_ClientIdValue)]
    xres.XResClientIdsDestroy.restype = None
    return xlib, xres


def _query_client_pids(window_ids):
    xlib, xres = _xres_libraries()
    display = xlib.XOpenDisplay(None)
    if not display:
        return {}
    try:
        major, minor = ctypes.c_int(), ctypes.c_int()
        if not xres.XResQueryVersion(display, ctypes.byref(major), ctypes.byref(minor)):
            return {}
        if (major.value, minor.value) < (1, 2):
            return {}
        owners = {}
        for window_id in window_ids:
            xid = int(window_id, 16)
            if not 0 < xid <= 0xffffffff:
                continue
            spec = _ClientIdSpec(xid, 2)
            count = ctypes.c_long()
            values = ctypes.POINTER(_ClientIdValue)()
            status = xres.XResQueryClientIds(
                display, 1, ctypes.byref(spec), ctypes.byref(count), ctypes.byref(values))
            if status != 0:
                continue
            try:
                pids = {xres.XResGetClientPid(ctypes.byref(values[index]))
                        for index in range(count.value)}
                pids.discard(-1)
                if len(pids) == 1 and next(iter(pids)) > 0:
                    owners[window_id] = pids.pop()
            finally:
                xres.XResClientIdsDestroy(count, values)
        return owners
    finally:
        xlib.XCloseDisplay(display)


def x11_client_pids(window_ids):
    """Return proven local client PIDs; missing entries remain unidentified."""
    if not window_ids:
        return {}
    try:
        result = subprocess.run(
            [sys.executable, "-I", __file__, *window_ids],
            capture_output=True, text=True, timeout=3,
        )
        if result.returncode != 0:
            return {}
        owners = json.loads(result.stdout)
        if not isinstance(owners, dict):
            return {}
        return {window_id: owners[window_id] for window_id in window_ids
                if type(owners.get(window_id)) is int and owners[window_id] > 0}
    except (OSError, subprocess.TimeoutExpired, ValueError):
        return {}


if __name__ == "__main__":
    print(json.dumps(_query_client_pids(sys.argv[1:])))
