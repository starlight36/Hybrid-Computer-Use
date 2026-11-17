package core

import (
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

type envelopeFailureBackend struct {
	*reportBackend
	failure  ProviderAccessibilityResult
	failAt   int
	attempts int
}

func (backend *envelopeFailureBackend) ResolveAndAct(request ProviderActionRequest) (ProviderAccessibilityResult, error) {
	backend.attempts++
	if backend.attempts == backend.failAt {
		return backend.failure, nil
	}
	return backend.reportBackend.ResolveAndAct(request)
}

func TestGlobalInputForbiddenKeepsCauseAndBatchFacts(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		for _, failAt := range []int{1, 2} {
			service, desktop := reportService(t, reportMain())
			_, refs := reportObserve(t, service, "OK")
			code, phase := contract.AXCodeGlobalInputForbidden, "precondition"
			if unknown {
				code, phase = contract.AXCodeEffectUnknown, "emission"
			}
			backend := &envelopeFailureBackend{
				reportBackend: desktop, failAt: failAt,
				failure: ProviderAccessibilityResult{
					Error:    "semantic route unavailable or unverified; no global fallback",
					Failure:  &ProviderFailure{Code: code, Phase: phase, FactsComplete: true},
					Mutation: &ProviderMutationFacts{InputEventsEmitted: unknown, MutationUnknown: unknown},
				},
			}
			service.Backend = &globalInputPolicyBackend{backend, "linux"}
			result := PrepareAXEvidence(service.callTool(contract.PublicToolAX, map[string]any{
				"mode": "act", "actions": []any{
					map[string]any{"type": "click", "ref": refs["OK"], "allow_global_input": false},
					map[string]any{"type": "click", "ref": refs["OK"], "allow_global_input": false},
				},
			}))
			public := result.StructuredContent
			if !result.IsError || public["code"] != code || public["completed"] != failAt-1 ||
				public["input_emitted"] != (unknown || failAt > 1) {
				t.Fatalf("failed action/prefix evidence lost: %#v, %s", public, ResultText(result))
			}
			if backend.attempts != failAt {
				t.Fatalf("failure retried or later action ran: %d attempts", backend.attempts)
			}
		}
	}
}

type synthesisNoteBackend struct {
	*reportBackend
	note string
}

func (backend *synthesisNoteBackend) ResolveAndAct(request ProviderActionRequest) (ProviderAccessibilityResult, error) {
	result, err := backend.reportBackend.ResolveAndAct(request)
	if len(backend.actionRequests) == 1 {
		result.Notes = []string{backend.note}
	}
	return result, err
}

func TestAXBatchRetainsEarlierSynthesisWarning(t *testing.T) {
	for _, observe := range []string{"none", "diff", "tree"} {
		service, desktop := reportService(t, reportMain())
		_, refs := reportObserve(t, service, "OK", "Size")
		note := "[a11y][synthesis] Synthesized click. Global input may change focus; verify the effect."
		service.Backend = &synthesisNoteBackend{desktop, note}
		result := PrepareAXEvidence(service.callTool(contract.PublicToolAX, map[string]any{
			"mode": "act", "observe": observe, "actions": []any{
				map[string]any{"type": "click", "ref": refs["OK"]},
				map[string]any{"type": "set_value", "ref": refs["Size"], "value": "14"},
			},
		}))
		if result.IsError || strings.Count(ResultText(result), note) != 1 {
			t.Fatalf("%s lost or duplicated earlier synthesis warning: %s", observe, ResultText(result))
		}
	}
}

func TestRuntimeFailureEnvelopeKeepsCauseAndBatchFacts(t *testing.T) {
	for _, failure := range []struct {
		code, phase, reason, publicCode string
	}{
		{"invalid_argument", "precondition", `select_text_not_found: "missing" is not in the element's text; no input was emitted.`, contract.AXCodeInvalidArgument},
		{"invalid_argument", "precondition", "select_unavailable: to_ref must be a later text sibling of ref in the same container; no input was emitted.", contract.AXCodeInvalidArgument},
		{"unsupported_action", "precondition", "This control has no verified text editor; no text input was emitted.", contract.AXCodeInvalidArgument},
		{"input_failed", "emit", "input_failed: the accessibility runtime failed before input.", contract.AXCodeRuntimeError},
	} {
		for _, failAt := range []int{1, 2} {
			t.Run(failure.reason+itoa(failAt), func(t *testing.T) {
				service, desktop := reportService(t, reportMain())
				window, refs := reportObserve(t, service, "Size")
				backend := &envelopeFailureBackend{reportBackend: desktop, failAt: failAt,
					failure: ProviderAccessibilityResult{OK: false, Error: failure.reason,
						Failure:  &ProviderFailure{Code: failure.code, Phase: failure.phase, Subject: "element", FactsComplete: true},
						Mutation: &ProviderMutationFacts{}},
				}
				service.Backend = backend
				result := PrepareAXEvidence(service.callTool(contract.PublicToolAX, map[string]any{
					"mode": "act", "window_ref": window, "observe": "none",
					"actions": []any{
						map[string]any{"type": "set_value", "ref": refs["Size"], "value": "16"},
						map[string]any{"type": "set_value", "ref": refs["Size"], "value": "18"},
						map[string]any{"type": "set_value", "ref": refs["Size"], "value": "20"},
					},
				}))
				public := result.StructuredContent
				wantCode := failure.publicCode
				if failAt > 1 {
					wantCode = contract.AXCodeEffectUnknown
				}
				if !result.IsError || public["code"] != wantCode || public["message"] != failure.reason {
					t.Errorf("lost failure cause: %#v\n%s", public, ResultText(result))
				}
				if wantCode == contract.AXCodeInvalidArgument && public["path"] != "actions[0]" {
					t.Errorf("argument failure must name the failed action: %#v", public)
				}
				if backend.attempts != failAt || public["completed"] != failAt-1 || public["requested"] != 3 ||
					public["input_emitted"] != (failAt > 1) || public["failure_code"] != failure.code {
					t.Fatalf("lost batch stop/input facts: attempts=%d %#v", backend.attempts, public)
				}
				batch, _ := public["batch"].(map[string]any)
				if batch["failed_action_index"] != failAt-1 || !strings.Contains(ResultText(result), "Stopped at action") {
					t.Fatalf("lost batch context: %#v %s", batch, ResultText(result))
				}
				hint, _ := public["hint"].(string)
				if failure.code == "unsupported_action" {
					if hint != GUIScreenshotCall {
						t.Errorf("unsupported semantic action needs visual recovery: %q", hint)
					}
				} else {
					args := decodeJSONForTest(t, hint)
					if args["mode"] != "observe" || args["window_ref"] != window {
						t.Errorf("recovery must inspect the failed target's window: %s", hint)
					}
					if _, err := DecodeAXRequest(args); err != nil {
						t.Errorf("invalid recovery call: %v", err)
					}
				}
			})
		}
	}
}

func TestBatchFailureHintPreservesExplicitRecoveryAndPreparationStops(t *testing.T) {
	context := &AXBatchContext{WindowRef: "w7"}
	for _, evidence := range []map[string]any{
		{"failure_code": "invalid_argument", contract.AXPublicErrorHintKey: "specific recovery"},
		{"failure_code": "input_failed", "focus_preparation": "unknown"},
		{"failure_code": "input_failed", "pointer_preparation": "unknown"},
		{"failure_code": "stale_element_ref"},
	} {
		if hint := axBatchFailureHint(context, evidence); hint != "" {
			t.Fatalf("overrode existing recovery policy: %s %#v", hint, evidence)
		}
	}
	if hint := axBatchFailureHint(nil, map[string]any{"failure_code": "input_failed"}); hint != GUIScreenshotCall {
		t.Fatalf("unaddressable window must not invent a ref: %s", hint)
	}
}

func TestProviderArgumentFailureWithUncertainInputIsEffectUnknown(t *testing.T) {
	for _, code := range []string{"invalid_argument", "unsupported_action", "input_failed"} {
		for _, evidence := range []map[string]any{
			{"partial_input_possible": true},
			{"mutation_unknown": true},
			{"input_events_emitted": true},
		} {
			evidence["failure_code"] = code
			evidence["failure_phase"] = "precondition"
			if got := axPublicErrorCode(evidence); got != contract.AXCodeEffectUnknown {
				t.Errorf("uncertain input must not become a no-input error: %s %#v", got, evidence)
			}
		}
	}
}
