package mapping

import (
	"path/filepath"
	"testing"
)

// The API server supplies ObjectMeta fields omitted by controller-gen's CRD
// schema. Rejecting them blocks the label needed by the DPF host-agent.
func TestSchemaHasPathRecognizesImplicitMetadataMaps(t *testing.T) {
	pin := loadPinnedVersions(t)
	index := loadCRDIndex(t, filepath.Join(repoRoot(t), pin.DPF.Fixtures))
	root := index["provisioning.dpu.nvidia.com/DPU"]
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"metadata.labels", true},
		{"metadata.annotations", true},
		{"metadata.lables", false},
		{"metadata.labels[]", false},
		{"metadata.annotations[]", false},
		{"spec.metadata.labels", false},
		{"spec.dpuNodeName", true},
		{"spec.madeUp", false},
		{"status.phase", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := SchemaHasPath(root, tc.path); got != tc.want {
				t.Fatalf("SchemaHasPath(DPU, %q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
	if SchemaHasPath(nil, "metadata.labels") {
		t.Fatal("a missing schema must not accept implicit metadata")
	}
}
