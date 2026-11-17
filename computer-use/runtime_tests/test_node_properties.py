#!/usr/bin/env python3
"""Linux provider node property tests: actions, placeholders, text and frames."""

import types

from runtime_test_support import *  # noqa: F401,F403


class ClickableMarkerTests(AtspiPatchedTestCase):
    """The tree marks which elements can be clicked without coordinates."""

    def make(self, actions):
        class Node(_TreeNode):
            def get_action_iface(self):
                # Real nodes without actions do not implement Action. The
                # runtime checks the interface before the action count because
                # LibreOffice's ATK bridge asserts on non-ATK_ACTION objects.
                return object() if actions else None

            def get_n_actions(self):
                return len(actions)

            def get_action_name(self, index):
                return actions[index]

            def get_action_description(self, index):
                return ""

        return Node(role="push button", name="Home", x=1, y=1, w=5, h=5)

    def segment(self, node):
        """Like record_for, read the action table once and derive the marker."""
        _, has_click = runtime.node_actions(node)
        return runtime.state_segment(node, has_click_action=has_click)

    def test_element_with_click_action_is_marked(self):
        self.assertIn("has-click-action", self.segment(self.make(["click"])))

    def test_element_without_any_action_is_not_marked(self):
        """Nautilus sidebar rows have a Component interface but no Action interface.

        node_actions() hides click-like actions, so without the marker such a
        row renders exactly like a push button with a click action, and the
        agent has to fall back to coordinates for both.
        """
        self.assertNotIn("has-click-action", self.segment(self.make([])))

    def test_marker_matches_what_click_would_actually_invoke(self):
        """The click marker agrees with the action that click would invoke.

        `node_actions()` produces the marker and `preferred_action_index()`
        picks the action, because rendering may read the action table only
        once: repeated queries through LibreOffice's ATK bridge can make the
        application exit. This test keeps the two implementations consistent.
        """
        cases = [
            ["menu"],                    # secondary action only, nothing to click
            ["activate"],                # exact match
            ["click", "menu"],           # mixed
            [],                          # no actions at all
            ["Press the button"],        # descriptive name, substring fallback
        ]
        for actions in cases:
            node = self.make(actions)
            _, has_click = runtime.node_actions(node)
            invocable = runtime.preferred_action_index(node) is not None
            self.assertEqual(
                has_click, invocable,
                "action table {!r}: mark says {}, invocable is {}".format(
                    actions, has_click, invocable),
            )
            self.assertEqual(has_click, "has-click-action" in self.segment(node))


class PlaceholderAttributeIntrospectionTests(AtspiPatchedTestCase):
    """Object attributes are read only for text inputs that may carry a placeholder."""

    class AttributeNode(_TreeNode):
        def __init__(self, *args, attributes=None, **kwargs):
            super().__init__(*args, **kwargs)
            self.attributes = attributes or {}
            self.attribute_reads = 0

        def get_attributes(self):
            self.attribute_reads += 1
            return self.attributes

    def test_non_text_record_does_not_read_object_attributes(self):
        node = self.AttributeNode(
            role="push button",
            name="Insert",
            x=1,
            y=1,
            w=20,
            h=10,
            attributes={"placeholder-text": "must not be read"},
        )

        record = runtime.record_for(node, 1, [0, 0], None)

        self.assertEqual(record["placeholder"], "")
        self.assertEqual(node.attribute_reads, 0)

    def test_text_input_reads_placeholder_once(self):
        node = self.AttributeNode(
            role="entry",
            name="",
            x=1,
            y=1,
            w=20,
            h=10,
            attributes={"placeholder-text": "Search files"},
        )

        record = runtime.record_for(node, 1, [0, 0], None)

        self.assertEqual(record["placeholder"], "Search files")
        self.assertEqual(node.attribute_reads, 1)

    def test_role_aliases_cover_model_facing_text_inputs(self):
        for role in (
            "entry",
            "text",
            "text box",
            "textbox",
            "search box",
            "searchbox",
            "text field",
            "textfield",
            "text area",
            "textarea",
            "password text",
            "combo box",
        ):
            node = self.AttributeNode(
                role=role,
                attributes={"placeholder": "Hint"},
            )
            self.assertEqual(runtime.placeholder_text(node), "Hint", role)
            self.assertEqual(node.attribute_reads, 1, role)


class DropdownItemTests(AtspiPatchedTestCase):
    """Semantic actions on dropdown items close the popup without committing.

    Nothing afterwards reveals that the value was not applied.
    """

    def popup(self):
        return _TreeNode(role="window", name="")

    def test_table_cell_in_unnamed_popup_is_a_dropdown_item(self):
        """LibreOffice's line spacing dropdown exposes its items as table cells.

        do_action on `table cell Double` returns True and closes the dropdown,
        but the control still shows Single; only a coordinate click commits.
        The failure is invisible afterwards: the popup disappears with its
        elements, so the tree always changes and a "nothing changed, retry"
        check never fires, and the dialog's combo box is a phantom node that
        reports no value to read back.
        """
        cell = _TreeNode(role="table cell", name="Double", x=3, y=71, w=358, h=21)

        self.assertTrue(runtime.is_dropdown_item(cell, self.popup()))

    def test_menu_item_is_not_treated_as_dropdown_item(self):
        """Menu items are not dropdown items.

        do_action works on items such as `Rename…` in the Nautilus context
        menu, so treating them as dropdown items would block a working path.
        """
        item = _TreeNode(role="menu item", name="Rename…", x=7, y=189, w=304, h=25)

        self.assertFalse(runtime.is_dropdown_item(item, self.popup()))

    def test_table_cell_in_main_frame_is_not_a_dropdown_item(self):
        """Calc sheet cells also use the table cell role but are not dropdown items."""
        cell = _TreeNode(role="table cell", name="A1", x=10, y=10, w=80, h=20)
        frame = _TreeNode(role="frame", name="untitled - Calc", w=900, h=600)

        self.assertFalse(runtime.is_dropdown_item(cell, frame))

    def test_table_cell_in_named_dialog_is_not_a_dropdown_item(self):
        """A dialog has a title and the dialog role; a dropdown is an unnamed window."""
        cell = _TreeNode(role="table cell", name="calc-test.csv", x=41, y=157, w=332, h=21)
        dialog = _TreeNode(role="dialog", name="Document Recovery", w=578, h=406)

        self.assertFalse(runtime.is_dropdown_item(cell, dialog))


class ElectronActionNamesTests(AtspiPatchedTestCase):
    """Chromium and Electron action names cause both missed and false click entries."""

    def make(self, actions):
        class Node(_TreeNode):
            def get_action_iface(self):
                return object() if actions else None

            def get_n_actions(self):
                return len(actions)

            def get_action_name(self, index):
                return actions[index]

            def get_action_description(self, index):
                return ""

        return Node(role="section", x=1, y=1, w=5, h=5)

    def test_do_default_is_the_click_entry(self):
        """`doDefault` is Chromium's default action and serves as the click entry.

        VS Code nodes expose ('doDefault', 'showContextMenu'). Without this name
        they get no `[has-click-action]` marker and
        `click_method:"accessibility"` fails, so Electron apps could not be
        clicked through the semantic channel at all.
        """
        node = self.make(["doDefault", "showContextMenu"])
        names, has_click = runtime.node_actions(node)

        self.assertTrue(has_click)
        self.assertIsNotNone(runtime.preferred_action_index(node))
        self.assertNotIn("doDefault", names,
                         "it is the click itself and must not be listed in More actions")

    def test_click_ancestor_is_not_this_element_s_click(self):
        """`clickAncestor` contains "click" but clicks an ancestor element.

        VS Code nodes expose ('clickAncestor', 'showContextMenu'), which the
        substring fallback would otherwise match. Treating it as this
        element's click would act elsewhere while neither the result nor the
        tree shows it, the worst class of failure.
        """
        node = self.make(["clickAncestor", "showContextMenu"])
        _, has_click = runtime.node_actions(node)

        self.assertFalse(has_click)
        self.assertIsNone(runtime.preferred_action_index(node))

    def test_gecko_spells_it_with_a_space(self):
        """Toolkits spell the same action differently.

        Chromium and Electron use `clickAncestor`, while Gecko (Thunderbird)
        uses `click ancestor` with a space. Excluding only one spelling lets
        the other match the substring fallback and click the ancestor again.
        """
        node = self.make(["click ancestor"])
        _, has_click = runtime.node_actions(node)

        self.assertFalse(has_click)
        self.assertIsNone(runtime.preferred_action_index(node))

    def test_normalization_ignores_case_and_separators(self):
        for spelling in ("clickAncestor", "click ancestor", "Click-Ancestor",
                         "click_ancestor", "CLICKANCESTOR"):
            node = self.make([spelling])
            _, has_click = runtime.node_actions(node)
            self.assertFalse(has_click, "{!r} must not count as this element's action".format(spelling))

    def test_gecko_checkbox_state_named_actions_are_click_entries(self):
        """Gecko names checkbox actions after the resulting state.

        A checked box exposes only `uncheck` and an unchecked one only
        `check`; invoking it toggles the box. Without these names every Gecko
        checkbox falls back to coordinate clicks, and settings screens consist
        mostly of checkboxes.
        """
        for spelling in ("check", "uncheck"):
            node = self.make([spelling])
            names, has_click = runtime.node_actions(node)
            self.assertTrue(has_click, "{!r} must count as a click entry point".format(spelling))
            self.assertNotIn(spelling, names, "it is the click itself and must not be listed in More actions")

    def test_plain_click_still_works(self):
        node = self.make(["click", "showContextMenu"])
        _, has_click = runtime.node_actions(node)

        self.assertTrue(has_click)
        self.assertIsNotNone(runtime.preferred_action_index(node))


class ObjectReplacementTests(AtspiPatchedTestCase):
    def test_object_replacement_placeholder_is_stripped(self):
        """Chromium marks embedded objects with U+FFFC, rendered as `Value: ￼￼￼`.

        The placeholder carries no information yet looks like content, which
        suggests that the control already has a value. Pages such as the
        VS Code welcome page contain many of them.
        """
        self.assertEqual(runtime.limit_text("￼"), "")
        self.assertEqual(runtime.limit_text("a￼￼b"), "ab")

    def test_real_text_is_untouched(self):
        self.assertEqual(runtime.limit_text("Save As…"), "Save As…")


class QtRichTextTests(AtspiPatchedTestCase):
    def test_qt_tooltip_html_is_reduced_to_its_message(self):
        """Qt stores tooltips as whole HTML documents, CSS included.

        In VLC's preferences such HTML took more than half of an observation
        while the message is usually one sentence, so an unprocessed
        `Description:` would exhaust the observation budget.
        """
        blob = (
            '<html><head><meta name="qrichtext" content="1" />'
            '<style type="text/css"> p, li { white-space: pre-wrap; } </style>'
            '</head><body><p>Show a controller in fullscreen mode</p></body></html>'
        )

        self.assertEqual(
            runtime.plain_text_from_rich_text(blob),
            "Show a controller in fullscreen mode",
        )

    def test_br_becomes_a_separator_not_a_join(self):
        blob = "<html><body><p>one<br/>two</p></body></html>"

        self.assertEqual(runtime.plain_text_from_rich_text(blob), "one two")

    def test_plain_text_with_angle_brackets_is_untouched(self):
        """Plain text is untouched, since code, templates and math use angle brackets.

        Stripping them would alter the data the agent reads.
        """
        for text in ("a < b and c > d", "List<String> items", "if x<3: pass"):
            self.assertEqual(runtime.plain_text_from_rich_text(text), text)

    def test_empty_and_none_are_safe(self):
        self.assertEqual(runtime.plain_text_from_rich_text(None), "")
        self.assertEqual(runtime.plain_text_from_rich_text(""), "")


class TextReadTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        self.node = _TreeNode(role="entry", name="Document")
        self.node._text = FakeTextIface("A🙂e\u0301\ufffc中文\n" * 1000)

    def read(self, field="value", start=0, limit=4000):
        return runtime.read_element_text(
            self.node, {"field": field, "start": start, "limit": limit})

    def test_pages_preserve_codepoints_and_reconstruct_text(self):
        chunks = []
        offset = 0
        while offset < len(self.node._text.text):
            page = self.read(start=offset, limit=997)
            self.assertEqual(page["total"], len(self.node._text.text))
            self.assertEqual(page["start"], offset)
            self.assertEqual(page["end"], offset + len(page["text"]))
            self.assertLessEqual(len(page["text"]), 997)
            chunks.append(page["text"])
            offset = page["end"]
        self.assertEqual("".join(chunks), self.node._text.text)

    def test_exact_end_and_empty_text_are_valid_empty_pages(self):
        for text in ("", "🙂中"):
            self.node._text.text = text
            self.assertEqual(self.read(start=len(text)), {
                "field": "value", "start": len(text), "end": len(text),
                "total": len(text), "text": "",
            })

    def test_reads_only_requested_bounded_span(self):
        with mock.patch.object(FakeAtspi.Text, "get_text", wraps=FakeAtspi.Text.get_text) as read:
            page = self.read(start=1)
        read.assert_called_once_with(self.node._text, 1, 4001)
        self.assertEqual(page["text"], self.node._text.text[1:4001])

    def test_invalid_range_fails_without_reading(self):
        for start, limit in ((-1, 1), (False, 1), (0, True), (0, 0),
                             (0, 4001), (0.5, 1), (99999, 10), (2147483648, 1)):
            with self.subTest(start=start, limit=limit), \
                    mock.patch.object(FakeAtspi.Text, "get_text") as read:
                with self.assertRaises(runtime.OperationFailure) as raised:
                    self.read(start=start, limit=limit)
                self.assertEqual(raised.exception.failure_code, "runtime_error")
                self.assertFalse(raised.exception.input_events_emitted)
                read.assert_not_called()

    def test_maximum_start_is_accepted_at_exact_end(self):
        with mock.patch.object(FakeAtspi.Text, "get_character_count", return_value=2147483647), \
                mock.patch.object(FakeAtspi.Text, "get_text", return_value="") as read:
            page = self.read(start=2147483647, limit=1)
        self.assertEqual(page["start"], 2147483647)
        self.assertEqual(page["end"], 2147483647)
        self.assertEqual(page["text"], "")
        read.assert_called_once_with(self.node._text, 2147483647, 2147483647)

    def test_missing_text_never_uses_numeric_or_descendant_fallback(self):
        self.node._text = None
        self.node.value = FakeValueIface(42)
        with mock.patch.object(runtime, "element_value") as fallback:
            with self.assertRaises(runtime.OperationFailure):
                self.read()
        fallback.assert_not_called()

    def test_unreadable_interface_and_counts_fail(self):
        with mock.patch.object(self.node, "get_text_iface", side_effect=RuntimeError("offline")):
            with self.assertRaises(runtime.OperationFailure):
                self.read()
        for count in (None, -1, True, 3.5):
            with self.subTest(count=count), \
                    mock.patch.object(FakeAtspi.Text, "get_character_count", return_value=count):
                with self.assertRaises(runtime.OperationFailure):
                    self.read()
        with mock.patch.object(FakeAtspi.Text, "get_character_count", side_effect=RuntimeError("offline")):
            with self.assertRaises(runtime.OperationFailure):
                self.read()

    def test_short_long_and_unreadable_text_fail(self):
        for text in (None, "", "x", "abcd", b"abc"):
            with self.subTest(text=text), \
                    mock.patch.object(FakeAtspi.Text, "get_text", return_value=text):
                with self.assertRaises(runtime.OperationFailure):
                    self.read(limit=3)
        with mock.patch.object(FakeAtspi.Text, "get_text", side_effect=RuntimeError("offline")):
            with self.assertRaises(runtime.OperationFailure):
                self.read()

    def test_count_changes_and_readback_failures_fail(self):
        for after in (7999, 8001, None, True, RuntimeError("offline")):
            with self.subTest(after=after), mock.patch.object(
                    FakeAtspi.Text, "get_character_count", side_effect=[8000, after]):
                with self.assertRaises(runtime.OperationFailure):
                    self.read(limit=3)

    def test_full_name_and_normalized_description_do_not_read_value(self):
        self.node.title = "🙂Name\ufffc" * 900
        self.node.description = "<html><p>" + "中 &amp; text<br/>" * 900 + "</p></html>"
        expected_description = runtime.plain_text_from_rich_text(self.node.description)
        with mock.patch.object(self.node, "get_text_iface", side_effect=AssertionError("value read")):
            for field, full in (("name", self.node.title), ("description", expected_description)):
                page = self.read(field, start=501, limit=1000)
                self.assertEqual(page["text"], full[501:1501])
                self.assertEqual(page["total"], len(full))

    def test_unreadable_name_and_description_fail(self):
        for field, getter in (("name", "get_name"), ("description", "get_description")):
            with self.subTest(field=field), mock.patch.object(
                    self.node, getter, side_effect=RuntimeError("offline")):
                with self.assertRaises(runtime.OperationFailure):
                    self.read(field)

    def test_password_fields_refuse_before_reading_content(self):
        self.node.role = "password text"
        with mock.patch.object(self.node, "get_text_iface") as text, \
                mock.patch.object(self.node, "get_name") as name, \
                mock.patch.object(self.node, "get_description") as description:
            for field in ("name", "value", "description"):
                with self.assertRaises(runtime.OperationFailure) as raised:
                    self.read(field)
                self.assertIn("protected", str(raised.exception))
            text.assert_not_called()
            name.assert_not_called()
            description.assert_not_called()


class TextLengthMetadataTests(AtspiPatchedTestCase):
    def test_explicit_truncation_survives_ellipsis_length_overlap(self):
        for total in (500, 501, 502, 503):
            with self.subTest(total=total):
                full = "🙂" * total
                node = _TreeNode(role="entry", name=full, description=full)
                node._text = FakeTextIface(full)
                with mock.patch.object(FakeAtspi.Text, "get_character_count", wraps=FakeAtspi.Text.get_character_count) as count, \
                        mock.patch.object(FakeAtspi.Text, "get_text", wraps=FakeAtspi.Text.get_text) as read:
                    record = runtime.record_for(node, 0, [0], None)
                expected = ["name", "value", "description"] if total > 500 else []
                self.assertCountEqual(record["textTruncated"], expected)
                count.assert_called_once_with(node._text)
                read.assert_called_once_with(node._text, 0, min(total, 501))
                for field in ("name", "value", "description"):
                    self.assertEqual(record["textLengths"][field], total)
                    self.assertEqual(record[field], full[:500] + ("..." if total > 500 else ""))
                    if total > 500:
                        self.assertLessEqual(total, len(record[field]))

    def test_complete_text_with_literal_ellipsis_is_not_marked(self):
        full = "x" * 497 + "..."
        node = _TreeNode(role="entry", name=full, description=full)
        node._text = FakeTextIface(full)
        self.assertEqual(runtime.record_for(node, 0, [0], None)["textTruncated"], [])
        node.title = node.description = node._text.text = "x" * 600
        self.assertEqual(runtime.record_for(node, 0, [0], None, text_limit=None)["textTruncated"], [])

    def test_value_truncation_uses_raw_count_and_actual_preview(self):
        node = _TreeNode(role="entry")
        node._text = FakeTextIface("\ufffc" + "x" * 500)
        record = runtime.record_for(node, 0, [0], None)
        self.assertEqual(record["value"], "x" * 500)
        self.assertNotIn("value", record["textTruncated"])
        node._text.text += "z"
        record = runtime.record_for(node, 0, [0], None)
        self.assertEqual(record["value"], "x" * 500)
        self.assertIn("value", record["textTruncated"])
        with mock.patch.object(FakeAtspi.Text, "get_text", return_value="x" * 500):
            record = runtime.record_for(node, 0, [0], None)
        self.assertNotIn("value", record["textTruncated"])
        self.assertNotIn("value", record["textLengths"])

    def test_fallback_values_do_not_inherit_raw_text_truncation(self):
        node = _TreeNode(role="entry")
        node._text = FakeTextIface("\ufffc" * 600)
        node.value = FakeValueIface(42)
        record = runtime.record_for(node, 0, [0], None)
        self.assertEqual(record["value"], "42.0")
        self.assertNotIn("value", record["textTruncated"])
        node.value = None
        with mock.patch.object(runtime, "embedded_editor_text", return_value="aggregate"):
            record = runtime.record_for(node, 0, [0], None)
        self.assertNotIn("value", record["textTruncated"])

    def test_full_lengths_share_reads_and_keep_default_preview(self):
        node = _TreeNode(role="entry", name="🙂中" * 600, description="说明 " * 600)
        node._text = FakeTextIface("🙂正文" * 100000)
        facts = runtime.NodeFacts(node)
        with mock.patch.object(node, "get_name", wraps=node.get_name) as name, \
                mock.patch.object(node, "get_description", wraps=node.get_description) as description, \
                mock.patch.object(FakeAtspi.Text, "get_character_count", wraps=FakeAtspi.Text.get_character_count) as count, \
                mock.patch.object(FakeAtspi.Text, "get_text", wraps=FakeAtspi.Text.get_text) as text:
            facts.value("name")
            facts.value("description")
            record = runtime.record_for(node, 0, [0], None, facts=facts)
        name.assert_called_once_with()
        description.assert_called_once_with()
        count.assert_called_once_with(node._text)
        text.assert_called_once_with(node._text, 0, 501)
        expected = {"name": node.title, "value": node._text.text,
                    "description": runtime.plain_text_from_rich_text(node.description)}
        for field, full in expected.items():
            self.assertEqual(record[field], full[:500] + "...")
            self.assertEqual(record["textLengths"][field], len(full))

    def test_labelled_name_uses_full_labels_without_reading_their_values(self):
        label = _TreeNode(role="label", name="Long 🙂 label " * 90 + ":")
        node = _TreeNode(role="entry")
        node.get_relation_set = lambda: [_FakeRelation(FakeAtspi.RelationType.LABELLED_BY, [label])]
        expected = label.title.rstrip(":").strip()
        with mock.patch.object(label, "get_name", wraps=label.get_name) as read, \
                mock.patch.object(label, "get_text_iface", side_effect=AssertionError("value read")):
            record = runtime.record_for(node, 0, [0], None)
            read.assert_called_once_with()
            page = runtime.read_element_text(node, {"field": "name", "start": 500, "limit": 900})
        self.assertEqual(record["name"], expected[:500] + "...")
        self.assertEqual(record["textLengths"]["name"], len(expected))
        self.assertEqual(record["nameHash"], runtime.name_fingerprint(expected))
        self.assertIn("name", record["textTruncated"])
        self.assertEqual(page["text"], expected[500:1400])
        self.assertEqual(page["total"], len(expected))

    def test_value_length_is_not_numeric_or_aggregated_length(self):
        node = _TreeNode(role="entry")
        for iface in (None, FakeTextIface("")):
            node._text = iface
            node.value = FakeValueIface(42)
            record = runtime.record_for(node, 0, [0], None)
            self.assertEqual(record["value"], "42.0")
            self.assertNotIn("value", record["textLengths"])
        node.value = None
        with mock.patch.object(runtime, "embedded_editor_text", return_value="line one\nline two"):
            record = runtime.record_for(node, 0, [0], None)
        self.assertNotIn("value", record["textLengths"])
        node.role = "combo box"
        with mock.patch.object(runtime, "selected_option_name", return_value="Option"):
            record = runtime.record_for(node, 0, [0], None)
        self.assertEqual(record["value"], "Option")
        self.assertNotIn("value", record["textLengths"])

    def test_borrowed_name_pages_follow_the_same_bounded_relation_read(self):
        labels = [_TreeNode(role="label", name="Label " + str(index)) for index in range(20)]
        relation = _FakeRelation(FakeAtspi.RelationType.LABELLED_BY, labels)
        node = _TreeNode(role="entry")
        node.get_relation_set = lambda: [relation]
        record = runtime.record_for(node, 0, [0], None)
        with mock.patch.object(relation, "get_target", wraps=relation.get_target) as targets:
            page = runtime.read_element_text(node, {"field": "name", "start": 0, "limit": 4000})
        self.assertEqual(page["text"], record["name"])
        self.assertEqual(page["total"], record["textLengths"]["name"])
        self.assertEqual(targets.call_count, 16)

    def test_projected_combo_value_does_not_claim_its_editors_length(self):
        toggle = _TreeNode(role="toggle button")
        toggle._text = FakeTextIface("toggle text" * 100)
        combo = _TreeNode(role="combo box", name="Choice")
        editor = _TreeNode(role="entry")
        editor._text = FakeTextIface("actual editor text")
        with mock.patch.object(runtime, "gtk_combo_partner", return_value=combo), \
                mock.patch.object(runtime, "combo_edit_target", return_value=editor), \
                mock.patch.object(runtime, "combo_edit_identity", return_value={"providerBusName": ":1.42"}):
            record = runtime.record_for(toggle, 0, [0], None)
            page = runtime.read_element_text(toggle, {"field": "name", "start": 0, "limit": 4000})
        self.assertEqual(record["value"], editor._text.text)
        self.assertNotIn("value", record["textLengths"])
        self.assertNotIn("value", record["textTruncated"])
        self.assertEqual(page["text"], combo.title)

    def test_content_name_pages_use_full_visible_labels(self):
        label = _TreeNode(role="label", name="Visible label " * 100)
        node = _TreeNode(role="list item", kids=[label])
        record = runtime.record_for(node, 0, [0], None)
        page = runtime.read_element_text(node, {"field": "name", "start": 500, "limit": 900})
        self.assertEqual(record["nameSource"], "content")
        self.assertEqual(page["text"], label.title.strip()[500:1400])
        self.assertEqual(page["total"], record["textLengths"]["name"])

    def test_empty_text_keeps_zero_and_unreadable_text_has_no_length(self):
        node = _TreeNode(role="entry")
        node._text = FakeTextIface("")
        self.assertEqual(runtime.record_for(node, 0, [0], None)["textLengths"]["value"], 0)
        node._text.text = "abcd"
        with mock.patch.object(FakeAtspi.Text, "get_text", return_value="a"):
            record = runtime.record_for(node, 0, [0], None)
        self.assertNotIn("value", record["textLengths"])

    def test_unreadable_description_has_no_claimed_length(self):
        node = _TreeNode(role="entry")
        with mock.patch.object(node, "get_description", side_effect=RuntimeError("offline")):
            record = runtime.record_for(node, 0, [0], None)
        self.assertNotIn("description", record["textLengths"])

    def test_password_record_never_reads_text(self):
        node = _TreeNode(role="password text", name="Password")
        node._text = FakeTextIface("secret")
        with mock.patch.object(node, "get_text_iface", side_effect=AssertionError("password read")) as text:
            record = runtime.record_for(node, 0, [0], None)
        text.assert_not_called()
        self.assertEqual(record["value"], "")
        self.assertNotIn("value", record["textLengths"])


class SaneRectFrameTests(unittest.TestCase):
    """Screen frames of content scrolled far away are real.

    A web table of 6000 rows of 25 px is 150,000 px tall, and its last rows
    sit 150,000 px below the viewport. A 100,000 px limit dropped their
    frames, so observe and find lost them, and the table itself had no frame
    for coordinate cell reads. Unrendered GTK controls still report origins
    near INT_MIN, which stay rejected.
    """

    @staticmethod
    def rect(x, y, width, height):
        return types.SimpleNamespace(x=x, y=y, width=width, height=height)

    def test_far_scrolled_content_keeps_its_frame(self):
        self.assertIsNotNone(runtime.sane_rect_frame(self.rect(0, 149975, 800, 25)))
        self.assertIsNotNone(runtime.sane_rect_frame(self.rect(0, -60, 800, 150000)))

    def test_sentinel_geometry_is_rejected(self):
        self.assertIsNone(runtime.sane_rect_frame(self.rect(-2147483648, -2147483648, 1, 1)))
        self.assertIsNone(runtime.sane_rect_frame(self.rect(0, 0, 2147483647, 20)))
        self.assertIsNone(runtime.sane_rect_frame(self.rect(0, 0, 0, 20)))
