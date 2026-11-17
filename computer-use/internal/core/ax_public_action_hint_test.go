package core

import (
	"reflect"
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

func TestAXCallShapeHintsPreserveIntent(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     string
		path      string
		corrected string
	}{
		{
			name:      "activate mode keeps window and observation",
			input:     `{"mode":"activate","actions":[{"type":"activate","ref":"w7"}],"observe":"full"}`,
			path:      "mode",
			corrected: `{"mode":"act","actions":[{"type":"activate","ref":"w7"}],"observe":"full"}`,
		},
		{
			name:      "activate mode keeps title and later input",
			input:     `{"mode":"activate","window_ref":"w7","actions":[{"type":"activate","title":"Report"},{"type":"type","text":"Quarterly report"}],"observe":"tree"}`,
			path:      "mode",
			corrected: `{"mode":"act","window_ref":"w7","actions":[{"type":"activate","title":"Report"},{"type":"type","text":"Quarterly report"}],"observe":"tree"}`,
		},
		{
			name:      "activate mode also repairs window field",
			input:     `{"mode":"activate","actions":[{"type":"activate","window_ref":"w7"}],"observe":"none"}`,
			path:      "mode",
			corrected: `{"mode":"act","actions":[{"type":"activate","ref":"w7"}],"observe":"none"}`,
		},
		{
			name:      "settle keeps preceding input timeout and following input",
			input:     `{"mode":"act","window_ref":"w7","actions":[{"type":"keypress","keys":["CTRL","O"]},{"type":"settle","timeout_ms":1200},{"type":"type","text":"report.txt"}],"observe":"tree"}`,
			path:      "actions[1].type",
			corrected: `{"mode":"act","window_ref":"w7","actions":[{"type":"keypress","keys":["CTRL","O"]},{"type":"wait","settle":true,"timeout_ms":1200},{"type":"type","text":"report.txt"}],"observe":"tree"}`,
		},
		{
			name:      "settle removes unsupported fields",
			input:     `{"mode":"act","actions":[{"type":"click","ref":"r9"},{"type":"settle","direction":"down","timeout_ms":1500}],"observe":"none"}`,
			path:      "actions[1].type",
			corrected: `{"mode":"act","actions":[{"type":"click","ref":"r9"},{"type":"wait","settle":true,"timeout_ms":1500}],"observe":"none"}`,
		},
		{
			name:      "activate mode and settle repair together",
			input:     `{"mode":"activate","actions":[{"type":"activate","ref":"w7"},{"type":"settle","timeout_ms":1500}],"observe":"full"}`,
			path:      "mode",
			corrected: `{"mode":"act","actions":[{"type":"activate","ref":"w7"},{"type":"wait","settle":true,"timeout_ms":1500}],"observe":"full"}`,
		},
		{
			name:      "double click suggests one invocation on the same ref",
			input:     `{"mode":"act","actions":[{"type":"double_click","ref":"r9"}],"observe":"full"}`,
			path:      "actions[0].type",
			corrected: `{"mode":"act","actions":[{"type":"click","ref":"r9"}],"observe":"full"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := decodeTestArguments(t, test.input)
			original := axCloneValue(args)
			request, err := DecodeAXRequest(args)
			if request != nil || err == nil || err.Code != contract.AXCodeInvalidArgument || err.path != test.path {
				t.Fatalf("want rejection at %s, got request=%#v error=%#v", test.path, request, err)
			}
			if !reflect.DeepEqual(args, original) {
				t.Fatal("hint generation changed the rejected call")
			}
			if hintErr := axHintDecodeError(args, err); hintErr != nil {
				t.Fatalf("hint %s does not decode: %v", err.Hint, hintErr)
			}
			if len(err.Hint) > contract.MaxArgumentHintBytes {
				t.Fatalf("hint exceeds limit: %s", err.Hint)
			}
			corrected, ok := axSpliceHint(args, err.path, err.Hint)
			if !ok || !reflect.DeepEqual(corrected, decodeTestArguments(t, test.corrected)) {
				t.Fatalf("hint %s produces %s; want %s", err.Hint, contract.CompactJSON(corrected), test.corrected)
			}
			if schemaErr := contract.ValidateToolArguments(contract.PublicToolAX, corrected); schemaErr != nil {
				t.Fatalf("corrected call fails public schema: %v", schemaErr)
			}
		})
	}
}

func TestAXDoubleClickHintExplainsGUICallShape(t *testing.T) {
	for _, action := range []string{
		`{"type":"double_click","ref":"r9"}`,
		`{"type":"double_click","x":320,"y":240}`,
	} {
		args := decodeTestArguments(t, `{"mode":"act","actions":[`+action+`]}`)
		_, err := DecodeAXRequest(args)
		if err == nil || err.Code != contract.AXCodeInvalidArgument {
			t.Fatalf("double_click must remain rejected: %#v", err)
		}
		for _, part := range []string{"AX click", "once", "GUI", "actions", `type "double_click"`, "x/y", "full-desktop screenshot"} {
			if !strings.Contains(err.Message, part) {
				t.Errorf("message %q does not explain %q", err.Message, part)
			}
		}
		if hintErr := axHintDecodeError(args, err); hintErr != nil {
			t.Fatalf("hint %s does not decode: %v", err.Hint, hintErr)
		}
	}
	if err := contract.ValidateToolArguments(contract.PublicToolGUI, decodeTestArguments(t,
		`{"actions":[{"type":"double_click","x":320,"y":240}]}`)); err != nil {
		t.Fatalf("suggested GUI call shape is unsupported: %v", err)
	}
}

func TestAXCallShapeHintsRemainValidWithMissingContext(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"act","actions":[{"type":"settle","timeout_ms":1200}]}`,
		`{"mode":"act","actions":[{"type":"wait","text":"Ready"},{"type":"settle"}]}`,
		`{"mode":"act","actions":[{"type":"click","ref":"r9"},{"type":"settle","gone":true,"timeout_ms":"1500"}]}`,
		`{"mode":"activate","actions":[{"type":"activate","title":"` + strings.Repeat("long title ", 40) + `"}]}`,
	} {
		args := decodeTestArguments(t, raw)
		original := axCloneValue(args)
		request, err := DecodeAXRequest(args)
		if request != nil || err == nil || err.Code != contract.AXCodeInvalidArgument {
			t.Fatalf("call must remain rejected: request=%#v error=%#v", request, err)
		}
		if err.Hint == "" || len(err.Hint) > contract.MaxArgumentHintBytes {
			t.Fatalf("hint is empty or exceeds limit: %q", err.Hint)
		}
		if hintErr := axHintDecodeError(args, err); hintErr != nil {
			t.Fatalf("hint %s does not decode: %v", err.Hint, hintErr)
		}
		if !reflect.DeepEqual(args, original) {
			t.Fatal("hint generation changed the rejected call")
		}
	}
}
