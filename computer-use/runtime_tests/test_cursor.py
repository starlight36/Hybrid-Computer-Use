"""Cursor cues never change input evidence, and dirty captures are refused."""

from runtime_test_support import *  # noqa: F401,F403
import socket
import threading


class CursorChannelTests(unittest.TestCase):
    def pair(self):
        client, server = socket.socketpair()
        self.addCleanup(client.close)
        self.addCleanup(server.close)
        server.settimeout(2)
        return cursor.CursorChannel(client), server

    def test_shared_pose_fixtures(self):
        fixture = json.loads((Path(__file__).parents[1] / "testdata/provider-wire/cursor-events.json").read_text())
        channel, server = self.pair()
        for event in fixture["events"][:6]:
            channel.cue(event["kind"], event["x"], event["y"], event.get("rect"))
            self.assertEqual(json.loads(server.recv(4096)), event)

    def test_capture_restores_after_capture_exception(self):
        channel, server = self.pair()
        seen = []
        errors = []

        def acknowledge():
            try:
                stream = server.makefile("rb")
                with stream:
                    for _ in range(2):
                        event = json.loads(stream.readline())
                        seen.append(event)
                        server.sendall((json.dumps({"schema_version": 1, "sequence": event["sequence"], "ok": True})+"\n").encode())
            except BaseException as exc:
                errors.append(exc)

        thread = threading.Thread(target=acknowledge)
        thread.start()
        with self.assertRaisesRegex(ValueError, "capture failed"):
            with channel.capture():
                raise ValueError("capture failed")
        thread.join(3)
        self.assertFalse(thread.is_alive())
        self.assertFalse(errors)
        self.assertEqual([e["kind"] for e in seen], ["capture_begin", "capture_end"])
        self.assertEqual([e["sequence"] for e in seen], [1, 1])

    def test_wrong_acknowledgement_prevents_capture(self):
        channel, server = self.pair()
        server.sendall(b'{"schema_version":1,"sequence":99,"ok":true}\n')
        with self.assertRaisesRegex(RuntimeError, "capture exclusion unavailable"):
            with channel.capture():
                self.fail("captured without an exclusion acknowledgement")
        self.assertTrue(channel.failed)

    def test_capture_identity_does_not_take_a_capture_lease(self):
        fixture = json.loads((Path(__file__).parents[1] / "testdata/provider-wire/cursor-events.json").read_text())
        channel, server = self.pair()
        channel.sequence = 1
        acknowledgement = fixture["identity_acknowledgement"]
        server.sendall((json.dumps(acknowledgement) + "\n").encode())
        self.assertEqual(channel.capture_identity(), bytes.fromhex(acknowledgement["session"]))
        self.assertEqual(json.loads(server.recv(4096)), fixture["events"][-1])
        self.assertFalse(channel.failed)

    def test_closed_channel_prevents_later_capture(self):
        channel, server = self.pair()
        server.close()
        channel.cue("move", 20, 30)
        with self.assertRaises(RuntimeError):
            with channel.capture():
                self.fail("captured after renderer loss")

    def test_failed_capture_requests_worker_reset_without_losing_input_history(self):
        fixture = json.loads((Path(__file__).parents[1] / "testdata/provider-wire/cursor-events.json").read_text())
        failed, server = self.pair()
        failed.failed = True
        request = {"operation": "desktop_execute_batch", "request_id": "cursor-reset", "desktop": {}}
        for emitted in (False, True):
            with self.subTest(emitted=emitted), mock.patch.object(cursor, "_channel", failed), mock.patch.object(
                wire, "perform_provider_request", return_value={"ok": False, "input_events_emitted": emitted}
            ):
                result = wire._execute(request)
                self.assertEqual(result["provider_reset_required"], fixture["failed_capture_requires_provider_reset"])
                if emitted:
                    self.assertTrue(result["desktop"]["mutation"]["input_events_emitted"])

    def test_fresh_worker_has_no_inherited_failure(self):
        with mock.patch.object(cursor, "_channel", None):
            self.assertFalse(cursor.reset_required())

    def test_ax_geometry_is_window_relative_and_failure_is_a_cue(self):
        sink = mock.Mock()
        record = {"frame": {"x": 5, "y": 10, "width": 30, "height": 20}, "value": "private text"}
        with mock.patch.object(cursor, "channel", return_value=sink):
            with self.assertRaisesRegex(ValueError, "action failed"):
                with cursor.action(record, {"x": 100, "y": 200}):
                    raise ValueError("action failed")
        self.assertEqual(sink.cue.call_args_list, [
            mock.call("target", 120, 220, [105, 210, 30, 20]),
            mock.call("error", 120, 220, [105, 210, 30, 20]),
        ])

    def test_rejected_mouse_event_has_no_click_cue(self):
        with mock.patch.object(input_primitives.Atspi, "generate_mouse_event", return_value=False), mock.patch.object(cursor, "pointer") as cue:
            with self.assertRaises(input_primitives.InputEmissionError):
                input_primitives.emit_mouse_event(12, 34, "b1p")
            cue.assert_not_called()

    def test_wheel_click_does_not_leave_button_held(self):
        sink = mock.Mock()
        with mock.patch.object(cursor, "channel", return_value=sink):
            cursor.pointer(12, 34, "b4c")
        self.assertEqual(sink.cue.call_args_list, [
            mock.call("press", 12, 34), mock.call("release", 12, 34)])

    def test_capture_does_not_read_pixels_when_exclusion_fails(self):
        failed = mock.Mock()
        failed.capture.side_effect = RuntimeError("capture exclusion unavailable")
        fake_screen = mock.Mock()
        with mock.patch.object(cursor, "channel", return_value=failed), mock.patch.object(gui_capture, "Gdk") as gdk:
            gdk.Screen.get_default.return_value = fake_screen
            bounds = {"x": 0, "y": 0, "width": 100, "height": 100}
            self.assertIsNone(gui_capture.capture_gui_pixels(bounds))
            self.assertIsNone(gui_capture.capture_window_pixels(bounds))
            gdk.pixbuf_get_from_window.assert_not_called()
