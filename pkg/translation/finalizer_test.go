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
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

func plainScheme(gvks ...schema.GroupVersionKind) *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	for _, g := range gvks {
		s.AddKnownTypeWithName(g, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(g.GroupVersion().WithKind(g.Kind+"List"), &unstructured.UnstructuredList{})
		metav1.AddToGroupVersion(s, g.GroupVersion())
	}
	return s
}

// TestFinalizerAndCleanup verifies the Track B path with a fake client: the
// reconciler stamps the cleanup finalizer, a cross-namespace child is
// annotation-tracked (no owner ref), and cleanupAnnotatedChildren deletes it.
func TestFinalizerAndCleanup(t *testing.T) {
	sfc := schema.GroupVersionKind{Group: "config.openshift.io", Version: "v1", Kind: "ServiceFunctionChain"}
	svc := schema.GroupVersionKind{Group: "svc.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUService"}
	sch := plainScheme(sfc, svc)

	spec := &mapping.Spec{
		APIVersion: mapping.APIVersion,
		Kind:       mapping.Kind,
		Metadata:   mapping.Metadata{Name: "sfc-xns"},
		Source:     mapping.ObjectRef{Group: "config.openshift.io", Version: "v1", Kind: "ServiceFunctionChain"},
		Emit: []mapping.Emit{{
			Target:    mapping.ObjectRef{Group: "svc.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUService"},
			ForEach:   &mapping.ForEach{In: "spec.networkFunctions"},
			When:      "'chart' in item",
			Name:      mapping.Value{CEL: "item.name"},
			Namespace: mapping.Value{Value: "dpf-operator-system"},
			Fields:    []mapping.Field{{To: "spec.serviceID", From: "item.name", Required: true}},
		}},
	}

	src := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"networkFunctions": []any{
			map[string]any{"name": "hbn", "chart": map[string]any{"repository": "r", "name": "hbn", "version": "v1"}},
		}},
	}}
	src.SetGroupVersionKind(sfc)
	src.SetName("xns-chain")
	src.SetNamespace("opi")
	src.SetUID("sfc-uid")

	cl := fake.NewClientBuilder().WithScheme(sch).WithObjects(src).Build()
	r := &Reconciler{Client: cl, Scheme: sch, Spec: spec}
	ctx := context.Background()
	srcNN := types.NamespacedName{Name: "xns-chain", Namespace: "opi"}
	childNN := types.NamespacedName{Name: "hbn", Namespace: "dpf-operator-system"}

	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: srcNN}); err != nil {
		t.Fatal(err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(sfc)
	if err := cl.Get(ctx, srcNN, got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(got, CleanupFinalizer) {
		t.Fatalf("cleanup finalizer not stamped: %v", got.GetFinalizers())
	}

	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(svc)
	if err := cl.Get(ctx, childNN, child); err != nil {
		t.Fatalf("cross-namespace child not created: %v", err)
	}
	if len(child.GetOwnerReferences()) != 0 {
		t.Errorf("cross-namespace child must have no owner ref, got %v", child.GetOwnerReferences())
	}
	if v := child.GetAnnotations()[AnnSource]; v != "ServiceFunctionChain:opi/xns-chain" {
		t.Errorf("annSource=%q, want ServiceFunctionChain:opi/xns-chain", v)
	}

	if err := r.cleanupAnnotatedChildren(ctx, got); err != nil {
		t.Fatal(err)
	}
	gone := &unstructured.Unstructured{}
	gone.SetGroupVersionKind(svc)
	if err := cl.Get(ctx, childNN, gone); !apierrors.IsNotFound(err) {
		t.Errorf("expected child deleted, got err=%v", err)
	}
}
