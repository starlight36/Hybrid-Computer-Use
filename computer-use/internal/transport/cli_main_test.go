package transport

import (
	"bytes"
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/core"
)

// A misspelled ablation stops every entry point before it serves anything;
// help and version still answer.
func TestInvalidAblationStopsStartup(t *testing.T) {
	t.Setenv(contract.AblationEnv, "wait,diffs")
	for _, args := range [][]string{
		{"mcp"}, {"serve"}, {"daemon", "status"}, {"schema"}, {"call", "AX", "--args", `{"mode":"apps"}`}, {"apps"},
	} {
		var stdout bytes.Buffer
		err := RunCLI(args, &stdout)
		if err == nil || !strings.Contains(err.Error(), `unknown ablation "diffs"`) {
			t.Fatalf("%v: got %v", args, err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("%v wrote %q before failing", args, stdout.String())
		}
	}
	for _, args := range [][]string{{"help"}, {"version"}} {
		if err := RunCLI(args, &bytes.Buffer{}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	t.Setenv(contract.AblationEnv, "guard")
	t.Setenv(contract.GuardLogEnv, "")
	if err := RunCLI([]string{"mcp"}, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), contract.GuardLogEnv) {
		t.Fatalf("guard without a log: %v", err)
	}
}

func TestMCPInitializeServesTheAblatedInstructions(t *testing.T) {
	t.Setenv(contract.AblationEnv, "wait")
	response := handleMCPRequest(map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "initialize", "params": map[string]any{},
	}, core.NewService())
	instructions := response["result"].(map[string]any)["instructions"].(string)
	if instructions == contract.ServerInstructions || strings.Contains(instructions, "wait step") {
		t.Fatalf("instructions: %s", instructions)
	}
}
