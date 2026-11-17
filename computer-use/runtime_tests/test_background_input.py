"""Semantic click/scroll delivery and pre-emission global-input refusal."""

from runtime_test_support import *
from runtime_tests.test_capabilities import _Node


class BackgroundInputTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        self.notes, self.evidence = [], {}
        self.focus = mock.Mock()
        self.pointer = mock.Mock()
        self.wheel = mock.Mock(return_value="wheel")
        self.geometry = mock.Mock(side_effect=lambda node, record, bounds: (record, True))
        for name, replacement in (
            ("require_coordinate_delivery", self.focus),
            ("require_window_focus", self.focus),
            ("focus_target_element", self.focus),
            ("send_mouse_click", self.pointer),
            ("scroll_element", self.wheel),
            ("screen_point", mock.Mock(return_value=(10, 20))),
            ("require_stable_coordinate_geometry", self.geometry),
            ("preferred_action_index", mock.Mock(return_value=None)),
            ("do_action_by_index", mock.Mock(return_value=False)),
            ("is_dropdown_item", mock.Mock(return_value=False)),
        ):
            patch = mock.patch.object(runtime, name, replacement)
            patch.start()
            self.addCleanup(patch.stop)

    def click(self, node, **kwargs):
        return runtime._perform_click(kwargs, object(), {}, node,
                                      {"frame": {"x": 0}}, self.notes, self.evidence)

    def scroll(self, node, **kwargs):
        return runtime._perform_scroll(kwargs, object(), {}, node,
                                       {"frame": {"x": 0}}, self.notes, self.evidence)

    def assert_quiet(self):
        self.geometry.assert_not_called()
        self.focus.assert_not_called()
        self.pointer.assert_not_called()
        self.wheel.assert_not_called()

    def tab(self):
        parent = _Node(role="page tab list")
        parent.get_selection_iface = lambda: parent
        tab = _Node(role="page tab", states=(STATE.SELECTABLE,), parent=parent)
        parent.add(tab)
        selection = mock.Mock()
        selection.select_child.side_effect = lambda *_: tab.states.add(STATE.SELECTED) or True
        selection.is_child_selected.side_effect = lambda *_: STATE.SELECTED in tab.states
        patch = mock.patch.object(runtime.Atspi, "Selection", selection, create=True)
        patch.start()
        self.addCleanup(patch.stop)
        return tab, selection

    def range_node(self, orientation=STATE.VERTICAL, current=20, increment=3):
        pane = _Node(role="scroll pane")
        target = _Node(role="table cell", parent=pane)
        bar = _Node(role="scroll bar", states=(orientation,), parent=pane)
        pane.add(target)
        pane.add(bar)
        value = FakeValueIface(current)
        bar.get_value_iface = lambda: value
        for name, result in (("get_minimum_value", 0), ("get_maximum_value", 100),
                             ("get_minimum_increment", increment)):
            patch = mock.patch.object(runtime.Atspi.Value, name, return_value=result, create=True)
            patch.start()
            self.addCleanup(patch.stop)
        return target, bar, value

    def test_tab_without_action_uses_selection_and_reads_back(self):
        tab, selection = self.tab()
        self.click(tab, allow_global_input=False)
        selection.select_child.assert_called_once()
        self.assertIn(STATE.SELECTED, tab.states)
        self.assertTrue(self.evidence["readback_verified"])
        self.assert_quiet()

    def test_lying_selection_never_retries_with_pointer(self):
        tab, selection = self.tab()
        selection.select_child.side_effect = None
        selection.select_child.return_value = True
        with mock.patch.object(runtime, "_wait_until", side_effect=lambda predicate, timeout: predicate()):
            with self.assertRaises(runtime.OperationFailure) as caught:
                self.click(tab)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertTrue(caught.exception.input_events_emitted)
        self.assert_quiet()

    def test_pointer_fallback_is_refused_before_focus_or_geometry(self):
        for method in ("auto", "global"):
            with self.subTest(method=method):
                with self.assertRaises(runtime.OperationFailure) as caught:
                    self.click(FakeNode(), click_method=method, allow_global_input=False)
                self.assertEqual(caught.exception.failure_code, "global_input_forbidden")
                self.assertFalse(caught.exception.input_events_emitted)
        self.assert_quiet()

    def test_editor_focus_is_forbidden_before_grab_focus(self):
        editor = FakeNode(states=(STATE.EDITABLE,), text=FakeTextIface("old"))
        editor.get_role_name = lambda: "entry"
        editor.get_action_name = lambda index: "activate"
        with mock.patch.object(runtime, "preferred_action_index", return_value=0):
            with self.assertRaises(runtime.OperationFailure) as caught:
                self.click(editor, allow_global_input=False)
        self.assertEqual(caught.exception.failure_code, "global_input_forbidden")
        self.assert_quiet()

    def test_state_only_pointer_route_is_forbidden(self):
        with mock.patch.object(runtime, "preferred_action_index", return_value=0), \
                mock.patch.object(runtime, "action_only_sets_state", return_value=True):
            with self.assertRaises(runtime.OperationFailure) as caught:
                self.click(FakeNode(), allow_global_input=False)
        self.assertEqual(caught.exception.failure_code, "global_input_forbidden")
        self.assert_quiet()

    def test_scroll_ancestor_range_reads_back_without_global_input(self):
        target, bar, value = self.range_node()
        self.scroll(target, direction="down", pages=1.5, allow_global_input=False)
        self.assertEqual(value.current, 42.5)
        self.assertEqual(value.set_calls, [42.5])
        self.assertTrue(self.evidence["readback_verified"])
        self.assert_quiet()

    def test_horizontal_scroll_uses_horizontal_range(self):
        target, bar, value = self.range_node(STATE.HORIZONTAL)
        self.scroll(target, direction="left", pages=1, allow_global_input=False)
        self.assertEqual(value.current, 5)
        self.assert_quiet()

    def test_scroll_clamps_to_boundary_without_repeating_input(self):
        target, bar, value = self.range_node(current=96)
        self.scroll(target, direction="down", pages=1)
        self.scroll(target, direction="down", pages=1)
        self.assertEqual(value.set_calls, [100])
        self.assertFalse(self.evidence["input_emitted"])
        self.assertTrue(self.evidence["readback_verified"])
        self.assert_quiet()

    def test_lying_range_returns_unknown_without_global_retry(self):
        target, bar, value = self.range_node()
        value.honest = False
        with mock.patch.object(runtime, "_wait_until", side_effect=lambda predicate, timeout: predicate()):
            with self.assertRaises(runtime.OperationFailure) as caught:
                self.scroll(target, direction="down", pages=1)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertTrue(caught.exception.input_events_emitted)
        self.assertFalse(self.evidence["readback_verified"])
        self.assert_quiet()

    def test_missing_or_unusable_range_refuses_without_input(self):
        target, bar, value = self.range_node(increment=float("nan"))
        for node in (FakeNode(), target):
            with self.subTest(node=node):
                with self.assertRaises(runtime.OperationFailure) as caught:
                    self.scroll(node, allow_global_input=False)
                self.assertEqual(caught.exception.failure_code, "global_input_forbidden")
                self.assertFalse(caught.exception.input_events_emitted)
        self.assertEqual(value.set_calls, [])
        self.assert_quiet()

    def test_default_keeps_explicit_foreground_fallback(self):
        self.scroll(FakeNode())
        self.focus.assert_called_once()
        self.wheel.assert_called_once()
        self.assertTrue(any("[synthesis]" in note for note in self.notes))

    def test_attempted_action_failure_is_not_a_no_input_refusal(self):
        with mock.patch.object(runtime, "preferred_action_index", return_value=0):
            with self.assertRaises(runtime.OperationFailure) as caught:
                self.click(FakeNode(), allow_global_input=False)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertTrue(caught.exception.input_events_emitted)
        self.assert_quiet()

    def test_ambiguous_scrollbars_refuse_without_choosing_one(self):
        target, bar, value = self.range_node()
        target.parent.add(_Node(role="scroll bar", states=(STATE.VERTICAL,)))
        with self.assertRaises(runtime.OperationFailure):
            self.scroll(target, allow_global_input=False)
        self.assertEqual(value.set_calls, [])
        self.assert_quiet()

    def test_nearest_scroll_pane_does_not_bubble_to_outer_scrollbar(self):
        target, bar, value = self.range_node()
        inner = _Node(role="scroll pane", parent=target.parent)
        nested = _Node(role="table cell", parent=inner)
        with self.assertRaises(runtime.OperationFailure):
            self.scroll(nested, allow_global_input=False)
        self.assertEqual(value.set_calls, [])
        self.assert_quiet()

    def test_range_mutates_then_throws_retains_emission_evidence(self):
        target, bar, value = self.range_node()
        def lost_reply(iface, requested):
            iface.current = requested
            raise RuntimeError("reply lost")
        with mock.patch.object(runtime.Atspi.Value, "set_current_value", side_effect=lost_reply):
            with self.assertRaises(runtime.OperationFailure) as caught:
                self.scroll(target, allow_global_input=False)
        self.assertTrue(caught.exception.input_events_emitted)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertEqual(value.current, 35)
        self.assert_quiet()

    def test_sibling_range_in_generic_panel_is_not_scroll_ownership(self):
        target, bar, value = self.range_node()
        target.parent.role = "panel"
        with self.assertRaises(runtime.OperationFailure):
            self.scroll(target, allow_global_input=False)
        self.assertEqual(value.set_calls, [])
        self.assert_quiet()

    def test_unrepresentable_delta_is_not_reported_as_a_boundary(self):
        target, bar, value = self.range_node()
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.scroll(target, direction="down", pages=1e-300, allow_global_input=False)
        self.assertEqual(caught.exception.failure_code, "invalid_argument")
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertEqual(value.set_calls, [])
        self.assert_quiet()

    def test_unchanged_value_within_tolerance_is_not_verified(self):
        target, bar, value = self.range_node(increment=1e-11)
        value.honest = False
        with mock.patch.object(runtime, "_wait_until", side_effect=lambda predicate, timeout: predicate()):
            with self.assertRaises(runtime.OperationFailure) as caught:
                self.scroll(target, direction="down", pages=1, allow_global_input=False)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertTrue(caught.exception.input_events_emitted)
        self.assert_quiet()


class BackgroundInputWireTests(unittest.TestCase):
    def test_linux_policy_fixtures_reach_dispatch_unchanged(self):
        root = Path(__file__).parents[1] / "testdata" / "provider-wire"
        manifest = json.loads((root / "linux-input-manifest.json").read_text())
        self.assertTrue(manifest["allow_global_input_platforms"]["linux"])
        for fixture in manifest["fixtures"]:
            with self.subTest(fixture=fixture["name"]):
                request = json.loads((root / fixture["file"]).read_text())
                with mock.patch.object(wire, "_initialize_provider"), \
                        mock.patch.object(wire, "perform_provider_request", return_value={"ok": True}) as dispatch:
                    response = wire._execute_serialized_request(json.dumps(request))
                self.assertTrue(response["accessibility"]["ok"])
                dispatch.assert_called_once_with("resolve_and_act", request["action"])
                self.assertIs(dispatch.call_args.args[1]["input"]["allow_global_input"], False)

    def test_policy_requires_a_boolean(self):
        for intent in ("click", "scroll"):
            for policy in (None, "false", 0, 1, [], {}):
                with self.subTest(intent=intent, policy=policy):
                    payload = {"intent": intent, "target": {}, "input": {
                        "allow_global_input": policy}}
                    if intent == "scroll":
                        payload["input"].update(direction="down", pages=1)
                    with self.assertRaises(wire_request.ProviderRequestError):
                        wire_request._validate_action_input(payload)

    def test_policy_absent_true_and_false_are_accepted(self):
        for intent in ("click", "scroll"):
            for policy in ({}, {"allow_global_input": True}, {"allow_global_input": False}):
                with self.subTest(intent=intent, policy=policy):
                    payload = {"intent": intent, "target": {}, "input": dict(policy)}
                    if intent == "scroll":
                        payload["input"].update(direction="down", pages=1)
                    wire_request._validate_action_input(payload)
