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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func lcmScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	s.AddKnownTypeWithName(DpuOperatorConfigGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		DpuOperatorConfigGVK.GroupVersion().WithKind("DpuOperatorConfigList"),
		&unstructured.UnstructuredList{},
	)
	metav1.AddToGroupVersion(s, DpuOperatorConfigGVK.GroupVersion())
	return s
}

func newConfig(name string, spec map[string]any) *unstructured.Unstructured {
	c := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	c.SetGroupVersionKind(DpuOperatorConfigGVK)
	c.SetName(name)
	return c
}

func TestLifecycleReconcile(t *testing.T) {
	sch := lcmScheme()
	cases := []struct {
		name  string
		obj   *unstructured.Unstructured
		reqNN types.NamespacedName
	}{
		{"with vendor", newConfig("cfg-dpf", map[string]any{"vendor": "dpf"}), types.NamespacedName{Name: "cfg-dpf"}},
		{"no vendor", newConfig("cfg-empty", map[string]any{"logLevel": int64(2)}), types.NamespacedName{Name: "cfg-empty"}},
		{"missing object", newConfig("placeholder", map[string]any{}), types.NamespacedName{Name: "does-not-exist"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(sch).WithObjects(tc.obj).Build()
			r := &LifecycleManager{Client: cl, Scheme: sch}
			if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: tc.reqNN}); err != nil {
				t.Fatalf("Reconcile(%s) error: %v", tc.name, err)
			}
		})
	}
}
