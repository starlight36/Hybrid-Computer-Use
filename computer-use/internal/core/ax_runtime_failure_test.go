package core

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

type unconfirmedSetValueBackend struct {
	*reportBackend
	failure           ProviderAccessibilityResult
	failAt            int
	snapshotInFailure bool
}

func (*unconfirmedSetValueBackend) HostInfo() HostBackendInfo {
	return HostBackendInfo{Platform: "linux"}
}

func (backend *unconfirmedSetValueBackend) ResolveAndAct(request ProviderActionRequest) (ProviderAccessibilityResult, error) {
	response, err := backend.reportBackend.ResolveAndAct(request)
	if len(backend.actionRequests) != backend.failAt {
		return response, err
	}
	failure := backend.failure
	if backend.snapshotInFailure {
		failure.Snapshot = response.Snapshot
	}
	return failure, nil
}

func TestSetValueUnconfirmedEditorStopsBatchDespiteObservedChanges(t *testing.T) {
	for _, scenario := range []struct {
		name              string
		focusChanged      bool
		pixelsChanged     bool
		snapshotInFailure bool
	}{
		{name: "unchanged"},
		{name: "focus_changed", focusChanged: true},
		{name: "editor_and_pixels_changed", focusChanged: true, pixelsChanged: true},
		{name: "changed_failure_snapshot", focusChanged: true, pixelsChanged: true, snapshotInFailure: true},
	} {
		for _, failAt := range []int{1, 2} {
			for _, observe := range []string{contract.ObserveNone, contract.ObserveFull} {
				t.Run(fmt.Sprintf("%s/action_%d/%s", scenario.name, failAt, observe), func(t *testing.T) {
					service, desktop := reportService(t, reportMain())
					window, refs := reportObserve(t, service, "Size", "Interface")
					before := desktop.snapshot(reportMain())
					desktop.desktopResponse = testDesktopFrame(t, "before", 40, 30)
					beforePixels := desktop.desktopResponse.Snapshot.ScreenshotPNGBase64
					response, err := DecodeProviderWireResponse([]byte(fmt.Sprintf(`{
						"protocol":%q,"schema_version":%d,"request_id":"set-value","operation":"resolve_and_act",
						"accessibility":{"ok":false,
							"error":"effect_unknown: the original editor did not confirm the requested value; text was not retried. Observe before sending more input.",
							"failure":{"code":"effect_unknown","phase":"readback","subject":"element"},
							"action_evidence":{"route":"keyboard_synthesis","input_emitted":true,"geometry_sampled":false,"readback_sampled":true,"readback_verified":false}}
					}`, ProviderProtocolName, ProviderProtocolVersion)))
					if err != nil {
						t.Fatalf("decode existing failure wire: %v", err)
					}
					backend := &unconfirmedSetValueBackend{reportBackend: desktop,
						failure: *response.Accessibility, failAt: failAt, snapshotInFailure: scenario.snapshotInFailure}
					service.Backend = backend
					desktop.onAction = func(current *reportBackend) {
						if len(current.actionRequests) != failAt {
							current.checked = true
							return
						}
						if scenario.focusChanged {
							current.textStates = "[editable focusable focused]"
						}
						if scenario.pixelsChanged {
							current.page = "Editing"
							pixels := image.NewRGBA(image.Rect(0, 0, 40, 30))
							pixels.SetRGBA(10, 10, color.RGBA{R: 255, A: 255})
							var encoded bytes.Buffer
							if err := png.Encode(&encoded, pixels); err != nil {
								t.Fatal(err)
							}
							current.desktopResponse = testDesktopFrame(t, "after", 40, 30)
							current.desktopResponse.Snapshot.ScreenshotPNGBase64 = base64.StdEncoding.EncodeToString(encoded.Bytes())
							backend.failure.Notes = []string{"[pixels] 1% of the window changed on screen."}
						}
					}
					actions := []any{}
					if failAt == 2 {
						actions = append(actions, map[string]any{"type": "click", "ref": refs["Interface"]})
					}
					actions = append(actions,
						map[string]any{"type": "set_value", "ref": refs["Size"], "value": "16"},
						map[string]any{"type": "wait", "ref": refs["Size"], "value": "12", "timeout_ms": 1.0},
						map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
					)
					result := service.callTool(contract.PublicToolAX, map[string]any{
						"mode": "act", "window_ref": window, "observe": observe, "actions": actions,
					})
					if len(backend.actionRequests) != failAt {
						t.Fatalf("failed input was retried or later input was sent: %#v", backend.actionRequests)
					}
					if request := backend.actionRequests[failAt-1]; request.Intent != AccessibilitySetValue || request.SetValue == nil || request.SetValue.Value == nil || *request.SetValue.Value != "16" {
						t.Fatalf("failure did not exercise set_value: %#v", request)
					}
					full := result.StructuredContent
					batch, _ := full["a11y_batch_result"].(map[string]any)
					entries, _ := batch["actions"].([]map[string]any)
					if len(entries) != failAt || entries[failAt-1]["call_error"] != true ||
						batch["failed_action_index"] != failAt-1 || batch["failed_action_type"] != "set_value" {
						t.Fatalf("failure or batch stop was lost: %#v", batch)
					}
					for _, evidence := range []map[string]any{full, entries[failAt-1]} {
						runtime, ok := evidence["runtime_action_evidence"].(RuntimeActionEvidence)
						if !ok || runtime.Route != "keyboard_synthesis" || !runtime.InputEmitted ||
							!runtime.ReadbackSampled || runtime.ReadbackVerified == nil || *runtime.ReadbackVerified ||
							evidence["input_events_emitted"] != true || evidence["mutation_unknown"] != true {
							t.Fatalf("unconfirmed runtime input evidence was lost: %#v", evidence)
						}
					}
					prepared := PrepareAXEvidence(result)
					public := prepared.StructuredContent
					if !prepared.IsError || public["ok"] != false || public["code"] != contract.AXCodeEffectUnknown ||
						public["completed"] != failAt-1 || public["requested"] != len(actions) || public["input_emitted"] != true ||
						public["failure_phase"] != "readback" {
						t.Fatalf("changed state overrode provider failure: %#v\n%s", public, ResultText(prepared))
					}
					continuation, _ := public["continuation"].(map[string]any)
					if continuation["state"] != "verify_only" || continuation["retry_same_request"] != false {
						t.Fatalf("unsafe continuation after unconfirmed input: %#v", continuation)
					}
					for _, required := range []string{"state=verify_only", "retry_same_request=false", "text was not retried", "Observe before sending more input"} {
						if !strings.Contains(ResultText(prepared), required) {
							t.Errorf("model text lost %q: %s", required, ResultText(prepared))
						}
					}
					after := desktop.snapshot(reportMain())
					if desktop.value != "12" || (after.Elements[2].States != before.Elements[2].States) != scenario.focusChanged ||
						(desktop.desktopResponse.Snapshot.ScreenshotPNGBase64 != beforePixels) != scenario.pixelsChanged {
						t.Fatal("fixture did not keep the original value while changing only the requested focus/pixels")
					}
					if observe == contract.ObserveFull {
						if full["recovery_observation_returned"] != true || full["task_effect_verified"] != false || full["cause_unattributed"] != true {
							t.Fatalf("fresh observation falsely confirmed the failed action: %#v", full)
						}
						if onlyImage(t, prepared).Data != desktop.desktopResponse.Snapshot.ScreenshotPNGBase64 {
							t.Fatal("failure did not return the current pixels")
						}
						if scenario.pixelsChanged && !strings.Contains(ResultText(prepared), "Editing") {
							t.Fatalf("failure did not return the changed editor: %s", ResultText(prepared))
						}
					} else if len(desktop.observeRequests) != 1 || len(desktop.desktopRequests) != 0 {
						t.Fatal("observe none unexpectedly read the desktop or ran the wait")
					}
				})
			}
		}
	}
}

func TestInvalidKeyKeepsPublicArgumentErrorAndAcceptedSpelling(t *testing.T) {
	response := ProviderAccessibilityResult{
		Error: "Unsupported key: KP_AD. Use KP_ADD.",
		Failure: &ProviderFailure{
			Code: "invalid_argument", Phase: "precondition", FactsComplete: true,
		},
	}
	result := NewService().runtimeReportedFailure("", AXServiceRequest{}, response)
	result = prependAXBatchSummary(result, "Stopped at action 1/1 (keypress).", map[string]any{
		"actions_requested": 1, "actions_completed_without_call_error": 0,
	})
	public := axPublicEvidence(result.StructuredContent, result.IsError, result.Content)
	if public["code"] != "invalid_argument" || public["input_emitted"] != false || public["completed"] != 0 {
		t.Fatalf("invalid key classification lost: %#v", public)
	}
	if message, _ := public["message"].(string); !strings.Contains(message, "Use KP_ADD") {
		t.Fatalf("accepted spelling lost behind batch summary: %#v", public)
	}
}
