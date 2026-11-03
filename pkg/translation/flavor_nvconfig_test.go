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

package translation

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

func TestReconcileDPUFlavorHonorsCurrentSFAnnotations(t *testing.T) {
	loaded, err := mapping.LoadDir(filepath.Join("..", "..", "config", "mappings"))
	if err != nil {
		t.Fatal(err)
	}
	var spec *mapping.Spec
	for _, s := range loaded {
		if s.Metadata.Name == "dataprocessingunit" {
			spec = s
			break
		}
	}
	if spec == nil {
		t.Fatal("dataprocessingunit mapping missing")
	}

	flavorGVK := schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUFlavor"}
	bfbGVK := schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "BFB"}
	nvidiaDPU := schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPU"}
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	for _, gvk := range []schema.GroupVersionKind{dpuGVK, devGVK, flavorGVK, bfbGVK, nvidiaDPU} {
		sch.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		sch.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
		metav1.AddToGroupVersion(sch, gvk.GroupVersion())
	}

	src := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"nodeName":       "dh2",
			"dpuProductName": "BlueField-3",
			"isDpuSide":      false,
		},
	}}
	src.SetGroupVersionKind(dpuGVK)
	src.SetName("bf3-worker")
	src.SetUID("dpu-uid-reboot")
	src.SetAnnotations(map[string]string{
		"discovery.opiproject.org/product-name":     "BlueField-3",
		"provisioning.dpu.nvidia.com/serial-number": "MTEXAMPLE0001",
		"provisioning.dpu.nvidia.com/pci-address":   "0000-0f-00",
		"dpu.nvidia.com/bfb-url":                    "https://example.invalid/bf.bfb",
		"dpu.nvidia.com/pf-total-sf":                "32",
		"dpu.nvidia.com/pf-sf-bar-size":             "8",
	})

	cl := fake.NewClientBuilder().WithScheme(sch).WithObjects(src).WithStatusSubresource(src).Build()
	r := &Reconciler{Client: cl, Scheme: sch, Spec: spec}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "bf3-worker"},
	}); err != nil {
		t.Fatal(err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(flavorGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name: "dpf-default-flavor", Namespace: "dpf-operator-system",
	}, got); err != nil {
		t.Fatal(err)
	}
	nv, _, _ := unstructured.NestedSlice(got.Object, "spec", "nvconfig")
	text := fmt.Sprint(nv)
	if !strings.Contains(text, "PF_TOTAL_SF=32") || !strings.Contains(text, "PF_SF_BAR_SIZE=8") {
		t.Fatalf("reconciled DPUFlavor must match current SF annotations, got %s", text)
	}
}
