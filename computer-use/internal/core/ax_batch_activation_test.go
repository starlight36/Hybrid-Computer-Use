package core

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

type axActivationBackend struct {
	recordingPlatformBackend
	windows              []reportWindow
	owners               map[string]AppDescriptor
	windowRequests       []ProviderWindowActionRequest
	deliveries           []string
	failActivate         string
	transitionAfterInput string
	activationModal      *reportWindow
	inputModal           *reportWindow
}

func (backend *axActivationBackend) snapshot(handle string) *ProviderSnapshot {
	owner := backend.owners[handle]
	fixture := &reportBackend{value: "12"}
	var selected reportWindow
	for _, window := range backend.windows {
		if backend.owners[window.handle].Name == owner.Name {
			fixture.windows = append(fixture.windows, window)
		}
		if window.handle == handle {
			selected = window
		}
	}
	snapshot := fixture.snapshot(selected)
	snapshot.App = owner
	for index := range snapshot.Elements {
		element := &snapshot.Elements[index]
		element.ProviderBusName = owner.ProviderBusName
		element.ProcessGeneration = owner.ProcessGeneration
		element.ProviderWindowBusName = owner.ProviderBusName
		element.ProviderWindowProcessGeneration = owner.ProcessGeneration
	}
	for index := range snapshot.Windows {
		window := &snapshot.Windows[index]
		window.ProviderBusName = owner.ProviderBusName
		window.ProviderProcessGeneration = owner.ProcessGeneration
	}
	return snapshot
}

func (backend *axActivationBackend) ObserveApps(ProviderObserveAppsRequest) (ProviderAccessibilityResult, error) {
	apps := []AppDescriptor{}
	for _, window := range backend.windows {
		owner := backend.owners[window.handle]
		index := slices.IndexFunc(apps, func(app AppDescriptor) bool { return app.Name == owner.Name })
		if index < 0 {
			index = len(apps)
			apps = append(apps, owner)
		}
		apps[index].Windows = append(apps[index].Windows, ProviderAppWindow{
			Title: window.title, Role: window.role, Handle: window.handle,
			ProviderBusName: owner.ProviderBusName, ProviderObjectPath: window.path,
			ProcessGeneration: owner.ProcessGeneration, Active: window.active, InputTarget: true,
		})
	}
	return ProviderAccessibilityResult{OK: true, Apps: apps}, nil
}

func (backend *axActivationBackend) Observe(request ProviderObserveRequest) (ProviderAccessibilityResult, error) {
	backend.observeRequests = append(backend.observeRequests, request)
	if _, exists := backend.owners[request.WindowHandle]; !exists {
		return ProviderAccessibilityResult{}, fmt.Errorf("unexpected observation handle %q", request.WindowHandle)
	}
	return ProviderAccessibilityResult{OK: true, Snapshot: backend.snapshot(request.WindowHandle)}, nil
}

func (backend *axActivationBackend) ResolveAndAct(request ProviderActionRequest) (ProviderAccessibilityResult, error) {
	backend.actionRequests = append(backend.actionRequests, request)
	handle := request.WindowHandle
	if request.Target != nil && request.Target.WindowHandle != "" {
		if handle != "" && handle != request.Target.WindowHandle {
			return ProviderAccessibilityResult{}, fmt.Errorf("input and target disagree: %q, %q", handle, request.Target.WindowHandle)
		}
		handle = request.Target.WindowHandle
	}
	backend.deliveries = append(backend.deliveries, string(request.Intent)+"@"+handle)
	if _, exists := backend.owners[handle]; !exists {
		return ProviderAccessibilityResult{}, fmt.Errorf("unexpected input handle %q", handle)
	}
	if backend.transitionAfterInput != "" {
		handle = backend.transitionAfterInput
		backend.transitionAfterInput = ""
	}
	if backend.inputModal != nil {
		modal := *backend.inputModal
		backend.windows = append(backend.windows, modal)
		backend.owners[modal.handle] = backend.owners[handle]
		handle = modal.handle
		backend.inputModal = nil
	}
	return ProviderAccessibilityResult{
		OK: true, Snapshot: backend.snapshot(handle),
		Mutation:       &ProviderMutationFacts{InputEventsEmitted: true, DesktopStateMutated: true},
		ActionEvidence: &ProviderActionEvidence{InputEmitted: true, Route: "semantic_action"},
	}, nil
}

func (backend *axActivationBackend) ActOnWindow(request ProviderWindowActionRequest) (ProviderWindowResult, error) {
	backend.windowRequests = append(backend.windowRequests, request)
	handle := request.WindowHandle
	if request.Target != nil && request.Target.WindowHandle != "" {
		if handle != "" && handle != request.Target.WindowHandle {
			return ProviderWindowResult{}, fmt.Errorf("activation and target disagree: %q, %q", handle, request.Target.WindowHandle)
		}
		handle = request.Target.WindowHandle
	}
	backend.deliveries = append(backend.deliveries, string(request.Action)+"@"+handle)
	if handle == backend.failActivate {
		return ProviderWindowResult{
			OK: false, Error: "activation failed before input",
			Failure:  &ProviderFailure{Code: "input_failed", Phase: "emit", Subject: "window", FactsComplete: true},
			Mutation: &ProviderMutationFacts{},
		}, nil
	}
	if request.Action != "activate" {
		return ProviderWindowResult{}, fmt.Errorf("unexpected window action %q", request.Action)
	}
	if _, exists := backend.owners[handle]; !exists {
		return ProviderWindowResult{}, fmt.Errorf("unexpected activation handle %q", handle)
	}
	if backend.activationModal != nil {
		modal := *backend.activationModal
		backend.windows = append(backend.windows, modal)
		backend.owners[modal.handle] = backend.owners[handle]
		handle = modal.handle
		backend.activationModal = nil
	}
	for index := range backend.windows {
		backend.windows[index].active = backend.windows[index].handle == handle
	}
	return ProviderWindowResult{
		OK: true, Snapshot: backend.snapshot(handle),
		Mutation:       &ProviderMutationFacts{InputEventsEmitted: true, DesktopStateMutated: true},
		ActionEvidence: &ProviderActionEvidence{InputEmitted: true, Route: "native_window_command"},
	}, nil
}

type axActivationRefs struct {
	window string
	button string
	entry  string
}

func axActivationService(t *testing.T, crossApp bool) (*Service, *axActivationBackend, axActivationRefs, axActivationRefs) {
	t.Helper()
	first := AppDescriptor{Name: "Editor", PID: 10, ProviderBusName: ":1.84",
		ProcessGeneration: "pid:10:start:100", InputCapable: true}
	second := first
	if crossApp {
		second = AppDescriptor{Name: "Viewer", PID: 20, ProviderBusName: ":1.85",
			ProcessGeneration: "pid:20:start:200", InputCapable: true}
	}
	backend := &axActivationBackend{
		windows: []reportWindow{
			reportMain(),
			{handle: reportOtherHandle, role: "frame", title: "Notes", path: "/window/2"},
		},
		owners: map[string]AppDescriptor{reportMainHandle: first, reportOtherHandle: second},
	}
	service := NewServiceWithBackend(backend)
	listed := service.callTool(contract.PublicToolAX, map[string]any{"mode": "apps"})
	if listed.IsError {
		t.Fatalf("list fixture windows: %s", ResultText(listed))
	}
	refs := make([]axActivationRefs, 2)
	for index, window := range backend.windows {
		match := regexp.MustCompile(`(?m)^\s*(w[0-9]+)[^\n]*"` + regexp.QuoteMeta(window.title) + `"`).FindStringSubmatch(ResultText(listed))
		if match == nil {
			t.Fatalf("missing window %q in %s", window.title, ResultText(listed))
		}
		refs[index].window = match[1]
		observed := service.callTool(contract.PublicToolAX, map[string]any{
			"mode": "observe", "window_ref": match[1], "observe": "tree",
		})
		if observed.IsError {
			t.Fatalf("observe fixture window: %s", ResultText(observed))
		}
		for name, destination := range map[string]*string{"OK": &refs[index].button, "Size": &refs[index].entry} {
			match := regexp.MustCompile(`"` + name + `"[^\n]*\[ref=(r[0-9]+)\]`).FindStringSubmatch(ResultText(observed))
			if match == nil {
				t.Fatalf("missing element %q in %s", name, ResultText(observed))
			}
			*destination = match[1]
		}
	}
	return service, backend, refs[0], refs[1]
}

func axActivationEvidence(t *testing.T, result ToolCallResult, windows ...string) {
	t.Helper()
	batch := result.StructuredContent
	if nested, exists := batch["a11y_batch_result"]; exists {
		var valid bool
		batch, valid = nested.(map[string]any)
		if !valid {
			t.Fatalf("invalid batch evidence: %#v", nested)
		}
	}
	actions, _ := batch["actions"].([]map[string]any)
	if len(actions) != len(windows) {
		t.Fatalf("action evidence count = %d, want %d: %#v", len(actions), len(windows), batch)
	}
	for index, window := range windows {
		if actions[index]["window_ref"] != window {
			t.Errorf("action %d window evidence = %#v, want %s", index, actions[index], window)
		}
	}
}

func TestAXBatchActivationSwitchesTarget(t *testing.T) {
	for _, scenario := range []struct {
		name                        string
		crossApp, byTitle, explicit bool
	}{
		{name: "same_app_ref"},
		{name: "same_app_title_overrides_window", byTitle: true, explicit: true},
		{name: "cross_app_ref_overrides_window", crossApp: true, explicit: true},
		{name: "cross_app_title", crossApp: true, byTitle: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, backend, first, second := axActivationService(t, scenario.crossApp)
			activation := map[string]any{"type": "activate", "ref": second.window}
			if scenario.byTitle {
				activation = map[string]any{"type": "activate", "title": "Notes"}
			}
			args := map[string]any{"mode": "act", "observe": "tree", "actions": []any{
				map[string]any{"type": "click", "ref": first.button},
				activation,
				map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
				map[string]any{"type": "type", "text": "untargeted"},
				map[string]any{"type": "click", "ref": second.button},
				map[string]any{"type": "keypress", "ref": second.entry, "keys": []any{"TAB"}},
				map[string]any{"type": "type", "ref": second.entry, "text": "targeted"},
			}}
			if scenario.explicit {
				args["window_ref"] = first.window
			}
			result := service.callTool(contract.PublicToolAX, args)
			if result.IsError {
				t.Fatalf("explicit switch rejected: %s", ResultText(result))
			}
			want := []string{"click@" + reportMainHandle, "activate@" + reportOtherHandle,
				"keypress@" + reportOtherHandle, "type@" + reportOtherHandle, "click@" + reportOtherHandle,
				"keypress@" + reportOtherHandle, "type@" + reportOtherHandle}
			if !slices.Equal(backend.deliveries, want) {
				t.Errorf("delivery order/handles = %v, want %v", backend.deliveries, want)
			}
			axActivationEvidence(t, result, first.window, second.window, second.window,
				second.window, second.window, second.window, second.window)
			owner := backend.owners[reportOtherHandle]
			snapshot := service.currentSnapshot(owner.Name)
			if snapshot == nil || snapshot.WindowHandle != reportOtherHandle {
				t.Fatalf("final snapshot does not belong to B: %#v", snapshot)
			}
			if !strings.Contains(ResultText(result), `window: "Notes" [ref=`+second.window+`]`) {
				t.Errorf("final observation does not identify B: %s", ResultText(result))
			}
			if result.StructuredContent["final_observation_returned"] != true {
				t.Errorf("missing final observation evidence: %#v", result.StructuredContent)
			}
		})
	}
}

func TestAXBatchActivationReturnsToOriginalWindow(t *testing.T) {
	for _, crossApp := range []bool{false, true} {
		t.Run(fmt.Sprintf("cross_app_%t", crossApp), func(t *testing.T) {
			service, backend, first, second := axActivationService(t, crossApp)
			result := service.callTool(contract.PublicToolAX, map[string]any{
				"mode": "act", "window_ref": first.window, "observe": "tree", "actions": []any{
					map[string]any{"type": "activate", "ref": first.window},
					map[string]any{"type": "click", "ref": first.button},
					map[string]any{"type": "activate", "ref": second.window},
					map[string]any{"type": "click", "ref": second.button},
					map[string]any{"type": "activate", "ref": first.window},
					map[string]any{"type": "click", "ref": first.button},
				},
			})
			if result.IsError {
				t.Fatalf("A -> B -> A rejected: %s", ResultText(result))
			}
			want := []string{"activate@" + reportMainHandle, "click@" + reportMainHandle,
				"activate@" + reportOtherHandle, "click@" + reportOtherHandle,
				"activate@" + reportMainHandle, "click@" + reportMainHandle}
			if !slices.Equal(backend.deliveries, want) {
				t.Errorf("delivery order/handles = %v, want %v", backend.deliveries, want)
			}
			axActivationEvidence(t, result, first.window, first.window, second.window,
				second.window, first.window, first.window)
			if !strings.Contains(ResultText(result), `window: "Report" [ref=`+first.window+`]`) {
				t.Errorf("final observation did not return to A: %s", ResultText(result))
			}
		})
	}
}

func TestAXBatchActivationFirstActionOverridesWindow(t *testing.T) {
	service, backend, first, second := axActivationService(t, false)
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "window_ref": first.window, "observe": "tree", "actions": []any{
			map[string]any{"type": "activate", "ref": second.window},
			map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
			map[string]any{"type": "type", "text": "only B receives input"},
		},
	})
	if result.IsError {
		t.Fatalf("first activation did not override window_ref: %s", ResultText(result))
	}
	want := []string{"activate@" + reportOtherHandle, "keypress@" + reportOtherHandle, "type@" + reportOtherHandle}
	if !slices.Equal(backend.deliveries, want) {
		t.Errorf("first activation delivery targets = %v, want %v", backend.deliveries, want)
	}
	axActivationEvidence(t, result, second.window, second.window, second.window)
	if !strings.Contains(ResultText(result), `window: "Notes" [ref=`+second.window+`]`) {
		t.Errorf("final observation does not identify B: %s", ResultText(result))
	}
}

func TestAXBatchActivationBindsInitialRefLessInputToFocus(t *testing.T) {
	for _, crossApp := range []bool{false, true} {
		t.Run(fmt.Sprintf("cross_app_%t", crossApp), func(t *testing.T) {
			service, backend, first, second := axActivationService(t, crossApp)
			result := service.callTool(contract.PublicToolAX, map[string]any{
				"mode": "act", "observe": "tree", "actions": []any{
					map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
					map[string]any{"type": "type", "text": "focused A"},
					map[string]any{"type": "activate", "ref": second.window},
					map[string]any{"type": "keypress", "keys": []any{"TAB"}},
					map[string]any{"type": "type", "text": "activated B"},
				},
			})
			if result.IsError {
				t.Fatalf("focus-bound initial segment rejected: %s", ResultText(result))
			}
			want := []string{"keypress@" + reportMainHandle, "type@" + reportMainHandle,
				"activate@" + reportOtherHandle, "keypress@" + reportOtherHandle, "type@" + reportOtherHandle}
			if !slices.Equal(backend.deliveries, want) {
				t.Errorf("focus/activation delivery targets = %v, want %v", backend.deliveries, want)
			}
			axActivationEvidence(t, result, first.window, first.window, second.window, second.window, second.window)
			if !strings.Contains(ResultText(result), `window: "Notes" [ref=`+second.window+`]`) {
				t.Errorf("final observation does not identify B: %s", ResultText(result))
			}
		})
	}
}

func TestAXBatchActivationStopsWhenActivationOpensModal(t *testing.T) {
	service, backend, first, second := axActivationService(t, true)
	backend.activationModal = &reportWindow{
		handle: reportDialogHandle, role: "dialog", title: "Confirm changes", path: "/window/3",
	}
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "observe": "tree", "actions": []any{
			map[string]any{"type": "click", "ref": first.button},
			map[string]any{"type": "activate", "ref": second.window},
			map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
			map[string]any{"type": "type", "text": "must not reach modal"},
		},
	})
	public := PrepareAXEvidence(result).StructuredContent
	if !result.IsError || public["code"] != contract.AXCodeWindowChanged {
		t.Errorf("unexpected modal lost window_changed guard: %#v\n%s", public, ResultText(result))
	}
	want := []string{"click@" + reportMainHandle, "activate@" + reportOtherHandle}
	if !slices.Equal(backend.deliveries, want) {
		t.Errorf("input continued after activation opened a modal: %v, want %v", backend.deliveries, want)
	}
	axActivationEvidence(t, result, first.window, second.window)
	if public["completed"] != 2 || result.StructuredContent["stopped_before_action_index"] != 2 {
		t.Errorf("modal stop lost completed/next step: %#v", result.StructuredContent)
	}
}

func TestAXBatchActivationRejectsWrongWindowRefsBeforeInput(t *testing.T) {
	for _, scenario := range []struct {
		name, field string
		afterSwitch bool
	}{
		{"old_ref_after_switch", "ref", true},
		{"early_ref_before_switch", "ref", false},
		{"old_wait_ref_after_switch", "wait", true},
		{"early_wait_ref_before_switch", "wait", false},
		{"old_to_ref_after_switch", "to_ref", true},
		{"early_to_ref_before_switch", "to_ref", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, backend, first, second := axActivationService(t, false)
			current, wrong := first, second
			actions := []any{map[string]any{"type": "click", "ref": first.button}}
			if scenario.afterSwitch {
				current, wrong = second, first
				actions = append(actions, map[string]any{"type": "activate", "ref": second.window})
			}
			badIndex := len(actions)
			field := "ref"
			badAction := map[string]any{"type": "click", "ref": wrong.button}
			switch scenario.field {
			case "wait":
				badAction = map[string]any{"type": "wait", "ref": wrong.entry, "state": "visible", "timeout_ms": 1}
			case "to_ref":
				field = "to_ref"
				badAction = map[string]any{"type": "select", "ref": current.entry, "to_ref": wrong.entry}
			}
			actions = append(actions, badAction)
			if !scenario.afterSwitch {
				actions = append(actions, map[string]any{"type": "activate", "ref": second.window})
			}
			result := service.callTool(contract.PublicToolAX, map[string]any{
				"mode": "act", "actions": actions,
			})
			path := fmt.Sprintf("actions[%d].%s", badIndex, field)
			public := PrepareAXEvidence(result).StructuredContent
			if !result.IsError || public["path"] != path {
				t.Errorf("wrong-window ref must fail at %s: %#v", path, public)
			}
			if len(backend.actionRequests) != 0 || len(backend.windowRequests) != 0 || len(backend.desktopRequests) != 0 {
				t.Errorf("ownership preflight reached input backend: actions=%#v, windows=%#v, desktop=%#v",
					backend.actionRequests, backend.windowRequests, backend.desktopRequests)
			}
			if public["input_emitted"] != false || public["completed"] != 0 || public["code"] != contract.AXCodeInvalidArgument {
				t.Errorf("wrong-window rejection evidence: %#v", public)
			}
		})
	}
}

func TestAXBatchActivationFailureStopsFollowingInput(t *testing.T) {
	service, backend, first, second := axActivationService(t, true)
	backend.failActivate = reportOtherHandle
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "observe": "tree", "actions": []any{
			map[string]any{"type": "click", "ref": first.button},
			map[string]any{"type": "activate", "ref": second.window},
			map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
			map[string]any{"type": "type", "ref": second.entry, "text": "must not run"},
		},
	})
	if !result.IsError || !strings.Contains(ResultText(result), "activation failed before input") {
		t.Errorf("activation failure missing: %s", ResultText(result))
	}
	want := []string{"click@" + reportMainHandle, "activate@" + reportOtherHandle}
	if !slices.Equal(backend.deliveries, want) {
		t.Errorf("failure did not stop immediately: %v, want %v", backend.deliveries, want)
	}
	axActivationEvidence(t, result, first.window, second.window)
	batch, _ := result.StructuredContent["a11y_batch_result"].(map[string]any)
	if batch["failed_action_index"] != 1 || batch["actions_completed_without_call_error"] != 1 {
		t.Errorf("failure location/completed count: %#v", batch)
	}
}

func TestAXBatchActivationKeepsImplicitWindowChangeGuard(t *testing.T) {
	service, backend, first, _ := axActivationService(t, false)
	backend.transitionAfterInput = reportOtherHandle
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "window_ref": first.window, "observe": "tree", "actions": []any{
			map[string]any{"type": "click", "ref": first.button},
			map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
		},
	})
	public := PrepareAXEvidence(result).StructuredContent
	if !result.IsError || public["code"] != contract.AXCodeWindowChanged {
		t.Errorf("implicit switch lost window_changed guard: %#v\n%s", public, ResultText(result))
	}
	if want := []string{"click@" + reportMainHandle}; !slices.Equal(backend.deliveries, want) {
		t.Errorf("input continued after implicit switch: %v", backend.deliveries)
	}
}

func TestAXBatchActivationRejectsOldWindowCloseAfterWait(t *testing.T) {
	service, backend, first, _ := axActivationService(t, false)
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "actions": []any{
			map[string]any{"type": "activate", "ref": first.window},
			map[string]any{"type": "wait", "window": "Notes"},
			map[string]any{"type": "close", "ref": first.window},
		},
	})
	public := PrepareAXEvidence(result).StructuredContent
	if !result.IsError || public["code"] != contract.AXCodeInvalidArgument ||
		public["path"] != "actions[2].ref" || public["completed"] != 2 {
		t.Fatalf("old window close was not rejected after wait: %#v", public)
	}
	if len(backend.deliveries) != 1 {
		t.Fatalf("old window received input after wait: %v", backend.deliveries)
	}
}

func TestAXBatchActivationFinalObservationIdentifiesModal(t *testing.T) {
	service, backend, first, second := axActivationService(t, true)
	backend.inputModal = &reportWindow{
		handle: reportDialogHandle, role: "dialog", title: "Opened document", path: "/window/3", active: true,
	}
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "observe": "tree", "actions": []any{
			map[string]any{"type": "activate", "ref": first.window},
			map[string]any{"type": "activate", "ref": second.window},
			map[string]any{"type": "keypress", "keys": []any{"CTRL", "O"}},
		},
	})
	if result.IsError {
		t.Fatal(ResultText(result))
	}
	snapshot := service.currentSnapshot(backend.owners[reportDialogHandle].Name)
	if result.StructuredContent["final_observation_window_ref"] != snapshot.publicWindowRef ||
		!strings.Contains(ResultText(result), "final "+snapshot.publicWindowRef) {
		t.Fatalf("modal observation has the wrong identity: %#v\n%s", result.StructuredContent, ResultText(result))
	}
	axActivationEvidence(t, result, first.window, second.window, second.window)
}

func TestAXBatchActivationFinalWaitDoesNotInventWindowLifecycle(t *testing.T) {
	service, _, first, second := axActivationService(t, true)
	result := service.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "observe": "tree", "actions": []any{
			map[string]any{"type": "activate", "ref": first.window},
			map[string]any{"type": "wait", "window": "Notes"},
		},
	})
	text := ResultText(result)
	if result.IsError || strings.Contains(text, "opened "+second.window) || strings.Contains(text, "closed "+first.window) {
		t.Fatalf("wait fabricated window lifecycle: %s", text)
	}
	if !strings.Contains(text, "final "+second.window) {
		t.Fatalf("wait report lost final window: %s", text)
	}
	axActivationEvidence(t, result, first.window, second.window)
}
