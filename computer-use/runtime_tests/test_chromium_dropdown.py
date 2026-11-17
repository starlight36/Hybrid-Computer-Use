"""Chromium HTML combos verify option actions; list boxes refuse mutations."""

from runtime_test_support import (
    AtspiPatchedTestCase, FakeStateSet, FakeTextIface, STATE, _FakeClock,
    action_evidence, mock, runtime,
)
from runtime_tests.test_capabilities import _Node


class ChromiumDropdownTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        action_evidence.reset()
        self.addCleanup(action_evidence.reset)
        self.clock = _FakeClock()
        self.patch(runtime, "time", self.clock)
        self.selection = mock.Mock()
        self.selection.is_child_selected.return_value = False
        self.patch(runtime.Atspi, "Selection", self.selection, create=True)
        for name in ("select_child", "deselect_child", "deselect_selected_child",
                     "clear_selection", "select_all"):
            method = getattr(self.selection, name)
            method.side_effect = AssertionError("unexpected Selection mutation: " + name)
            self.addCleanup(method.assert_not_called)
        for name in (
                "send_key", "send_text", "_global_click", "perform_gui",
                "emit_keyboard_event", "emit_mouse_event",
                "focus_target_element", "require_window_focus",
                "_open_popup", "_commit_from_popup"):
            fallback = self.patch(
                runtime, name,
                side_effect=AssertionError("unexpected fallback: " + name))
            self.addCleanup(fallback.assert_not_called)
        focus = self.patch(
            runtime.Atspi.Component, "grab_focus",
            side_effect=AssertionError("unexpected native focus"))
        self.addCleanup(focus.assert_not_called)

    def patch(self, target, name, *args, **kwargs):
        patcher = mock.patch.object(target, name, *args, **kwargs)
        result = patcher.start()
        self.addCleanup(patcher.stop)
        return result

    def dropdown(self, role="combo box", multiple=False):
        control = _Node(role=role, name="City", states=(STATE.ENABLED, STATE.SENSITIVE))
        control.app.get_toolkit_name.return_value = "chromium"
        control.get_application = lambda: control.app
        control.get_attributes = lambda: {"tag": "select"}
        if multiple:
            control.states.add(STATE.MULTISELECTABLE)
        elif role == "combo box":
            control.states.add(STATE.EXPANDABLE)
        options = [
            _Node(role="menu item" if role == "combo box" else "list item",
                  name=name, states=(STATE.ENABLED, STATE.SENSITIVE, STATE.SELECTABLE))
            for name in ("Beijing", "Seattle", "Tokyo")
        ]
        options[0].states.add(STATE.SELECTED)
        group = _Node(role="menu" if role == "combo box" else "section").add(*options)
        control.add(group)
        control.selection_iface = control
        group.selection_iface = group
        control.get_n_actions = lambda: 1
        control.get_action_name = lambda _index: "open"
        control.do_action = mock.Mock(side_effect=AssertionError("unexpected popup action"))
        self.addCleanup(control.do_action.assert_not_called)
        for option in options:
            option.app = control.app
            option.get_application = lambda: control.app
            option.get_n_actions = lambda: 2
            option.get_action_name = lambda index: ("press", "select")[index]
            option.get_action_iface = lambda option=option: option

            def commit(index, option=option):
                if index != 1:
                    raise AssertionError("only the named select action may be invoked")
                if not multiple:
                    for other in options:
                        other.states.discard(STATE.SELECTED)
                option.states.add(STATE.SELECTED)
                return True

            option.do_action = mock.Mock(side_effect=commit)
        return control, options

    def invoke(self, intent, control, wanted="Seattle"):
        action_evidence.reset()
        self.evidence = {}
        notes = []
        record = {"controlType": control.role}
        if intent == "set_value":
            runtime._perform_set_value(
                {"value": wanted}, None, control, record, notes, self.evidence)
        else:
            runtime.perform_select(
                {"text": wanted}, None, control, record, None, notes, self.evidence)
        return notes, self.evidence

    def assert_precondition(self, intent, control, options, wanted="Seattle",
                            code="select_unavailable"):
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.invoke(intent, control, wanted)
        self.assertEqual(caught.exception.failure_code, code)
        self.assertEqual(caught.exception.failure_phase, "precondition")
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertFalse(self.evidence.get("input_emitted", False))
        for option in options:
            option.do_action.assert_not_called()
        return caught.exception

    def assert_unknown(self, intent, control, options, phase="readback"):
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.invoke(intent, control)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertEqual(caught.exception.failure_phase, phase)
        self.assertTrue(caught.exception.input_events_emitted)
        self.assertTrue(self.evidence["input_emitted"])
        self.assertFalse(self.evidence.get("readback_verified", False))
        self.assertEqual(self.evidence["route"], "semantic_action")
        options[1].do_action.assert_called_once_with(1)
        options[0].do_action.assert_not_called()
        options[2].do_action.assert_not_called()
        return caught.exception

    def test_named_option_action_commits_even_if_it_returns_false(self):
        for intent in ("set_value", "select"):
            for accepted in (True, False):
                with self.subTest(intent=intent, accepted=accepted):
                    control, options = self.dropdown()
                    commit = options[1].do_action.side_effect

                    def action(index):
                        self.assertTrue(self.evidence["input_emitted"])
                        commit(index)
                        return accepted

                    options[1].do_action.side_effect = action
                    notes, evidence = self.invoke(intent, control)
                    options[1].do_action.assert_called_once_with(1)
                    options[0].do_action.assert_not_called()
                    options[2].do_action.assert_not_called()
                    self.assertEqual(
                        [item.title for item in options if STATE.SELECTED in item.states],
                        ["Seattle"])
                    self.assertEqual(evidence["route"], "semantic_action")
                    self.assertTrue(evidence["input_emitted"])
                    self.assertTrue(evidence["readback_sampled"])
                    self.assertTrue(evidence["readback_verified"])
                    self.assertTrue(any("Seattle" in note for note in notes))

    def test_same_value_needs_no_action_even_without_select_action(self):
        for intent in ("set_value", "select"):
            for role, multiple in (("combo box", False), ("list box", False),
                                   ("list box", True)):
                with self.subTest(intent=intent, role=role, multiple=multiple):
                    control, options = self.dropdown(role, multiple)
                    if multiple:
                        options[2].states.add(STATE.SELECTED)
                    options[0].get_n_actions = lambda: 0
                    before = [set(option.states) for option in options]
                    notes, evidence = self.invoke(intent, control, "Beijing")
                    for option in options:
                        option.do_action.assert_not_called()
                    self.assertEqual([option.states for option in options], before)
                    self.assertFalse(evidence["input_emitted"])
                    self.assertTrue(evidence["readback_verified"])
                    self.assertTrue(any("Beijing" in note for note in notes))

    def test_list_box_mutation_is_refused_without_action_or_editor_fallback(self):
        for intent in ("set_value", "select"):
            for multiple in (False, True):
                with self.subTest(intent=intent, multiple=multiple):
                    control, options = self.dropdown("list box", multiple)
                    if multiple:
                        options[2].states.add(STATE.SELECTED)
                    control.states.add(STATE.EDITABLE)
                    control.text_iface = FakeTextIface("Beijing")
                    control.get_editable_text_iface = lambda: control.text_iface
                    before = [set(option.states) for option in options]
                    with mock.patch.object(runtime, "resolve_combo_editor") as resolve, \
                            mock.patch.object(runtime, "set_element_value_outcome") as write:
                        failure = self.assert_precondition(intent, control, options)
                        resolve.assert_not_called()
                        write.assert_not_called()
                    self.assertIn("input", str(failure).lower())
                    self.assertIn("change", str(failure).lower())
                    self.assertEqual([option.states for option in options], before)
                    self.assertEqual(control.text_iface.text, "Beijing")
                    self.assertEqual(control.text_iface.set_calls, [])

    def test_list_item_ref_cannot_bypass_list_box_mutation_guard(self):
        for multiple in (False, True):
            for grouped in (False, True):
                with self.subTest(multiple=multiple, grouped=grouped):
                    control, options = self.dropdown("list box", multiple)
                    if multiple:
                        options[2].states.add(STATE.SELECTED)
                    if not grouped:
                        control.children.clear()
                        control.add(*options)
                    before = [set(option.states) for option in options]
                    action_evidence.reset()
                    evidence = {}
                    with self.assertRaises(runtime.OperationFailure) as caught:
                        runtime.perform_select(
                            {}, None, options[1], {"controlType": "list item"},
                            None, [], evidence)
                    self.assertEqual(caught.exception.failure_code, "select_unavailable")
                    self.assertEqual(caught.exception.failure_phase, "precondition")
                    self.assertFalse(caught.exception.input_events_emitted)
                    self.assertFalse(evidence.get("input_emitted", False))
                    self.assertFalse(evidence.get("readback_verified", False))
                    self.assertIn("input", str(caught.exception).lower())
                    self.assertIn("change", str(caught.exception).lower())
                    self.assertEqual([option.states for option in options], before)
                    for option in options:
                        option.do_action.assert_not_called()

    def test_false_or_accepted_action_without_selection_is_unknown(self):
        for intent in ("set_value", "select"):
            for accepted in (False, True):
                with self.subTest(intent=intent, accepted=accepted):
                    control, options = self.dropdown()
                    options[1].do_action.side_effect = None
                    options[1].do_action.return_value = accepted
                    before = [set(option.states) for option in options]
                    self.assert_unknown(intent, control, options)
                    self.assertEqual([option.states for option in options], before)

    def test_lost_action_reply_is_unknown_even_after_commit(self):
        for intent in ("set_value", "select"):
            for commits in (False, True):
                for structured in (False, True):
                    with self.subTest(intent=intent, commits=commits, structured=structured):
                        control, options = self.dropdown()
                        commit = options[1].do_action.side_effect
                        error = (runtime.OperationFailure(
                            "lost action reply", failure_code="effect_unknown",
                            failure_phase="emission", input_events_emitted=True)
                            if structured else RuntimeError("lost action reply"))

                        def disconnect(index):
                            self.assertTrue(self.evidence["input_emitted"])
                            if commits:
                                commit(index)
                            raise error

                        options[1].do_action.side_effect = disconnect
                        self.assert_unknown(intent, control, options, phase="emission")
                        self.assertEqual(STATE.SELECTED in options[1].states, commits)

    def test_readback_waits_for_selected_state_with_fake_clock(self):
        for intent in ("set_value", "select"):
            with self.subTest(intent=intent):
                control, options = self.dropdown()
                ready_at = self.clock.monotonic() + 0.2

                def accept(_index):
                    options[0].states.discard(STATE.SELECTED)
                    options[1].get_state_set = lambda: FakeStateSet(
                        options[1].states | ({STATE.SELECTED}
                                            if self.clock.monotonic() >= ready_at else set()))
                    return True

                options[1].do_action.side_effect = accept
                _notes, evidence = self.invoke(intent, control)
                self.assertGreaterEqual(self.clock.monotonic(), ready_at)
                self.assertTrue(evidence["readback_verified"])
                options[1].do_action.assert_called_once_with(1)

    def test_fresh_proxy_with_same_identity_and_name_can_confirm_selection(self):
        for intent in ("set_value", "select"):
            with self.subTest(intent=intent):
                control, options = self.dropdown()
                replacement = _Node(role="menu item", name="Seattle", states=(STATE.SELECTED,))
                replacement.path = options[1].path
                replacement.app = options[1].app

                def commit(_index):
                    options[0].states.discard(STATE.SELECTED)
                    control.children[0].children[1] = replacement
                    replacement.parent = control.children[0]
                    return True

                options[1].do_action.side_effect = commit
                _notes, evidence = self.invoke(intent, control)
                self.assertNotIn(STATE.SELECTED, options[1].states)
                self.assertTrue(evidence["readback_verified"])
                options[1].do_action.assert_called_once_with(1)

    def test_failed_fresh_readback_never_confirms_cached_option(self):
        faults = (
            "removed", "replacement", "foreign_bus", "renamed", "duplicate",
            "none_selected", "multiple_selected", "child_count", "missing_child",
            "name_disconnected", "role_disconnected", "target_state", "other_state",
        )
        for intent in ("set_value", "select"):
            for fault in faults:
                with self.subTest(intent=intent, fault=fault):
                    control, options = self.dropdown()
                    group = control.children[0]
                    commit = options[1].do_action.side_effect

                    def action(index):
                        commit(index)
                        if fault == "removed":
                            group.children.remove(options[1])
                        elif fault in ("replacement", "foreign_bus"):
                            other = _Node(role="menu item", name="Seattle", states=(STATE.SELECTED,))
                            if fault == "foreign_bus":
                                other.path = options[1].path
                                other.app = mock.Mock(bus_name=":1.99")
                            group.children[1] = other
                            other.parent = group
                        elif fault == "renamed":
                            options[1].title = "Portland"
                        elif fault == "duplicate":
                            group.add(_Node(role="menu item", name="Seattle"))
                        elif fault == "none_selected":
                            options[1].states.discard(STATE.SELECTED)
                        elif fault == "multiple_selected":
                            options[0].states.add(STATE.SELECTED)
                        elif fault == "child_count":
                            group.get_child_count = mock.Mock(side_effect=RuntimeError("disconnected"))
                        elif fault == "missing_child":
                            group.get_child_at_index = lambda _index: None
                        elif fault == "name_disconnected":
                            options[1].get_name = mock.Mock(side_effect=RuntimeError("disconnected"))
                        elif fault == "role_disconnected":
                            options[0].get_role_name = mock.Mock(side_effect=RuntimeError("disconnected"))
                        else:
                            target = options[1] if fault == "target_state" else options[0]
                            target.get_state_set = mock.Mock(side_effect=RuntimeError("disconnected"))
                        return True

                    options[1].do_action.side_effect = action
                    self.assert_unknown(intent, control, options)

    def test_source_changes_during_fresh_scan_are_rejected_before_action(self):
        for intent in ("set_value", "select"):
            for fault in ("removed", "replacement", "renamed"):
                with self.subTest(intent=intent, fault=fault):
                    control, options = self.dropdown()
                    group = control.children[0]
                    scans = 0

                    def child_count():
                        nonlocal scans
                        scans += 1
                        if scans == 2:
                            if fault == "removed":
                                group.children.remove(options[1])
                                options[1].parent = None
                            elif fault == "replacement":
                                other = _Node(role="menu item", name="Seattle",
                                              states=(STATE.ENABLED, STATE.SENSITIVE))
                                group.children[1] = other
                                other.parent = group
                                options[1].parent = None
                            else:
                                options[1].title = "Portland"
                        return len(group.children)

                    group.get_child_count = child_count
                    self.assert_precondition(intent, control, options)
                    self.assertGreaterEqual(scans, 2)
                    self.assertNotIn(STATE.SELECTED, options[1].states)

    def test_disabled_node_or_option_rejected_before_mutation(self):
        for intent in ("set_value", "select"):
            for target in ("control", "option"):
                for state in (STATE.ENABLED, STATE.SENSITIVE):
                    with self.subTest(intent=intent, target=target, state=state):
                        control, options = self.dropdown()
                        node = control if target == "control" else options[1]
                        node.states.discard(state)
                        self.assert_precondition(intent, control, options)

    def test_missing_or_duplicate_name_rejected_before_mutation(self):
        for intent in ("set_value", "select"):
            for wanted, duplicate in (("Missing", False), ("Seattle", True), ("Beijing", True)):
                with self.subTest(intent=intent, wanted=wanted, duplicate=duplicate):
                    control, options = self.dropdown()
                    if duplicate:
                        control.children[0].add(_Node(role="menu item", name=wanted))
                    self.assert_precondition(
                        intent, control, options, wanted=wanted, code="invalid_argument")

    def test_incomplete_option_scan_rejected_before_mutation(self):
        faults = ("child_count", "negative_count", "missing_child", "child_disconnected",
                  "name_disconnected", "role_disconnected", "scan_limit")
        for intent in ("set_value", "select"):
            for fault in faults:
                with self.subTest(intent=intent, fault=fault):
                    control, options = self.dropdown()
                    group = control.children[0]
                    if fault == "child_count":
                        group.get_child_count = mock.Mock(side_effect=RuntimeError("disconnected"))
                    elif fault == "negative_count":
                        group.get_child_count = lambda: -1
                    elif fault == "missing_child":
                        group.get_child_at_index = lambda _index: None
                    elif fault == "child_disconnected":
                        group.get_child_at_index = mock.Mock(side_effect=RuntimeError("disconnected"))
                    elif fault == "name_disconnected":
                        options[0].get_name = mock.Mock(side_effect=RuntimeError("disconnected"))
                    elif fault == "role_disconnected":
                        options[0].get_role_name = mock.Mock(side_effect=RuntimeError("disconnected"))
                    else:
                        group.get_child_count = lambda: runtime.MAX_DROPDOWN_OPTIONS + 1
                    failure = self.assert_precondition(intent, control, options)
                    self.assertIn("select_options_incomplete", str(failure))

    def test_missing_select_action_has_no_popup_advice_or_fallback(self):
        for intent in ("set_value", "select"):
            for fault in ("none", "other_actions", "count_disconnected", "name_disconnected"):
                with self.subTest(intent=intent, fault=fault):
                    control, options = self.dropdown()
                    if fault == "none":
                        options[1].get_n_actions = lambda: 0
                    elif fault == "other_actions":
                        options[1].get_action_name = lambda index: ("press", "click")[index]
                    elif fault == "count_disconnected":
                        options[1].get_n_actions = mock.Mock(side_effect=RuntimeError("disconnected"))
                    else:
                        options[1].get_action_name = mock.Mock(side_effect=RuntimeError("disconnected"))
                    failure = self.assert_precondition(intent, control, options)
                    self.assertNotRegex(str(failure).lower(), r"popup|open.*list|keyboard|focus")

    def test_html_select_never_resolves_or_writes_an_editor(self):
        for intent in ("set_value", "select"):
            with self.subTest(intent=intent):
                control, _options = self.dropdown()
                control.states.add(STATE.EDITABLE)
                control.text_iface = FakeTextIface("Beijing")
                control.get_editable_text_iface = lambda: control.text_iface
                with mock.patch.object(runtime, "resolve_combo_editor") as resolve, \
                        mock.patch.object(runtime, "set_element_value_outcome") as write:
                    _notes, evidence = self.invoke(intent, control)
                    resolve.assert_not_called()
                    write.assert_not_called()
                self.assertEqual(control.text_iface.text, "Beijing")
                self.assertTrue(evidence["readback_verified"])

    def test_non_html_selects_keep_editable_value_behavior(self):
        cases = (
            ("chromium", "combo box", "input"),
            ("chromium", "combo box", "div"),
            ("chromium", "combo box", None),
            ("chromium", "entry", "select"),
            ("chromium", "list box", "div"),
            ("qt", "combo box", "select"),
            ("gecko", "combo box", "select"),
            ("gtk", "list box", "select"),
        )
        for toolkit, role, tag in cases:
            with self.subTest(toolkit=toolkit, role=role, tag=tag):
                control, options = self.dropdown(role)
                control.app.get_toolkit_name.return_value = toolkit
                control.get_attributes = lambda: {} if tag is None else {"tag": tag}
                control.states.add(STATE.EDITABLE)
                control.text_iface = FakeTextIface("Beijing")
                control.get_editable_text_iface = lambda: control.text_iface
                _notes, evidence = self.invoke("set_value", control, "Portland")
                self.assertEqual(control.text_iface.text, "Portland")
                self.assertEqual(control.text_iface.set_calls, ["Portland"])
                self.assertEqual(evidence["route"], "value_api")
                self.assertTrue(evidence["readback_verified"])
                for option in options:
                    option.do_action.assert_not_called()

    def test_aria_combobox_and_other_toolkits_keep_selection_fallback(self):
        for toolkit, tag in (("chromium", "input"), ("chromium", "div"),
                             ("qt", "select"), ("other", "select")):
            with self.subTest(toolkit=toolkit, tag=tag):
                control, options = self.dropdown()
                control.app.get_toolkit_name.return_value = toolkit
                control.get_attributes = lambda: {"tag": tag}
                control.children[0].selection_iface = None
                options[1].get_n_actions = lambda: 0

                def commit(owner, index):
                    self.assertIs(owner, control)
                    options[0].states.discard(STATE.SELECTED)
                    options[index].states.add(STATE.SELECTED)
                    control.title = options[index].title
                    return True

                with mock.patch.object(self.selection, "select_child", side_effect=commit) as select:
                    _notes, evidence = self.invoke("select", control)
                    select.assert_called_once_with(control, 1)
                    self.assertTrue(evidence["readback_verified"])
                for option in options:
                    option.do_action.assert_not_called()
