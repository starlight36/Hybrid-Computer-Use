"""Native GTK combos commit one selection without popup or text input."""

from runtime_test_support import (
    AtspiPatchedTestCase, FakeTextIface, STATE, _FakeClock,
    action_evidence, mock, runtime,
)
from runtime_tests import test_select_boundaries as _select_boundaries
from runtime_tests.test_capabilities import _Node


class GtkComboTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        action_evidence.reset()
        self.addCleanup(action_evidence.reset)
        patch = mock.patch.object(runtime, "time", _FakeClock())
        patch.start()
        self.addCleanup(patch.stop)
        for name in (
                "send_key", "send_text", "_global_click", "perform_gui",
                "emit_keyboard_event", "emit_mouse_event",
                "focus_target_element", "require_window_focus",
                "_open_popup", "_commit_from_popup"):
            patch = mock.patch.object(
                runtime, name, side_effect=AssertionError("unexpected fallback: " + name))
            fallback = patch.start()
            self.addCleanup(patch.stop)
            self.addCleanup(fallback.assert_not_called)

    def combo(self):
        combo, active, selection = _select_boundaries.DropdownCommitTests._gtk_named_combo(self)
        menu = combo.children[0]
        menu.selection_iface = menu
        for node in (combo, *menu.children):
            node.states.update((STATE.ENABLED, STATE.SENSITIVE))
            node.get_n_actions = lambda: 1
            node.get_action_name = lambda _index, node=node: (
                "press" if node is combo else "select")
            node.do_action = mock.Mock(return_value=True)
            self.addCleanup(node.do_action.assert_not_called)
        return combo, active, selection

    def invoke(self, intent, combo, wanted="Seattle", record=None):
        notes = []
        self.evidence = {}
        record = {"controlType": "combo box"} if record is None else record
        if intent == "set_value":
            runtime._perform_set_value(
                {"value": wanted}, None, combo, record, notes, self.evidence)
        else:
            runtime.perform_select(
                {"text": wanted}, None, combo, record, None, notes, self.evidence)
        return notes, self.evidence

    def assert_precondition(self, intent, combo, selection,
                            code="select_unavailable", wanted="Seattle"):
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.invoke(intent, combo, wanted)
        self.assertEqual(caught.exception.failure_code, code)
        self.assertEqual(caught.exception.failure_phase, "precondition")
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertFalse(self.evidence.get("input_emitted", False))
        selection.select_child.assert_not_called()
        return caught.exception

    def assert_unknown(self, intent, combo, selection, phase="readback"):
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.invoke(intent, combo)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertEqual(caught.exception.failure_phase, phase)
        self.assertTrue(caught.exception.input_events_emitted)
        self.assertTrue(self.evidence["input_emitted"])
        self.assertFalse(self.evidence.get("readback_verified", False))
        selection.select_child.assert_called_once_with(combo, 1)

    def test_committed_selection_succeeds_even_when_api_returns_false(self):
        for intent in ("set_value", "select"):
            for accepted in (True, False):
                with self.subTest(intent=intent, accepted=accepted):
                    combo, active, selection = self.combo()
                    events = []

                    def commit(owner, index):
                        events.append("select")
                        active[0] = index
                        return accepted

                    def read_selected(owner, index):
                        events.append("read")
                        return combo.children[0].children[active[0]]

                    selection.select_child.side_effect = commit
                    selection.get_selected_child.side_effect = read_selected
                    notes, evidence = self.invoke(intent, combo)
                    selection.select_child.assert_called_once_with(combo, 1)
                    self.assertIn("read", events[events.index("select") + 1:])
                    self.assertEqual(active, [1])
                    selection.get_n_selected_children.assert_called_with(combo)
                    selection.get_selected_child.assert_called_with(combo, 0)
                    self.assertEqual(evidence["route"], "selection_api")
                    self.assertTrue(evidence["input_emitted"])
                    self.assertTrue(evidence["readback_verified"])
                    self.assertTrue(any("Seattle" in note for note in notes))

    def test_selected_value_is_a_noop_despite_label_and_popup_highlight(self):
        for intent in ("set_value", "select"):
            with self.subTest(intent=intent):
                combo, active, selection = self.combo()
                notes, evidence = self.invoke(intent, combo, "Beijing")
                self.assertEqual(combo.title, "Seattle")
                self.assertIn(STATE.SELECTED, combo.children[0].children[1].states)
                self.assertEqual(active, [0])
                selection.select_child.assert_not_called()
                self.assertFalse(evidence["input_emitted"])
                self.assertTrue(evidence["readback_verified"])
                self.assertTrue(any("Beijing" in note for note in notes))

    def test_refused_or_ineffective_selection_is_unknown_without_retry(self):
        for intent in ("set_value", "select"):
            for accepted in (False, True):
                with self.subTest(intent=intent, accepted=accepted):
                    combo, active, selection = self.combo()
                    selection.select_child.side_effect = None
                    selection.select_child.return_value = accepted
                    self.assert_unknown(intent, combo, selection)
                    self.assertEqual(active, [0])

    def test_disconnected_selection_is_unknown_even_if_it_committed(self):
        for intent in ("set_value", "select"):
            for commits in (False, True):
                with self.subTest(intent=intent, commits=commits):
                    combo, active, selection = self.combo()

                    def disconnect(owner, index):
                        if commits:
                            active[0] = index
                        raise RuntimeError("selection disconnected")

                    selection.select_child.side_effect = disconnect
                    self.assert_unknown(intent, combo, selection, phase="emission")
                    self.assertEqual(active, [1 if commits else 0])

    def test_failed_readback_cannot_verify_selection(self):
        for intent in ("set_value", "select"):
            for fault in ("count_disconnected", "child_disconnected", "none_selected",
                          "multiple_selected", "missing_child", "wrong_identity",
                          "foreign_bus", "renamed", "name_disconnected"):
                with self.subTest(intent=intent, fault=fault):
                    combo, active, selection = self.combo()
                    option = combo.children[0].children[1]

                    def commit(owner, index):
                        active[0] = index
                        if fault == "count_disconnected":
                            selection.get_n_selected_children.side_effect = RuntimeError(
                                "selection count disconnected")
                        elif fault == "child_disconnected":
                            selection.get_selected_child.side_effect = RuntimeError(
                                "selected child disconnected")
                        elif fault in ("none_selected", "multiple_selected"):
                            selection.get_n_selected_children.return_value = (
                                0 if fault == "none_selected" else 2)
                        elif fault == "missing_child":
                            selection.get_selected_child.side_effect = None
                            selection.get_selected_child.return_value = None
                        elif fault in ("wrong_identity", "foreign_bus"):
                            other = _Node(role="menu item", name="Seattle")
                            if fault == "foreign_bus":
                                other.path = option.path
                                other.app = mock.Mock(bus_name=":1.99")
                            selection.get_selected_child.side_effect = lambda *_: other
                        elif fault == "renamed":
                            option.title = "Changed choice"
                        else:
                            option.get_name = mock.Mock(
                                side_effect=RuntimeError("option name disconnected"))
                        return True

                    selection.select_child.side_effect = commit
                    self.assert_unknown(intent, combo, selection)
                    self.assertEqual(active, [1])

    def test_missing_selection_interface_is_a_precondition(self):
        for intent in ("set_value", "select"):
            for disconnected in (False, True):
                with self.subTest(intent=intent, disconnected=disconnected):
                    combo, _active, selection = self.combo()
                    if disconnected:
                        combo.get_selection_iface = mock.Mock(
                            side_effect=RuntimeError("interface disconnected"))
                    else:
                        combo.selection_iface = None
                    self.assert_precondition(intent, combo, selection)

    def test_disabled_combo_or_option_is_a_precondition(self):
        for intent in ("set_value", "select"):
            for target in ("combo", "option"):
                for state in (STATE.ENABLED, STATE.SENSITIVE):
                    with self.subTest(intent=intent, target=target, state=state):
                        combo, _active, selection = self.combo()
                        node = combo if target == "combo" else combo.children[0].children[1]
                        node.states.discard(state)
                        self.assert_precondition(intent, combo, selection)

    def test_missing_or_duplicate_option_is_a_precondition(self):
        for intent in ("set_value", "select"):
            for wanted, duplicate in (("Missing", False), ("Seattle", True),
                                      ("Beijing", True)):
                with self.subTest(intent=intent, wanted=wanted, duplicate=duplicate):
                    combo, _active, selection = self.combo()
                    if duplicate:
                        combo.children[0].add(_Node(
                            role="menu item", name=wanted,
                            states=(STATE.ENABLED, STATE.SENSITIVE)))
                    self.assert_precondition(
                        intent, combo, selection, code="invalid_argument", wanted=wanted)

    def test_incomplete_scan_cannot_prove_unique_choice(self):
        for intent in ("set_value", "select"):
            for fault in ("child_count", "missing_child", "child_disconnected",
                          "name_disconnected", "role_disconnected", "scan_limit"):
                with self.subTest(intent=intent, fault=fault):
                    combo, _active, selection = self.combo()
                    menu = combo.children[0]
                    if fault == "child_count":
                        menu.get_child_count = mock.Mock(
                            side_effect=RuntimeError("child count disconnected"))
                    elif fault in ("missing_child", "child_disconnected"):
                        menu.get_child_count = lambda: 3
                        if fault == "missing_child":
                            menu.get_child_at_index = lambda index: (
                                menu.children[index] if index < 2 else None)
                    elif fault == "name_disconnected":
                        menu.children[0].get_name = mock.Mock(
                            side_effect=RuntimeError("name disconnected"))
                    elif fault == "role_disconnected":
                        menu.children[0].get_role_name = mock.Mock(
                            side_effect=RuntimeError("role disconnected"))
                    else:
                        menu.get_child_count = lambda: runtime.MAX_DROPDOWN_OPTIONS + 1
                    failure = self.assert_precondition(intent, combo, selection)
                    self.assertIn("select_options_incomplete", str(failure))

    def test_invalid_or_changed_index_is_a_precondition(self):
        for intent in ("set_value", "select"):
            for index in (-1, None, 2, 0, "changed", "disconnected"):
                with self.subTest(intent=intent, index=index):
                    combo, _active, selection = self.combo()
                    option = combo.children[0].children[1]
                    if index == "changed":
                        option.get_index_in_parent = mock.Mock(side_effect=[1, 0])
                    elif index == "disconnected":
                        option.get_index_in_parent = mock.Mock(
                            side_effect=RuntimeError("index disconnected"))
                    else:
                        option.get_index_in_parent = lambda: index
                    self.assert_precondition(intent, combo, selection)

    def test_stale_parent_or_child_identity_is_a_precondition(self):
        for intent in ("set_value", "select"):
            for fault in ("orphan", "foreign_parent", "foreign_combo", "replaced_child"):
                with self.subTest(intent=intent, fault=fault):
                    combo, _active, selection = self.combo()
                    menu = combo.children[0]
                    option = menu.children[1]
                    if fault == "orphan":
                        option.parent = None
                    elif fault == "foreign_parent":
                        _Node(role="menu").add(option)
                    elif fault == "foreign_combo":
                        foreign = _Node(role="combo box")
                        foreign.path = combo.path
                        foreign.app = mock.Mock(bus_name=":1.99")
                        foreign.add(menu)
                    else:
                        other = _Node(role="menu item", name="Seattle")
                        menu.get_child_at_index = mock.Mock(
                            side_effect=[menu.children[0], option, other])
                    self.assert_precondition(intent, combo, selection)

    def test_parent_change_after_selection_is_unknown_without_retry(self):
        for intent in ("set_value", "select"):
            with self.subTest(intent=intent):
                combo, active, selection = self.combo()
                option = combo.children[0].children[1]

                def commit(owner, index):
                    active[0] = index
                    option.parent = None
                    return True

                selection.select_child.side_effect = commit
                self.assert_unknown(intent, combo, selection)

    def test_native_editable_combo_never_writes_entry_text(self):
        for intent in ("set_value", "select"):
            for wanted in ("Seattle", "Beijing", "Not an option"):
                with self.subTest(intent=intent, wanted=wanted):
                    combo, active, selection = self.combo()
                    editor = _Node(role="entry", states=(
                        STATE.ENABLED, STATE.SENSITIVE, STATE.EDITABLE))
                    editor.text_iface = FakeTextIface("Beijing")
                    editor.get_editable_text_iface = lambda: editor.text_iface
                    combo.add(editor)
                    combo.states.add(STATE.EDITABLE)
                    combo.text_iface = editor.text_iface
                    combo.get_editable_text_iface = lambda: editor.text_iface
                    with mock.patch.object(runtime, "resolve_combo_editor") as resolve, \
                            mock.patch.object(runtime, "set_element_value_outcome") as write:
                        if wanted == "Not an option":
                            self.assert_precondition(
                                intent, combo, selection, code="invalid_argument", wanted=wanted)
                        else:
                            self.invoke(intent, combo, wanted)
                            self.assertEqual(active, [1 if wanted == "Seattle" else 0])
                        resolve.assert_not_called()
                        write.assert_not_called()
                    self.assertEqual(editor.text_iface.text, "Beijing")
                    self.assertEqual(editor.text_iface.set_calls, [])
                    self.assertEqual(editor.text_iface.inserts, [])
                    self.assertEqual(editor.text_iface.deletes, [])

    def test_projected_gtk_combo_keeps_its_editor_write(self):
        combo, _active, selection = self.combo()
        combo.children.clear()
        combo.states.add(STATE.SHOWING)
        combo.get_action_name = lambda _index: "click"
        hidden = _Node(role="combo box", states=())
        editor = _Node(role="text", states=(
            STATE.SHOWING, STATE.VISIBLE, STATE.EDITABLE,
            STATE.ENABLED, STATE.SENSITIVE))
        editor.text_iface = FakeTextIface("old")
        editor.get_editable_text_iface = lambda: editor.text_iface
        _Node(role="filler").add(editor, hidden, combo)
        with mock.patch.object(runtime, "process_generation", return_value="fixture-generation"):
            self.assertIs(runtime.gtk_combo_partner(combo, "combo box"), hidden)
            record = {"controlType": "combo box",
                      "editTarget": runtime.combo_edit_identity(editor)}
            _notes, evidence = self.invoke("set_value", combo, "new text", record)
        self.assertEqual(editor.text_iface.text, "new text")
        self.assertEqual(editor.text_iface.set_calls, ["new text"])
        self.assertTrue(evidence["readback_verified"])
        selection.select_child.assert_not_called()
