package translation

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

func TestNVIDIAHostAgentSelectorAfterReconcile(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		assets = getFirstFoundEnvTestBinaryDir()
	}
	if assets == "" {
		t.Skip("envtest binaries are required for the host-agent API integration test")
	}
	env := &envtest.Environment{
		BinaryAssetsDirectory: assets,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "test", "e2e", "crds", "opi"),
			filepath.Join("..", "..", "test", "e2e", "crds", "dpf"),
		},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	spec, err := mapping.LoadFile(filepath.Join("..", "..", "config", "mappings", "dataprocessingunit.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gvk := range []schema.GroupVersionKind{spec.Source.GVK(),
		{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPU"},
	} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
		metav1.AddToGroupVersion(scheme, gvk.GroupVersion())
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName("dpf-operator-system")
	if err := cl.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	src := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"nodeName": "worker-a", "dpuProductName": "BlueField-3", "isDpuSide": false},
	}}
	src.SetGroupVersionKind(spec.Source.GVK())
	src.SetName("selector-test")
	src.SetAnnotations(map[string]string{
		"provisioning.dpu.nvidia.com/serial-number": "MTTEST00001",
		"discovery.opiproject.org/product-name":     "BlueField-3",
		"provisioning.dpu.nvidia.com/pci-address":   "0000-03-00",
		"dpu.nvidia.com/bfb-url":                    "https://example.invalid/test.bfb",
	})
	if err := cl.Create(ctx, src); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: cl, Scheme: scheme, Spec: spec}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: src.GetName()}}
	var previous *unstructured.Unstructured
	for attempt := range 3 {
		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
		selected := &unstructured.UnstructuredList{}
		selected.SetAPIVersion("provisioning.dpu.nvidia.com/v1alpha1")
		selected.SetKind("DPUList")
		if err := cl.List(ctx, selected, client.InNamespace("dpf-operator-system"), client.MatchingLabels{
			"provisioning.dpu.nvidia.com/dpunode-name": "worker-a",
		}); err != nil {
			t.Fatal(err)
		}
		if len(selected.Items) != 1 || selected.Items[0].GetName() != "selector-test" {
			t.Fatalf("reconcile %d: host-agent selected %d DPUs, want exactly selector-test", attempt, len(selected.Items))
		}
		got := selected.Items[0].DeepCopy()
		if previous != nil && (got.GetUID() != previous.GetUID() ||
			!reflect.DeepEqual(got.Object["spec"], previous.Object["spec"])) {
			t.Fatal("reconcile replaced the DPU or changed its specification")
		}
		if previous != nil && got.GetLabels()["example.test/vendor-label"] != "preserve" {
			t.Fatal("reconcile removed a label owned by another controller")
		}
		previous = got.DeepCopy()
		original := got.DeepCopy()
		labels := got.GetLabels()
		labels["example.test/vendor-label"] = "preserve"
		got.SetLabels(labels)
		if err := cl.Patch(ctx, got, client.MergeFrom(original)); err != nil {
			t.Fatal(err)
		}
	}
	assertIneligibleCardPreservesDPU(t, cl, r, src, previous)
}

func assertIneligibleCardPreservesDPU(t *testing.T, cl client.Client, r *Reconciler,
	src, previous *unstructured.Unstructured) {
	t.Helper()
	ctx := context.Background()
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(src)}
	// A card replacement must stop new provisioning without deleting the old
	// children or overwriting the daemon's inventory condition.
	if err := cl.Get(ctx, request.NamespacedName, src); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(src.Object, "BlueField-2", "spec", "dpuProductName")
	annotations := src.GetAnnotations()
	annotations["discovery.opiproject.org/product-name"] = "BlueField-2"
	src.SetAnnotations(annotations)
	if err := cl.Update(ctx, src); err != nil {
		t.Fatal(err)
	}
	ready := map[string]any{"type": "Ready", "status": "True", "reason": "DeviceHealthy",
		"message": "Inventory remains available", "lastTransitionTime": "2026-10-28T00:00:00Z"}
	src.Object["status"] = map[string]any{"conditions": []any{ready}}
	if err := cl.Status().Update(ctx, src); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	preserved := &unstructured.Unstructured{}
	preserved.SetGroupVersionKind(previous.GroupVersionKind())
	if err := cl.Get(ctx, client.ObjectKeyFromObject(previous), preserved); err != nil {
		t.Fatal(err)
	}
	if preserved.GetUID() != previous.GetUID() || !reflect.DeepEqual(preserved.Object["spec"], previous.Object["spec"]) {
		t.Fatal("unsupported hardware transition replaced or changed the existing DPU")
	}
	if err := cl.Get(ctx, request.NamespacedName, src); err != nil {
		t.Fatal(err)
	}
	conditions, _, _ := unstructured.NestedSlice(src.Object, "status", "conditions")
	seen := map[string]bool{}
	for _, raw := range conditions {
		condition := raw.(map[string]any)
		name, _ := condition["type"].(string)
		seen[name] = true
		if name == "Ready" && !reflect.DeepEqual(condition, ready) {
			t.Fatalf("inventory Ready changed: %v", condition)
		}
		if name == "DPFReady" || name == "DPFHardwareCompatible" {
			if condition["status"] != "False" || condition["reason"] != "UnsupportedHardware" {
				t.Fatalf("ineligible hardware condition: %v", condition)
			}
		}
	}
	for _, name := range []string{"Ready", "DPFReady", "DPFHardwareCompatible"} {
		if !seen[name] {
			t.Fatalf("API did not retain condition %s: %v", name, conditions)
		}
	}
}
