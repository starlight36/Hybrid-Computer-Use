package core

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

type axNoTreeActivationBackend struct {
	*axActivationBackend
	listings       int
	issues         []ProviderInventoryIssue
	inventoryError error
	nativeReads    int
}

func (backend *axNoTreeActivationBackend) ObserveApps(request ProviderObserveAppsRequest) (ProviderAccessibilityResult, error) {
	backend.listings++
	if backend.inventoryError != nil {
		return ProviderAccessibilityResult{}, backend.inventoryError
	}
	result, err := backend.axActivationBackend.ObserveApps(request)
	result.InventoryIssues = backend.issues
	for appIndex := range result.Apps {
		for windowIndex := range result.Apps[appIndex].Windows {
			window := &result.Apps[appIndex].Windows[windowIndex]
			window.NoAccessibilityTree = window.Handle == reportMainHandle
		}
	}
	return result, err
}

func (backend *axNoTreeActivationBackend) Observe(request ProviderObserveRequest) (ProviderAccessibilityResult, error) {
	if request.WindowHandle == reportMainHandle {
		backend.nativeReads++
		return ProviderAccessibilityResult{}, fmt.Errorf("native window has no accessibility tree")
	}
	return backend.axActivationBackend.Observe(request)
}

func axNoTreeActivationService(t *testing.T) (*Service, *axNoTreeActivationBackend, axActivationRefs, axActivationRefs) {
	t.Helper()
	service, activation, native, tree := axActivationService(t, true)
	backend := &axNoTreeActivationBackend{axActivationBackend: activation}
	service.Backend = backend
	if listed := service.listApps(); listed.IsError {
		t.Fatalf("list native window: %s", ResultText(listed))
	}
	return service, backend, native, tree
}

func TestAXNoTreeMixedActivationToTree(t *testing.T) {
	for _, byTitle := range []bool{false, true} {
		t.Run(fmt.Sprintf("title_%t", byTitle), func(t *testing.T) {
			service, backend, native, tree := axNoTreeActivationService(t)
			activation := map[string]any{"type": "activate", "ref": tree.window}
			if byTitle {
				activation = map[string]any{"type": "activate", "title": "Notes"}
			}
			result := service.callTool(contract.PublicToolAX, map[string]any{
				"mode": "act", "window_ref": native.window, "observe": "tree", "actions": []any{
					map[string]any{"type": "activate", "ref": native.window},
					activation,
					map[string]any{"type": "type", "text": "untargeted"},
					map[string]any{"type": "type", "ref": tree.entry, "text": "targeted"},
				},
			})
			if result.IsError {
				t.Fatalf("native -> tree rejected: %s", ResultText(result))
			}
			want := []string{"activate@" + reportMainHandle, "activate@" + reportOtherHandle, "type@" + reportOtherHandle, "type@" + reportOtherHandle}
			if !slices.Equal(backend.deliveries, want) {
				t.Errorf("deliveries = %v, want %v", backend.deliveries, want)
			}
			axActivationEvidence(t, result, native.window, tree.window, tree.window, tree.window)
			if backend.nativeReads != 0 || result.StructuredContent["final_observation_returned"] != true || !strings.Contains(ResultText(result), `window: "Notes" [ref=`+tree.window+`]`) {
				t.Errorf("wrong final observation: native reads %d, result %#v\n%s", backend.nativeReads, result.StructuredContent, ResultText(result))
			}
		})
	}
}

func TestAXNoTreeMixedActivationFinalNative(t *testing.T) {
	service, backend, native, tree := axNoTreeActivationService(t)
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "window_ref": tree.window, "observe": "tree", "actions": []any{
			map[string]any{"type": "activate", "ref": tree.window},
			map[string]any{"type": "type", "ref": tree.entry, "text": "before switch"},
			map[string]any{"type": "activate", "title": "Report"},
		},
	})
	if result.IsError {
		t.Fatalf("tree -> native rejected: %s", ResultText(result))
	}
	want := []string{"activate@" + reportOtherHandle, "type@" + reportOtherHandle, "activate@" + reportMainHandle}
	if !slices.Equal(backend.deliveries, want) {
		t.Errorf("deliveries = %v, want %v", backend.deliveries, want)
	}
	axActivationEvidence(t, result, tree.window, tree.window, native.window)
	if backend.nativeReads != 0 || !strings.Contains(ResultText(result), native.window+" "+axNoTreeWindowMark) {
		t.Errorf("native final observation: reads %d, text %s", backend.nativeReads, ResultText(result))
	}
	for key, want := range map[string]any{"input_events_emitted": true, "final_observation_returned": false, "final_observation_kind": "none", "observation_required": false} {
		if result.StructuredContent[key] != want {
			t.Errorf("%s = %v, want %v", key, result.StructuredContent[key], want)
		}
	}
}

func TestAXNoTreeMixedInputRejectedBeforeAnyDelivery(t *testing.T) {
	for _, action := range []map[string]any{
		{"type": "keypress", "keys": []any{"ENTER"}},
		{"type": "type", "text": "must not be sent"},
		{"type": "wait", "text": "not a window wait", "timeout_ms": 200},
	} {
		t.Run(fmt.Sprint(action["type"]), func(t *testing.T) {
			service, backend, native, tree := axNoTreeActivationService(t)
			result := service.callTool(contract.PublicToolAX, map[string]any{
				"mode": "act", "actions": []any{
					map[string]any{"type": "activate", "ref": tree.window},
					map[string]any{"type": "activate", "ref": native.window},
					action,
					map[string]any{"type": "activate", "ref": tree.window},
				},
			})
			if !result.IsError || result.StructuredContent[contract.AXPublicErrorCodeKey] != contract.AXCodeAppUnavailable || result.StructuredContent[contract.AXPublicErrorPathKey] != "actions[2]" || !strings.Contains(ResultText(result), "no accessibility tree") {
				t.Errorf("want native input preflight error, got %#v\n%s", result.StructuredContent, ResultText(result))
			}
			if len(backend.deliveries) != 0 || axEvidenceReportsInputEmission(result.StructuredContent) {
				t.Errorf("invalid batch emitted input: %v, %#v", backend.deliveries, result.StructuredContent)
			}
		})
	}
}

func TestAXNoTreeSegmentValidation(t *testing.T) {
	service, backend, native, tree := axNoTreeActivationService(t)
	allowed := []axBatchAction{
		{actionType: "activate", targetRef: native.window},
		{actionType: "wait", wait: &axWait{window: "Report"}},
		{actionType: "close", targetRef: native.window},
	}
	if err := service.validateAXNoTreeSegment(&AXBatchContext{WindowRef: native.window}, allowed, 4); err != nil {
		t.Fatalf("native window operations rejected: %v", err)
	}
	for _, action := range []axBatchAction{
		{actionType: "keypress", key: "ENTER"},
		{actionType: "type", text: "unsent"},
		{actionType: "click", targetRef: tree.button},
		{actionType: "scroll", direction: "down"},
		{actionType: "set_value", targetRef: tree.entry, value: "unsent"},
		{actionType: "select", targetRef: tree.entry, toRef: tree.entry},
		{actionType: "wait"},
		{actionType: "wait", wait: &axWait{ref: tree.entry}},
		{actionType: "wait", wait: &axWait{query: map[string]any{"role": "button"}}},
		{actionType: "wait", wait: &axWait{settle: true}},
	} {
		actions := []axBatchAction{allowed[0], action}
		err := service.validateAXNoTreeSegment(&AXBatchContext{WindowRef: native.window}, actions, 4)
		var argument *axArgError
		if !errors.As(err, &argument) || argument.Code != contract.AXCodeAppUnavailable || argument.path != "actions[5]" || argument.Hint != GUIScreenshotCall {
			t.Errorf("%s rejection = %v", action.actionType, err)
		}
		if err := service.validateAXNoTreeSegment(&AXBatchContext{WindowRef: tree.window}, actions, 4); err != nil {
			t.Errorf("tree segment rejected by native validation: %v", err)
		}
	}
	if len(backend.deliveries) != 0 || backend.nativeReads != 0 {
		t.Errorf("segment validation reached input/tree: %v, %d", backend.deliveries, backend.nativeReads)
	}
}

func TestAXNoTreeBatchWindowContinuityUsesFreshStrongHandle(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		mutate func(*axNoTreeActivationBackend)
		want   axWindowContinuity
	}{
		{name: "same", want: axWindowSame},
		{name: "renamed", want: axWindowSame, mutate: func(backend *axNoTreeActivationBackend) { backend.windows[0].title = "Renamed" }},
		{name: "other_window_focused", want: axWindowSame, mutate: func(backend *axNoTreeActivationBackend) { backend.windows[1].active = true }},
		{name: "gone", want: axWindowChanged, mutate: func(backend *axNoTreeActivationBackend) { backend.windows = backend.windows[1:] }},
		{name: "same_title_new_handle", want: axWindowChanged, mutate: func(backend *axNoTreeActivationBackend) {
			backend.owners[testHandleScene] = backend.owners[reportMainHandle]
			backend.windows[0].handle = testHandleScene
		}},
		{name: "incomplete_present", want: axWindowUnknown, mutate: func(backend *axNoTreeActivationBackend) {
			backend.issues = []ProviderInventoryIssue{{Reason: "enumeration incomplete"}}
		}},
		{name: "incomplete_absent", want: axWindowUnknown, mutate: func(backend *axNoTreeActivationBackend) {
			backend.windows = backend.windows[1:]
			backend.issues = []ProviderInventoryIssue{{Reason: "enumeration incomplete"}}
		}},
		{name: "failed", want: axWindowUnknown, mutate: func(backend *axNoTreeActivationBackend) { backend.inventoryError = errors.New("inventory unavailable") }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, backend, native, _ := axNoTreeActivationService(t)
			if scenario.mutate != nil {
				scenario.mutate(backend)
			}
			before := backend.listings
			state, handled := service.axNoTreeBatchWindowContinuity(&AXBatchContext{WindowRef: native.window, WindowHandle: reportMainHandle})
			if !handled || state != scenario.want || backend.listings != before+1 {
				t.Errorf("continuity = %s/%t, listings %d -> %d; want %s and one fresh inventory", state, handled, before, backend.listings, scenario.want)
			}
			if backend.nativeReads != 0 || len(backend.deliveries) != 0 {
				t.Errorf("continuity read native tree or sent input: %d, %v", backend.nativeReads, backend.deliveries)
			}
		})
	}
	service, backend, native, tree := axNoTreeActivationService(t)
	before := backend.listings
	if _, handled := service.axNoTreeBatchWindowContinuity(&AXBatchContext{WindowRef: tree.window, WindowHandle: reportOtherHandle}); handled || backend.listings != before {
		t.Errorf("ordinary tree continuity intercepted: %t, listings %d -> %d", handled, before, backend.listings)
	}
	state, handled := service.axNoTreeBatchWindowContinuity(&AXBatchContext{WindowRef: native.window})
	if !handled || state != axWindowUnknown || backend.listings != before {
		t.Errorf("missing strong handle should stay unknown without enumeration: %s/%t", state, handled)
	}
}

func TestAXNoTreeBatchActionAndObservationEvidence(t *testing.T) {
	for _, action := range []string{"activate", "close"} {
		t.Run(action, func(t *testing.T) {
			service, backend := windowScriptService(sceneApps())
			window := service.noTreeWindowEntry(noTreeWindowRef(t, service))
			backend.windowResponse = &ProviderWindowResult{OK: true}
			result := service.axNoTreeBatchAction(action, window)
			if result.IsError || !slices.Equal(backend.calls, []string{"window_" + action}) || !strings.Contains(ResultText(result), axNoTreeWindowMark) {
				t.Fatalf("native action failed: %v, %s", backend.calls, ResultText(result))
			}
			for key, want := range map[string]any{"input_events_emitted": true, "final_observation_returned": false, "final_observation_kind": "none", "observation_required": false} {
				if result.StructuredContent[key] != want {
					t.Errorf("%s = %v, want %v", key, result.StructuredContent[key], want)
				}
			}
			observation := axNoTreeBatchObservation(window)
			if observation.IsError || observation.StructuredContent["input_events_emitted"] != false || len(backend.calls) != 1 {
				t.Errorf("native final observation emitted input: %#v, %v", observation, backend.calls)
			}
		})
	}
}

func TestAXNoTreeBatchActionPreservesProviderFailure(t *testing.T) {
	for _, emitted := range []bool{false, true} {
		t.Run(fmt.Sprintf("emitted_%t", emitted), func(t *testing.T) {
			service, backend := windowScriptService(sceneApps())
			window := service.noTreeWindowEntry(noTreeWindowRef(t, service))
			backend.windowResponse = &ProviderWindowResult{
				Error:    "provider could not confirm activation",
				Failure:  &ProviderFailure{Code: "input_failed", Phase: "emit", FactsComplete: true},
				Mutation: &ProviderMutationFacts{InputEventsEmitted: emitted},
			}
			want := service.axNoTreeWindowCommand("activate", window)
			got := service.axNoTreeBatchAction("activate", window)
			if !got.IsError || !reflect.DeepEqual(got, want) || got.StructuredContent["input_events_emitted"] != emitted {
				t.Errorf("failure facts changed: got %#v, want %#v", got, want)
			}
		})
	}
}

func TestAXNoTreeBatchKeepsNativeWindowBinding(t *testing.T) {
	for _, wrongClose := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrong_close_%t", wrongClose), func(t *testing.T) {
			apps := sceneApps()
			nativeApp := &apps[len(apps)-1]
			second := nativeApp.Windows[0]
			second.Handle = testHandleNotes
			second.Title = "Render - Paint3D"
			nativeApp.Windows = append(nativeApp.Windows, second)
			service, backend := windowScriptService(apps)
			backend.windowResponse = &ProviderWindowResult{OK: true}
			firstRef := noTreeWindowRef(t, service)
			inventory, failure := service.listDesktopWindows(false)
			if inventory == nil {
				t.Fatalf("list native windows: %s", ResultText(failure))
			}
			secondRef := ""
			for _, window := range inventory.windows {
				if window.handle == second.Handle {
					secondRef = window.ref
				}
			}
			if secondRef == "" {
				t.Fatal("missing second native window")
			}
			closeRef := secondRef
			if wrongClose {
				closeRef = firstRef
			}
			result := service.callTool(contract.PublicToolAX, map[string]any{
				"mode": "act", "window_ref": firstRef, "observe": "tree", "actions": []any{
					map[string]any{"type": "activate", "ref": firstRef},
					map[string]any{"type": "activate", "title": "Render"},
					map[string]any{"type": "wait", "window": "Render", "timeout_ms": 200},
					map[string]any{"type": "close", "ref": closeRef},
				},
			})
			if wrongClose {
				if !result.IsError || len(backend.calls) != 0 || axEvidenceReportsInputEmission(result.StructuredContent) || result.StructuredContent[contract.AXPublicErrorCodeKey] != contract.AXCodeInvalidArgument {
					t.Fatalf("close in previous native window was not rejected before input: %v, %#v", backend.calls, result)
				}
				return
			}
			if result.IsError || !slices.Equal(backend.calls, []string{"window_activate", "window_activate", "window_close"}) {
				t.Fatalf("all native batch failed: %v, %s", backend.calls, ResultText(result))
			}
			axActivationEvidence(t, result, firstRef, secondRef, secondRef, secondRef)
			if result.StructuredContent["input_events_emitted"] != true || result.StructuredContent["final_observation_returned"] != false || result.StructuredContent["final_observation_kind"] != "none" || result.StructuredContent["observation_required"] != false {
				t.Errorf("native final evidence changed: %#v", result.StructuredContent)
			}
		})
	}
}
