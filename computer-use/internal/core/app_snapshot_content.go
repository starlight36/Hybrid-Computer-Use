package core

import (
	"strings"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

func (snapshot *AppSnapshot) contentUnavailableNote() string {
	if snapshot == nil || snapshot.WindowAbsent ||
		!validInternalWindowHandle(snapshot.WindowHandle) ||
		len(snapshot.Elements) != 1 || snapshot.deliveryOnly || snapshot.lightReadback ||
		snapshot.FindCandidatesOnly || len(snapshot.FindResume) != 0 || snapshot.findResume != nil ||
		snapshot.observationRoot != nil || snapshot.projection.Root != "" ||
		normalizedAXScope(snapshot.Scope) != contract.AXScopeWindow ||
		normalizedAXScope(snapshot.projection.Scope) != contract.AXScopeWindow {
		return ""
	}
	coverageNote := ""
	coverage := providerCoverageSummary(snapshot.Annotations)
	if coverage.ProjectionHidden != 0 || coverage.ExpandableHidden != 0 ||
		coverage.AcquisitionIncomplete != 0 {
		coverageNote = "This read has coverage gaps; omitted content may still exist. "
	}
	root := &snapshot.Elements[0]
	if len(root.RuntimeID) > 1 || len(root.TextTruncated) != 0 ||
		strings.TrimSpace(root.Value) != "" || strings.TrimSpace(root.Description) != "" ||
		strings.TrimSpace(root.Placeholder) != "" || strings.TrimSpace(snapshot.SelectedText) != "" {
		return ""
	}
	switch ontologyProviderRole(root) {
	case "frame", "window", "dialog", "alert":
		return "AX content unavailable in this snapshot: only the top-level window root was returned. " +
			coverageNote +
			"Visible emptiness, renderer crash, and navigation outcome are unknown. " +
			"Inspect a screenshot. Verify the outcome of any prior delete or submit before repeating it. " +
			"If the app supports reloading and it is safe, reload, then observe again."
	default:
		return ""
	}
}
