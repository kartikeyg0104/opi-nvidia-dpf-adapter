package mapping

import (
	"testing"

	"k8s.io/apimachinery/pkg/labels"
)

func TestNVIDIADPUIsSelectableByHostAgent(t *testing.T) {
	spec, err := LoadFile(mappingDir + "/dataprocessingunit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	selector := labels.SelectorFromSet(labels.Set{
		"provisioning.dpu.nvidia.com/dpunode-name": "worker-a",
	})
	for _, tc := range []struct {
		node    string
		matches bool
	}{
		{"worker-a", true},
		{"worker-b", false},
	} {
		t.Run(tc.node, func(t *testing.T) {
			source := blueFieldSource(nil)
			source["spec"].(map[string]any)["nodeName"] = tc.node
			objects, err := Apply(spec, source)
			if err != nil {
				t.Fatal(err)
			}
			for _, obj := range objects {
				if obj.GetKind() != "DPU" {
					continue
				}
				got := obj.GetLabels()
				if got["provisioning.dpu.nvidia.com/dpunode-name"] != tc.node {
					t.Fatalf("host-agent association label = %q, want %q",
						got["provisioning.dpu.nvidia.com/dpunode-name"], tc.node)
				}
				if selector.Matches(labels.Set(got)) != tc.matches {
					t.Fatalf("host-agent selector chose the wrong node: %v", got)
				}
				if got[LabelMapping] != "dataprocessingunit" ||
					got[LabelSourceKind] != "DataProcessingUnit" ||
					got[LabelSourceName] != obj.GetName() {
					t.Fatalf("DPF association must coexist with translation provenance: %v", got)
				}
				return
			}
			t.Fatal("mapping emitted no DPU")
		})
	}
}
