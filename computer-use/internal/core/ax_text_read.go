package core

import (
	"fmt"
	"runtime"
	"unicode/utf8"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

func decodeAXTextRead(raw any) (*ProviderTextRead, *axArgError) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, axInvalid("read_text", "read_text must be an object", "").withFix(axSet("read_text", map[string]any{"field": "value"}))
	}
	if err := axCheckFields(object, []string{"field", "start", "limit"}, "read_text", "read_text"); err != nil {
		return nil, err
	}
	field, err := axOptionalEnum(object, "field", "read_text", []string{"name", "value", "description"})
	if err != nil {
		return nil, err
	}
	if field == "" {
		return nil, axInvalid("read_text.field", "read_text.field is required", "").withFix(axSet("read_text.field", "value"))
	}
	read := &ProviderTextRead{Field: field, Limit: contract.AXMaxReadText}
	for _, item := range []struct {
		name    string
		minimum int
		maximum int
		target  *int
	}{{"start", 0, int(^uint32(0) >> 1), &read.Start}, {"limit", 1, contract.AXMaxReadText, &read.Limit}} {
		if value, present := object[item.name]; present {
			number, valid := axInteger(value)
			if !valid || number < item.minimum || number > item.maximum {
				path := "read_text." + item.name
				return nil, axInvalid(path, fmt.Sprintf("%s must be an integer from %d to %d", path, item.minimum, item.maximum), "").withFix(axNumberEdit(path, value, float64(item.minimum), float64(item.maximum), true))
			}
			*item.target = number
		}
	}
	return read, nil
}

func axTextReadError(message string) ToolCallResult {
	result := TextResult(message, true)
	result.StructuredContent = map[string]any{
		"code": contract.AXCodeRuntimeError, "message": message,
		"input_events_emitted": false, "desktop_state_mutated": false,
		"mutation_unknown": false, "evidence_scope": "error_only",
	}
	return result
}

func (s *Service) axReadText(request *axRequest) ToolCallResult {
	if runtime.GOOS != "linux" {
		return axTextReadError("read_text is supported only by the Linux provider")
	}
	identity, err := s.resolveAXReadIdentity(request.app, request.windowRef, request.root)
	if err != nil {
		return axIdentityErrorResult(err)
	}
	entry, err := s.resolveShortAXRefOwned(request.root)
	if err != nil {
		return axIdentityErrorResult(err)
	}
	target, err := semanticTargetFromRefEntry(entry)
	if err != nil {
		return axStaleRefResult(request.root, err)
	}
	read := *request.readText
	available := MaxInlineChars() - 3072
	if available < 1 {
		return axTextReadError("read_text needs COMPUTER_USE_MAX_INLINE_CHARS above 3072 to deliver a text page and its continuation")
	}
	if read.Limit > available {
		read.Limit = available
	}
	no := false
	operation := AXServiceRequest{
		Operation: axServiceObserve, App: identity.appToken,
		TargetWindowHandle: identity.windowHandle, SemanticTarget: target,
		SourceRef: request.root, Action: axResolveTargetCommand{},
		TextRead: &read, CaptureSnapshot: &no, IncludeScreenshot: &no,
	}
	if err := s.bindRuntimeAppIdentity(&operation); err != nil {
		return axIdentityErrorResult(err)
	}
	response, err := s.callProviderAction(providerActionRequestFromOperation(operation))
	if err != nil {
		return axTextReadError(err.Error())
	}
	if !response.OK {
		return s.runtimeReportedFailure(identity.appToken, operation, *response)
	}
	page := response.TextRead
	if page == nil || page.Field != read.Field || page.Start != read.Start ||
		page.End < page.Start || page.End > page.Total ||
		page.End-page.Start > read.Limit || !utf8.ValidString(page.Text) ||
		utf8.RuneCountInString(page.Text) != page.End-page.Start ||
		(page.End < page.Total && page.End-page.Start != read.Limit) {
		return axTextReadError("the provider did not return a complete, valid text page")
	}
	metadata := map[string]any{
		"ref": request.root, "window_ref": identity.windowRef,
		"field": page.Field, "start": page.Start, "end": page.End, "total": page.Total,
		"truncated": page.End < page.Total,
	}
	if page.End < page.Total {
		metadata["next_read"] = axTextReadCall(identity.windowRef, request.root, page.Field, page.End, request.readText.Limit)
	}
	result := TextResult(contract.CompactJSON(metadata)+"\n"+page.Text, false)
	result.StructuredContent = map[string]any{
		"text_page": page, "text_read": metadata,
		"input_events_emitted": false, "desktop_state_mutated": false,
		"mutation_unknown": false, "evidence_scope": "independent_ui_read",
	}
	return result
}

func axTextReadCall(window, ref, field string, start, limit int) map[string]any {
	call := map[string]any{
		"mode": "observe", "root": ref,
		"read_text": map[string]any{"field": field, "start": start, "limit": limit},
	}
	if window != "" {
		call["window_ref"] = window
	}
	return call
}

func axTextLengths(record *ElementRecord) map[string]int {
	lengths := make(map[string]int, 3)
	for field, value := range map[string]string{"name": record.Name, "value": record.Value, "description": record.Description} {
		length := utf8.RuneCountInString(value)
		if full, known := record.TextLengths[field]; known {
			length = full
		}
		if length > 0 {
			lengths[field] = length
		}
	}
	return lengths
}

func axTextPreviewTruncated(node *AXNode, field, preview string) bool {
	if contract.ContainsString(node.Properties.TextTruncated, field) {
		return true
	}
	original := ""
	if record := node.SourceRecord; record != nil {
		switch field {
		case "name":
			original = record.Name
		case "value":
			original = record.Value
		case "description":
			original = record.Description
		}
	} else {
		switch field {
		case "name":
			original = node.Name
		case "value":
			original = node.Properties.Value
		case "description":
			original = node.Properties.Description
		}
	}
	return utf8.RuneCountInString(normalizedTreeText(original)) > utf8.RuneCountInString(preview)
}
