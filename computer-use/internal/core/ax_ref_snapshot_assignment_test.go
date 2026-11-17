package core

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/Hybrid-Computer-Use/computer-use/internal/contract"
)

func modalRefSnapshot(revision string, records ...ElementRecord) *AppSnapshot {
	for index := range records {
		records[index].Index = index + 1
		records[index].ProviderBusName = ":1.42"
		records[index].ProcessGeneration = "epoch1"
		records[index].ProviderWindowBusName = ":1.42"
		records[index].ProviderWindowObjectPath = "/window"
		records[index].ProviderWindowProcessGeneration = "epoch1"
	}
	return &AppSnapshot{
		App:          AppDescriptor{Name: "Messages"},
		WindowHandle: "w2:" + strings.Repeat("a", 32),
		SnapshotID:   revision, Scope: contract.AXScopeWindow,
		publicAppRef: "a1", publicWindowRef: "w1", Elements: records,
	}
}

func assignModalRefSnapshot(t *testing.T, service *Service, source *AppSnapshot) *AXSnapshot {
	t.Helper()
	tree, err := buildAXSnapshot(source)
	if err != nil {
		t.Fatal(err)
	}
	service.RefRegistry.AssignSnapshotRefs(tree)
	source.AXSnapshot = tree
	service.rememberSnapshot("messages", source)
	return tree
}

func TestModalScopePreservesRefUntilCompleteRead(t *testing.T) {
	payload, err := os.ReadFile("../../testdata/provider-wire/modal-scope.response.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Accessibility struct {
			Snapshot AppSnapshot `json:"snapshot"`
		} `json:"accessibility"`
	}
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"same_path", "moved", "renamed", "ambiguous", "absent", "window_replaced"} {
		t.Run(outcome, func(t *testing.T) {
			service := NewService()
			button := ElementRecord{
				RuntimeID: []int{0, 1}, ControlType: "push button", Name: "Invoice",
				ProviderObjectPath: "/old/invoice", Actions: []string{"click"},
			}
			before := assignModalRefSnapshot(t, service, modalRefSnapshot("before", button))
			ref := before.ByIndex[1].Ref
			entry, err := service.RefRegistry.Resolve(ref, "messages")
			if err != nil || entry.LocatorKind != AXLocatorSemantic {
				t.Fatalf("initial semantic ref: %v, %#v", err, entry)
			}
			modal := modalRefSnapshot("modal", ElementRecord{
				RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Close",
				ProviderObjectPath: "/dialog/close", Actions: []string{"click"},
			})
			modal.Annotations = fixture.Accessibility.Snapshot.Annotations
			current := assignModalRefSnapshot(t, service, modal)
			if current.RefInventoryComplete || current.AcquisitionIncomplete || !current.IdentityCoverageIncomplete {
				t.Fatalf("modal is a coverage boundary, not a failed read: %#v", current)
			}
			entry, err = service.RefRegistry.Resolve(ref, "messages")
			if err != nil || entry.LastSeenRevision != "before" {
				t.Fatalf("modal retired or refreshed the hidden ref: %v, %#v", err, entry)
			}
			target, err := semanticTargetFromRefEntry(entry)
			if err != nil {
				t.Fatal(err)
			}
			service.authorizeProviderRebind("messages", entry, target)
			if target.AllowProviderRebind {
				t.Fatal("hidden target allowed semantic rebind inside modal scope")
			}
			button.ProviderObjectPath = "/rebuilt/invoice"
			after := modalRefSnapshot("after", button)
			switch outcome {
			case "moved":
				after.Elements[0].RuntimeID = []int{0, 2}
			case "renamed":
				after.Elements[0].Name = "Delete"
			case "ambiguous":
				peer := after.Elements[0]
				peer.Index, peer.RuntimeID, peer.ProviderObjectPath = 2, []int{0, 2}, "/peer/invoice"
				after.Elements = append(after.Elements, peer)
			case "absent":
				after.Elements = nil
			case "window_replaced":
				after.Elements[0].ProviderWindowObjectPath = "/new/window"
			}
			current = assignModalRefSnapshot(t, service, after)
			entry, err = service.RefRegistry.Resolve(ref, "messages")
			if outcome != "same_path" {
				if err == nil {
					t.Fatalf("%s revived the old ref", outcome)
				}
				return
			}
			if err != nil || current.ByIndex[1].Ref != ref || entry.ProviderObjectPath != "/old/invoice" {
				t.Fatalf("same-path rebuild lost ref or skipped live validation: %v, %#v", err, entry)
			}
			service.authorizeProviderRebind("messages", entry, target)
			if !target.AllowProviderRebind {
				t.Fatal("complete fresh snapshot did not allow guarded live rebind")
			}
		})
	}
}
