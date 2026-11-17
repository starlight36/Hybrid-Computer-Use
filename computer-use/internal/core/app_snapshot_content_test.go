package core

import (
	"reflect"
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

func contentRootSnapshot() *AppSnapshot {
	backend := &reportBackend{windows: []reportWindow{reportMain()}}
	snapshot := backend.snapshot(reportMain())
	snapshot.Elements = snapshot.Elements[:1]
	snapshot.TreeLines = snapshot.TreeLines[:1]
	snapshot.SnapshotID = "root-only"
	snapshot.projection = snapshotProjection{View: contract.TreeViewWorking, Scope: contract.AXScopeWindow}
	return snapshot
}

func TestAppSnapshotContentDiagnosisRequiresWholeWindowRoot(test *testing.T) {
	for _, role := range []string{"frame", "window", "dialog", "alert"} {
		test.Run(role, func(test *testing.T) {
			snapshot := contentRootSnapshot()
			snapshot.Elements[0].ControlType = role
			if snapshot.contentUnavailableNote() == "" {
				test.Fatal("complete root-only window has no content diagnostic")
			}
		})
	}
	var absent *AppSnapshot
	if absent.contentUnavailableNote() != "" {
		test.Fatal("nil snapshot was diagnosed")
	}
	for _, scenario := range []struct {
		name   string
		change func(*AppSnapshot)
	}{
		{"static descendant", func(snapshot *AppSnapshot) {
			snapshot.Elements = append(snapshot.Elements, ElementRecord{
				Index: 1, RuntimeID: []int{0, 0}, ControlType: "label", Name: "Saved successfully",
			})
		}},
		{"anonymous descendant", func(snapshot *AppSnapshot) {
			snapshot.Elements = append(snapshot.Elements, ElementRecord{
				Index: 1, RuntimeID: []int{0, 0}, ControlType: "panel",
			})
		}},
		{"static root", func(snapshot *AppSnapshot) { snapshot.Elements[0].ControlType = "text" }},
		{"document root", func(snapshot *AppSnapshot) { snapshot.Elements[0].ControlType = "document web" }},
		{"root value", func(snapshot *AppSnapshot) { snapshot.Elements[0].Value = "Saved successfully" }},
		{"root description", func(snapshot *AppSnapshot) { snapshot.Elements[0].Description = "Status details" }},
		{"root placeholder", func(snapshot *AppSnapshot) { snapshot.Elements[0].Placeholder = "Search" }},
		{"selected text", func(snapshot *AppSnapshot) { snapshot.SelectedText = "Selected content" }},
		{"missing root", func(snapshot *AppSnapshot) { snapshot.Elements = nil }},
		{"window absent", func(snapshot *AppSnapshot) { snapshot.WindowAbsent = true }},
		{"no window identity", func(snapshot *AppSnapshot) { snapshot.WindowHandle = "" }},
		{"weak window identity", func(snapshot *AppSnapshot) { snapshot.WindowHandle = "weak:pid10/win0/frame" }},
		{"delivery only", func(snapshot *AppSnapshot) { snapshot.deliveryOnly = true }},
		{"light readback", func(snapshot *AppSnapshot) { snapshot.lightReadback = true }},
		{"find candidates", func(snapshot *AppSnapshot) { snapshot.FindCandidatesOnly = true }},
		{"find continuation", func(snapshot *AppSnapshot) { snapshot.FindResume = []int{0, 1} }},
		{"content scope", func(snapshot *AppSnapshot) { snapshot.Scope = contract.AXScopeContent }},
		{"content projection", func(snapshot *AppSnapshot) { snapshot.projection.Scope = contract.AXScopeContent }},
		{"rooted observation", func(snapshot *AppSnapshot) { snapshot.observationRoot = &snapshot.Elements[0] }},
		{"rooted projection", func(snapshot *AppSnapshot) { snapshot.projection.Root = `{"focused":true}` }},
		{"descendant-only record", func(snapshot *AppSnapshot) { snapshot.Elements[0].RuntimeID = []int{0, 1} }},
		{"truncated root text", func(snapshot *AppSnapshot) { snapshot.Elements[0].TextTruncated = []string{"name"} }},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			snapshot := contentRootSnapshot()
			scenario.change(snapshot)
			if note := snapshot.contentUnavailableNote(); note != "" {
				test.Fatalf("partial or content-bearing snapshot was diagnosed: %s", note)
			}
			snapshot.Annotations = []axProviderAnnotation{{Kind: "provider_omission", Count: 10, Reason: "filtered", ParentRuntimeID: []int{0}}}
			if note := snapshot.contentUnavailableNote(); note != "" {
				test.Fatalf("coverage qualification bypassed the diagnosis exclusion: %s", note)
			}
		})
	}
}

func TestAppSnapshotContentDiagnosisQualifiesCoverageGaps(test *testing.T) {
	for _, reason := range []string{
		"filtered", "pressure", "collapsed_menu", "provider_children_not_rendered", "modal_dialog",
		"time", "chars", "budget", "depth", "render_error", "fanout", "outside_view",
		"provider_probe_error", "null_reference", "filtered,depth", "unknown_reason", "",
	} {
		test.Run(reason, func(test *testing.T) {
			snapshot := contentRootSnapshot()
			snapshot.Annotations = []axProviderAnnotation{{Kind: "provider_omission", Count: 1, Reason: reason}}
			note := snapshot.contentUnavailableNote()
			if !strings.Contains(note, "AX content unavailable in this snapshot:") ||
				!strings.Contains(note, "This read has coverage gaps; omitted content may still exist.") {
				test.Fatalf("root-only snapshot lost its qualified diagnosis: %s", note)
			}
		})
	}
}

func TestAppSnapshotContentDiagnosisPreservesCoverage(test *testing.T) {
	for _, scenario := range []struct {
		reason, evidenceKey string
	}{
		{"filtered", "projection_hidden_nodes"},
		{"collapsed_menu", "expandable_hidden_nodes"},
		{"provider_probe_error", "coverage_incomplete_nodes"},
		{"null_reference", "coverage_incomplete_nodes"},
	} {
		test.Run(scenario.reason, func(test *testing.T) {
			service := NewService()
			snapshot := contentRootSnapshot()
			original := []axProviderAnnotation{{
				Kind: "provider_omission", Count: 10, Reason: scenario.reason, ParentRuntimeID: []int{0},
			}}
			snapshot.Annotations = append([]axProviderAnnotation(nil), original...)
			if err := service.BuildAndRenderAXSnapshot(snapshot, contract.TreeViewWorking); err != nil {
				test.Fatal(err)
			}
			providerNote := "The accessibility read omitted content."
			result := service.getAppStateResult(snapshot, []string{providerNote}, false)
			text := ResultText(result)
			for _, required := range []string{
				"AX content unavailable in this snapshot: only the top-level window root was returned.",
				"This read has coverage gaps; omitted content may still exist.",
				"Visible emptiness, renderer crash, and navigation outcome are unknown.",
				providerNote,
			} {
				if !strings.Contains(text, required) {
					test.Fatalf("whole-window observation lost %q:\n%s", required, text)
				}
			}
			if result.IsError || result.StructuredContent[scenario.evidenceKey] != 10 ||
				!reflect.DeepEqual(snapshot.Annotations, original) {
				test.Fatalf("diagnosis changed provider coverage or result: %#v annotations=%#v",
					result, snapshot.Annotations)
			}
			if scenario.evidenceKey == "coverage_incomplete_nodes" {
				public := PrepareAXEvidence(EnforceAXResponseBudget(WithAXModeEvidence(result, contract.AXModeObserve)))
				if public.StructuredContent["coverage"] != "partial" || public.StructuredContent["tree_complete"] != false {
					test.Fatalf("incomplete acquisition was presented as complete: %#v", public.StructuredContent)
				}
			}
		})
	}
}

func TestAppSnapshotContentDiagnosisSurvivesTreeReference(test *testing.T) {
	service := NewService()
	snapshot := contentRootSnapshot()
	if err := service.BuildAndRenderAXSnapshot(snapshot, contract.TreeViewWorking); err != nil {
		test.Fatal(err)
	}
	first := service.getAppStateResult(snapshot, nil, false)
	text := ResultText(first)
	for _, required := range []string{
		"AX content unavailable in this snapshot:",
		"Visible emptiness, renderer crash, and navigation outcome are unknown",
		"Inspect a screenshot", "Verify the outcome of any prior delete or submit before repeating it",
		"If the app supports reloading and it is safe, reload, then observe again",
	} {
		if !strings.Contains(text, required) {
			test.Fatalf("missing diagnosis or recovery guidance %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "coverage gaps") || strings.Contains(text, "omitted content may still exist") {
		test.Fatalf("complete root-only snapshot acquired a coverage qualifier:\n%s", text)
	}
	if strings.Index(text, "AX content unavailable") < strings.Index(text, "window:") ||
		strings.Index(text, "AX content unavailable") > strings.Index(text, "ax:") {
		test.Fatalf("diagnosis is not between window identity and tree:\n%s", text)
	}
	repeated := service.getAppStateResult(snapshot, nil, false)
	if repeated.StructuredContent["tree_reference_only"] != true ||
		!strings.Contains(ResultText(repeated), "AX content unavailable in this snapshot:") {
		test.Fatalf("tree reference lost current content diagnosis: %#v\n%s", repeated.StructuredContent, ResultText(repeated))
	}
	if first.IsError || repeated.IsError {
		test.Fatal("content diagnosis changed successful observation into an error")
	}
}

func TestAppSnapshotContentDiagnosisSurvivesDiff(test *testing.T) {
	for _, scenario := range []struct {
		name           string
		withContent    bool
		wantKind       string
		coverageReason string
	}{
		{"unchanged root", false, "diff", ""},
		{"content disappears", true, "working_tree", ""},
		{"filtered root", false, "diff", "filtered"},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			service := NewService()
			before := contentRootSnapshot()
			if scenario.withContent {
				backend := &reportBackend{windows: []reportWindow{reportMain()}}
				before = backend.snapshot(reportMain())
				before.projection = snapshotProjection{View: contract.TreeViewWorking, Scope: contract.AXScopeWindow}
			}
			if err := service.BuildAndRenderAXSnapshot(before, contract.TreeViewWorking); err != nil {
				test.Fatal(err)
			}
			service.getAppStateResult(before, nil, false)
			after := contentRootSnapshot()
			if scenario.coverageReason != "" {
				after.Annotations = []axProviderAnnotation{{
					Kind: "provider_omission", Count: 10, Reason: scenario.coverageReason, ParentRuntimeID: []int{0},
				}}
			}
			if err := service.BuildAndRenderAXSnapshot(after, contract.TreeViewWorking); err != nil {
				test.Fatal(err)
			}
			result := service.renderAXBatchFinalObservation(after, nil, observation{mode: contract.ObserveDiff})
			if result.IsError || result.StructuredContent["final_observation_kind"] != scenario.wantKind ||
				!strings.Contains(ResultText(result), "AX content unavailable in this snapshot:") {
				test.Fatalf("diff observation lost current content diagnosis: %#v\n%s", result.StructuredContent, ResultText(result))
			}
			if strings.Contains(ResultText(result), "This read has coverage gaps;") != (scenario.coverageReason != "") {
				test.Fatalf("diff changed the coverage qualification: %s", ResultText(result))
			}
		})
	}
}

func TestAppSnapshotContentDiagnosisPreservesActionUncertainty(test *testing.T) {
	service := NewService()
	backend := &recordingPlatformBackend{accessibilityResponse: &ProviderAccessibilityResult{
		OK: true, Snapshot: contentRootSnapshot(),
	}}
	service.Backend = backend
	window, _ := reportObserve(test, service)
	backend.accessibilityResponse = &ProviderAccessibilityResult{
		OK: true, Snapshot: contentRootSnapshot(),
		Mutation:       &ProviderMutationFacts{InputEventsEmitted: true, DesktopStateMutated: true},
		ActionEvidence: &ProviderActionEvidence{InputEmitted: true, Route: "synthesis"},
	}
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "window_ref": window, "observe": "tree", "actions": keys("Delete"),
	})
	if result.IsError {
		test.Fatalf("root-only action failed: %s", ResultText(result))
	}
	public := WithAXTextEvidence(PrepareAXEvidence(result))
	text := ResultText(public)
	if !strings.Contains(text, `"effect_unknown":true`) ||
		!strings.Contains(text, "AX content unavailable in this snapshot:") {
		test.Fatalf("action lost uncertainty or content diagnosis:\n%s", text)
	}
	if len(backend.actionRequests) != 1 || len(backend.observeRequests) != 1 {
		test.Fatalf("diagnosis caused extra actions or observations: actions=%d observations=%d",
			len(backend.actionRequests), len(backend.observeRequests))
	}
}

func TestAppSnapshotContentDiagnosisDoesNotLeakFromLightReadback(test *testing.T) {
	service, backend := rootOnlyService(test, false, reportMain())
	window, _ := reportObserve(test, service)
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "window_ref": window, "observe": "tree", "actions": keys("a", "b", "c", "d"),
	})
	if result.IsError || strings.Contains(ResultText(result), "AX content unavailable in this snapshot:") {
		test.Fatalf("light readback produced a false diagnosis:\n%s", ResultText(result))
	}
	if backend.rootOnlyReads != 2 || backend.fullReads != 2 ||
		len(backend.actionRequests) != 4 || len(backend.observeRequests) != 1 {
		test.Fatalf("read or action counts changed: root=%d full=%d actions=%d observations=%d",
			backend.rootOnlyReads, backend.fullReads, len(backend.actionRequests), len(backend.observeRequests))
	}
}
