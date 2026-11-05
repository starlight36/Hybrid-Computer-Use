import array
import os
import socket
from pathlib import Path
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[3]))
from native.linux.provider import compositor_capture as capture

def main():
    from Xlib import X, Xatom, display

    d = display.Display()
    s = d.screen()
    root = s.root
    w, h = s.width_in_pixels, s.height_in_pixels
    endpoint = os.environ["HCU_CLEAN_CAPTURE_SOCKET"]
    atom = d.intern_atom("_HCU_CLEAN_CAPTURE_SESSION")
    session = bytes(root.get_full_property(atom, Xatom.STRING).value)
    assert len(session) == 16

    def grab(x, y, width, height):
        return capture.grab_rgb(endpoint, x, y, width, height, session)

    def assert_rejected(token, deadline):
        with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as client:
            client.settimeout(1)
            client.bind("")
            client.connect(endpoint)
            client.sendall(capture._REQUEST.pack(capture._VERSION, 0, 0, w, h, token, deadline))
            header, ancillary, _, _ = client.recvmsg(capture._REPLY.size, socket.CMSG_SPACE(4))
            assert not ancillary and capture._REPLY.unpack(header)[3:5] == (0, 0)

    assert_rejected(bytes([session[0] ^ 255]) + session[1:], time.monotonic_ns() + 500_000_000)
    assert_rejected(session, time.monotonic_ns() - 1)
    marker = d.intern_atom("_HCU_CURSOR_OVERLAY")
    argb = next(v.visual_id for depth in s.allowed_depths if depth.depth == 32 for v in depth.visuals)
    colormap = root.create_colormap(argb, X.AllocNone)
    def layer(x, y, width, height, color, alpha=False, excluded=False):
        args = dict(background_pixel=color, border_pixel=0, override_redirect=1)
        if alpha:
            args["colormap"] = colormap
        win = root.create_window(x, y, width, height, 0, 32 if alpha else s.root_depth,
                                 X.InputOutput, argb if alpha else X.CopyFromParent, **args)
        if excluded:
            win.change_property(marker, Xatom.CARDINAL, 32, [1])
        win.map()
        d.sync()
        return win
    def rgb(raw):
        result = bytearray(len(raw)//4*3)
        for idx, channel in enumerate((2,1,0)):
            result[idx::3] = raw[channel::4]
        return bytes(result)
    def onscreen(x=0, y=0, width=w, height=h):
        return rgb(root.get_image(x,y,width,height,X.ZPixmap,0xffffffff).data)
    def wait_until(predicate):
        end = time.monotonic()+3
        while not predicate():
            if time.monotonic() > end:
                raise AssertionError("compositor did not present expected frame")
            time.sleep(.01)
    def at(x,y):
        return onscreen(x,y,1,1)
    def color(value):
        return value.to_bytes(3,"big")

    layer(0,0,w,h,0x8c3b45)
    layer(0,h-30,w,30,0x466c9c)
    dialog = layer(100,100,200,140,0x3b9458)
    layer(110,108,60,25,0x80400000,alpha=True)
    wait_until(lambda: at(180,145)==color(0x3b9458) and at(120,115)!=color(0x3b9458) and
               at(5,h-10)==color(0x466c9c))
    baseline = onscreen()
    assert grab(0, 0, w, h) == baseline
    clients = []
    buffers = []
    try:
        deadline = time.monotonic_ns() + 600_000_000
        for x, y, width, height in ((0, 0, w, h), (160, 145, 2, 2), (5, 6, 15, 16)):
            client = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
            clients.append(client)
            client.settimeout(1)
            client.bind("")
            client.connect(endpoint)
            client.sendall(capture._REQUEST.pack(capture._VERSION, x, y, width, height, session, deadline))
        for client in clients:
            header, ancillary, _, _ = client.recvmsg(
                capture._REPLY.size, socket.CMSG_SPACE(4), socket.MSG_CMSG_CLOEXEC)
            assert capture._REPLY.unpack(header) == (capture._VERSION, 0, 0, w, h, session)
            descriptor = array.array("i")
            descriptor.frombytes(ancillary[0][2])
            buffers.extend(descriptor)
        assert len(buffers) == 3 and len({os.fstat(fd).st_ino for fd in buffers}) == 1
        assert capture._rgb_crop(buffers[1], (0, 0, w, h), (160, 145, 2, 2)) == color(0x3b9458) * 4
    finally:
        for fd in buffers:
            os.close(fd)
        for client in clients:
            client.close()
    print("concurrent crops shared a sealed frame; wrong session and expired requests were refused")
    cue = layer(150,130,28,32,0x5ec0e8,excluded=True)
    wait_until(lambda: at(160,145)==color(0x5ec0e8))
    annotated = onscreen()
    start = time.monotonic()
    for _ in range(24):
        assert grab(0, 0, w, h) == baseline
        assert onscreen() == annotated
    print("24 clean frames while the visible overlay stayed unchanged, mean elapsed %.1f ms/frame" %
          ((time.monotonic()-start)*1000/24))
    dialog.change_attributes(background_pixel=0x703a99)
    dialog.clear_area()
    d.sync()
    wait_until(lambda: at(190,145)==color(0x703a99))
    assert grab(160, 145, 1, 1)==color(0x703a99)
    assert at(160,145)==color(0x5ec0e8)
    cue.destroy()
    d.sync()
    assert grab(160, 145, 1, 1)==color(0x703a99)
    print("covered live updates and capture during cue destruction are clean")


if __name__ == "__main__":
    main()
