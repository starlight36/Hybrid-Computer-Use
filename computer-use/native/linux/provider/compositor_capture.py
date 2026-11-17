"""Read an RGB crop from the configured local XRender compositor."""

import array
import fcntl
import mmap
import os
import socket
import stat
import struct
import time

_VERSION = 2
_MAX_PIXELS = 16 * 1024 * 1024
_REQUEST = struct.Struct("<IIIII16sQ")
_REPLY = struct.Struct("<IIIII16s")
_FD_SIZE = array.array("i").itemsize
_REQUIRED_SEALS = fcntl.F_SEAL_SHRINK | fcntl.F_SEAL_GROW | fcntl.F_SEAL_WRITE
_CAPTURE_TIMEOUT = .75


def _rgb_crop(fd, frame, crop):
    left, top, stride, rows = frame
    x, y, width, height = crop
    size = stride * rows * 4
    info = os.fstat(fd)
    if (not stat.S_ISREG(info.st_mode) or info.st_size != size or
            fcntl.fcntl(fd, fcntl.F_GET_SEALS) & _REQUIRED_SEALS != _REQUIRED_SEALS):
        raise RuntimeError("invalid compositor capture buffer")
    rgb = bytearray(width * height * 3)
    # Some sandbox runtimes reject even read-only shared mappings of a sealed memfd.
    with mmap.mmap(fd, size, flags=mmap.MAP_PRIVATE, prot=mmap.PROT_READ) as data:
        offset = ((y - top) * stride + x - left) * 4
        if width == stride:
            raw = data[offset:offset + width * height * 4]
            rgb[0::3], rgb[1::3], rgb[2::3] = raw[2::4], raw[1::4], raw[0::4]
        else:
            for row in range(height):
                start = offset + row * stride * 4
                raw = data[start:start + width * 4]
                output = row * width * 3
                end = output + width * 3
                rgb[output:end:3], rgb[output+1:end:3], rgb[output+2:end:3] = (
                    raw[2::4], raw[1::4], raw[0::4])
    return bytes(rgb)


def grab_rgb(path, x, y, width, height, session):
    """Read the requested region of a validated, possibly shared frame."""
    if not (0 <= x <= 32767 and 0 <= y <= 32767 and
            1 <= width <= 32767 and 1 <= height <= 32767 and
            width * height <= _MAX_PIXELS):
        raise ValueError("invalid compositor capture crop")
    if not os.path.isabs(path) or len(session) != 16:
        raise ValueError("invalid compositor capture endpoint")

    deadline = time.monotonic_ns() + int(_CAPTURE_TIMEOUT * 1e9)
    descriptors = array.array("i")
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as client:
            client.settimeout(_CAPTURE_TIMEOUT)
            client.bind("")  # Linux abstract address, scoped to this request.
            client.connect(path)
            client.sendall(_REQUEST.pack(_VERSION, x, y, width, height, session, deadline))
            remaining = (deadline - time.monotonic_ns()) / 1e9
            if remaining <= 0:
                raise TimeoutError("compositor capture request expired")
            client.settimeout(remaining)
            header, ancillary, flags, _ = client.recvmsg(
                _REPLY.size, socket.CMSG_SPACE(_FD_SIZE), socket.MSG_CMSG_CLOEXEC)
        for level, kind, data in ancillary:
            if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
                descriptors.frombytes(data[:len(data) - len(data) % _FD_SIZE])
        if (flags & (socket.MSG_TRUNC | socket.MSG_CTRUNC) or
                len(header) != _REPLY.size or len(descriptors) != 1):
            raise RuntimeError("invalid compositor capture response")
        version, left, top, stride, rows, peer_session = _REPLY.unpack(header)
        if (version != _VERSION or peer_session != session or
                not (1 <= stride <= 32767 and 1 <= rows <= 32767) or
                stride * rows > _MAX_PIXELS or
                not (left <= x and top <= y and x + width <= left + stride and y + height <= top + rows)):
            raise RuntimeError("invalid compositor capture frame")
        return _rgb_crop(descriptors[0], (left, top, stride, rows), (x, y, width, height))
    finally:
        for fd in descriptors:
            os.close(fd)
