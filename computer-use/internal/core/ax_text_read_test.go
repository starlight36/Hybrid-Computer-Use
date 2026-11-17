package core

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

func textReadService(t *testing.T) (*Service, *recordingPlatformBackend, string) {
	t.Helper()
	snapshot := windowTransitionSnapshot("w2:0123456789abcdef0123456789abcdef", "Document", true)
	backend := &recordingPlatformBackend{accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot}}
	service := NewServiceWithBackend(backend)
	result := service.callTool("AX", map[string]any{"mode": "observe", "app": "Editor"})
	if result.IsError {
		t.Fatal(ResultText(result))
	}
	ref := service.currentSnapshot("Editor").AXSnapshot.ByIndex[3].Ref
	if ref == "" {
		t.Fatal("setup did not issue a ref")
	}
	backend.calls = nil
	return service, backend, ref
}

func TestTextReadPagesKeepUnicodeAndLiveIdentity(t *testing.T) {
	service, backend, ref := textReadService(t)
	body := strings.Repeat("中🙂e\u0301", 1100)
	runes := []rune(body)
	backend.accessibilityResponses = []ProviderAccessibilityResult{
		{OK: true, TextRead: &ProviderTextPage{Field: "value", Start: 0, End: 4000, Total: len(runes), Text: string(runes[:4000])}},
		{OK: true, TextRead: &ProviderTextPage{Field: "value", Start: 4000, End: len(runes), Total: len(runes), Text: string(runes[4000:])}},
	}
	result := service.callTool("AX", axTextReadCall("", ref, "value", 0, 4000))
	if result.IsError {
		t.Fatal(ResultText(result))
	}
	page := result.StructuredContent["text_page"].(*ProviderTextPage)
	metadata := result.StructuredContent["text_read"].(map[string]any)
	next := metadata["next_read"].(map[string]any)
	result = service.callTool("AX", next)
	if result.IsError {
		t.Fatal(ResultText(result))
	}
	last := result.StructuredContent["text_page"].(*ProviderTextPage)
	if page.Text+last.Text != body || result.StructuredContent["text_read"].(map[string]any)["next_read"] != nil {
		t.Fatal("text paging lost characters or left a false continuation")
	}
	if len(backend.calls) != 2 || len(backend.actionRequests) != 2 {
		t.Fatalf("text reads took unrelated snapshots: %v", backend.calls)
	}
	for _, request := range backend.actionRequests {
		if request.Intent != AccessibilityResolveTarget || request.Target.ProviderObjectPath != "/dialog/3" ||
			request.Target.WindowHandle == "" || request.AppIdentity == nil ||
			request.Observation.CaptureSnapshot == nil || *request.Observation.CaptureSnapshot ||
			request.Observation.IncludeScreenshot == nil || *request.Observation.IncludeScreenshot {
			t.Fatalf("read lost identity or requested side effects: %#v", request)
		}
	}
}

func TestTextReadRecordsUseTheSameBoundedProviderRead(t *testing.T) {
	service, backend, ref := textReadService(t)
	backend.accessibilityResponse = &ProviderAccessibilityResult{OK: true, TextRead: &ProviderTextPage{Field: "value", Start: 0, End: 3, Total: 3, Text: "中🙂e"}}
	record := service.CallRecord("AX", axTextReadCall("", ref, "value", 0, 3), CallOptions{})
	if !record.OK || record.Evidence["text_page"] == nil || len(backend.calls) != 1 || backend.calls[0] != "resolve_and_act" {
		t.Fatalf("records path did not read the same text page: %#v %v", record, backend.calls)
	}
}

func TestTextReadRejectsWrongWindowAndStaleRefBeforeProvider(t *testing.T) {
	service, backend, ref := textReadService(t)
	for _, call := range []map[string]any{
		axTextReadCall("w999", ref, "value", 0, 1),
		axTextReadCall("", "r999", "value", 0, 1),
	} {
		result := service.callTool("AX", call)
		if !result.IsError || len(backend.calls) != 0 {
			t.Fatalf("invalid owner reached provider: %#v %v", result, backend.calls)
		}
	}
}

func TestTextReadRejectsAbsentShortOrMalformedProviderPages(t *testing.T) {
	for _, page := range []*ProviderTextPage{
		nil,
		{Field: "value", Start: 0, End: 0, Total: 5},
		{Field: "value", Start: 0, End: 3, Total: 3, Text: "ab"},
		{Field: "name", Start: 0, End: 3, Total: 3, Text: "abc"},
		{Field: "value", Start: 1, End: 3, Total: 3, Text: "ab"},
	} {
		service, backend, ref := textReadService(t)
		backend.accessibilityResponse = &ProviderAccessibilityResult{OK: true, TextRead: page}
		result := service.callTool("AX", axTextReadCall("", ref, "value", 0, 3))
		if !result.IsError || result.StructuredContent["code"] != contract.AXCodeRuntimeError {
			t.Fatalf("invalid page reported success: %#v", result)
		}
	}
}

func TestTextReadPropagatesProviderFailures(t *testing.T) {
	service, backend, ref := textReadService(t)
	backend.err = errors.New("Text interface read failed")
	result := service.callTool("AX", axTextReadCall("", ref, "value", 0, 2))
	if !result.IsError || !strings.Contains(ResultText(result), "Text interface read failed") {
		t.Fatalf("read error hidden: %#v", result)
	}
	backend.err = nil
	backend.accessibilityResponse = &ProviderAccessibilityResult{OK: false, Error: "stale target", Failure: &ProviderFailure{Code: "stale_target", Phase: "precondition"}}
	result = service.callTool("AX", axTextReadCall("", ref, "value", 0, 2))
	if !result.IsError || axPublicErrorCode(result.StructuredContent) != contract.AXCodeStaleRef {
		t.Fatalf("stale live target hidden: %#v", result)
	}
}

func TestTextReadBudgetDoesNotCutDeliveredPage(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "4096")
	service, backend, ref := textReadService(t)
	body := "[TRUNCATED: this is application text]\n" + strings.Repeat("🙂", 1100)
	runes := []rune(body)
	backend.accessibilityResponse = &ProviderAccessibilityResult{OK: true, TextRead: &ProviderTextPage{
		Field: "value", Start: 0, End: 1024, Total: len(runes), Text: string(runes[:1024]),
	}}
	result := service.callTool("AX", axTextReadCall("", ref, "value", 0, 4000))
	if result.IsError || backend.actionRequests[0].Observation.TextRead.Limit != 1024 ||
		result.StructuredContent["truncation_reason"] == "response_budget" ||
		!strings.Contains(ResultText(result), string(runes[:1024])) || utf8.RuneCountInString(ResultText(result)) > 4096 {
		t.Fatalf("budget damaged page or continuation: %#v", result)
	}
	for _, structured := range []bool{false, true} {
		final := FinalizeForModel("AX", result, structured)
		if !strings.Contains(ResultText(final), string(runes[:1024])) || utf8.RuneCountInString(ResultText(final)) > 4096 {
			t.Fatalf("model finalization damaged text: %#v", final)
		}
		if structured && final.StructuredContent["text_page"] == nil {
			t.Fatal("structured consumers lost the text page")
		}
	}
}

func TestTextReadSurvivesMechanismAblations(t *testing.T) {
	for _, ablation := range []string{"", contract.AblateDiff, contract.AblateBatching, contract.AblateWait} {
		t.Run(ablation, func(t *testing.T) {
			t.Setenv(contract.AblationEnv, ablation)
			request, err := DecodeAXRequest(axTextReadCall("", "r1", "value", 0, 4000))
			if err != nil || request.readText.Limit != 4000 {
				t.Fatalf("read_text was lost in ablation: %v", err)
			}
		})
	}
}

func TestProviderTruncationIsMarkedEvenWhenEllipsisFillsTheGap(t *testing.T) {
	for _, total := range []int{501, 502, 503} {
		record := &ElementRecord{Name: strings.Repeat("x", 500) + "...", TextLengths: map[string]int{"name": total}, TextTruncated: []string{"name"}}
		node := &AXNode{Name: record.Name, Ref: "r1", SourceRecord: record, Properties: AXProperties{TextLengths: axTextLengths(record), TextTruncated: record.TextTruncated}}
		found := false
		for _, property := range renderAXProperties(node, AXRenderOptions{}) {
			if property[0] == "/text-truncated" {
				found = true
				if !strings.Contains(property[1], "read_text") {
					t.Fatal("missing recovery route")
				}
			}
		}
		if !found {
			t.Fatalf("total=%d was silently clipped", total)
		}
	}
}

func TestWhitespaceNormalizationIsNotReportedAsTruncation(t *testing.T) {
	record := &ElementRecord{Value: "hello\n\nworld", TextLengths: map[string]int{"value": 12}}
	node := &AXNode{Role: "paragraph", SourceRecord: record, Properties: AXProperties{Value: "hello world", TextLengths: axTextLengths(record)}}
	for _, property := range renderAXProperties(node, AXRenderOptions{}) {
		if property[0] == "/text-truncated" {
			t.Fatal("whitespace normalization was reported as truncation")
		}
	}
}
