"""Keyboard value writes require readback from the requested control."""

from runtime_test_support import *  # noqa: F401,F403


class SetValueReadbackTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        self.clock = 0.0
        self.control = FakeTextIface(text="pending", honest=False)
        self.editor = FakeTextIface(text="")
        self.target = FakeNode(text=self.control)
        self.evidence = runtime._new_action_evidence()
        self.notes = []
        self.key = mock.Mock()
        self.send = mock.Mock(side_effect=self.type_into_editor)
        for name, replacement in (
            ("focus_target_element", mock.Mock()),
            ("send_key", self.key),
            ("send_text", self.send),
            ("time", mock.Mock(monotonic=lambda: self.clock,
                               sleep=self.advance_clock)),
        ):
            patcher = mock.patch.object(runtime, name, replacement)
            patcher.start()
            self.addCleanup(patcher.stop)

    def advance_clock(self, duration):
        self.clock += duration

    def type_into_editor(self, value):
        self.editor.text = value

    def write(self, value="matched"):
        runtime._perform_set_value(
            {"value": value}, None, self.target, {}, self.notes, self.evidence)

    def assert_unknown(self, value="matched"):
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.write(value)
        failure = caught.exception
        self.assertEqual(failure.failure_code, "effect_unknown")
        self.assertEqual(failure.failure_phase, "readback")
        self.assertTrue(failure.input_events_emitted)
        self.assertIn("typed", str(failure))
        self.assertIn("Do not retype", str(failure))
        self.assertEqual(self.evidence["route"], "keyboard_synthesis")
        self.assertTrue(self.evidence["input_emitted"])
        self.assertTrue(self.evidence["readback_sampled"])
        self.assertIsNot(self.evidence["readback_verified"], True)
        self.key.assert_called_once_with("ctrl+a")
        self.send.assert_called_once_with(value)

    def test_editor_text_does_not_confirm_the_control_value(self):
        self.assert_unknown()
        self.assertEqual(self.editor.text, "matched")
        self.assertEqual(self.control.text, "pending")
        self.assertFalse(self.evidence["readback_verified"])

    def test_unreadable_control_does_not_confirm_even_an_empty_value(self):
        self.target = FakeNode()
        for value in ("matched", ""):
            with self.subTest(value=value):
                self.key.reset_mock()
                self.send.reset_mock()
                self.assert_unknown(value)
                self.assertIsNone(self.evidence["readback_verified"])

    def test_different_control_value_is_not_verified_or_retried(self):
        def normalize(value):
            self.control.text = value.upper()
        self.send.side_effect = normalize
        self.assert_unknown()
        self.assertEqual(self.control.text, "MATCHED")

    def test_keyboard_write_confirms_exact_target_text_without_enter(self):
        for value in ("matched", "", "中文🙂", " matched "):
            with self.subTest(value=value):
                self.key.reset_mock()
                self.send.reset_mock()
                self.control.text = "pending"
                self.send.side_effect = lambda payload: setattr(self.control, "text", payload)
                self.write(value)
                self.assertEqual(self.control.text, value)
                self.assertTrue(self.evidence["readback_verified"])
                self.assertTrue(self.evidence["readback_sampled"])
                self.key.assert_called_once_with("ctrl+a")
                self.send.assert_called_once_with(value)

    def test_waits_for_delayed_control_update_without_retyping(self):
        def commit_after_delay(duration):
            self.advance_clock(duration)
            if self.clock >= 0.1:
                self.control.text = self.editor.text
        with mock.patch.object(runtime.time, "sleep", side_effect=commit_after_delay):
            self.write()
        self.assertTrue(self.evidence["readback_verified"])
        self.assertGreaterEqual(self.clock, 0.1)
        self.send.assert_called_once_with("matched")

    def test_verified_semantic_write_needs_no_keyboard_or_commit(self):
        self.control.honest = True
        self.write()
        self.assertEqual(self.control.text, "matched")
        self.assertEqual(self.evidence["route"], "value_api")
        self.assertTrue(self.evidence["readback_verified"])
        self.key.assert_not_called()
        self.send.assert_not_called()
