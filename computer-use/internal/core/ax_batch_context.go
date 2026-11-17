package core

import (
	"errors"
	"fmt"
	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
	"strconv"
	"strings"
)

type AXBatchContext struct {
	AppRef         string `json:"-"`
	WindowRef      string `json:"-"`
	AppToken       string `json:"-"`
	WindowHandle   string `json:"-"`
	expectedWindow *ProviderWindowExpectation
	// boundByFocus marks a batch bound to the window that held keyboard
	// focus before input; its keys and text must still reach that window.
	boundByFocus              bool
	windowsQueuedInputPending bool
	// report records the snapshots the batch passes through for the act
	// report; nil outside act.
	report *actReportTrace
	// settleBaseline is the state read before the input step that a settle
	// wait follows; nil when no settle wait follows the current step.
	settleBaseline *axSettleState
}

type axWindowContinuity string

const (
	axWindowSame    axWindowContinuity = "same"
	axWindowChanged axWindowContinuity = "changed"
	axWindowUnknown axWindowContinuity = "unknown"
)

func axBatchWindowEnd(actions []axBatchAction, start int) int {
	for index := start + 1; index < len(actions); index++ {
		if actions[index].actionType == "activate" {
			return index
		}
	}
	return len(actions)
}

func (s *Service) deriveAXBatchContexts(
	explicitWindowRef string,
	actions []axBatchAction,
	focus *axDesktopWindow,
) ([]*AXBatchContext, error) {
	contexts := make([]*AXBatchContext, len(actions))
	for start := 0; start < len(actions); {
		end := axBatchWindowEnd(actions, start)
		windowRef := explicitWindowRef
		if actions[start].actionType == "activate" {
			windowRef = actions[start].targetRef
		}
		context, err := s.deriveAXWindowContext(windowRef, actions[start:end], focus, start)
		if err != nil {
			return nil, err
		}
		if err := s.validateAXNoTreeSegment(context, actions[start:end], start); err != nil {
			return nil, err
		}
		for index := start; index < end; index++ {
			contexts[index] = context
			if actions[index].wait != nil && actions[index].wait.ref != "" {
				if err := s.preflightAXWaitRef(context, actions[index].wait.ref); err != nil {
					return nil, axBatchRefError(index, "ref", err)
				}
			}
			if actions[index].toRef != "" {
				if err := s.preflightAXWaitRef(context, actions[index].toRef); err != nil {
					return nil, axBatchRefError(index, "to_ref", err)
				}
			}
		}
		start = end
	}
	return contexts, nil
}

func (s *Service) deriveAXBatchContext(
	explicitWindowRef string,
	actions []axBatchAction,
	focus *axDesktopWindow,
) (*AXBatchContext, error) {
	return s.deriveAXWindowContext(explicitWindowRef, actions, focus, 0)
}

func (s *Service) deriveAXWindowContext(
	explicitWindowRef string,
	actions []axBatchAction,
	focus *axDesktopWindow,
	actionOffset int,
) (*AXBatchContext, error) {
	context := &AXBatchContext{}
	bindWindow := func(ref, handle, appRef, appIdentity string) error {
		ref = strings.TrimSpace(ref)
		handle = strings.TrimSpace(handle)
		if context.WindowHandle != "" && context.WindowHandle != handle {
			return axInvalid("", fmt.Sprintf(
				"ref belongs to window %s, not the current batch window %s; activate that window before using its refs",
				ref, context.WindowRef),
				"Use refs from the current window, or activate the other window before acting in it.",
			)
		}
		if context.WindowRef == "" {
			context.WindowRef = ref
			context.WindowHandle = handle
		}
		if appRef != "" {
			context.AppRef = appRef
			context.AppToken = appRef
		} else if context.AppToken == "" {
			context.AppToken = appIdentity
		}
		return nil
	}
	if explicitWindowRef != "" {
		entry, err := s.resolveShortAXWindowRefOwned(explicitWindowRef)
		if err != nil {
			return nil, err
		}
		if err := bindWindow(
			entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity,
		); err != nil {
			return nil, err
		}
	}
	hasTarget := false
	for index, action := range actions {
		address := strings.TrimSpace(action.targetRef)
		if address == "" {
			continue
		}
		hasTarget = true
		var err error
		if isShortAXRef(address) {
			var entry *AXRefEntry
			entry, err = s.resolveShortAXRefOwned(address)
			if err == nil {
				err = bindWindow(entry.LogicalWindowRef, entry.WindowHandle,
					entry.AppRef, entry.AppIdentity)
				if err != nil && explicitWindowRef != "" && index == 0 && actionOffset == 0 {
					err = axInvalid("window_ref", fmt.Sprintf("%s belongs to window %s, not window_ref %s; omit window_ref",
						address, entry.LogicalWindowRef, explicitWindowRef),
						"Omit window_ref so the action refs determine the window, or use refs from window_ref.")
				}
			}
		} else {
			var entry *AXWindowRefEntry
			entry, err = s.resolveShortAXWindowRefOwned(address)
			if err == nil {
				err = bindWindow(entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity)
			}
		}
		if err != nil {
			return nil, axBatchRefError(actionOffset+index, "ref", err)
		}
	}
	if context.WindowHandle == "" && !hasTarget && focus != nil {
		entry, err := s.resolveShortAXWindowRefOwned(focus.ref)
		if err != nil {
			return nil, err
		}
		if err := bindWindow(entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity); err != nil {
			return nil, err
		}
		context.boundByFocus = true
	}
	if context.WindowHandle == "" {
		return nil, errors.New("the batch has no window: give an rN or wN ref, a title, or window_ref")
	}
	if context.AppToken == "" {
		return nil, errors.New("the batch window has no current application owner; observe it again")
	}
	if entry, err := s.WindowRefRegistry.Resolve(context.WindowRef, ""); err == nil {
		context.expectedWindow = &ProviderWindowExpectation{
			WindowHandle:       entry.WindowHandle,
			ProviderBusName:    entry.ProviderWindowBusName,
			ProviderObjectPath: entry.ProviderWindowObjectPath,
			ProcessGeneration:  entry.ProviderWindowProcessGeneration,
			Role:               entry.ProviderWindowRole,
			Name:               entry.ProviderWindowName,
		}
	}
	return context, nil
}

func axBatchRefError(index int, field string, err error) error {
	path := fmt.Sprintf("actions[%d].%s", index, field)
	var argument *axArgError
	if errors.As(err, &argument) && argument.path == "" {
		argument.path = path
	}
	return fmt.Errorf("%s: %w", path, err)
}

func (s *Service) axBatchWindowContinuity(
	context *AXBatchContext,
) axWindowContinuity {
	if s == nil || context == nil || strings.TrimSpace(context.WindowHandle) == "" {
		return axWindowUnknown
	}
	if continuity, native := s.axNoTreeBatchWindowContinuity(context); native {
		return continuity
	}
	snapshot := s.currentSnapshot(context.AppToken)
	if snapshot == nil {
		return axWindowUnknown
	}
	if snapshot.WindowAbsent {
		return axWindowChanged
	}
	if strings.TrimSpace(snapshot.WindowHandle) == "" ||
		snapshot.WindowHandle != context.WindowHandle {
		if strings.TrimSpace(snapshot.WindowHandle) == "" {
			return axWindowUnknown
		}
		return axWindowChanged
	}
	if strings.TrimSpace(context.WindowRef) == "" ||
		strings.TrimSpace(snapshot.publicWindowRef) == "" {
		return axWindowUnknown
	}
	windowEntry, err := s.WindowRefRegistry.Resolve(context.WindowRef, "")
	if err != nil {
		var lifecycle *axRefLifecycleError
		if errors.As(err, &lifecycle) && lifecycle.lifecycle == AXRefNeedsRevalidation {
			return axWindowUnknown
		}
		return axWindowChanged
	}
	if windowEntry.WindowHandle != context.WindowHandle ||
		snapshot.publicWindowRef != context.WindowRef {
		return axWindowChanged
	}
	return axWindowSame
}

func (s *Service) validateAXActionWindow(context *AXBatchContext, action axBatchAction, index int) error {
	for _, target := range []struct{ field, ref string }{{"ref", action.targetRef}, {"to_ref", action.toRef}} {
		if isShortAXRef(target.ref) {
			if err := s.preflightAXWaitRef(context, target.ref); err != nil {
				return axBatchRefError(index, target.field, err)
			}
		} else if isShortAXWindowRef(target.ref) && action.actionType != "activate" {
			entry, err := s.resolveShortAXWindowRefOwned(target.ref)
			if err != nil {
				return axBatchRefError(index, target.field, err)
			}
			if entry.WindowHandle != context.WindowHandle {
				return axBatchRefError(index, target.field, axInvalid("", fmt.Sprintf(
					"%s belongs to another window, not the current batch window %s", target.ref, context.WindowRef),
					"Activate that window before acting in it."))
			}
		}
	}
	return nil
}

// axBatchFocusChanged reports whether keyboard focus left the window a
// focus-bound batch was bound to, and if so returns the stop result.
func (s *Service) axBatchFocusChanged(
	context *AXBatchContext,
	index, count int,
	actionEvidence []map[string]any,
) (ToolCallResult, bool) {
	focus, argErr := s.resolveFocusedWindow()
	if argErr == nil && focus.handle == context.WindowHandle {
		return ToolCallResult{}, false
	}
	if shadowGuard("ax", "batch_window", "focus_left_bound_window", map[string]any{
		"window_ref": context.WindowRef, "next_action_index": index,
	}) {
		return ToolCallResult{}, false
	}
	now := "no window has focus"
	if argErr == nil {
		now = fmt.Sprintf("focus is now on %s %s (%s)", focus.ref, strconv.Quote(focus.title), focus.appName)
	} else if argErr.Code == contract.AXCodeRuntimeError {
		now = "the focused window could not be read"
	}
	anyInputEmitted, anyPartialPossible, anyMutationUnknown := batchMutationSummary(actionEvidence)
	message := fmt.Sprintf(
		"keyboard focus left the bound window %s before action %d/%d; %s. "+
			"%d action(s) ran; the rest were not sent.",
		context.WindowRef, index+1, count, now, index,
	)
	stopped := TextResult(message, true)
	stopped.StructuredContent = map[string]any{
		contract.AXPublicErrorCodeKey:   contract.AXCodeWindowChanged,
		contract.AXPublicErrorDetailKey: message,
		contract.AXPublicErrorHintKey:   `{"mode":"act","actions":[{"type":"activate","ref":"` + context.WindowRef + `"}]}`,
		"failure_code":                  "stale_target",
		"failure_phase":                 "between_actions",
		"input_events_emitted":          anyInputEmitted,
		"partial_input_possible":        anyPartialPossible,
		"mutation_unknown":              anyMutationUnknown || anyInputEmitted,
		"observation_required":          true,
		"a11y_batch_result": map[string]any{
			"actions_requested":                    count,
			"actions_completed_without_call_error": index,
			"stopped_before_action_index":          index,
			"rejected_before_first_action":         index == 0,
			"partial_sequence":                     index > 0,
			"actions":                              actionEvidence,
		},
	}
	return stopped, true
}
