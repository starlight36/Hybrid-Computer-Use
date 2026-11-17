#!/usr/bin/env python3
"""Linux provider tests for the guard ablation's shadow mode."""

import json
import os
import tempfile

from runtime_test_support import *  # noqa: F401,F403
from runtime_tests import test_gui_capture


class GuardLogMixin:
    """Run with or without the guard ablation and read the guard log."""

    def setUp(self):
        super().setUp()
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.log_path = os.path.join(directory.name, "guard.jsonl")
        patcher = mock.patch.dict(os.environ, {
            "COMPUTER_USE_ABLATE": "", "COMPUTER_USE_GUARD_LOG": self.log_path,
        })
        patcher.start()
        self.addCleanup(patcher.stop)

    def ablate_guard(self):
        os.environ["COMPUTER_USE_ABLATE"] = "wait,guard"

    def guard_log(self):
        if not os.path.exists(self.log_path):
            return []
        with open(self.log_path, encoding="utf-8") as log:
            return [json.loads(line) for line in log]


class AXGuardShadowTests(GuardLogMixin, unittest.TestCase):
    record = {
        "runtimeId": [0, 2], "controlType": "push button", "name": "Rename…",
    }

    def resolve_mismatch(self):
        node = object()
        with mock.patch.object(element_resolver, "resolve_path", return_value=node), \
                mock.patch.object(element_resolver, "record_still_matches", return_value=False), \
                mock.patch.object(element_resolver, "effective_role", return_value="toggle button"), \
                mock.patch.object(element_resolver, "effective_name", return_value="Bold"):
            return node, element_resolver.find_element_at_snapshot_path_with_bounds(
                object(), dict(self.record), None)

    def test_identity_mismatch_stops_by_default(self):
        _node, resolved = self.resolve_mismatch()
        self.assertIsNone(resolved)
        self.assertEqual(self.guard_log(), [])

    def test_identity_mismatch_acts_on_the_path_node_and_logs_it(self):
        self.ablate_guard()
        node, resolved = self.resolve_mismatch()
        self.assertIs(resolved, node)
        [entry] = self.guard_log()
        self.assertEqual(entry["channel"], "ax")
        self.assertEqual(entry["guard"], "target_identity")
        self.assertEqual(entry["path"], [0, 2])
        self.assertEqual(entry["expected"]["name"], "Rename…")
        self.assertEqual(entry["live"], {"role": "toggle button", "name": "Bold"})
        self.assertTrue(entry["would_block"])

    def test_a_path_without_a_node_stays_stale(self):
        self.ablate_guard()
        with mock.patch.object(element_resolver, "resolve_path", return_value=None):
            resolved = element_resolver.find_element_at_snapshot_path_with_bounds(
                object(), dict(self.record), None)
        self.assertIsNone(resolved)
        self.assertEqual(self.guard_log(), [])

    def semantic_refusal(self, code, node, path=(0, 4), role="push button", app=None,
                         combo=None, edit_identity=None):
        refused = runtime.OperationFailure(
            code + " selector='button:Save'; no input was emitted.",
            failure_code=code, failure_phase="precondition", input_events_emitted=False)
        target = {"selector": "button:Save", "runtimeId": list(path)}
        with mock.patch.object(element_resolver, "resolve_path", return_value=node) as resolve, \
                mock.patch.object(element_resolver, "state_value", return_value=None), \
                mock.patch.object(element_resolver, "node_role", return_value=role), \
                mock.patch.object(element_resolver, "node_name", return_value="Cancel"), \
                mock.patch.object(element_resolver, "relative_frame", return_value=None), \
                mock.patch.object(element_resolver, "window_bounds", return_value=(1, 2, 3, 4)), \
                mock.patch.object(element_resolver, "gtk_combo_partner", return_value=combo), \
                mock.patch.object(element_resolver, "combo_edit_target", return_value=object()), \
                mock.patch.object(element_resolver, "combo_edit_identity", return_value=edit_identity):
            self.resolve_path = resolve
            return element_resolver.shadow_semantic_refusal(
                app or object(), 0, "main", (0, 0, 9, 9), target, refused)

    def test_semantic_identity_refusal_falls_back_to_the_snapshot_path(self):
        node = object()
        self.assertIsNone(self.semantic_refusal("semantic_target_not_found", node))
        # The full arm does not read the tree for a shadow it will not use.
        self.resolve_path.assert_not_called()
        self.ablate_guard()
        for code in ("semantic_target_not_found", "semantic_target_ambiguous", "stale_element_ref"):
            resolved, record, window, bounds = self.semantic_refusal(code, node)
            self.assertIs(resolved, node)
            self.assertEqual(record["runtimeId"], [0, 4])
            self.assertEqual((window, bounds), ("main", (0, 0, 9, 9)))
        self.assertEqual([entry["reason"] for entry in self.guard_log()],
                         ["semantic_target_not_found", "semantic_target_ambiguous", "stale_element_ref"])
        # Refusals that are not identity checks, and paths without a node, stay.
        self.assertIsNone(self.semantic_refusal("observation_incomplete", node))
        self.assertIsNone(self.semantic_refusal("stale_element_ref", None))
        self.assertEqual(len(self.guard_log()), 3)

    def test_shadowed_target_keeps_its_own_window(self):
        self.ablate_guard()
        dialog = object()
        app = mock.Mock()
        app.get_child_at_index.return_value = dialog
        _node, _record, window, bounds = self.semantic_refusal(
            "stale_element_ref", object(), path=(2, 1), app=app)
        app.get_child_at_index.assert_called_once_with(2)
        self.assertEqual((window, bounds), (dialog, (1, 2, 3, 4)))

    def test_shadowed_combo_keeps_its_edit_partner(self):
        self.ablate_guard()
        identity = {"providerBusName": ":1.4", "providerObjectPath": "/e", "processGeneration": "g"}
        _node, record, _window, _bounds = self.semantic_refusal(
            "stale_element_ref", object(), role="toggle button", combo=object(),
            edit_identity=identity)
        self.assertEqual(record["controlType"], "combo box")
        self.assertEqual(record["editTarget"], identity)

    def test_select_end_identity_refusal_is_logged_and_let_through(self):
        refused = runtime.OperationFailure(
            "stale_element_ref; no input was emitted.", failure_code="stale_element_ref",
            failure_phase="precondition", input_events_emitted=False)
        window, end = object(), object()
        app = mock.Mock()
        app.get_child_count.return_value = 1
        app.get_child_at_index.return_value = window
        operation = {}
        action_input = {"to_target": {"runtimeId": [0, 7]}}
        with mock.patch.object(ax_dispatch, "provider_object_path", return_value="/w"), \
                mock.patch.object(ax_dispatch, "resolve_semantic_target", side_effect=refused), \
                mock.patch.object(ax_dispatch, "shadow_semantic_refusal",
                                  wraps=lambda *args: None if not guard_shadow.guards_shadowed()
                                  else (end, {}, window, None)):
            with self.assertRaises(runtime.OperationFailure):
                ax_dispatch._resolve_select_end(app, operation, action_input, window, None)
            self.ablate_guard()
            self.assertIs(ax_dispatch._resolve_select_end(app, operation, action_input, window, None), end)

    def expected_window_mismatch(self):
        with mock.patch.object(ax_dispatch, "provider_bus_name", return_value=":1.9"), \
                mock.patch.object(ax_dispatch, "process_generation", return_value="g2"), \
                mock.patch.object(ax_dispatch, "node_pid", return_value=7), \
                mock.patch.object(ax_dispatch, "node_role", return_value="frame"):
            ax_dispatch._verify_expected_window(object(), {
                "role": "frame", "providerBusName": ":1.4", "processGeneration": "g1",
            })

    def test_batch_window_change_is_logged_and_let_through(self):
        with self.assertRaises(runtime.OperationFailure):
            self.expected_window_mismatch()
        self.ablate_guard()
        self.expected_window_mismatch()
        [entry] = self.guard_log()
        self.assertEqual(entry["guard"], "batch_window")

    def test_point_owner_is_logged_and_let_through(self):
        with mock.patch.object(actionability, "require_window_focus"), \
                mock.patch.object(actionability, "coordinate_point_owner", return_value=False):
            with self.assertRaises(runtime.OperationFailure):
                actionability.require_coordinate_delivery(object(), 5, 6, "click")
            self.ablate_guard()
            actionability.require_coordinate_delivery(object(), 5, 6, "click")
            # A check that only chooses a route keeps deciding normally.
            with guard_shadow.guards_enforced(), self.assertRaises(runtime.OperationFailure):
                actionability.require_coordinate_delivery(object(), 5, 6, "click")
        [entry] = self.guard_log()
        self.assertEqual((entry["guard"], entry["reason"], entry["x"]), ("point_owner", "different_window", 5))


class GUIGuardShadowTests(GuardLogMixin, unittest.TestCase):
    # The capture pipeline fixture, without its tests.
    patch = test_gui_capture.GUICapturePipelineTests.patch
    sample_layout = test_gui_capture.GUICapturePipelineTests.sample_layout
    capture_pixels = test_gui_capture.GUICapturePipelineTests.capture_pixels
    sample_pixels = test_gui_capture.GUICapturePipelineTests.sample_pixels
    encode_pixels = test_gui_capture.GUICapturePipelineTests.encode_pixels
    layouts = test_gui_capture.GUICapturePipelineTests.layouts
    publish = test_gui_capture.GUICapturePipelineTests.publish
    click_after = test_gui_capture.GUICapturePipelineTests.click_after
    zoom_after = test_gui_capture.GUICapturePipelineTests.zoom_after

    def setUp(self):
        super().setUp()
        test_gui_capture.GUICapturePipelineTests.setUp(self)

    def covered(self):
        return self.layouts([("0xa", 0, 0, 50, 50)],
                            [("0xa", 0, 0, 50, 50), ("0xb", 5, 5, 20, 20)])

    def test_layout_change_stops_the_batch_by_default(self):
        response = self.click_after(*self.covered(), (10, 10))
        self.assertEqual(response["failureCode"], "stale_gui_frame")
        self.execute.assert_not_called()
        self.assertEqual(self.guard_log(), [])

    def test_layout_change_is_logged_and_the_click_is_sent(self):
        self.ablate_guard()
        response = self.click_after(*self.covered(), (10, 10))
        self.assertTrue(response["ok"], response.get("error"))
        self.execute.assert_called_once()
        [entry] = self.guard_log()
        self.assertEqual(entry["channel"], "gui")
        self.assertEqual((entry["guard"], entry["reason"]), ("layout", "changed_before_batch"))
        self.assertEqual(entry["actions"], [{"type": "click", "x": 10, "y": 10}])

    def test_zoom_over_a_changed_region_is_logged_and_returned(self):
        self.ablate_guard()
        response = self.zoom_after(*self.covered(), [0, 0, 30, 30])
        self.assertTrue(response["ok"], response.get("error"))
        self.assertEqual(len(self.zoomed), 1)
        [entry] = self.guard_log()
        self.assertEqual(entry["guard"], "zoom_layout")


if __name__ == "__main__":
    unittest.main()
