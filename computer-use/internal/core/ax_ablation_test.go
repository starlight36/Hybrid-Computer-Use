package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

// ablationSets returns every subset of the ablations as a
// COMPUTER_USE_ABLATE value, the empty set first.
func ablationSets() []string {
	sets := []string{}
	for mask := 0; mask < 1<<len(contract.AblationNames); mask++ {
		names := []string{}
		for index, name := range contract.AblationNames {
			if mask&(1<<index) != 0 {
				names = append(names, name)
			}
		}
		sets = append(sets, strings.Join(names, ","))
	}
	return sets
}

// axVisibleText is what a model reads about AX: the server instructions,
// the AX tool and the GUI settle_ms text, which points to AX waits.
func axVisibleText(t *testing.T) (string, map[string]any) {
	t.Helper()
	var ax contract.ToolDefinition
	for _, tool := range contract.ToolDefinitions() {
		if tool.Name == contract.PublicToolAX {
			ax = tool
		}
	}
	encoded, err := json.Marshal(ax)
	if err != nil {
		t.Fatal(err)
	}
	settle := contract.GUIInputSchema()["oneOf"].([]any)[1].(map[string]any)["properties"].(map[string]any)["settle_ms"]
	settleText, _ := json.Marshal(settle)
	// The description also goes in unescaped, so quoted example steps match.
	return contract.PublishedServerInstructions() + "\n" + ax.Description + "\n" +
			string(encoded) + "\n" + string(settleText),
		ax.InputSchema["properties"].(map[string]any)
}

// The words of each mechanism; none may remain once it is ablated.
var ablatedWords = map[string][]*regexp.Regexp{
	contract.AblateWait: {
		regexp.MustCompile(`"wait"`), regexp.MustCompile(`(?i)wait step`),
		regexp.MustCompile(`settle needs|"settle"`), regexp.MustCompile(`timeout_ms|Wait timeouts`),
		regexp.MustCompile(`waits with`), regexp.MustCompile(`"gone"`),
	},
	contract.AblateDiff: {
		regexp.MustCompile(`\bdiffs?\b`), regexp.MustCompile(`report what changed`),
		regexp.MustCompile(`reports each target`), regexp.MustCompile(`selection read back`),
		regexp.MustCompile(`"none"`),
	},
	contract.AblateBatching: {
		regexp.MustCompile(`ordered steps`), regexp.MustCompile(`"type":"type"`),
	},
}

func TestAblatedTextOmitsTheRemovedMechanisms(t *testing.T) {
	full, _ := axVisibleText(t)
	fullDescription := findToolDefinition(t, contract.PublicToolAX).Description
	for _, set := range ablationSets() {
		t.Run("ablate="+set, func(t *testing.T) {
			t.Setenv(contract.AblationEnv, set)
			text, properties := axVisibleText(t)
			if set == "" || set == contract.AblateGuard {
				// guard changes no text: its errors only stop occurring.
				if text != full {
					t.Fatalf("text changed without a text-changing ablation")
				}
				return
			}
			for _, name := range strings.Split(set, ",") {
				for _, word := range ablatedWords[name] {
					if match := word.FindString(text); match != "" {
						t.Errorf("%s ablated, but %q remains", name, match)
					}
				}
			}
			if description := findToolDefinition(t, contract.PublicToolAX).Description; len(description) > len(fullDescription) {
				t.Errorf("AX description grew to %d bytes from %d", len(description), len(fullDescription))
			}
			actions := properties["actions"].(map[string]any)
			wantMax := contract.AXBatchMaxActions
			if strings.Contains(set, "batching") && strings.Contains(set, "wait") {
				wantMax = 1
			}
			if actions["maxItems"] != wantMax {
				t.Errorf("maxItems %v want %d", actions["maxItems"], wantMax)
			}
		})
	}
}

// Each text-changing ablation states how the ablated act behaves, so an
// agent does not have to learn it from rejected calls.
func TestAblatedDescriptionStatesTheChangedBehavior(t *testing.T) {
	statements := map[string]string{
		contract.AblateBatching: "act runs one input step with optional wait steps before or after it",
		contract.AblateWait:     "act cannot pause for a later state; to check one, read again with observe or find.",
		contract.AblateDiff:     "Its result shows the whole current tree of its window after its actions instead of a report of what changed.",
	}
	for _, set := range ablationSets() {
		t.Run("ablate="+set, func(t *testing.T) {
			t.Setenv(contract.AblationEnv, set)
			description := findToolDefinition(t, contract.PublicToolAX).Description
			for name, statement := range statements {
				ablated := strings.Contains(","+set+",", ","+name+",")
				if name == contract.AblateBatching && strings.Contains(set, contract.AblateWait) {
					statement = "act runs one step"
				}
				if strings.Contains(description, statement) != ablated {
					t.Errorf("%s ablated=%v, description:\n%s", name, ablated, description)
				}
			}
		})
	}
}

func TestAblatedDescriptionsKeepTheirExamplesValid(t *testing.T) {
	example := regexp.MustCompile(`\{"mode":"act".*\}`)
	for _, set := range ablationSets() {
		t.Run("ablate="+set, func(t *testing.T) {
			t.Setenv(contract.AblationEnv, set)
			t.Setenv(contract.GuardLogEnv, filepath.Join(t.TempDir(), "guard.jsonl"))
			description := findToolDefinition(t, contract.PublicToolAX).Description
			call := example.FindString(description)
			if call == "" {
				t.Fatalf("no act example in %s", description)
			}
			if _, err := DecodeAXRequest(decodeTestArguments(t, call)); err != nil {
				t.Fatalf("example %s: %s", call, err.Message)
			}
			if argErr := contract.ValidateToolArguments(contract.PublicToolAX, decodeTestArguments(t, call)); argErr != nil {
				t.Fatalf("example %s: %s", call, argErr.Message)
			}
		})
	}
}

func TestBatchingAblationAllowsOneInputStep(t *testing.T) {
	t.Setenv(contract.AblationEnv, contract.AblateBatching)
	for _, raw := range []string{
		`{"mode":"act","actions":[{"type":"click","ref":"r1"}]}`,
		`{"mode":"act","actions":[{"type":"wait","window":"A"},{"type":"click","ref":"r1"},{"type":"wait","settle":true}]}`,
		`{"mode":"act","actions":[{"type":"close","ref":"w1"},{"type":"wait","window":"A","gone":true}]}`,
	} {
		if _, err := DecodeAXRequest(decodeTestArguments(t, raw)); err != nil {
			t.Fatalf("%s: %s", raw, err.Message)
		}
	}
	_, err := DecodeAXRequest(decodeTestArguments(t,
		`{"mode":"act","actions":[{"type":"click","ref":"r1"},{"type":"wait","window":"A"},{"type":"select","ref":"r2"}]}`))
	if err == nil || err.rule != contract.ArgumentRuleOneInputStep || err.path != "actions[2]" {
		t.Fatalf("second input step: %#v", err)
	}
	if !strings.Contains(err.Hint, `{"type":"wait","window":"A"}`) || strings.Contains(err.Hint, "select") {
		t.Fatalf("hint should keep the steps before the second input: %s", err.Hint)
	}
	// Input after close is refused as close-not-last, without suggesting a
	// wait that would put a second input step in the call.
	_, err = DecodeAXRequest(decodeTestArguments(t,
		`{"mode":"act","actions":[{"type":"close","ref":"w1"},{"type":"click","ref":"r1"}]}`))
	if err == nil || err.rule != contract.ArgumentRuleCloseNotLast ||
		!strings.HasPrefix(err.Message, "close must be the last step") || strings.Contains(err.Hint, `"gone"`) {
		t.Fatalf("input after close: %#v", err)
	}
	if focus := noFocusMessage(t); strings.Contains(focus, "batch") {
		t.Fatalf("no-focus message: %s", focus)
	}

	t.Setenv(contract.AblationEnv, "batching,wait")
	_, err = DecodeAXRequest(decodeTestArguments(t,
		`{"mode":"act","actions":[{"type":"click","ref":"r1"},{"type":"click","ref":"r2"}]}`))
	if err == nil || err.Message != "act takes one action per call; send the others in later calls" {
		t.Fatalf("two actions without waits: %#v", err)
	}
}

// noFocusMessage returns the no_focused_window message for a desktop where
// no window has focus.
func noFocusMessage(t *testing.T) string {
	t.Helper()
	backend := &recordingPlatformBackend{accessibilityResponse: &ProviderAccessibilityResult{
		OK: true, Apps: editorApps(testWindow{testHandleReport, "Report - Editor", false}),
	}}
	_, err := NewServiceWithBackend(backend).resolveFocusedWindow()
	if err == nil || err.Code != contract.AXCodeNoFocusedWindow {
		t.Fatalf("want no_focused_window, got %#v", err)
	}
	return err.Message
}

func TestBatchingAblationKeepsGUIBatches(t *testing.T) {
	t.Setenv(contract.AblationEnv, "batching,wait,diff")
	args := decodeTestArguments(t, `{"actions":[{"type":"click","x":1,"y":2},{"type":"type","text":"a"},{"type":"keypress","keys":["ENTER"]}]}`)
	if err := contract.ValidateToolArguments(contract.PublicToolGUI, args); err != nil {
		t.Fatalf("GUI batch: %s", err.Message)
	}
}

func TestWaitAblationRemovesWaitSteps(t *testing.T) {
	t.Setenv(contract.AblationEnv, contract.AblateWait)
	_, err := DecodeAXRequest(decodeTestArguments(t, `{"mode":"act","actions":[{"type":"wait","window":"A"}]}`))
	if err == nil || !strings.HasPrefix(err.Message, "type must be one of click, type, keypress, set_value, select, scroll, activate, close;") ||
		strings.Contains(err.Hint, "wait") || err.path != "actions[0].type" {
		t.Fatalf("wait step: %#v", err)
	}
	// A rejected wait points to a fresh read, never to an input action
	// guessed from its fields.
	for _, step := range []string{
		`{"type":"wait","text":"Saved"}`,
		`{"type":"wait","ref":"r1","gone":true}`,
		`{"type":"wait","ref":"w1"}`,
		`{"type":"wait","keys":["ENTER"]}`,
	} {
		_, err = DecodeAXRequest(decodeTestArguments(t, `{"mode":"act","actions":[`+step+`]}`))
		if err == nil || err.Hint != axMinimalCalls[contract.AXModeObserve] {
			t.Fatalf("%s: rejected wait got %#v", step, err)
		}
	}
	_, err = DecodeAXRequest(decodeTestArguments(t, `{"mode":"act","actions":[{"action":"wait","text":"Saved"}]}`))
	if err == nil || err.Hint != axMinimalCalls[contract.AXModeObserve] {
		t.Fatalf("action=wait: %#v", err)
	}
	if text := (&axNoTreeWindowError{ref: "w2"}).Error(); strings.Contains(text, "wait") {
		t.Fatalf("no-tree window text: %s", text)
	}
	_, err = DecodeAXRequest(decodeTestArguments(t,
		`{"mode":"act","actions":[{"type":"close","ref":"w1"},{"type":"keypress","keys":["ENTER"]}]}`))
	if err == nil || err.rule != contract.ArgumentRuleCloseNotLast || strings.Contains(err.Message+err.Hint, "wait") {
		t.Fatalf("input after close: %#v", err)
	}
	_, err = DecodeAXRequest(decodeTestArguments(t, `{"mode":"act","actions":[{"type":"click","ref":"r1","until":{"type":"exists","ref":"r2"}}]}`))
	if err == nil || strings.Contains(err.Message+err.Hint, "wait") {
		t.Fatalf("removed until field: %#v", err)
	}
	if strings.Contains(strings.Join(continuityNotesForUnchanged(t), "\n"), "wait step") {
		t.Fatal("the unchanged-snapshot note still suggests a wait step")
	}
}

// continuityNotesForUnchanged returns the notes for a snapshot identical to
// the delivered one.
func continuityNotesForUnchanged(t *testing.T) []string {
	t.Helper()
	previous := &AppSnapshot{SnapshotID: "s1", WindowHandle: "h1", WindowTitle: "T"}
	current := &AppSnapshot{SnapshotID: "s1", WindowHandle: "h1", WindowTitle: "T"}
	return continuityNotes(previous, current)
}

// modelText is the text a model reads of an AX result.
func modelText(result ToolCallResult) string {
	texts := []string{}
	for _, item := range FinalizeForModel(contract.PublicToolAX, result, false).Content {
		if item.Type == "text" {
			texts = append(texts, item.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// reportActUnder observes the report fixture, sets COMPUTER_USE_ABLATE to
// set, and runs one act whose input applies change.
func reportActUnder(t *testing.T, set string, change func(*reportBackend), act func(map[string]string) map[string]any, names ...string) (*Service, map[string]string, ToolCallResult) {
	t.Helper()
	s, backend := reportService(t, reportMain())
	_, refs := reportObserve(t, s, names...)
	t.Setenv(contract.AblationEnv, set)
	backend.onAction = change
	return s, refs, s.callTool(contract.PublicToolAX, act(refs))
}

func clickOn(name string, extra map[string]any) func(map[string]string) map[string]any {
	return func(refs map[string]string) map[string]any {
		args := map[string]any{"mode": "act", "actions": []any{map[string]any{"type": "click", "ref": refs[name]}}}
		for key, value := range extra {
			args[key] = value
		}
		return args
	}
}

// The full HCU must stay as it was: the act reports the effect, the windows
// and a diff, and observe answers an unchanged tree by reference.
func TestFullHCUActOutputIsUnchanged(t *testing.T) {
	s, _, result := reportActUnder(t, "", func(b *reportBackend) { b.checked = true },
		clickOn("Interface", nil), "Interface")
	want := `{"ok":true,"input_emitted":true,"completed":1,"requested":1,"final_observation_kind":"diff",` +
		`"final_observation_returned":true,"next_identical_observation_required":false,"observation_required":false}` + "\n" +
		`effect: r1 checkbox "Interface" checked=false→true (only state flags changed in this window)` + "\n" +
		"windows: no change\n\n" +
		"app: \"Editor\"\nwindow: \"Report\" [ref=w1]\nax_diff:\n" +
		` - <changed> checkbox "Interface" [checked=true] [ref=r1]` + "\n"
	if got := modelText(result); got != want {
		t.Fatalf("full act:\n%s\nwant:\n%s", got, want)
	}
	observed := modelText(s.callTool(contract.PublicToolAX, map[string]any{"mode": "observe", "app": "Editor"}))
	if !strings.Contains(observed, "Note: [state] semantic_state=unchanged") ||
		!strings.Contains(observed, "Tree unchanged: identical to the state of this window already "+
			"delivered by an earlier result (last full tree plus later diffs)") {
		t.Fatalf("full observe:\n%s", observed)
	}
}

// Under diff an act returns its evidence and the whole current tree of its
// window, as observe "tree" does, without the report of what changed.
func TestDiffAblationActReturnsTheWholeTree(t *testing.T) {
	check := func(change func(*reportBackend)) (string, string) {
		_, _, full := reportActUnder(t, "", change, clickOn("Interface", map[string]any{"observe": "tree"}), "Interface")
		s, _, ablated := reportActUnder(t, contract.AblateDiff, change, clickOn("Interface", nil), "Interface")
		text := modelText(ablated)
		for _, removed := range []string{"effect:", "windows: no change", "ax_diff", "semantic_state", "visual_change", "ax_change"} {
			if strings.Contains(text, removed) {
				t.Fatalf("act under diff still has %q:\n%s", removed, text)
			}
		}
		// The internally decided evidence is that of the full HCU.
		for _, key := range []string{"target_effect_observed", "effect_unknown", "final_observation_kind"} {
			if full.StructuredContent[key] != ablated.StructuredContent[key] {
				t.Fatalf("%s: full %v, diff %v", key, full.StructuredContent[key], ablated.StructuredContent[key])
			}
		}
		// The tree was delivered, so a later observe returns it again,
		// never "unchanged".
		observed := modelText(s.callTool(contract.PublicToolAX, map[string]any{"mode": "observe", "app": "Editor"}))
		if strings.Contains(observed, "Tree unchanged") || strings.Contains(observed, "semantic_state") ||
			!strings.Contains(observed, `checkbox "Interface"`) {
			t.Fatalf("observe under diff:\n%s", observed)
		}
		return text, modelText(full)
	}
	text, full := check(func(b *reportBackend) { b.checked = true })
	tree := text[strings.Index(text, "app: "):]
	if !strings.HasPrefix(text, `{"ok":true,"input_emitted":true,"completed":1,"requested":1,"final_observation_kind":"tree"`) ||
		!strings.Contains(tree, `checkbox "Interface" [checked=true]`) || !strings.HasSuffix(full, tree) {
		t.Fatalf("act under diff:\n%s\nfull with observe tree:\n%s", text, full)
	}
	check(func(*reportBackend) {})

	_, err := DecodeAXRequest(map[string]any{"mode": "act", "observe": "tree",
		"actions": []any{map[string]any{"type": "click", "ref": "r1"}}})
	if err == nil || err.path != "observe" {
		t.Fatalf("act with observe: %#v", err)
	}
}

// A window the act opened appears with its wN in the tree's window list.
func TestDiffAblationTreeListsAnOpenedWindow(t *testing.T) {
	_, _, result := reportActUnder(t, contract.AblateDiff, func(b *reportBackend) {
		b.windows[0].active = false
		b.windows = append(b.windows, reportWindow{
			handle: reportDialogHandle, role: "dialog", title: "Insert Sheet", path: "/window/2", active: true,
		})
	}, clickOn("OK", nil), "OK")
	text := modelText(result)
	if !regexp.MustCompile(`(?m)^  - dialog "Insert Sheet" \[ref=w[0-9]+\]$`).MatchString(text) ||
		strings.Contains(text, "opened") {
		t.Fatalf("act under diff:\n%s", text)
	}
}

// Error texts belong to other mechanisms (waits, postconditions, recovery)
// and stay as in the full HCU; only the report of what changed goes.
func TestDiffAblationErrorsKeepTheirMessages(t *testing.T) {
	s, _ := reportService(t, reportMain())
	reportObserve(t, s, "Interface")
	context := &AXBatchContext{AppToken: "Editor"}
	message, hint, current := s.axBatchContinuityRecovery(context)
	t.Setenv(contract.AblationEnv, contract.AblateDiff)
	if m, h, c := s.axBatchContinuityRecovery(context); m != message || h != hint || c != current {
		t.Fatalf("recovery under diff: %q %q %q, full: %q %q %q", m, h, c, message, hint, current)
	}
	t.Setenv(contract.AblationEnv, "")

	evidence := func(result ToolCallResult) map[string]any {
		t.Helper()
		var line map[string]any
		if err := json.Unmarshal([]byte(strings.SplitN(modelText(result), "\n", 2)[0]), &line); err != nil {
			t.Fatal(err)
		}
		return line
	}
	waitAct := func(refs map[string]string) map[string]any {
		return map[string]any{"mode": "act", "actions": []any{
			map[string]any{"type": "click", "ref": refs["Interface"]},
			map[string]any{"type": "wait", "ref": refs["Size"], "value": "99", "timeout_ms": 200},
		}}
	}
	withTree := func(act func(map[string]string) map[string]any) func(map[string]string) map[string]any {
		return func(refs map[string]string) map[string]any {
			args := act(refs)
			args["observe"] = "tree"
			return args
		}
	}
	_, _, full := reportActUnder(t, "", nil, withTree(waitAct), "Interface", "Size")
	_, _, ablated := reportActUnder(t, contract.AblateDiff, nil, waitAct, "Interface", "Size")
	if evidence(ablated)["code"] != contract.AXCodeWaitTimeout ||
		fmt.Sprint(evidence(ablated)) != fmt.Sprint(evidence(full)) ||
		!strings.Contains(fmt.Sprint(evidence(ablated)["message"]), `value is "12"`) ||
		!strings.Contains(modelText(ablated), `textbox "Size"`) || strings.Contains(modelText(ablated), "effect:") {
		t.Fatalf("wait timeout under diff:\n%s\nfull:\n%s", modelText(ablated), modelText(full))
	}

	lose := func(b *reportBackend) { b.loseReadback = true }
	_, _, full = reportActUnder(t, "", lose, clickOn("Interface", map[string]any{"observe": "tree"}), "Interface")
	_, _, ablated = reportActUnder(t, contract.AblateDiff, lose, clickOn("Interface", nil), "Interface")
	if line := evidence(ablated); line["code"] != "effect_unknown" ||
		fmt.Sprint(line) != fmt.Sprint(evidence(full)) ||
		!strings.Contains(fmt.Sprint(line["message"]), "post_action_observation_failed") ||
		!strings.Contains(modelText(ablated), "A fresh current-state observation follows") ||
		!strings.Contains(modelText(ablated), `checkbox "Interface"`) || strings.Contains(modelText(ablated), "effect:") {
		t.Fatalf("lost readback under diff:\n%s\nfull:\n%s", modelText(ablated), modelText(full))
	}
}

func TestDiffAblationKeepsTheErrorOfAFailedAct(t *testing.T) {
	stale := func(map[string]string) map[string]any {
		return map[string]any{"mode": "act", "actions": []any{map[string]any{"type": "click", "ref": "r999"}}}
	}
	_, _, full := reportActUnder(t, "", nil, stale, "Interface")
	_, _, ablated := reportActUnder(t, contract.AblateDiff, nil, stale, "Interface")
	if !ablated.IsError || modelText(ablated) != modelText(full) {
		t.Fatalf("rejected act under diff:\n%s\nfull:\n%s", modelText(ablated), modelText(full))
	}
}

// Notes that compare with an earlier read go; notes on the current state stay.
func TestDiffAblationDropsChangeNotes(t *testing.T) {
	notes := []string{
		"[diagnostic snapshot_unchanged] same tree", "[pixels] persistent_change=false",
		"No accessibility change observed: window title, tree, focus and selection match.",
		"[text-attrs] Document text formatting changed", `[select] selected 3 chars in 1 elements, from "a" to "c"`,
		"[diagnostic snapshot_busy] busy", "[expect] r2 value is \"12\"",
		"The accessibility tree structure is unchanged, while accessibility text attributes changed (see the [text-attrs] note above).",
		"[transition] A bounded no-input probe observed the transient window finish and returned the current application window.",
		"[identity] The new top-level window was refreshed once without input; the current wN/rN refs below are actionable.",
		"The set of top-level windows changed but no window took focus within 0.8s. The snapshot below may show a different window.",
	}
	full := modelFacingAXNotes(notes)
	t.Setenv(contract.AblationEnv, contract.AblateDiff)
	got := modelFacingAXNotes(notes)
	want := []string{"[state] busy=true recovery=wait_then_observe", "[expect] r2 value is \"12\""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("notes under diff %q, full %q", got, full)
	}
	if len(full) <= len(got) {
		t.Fatalf("full notes %q", full)
	}
}

// The whole tree an act returns under diff is read with the same post-action
// stabilization as a diff: a value that arrives late is in it.
func TestDiffAblationReadsTheTreeAfterLateContent(t *testing.T) {
	for _, set := range []string{"", contract.AblateDiff} {
		backend := &recordingPlatformBackend{
			accessibilityResponses: []ProviderAccessibilityResult{
				{OK: true, Snapshot: contentStabilizationSnapshot("v1", "Ready")},
				{OK: true, Snapshot: contentStabilizationSnapshot("v1", "Ready")},
				{OK: true, Snapshot: contentStabilizationSnapshot("v2", "Saved")},
			},
		}
		service := NewServiceWithBackend(backend)
		off := false
		service.getAppState("Editor", nil, nil, nil, nil, &off, nil, contract.TreeViewWorking, "", nil)
		t.Setenv(contract.AblationEnv, set)
		obs := axActObservation(observation{mode: contract.ObserveDiff})
		result := service.actionResult("Editor", AXServiceRequest{
			App: "Editor", Action: axKeypressCommand{Key: "Tab"},
			Observe: obs.resolvedMode(), IncludeScreenshot: &off, TreeView: contract.TreeViewWorking,
		})
		if result.StructuredContent["content_stabilized"] != true || !strings.Contains(ResultText(result), `"Saved"`) {
			t.Fatalf("ablate=%q read the transient state: %v\n%s", set,
				result.StructuredContent["content_stabilized"], ResultText(result))
		}
	}
}

// A verified close returns the current state, the absent window, not a diff.
func TestDiffAblationClosedWindowIsCurrentState(t *testing.T) {
	context := &AXBatchContext{WindowRef: "w3"}
	full := ResultText(closedWindowFinalObservation(context, observation{mode: contract.ObserveTree}))
	t.Setenv(contract.AblationEnv, contract.AblateDiff)
	ablated := ResultText(closedWindowFinalObservation(context, observation{mode: contract.ObserveTree}))
	if !strings.HasPrefix(full, "ax_diff:") || ablated != "window: null\nabsent_refs: [w3]" {
		t.Fatalf("closed window under diff %q, full %q", ablated, full)
	}
}

func TestGuardAblationLogsTheDecisionAndLetsInputThrough(t *testing.T) {
	log := filepath.Join(t.TempDir(), "logs", "guard.jsonl")
	t.Setenv(contract.GuardLogEnv, log)
	if shadowGuard("ax", "batch_window", "changed", nil) {
		t.Fatal("without the ablation the guard must stop the input")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("a guard log was written without the ablation")
	}
	t.Setenv(contract.AblationEnv, contract.AblateGuard)
	if !shadowGuard("ax", "batch_window", "changed", map[string]any{"window_ref": "w1"}) {
		t.Fatal("with the ablation the guard must let the input through")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["channel"] != "ax" || record["guard"] != "batch_window" || record["window_ref"] != "w1" ||
		record["would_block"] != true || record["at"] == nil {
		t.Fatalf("record %v", record)
	}
}
