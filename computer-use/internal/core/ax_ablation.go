package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

// Behavior of the mechanism ablations in the AX service; contract/ablation.go
// says what each one removes.

// axActObservation is the observation an act returns. Under the diff
// ablation an act takes no observe field and always returns the whole
// current working tree, as observe "tree" does; withActReport and
// modelFacingAXNotes leave out what changed.
func axActObservation(obs observation) observation {
	if contract.Ablated(contract.AblateDiff) {
		obs.mode = contract.ObserveTree
	}
	return obs
}

// postActionReadStabilizes reports whether the read after an action waits
// for late content and a replaced top-level window. A diff act does; under
// the diff ablation the act's whole tree takes its place and is read the
// same way, so the ablation changes what is shown, not when it is read.
func postActionReadStabilizes(mode string) bool {
	return mode == contract.ObserveDiff ||
		(mode == contract.ObserveTree && contract.Ablated(contract.AblateDiff))
}

// axChangeNote reports a model-facing note that compares the state with an
// earlier read: what the diff ablation removes from AX results.
func axChangeNote(note string) bool {
	for _, prefix := range []string{
		"[state] semantic_state=unchanged", "[state] visual_change=", "[state] ax_change=",
		"[text-attrs]", "[select] ", "[transition] ",
		"The accessibility tree structure is unchanged", "The accessibility tree is unchanged",
		"[identity] The new top-level window", "The set of top-level windows changed",
	} {
		if strings.HasPrefix(note, prefix) {
			return true
		}
	}
	return false
}

// axCheckOneInputStep enforces the batching ablation: an act holds at most
// one input step. Wait steps send no input and may surround it.
func axCheckOneInputStep(earlier []axBatchAction, action axBatchAction, items []any, path string) *axArgError {
	if !contract.Ablated(contract.AblateBatching) || action.actionType == "wait" {
		return nil
	}
	for _, step := range earlier {
		if step.actionType != "wait" {
			return axInvalid(path, fmt.Sprintf(
				"act takes one input step per call, and %s is the second; send it in a later call", path), "").
				withRule(contract.ArgumentRuleOneInputStep).withFix(axSet("actions", items[:len(earlier)]))
		}
	}
	return nil
}

// shadowGuard reports whether the guard ablation lets input through that a
// guard decided to stop. It first appends the decision to
// COMPUTER_USE_GUARD_LOG, in the format of the Linux provider's
// guard_shadow.py, so both sides of one run share one log.
func shadowGuard(channel, guard, reason string, detail map[string]any) bool {
	if !contract.Ablated(contract.AblateGuard) {
		return false
	}
	record := map[string]any{
		"at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "channel": channel,
		"guard": guard, "reason": reason, "would_block": true,
	}
	for key, value := range detail {
		record[key] = value
	}
	path := strings.TrimSpace(os.Getenv(contract.GuardLogEnv))
	line, err := json.Marshal(record)
	if path == "" || err != nil {
		return true
	}
	// A lost log line must not fail the input it describes.
	if dir := filepath.Dir(path); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return true
	}
	defer file.Close()
	_, _ = file.Write(append(line, '\n'))
	return true
}

// axAblatedActionType reports an AX action type that COMPUTER_USE_ABLATE
// removed from the published contract.
func axAblatedActionType(verb string) bool {
	return contract.ContainsString(contract.AXActionTypes, verb) &&
		!contract.ContainsString(contract.PublishedAXActionTypes(), verb)
}
