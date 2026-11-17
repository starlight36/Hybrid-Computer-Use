package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAXGlobalInputPolicyWireRoundTrip(t *testing.T) {
	for _, intent := range []AccessibilityOperation{AccessibilityClick, AccessibilityScroll} {
		for _, value := range []*bool{nil, boolPointer(false), boolPointer(true)} {
			var command axActionCommand = AXClickCommand{AllowGlobalInput: value}
			if intent == AccessibilityScroll {
				command = axScrollCommand{AllowGlobalInput: value, Direction: "down", Pages: 1}
			}
			request := ProviderActionRequest{
				ProtocolVersion: ProviderProtocolVersion, Intent: intent, App: "Editor",
				Target: &ProviderSemanticTarget{Selector: `button "Save"`, HasName: true},
			}
			command.applyProviderAction(&request)
			wire := NewProviderWireRequest("policy", ProviderWireResolveAndAct)
			wire.Action = &request
			data, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var raw struct {
				Action struct {
					Input map[string]any `json:"input"`
				} `json:"action"`
			}
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			serialized, present := raw.Action.Input["allow_global_input"]
			if (value != nil) != present || (value != nil && serialized != *value) {
				t.Fatalf("wire lost %s policy %v: %s", intent, value, data)
			}
			decoded, err := DecodeProviderWireRequest(data)
			if err != nil {
				t.Fatal(err)
			}
			var actual *bool
			if intent == AccessibilityClick {
				actual = decoded.Action.Click.AllowGlobalInput
			} else {
				actual = decoded.Action.Scroll.AllowGlobalInput
			}
			if !reflect.DeepEqual(actual, value) {
				t.Fatalf("wire round trip lost %s policy: got %v, want %v", intent, actual, value)
			}
			trace := map[string]any{}
			command.appendTraceFields(trace)
			traced, present := trace["allow_global_input"]
			if (value != nil) != present || (value != nil && traced != *value) {
				t.Fatalf("trace lost %s policy: %v", intent, trace)
			}
			if value != nil && !*value {
				for _, invalid := range []string{"null", "  null  ", "0", `"false"`, "[]", "{}"} {
					invalidData := strings.Replace(string(data), `"allow_global_input":false`, `"allow_global_input":`+invalid, 1)
					if _, err := DecodeProviderWireRequest([]byte(invalidData)); err == nil {
						t.Fatalf("wire accepted invalid %s policy %s", intent, invalid)
					}
				}
			}
		}
	}
}
