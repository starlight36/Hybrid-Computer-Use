"""The compositor route validates frames and never hides the visible cue."""

import array
import fcntl
import socket
import time
import tempfile
import threading

from runtime_test_support import *  # noqa: F401,F403


class CompositorCaptureTests(unittest.TestCase):
    def test_sealed_rgb_frame_matches_wire_fixture(self):
        path = Path(__file__).parents[1] / "testdata/provider-wire/compositor-capture.json"
        fixture = json.loads(path.read_text())
        pixels = bytes.fromhex(fixture["bgrx_hex"])
        session = bytes.fromhex(fixture["session_hex"])
        with tempfile.TemporaryDirectory() as directory, socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as server:
            endpoint = os.path.join(directory, "capture.sock")
            server.bind(endpoint)
            server.settimeout(2)
            errors = []

            def reply():
                try:
                    request, peer = server.recvfrom(64)
                    self.assertEqual(request[:-8], bytes.fromhex(fixture["request_prefix_le_hex"]))
                    self.assertGreater(int.from_bytes(request[-8:], "little"), time.monotonic_ns())
                    fd = os.memfd_create("test-clean-frame", os.MFD_CLOEXEC | os.MFD_ALLOW_SEALING)
                    try:
                        os.write(fd, pixels)
                        fcntl.fcntl(fd, fcntl.F_ADD_SEALS, compositor_capture._REQUIRED_SEALS)
                        server.sendmsg([bytes.fromhex(fixture["reply_le_hex"])],
                                       [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [fd]))],
                                       0, peer)
                    finally:
                        os.close(fd)
                except BaseException as exc:
                    errors.append(exc)

            thread = threading.Thread(target=reply)
            thread.start()
            try:
                result = compositor_capture.grab_rgb(endpoint, *fixture["crop"], session)
            finally:
                thread.join(3)
            self.assertFalse(thread.is_alive())
            self.assertFalse(errors)
            self.assertEqual(result, bytes.fromhex(fixture["rgb_hex"]))

    def test_configured_endpoint_drives_both_capture_paths(self):
        bounds = {"x": 4, "y": 7, "width": 2, "height": 2}
        pixels = bytes([94, 192, 232] * 4)
        session = bytes(range(16))
        with mock.patch.dict(os.environ, {"HCU_CLEAN_CAPTURE_SOCKET": "/tmp/clean.sock"}), mock.patch.object(
            gui_capture, "Gdk"
        ), mock.patch.object(gui_capture, "compositor_session", return_value=session), mock.patch.object(
            gui_capture, "grab_rgb", return_value=pixels
        ) as grab, mock.patch.object(gui_capture, "clean_capture") as hide:
            frame = gui_capture.capture_gui_pixels(bounds)
            sample = gui_capture.capture_window_pixels(bounds)
            self.assertEqual(frame.read_pixel_bytes().get_data(), pixels)
            self.assertEqual(sample["data"], pixels)
            self.assertEqual(grab.call_args_list, [mock.call("/tmp/clean.sock", 4, 7, 2, 2, session)] * 2)
            hide.assert_not_called()

    def test_failed_compositor_capture_does_not_hide_or_read_root(self):
        bounds = {"x": 0, "y": 0, "width": 2, "height": 2}
        with mock.patch.dict(os.environ, {"HCU_CLEAN_CAPTURE_SOCKET": "/tmp/clean.sock"}), mock.patch.object(
            gui_capture, "grab_rgb", side_effect=TimeoutError
        ), mock.patch.object(gui_capture, "compositor_session", return_value=bytes(16)), mock.patch.object(gui_capture, "clean_capture") as hide, mock.patch.object(gui_capture, "Gdk") as gdk:
            self.assertIsNone(gui_capture.capture_gui_pixels(bounds))
            self.assertIsNone(gui_capture.capture_window_pixels(bounds))
            hide.assert_not_called()
            gdk.pixbuf_get_from_window.assert_not_called()
