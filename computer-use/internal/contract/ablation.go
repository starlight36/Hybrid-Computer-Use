package contract

import (
	"fmt"
	"os"
	"strings"
)

// Mechanism ablations remove one HCU mechanism each, for experiments that
// measure what the mechanism contributes. COMPUTER_USE_ABLATE is a
// comma-separated list of the names below; by default nothing is removed.
//
// A removed mechanism is absent from everything the model sees: schemas,
// tool descriptions, server instructions and error texts. Like a disabled
// channel, it is not merely rejected at call time.
//
//   - batching: an AX act holds at most one input step; wait steps may
//     still surround it. GUI batches are unchanged.
//   - wait: AX acts take no wait steps. The tools' own stabilization after
//     input is unchanged.
//   - diff: an AX act returns its execution evidence and the whole current
//     tree of its window, as observe "tree" does, without the effect and
//     windows report, the diff, or notes that compare with an earlier read.
//     observe always returns the tree, never "unchanged". The tree is read
//     with the same post-action stabilization as a diff. Two things stay:
//     effect_unknown in the evidence (whether input had a verified effect
//     is execution evidence, not a diff) and a settle wait step's message
//     (it belongs to wait).
//   - guard: the target and layout guards still decide, but only log the
//     input they would have stopped (COMPUTER_USE_GUARD_LOG) and let it
//     through. Only the Linux provider implements this.
const (
	AblationEnv    = "COMPUTER_USE_ABLATE"
	GuardLogEnv    = "COMPUTER_USE_GUARD_LOG"
	AblateBatching = "batching"
	AblateWait     = "wait"
	AblateDiff     = "diff"
	AblateGuard    = "guard"
)

// AblationNames lists the ablations in their canonical order.
var AblationNames = []string{AblateBatching, AblateWait, AblateDiff, AblateGuard}

// ParseAblations returns the ablations named in raw. An unknown or repeated
// name is an error: an experiment with a misspelled switch would otherwise
// run the complete HCU under an ablation label.
func ParseAblations(raw string) (map[string]bool, error) {
	ablated := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		return ablated, nil
	}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if !ContainsString(AblationNames, name) {
			return nil, fmt.Errorf("%s: unknown ablation %q; use a comma-separated subset of %s",
				AblationEnv, name, strings.Join(AblationNames, ", "))
		}
		if ablated[name] {
			return nil, fmt.Errorf("%s: ablation %q is listed twice", AblationEnv, name)
		}
		ablated[name] = true
	}
	return ablated, nil
}

// ValidateAblations checks COMPUTER_USE_ABLATE for a server, daemon or
// command started on goos. Entry points call it before serving anything.
func ValidateAblations(goos string, getenv func(string) string) error {
	ablated, err := ParseAblations(getenv(AblationEnv))
	if err != nil {
		return err
	}
	if ablated[AblateGuard] {
		if goos != "linux" {
			return fmt.Errorf("%s: the guard ablation is implemented only by the Linux provider, not on %s",
				AblationEnv, goos)
		}
		if strings.TrimSpace(getenv(GuardLogEnv)) == "" {
			return fmt.Errorf("%s: the guard ablation needs %s, the file that records the input the guards would have stopped",
				AblationEnv, GuardLogEnv)
		}
	}
	return nil
}

// Ablated reports whether the named mechanism is removed. Entry points
// validate the variable at startup, so a value that does not parse here
// means the process skipped that check.
func Ablated(name string) bool {
	ablated, err := ParseAblations(os.Getenv(AblationEnv))
	if err != nil {
		panic(err)
	}
	return ablated[name]
}

// SortedAblations returns the active ablations in canonical order.
func SortedAblations() []string {
	names := []string{}
	for _, name := range AblationNames {
		if Ablated(name) {
			names = append(names, name)
		}
	}
	return names
}

// PublishedAXActionTypes is the AX action list the schema publishes and the
// decoder accepts.
func PublishedAXActionTypes() []string {
	if !Ablated(AblateWait) {
		return AXActionTypes
	}
	types := []string{}
	for _, actionType := range AXActionTypes {
		if actionType != "wait" {
			types = append(types, actionType)
		}
	}
	return types
}

// PublishedObserveValues is the observe list the schema publishes and the
// decoder accepts. Without diff, act takes no observe field (it always
// returns the tree), so diff and none, which only act uses, are gone too.
func PublishedObserveValues() []string {
	if !Ablated(AblateDiff) {
		return ObserveValues
	}
	return []string{ObserveTree, ObserveFull, ObserveScreenshot}
}

// AXActMaxActions is the published maximum length of an AX act.
func AXActMaxActions() int {
	if Ablated(AblateBatching) && Ablated(AblateWait) {
		return 1
	}
	return AXBatchMaxActions
}
