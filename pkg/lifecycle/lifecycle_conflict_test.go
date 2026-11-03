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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const concurrentReconcileUsers = 32

func TestConcurrentReconcileSameConfigSucceeds(t *testing.T) {
	sch := lcmScheme()
	obj := newConfig("dpu-operator-config", map[string]any{"vendor": "nvidia"})
	obj.SetAnnotations(map[string]string{VersionAnnotation: NVIDIAChartVersion})
	if err := unstructured.SetNestedSlice(obj.Object, []any{
		map[string]any{
			"type":               "Ready",
			"status":             "True",
			"reason":             "Daemon",
			"message":            "daemon owns Ready",
			"lastTransitionTime": "2026-01-01T00:00:00Z",
		},
	}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(obj).WithStatusSubresource(obj).Build()
	r := &LifecycleManager{Client: cl, Scheme: sch, Probe: ReadyClusterProbe()}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "dpu-operator-config"}}

	var errN, panicN int32
	var wg sync.WaitGroup
	wg.Add(concurrentReconcileUsers)
	for range concurrentReconcileUsers {
		go func() {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					atomic.AddInt32(&panicN, 1)
				}
			}()
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				atomic.AddInt32(&errN, 1)
			}
		}()
	}
	wg.Wait()
	if panicN != 0 || errN != 0 {
		t.Fatalf("concurrent Reconcile: err=%d panic=%d, want 0 (409 must retry)", errN, panicN)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(DpuOperatorConfigGVK)
	if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(got, CleanupFinalizer) {
		t.Fatal("finalizer missing after concurrent Reconcile")
	}
	if c := conditionOfType(t, got, SupportedConditionType); c == nil {
		t.Fatal("VendorOperatorSupported missing")
	}
	ready := conditionOfType(t, got, "Ready")
	if ready == nil || ready["reason"] != "Daemon" {
		t.Fatalf("daemon Ready overwritten: %v", ready)
	}
}

func TestConcurrentCleanupReconcileSameConfigSucceeds(t *testing.T) {
	sch := lcmScheme()
	obj := newConfig("dpu-operator-config", map[string]any{"vendor": "nvidia"})
	now := metav1.Now()
	obj.SetDeletionTimestamp(&now)
	obj.SetFinalizers([]string{CleanupFinalizer})
	cl := fake.NewClientBuilder().WithScheme(sch).
		WithObjects(obj).WithStatusSubresource(obj).Build()
	r := &LifecycleManager{
		Client: cl, Scheme: sch, Probe: ReadyClusterProbe(),
		Index: &MemoryResourceIndex{}, Helm: &MemoryHelmClient{},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "dpu-operator-config"}}

	var errN int32
	var wg sync.WaitGroup
	wg.Add(concurrentReconcileUsers)
	for range concurrentReconcileUsers {
		go func() {
			defer wg.Done()
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				if client.IgnoreNotFound(err) != nil {
					atomic.AddInt32(&errN, 1)
				}
			}
		}()
	}
	wg.Wait()
	if errN != 0 {
		t.Fatalf("concurrent cleanup Reconcile: err=%d, want 0", errN)
	}
}

func TestMainDefaultsLeaderElect(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "cmd", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `flag.BoolVar(&enableLeaderElection, "leader-elect", false`) {
		t.Fatal("cmd/main.go must default --leader-elect to false; multi-replica deployments must enable it explicitly")
	}
}

func TestSetupWithManagerPinsOneWorker(t *testing.T) {
	b, err := os.ReadFile("lifecycle_controller.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "MaxConcurrentReconciles") {
		t.Fatal("SetupWithManager must pin MaxConcurrentReconciles so a raised default cannot fan-out writes")
	}
	if !strings.Contains(src, "Watches") || !strings.Contains(src, "nvidiaProbeGVK") {
		t.Fatal("SetupWithManager must watch NVIDIA DPU so Rebooting is not only a 5s timer")
	}
}
