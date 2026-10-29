package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

// Exercise the deployment artifacts through YAML decoding and RFC6902 patching,
// then evaluate their label requirements, including OR between selector terms.
func profilePod(t *testing.T, file, patch string) corev1.PodSpec {
	t.Helper()
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		j, err := yaml.YAMLToJSON(b)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	b := read(file)
	if patch != "" {
		p, err := jsonpatch.DecodePatch(read(patch))
		if err != nil {
			t.Fatal(err)
		}
		b, err = p.Apply(b)
		if err != nil {
			t.Fatal(err)
		}
	}
	var ds appsv1.DaemonSet
	if err := json.Unmarshal(b, &ds); err != nil {
		t.Fatal(err)
	}
	return ds.Spec.Template.Spec
}

func profileSelects(t *testing.T, pod corev1.PodSpec, node labels.Set) bool {
	t.Helper()
	if !labels.SelectorFromSet(pod.NodeSelector).Matches(node) {
		return false
	}
	if pod.Affinity == nil || pod.Affinity.NodeAffinity == nil ||
		pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return true
	}
	for _, term := range pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		if len(term.MatchFields) != 0 {
			t.Fatal("profile must use hardware labels, not node names")
		}
		if len(term.MatchExpressions) == 0 {
			continue
		}
		selector := &metav1.LabelSelector{}
		for _, req := range term.MatchExpressions {
			selector.MatchExpressions = append(selector.MatchExpressions, metav1.LabelSelectorRequirement{
				Key: req.Key, Operator: metav1.LabelSelectorOperator(req.Operator), Values: req.Values,
			})
		}
		match, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			t.Fatal(err)
		}
		if match.Matches(node) {
			return true
		}
	}
	return false
}

func TestHardwareDeploymentsSelectOnlyBlueFieldPCIFunctions(t *testing.T) {
	vsp := profilePod(t, "config/vsp/daemonset.yaml", "config/overlays/hardware-vsp/pci-patch.yaml")
	daemon := profilePod(t, "config/opi-daemon/daemonset.yaml", "")
	for _, tc := range []struct {
		name, key, value string
		want             bool
	}{
		{"bf2-a2d2", "pci-0200_15b3_a2d2.present", "true", true},
		{"bf2-a2d6", "pci-0200_15b3_a2d6.present", "true", true},
		{"bf3-a2dc", "pci-0200_15b3_a2dc.present", "true", true},
		{"bf3-a2dd", "pci-0200_15b3_a2dd.present", "true", true},
		{"connectx6", "pci-0200_15b3_101d.present", "true", false},
		{"vendor-alone", "pci-15b3.present", "true", false},
		{"management-function", "pci-0200_15b3_c2d3.present", "true", false},
		{"amd", "pci-0200_1dd8_1002.present", "true", false},
		{"marvell", "pci-0200_177d_b900.present", "true", false},
		{"false-label", "pci-0200_15b3_a2dc.present", "false", false},
		{"unlabeled", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := labels.Set{}
			if tc.key != "" {
				node["feature.node.kubernetes.io/"+tc.key] = tc.value
			}
			if tc.want {
				// Installation enrolls a host only after checking its real PCI IDs.
				node["hardware.opiproject.org/profile"] = "nvidia-bluefield"
			}
			for name, pod := range map[string]corev1.PodSpec{"vsp": vsp, "daemon": daemon} {
				if got := profileSelects(t, pod, node); got != tc.want {
					t.Errorf("%s selects %s = %v, want %v", name, tc.name, got, tc.want)
				}
			}
		})
	}
	if !slices.Contains(vsp.Containers[0].Args, "--pci-vendor=nvidia") {
		t.Error("hardware firmware deployment does not restrict inventory on mixed vendor hosts")
	}
	mock := profilePod(t, "config/vsp/daemonset.yaml", "")
	if !profileSelects(t, mock, labels.Set{}) || len(mock.Containers) != 2 {
		t.Fatal("Kind mock must retain its sidecar and work without hardware labels")
	}
}

func TestEnrolledHardwareProfileSurvivesLastCardRemoval(t *testing.T) {
	for _, tc := range []struct{ file, patch string }{
		{"config/vsp/daemonset.yaml", "config/overlays/hardware-vsp/pci-patch.yaml"},
		{"config/opi-daemon/daemonset.yaml", ""},
	} {
		pod := profilePod(t, tc.file, tc.patch)
		// NFD removes transient PCI labels after unplug/reboot. Inventory must
		// still start on the explicitly enrolled host to clear stale identities.
		if !profileSelects(t, pod, labels.Set{"hardware.opiproject.org/profile": "nvidia-bluefield"}) {
			t.Errorf("%s cannot clean inventory after the last card disappears", tc.file)
		}
		if profileSelects(t, pod, labels.Set{
			"hardware.opiproject.org/profile":                       "amd-pensando",
			"feature.node.kubernetes.io/pci-0200_15b3_a2dc.present": "true",
		}) {
			t.Errorf("%s ignored explicit vendor profile ownership", tc.file)
		}
	}
}
