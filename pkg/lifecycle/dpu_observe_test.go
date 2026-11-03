/*
Copyright 2026 Kartikey Gupta.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lifecycle

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func dpuScheme() *runtime.Scheme {
	s := lcmScheme()
	s.AddKnownTypeWithName(nvidiaProbeGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		nvidiaProbeGVK.GroupVersion().WithKind(dpuKind+"List"),
		&unstructured.UnstructuredList{},
	)
	metav1.AddToGroupVersion(s, nvidiaProbeGVK.GroupVersion())
	_ = clientgoscheme.AddToScheme(s)
	return s
}

const testDPUName = "dh2-bluefield-2"

func nvidiaDPU(phase string, powerCycle bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"phase": phase},
	}}
	u.SetGroupVersionKind(nvidiaProbeGVK)
	u.SetNamespace(DefaultNamespace)
	u.SetName(testDPUName)
	if powerCycle {
		u.SetAnnotations(map[string]string{hostPowerCycleRequiredKey: "true"})
	}
	return u
}

func TestObserveNVIDIADPUsRebootingIsNotOperatorFailure(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(dpuScheme()).
		WithObjects(nvidiaDPU(DPUPhaseRebooting, true)).Build()
	got := ObserveNVIDIADPUs(context.Background(), cl)
	if got.Ready || got.Phase != DPUPhaseRebooting || len(got.Rebooting) != 1 {
		t.Fatalf("rebooting DPU = %+v", got)
	}
	if !strings.Contains(got.Message, "mlxfwreset") {
		t.Fatalf("message must say this adapter does not reset: %s", got.Message)
	}
	st, reason, _ := dpuCondition(got)
	if st != string(metav1.ConditionFalse) || reason != DPUPhaseRebooting {
		t.Fatalf("condition = %s/%s", st, reason)
	}
}

func TestObserveNVIDIADPUsReadyWhenNotRebooting(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(dpuScheme()).
		WithObjects(nvidiaDPU("Ready", false)).Build()
	got := ObserveNVIDIADPUs(context.Background(), cl)
	if !got.Ready || len(got.Rebooting) != 0 {
		t.Fatalf("ready DPU = %+v", got)
	}
}

func TestObserveNVIDIADPUsReadyOverridesHistoricalResetMethod(t *testing.T) {
	dpu := nvidiaDPU("Ready", false)
	_ = unstructured.SetNestedField(dpu.Object, rebootMethodSystemReset,
		"status", "agentStatus", "rebootMethod")
	cl := fake.NewClientBuilder().WithScheme(dpuScheme()).WithObjects(dpu).Build()
	got := ObserveNVIDIADPUs(context.Background(), cl)
	if !got.Ready || len(got.Rebooting) != 0 {
		t.Fatalf("completed provisioning must not be blocked by the last reset discovery: %+v", got)
	}
}

func TestObserveNVIDIADPUsEmptyIsUnknown(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(dpuScheme()).Build()
	got := ObserveNVIDIADPUs(context.Background(), cl)
	if got.Ready || got.Phase != "Unknown" {
		t.Fatalf("empty list = %+v", got)
	}
}

func TestObserveNVIDIADPUsNilClient(t *testing.T) {
	got := ObserveNVIDIADPUs(context.Background(), nil)
	if got.Ready {
		t.Fatal("nil client must not report Ready")
	}
}

func TestObserveNVIDIADPUsPowerCycleAnnotationStalls(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(dpuScheme()).
		WithObjects(nvidiaDPU("Ready", true)).Build()
	got := ObserveNVIDIADPUs(context.Background(), cl)
	if got.Ready || !dpuNeedsRequeue(got) {
		t.Fatalf("power-cycle annotation = %+v", got)
	}
}

func TestDPURequeueAfterIsFiveSeconds(t *testing.T) {
	if DPURequeueAfter != 5*time.Second {
		t.Fatalf("DPURequeueAfter = %s, want 5s so VendorDPUReady tracks DPF without a 30s dead-time", DPURequeueAfter)
	}
}

func TestObserveNVIDIADPUsReportsPendingNvconfig(t *testing.T) {
	dpu := nvidiaDPU(DPUPhaseRebooting, true)
	_ = unstructured.SetNestedSlice(dpu.Object, []any{
		map[string]any{
			"device": "/dev/mst/mt41686_pciconf0",
			"entries": []any{
				map[string]any{"name": "PF_SF_BAR_SIZE", "current": "8", "next_boot": "0"},
				map[string]any{"name": "PF_TOTAL_SF", "current": "32", "next_boot": "0"},
			},
		},
	}, "status", "agentStatus", "lastObservedPendingNvconfig", "devices")
	cl := fake.NewClientBuilder().WithScheme(dpuScheme()).WithObjects(dpu).Build()
	got := ObserveNVIDIADPUs(context.Background(), cl)
	if got.Ready ||
		!strings.Contains(got.Pending, "PF_SF_BAR_SIZE 8→0") ||
		!strings.Contains(got.Pending, "PF_TOTAL_SF 32→0") {
		t.Fatalf("pending NVCONFIG = %+v", got)
	}
	if !strings.Contains(got.Message, "PF_SF_BAR_SIZE 8→0") {
		t.Fatalf("message must surface pending NVCONFIG: %s", got.Message)
	}
}

func TestObserveNVIDIADPUsReadsSystemLevelReset(t *testing.T) {
	dpu := nvidiaDPU(DPUPhaseRebooting, true)
	_ = unstructured.SetNestedField(dpu.Object, rebootMethodSystemReset, "status", "agentStatus", "rebootMethod")
	cl := fake.NewClientBuilder().WithScheme(dpuScheme()).WithObjects(dpu).Build()
	got := ObserveNVIDIADPUs(context.Background(), cl)
	if got.Ready || got.Method != rebootMethodSystemReset {
		t.Fatalf("agent reboot method = %+v", got)
	}
	if !strings.Contains(got.Message, "dpf-operator-system/dh2-bluefield-2") {
		t.Fatalf("message must name the DPU: %s", got.Message)
	}
}

func TestObserveNVIDIADPUsDoesNotMutate(t *testing.T) {
	dpu := nvidiaDPU(DPUPhaseRebooting, true)
	inner := fake.NewClientBuilder().WithScheme(dpuScheme()).WithObjects(dpu).Build()
	cl := &countingUpdateClient{Client: inner}
	_ = ObserveNVIDIADPUs(context.Background(), cl)
	if cl.updates.Load() != 0 {
		t.Fatalf("observer wrote DPU updates=%d", cl.updates.Load())
	}
}

func TestDPUObserveSourceHasNoReset(t *testing.T) {
	b, err := os.ReadFile("dpu_observe.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, "exec.Command") || strings.Contains(src, "os/exec") {
		t.Fatal("observer must not shell out")
	}
	if !strings.Contains(src, "does not run mlxfwreset") {
		t.Fatal("observer must say it does not run mlxfwreset")
	}
}

func TestReconcileWritesVendorDPUReadyOnReboot(t *testing.T) {
	sch := dpuScheme()
	cfg := configWithVersion(VendorNVIDIA, NVIDIAChartVersion)
	dpu := nvidiaDPU(DPUPhaseRebooting, true)
	cl := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(cfg, dpu).WithStatusSubresource(cfg).Build()
	r := &LifecycleManager{Client: cl, Scheme: sch, Probe: ReadyClusterProbe()}
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "cfg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != DPURequeueAfter {
		t.Fatalf("RequeueAfter = %s, want %s while DPU is Rebooting", res.RequeueAfter, DPURequeueAfter)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(DpuOperatorConfigGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cfg"}, got); err != nil {
		t.Fatal(err)
	}
	cond := conditionOfType(t, got, DPUReadyConditionType)
	if cond == nil || cond["status"] != "False" || cond["reason"] != DPUPhaseRebooting {
		t.Fatalf("VendorDPUReady = %v", cond)
	}
	if ready := conditionOfType(t, got, OperatorReadyConditionType); ready == nil || ready["status"] != "True" {
		t.Fatalf("DPU reboot must not flip VendorOperatorReady: %v", ready)
	}
	if c := conditionOfType(t, got, "Ready"); c != nil {
		t.Fatalf("must not write daemon Ready: %v", c)
	}
}
