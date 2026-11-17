package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

// A window can lack an accessibility tree even when another window of its
// process exports one. The apps listing gives it a wN so that
// activate, close and window waits find it by title; reads and element
// actions say that it has no tree and point to GUI.

const axNoTreeWindowMark = "(no accessibility tree: use GUI)"

// axNoTreeWindowError is a read or action aimed at such a window.
type axNoTreeWindowError struct {
	ref string
}

func (err *axNoTreeWindowError) Error() string {
	if contract.Ablated(contract.AblateWait) {
		return err.ref + " has no accessibility tree. " +
			"Read and operate it with GUI; AX can only activate or close this window."
	}
	return err.ref + " has no accessibility tree. " +
		"Read and operate it with GUI; AX can only activate, " +
		"close or wait for this window."
}

func axNoTreeWindowResult(err *axNoTreeWindowError, path string, requested int) ToolCallResult {
	return axArgErrorResult(&axArgError{
		Code:    contract.AXCodeAppUnavailable,
		path:    path,
		Message: err.Error(),
		Hint:    GUIScreenshotCall,
	}, requested)
}

// noTreeWindowEntry returns the registry entry of ref when it names a
// window without an accessibility tree.
func (s *Service) noTreeWindowEntry(ref string) *AXWindowRefEntry {
	if !isShortAXWindowRef(ref) {
		return nil
	}
	entry, err := s.WindowRefRegistry.Resolve(ref, "")
	if err != nil || entry == nil || !entry.NoAccessibilityTree {
		return nil
	}
	return entry
}

func (s *Service) validateAXNoTreeSegment(context *AXBatchContext, actions []axBatchAction, start int) error {
	if context == nil || s.noTreeWindowEntry(context.WindowRef) == nil {
		return nil
	}
	for index, action := range actions {
		switch action.actionType {
		case "activate", "close":
			continue
		case "wait":
			if action.wait != nil && action.wait.window != "" {
				continue
			}
		}
		return &axArgError{
			Code:    contract.AXCodeAppUnavailable,
			path:    fmt.Sprintf("actions[%d]", start+index),
			Message: (&axNoTreeWindowError{ref: context.WindowRef}).Error(),
			Hint:    GUIScreenshotCall,
		}
	}
	return nil
}

func (s *Service) axNoTreeBatchAction(action string, window *AXWindowRefEntry) ToolCallResult {
	result := s.axNoTreeWindowCommand(action, window)
	if result.IsError {
		return result
	}
	result = axNoTreeBatchObservation(window)
	verb := "activated"
	if action == "close" {
		verb = "closed"
	}
	result.Content = append([]ContentItem{{Type: "text", Text: verb + " " + window.Ref}}, result.Content...)
	result.StructuredContent["input_events_emitted"] = true
	return result
}

func axNoTreeBatchObservation(window *AXWindowRefEntry) ToolCallResult {
	result := TextResult(window.Ref+" "+axNoTreeWindowMark, false)
	result.StructuredContent = map[string]any{
		"input_events_emitted":       false,
		"final_observation_returned": false,
		"final_observation_kind":     "none",
		"observation_required":       false,
		"evidence_scope":             "delivery_or_local_ui",
	}
	return result
}

func (s *Service) axNoTreeBatchWindowContinuity(context *AXBatchContext) (axWindowContinuity, bool) {
	if s == nil || context == nil || s.noTreeWindowEntry(context.WindowRef) == nil {
		return axWindowUnknown, false
	}
	if !validInternalWindowHandle(context.WindowHandle) {
		return axWindowUnknown, true
	}
	inventory, _ := s.listDesktopWindows(false)
	if inventory == nil {
		return axWindowUnknown, true
	}
	for _, window := range inventory.windows {
		if window.handle == context.WindowHandle {
			return axWindowSame, true
		}
	}
	return axWindowChanged, true
}

// axNoTreeWindowCommand asks the provider to activate or close the native
// window and to confirm the effect with the window manager.
func (s *Service) axNoTreeWindowCommand(action string, window *AXWindowRefEntry) ToolCallResult {
	if window == nil {
		return TextResult("the window is no longer listed; call AX apps", true)
	}
	operation := WindowOperation(action)
	request := ProviderWindowActionRequest{
		ProtocolVersion: ProviderProtocolVersion,
		Action:          operation,
		App:             window.AppIdentity,
		WindowHandle:    window.WindowHandle,
		Target: &ProviderSemanticTarget{
			Selector:     "window " + strconv.Quote(window.ProviderWindowName),
			HasName:      window.ProviderWindowName != "",
			Name:         window.ProviderWindowName,
			Role:         "window",
			WindowHandle: window.WindowHandle,
		},
	}
	response, err := s.callProviderWindowAction(request)
	if err == nil && response != nil && response.OK {
		return TextResult(action, false)
	}
	message := action + " failed"
	if err != nil {
		message = err.Error()
	} else if response != nil && strings.TrimSpace(response.Error) != "" {
		message = strings.TrimSpace(response.Error)
	}
	result := TextResult(message, true)
	result.StructuredContent = map[string]any{
		"failure_code":         "effect_unknown",
		"input_events_emitted": true,
		"mutation_unknown":     true,
	}
	if response != nil && response.Failure != nil {
		result.StructuredContent["failure_code"] = response.Failure.Code
		result.StructuredContent["failure_phase"] = response.Failure.Phase
		emitted := response.Mutation != nil && response.Mutation.InputEventsEmitted
		result.StructuredContent["input_events_emitted"] = emitted
		result.StructuredContent["mutation_unknown"] = emitted
	}
	return result
}

// asNoTreeWindowError reports whether err is a read of a window without a tree.
func asNoTreeWindowError(err error) (*axNoTreeWindowError, bool) {
	var noTree *axNoTreeWindowError
	ok := errors.As(err, &noTree)
	return noTree, ok
}
